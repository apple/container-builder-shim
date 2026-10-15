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
	"testing"
)

const yamlfileSample = `# syntax=ghcr.io/builderhub/yamlfile:latest
apiVersion: v1alpha1
targets:
  myapp:
    from: alpine:latest
    steps:
      - run: echo hello
`

func TestExternalFrontendSkipsDockerfileParse(t *testing.T) {
	frontend := resolveFrontend([]byte(yamlfileSample), map[string]string{}, map[string][]string{})
	if frontend.native() {
		t.Fatal("expected external frontend for yamlfile syntax header")
	}

	_, _, err := parseDockerfileMetadata([]byte(yamlfileSample), map[string]string{})
	if err == nil {
		t.Fatal("parseDockerfileMetadata should fail on yamlfile content")
	}
}

func TestNativeFrontendParsesDockerfileMetadata(t *testing.T) {
	dockerfile := `FROM alpine:latest
RUN echo hi
`
	frontend := resolveFrontend([]byte(dockerfile), map[string]string{}, map[string][]string{})
	if !frontend.native() {
		t.Fatalf("expected native frontend, got %+v", frontend)
	}

	globs, _, err := parseDockerfileMetadata([]byte(dockerfile), map[string]string{})
	if err != nil {
		t.Fatalf("parseDockerfileMetadata() error = %v", err)
	}
	if len(globs) != 0 {
		t.Fatalf("parseDockerfileMetadata() globs = %v, want empty", globs)
	}
}

func TestEnsureDockerfileStagingExcluded(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{
			name:  "empty input adds staging exclude",
			input: nil,
			want:  DockerfileStaging + "\n",
		},
		{
			name:  "already present is unchanged",
			input: []byte("node_modules\n" + DockerfileStaging + "\n"),
			want:  "node_modules\n" + DockerfileStaging + "\n",
		},
		{
			name:  "appends staging exclude",
			input: []byte("node_modules\n"),
			want:  "node_modules\n\n" + DockerfileStaging,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(ensureDockerfileStagingExcluded(tt.input))
			if got != tt.want {
				t.Fatalf("ensureDockerfileStagingExcluded() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateFrontendRejectsDockerfileV0(t *testing.T) {
	err := validateFrontend(frontendSettings{Name: frontendDockerfileV0})
	if err == nil {
		t.Fatal("expected error for dockerfile.v0 frontend")
	}
}
