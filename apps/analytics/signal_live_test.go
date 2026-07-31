// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build datastore_live

// EVERY SIGNAL, ONE DOOR — the live proof that the plane's four tables are reachable
// from the one ingest contract, and that each fact lands in exactly ONE of them.
//
// capture_live_test.go proves the product-event and the error legs. This file closes
// the pair the reads depend on and that nothing had yet been shown to fill: event.log
// and event.span. The signal is `type` on the wire; routeOf (fact.go) is the whole
// routing rule; the writer table for that signal is the whole storage rule. Nothing
// in between gets an opinion, which is why one door can serve four tables.
//
// It also pins the property the separate ORDER BYs exist for: a log is read by
// (org, service, time) and a span by (org, trace_id, time), so both carry the columns
// those keys need — and a fact that landed in two tables would mean the signal
// stopped picking one.
//
// Run (needs both, because both are the plane):
//
//	DATASTORE_ADDR=127.0.0.1:9000 DATASTORE_DB=event \
//	CLOUD_EVENT_NATS_URL=nats://127.0.0.1:4222 \
//	  go test -tags datastore_live -run TestLiveEverySignal -v ./apps/analytics/
package analytics

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
)

// TestLiveEverySignalLandsInItsOwnTable drives one fact of EACH signal through the
// SAME door and asserts each landed in its own table and nowhere else.
func TestLiveEverySignalLandsInItsOwnTable(t *testing.T) {
	ctx := liveReady(t)
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
	code, resp := livePost(t, app, "/v1/event", "u-1", org, body)
	if code != http.StatusOK {
		t.Fatalf("POST /v1/event = %d (%s)", code, resp)
	}
	t.Logf("ingest receipt: %s", strings.TrimSpace(string(resp)))

	// Each signal's own table, with the columns its ORDER BY is built on.
	logRows := awaitRows(t, ctx, 1,
		"SELECT name, service, severity_text, severity_number, body, trace_id, span_id "+
			"FROM "+datastore.Log+" WHERE org = ?", org)
	lr := logRows[0]
	t.Logf("── %s landed ── name=%q service=%q severity=%q body=%q trace=%q",
		datastore.Log, aString(lr["name"]), aString(lr["service"]),
		aString(lr["severity_text"]), aString(lr["body"]), aString(lr["trace_id"]))
	if aString(lr["service"]) != "gateway" {
		t.Fatalf("service = %q, want gateway — it leads event.log's sort key after org", aString(lr["service"]))
	}
	if aString(lr["severity_text"]) != "info" || aInt64(lr["severity_number"]) != 9 {
		t.Fatalf("severity did not land: text=%q number=%v", aString(lr["severity_text"]), lr["severity_number"])
	}
	if aString(lr["body"]) != "served 200 in 12ms" {
		t.Fatalf("body did not land: %q", aString(lr["body"]))
	}

	spanRows := awaitRows(t, ctx, 1,
		"SELECT name, kind, service, trace_id, span_id, parent, duration, status "+
			"FROM "+datastore.Span+" WHERE org = ?", org)
	sr := spanRows[0]
	t.Logf("── %s landed ── name=%q kind=%q service=%q trace=%q span=%q parent=%q duration=%v status=%q",
		datastore.Span, aString(sr["name"]), aString(sr["kind"]), aString(sr["service"]),
		aString(sr["trace_id"]), aString(sr["span_id"]), aString(sr["parent"]),
		sr["duration"], aString(sr["status"]))
	if aString(sr["trace_id"]) == "" {
		t.Fatal("no trace_id — it leads event.span's sort key after org, and assembling a trace is the read")
	}
	// kind is the discriminator WITHIN a signal: track|page|identify|group on
	// event.event, the OTel span kind here. One column, not two.
	if aString(sr["kind"]) != "server" {
		t.Fatalf("kind = %q, want server — the span kind IS the envelope's kind column", aString(sr["kind"]))
	}
	if aString(sr["parent"]) != "span-1" || aString(sr["span_id"]) != "span-2" {
		t.Fatalf("span identity did not land: id=%q parent=%q", aString(sr["span_id"]), aString(sr["parent"]))
	}
	// status is a small NAMED SET, normalized to lower case — `status = 'error'`
	// (datastore.SpanError) says what it means where a status_code = 2 needed a comment.
	if aInt64(sr["duration"]) != 12000000 || aString(sr["status"]) != "ok" {
		t.Fatalf("span outcome did not land: duration=%v status=%q", sr["duration"], aString(sr["status"]))
	}
	// The log and the span share a trace: the correlation the identical envelope buys.
	if aString(lr["trace_id"]) != aString(sr["trace_id"]) {
		t.Fatalf("log trace %q != span trace %q", aString(lr["trace_id"]), aString(sr["trace_id"]))
	}

	// One fact, one table. Landing is a CONSUMER, so each table is awaited before it is
	// counted — reading a count the instant a sibling table landed would be asserting
	// the synchronous insert this design removed, and would fail on the drain's order
	// rather than on anything about the routing.
	for _, table := range []string{datastore.Event, datastore.Error, datastore.Log, datastore.Span} {
		rows := awaitRows(t, ctx, 1, "SELECT id FROM "+table+" WHERE org = ?", org)
		if len(rows) != 1 {
			t.Fatalf("%s has %d rows for org %s, want exactly 1 — a signal did not pick exactly one table", table, len(rows), org)
		}
	}
	t.Logf("LIVE OK: one door, four signals, four tables — event/error/log/span each landed exactly one row (org=%s)", org)
}
