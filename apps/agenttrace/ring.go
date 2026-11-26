// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttrace

import (
	"sync"
	"sync/atomic"
)

// BackpressureMode controls ring buffer behavior when full.
type BackpressureMode string

const (
	// BackpressureDropNewest never blocks the agent execution hot path: when the
	// ring buffer is full, incoming spans are dropped and DroppedSpans is incremented.
	BackpressureDropNewest BackpressureMode = "drop_newest"

	// BackpressureBlock stalls producers until space is drained by the exporter.
	// Useful only for negative-regime latency comparison; never default in production.
	BackpressureBlock BackpressureMode = "block"
)

// RingStats reports bounded ring buffer occupancy and drop telemetry.
type RingStats struct {
	Capacity      int    `json:"capacity"`
	Buffered      int    `json:"buffered"`
	HighWaterMark int    `json:"highWaterMark"`
	EnqueuedSpans uint64 `json:"enqueuedSpans"`
	DroppedSpans  uint64 `json:"droppedSpans"`
	DrainedSpans  uint64 `json:"drainedSpans"`
}

// RingBuffer is a bounded, concurrency-safe circular queue of completed Spans
// supporting non-blocking push (default) and batch drain in one lock acquisition.
type RingBuffer struct {
	mu       sync.Mutex
	notFull  *sync.Cond
	buf      []Span
	head     int
	tail     int
	count    int
	capacity int
	closed   bool
	mode     BackpressureMode

	highWater atomic.Int64
	enqueued  atomic.Uint64
	dropped   atomic.Uint64
	drained   atomic.Uint64
}

// NewRingBuffer allocates a bounded ring buffer with the given slot capacity.
func NewRingBuffer(capacity int, mode BackpressureMode) *RingBuffer {
	if capacity <= 0 {
		capacity = 4096
	}
	if mode != BackpressureBlock {
		mode = BackpressureDropNewest
	}
	r := &RingBuffer{
		buf:      make([]Span, capacity),
		capacity: capacity,
		mode:     mode,
	}
	r.notFull = sync.NewCond(&r.mu)
	return r
}

// Push enqueues sp into the ring buffer. Under BackpressureDropNewest (default),
// Push never blocks: if full or closed, it returns (false, currentCount) and
// increments DroppedSpans.
func (r *RingBuffer) Push(sp Span) (accepted bool, buffered int) {
	r.mu.Lock()
	if r.mode == BackpressureBlock {
		for r.count == r.capacity && !r.closed {
			r.notFull.Wait()
		}
	}
	if r.closed || r.count == r.capacity {
		c := r.count
		r.mu.Unlock()
		r.dropped.Add(1)
		return false, c
	}
	r.buf[r.tail] = sp
	r.tail++
	if r.tail == r.capacity {
		r.tail = 0
	}
	r.count++
	c := r.count
	r.mu.Unlock()

	r.enqueued.Add(1)
	for {
		hw := r.highWater.Load()
		if int64(c) <= hw || r.highWater.CompareAndSwap(hw, int64(c)) {
			break
		}
	}
	return true, c
}

// DrainBatch removes up to max spans into dst and returns the drained slice.
func (r *RingBuffer) DrainBatch(dst []Span, max int) []Span {
	if max <= 0 {
		return dst[:0]
	}
	r.mu.Lock()
	n := r.count
	if n == 0 {
		r.mu.Unlock()
		return dst[:0]
	}
	if n > max {
		n = max
	}
	if cap(dst) < n {
		dst = make([]Span, 0, n)
	} else {
		dst = dst[:0]
	}
	first := r.capacity - r.head
	if first > n {
		first = n
	}
	dst = append(dst, r.buf[r.head:r.head+first]...)
	// Clear references in ring slots so GC can reclaim Span attribute slices promptly.
	clear(r.buf[r.head : r.head+first])
	rem := n - first
	if rem > 0 {
		dst = append(dst, r.buf[0:rem]...)
		clear(r.buf[0:rem])
		r.head = rem
	} else {
		r.head += first
		if r.head == r.capacity {
			r.head = 0
		}
	}
	r.count -= n
	if r.mode == BackpressureBlock {
		r.notFull.Broadcast()
	}
	r.mu.Unlock()

	r.drained.Add(uint64(n))
	return dst
}

// Len returns the current number of buffered spans.
func (r *RingBuffer) Len() int {
	r.mu.Lock()
	c := r.count
	r.mu.Unlock()
	return c
}

// Close marks the ring closed and wakes any blocked producers.
func (r *RingBuffer) Close() {
	r.mu.Lock()
	r.closed = true
	r.notFull.Broadcast()
	r.mu.Unlock()
}

// Stats returns an instantaneous snapshot of ring buffer telemetry.
func (r *RingBuffer) Stats() RingStats {
	r.mu.Lock()
	c := r.count
	r.mu.Unlock()
	return RingStats{
		Capacity:      r.capacity,
		Buffered:      c,
		HighWaterMark: int(r.highWater.Load()),
		EnqueuedSpans: r.enqueued.Load(),
		DroppedSpans:  r.dropped.Load(),
		DrainedSpans:  r.drained.Load(),
	}
}
