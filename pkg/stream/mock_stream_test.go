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
	"sync"

	"github.com/apple/container-builder-shim/pkg/api"
)

// mockStream is an in-memory implementation of Stream
type mockStream struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex
	sent []*api.ServerStream

	recvCh  chan *api.ClientStream
	recvErr error
}

func newMockStream() *mockStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &mockStream{
		ctx:    ctx,
		cancel: cancel,
		recvCh: make(chan *api.ClientStream, 16),
	}
}

func (m *mockStream) Recv() (*api.ClientStream, error) {
	select {
	case <-m.ctx.Done():
		return nil, m.ctx.Err()
	case pkt, ok := <-m.recvCh:
		if !ok {
			return nil, m.recvErr
		}
		return pkt, nil
	}
}

func (m *mockStream) Send(s *api.ServerStream) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, s)
	return nil
}

func (m *mockStream) Context() context.Context {
	return m.ctx
}

func (m *mockStream) push(pkt *api.ClientStream) {
	m.recvCh <- pkt
}

func (m *mockStream) closeRecv(err error) {
	m.recvErr = err
	close(m.recvCh)
}
