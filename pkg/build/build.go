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

import (
	"context"
	"io"
	"strings"

	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/cmd/buildctl/build"
	"github.com/moby/buildkit/session"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	"github.com/apple-uat/container-builder-shim/pkg/exporter"
)

func Build(ctx context.Context, opts *BOpts) error {
	grpcOpts := []grpc.DialOption{
		grpc.WithReadBufferSize(16 << 20),        // 16MB
		grpc.WithWriteBufferSize(8 << 20),        // 8MB
		grpc.WithInitialWindowSize(8 << 10),      // 8KB
		grpc.WithInitialConnWindowSize(16 << 10), // 16KB
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),

		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(512<<20), // 512MB
			grpc.MaxCallSendMsgSize(512<<20), // 512MB
		),
	}
	clientOpts := []client.ClientOpt{}
	for _, opt := range grpcOpts {
		clientOpts = append(clientOpts, client.WithGRPCDialOption(opt))
	}

	buildkit, err := client.New(ctx, "", clientOpts...)
	if err != nil {
		logrus.Debugf("failed to connect to buildkit")
		return err
	}

	exports, err := build.ParseOutput(opts.Outputs)
	if err != nil {
		return err
	}

	if len(exports) == 0 {
		exports = append(exports, client.ExportEntry{
			Type: "oci",
		})
	}

	exportsWithOutput := []client.ExportEntry{}
	for _, export := range exports {
		export.Output = func(attrs map[string]string) (io.WriteCloser, error) {
			return exporter.NewBufferedWriteCloser(io.WriteCloser(opts.Exporter), 1<<22), nil
		}
		if _, ok := export.Attrs["name"]; !ok {
			export.Attrs["name"] = opts.Tag
		}
		if _, ok := export.Attrs["annotation-index-descriptor.com.apple.containerization.image.name"]; !ok {
			export.Attrs["annotation-index-descriptor.com.apple.containerization.image.name"] = opts.Tag
		}
		exportsWithOutput = append(exportsWithOutput, export)
	}

	cacheImports, err := build.ParseImportCache(opts.CacheIn)
	if err != nil {
		return err
	}

	cacheExports, err := build.ParseExportCache(opts.CacheOut)
	if err != nil {
		return err
	}

	solveOpt := client.SolveOpt{
		Exports:      exportsWithOutput,
		CacheImports: cacheImports,
		CacheExports: cacheExports,
		Session: []session.Attachable{
			opts.FSSync,
		},
		FrontendAttrs: map[string]string{},
	}
	solveOpt.OCIStores = map[string]content.Store{
		KeyContentStoreName: opts.ContentStore,
	}
	if opts.NoCache {
		solveOpt.FrontendAttrs["no-cache"] = ""
	}

	for k, v := range opts.BuildArgs {
		solveOpt.FrontendAttrs["build-arg:"+k] = v
	}

	platformStrings := []string{}
	for _, platform := range opts.Platforms {
		platformStrings = append(platformStrings, platforms.Format(platforms.Normalize(platform)))
	}
	if len(opts.Platforms) > 0 {
		solveOpt.FrontendAttrs["platform"] = strings.Join(platformStrings, ",")
	}
	if len(opts.Platforms) > 1 {
		solveOpt.FrontendAttrs["multi-platform"] = "true"
	}
	if opts.Target != "" {
		solveOpt.FrontendAttrs["target"] = opts.Target
	}
	for k, v := range opts.Labels {
		solveOpt.FrontendAttrs["label:"+k] = v
	}
	solveOpt.Frontend = "dockerfile.v1"

	_, err = buildkit.Build(opts.Context(ctx), solveOpt, "", frontend, opts.ProgressWriter.Status())
	<-opts.ProgressWriter.Done()
	return err
}
