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

package fssync

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/moby/patternmatcher"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/fileutils"
	"github.com/apple/container-builder-shim/pkg/stream"
	"google.golang.org/grpc/metadata"
)

/*
Walk requests build-context files from the macOS host and presents them to BuildKit.

The host is asked for a tar archive containing the paths identified by
followpaths (glob patterns BuildKit sends in the request metadata). The shim
unpacks the tar to a content-addressed local cache and then walks the unpacked
tree, filtering each entry through the exclude-patterns (from .dockerignore)
before passing it to fn.

Only TAR mode is supported. The JSON mode wire format is defined in
RawFileInfo below but is not exercised by the current shim.

If BuildKit does not supply followpaths, the shim falls back to addedGlobs —
source paths pre-computed from the Dockerfile AST (see pkg/build/buildopts.go).

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

	walkMeta, err := unmarshalWalkMetadata(cancellableCtx, f.proxy.mode)
	if err != nil {
		return err
	}

	excludeMatcher, err := patternmatcher.New(strings.Split(walkMeta.ExcludedPatterns, ","))
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
		checksum, err := receiver.Receive(ctx, f.proxy.dockerfile, f.proxy.dockerignore,
			func(path string, d fs.DirEntry, err error) error {
				excluded, err := excludeMatcher.MatchesOrParentMatches(path)
				if excluded {
					return nil
				}

				return fn(path, d, err)
			})
		if err != nil {
			return err
		}
		f._checksumMutex.Lock()
		defer f._checksumMutex.Unlock()
		f._checksum = checksum
		return nil
	case ModeJSON:
		return receiveJSON(demux, excludeMatcher, f.proxy.dockerfile, f.proxy.dockerignore, fn)
	default:
		return fmt.Errorf("unsupported walk mode: %q", walkMeta.Mode)
	}
}

// receiveJSON handles ModeJSON walk responses. The proxy sends a single
// BuildTransfer whose Data field is a JSON array of RawFileInfo. File contents
// are not transferred here; they are fetched on-demand via FS.Open.
func receiveJSON(demux *stream.Demultiplexer, excludeMatcher *patternmatcher.PatternMatcher, dockerfile, dockerignore []byte, fn fs.WalkDirFunc) error {
	resp, err := demux.Recv()
	if err != nil {
		return fmt.Errorf("json walk: failed receiving response: %w", err)
	}
	bt := resp.GetBuildTransfer()
	if bt == nil {
		return fmt.Errorf("json walk: expected BuildTransfer, got nil")
	}
	if errMsg, ok := bt.Metadata["error"]; ok {
		return fmt.Errorf("json walk: server error: %s", errMsg)
	}

	var files []RawFileInfo
	if err := json.Unmarshal(bt.Data, &files); err != nil {
		return fmt.Errorf("json walk: failed to unmarshal file list: %w", err)
	}

	// Staged Dockerfile/dockerignore live under DockerfileStaging (".com.apple.container").
	// That prefix starts with '.' which sorts before any regular path component,
	// so these entries must be emitted BEFORE the regular file list.
	// Skip the staging dir entirely when it is covered by the exclude patterns
	// (e.g. a docker-specific .dockerignore appends ".com.apple.container"); this
	// mirrors the TAR-mode path where filepath.Walk hits the same exclude filter.
	if len(dockerignore) > 0 {
		stagingDir := DockerfileStaging
		stagingExcluded, err := excludeMatcher.MatchesOrParentMatches(stagingDir)
		if err != nil {
			return err
		}
		if !stagingExcluded {
			dirEntry := &fileutils.FileInfo{
				NameVal:  stagingDir,
				ModeVal:  fs.ModeDir | 0755,
				IsDirVal: true,
			}
			if err := fn(stagingDir, fs.FileInfoToDirEntry(dirEntry), nil); err != nil {
				return err
			}
			for _, staged := range []struct {
				name string
				data []byte
			}{
				{"Dockerfile", dockerfile},
				{"Dockerfile.dockerignore", dockerignore},
			} {
				path := stagingDir + "/" + staged.name
				fi := &fileutils.FileInfo{
					NameVal: path,
					SizeVal: int64(len(staged.data)),
					ModeVal: 0644,
				}
				if err := fn(path, fs.FileInfoToDirEntry(fi), nil); err != nil {
					return err
				}
			}
		}
	}

	for _, f := range files {
		excluded, err := excludeMatcher.MatchesOrParentMatches(f.Name)
		if err != nil {
			return err
		}
		if excluded {
			continue
		}
		modTime, err := time.Parse(time.RFC3339, f.ModTime)
		if err != nil {
			modTime = time.Time{}
		}
		modeVal := fs.FileMode(f.Mode)
		if f.IsDir {
			modeVal |= fs.ModeDir
		} else if f.Target != "" {
			modeVal |= fs.ModeSymlink
		}
		fi := &fileutils.FileInfo{
			NameVal:    f.Name,
			SizeVal:    int64(f.Size),
			ModeVal:    modeVal,
			ModTimeVal: modTime,
			IsDirVal:   f.IsDir,
			Uid:        f.UID,
			Gid:        f.GID,
			LinkName:   f.Target,
		}
		if err := fn(f.Name, fs.FileInfoToDirEntry(fi), nil); err != nil {
			return err
		}
	}
	return nil
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
	IncludePatterns  string
	ExcludedPatterns string
	FollowPaths      string
	DirName          string
	Mode             TransferMode
}

func unmarshalWalkMetadata(ctx context.Context, mode TransferMode) (*WalkMetadata, error) {
	md := &WalkMetadata{Mode: mode}
	if m, ok := metadata.FromIncomingContext(ctx); ok {
		md.IncludePatterns = strings.Join(m["include-patterns"], ",")
		md.ExcludedPatterns = strings.Join(m["exclude-patterns"], ",")
		md.FollowPaths = strings.Join(m["followpaths"], ",")
		md.DirName = strings.Join(m["dir-name"], ",")
	}
	return md, nil
}
