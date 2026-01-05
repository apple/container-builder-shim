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
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/containerd/platforms"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/resolver"
	"github.com/apple/container-builder-shim/pkg/stream"
)

// testMockStream is an in-memory implementation of stream.Stream for testing
type testMockStream struct {
	sendCh chan *api.ServerStream
	recvCh chan *api.ClientStream
	ctx    context.Context
}

func newTestMockStream(ctx context.Context) *testMockStream {
	return &testMockStream{
		sendCh: make(chan *api.ServerStream, 10),
		recvCh: make(chan *api.ClientStream, 10),
		ctx:    ctx,
	}
}

func (m *testMockStream) Send(s *api.ServerStream) error {
	select {
	case m.sendCh <- s:
		return nil
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
}

func (m *testMockStream) Recv() (*api.ClientStream, error) {
	select {
	case c := <-m.recvCh:
		return c, nil
	case <-m.ctx.Done():
		return nil, m.ctx.Err()
	}
}

func (m *testMockStream) Context() context.Context {
	return m.ctx
}

// testInterceptingResolver captures resolver calls and provides mock responses
type testInterceptingResolver struct {
	*resolver.ResolverProxy
	calls []testResolverCall
}

type testResolverCall struct {
	ref      string
	platform *ocispecs.Platform
}

func (r *testInterceptingResolver) captureCall(ref string, platform *ocispecs.Platform) {
	call := testResolverCall{
		ref:      ref,
		platform: platform,
	}
	r.calls = append(r.calls, call)
}

// handleResolverRequests processes resolver requests and captures calls
func (m *testMockStream) handleResolverRequests(interceptor *testInterceptingResolver) {
	go func() {
		for {
			select {
			case req := <-m.sendCh:
				if imageTransfer := req.GetImageTransfer(); imageTransfer != nil {
					ref := imageTransfer.Metadata["ref"]
					platformStr := imageTransfer.Metadata["platform"]

					// Parse and capture the platform information
					platform := platforms.MustParse(platformStr)
					interceptor.captureCall(ref, &platform)

					// Create mock response
					mockDigest := digest.FromString("mock-digest-for-" + ref)
					response := &api.ImageTransfer{
						Id:        imageTransfer.Id,
						Direction: api.TransferDirection_INTO,
						Tag:       string(mockDigest),
						Metadata:  imageTransfer.Metadata,
						Data:      []byte(`{"config":{"Env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]}}`),
					}

					// Send response back
					clientResponse := &api.ClientStream{
						BuildId:    req.BuildId,
						PacketType: &api.ClientStream_ImageTransfer{ImageTransfer: response},
					}

					select {
					case m.recvCh <- clientResponse:
					case <-m.ctx.Done():
						return
					}
				}
			case <-m.ctx.Done():
				return
			}
		}
	}()
}

func newTestInterceptingResolver(ctx context.Context) (*testInterceptingResolver, func(), error) {
	// Create mock stream
	mockStr := newTestMockStream(ctx)

	// Create resolver
	resolver := resolver.NewResolverProxy()
	interceptor := &testInterceptingResolver{
		ResolverProxy: resolver,
		calls:         make([]testResolverCall, 0),
	}

	// Create pipeline with the resolver as a stage
	pipeline, err := stream.NewPipeline(ctx, mockStr, resolver)
	if err != nil {
		return nil, nil, err
	}

	// Start handling requests
	mockStr.handleResolverRequests(interceptor)

	// Start pipeline in background
	go func() {
		pipeline.Run()
	}()

	cleanup := func() {
		// Pipeline will be cleaned up when context is cancelled
	}

	return interceptor, cleanup, nil
}

func TestGlobalArgs(t *testing.T) {
	tests := []struct {
		name           string
		buildPlatform  ocispecs.Platform
		targetPlatform ocispecs.Platform
		buildArgs      map[string]string
		target         string
		want           map[string]string
	}{
		{
			name: "basic platforms with no build args",
			buildPlatform: ocispecs.Platform{
				OS:           "linux",
				Architecture: "amd64",
			},
			targetPlatform: ocispecs.Platform{
				OS:           "linux",
				Architecture: "arm64",
			},
			buildArgs: map[string]string{},
			target:    "production",
			want: map[string]string{
				"BUILDPLATFORM":   "linux/amd64",
				"BUILDOS":         "linux",
				"BUILDOSVERSION":  "",
				"BUILDARCH":       "amd64",
				"BUILDVARIANT":    "",
				"TARGETPLATFORM":  "linux/arm64",
				"TARGETOS":        "linux",
				"TARGETOSVERSION": "",
				"TARGETARCH":      "arm64",
				"TARGETVARIANT":   "",
				"TARGETSTAGE":     "production",
			},
		},
		{
			name: "empty target defaults to 'default'",
			buildPlatform: ocispecs.Platform{
				OS:           "linux",
				Architecture: "amd64",
			},
			targetPlatform: ocispecs.Platform{
				OS:           "linux",
				Architecture: "amd64",
			},
			buildArgs: map[string]string{},
			target:    "",
			want: map[string]string{
				"BUILDPLATFORM":   "linux/amd64",
				"BUILDOS":         "linux",
				"BUILDOSVERSION":  "",
				"BUILDARCH":       "amd64",
				"BUILDVARIANT":    "",
				"TARGETPLATFORM":  "linux/amd64",
				"TARGETOS":        "linux",
				"TARGETOSVERSION": "",
				"TARGETARCH":      "amd64",
				"TARGETVARIANT":   "",
				"TARGETSTAGE":     "default",
			},
		},
		{
			name: "platforms with version and variant",
			buildPlatform: ocispecs.Platform{
				OS:           "windows",
				OSVersion:    "10.0.19041",
				Architecture: "amd64",
			},
			targetPlatform: ocispecs.Platform{
				OS:           "linux",
				Architecture: "arm",
				Variant:      "v7",
			},
			buildArgs: map[string]string{},
			target:    "test",
			want: map[string]string{
				"BUILDPLATFORM":   "windows/amd64",
				"BUILDOS":         "windows",
				"BUILDOSVERSION":  "10.0.19041",
				"BUILDARCH":       "amd64",
				"BUILDVARIANT":    "",
				"TARGETPLATFORM":  "linux/arm/v7",
				"TARGETOS":        "linux",
				"TARGETOSVERSION": "",
				"TARGETARCH":      "arm",
				"TARGETVARIANT":   "v7",
				"TARGETSTAGE":     "test",
			},
		},
		{
			name: "build args override platform args",
			buildPlatform: ocispecs.Platform{
				OS:           "linux",
				Architecture: "amd64",
			},
			targetPlatform: ocispecs.Platform{
				OS:           "linux",
				Architecture: "arm64",
			},
			buildArgs: map[string]string{
				"BUILDARCH":   "overridden",
				"CUSTOM_ARG":  "custom_value",
				"TARGETSTAGE": "custom_stage",
			},
			target: "production",
			want: map[string]string{
				"BUILDPLATFORM":   "linux/amd64",
				"BUILDOS":         "linux",
				"BUILDOSVERSION":  "",
				"BUILDARCH":       "overridden", // Overridden by build args
				"BUILDVARIANT":    "",
				"TARGETPLATFORM":  "linux/arm64",
				"TARGETOS":        "linux",
				"TARGETOSVERSION": "",
				"TARGETARCH":      "arm64",
				"TARGETVARIANT":   "",
				"TARGETSTAGE":     "custom_stage", // Overridden by build args
				"CUSTOM_ARG":      "custom_value", // Custom build arg
			},
		},
		{
			name: "darwin platform",
			buildPlatform: ocispecs.Platform{
				OS:           "darwin",
				Architecture: "arm64",
			},
			targetPlatform: ocispecs.Platform{
				OS:           "darwin",
				Architecture: "amd64",
			},
			buildArgs: map[string]string{},
			target:    "release",
			want: map[string]string{
				"BUILDPLATFORM":   "darwin/arm64",
				"BUILDOS":         "darwin",
				"BUILDOSVERSION":  "",
				"BUILDARCH":       "arm64",
				"BUILDVARIANT":    "",
				"TARGETPLATFORM":  "darwin/amd64",
				"TARGETOS":        "darwin",
				"TARGETOSVERSION": "",
				"TARGETARCH":      "amd64",
				"TARGETVARIANT":   "",
				"TARGETSTAGE":     "release",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := globalArgs(tt.buildPlatform, tt.targetPlatform, tt.buildArgs, tt.target)

			// Convert to map for comparison
			gotMap := make(map[string]string)
			for _, key := range got.Keys() {
				value, found := got.Get(key)
				if found {
					gotMap[key] = value
				}
			}

			if !reflect.DeepEqual(gotMap, tt.want) {
				t.Errorf("globalArgs() = %v, want %v", gotMap, tt.want)
			}

			// Verify that all expected keys are present
			for wantKey, wantValue := range tt.want {
				gotValue, found := got.Get(wantKey)
				if !found {
					t.Errorf("globalArgs() missing key %q", wantKey)
				} else if gotValue != wantValue {
					t.Errorf("globalArgs() key %q = %q, want %q", wantKey, gotValue, wantValue)
				}
			}

			// Verify that no unexpected keys are present
			for _, gotKey := range got.Keys() {
				if _, expected := tt.want[gotKey]; !expected {
					t.Errorf("globalArgs() unexpected key %q", gotKey)
				}
			}
		})
	}
}

func TestResolveStates(t *testing.T) {
	tests := []struct {
		name                  string
		dockerfile            string
		buildPlatforms        []ocispecs.Platform
		targetPlatform        ocispecs.Platform
		buildArgs             map[string]string
		target                string
		expectedResolverCalls []testResolverCall
		expectedStates        int
		wantErr               bool
		errContains           string
	}{
		// Error cases
		{
			name: "invalid dockerfile syntax should error",
			dockerfile: `INVALID INSTRUCTION
FROM alpine:latest`,
			buildPlatforms:        []ocispecs.Platform{{OS: "linux", Architecture: "amd64"}},
			targetPlatform:        ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:             map[string]string{},
			target:                "",
			expectedResolverCalls: []testResolverCall{},
			expectedStates:        0,
			wantErr:               true,
			errContains:           "",
		},
		{
			name: "FROM with invalid platform should error",
			dockerfile: `FROM --platform=not-a-valid-platform alpine:latest
RUN echo "hello"`,
			buildPlatforms:        []ocispecs.Platform{{OS: "linux", Architecture: "amd64"}},
			targetPlatform:        ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:             map[string]string{},
			target:                "",
			expectedResolverCalls: []testResolverCall{},
			expectedStates:        0,
			wantErr:               true,
			errContains:           "invalid platform",
		},
		{
			name: "FROM with invalid platform from build arg should error",
			dockerfile: `FROM --platform=$BAD_PLATFORM alpine:latest
RUN echo "hello"`,
			buildPlatforms:        []ocispecs.Platform{{OS: "linux", Architecture: "amd64"}},
			targetPlatform:        ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:             map[string]string{"BAD_PLATFORM": "not-a-valid-platform"},
			target:                "",
			expectedResolverCalls: []testResolverCall{},
			expectedStates:        0,
			wantErr:               true,
			errContains:           "invalid platform",
		},
		// Scratch/context cases (no resolver calls)
		{
			name: "FROM scratch with platform should be skipped",
			dockerfile: `FROM --platform=linux/arm64 scratch
COPY app /app`,
			buildPlatforms:        []ocispecs.Platform{{OS: "linux", Architecture: "amd64"}},
			targetPlatform:        ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:             map[string]string{},
			target:                "",
			expectedResolverCalls: []testResolverCall{}, // No resolver calls for scratch
			expectedStates:        0,
			wantErr:               false,
			errContains:           "",
		},
		{
			name: "FROM context with platform variables",
			dockerfile: `FROM --platform=$BUILDPLATFORM context
COPY app /app`,
			buildPlatforms:        []ocispecs.Platform{{OS: "darwin", Architecture: "arm64"}},
			targetPlatform:        ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:             map[string]string{},
			target:                "",
			expectedResolverCalls: []testResolverCall{}, // No resolver calls for context
			expectedStates:        0,
			wantErr:               false,
			errContains:           "",
		},
		{
			name: "FROM with build args overriding platform vars (context)",
			dockerfile: `FROM --platform=$CUSTOM_PLATFORM context
COPY app /app`,
			buildPlatforms:        []ocispecs.Platform{{OS: "linux", Architecture: "amd64"}},
			targetPlatform:        ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:             map[string]string{"CUSTOM_PLATFORM": "windows/amd64"},
			target:                "",
			expectedResolverCalls: []testResolverCall{}, // No resolver calls for context
			expectedStates:        0,
			wantErr:               false,
			errContains:           "",
		},
		{
			name: "Multi-stage with scratch and context",
			dockerfile: `FROM --platform=$BUILDPLATFORM scratch AS build
COPY app /build

FROM --platform=$TARGETPLATFORM context
COPY --from=build /build /app`,
			buildPlatforms:        []ocispecs.Platform{{OS: "darwin", Architecture: "arm64"}},
			targetPlatform:        ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:             map[string]string{},
			target:                "",
			expectedResolverCalls: []testResolverCall{}, // No resolver calls for scratch/context
			expectedStates:        0,
			wantErr:               false,
			errContains:           "",
		},
		// Real image resolution cases
		{
			name: "FROM alpine with TARGETPLATFORM",
			dockerfile: `FROM --platform=$TARGETPLATFORM alpine:latest
RUN echo "hello"`,
			buildPlatforms: []ocispecs.Platform{{OS: "linux", Architecture: "amd64"}},
			targetPlatform: ocispecs.Platform{OS: "linux", Architecture: "arm64"},
			buildArgs:      map[string]string{},
			target:         "",
			expectedResolverCalls: []testResolverCall{
				{
					ref:      "alpine:latest",
					platform: &ocispecs.Platform{OS: "linux", Architecture: "arm64"},
				},
			},
			expectedStates: 1,
			wantErr:        false,
		},
		{
			name: "FROM nginx with BUILDPLATFORM",
			dockerfile: `FROM --platform=$BUILDPLATFORM nginx:alpine
RUN echo "hello"`,
			buildPlatforms: []ocispecs.Platform{{OS: "darwin", Architecture: "arm64"}},
			targetPlatform: ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:      map[string]string{},
			target:         "",
			expectedResolverCalls: []testResolverCall{
				{
					ref:      "nginx:alpine",
					platform: &ocispecs.Platform{OS: "darwin", Architecture: "arm64"},
				},
			},
			expectedStates: 1,
			wantErr:        false,
		},
		{
			name: "FROM with build args overriding platform vars",
			dockerfile: `FROM --platform=$CUSTOM_PLATFORM redis:alpine
RUN echo "hello"`,
			buildPlatforms: []ocispecs.Platform{
				{OS: "linux", Architecture: "amd64"},
				{OS: "linux", Architecture: "arm64"},
			},
			targetPlatform: ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:      map[string]string{"CUSTOM_PLATFORM": "windows/amd64"},
			target:         "",
			expectedResolverCalls: []testResolverCall{
				{
					ref:      "redis:alpine",
					platform: &ocispecs.Platform{OS: "windows", Architecture: "amd64"},
				},
			},
			expectedStates: 1,
			wantErr:        false,
		},
		{
			name: "FROM with platform variant",
			dockerfile: `FROM --platform=linux/arm/v7 node:18-alpine
RUN npm install`,
			buildPlatforms: []ocispecs.Platform{
				{OS: "linux", Architecture: "amd64"},
				{OS: "darwin", Architecture: "arm64"},
			},
			targetPlatform: ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:      map[string]string{},
			target:         "",
			expectedResolverCalls: []testResolverCall{
				{
					ref:      "node:18-alpine",
					platform: &ocispecs.Platform{OS: "linux", Architecture: "arm", Variant: "v7"},
				},
			},
			expectedStates: 1,
			wantErr:        false,
		},
		{
			name: "FROM with image name from build arg",
			dockerfile: `ARG BASE_IMAGE=postgres:13
FROM --platform=$TARGETPLATFORM $BASE_IMAGE
RUN echo "hello"`,
			buildPlatforms: []ocispecs.Platform{{OS: "linux", Architecture: "amd64"}},
			targetPlatform: ocispecs.Platform{OS: "linux", Architecture: "arm64"},
			buildArgs:      map[string]string{"BASE_IMAGE": "mysql:8.0"},
			target:         "",
			expectedResolverCalls: []testResolverCall{
				{
					ref:      "mysql:8.0", // Should use the build arg value, not the default
					platform: &ocispecs.Platform{OS: "linux", Architecture: "arm64"},
				},
			},
			expectedStates: 1,
			wantErr:        false,
		},
		{
			name: "Multi-stage with different platforms",
			dockerfile: `FROM --platform=$BUILDPLATFORM golang:1.19 AS builder
RUN go build -o app main.go

FROM --platform=$TARGETPLATFORM alpine:latest
COPY --from=builder /app /app`,
			buildPlatforms: []ocispecs.Platform{{OS: "darwin", Architecture: "arm64"}},
			targetPlatform: ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:      map[string]string{},
			target:         "",
			expectedResolverCalls: []testResolverCall{
				{
					ref:      "golang:1.19",
					platform: &ocispecs.Platform{OS: "darwin", Architecture: "arm64"}, // Should use BUILDPLATFORM
				},
				{
					ref:      "alpine:latest",
					platform: &ocispecs.Platform{OS: "linux", Architecture: "amd64"}, // Should use TARGETPLATFORM
				},
			},
			expectedStates: 2,
			wantErr:        false,
		},
		{
			name: "Multiple build platforms - uses first",
			dockerfile: `FROM --platform=$BUILDPLATFORM alpine:latest
RUN echo "hello"`,
			buildPlatforms: []ocispecs.Platform{
				{OS: "linux", Architecture: "amd64"},
				{OS: "linux", Architecture: "arm64"},
			},
			targetPlatform: ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:      map[string]string{},
			target:         "",
			expectedResolverCalls: []testResolverCall{
				{
					ref:      "alpine:latest",
					platform: &ocispecs.Platform{OS: "linux", Architecture: "amd64"}, // Should use first BUILDPLATFORM
				},
			},
			expectedStates: 1,
			wantErr:        false,
		},
		{
			name: "Multiple build platforms with cross-compilation",
			dockerfile: `FROM --platform=$BUILDPLATFORM golang:1.19 AS builder
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o app main.go

FROM --platform=$TARGETPLATFORM alpine:latest
COPY --from=builder /app /app`,
			buildPlatforms: []ocispecs.Platform{
				{OS: "linux", Architecture: "amd64"},
				{OS: "darwin", Architecture: "arm64"},
				{OS: "windows", Architecture: "amd64"},
			},
			targetPlatform: ocispecs.Platform{OS: "linux", Architecture: "arm64"},
			buildArgs:      map[string]string{},
			target:         "",
			expectedResolverCalls: []testResolverCall{
				{
					ref:      "golang:1.19",
					platform: &ocispecs.Platform{OS: "linux", Architecture: "amd64"}, // Should use first BUILDPLATFORM
				},
				{
					ref:      "alpine:latest",
					platform: &ocispecs.Platform{OS: "linux", Architecture: "arm64"}, // Should use TARGETPLATFORM
				},
			},
			expectedStates: 2,
			wantErr:        false,
		},
		// Environment-only Dockerfile tests
		{
			name: "FROM scratch with only ENV directives",
			dockerfile: `FROM scratch

ARG BUILD_DATE
ENV TERM=xterm \
    BUILD_DATE=${BUILD_DATE}`,
			buildPlatforms:        []ocispecs.Platform{{OS: "linux", Architecture: "arm64"}},
			targetPlatform:        ocispecs.Platform{OS: "linux", Architecture: "arm64"},
			buildArgs:             map[string]string{"BUILD_DATE": "2025-01-01"},
			target:                "",
			expectedResolverCalls: []testResolverCall{}, // No resolver calls for scratch
			expectedStates:        0,
			wantErr:               false,
			errContains:           "",
		},
		{
			name: "FROM alpine with only ENV and LABEL",
			dockerfile: `FROM alpine:latest
ENV APP_VERSION=1.0.0 \
    APP_NAME=myapp
LABEL maintainer="test@example.com" \
      version="1.0.0"`,
			buildPlatforms: []ocispecs.Platform{{OS: "linux", Architecture: "arm64"}},
			targetPlatform: ocispecs.Platform{OS: "linux", Architecture: "arm64"},
			buildArgs:      map[string]string{},
			target:         "",
			expectedResolverCalls: []testResolverCall{
				{ref: "alpine:latest", platform: &ocispecs.Platform{OS: "linux", Architecture: "arm64"}},
			},
			expectedStates: 1,
			wantErr:        false,
			errContains:    "",
		},
		{
			name: "Complex ARG and ENV combinations",
			dockerfile: `FROM scratch
ARG JOBS=6
ARG ARCH=amd64
ENV MAKEOPTS="-j${JOBS}" \
    ARCH="${ARCH}" \
    PATH="/usr/local/bin:/usr/bin"`,
			buildPlatforms:        []ocispecs.Platform{{OS: "linux", Architecture: "arm64"}},
			targetPlatform:        ocispecs.Platform{OS: "linux", Architecture: "arm64"},
			buildArgs:             map[string]string{"JOBS": "8", "ARCH": "arm64"},
			target:                "",
			expectedResolverCalls: []testResolverCall{},
			expectedStates:        0,
			wantErr:               false,
			errContains:           "",
		},
		{
			name: "FROM scratch with only LABEL directives",
			dockerfile: `FROM scratch
LABEL maintainer="test@example.com" \
      version="1.0.0" \
      description="Test image"`,
			buildPlatforms:        []ocispecs.Platform{{OS: "linux", Architecture: "arm64"}},
			targetPlatform:        ocispecs.Platform{OS: "linux", Architecture: "arm64"},
			buildArgs:             map[string]string{},
			target:                "",
			expectedResolverCalls: []testResolverCall{},
			expectedStates:        0,
			wantErr:               false,
			errContains:           "",
		},
		{
			name: "FROM debian with ENV ARG and LABEL mix",
			dockerfile: `FROM debian:bullseye
ARG VERSION=1.0.0
ARG MAINTAINER=devops@example.com
ENV APP_VERSION=${VERSION} \
    PATH=/app/bin:$PATH
LABEL maintainer="${MAINTAINER}" \
      version="${VERSION}"`,
			buildPlatforms: []ocispecs.Platform{{OS: "linux", Architecture: "amd64"}},
			targetPlatform: ocispecs.Platform{OS: "linux", Architecture: "amd64"},
			buildArgs:      map[string]string{"VERSION": "2.0.0", "MAINTAINER": "ops@test.com"},
			target:         "",
			expectedResolverCalls: []testResolverCall{
				{ref: "debian:bullseye", platform: &ocispecs.Platform{OS: "linux", Architecture: "amd64"}},
			},
			expectedStates: 1,
			wantErr:        false,
			errContains:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Create intercepting resolver
			interceptor, cleanup, err := newTestInterceptingResolver(ctx)
			if err != nil {
				t.Fatalf("Failed to create intercepting resolver: %v", err)
			}
			defer cleanup()

			// Create BOpts with the intercepting resolver
			bopts := &BOpts{
				Dockerfile:     []byte(tt.dockerfile),
				BuildPlatforms: tt.buildPlatforms,
				BuildArgs:      tt.buildArgs,
				Target:         tt.target,
				Resolver:       interceptor.ResolverProxy,
			}

			clog := func(format string, params ...any) {
				// No-op for tests
			}

			// Call resolveStates
			states, err := resolveStates(ctx, bopts, tt.targetPlatform, clog)

			if tt.wantErr {
				if err == nil {
					t.Errorf("resolveStates() expected error, got nil")
					return
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("resolveStates() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Errorf("resolveStates() unexpected error = %v", err)
				return
			}

			// Check number of states
			if len(states) != tt.expectedStates {
				t.Errorf("resolveStates() got %d states, want %d states", len(states), tt.expectedStates)
			}

			// Check resolver calls
			if len(interceptor.calls) != len(tt.expectedResolverCalls) {
				t.Errorf("resolver called %d times, expected %d calls", len(interceptor.calls), len(tt.expectedResolverCalls))
				t.Logf("Actual calls: %+v", interceptor.calls)
				t.Logf("Expected calls: %+v", tt.expectedResolverCalls)
				return
			}

			// Verify resolver calls (order-agnostic for multi-stage builds)
			expectedCallsMap := make(map[string]testResolverCall)
			for _, expectedCall := range tt.expectedResolverCalls {
				expectedCallsMap[expectedCall.ref] = expectedCall
			}

			for i, actualCall := range interceptor.calls {
				expectedCall, found := expectedCallsMap[actualCall.ref]
				if !found {
					t.Errorf("resolver call %d: unexpected ref %q", i, actualCall.ref)
					continue
				}

				if actualCall.platform == nil && expectedCall.platform != nil {
					t.Errorf("resolver call %d (%s): got nil platform, want %+v", i, actualCall.ref, expectedCall.platform)
					continue
				}
				if actualCall.platform != nil && expectedCall.platform == nil {
					t.Errorf("resolver call %d (%s): got platform %+v, want nil", i, actualCall.ref, actualCall.platform)
					continue
				}
				if actualCall.platform != nil && expectedCall.platform != nil {
					matcher := platforms.NewMatcher(*expectedCall.platform)
					if !matcher.Match(*actualCall.platform) {
						t.Errorf("resolver call %d (%s): got platform %+v, want %+v", i, actualCall.ref, actualCall.platform, expectedCall.platform)
					}
				}
				// Remove from expected map to ensure we don't match it twice
				delete(expectedCallsMap, actualCall.ref)
			}

			// Check if all expected calls were matched
			for ref := range expectedCallsMap {
				t.Errorf("Expected resolver call for %q was not found", ref)
			}

			// Log successful verification
			t.Logf("Successfully intercepted %d resolver calls:", len(interceptor.calls))
			for i, call := range interceptor.calls {
				platformStr := call.platform.OS + "/" + call.platform.Architecture
				if call.platform.Variant != "" {
					platformStr += "/" + call.platform.Variant
				}
				t.Logf("  Call %d: %s -> platform=%s", i+1, call.ref, platformStr)
			}
		})
	}
}
