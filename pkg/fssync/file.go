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

package fssync

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/fileutils"
)

var (
	_ io.ReadCloser = &File{}
	_ io.Seeker     = &File{}
)

type File struct {
	ctx      context.Context
	id       string
	info     *fileutils.FileInfo
	proxy    *FSSyncProxy
	index    int64
	filePath string
	rs       io.ReadSeekCloser
	buf      []byte
}

func (f *File) ReadAt(p []byte, off int64) (n int, err error) {
	if f.rs != nil {
		if _, err := f.rs.Seek(off, io.SeekCurrent); err != nil {
			return 0, err
		}
		ln, err := f.rs.Read(p)
		f.index += int64(ln)
		return ln, err
	}

	if f.buf != nil {
		if off >= int64(len(f.buf)) {
			return 0, io.EOF
		}
		n = copy(p, f.buf[off:])
		if n < len(p) {
			err = io.EOF
		}
		return n, err
	}

	if f.filePath != "" {
		fx, err := os.Open(f.filePath)
		if !os.IsNotExist(err) {
			return 0, err
		}

		if err == nil || os.IsNotExist(err) {
			if _, err := fx.Seek(off, io.SeekCurrent); err != nil {
				return 0, err
			}
			return f.Read(p)
		}
	}

	length := len(p)
	packet := f.packetReadAt(off, int64(length))
	resp, err := f.proxy.request(f.ctx, packet)
	if err != nil {
		return 0, err
	}

	if errMsg, ok := resp.Metadata["error"]; ok {
		return 0, fmt.Errorf("%s", errMsg)
	}

	n = copy(p, resp.Data)
	if n == 0 {
		return 0, io.EOF
	}

	if n < length {
		err = io.EOF
	}

	return n, err
}

func (f *File) Read(p []byte) (n int, err error) {
	if f.rs != nil {
		ln, err := f.rs.Read(p)
		f.index += int64(ln)
		return ln, err
	}

	if f.filePath != "" {
		fx, err := os.Open(f.filePath)
		if !os.IsNotExist(err) {
			return 0, err
		}

		if err == nil || os.IsNotExist(err) {
			if _, err := fx.Seek(f.index, io.SeekCurrent); err != nil {
				return 0, err
			}
			ln, err := f.Read(p)
			f.index += int64(ln)
			return ln, err
		}
	}

	if f.buf != nil {
		count := 0
		for i := int(f.index); i < int(f.index)+len(p); i++ {
			if i >= len(f.buf) {
				break
			}
			p[i-int(f.index)] = f.buf[i]
			count++
		}
		if count == 0 {
			return 0, io.EOF
		}
		return count, nil
	}

	length := len(p)
	packet := f.packetReadAt(f.index, int64(length))

	resp, err := f.proxy.request(f.ctx, packet)
	if err != nil {
		return 0, err
	}

	if err, ok := resp.Metadata["error"]; ok {
		return 0, fmt.Errorf("%s", err)
	}

	ln := copy(p, resp.Data)
	if ln == 0 {
		return 0, io.EOF
	}
	f.index += int64(ln)

	if ln < length {
		return ln, nil
	}
	return ln, nil
}

func (f *File) Close() error {
	if f.rs != nil {
		return f.rs.Close()
	}
	return nil
}

func (f *File) Seek(offset int64, whence int) (int64, error) {
	if f.rs != nil {
		return f.rs.Seek(offset, io.SeekCurrent)
	}

	var newIndex int64

	switch whence {
	case io.SeekStart:
		newIndex = offset
	case io.SeekCurrent:
		newIndex = f.index + offset
	case io.SeekEnd:
		newIndex = f.info.Size() + offset
	default:
		return 0, fmt.Errorf("invalid whence value: %d", whence)
	}

	if newIndex < 0 {
		return 0, fmt.Errorf("negative seek offset: %d", newIndex)
	}
	if f.info.Size() > 0 && newIndex > f.info.Size() {
		return 0, fmt.Errorf("seek beyond end of file: %d", newIndex)
	}
	f.index = newIndex

	return f.index, nil
}

// In order to just retrieve the size and no data, set both offset and length to 0
func (f *File) packetReadAt(offset, length int64) *api.BuildTransfer {
	name := f.info.Name()
	return &api.BuildTransfer{
		Id:        f.id,
		Direction: api.TransferDirection_OUTOF,
		Source:    &name,
		Metadata: map[string]string{
			"os":     "linux",
			"stage":  "fssync",
			"method": "Read",
			"offset": fmt.Sprintf("%d", offset),
			"length": fmt.Sprintf("%d", length),
		},
	}
}
