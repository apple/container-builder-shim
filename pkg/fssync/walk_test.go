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
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	gofs "io/fs"
	"testing"
	"time"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
)

func (p *FSSyncProxy) RegisterDemux(id string, d *stream.Demultiplexer) {
	demuxes[id] = d
}

var (
	demuxes = map[string]*stream.Demultiplexer{}
	headers = map[string][]byte{}
)

func makeNestedTarHeaderAndBody() (checksum string, full []byte) {
	const payload = "hello from tar with nesting\n"

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	dirHeader := &tar.Header{
		Name:     "dir",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
		ModTime:  time.Time{},
	}
	_ = tw.WriteHeader(dirHeader)

	fileHeader := &tar.Header{
		Name:     "dir/foo",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len(payload)),
		ModTime:  time.Time{},
	}
	_ = tw.WriteHeader(fileHeader)
	_, _ = tw.Write([]byte(payload))

	_ = tw.Close()

	full = buf.Bytes()
	header := sha256.Sum256(full)
	return hex.EncodeToString(header[:]), full
}

func (p *FSSyncProxy) Send(s *api.ServerStream) error {
	id := s.BuildId
	d := demuxes[id]

	bt := s.GetBuildTransfer()
	if bt != nil && bt.Metadata["mode"] == string(ModeJSON) {
		files := []RawFileInfo{
			{Name: "dir", Mode: 0o755, IsDir: true, ModTime: time.Now().UTC().Format(time.RFC3339)},
			{Name: "dir/file.txt", Size: 42, Mode: 0o644, IsDir: false, ModTime: time.Now().UTC().Format(time.RFC3339)},
		}
		data, _ := json.Marshal(files)
		go func() {
			_ = d.Accept(&api.ClientStream{
				BuildId: id,
				PacketType: &api.ClientStream_BuildTransfer{
					BuildTransfer: &api.BuildTransfer{
						Id:       id,
						Complete: true,
						Data:     data,
					},
				},
			})
		}()
		return nil
	}

	checksum, full := makeNestedTarHeaderAndBody()
	go func() {
		_ = d.Accept(&api.ClientStream{
			BuildId: id,
			PacketType: &api.ClientStream_BuildTransfer{
				BuildTransfer: &api.BuildTransfer{
					Id:        id,
					Direction: api.TransferDirection_INTO,
					Metadata:  map[string]string{"hash": checksum},
				},
			},
		})

		_ = d.Accept(&api.ClientStream{
			BuildId: id,
			PacketType: &api.ClientStream_BuildTransfer{
				BuildTransfer: &api.BuildTransfer{
					Id:        id,
					Direction: api.TransferDirection_INTO,
					Data:      full,
				},
			},
		})

		_ = d.Accept(&api.ClientStream{
			BuildId: id,
			PacketType: &api.ClientStream_BuildTransfer{
				BuildTransfer: &api.BuildTransfer{
					Id:        id,
					Direction: api.TransferDirection_INTO,
					Complete:  true,
				},
			},
		})
	}()
	return nil
}

func TestUnmarshalWalkMetadata_Defaults(t *testing.T) {
	md, err := unmarshalWalkMetadata(context.Background(), ModeTAR)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if md.Mode != ModeTAR {
		t.Fatalf("default mode = %q, want %q", md.Mode, ModeTAR)
	}
}

func TestWalk_UnsupportedMode(t *testing.T) {
	// A zero-value FSSyncProxy has mode="" which is not a recognised TransferMode.
	fs := NewFS(context.Background(), &FSSyncProxy{}, "/", t.TempDir())
	var fn gofs.WalkDirFunc = func(string, gofs.DirEntry, error) error { return nil }
	err := fs.Walk(context.Background(), "", fn)
	if err == nil {
		t.Fatal("Walk returned nil error, want unsupported-mode error")
	}
}

func TestWalk_TarModeSuccess(t *testing.T) {
	tmp := t.TempDir()

	_, full := makeNestedTarHeaderAndBody()

	fs := NewFS(context.Background(), &FSSyncProxy{mode: ModeTAR}, "/", tmp)

	var walked []string
	err := fs.Walk(context.Background(), "", func(path string, _ gofs.DirEntry, _ error) error {
		walked = append(walked, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk returned err=%v", err)
	}
	fsSum := sha256.Sum256(full)
	fsChecksum := hex.EncodeToString(fsSum[:])
	if fs.getChecksum() != fsChecksum {
		t.Errorf("checksum = %q, want %q", fs.getChecksum(), fsChecksum)
	}
	if len(walked) == 0 {
		t.Errorf("walk callback not invoked")
	}
}

func TestWalk_JSONModeSuccess(t *testing.T) {
	fs := NewFS(context.Background(), &FSSyncProxy{mode: ModeJSON}, "/", t.TempDir())

	type result struct {
		path  string
		isDir bool
		mode  gofs.FileMode
	}
	var walked []result
	err := fs.Walk(context.Background(), "", func(path string, d gofs.DirEntry, _ error) error {
		info, _ := d.Info()
		walked = append(walked, result{path: path, isDir: d.IsDir(), mode: info.Mode()})
		return nil
	})
	if err != nil {
		t.Fatalf("Walk returned err=%v", err)
	}
	if len(walked) != 2 {
		t.Fatalf("got %d entries, want 2", len(walked))
	}
	if walked[0].path != "dir" || !walked[0].isDir || walked[0].mode&gofs.ModeDir == 0 {
		t.Errorf("entry[0]: got path=%q isDir=%v mode=%v, want dir entry with ModeDir set",
			walked[0].path, walked[0].isDir, walked[0].mode)
	}
	if walked[1].path != "dir/file.txt" || walked[1].isDir {
		t.Errorf("entry[1]: got path=%q isDir=%v, want dir/file.txt regular file",
			walked[1].path, walked[1].isDir)
	}
}
