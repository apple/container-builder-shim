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

package content

import (
	"context"
	"fmt"
	"strings"

	contentx "github.com/containerd/containerd/v2/core/content"
)

/*
Update proxies content.Update over grpc stream to the caller

Request Format:

	ImageTransfer {
	    ID: $uuid,
	    Direction: OUTOF,
	    Tag: $digest
	    Metadata: {
	        "os": "linux",
			"stage":  "content-store",
	        "method": "/containerd.services.content.v1.Content/Update",
	        "size": "$size",
	        "created_at": "$created_timestamp",
	        "updated_at": "$updated_timestamp",
	        "__label:keyA": "valueA",
	        "__label:keyB": "valueB",
	        "fieldpaths": "size,createdAt,updatedAt",
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
	        "method": "/containerd.services.content.v1.Content/Update",
	        "size": "$size",
	        "created_at": "$created_timestamp",
	        "updated_at": "$updated_timestamp",
	        "__label:keyA": "valueA",
	        "__label:keyB": "valueB",
	    },
		"complete": "true"
	}
*/
func (r *ContentStoreProxy) Update(ctx context.Context, info contentx.Info, fieldpaths ...string) (contentx.Info, error) {
	transformer := &infoTransformer{}
	packet := transformer.TransformFromContentInfo(&info)
	packet.Metadata["os"] = "linux"
	packet.Metadata["stage"] = "content-store"
	packet.Metadata["method"] = "/containerd.services.content.v1.Content/Update"
	packet.Metadata["fieldpaths"] = strings.Join(fieldpaths, ",")

	resp, err := r.request(ctx, packet)
	if err != nil {
		return contentx.Info{}, err
	}
	if err, ok := resp.Metadata["error"]; ok {
		return contentx.Info{}, fmt.Errorf("%s", err)
	}

	updatedInfo, err := transformer.TransformIntoContentInfo(resp)
	if err != nil {
		return contentx.Info{}, err
	}
	return *updatedInfo, nil
}
