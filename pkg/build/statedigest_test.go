//===----------------------------------------------------------------------===//
// Copyright © 2026 Apple Inc. and the container-builder-shim project authors.
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

package build

import (
	"context"
	"testing"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
)

// The frontend hands each pre-resolved base to the solver as a marshaled
// state, and the solver caches by the digests inside. A state built twice
// from the same inputs must marshal to the same definition, or every build
// re-runs whatever consumes it.
func TestStateDigestDeterminism(t *testing.T) {
	img := []byte(`{"architecture":"arm64","os":"linux","config":{"Env":["PATH=/usr/bin"]},"rootfs":{"type":"layers","diff_ids":["sha256:0000000000000000000000000000000000000000000000000000000000000000"]}}`)
	const fqdn = "docker.io/library/debian@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	pl := platforms.Normalize(platforms.MustParse("linux/arm64"))

	build := func() string {
		st := llb.OCILayout(fqdn,
			llb.Platform(pl),
			llb.OCIStore("", KeyContentStoreName),
		)
		st, err := st.WithImageConfig(img)
		if err != nil {
			t.Fatalf("WithImageConfig: %v", err)
		}
		def, err := st.Platform(pl).Marshal(context.Background())
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if len(def.Def) == 0 {
			t.Fatal("empty definition")
		}
		dgst, err := def.Head()
		if err != nil {
			t.Fatalf("Head: %v", err)
		}
		return dgst.String()
	}

	first := build()
	second := build()
	if first != second {
		t.Fatalf("same inputs marshaled to different digests:\n  %s\n  %s", first, second)
	}
	t.Logf("state digest: %s", first)
}
