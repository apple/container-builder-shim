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

package server

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/mdlayher/vsock"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/build"
	"github.com/apple/container-builder-shim/pkg/stream"
)

var _ api.BuilderServer = &BuilderProxy{}

type BuilderProxy struct {
	api.UnimplementedBuilderServer

	ctx    context.Context
	exitCh chan error
	path   string
}

type SocketConfig struct {
	Port       uint32
	SocketPath string
	SocketType string
}

const (
	SocketTypeUnix  = "unix"
	SocketTypeVSock = "vsock"
)

func (b *BuilderProxy) CreateBuild(ctx context.Context, req *api.CreateBuildRequest) (*api.CreateBuildResponse, error) {
	return nil, nil
}

func (b *BuilderProxy) PerformBuild(s api.Builder_PerformBuildServer) (err error) {
	ctx, cancel := context.WithCancel(s.Context())
	defer cancel()

	md, ok := metadata.FromIncomingContext(s.Context())
	if !ok {
		return ErrMetadataMissing
	}

	var opts *build.BOpts
	opts, err = build.NewBuildOpts(ctx, b.path, md)
	if err != nil {
		return err
	}

	stages := []stream.Stage{
		opts.ContentStore,
		opts.FSSync,
		opts.Resolver,
		opts.Stdio,
	}

	var pipeline *stream.StreamPipeline
	pipeCtx, pipeCancel := context.WithCancel(ctx)
	defer pipeCancel()
	pipeline, err = stream.NewPipeline(pipeCtx, s, stages...)
	if err != nil {
		return err
	}
	go func() {
		if err := pipeline.Run(); err != nil {
			if err != context.Canceled {
				logrus.Errorf("pipeline run failure: %v", err)
			}
		}
	}()

	err = build.Build(ctx, opts)
	return err
}

func (b *BuilderProxy) Info(ctx context.Context, req *api.InfoRequest) (*api.InfoResponse, error) {
	return &api.InfoResponse{}, nil
}

func Run(ctx context.Context, basePath string, socketConfig SocketConfig) error {
	grpcServer := grpc.NewServer(
		grpc.ReadBufferSize(8<<20),                // 8MB
		grpc.WriteBufferSize(4<<20),               // 4MB
		grpc.InitialWindowSize(int32(8<<10)),      // 8KB
		grpc.InitialConnWindowSize(int32(16<<10)), // 16KB
		grpc.MaxRecvMsgSize(512<<20),              // 512 MB
		grpc.MaxSendMsgSize(512<<20),              // 512 MB
		grpc.MaxConcurrentStreams(uint32(64)),
		grpc.UnaryInterceptor(debugGRPCLogger),
	)

	newCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error)
	exitCh := make(chan error)

	reflection.Register(grpcServer)
	api.RegisterBuilderServer(grpcServer, &BuilderProxy{
		ctx:    newCtx,
		path:   basePath,
		exitCh: exitCh,
	})

	var listener net.Listener
	switch socketConfig.SocketType {
	case SocketTypeUnix:
		var lc net.ListenConfig
		var err error
		listener, err = lc.Listen(newCtx, "unix", socketConfig.SocketPath)
		if err != nil {
			return err
		}
	case SocketTypeVSock:
		var err error
		listener, err = listenVSock(socketConfig.Port)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown socket type: %s", socketConfig.SocketType)
	}

	go func() {
		defer listener.Close()
		errCh <- grpcServer.Serve(listener)
	}()

	select {
	case err := <-errCh:
		return err
	case err := <-exitCh:
		return err
	}
}

func listenVSock(port uint32) (*vsock.Listener, error) {
	var (
		l   *vsock.Listener
		err error
	)
	for range 1000 {
		if l, err = vsock.Listen(port, nil); err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		break
	}
	if l == nil {
		return nil, err
	}
	return l, nil
}
