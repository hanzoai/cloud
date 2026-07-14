// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build datastore_live

// Live end-to-end proof of the capture plane against a REAL datastore
// (ClickHouse). It drives the ACTUAL POST /v1/analytics handler (bind → normalize
// → EnsureEventsTable → DatastoreExec) and then reads the rows back through the
// EXACT SQL the /v1/analytics/overview + /top handlers run — so a green run proves
// "emit → hanzo.events row lands → analytics read lens sees it".
//
// Run:
//
//	DATASTORE_ADDR=127.0.0.1:9000 DATASTORE_DB=hanzo \
//	  go test -tags datastore_live -run TestLiveCaptureRoundTrip -v ./clients/analytics/
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

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func liveApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("live")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("live")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func TestLiveCaptureRoundTrip(t *testing.T) {
	// Connect the shared datastore client from DATASTORE_ADDR (async; poll ready).
	aiobject.InitDatastore()
	deadline := time.Now().Add(20 * time.Second)
	for !aiobject.DatastoreEnabled() && time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
	}
	if !aiobject.DatastoreEnabled() {
		t.Fatal("datastore did not connect (set DATASTORE_ADDR=127.0.0.1:9000 with a live ClickHouse)")
	}
	ctx := context.Background()

	// Isolate this run so re-runs are deterministic: use a unique org and drop any
	// prior rows for it.
	org := "acme-live-" + strings.ReplaceAll(time.Now().UTC().Format("150405.000"), ".", "")
	if err := EnsureEventsTable(ctx); err != nil {
		t.Fatalf("EnsureEventsTable: %v", err)
	}

	app := liveApp(t)

	// A realistic session: first-touch attribution + the full signup funnel +
	// upgrade-intent + a plan purchase, exactly as the products will emit them.
	now := time.Now().UTC().Format(time.RFC3339)
	body := `{"batch":[
	  {"type":"pageview","event":"$pageview","distinctId":"anon-1","sessionId":"s1","product":"console","path":"/","referrer":"https://google.com/","utm":{"source":"google","medium":"cpc","campaign":"launch"},"refCode":"REF123","channel":"paid","timestamp":"` + now + `"},
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
	t.Logf("capture receipt: accepted=%d dropped=%d", res.Accepted, res.Dropped)
	if res.Accepted != 10 {
		t.Fatalf("accepted = %d, want 10", res.Accepted)
	}

	// ClickHouse MergeTree inserts are visible immediately to a direct SELECT.
	// 1) Raw landing proof: per-event counts for THIS org.
	rows, err := aiobject.DatastoreQuery(ctx,
		"SELECT event, count() AS n FROM hanzo.events WHERE tenant_id = ? GROUP BY event ORDER BY event", org)
	if err != nil {
		t.Fatalf("readback query: %v", err)
	}
	t.Logf("── hanzo.events landed rows (tenant_id=%s) ──", org)
	total := 0
	for _, r := range rows {
		n := aInt64(r["n"])
		total += int(n)
		t.Logf("  %-20s %d", aString(r["event"]), n)
	}
	if total != 10 {
		t.Fatalf("total rows for org = %d, want 10", total)
	}

	// 2) Privacy proof: the scrubbed signup_submitted row must NOT contain the
	//    password or the raw email anywhere in its stored properties.
	pr, err := aiobject.DatastoreQuery(ctx,
		"SELECT properties FROM hanzo.events WHERE tenant_id = ? AND event = 'signup_submitted'", org)
	if err != nil || len(pr) == 0 {
		t.Fatalf("props readback: %v", err)
	}
	props := aString(pr[0]["properties"])
	t.Logf("signup_submitted stored properties: %s", props)
	if strings.Contains(props, "should-be-scrubbed") || strings.Contains(props, "z@hanzo.ai") || strings.Contains(strings.ToLower(props), "password") {
		t.Fatalf("PRIVACY LEAK: secret/PII persisted in properties: %s", props)
	}
	if !strings.Contains(props, "plan_interest") {
		t.Fatalf("legit property dropped: %s", props)
	}

	// 3) Read-lens proof: run the EXACT overview events SQL the /v1/analytics
	//    handler runs, scoped to this org, over a wide window.
	start := time.Now().Add(-time.Hour)
	end := time.Now().Add(time.Hour)
	where, args := eventsWhere(org, start, end)
	overSQL := "SELECT countIf(event = '$pageview') AS pageviews, uniqExact(distinct_id) AS visitors, " +
		"uniqExact(session_id) AS sessions, countIf(event = 'order_completed') AS orders, " +
		"toFloat64(sum(revenue)) AS revenue FROM " + eventsTable + " WHERE " + where
	orows, err := aiobject.DatastoreQuery(ctx, overSQL, args...)
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
	// Build the WebOverview/CommerceOverview via the SAME pure assemblers the
	// handler uses, to prove the row shape feeds the response types cleanly.
	web := buildWebOverview(o, true)
	com := buildCommerceOverview(o, true)
	t.Logf("  WebOverview=%+v", web)
	t.Logf("  CommerceOverview=%+v", com)

	// 4) Top-products lens.
	pwhere, pargs := eventsWhere(org, start, end)
	prodSQL := "SELECT product_id AS productId, countIf(event = 'order_completed') AS orders, " +
		"toFloat64(sum(revenue)) AS revenue, sum(quantity) AS units FROM " + eventsTable +
		" WHERE " + pwhere + " AND product_id != '' GROUP BY product_id ORDER BY revenue DESC LIMIT 10"
	prows, err := aiobject.DatastoreQuery(ctx, prodSQL, pargs...)
	if err != nil {
		t.Fatalf("top-products query: %v", err)
	}
	tp := buildTopProducts(prows, true)
	t.Logf("── /v1/analytics/top products (org=%s) ── %+v", org, tp.Items)
	if len(tp.Items) != 1 || tp.Items[0].ProductID != "plan_pro" || tp.Items[0].Revenue != 49.0 {
		t.Fatalf("top-products mismatch: %+v", tp.Items)
	}

	t.Logf("LIVE E2E OK: 10 events emitted via POST /v1/analytics landed in %s and read back through the analytics lenses", eventsTable)
}

func livePost(t *testing.T, app *zip.App, path, user, org, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", user)
	req.Header.Set("X-Org-Id", org)
	resp, err := app.Fiber().Test(req) // table pre-ensured, so the insert is fast
	if err != nil {
		t.Fatalf("Test POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestLiveAnonymousCapture proves the marketing-site path: an ANONYMOUS pageview
// (no principal) posted to a recognized brand Host lands under that brand's public
// org, resolved server-side — never from a client field.
func TestLiveAnonymousCapture(t *testing.T) {
	aiobject.InitDatastore()
	deadline := time.Now().Add(20 * time.Second)
	for !aiobject.DatastoreEnabled() && time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
	}
	if !aiobject.DatastoreEnabled() {
		t.Fatal("datastore did not connect")
	}
	ctx := context.Background()
	if err := EnsureEventsTable(ctx); err != nil {
		t.Fatalf("EnsureEventsTable: %v", err)
	}
	app := liveApp(t)

	// A unique marker in properties lets us find exactly this run's row.
	marker := "anon-" + time.Now().UTC().Format("150405.000")
	body := `{"batch":[{"type":"pageview","event":"$pageview","distinctId":"visitor-x","product":"site","path":"/","properties":{"marker":"` + marker + `"}}]}`

	req := httptest.NewRequest(http.MethodPost, "/v1/analytics", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "hanzo.ai" // recognized brand host; NO principal headers
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("anon POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anon capture = %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	rows, err := aiobject.DatastoreQuery(ctx,
		"SELECT tenant_id, event, product FROM hanzo.events WHERE JSONExtractString(properties,'marker') = ?", marker)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("anon rows = %d, want 1", len(rows))
	}
	tenant := aString(rows[0]["tenant_id"])
	t.Logf("anonymous pageview landed: tenant_id=%q event=%q product=%q",
		tenant, aString(rows[0]["event"]), aString(rows[0]["product"]))
	if tenant != "hanzo" {
		t.Fatalf("anon tenant = %q, want hanzo (brand from Host)", tenant)
	}
}
