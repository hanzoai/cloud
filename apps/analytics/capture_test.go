// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// ── normalize: tenancy, route, name, clock skew ──────────────────────────────

func TestNormalize_TenantAlwaysServerOrg(t *testing.T) {
	// Even a client that stuffs a foreign org into every field cannot change the
	// stored tenant: normalize stamps org verbatim, and org is not a wire field at all.
	e := CaptureEvent{Type: "event", Event: "signup_completed", DistinctID: "u1"}
	f, ok := normalize("acme", time.Now(), e)
	if !ok {
		t.Fatal("want ok")
	}
	if f.org != "acme" {
		t.Fatalf("org = %q, want acme", f.org)
	}
	if f.name != "signup_completed" || f.kind != kindTrack || f.signal != signalEvent {
		t.Fatalf("name/kind/signal = %q/%q/%q", f.name, f.kind, f.signal)
	}
}

// TestRouteOf pins the ONE job of the wire's `type` field: pick the signal (hence the
// table and the subject) and the default kind column. Signal and kind are different
// things, and this is where they are kept apart.
func TestRouteOf(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		want route
	}{
		{"", route{signalEvent, kindTrack}},
		{"event", route{signalEvent, kindTrack}},
		{"pageview", route{signalEvent, kindPage}},
		{"page", route{signalEvent, kindPage}},
		{"PAGE", route{signalEvent, kindPage}},
		{"identify", route{signalEvent, kindIdentify}},
		{"group", route{signalEvent, kindGroup}},
		{"error", route{signal: signalError}},
		{"exception", route{signal: signalError}},
		{"log", route{signal: signalLog}},
		{"span", route{signalSpan, kindInternal}},
		{"metric", route{signal: signalMetric}},
		{"nonsense", route{signalEvent, kindTrack}},
	} {
		if got := routeOf(CaptureEvent{Type: tc.typ}); got != tc.want {
			t.Errorf("routeOf(%q) = %+v, want %+v", tc.typ, got, tc.want)
		}
	}
}

// TestErrorObjectRoutesToTheErrorSignal: an event that attached an exception and named
// no type IS an error. This used to be done by mutating the event mid-pipeline; it is
// now a property of the pure route function, so nothing rewrites an event on its way
// past and the route is decidable from the wire alone.
func TestErrorObjectRoutesToTheErrorSignal(t *testing.T) {
	e := CaptureEvent{Error: &Exception{Message: "boom"}}
	if got := routeOf(e); got.signal != signalError {
		t.Fatalf("routeOf(error object, no type) = %+v, want the error signal", got)
	}
	f, ok := normalize("acme", time.Now(), e)
	if !ok || f.signal != signalError || f.fault == nil {
		t.Fatalf("normalize did not produce an error fact: ok=%v signal=%q fault=%v", ok, f.signal, f.fault)
	}
	// An explicit type still wins — only an UNSTATED one is inferred.
	if got := routeOf(CaptureEvent{Type: "page", Error: &Exception{Message: "boom"}}); got.kind != kindPage {
		t.Fatalf("an explicit type must win over the inferred one, got %+v", got)
	}
}

// TestRouteSpellRoundTrips pins the inverse the anonymous projection depends on: for
// every route, routeOf(spell(route)) is that same route. If it ever stopped holding,
// the projection would rebuild an admitted page view as something else — silently.
func TestRouteSpellRoundTrips(t *testing.T) {
	for _, r := range []route{
		{signalEvent, kindTrack}, {signalEvent, kindPage},
		{signalEvent, kindIdentify}, {signalEvent, kindGroup},
		{signal: signalError}, {signal: signalLog},
		{signalSpan, kindInternal}, {signal: signalMetric},
	} {
		if got := routeOf(CaptureEvent{Type: r.spell()}); got != r {
			t.Errorf("routeOf(%q) = %+v, want %+v", r.spell(), got, r)
		}
	}
}

func TestResolveName(t *testing.T) {
	for _, tc := range []struct {
		typ, name, want string
	}{
		{"pageview", "", "page_viewed"},
		{"page", "", "page_viewed"},
		{"pageview", "custom_view", "custom_view"},
		{"identify", "", "user_identified"},
		{"group", "", "group_identified"},
		{"event", "plan_clicked", "plan_clicked"},
		{"", "bare_event", "bare_event"}, // absent type defaults to a tracked event
		{"event", "", ""},                // unroutable → dropped
		{"event", "   ", ""},
		{"log", "", "log_record"},
		{"span", "", "span"},
	} {
		e := CaptureEvent{Type: tc.typ, Event: tc.name}
		if got := resolveName(routeOf(e), e); got != tc.want {
			t.Errorf("resolveName(%q,%q) = %q, want %q", tc.typ, tc.name, got, tc.want)
		}
	}
}

