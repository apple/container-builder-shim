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

package content

import (
	"context"

	contentx "github.com/containerd/containerd/v2/core/content"

	"github.com/google/uuid"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
)

var (
	_ contentx.Store = &ContentStoreProxy{}
	_ stream.Stage   = &ContentStoreProxy{}
)

// A content store that proxies requests over the bidirectional grpc stream
// Since buildkit only needs to read content, and never writes to content store,
// this proxy only implements reader and status calls.
// All writer calls are left unimplemented.
type ContentStoreProxy struct {
	contentx.Store
	stream.UnimplementedBaseStage
}

func NewContentStoreProxy() (*ContentStoreProxy, error) {
	contentProxy := new(ContentStoreProxy)
	return contentProxy, nil
}

func (r *ContentStoreProxy) Filter(c *api.ClientStream) error {
	if it := c.GetImageTransfer(); it != nil {
		if it.Metadata["stage"] == r.String() {
			return nil
		}
	}
	return stream.ErrIgnorePacket
}

func (r *ContentStoreProxy) request(ctx context.Context, packet *api.ImageTransfer) (*api.ImageTransfer, error) {
	cancellableCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	id := uuid.NewString()
	if packet.Id == "" {
		packet.Id = id
	}
	resp, err := r.Request(cancellableCtx, &api.ServerStream{
		BuildId: id,
		PacketType: &api.ServerStream_ImageTransfer{
			ImageTransfer: packet,
		},
	}, id, stream.FilterByBuildID)
	if err != nil {
		return nil, err
	}

	imageTransfer := resp.GetImageTransfer()
	return imageTransfer, nil
}

func (r *ContentStoreProxy) String() string {
	return "content-store"
}

// Writer methods NOT required for building images
func (r *ContentStoreProxy) Writer(ctx context.Context, opts ...contentx.WriterOpt) (contentx.Writer, error) {
	panic("unimplemented")
}

func (r *ContentStoreProxy) Status(ctx context.Context, ref string) (contentx.Status, error) {
	panic("unimplemented")
}

func (r *ContentStoreProxy) ListStatuses(ctx context.Context, filters ...string) ([]contentx.Status, error) {
	panic("unimplemented")
}

func (r *ContentStoreProxy) Abort(ctx context.Context, ref string) error {
	panic("unimplemented")
}
