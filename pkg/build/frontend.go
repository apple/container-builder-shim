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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/containerd/containerd/reference"
	"github.com/containerd/platforms"
	dref "github.com/distribution/reference"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
	"github.com/moby/buildkit/frontend/dockerui"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress/progresswriter"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"

	"github.com/apple/container-builder-shim/pkg/build/utils"
)

func frontend(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
	bopts, err := newBOptsFromContext(ctx)
	if err != nil {
		return nil, err
	}

	res := gateway.NewResult()
	expPlatforms := &exptypes.Platforms{
		Platforms: make([]exptypes.Platform, len(bopts.Platforms)),
	}

	clog := func(format string, params ...interface{}) {
		if bopts.ProgressWriter != nil {
			msg := fmt.Sprintf(format, params...)
			progresswriter.Write(bopts.ProgressWriter, msg, nil)
		}
	}

	plWG := sync.WaitGroup{}
	plErrCh := make(chan error)
	plDoneCh := make(chan struct{})
	for i := range bopts.Platforms {
		plWG.Add(1)
		go func(i int) {
			defer plWG.Done()
			pl := bopts.Platforms[i]

			states, err := resolveStates(ctx, bopts, pl, clog)
			if err != nil {
				plErrCh <- err
			}

			ref, cfgJSON, err := solvePlatform(ctx, bopts, pl, c, states)
			if err != nil {
				plErrCh <- err
				return
			}
			plStr := platforms.FormatAll(platforms.Normalize(pl))
			res.AddRef(plStr, ref)
			res.AddMeta(fmt.Sprintf("%s/%s", exptypes.ExporterImageConfigKey, plStr), cfgJSON)
			expPlatforms.Platforms[i] = exptypes.Platform{
				ID:       plStr,
				Platform: pl,
			}
		}(i)
	}
	go func() { plWG.Wait(); plDoneCh <- struct{}{} }()
	select {
	case err := <-plErrCh:
		return nil, err
	case <-plDoneCh:
	}

	dt, err := json.Marshal(expPlatforms)
	if err != nil {
		return nil, err
	}
	res.AddMeta(exptypes.ExporterPlatformsKey, dt)
	return res, nil
}

type stateMeta struct {
	state   llb.State
	imgMeta []byte
}