func TestNormalize_UnroutableDropped(t *testing.T) {
	if _, ok := normalize("acme", time.Now(), CaptureEvent{Type: "event"}); ok {
		t.Fatal("event with no name must be dropped (ok=false)")
	}
}

func TestClampTS(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	// valid past ts kept
	if got := clampTS("2026-07-13T11:00:00Z", now); !got.Equal(now.Add(-time.Hour)) {
		t.Errorf("past ts not preserved: %v", got)
	}
	// future beyond skew clamped to now
	if got := clampTS("2026-07-13T13:00:00Z", now); !got.Equal(now) {
		t.Errorf("future ts not clamped: %v", got)
	}
	// unparseable → now
	if got := clampTS("garbage", now); !got.Equal(now) {
		t.Errorf("bad ts not defaulted: %v", got)
	}
	// absent → now
	if got := clampTS("", now); !got.Equal(now) {
		t.Errorf("empty ts not defaulted: %v", got)
	}
}

// TestBackdatedTimestampIsClamped is the PAST bound, and it guards a warehouse property
// rather than a data-quality one. `timestamp` is caller-chosen and leads ORDER BY, so an
// unbounded past lets one small batch produce a part whose key range spans years — and a
// MergeTree part is skippable only when its range misses the query's, so that one part is
// scanned for every window every tenant asks for. O(1) to write, O(table) to read, and
// the reader is not the attacker.
//
// The boundary cases are the test: `maxBackdate` exactly is INSIDE (a late beacon flush
// is real traffic and must survive), one second past it is not.
func TestBackdatedTimestampIsClamped(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		at   time.Time
		want time.Time
	}{
		{"a late beacon flush is kept", now.Add(-6 * 24 * time.Hour), now.Add(-6 * 24 * time.Hour)},
		{"the floor itself is kept", now.Add(-maxBackdate), now.Add(-maxBackdate)},
		{"one second past the floor is clamped", now.Add(-maxBackdate - time.Second), now},
		{"the unix epoch is clamped", time.Unix(0, 0).UTC(), now},
		{"a year of key range is clamped", now.AddDate(-1, 0, 0), now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampTS(tc.at.Format(time.RFC3339), now); !got.Equal(tc.want) {
				t.Errorf("clampTS(%s) = %s, want %s", tc.at.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}

// TestRetentionIsNotARequestParameter: a row must expire on a clock the wire cannot
// reach. The schema half (TTL over ingested_at) is applied to the store out of band;
// the half this package owns is the INSERT column list, and it is the half that can
// regress here — a TTL over a server-stamped column is worthless the moment the column
// becomes settable. ingested_at is absent from every writer, so nothing on the wire can
// influence when a row expires.
func TestRetentionIsNotARequestParameter(t *testing.T) {
	for _, w := range writers {
		for _, c := range w.columns {
			if c == "ingested_at" {
				t.Errorf("%s inserts ingested_at, so the caller can set the column retention is measured from", w.signal.table())
			}
		}
		if strings.Contains(w.statement(), "ingested_at") {
			t.Errorf("%s names ingested_at in its statement", w.signal.table())
		}
	}
}

// TestOrgLeadsEveryTable: org is the tenant key, and it leads the envelope — which is
// the leading column of every table's ORDER BY. A writer that put anything before it
// would make a part's key range span tenants, so one tenant's part is compared against
// every tenant's query. The layout is the isolation; this pins the writer's half.
func TestOrgLeadsEveryTable(t *testing.T) {
	if envelopeColumns[0] != "org" {
		t.Fatalf("envelope does not lead with org: %v", envelopeColumns[:3])
	}
	for _, w := range writers {
		for i, c := range envelopeColumns {
			if w.columns[i] != c {
				t.Fatalf("%s column %d = %q, want the shared envelope's %q — the tables stop being "+
					"UNION ALL-able the moment one of them reorders the envelope", w.signal.table(), i, w.columns[i], c)
			}
		}
	}
	// The time half of the key is caller-chosen and only the clamp bounds it.
	if maxBackdate <= 0 || maxBackdate > 90*24*time.Hour {
		t.Errorf("maxBackdate = %s: the time half of every sort key is caller-chosen and only this bounds it", maxBackdate)
	}
}

func TestNormalize_MintsIDWhenAbsent(t *testing.T) {
	f, _ := normalize("acme", time.Now(), CaptureEvent{Type: "pageview"})
	if f.id == "" {
		t.Fatal("server must mint an id when messageId is absent")
	}
	f2, _ := normalize("acme", time.Now(), CaptureEvent{Type: "pageview", MessageID: "m-123"})
	if f2.id != "m-123" {
		t.Fatalf("client messageId must be preserved, got %q", f2.id)
	}
}

func TestNormalize_ReferrerDomain(t *testing.T) {
	f, _ := normalize("acme", time.Now(), CaptureEvent{
		Type: "pageview", Referrer: "https://news.ycombinator.com/item?id=42",
	})
	if got := f.attributes["referrer_domain"]; got != "news.ycombinator.com" {
		t.Fatalf("attributes[referrer_domain] = %q", got)
	}
}

// ── the scrub: privacy boundary ──────────────────────────────────────────────

func TestScrub_DropsSecretsAndPII(t *testing.T) {
	in := map[string]any{
		"plan":           "pro",
		"password":       "hunter2",
		"api_key":        "sk-123",
		"access_token":   "abc",
		"email":          "z@hanzo.ai",
		"name":           "Zed",
		"credit_card":    "4111111111111111",
		"session_cookie": "s=1",
		"count":          3,
	}
	out := attributesOf(scrubMap(in), CaptureEvent{})
	for _, banned := range []string{"password", "api_key", "access_token", "email", "name", "credit_card", "session_cookie"} {
		if _, ok := out[banned]; ok {
			t.Errorf("the scrub kept banned key %q", banned)
		}
	}
	if out["plan"] != "pro" || out["count"] != "3" {
		t.Errorf("the scrub dropped legit keys: %v", out)
	}
}

func TestScrub_RedactsEmailValues(t *testing.T) {
	out := attributesOf(scrubMap(map[string]any{"note": "ping me at z@hanzo.ai please"}), CaptureEvent{})
	if strings.Contains(out["note"], "@hanzo.ai") {
		t.Fatalf("email value not redacted: %q", out["note"])
	}
}

func TestScrub_Nested(t *testing.T) {
	out := scrubMap(map[string]any{"meta": map[string]any{"password": "x", "ok": true}})
	meta, _ := out["meta"].(map[string]any)
	if _, bad := meta["password"]; bad {
		t.Fatalf("nested secret not scrubbed: %v", meta)
	}
	if meta["ok"] != true {
		t.Fatalf("nested legit value dropped: %v", meta)
	}
}

func TestScrub_Empty(t *testing.T) {
	if got := scrubMap(nil); len(got) != 0 {
		t.Errorf("nil props = %v, want empty", got)
	}
	if got := attributesOf(scrubMap(map[string]any{"email": "z@hanzo.ai"}), CaptureEvent{}); len(got) != 0 {
		t.Errorf("all-scrubbed props = %v, want empty", got)
	}
}

// TestStoredAttributesAreScrubbed pins the scrub's CALL SITE, not the scrub: every test
// above calls it directly, so the privacy boundary was proven correct and proven
// nowhere in particular. normalize is the ONE place a property bag becomes stored
// values, and passing e.Properties through raw there keeps the whole suite green while
// every secret and every email a caller ever sent goes to rest in the warehouse.
//
// It runs on the VOUCHED-FOR lane on purpose: the anonymous lane never reaches this at
// all (admitPublic REBUILDS the event without Properties, so its facts carry only what
// the server put there). A bearer's properties are the only ones that reach a column,
// which makes this lane the whole exposure.
//
// The legit key is asserted to SURVIVE. Without it the case also passes when the fact
// stores nothing at all, which is the cheapest way to make a privacy assertion vacuous.
func TestStoredAttributesAreScrubbed(t *testing.T) {
	w := fakePlane(t)
	app := mountApp(t)
	const body = `{"batch":[{"type":"event","event":"checkout_started","properties":{` +
		`"plan":"pro","password":"hunter2","note":"reach me at z@hanzo.ai",` +
		`"callback":"https://x.test/cb?access_token=abcdef0123456789"}}]}`
	code, resp := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", body)
	if code != http.StatusOK {
		t.Fatalf("bearer ingest = %d (%s), want 200", code, resp)
	}
	if len(w.facts) != 1 {
		t.Fatalf("published %d facts, want 1", len(w.facts))
	}
	attrs := w.facts[0].attributes
	if _, bad := attrs["password"]; bad {
		t.Errorf("a credential-shaped KEY reached the fact: %v", attrs)
	}
	flat := fmt.Sprint(attrs)
	if strings.Contains(flat, "@hanzo.ai") {
		t.Errorf("a PII-shaped VALUE reached the fact unredacted: %v", attrs)
	}
	if strings.Contains(flat, "abcdef0123456789") {
		t.Errorf("a query-string secret reached the fact unredacted: %v", attrs)
	}
	if attrs["plan"] != "pro" {
		t.Errorf("the legit property did not survive (%v) — a fact that stores nothing "+
			"passes every assertion above without scrubbing anything", attrs)
	}
}

func decodeProps(t *testing.T, s string) map[string]any {
	t.Helper()
	if s == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("props not valid json: %v (%s)", err, s)
	}
	return m
}

// ── the writers: shape + positional integrity ────────────────────────────────

// TestWriterStatementShape: every writer binds one placeholder per value, in the
// column list's own order, into its OWN table. `el` is the one column that binds a
// tuple of six, which is why the placeholders are built from the columns rather than
// counted.
func TestWriterStatementShape(t *testing.T) {
	for _, w := range writers {
		stmt := w.statement()
		if !strings.HasPrefix(stmt, "INSERT INTO "+w.signal.table()+" (") {
			t.Fatalf("%s: stmt prefix: %s", w.signal, stmt)
		}
		want := len(w.columns) + 5 // el contributes six placeholders, not one
		if got := strings.Count(stmt, "?"); got != want {
			t.Fatalf("%s: placeholder count = %d, want %d", w.signal, got, want)
		}
		args := w.args(message{Org: "acme", Name: "n"})
		if len(args) != want {
			t.Fatalf("%s: args len = %d, want %d placeholders", w.signal, len(args), want)
		}
		if args[0] != "acme" {
			t.Fatalf("%s: first arg = %v, want the server tenant bound first", w.signal, args[0])
		}
	}
}

// TestWriterStatementSettleBeforeValues pins the one thing the shape check cannot
// see: where SETTINGS renders.
//
// The Values input format reads everything after VALUES as data, so SETTINGS may
// only appear BEFORE it. Rendered after, the store rejects every write with
// CANNOT_PARSE_INPUT_ASSERTION_FAILED — while the door still answers 200, the shape
// test still passes, and the loss is silent. A prefix and a placeholder count are
// both satisfied by a statement the store refuses; the ordering is not.
func TestWriterSettingsRenderBeforeValues(t *testing.T) {
	for _, w := range writers {
		stmt := w.statement()
		settings := strings.Index(stmt, " SETTINGS ")
		values := strings.Index(stmt, " VALUES ")
		switch {
		case settings < 0:
			t.Errorf("%s: no SETTINGS in %s", w.signal, stmt)
		case values < 0:
			t.Errorf("%s: no VALUES in %s", w.signal, stmt)
		case settings > values:
			t.Errorf("%s: SETTINGS renders after VALUES, which the Values format reads as a row, not a setting: %s", w.signal, stmt)
		}
	}
}

// TestSubjectAndTableAreOneName: the whole plane rests on a signal's subject and its
// table being the same string. Two constants would drift; one derivation cannot.
func TestSubjectAndTableAreOneName(t *testing.T) {
	for _, sig := range []signal{signalEvent, signalError, signalLog, signalSpan, signalMetric} {
		if sig.subject() != sig.table() {
			t.Errorf("%s: subject %q != table %q", sig, sig.subject(), sig.table())
		}
		if want := "event." + string(sig); sig.subject() != want {
			t.Errorf("%s: subject = %q, want %q", sig, sig.subject(), want)
		}
	}
}

// TestNoWriterCanProduceAnUnattributedRow: org is stamped server-side, so an empty one
// cannot arrive from a well-formed lane — but write is the last gate before a row
// exists, and a row with no tenant is readable by every tenant. It fails closed.
func TestNoWriterCanProduceAnUnattributedRow(t *testing.T) {
	orig := warehouseExec
	called := false
	warehouseExec = func(context.Context, string, ...any) error { called = true; return nil }
	t.Cleanup(func() { warehouseExec = orig })
	for _, w := range writers {
		if err := w.write(context.Background(), message{ID: "x"}); err == nil {
			t.Errorf("%s accepted a fact with no org", w.signal.table())
		}
	}
	if called {
		t.Error("an unattributed fact reached the store")
	}
}

// TestMetricHasNoWriter states the one deliberate gap, so removing it is a decision
// rather than an accident: event.metric has NO org column, so a writer for it would
// insert a row it cannot attribute — and two orgs reporting the same metric name and
// labels would hash to the same fingerprint and interleave into one series. Metric
// facts are still published; only the warehousing waits on the schema fix.
func TestMetricHasNoWriter(t *testing.T) {
	for _, w := range writers {
		if w.signal == signalMetric {
			t.Fatal("event.metric has a writer, but the table has no org column — that is a cross-tenant write")
		}
	}
	f, ok := normalize("acme", time.Now(), CaptureEvent{Type: "metric", Metric: &Metric{Name: "latency", Value: 1}})
	if !ok || f.signal != signalMetric || f.org != "acme" {
		t.Fatalf("a metric must still normalize and carry its org: ok=%v signal=%q org=%q", ok, f.signal, f.org)
	}
}

// ── HTTP contract ────────────────────────────────────────────────────────────

// capturePaths are the three POST ingest routes the sunsetting aliases own.
var capturePaths = []string{"/v1/analytics", "/v1/analytics/batch", "/v1/tracker"}

// doBody issues a request with a JSON body, mirroring http_test.go's do() (which
// carries no body). user/org simulate the SanitizeIdentity-minted headers.
func doBody(t *testing.T, app *zip.App, method, path, user, org, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestCapture_NoPrincipalGetsAnonymousLane: a credential-less POST to a deprecated
// alias is not refused outright — it takes the SAME anonymous lane the canonical door
// takes, because the aliases speak the same wire and admission is decided by trust
// level rather than per door. A page view is admitted under the reserved public
// tenant, and everything beyond the allowlist is dropped, which is what these routes
// used to get WRONG in the other direction: they resolved a REAL brand org from the
// Host and admitted the lot.
func TestCapture_NoPrincipalGetsAnonymousLane(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	for _, p := range capturePaths {
		w := fakePlane(t)
		app := mountApp(t)
		code, body := doBody(t, app, http.MethodPost, p, "", "", `{"batch":[{"type":"pageview"}]}`)
		if code != http.StatusOK {
			t.Fatalf("no-principal POST %s want 200 (anonymous lane, admitted), got %d (%s)", p, code, body)
		}
		if got := w.tenants(t); len(got) != 1 || got[0] != publicTenant {
			t.Fatalf("no-principal POST %s landed under %v, want the reserved public tenant", p, got)
		}
		code, body = doBody(t, app, http.MethodPost, p, "", "", `{"batch":[{"type":"event","event":"order_completed","revenue":99}]}`)
		if code != http.StatusOK {
			t.Fatalf("no-principal commerce POST %s want 200 all-dropped, got %d (%s)", p, code, body)
		}
		if r := receipt(t, body); r.Accepted != 0 || r.Dropped != 1 {
			t.Fatalf("no-principal commerce POST %s receipt = %+v, want accepted:0 dropped:1", p, r)
		}
	}
}

// TestCapture_ForgedOrgWithoutBearerBuysNothing: a raw X-Org-Id with no validated
// principal is the cross-tenant forge. It is not refused (the request is anonymous,
// and anonymous traffic is admitted), but it buys NOTHING: the anonymous tenant is
// server-chosen, so the forged org cannot be the one a fact lands in, and the
// projection strips every field that could name the victim anyway.
func TestCapture_ForgedOrgWithoutBearerBuysNothing(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	w := fakePlane(t)
	app := mountApp(t)
	for _, p := range capturePaths {
		code, body := doBody(t, app, http.MethodPost, p, "", "maxpower",
			`{"batch":[{"type":"event","event":"steal","groupId":"maxpower","personId":"victim","revenue":1}]}`)
		if code != http.StatusOK {
			t.Fatalf("forged-org-no-bearer POST %s want 200 all-dropped, got %d (%s)", p, code, body)
		}
		if r := receipt(t, body); r.Accepted != 0 || r.Dropped != 1 {
			t.Fatalf("forged-org-no-bearer POST %s receipt = %+v, want accepted:0 dropped:1", p, r)
		}
	}
	if len(w.facts) != 0 {
		t.Fatalf("a forged org published %d facts, want none", len(w.facts))
	}
}

func TestCapture_EmptyBatchOK(t *testing.T) {
	app := mountApp(t)
	// Empty batch returns 200 with zero counts BEFORE the plane is consulted.
	code, body := doBody(t, app, http.MethodPost, "/v1/analytics", "user-dave", "acme", `{"batch":[]}`)
	if code != http.StatusOK {
		t.Fatalf("empty batch want 200, got %d (%s)", code, body)
	}
	var r CaptureResult
	if err := json.Unmarshal(body, &r); err != nil || r.Accepted != 0 {
		t.Fatalf("empty batch result = %s (err %v)", body, err)
	}
}

func TestCapture_TooLarge400(t *testing.T) {
	app := mountApp(t)
	var sb strings.Builder
	sb.WriteString(`{"batch":[`)
	for i := 0; i < maxBatch+1; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"type":"pageview"}`)
	}
	sb.WriteString(`]}`)
	code, _ := doBody(t, app, http.MethodPost, "/v1/analytics", "user-dave", "acme", sb.String())
	if code != http.StatusBadRequest {
		t.Fatalf("oversized batch want 400, got %d", code)
	}
}

// TestCapture_PlaneDownHonest503: a fact that was admitted and could not be made
// DURABLE is a 503, never a 200 with a short count. Publish is the commit point, so
// "accepted" has to mean it is on the log — a success receipt for a fact that vanished
// is the one failure this design exists to prevent.
func TestCapture_PlaneDownHonest503(t *testing.T) {
	w := fakePlane(t)
	w.refuse(t)
	app := mountApp(t)
	code, body := doBody(t, app, http.MethodPost, "/v1/analytics", "user-dave", "acme", `{"batch":[{"type":"event","event":"signup_completed"}]}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("plane-down capture want 503, got %d (%s)", code, body)
	}
}

