//===----------------------------------------------------------------------===//
// Copyright © 2025-2026 Apple Inc. and the container-builder-shim project authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//===----------------------------------------------------------------------===//

package fileutils

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/apple/container-builder-shim/pkg/stream"
)

// Receiver streams a remote tar archive, caches it under cacheBase and calls fn
// for every entry that is a regular file, directory or symlink.
type Receiver struct {
	demux     *stream.Demultiplexer
	cacheBase string
}

func NewTarReceiver(cacheBase string, demux *stream.Demultiplexer) *Receiver {
	return &Receiver{demux: demux, cacheBase: cacheBase}
}

func (r *Receiver) Receive(ctx context.Context, fn fs.WalkDirFunc) (string, error) {
	errCh := make(chan error, 1)
	hashCh := make(chan string, 1)
	dataCh := make(chan []byte)
	go startTar(r.demux, errCh, hashCh, dataCh)

	hash, err := readTarHash(ctx, errCh, hashCh)
	if err != nil {
		return "", err
	}

	checksum := string(hash)
	cacheDir := filepath.Join(r.cacheBase, checksum)
	tarFile := cacheDir + ".tar"

	cached, err := checkCache(cacheDir, r.cacheBase)
	if err != nil {
		return "", err
	}

	header, err := readTarHeader(ctx, errCh, dataCh)
	if err != nil {
		return "", err
	}

	if !cached {
		if err := writeTar(tarFile, header); err != nil {
			return "", err
		}
	}

	full, err := readTarBody(ctx, errCh, dataCh, tarFile, cached)
	if err != nil {
		if !cached {
			_ = os.Remove(tarFile)
		}
		return "", err
	}

	if !cached && full {
		if err := unpackTar(ctx, tarFile, cacheDir); err != nil {
			return "", err
		}
		_ = os.Remove(tarFile)
	}

	return checksum, filepath.Walk(cacheDir, func(p string, info os.FileInfo, _ error) error {
		rel, err := filepath.Rel(cacheDir, p)
		if err != nil || rel == "." {
			return err
		}
		if info.Mode().IsRegular() || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			linkName := ""
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(p)
				if err != nil {
					return err
				}
				linkName = target
			}
			return fn(rel, fs.FileInfoToDirEntry(&FileInfo{
				NameVal:    rel,
				SizeVal:    info.Size(),
				ModeVal:    info.Mode(),
				ModTimeVal: info.ModTime(),
				IsDirVal:   info.IsDir(),
				LinkName:   linkName,
			}), nil)
		}
		return nil
	})
}

func startTar(demux *stream.Demultiplexer, errCh chan<- error, hashCh chan<- string, dataCh chan<- []byte) {
	defer close(errCh)
	defer close(dataCh)

	for {
		resp, err := demux.Recv()
		if err != nil {
			if err == io.EOF || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "transport is closing") {
				errCh <- nil
				return
			}
			errCh <- err
			return
		}
		if bt := resp.GetBuildTransfer(); bt != nil {
			if errMsg, ok := bt.Metadata["error"]; ok {
				errCh <- fmt.Errorf("server error in TAR mode: %s", errMsg)
				return
			}

			if hash, ok := bt.Metadata["hash"]; ok {
				hashCh <- hash
				continue
			}

			dataCh <- bt.Data
			if bt.Complete {
				errCh <- nil
				return
			}
			continue
		}
		if it := resp.GetImageTransfer(); it != nil {
			if errMsg, ok := it.Metadata["error"]; ok {
				errCh <- fmt.Errorf("server error in TAR mode: %s", errMsg)
				return
			}
			dataCh <- it.Data
			if it.Complete {
				errCh <- nil
				return
			}
			continue
		}
		errCh <- fmt.Errorf("tar stream: unexpected packet type")
	}
}

func readTarHash(ctx context.Context, errCh <-chan error, hashCh <-chan string) (string, error) {
	select {
	case h, ok := <-hashCh:
		if !ok {
			if e := <-errCh; e != nil {
				return "", e
			}
			return "", nil
		}
		return h, nil
	case e := <-errCh:
		return "", e
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func readTarHeader(ctx context.Context, errCh <-chan error, dataCh <-chan []byte) ([]byte, error) {
	select {
	case d, ok := <-dataCh:
		if !ok {
			if e := <-errCh; e != nil {
				return nil, e
			}
			return nil, nil
		}
		return d, nil
	case e := <-errCh:
		return nil, e
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func readTarBody(ctx context.Context, errCh <-chan error, dataCh <-chan []byte, tarPath string, cached bool) (bool, error) {
	var f *os.File
	var err error
	if !cached {
		f, err = os.OpenFile(tarPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return false, err
		}
		defer f.Close()
	}

	for {
		select {
		case d, ok := <-dataCh:
			if !ok {
				return true, nil
			}
			if !cached {
				if _, wErr := f.Write(d); wErr != nil {
					return false, wErr
				}
			}
		case e := <-errCh:
			if e != nil {
				return false, e
			}
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

func checkCache(cachePath, basePath string) (bool, error) {
	if fi, err := os.Stat(cachePath); err == nil && fi.IsDir() {
		return true, nil
	}
	return false, os.MkdirAll(basePath, 0o755)
}

func writeTar(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

func unpackTar(ctx context.Context, tarFile, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	r, err := os.Open(tarFile)
	if err != nil {
		return err
	}
	defer r.Close()

	tr := tar.NewReader(r)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, hdr.Name)
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("invalid tar path: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			_ = f.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.RemoveAll(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			linkTarget := filepath.Join(dest, hdr.Linkname)
			_ = os.RemoveAll(target)
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		}
	}
	return nil
}
