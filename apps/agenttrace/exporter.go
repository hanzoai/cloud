// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttrace

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Sink consumes batched OTLP/JSON payloads and/or structured span slices from
// BatchExporter. Implementations must be concurrency-safe.
type Sink interface {
	ExportBatch(ctx context.Context, spans []Span, otlpJSON []byte) error
}

// SinkFunc adapts a function into a Sink.
type SinkFunc func(ctx context.Context, spans []Span, otlpJSON []byte) error

// ExportBatch implements Sink.
func (f SinkFunc) ExportBatch(ctx context.Context, spans []Span, otlpJSON []byte) error {
	return f(ctx, spans, otlpJSON)
}

// WriterSink writes newline-delimited OTLP/JSON batch payloads to w.
type WriterSink struct {
	mu          sync.Mutex
	w           io.Writer
	syncer      interface{ Sync() error }
	syncOnWrite bool
	bytesOut    atomic.Uint64
}

// NewWriterSink creates a WriterSink writing OTLP/JSON lines to w.
func NewWriterSink(w io.Writer, syncOnWrite bool) *WriterSink {
	s := &WriterSink{w: w, syncOnWrite: syncOnWrite}
	if sy, ok := w.(interface{ Sync() error }); ok {
		s.syncer = sy
	}
	return s
}

// ExportBatch writes one OTLP/JSON payload followed by a newline.
func (s *WriterSink) ExportBatch(_ context.Context, _ []Span, otlpJSON []byte) error {
	if s.w == nil || len(otlpJSON) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.w.Write(otlpJSON)
	if err != nil {
		return err
	}
	if _, err := s.w.Write([]byte{'\n'}); err != nil {
		return err
	}
	s.bytesOut.Add(uint64(n + 1))
	if s.syncOnWrite && s.syncer != nil {
		return s.syncer.Sync()
	}
	return nil
}

// BytesWritten returns the total OTLP/JSON bytes written by this sink.
func (s *WriterSink) BytesWritten() uint64 {
	return s.bytesOut.Load()
}

// MultiSink fans out exported batches to multiple sinks sequentially.
type MultiSink struct {
	Sinks []Sink
}

