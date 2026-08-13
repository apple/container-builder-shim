//===----------------------------------------------------------------------===//
// Copyright © 2026 Apple Inc. and the container-builder-shim project authors.
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
	"strconv"
	"time"

	"github.com/apple/container-builder-shim/pkg/api"
	contentx "github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
)

var _ contentx.Writer = &writer{}

// The chunk size mirrors the read path's: large enough to amortize the
// packet round trip, small enough to keep gRPC buffers modest.
const writeChunkSize = 1 << 20

/*
Writer proxies content.Writer over the grpc stream to the caller, so a
blob BuildKit exports is written into the caller's content store as it
is finalized, one chunk at a time.

Request Format:

	ImageTransfer {
	    ID: $uuid,
	    Direction: OUTOF,
	    Metadata: {
	        "os": "linux",
	        "stage":  "content-store",
	        "method": "/containerd.services.content.v1.Content/Write",
	        "action": "write" | "commit",
	        "ref": "$ref",
	        "offset": "$offset",            // write
	        "expected": "$digest",          // commit
	        "total": "$size",               // commit
	    }
	    Data: []byte{...}                   // write
	}

Response Format:

	ImageTransfer {
	    ID: $uuid,
	    Direction: INTO,
	    Metadata: {
	        "os": "linux",
	        "stage":  "content-store",
	        "method": "/containerd.services.content.v1.Content/Write",
	        "offset": "$offset",            // bytes the caller holds for the ref
	        "exists": "true",               // commit of a blob the store already has
	        "error": "...",                 // any failure
	    },
	}
*/
func (r *ContentStoreProxy) Writer(ctx context.Context, opts ...contentx.WriterOpt) (contentx.Writer, error) {
	var wopts contentx.WriterOpts
	for _, o := range opts {
		if err := o(&wopts); err != nil {
			return nil, err
		}
	}

	// The store the writes land in is the caller's, so the caller's word on
	// what it already holds is what makes a write unnecessary: a blob whose
	// digest is known and present is skipped the way any content store
	// skips it, by answering the open with what already exists.
	if wopts.Desc.Digest != "" {
		if _, err := r.Info(ctx, wopts.Desc.Digest); err == nil {
			return nil, errdefs.ErrAlreadyExists
		}
	}

	writerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w := &writer{
		id:     uuid.NewString(),
		ref:    wopts.Ref,
		proxy:  r,
		ctx:    writerCtx,
		cancel: cancel,
	}

	return w, nil
}

type writer struct {
	id  string
	ref string

	offset    int64
	total     int64
	committed bool
	digester  digest.Digester

	proxy  *ContentStoreProxy
	ctx    context.Context
	cancel func()
}

func (w *writer) packet(action string, data []byte, metadata map[string]string) *api.ImageTransfer {
	md := map[string]string{
		"os":     "linux",
		"stage":  "content-store",
		"method": "/containerd.services.content.v1.Content/Write",
		"action": action,
		"ref":    w.ref,
	}
	for k, v := range metadata {
		md[k] = v
	}
	return &api.ImageTransfer{
		Id:        w.id,
		Direction: api.TransferDirection_OUTOF,
		Metadata:  md,
		Data:      data,
	}
}

func (w *writer) send(packet *api.ImageTransfer) (*api.ImageTransfer, error) {
	resp, err := w.proxy.request(w.ctx, packet)
	if err != nil {
		return nil, err
	}
	if errMsg, ok := resp.Metadata["error"]; ok {
		return nil, fmt.Errorf("%s", errMsg)
	}
	return resp, nil
}

func (w *writer) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		end := written + writeChunkSize
		if end > len(p) {
			end = len(p)
		}
		chunk := p[written:end]
		_, err := w.send(w.packet("write", chunk, map[string]string{
			"offset": strconv.FormatInt(w.offset, 10),
		}))
		if err != nil {
			return written, err
		}
		if w.digester == nil {
			w.digester = digest.SHA256.Digester()
		}
		if _, err := w.digester.Hash().Write(chunk); err != nil {
			return written, err
		}
		w.offset += int64(len(chunk))
		written = end
	}
	return written, nil
}

func (w *writer) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...contentx.Opt) error {
	if w.committed {
		return errdefs.ErrAlreadyExists
	}
	if size > 0 && size != w.offset {
		return fmt.Errorf("commit size %d does not match bytes written %d", size, w.offset)
	}
	if expected == "" && w.digester != nil {
		expected = w.digester.Digest()
	}
	resp, err := w.send(w.packet("commit", nil, map[string]string{
		"expected": expected.String(),
		"total":    strconv.FormatInt(w.offset, 10),
	}))
	if err != nil {
		return err
	}
	w.committed = true
	w.total = w.offset
	if resp.Metadata["exists"] == "true" {
		return errdefs.ErrAlreadyExists
	}
	return nil
}

func (w *writer) Status() (contentx.Status, error) {
	now := time.Now()
	return contentx.Status{
		Ref:       w.ref,
		Offset:    w.offset,
		Total:     w.total,
		StartedAt: now,
		UpdatedAt: now,
	}, nil
}

func (w *writer) Digest() digest.Digest {
	if w.digester == nil {
		return ""
	}
	return w.digester.Digest()
}

func (w *writer) Truncate(size int64) error {
	if size != 0 {
		return fmt.Errorf("truncate to %d not supported: only a restart from zero is", size)
	}
	w.offset = 0
	w.digester = nil
	return nil
}

func (w *writer) Close() error {
	w.cancel()
	return nil
}
