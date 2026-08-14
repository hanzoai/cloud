// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/o11y/pkg/zapmetricreceiver"
)

func bufQuietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func bufBatch(name string) *zapmetricreceiver.MetricBatch {
	return &zapmetricreceiver.MetricBatch{
		Families: []zapmetricreceiver.MetricFamily{{Name: name, Type: "gauge"}},
	}
}

// writeRecorder counts WRITE CALLS, which is the number the whole design exists to
// reduce, and the batches each call carried.
type writeRecorder struct {
	mu     sync.Mutex
	calls  int
	counts []int
	err    error
}

func (r *writeRecorder) write(_ context.Context, bs []*zapmetricreceiver.MetricBatch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.counts = append(r.counts, len(bs))
	return r.err
}

func (r *writeRecorder) snapshot() (int, []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, append([]int(nil), r.counts...)
}

// TestBufferCoalescesIntoOneWrite is the contract: many batches, ONE write.
// Without it each batch is its own INSERT, and each INSERT is three parts once
// the rollup views fan out — which is what put event.metric_30m over its part
// ceiling and made Datastore reject every metric write for an hour.
func TestBufferCoalescesIntoOneWrite(t *testing.T) {
	r := &writeRecorder{}
	b := newMetricBuffer(r.write, time.Hour, 1000, bufQuietLog()) // ticker must not fire
	for range 50 {
		if err := b.add(context.Background(), bufBatch("m")); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if calls, _ := r.snapshot(); calls != 0 {
		t.Fatalf("nothing should be written before a flush, got %d calls", calls)
	}
	b.Close() // Close must flush what is held
	calls, counts := r.snapshot()
	if calls != 1 || len(counts) != 1 || counts[0] != 50 {
		t.Fatalf("want one write of 50 batches, got %d calls %v", calls, counts)
	}
}

// TestBufferFlushesWhenFull bounds memory: a datastore that stops accepting
// writes must not turn into unbounded growth in the ingest process.
func TestBufferFlushesWhenFull(t *testing.T) {
	r := &writeRecorder{}
	b := newMetricBuffer(r.write, time.Hour, 10, bufQuietLog())
	defer b.Close()
	for range 25 {
		_ = b.add(context.Background(), bufBatch("m"))
	}
	calls, counts := r.snapshot()
	if calls != 2 {
		t.Fatalf("25 batches at max 10 should flush twice, got %d calls %v", calls, counts)
	}
	for _, n := range counts {
		if n != 10 {
			t.Fatalf("a full flush should carry exactly the cap, got %v", counts)
		}
	}
}

// TestBufferFlushesOnTick bounds staleness — the other half of the trade.
func TestBufferFlushesOnTick(t *testing.T) {
	r := &writeRecorder{}
	b := newMetricBuffer(r.write, 20*time.Millisecond, 1000, bufQuietLog())
	defer b.Close()
	_ = b.add(context.Background(), bufBatch("m"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls, _ := r.snapshot(); calls > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ticker never flushed — a quiet sender's metrics would sit forever")
}

// TestBufferSkipsEmptyAndSurvivesWriteError: one dead sender must not discard a
// window of everyone else's metrics, and a failing datastore must not wedge the
// buffer.
func TestBufferSkipsEmptyAndSurvivesWriteError(t *testing.T) {
	r := &writeRecorder{err: context.DeadlineExceeded}
	b := newMetricBuffer(r.write, time.Hour, 1000, bufQuietLog())

	_ = b.add(context.Background(), nil)
	_ = b.add(context.Background(), &zapmetricreceiver.MetricBatch{})
	if calls, _ := r.snapshot(); calls != 0 {
		t.Fatalf("nil/empty batches must not be buffered, got %d calls", calls)
	}

	_ = b.add(context.Background(), bufBatch("m"))
	b.flush(context.Background()) // errors
	if calls, _ := r.snapshot(); calls != 1 {
		t.Fatalf("want the failing write attempted once, got %d", calls)
	}
	// The failed batch is dropped, not retried forever.
	r.err = nil
	b.flush(context.Background())
	if calls, _ := r.snapshot(); calls != 1 {
		t.Fatalf("a dropped batch must not be re-flushed, got %d calls", calls)
	}
	b.Close()
}
