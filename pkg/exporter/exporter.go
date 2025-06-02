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

package exporter

import (
	"context"
	"io"

	"github.com/apple-uat/container-builder-shim/pkg/api"
	"github.com/apple-uat/container-builder-shim/pkg/stream"
	"github.com/google/uuid"
)

var _ io.WriteCloser = &ExporterProxy{}

// A exporter that exports a tar stream over the bidirectional grpc stream
// It is used by buildkit to return built images
type ExporterProxy struct {
	stream.UnimplementedBaseStage

	closeCh chan struct{}
	ctx     context.Context
}

func NewExporterProxy(ctx context.Context) *ExporterProxy {
	proxy := new(ExporterProxy)
	proxy.closeCh = make(chan struct{})
	proxy.ctx = ctx
	return proxy
}

func (e *ExporterProxy) Filter(c *api.ClientStream) error {
	if bt := c.GetBuildTransfer(); bt != nil {
		if bt.Metadata["stage"] == e.String() {
			return nil
		}
	}
	return stream.ErrIgnorePacket
}

func (e *ExporterProxy) String() string {
	return "exporter"
}

func (e *ExporterProxy) Close() error {
	e.Write([]byte{})
	close(e.closeCh)
	return nil
}

func (e *ExporterProxy) Done() chan struct{} {
	return e.closeCh
}

/*
Write is proxied over grpc stream to the caller

Request Format:

	BuildTransfer {
	    ID: $uuid,
	    Direction: OUTOF,
	    Metadata: {
	        "os": "linux",
			"stage":  "exporter",
	        "method": "Write",
	    },
		Data: []byte{...}
	}
*/
func (e *ExporterProxy) Write(p []byte) (written int, err error) {
	cancellableCtx, cancel := context.WithCancel(e.ctx)
	defer cancel()

	if len(p) == 0 { // mark as completed
		id := uuid.NewString()
		packet := &api.BuildTransfer{
			Id:        id,
			Direction: api.TransferDirection_OUTOF,
			Metadata: map[string]string{
				"os":     "linux",
				"stage":  "exporter",
				"method": "Write",
			},
			Data:     []byte{},
			Complete: true,
		}

		_, err = e.Request(cancellableCtx, &api.ServerStream{
			BuildId: id,
			PacketType: &api.ServerStream_BuildTransfer{
				BuildTransfer: packet,
			},
		}, id, stream.FilterByBuildID)
		if err != nil {
			return 0, err
		}
		return 0, nil
	}

	id := uuid.NewString()
	packet := &api.BuildTransfer{
		Id:        id,
		Direction: api.TransferDirection_OUTOF,
		Metadata: map[string]string{
			"os":     "linux",
			"stage":  "exporter",
			"method": "Write",
		},
		Data: p,
	}

	_, err = e.Request(cancellableCtx, &api.ServerStream{
		BuildId: id,
		PacketType: &api.ServerStream_BuildTransfer{
			BuildTransfer: packet,
		},
	}, id, stream.FilterByBuildID)
	if err != nil {
		return 0, err
	}

	return len(p), nil
}
