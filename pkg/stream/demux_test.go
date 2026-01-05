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

package stream

import (
	"context"
	"testing"

	"github.com/apple/container-builder-shim/pkg/api"
)

func TestDemultiplexerAcceptRecv(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id := "demo-1"
	pkt := &api.ClientStream{BuildId: id}

	dm := NewDemuxWithContext(ctx, id, FilterAllowAll, func(any) {})

	if err := dm.Accept(pkt); err != nil {
		t.Fatalf("Accept returned %v", err)
	}

	out, err := dm.Recv()
	if err != nil {
		t.Fatalf("Recv returned error %v", err)
	}
	if out != pkt {
		t.Fatalf("unexpected packet: %+v", out)
	}
}

func TestDemultiplexerContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	id := "demo-2"
	dm := NewDemuxWithContext(ctx, id, FilterAllowAll, func(any) {})

	cancel() // cancel before Recv()

	_, err := dm.Recv()
	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
}
