//===----------------------------------------------------------------------===//
// Copyright © 2025 Apple Inc. and the container-builder-shim project authors.
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
	gofs "io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
	"google.golang.org/grpc/metadata"
)

func (p *FSSyncProxy) RegisterDemux(id string, d *stream.Demultiplexer) {
	demuxes[id] = d
}

var (
	demuxes = map[string]*stream.Demultiplexer{}
	headers = map[string][]byte{}
)

func makeNestedTarHeaderAndBody() (hdr, rest []byte) {
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

	full := buf.Bytes()
	return full[:512], full[512:]
}

func (p *FSSyncProxy) Send(s *api.ServerStream) error {
	id := s.BuildId
	d := demuxes[id]
	header, body := makeNestedTarHeaderAndBody()
	go func() {
		_ = d.Accept(&api.ClientStream{
			BuildId: id,
			PacketType: &api.ClientStream_BuildTransfer{
				BuildTransfer: &api.BuildTransfer{
					Id:        id,
					Direction: api.TransferDirection_INTO,
					Data:      append(header, body...),
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
	md, err := unmarshalWalkMetadata(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if md.Mode != ModeTAR {
		t.Fatalf("default mode = %q, want %q", md.Mode, ModeTAR)
	}
}

func TestUnmarshalWalkMetadata_InvalidMode(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("mode", "json"))
	_, err := unmarshalWalkMetadata(ctx)
	if err == nil {
		t.Fatal("expected error for unsupported mode 'json', got nil")
	}
}

func TestWalk_UnsupportedMode(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("mode", "json"))
	fs := NewFS(ctx, &FSSyncProxy{}, "/", t.TempDir()) // proxy never used
	var fn gofs.WalkDirFunc = func(string, gofs.DirEntry, error) error { return nil }
	err := fs.Walk(ctx, "", fn)
	if err == nil {
		t.Fatal("Walk returned nil error, want unsupported-mode error")
	}
}

func TestWalk_TarModeSuccess(t *testing.T) {
	tmp := t.TempDir()

	header, body := makeNestedTarHeaderAndBody()
	sum := sha256.Sum256(header)
	checksum := hex.EncodeToString(sum[:])
	cacheDir := filepath.Join(tmp, checksum)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cacheDir: %v", err)
	}

	fs := NewFS(context.Background(), &FSSyncProxy{}, "/", tmp)

	var walked []string
	err := fs.Walk(context.Background(), "", func(path string, _ gofs.DirEntry, _ error) error {
		walked = append(walked, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk returned err=%v", err)
	}
	fsSum := sha256.Sum256(append(header, body...))
	fsChecksum := hex.EncodeToString(fsSum[:])
	if fs.getChecksum() != fsChecksum {
		t.Errorf("checksum = %q, want %q", fs.getChecksum(), checksum)
	}
	if len(walked) == 0 {
		t.Errorf("walk callback not invoked")
	}
}
