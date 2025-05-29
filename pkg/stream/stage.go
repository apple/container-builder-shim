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

package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/apple-uat/container-builder-shim/pkg/api"
	"github.com/sirupsen/logrus"

	"golang.org/x/sync/singleflight"
)

// Stage is the common interface implemented by ContentStore, FSSync, Stdio,
// and other build stages.
type Stage interface {
	Filter
	fmt.Stringer
	Send(*api.ServerStream) error

	getDemuxTable() *sync.Map
	setDemuxTable(*sync.Map)

	getSendCh() chan *api.ServerStream
	setSendCh(chan *api.ServerStream)

	getRecvCh() chan *api.ClientStream
	setRecvCh(chan *api.ClientStream)

	process(*api.ClientStream)
	run(context.Context) error
}

type UnimplementedBaseStage struct {
	sendCh chan *api.ServerStream
	recvCh chan *api.ClientStream

	demux *sync.Map
	group singleflight.Group
}

func (b *UnimplementedBaseStage) getDemuxTable() *sync.Map {
	return b.demux
}

func (b *UnimplementedBaseStage) setDemuxTable(m *sync.Map) {
	b.demux = m
}

func (b *UnimplementedBaseStage) getSendCh() chan *api.ServerStream {
	return b.sendCh
}

func (b *UnimplementedBaseStage) setSendCh(c chan *api.ServerStream) {
	b.sendCh = c
}

func (b *UnimplementedBaseStage) getRecvCh() chan *api.ClientStream {
	return b.recvCh
}

func (b *UnimplementedBaseStage) setRecvCh(c chan *api.ClientStream) {
	b.recvCh = c
}

// process forwards the packet to the stage-specific goroutine.
func (b *UnimplementedBaseStage) process(c *api.ClientStream) {
	b.recvCh <- c
}

func (b *UnimplementedBaseStage) run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case c := <-b.recvCh:
			v, ok := b.demux.Load(c.BuildId)
			if !ok {
				logrus.WithFields(logrus.Fields{
					"build_id":       c.BuildId,
					"ImageTransfer?": c.GetImageTransfer() != nil,
					"BuildTransfer?": c.GetBuildTransfer() != nil,
					"STDIO_CMD?":     c.GetCommand() != nil,
				}).WithError(ErrNoHandlerFound).Debug("dropping packet with no matching handler")

				if bf := c.GetBuildTransfer(); bf != nil {
					blob, _ := json.MarshalIndent(bf.GetMetadata(), "", " ")
					fmt.Println(string(blob))
				}
				if im := c.GetImageTransfer(); im != nil {
					blob, _ := json.MarshalIndent(im.GetMetadata(), "", " ")
					fmt.Println(string(blob))
				}
				continue
			}
			handler := v.(*Demultiplexer)

			if handler.Closed() {
				logrus.WithFields(logrus.Fields{
					"build_id":       c.BuildId,
					"ImageTransfer?": c.GetImageTransfer() != nil,
					"BuildTransfer?": c.GetBuildTransfer() != nil,
					"STDIO_CMD?":     c.GetCommand() != nil,
				}).WithError(handler.Err()).Debug("handler already closed")
				continue
			}
			if err := handler.Accept(c); err != nil && err != ErrIgnorePacket {
				logrus.WithError(err).Warn("handler refused packet")
			}
		}
	}
}

// Send pushes a ServerStream message back to the shared channel.
func (b *UnimplementedBaseStage) Send(s *api.ServerStream) error {
	select {
	case b.sendCh <- s:
		return nil
	case <-time.After(5 * time.Second):
		return ErrSendStreamBlocked
	}
}

func (b *UnimplementedBaseStage) Request(ctx context.Context, s *api.ServerStream, id string, filter FilterByIDFn) (*api.ClientStream, error) {

	v, err, _ := b.group.Do(id, func() (interface{}, error) {
		if dm, ok := b.demux.Load(id); ok {
			return dm.(*Demultiplexer).Recv()
		}

		dm := NewDemuxWithContext(ctx, id, filter(id), b.demux.Delete)
		b.RegisterDemux(id, dm)

		if err := b.Send(s); err != nil {
			b.demux.Delete(id)
			b.group.Forget(id)
			return nil, err
		}

		return dm.Recv()
	})
	if err != nil {
		return nil, err
	}
	return v.(*api.ClientStream), nil
}

func (b *UnimplementedBaseStage) RecvFilter(ctx context.Context, id string, filter FilterByIDFn) (*api.ClientStream, error) {

	v, err, _ := b.group.Do(id, func() (interface{}, error) {
		if dm, ok := b.demux.Load(id); ok {
			return dm.(*Demultiplexer).Recv()
		}
		dm := NewDemuxWithContext(ctx, id, filter(id), b.demux.Delete)
		b.RegisterDemux(id, dm)
		return dm.Recv()
	})
	if err != nil {
		return nil, err
	}
	return v.(*api.ClientStream), nil
}

// RegisterDemux stores dm under id (overwriting any previous value).
func (b *UnimplementedBaseStage) RegisterDemux(id string, dm *Demultiplexer) {
	b.demux.Store(id, dm)
}
