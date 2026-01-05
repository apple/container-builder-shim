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

package resolver

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/platforms"
	"github.com/google/uuid"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/opencontainers/go-digest"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
)

var (
	_ stream.Stage          = &ResolverProxy{}
	_ llb.ImageMetaResolver = &ResolverProxy{}
)

// A resolver that proxies requests over the bidirectional grpc stream
// It is used by buildkit to resolve image names into digest SHAs
type ResolverProxy struct {
	stream.UnimplementedBaseStage
}

func NewResolverProxy() *ResolverProxy {
	return new(ResolverProxy)
}

func (r *ResolverProxy) Filter(c *api.ClientStream) error {
	if it := c.GetImageTransfer(); it != nil {
		if it.Metadata["stage"] == r.String() {
			return nil
		}
	}
	return stream.ErrIgnorePacket
}

func (r *ResolverProxy) request(ctx context.Context, packet *api.ImageTransfer) (*api.ImageTransfer, error) {
	cancellableCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// logrus.Debugf("sending resolver packet: %v", packet)
	id := uuid.NewString()
	packet.Id = id
	resp, err := r.Request(cancellableCtx, &api.ServerStream{
		BuildId: id,
		PacketType: &api.ServerStream_ImageTransfer{
			ImageTransfer: packet,
		},
	}, id, stream.FilterByBuildID)
	if err != nil {
		return nil, err
	}

	// resp, err := r.RecvFilter(cancellableCtx, id, stream.FilterByImageTransferID)
	// if err != nil {
	// 	return nil, err
	// }
	imageTransfer := resp.GetImageTransfer()
	return imageTransfer, nil
}

func (r *ResolverProxy) String() string {
	return "resolver"
}

/*
Request Format:

	ImageTransfer {
	    ID: $uuid,
	    Direction: OUTOF,
	    Metadata: {
	        "os": "linux",
			"stage":  "resolver",
	        "method": "/resolve",
			"ref": $image_name,
			"platform": $platform,
	    }
	}

Response Format:

	ImageTransfer {
	    ID: $uuid,
	    Direction: INTO,
	    Tag: $digest
	    Metadata: {
	        "os": "linux",
			"stage":  "resolver",
	        "method": "/resolve",
			"ref": $image_name,
			"platform": $platform,
	    },
		data: []byte{}, # ocispecs.Image encoded as json
		"complete": "true"
	}
*/
func (r *ResolverProxy) ResolveImageConfig(ctx context.Context, ref string, opt sourceresolver.Opt) (string, digest.Digest, []byte, error) {
	var err error

	req := &api.ImageTransfer{
		Direction: api.TransferDirection_INTO,
		Metadata: map[string]string{
			"os":       "linux",
			"stage":    "resolver",
			"method":   "/resolve",
			"ref":      ref,
			"platform": platforms.Format(*opt.Platform),
		},
	}
	resp, err := r.request(ctx, req)
	if err != nil {
		return ref, digest.Digest(""), nil, err
	}

	if err, ok := resp.Metadata["error"]; ok {
		return ref, digest.Digest(""), nil, fmt.Errorf("%s", err)
	}

	return ref, digest.Digest(resp.Tag), resp.Data, nil
}
