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
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/tonistiigi/fsutil/types"
)

type mockConn struct {
	recvQ chan *types.Packet
	sent  []*types.Packet
	mu    sync.Mutex
}

func newMockStream() *mockConn {
	return &mockConn{
		recvQ: make(chan *types.Packet, 1),
		sent:  []*types.Packet{},
	}
}

func (m *mockConn) RecvMsg(msg any) error {
	p, ok := <-m.recvQ
	if !ok {
		return errors.New("stream closed")
	}
	switch t := msg.(type) {
	case *types.Packet:
		*t = *p
	default:
		return errors.New("unexpected type to RecvMsg")
	}
	return nil
}

func (m *mockConn) SendMsg(msg any) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := msg.(*types.Packet)
	if !ok {
		return errors.New("SendMsg expects *types.Packet")
	}
	cp := *p
	m.sent = append(m.sent, &cp)
	return nil
}

func (m *mockConn) Context() context.Context {
	return context.Background()
}

func TestFileSenderWriteEmitsPacketData(t *testing.T) {
	ms := newMockStream()
	s := &sender{conn: ms}
	fs := &fileSender{sender: s, id: 42}

	payload := []byte("hello-world")
	n, err := fs.Write(payload)
	if err != nil {
		t.Fatalf("fileSender.Write returned err=%v, want nil", err)
	}
	if n != len(payload) {
		t.Fatalf("fileSender.Write wrote %d bytes, want %d", n, len(payload))
	}

	if len(ms.sent) != 1 {
		t.Fatalf("mockConn captured %d packets, want 1", len(ms.sent))
	}
	p := ms.sent[0]
	if p.Type != types.PACKET_DATA {
		t.Errorf("packet Type=%v, want PACKET_DATA", p.Type)
	}
	if p.ID != 42 {
		t.Errorf("packet ID=%d, want 42", p.ID)
	}
	if string(p.Data) != "hello-world" {
		t.Errorf("packet Data=%q, want %q", p.Data, payload)
	}
}

func TestSenderQueuePushesHandleAndDeletesEntry(t *testing.T) {
	ms := newMockStream()
	sendq := make(chan *sendHandle, 1)

	s := &sender{
		conn:         ms,
		fs:           nil,
		files:        map[uint32]string{7: "/foo/bar"},
		sendpipeline: sendq,
	}

	if err := s.queue(7); err != nil {
		t.Fatalf("queue returned err=%v, want nil", err)
	}

	select {
	case h := <-sendq:
		if h.id != 7 || h.path != "/foo/bar" {
			t.Errorf("queued handle = %+v, want id=7 path=/foo/bar", h)
		}
	default:
		t.Fatalf("no handle pushed onto sendpipeline")
	}

	if _, stillThere := s.files[7]; stillThere {
		t.Errorf("files map still contains id 7 after queue(); want deleted")
	}
}

func TestSenderQueueInvalidIDReturnsError(t *testing.T) {
	ms := newMockStream()
	s := &sender{
		conn:         ms,
		fs:           nil,
		files:        map[uint32]string{1: "exists"},
		sendpipeline: make(chan *sendHandle, 1),
	}

	if err := s.queue(99); err == nil {
		t.Fatalf("queue(99) returned nil error, want non-nil")
	}
}

func TestFileCanRequestData(t *testing.T) {
	tests := []struct {
		mode os.FileMode
		want bool
		desc string
	}{
		{0, true, "regular file"},
		{os.ModeDir, false, "directory"},
		{os.ModeSymlink, false, "symlink"},
		{os.ModeNamedPipe, false, "FIFO"},
	}

	for _, tc := range tests {
		if got := fileCanRequestData(tc.mode); got != tc.want {
			t.Errorf("fileCanRequestData(%s) = %v, want %v (%s)",
				tc.mode.String(), got, tc.want, tc.desc)
		}
	}
}
