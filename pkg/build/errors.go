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

package build

import "fmt"

var (
	ErrInvalidGatewayRequest     = fmt.Errorf("invalid request to gateway")
	ErrMissingContext            = fmt.Errorf("context missing")
	ErrMissingContextDockerfile  = fmt.Errorf("dockerfile missing in build context")
	ErrMissingContextRef         = fmt.Errorf("ref missing in build context")
	ErrTypeAssertionFail         = fmt.Errorf("type assertion failed")
	ErrNoBuildDirectives         = fmt.Errorf("no build directives")
	ErrInvalidImageContextFormat = fmt.Errorf("image resolver: image name format is invalid")
	ErrInvalidProgress           = fmt.Errorf("build arg progress value is invalid")
)
