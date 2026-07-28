// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeSink captures Insert calls so tests assert routing/batching without a
// Datastore. It is the eventSink seam that keeps processBatch pure.
type fakeSink struct {
	inserts []fakeInsert
	err     error
}

type fakeInsert struct {
	table   string
	columns []string
	rows    [][]any
}

func (f *fakeSink) Insert(_ context.Context, table string, columns []string, rows [][]any) error {
	if f.err != nil {
		return f.err
	}
	f.inserts = append(f.inserts, fakeInsert{table: table, columns: columns, rows: rows})
	return nil
}

func (f *fakeSink) Close() error { return nil }

// byTable returns the captured rows for a table (nil if it was never inserted).
func (f *fakeSink) byTable(table string) [][]any {
	for _, in := range f.inserts {
		if in.table == table {
			return in.rows
		}
	}
	return nil
}

// fakeBlob records overflow puts and returns a deterministic ref.
type fakeBlob struct {
	puts     int
	lastKey  string
	lastSize int
	err      error
}

func (f *fakeBlob) Put(_ context.Context, key string, body []byte) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.puts++
	f.lastKey = key
	f.lastSize = len(body)
	return "blob://" + key, nil
}

func ev(id, typ, body string) o11yEvent {
	return o11yEvent{ID: id, Type: typ, Timestamp: "2026-07-10T00:00:00Z", Body: json.RawMessage(body)}
}

func TestTableForType(t *testing.T) {
	for _, tc := range []struct {
		typ   string
		table string
		ok    bool
	}{
		{"trace-create", "traces", true},
		{"score-create", "scores", true},
		{"observation-create", "observations", true},
		{"observation-update", "observations", true},
		{"span-create", "observations", true},
		{"generation-create", "observations", true},
		{"event-create", "observations", true},
		{"unknown-thing", "", false},
		{"", "", false},
	} {
		table, ok := tableForType(tc.typ)
		if table != tc.table || ok != tc.ok {
			t.Errorf("tableForType(%q) = (%q,%v), want (%q,%v)", tc.typ, table, ok, tc.table, tc.ok)
		}
	}
}

func TestProcessBatch_RoutesAndBatches(t *testing.T) {
	sink := &fakeSink{}
	events := []o11yEvent{
		ev("t1", "trace-create", `{"name":"a"}`),
		ev("o1", "observation-create", `{"k":1}`),
		ev("o2", "generation-create", `{"k":2}`),
		ev("s1", "score-create", `{"v":0.5}`),
		ev("x1", "mystery-create", `{}`), // unknown → dropped
	}

	accepted, dropped, err := processBatch(context.Background(), "acme", events, sink, nil, defaultBlobThreshold)
	if err != nil {
		t.Fatalf("processBatch error: %v", err)
	}
	if accepted != 4 || dropped != 1 {
		t.Fatalf("accepted=%d dropped=%d, want 4/1", accepted, dropped)
	}
	if got := len(sink.byTable("traces")); got != 1 {
		t.Errorf("traces rows = %d, want 1", got)
	}
	if got := len(sink.byTable("observations")); got != 2 {
		t.Errorf("observations rows = %d, want 2", got)
	}
	if got := len(sink.byTable("scores")); got != 1 {
		t.Errorf("scores rows = %d, want 1", got)
	}
	// Row shape matches ingestColumns: [id, org, type, timestamp, body, blob_ref].
	row := sink.byTable("traces")[0]
	if len(row) != len(ingestColumns) {
		t.Fatalf("row width = %d, want %d", len(row), len(ingestColumns))
	}
	if row[1] != "acme" {
		t.Errorf("row org = %v, want acme (server-side tenant, not spoofable)", row[1])
	}
	if row[4] != `{"name":"a"}` {
		t.Errorf("row body = %v, want inline JSON", row[4])
	}
	if row[5] != "" {
		t.Errorf("row blob_ref = %v, want empty (under threshold)", row[5])
	}
}

