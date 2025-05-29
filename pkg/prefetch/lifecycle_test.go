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
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type flakeyReaderAt struct {
	data  []byte
	calls atomic.Int32
}

func (f *flakeyReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c := f.calls.Add(1)
	if c == 1 {
		return 0, errors.New("transient")
	}
	if off < 0 || off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	return n, nil
}

type concurrencyCounterAt struct {
	ReaderAt
	current atomic.Int32
	max     atomic.Int32
}

func (c *concurrencyCounterAt) ReadAt(p []byte, off int64) (int, error) {
	cur := c.current.Add(1)
	if cur > c.max.Load() {
		c.max.Store(cur)
	}
	defer c.current.Add(-1)
	return c.ReaderAt.ReadAt(p, off)
}

func TestSizeSemantics(t *testing.T) {
	mock := newMockReaderAt(100, 0, 0)
	pf, err := New(mock, 100)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer pf.Close()
	pf2, err := New(mock, -1)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer pf2.Close()
	if sz := pf2.Size(); sz != -1 {
		t.Errorf("Size() expected -1,nil got %d,%v", sz, err)
	}
	pf.Close()
}

func TestSlidingWindowEviction(t *testing.T) {
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}
	reader := &sliceReaderAt{data}
	cfg := DefaultConfig()
	cfg.ChunkSize = 10
	cfg.WindowSize = 20
	p, err := New(reader, int64(len(data)), cfg)
	if err != nil {
		t.Fatalf("Failed to create prefetcher: %v", err)
	}
	defer p.Close()
	buf := make([]byte, cfg.ChunkSize)

	if _, err := p.ReadAt(buf, 0); err != nil {
		t.Fatalf("First ReadAt failed: %v", err)
	}
	impl, ok := p.(*prefetcher)
	if !ok {
		t.Fatalf("Expected *prefetcher implementation, got %T", p)
	}
	for idx := range impl.cache.chunkMap {
		if idx < 0 || idx > 2 {
			t.Errorf("Unexpected chunk index %d in cache after first read", idx)
		}
	}

	if _, err := p.ReadAt(buf, int64(cfg.ChunkSize*2)); err != nil {
		t.Fatalf("Second ReadAt failed: %v", err)
	}
	for idx := range impl.cache.chunkMap {
		if idx < 2 {
			t.Errorf("Old chunk %d was not evicted after sliding window moved", idx)
		}
	}
}

func TestWindowNormalization(t *testing.T) {
	mock := newMockReaderAt(128, 0, 0)
	cfg := DefaultConfig()
	cfg.ChunkSize = 64
	cfg.WindowSize = 1
	pf, err := New(mock, 128, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer pf.Close()
	p := pf.(*prefetcher)
	if p.config.WindowSize < int64(p.config.ChunkSize) {
		t.Errorf("WindowSize (%d) < ChunkSize (%d)", p.config.WindowSize, p.config.ChunkSize)
	}
}

func TestReadTimeout(t *testing.T) {
	slow := &mockReaderAt{data: make([]byte, 1024), readDelay: 50 * time.Millisecond}
	cfg := DefaultConfig()
	cfg.ReadTimeout = 10 * time.Millisecond
	cfg.MaxRetries = 0
	pf, err := New(slow, int64(len(slow.data)), cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer pf.Close()
	buf := make([]byte, 512)
	_, err = pf.ReadAt(buf, 0)
	if !errors.Is(err, ErrReadFailed) {
		t.Errorf("Expected ErrReadFailed, got %v", err)
	}
}

func TestRetryLogic(t *testing.T) {
	data := make([]byte, 64)
	for i := range data {
		data[i] = byte(i)
	}
	flakey := &flakeyReaderAt{data: data}
	cfg := DefaultConfig()
	cfg.ChunkSize = len(data)
	cfg.MaxRetries = 1
	pf, err := New(flakey, int64(len(data)), cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer pf.Close()
	buf := make([]byte, len(data))
	n, err := pf.ReadAt(buf, 0)
	if n != len(data) {
		t.Errorf("Expected to read %d bytes, got %d", len(data), n)
	}
	if err != nil && err != io.EOF {
		t.Errorf("Expected EOF or nil after read, got %v", err)
	}
	if calls := flakey.calls.Load(); calls < 2 {
		t.Errorf("Expected at least 2 underlying attempts, got %d", calls)
	}
}

func TestParallelReadThrottling(t *testing.T) {
	dataSize := 8 * 1024
	mock := newMockReaderAt(dataSize, 0, 0)
	var ctr concurrencyCounterAt
	ctr.ReaderAt = mock
	cfg := DefaultConfig()
	cfg.ChunkSize = 512
	cfg.WindowSize = 4 * 512
	cfg.MaxParallelReads = 3
	pf, err := New(&ctr, int64(dataSize), cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer pf.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(off int64) {
			defer wg.Done()
			buf := make([]byte, 256)
			pf.ReadAt(buf, off)
		}(int64((i % 16) * 256))
	}
	wg.Wait()
	if got := ctr.max.Load(); got > int32(cfg.MaxParallelReads) {
		t.Errorf("Max concurrent underlying reads %d > %d", got, cfg.MaxParallelReads)
	}
}

func TestIntervalTreeLookup(t *testing.T) {
	cc := newChunkCache(10)
	for _, idx := range []int64{1, 2, 3, 5, 10} {
		cc.addChunk(&chunk{index: idx, data: nil, size: 0})
	}
	result := cc.findOverlappingChunks(2, 5)
	seen := map[int64]bool{}
	for _, ch := range result {
		seen[ch.index] = true
	}
	for _, want := range []int64{2, 3, 5} {
		if !seen[want] {
			t.Errorf("Missing chunk %d in overlaps", want)
		}
	}
	if len(result) != 3 {
		t.Errorf("Expected 3 overlaps, got %d", len(result))
	}
}

func TestLRUEvictionOrder(t *testing.T) {
	cc := newChunkCache(8)
	cc.maxCacheSize = 3
	cc.evictCount = 1
	for i := int64(1); i <= 3; i++ {
		cc.addChunk(&chunk{index: i, data: nil, size: 0})
	}
	_ = cc.getChunk(2)
	_ = cc.getChunk(3)
	cc.addChunk(&chunk{index: 4, data: nil, size: 0})
	if _, ok := cc.chunkMap[1]; ok {
		t.Errorf("Chunk 1 should have been evicted")
	}
	for _, idx := range []int64{2, 3, 4} {
		if _, ok := cc.chunkMap[idx]; !ok {
			t.Errorf("Chunk %d unexpectedly evicted", idx)
		}
	}
}
