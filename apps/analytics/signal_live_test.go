// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build datastore_live

// EVERY SIGNAL, ONE DOOR — the live proof that each kind of fact is reachable from
// the one ingest contract and lands as exactly ONE row under its own signal.
//
// capture_live_test.go proves the act leg end-to-end (emit → fact → row → read
// lens). This file closes the set: error, log and span driven through the SAME
// door in one batch. The signal is `type` on the wire; routeOf (fact.go) is the
// whole routing rule; the writers table (warehouse.go) is the whole storage rule.
// Nothing in between gets an opinion, which is why one door serves every signal.
//
// It also pins what the per-signal reads depend on: a log is read by
// (org, service, time) and a span assembled by trace, so the row carries service
// and severity for the log and trace/span/parent/kind/status/duration for the
// span — and the two share a trace_id, the correlation the identical envelope
// buys. One fact never lands twice: the batch's four events produce exactly four
// rows, one per signal.
//
// Run:
//
//	DATASTORE_ADDR=127.0.0.1:9000 DATASTORE_DB=hanzo \
//	  go test -tags datastore_live -run TestLiveEverySignal -v ./apps/analytics/
package analytics

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
)

// TestLiveEverySignalLandsItsOwnRow drives one fact of EACH landable signal
// through the one door and asserts each landed as one row under its own signal.
func TestLiveEverySignalLandsItsOwnRow(t *testing.T) {
	ready, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := datastore.Wait(ready); err != nil {
		t.Fatalf("datastore did not connect (set DATASTORE_ADDR=127.0.0.1:9000 with a live Datastore): %v", err)
	}
	ctx := context.Background()
	requirePlane(t, ctx)
	landDirect(t)

	org := "acme-sig-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000"), ".", "")
	app := liveApp(t)

	// One batch, four signals. The wire says which; nothing else does.
	body := `{"batch":[
	  {"type":"page","event":"page_viewed","distinctId":"u-1","product":"console","path":"/pricing"},
	  {"type":"error","distinctId":"u-1","product":"console","service":"web",
	   "error":{"type":"RangeError","message":"out of range","stack":"at f (https://app.test/a.js:2:1)"}},
	  {"type":"log","event":"request.served","distinctId":"u-1","product":"console",
	   "service":"gateway","traceId":"trace-abc","spanId":"span-1",
	   "log":{"severity":"info","number":9,"body":"served 200 in 12ms"}},
	  {"type":"span","event":"GET /v1/event","distinctId":"u-1","product":"console",
	   "service":"gateway",
	   "span":{"trace":"trace-abc","id":"span-2","parent":"span-1",
	           "kind":"server","duration":12000000,"status":"OK"}}
	]}`
	code, resp := livePost(t, app, canonDoor, "u-1", org, body)
	if code != http.StatusOK {
		t.Fatalf("POST %s = %d (%s)", canonDoor, code, resp)
	}
	t.Logf("ingest receipt: %s", strings.TrimSpace(string(resp)))

	row := func(signal string) map[string]any {
		t.Helper()
		rows, err := datastore.Query(ctx,
			"SELECT name, kind, message, severity, duration, service, trace_id, span_id, parent, status, class, issue "+
				"FROM "+factTable+" WHERE org = ? AND signal = ?", org, signal)
		if err != nil {
			t.Fatalf("readback %s: %v", signal, err)
		}
		if len(rows) != 1 {
			t.Fatalf("signal %q has %d rows for org %s, want exactly 1 — a fact did not land, or landed twice", signal, len(rows), org)
		}
		return rows[0]
	}

	act := row("act")
	t.Logf("── act landed ── name=%q kind=%q", aString(act["name"]), aString(act["kind"]))
	if aString(act["name"]) != "page_viewed" || aString(act["kind"]) != "page" {
		t.Fatalf("act row mismatch: name=%q kind=%q", aString(act["name"]), aString(act["kind"]))
	}

	er := row("error")
	t.Logf("── error landed ── class=%q message=%q issue=%q", aString(er["class"]), aString(er["message"]), aString(er["issue"]))
	if aString(er["class"]) != "RangeError" || aString(er["message"]) != "out of range" {
		t.Fatalf("error columns did not land: class=%q message=%q", aString(er["class"]), aString(er["message"]))
	}
	if aString(er["issue"]) == "" {
		t.Fatal("no issue fingerprint — the error cannot be grouped, and grouping is the read")
	}

	lr := row("log")
	t.Logf("── log landed ── service=%q severity=%v message=%q trace=%q",
		aString(lr["service"]), lr["severity"], aString(lr["message"]), aString(lr["trace_id"]))
	if aString(lr["service"]) != "gateway" {
		t.Fatalf("service = %q, want gateway — the (org, service, time) log read depends on it", aString(lr["service"]))
	}
	if aInt64(lr["severity"]) != 9 {
		t.Fatalf("severity did not land: %v", lr["severity"])
	}
	if aString(lr["message"]) != "served 200 in 12ms" {
		t.Fatalf("log body did not land: %q", aString(lr["message"]))
	}

	sr := row("span")
	t.Logf("── span landed ── kind=%q trace=%q span=%q parent=%q duration=%v status=%q",
		aString(sr["kind"]), aString(sr["trace_id"]), aString(sr["span_id"]),
		aString(sr["parent"]), sr["duration"], aString(sr["status"]))
	if aString(sr["trace_id"]) == "" {
		t.Fatal("no trace_id — assembling the trace is the read")
	}
	// kind is the discriminator WITHIN a signal: track|page|identify|group on an
	// act, the OTel span kind here. One column, not two.
	if aString(sr["kind"]) != "server" {
		t.Fatalf("kind = %q, want server — the span kind IS the envelope's kind column", aString(sr["kind"]))
	}
	if aString(sr["span_id"]) != "span-2" || aString(sr["parent"]) != "span-1" {
		t.Fatalf("span identity did not land: id=%q parent=%q", aString(sr["span_id"]), aString(sr["parent"]))
	}
	// status is a small named set, normalized to lower case, so `status = 'error'`
	// says what it means where a status_code = 2 needed a comment.
	if aInt64(sr["duration"]) != 12000000 || aString(sr["status"]) != "ok" {
		t.Fatalf("span outcome did not land: duration=%v status=%q", sr["duration"], aString(sr["status"]))
	}

	// The log and the span share a trace: the correlation the identical envelope buys.
	if aString(lr["trace_id"]) != aString(sr["trace_id"]) {
		t.Fatalf("log trace %q != span trace %q", aString(lr["trace_id"]), aString(sr["trace_id"]))
	}

	// One fact, one row: four signals in, exactly four rows out.
	total, err := datastore.Query(ctx, "SELECT count() AS n FROM "+factTable+" WHERE org = ?", org)
	if err != nil || len(total) == 0 {
		t.Fatalf("count readback: %v", err)
	}
	if n := aInt64(total[0]["n"]); n != 4 {
		t.Fatalf("%s has %d rows for org %s, want exactly 4 — a signal landed twice or not at all", factTable, n, org)
	}
	t.Logf("LIVE OK: one door, four signals — act/error/log/span each landed exactly one row (org=%s)", org)
}
