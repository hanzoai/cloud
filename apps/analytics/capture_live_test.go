// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build datastore_live

// Live end-to-end proof of the capture plane against a REAL datastore.
// It drives the ACTUAL POST /v1/event handler (bind → normalize → publish) with the
// publish seam landing each fact SYNCHRONOUSLY through the SAME per-signal writers
// the sink uses (warehouse.go) — the bus hop is the one piece substituted, because a
// live NATS is not part of this harness — and then reads the rows back through the
// EXACT SQL the /v1/analytics/overview + /top handlers run. So a green run proves
// "emit → fact → event.event row lands → analytics read lens sees it".
//
// The plane's DDL is OWNED by hanzoai/o11y and this package never creates it, so the
// run SKIPS (not fails) when event.event is absent — the honest statement that the
// deployment under test has no plane provisioned.
//
// Run:
//
//	DATASTORE_ADDR=127.0.0.1:9000 DATASTORE_DB=hanzo \
//	  go test -tags datastore_live -run TestLiveCaptureRoundTrip -v ./apps/analytics/
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

func liveApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("live")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("live")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	stopSink() // see mountApp: a test process holds no live consumer
	return app
}

// landDirect substitutes the publish seam so every fact lands through its writer's
// REAL statement against the REAL datastore, synchronously — the exact INSERT the
// drain executes, minus the bus in between. Restored via t.Cleanup.
func landDirect(t *testing.T) {
	t.Helper()
	bySignal := make(map[signal]writer, len(writers))
	for _, w := range writers {
		bySignal[w.signal] = w
	}
	prev := publish
	publish = func(ctx context.Context, facts []fact) error {
		for _, f := range facts {
			w, ok := bySignal[f.signal]
			if !ok {
				t.Fatalf("no writer for signal %q", f.signal)
			}
			if err := w.write(ctx, wire(f)); err != nil {
				return err
			}
		}
		return nil
	}
	t.Cleanup(func() { publish = prev })
}

// requirePlane skips the run when the o11y-owned plane is not provisioned on the
// datastore under test — cloud never creates it, so absence is a skip, not a failure.
func requirePlane(t *testing.T, ctx context.Context) {
	t.Helper()
	for _, tbl := range []string{eventsTable, errorsTable} {
		if !tableExists(ctx, tbl) {
			t.Skipf("%s not provisioned (the plane's DDL owner is hanzoai/o11y); skipping live round trip", tbl)
		}
	}
}

