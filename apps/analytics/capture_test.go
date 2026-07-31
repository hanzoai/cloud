// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// ── normalizeEvent: tenancy + event-name + clock skew ────────────────────────

func TestNormalizeEvent_TenantAlwaysServerOrg(t *testing.T) {
	// Even a client that stuffs a foreign org into every field cannot change the
	// stored tenant: normalizeEvent stamps org verbatim, ignoring client input.
	e := CaptureEvent{Type: "event", Event: "signup_completed", DistinctID: "u1"}
	row, ok := normalizeEvent("acme", time.Now(), e)
	if !ok {
		t.Fatal("want ok")
	}
	if row.tenant != "acme" {
		t.Fatalf("tenant = %q, want acme", row.tenant)
	}
	if row.event != "signup_completed" || row.eventType != "event" {
		t.Fatalf("event/type = %q/%q", row.event, row.eventType)
	}
}

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

func TestNormalizeEvent_UnroutableDropped(t *testing.T) {
	if _, ok := normalizeEvent("acme", time.Now(), CaptureEvent{Type: "event"}); ok {
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

// TestRetentionIsNotARequestParameter: the TTL clause and the INSERT column list are two
// halves of ONE property — a row expires on a clock the wire cannot reach. Either half
// alone proves nothing: a TTL over ingested_at is worthless once ingested_at becomes
// settable, and keeping it unsettable is worthless while the TTL reads the caller's
// timestamp. Over `timestamp` the failure is a write that answers 200, fans out to every
// destination in forward.go, and is expirable before the reply lands.
func TestRetentionIsNotARequestParameter(t *testing.T) {
	if !strings.Contains(eventsTableDDL, "TTL ingested_at + INTERVAL 2 YEAR") {
		t.Errorf("retention is not measured from the server-stamped ingested_at:\n%s", eventsTableDDL)
	}
	if strings.Contains(eventsTableDDL, "TTL timestamp") {
		t.Error("retention is measured from the CALLER's timestamp — a back-dated row inserts, fans out, and is immediately TTL-eligible")
	}
	if !strings.Contains(eventsTableDDL, "ingested_at DateTime DEFAULT now()") {
		t.Errorf("ingested_at is not server-stamped, so the TTL column has no value of its own:\n%s", eventsTableDDL)
	}
	for _, c := range eventColumns {
		if c == "ingested_at" {
			t.Error("ingested_at is in the INSERT column list, so the caller can set the column retention is measured from")
		}
	}
}

// TestTenantIsThePartitionBoundary: unpartitioned, every tenant shares one "all"
// partition, so ONE tenant's part range is compared against EVERY tenant's query and a
// wide-ranged part defeats primary-index pruning for all of them. Partitioning on
// (tenant, month) makes that impossible by layout rather than by key range — a part
// cannot be scanned for a tenant it does not belong to — and both halves of the key are
// exactly the read lens's own predicate (query.go filters a timestamp window AND
// tenant_id), so pruning happens before the primary index is consulted.
func TestTenantIsThePartitionBoundary(t *testing.T) {
	if !strings.Contains(eventsTableDDL, "PARTITION BY (tenant_id, toYYYYMM(timestamp))") {
		t.Errorf("hanzo.events is not partitioned on (tenant, month):\n%s", eventsTableDDL)
	}
	// The partition key is only bounded because the clamp bounds its time half. Without
	// the floor, a caller-chosen month is a caller-chosen partition, and the same batch
	// that used to widen one part's key range instead proliferates partitions.
	if maxBackdate <= 0 || maxBackdate > 90*24*time.Hour {
		t.Errorf("maxBackdate = %s: the month half of the partition key is caller-chosen and only this bounds it", maxBackdate)
	}
}

func TestNormalizeEvent_MintsIDWhenAbsent(t *testing.T) {
	row, _ := normalizeEvent("acme", time.Now(), CaptureEvent{Type: "pageview"})
	if row.id == "" {
		t.Fatal("server must mint an id when messageId is absent")
	}
	row2, _ := normalizeEvent("acme", time.Now(), CaptureEvent{Type: "pageview", MessageID: "m-123"})
	if row2.id != "m-123" {
		t.Fatalf("client messageId must be preserved, got %q", row2.id)
	}
}

func TestNormalizeEvent_ReferrerDomain(t *testing.T) {
	row, _ := normalizeEvent("acme", time.Now(), CaptureEvent{
		Type: "pageview", Referrer: "https://news.ycombinator.com/item?id=42",
	})
	if row.referrerDomain != "news.ycombinator.com" {
		t.Fatalf("referrerDomain = %q", row.referrerDomain)
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
// test above calls scrubProps directly, so the privacy boundary was proven correct and
// proven nowhere in particular. normalizeEvent is the ONE place a property bag becomes
// a column value, and storing e.Properties raw there keeps the whole suite green while
// every secret and every email a caller ever sent goes to rest in the warehouse.
//
// It runs on the VOUCHED-FOR lane on purpose: the anonymous lane never reaches this at
// all (admitPublic REBUILDS the event without Properties, so its rows carry only what
// the server put there). A bearer's properties are the only ones that reach the column,
// which makes this lane the whole exposure.
//
// The legit key is asserted to SURVIVE. Without it the case also passes when the row
// stores nothing at all, which is the cheapest way to make a privacy assertion vacuous.
func TestStoredPropertiesAreScrubbed(t *testing.T) {
	w := fakeWarehouse(t)
	app := mountApp(t)
	const body = `{"batch":[{"type":"event","event":"checkout_started","properties":{` +
		`"plan":"pro","password":"hunter2","note":"reach me at z@hanzo.ai",` +
		`"callback":"https://x.test/cb?access_token=abcdef0123456789"}}]}`
	code, resp := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", body)
	if code != http.StatusOK {
		t.Fatalf("bearer ingest = %d (%s), want 200 (written to the fake warehouse)", code, resp)
	}
	if len(w.rows) != 1 {
		t.Fatalf("wrote %d rows, want 1", len(w.rows))
	}
	stored, _ := w.col(t, 0, "properties").(string)
	props := decodeProps(t, stored)
	if _, bad := props["password"]; bad {
		t.Errorf("a credential-shaped KEY reached the row: %s", stored)
	}
	if strings.Contains(stored, "@hanzo.ai") {
		t.Errorf("a PII-shaped VALUE reached the row unredacted: %s", stored)
	}
	if strings.Contains(stored, "abcdef0123456789") {
		t.Errorf("a query-string secret reached the row unredacted: %s", stored)
	}
	if props["plan"] != "pro" {
		t.Errorf("the legit property did not survive (%s) — a row that stores nothing "+
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

// ── buildEventsInsert: shape + positional integrity ──────────────────────────

func TestBuildEventsInsert_Shape(t *testing.T) {
	rows := []eventRow{
		{id: "a", tenant: "acme", event: "$pageview", eventType: "pageview"},
		{id: "b", tenant: "acme", event: "signup_completed", eventType: "event"},
	}
	stmt, args := buildEventsInsert(rows)
	if !strings.HasPrefix(stmt, "INSERT INTO hanzo.events (") {
		t.Fatalf("stmt prefix: %s", stmt)
	}
	// one placeholder group per row, each with len(eventColumns) '?'.
	if got := strings.Count(stmt, "?"); got != len(rows)*len(eventColumns) {
		t.Fatalf("placeholder count = %d, want %d", got, len(rows)*len(eventColumns))
	}
	if len(args) != len(rows)*len(eventColumns) {
		t.Fatalf("args len = %d, want %d", len(args), len(rows)*len(eventColumns))
	}
	// tenant sits at column index 2 (id, timestamp, tenant_id, ...) for row 0.
	if args[2] != "acme" {
		t.Fatalf("tenant arg = %v, want acme (server tenant, positional)", args[2])
	}
	// row 1 tenant at offset len(eventColumns)+2.
	if args[len(eventColumns)+2] != "acme" {
		t.Fatalf("row1 tenant arg = %v", args[len(eventColumns)+2])
	}
}

func TestBuildEventsInsert_Empty(t *testing.T) {
	if stmt, args := buildEventsInsert(nil); stmt != "" || args != nil {
		t.Fatalf("empty rows must yield no statement, got %q / %v", stmt, args)
	}
}

func TestEventColumnsMatchArgsWidth(t *testing.T) {
	// The row's positional args MUST be exactly as wide as the column list, or the
	// INSERT binds the wrong column — the one invariant that silently corrupts data.
	if got := len(eventRow{}.args()); got != len(eventColumns) {
		t.Fatalf("eventRow.args width = %d, eventColumns = %d", got, len(eventColumns))
	}
}

// ── HTTP contract (datastore is DOWN in this harness) ────────────────────────

// canonDoor is the ONE path the canonical wire is served on. The three name-aliases
// this file used to sweep (/v1/analytics{,/batch}, /v1/tracker) are retired, and
// doors_test.go holds them shut on both surfaces. The properties below are the
// canonical door's own; the per-wire generalisation over every declared door lives
// in doors_test.go, which builds each door's body from its own decoder.
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
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestCapture_NoPrincipalGetsAnonymousLane: a credential-less POST is not refused
// outright — it takes the anonymous lane, because admission is decided by trust level
// rather than per door. A pageview is admitted (503, datastore down) under the
// reserved public tenant, and everything beyond the allowlist is dropped, which is
// what the retired alias routes used to get WRONG in the other direction: they
// resolved a REAL brand org from the Host and admitted the lot.
func TestCapture_NoPrincipalGetsAnonymousLane(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := mountApp(t)
	p := canonDoor
	if code, body := doBody(t, app, http.MethodPost, p, "", "", `{"batch":[{"type":"pageview"}]}`); code != http.StatusServiceUnavailable {
		t.Fatalf("no-principal POST %s want 503 (anonymous lane, admitted), got %d (%s)", p, code, body)
	}
	code, body := doBody(t, app, http.MethodPost, p, "", "", `{"batch":[{"type":"event","event":"order_completed","revenue":99}]}`)
	if code != http.StatusOK {
		t.Fatalf("no-principal commerce POST %s want 200 all-dropped, got %d (%s)", p, code, body)
	}
	if r := receipt(t, body); r.Accepted != 0 || r.Dropped != 1 {
		t.Fatalf("no-principal commerce POST %s receipt = %+v, want accepted:0 dropped:1", p, r)
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
	if code != http.StatusOK {
		t.Fatalf("forged-org-no-bearer POST %s want 200 all-dropped, got %d (%s)", p, code, body)
	}
	if r := receipt(t, body); r.Accepted != 0 || r.Dropped != 1 {
		t.Fatalf("forged-org-no-bearer POST %s receipt = %+v, want accepted:0 dropped:1", p, r)
	}
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
	for i := 0; i < maxBatch+1; i++ {
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
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestCapture_HostIsNotATenant: marketing traffic on a recognized brand Host is still
// ACCEPTED (503 — admitted, datastore down), so nothing external breaks; what changed is
// that the Host no longer picks the TENANT. Anonymous traffic lands under the reserved
// public tenant whatever the Host says, and an UNRECOGNIZED Host now behaves exactly
// like a recognized one — the two used to differ (403 vs. a real brand org), which is
// precisely how a caller-settable header ended up selecting a real partition.
func TestCapture_HostIsNotATenant(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := mountApp(t)
	// Each row carries its door's OWN wire: a canonical pageview decodes to nothing
	// on the PostHog door and would answer 200 all-dropped, which reads like the
	// refusal this test exists to rule out. The wire has to be the door's or the
	// assertion measures the decoder instead of the tenant rule.
	for _, tc := range []struct{ path, host, body string }{
		{canonDoor, "hanzo.ai", canonPageview},
		{canonDoor, "app.lux.cloud", canonPageview},
		{canonDoor, "evil.example.com", canonPageview}, // an unknown Host is treated the same
		{"/v1/event", "hanzo.ai", posthogPage},
		{"/v1/event", "evil.example.com", posthogPage},
	} {
		if code, body := doHost(t, app, tc.path, "", "", tc.host, tc.body); code != http.StatusServiceUnavailable {
			t.Fatalf("anonymous pageview %s on host %q want 503 (admitted), got %d (%s)", tc.path, tc.host, code, body)
		}
	}
}

func TestCapture_PublicCaptureDisabled(t *testing.T) {
	t.Setenv(publicCaptureEnv, "off")
	app := mountApp(t)
	// With public capture disabled, even a recognized brand host is refused
	// without a validated principal.
	code, _ := doHost(t, app, canonDoor, "", "", "hanzo.ai", `{"batch":[{"type":"pageview"}]}`)
	if code != http.StatusForbidden {
		t.Fatalf("public-capture-off anonymous want 403, got %d", code)
	}
}
