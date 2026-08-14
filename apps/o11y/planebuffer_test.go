// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

// rowRecorder counts WRITE CALLS — the number this whole design exists to
// reduce — and the rows each call carried.
type rowRecorder struct {
	mu    sync.Mutex
	calls int
	rows  []int
	err   error
}

func (r *rowRecorder) write(_ context.Context, rows [][]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.rows = append(r.rows, len(rows))
	return r.err
}

func (r *rowRecorder) seen() (int, []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, append([]int(nil), r.rows...)
}

func testRows(n int) [][]any {
	rows := make([][]any, n)
	for i := range rows {
		rows[i] = []any{i}
	}
	return rows
}

// The headline: many small batches become ONE statement. This is the ratio the
// store's ceiling is made of — 20-row statements at 163 ms put ~9 of them
// resident at once, and nothing downstream survives that.
func TestRowBufferCoalescesManyBatchesIntoOneWrite(t *testing.T) {
	rec := &rowRecorder{}
	b := newRowBuffer("test", rec.write, time.Hour, 10000, luxlog.NewNoOpLogger())

	for i := 0; i < 100; i++ {
		if err := b.add(context.Background(), testRows(20)); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if calls, _ := rec.seen(); calls != 0 {
		t.Fatalf("nothing should have been written before a flush trips; got %d writes", calls)
	}
	b.Close() // flushes

	calls, rows := rec.seen()
	if calls != 1 {
		t.Fatalf("want 1 write for 100 batches, got %d (%v)", calls, rows)
	}
	if rows[0] != 2000 {
		t.Fatalf("want all 2000 rows in the one write, got %d", rows[0])
	}
}

// The memory bound. A burst past the cap must write inline rather than hold
// unbounded rows, because the cap is the only thing standing between a sender
// storm and the pod's ceiling.
func TestRowBufferFlushesInlineWhenFull(t *testing.T) {
	rec := &rowRecorder{}
	b := newRowBuffer("test", rec.write, time.Hour, 100, luxlog.NewNoOpLogger())
	defer b.Close()

	for i := 0; i < 10; i++ {
		if err := b.add(context.Background(), testRows(20)); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	calls, rows := rec.seen()
	if calls != 2 {
		t.Fatalf("want 2 inline writes at a 100-row cap over 200 rows, got %d (%v)", calls, rows)
	}
	for _, n := range rows {
		if n < 100 {
			t.Fatalf("an inline flush must carry at least the cap, got %v", rows)
		}
	}
}

// The staleness bound. Rows must land on the interval without a full buffer,
// because the fleet is usually quieter than the cap.
func TestRowBufferFlushesOnInterval(t *testing.T) {
	rec := &rowRecorder{}
	b := newRowBuffer("test", rec.write, 20*time.Millisecond, 10000, luxlog.NewNoOpLogger())
	defer b.Close()

	if err := b.add(context.Background(), testRows(5)); err != nil {
		t.Fatalf("add: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls, _ := rec.seen(); calls > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("interval flush never wrote")
}

// Shutdown must not drop what it is already holding — that is the whole reason
// Close flushes before the sink closes.
func TestRowBufferCloseFlushesWhatItHolds(t *testing.T) {
	rec := &rowRecorder{}
	b := newRowBuffer("test", rec.write, time.Hour, 10000, luxlog.NewNoOpLogger())
	if err := b.add(context.Background(), testRows(7)); err != nil {
		t.Fatalf("add: %v", err)
	}
	b.Close()

	calls, rows := rec.seen()
	if calls != 1 || rows[0] != 7 {
		t.Fatalf("Close must flush its held rows; got %d writes %v", calls, rows)
	}
}

// A failing store must not turn an ingest problem into an out-of-memory one:
// the rows are dropped and reported, never retained and retried.
func TestRowBufferDropsOnWriteFailureRatherThanGrow(t *testing.T) {
	rec := &rowRecorder{err: errors.New("store refusing writes")}
	b := newRowBuffer("test", rec.write, time.Hour, 50, luxlog.NewNoOpLogger())
	defer b.Close()

	for i := 0; i < 6; i++ {
		if err := b.add(context.Background(), testRows(50)); err != nil {
			t.Fatalf("add must stay fail-soft toward the receiver, got %v", err)
		}
	}
	b.mu.Lock()
	held := len(b.pending)
	b.mu.Unlock()
	if held > 50 {
		t.Fatalf("a failing write must not accumulate a backlog; holding %d rows", held)
	}
}

// An empty batch must cost nothing — the receiver hands these over routinely.
func TestRowBufferIgnoresEmpty(t *testing.T) {
	rec := &rowRecorder{}
	b := newRowBuffer("test", rec.write, time.Hour, 10, luxlog.NewNoOpLogger())
	if err := b.add(context.Background(), nil); err != nil {
		t.Fatalf("add: %v", err)
	}
	b.Close()
	if calls, _ := rec.seen(); calls != 0 {
		t.Fatalf("empty batches must not produce a statement; got %d", calls)
	}
}
