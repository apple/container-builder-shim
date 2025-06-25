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
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/apple/container-builder-shim/pkg/api"
	prefetcher "github.com/apple/container-builder-shim/pkg/prefetch"
	"github.com/apple/container-builder-shim/pkg/stream"
	contentx "github.com/containerd/containerd/v2/core/content"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var (
	_ io.ReadCloser = &readerAt{}
	_ io.Seeker     = &readerAt{}
)

/*
ReaderAt proxies content.ReaderAt over grpc stream to the caller

Request Format:

	ImageTransfer {
	    ID: $uuid,
	    Direction: OUTOF,
	    Metadata: {
	        "os": "linux",
			"stage":  "content-store",
	        "method": "/containerd.services.content.v1.Content/ReaderAt",
	        "offset": "$offset",
	        "length": "$length",
	    }
	    Descriptor: $desc,
	}

Response Format:

	ImageTransfer {
	    ID: $uuid,
	    Direction: INTO,
	    Metadata: {
	        "os": "linux",
			"stage":  "content-store",
	        "method": "/containerd.services.content.v1.Content/ReaderAt",
	        "offset": "$offset",
	        "length": "$length",
	        "size": "$size",
	    },
	    Descriptor: $desc,
	    Data: []byte{...} // number of bytes actually read can be obtained by len(Data), if 0, then EOF
	}
*/
func (r *ContentStoreProxy) ReaderAt(ctx context.Context, desc ocispec.Descriptor) (contentx.ReaderAt, error) {
	readerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	ra := &readerAt{
		id:         uuid.NewString(),
		descriptor: desc,
		proxy:      r,
		ctx:        readerCtx,
		cancel:     cancel,
	}
	if err := ra.init(); err != nil {
		return nil, err
	}

	return prefetcher.New(ra, ra.size, prefetcher.Config{
		WindowSize:       8 << 20,
		ChunkSize:        1 << 20,
		MaxParallelReads: 4,
		ReadTimeout:      30 * time.Second,
	})
}

type readerAt struct {
	id         string
	descriptor ocispec.Descriptor

	index int64
	size  int64

	proxy  *ContentStoreProxy
	ctx    context.Context
	cancel func()
}

func (r *readerAt) init() error {
	req := r.packetReaderAt(0, 0)

	demux := stream.NewDemuxWithContext(r.ctx, r.id, stream.FilterByBuildID(r.id), func(any) {})
	r.proxy.RegisterDemux(r.id, demux)

	resp, err := r.proxy.request(r.ctx, req)
	if err != nil {
		return err
	}
	if errMsg, ok := resp.Metadata["error"]; ok {
		return fmt.Errorf("%s", errMsg)
	}

	sz, err := strconv.ParseInt(resp.Metadata["size"], 10, 64)
	if err != nil {
		return err
	}
	r.size = sz
	return nil
}

func (r *readerAt) ReadAt(p []byte, off int64) (n int, err error) {
	length := len(p)

	req := r.packetReaderAt(off, int64(length))
	resp, err := r.proxy.request(r.ctx, req)
	if err != nil {
		return 0, err
	}

	if errMsg, ok := resp.Metadata["error"]; ok {
		return 0, fmt.Errorf("%s", errMsg)
	}

	ln := copy(p, resp.Data)
	if ln == 0 {
		return 0, io.EOF
	}
	if ln < length {
		return ln, nil
	}
	return ln, nil
}

func (r *readerAt) Close() (err error) {
	r.cancel()
	return nil
}

func (r *readerAt) Size() int64 {
	return r.size
}

func (r *readerAt) Read(p []byte) (int, error) {
	n, err := r.ReadAt(p, r.index)
	if err != nil {
		if err != io.EOF {
			return n, err
		}
		return 0, io.EOF
	}
	r.index += int64(n)
	return n, err
}

func (r *readerAt) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = r.index + offset
	case io.SeekEnd:
		newPos = r.size + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}

	if newPos < 0 {
		return 0, fmt.Errorf("negative seek offset: %d", newPos)
	}
	if newPos > r.size {
		// If seeking beyond EOF is undesirable, return an error.
		// Alternatively, allow it and return newPos without error.
		return 0, fmt.Errorf("seek beyond end of file: %d", newPos)
	}

	r.index = newPos
	return r.index, nil
}

func (r *readerAt) packetReaderAt(offset, length int64) *api.ImageTransfer {
	var pl *api.Platform
	if r.descriptor.Platform != nil {
		dpl := r.descriptor.Platform
		pl = &api.Platform{
			Os:           dpl.OS,
			Architecture: dpl.Architecture,
			Variant:      dpl.Variant,
			OsVersion:    dpl.OSVersion,
			OsFeatures:   dpl.OSFeatures,
		}
	}
	return &api.ImageTransfer{
		Id:        r.id,
		Direction: api.TransferDirection_OUTOF,
		Metadata: map[string]string{
			"os":     "linux",
			"stage":  "content-store",
			"method": "/containerd.services.content.v1.Content/ReaderAt",
			"offset": fmt.Sprintf("%d", offset),
			"length": fmt.Sprintf("%d", length),
		},
		Descriptor_: &api.Descriptor{
			MediaType:   r.descriptor.MediaType,
			Digest:      r.descriptor.Digest.String(),
			Size:        r.descriptor.Size,
			Urls:        r.descriptor.URLs,
			Annotations: r.descriptor.Annotations,
			Platform:    pl,
		},
	}
}
