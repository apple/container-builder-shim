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

package prefetcher

import (
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type counter struct {
	ReaderAt
	calls atomic.Int64
}

func (c *counter) ReadAt(p []byte, off int64) (int, error) {
	c.calls.Add(1)
	return c.ReaderAt.ReadAt(p, off)
}

func TestGoroutineLeakDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping goroutine leak detection in short mode")
	}
	initial := runtime.NumGoroutine()
	mock := newMockReaderAt(2*1024*1024, 0, 0)
	config := DefaultConfig()
	config.ChunkSize = 1024
	config.WindowSize = 1024
	pf, err := New(mock, int64(len(mock.data)), config)
	if err != nil {
		t.Fatalf("New prefetcher error: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(off int64) {
			defer wg.Done()
			buf := make([]byte, 512)
			pf.ReadAt(buf, off)
		}(int64((i % 100) * 512))
	}
	wg.Wait()
	if err := pf.Close(); err != nil {
		t.Errorf("Close error: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	final := runtime.NumGoroutine()
	if delta := final - initial; delta > 5 {
		t.Errorf("Possible goroutine leak: before=%d, after=%d (delta=%d)", initial, final, delta)
	}
}

func TestMemoryLeakDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory leak detection in short mode")
	}
	// Force GC to get baseline
	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	for cycle := 0; cycle < 10; cycle++ {
		mock := newMockReaderAt(1024*1024, 2*time.Millisecond, 0)
		config := DefaultConfig()
		config.ChunkSize = 2048
		config.WindowSize = 8 * 2048
		pf, err := New(mock, int64(len(mock.data)), config)
		if err != nil {
			t.Fatalf("New prefetcher error: %v", err)
		}
		buf := make([]byte, 1024)
		// perform some reads
		for j := 0; j < 20; j++ {
			pf.ReadAt(buf, int64((j%10)*1024))
		}
		pf.Close()
	}
	time.Sleep(10 * time.Millisecond)
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	const maxGrow = 20 * 1024 * 1024 // 20MB
	if m1.Alloc > m0.Alloc+maxGrow {
		t.Errorf("Memory growth too high: before=%d, after=%d", m0.Alloc, m1.Alloc)
	}
}

func TestCacheEvictionBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping cache eviction behavior in short mode")
	}
	cc := newChunkCache(1024)
	total := cc.maxCacheSize * 2
	for i := 0; i < total; i++ {
		ch := &chunk{index: int64(i), data: nil, size: 0}
		cc.addChunk(ch)
	}
	cc.mutex.RLock()
	size := len(cc.chunkMap)
	cc.mutex.RUnlock()
	if size > cc.maxCacheSize {
		t.Errorf("Cache size %d exceeds limit %d", size, cc.maxCacheSize)
	}
}

func TestPreventRedundantReads(t *testing.T) {
	mock := newMockReaderAt(4096, 0, 0)
	var ctr counter
	ctr.ReaderAt = mock
	config := DefaultConfig()
	config.ChunkSize = 1024
	config.WindowSize = 1024
	pf, err := New(&ctr, int64(len(mock.data)), config)
	if err != nil {
		t.Fatalf("New prefetcher error: %v", err)
	}
	defer pf.Close()
	buf := make([]byte, 512)
	pf.ReadAt(buf, 0)
	first := ctr.calls.Load()
	pf.ReadAt(buf, 0)
	second := ctr.calls.Load()
	if diff := second - first; diff != 0 {
		t.Errorf("Unexpected underlying reads: got %d new calls", diff)
	}
}

func TestLongTermStability(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping long-term stability test in short mode")
	}
	mock := newMockReaderAt(1024*1024, 0, 0)
	config := DefaultConfig()
	config.ChunkSize = 4096
	config.WindowSize = 64 * 4096
	config.MaxParallelReads = 8
	pf, err := New(mock, int64(len(mock.data)), config)
	if err != nil {
		t.Fatalf("New prefetcher error: %v", err)
	}
	defer pf.Close()
	buf := make([]byte, 1024)
	for i := 0; i < 1000; i++ {
		off := int64((i * 1024) % len(mock.data))
		if _, err := pf.ReadAt(buf, off); err != nil && err != io.EOF {
			t.Fatalf("ReadAt error at iteration %d: %v", i, err)
		}
	}
	if err := pf.Close(); err != nil {
		t.Errorf("Close after long run error: %v", err)
	}
}
