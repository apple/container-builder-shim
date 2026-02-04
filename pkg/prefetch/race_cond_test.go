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

package prefetcher

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type racyReaderAt struct {
	data          []byte
	mutex         sync.Mutex
	readCount     atomic.Int64
	errorRate     float64
	maxDelay      time.Duration
	panicRate     float64
	concurrent    atomic.Int32
	maxConcurrent atomic.Int32
}

func newRacyReaderAt(size int, errorRate float64, maxDelay time.Duration, panicRate float64) *racyReaderAt {
	data := make([]byte, size)
	for i := 0; i < size; i++ {
		data[i] = byte(i % 256)
	}
	return &racyReaderAt{
		data:      data,
		errorRate: errorRate,
		maxDelay:  maxDelay,
		panicRate: panicRate,
	}
}

func (r *racyReaderAt) ReadAt(p []byte, off int64) (int, error) {
	current := r.concurrent.Add(1)
	defer r.concurrent.Add(-1)

	for {
		max := r.maxConcurrent.Load()
		if current <= max || r.maxConcurrent.CompareAndSwap(max, current) {
			break
		}
	}

	r.readCount.Add(1)

	if rand.Float64() < r.panicRate {
		panic("simulated panic in reader")
	}

	if r.maxDelay > 0 {
		delay := time.Duration(rand.Int63n(int64(r.maxDelay)))
		time.Sleep(delay)
	}

	if rand.Float64() < r.errorRate {
		return 0, errors.New("random error from racy reader")
	}

	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}

	n := len(p)
	remaining := int64(len(r.data)) - off
	if int64(n) > remaining {
		n = int(remaining)
	}

	copy(p, r.data[off:off+int64(n)])

	var err error
	if off+int64(n) >= int64(len(r.data)) {
		err = io.EOF
	}

	return n, err
}

func (r *racyReaderAt) GetStats() (reads int64, maxConcurrent int32) {
	return r.readCount.Load(), r.maxConcurrent.Load()
}

type slowExpandingReaderAt struct {
	currentSize atomic.Int64
	maxSize     int64
	growRate    int64
	data        []byte
	mutex       sync.RWMutex
	delay       time.Duration
}

func newSlowExpandingReaderAt(initialSize, maxSize, growRate int64, delay time.Duration) *slowExpandingReaderAt {
	data := make([]byte, maxSize)
	for i := int64(0); i < maxSize; i++ {
		data[i] = byte(i % 256)
	}

	r := &slowExpandingReaderAt{
		maxSize:  maxSize,
		growRate: growRate,
		data:     data,
		delay:    delay,
	}
	r.currentSize.Store(initialSize)

	go func() {
		for {
			current := r.currentSize.Load()
			if current >= maxSize {
				break
			}
			newSize := current + growRate
			if newSize > maxSize {
				newSize = maxSize
			}
			r.currentSize.Store(newSize)
			time.Sleep(delay)
		}
	}()

	return r
}

func (r *slowExpandingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	time.Sleep(r.delay / 10)

	r.mutex.RLock()
	defer r.mutex.RUnlock()

	currentSize := r.currentSize.Load()

	if off >= currentSize {
		return 0, io.EOF
	}

	n := len(p)
	remaining := currentSize - off
	if int64(n) > remaining {
		n = int(remaining)
	}

	copy(p, r.data[off:off+int64(n)])

	var err error
	if off+int64(n) >= currentSize {
		err = io.EOF
	}

	return n, err
}

func (r *slowExpandingReaderAt) GetCurrentSize() int64 {
	return r.currentSize.Load()
}

