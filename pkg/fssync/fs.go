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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"github.com/tonistiigi/fsutil"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/fileutils"
	"github.com/apple/container-builder-shim/pkg/stream"
)

type TransferMode string

const (
	ModeJSON TransferMode = "json"
	ModeTAR  TransferMode = "tar"
)

var _ fsutil.FS = &FS{}

type FS struct {
	proxy  *FSSyncProxy
	root   string
	fsPath string
	ctx    context.Context

	// internally used fields - do not manipulate directly
	_checksumMutex *sync.Mutex
	_checksum      string
}

func NewFS(ctx context.Context, proxy *FSSyncProxy, root string, fsPath string) *FS {
	return &FS{
		ctx:    ctx,
		proxy:  proxy,
		root:   root,
		fsPath: fsPath,

		_checksumMutex: &sync.Mutex{},
	}
}

func (f *FS) Open(path string) (io.ReadCloser, error) {
	id := uuid.NewString()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	filePath := ""
	checksum := f.getChecksum()
	if checksum != "" {
		filePath = filepath.Join(f.fsPath, checksum, path)
	}

	var rs io.ReadSeekCloser
	var err error
	if filePath != "" {
		return os.Open(filePath)
	}

	packet := &api.BuildTransfer{
		Id:        id,
		Direction: api.TransferDirection_OUTOF,
		Source:    &path,
		Metadata: map[string]string{
			"os":     "linux",
			"stage":  "fssync",
			"method": "Info",
		},
	}
	resp, err := f.proxy.Request(ctx, &api.ServerStream{
		PacketType: &api.ServerStream_BuildTransfer{BuildTransfer: packet},
		BuildId:    id,
	}, id, stream.FilterByBuildID)
	if err != nil {
		return nil, fmt.Errorf("failed requesting file info for %s: %w", path, err)
	}

	transformer := &fileutils.FileInfoTransformer{}
	if errMsg, ok := resp.GetBuildTransfer().Metadata["error"]; ok {
		return nil, fmt.Errorf("server error getting file info for %s: %s", path, errMsg)
	}
	info, err := transformer.TransformIntoFileInfo(resp.GetBuildTransfer())
	if err != nil {
		return nil, err
	}

	return &File{
		ctx:      f.ctx,
		id:       id,
		info:     info,
		proxy:    f.proxy,
		filePath: filePath,
		rs:       rs,
	}, nil
}

func (f *FS) getChecksum() string {
	return f._checksum
}
