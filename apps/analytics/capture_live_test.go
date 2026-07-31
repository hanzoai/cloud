// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build datastore_live

// Live end-to-end proof of the event plane against a REAL bus and a REAL store. It
// drives the ACTUAL POST handler (decode → admission → normalize → publish), lets the
// warehouse consumer land the facts, and then reads them back through the EXACT SQL the
// /v1/analytics handlers run — so a green run proves the whole chain:
//
//	emit -> event.<signal> on the EVENT stream -> the drain -> event.event -> the lens
//
// THE READBACK POLLS, and that is the point rather than a workaround: publish is the
// commit, landing is a consumer, so a fact is durable BEFORE it is queryable. A test
// that asserted the row exists the instant the POST returns would be asserting the
// synchronous insert this design deliberately removed.
//
// Run (needs both, because both are the plane):
//
//	DATASTORE_ADDR=127.0.0.1:9000 DATASTORE_DB=event \
//	CLOUD_EVENT_NATS_URL=nats://127.0.0.1:4222 \
//	  go test -tags datastore_live -run TestLive -v ./apps/analytics/
package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/datastore"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// landWait bounds how long a live test waits for the drain to land a published batch.
const landWait = 30 * time.Second

func liveApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("live")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("live")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// liveReady waits for the store, and fails with the exact env a run needs.
func liveReady(t *testing.T) context.Context {
	t.Helper()
	ready, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := datastore.Wait(ready); err != nil {
		t.Fatalf("store did not connect (set DATASTORE_ADDR=127.0.0.1:9000 against a live store): %v", err)
	}
	return context.Background()
}

