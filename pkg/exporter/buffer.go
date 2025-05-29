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

package exporter

import (
	"bufio"
	"io"
)

func NewBufferedWriteCloser(w io.Writer, size int) io.WriteCloser {
	if size <= 0 {
		size = 1 << 15
	}

	bw := bufio.NewWriterSize(w, size)

	var c io.Closer
	if closer, ok := w.(io.Closer); ok {
		c = closer
	}

	return &bufferedWriteCloser{bw, c}
}

type bufferedWriteCloser struct {
	*bufio.Writer
	closer io.Closer
}

func (b *bufferedWriteCloser) Close() error {
	if err := b.Writer.Flush(); err != nil {
		return err
	}
	if b.closer != nil {
		return b.closer.Close()
	}
	return nil
}
