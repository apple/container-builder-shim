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
	"context"
	"fmt"
	"io/fs"
	"strings"

	"github.com/google/uuid"

	"github.com/apple-uat/container-builder-shim/pkg/api"
	"github.com/apple-uat/container-builder-shim/pkg/fileutils"
	"github.com/apple-uat/container-builder-shim/pkg/stream"
	"google.golang.org/grpc/metadata"
)

/*
Walk is proxied over the gRPC stream to the caller.

In JSON mode, the server sends file info as JSON; we walk those files directly.

Request Format:

	BuildTransfer {
	    ID: $uuid,
	    Direction: OUTOF,
	    Source: $path,
	    Metadata: {
	        "os":    "linux",
	        "stage": "fssync",
	        "method": "Walk",
	        "mode":  "json" | "tar"
	    }
	}

Depending on the specified mode, the server may respond with file info in JSON format,
or send a tar archive for remote file data.

Response Format ('json' mode):

	BuildTransfer {
	    ID: $uuid,
	    Direction: INTO,
	    Source: $path,
	    Metadata: {
	        "os":          "linux",
	        "stage":       "fssync",
	        "method":      "Walk",
	        "size":        "$size",
	        "mode":        $file_mode, // uint32 value
	        "modified_at": "$modified_timestamp",
	        "uid":         $uid,
	        "gid":         $gid,
	    },
	    "is_directory": $is_directory,
	    "complete":     "true"
	}

In TAR mode, the server sends a tar archive; we unpack it locally and then walk
the resulting directory paths.
*/
func (f *FS) Walk(ctx context.Context, target string, fn fs.WalkDirFunc) error {
	cancellableCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	walkMeta, err := unmarshalWalkMetadata(cancellableCtx)
	if err != nil {
		return err
	}

	id := uuid.NewString()
	demux := stream.NewDemuxWithContext(cancellableCtx, id, stream.FilterByBuildID(id), func(any) {})
	f.proxy.RegisterDemux(id, demux)

	followPaths := walkMeta.FollowPaths
	if followPaths == "" {
		followPaths = strings.Join(f.proxy.addedGlobs, ",")
	}

	packet := &api.BuildTransfer{
		Id:        id,
		Direction: api.TransferDirection_OUTOF,
		Source:    &f.root,
		Metadata: map[string]string{
			"os":               "linux",
			"stage":            "fssync",
			"method":           "Walk",
			"dir-name":         walkMeta.DirName,
			"include-patterns": walkMeta.IncludePatterns,
			"followpaths":      followPaths,
			"mode":             string(walkMeta.Mode),
		},
	}
	if err := f.proxy.Send(&api.ServerStream{
		BuildId: id,
		PacketType: &api.ServerStream_BuildTransfer{
			BuildTransfer: packet,
		},
	}); err != nil {
		return fmt.Errorf("failed sending walk request: %w", err)
	}

	switch walkMeta.Mode {
	case ModeTAR:
		receiver := fileutils.NewTarReceiver(f.fsPath, demux)
		checksum, err := receiver.Receive(ctx, fn)
		if err != nil {
			return err
		}
		f._checksumMutex.Lock()
		defer f._checksumMutex.Unlock()
		f._checksum = checksum
		return nil
	default:
		return fmt.Errorf("unsupported walk mode: %q", walkMeta.Mode)
	}
}

// RawFileInfo is the wire‑format for Walk (json mode).
type RawFileInfo struct {
	Name    string `json:"name"`
	Size    uint64 `json:"size"`
	Mode    uint32 `json:"mode"`
	IsDir   bool   `json:"isDir"`
	ModTime string `json:"modTime"`
	UID     uint32 `json:"uid"`
	GID     uint32 `json:"gid"`
	Target  string `json:"target"`
}

type WalkMetadata struct {
	IncludePatterns string
	FollowPaths     string
	DirName         string
	Mode            TransferMode
}

func unmarshalWalkMetadata(ctx context.Context) (*WalkMetadata, error) {
	md := &WalkMetadata{}
	if m, ok := metadata.FromIncomingContext(ctx); ok {
		md.IncludePatterns = strings.Join(m["include-patterns"], ",")
		md.FollowPaths = strings.Join(m["followpaths"], ",")
		md.DirName = strings.Join(m["dir-name"], ",")
		modeStr := strings.Join(m["mode"], ",")
		switch modeStr {
		case "", "tar":
			modeStr = string(ModeTAR)
		default:
			return nil, fmt.Errorf("invalid walk mode: %s", modeStr)
		}
		md.Mode = TransferMode(modeStr)
	} else {
		md.Mode = ModeTAR
	}
	return md, nil
}
