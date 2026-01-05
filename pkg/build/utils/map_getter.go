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

package utils

import (
	"sort"
)

type MapGetter interface {
	Keys() []string
	Get(key string) (string, bool)
}

type mapGetter struct {
	m map[string]string
}

func (g mapGetter) Keys() []string {
	keys := make([]string, 0, len(g.m))
	for k := range g.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (g mapGetter) Get(key string) (string, bool) {
	v, ok := g.m[key]
	return v, ok
}

func NewMapGetter(m map[string]string) MapGetter {
	return mapGetter{m: m}
}
