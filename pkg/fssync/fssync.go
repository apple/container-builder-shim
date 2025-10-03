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

package fssync

import (
	"context"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"

	"google.golang.org/grpc"
)

var (
	_ stream.Stage            = &FSSyncProxy{}
	_ session.Attachable      = &FSSyncProxy{}
	_ filesync.FileSyncServer = &FSSyncProxy{}
)

// A fsSync that proxies requests over the bidirectional grpc stream
// It is used by buildkit to retrieve context directory and other build artifacts
type FSSyncProxy struct {
	stream.UnimplementedBaseStage
	filesync.UnimplementedFileSyncServer

	contextDir string
	basePath   string

	addedGlobs []string
}

func NewFSSyncProxy(contextDir string, basePath string, addedGlobs []string) (*FSSyncProxy, error) {
	f := new(FSSyncProxy)
	f.contextDir = contextDir
	f.basePath = filepath.Join(basePath, f.String())
	f.addedGlobs = addedGlobs
	return f, nil
}

func (f *FSSyncProxy) Filter(c *api.ClientStream) error {
	if bt := c.GetBuildTransfer(); bt != nil {
		if bt.Metadata["stage"] == f.String() {
			return nil
		}
	}
	return stream.ErrIgnorePacket
}

func (f *FSSyncProxy) request(ctx context.Context, packet *api.BuildTransfer) (*api.BuildTransfer, error) {
	cancellableCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	id := uuid.NewString()
	packet.Id = id
	resp, err := f.Request(cancellableCtx, &api.ServerStream{
		BuildId: id,
		PacketType: &api.ServerStream_BuildTransfer{
			BuildTransfer: packet,
		},
	}, id, stream.FilterByBuildID)
	if err != nil {
		return nil, err
	}

	return resp.GetBuildTransfer(), nil
}

func (f *FSSyncProxy) String() string {
	return "fssync"
}

func (f *FSSyncProxy) Register(server *grpc.Server) {
	filesync.RegisterFileSyncServer(server, f)
}

func (f *FSSyncProxy) TarStream(filesync.FileSync_TarStreamServer) error {
	return ErrFailedProtocolNegotiation
}
