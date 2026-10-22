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

package fssync

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	gofs "io/fs"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
	"github.com/moby/patternmatcher"
	"google.golang.org/grpc/metadata"
)

func (p *FSSyncProxy) RegisterDemux(id string, d *stream.Demultiplexer) {
	demuxes[id] = d
}

var (
	demuxes = map[string]*stream.Demultiplexer{}
	headers = map[string][]byte{}
)

func makeNestedTarHeaderAndBody() (checksum string, full []byte) {
	const payload = "hello from tar with nesting\n"

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	dirHeader := &tar.Header{
		Name:     "dir",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
		ModTime:  time.Time{},
	}
	_ = tw.WriteHeader(dirHeader)

	fileHeader := &tar.Header{
		Name:     "dir/foo",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len(payload)),
		ModTime:  time.Time{},
	}
	_ = tw.WriteHeader(fileHeader)
	_, _ = tw.Write([]byte(payload))

	_ = tw.Close()

	full = buf.Bytes()
	header := sha256.Sum256(full)
	return hex.EncodeToString(header[:]), full
}

func (p *FSSyncProxy) Send(s *api.ServerStream) error {
	id := s.BuildId
	d := demuxes[id]
	checksum, full := makeNestedTarHeaderAndBody()
	go func() {
		_ = d.Accept(&api.ClientStream{
			BuildId: id,
			PacketType: &api.ClientStream_BuildTransfer{
				BuildTransfer: &api.BuildTransfer{
					Id:        id,
					Direction: api.TransferDirection_INTO,
					Metadata:  map[string]string{"hash": checksum},
				},
			},
		})

		_ = d.Accept(&api.ClientStream{
			BuildId: id,
			PacketType: &api.ClientStream_BuildTransfer{
				BuildTransfer: &api.BuildTransfer{
					Id:        id,
					Direction: api.TransferDirection_INTO,
					Data:      full,
				},
			},
		})

		_ = d.Accept(&api.ClientStream{
			BuildId: id,
			PacketType: &api.ClientStream_BuildTransfer{
				BuildTransfer: &api.BuildTransfer{
					Id:        id,
					Direction: api.TransferDirection_INTO,
					Complete:  true,
				},
			},
		})
	}()
	return nil
}

func TestUnmarshalWalkMetadata_Defaults(t *testing.T) {
	md, err := unmarshalWalkMetadata(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if md.Mode != ModeTAR {
		t.Fatalf("default mode = %q, want %q", md.Mode, ModeTAR)
	}
}

func TestUnmarshalWalkMetadata_InvalidMode(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("mode", "json"))
	_, err := unmarshalWalkMetadata(ctx)
	if err == nil {
		t.Fatal("expected error for unsupported mode 'json', got nil")
	}
}

// BuildKit query-escapes all values of a key once any of them is non-ASCII.
func TestUnmarshalWalkMetadata_DecodesEncodedValues(t *testing.T) {
	excludes := []string{"ignored/*", "report-日本語.txt"}
	md := metadata.MD{"exclude-patterns-encoded": {"1"}}
	for _, e := range excludes {
		md["exclude-patterns"] = append(md["exclude-patterns"], url.QueryEscape(e))
	}
	md["followpaths"] = []string{url.QueryEscape("café"), url.QueryEscape("src")}
	md["followpaths-encoded"] = []string{"1"}

	got, err := unmarshalWalkMetadata(metadata.NewIncomingContext(context.Background(), md))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if want := "ignored/*,report-日本語.txt"; got.ExcludedPatterns != want {
		t.Errorf("ExcludedPatterns = %q, want %q", got.ExcludedPatterns, want)
	}
	if want := "café,src"; got.FollowPaths != want {
		t.Errorf("FollowPaths = %q, want %q", got.FollowPaths, want)
	}

	matcher, err := patternmatcher.New(strings.Split(got.ExcludedPatterns, ","))
	if err != nil {
		t.Fatalf("patternmatcher.New: %v", err)
	}
	for _, path := range []string{"ignored/file.txt", "report-日本語.txt"} {
		excluded, err := matcher.MatchesOrParentMatches(path)
		if err != nil {
			t.Fatalf("MatchesOrParentMatches(%q): %v", path, err)
		}
		if !excluded {
			t.Errorf("%q is not excluded", path)
		}
	}
}

func TestUnmarshalWalkMetadata_KeepsUnencodedValues(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("exclude-patterns", "100%+done"))
	got, err := unmarshalWalkMetadata(ctx)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if want := "100%+done"; got.ExcludedPatterns != want {
		t.Errorf("ExcludedPatterns = %q, want %q", got.ExcludedPatterns, want)
	}
}

func TestUnmarshalWalkMetadata_InvalidEncoding(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"exclude-patterns", "%zz",
		"exclude-patterns-encoded", "1",
	))
	if _, err := unmarshalWalkMetadata(ctx); err == nil {
		t.Fatal("expected error for invalid encoding, got nil")
	}
}

func TestUnmarshalWalkMetadata_InvalidEncodedFlag(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"exclude-patterns", url.QueryEscape("ignored/*"),
		"exclude-patterns-encoded", "yes",
	))
	if _, err := unmarshalWalkMetadata(ctx); err == nil {
		t.Fatal("expected error for invalid encoded flag, got nil")
	}
}

func TestWalk_UnsupportedMode(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("mode", "json"))
	fs := NewFS(ctx, &FSSyncProxy{}, "/", t.TempDir()) // proxy never used
	var fn gofs.WalkDirFunc = func(string, gofs.DirEntry, error) error { return nil }
	err := fs.Walk(ctx, "", fn)
	if err == nil {
		t.Fatal("Walk returned nil error, want unsupported-mode error")
	}
}

func TestWalk_TarModeSuccess(t *testing.T) {
	tmp := t.TempDir()

	_, full := makeNestedTarHeaderAndBody()

	fs := NewFS(context.Background(), &FSSyncProxy{}, "/", tmp)

	var walked []string
	err := fs.Walk(context.Background(), "", func(path string, _ gofs.DirEntry, _ error) error {
		walked = append(walked, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk returned err=%v", err)
	}
	fsSum := sha256.Sum256(full)
	fsChecksum := hex.EncodeToString(fsSum[:])
	if fs.getChecksum() != fsChecksum {
		t.Errorf("checksum = %q, want %q", fs.getChecksum(), fsChecksum)
	}
	if len(walked) == 0 {
		t.Errorf("walk callback not invoked")
	}
}

// BuildKit sends no followpaths when a step needs the whole context (for
// example RUN --mount=type=tmpfs or COPY . .). The host reads an empty list as
// "nothing", so the shim must ask for everything explicitly.
func TestEffectiveFollowPaths(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty requests the whole context", "", "."},
		{"single path is kept", "Gemfile", "Gemfile"},
		{"list is kept as is", "docker/builder-image/Dockerfile,Gemfile,.node-version", "docker/builder-image/Dockerfile,Gemfile,.node-version"},
		{"explicit whole context is kept", ".", "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveFollowPaths(tt.in); got != tt.want {
				t.Errorf("effectiveFollowPaths(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
