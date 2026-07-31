// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// O11Y LLM-OBSERVABILITY EVENT INGEST — the native-Go write path for
// LLM-observability events (traces / observations / scores), folding the RETIRED
// console-worker (a Node BullMQ→Valkey→Datastore worker) into the one cloud binary.
//
// GROUNDING — why a new SPECIFIC route, not a wire into the embed:
//
//	The embedded o11y runtime (embed.go, github.com/hanzoai/o11y) is our infra-observability engine:
//	it serves INFRA observability (OTLP traces/logs/metrics, dashboards, alerts) and
//	mounts `app.All("/v1/o11y/*")` as a WILDCARD proxy at subsystem order 70. It has
//	NO LLM-observability ingestion surface (no traces/observations/scores writer) —
//	that data is a DIFFERENT product (the console-worker's Datastore tables). So this
//	file adds the MISSING write path — not as its own route: POST /v1/event is
//	the ONE event door (apps/analytics owns it), and this plane installs a
//	cloud.SetObsEventIngest claim that takes the bodies which ARE LLM-obs
//	ingestion batches and declines the rest. The Sentry wire is
//	/v1/event/error — see o11y.go mountEventFamily.
//
//	Fiber matches routes in REGISTRATION ORDER, so a specific /v1/o11y/* route only
//	binds ahead of the order-70 wildcard when it registers BEFORE it — exactly the
//	constraint scope.go documents for /v1/o11y/{logs,metrics,status} at order 69.
//	This subsystem therefore mounts at order 68 (before the wildcard). A route at the
//	OTLP-ingest order (72) would be SWALLOWED by the proxy and never reached.
//
// PIPELINE — POST /v1/event (validated tenant, claimed batches) → parse the event batch →
// group by target Datastore table → batch-insert via the branded
// github.com/hanzo-ds/go client → oversized event bodies overflow to
// object storage, only the blob ref is stored inline. The store is the Datastore;
// datastore-go is verified to bring the ONE ch-go transport line the o11y
// runtime already uses (MVS-unified), so it coexists in the single binary.
//
// Safety posture (this writes a LIVE, SHARED Datastore):
//   - Always mounted as a normal o11y subsystem — NO on/off feature flag. The write
//     path is live wherever O11Y_DATASTORE_DSN is set (the SAME knob embed.go reads);
//     the Datastore is a required DEPENDENCY, not a gate. Inert in prod until the
//     console producer repoints to /v1/event (nothing calls it yet).
//   - Fail-soft: a missing DSN or a construction error logs and returns nil — never
//     blocks cloud boot (mirrors ingest.go / tracesink.go).
//   - Durable hand-off: the flush runs INLINE today (always works, mirroring ai's
//     durable-ingest inline fallback). When cloud.EmbeddedTasks() is wired, the
//     accept path enqueues the batch to the ONE in-process durable engine (no
//     BullMQ, no Valkey) and a registered activity flushes it — that worker is the
//     next reviewed step (a Datastore insert must be a durable Activity, not run in
//     a workflow function).

package o11y

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	ds "github.com/hanzo-ds/go"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
)

const (
	// o11yBlobThresholdEnv overrides the inline-body size cap (bytes). A body larger
	// than this overflows to object storage; the row keeps only the blob ref.
	o11yBlobThresholdEnv = "CLOUD_O11Y_INGEST_BLOB_BYTES"

	// defaultBlobThreshold: 1 MiB. Bodies at or under this stay inline in the row.
	defaultBlobThreshold = 1 << 20
)

// ingestColumns is the row shape written to every target table.
//
// ⚠️ ASSUMED SCHEMA: the exact column set of the traces/observations/scores tables
// could NOT be grounded against the console web's ingestion schema (the worker
// source ships dist-only — worker/src was not in the checkout). This minimal,
// self-describing shape (raw body + provenance) is a faithful placeholder; the real
// per-table columns must be reconciled with the Datastore migration
// (002_llm_observability.sql) before the flag is flipped on. Kept in ONE place so
// that reconciliation is a single edit.
var ingestColumns = []string{"id", "org", "type", "timestamp", "body", "blob_ref"}

