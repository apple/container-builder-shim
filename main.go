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

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"net/http"
	_ "net/http/pprof"

	"github.com/apple/container-builder-shim/pkg/buildkit"
	"github.com/apple/container-builder-shim/pkg/server"
)

var (
	VERSION         = "dev"
	debug           = false
	enableQemu      = false
	socketPath      = "/run/buildkit/shim.sock"
	buildkitdPath   = "/usr/bin/buildkitd"
	basePath        = "/var/lib/container-builder-shim"
	registryMirrors = []string{}
	vsockPort       = 8088
	vsockMode       = false
)

var app = &cobra.Command{
	Use:           os.Args[0],
	Short:         "BuildKit shim that interfaces with the container builder API",
	SilenceUsage:  true,
	SilenceErrors: true,
	Version:       VERSION,
	CompletionOptions: cobra.CompletionOptions{
		DisableDefaultCmd:   true,
		DisableNoDescFlag:   true,
		DisableDescriptions: true,
		HiddenDefaultCmd:    true,
	},
	PersistentPreRunE: func(c *cobra.Command, args []string) error {
		if debug {
			log.SetLevel(log.DebugLevel)
		}
		if !vsockMode {
			socketDir := filepath.Dir(socketPath)
			if err := os.MkdirAll(socketDir, os.ModeDir); err != nil {
				return err
			}
			// make sure socket path is cleaned after previous runs
			return os.RemoveAll(socketPath)
		}
		if debug {
			go func() {
				// Start pprof server on :10000
				if err := http.ListenAndServe(":10000", nil); err != nil {
					log.Errorf("pprof HTTP server failed: %v", err)
				}
			}()
		}
		return nil
	},
	RunE: func(c *cobra.Command, args []string) error {
		ctx := c.Context()
		cancellableCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		if !enableQemu {
			disableQemu()
		}

		errCh := make(chan error)
		go func() {
			config := buildkit.DefaultConfig
			for _, rm := range registryMirrors {
				parts := strings.Split(rm, "=")
				if len(parts) != 2 {
					errCh <- fmt.Errorf("invalid registry mirror specification: %s", rm)
					return
				}
				key := parts[0]
				value := parts[1]

				var rc buildkit.RegistryConfig
				var ok bool
				rc, ok = config.Registry[key]
				if !ok {
					rc = buildkit.RegistryConfig{}
				}
				rc.Mirrors = append(rc.Mirrors, value)

				config.Registry[key] = rc
			}
			if debug {
				config.Debug = true
				config.GRPC.DebugAddress = "0.0.0.0:10001"
			}
			runcPath, err := exec.LookPath("buildkit-runc")
			if err == nil {
				config.Worker.OCI.RuncBinaryPath = runcPath
			}

			errCh <- buildkit.Start(cancellableCtx, config, buildkitdPath)
		}()

		go func() {
			socketConfig := server.SocketConfig{}
			if vsockMode {
				socketConfig.Port = uint32(vsockPort)
				socketConfig.SocketType = server.SocketTypeVSock
			} else {
				socketConfig.SocketPath = socketPath
				socketConfig.SocketType = server.SocketTypeUnix
			}
			errCh <- server.Run(cancellableCtx, basePath, socketConfig)
		}()

		err := <-errCh
		log.Errorf("Exiting %v", err)
		return err
	},
}

func disableQemu() {
	path := "/usr/bin/buildkit-qemu-x86_64"
	disabled := path + ".disabled"

	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, disabled); err != nil {
			log.Warnf("failed to disable %s: %v", path, err)
		} else {
			log.Infof("Renamed %s to %s", filepath.Base(path), filepath.Base(disabled))
		}
	} else if os.IsNotExist(err) {
		log.Infof("%s not found; nothing to do", path)
	} else {
		log.Warnf("error checking %s: %v", path, err)
	}
}

func init() {
	app.PersistentFlags().BoolVarP(&debug, "debug", "d", debug, "enable debug logging")
	app.Flags().BoolVar(&enableQemu, "enable-qemu", enableQemu, "use QEMU instead of Rosetta for amd64 builds")
	app.Flags().IntVarP(&vsockPort, "vsock-port", "p", vsockPort, "vsock port for shim listener")
	app.Flags().BoolVarP(&vsockMode, "vsock", "v", vsockMode, "toggle vsock listener (turns off UDS listener)")
	app.Flags().StringVarP(&socketPath, "socket", "s", socketPath, "socket path for shim listener")
	app.Flags().StringVarP(&buildkitdPath, "buildkitd-path", "b", buildkitdPath, "path to buildkitd binary")
	app.Flags().StringSliceVarP(&registryMirrors, "registry-mirrors", "r", registryMirrors, "list of registry mirrors in k=v pairs")
}

func main() {
	logFile, err := os.OpenFile("/var/log/container-builder-shim.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalln("failed to open logfile", err)
	}
	defer logFile.Close()

	output := io.MultiWriter(os.Stdout, logFile)
	log.SetOutput(output)
	log.SetFormatter(&log.TextFormatter{
		FieldMap: log.FieldMap{
			log.FieldKeyTime:  "timestamp",
			log.FieldKeyLevel: "level",
			log.FieldKeyMsg:   "message",
		},
	})

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGSEGV)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		s := <-sigs
		log.Debugf("Signal %s received", s.String())
		cancel()
		<-time.After(1 * time.Second)
		os.Exit(1)
	}()

	if err := app.ExecuteContext(ctx); err != nil {
		log.Fatalln(err)
	}
}