func resolveStates(ctx context.Context, bopts *BOpts, platform ocispecs.Platform, clog func(string, ...interface{})) (map[string]stateMeta, error) {
	dockerfile, err := parser.Parse(bytes.NewReader(bopts.Dockerfile))
	if err != nil {
		return nil, err
	}

	stages, _, err := instructions.Parse(dockerfile.AST, nil)
	if err != nil {
		return nil, err
	}

	wg := sync.WaitGroup{}
	doneCh := make(chan struct{})
	errCh := make(chan error)

	states := map[string]stateMeta{}
	stateLock := sync.Mutex{}

	for i, stage := range stages {
		wg.Add(1)
		go func(stage instructions.Stage) {
			defer wg.Done()

			shlex := shell.NewLex(dockerfile.EscapeToken)
			resolvedGlobalArgs := globalArgs(bopts.BuildPlatforms[0], platform, bopts.BuildArgs, bopts.Target)
			resolvedBaseStageName, err := shlex.ProcessWordWithMatches(stage.BaseName, resolvedGlobalArgs)
			if err != nil {
				errCh <- fmt.Errorf("invalid arg for stage[%s]: %v", stage.BaseName, err)
				return
			}

			if strings.EqualFold(resolvedBaseStageName.Result, "scratch") || strings.EqualFold(resolvedBaseStageName.Result, "context") {
				return
			}

			// if platform is specified for the stage, parse and use as the target platform
			stagePlatform := platform
			if stage.Platform != "" {
				resolvedStagePlatformStr, err := shlex.ProcessWordWithMatches(stage.Platform, resolvedGlobalArgs)
				if err != nil {
					errCh <- fmt.Errorf("invalid platform for stage[%s]: %v", stage.BaseName, err)
					return
				}
				resolvedStagePlatform, err := platforms.Parse(resolvedStagePlatformStr.Result)
				if err != nil {
					errCh <- fmt.Errorf("invalid platform for stage[%s]: %v", stage.BaseName, err)
					return
				}
				stagePlatform = resolvedStagePlatform
			}

			// if there's another stage with this name before the current stage, that will be used as the source
			namedIndex, hasNamedStage := instructions.HasStage(stages, resolvedBaseStageName.Result)
			if hasNamedStage && namedIndex < i {
				return
			}

			ref, err := dref.ParseAnyReference(resolvedBaseStageName.Result)
			if err != nil {
				if err == reference.ErrObjectRequired {
					return
				}

				errCh <- fmt.Errorf("invalid ref: %s", stage.BaseName)
				return
			}

			clog("[resolver] fetching image...%s", ref.String())

			resolverOpts := sourceresolver.Opt{
				Platform: &stagePlatform,
			}
			resolverOpts.OCILayoutOpt = &sourceresolver.ResolveOCILayoutOpt{
				Store: sourceresolver.ResolveImageConfigOptStore{
					StoreID:   "container",
					SessionID: "",
				},
			}
			resolverOpts.ImageOpt = &sourceresolver.ResolveImageOpt{
				ResolveMode: llb.ResolveModePreferLocal.String(),
			}

			// `resolvedBaseStageName.Result` is the image name as it was specified in the Dockerfile
			// with the build args applied
			// NOTE: DO NOT USE `ref.String()` in the call to ResolveImageConfig
			// `ref`` is the qualified reference, with a default domain
			// In case of local images, where there is no registry, resolution will fail
			// due to the addition of the default domain.
			_, digest, img, err := bopts.Resolver.ResolveImageConfig(ctx, resolvedBaseStageName.Result, resolverOpts)
			if err != nil {
				if err == reference.ErrObjectRequired {
					return
				}
				logrus.Errorf("error resolving image: %v", err)
				errCh <- err
				return
			}
			fqdn := ref.String()
			if _, ok := ref.(dref.Digested); !ok {
				fqdn = fqdn + "@" + digest.String()
			}
			logrus.WithField("ref", fqdn).Infof("creating llb")
			st := llb.OCILayout(fqdn, llb.OCIStore("", "container"), llb.Platform(stagePlatform))

			named, err := dref.ParseNormalizedNamed(ref.String())
			if err != nil {
				errCh <- fmt.Errorf("invalid context name %s %v", ref.String(), err)
				return
			}
			name := strings.TrimSuffix(dref.FamiliarString(named), ":latest")

			// pname constructs a platform-qualified image reference in the format buildkit requires for digest resolution
			pname := name + "::" + platforms.FormatAll(platforms.Normalize(stagePlatform))
			imgMetaMap := map[string][]byte{
				exptypes.ExporterImageConfigKey: img,
			}

			imgMeta, err := json.Marshal(imgMetaMap)
			if err != nil {
				errCh <- err
				return
			}

			stateLock.Lock()
			defer stateLock.Unlock()

			states[pname] = stateMeta{
				state:   st.Platform(stagePlatform),
				imgMeta: imgMeta,
			}
		}(stage)
	}
	go func() { wg.Wait(); doneCh <- struct{}{} }()
	select {
	case err := <-errCh:
		return nil, err
	case <-doneCh:
	}
	return states, nil
}

type frontendClient struct {
	gateway.Client
	frontendOpt    map[string]string
	frontendInputs map[string]*pb.Definition
}

func (fc *frontendClient) BuildOpts() gateway.BuildOpts {
	opts := fc.Client.BuildOpts()

	for k, v := range fc.frontendOpt {
		if _, ok := opts.Opts[k]; !ok {
			opts.Opts[k] = v
			splits := strings.SplitN(k, "::", 2)
			if len(splits) != 2 {
				continue
			}
			opts.Opts[splits[0]] = v
		}
	}

	return opts
}

func (fc *frontendClient) Inputs(ctx context.Context) (map[string]llb.State, error) {
	inputs, err := fc.Client.Inputs(ctx)
	if err != nil {
		return nil, err
	}

	for k, v := range fc.frontendInputs {
		if _, ok := inputs[k]; !ok {
			defOp, err := llb.NewDefinitionOp(v)
			if err != nil {
				return nil, err
			}

			inputs[k] = llb.NewState(defOp.Output())
		}
	}
	return inputs, nil
}

