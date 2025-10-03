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
	"sync"
	"time"
)

type chunk struct {
	index    int64
	data     []byte
	size     int
	lastUsed time.Time
}

type intervalNode struct {
	start, end int64
	chunks     map[int64]*chunk
	left       *intervalNode
	right      *intervalNode
	max        int64
}

type chunkCache struct {
	mutex        sync.RWMutex
	chunkSize    int
	intervalTree *intervalNode
	chunkMap     map[int64]*chunk
	maxCacheSize int
	evictCount   int
	minIndex     int64
}

func newChunkCache(chunkSize int) *chunkCache {
	maxCacheSize := 1024
	return &chunkCache{
		chunkSize:    chunkSize,
		intervalTree: nil,
		chunkMap:     make(map[int64]*chunk),
		maxCacheSize: maxCacheSize,
		evictCount:   maxCacheSize / 10,
		minIndex:     0,
	}
}

func (c *chunkCache) hasChunk(index int64) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	_, exists := c.chunkMap[index]
	return exists
}

func (c *chunkCache) getChunk(index int64) *chunk {
	c.mutex.RLock()
	ch, exists := c.chunkMap[index]
	if exists {
		c.mutex.RUnlock()
		c.mutex.Lock()
		if ch, exists := c.chunkMap[index]; exists {
			ch.lastUsed = time.Now()
		}
		c.mutex.Unlock()
		return ch
	}
	c.mutex.RUnlock()

	return nil
}

func (c *chunkCache) addChunk(ch *chunk) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if ch.index < c.minIndex {
		return
	}
	ch.lastUsed = time.Now()
	c.chunkMap[ch.index] = ch

	if c.intervalTree == nil {
		c.intervalTree = &intervalNode{
			start:  ch.index,
			end:    ch.index,
			chunks: map[int64]*chunk{ch.index: ch},
			max:    ch.index,
		}
	} else {
		c.insertInterval(c.intervalTree, ch.index, ch.index, ch)
	}
	if len(c.chunkMap) > c.maxCacheSize {
		c.evictOldest()
	}
}

func (c *chunkCache) insertInterval(node *intervalNode, start, end int64, ch *chunk) {
	if start <= node.start {
		if node.left == nil {
			node.left = &intervalNode{
				start:  start,
				end:    end,
				chunks: map[int64]*chunk{ch.index: ch},
				max:    end,
			}
		} else {
			c.insertInterval(node.left, start, end, ch)
			if node.left.max > node.max {
				node.max = node.left.max
			}
		}
	} else {
		if node.right == nil {
			node.right = &intervalNode{
				start:  start,
				end:    end,
				chunks: map[int64]*chunk{ch.index: ch},
				max:    end,
			}
		} else {
			c.insertInterval(node.right, start, end, ch)
			if node.right.max > node.max {
				node.max = node.right.max
			}
		}
	}

	if end > node.max {
		node.max = end
	}

	if !(end < node.start || start > node.end) {
		if node.chunks == nil {
			node.chunks = make(map[int64]*chunk)
		}
		node.chunks[ch.index] = ch
	}
}

func (c *chunkCache) evictBefore(minIdx int64) {
	c.mutex.RLock()
	if minIdx <= c.minIndex {
		c.mutex.RUnlock()
		return
	}
	c.mutex.RUnlock()

	c.mutex.Lock()
	defer c.mutex.Unlock()
	if minIdx <= c.minIndex {
		return
	}
	c.minIndex = minIdx
	for idx := range c.chunkMap {
		if idx < minIdx {
			delete(c.chunkMap, idx)
		}
	}
}

func (c *chunkCache) findOverlappingChunks(start, end int64) []*chunk {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	result := make([]*chunk, 0)
	if c.intervalTree == nil {
		return result
	}

	c.findOverlapsRecursive(c.intervalTree, start, end, &result)
	return result
}

func (c *chunkCache) findOverlapsRecursive(node *intervalNode, start, end int64, result *[]*chunk) {
	if node == nil || start > node.max {
		return
	}

	c.findOverlapsRecursive(node.left, start, end, result)
	if !(end < node.start || start > node.end) {
		for _, ch := range node.chunks {
			if ch.index >= start && ch.index <= end {
				*result = append(*result, ch)
			}
		}
	}

	if end >= node.start {
		c.findOverlapsRecursive(node.right, start, end, result)
	}
}

func (c *chunkCache) removeChunk(index int64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	delete(c.chunkMap, index)

	if len(c.chunkMap) == 0 {
		c.intervalTree = nil
	}
}

func (c *chunkCache) evictOldest() {
	if len(c.chunkMap) <= c.maxCacheSize-c.evictCount {
		return
	}

	chunks := make([]*chunk, 0, len(c.chunkMap))
	for _, ch := range c.chunkMap {
		chunks = append(chunks, ch)
	}

	for i := 1; i < len(chunks); i++ {
		key := chunks[i]
		j := i - 1
		for j >= 0 && chunks[j].lastUsed.After(key.lastUsed) {
			chunks[j+1] = chunks[j]
			j--
		}
		chunks[j+1] = key
	}

	evictCount := c.evictCount
	if evictCount > len(chunks) {
		evictCount = len(chunks)
	}

	for i := 0; i < evictCount; i++ {
		delete(c.chunkMap, chunks[i].index)
	}

	if float64(len(c.chunkMap))/float64(c.maxCacheSize) < 0.5 {
		c.rebuildIntervalTree()
	}
}

func (c *chunkCache) rebuildIntervalTree() {
	c.intervalTree = nil

	// Re-insert all chunks
	for idx, ch := range c.chunkMap {
		if c.intervalTree == nil {
			c.intervalTree = &intervalNode{
				start:  idx,
				end:    idx,
				chunks: map[int64]*chunk{idx: ch},
				max:    idx,
			}
		} else {
			c.insertInterval(c.intervalTree, idx, idx, ch)
		}
	}
}

func (c *chunkCache) clear() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.chunkMap = make(map[int64]*chunk)
	c.intervalTree = nil
}
