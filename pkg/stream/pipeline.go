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
	"io"
	"sync"

	"github.com/apple-uat/container-builder-shim/pkg/api"

	"github.com/sirupsen/logrus"
)

// Stream abstracts a duplex gRPC stream.
type Stream interface {
	Recv() (*api.ClientStream, error)
	Send(*api.ServerStream) error
	Context() context.Context
}

// StreamPipeline multiplexes a single gRPC stream into independent stages.
type StreamPipeline struct {
	ctx    context.Context
	cancel context.CancelFunc

	raw Stream

	sendCh chan *api.ServerStream
	wg     sync.WaitGroup

	stages []Stage
}

// NewPipeline wires the stages and starts their goroutines.
func NewPipeline(parent context.Context, raw Stream, stages ...Stage) (*StreamPipeline, error) {
	ctx, cancel := context.WithCancel(parent)

	p := &StreamPipeline{
		ctx:    ctx,
		cancel: cancel,
		raw:    raw,
		sendCh: make(chan *api.ServerStream, 64),
	}

	// Attach common resources to every stage.
	for i, stg := range stages {
		stg.setSendCh(p.sendCh)
		stg.setRecvCh(make(chan *api.ClientStream, 4))
		p.stages = append(p.stages, stg)

		stg.setDemuxTable(&sync.Map{})
		p.wg.Add(1)
		go func(s Stage) {
			defer p.wg.Done()
			if err := s.run(ctx); err != nil && ctx.Err() == nil {
				logrus.WithError(err).Error("stage terminated")
				cancel()
			}
		}(stg)

		stages[i] = stg
	}

	return p, nil
}

// Run blocks until the pipeline or its parent context ends.
func (p *StreamPipeline) Run() error {
	// Sender goroutine
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case s := <-p.sendCh:
				if err := p.raw.Send(s); err != nil {
					logrus.WithError(err).Error("Send error")
					p.cancel()
					return
				}
			case <-p.ctx.Done():
				return
			}
		}
	}()

	// Receiver goroutine
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			pkt, err := p.raw.Recv()
			if err != nil {
				if err != io.EOF {
					logrus.WithError(err).Error("Recv error")
				}
				p.cancel()
				return
			}

			handled := false
			for _, stage := range p.stages {
				err := stage.Filter(pkt)
				switch {
				case err == nil:
					stage.process(pkt)
					handled = true
					break
				case err == ErrIgnorePacket:
					continue
				default: // real error
					logrus.WithError(err).Warn("Filter error")
					continue
				}
			}
			if !handled {
				logrus.WithField("build_id", pkt.BuildId).Debug("dropped unhandled packet")
			}
		}
	}()

	<-p.ctx.Done()
	p.wg.Wait()
	close(p.sendCh)
	return p.ctx.Err()
}