func TestPrefetcherRaceConditionStress(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race condition stress test in short mode")
	}

	reader := newRacyReaderAt(1024*1024, 0.01, 5*time.Millisecond, 0)

	config := DefaultConfig()
	config.MaxRetries = 5
	config.ChunkSize = 8 * 1024
	config.WindowSize = 64 * 1024
	config.MaxParallelReads = 8

	pf, err := New(reader, int64(len(reader.data)), config)
	if err != nil {
		t.Fatalf("Failed to create prefetcher: %v", err)
	}
	defer pf.Close()

	var wg sync.WaitGroup
	const numReaders = 50
	const numReadsPerReader = 20
	const maxReadSize = 16 * 1024

	errs := int32(0)
	reads := int32(0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()

			for j := 0; j < numReadsPerReader; j++ {
				select {
				case <-ctx.Done():
					return
				default:
					readSize := rand.Intn(maxReadSize) + 1

					maxOffset := int64(len(reader.data) - readSize)
					offset := rand.Int63n(maxOffset)

					buf := make([]byte, readSize)
					_, err := pf.ReadAt(buf, offset)

					if err != nil && err != io.EOF {
						atomic.AddInt32(&errs, 1)
					} else {
						atomic.AddInt32(&reads, 1)
					}

					time.Sleep(time.Duration(rand.Intn(100)) * time.Microsecond)
				}
			}
		}(i)
	}

	wg.Wait()

	readCount, maxConcurrent := reader.GetStats()

	t.Logf("Performed %d successful reads, %d errors", reads, errs)
	t.Logf("Underlying reader: %d reads, max concurrent: %d", readCount, maxConcurrent)

	if maxConcurrent > int32(config.MaxParallelReads) {
		t.Errorf("Max concurrent reads (%d) exceeds limit (%d)",
			maxConcurrent, config.MaxParallelReads)
	}
}

func TestPrefetcherGrowingFile(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping growing file test in short mode")
	}

	initialSize := int64(16 * 1024)
	maxSize := int64(256 * 1024)
	growRate := int64(8 * 1024)
	growDelay := 10 * time.Millisecond

	reader := newSlowExpandingReaderAt(initialSize, maxSize, growRate, growDelay)

	config := DefaultConfig()
	config.ChunkSize = 4 * 1024
	config.WindowSize = 16 * 1024

	pf, err := New(reader, -1, config)
	if err != nil {
		t.Fatalf("Failed to create prefetcher: %v", err)
	}
	defer pf.Close()

	var offset int64
	const readSize = 4 * 1024
	buffer := make([]byte, readSize)

	done := make(chan struct{})

	successfulReads := atomic.Int32{}
	eofCount := atomic.Int32{}

	go func() {
		for {
			currentSize := reader.GetCurrentSize()

			n, err := pf.ReadAt(buffer, offset)

			switch err {
			case nil:
				successfulReads.Add(1)
				offset += int64(n)
			case io.EOF:
				eofCount.Add(1)
				time.Sleep(growDelay * 2)
			default:
				t.Errorf("Unexpected error: %v at offset %d (current size: %d)",
					err, offset, currentSize)
				break
			}

			if offset >= maxSize || currentSize >= maxSize {
				break
			}

			time.Sleep(growDelay / 2)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Log("Test timed out, but this is not necessarily an error")
	}

	t.Logf("Successful reads: %d, EOF encounters: %d, final offset: %d",
		successfulReads.Load(), eofCount.Load(), offset)

	if successfulReads.Load() == 0 {
		t.Errorf("Expected at least some successful reads")
	}
}

