// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"
)

// rowBuffer coalesces rendered rows so the datastore takes a few INSERTs a
// second instead of one per wire batch.
//
// WHY THIS EXISTS. The cost of this path is STATEMENTS, not bytes. The ZAP wire
// carries one batch per RESOURCE per window — about twenty log rows — and this
// sink wrote one INSERT per batch, so statement volume tracked how many pods the
// fleet runs rather than how much they say. Measured on hanzo-k8s over thirty
// minutes: 97,984 INSERTs into event.log averaging 20 rows and 163 ms each,
// which is 8.9 statements resident at every instant. That product is the whole
// ceiling of the path — about a thousand rows a second — and it is reached with
// the store idle: 163 ms is per-statement overhead, not work. Parts were never
// the constraint over that window (163 active parts averaging 5.6M rows, 8
// merges running and keeping up), so larger batches cost the merge scheduler
// nothing.
//
// event.series on the same server, in the same half hour: 820 INSERTs at 18,523
// rows each. That is the shape a write path is supposed to have, and this buffer
// is how logs and spans reach it. Fidelity is untouched — rows are rendered
// exactly as before, and only the statement count moves.
//
// The receiver hands rows over with Send and never waits for a reply, so
// accepting a row here rather than after it lands gives up no delivery
// guarantee that existed. What it does change is the crash window: a cloud that
// dies loses what is held instead of at most one batch. flushEvery is what
// bounds that, and it is set in sub-second territory for exactly this reason.
type rowBuffer struct {
	write func(context.Context, [][]any) error
	log   luxlog.Logger
	what  string

	// flushEvery bounds staleness and the crash window; maxRows bounds memory.
	// Whichever trips first wins.
	flushEvery time.Duration
	maxRows    int

	mu      sync.Mutex
	pending [][]any

	stop chan struct{}
	done chan struct{}
}

func newRowBuffer(
	what string,
	write func(context.Context, [][]any) error,
	flushEvery time.Duration, maxRows int, log luxlog.Logger,
) *rowBuffer {
	b := &rowBuffer{
		what: what, write: write, log: log,
		flushEvery: flushEvery, maxRows: maxRows,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go b.loop()
	return b
}

// add is what a receiver's OnBatch calls. It must return fast — the ZAP handler
// runs ON THE READ LOOP of the sender's connection, so every millisecond spent
// here is a millisecond that connection is not being drained, and a sender that
// misses its own deadline abandons the socket rather than waits. So this only
// appends, and writes inline solely when the buffer is full. A full-buffer flush
// is back-pressure on ONE sender instead of unbounded memory for all of them.
func (b *rowBuffer) add(ctx context.Context, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	b.mu.Lock()
	b.pending = append(b.pending, rows...)
	full := len(b.pending) >= b.maxRows
	b.mu.Unlock()
	if full {
		b.flush(ctx)
	}
	return nil
}

func (b *rowBuffer) loop() {
	defer close(b.done)
	t := time.NewTicker(b.flushEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			b.flush(context.Background())
		case <-b.stop:
			b.flush(context.Background()) // never drop what is already held
			return
		}
	}
}

// flush writes whatever is held. A failed write is LOGGED AND DROPPED, not
// retried: the sender has already been told the rows were accepted, and holding
// a growing backlog against a datastore that is refusing writes is how a
// telemetry plane turns an ingest problem into an out-of-memory one. Dropping is
// visible in the freshness of the data; a silent unbounded queue is not. This is
// the discipline metricsbuffer.go already holds next door.
func (b *rowBuffer) flush(ctx context.Context) {
	b.mu.Lock()
	rows := b.pending
	b.pending = nil
	b.mu.Unlock()
	if len(rows) == 0 {
		return
	}
	if err := b.write(ctx, rows); err != nil {
		b.log.Warn("plane row buffer: write failed, rows dropped",
			"what", b.what, "rows", len(rows), "err", err)
	}
}

// Close flushes and stops the ticker. Safe to call once, and it must be called
// BEFORE the sink it writes through is closed — the final flush needs a live
// connection, and it is the one that turns a graceful shutdown from "lose the
// buffer" into "lose nothing".
func (b *rowBuffer) Close() {
	close(b.stop)
	<-b.done
}
