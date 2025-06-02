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
	"io/fs"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/tonistiigi/fsutil/types"
)

type FileInfos []*FileInfo

var _ fs.FileInfo = &FileInfo{}
var _ sort.Interface = FileInfos{}

type FileInfo struct {
	NameVal    string
	SizeVal    int64
	ModeVal    fs.FileMode
	ModTimeVal time.Time
	IsDirVal   bool
	Uid        uint32
	Gid        uint32
	LinkName   string
}

func (f *FileInfo) Name() string {
	return f.NameVal
}

func (f *FileInfo) Size() int64 {
	return f.SizeVal
}

func (f *FileInfo) Mode() fs.FileMode {
	return f.ModeVal
}

func (f *FileInfo) ModTime() time.Time {
	return f.ModTimeVal
}

func (f *FileInfo) IsDir() bool {
	return f.IsDirVal
}

func (f *FileInfo) Sys() any {
	return &types.Stat{
		Path:     f.NameVal,
		Mode:     uint32(f.ModeVal),
		Uid:      f.Uid,
		Gid:      f.Gid,
		ModTime:  f.ModTimeVal.UnixNano(),
		Linkname: f.LinkName,
	}
}

func (f FileInfos) Len() int {
	return len(f)
}

func (f FileInfos) Less(i, j int) bool {
	return f[i].Name() < f[j].Name()
}

func (f FileInfos) Swap(i, j int) {
	intermediate := f[i]
	f[i] = f[j]
	f[j] = intermediate
}

type FileInfoTransformer struct{}

func (i *FileInfoTransformer) TransformIntoFileInfo(it *api.BuildTransfer) (*FileInfo, error) {
	Size, err := i.TransformSize(it.Metadata)
	if err != nil {
		return nil, err
	}

	Mode, err := i.TransformFileMode(it.Metadata)
	if err != nil {
		return nil, err
	}

	ModTime, err := i.TransformModTime(it.Metadata)
	if err != nil {
		return nil, err
	}

	Uid, err := i.TransformUID(it.Metadata)
	if err != nil {
		return nil, err
	}

	Gid, err := i.TransformGID(it.Metadata)
	if err != nil {
		return nil, err
	}

	fileMode := fs.FileMode(Mode)
	linkName := i.TransformLinkName(it.Metadata)

	return &FileInfo{
		NameVal:    *it.Source,
		SizeVal:    Size,
		ModeVal:    fileMode,
		ModTimeVal: ModTime,
		IsDirVal:   it.IsDirectory,
		Uid:        Uid,
		Gid:        Gid,
		LinkName:   linkName,
	}, nil
}

func (i *FileInfoTransformer) TransformSize(metadata map[string]string) (int64, error) {
	if SizeString, ok := metadata["size"]; ok {
		return strconv.ParseInt(SizeString, 10, 64)
	}
	return 0, nil
}

func (i *FileInfoTransformer) TransformFileMode(metadata map[string]string) (fs.FileMode, error) {
	ModeString := metadata["mode"]
	if ModeString == "" {
		return 0, nil
	}
	val, err := strconv.ParseUint(ModeString, 10, 32)
	if err != nil {
		return 0, err
	}
	return os.FileMode(val), nil
}

func (i *FileInfoTransformer) TransformModTime(metadata map[string]string) (time.Time, error) {
	modifiedAt := metadata["modified_at"]
	if modifiedAt == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, modifiedAt)
}

func (i *FileInfoTransformer) TransformUID(metadata map[string]string) (uint32, error) {
	UidString := metadata["uid"]
	if UidString == "" {
		return 0, nil
	}
	Uid, err := strconv.ParseUint(UidString, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(Uid), nil
}

func (i *FileInfoTransformer) TransformGID(metadata map[string]string) (uint32, error) {
	GidString := metadata["gid"]
	if GidString == "" {
		return 0, nil
	}
	Gid, err := strconv.ParseUint(GidString, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(Gid), nil
}

func (i *FileInfoTransformer) TransformLinkName(metadata map[string]string) string {
	linkName, ok := metadata["target"]
	if !ok {
		return ""
	}
	return string(linkName)
}