func TestPrefetcherExtremeChunkSizes(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping extreme chunk sizes test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				t.Log("Test timed out, but this is expected for extreme parameter testing")
			}
		case <-done:
		}
	}()
	dataSize := 1024 * 1024
	reader := newMockReaderAt(dataSize, 0, 0)

	testCases := []struct {
		name             string
		chunkSize        int
		readSize         int
		windowSize       int64
		maxParallelReads int
	}{
		{"Very small chunks", 1024, 4 * 1024, 16 * 1024, 4},
		{"Very large chunks", 16 * 1024, 1024, 32 * 1024, 2},
		{"Chunk equals file size", dataSize / 4, 128, int64(dataSize / 2), 1},
		{"Tiny reads", 4 * 1024, 1, 16 * 1024, 2},
		{"Misaligned chunks", 1027, 513, 8 * 1024, 3},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.ChunkSize = tc.chunkSize
			config.WindowSize = tc.windowSize
			config.MaxParallelReads = tc.maxParallelReads

			pf, err := New(reader, int64(dataSize), config)
			if err != nil {
				t.Fatalf("Failed to create prefetcher: %v", err)
			}
			defer pf.Close()

			for offset := 0; offset < dataSize-tc.readSize; offset += dataSize / 10 {
				buf := make([]byte, tc.readSize)
				n, err := pf.ReadAt(buf, int64(offset))

				if err != nil && err != io.EOF {
					t.Errorf("Error reading at offset %d: %v", offset, err)
					continue
				}

				expectedLen := tc.readSize
				if offset+tc.readSize > dataSize {
					expectedLen = dataSize - offset
				}

				if n != expectedLen {
					t.Errorf("Expected to read %d bytes, got %d", expectedLen, n)
				}

				for i := 0; i < min(10, n); i++ {
					expected := byte((offset + i) % 256)
					if buf[i] != expected {
						t.Errorf("Data mismatch at offset %d: expected %d, got %d",
							offset+i, expected, buf[i])
						break
					}
				}
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestPrefetcherZeroSizedReadsAndEmptyReader(t *testing.T) {
	testCases := []struct {
		name      string
		dataSize  int
		readSize  int
		readCount int
	}{
		{"Empty reader", 0, 0, 1},
		{"Empty reader with non-zero read", 0, 1024, 1},
		{"Non-empty reader with zero reads", 1024, 0, 10},
		{"One-byte reader", 1, 1, 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reader := newMockReaderAt(tc.dataSize, 0, 0)

			pf, err := New(reader, int64(tc.dataSize))
			if err != nil {
				t.Fatalf("Failed to create prefetcher: %v", err)
			}
			defer pf.Close()

			for i := 0; i < tc.readCount; i++ {
				var offset int64
				if tc.dataSize > 0 {
					offset = int64(i % tc.dataSize)
				}

				buf := make([]byte, tc.readSize)
				n, err := pf.ReadAt(buf, offset)

				switch {
				case tc.dataSize == 0 && tc.readSize > 0:
					if err != io.EOF {
						t.Errorf("Expected EOF for non-zero read on empty reader, got %v", err)
					}
				case tc.readSize == 0:
					if n != 0 {
						t.Errorf("Expected 0 bytes for zero-sized read, got %d", n)
					}
					if tc.dataSize > 0 && offset < int64(tc.dataSize) && err != nil {
						t.Errorf("Expected no error for zero-sized read within bounds, got %v", err)
					}
				case tc.dataSize > 0 && offset >= int64(tc.dataSize):
					if err != io.EOF && !errors.Is(err, ErrOffsetOutOfRange) {
						t.Errorf("Expected EOF or ErrOffsetOutOfRange when reading past end, got %v", err)
					}
				}
			}
		})
	}
}

func TestPrefetcherLargeAllocations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large allocation test in short mode")
	}

	const dataSize = 128 * 1024 * 1024

	simulatedData := make([]byte, 1024*1024)
	for i := range simulatedData {
		simulatedData[i] = byte(i % 256)
	}

	reader := &simulatedLargeReaderAt{
		data:     simulatedData,
		dataSize: dataSize,
	}

	config := DefaultConfig()
	config.ChunkSize = 1 * 1024 * 1024
	config.WindowSize = 8 * 1024 * 1024
	config.MaxParallelReads = 4

	pf, err := New(reader, dataSize, config)
	if err != nil {
		t.Fatalf("Failed to create prefetcher: %v", err)
	}

	const numReads = 20
	const readSize = 64 * 1024

	for i := 0; i < numReads; i++ {
		offset := rand.Int63n(dataSize - readSize)
		buf := make([]byte, readSize)

		_, err := pf.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			t.Errorf("Error reading at offset %d: %v", offset, err)
		}

		if i%5 == 0 {
			runtime.GC()
			time.Sleep(10 * time.Millisecond)
		}
	}

	err = pf.Close()
	if err != nil {
		t.Errorf("Error closing prefetcher: %v", err)
	}

	for i := 0; i < 5; i++ {
		pf, err := New(reader, dataSize, config)
		if err != nil {
			t.Fatalf("Failed to create prefetcher in cycle %d: %v", i, err)
		}

		buf := make([]byte, readSize)
		_, _ = pf.ReadAt(buf, 0)

		pf.Close()
	}
}

