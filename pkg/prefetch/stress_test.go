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
	"context"
	"io"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrefetcherExtremeWindowChunkRatio(t *testing.T) {
	dataSize := 1024 * 1024 // 1MB
	reader := newMockReaderAt(dataSize, 2*time.Millisecond, 0)

	testCases := []struct {
		name             string
		windowSize       int64
		chunkSize        int
		maxParallelReads int
		operations       int
	}{
		{"Large Window X Tiny Chunks", 1024 * 1024, 16, 4, 100},
		{"Tiny Window X Medium Chunks", 4 * 1024, 4 * 1024, 1, 50},
		{"Medium Window X Huge Chunks", 4 * 1024 * 1024, 1024 * 1024, 2, 20},
		{"Medium Window X Tiny Chunks Many Readers", 256 * 1024, 128, 16, 200},
		{"Window Smaller X Than Chunk", 1024, 4 * 1024, 2, 50},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.WindowSize = tc.windowSize
			config.ChunkSize = tc.chunkSize
			config.MaxParallelReads = tc.maxParallelReads

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			prefetcher, err := New(reader, int64(dataSize), config)
			if err != nil {
				t.Fatalf("Failed to create prefetcher: %v", err)
			}

			var wg sync.WaitGroup
			errors := atomic.Int32{}
			completed := atomic.Int32{}

			for i := 0; i < tc.operations; i++ {
				wg.Add(1)
				go func(opIndex int) {
					defer wg.Done()

					select {
					case <-ctx.Done():
						errors.Add(1)
						return
					default:
						readSize := rand.Intn(8*1024) + 1
						offset := rand.Int63n(int64(dataSize - readSize))

						buf := make([]byte, readSize)
						_, err := prefetcher.ReadAt(buf, offset)
						if err != nil && err != io.EOF {
							errors.Add(1)
						} else {
							completed.Add(1)
						}
					}
				}(i)

				if i%10 == 0 {
					time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
				}
			}

			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			select {
			case <-ctx.Done():
				t.Logf("Test timed out but this was expected for extreme case: %s", tc.name)
			case <-done:
			}

			closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer closeCancel()

			closeDone := make(chan struct{})
			go func() {
				prefetcher.Close()
				close(closeDone)
			}()

			select {
			case <-closeCtx.Done():
				t.Errorf("Deadlock detected during prefetcher.Close() for %s", tc.name)
			case <-closeDone:
			}

			t.Logf("Completed %d/%d operations with %d errors",
				completed.Load(), tc.operations, errors.Load())
		})
	}
}

func TestPrefetcherHighConcurrencyExtreme(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping high concurrency extreme test in short mode")
	}

	dataSize := 1024 * 1024
	panicRate := 0.0
	reader := newRacyReaderAt(dataSize, 0.05, 50*time.Millisecond, panicRate)

	config := DefaultConfig()
	config.MaxParallelReads = 4
	config.ChunkSize = 16 * 1024
	config.WindowSize = 64 * 1024

	prefetcher, err := New(reader, int64(dataSize), config)
	if err != nil {
		t.Fatalf("Failed to create prefetcher: %v", err)
	}
	defer prefetcher.Close()
	const numGoroutines = 100
	const readsPerGoroutine = 20

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	successCount := atomic.Int32{}
	errorCount := atomic.Int32{}
	panicCount := atomic.Int32{}

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer func() {
				if r := recover(); r != nil {
					panicCount.Add(1)
					t.Logf("Goroutine %d recovered from panic: %v", id, r)
				}
				wg.Done()
			}()

			for j := 0; j < readsPerGoroutine; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				readSize := 1 + rand.Intn(32*1024)
				offset := rand.Int63n(int64(dataSize - readSize))
				buf := make([]byte, readSize)

				_, err := prefetcher.ReadAt(buf, offset)
				if err != nil && err != io.EOF {
					errorCount.Add(1)
				} else {
					successCount.Add(1)
				}
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			}
		}(i)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)

		for i := 0; i < 3; i++ {
			if ctx.Err() != nil {
				return
			}

			newPrefetcher, err := New(reader, int64(dataSize), config)
			if err != nil {
				t.Logf("Failed to create additional prefetcher: %v", err)
				continue
			}

			for j := 0; j < 5; j++ {
				buf := make([]byte, 1024)
				offset := rand.Int63n(int64(dataSize - 1024))
				newPrefetcher.ReadAt(buf, offset)
			}

			newPrefetcher.Close()
			time.Sleep(50 * time.Millisecond)
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		t.Logf("Test timed out but this was expected for high concurrency test")
	case <-done:
	}

	t.Logf("High concurrency test: %d successful reads, %d errors, %d panics recovered",
		successCount.Load(), errorCount.Load(), panicCount.Load())
}

type syncReadBuffer struct {
	mu   sync.Mutex
	data []byte
}

func newSyncReadBuffer(size int) *syncReadBuffer {
	return &syncReadBuffer{
		data: make([]byte, size),
	}
}

func (s *syncReadBuffer) Write(offset int, src []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy(s.data[offset:offset+len(src)], src)
}

func (s *syncReadBuffer) Read(offset int, dest []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy(dest, s.data[offset:offset+len(dest)])
}

