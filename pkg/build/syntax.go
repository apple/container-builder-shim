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
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

const (
	keyBuildkitSyntax = "BUILDKIT_SYNTAX"
)

type frontendSettings struct {
	Name    string // "dockerfile.v0", "gateway.v0", or "" for the native dockerfile path
	Source  string // gateway image ref when applicable
	Cmdline string
	Opts    map[string]string // extra FrontendAttrs
}

func (f frontendSettings) native() bool {
	return f.Name == ""
}

func resolveFrontend(dockerfile []byte, buildArgs map[string]string, contextMap map[string][]string) frontendSettings {
	opts := mapExtract(contextMap, KeyFrontendOpt)
	if len(opts) == 0 {
		opts = nil
	}

	if name, ok := first(contextMap, KeyFrontend); ok {
		settings := frontendSettings{
			Name: name,
			Opts: opts,
		}
		if source, ok := opts["source"]; ok {
			settings.Source = source
		}
		if cmdline, ok := opts["cmdline"]; ok {
			settings.Cmdline = cmdline
		}
		return settings
	}

	if cmdline, ok := buildArgs[keyBuildkitSyntax]; ok {
		cmdline = strings.TrimSpace(cmdline)
		if cmdline != "" {
			ref, _ := splitSyntaxCmdline(cmdline)
			if ref != "" {
				return frontendSettings{
					Name:    frontendGatewayV0,
					Source:  ref,
					Cmdline: cmdline,
					Opts:    opts,
				}
			}
		}
	}

	if ref, cmdline, _, ok := parser.DetectSyntax(dockerfile); ok {
		ref = strings.TrimSpace(ref)
		if ref != "" {
			return frontendSettings{
				Name:    frontendGatewayV0,
				Source:  ref,
				Cmdline: cmdline,
				Opts:    opts,
			}
		}
	}

	return frontendSettings{}
}

func splitSyntaxCmdline(cmdline string) (ref, full string) {
	cmdline = strings.TrimSpace(cmdline)
	parts := strings.SplitN(cmdline, " ", 2)
	return parts[0], cmdline
}
