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

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
	contentx "github.com/containerd/containerd/v2/core/content"
	"github.com/google/uuid"
)

/*
Walk proxies content.Walk over grpc stream to the caller

Request Format:

	ImageTransfer {
	    ID: $uuid,
	    Direction: OUTOF,
	    Metadata: {
	        "os": "linux",
			"stage":  "content-store",
	        "method": "/containerd.services.content.v1.Content/Walk",
			"filters": "filter1,filter2,filter3..."
	    }
	}

Response Format:

	ImageTransfer {
	    ID: $uuid,
	    Direction: INTO,
	    Tag: $digest
	    Metadata: {
	        "os": "linux",
			"stage":  "content-store",
	        "method": "/containerd.services.content.v1.Content/Walk",
	        "size": "$size",
	        "created_at": "$created_timestamp",
	        "updated_at": "$updated_timestamp",
	        "__label:keyA": "valueA",
	        "__label:keyB": "valueB",
	    },
		"complete": "false"
	}

	ImageTransfer {
	    ID: $uuid,
	    Direction: INTO,
	    Tag: $digest
	    Metadata: {
	        "os": "linux",
			"stage":  "content-store",
	        "method": "/containerd.services.content.v1.Content/Walk",
	        "size": "$size",
	        "created_at": "$created_timestamp",
	        "updated_at": "$updated_timestamp",
	        "__label:keyA": "valueA",
	        "__label:keyB": "valueB",
	    },
		"complete": "true"
	}
*/
func (r *ContentStoreProxy) Walk(ctx context.Context, fn contentx.WalkFunc, filters ...string) error {
	cancellableCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	id := uuid.NewString()
	packet := &api.ImageTransfer{
		Id: id,
		Metadata: map[string]string{
			"os":     "linux",
			"stage":  "content-store",
			"method": "/containerd.services.content.v1.Content/Walk",
		},
	}
	processor := stream.NewDemuxWithContext(cancellableCtx, id, stream.FilterByImageTransferID(id), func(any) {})
	r.RegisterDemux(id, processor)
	if err := r.Send(&api.ServerStream{
		BuildId: id,
		PacketType: &api.ServerStream_ImageTransfer{
			ImageTransfer: packet,
		},
	}); err != nil {
		return err
	}
	transformer := &infoTransformer{}
	infoCh := make(chan *contentx.Info, 32)
	errCh := make(chan error)

	go func() {
		for {
			respStream, err := processor.Recv()
			if err != nil {
				errCh <- err
				return
			}
			imageTransfer := respStream.GetImageTransfer()
			if err, ok := imageTransfer.Metadata["error"]; ok {
				errCh <- fmt.Errorf("%s", err)
				return
			}
			info, err := transformer.TransformIntoContentInfo(imageTransfer)
			if err != nil {
				errCh <- err
				return
			}
			infoCh <- info
			if imageTransfer.Complete {
				errCh <- nil
				return
			}
		}
	}()
	for {
		select {
		case info := <-infoCh:
			if err := fn(*info); err != nil {
				return err
			}
		case err := <-errCh:
			return err
		}
	}
}
