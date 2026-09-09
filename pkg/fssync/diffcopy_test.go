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
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/tonistiigi/fsutil"
	"github.com/tonistiigi/fsutil/types"
	"google.golang.org/grpc/metadata"
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

// makeDockerignoreReproTar builds the build-context tree from
// https://github.com/apple/container/issues/1800:
//
//	foo/.gitkeep
//	foo/bar/.gitkeep
func makeDockerignoreReproTar() (checksum string, full []byte) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for _, dir := range []string{"foo", "foo/bar"} {
		_ = tw.WriteHeader(&tar.Header{
			Name:     dir,
			Typeflag: tar.TypeDir,
			Mode:     0o755,
			ModTime:  time.Time{},
		})
	}
	for _, file := range []string{"foo/.gitkeep", "foo/bar/.gitkeep"} {
		_ = tw.WriteHeader(&tar.Header{
			Name:     file,
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     0,
			ModTime:  time.Time{},
		})
	}
	_ = tw.Close()

	full = buf.Bytes()
	sum := sha256.Sum256(full)
	return hex.EncodeToString(sum[:]), full
}

// Regression test for https://github.com/apple/container/issues/1800.
//
// A .dockerignore that excludes a directory's contents but re-includes some
// of its descendants with negation patterns (the default Rails template does
// this) must still emit the excluded ancestor directories before the
// re-included files. BuildKit's receiver validates parent ordering and
// rejects the stream with "changes out of order" otherwise.
func TestDiffCopyEmitsExcludedParentDirsOfReincludedFiles(t *testing.T) {
	prevTarFactory := testTarFactory
	testTarFactory = makeDockerignoreReproTar
	defer func() { testTarFactory = prevTarFactory }()

	// The patterns from the issue's .dockerignore as BuildKit sends them
	// over the DiffCopy request metadata (the dockerignore parser strips
	// leading slashes):
	//
	//	/foo/*
	//	!/foo/.gitkeep
	//	/foo/bar/*
	//	!/foo/bar/.gitkeep
	ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{
		"exclude-patterns": []string{"foo/*", "!foo/.gitkeep", "foo/bar/*", "!foo/bar/.gitkeep"},
	})

	fs, err := filteredFS(ctx, NewFS(ctx, &FSSyncProxy{}, "/", t.TempDir()))
	if err != nil {
		t.Fatalf("filteredFS returned err=%v", err)
	}

	ms := newMockStream()
	s := &sender{
		conn:         &syncStream{Stream: ms},
		fs:           fs,
		files:        make(map[uint32]string),
		sendpipeline: make(chan *sendHandle, 128),
	}
	if err := s.walk(ctx); err != nil {
		t.Fatalf("walk returned err=%v", err)
	}

	var paths []string
	validator := &fsutil.Validator{}
	for _, p := range ms.sent {
		if p.Type != types.PACKET_STAT || p.Stat == nil {
			continue
		}
		paths = append(paths, p.Stat.Path)
		if err := validator.HandleChange(fsutil.ChangeKindAdd, p.Stat.Path, &fsutil.StatInfo{Stat: p.Stat}, nil); err != nil {
			t.Fatalf("BuildKit's receiver would reject the stream: %v (paths so far: %v)", err, paths)
		}
	}

	want := []string{"foo", "foo/.gitkeep", "foo/bar", "foo/bar/.gitkeep"}
	if !slices.Equal(paths, want) {
		t.Fatalf("sent paths = %v, want %v", paths, want)
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
