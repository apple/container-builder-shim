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
	"testing"

	"github.com/apple/container-builder-shim/pkg/api"
)

func TestFilterByBuildID(t *testing.T) {
	buildID := "build-xyz"
	okPkt := &api.ClientStream{BuildId: buildID}
	badPkt := &api.ClientStream{BuildId: "other"}

	f := FilterByBuildID(buildID)

	if err := f(okPkt); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
	if err := f(badPkt); err != ErrIgnorePacket {
		t.Fatalf("expected ErrIgnorePacket, got %v", err)
	}
}

func TestFilterChainStopsOnFirstError(t *testing.T) {
	called := 0
	fn1 := func(*api.ClientStream) error {
		called++
		return ErrIgnorePacket
	}
	fn2 := func(*api.ClientStream) error {
		called++
		return nil
	}

	chain := FilterChain(fn1, fn2)
	if err := chain(&api.ClientStream{}); err != ErrIgnorePacket {
		t.Fatalf("expected ErrIgnorePacket, got %v", err)
	}
	if called != 1 {
		t.Fatalf("expected first filter only, called=%d", called)
	}
}
