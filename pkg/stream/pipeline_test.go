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

package stream

import (
	"context"
	"testing"
	"time"

	"github.com/apple/container-builder-shim/pkg/api"
)

type testStage struct {
	UnimplementedBaseStage
	name      string
	processed chan *api.ClientStream
}

func newTestStage(name string) *testStage {
	return &testStage{
		name:      name,
		processed: make(chan *api.ClientStream, 8),
	}
}

func (t *testStage) Filter(*api.ClientStream) error {
	return nil
}

func (t *testStage) String() string {
	return t.name
}

func (t *testStage) process(c *api.ClientStream) {
	t.processed <- c
}

func (t *testStage) run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case pkt := <-t.getRecvCh():
			t.process(pkt)
		}
	}
}

func TestStreamPipelineRoutesPackets(t *testing.T) {
	mock := newMockStream()
	stg := newTestStage("stage-A")

	pipe, err := NewPipeline(mock.Context(), mock, stg)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}

	go func() {
		if err := pipe.Run(); err != nil && err != context.Canceled {
			t.Errorf("pipeline ended with %v", err)
		}
	}()

	in := &api.ClientStream{BuildId: "foo"}
	mock.push(in)

	select {
	case got := <-stg.processed:
		if got != in {
			t.Fatalf("stage received wrong packet: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for packet in stage")
	}

	mock.closeRecv(context.Canceled)
}
