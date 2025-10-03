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
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

var (
	ErrOffsetOutOfRange = errors.New("offset is out of range")
	ErrReadFailed       = errors.New("read from underlying reader failed")
	ErrPrefetcherClosed = errors.New("prefetcher is closed")
)

type Config struct {
	// WindowSize represents the maximum size of the sliding window for prefetching
	WindowSize int64

	// ChunkSize defines the size of chunks to read from the underlying reader
	ChunkSize int

	// MaxParallelReads limits the number of concurrent reads from the underlying reader
	MaxParallelReads int

	ReadTimeout   time.Duration
	RetryInterval time.Duration
	MaxRetries    int
}

func DefaultConfig() Config {
	return Config{
		WindowSize:       1 << 20,
		ChunkSize:        1 << 16,
		MaxParallelReads: 4,
		ReadTimeout:      1<<63 - 1,
		RetryInterval:    500 * time.Millisecond,
		MaxRetries:       3,
	}
}

type ReaderAt interface {
	io.ReaderAt
}

type Prefetcher interface {
	ReadAt(p []byte, off int64) (n int, err error)
	Size() int64
	Close() error
}

type prefetcher struct {
	reader     ReaderAt
	size       int64
	config     Config
	cache      *chunkCache
	semaphore  chan struct{}
	readCount  atomic.Int64
	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
	closed     atomic.Bool
	group      singleflight.Group
}

func New(reader ReaderAt, size int64, configs ...Config) (Prefetcher, error) {
	if reader == nil {
		return nil, errors.New("reader cannot be nil")
	}

	config := DefaultConfig()
	if len(configs) > 0 {
		config = configs[0]
	}

	if config.WindowSize <= 0 {
		config.WindowSize = DefaultConfig().WindowSize
	}
	if config.ChunkSize <= 0 {
		config.ChunkSize = DefaultConfig().ChunkSize
	}
	if config.MaxParallelReads <= 0 {
		config.MaxParallelReads = DefaultConfig().MaxParallelReads
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = DefaultConfig().ReadTimeout
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = DefaultConfig().RetryInterval
	}
	if config.MaxRetries < 0 {
		config.MaxRetries = DefaultConfig().MaxRetries
	}
	minWindow := int64(config.ChunkSize)
	if config.WindowSize < minWindow {
		config.WindowSize = minWindow
	}

	ctx, cancel := context.WithCancel(context.Background())

	p := &prefetcher{
		reader:     reader,
		size:       size,
		config:     config,
		cache:      newChunkCache(config.ChunkSize),
		semaphore:  make(chan struct{}, config.MaxParallelReads),
		ctx:        ctx,
		cancelFunc: cancel,
	}

	return p, nil
}

