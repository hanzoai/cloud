// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hanzoai/o11y/pkg/zapmetricreceiver"
)

// metricBuffer coalesces incoming metric batches so the datastore takes a
// couple of INSERTs a window instead of one per batch.
//
// WHY THIS EXISTS. The cost of this path is statements, not bytes. Each insert
// into event.metric fans out through the fill_metric_5m and fill_metric_30m
// views, so one insert becomes three parts — and the ZAP wire carries one batch
// per RESOURCE, so insert volume tracks how many OBJECTS the fleet observes
// rather than how much data it produces. Measured on hanzo-k8s 2026-08-08:
// ~518 writes a minute, event.metric_30m past its part ceiling, and Datastore
// answering code 252 to EVERY metric write for an hour — app telemetry that had
// been landing for months, not just the new collectors.
//
// Buffering is the fix that does not cost fidelity: WriteMetricsMany builds
// each batch exactly as WriteMetrics would, so resource attributes and
// fingerprints are untouched and only the statement count changes.
type metricBuffer struct {
	write func(context.Context, []*zapmetricreceiver.MetricBatch) error
	log   *slog.Logger

	// flushEvery bounds staleness, maxBatches bounds memory. Whichever trips
	// first wins.
	flushEvery time.Duration
	maxBatches int

	mu      sync.Mutex
	pending []*zapmetricreceiver.MetricBatch

	stop chan struct{}
	done chan struct{}
}

func newMetricBuffer(
	write func(context.Context, []*zapmetricreceiver.MetricBatch) error,
	flushEvery time.Duration, maxBatches int, log *slog.Logger,
) *metricBuffer {
	b := &metricBuffer{
		write: write, log: log,
		flushEvery: flushEvery, maxBatches: maxBatches,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go b.loop()
	return b
}

// add is the receiver's OnBatch. It must return fast — the ZAP handler is on
// the read path of every sender — so it only appends, and flushes inline solely
// when the buffer is full. A full-buffer flush is back-pressure on ONE sender
// rather than unbounded memory for all of them.
func (b *metricBuffer) add(ctx context.Context, batch *zapmetricreceiver.MetricBatch) error {
	if batch == nil || len(batch.Families) == 0 {
		return nil
	}
	b.mu.Lock()
	b.pending = append(b.pending, batch)
	full := len(b.pending) >= b.maxBatches
	b.mu.Unlock()
	if full {
		b.flush(ctx)
	}
	return nil
}

func (b *metricBuffer) loop() {
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
// retried: the sender has already been told the batch was accepted, and holding
// a growing backlog against a datastore that is refusing writes is how a
// telemetry plane turns an ingest problem into an out-of-memory one. Dropping
// is visible in the freshness of the data; a silent unbounded queue is not.
func (b *metricBuffer) flush(ctx context.Context) {
	b.mu.Lock()
	batches := b.pending
	b.pending = nil
	b.mu.Unlock()
	if len(batches) == 0 {
		return
	}
	if err := b.write(ctx, batches); err != nil {
		b.log.Warn("metric buffer: write failed, batches dropped",
			"batches", len(batches), "err", err)
	}
}

// Close flushes and stops the ticker. Safe to call once.
func (b *metricBuffer) Close() {
	close(b.stop)
	<-b.done
}