func solvePlatform(ctx context.Context, bopts *BOpts, pl ocispecs.Platform, c gateway.Client, states map[string]stateMeta) (gateway.Reference, []byte, error) {
	capset := pb.Caps.CapSet(utils.Caps().All())
	frontendOpt := map[string]string{
		// In v0.21.0, this was being defaulted to "true"
		// We want to disable it, as it could break fssync
		"local.metadatatransfer": "false",

		// https://github.com/moby/buildkit/pull/5899 introduced a change
		// that ignore apple's xattrs while diffing. This breaks differ
		// breaks due to lack of xattrs, so it is turned off
		"local.differ": "none",
	}

	frontendInputs := map[string]*pb.Definition{}
	for k, v := range states {
		frontendOpt["context:"+k] = "input:" + k
		frontendOpt["input-metadata:"+k] = string(v.imgMeta)

		def, err := v.state.Marshal(ctx)
		if err != nil {
			return nil, nil, err
		}
		frontendInputs[k] = def.ToPB()
	}
	cl, err := dockerui.NewClient(&frontendClient{
		Client:         c,
		frontendInputs: frontendInputs,
		frontendOpt:    frontendOpt,
	})
	if err != nil {
		return nil, nil, err
	}

	convertOpt := dockerfile2llb.ConvertOpt{
		TargetPlatform: &pl,
		MetaResolver:   bopts.Resolver,
		LLBCaps:        &capset,
		Client:         cl,
	}

	convertOpt.BuildPlatforms = bopts.BuildPlatforms
	convertOpt.TargetPlatforms = bopts.Platforms
	convertOpt.BuildArgs = bopts.BuildArgs
	convertOpt.Labels = bopts.Labels
	convertOpt.Target = bopts.Target
	convertOpt.MultiPlatformRequested = true
	convertOpt.ImageResolveMode = llb.ResolveModePreferLocal

	// 3rd return value is a list of SBOMTargets for this Image. Since container
	// doesn't support this feature, we can safely ignore it for now
	state, img, _, _, err := dockerfile2llb.Dockerfile2LLB(ctx, bopts.Dockerfile, convertOpt)
	if err != nil {
		return nil, nil, err
	}

	def, err := state.Marshal(ctx)
	if err != nil {
		return nil, nil, err
	}

	platform, err := state.GetPlatform(ctx)
	if err != nil {
		return nil, nil, err
	}

	if platform == nil {
		platform = &pl
	}

	r, err := c.Solve(ctx, gateway.SolveRequest{
		Evaluate:       false,
		Definition:     def.ToPB(),
		FrontendOpt:    frontendOpt,
		FrontendInputs: frontendInputs,
	})
	if err != nil {
		return nil, nil, err
	}

	ref, err := r.SingleRef()
	if err != nil {
		return nil, nil, err
	}

	// This only happens when the dockerfile is just `FROM scratch`
	if ref == nil {
		return nil, nil, ErrNoBuildDirectives
	}

	_, err = ref.ToState()
	if err != nil {
		return nil, nil, err
	}

	cfgJSON, err := json.Marshal(img)
	if err != nil {
		return nil, nil, err
	}
	return ref, cfgJSON, nil
}

func globalArgs(buildPlatform, targetPlatform ocispecs.Platform, buildArgs map[string]string, target string) utils.MapGetter {
	if target == "" {
		target = "default"
	}
	args := map[string]string{
		"BUILDPLATFORM":   platforms.Format(buildPlatform),
		"BUILDOS":         buildPlatform.OS,
		"BUILDOSVERSION":  buildPlatform.OSVersion,
		"BUILDARCH":       buildPlatform.Architecture,
		"BUILDVARIANT":    buildPlatform.Variant,
		"TARGETPLATFORM":  platforms.FormatAll(targetPlatform),
		"TARGETOS":        targetPlatform.OS,
		"TARGETOSVERSION": targetPlatform.OSVersion,
		"TARGETARCH":      targetPlatform.Architecture,
		"TARGETVARIANT":   targetPlatform.Variant,
		"TARGETSTAGE":     target,
	}
	for k, v := range buildArgs {
		args[k] = v
	}
	return utils.NewMapGetter(args)
}