// awaitRows polls until the query returns at least want rows or landWait expires. It
// reports what it last saw, so a timeout says how far the chain got rather than only
// that it did not finish.
func awaitRows(t *testing.T, ctx context.Context, want int, sql string, args ...any) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(landWait)
	var last []map[string]any
	for time.Now().Before(deadline) {
		rows, err := datastore.Query(ctx, sql, args...)
		if err != nil {
			t.Fatalf("readback query: %v", err)
		}
		last = rows
		if len(rows) >= want {
			return rows
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("only %d of %d rows landed within %s — the drain is not consuming (%s)", len(last), want, landWait, sql)
	return nil
}

func TestLiveCaptureRoundTrip(t *testing.T) {
	ctx := liveReady(t)

	// Isolate this run so re-runs are deterministic: a unique org per run.
	org := "acme-live-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000"), ".", "")
	app := liveApp(t)

	// A realistic session: first-touch attribution + the full signup funnel +
	// upgrade-intent + a plan purchase, exactly as the products will emit them.
	now := time.Now().UTC().Format(time.RFC3339)
	body := `{"batch":[
	  {"type":"pageview","distinctId":"anon-1","sessionId":"s1","product":"console","path":"/","referrer":"https://google.com/","utm":{"source":"google","medium":"cpc","campaign":"launch"},"refCode":"REF123","channel":"paid","timestamp":"` + now + `"},
	  {"type":"event","event":"signup_viewed","distinctId":"anon-1","sessionId":"s1","product":"console"},
	  {"type":"event","event":"signup_submitted","distinctId":"anon-1","sessionId":"s1","product":"console","properties":{"password":"should-be-scrubbed","email":"z@hanzo.ai","plan_interest":"pro"}},
	  {"type":"event","event":"signup_verified","distinctId":"user-42","sessionId":"s1","product":"console"},
	  {"type":"identify","distinctId":"user-42","personId":"user-42","product":"console","signupWeek":"2026-W28","channel":"paid","refCode":"REF123"},
	  {"type":"event","event":"first_action","distinctId":"user-42","sessionId":"s1","product":"console","properties":{"action":"create_api_key"}},
	  {"type":"event","event":"pricing_viewed","distinctId":"user-42","sessionId":"s1","product":"console"},
	  {"type":"event","event":"plan_clicked","distinctId":"user-42","sessionId":"s1","product":"console","properties":{"plan":"pro"}},
	  {"type":"event","event":"order_completed","distinctId":"user-42","sessionId":"s1","product":"console","productId":"plan_pro","quantity":1,"revenue":49.0,"currency":"usd"},
	  {"type":"event","event":"waitlist_joined","distinctId":"anon-1","sessionId":"s1","product":"console","refCode":"REF123"}
	]}`

	code, respBody := livePost(t, app, "/v1/analytics", "user-42", org, body)
	if code != http.StatusOK {
		t.Fatalf("POST /v1/analytics = %d (%s)", code, respBody)
	}
	var res CaptureResult
	if err := json.Unmarshal(respBody, &res); err != nil {
		t.Fatalf("decode result: %v (%s)", err, respBody)
	}
	t.Logf("ingest receipt: accepted=%d dropped=%d (accepted == durable on the stream)", res.Accepted, res.Dropped)
	if res.Accepted != 10 {
		t.Fatalf("accepted = %d, want 10", res.Accepted)
	}

	// 1) Landing proof: per-name counts for THIS org, once the drain has consumed.
	rows := awaitRows(t, ctx, 9, // 10 events over 9 distinct names (a page view + 8 named)
		"SELECT name, count() AS n FROM "+eventsTable+" WHERE org = ? GROUP BY name ORDER BY name", org)
	t.Logf("── %s landed rows (org=%s) ──", eventsTable, org)
	total := 0
	for _, r := range rows {
		n := aInt64(r["n"])
		total += int(n)
		t.Logf("  %-20s %d", aString(r["name"]), n)
	}
	if total != 10 {
		t.Fatalf("total rows for org = %d, want 10", total)
	}

	// 2) Privacy proof: the scrubbed signup_submitted row must NOT carry the password
	//    or the raw email anywhere in its stored attributes.
	pr, err := datastore.Query(ctx,
		"SELECT attributes FROM "+eventsTable+" WHERE org = ? AND name = 'signup_submitted'", org)
	if err != nil || len(pr) == 0 {
		t.Fatalf("attributes readback: %v", err)
	}
	attrs := attributes(pr[0])
	t.Logf("signup_submitted stored attributes: %v", attrs)
	for k, v := range attrs {
		if strings.Contains(v, "should-be-scrubbed") || strings.Contains(v, "z@hanzo.ai") ||
			strings.Contains(strings.ToLower(k), "password") {
			t.Fatalf("PRIVACY LEAK: secret/PII persisted in attributes: %q=%q", k, v)
		}
	}
	if attrs["plan_interest"] != "pro" {
		t.Fatalf("legit attribute dropped: %v", attrs)
	}

	// 3) Read-lens proof: run the EXACT overview SQL the /v1/analytics handler runs.
	start := time.Now().Add(-time.Hour)
	end := time.Now().Add(time.Hour)
	where, args := eventsWhere(org, start, end)
	overSQL := "SELECT countIf(kind = '" + kindPage + "') AS pageviews, uniqExact(distinct_id) AS visitors, " +
		"uniqExact(session_id) AS sessions, countIf(name = 'order_completed') AS orders, " +
		"sum(toFloat64OrZero(attributes['revenue'])) AS revenue FROM " + eventsTable + " WHERE " + where
	orows, err := datastore.Query(ctx, overSQL, args...)
	if err != nil || len(orows) == 0 {
		t.Fatalf("overview lens query: %v", err)
	}
	o := orows[0]
	t.Logf("── /v1/analytics/overview web+commerce lens (org=%s) ──", org)
	t.Logf("  pageviews=%d visitors=%d sessions=%d orders=%d revenue=%.2f",
		aInt64(o["pageviews"]), aInt64(o["visitors"]), aInt64(o["sessions"]),
		aInt64(o["orders"]), aFloat64(o["revenue"]))
	if aInt64(o["pageviews"]) != 1 || aInt64(o["orders"]) != 1 || aFloat64(o["revenue"]) != 49.0 {
		t.Fatalf("overview lens mismatch: %+v", o)
	}
	t.Logf("  WebOverview=%+v", buildWebOverview(o, true))
	t.Logf("  CommerceOverview=%+v", buildCommerceOverview(o, true))

	// 4) Top-products lens.
	pwhere, pargs := eventsWhere(org, start, end)
	prodSQL := "SELECT attributes['product_id'] AS productId, countIf(name = 'order_completed') AS orders, " +
		"sum(toFloat64OrZero(attributes['revenue'])) AS revenue, " +
		"sum(toUInt64OrZero(attributes['quantity'])) AS units FROM " + eventsTable +
		" WHERE " + pwhere + " AND attributes['product_id'] != '' GROUP BY productId ORDER BY revenue DESC LIMIT 10"
	prows, err := datastore.Query(ctx, prodSQL, pargs...)
	if err != nil {
		t.Fatalf("top-products query: %v", err)
	}
	tp := buildTopProducts(prows, true)
	t.Logf("── /v1/analytics/top products (org=%s) ── %+v", org, tp.Items)
	if len(tp.Items) != 1 || tp.Items[0].ProductID != "plan_pro" || tp.Items[0].Revenue != 49.0 {
		t.Fatalf("top-products mismatch: %+v", tp.Items)
	}

	t.Logf("LIVE E2E OK: 10 events published to the EVENT stream, drained into %s, read back through the lenses", eventsTable)
}

