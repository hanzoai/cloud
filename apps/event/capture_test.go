// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// ── normalize: tenancy + event-name + clock skew ─────────────────────────────
//
// These used to drive normalizeEvent, the wide-row projection. The flip left ONE
// normalizer — normalize (fact.go), the plane's — so the same properties are now
// pinned on the fact: org stamp, name resolution, id minting, attribute derivation.

func TestNormalize_TenantAlwaysServerOrg(t *testing.T) {
	// Even a client that stuffs a foreign org into every field cannot change the
	// stored tenant: normalize stamps org verbatim, ignoring client input.
	e := CaptureEvent{Type: "event", Event: "signup_completed", DistinctID: "u1"}
	f, ok := normalize("acme", time.Now(), e)
	if !ok {
		t.Fatal("want ok")
	}
	if f.org != "acme" {
		t.Fatalf("org = %q, want acme", f.org)
	}
	if f.name != "signup_completed" || f.signal != signalAct || f.kind != kindTrack {
		t.Fatalf("name/signal/kind = %q/%q/%q", f.name, f.signal, f.kind)
	}
}

// TestResolveEventName pins the SUBSCRIBER vocabulary: the $-names the destinations
// fan-out and webhook envelopes carry (a published grammar — orgs subscribe to
// event.pageview et al). The STORED vocabulary is resolveName's (kind=page,
// name=page_viewed); both drop the same unnamed tracked event.
func TestResolveEventName(t *testing.T) {
	for _, tc := range []struct {
		typ, name, want string
	}{
		{"pageview", "", "$pageview"},
		{"page", "", "$pageview"},
		{"pageview", "custom_view", "custom_view"},
		{"identify", "ignored", "$identify"},
		{"group", "", "$group"},
		{"event", "plan_clicked", "plan_clicked"},
		{"", "bare_event", "bare_event"}, // absent type defaults to "event"
		{"event", "", ""},                // unroutable → dropped
		{"event", "   ", ""},
	} {
		got := resolveEventName(CaptureEvent{Type: tc.typ, Event: tc.name})
		if got != tc.want {
			t.Errorf("resolveEventName(%q,%q) = %q, want %q", tc.typ, tc.name, got, tc.want)
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
// rather than a data-quality one. `time` is caller-chosen and sits in event.event's
// ORDER BY ((org, time, id)), so an unbounded past lets one small batch produce a part
// whose key range spans years — and a MergeTree part is skippable only when its range
// misses the query's, so that one part is scanned for every window every tenant asks
// for. O(1) to write, O(table) to read, and the reader is not the attacker.
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

// TestRetentionIsNotARequestParameter: the plane's TTL is measured from ingested_at,
// a server-stamped column DEFAULT — and the half of that property THIS repo owns is
// the INSERT list: no writer may bind ingested_at, or the caller sets the clock its
// own row expires on (a write that answers 200, fans out, and is TTL-eligible before
// the reply lands). The TTL clause itself lives in the plane's DDL, whose one owner
// is hanzoai/o11y; the pin over the DDL string moved there with the DDL.
func TestRetentionIsNotARequestParameter(t *testing.T) {
	for _, w := range writers {
		for _, c := range w.table.columns {
			if strings.Contains(c, "ingested_at") {
				t.Errorf("%s writer binds ingested_at — the caller can set the column retention is measured from", w.signal)
			}
		}
	}
}

// TestCloudDoesNotPartitionByTenant — THE INVERTED PIN. Its predecessor
// (TestTenantIsThePartitionBoundary) asserted the OPPOSITE: that this package's DDL
// partitioned hanzo.events BY (tenant_id, toYYYYMM). Measurement then showed that
// key IS the 59.7-byte/row defect — one partition-set per tenant per month explodes
// the part count, and the well-engineered plane table (event.event: PARTITION BY
// toYYYYMM(ingested_at) ONLY, ORDER BY (org, time, id)) stores the same stream at
// the 2.8-byte/row class. Tenant isolation is the ORDER BY prefix + the bound-org
// predicate, not the partition key.
//
// So the pin now protects the FIX the way it used to protect the bug: cloud owns NO
// events-table DDL at all — no CREATE, no PARTITION BY — and therefore cannot
// reintroduce a tenant-partitioned events table. The one owner of the plane's schema
// is hanzoai/o11y. The scan reads this package's non-test sources, which is exactly
// the scope a re-grown DDL would have to appear in.
func TestCloudDoesNotPartitionByTenant(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "PARTITION BY") {
			t.Errorf("%s declares a PARTITION BY — cloud owns no events DDL; the plane's owner is hanzoai/o11y", name)
		}
		if strings.Contains(string(src), "CREATE TABLE") {
			t.Errorf("%s declares a CREATE TABLE — cloud is a writer/reader of the plane, never a creator", name)
		}
	}
	// The time half of the ORDER BY key stays bounded by the clamp: without the
	// floor, a caller-chosen `time` is a caller-chosen key range, and one wide part
	// defeats pruning for every window read.
	if maxBackdate <= 0 || maxBackdate > 90*24*time.Hour {
		t.Errorf("maxBackdate = %s: the caller-chosen time column is only bounded by this", maxBackdate)
	}
}

func TestNormalize_MintsIDWhenAbsent(t *testing.T) {
	f, _ := normalize("acme", time.Now(), CaptureEvent{Type: "pageview"})
	if f.id == "" {
		t.Fatal("server must mint an id when messageId is absent")
	}
	// id is THE idempotency key: event.event is a ReplacingMergeTree keyed
	// (org, time, id), so a retried batch must carry the same ids — the client's
	// messageId is preserved verbatim.
	f2, _ := normalize("acme", time.Now(), CaptureEvent{Type: "pageview", MessageID: "m-123"})
	if f2.id != "m-123" {
		t.Fatalf("client messageId must be preserved, got %q", f2.id)
	}
}

func TestNormalize_ReferrerDomain(t *testing.T) {
	f, _ := normalize("acme", time.Now(), CaptureEvent{
		Type: "pageview", Referrer: "https://news.ycombinator.com/item?id=42",
	})
	if f.attributes["referrer_domain"] != "news.ycombinator.com" {
		t.Fatalf("attributes[referrer_domain] = %q", f.attributes["referrer_domain"])
	}
}

// ── scrubProps: privacy boundary ─────────────────────────────────────────────

func TestScrubProps_DropsSecretsAndPII(t *testing.T) {
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
	out := decodeProps(t, scrubProps(in))
	for _, banned := range []string{"password", "api_key", "access_token", "email", "name", "credit_card", "session_cookie"} {
		if _, ok := out[banned]; ok {
			t.Errorf("scrubProps kept banned key %q", banned)
		}
	}
	if out["plan"] != "pro" || out["count"].(float64) != 3 {
		t.Errorf("scrubProps dropped legit keys: %v", out)
	}
}

func TestScrubProps_RedactsEmailValues(t *testing.T) {
	out := decodeProps(t, scrubProps(map[string]any{"note": "ping me at z@hanzo.ai please"}))
	if s, _ := out["note"].(string); strings.Contains(s, "@hanzo.ai") {
		t.Fatalf("email value not redacted: %q", s)
	}
}

func TestScrubProps_Nested(t *testing.T) {
	out := decodeProps(t, scrubProps(map[string]any{
		"meta": map[string]any{"password": "x", "ok": true},
	}))
	meta, _ := out["meta"].(map[string]any)
	if _, bad := meta["password"]; bad {
		t.Fatalf("nested secret not scrubbed: %v", meta)
	}
	if meta["ok"] != true {
		t.Fatalf("nested legit value dropped: %v", meta)
	}
}

func TestScrubProps_Empty(t *testing.T) {
	if got := scrubProps(nil); got != "" {
		t.Errorf("nil props = %q, want empty", got)
	}
	if got := scrubProps(map[string]any{"email": "z@hanzo.ai"}); got != "" {
		t.Errorf("all-scrubbed props = %q, want empty", got)
	}
}

// TestStoredPropertiesAreScrubbed pins the scrub's CALL SITE, not the scrub: every
// test above calls scrubProps/scrubMap directly, so the privacy boundary was proven
// correct and proven nowhere in particular. normalize is the ONE place a property bag
// becomes column values (the attributes map), and carrying e.Properties raw there
// keeps the whole suite green while every secret and every email a caller ever sent
// goes to rest in the warehouse — and onto the bus every consumer reads.
//
// It runs on the VOUCHED-FOR lane on purpose: the anonymous lane never reaches this at
// all (admitPublic REBUILDS the event without Properties, so its rows carry only what
// the server put there). A bearer's properties are the only ones that reach the column,
// which makes this lane the whole exposure.
//
// The legit key is asserted to SURVIVE. Without it the case also passes when the fact
// carries nothing at all, which is the cheapest way to make a privacy assertion vacuous.
func TestStoredPropertiesAreScrubbed(t *testing.T) {
	w := fakeWarehouse(t)
	app := mountApp(t)
	const body = `{"batch":[{"type":"event","event":"checkout_started","properties":{` +
		`"plan":"pro","password":"hunter2","note":"reach me at z@hanzo.ai",` +
		`"callback":"https://x.test/cb?access_token=abcdef0123456789"}}]}`
	code, resp := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", body)
	if code != http.StatusOK {
		t.Fatalf("bearer ingest = %d (%s), want 200 (committed to the fake plane)", code, resp)
	}
	if len(w.facts) != 1 {
		t.Fatalf("committed %d facts, want 1", len(w.facts))
	}
	attrs := w.facts[0].attributes
	if _, bad := attrs["password"]; bad {
		t.Errorf("a credential-shaped KEY reached the fact: %v", attrs)
	}
	stored := fmt.Sprintf("%v", attrs)
	if strings.Contains(stored, "@hanzo.ai") {
		t.Errorf("a PII-shaped VALUE reached the fact unredacted: %s", stored)
	}
	if strings.Contains(stored, "abcdef0123456789") {
		t.Errorf("a query-string secret reached the fact unredacted: %s", stored)
	}
	if attrs["plan"] != "pro" {
		t.Errorf("the legit property did not survive (%s) — a fact that stores nothing "+
			"passes every assertion above without scrubbing anything", stored)
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

// ── the event writer: shape + positional integrity ──────────────────────────
//
// These pins moved with the write path. buildEventsInsert rendered
// "INSERT INTO hanzo.events (…)" — the statement whose target table WAS the
// measured 59.7-byte/row defect — and the flip's INSERT is the event writer's
// (warehouse.go), targeting the plane. The prefix pin flips with it: the one
// statement that lands a product event now opens "INSERT INTO event.event (".

func TestEventWriterStatementTargetsThePlane(t *testing.T) {
	var ew writer
	for _, w := range writers {
		if w.signal == signalAct {
			ew = w
		}
	}
	stmt := ew.table.statement()
	if !strings.HasPrefix(stmt, "INSERT INTO event.fact (") {
		t.Fatalf("stmt prefix: %s — the ONE occurrence INSERT lands on the plane's one "+
			"fact table, never on a per-signal table and never on the retired wide table", stmt)
	}
	if strings.Contains(stmt, "hanzo.events") {
		t.Fatalf("the event writer still names the retired wide table: %s", stmt)
	}
	// The batching lives in the store (async_insert), which is what keeps one
	// statement per fact affordable — the settings must render before VALUES.
	if !strings.Contains(stmt, insertSettings+" VALUES") {
		t.Fatalf("insert settings must render before VALUES: %s", stmt)
	}
	// One placeholder per bound value: the el tuple binds six, every other
	// envelope column one.
	want := len(factColumns) - 1 + 6
	if got := strings.Count(stmt, "?"); got != want {
		t.Fatalf("placeholder count = %d, want %d", got, want)
	}
	// EVERY signal renders the IDENTICAL statement, which is the property that
	// replaced "one table per signal": five writers cannot drift into five row
	// shapes if they are literally the same string.
	for _, w := range writers {
		if got := w.table.statement(); got != stmt {
			t.Fatalf("%s writer renders a different INSERT than act:\n%s\n%s", w.signal, got, stmt)
		}
	}
}

func TestFactColumnsMatchArgsWidth(t *testing.T) {
	// The positional args MUST be exactly as wide as the placeholder list, or the
	// INSERT binds the wrong column — the one invariant that silently corrupts data.
	// (el is one column bound as a six-element tuple, hence the +5.)
	if got, want := len(factArgs(message{})), len(factColumns)+5; got != want {
		t.Fatalf("factArgs width = %d, want %d (factColumns + el's extra 5)", got, want)
	}
	// org leads: the tenant is the first bound value of every fact insert.
	args := factArgs(message{Org: "acme"})
	if args[0] != "acme" {
		t.Fatalf("org arg = %v, want acme (server tenant, positional)", args[0])
	}
}

// ── HTTP contract (datastore is DOWN in this harness) ────────────────────────

// canonDoor is the ONE path the canonical wire is served on. The three name-aliases
// this file used to sweep (/v1/event{,/batch}, /v1/todo) are retired, and
// doors_test.go holds them shut on both surfaces. The properties below are the
// canonical endpoint's own; the per-wire generalisation over every declared
// endpoint lives in doors_test.go, which builds each endpoint's body from its own
// decoder.
const canonDoor = "/v1/event"

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
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestCapture_NoPrincipalGetsAnonymousLane: a credential-less POST is not refused
// outright at the endpoint and admitted nowhere: admission is decided by trust
// level rather than per endpoint, and with no credential there is no trust level
// to decide on.
// The retired alias routes got this WRONG in the other direction — they resolved a
// REAL brand org from the Host and admitted the lot.
func TestCapture_NoPrincipalIsRefused(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := mountApp(t)
	p := canonDoor
	for _, body := range []string{
		`{"batch":[{"type":"pageview"}]}`,
		`{"batch":[{"type":"event","event":"order_completed","revenue":99}]}`,
	} {
		code, got := doBody(t, app, http.MethodPost, p, "", "", body)
		refusedAnon(t, "no-principal POST "+p+" "+body, code, got)
	}
}

// TestCapture_ForgedOrgWithoutBearerBuysNothing: a raw X-Org-Id with no validated
// principal is the cross-tenant forge. It is not refused any more (the request is
// anonymous, and anonymous traffic is admitted), but it buys NOTHING: the anonymous
// tenant is server-chosen, so the forged org cannot be the one a row lands in
// (TestAdmitPublic_DoorOwnsTheTenant), and the projection strips every field that
// could name the victim anyway.
func TestCapture_ForgedOrgWithoutBearerBuysNothing(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := mountApp(t)
	p := canonDoor
	code, body := doBody(t, app, http.MethodPost, p, "", "maxpower",
		`{"batch":[{"type":"event","event":"steal","groupId":"maxpower","personId":"victim","revenue":1}]}`)
	refusedAnon(t, "forged-org-no-bearer POST "+p, code, body)
}

func TestCapture_EmptyBatchOK(t *testing.T) {
	app := mountApp(t)
	// Empty batch returns 200 with zero counts BEFORE the datastore is consulted.
	code, body := doBody(t, app, http.MethodPost, canonDoor, "user-dave", "acme", `{"batch":[]}`)
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
	for i := range maxBatch + 1 {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`{"type":"pageview"}`)
	}
	sb.WriteString(`]}`)
	code, _ := doBody(t, app, http.MethodPost, canonDoor, "user-dave", "acme", sb.String())
	if code != http.StatusBadRequest {
		t.Fatalf("oversized batch want 400, got %d", code)
	}
}

func TestCapture_DatastoreDownHonest503(t *testing.T) {
	app := mountApp(t)
	// A real batch with a validated principal but no datastore → honest 503,
	// never a fake 200 (mirrors the read side's no-fabrication contract).
	code, _ := doBody(t, app, http.MethodPost, canonDoor, "user-dave", "acme", `{"batch":[{"type":"event","event":"signup_completed"}]}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("datastore-down capture want 503, got %d", code)
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
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestCapture_HostIsNotATenant: a Host never picks a tenant, and now never admits one
// either. A recognized brand Host, an unrecognized one and a customer's own all answer
// the SAME 401 — the Host is not evidence of anything. It used to differ (403 vs. a
// real brand org), which is precisely how a caller-settable header ended up selecting
// a real partition.
func TestCapture_HostIsNotATenant(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := mountApp(t)
	// Each row carries its endpoint's OWN wire: a canonical pageview decodes to
	// nothing on the PostHog endpoint and would answer 200 all-dropped, which reads
	// like the refusal this test exists to rule out. The wire has to be the
	// endpoint's or the assertion measures the decoder instead of the tenant rule.
	for _, tc := range []struct{ path, host, body string }{
		{canonDoor, "hanzo.ai", canonPageview},
		{canonDoor, "app.lux.cloud", canonPageview},
		{canonDoor, "evil.example.com", canonPageview}, // an unknown Host is treated the same
		{"/v1/event", "hanzo.ai", posthogPage},
		{"/v1/event", "evil.example.com", posthogPage},
	} {
		if code, body := doHost(t, app, tc.path, "", "", tc.host, tc.body); code != http.StatusUnauthorized {
			t.Fatalf("anonymous pageview %s on host %q want 401 (no key), got %d (%s)", tc.path, tc.host, code, body)
		}
	}
}