// o11yEvent is one LLM-observability ingestion event: a typed envelope whose Body
// is the type-specific payload. Batches arrive as {"batch":[...]}.
type o11yEvent struct {
	// ID is the producer's id for this event; it becomes the row's primary key
	// and the blob key when an oversized body overflows.
	ID string `json:"id"`
	// Type routes the event to its table: trace-create, score-create, or one of
	// the observation kinds (observation-*, span-*, generation-*, event-create).
	// An unrecognised type is dropped, never mis-routed.
	Type string `json:"type"`
	// Timestamp is the producer's event time, stored verbatim.
	Timestamp string `json:"timestamp"`
	// Body is the type-specific payload. A body over the inline cap is written
	// to object storage and the row keeps only the reference.
	Body json.RawMessage `json:"body"`
}

// o11yBatch is the ingestion request envelope ({"batch":[...]}).
type o11yBatch struct {
	// Batch is the events to persist, in one request.
	Batch []o11yEvent `json:"batch"`
}

// eventSink writes coalesced rows to a Datastore table. The real impl is a
// datastore-go prepared batch; tests inject a fake — the ONE seam that keeps
// processBatch pure (no network).
type eventSink interface {
	Insert(ctx context.Context, table string, columns []string, rows [][]any) error
	Close() error
}

// blobStore overflows an oversized event body out of the row, returning a ref
// stored inline. A nil blobStore disables overflow (bodies stay inline).
type blobStore interface {
	Put(ctx context.Context, key string, body []byte) (ref string, err error)
}

// tableForType maps an ingestion event type to its Datastore table. Unknown types
// are dropped (counted, reported) — never mis-routed into the wrong table.
//
// ⚠️ ASSUMED MAPPING: derived from LLM-obs ingestion-API conventions, NOT grounded
// against the console web schema (dist-only). Reconcile with the producer at cutover.
func tableForType(t string) (string, bool) {
	switch {
	case t == "trace-create":
		return "traces", true
	case t == "score-create":
		return "scores", true
	case strings.HasPrefix(t, "observation-"),
		t == "span-create", t == "span-update",
		t == "generation-create", t == "generation-update",
		t == "event-create":
		return "observations", true
	default:
		return "", false
	}
}