// ExportBatch delivers the batch to every non-nil sink in Sinks.
func (m *MultiSink) ExportBatch(ctx context.Context, spans []Span, otlpJSON []byte) error {
	var firstErr error
	for _, s := range m.Sinks {
		if s == nil {
			continue
		}
		if err := s.ExportBatch(ctx, spans, otlpJSON); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SlowSink wraps an inner sink with a fixed per-batch export latency to model
// lagging OTLP collectors in overload tests and negative-regime benchmarks.
type SlowSink struct {
	Delay time.Duration
	Inner Sink
}

// ExportBatch sleeps for Delay (or exits early on ctx cancel) before forwarding.
func (s *SlowSink) ExportBatch(ctx context.Context, spans []Span, otlpJSON []byte) error {
	if s.Delay > 0 {
		t := time.NewTimer(s.Delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	if s.Inner != nil {
		return s.Inner.ExportBatch(ctx, spans, otlpJSON)
	}
	return nil
}

// ExporterStats summarizes ring buffer and batch exporter health telemetry.
type ExporterStats struct {
	Ring            RingStats `json:"ring"`
	BatchSize       int       `json:"batchSize"`
	ExportedSpans   uint64    `json:"exportedSpans"`
	ExportedBatches uint64    `json:"exportedBatches"`
	ExportedBytes   uint64    `json:"exportedBytes"`
	ExportErrors    uint64    `json:"exportErrors"`
	FlushCount      uint64    `json:"flushCount"`
	SkipOTLPEncode  bool      `json:"skipOtlpEncode,omitempty"`
}

// BatchExporterConfig configures BatchExporter.
type BatchExporterConfig struct {
	RingCapacity     int
	BatchSize        int
	FlushInterval    time.Duration
	BackpressureMode BackpressureMode
	ServiceName      string
	Sink             Sink
}

// BatchExporter drains spans from a bounded RingBuffer in batches, encodes each
// batch into standard OTLP/JSON, and delivers it to Sink asynchronously so the
// agent hot path never waits on serialization or I/O.
type BatchExporter struct {
	ring          *RingBuffer
	batchSize     int
	flushInterval time.Duration
	serviceName   string
	sink          Sink

	wakeCh  chan struct{}
	flushCh chan chan struct{}
	stopCh  chan struct{}
	doneCh  chan struct{}

	stopOnce sync.Once

	exportedSpans   atomic.Uint64
	exportedBatches atomic.Uint64
	exportedBytes   atomic.Uint64
	exportErrors    atomic.Uint64
	flushCount      atomic.Uint64
}

// NewBatchExporter constructs and starts a background BatchExporter worker.
func NewBatchExporter(cfg BatchExporterConfig) *BatchExporter {
	if cfg.RingCapacity <= 0 {
		cfg.RingCapacity = 4096
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 256
	}
	if cfg.BatchSize > cfg.RingCapacity {
		cfg.BatchSize = cfg.RingCapacity
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 25 * time.Millisecond
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = ServiceName
	}
	e := &BatchExporter{
		ring:          NewRingBuffer(cfg.RingCapacity, cfg.BackpressureMode),
		batchSize:     cfg.BatchSize,
		flushInterval: cfg.FlushInterval,
		serviceName:   cfg.ServiceName,
		sink:          cfg.Sink,
		wakeCh:        make(chan struct{}, 1),
		flushCh:       make(chan chan struct{}, 16),
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
	go e.run()
	return e
}

// Enqueue pushes sp into the bounded ring buffer and signals the exporter
// goroutine once a full batch is ready.
func (e *BatchExporter) Enqueue(sp Span) bool {
	ok, buffered := e.ring.Push(sp)
	if ok && buffered >= e.batchSize {
		select {
		case e.wakeCh <- struct{}{}:
		default:
		}
	}
	return ok
}

// Flush forces an immediate drain of all currently buffered spans and waits
// until the exporter finishes delivering them or ctx expires.
func (e *BatchExporter) Flush(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ack := make(chan struct{})
	select {
	case e.flushCh <- ack:
	case <-e.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-ack:
		return nil
	case <-e.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Shutdown closes the ring buffer, drains all remaining buffered spans, and
// waits for the background worker to exit cleanly.
func (e *BatchExporter) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.stopOnce.Do(func() {
		e.ring.Close()
		close(e.stopCh)
	})
	select {
	case <-e.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stats returns an instantaneous snapshot of exporter & ring buffer telemetry.
func (e *BatchExporter) Stats() ExporterStats {
	return ExporterStats{
		Ring:            e.ring.Stats(),
		BatchSize:       e.batchSize,
		ExportedSpans:   e.exportedSpans.Load(),
		ExportedBatches: e.exportedBatches.Load(),
		ExportedBytes:   e.exportedBytes.Load(),
		ExportErrors:    e.exportErrors.Load(),
		FlushCount:      e.flushCount.Load(),
	}
}

func (e *BatchExporter) run() {
	defer close(e.doneCh)
	ticker := time.NewTicker(e.flushInterval)
	defer ticker.Stop()

	batch := make([]Span, 0, e.batchSize)
	encBuf := bytes.NewBuffer(make([]byte, 0, 32*1024))

	drainAll := func() {
		for {
			batch = e.ring.DrainBatch(batch, e.batchSize)
			if len(batch) == 0 {
				return
			}
			e.exportOne(batch, encBuf)
		}
	}

	for {
		select {
		case <-e.wakeCh:
			for e.ring.Len() >= e.batchSize {
				batch = e.ring.DrainBatch(batch, e.batchSize)
				if len(batch) == 0 {
					break
				}
				e.exportOne(batch, encBuf)
			}
		case <-ticker.C:
			drainAll()
		case ack := <-e.flushCh:
			e.flushCount.Add(1)
			drainAll()
			close(ack)
		case <-e.stopCh:
			drainAll()
			// Ack any pending flushes before exit.
			for {
				select {
				case ack := <-e.flushCh:
					close(ack)
				default:
					return
				}
			}
		}
	}
}

func (e *BatchExporter) exportOne(batch []Span, encBuf *bytes.Buffer) {
	encBuf.Reset()
	AppendOTLPJSON(encBuf, batch, e.serviceName)
	payload := encBuf.Bytes()
	e.exportedBytes.Add(uint64(len(payload)))
	if e.sink != nil {
		// Copy payload bytes if sink might retain them, or pass slice copied cleanly.
		if err := e.sink.ExportBatch(context.Background(), batch, payload); err != nil {
			e.exportErrors.Add(1)
			return
		}
	}
	e.exportedSpans.Add(uint64(len(batch)))
	e.exportedBatches.Add(1)
}