func (p *prefetcher) ReadAt(b []byte, off int64) (int, error) {
	if p.closed.Load() {
		return 0, ErrPrefetcherClosed
	}

	if off < 0 {
		return 0, ErrOffsetOutOfRange
	}
	if len(b) == 0 {
		return 0, nil
	}

	if p.size == 0 {
		return 0, io.EOF
	}

	if p.size > 0 && off >= p.size {
		return 0, ErrOffsetOutOfRange
	}

	p.readCount.Add(1)
	defer p.readCount.Add(-1)

	startChunkIdx := off / int64(p.config.ChunkSize)
	endChunkIdx := (off + int64(len(b)) - 1) / int64(p.config.ChunkSize)
	p.cache.evictBefore(startChunkIdx)

	if p.size > 0 && off+int64(len(b)) > p.size && startChunkIdx == endChunkIdx {
		return 0, io.EOF
	}

	windowChunks := p.config.WindowSize / int64(p.config.ChunkSize)
	if rem := p.config.WindowSize % int64(p.config.ChunkSize); rem > 0 {
		windowChunks++
	}
	if windowChunks < 1 {
		windowChunks = 1
	}
	windowEndChunkIdx := startChunkIdx + windowChunks - 1
	if p.size > 0 {
		maxChunkIdx := (p.size - 1) / int64(p.config.ChunkSize)
		if windowEndChunkIdx > maxChunkIdx {
			windowEndChunkIdx = maxChunkIdx
		}
	}

	maxPrefetchOps := p.config.WindowSize / int64(p.config.ChunkSize)
	if maxPrefetchOps > 1000 {
		maxPrefetchOps = 1000
	}

	for chunkIdx := startChunkIdx; chunkIdx <= endChunkIdx; chunkIdx++ {
		if !p.cache.hasChunk(chunkIdx) {
			p.wg.Add(1)
			go func(idx int64) {
				defer p.wg.Done()
				if p.ctx.Err() != nil {
					return
				}
				if !p.cache.hasChunk(idx) {
					_, _ = p.fetchChunk(idx)
				}
			}(chunkIdx)
		}
	}

	prefetchCount := int64(0)
	for i := endChunkIdx + 1; i <= windowEndChunkIdx && prefetchCount < maxPrefetchOps; i++ {
		chunkIdx := i
		if !p.cache.hasChunk(chunkIdx) {
			prefetchCount++
			p.wg.Add(1)
			go func(idx int64) {
				defer p.wg.Done()
				time.Sleep(5 * time.Millisecond)
				if p.ctx.Err() != nil {
					return
				}
				if p.cache.hasChunk(idx) {
					return
				}
				select {
				case p.semaphore <- struct{}{}:
					defer func() { <-p.semaphore }()
					buf := make([]byte, p.config.ChunkSize)
					start := idx * int64(p.config.ChunkSize)
					n, err := p.reader.ReadAt(buf, start)
					if err != nil && err != io.EOF {
						return
					}
					ch := &chunk{
						index:    idx,
						data:     buf,
						size:     n,
						lastUsed: time.Now(),
					}
					p.cache.addChunk(ch)
				default:
				}
			}(chunkIdx)
		}
	}

	bytesRead := 0
	bufferOffset := 0

	for chunkIdx := startChunkIdx; chunkIdx <= endChunkIdx; chunkIdx++ {
		chunk, err := p.getChunk(chunkIdx)
		if err != nil {
			return bytesRead, err
		}

		startOffsetInChunk := 0
		if chunkIdx == startChunkIdx {
			startOffsetInChunk = int(off % int64(p.config.ChunkSize))
		}

		bytesToCopy := chunk.size - startOffsetInChunk
		if bytesToCopy <= 0 {
			break
		}
		remainingBytes := len(b) - bufferOffset
		if bytesToCopy > remainingBytes {
			bytesToCopy = remainingBytes
		}

		copy(b[bufferOffset:bufferOffset+bytesToCopy], chunk.data[startOffsetInChunk:startOffsetInChunk+bytesToCopy])
		bufferOffset += bytesToCopy
		bytesRead += bytesToCopy

		if bufferOffset >= len(b) {
			break
		}
	}

	var err error
	if p.size > 0 && off+int64(bytesRead) >= p.size {
		err = io.EOF
	}

	return bytesRead, err
}

func (p *prefetcher) getChunk(chunkIdx int64) (*chunk, error) {
	if chunk := p.cache.getChunk(chunkIdx); chunk != nil {
		return chunk, nil
	}

	return p.fetchChunk(chunkIdx)
}

func (p *prefetcher) fetchChunk(chunkIdx int64) (*chunk, error) {
	key := strconv.FormatInt(chunkIdx, 10)
	val, err, _ := p.group.Do(key, func() (interface{}, error) {
		if ch := p.cache.getChunk(chunkIdx); ch != nil {
			return ch, nil
		}
		select {
		case <-p.ctx.Done():
			return nil, p.ctx.Err()
		case p.semaphore <- struct{}{}:
			defer func() { <-p.semaphore }()
		case <-time.After(p.config.ReadTimeout):
			return nil, ErrReadFailed
		}
		start := chunkIdx * int64(p.config.ChunkSize)
		var n int
		var rerr error
		var buf []byte
		for attempt := 0; attempt <= p.config.MaxRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-p.ctx.Done():
					return nil, p.ctx.Err()
				case <-time.After(p.config.RetryInterval):
				}
			}
			tmp := make([]byte, p.config.ChunkSize)
			readCtx, cancel := context.WithTimeout(p.ctx, p.config.ReadTimeout)
			resCh := make(chan struct {
				n   int
				err error
			}, 1)
			go func() {
				nn, ee := p.reader.ReadAt(tmp, start)
				resCh <- struct {
					n   int
					err error
				}{nn, ee}
			}()
			select {
			case <-readCtx.Done():
				cancel()
				n = 0
				rerr = ErrReadFailed
			case r := <-resCh:
				cancel()
				n, rerr = r.n, r.err
			}
			buf = tmp
			if rerr == nil || rerr == io.EOF {
				break
			}
		}
		if rerr != nil && rerr != io.EOF {
			return nil, ErrReadFailed
		}
		ch := &chunk{index: chunkIdx, data: buf, size: n, lastUsed: time.Now()}
		p.cache.addChunk(ch)
		return ch, nil
	})
	if err != nil {
		return nil, err
	}
	return val.(*chunk), nil
}

func (p *prefetcher) Size() int64 {
	return p.size
}

func (p *prefetcher) Close() error {
	if p.closed.Swap(true) {
		return nil
	}

	p.cancelFunc()
	p.wg.Wait()
	p.cache.clear()

	return nil
}