func TestProcessBatch_Empty(t *testing.T) {
	sink := &fakeSink{}
	accepted, dropped, err := processBatch(context.Background(), "acme", nil, sink, nil, defaultBlobThreshold)
	if err != nil || accepted != 0 || dropped != 0 {
		t.Fatalf("empty batch: accepted=%d dropped=%d err=%v", accepted, dropped, err)
	}
	if len(sink.inserts) != 0 {
		t.Errorf("empty batch triggered %d inserts, want 0", len(sink.inserts))
	}
}

func TestProcessBatch_BlobOverflow(t *testing.T) {
	sink := &fakeSink{}
	blobs := &fakeBlob{}
	big := `{"data":"` + string(make([]byte, 2048)) + `"}` // > threshold
	events := []o11yEvent{ev("o1", "observation-create", big)}

	accepted, _, err := processBatch(context.Background(), "acme", events, sink, blobs, 1024)
	if err != nil {
		t.Fatalf("processBatch error: %v", err)
	}
	if accepted != 1 {
		t.Fatalf("accepted=%d, want 1", accepted)
	}
	if blobs.puts != 1 {
		t.Fatalf("blob puts = %d, want 1 (body over threshold must overflow)", blobs.puts)
	}
	row := sink.byTable("observations")[0]
	if row[4] != "" {
		t.Errorf("overflowed row body = %q, want empty (moved to blob)", row[4])
	}
	if row[5] != "blob://"+blobKey("acme", "observations", "o1") {
		t.Errorf("row blob_ref = %v, want the blob key ref", row[5])
	}
}

func TestProcessBatch_BlobDisabled(t *testing.T) {
	// nil blobStore OR threshold 0 ⇒ oversized bodies stay inline, never dropped.
	big := `{"data":"` + string(make([]byte, 4096)) + `"}`
	for _, tc := range []struct {
		name      string
		blobs     blobStore
		threshold int
	}{
		{"nil-store", nil, 1024},
		{"zero-threshold", &fakeBlob{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &fakeSink{}
			_, _, err := processBatch(context.Background(), "acme", []o11yEvent{ev("o1", "observation-create", big)}, sink, tc.blobs, tc.threshold)
			if err != nil {
				t.Fatalf("processBatch error: %v", err)
			}
			row := sink.byTable("observations")[0]
			if row[4] != big {
				t.Errorf("body not kept inline when overflow disabled")
			}
			if row[5] != "" {
				t.Errorf("blob_ref = %v, want empty when overflow disabled", row[5])
			}
		})
	}
}

func TestProcessBatch_SinkError(t *testing.T) {
	sentinel := errors.New("datastore down")
	sink := &fakeSink{err: sentinel}
	_, _, err := processBatch(context.Background(), "acme", []o11yEvent{ev("t1", "trace-create", `{}`)}, sink, nil, defaultBlobThreshold)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapped sentinel", err)
	}
}

func TestProcessBatch_BlobError(t *testing.T) {
	sentinel := errors.New("blob store down")
	blobs := &fakeBlob{err: sentinel}
	big := `{"data":"` + string(make([]byte, 2048)) + `"}`
	_, _, err := processBatch(context.Background(), "acme", []o11yEvent{ev("o1", "observation-create", big)}, &fakeSink{}, blobs, 1024)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapped blob sentinel", err)
	}
}

func TestBlobThreshold(t *testing.T) {
	t.Setenv(o11yBlobThresholdEnv, "")
	if got := blobThreshold(); got != defaultBlobThreshold {
		t.Errorf("default = %d, want %d", got, defaultBlobThreshold)
	}
	t.Setenv(o11yBlobThresholdEnv, "4096")
	if got := blobThreshold(); got != 4096 {
		t.Errorf("override = %d, want 4096", got)
	}
	t.Setenv(o11yBlobThresholdEnv, "0") // 0 = overflow disabled, a valid explicit value
	if got := blobThreshold(); got != 0 {
		t.Errorf("zero = %d, want 0", got)
	}
	t.Setenv(o11yBlobThresholdEnv, "garbage")
	if got := blobThreshold(); got != defaultBlobThreshold {
		t.Errorf("garbage falls back to default = %d, want %d", got, defaultBlobThreshold)
	}
}
