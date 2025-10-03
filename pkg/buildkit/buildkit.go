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
	"context"
	"os"
	"strings"
	"syscall"

	log "github.com/sirupsen/logrus"
)

func Start(ctx context.Context, config BuildkitdConfig, args ...string) error {
	if err := config.Save(); err != nil {
		return err
	}

	binary := ""
	if len(args) > 0 {
		binary = args[0]
	}

	if binary == "" {
		binary = "/usr/bin/buildkitd"
	}

	if len(args) == 0 {
		args = []string{binary}
	}

	path := "/sbin:/usr/sbin:/bin:/usr/bin"
	envs := []string{}
	for _, env := range os.Environ() {
		envSplits := strings.SplitN(env, "=", 2)
		k := envSplits[0]
		v := ""
		if len(envSplits) > 1 {
			v = envSplits[1]
		}
		if strings.EqualFold(k, "PATH") {
			v = v + ":" + path
		}
		envs = append(envs, k+"="+v)
	}
	if len(envs) == 0 {
		envs = append(envs, "PATH="+path)
	}
	attr := &os.ProcAttr{
		Files: []*os.File{
			nil,
			os.Stdout,
			os.Stderr,
		},
		Sys: &syscall.SysProcAttr{},
		Env: envs,
	}
	proc, err := os.StartProcess(binary, args, attr)
	if err != nil {
		log.Errorf("Failed to execute: %v", args)
		return err
	}
	errCh := make(chan error)
	go func() {
		state, err := proc.Wait()
		log.Debugf("Subprocess exited with: %v", state)
		errCh <- err
	}()
	select {
	case <-ctx.Done():
		proc.Signal(syscall.SIGTERM)
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
