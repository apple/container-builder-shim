//===----------------------------------------------------------------------===//
// Copyright © 2025 Apple Inc. and the container-builder-shim project authors.
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

package prefetcher

import (
	"io"
	"math/rand"
	"testing"
	"time"
)

func BenchmarkDirectReaderAt(b *testing.B) {
	const dataSize = 10 * 1024 * 1024
	mock := newMockReaderAt(dataSize, 2*time.Millisecond, 0)
	buf := make([]byte, 4*1024)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := int64((i * len(buf)) % dataSize)
		if _, err := mock.ReadAt(buf, off); err != nil && err != io.EOF {
			b.Fatalf("direct ReadAt error: %v", err)
		}
	}
}

func BenchmarkDirectReaderAtRandom(b *testing.B) {
	const dataSize = 10 * 1024 * 1024
	mock := newMockReaderAt(dataSize, 2*time.Millisecond, 0)
	buf := make([]byte, 4*1024)
	rnd := rand.New(rand.NewSource(42))
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := rnd.Int63n(dataSize - int64(len(buf)))
		if _, err := mock.ReadAt(buf, off); err != nil && err != io.EOF {
			b.Fatalf("direct ReadAt error: %v", err)
		}
	}
}

func BenchmarkPrefetcherSequential(b *testing.B) {
	const dataSize = 10 * 1024 * 1024
	mock := newMockReaderAt(dataSize, 2*time.Millisecond, 0)
	bufSize := 4 * 1024
	cfg := DefaultConfig()
	cfg.ChunkSize = 64 * 1024
	cfg.WindowSize = 1 * 1024 * 1024
	cfg.MaxParallelReads = 4
	pf, err := New(mock, dataSize, cfg)
	if err != nil {
		b.Fatalf("New prefetcher error: %v", err)
	}
	defer pf.Close()
	buf := make([]byte, bufSize)
	b.SetBytes(int64(bufSize))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := int64((i * bufSize) % dataSize)
		if _, err := pf.ReadAt(buf, off); err != nil && err != io.EOF {
			b.Fatalf("prefetch ReadAt error: %v", err)
		}
	}
}

func BenchmarkPrefetcherRandom(b *testing.B) {
	const dataSize = 10 * 1024 * 1024
	mock := newMockReaderAt(dataSize, 2*time.Millisecond, 0)
	bufSize := 4 * 1024
	cfg := DefaultConfig()
	cfg.ChunkSize = bufSize
	cfg.WindowSize = int64(bufSize)
	cfg.MaxParallelReads = 1
	pf, err := New(mock, dataSize, cfg)
	if err != nil {
		b.Fatalf("New prefetcher error: %v", err)
	}
	defer pf.Close()
	buf := make([]byte, bufSize)
	rnd := rand.New(rand.NewSource(42))
	startOff := rnd.Int63n(dataSize - int64(bufSize))
	pf.ReadAt(buf, startOff)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := rnd.Int63n(dataSize - int64(bufSize))
		if _, err := pf.ReadAt(buf, off); err != nil && err != io.EOF {
			b.Fatalf("prefetch ReadAt error: %v", err)
		}
	}
}
