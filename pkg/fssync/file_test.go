//===----------------------------------------------------------------------===//
// Copyright © 2025 Apple Inc. and the container-builder-shim project authors. All rights reserved.
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
	"bytes"
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/apple-uat/container-builder-shim/pkg/api"
	"github.com/apple-uat/container-builder-shim/pkg/fileutils"
	"github.com/apple-uat/container-builder-shim/pkg/stream"
)

type ctxKey struct{}

var FileKey ctxKey

// shadow this stage's Request
func (p *FSSyncProxy) Request(ctx context.Context, req *api.ServerStream, _ string, _ ...stream.FilterByIDFn) (*api.ServerStream, error) {
	bt := req.GetBuildTransfer()
	off := bt.GetMetadata()["offset"]
	length := bt.GetMetadata()["length"]
	offInt, _ := strconv.ParseInt(off, 10, 0)
	lenInt, _ := strconv.ParseInt(length, 10, 0)
	if v := ctx.Value(FileKey); v != nil {
		data := v.([]byte)
		start := offInt
		end := int(math.Min(float64(offInt+lenInt), float64(len(data))))
		return &api.ServerStream{
			PacketType: &api.ServerStream_BuildTransfer{
				BuildTransfer: &api.BuildTransfer{
					Data:     data[start:end],
					Metadata: map[string]string{},
				},
			},
		}, nil
	}
	return &api.ServerStream{
		PacketType: &api.ServerStream_BuildTransfer{
			BuildTransfer: &api.BuildTransfer{
				Data:     []byte{},
				Metadata: map[string]string{},
			},
		},
	}, nil
}

func newFile(ctx context.Context, size int64, proxy *FSSyncProxy) *File {
	return &File{
		ctx:   ctx,
		id:    "test-id",
		proxy: proxy,
		info: &fileutils.FileInfo{
			NameVal: "dummy",
			SizeVal: size,
		},
	}
}

func TestReadRemoteSequential(t *testing.T) {
	data := []byte("hello world!")
	ctx := context.WithValue(context.Background(), FileKey, data)
	f := newFile(ctx, int64(len(data)), &FSSyncProxy{})

	buf := make([]byte, 5)
	n, err := f.Read(buf)
	if err != nil || n != 5 || string(buf) != "hello" {
		t.Fatalf("first read got (%q,%v), want %q,nil", buf[:n], err, "hello")
	}

	n, err = f.Read(buf)
	if err != nil || n != 5 || string(buf) != " worl" {
		t.Fatalf("second read got (%q,%v)", buf[:n], err)
	}

	rest, err := io.ReadAll(f)
	if err != nil || string(rest) != "d!" {
		t.Fatalf("final read got %q (%v), want %q", rest, err, "d!")
	}
}

func TestReadAtRemote(t *testing.T) {
	data := []byte("0123456789")
	ctx := context.WithValue(context.Background(), FileKey, data)
	f := newFile(ctx, int64(len(data)), &FSSyncProxy{})

	buf := make([]byte, 4)
	n, err := f.ReadAt(buf, 3)
	if err != nil || n != 4 || string(buf) != "3456" {
		t.Fatalf("ReadAt got (%q,%v), want %q,nil", buf[:n], err, "3456")
	}
}

func TestSeekAndRead(t *testing.T) {
	data := []byte("abcdef")
	ctx := context.WithValue(context.Background(), FileKey, data)
	f := newFile(ctx, int64(len(data)), &FSSyncProxy{})

	if off, _ := f.Seek(2, io.SeekStart); off != 2 {
		t.Fatalf("SeekStart returned %d, want 2", off)
	}
	buf := make([]byte, 3)
	n, _ := f.Read(buf)
	if string(buf[:n]) != "cde" {
		t.Fatalf("after seek got %q, want %q", buf[:n], "cde")
	}
}

func TestBufferedReadPath(t *testing.T) {
	f := newFile(context.Background(), 3, &FSSyncProxy{})
	f.buf = []byte("xyz")

	out := make([]byte, 4)
	n, err := f.Read(out)
	if err != nil || n != 3 || !bytes.Equal(out[:n], []byte("xyz")) {
		t.Fatalf("buffer path read got (%q,%v)", out[:n], err)
	}
}

func TestPacketReadAtMetadata(t *testing.T) {
	f := newFile(context.Background(), 0, &FSSyncProxy{})
	p := f.packetReadAt(7, 42)

	if p.Metadata["offset"] != "7" || p.Metadata["length"] != "42" ||
		p.Metadata["method"] != "Read" || *p.Source != "dummy" {
		t.Errorf("packet metadata incorrect: %+v", p.Metadata)
	}
}

func TestReadEOFSequential(t *testing.T) {
	data := []byte("abc")
	ctx := context.WithValue(context.Background(), FileKey, data)
	f := newFile(ctx, int64(len(data)), &FSSyncProxy{})

	blk := make([]byte, 3)
	if n, err := f.Read(blk); err != nil || n != 3 {
		t.Fatalf("priming read failed: n=%d err=%v", n, err)
	}

	n, err := f.Read(blk)
	if err != io.EOF || n != 0 {
		t.Fatalf("Read after EOF got (n=%d, err=%v), want 0, io.EOF", n, err)
	}
}

func TestReadAtEOFBeyondEnd(t *testing.T) {
	data := []byte("abc")
	ctx := context.WithValue(context.Background(), FileKey, data)
	f := newFile(ctx, int64(len(data)), &FSSyncProxy{})

	buf := make([]byte, 4)
	n, err := f.ReadAt(buf, int64(len(data)))
	if err != io.EOF || n != 0 {
		t.Fatalf("ReadAt past end got (n=%d, err=%v), want 0, io.EOF", n, err)
	}
}

func TestOpen_UsesCachedFilePath(t *testing.T) {
	tmp := t.TempDir()

	const checksum = "deadbeef"
	cacheDir := filepath.Join(tmp, checksum)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir(%s): %v", cacheDir, err)
	}
	const (
		rel  = "foo/bar"
		data = "cached-content"
	)
	full := filepath.Join(cacheDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	fs := NewFS(nil, &FSSyncProxy{}, "/", tmp)
	fs._checksum = checksum

	rc, err := fs.Open(rel)
	if err != nil {
		t.Fatalf("Open(%q) returned err=%v, want nil", rel, err)
	}
	defer rc.Close()

	got, _ := io.ReadAll(rc)
	if string(got) != data {
		t.Fatalf("cached read returned %q, want %q", got, data)
	}
}
