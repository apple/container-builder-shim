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
	"github.com/moby/buildkit/frontend/dockerfile/shell"
	"github.com/moby/buildkit/util/progress/progresswriter"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/apple/container-builder-shim/pkg/build/utils"
	"github.com/apple/container-builder-shim/pkg/content"
	"github.com/apple/container-builder-shim/pkg/fssync"
	"github.com/apple/container-builder-shim/pkg/resolver"
	"github.com/apple/container-builder-shim/pkg/stdio"
)

const (
	// Name used to identify the content store.
	KeyContentStoreName = "container"
	// Base64-encoded Dockerfile contents.
	KeyDockerfile = "dockerfile"
	// Hidden directory for the dockerfile and dockerignore to be placed.
	// This is provided when docker specific ignore file is found, which might live outside the build context.
	KeyHiddenDockerDir = "hidden-docker-dir"
	// Image reference (name:tag) to assign to the built image.
	KeyTag = "tag"
	// Target platforms to build the image for.
	KeyPlatforms = "platforms"
	// Progress output mode: auto, tty, or plain.
	KeyProgress = "progress"
	// When present, disables layer caching.
	KeyNoCache = "no-cache"
	// Build context directory path.
	KeyContext = "context"
	// Dockerfile stage to build up to.
	KeyTarget = "target"
	// Key=value metadata labels to apply to the image.
	KeyLabels = "labels"
	// ARG key=value pairs passed to the Dockerfile.
	KeyBuildArgs = "build-args"
	// RUN --mount=type=secret,... id:value pairs passed to the Dockerfile.
	KeySecrets = "secrets"
	// Cache import sources.
	KeyCacheIn = "cache-in"
	// Cache export destinations.
	KeyCacheOut = "cache-out"
	// Additional export destinations.
	KeyOutput = "outputs"
	// Unique build identifier.
	KeyBuildID = "build-id"
)

const (
	GlobalExportPath = "/var/lib/container-builder-shim/exports"
)

var keyBOpts = struct{}{}

type BOpts struct {
	BuildID         string
	Dockerfile      []byte
	Tag             string
	ContextDir      string
	HiddenDockerDir string
	BuildPlatforms  []ocispecs.Platform
	Platforms       []ocispecs.Platform
	NoCache         bool
	Target          string
	BuildArgs       map[string]string
	Secrets         map[string][]byte
	CacheIn         []string
	CacheOut        []string
	Outputs         []string
	Labels          map[string]string
	ProgressWriter  progresswriter.Writer

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

	hiddenDockerDir, _ := first(KeyHiddenDockerDir)

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

	normalizeBuildArg := func(arg string) string {
		arg = strings.TrimSpace(arg)
		if len(arg) >= 2 {
			if (arg[0] == '"' && arg[len(arg)-1] == '"') || (arg[0] == '\'' && arg[len(arg)-1] == '\'') {
				arg = arg[1 : len(arg)-1]
			}
		}
		return arg
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
	mapExtractB64 := func(key string) (map[string][]byte, error) {
		values, ok := contextMap[key]
		if !ok {
			return map[string][]byte{}, nil
		}
		args := map[string][]byte{}
		for _, label := range values {
			parts := strings.SplitN(label, "=", 2)
			switch len(parts) {
			case 1:
				args[parts[0]] = []byte{}
			case 2:
				dat, err := base64.StdEncoding.DecodeString(parts[1])
				if err != nil {
					return nil, err
				}
				args[parts[0]] = dat
			}
		}
		return args, nil
	}

	labels := mapExtract(KeyLabels)
	buildArgs := mapExtract(KeyBuildArgs)
	secrets, err := mapExtractB64(KeySecrets)
	if err != nil {
		return nil, err
	}
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
			if _, ok := buildArgs[arg.Key]; ok {
				continue
			}
			// Expand with prior args and strip shell quotes
			resolved, err := shell.NewLex('\\').ProcessWordWithMatches(arg.ValueString(), utils.NewMapGetter(buildArgs))
			if err != nil {
				return nil, err
			}
			// Save the resolved value for later use
			buildArgs[arg.Key] = normalizeBuildArg(resolved.Result)
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

		// Extract source paths from bind mount flags in RUN commands
		if strings.EqualFold(node.Value, "RUN") {
			cmd, err := instructions.ParseInstruction(node)
			if err != nil {
				continue
			} else if runCmd, ok := cmd.(*instructions.RunCommand); ok {
				runCmd.Expand(func(word string) (string, error) {
					// Single word expander to normalize source path
					source := strings.TrimPrefix(word, "/")
					normalized := filepath.Clean(source)
					return normalized, nil
				})
				mounts := instructions.GetMounts(runCmd)
				for _, mount := range mounts {
					// Only add source paths from bind mounts (not from other stages)
					if mount.Type == instructions.MountTypeBind && mount.Source != "" && mount.From == "" {
						addedGlobs = append(addedGlobs, mount.Source)
					}
				}
			}
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
		BuildID:         buildID,
		Dockerfile:      dockerfileBytes,
		Tag:             tag,
		BuildPlatforms:  bps,
		Platforms:       pls,
		ContextDir:      ctxDir,
		HiddenDockerDir: hiddenDockerDir,
		ContentStore:    contentProxy,
		FSSync:          fssyncProxy,
		NoCache:         noCache,
		Resolver:        resolver.NewResolverProxy(),
		ProgressWriter:  pw,
		Stdio:           stdioProxy,
		Target:          target,
		Labels:          labels,
		BuildArgs:       buildArgs,
		Secrets:         secrets,
		CacheIn:         cacheIn,
		CacheOut:        cacheOut,
		Outputs:         outputs,
		basePath:        filepath.Join(basePath, buildID),
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
