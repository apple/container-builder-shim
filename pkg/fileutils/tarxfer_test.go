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
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
)

func makeTar() ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name:     "file1",
		Mode:     0o644,
		Size:     int64(len("contents")),
		ModTime:  time.Now(),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write([]byte("contents")); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func newDemux(ctx context.Context) *stream.Demultiplexer {
	return stream.NewDemuxWithContext(ctx, "test‑demux", func(*api.ClientStream) error { return nil }, func(any) {})
}

func btPacket(data []byte, complete bool, meta map[string]string) *api.ClientStream {
	bt := &api.BuildTransfer{Data: data, Complete: complete, Metadata: meta}
	return &api.ClientStream{PacketType: &api.ClientStream_BuildTransfer{BuildTransfer: bt}}
}

func TestReceiver_Receive_Success(t *testing.T) {
	archive, err := makeTar()
	if err != nil {
		t.Fatalf("makeTar: %v", err)
	}
	if len(archive) < 512 {
		t.Fatalf("tar archive too small: %d", len(archive))
	}
	header := archive[:512]
	body := archive[512:]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demux := newDemux(ctx)

	_ = demux.Accept(btPacket(header, false, nil))
	_ = demux.Accept(btPacket(body, true, nil))

	tmpDir := t.TempDir()
	r := NewTarReceiver(tmpDir, demux)

	var visited []string
	walkFn := func(p string, _ fs.DirEntry, _ error) error {
		visited = append(visited, p)
		return nil
	}

	checksum, err := r.Receive(ctx, walkFn)
	if err != nil {
		t.Fatalf("Receive returned error: %v", err)
	}

	wantChecksum := func(b []byte) string { h := sha256.Sum256(b); return fmt.Sprintf("%x", h) }(header)
	if checksum != wantChecksum {
		t.Fatalf("checksum mismatch: want %s, got %s", wantChecksum, checksum)
	}

	if len(visited) != 1 || visited[0] != "file1" {
		t.Fatalf("unexpected visited paths: %v", visited)
	}

	cacheDir := filepath.Join(tmpDir, checksum)
	if fi, err := os.Stat(filepath.Join(cacheDir, "file1")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("extracted file missing or not regular: %v", err)
	}
}

func TestReceiver_Receive_ServerError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demux := newDemux(ctx)

	metaErr := map[string]string{"error": "<err>"}
	_ = demux.Accept(btPacket(nil, true, metaErr))

	tmpDir := t.TempDir()
	r := NewTarReceiver(tmpDir, demux)

	_, err := r.Receive(ctx, func(string, fs.DirEntry, error) error { return nil })
	if err == nil {
		t.Fatalf("expected server error, got nil")
	}
	if !strings.Contains(err.Error(), "<err>") {
		t.Fatalf("unexpected error: %v", err)
	}
}
