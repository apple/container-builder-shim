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

package content

import (
	"context"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	contentx "github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
)

type ctxKey struct{}

var ContentKey ctxKey

// shadow this stage's Request
func (p *ContentStoreProxy) Request(ctx context.Context, req *api.ServerStream, _ string, _ ...stream.FilterByIDFn) (*api.ServerStream, error) {
	bt := req.GetImageTransfer()
	off := bt.GetMetadata()["offset"]
	length := bt.GetMetadata()["length"]
	offInt, _ := strconv.ParseInt(off, 10, 0)
	lenInt, _ := strconv.ParseInt(length, 10, 0)
	if v := ctx.Value(ContentKey); v != nil {
		if ret, ok := v.(*api.ImageTransfer); ok {
			return &api.ServerStream{
				PacketType: &api.ServerStream_ImageTransfer{
					ImageTransfer: ret,
				},
			}, nil
		}
		data := v.([]byte)
		start := offInt
		end := int(math.Min(float64(offInt+lenInt), float64(len(data))))
		return &api.ServerStream{
			PacketType: &api.ServerStream_ImageTransfer{
				ImageTransfer: &api.ImageTransfer{
					Data:     data[start:end],
					Metadata: map[string]string{},
				},
			},
		}, nil
	}
	return &api.ServerStream{
		PacketType: &api.ServerStream_ImageTransfer{
			ImageTransfer: &api.ImageTransfer{
				Data:     []byte{},
				Metadata: map[string]string{},
			},
		},
	}, nil
}

func TestTransformer_RoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	infoIn := &contentx.Info{
		Digest:    digest.Digest("sha256:74434a965d5273cdecf5aee6387b62e4266ac82b1928427a349924aa2103805c"),
		Size:      1234,
		CreatedAt: now,
		UpdatedAt: now.Add(10 * time.Minute),
		Labels: map[string]string{
			"a": "1",
			"b": "2",
		},
	}

	tr := &infoTransformer{}
	wire := tr.TransformFromContentInfo(infoIn)
	infoOut, err := tr.TransformIntoContentInfo(wire)
	if err != nil {
		t.Fatalf("TransformIntoContentInfo err=%v", err)
	}

	infoIn.CreatedAt = infoIn.CreatedAt.Truncate(time.Second)
	infoIn.UpdatedAt = infoIn.UpdatedAt.Truncate(time.Second)

	if !reflect.DeepEqual(infoOut, infoIn) {
		t.Errorf("round-trip mismatch:\n  in  =%+v\n  out =%+v", infoIn, infoOut)
	}
}

func makeSuccessResp(dgst digest.Digest) *api.ImageTransfer {
	now := time.Now().UTC().Truncate(time.Second)
	return (&infoTransformer{}).TransformFromContentInfo(&contentx.Info{
		Digest:    dgst,
		Size:      42,
		CreatedAt: now,
		UpdatedAt: now,
		Labels:    map[string]string{"key": "val"},
	})
}

func TestInfo_Success(t *testing.T) {
	d := digest.Digest("sha256:abcdef")
	ctx := context.WithValue(context.Background(), ContentKey, makeSuccessResp(d))

	cs := &ContentStoreProxy{}
	got, err := cs.Info(ctx, d)
	if err != nil {
		t.Fatalf("Info returned err=%v", err)
	}
	if got.Digest != d || got.Size != 42 || got.Labels["key"] != "val" {
		t.Errorf("unexpected Info: %+v", got)
	}
}

func TestInfo_ServerError(t *testing.T) {
	resp := &api.ImageTransfer{Metadata: map[string]string{"error": "boom"}}
	ctx := context.WithValue(context.Background(), ContentKey, resp)

	cs := &ContentStoreProxy{}
	if _, err := cs.Info(ctx, digest.Digest("sha256:bad")); err == nil {
		t.Fatal("Info returned nil error, want propagated server error")
	}
}

func TestTransformSize_Errors(t *testing.T) {
	tr := &infoTransformer{}
	_, err := tr.TransformSize(map[string]string{"size": "not-int"})
	if err == nil {
		t.Fatal("expected error from invalid size")
	}
}

func TestTransformTimestamps_Empty(t *testing.T) {
	tr := &infoTransformer{}
	if tm, _ := tr.TransformCreatedTimestamp(map[string]string{}); !tm.IsZero() {
		t.Errorf("empty created_at should yield zero Time, got %v", tm)
	}
	if tm, _ := tr.TransformUpdatedTimestamp(map[string]string{}); !tm.IsZero() {
		t.Errorf("empty updated_at should yield zero Time, got %v", tm)
	}
}

func TestTransformLabels(t *testing.T) {
	tr := &infoTransformer{}
	meta := map[string]string{
		"__label:foo": "bar",
		"unrelated":   "x",
	}
	want := map[string]string{"foo": "bar"}
	if got := tr.TransformLabels(meta); !reflect.DeepEqual(got, want) {
		t.Errorf("TransformLabels=%v, want %v", got, want)
	}
}

func TestTransformIntoContentInfo_BadTimestamp(t *testing.T) {
	tr := &infoTransformer{}
	_, err := tr.TransformIntoContentInfo(&api.ImageTransfer{
		Metadata: map[string]string{
			"size":       "1",
			"created_at": "bad-time",
			"updated_at": strconv.FormatInt(time.Now().Unix(), 10),
		},
	})
	if err == nil {
		t.Fatal("expected error from bad timestamp")
	}
}
