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
	"fmt"

	"github.com/containerd/console"
	"github.com/google/uuid"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
)

var (
	_                  console.File = &StdioProxy{}
	ErrWriteOnlyStream              = fmt.Errorf("stdio stream is write-only")
)

// A io stream that proxies requests over the bidirectional grpc stream
// It is used by buildkit to report build status
type StdioProxy struct {
	stream.UnimplementedBaseStage

	ctx     context.Context
	console console.Console
	s       string
	buf     []byte
}

func NewStdioProxy(ctx context.Context, tty bool) (*StdioProxy, error) {
	c, s, err := console.NewPty()
	if err != nil {
		return nil, err
	}
	proxy := new(StdioProxy)
	if tty {
		proxy.console = c
		proxy.s = s
	}
	proxy.ctx = ctx
	proxy.buf = make([]byte, 1<<20)

	return proxy, nil
}

// TerminalCommand is an API over Command type
// Valid commands include:
//
//	Winch:
//	  {"command_type": "terminal, "code": "winch", "rows": 30, "cols": 100}
type TerminalCommand struct {
	CommandType string `json:"command_type,omitempty"` // terminal
	Code        string `json:"code,omitempty"`         // winch,ack
	Rows        int    `json:"rows,omitempty"`         // rows when winch
	Cols        int    `json:"cols,omitempty"`         // cols when winch
}

func (r *StdioProxy) Filter(c *api.ClientStream) error {
	if cmd := c.GetCommand(); cmd != nil {

		byteCmd, err := base64.RawStdEncoding.DecodeString(cmd.Command)
		if err != nil {
			return stream.ErrIgnorePacket
		}

		termCmd := &TerminalCommand{}
		if err := json.Unmarshal(byteCmd, termCmd); err != nil {
			return stream.ErrIgnorePacket
		}

		if termCmd.CommandType != "terminal" {
			return stream.ErrIgnorePacket
		}

		switch termCmd.Code {
		case "winch":
			if r.console == nil {
				return stream.ErrNotATTY
			}
			if termCmd.Rows > 0 && termCmd.Cols > 0 {
				r.console.Resize(console.WinSize{
					Height: uint16(termCmd.Rows),
					Width:  uint16(termCmd.Cols),
				})
			}
			return nil
		case "ack":
			return nil
		default:
			return fmt.Errorf("invalid terminal command: %s", termCmd.Code)
		}
	}

	return stream.ErrIgnorePacket
}

func (r *StdioProxy) Read(p []byte) (int, error) {
	return 0, ErrWriteOnlyStream
}

func (r *StdioProxy) Write(p []byte) (int, error) {
	copy(r.buf, p)

	id := uuid.NewString()
	_, err := r.Request(r.ctx, &api.ServerStream{
		BuildId: id,
		PacketType: &api.ServerStream_Io{
			Io: &api.IO{
				Type: api.Stdio_STDERR,
				Data: r.buf[0:len(p)],
			},
		},
	}, id, stream.FilterByBuildID)
	if err != nil {
		return 0, err
	}
	return len(r.buf[0:len(p)]), nil
}

func (r *StdioProxy) String() string {
	return "stdio"
}

func (r *StdioProxy) Fd() uintptr {
	if r.console != nil {
		return r.console.Fd()
	}
	return 0
}

func (r *StdioProxy) Name() string {
	return r.String()
}

func (r *StdioProxy) Close() error {
	if r.console != nil {
		return r.console.Close()
	}
	return nil
}
