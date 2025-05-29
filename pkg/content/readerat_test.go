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

package content

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/apple-uat/container-builder-shim/pkg/api"
	"github.com/apple-uat/container-builder-shim/pkg/stream"
)

var PayloadKey ctxKey
var DemuxKey ctxKey

var demuxes = map[string]context.Context{}

func (p *ContentStoreProxy) RegisterDemux(id string, d *stream.Demultiplexer) {
	ctx := context.Background()
	otherCtx, ok := demuxes[id]
	if ok {
		ctx = otherCtx
	}
	demuxes[id] = context.WithValue(ctx, DemuxKey, d)
}

func (p *ContentStoreProxy) Send(s *api.ServerStream) error {
	id := s.BuildId
	dCtx := demuxes[id]
	fmt.Println("found demuxes", dCtx)
	v := dCtx.Value(PayloadKey).([]byte)
	d := dCtx.Value(DemuxKey).(*stream.Demultiplexer)
	metadata := map[string]string{
		"size": fmt.Sprintf("%d", len(v)),
	}
	go func() {
		_ = d.Accept(&api.ClientStream{
			BuildId: id,
			PacketType: &api.ClientStream_ImageTransfer{
				ImageTransfer: &api.ImageTransfer{
					Id:        id,
					Direction: api.TransferDirection_INTO,
					Data:      v,
					Metadata:  metadata,
				},
			},
		})

		_ = d.Accept(&api.ClientStream{
			BuildId: id,
			PacketType: &api.ClientStream_ImageTransfer{
				ImageTransfer: &api.ImageTransfer{
					Id:        id,
					Direction: api.TransferDirection_INTO,
					Complete:  true,
				},
			},
		})
	}()
	return nil
}

func newDescriptor(sz int64) ocispec.Descriptor {
	return ocispec.Descriptor{
		Digest:    digest.Digest("sha256:deadbeef"),
		MediaType: "application/octet-stream",
		Size:      sz,
	}
}

func TestReaderAt_ReadSequential(t *testing.T) {
	payload := []byte("hello, container reader!")
	ctx := context.WithValue(context.Background(), PayloadKey, payload)
	ctx = context.WithValue(ctx, ContentKey, &api.ImageTransfer{
		Data: payload,
		Metadata: map[string]string{
			"size": fmt.Sprintf("%d", len(payload)),
		},
	})

	rid := uuid.NewString()
	demuxes[rid] = ctx

	desc := newDescriptor(int64(len(payload)))
	cs := &ContentStoreProxy{}
	ra := &readerAt{
		ctx:        ctx,
		id:         rid,
		descriptor: desc,
		proxy:      cs,
		cancel:     func() {},
	}
	if err := ra.init(); err != nil {
		t.Fatalf("ra init err: %v", err)
	}
	defer ra.Close()

	buf := make([]byte, 5)
	n, err := ra.ReadAt(buf, 0)
	if err != nil || n != 5 || !bytes.Equal(buf, []byte("hello")) {
		t.Fatalf("first read got (%q,%v)", buf[:n], err)
	}
}

func TestReaderAt_ReadAtAndEOF(t *testing.T) {
	payload := []byte("0123456789")
	ctx := context.WithValue(context.Background(), PayloadKey, payload)

	rid := uuid.NewString()
	demuxes[rid] = ctx

	desc := newDescriptor(int64(len(payload)))
	cs := &ContentStoreProxy{}
	ra := &readerAt{
		ctx:        ctx,
		id:         rid,
		descriptor: desc,
		proxy:      cs,
		cancel:     func() {},
	}
	ra.init()
	defer ra.Close()

	// slice in the middle
	buf := make([]byte, 4)
	if n, err := ra.ReadAt(buf, 3); err != nil || n != 4 || string(buf) != "3456" {
		t.Fatalf("ReadAt mid-file got (%q,%v)", buf[:n], err)
	}

	// beyond EOF
	if n, err := ra.ReadAt(buf, int64(len(payload))); err != io.EOF || n != 0 {
		t.Fatalf("ReadAt at EOF got (n=%d, err=%v), want 0, io.EOF", n, err)
	}
}

func TestReaderAt_Seek(t *testing.T) {
	payload := []byte("abcdef")
	ctx := context.WithValue(context.Background(), PayloadKey, payload)
	ctx = context.WithValue(ctx, ContentKey, &api.ImageTransfer{
		Data: payload[2:],
		Metadata: map[string]string{
			"size": fmt.Sprintf("%d", len(payload)),
		},
	})

	rid := uuid.NewString()
	demuxes[rid] = ctx

	desc := newDescriptor(int64(len(payload)))
	cs := &ContentStoreProxy{}
	ra := &readerAt{
		ctx:        ctx,
		id:         rid,
		descriptor: desc,
		proxy:      cs,
		cancel:     func() {},
	}
	ra.init()
	defer ra.Close()

	buf := make([]byte, 3)
	ra.ReadAt(buf, 3)
	if string(buf) != "cde" {
		t.Fatalf("after seek read=%q, want \"cde\"", buf)
	}
}

func TestReaderAt_InitServerError(t *testing.T) {
	ctx := context.WithValue(context.Background(), ContentKey, &api.ImageTransfer{
		Metadata: map[string]string{"error": "boom"},
	})
	_, err := (&ContentStoreProxy{}).ReaderAt(ctx, newDescriptor(0))
	if err == nil {
		t.Fatal("ReaderAt returned nil error, want propagated server error")
	}
}

func TestReaderAt_Size(t *testing.T) {
	data := make([]byte, 100)
	ctx := context.WithValue(context.Background(), PayloadKey, data)
	ctx = context.WithValue(ctx, ContentKey, &api.ImageTransfer{
		Data: data,
		Metadata: map[string]string{
			"size": fmt.Sprintf("%d", len(data)),
		},
	})

	rid := uuid.NewString()
	demuxes[rid] = ctx

	desc := newDescriptor(int64(len(data)))
	cs := &ContentStoreProxy{}
	ra := &readerAt{
		ctx:        ctx,
		id:         rid,
		descriptor: desc,
		proxy:      cs,
		cancel:     func() {},
	}
	ra.init()
	defer ra.Close()

	if ra.Size() != int64(len(data)) {
		t.Fatalf("Size() = %d, want %d", ra.Size(), len(data))
	}
}
