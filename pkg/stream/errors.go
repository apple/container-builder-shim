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
	"errors"
	"fmt"
)

var (
	ErrIgnorePacket      = errors.New("ignore packet")
	ErrRecvStreamClosed  = errors.New("receive stream closed")
	ErrNoHandlerFound    = errors.New("no handler found for packet")
	ErrNotATTY           = errors.New("not a tty")
	ErrSendStreamBlocked = errors.New("send stream is blocked")
)

type UninitializedStageErr string

func (e UninitializedStageErr) Error() string {
	return fmt.Sprintf("uninitialized stage: %s", string(e))
}
