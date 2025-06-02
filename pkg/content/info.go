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
	"strconv"
	"strings"
	"time"

	"github.com/apple/container-builder-shim/pkg/api"
	contentx "github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
)

/*
Info proxies content.Info over grpc stream to the caller

Request Format:

	ImageTransfer {
	    Tag: digest,
	    Direction: OUTOF,
	    Metadata: {
	        "os": "linux",
			"stage":  "content-store",
	        "method": "/containerd.services.content.v1.Content/Info"
	    }
	}

Expected Response Format:

	ImageTransfer {
	    Tag: digest,
	    Direction: INTO,
	    Metadata: {
	        "os": "linux",
			"stage":  "content-store",
	        "method": "/containerd.services.content.v1.Content/Info",
	        "size": "$size",
	        "created_at": "$created_timestamp",
	        "updated_at": "$updated_timestamp",
	        "__label:keyA": "valueA",
	        "__label:keyB": "valueB",
	        ...
	    },
		"complete": "true"
	}
*/
func (r *ContentStoreProxy) Info(ctx context.Context, dgst digest.Digest) (contentx.Info, error) {
	req := &api.ImageTransfer{
		Tag:       dgst.String(),
		Direction: api.TransferDirection_OUTOF,
		Metadata: map[string]string{
			"os":     "linux",
			"stage":  "content-store",
			"method": "/containerd.services.content.v1.Content/Info",
		},
	}
	resp, err := r.request(ctx, req)
	if err != nil {
		return contentx.Info{}, err
	}
	if err, ok := resp.Metadata["error"]; ok {
		return contentx.Info{}, fmt.Errorf("%s", err)
	}

	info, err := (&infoTransformer{}).TransformIntoContentInfo(resp)
	if err != nil {
		return contentx.Info{}, err
	}
	return *info, nil
}

type infoTransformer struct{}

func (i *infoTransformer) TransformIntoContentInfo(it *api.ImageTransfer) (*contentx.Info, error) {
	size, err := i.TransformSize(it.Metadata)
	if err != nil {
		return nil, err
	}
	createdAt, err := i.TransformCreatedTimestamp(it.Metadata)
	if err != nil {
		return nil, err
	}
	updatedAt, err := i.TransformUpdatedTimestamp(it.Metadata)
	if err != nil {
		return nil, err
	}

	return &contentx.Info{
		Digest:    digest.Digest(it.Tag),
		Size:      size,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Labels:    i.TransformLabels(it.Metadata),
	}, nil
}

func (i *infoTransformer) TransformFromContentInfo(info *contentx.Info) *api.ImageTransfer {
	metadata := map[string]string{
		"os":         "linux",
		"size":       fmt.Sprintf("%d", info.Size),
		"created_at": info.CreatedAt.Format(time.RFC3339),
		"updated_at": info.UpdatedAt.Format(time.RFC3339),
	}
	for k, v := range info.Labels {
		metadata["__label:"+k] = v
	}
	return &api.ImageTransfer{
		Tag:      info.Digest.String(),
		Metadata: metadata,
	}
}

func (i *infoTransformer) TransformSize(metadata map[string]string) (int64, error) {
	sizeString := metadata["size"]
	return strconv.ParseInt(sizeString, 10, 64)
}

func (i *infoTransformer) TransformCreatedTimestamp(metadata map[string]string) (time.Time, error) {
	createdAt := metadata["created_at"]
	if createdAt == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, createdAt)
}

func (i *infoTransformer) TransformUpdatedTimestamp(metadata map[string]string) (time.Time, error) {
	updatedAt := metadata["updated_at"]
	if updatedAt == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, updatedAt)
}

func (i *infoTransformer) TransformLabels(metadata map[string]string) map[string]string {
	labels := map[string]string{}
	labelPrefix := "__label:"
	for k, v := range metadata {
		if strings.HasPrefix(k, labelPrefix) {
			labels[strings.TrimPrefix(k, labelPrefix)] = v
		}
	}
	return labels
}
