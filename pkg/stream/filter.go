//===----------------------------------------------------------------------===//
// Copyright © 2024-2025 Apple Inc. and the container-builder-shim project authors. All rights reserved.
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

import "github.com/apple-uat/container-builder-shim/pkg/api"

type FilterFn func(*api.ClientStream) error
type FilterByIDFn func(id string) FilterFn

type Filter interface {
	Filter(*api.ClientStream) error
}

var _ Filter = (*filterImpl)(nil)

type filterImpl struct{ f FilterFn }

func (f *filterImpl) Filter(c *api.ClientStream) error {
	if f.f == nil {
		return UninitializedStageErr("filter function")
	}
	return f.f(c)
}

func FilterChain(funcs ...FilterFn) FilterFn {
	return func(c *api.ClientStream) error {
		for _, fn := range funcs {
			if err := fn(c); err != nil {
				return err
			}
		}
		return nil
	}
}

func FilterByBuildID(id string) FilterFn {
	return func(c *api.ClientStream) error {
		if c.GetBuildId() == id {
			return nil
		}
		return ErrIgnorePacket
	}
}

func FilterByImageTransferID(id string) FilterFn {
	return func(c *api.ClientStream) error {
		if it := c.GetImageTransfer(); it != nil && it.Id == id {
			return nil
		}
		return ErrIgnorePacket
	}
}

func FilterByBuildTransferID(id string) FilterFn {
	return func(c *api.ClientStream) error {
		if bt := c.GetBuildTransfer(); bt != nil && bt.Id == id {
			return nil
		}
		return ErrIgnorePacket
	}
}

func FilterByCommandID(id string) FilterFn {
	return func(c *api.ClientStream) error {
		if cmd := c.GetCommand(); cmd != nil && cmd.Id == id {
			return nil
		}
		return ErrIgnorePacket
	}
}

func FilterAllowAll(c *api.ClientStream) error {
	return nil
}
