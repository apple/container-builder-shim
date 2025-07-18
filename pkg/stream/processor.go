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

package stream

import (
	"context"

	"github.com/apple/container-builder-shim/pkg/api"
)

// Demultiplexer fan-outs packets belonging to the same stage
type Demultiplexer struct {
	ctx     context.Context
	id      string
	ch      chan *api.ClientStream
	filter  FilterFn
	closeFn func(any)
}

func NewDemuxWithContext(
	ctx context.Context,
	id string,
	filter FilterFn,
	closeFn func(any),
) *Demultiplexer {
	return &Demultiplexer{
		ctx:     ctx,
		id:      id,
		ch:      make(chan *api.ClientStream, 32),
		filter:  filter,
		closeFn: closeFn,
	}
}

func (d *Demultiplexer) Done() <-chan struct{} {
	return d.ctx.Done()
}

func (d *Demultiplexer) Err() error {
	return d.ctx.Err()
}

func (d *Demultiplexer) Closed() bool {
	return d.ctx.Err() != nil
}

// Accept validates & enqueues a packet.
func (d *Demultiplexer) Accept(c *api.ClientStream) error {
	if err := d.filter(c); err != nil {
		return err
	}

	select {
	case <-d.ctx.Done():
		d.closeFn(d.id)
		return d.ctx.Err()
	case d.ch <- c:
		return nil
	default:
		// Channel is full - clean it up to prevent future packets from being sent here
		d.closeFn(d.id)
		return ErrDemuxChannelFull
	}
}

// Recv blocks until a packet is received or the context ends.
func (d *Demultiplexer) Recv() (*api.ClientStream, error) {
	select {
	case <-d.ctx.Done():
		d.closeFn(d.id)
		return nil, d.ctx.Err()
	case c := <-d.ch:
		return c, nil
	}
}
