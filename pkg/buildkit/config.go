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

package buildkit

import (
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml"
)

const ConfigPath = "/etc/buildkit/buildkitd.toml"

var DefaultConfig = BuildkitdConfig{
	Debug: false,
	Trace: false,
	Worker: &WorkerConfig{
		OCI: &OCIWorkerConfig{
			Enabled:        true,
			RuncBinaryPath: "/usr/bin/buildkit-runc",
			GC:             true,
			GCKeepStorage:  int64Ptr(1 << 35), // 32 GB
			GCPolicy:       []GCPolicyRule{},
		},
	},
	Registry: map[string]RegistryConfig{},
	GRPC: &GRPCConfig{
		DebugAddress: "",
	},
}

//  A minimal GRPC config type for the buildkitd.toml file
//  This is needed because the config type in moby/buildkit
//  does not have a default marshaler
//  -----
//   # buildkit.toml
//   [debug]
//     level = "debug"
//
//   [worker.oci]
//     enabled = true
//     gc = true
//     gckeepstorage = 34359738368  # 32GB
//     runc_binary_path = "/usr/bin/runc"
//
//   [registry."docker.io"]
//     mirrors = ["mirror1", "mirror2"]

type BuildkitdConfig struct {
	Debug    bool                      `toml:"debug"`
	Trace    bool                      `toml:"trace"`
	Worker   *WorkerConfig             `toml:"worker,omitempty"`
	Registry map[string]RegistryConfig `toml:"registry,omitempty"`
	GRPC     *GRPCConfig               `toml:"grpc,omitempty"`
}

type GRPCConfig struct {
	DebugAddress string `toml:"debugAddress,omitempty"`
}

type OCIWorkerConfig struct {
	Enabled        bool   `toml:"enabled"`
	RuncBinaryPath string `toml:"binary"`

	GC            bool           `toml:"gc"`
	GCKeepStorage *int64         `toml:"gckeepstorage,omitempty"`
	GCPolicy      []GCPolicyRule `toml:"gcpolicy,omitempty"`
}

type GCPolicyRule struct {
	Filters      []string `toml:"filters,omitempty"`
	KeepDuration *int64   `toml:"keepDuration,omitempty"`
	KeepBytes    *int64   `toml:"keepBytes,omitempty"`
	All          bool     `toml:"all,omitempty"`
}

type WorkerConfig struct {
	OCI *OCIWorkerConfig `toml:"oci,omitempty"`
}

type RegistryConfig struct {
	Mirrors []string `toml:"mirrors,omitempty"`
}

func (bc *BuildkitdConfig) Save() error {
	data, err := toml.Marshal(bc)
	if err != nil {
		return err
	}
	err = os.MkdirAll(filepath.Dir(ConfigPath), 0o755)
	if err != nil {
		return err
	}

	return os.WriteFile(ConfigPath, data, 0o644)
}

func int64Ptr(v int64) *int64 {
	return &v
}
