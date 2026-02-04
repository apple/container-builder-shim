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

package build

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/util/progress/progresswriter"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/apple/container-builder-shim/pkg/build/utils"
	"github.com/apple/container-builder-shim/pkg/content"
	"github.com/apple/container-builder-shim/pkg/fssync"
	"github.com/apple/container-builder-shim/pkg/resolver"
	"github.com/apple/container-builder-shim/pkg/stdio"
)

const (
	KeyContentStoreName = "container"
	KeyDockerfile       = "dockerfile"
	KeyTag              = "tag"
	KeyPlatforms        = "platforms"
	KeyProgress         = "progress"
	KeyNoCache          = "no-cache"
	KeyContext          = "context"
	KeyTarget           = "target"
	KeyLabels           = "labels"
	KeyBuildArgs        = "build-args"
	KeyCacheIn          = "cache-in"
	KeyCacheOut         = "cache-out"
	KeyOutput           = "outputs"
	KeyBuildID          = "build-id"
)

const (
	GlobalExportPath = "/var/lib/container-builder-shim/exports"
)

var keyBOpts = struct{}{}

type BOpts struct {
	BuildID        string
	Dockerfile     []byte
	Tag            string
	ContextDir     string
	BuildPlatforms []ocispecs.Platform
	Platforms      []ocispecs.Platform
	NoCache        bool
	Target         string
	BuildArgs      map[string]string
	CacheIn        []string
	CacheOut       []string
	Outputs        []string
	Labels         map[string]string
	ProgressWriter progresswriter.Writer

	// stages
	ContentStore *content.ContentStoreProxy
	Resolver     *resolver.ResolverProxy
	FSSync       *fssync.FSSyncProxy
	Stdio        *stdio.StdioProxy

	basePath string
}

func NewBuildOpts(ctx context.Context, basePath string, contextMap map[string][]string) (*BOpts, error) {
	first := func(key string) (string, bool) {
		values, ok := contextMap[key]
		if !ok {
			return "", false
		}
		return values[0], true
	}

	buildID, ok := first(KeyBuildID)
	if !ok {
		return nil, ErrMissingBuildID
	}

	dockerfileBase64Bytes, ok := first(KeyDockerfile)
	if !ok {
		return nil, ErrMissingContextDockerfile
	}

	dockerfileBytes, err := base64.StdEncoding.DecodeString(dockerfileBase64Bytes)
	if err != nil {
		return nil, err
	}

	progress, ok := first(KeyProgress)
	if !ok {
		progress = "auto"
	}
	switch progress {
	case "auto", "tty", "plain":
	default:
		return nil, ErrInvalidProgress
	}

	noCache := false
	if _, ok := first(KeyNoCache); ok {
		noCache = true
	}

	tag, ok := first(KeyTag)
	if !ok {
		return nil, ErrMissingContextRef
	}

	ctxDir := "."
	if c, ok := first(KeyContext); ok {
		ctxDir = c
	}

	bps := utils.BuildPlatforms()
	if len(bps) == 0 {
		bps = append(bps, platforms.DefaultSpec())
	}

	pls, err := func() ([]ocispecs.Platform, error) {
		pls := []ocispecs.Platform{}
		values, ok := contextMap[KeyPlatforms]
		if !ok {
			return []ocispecs.Platform{platforms.DefaultSpec()}, nil
		}
		for _, plStr := range values {
			pl, err := platforms.Parse(plStr)
			if err != nil {
				return nil, err
			}
			pls = append(pls, pl)
		}
		return pls, nil
	}()
	if err != nil {
		return nil, err
	}

	target := ""
	if tStr, ok := first(KeyTarget); ok {
		target = tStr
	}

	mapExtract := func(key string) map[string]string {
		values, ok := contextMap[key]
		if !ok {
			return map[string]string{}
		}
		args := map[string]string{}
		for _, label := range values {
			parts := strings.SplitN(label, "=", 2)
			switch len(parts) {
			case 1:
				args[parts[0]] = ""
			case 2:
				args[parts[0]] = parts[1]
			}
		}
		return args
	}

	labels := mapExtract(KeyLabels)
	buildArgs := mapExtract(KeyBuildArgs)
	cacheIn := contextMap[KeyCacheIn]
	cacheOut := contextMap[KeyCacheOut]
	outputs := contextMap[KeyOutput]

	stdioProxy, err := stdio.NewStdioProxy(ctx, progress == "tty")
	if err != nil {
		return nil, err
	}

	dockerfile, err := parser.Parse(bytes.NewReader(dockerfileBytes))
	if err != nil {
		return nil, err
	}

	_, metaArgs, err := instructions.Parse(dockerfile.AST, nil)
	if err != nil {
		return nil, err
	}

	for _, metaArg := range metaArgs {
		for _, arg := range metaArg.Args {
			// Only use the dockerfile meta arg if the user did not overwrite it
			if _, ok := buildArgs[arg.Key]; !ok {
				buildArgs[arg.Key] = arg.ValueString()
			}
		}
	}

	pw, err := progresswriter.NewPrinter(ctx, stdioProxy, progress)
	if err != nil {
		return nil, err
	}

	// do add .dockerignore support
	// to ExcludePatterns(dockerignore) patterns
	addedGlobs := []string{}
	for _, node := range dockerfile.AST.Children {
		if strings.EqualFold(node.Value, "COPY") || strings.EqualFold(node.Value, "ADD") {
			addedGlobs = append(addedGlobs, node.Next.Value)
		}
	}

	fssyncProxy, err := fssync.NewFSSyncProxy(".", basePath, addedGlobs)
	if err != nil {
		return nil, err
	}

	contentProxy, err := content.NewContentStoreProxy()
	if err != nil {
		return nil, err
	}

	bopts := &BOpts{
		BuildID:        buildID,
		Dockerfile:     dockerfileBytes,
		Tag:            tag,
		BuildPlatforms: bps,
		Platforms:      pls,
		ContextDir:     ctxDir,
		ContentStore:   contentProxy,
		FSSync:         fssyncProxy,
		NoCache:        noCache,
		Resolver:       resolver.NewResolverProxy(),
		ProgressWriter: pw,
		Stdio:          stdioProxy,
		Target:         target,
		Labels:         labels,
		BuildArgs:      buildArgs,
		CacheIn:        cacheIn,
		CacheOut:       cacheOut,
		Outputs:        outputs,
		basePath:       filepath.Join(basePath, buildID),
	}

	return bopts, nil
}

func (b *BOpts) Context(parent context.Context) context.Context {
	return context.WithValue(parent, keyBOpts, b)
}

func newBOptsFromContext(ctx context.Context) (*BOpts, error) {
	buildOptsAny := ctx.Value(keyBOpts)
	if buildOptsAny == nil {
		return nil, ErrMissingContext
	}
	buildOpts, ok := buildOptsAny.(*BOpts)
	if !ok {
		return nil, ErrTypeAssertionFail
	}
	return buildOpts, nil
}