// TestLiveErrorLandsInItsOwnTable is the proof that a signal picks a TABLE: the same
// door, an error payload, and the fact lands in event.error with its class, its
// grouping fingerprint and its FRAMES as queryable columns — the thing an opaque blob
// could not give.
func TestLiveErrorLandsInItsOwnTable(t *testing.T) {
	ctx := liveReady(t)
	org := "acme-err-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000"), ".", "")
	app := liveApp(t)

	body := `{"batch":[{"type":"error","distinctId":"user-9","product":"console","service":"web",
	  "error":{"type":"TypeError","message":"x is not a function",
	           "stack":"at render (https://app.test/main.js:10:5)\n  at boot (chrome-extension://abcd/inpage.js:1:1)"}}]}`
	code, respBody := livePost(t, app, "/v1/event", "user-9", org, body)
	if code != http.StatusOK {
		t.Fatalf("POST /v1/event = %d (%s)", code, respBody)
	}

	rows := awaitRows(t, ctx, 1,
		"SELECT name, class, message, `group`, level, `frames.function` AS fn, `frames.file` AS file, "+
			"`frames.own` AS own FROM "+errorsTable+" WHERE org = ?", org)
	r := rows[0]
	t.Logf("── %s landed (org=%s) ── name=%q class=%q group=%q level=%q",
		errorsTable, org, aString(r["name"]), aString(r["class"]), aString(r["group"]), aString(r["level"]))
	if aString(r["class"]) != "TypeError" || aString(r["message"]) != "x is not a function" {
		t.Fatalf("error columns mismatch: %+v", r)
	}
	if aString(r["group"]) == "" {
		t.Fatal("no grouping fingerprint — `group` leads this table's ORDER BY")
	}
	fn, file := strs(r["fn"]), strs(r["file"])
	if len(fn) != 2 || len(file) != 2 {
		t.Fatalf("frames did not land as columns: fn=%v file=%v", fn, file)
	}
	t.Logf("  frames: %v", fn)
	// The app frame is ours; the extension frame is not.
	if nth(r["own"], 0) != 1 || nth(r["own"], 1) != 0 {
		t.Fatalf("first-party marking wrong: own=%v for %v", r["own"], file)
	}
	// Nothing landed in the product-event table — a signal picks ONE table.
	ev, err := datastore.Query(ctx, "SELECT count() AS n FROM "+eventsTable+" WHERE org = ?", org)
	if err != nil {
		t.Fatalf("cross-table check: %v", err)
	}
	if aInt64(ev[0]["n"]) != 0 {
		t.Fatalf("an error also landed in %s (%d rows) — the signals are not separate", eventsTable, aInt64(ev[0]["n"]))
	}
}

func livePost(t *testing.T, app *zip.App, path, user, org, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", user)
	req.Header.Set("X-Org-Id", org)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestLiveAnonymousCapture proves the marketing-site path: an ANONYMOUS page view (no
// principal) is admitted and lands under the RESERVED PUBLIC TENANT — not under the
// brand the Host names. A Host header does not select a partition.
func TestLiveAnonymousCapture(t *testing.T) {
	ctx := liveReady(t)
	app := liveApp(t)

	// A unique marker lets us find exactly this run's row inside the shared public
	// tenant, which by definition is not isolated by org.
	marker := "anon-" + time.Now().UTC().Format("150405.000")
	body := `{"batch":[{"type":"pageview","distinctId":"visitor-x","product":"site","path":"/","properties":{"marker":"` + marker + `"}}]}`

	req := httptest.NewRequest(http.MethodPost, "/v1/analytics", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "hanzo.ai" // a recognized brand host; NO principal headers
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("anon POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anon capture = %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// The projection strips the caller's property bag, so the marker CANNOT survive —
	// which is the point. Find the row by its server-chosen name under the public
	// tenant instead, and assert the marker is gone.
	rows := awaitRows(t, ctx, 1,
		"SELECT org, name, kind, product, attributes FROM "+eventsTable+
			" WHERE org = ? AND product = 'site' AND time > ? ORDER BY time DESC LIMIT 1",
		publicTenant, tsLiteral(time.Now().Add(-2*time.Minute)))
	r := rows[0]
	t.Logf("anonymous page view landed: org=%q name=%q kind=%q product=%q",
		aString(r["org"]), aString(r["name"]), aString(r["kind"]), aString(r["product"]))
	if got := aString(r["org"]); got != publicTenant {
		t.Fatalf("anon tenant = %q, want %q — a Host header named a tenant", got, publicTenant)
	}
	if aString(r["kind"]) != kindPage {
		t.Fatalf("anon kind = %q, want %q", aString(r["kind"]), kindPage)
	}
	for k, v := range attributes(r) {
		if strings.Contains(v, marker) {
			t.Fatalf("the caller's property bag survived the anonymous projection: %q=%q", k, v)
		}
	}
}