func TestLiveCaptureRoundTrip(t *testing.T) {
	// Connect the shared datastore client from DATASTORE_ADDR. It dials in the
	// background, so wait for it to latch before reading.
	ready, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := datastore.Wait(ready); err != nil {
		t.Fatalf("datastore did not connect (set DATASTORE_ADDR=127.0.0.1:9000 with a live Datastore): %v", err)
	}
	ctx := context.Background()
	requirePlane(t, ctx)
	landDirect(t)

	// Isolate this run so re-runs are deterministic: use a unique org.
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

	code, respBody := livePost(t, app, canonDoor, "user-42", org, body)
	if code != http.StatusOK {
		t.Fatalf("POST %s = %d (%s)", canonDoor, code, respBody)
	}
	var res CaptureResult
	if err := json.Unmarshal(respBody, &res); err != nil {
		t.Fatalf("decode result: %v (%s)", err, respBody)
	}
	t.Logf("capture receipt: accepted=%d dropped=%d", res.Accepted, res.Dropped)
	if res.Accepted != 10 {
		t.Fatalf("accepted = %d, want 10", res.Accepted)
	}

	// Datastore MergeTree inserts are visible immediately to a direct SELECT.
	// 1) Raw landing proof: per-name counts for THIS org on event.event.
	rows, err := datastore.Query(ctx,
		"SELECT name, count() AS n FROM "+eventsTable+" WHERE org = ? GROUP BY name ORDER BY name", org)
	if err != nil {
		t.Fatalf("readback query: %v", err)
	}
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

	// 2) Privacy proof: the scrubbed signup_submitted row must NOT contain the
	//    password or the raw email anywhere in its stored attributes.
	pr, err := datastore.Query(ctx,
		"SELECT attributes FROM "+eventsTable+" WHERE org = ? AND name = 'signup_submitted'", org)
	if err != nil || len(pr) == 0 {
		t.Fatalf("attrs readback: %v", err)
	}
	attrs := aStrMap(pr[0]["attributes"])
	t.Logf("signup_submitted stored attributes: %v", attrs)
	if _, bad := attrs["password"]; bad {
		t.Fatalf("PRIVACY LEAK: credential key persisted in attributes: %v", attrs)
	}
	if strings.Contains(attrsString(attrs), "z@hanzo.ai") || strings.Contains(attrsString(attrs), "should-be-scrubbed") {
		t.Fatalf("PRIVACY LEAK: secret/PII persisted in attributes: %v", attrs)
	}
	if attrs["plan_interest"] != "pro" {
		t.Fatalf("legit property dropped: %v", attrs)
	}

	// 3) Read-lens proof: run the EXACT overview events SQL the /v1/analytics
	//    handler runs, scoped to this org, over a wide window.
	start := time.Now().Add(-time.Hour)
	end := time.Now().Add(time.Hour)
	where, args := eventsWhere(org, start, end)
	overSQL := "SELECT countIf(kind = 'page') AS pageviews, uniqExact(distinct_id) AS visitors, " +
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
	// Build the WebOverview/CommerceOverview via the SAME pure assemblers the
	// handler uses, to prove the row shape feeds the response types cleanly.
	web := buildWebOverview(o, true)
	com := buildCommerceOverview(o, true)
	t.Logf("  WebOverview=%+v", web)
	t.Logf("  CommerceOverview=%+v", com)

	// 4) Top-products lens.
	pwhere, pargs := eventsWhere(org, start, end)
	prodSQL := "SELECT attributes['product_id'] AS productId, countIf(name = 'order_completed') AS orders, " +
		"sum(toFloat64OrZero(attributes['revenue'])) AS revenue, sum(toUInt64OrZero(attributes['quantity'])) AS units " +
		"FROM " + eventsTable +
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

	t.Logf("LIVE E2E OK: 10 events emitted via POST /v1/event landed in %s and read back through the analytics lenses", eventsTable)
}

func attrsString(m map[string]string) string {
	var b strings.Builder
	for k, v := range m {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(v)
		b.WriteString(" ")
	}
	return b.String()
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

// TestLiveAnonymousCapture proves the marketing-site path: an ANONYMOUS pageview
// (no principal) posted with no credential lands under the reserved public tenant,
// resolved server-side — never from a client field.
func TestLiveAnonymousCapture(t *testing.T) {
	ready, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := datastore.Wait(ready); err != nil {
		t.Fatalf("datastore did not connect: %v", err)
	}
	ctx := context.Background()
	requirePlane(t, ctx)
	landDirect(t)
	app := liveApp(t)

	// A unique session id lets us find exactly this run's row (an anonymous row
	// carries no caller properties, so the marker rides a projected column).
	marker := "anon-" + time.Now().UTC().Format("150405.000")
	body := `{"batch":[{"type":"pageview","distinctId":"visitor-x","sessionId":"` + marker + `","product":"site","path":"/"}]}`

	req := httptest.NewRequest(http.MethodPost, canonDoor, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "hanzo.ai" // brand host buys NOTHING; the row lands under $public
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("anon POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anon capture = %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	rows, err := datastore.Query(ctx,
		"SELECT org, kind, product FROM "+eventsTable+" WHERE session_id = ?", marker)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("anon rows = %d, want 1", len(rows))
	}
	tenant := aString(rows[0]["org"])
	t.Logf("anonymous pageview landed: org=%q kind=%q product=%q",
		tenant, aString(rows[0]["kind"]), aString(rows[0]["product"]))
	if tenant != publicTenant {
		t.Fatalf("anon tenant = %q, want %s (no Host names a tenant)", tenant, publicTenant)
	}
}
