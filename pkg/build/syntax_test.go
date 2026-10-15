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
	"reflect"
	"testing"
)

func TestResolveFrontend(t *testing.T) {
	tests := []struct {
		name       string
		dockerfile string
		buildArgs  map[string]string
		contextMap map[string][]string
		want       frontendSettings
		wantNative bool
	}{
		{
			name: "plain dockerfile uses native path",
			dockerfile: `FROM alpine:latest
RUN echo hi`,
			buildArgs:  map[string]string{},
			contextMap: map[string][]string{},
			want:       frontendSettings{},
			wantNative: true,
		},
		{
			name: "yamlfile syntax header",
			dockerfile: `# syntax=ghcr.io/builderhub/yamlfile:latest
apiVersion: v1alpha1
targets:
  myapp:
    from: alpine:latest`,
			buildArgs:  map[string]string{},
			contextMap: map[string][]string{},
			want: frontendSettings{
				Name:    frontendGatewayV0,
				Source:  "ghcr.io/builderhub/yamlfile:latest",
				Cmdline: "ghcr.io/builderhub/yamlfile:latest",
			},
			wantNative: false,
		},
		{
			name:       "BUILDKIT_SYNTAX build arg",
			dockerfile: `FROM alpine:latest`,
			buildArgs: map[string]string{
				keyBuildkitSyntax: "ghcr.io/builderhub/yamlfile:latest",
			},
			contextMap: map[string][]string{},
			want: frontendSettings{
				Name:    frontendGatewayV0,
				Source:  "ghcr.io/builderhub/yamlfile:latest",
				Cmdline: "ghcr.io/builderhub/yamlfile:latest",
			},
			wantNative: false,
		},
		{
			name: "labs syntax directive",
			dockerfile: `# syntax=docker/dockerfile:1.7-labs
FROM alpine:latest
RUN --mount=type=cache echo hi`,
			buildArgs:  map[string]string{},
			contextMap: map[string][]string{},
			want: frontendSettings{
				Name:    frontendGatewayV0,
				Source:  "docker/dockerfile:1.7-labs",
				Cmdline: "docker/dockerfile:1.7-labs",
			},
			wantNative: false,
		},
		{
			name:       "explicit gateway frontend",
			dockerfile: `FROM alpine:latest`,
			buildArgs:  map[string]string{},
			contextMap: map[string][]string{
				KeyFrontend:    {"gateway.v0"},
				KeyFrontendOpt: {"source=ghcr.io/builderhub/yamlfile:latest"},
			},
			want: frontendSettings{
				Name:   frontendGatewayV0,
				Source: "ghcr.io/builderhub/yamlfile:latest",
				Opts: map[string]string{
					"source": "ghcr.io/builderhub/yamlfile:latest",
				},
			},
			wantNative: false,
		},
		{
			name:       "explicit dockerfile.v0 frontend",
			dockerfile: `FROM alpine:latest`,
			buildArgs:  map[string]string{},
			contextMap: map[string][]string{
				KeyFrontend: {"dockerfile.v0"},
			},
			want: frontendSettings{
				Name: frontendDockerfileV0,
			},
			wantNative: false,
		},
		{
			name: "BUILDKIT_SYNTAX takes priority over file syntax",
			dockerfile: `# syntax=docker/dockerfile:1.7-labs
FROM alpine:latest`,
			buildArgs: map[string]string{
				keyBuildkitSyntax: "ghcr.io/custom/frontend:v1",
			},
			contextMap: map[string][]string{},
			want: frontendSettings{
				Name:    frontendGatewayV0,
				Source:  "ghcr.io/custom/frontend:v1",
				Cmdline: "ghcr.io/custom/frontend:v1",
			},
			wantNative: false,
		},
		{
			name:       "explicit frontend metadata takes priority over BUILDKIT_SYNTAX",
			dockerfile: `FROM alpine:latest`,
			buildArgs: map[string]string{
				keyBuildkitSyntax: "ghcr.io/custom/frontend:v1",
			},
			contextMap: map[string][]string{
				KeyFrontend: {"dockerfile.v0"},
			},
			want: frontendSettings{
				Name: frontendDockerfileV0,
			},
			wantNative: false,
		},
		{
			name: "explicit frontend takes priority over file syntax header",
			dockerfile: `# syntax=ghcr.io/builderhub/yamlfile:latest
FROM alpine:latest`,
			buildArgs: map[string]string{},
			contextMap: map[string][]string{
				KeyFrontend:    {"gateway.v0"},
				KeyFrontendOpt: {"source=ghcr.io/custom/explicit:latest"},
			},
			want: frontendSettings{
				Name:   frontendGatewayV0,
				Source: "ghcr.io/custom/explicit:latest",
				Opts: map[string]string{
					"source": "ghcr.io/custom/explicit:latest",
				},
			},
			wantNative: false,
		},
		{
			name: "empty BUILDKIT_SYNTAX uses native path",
			dockerfile: `FROM alpine:latest
RUN echo hi`,
			buildArgs: map[string]string{
				keyBuildkitSyntax: "",
			},
			contextMap: map[string][]string{},
			want:       frontendSettings{},
			wantNative: true,
		},
		{
			name: "whitespace BUILDKIT_SYNTAX uses native path",
			dockerfile: `FROM alpine:latest
RUN echo hi`,
			buildArgs: map[string]string{
				keyBuildkitSyntax: "   ",
			},
			contextMap: map[string][]string{},
			want:       frontendSettings{},
			wantNative: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveFrontend([]byte(tt.dockerfile), tt.buildArgs, tt.contextMap)

			if got.native() != tt.wantNative {
				t.Errorf("resolveFrontend().native() = %v, want %v", got.native(), tt.wantNative)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveFrontend() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
