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

package stdio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
)

var lastRequest struct {
	ctx     context.Context
	payload *api.ServerStream
	id      string
}

// shadow base stage's Request
func (p *StdioProxy) Request(ctx context.Context, s *api.ServerStream, id string, filters ...stream.FilterByIDFn) (*api.ServerStream, error) {
	lastRequest.ctx = ctx
	lastRequest.payload = s
	lastRequest.id = id
	return &api.ServerStream{}, nil
}

func encodeTerminalCmd(t *testing.T, cmd TerminalCommand) string {
	t.Helper()
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal cmd: %v", err)
	}
	return base64.RawStdEncoding.EncodeToString(raw)
}

func newClientStream(encoded string) *api.ClientStream {
	return &api.ClientStream{
		PacketType: &api.ClientStream_Command{
			Command: &api.Run{Command: encoded},
		},
	}
}

func TestRead_IsWriteOnly(t *testing.T) {
	proxy := &StdioProxy{ctx: context.Background(), buf: make([]byte, 1<<20)}
	if _, err := proxy.Read(make([]byte, 8)); err != ErrWriteOnlyStream {
		t.Fatalf("expected ErrWriteOnlyStream, got %v", err)
	}
}

func TestWrite_SendsStderrPacket(t *testing.T) {
	proxy := &StdioProxy{ctx: context.Background(), buf: make([]byte, 1<<20)}

	payload := []byte("hello from stderr")
	n, err := proxy.Write(payload)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("expected %d bytes written, got %d", len(payload), n)
	}

	got := lastRequest.payload.GetIo()
	if got == nil {
		t.Fatalf("no IO payload captured in request")
	}
	if got.Type != api.Stdio_STDERR {
		t.Fatalf("expected packet type STDERR, got %v", got.Type)
	}
	if string(got.Data) != string(payload) {
		t.Fatalf("expected data %q, got %q", payload, got.Data)
	}
}

func TestFilter_Ack(t *testing.T) {
	proxy := &StdioProxy{ctx: context.Background(), buf: make([]byte, 1<<20)}
	cmd := encodeTerminalCmd(t, TerminalCommand{CommandType: "terminal", Code: "ack"})
	if err := proxy.Filter(newClientStream(cmd)); err != nil {
		t.Fatalf("expected nil error on ack, got %v", err)
	}
}

func TestFilter_Winch_NotTTY(t *testing.T) {
	proxy := &StdioProxy{ctx: context.Background(), buf: make([]byte, 1<<20)} // console == nil
	cmd := encodeTerminalCmd(t, TerminalCommand{CommandType: "terminal", Code: "winch", Rows: 30, Cols: 100})
	if err := proxy.Filter(newClientStream(cmd)); err != stream.ErrNotATTY {
		t.Fatalf("expected ErrNotATTY, got %v", err)
	}
}

func TestFilter_InvalidCode(t *testing.T) {
	proxy := &StdioProxy{ctx: context.Background(), buf: make([]byte, 1<<20)}
	cmd := encodeTerminalCmd(t, TerminalCommand{CommandType: "terminal", Code: "foo"})
	if err := proxy.Filter(newClientStream(cmd)); err == nil {
		t.Fatalf("expected error for invalid terminal code, got nil")
	}
}

func TestFilter_NonTerminalPacket(t *testing.T) {
	proxy := &StdioProxy{ctx: context.Background(), buf: make([]byte, 1<<20)}
	cmd := encodeTerminalCmd(t, TerminalCommand{CommandType: "metrics", Code: "whatever"})
	if err := proxy.Filter(newClientStream(cmd)); err != stream.ErrIgnorePacket {
		t.Fatalf("expected ErrIgnorePacket, got %v", err)
	}
}