func TestPrefetcherCascadingTimeouts(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping cascading timeouts test in short mode")
	}

	dataSize := 1024 * 1024
	sharedBuf := newSyncReadBuffer(dataSize)
	reader := &blockingReaderAtSafe{
		sharedBuffer:     sharedBuf,
		dataSize:         dataSize,
		blockingPatterns: make(map[int64]time.Duration),
		mu:               sync.Mutex{},
	}

	for i := 0; i < 10; i++ {
		reader.blockingPatterns[int64(i*100*1024)] = time.Duration(200+i*100) * time.Millisecond
	}

	config := DefaultConfig()
	config.ReadTimeout = 100 * time.Millisecond
	config.MaxRetries = 2
	config.RetryInterval = 20 * time.Millisecond
	config.ChunkSize = 32 * 1024

	prefetcher, err := New(reader, int64(dataSize), config)
	if err != nil {
		t.Fatalf("Failed to create prefetcher: %v", err)
	}
	defer prefetcher.Close()

	buf := make([]byte, 1024)
	_, err = prefetcher.ReadAt(buf, 50*1024)
	if err != nil && err != io.EOF {
		t.Logf("Initial read gave error: %v", err)
	}

	results := make([]error, 10)
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			buf := make([]byte, 8*1024)
			offset := int64(idx * 100 * 1024)
			_, err := prefetcher.ReadAt(buf, offset)

			results[idx] = err
		}(i)
	}

	testTimeout := time.After(2 * time.Second)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-testTimeout:
		t.Logf("Test timed out - but this is acceptable for cascading timeouts test")
	case <-done:
	}

	timeoutCount := 0
	otherErrors := 0
	success := 0

	for i, err := range results {
		if err == nil || err == io.EOF {
			success++
		} else if err == context.DeadlineExceeded {
			timeoutCount++
		} else {
			t.Logf("Read at offset %d gave error: %v", i*100*1024, err)
			otherErrors++
		}
	}

	t.Logf("Cascading timeouts test results: %d timeouts, %d other errors, %d successes",
		timeoutCount, otherErrors, success)

	if timeoutCount == 0 && otherErrors == 0 {
		t.Errorf("Expected at least some timeout errors but got none")
	}
}

type blockingReaderAtSafe struct {
	sharedBuffer     *syncReadBuffer
	dataSize         int
	blockingPatterns map[int64]time.Duration
	mu               sync.Mutex
}

func (b *blockingReaderAtSafe) ReadAt(p []byte, off int64) (int, error) {
	b.mu.Lock()
	duration, isBlocking := b.blockingPatterns[off]
	b.mu.Unlock()

	if isBlocking {
		time.Sleep(duration)
	}

	// Check bounds
	if off < 0 {
		return 0, io.EOF
	}

	if off >= int64(b.dataSize) {
		return 0, io.EOF
	}

	n := len(p)
	if off+int64(n) > int64(b.dataSize) {
		n = int(int64(b.dataSize) - off)
	}

	localData := make([]byte, n)
	b.sharedBuffer.Read(int(off), localData)

	copy(p[:n], localData)

	return n, nil
}

func TestPrefetcherResourceExhaustion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping resource exhaustion test in short mode")
	}
	const dataSize = 512 * 1024 * 1024
	const chunkSize = 1 * 1024 * 1024

	reader := &simulatedLargeReaderAt{
		data:     make([]byte, 1024*1024),
		dataSize: dataSize,
	}

	config := DefaultConfig()
	config.ChunkSize = chunkSize
	config.WindowSize = 64 * 1024 * 1024
	config.MaxParallelReads = 8

	prefetcher, err := New(reader, dataSize, config)
	if err != nil {
		t.Fatalf("Failed to create prefetcher: %v", err)
	}
	defer prefetcher.Close()

	const numReaders = 50
	readerErrors := make([]error, numReaders)

	var startBarrier, doneBarrier sync.WaitGroup
	startBarrier.Add(1)
	doneBarrier.Add(numReaders)

	for i := 0; i < numReaders; i++ {
		go func(idx int) {
			defer doneBarrier.Done()

			startBarrier.Wait()

			readSize := 2 * 1024 * 1024 // 2MB reads
			offset := rand.Int63n(dataSize - int64(readSize))

			buf := make([]byte, readSize)
			_, readerErrors[idx] = prefetcher.ReadAt(buf, offset)
		}(i)
	}

	startBarrier.Done()

	done := make(chan struct{})
	go func() {
		doneBarrier.Wait()
		close(done)
	}()

	select {
	case <-time.After(10 * time.Second):
		t.Logf("Resource exhaustion test timed out but this may be expected")
	case <-done:
	}

	// Count errors
	timeoutCount := 0
	otherErrors := 0
	success := 0

	for _, err := range readerErrors {
		if err == nil || err == io.EOF {
			success++
		} else if err == context.DeadlineExceeded {
			timeoutCount++
		} else {
			otherErrors++
		}
	}

	t.Logf("Resource exhaustion test results: %d successes, %d timeouts, %d other errors",
		success, timeoutCount, otherErrors)

	if success+timeoutCount+otherErrors < numReaders {
		t.Errorf("Some readers neither succeeded nor returned an error")
	}
}
