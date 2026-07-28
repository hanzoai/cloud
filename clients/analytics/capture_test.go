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

// capturePaths are the three POST ingest routes the capture plane owns.
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
// alias is no longer refused outright — it takes the SAME anonymous lane the canonical
// door has always taken, because the aliases speak the same wire and admission is now
// decided by trust level rather than per door. A pageview is admitted (503, datastore
// down) under the reserved public tenant, and everything beyond the allowlist is
// dropped, which is what these routes used to get WRONG in the other direction: they
// resolved a REAL brand org from the Host and admitted the lot.
func TestCapture_NoPrincipalGetsAnonymousLane(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := mountApp(t)
	for _, p := range capturePaths {
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
}

func TestCapture_EmptyBatchOK(t *testing.T) {
	app := mountApp(t)
	// Empty batch returns 200 with zero counts BEFORE the datastore is consulted.
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

func TestCapture_DatastoreDownHonest503(t *testing.T) {
	app := mountApp(t)
	// A real batch with a validated principal but no datastore → honest 503,
	// never a fake 200 (mirrors the read side's no-fabrication contract).
	code, _ := doBody(t, app, http.MethodPost, "/v1/analytics", "user-dave", "acme", `{"batch":[{"type":"event","event":"signup_completed"}]}`)
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
	for _, tc := range []struct{ path, host string }{
		{"/v1/analytics", "hanzo.ai"},
		{"/v1/tracker", "app.lux.cloud"},
		{"/v1/analytics", "evil.example.com"}, // an unknown Host is treated the same
		{"/v1/tracker", "evil.example.com"},
	} {
		if code, body := doHost(t, app, tc.path, "", "", tc.host, `{"batch":[{"type":"pageview"}]}`); code != http.StatusServiceUnavailable {
			t.Fatalf("anonymous pageview %s on host %q want 503 (admitted), got %d (%s)", tc.path, tc.host, code, body)
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
