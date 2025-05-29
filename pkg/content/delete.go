//===----------------------------------------------------------------------===//
// Copyright © 2024-2025 Apple Inc. and the container-builder-shim project authors. All rights reserved.
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

	"github.com/apple-uat/container-builder-shim/pkg/api"
	"github.com/opencontainers/go-digest"
)

/*
Delete proxies content.Delete over grpc stream to the caller

Request Format:

	ImageTransfer {
	    ID: $uuid,
	    Direction: OUTOF,
	    Tag: $digest
	    Metadata: {
	        "os": "linux",
			"stage":  "content-store",
	        "method": "/containerd.services.content.v1.Content/Delete",
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
	        "method": "/containerd.services.content.v1.Content/Delete",
	    },
		"complete": "true"
	}
*/
func (r *ContentStoreProxy) Delete(ctx context.Context, dgst digest.Digest) error {
	req := &api.ImageTransfer{
		Tag: dgst.String(),
		Metadata: map[string]string{
			"os":     "linux",
			"stage":  "content-store",
			"method": "/containerd.services.content.v1.Content/Delete",
		},
	}
	resp, err := r.request(ctx, req)
	if err, ok := resp.Metadata["error"]; ok {
		return fmt.Errorf("%s", err)
	}
	return err
}