// doHost issues a POST with an explicit Host header + body, for the anonymous
// brand-host attribution tests.
func doHost(t *testing.T, app *zip.App, path, user, org, host, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Host = host
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestCapture_HostIsNotATenant: marketing traffic on a recognized brand Host is still
// ACCEPTED, so nothing external breaks; what changed is that the Host no longer picks
// the TENANT. Anonymous traffic lands under the reserved public tenant whatever the
// Host says, and an UNRECOGNIZED Host behaves exactly like a recognized one — the two
// used to differ (403 vs. a real brand org), which is precisely how a caller-settable
// header ended up selecting a real partition.
func TestCapture_HostIsNotATenant(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	w := fakePlane(t)
	app := mountApp(t)
	for _, tc := range []struct{ path, host string }{
		{"/v1/analytics", "hanzo.ai"},
		{"/v1/tracker", "app.lux.cloud"},
		{"/v1/analytics", "evil.example.com"}, // an unknown Host is treated the same
		{"/v1/tracker", "evil.example.com"},
	} {
		if code, body := doHost(t, app, tc.path, "", "", tc.host, `{"batch":[{"type":"pageview"}]}`); code != http.StatusOK {
			t.Fatalf("anonymous pageview %s on host %q want 200 (admitted), got %d (%s)", tc.path, tc.host, code, body)
		}
	}
	for i, org := range w.tenants(t) {
		if org != publicTenant {
			t.Fatalf("fact %d landed under %q — a Host header named a tenant", i, org)
		}
	}
}

func TestCapture_PublicCaptureDisabled(t *testing.T) {
	t.Setenv(publicCaptureEnv, "off")
	app := mountApp(t)
	// With public capture disabled, even a recognized brand host is refused
	// without a validated principal.
	code, _ := doHost(t, app, "/v1/analytics", "", "", "hanzo.ai", `{"batch":[{"type":"pageview"}]}`)
	if code != http.StatusForbidden {
		t.Fatalf("public-capture-off anonymous want 403, got %d", code)
	}
}
