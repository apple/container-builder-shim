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

package utils

import (
	"sort"

	"github.com/containerd/platforms"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

func BuildPlatforms() []ocispecs.Platform {
	pl := func(arch string, variant string) ocispecs.Platform {
		return ocispecs.Platform{
			Architecture: arch,
			Variant:      variant,
			OS:           "linux",
		}
	}
	pls := []ocispecs.Platform{
		pl("arm64", "v8"),
		pl("arm", "v7"),
		pl("arm", "v6"),
		pl("amd64", ""),
		pl("mips64", ""),
		pl("mips64el", ""),
		pl("ppc64le", ""),
		pl("riscv64", ""),
		pl("s390x", ""),
	}
	currentPlatform := platforms.DefaultSpec()

	// order of platforms matters because this is the order in which
	// buildkit pulls dependencies for a build
	sort.SliceStable(pls, func(i, j int) bool {
		// current architecture should always be at the top of the list
		if currentPlatform.Architecture == "amd64" {
			if pls[i].Architecture == "amd64" {
				return true
			}
			if pls[j].Architecture == "amd64" {
				return false
			}
			// arm64/v8 should be the second in the list
			// then arm/v7, arm/v6
			if pls[i].Architecture == "arm64" {
				return true
			}
			if pls[j].Architecture == "arm64" {
				return false
			}
		}
		if currentPlatform.Architecture == "arm64" {
			if pls[i].Architecture == "arm64" {
				return true
			}
			if pls[j].Architecture == "arm64" {
				return false
			}
			// amd64 should be provided only after arm64/v8, arm/v7, arm/v6 in the list
			if pls[i].Architecture == "amd64" {
				return pls[j].Architecture != "arm"
			}
			if pls[j].Architecture == "amd64" {
				return pls[i].Architecture == "arm"
			}
		}
		return pls[i].Architecture < pls[j].Architecture
	})
	return pls
}
