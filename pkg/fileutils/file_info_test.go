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

package fileutils

import (
	"sort"
	"testing"
	"time"

	"github.com/apple-uat/container-builder-shim/pkg/api"
)

func TestFileInfoTransformer_TransformIntoFileInfo(t *testing.T) {
	tr := &FileInfoTransformer{}

	now := time.Now().UTC().Truncate(time.Second)
	meta := map[string]string{
		"size":        "1234",
		"mode":        "420", // 0644 in decimal
		"modified_at": now.Format(time.RFC3339),
		"uid":         "1000",
		"gid":         "2000",
		"target":      "link-target",
	}

	src := "hello"
	bt := &api.BuildTransfer{
		Source:      &src,
		IsDirectory: false,
		Metadata:    meta,
	}

	fi, err := tr.TransformIntoFileInfo(bt)
	if err != nil {
		t.Fatalf("TransformIntoFileInfo returned error: %v", err)
	}

	if fi.Name() != src {
		t.Fatalf("expected name %q, got %q", src, fi.Name())
	}
	if fi.Size() != 1234 {
		t.Fatalf("size mismatch: want 1234, got %d", fi.Size())
	}
	if fi.Mode() != 0644 {
		t.Fatalf("mode mismatch: want 0644, got %v", fi.Mode())
	}
	if !fi.ModTime().Equal(now) {
		t.Fatalf("modtime mismatch: want %v, got %v", now, fi.ModTime())
	}
	if fi.IsDir() {
		t.Fatalf("expected file, got directory")
	}
	if fi.Uid != 1000 || fi.Gid != 2000 {
		t.Fatalf("uid/gid mismatch: %d/%d", fi.Uid, fi.Gid)
	}
	if fi.LinkName != "link-target" {
		t.Fatalf("linkname mismatch: %q", fi.LinkName)
	}

	// verify fs.FileInfo interface works
	if fi.Sys() == nil {
		t.Fatalf("Sys() returned nil")
	}
}

func TestFileInfoTransformer_ParseErrors(t *testing.T) {
	tr := &FileInfoTransformer{}

	// bad size
	if _, err := tr.TransformSize(map[string]string{"size": "bad"}); err == nil {
		t.Fatalf("expected size parse error")
	}
	// bad mode
	if _, err := tr.TransformFileMode(map[string]string{"mode": "bad"}); err == nil {
		t.Fatalf("expected mode parse error")
	}
	// bad time
	if _, err := tr.TransformModTime(map[string]string{"modified_at": "not-time"}); err == nil {
		t.Fatalf("expected time parse error")
	}
	// bad uid
	if _, err := tr.TransformUID(map[string]string{"uid": "bad"}); err == nil {
		t.Fatalf("expected uid parse error")
	}
	// bad gid
	if _, err := tr.TransformGID(map[string]string{"gid": "bad"}); err == nil {
		t.Fatalf("expected gid parse error")
	}
}

func TestFileInfos_Sorting(t *testing.T) {
	f1 := &FileInfo{NameVal: "b"}
	f2 := &FileInfo{NameVal: "a"}
	list := FileInfos{f1, f2}
	sort.Sort(list)
	if list[0].Name() != "a" || list[1].Name() != "b" {
		t.Fatalf("sorting failed: %+v", list)
	}
}