// processBatch is the pure core: route each event to its table, overflow oversized
// bodies to the blobStore, and batch-insert per table. Deterministic table order so
// behaviour (and tests) never depend on map iteration. Returns accepted/dropped
// counts. Any sink or blob error aborts and surfaces (the handler fail-soft-logs).
func processBatch(ctx context.Context, org string, events []o11yEvent, sink eventSink, blobs blobStore, blobThreshold int) (accepted, dropped int, err error) {
	byTable := make(map[string][][]any)
	for _, e := range events {
		table, ok := tableForType(e.Type)
		if !ok {
			dropped++
			continue
		}
		body := []byte(e.Body)
		blobRef := ""
		if blobs != nil && blobThreshold > 0 && len(body) > blobThreshold {
			ref, perr := blobs.Put(ctx, blobKey(org, table, e.ID), body)
			if perr != nil {
				return accepted, dropped, fmt.Errorf("o11y ingest: blob overflow (%s/%s): %w", table, e.ID, perr)
			}
			blobRef = ref
			body = nil // the ref replaces the inline body
		}
		byTable[table] = append(byTable[table], []any{e.ID, org, e.Type, e.Timestamp, string(body), blobRef})
		accepted++
	}

	tables := make([]string, 0, len(byTable))
	for t := range byTable {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, table := range tables {
		if ierr := sink.Insert(ctx, table, ingestColumns, byTable[table]); ierr != nil {
			return accepted, dropped, fmt.Errorf("o11y ingest: insert %s: %w", table, ierr)
		}
	}
	return accepted, dropped, nil
}

// blobKey namespaces an overflowed body by org + table + event id.
func blobKey(org, table, id string) string {
	return path.Join("o11y", org, table, id)
}

// blobThreshold resolves the inline-body cap (bytes): the env override when a valid
// non-negative int, else the 1 MiB default. 0 disables overflow (all bodies inline).
func blobThreshold() int {
	if v := strings.TrimSpace(os.Getenv(o11yBlobThresholdEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultBlobThreshold
}

// eventIngestSink pins the live sink for the process so shutdownEventIngest can
// flush/close it, mirroring embed.go's embeddedRuntime and ingest.go's embeddedIngest.
var eventIngestSink eventSink

// mountEventIngest installs the LLM-obs claim on the ONE event door
// (cloud.SetObsEventIngest; the door itself is analytics' POST /v1/event).
// Called by mountO11y (o11y.go) inside the one order-69 `o11y` mount. No feature flag: it mounts whenever its Datastore dependency is
// available. Fail-soft at every branch — a missing DSN or a Datastore construction
// error logs and returns nil, leaving the write path unmounted (the retired
// console-worker is already gone, so "unmounted" is inert — no double-write).
func mountEventIngest(a cloud.Router, deps cloud.Deps) error {
	log := deps.Logger.New("subsystem", "o11y-event-ingest")

	dsn := embeddedDSN()
	if dsn == "" {
		log.Info("o11y event ingest: no Datastore DSN; write path unmounted (needs O11Y_DATASTORE_DSN)")
		return nil
	}

	sink, err := newDatastoreSink(context.Background(), dsn)
	if err != nil {
		log.Warn("o11y event ingest init failed; write path stays off", "err", err)
		return nil // fail-soft
	}
	eventIngestSink = sink

	o := ingestOps{sink: sink, threshold: blobThreshold(), log: log}
	cloud.SetObsEventIngest(o.claim)

	if cloud.EmbeddedTasks() != nil {
		log.Info("o11y event ingest: durable engine present; flush runs inline, durable Activity hand-off is the next reviewed step")
	}
	log.Info("o11y event ingest live", "door", "POST /v1/event (claimed batches)", "sink", "datastore:traces/observations/scores", "blobBytes", blobThreshold())
	return nil
}

// obsBatchOf reports whether body is an LLM-obs ingestion batch and parses it.
// The claim is deliberately strict: a JSON object whose "batch" is a non-empty
// array where EVERY element carries a type tableForType recognises. A product
// CaptureBatch ({"batch":[{"event":…}]}) has no such types and is declined; a
// mixed batch is declined whole (mis-claiming one product event would file it
// in the wrong plane, so ambiguity always loses to the door's own wire).
func obsBatchOf(body []byte) ([]o11yEvent, bool) {
	var probe struct {
		Batch []struct {
			Type string `json:"type"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || len(probe.Batch) == 0 {
		return nil, false
	}
	for _, e := range probe.Batch {
		if _, ok := tableForType(e.Type); !ok {
			return nil, false
		}
	}
	var b o11yBatch
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, false
	}
	return b.Batch, true
}

// claim is the installed cloud.ObsEventIngestFunc: first refusal on every
// authenticated POST /v1/event body. org is the door's server-resolved tenant.
func (o ingestOps) claim(ctx context.Context, org string, body []byte) (accepted, dropped int, claimed bool, err error) {
	events, ok := obsBatchOf(body)
	if !ok {
		return 0, 0, false, nil
	}
	accepted, dropped, err = processBatch(ctx, org, events, o.sink, o.blobs, o.threshold)
	if err != nil {
		o.log.Warn("o11y event ingest flush failed", "org", org, "err", err)
		return 0, 0, true, err
	}
	return accepted, dropped, true, nil
}

// ingestOps binds the sink (+ optional blob overflow) to the typed ingest op. A
// ingestOps binds the sink (+ optional blob overflow) to the claim.
type ingestOps struct {
	sink      eventSink
	blobs     blobStore
	threshold int
	log       luxlog.Logger
}

// shutdownEventIngest closes the Datastore connection so buffered inserts flush
// before exit. Idempotent and nil-safe.
func shutdownEventIngest(_ context.Context) error {
	if eventIngestSink != nil {
		return eventIngestSink.Close()
	}
	return nil
}

// datastoreSink is the real eventSink: a native connection to the Hanzo Datastore
// via the branded github.com/hanzo-ds/go client (the FIRST direct user
// in cloud — this thin wrapper is the whole client). datastore-go brings the ONE
// ch-go transport line the o11y runtime already uses (MVS-unified), so it
// coexists in the single binary.
type datastoreSink struct {
	conn ds.Conn
}

// newDatastoreSink opens (and pings) the native Datastore connection from the DSN.
func newDatastoreSink(ctx context.Context, dsn string) (*datastoreSink, error) {
	opt, err := ds.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse datastore dsn: %w", err)
	}
	conn, err := ds.Open(opt)
	if err != nil {
		return nil, fmt.Errorf("open datastore: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping datastore: %w", err)
	}
	return &datastoreSink{conn: conn}, nil
}

// Insert writes rows to a table as ONE prepared native batch.
func (s *datastoreSink) Insert(ctx context.Context, table string, columns []string, rows [][]any) error {
	query := "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ")"
	batch, err := s.conn.PrepareBatch(ctx, query)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, row := range rows {
		if err := batch.Append(row...); err != nil {
			return fmt.Errorf("append row: %w", err)
		}
	}
	return batch.Send()
}

// Close releases the native connection.
func (s *datastoreSink) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}