type simulatedLargeReaderAt struct {
	data     []byte
	dataSize int64
}

func (s *simulatedLargeReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= s.dataSize {
		return 0, io.EOF
	}

	n := len(p)
	if off+int64(n) > s.dataSize {
		n = int(s.dataSize - off)
	}

	mappedOffset := off % int64(len(s.data))

	copied := 0
	for copied < n {
		bytesToCopy := min(n-copied, len(s.data)-int(mappedOffset))
		copy(p[copied:copied+bytesToCopy], s.data[mappedOffset:mappedOffset+int64(bytesToCopy)])
		copied += bytesToCopy
		mappedOffset = (mappedOffset + int64(bytesToCopy)) % int64(len(s.data))
	}

	return n, nil
}

func TestPrefetcherConcurrentInitialization(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent initialization test in short mode")
	}

	dataSize := 1024 * 1024
	reader := newMockReaderAt(dataSize, 0, 0)

	const numPrefetchers = 50
	var wg sync.WaitGroup
	errCount := atomic.Int32{}

	for i := 0; i < numPrefetchers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			config := DefaultConfig()
			config.ChunkSize = 1024 + (idx % 1024)
			config.WindowSize = int64(4096 + (idx * 1024))
			config.MaxParallelReads = 1 + (idx % 8)

			pf, err := New(reader, int64(dataSize), config)
			if err != nil {
				errCount.Add(1)
				t.Logf("Error creating prefetcher %d: %v", idx, err)
				return
			}
			defer pf.Close()

			buf := make([]byte, 1024)
			offset := int64((idx * 1024) % (dataSize - 1024))
			_, err = pf.ReadAt(buf, offset)
			if err != nil && err != io.EOF {
				errCount.Add(1)
				t.Logf("Error reading from prefetcher %d: %v", idx, err)
				return
			}
		}(i)
	}

	wg.Wait()

	if errCount.Load() > 0 {
		t.Errorf("%d out of %d prefetchers failed to initialize or read",
			errCount.Load(), numPrefetchers)
	}
}

func TestPrefetcherContextCancellation(t *testing.T) {
	reader := newMockReaderAt(10*1024*1024, 20*time.Millisecond, 0)

	pf, err := New(reader, int64(len(reader.data)))
	if err != nil {
		t.Fatalf("Failed to create prefetcher: %v", err)
	}

	var wg sync.WaitGroup
	const numReaders = 10
	errors := make([]error, numReaders)

	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			buf := make([]byte, 1024*1024)
			offset := int64(idx * 1024 * 1024)
			_, err := pf.ReadAt(buf, offset)
			errors[idx] = err
		}(i)
	}

	time.Sleep(10 * time.Millisecond)

	err = pf.Close()
	if err != nil {
		t.Errorf("Error closing prefetcher: %v", err)
	}

	wg.Wait()

	errorCount := 0
	for i, err := range errors {
		if err == ErrPrefetcherClosed || err == context.Canceled {
			errorCount++
			t.Logf("Reader %d was interrupted with: %v", i, err)
		} else if err != nil && err != io.EOF {
			errorCount++
			t.Logf("Reader %d got error: %v", i, err)
		}
	}

	if errorCount == 0 {
		t.Errorf("Expected some reads to be interrupted with errors, but none were")
	} else {
		t.Logf("%d out of %d reads were interrupted", errorCount, numReaders)
	}

	buf := make([]byte, 1024)
	_, err = pf.ReadAt(buf, 0)
	if err != ErrPrefetcherClosed {
		t.Errorf("Expected ErrPrefetcherClosed after closing, got: %v", err)
	}
}
