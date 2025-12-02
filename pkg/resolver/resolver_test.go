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

package resolver

import (
	"context"
	"testing"

	"github.com/containerd/containerd/platforms"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/opencontainers/go-digest"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
)

var RespKey = struct{}{}

// shadow this stage's Request
func (p *ResolverProxy) Request(ctx context.Context, _ *api.ServerStream, _ string, _ ...stream.FilterByIDFn) (*api.ServerStream, error) {
	if v := ctx.Value(RespKey); v != nil {
		return v.(*api.ServerStream), nil
	}
	return &api.ServerStream{}, nil
}

func ctxWithResp(resp *api.ServerStream) context.Context {
	return context.WithValue(context.Background(), RespKey, resp)
}

func newImageTransferClientStream(it *api.ImageTransfer) *api.ClientStream {
	return &api.ClientStream{
		PacketType: &api.ClientStream_ImageTransfer{ImageTransfer: it},
	}
}

func TestResolverProxy_Filter_MatchingStage(t *testing.T) {
	proxy := NewResolverProxy()
	it := &api.ImageTransfer{Metadata: map[string]string{"stage": proxy.String()}}
	if err := proxy.Filter(newImageTransferClientStream(it)); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestResolverProxy_Filter_NotMatchingStage(t *testing.T) {
	proxy := NewResolverProxy()
	it := &api.ImageTransfer{Metadata: map[string]string{"stage": "other"}}
	if err := proxy.Filter(newImageTransferClientStream(it)); err != stream.ErrIgnorePacket {
		t.Fatalf("expected ErrIgnorePacket, got %v", err)
	}
}

func TestResolverProxy_ResolveImageConfig_Success(t *testing.T) {
	proxy := NewResolverProxy()

	wantDigest := digest.FromString("dummy")
	imageResp := &api.ImageTransfer{
		Tag:      string(wantDigest),
		Metadata: map[string]string{"stage": proxy.String()},
		Data:     []byte("{\"config\":true}"),
	}

	respStream := &api.ServerStream{PacketType: &api.ServerStream_ImageTransfer{ImageTransfer: imageResp}}
	ctx := ctxWithResp(respStream)

	ref := "docker.io/library/alpine:latest"
	plt := platforms.DefaultSpec()
	_, gotDigest, gotData, err := proxy.ResolveImageConfig(ctx, ref, sourceresolver.Opt{ImageOpt: &sourceresolver.ResolveImageOpt{Platform: &plt}})
	if err != nil {
		t.Fatalf("ResolveImageConfig returned error: %v", err)
	}
	if gotDigest != wantDigest {
		t.Fatalf("unexpected digest: want %s, got %s", wantDigest, gotDigest)
	}
	if string(gotData) != string(imageResp.Data) {
		t.Fatalf("unexpected data: want %q, got %q", imageResp.Data, gotData)
	}
}

func TestResolverProxy_ResolveImageConfig_ErrorMetadata(t *testing.T) {
	proxy := NewResolverProxy()
	imageResp := &api.ImageTransfer{Metadata: map[string]string{"error": "image not found"}}
	respStream := &api.ServerStream{PacketType: &api.ServerStream_ImageTransfer{ImageTransfer: imageResp}}
	ctx := ctxWithResp(respStream)

	ref := "docker.io/library/alpine:latest"
	plt := platforms.DefaultSpec()
	_, _, _, err := proxy.ResolveImageConfig(ctx, ref, sourceresolver.Opt{ImageOpt: &sourceresolver.ResolveImageOpt{Platform: &plt}})
	if err == nil {
		t.Fatalf("expected error from metadata, got nil")
	}
}
