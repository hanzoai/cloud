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

	"github.com/hanzoai/cloud/clients/team/token"
	"github.com/zap-proto/zip"
)

// teamAccount is a syntactically valid account UUID — token.Generate requires one.
const teamAccount = "550e8400-e29b-41d4-a716-446655440000"

// teamWire is a VERBATIM batch as the published team SPA emits it
// (packages/analytics-providers/src/analyticsCollector.ts): a bare JSON array whose
// elements carry the enum tag in `event`, epoch MILLIS in `timestamp`, and the person
// id under the snake_case `distinct_id`. Every assertion below runs against this exact
// shape, so a test passing here is a statement about the real caller and not about a
// payload invented to fit the decoder.
const teamWire = `[
  {"event":"error","properties":{"error_message":"boom","error_type":"TypeError","error_stack":"at f (app.js:1)\nBearer sk-live-DEADBEEF","analytics_collector":true,"$anonymous_id":"anon_1"},"timestamp":1750000000000,"distinct_id":"user@hanzo.ai"},
  {"event":"navigation","properties":{"path":"/tracker"},"timestamp":1750000001000,"distinct_id":"user@hanzo.ai"}
]`

// postBody issues a POST with a raw body and optional Authorization header.
func postBody(t *testing.T, app *zip.App, path, body, auth string) (int, CaptureResult) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var res CaptureResult
	_ = json.Unmarshal(b, &res)
	return resp.StatusCode, res
}

// ── the evidence for the door ────────────────────────────────────────────────

// TestCanonicalWireSilentlyDropsTeamBatch is THE reason /v1/event/collect exists, and
// it is a test of the OLD behavior, not the new: it pins what a naive repoint of
// ANALYTICS_COLLECTOR_URL at the canonical door would have done.
//
// decodeIngest sees the leading '[' and decodes []Event, whose keys are `distinctId`
// and `time`. The team wire has neither, so the person id and the timestamp are lost,
// and toCapture leaves Type empty. canonicalType("") is "event", which is not in
// publicKinds — so admitPublic drops EVERY event. Accepted 0, dropped 2.
//
// That is the accepted-then-dropped failure: the SPA would see a 200 and discard its
// retry queue while nothing was ever stored.
func TestCanonicalWireSilentlyDropsTeamBatch(t *testing.T) {
	evs, err := decodeIngest([]byte(teamWire))
	if err != nil {
		t.Fatalf("decodeIngest: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("decodeIngest events = %d, want 2", len(evs))
	}
	// The two fields the canonical wire cannot see.
	if evs[0].DistinctID != "" {
		t.Errorf("canonical decode DistinctID = %q, want empty (key is distinct_id, not distinctId)", evs[0].DistinctID)
	}
	if evs[0].Timestamp != "" {
		t.Errorf("canonical decode Timestamp = %q, want empty (key is timestamp:number, not time:string)", evs[0].Timestamp)
	}
	admitted, dropped := admitPublic(evs)
	if len(admitted) != 0 || dropped != 2 {
		t.Fatalf("canonical wire admitted %d dropped %d, want 0 admitted / 2 dropped", len(admitted), dropped)
	}
}

// TestTeamWireLands is the other half: the SAME bytes through the team door survive
// admission AND normalize into real rows. It walks the WHOLE pipeline offline —
// decode, the anonymous projection, the exception fold, then normalizeEvent, which is
// the last function before the INSERT. A row out of normalizeEvent with ok==true is
// what "the event landed" means everywhere else in this package.
func TestTeamWireLands(t *testing.T) {
	evs, err := decodeTeam([]byte(teamWire))
	if err != nil {
		t.Fatalf("decodeTeam: %v", err)
	}
	admitted, dropped := admitPublic(evs)
	if len(admitted) != 2 || dropped != 0 {
		t.Fatalf("team wire admitted %d dropped %d, want 2 admitted / 0 dropped", len(admitted), dropped)
	}
	now := time.Now().UTC()
	want := []struct{ name, kind string }{{"$error", "error"}, {"$pageview", "pageview"}}
	for i, ev := range admitted {
		row, ok := normalizeEvent("acme", now, foldException(ev))
		if !ok {
			t.Fatalf("event %d did not normalize into a row", i)
		}
		if row.event != want[i].name || row.eventType != want[i].kind {
			t.Errorf("event %d = (%s,%s), want (%s,%s)", i, row.event, row.eventType, want[i].name, want[i].kind)
		}
		if row.tenant != "acme" {
			t.Errorf("event %d tenant = %q, want acme", i, row.tenant)
		}
	}
}

// TestTeamWireKeepsIdentityOnFullLane: on the vouched-for lane there is no projection,
// so the two fields the canonical wire lost must both survive — the snake_case person
// id and the epoch-millis timestamp converted to the instant the SPA meant.
func TestTeamWireKeepsIdentityOnFullLane(t *testing.T) {
	evs, err := decodeTeam([]byte(teamWire))
	if err != nil {
		t.Fatalf("decodeTeam: %v", err)
	}
	row, ok := normalizeEvent("acme", time.Now().UTC(), foldException(evs[0]))
	if !ok {
		t.Fatal("did not normalize")
	}
	if row.distinctID != "user@hanzo.ai" {
		t.Errorf("distinctID = %q, want user@hanzo.ai", row.distinctID)
	}
	if row.anonymousID != "anon_1" {
		t.Errorf("anonymousID = %q, want anon_1", row.anonymousID)
	}
	if got := row.timestamp.UTC(); !got.Equal(time.UnixMilli(1750000000000).UTC()) {
		t.Errorf("timestamp = %s, want %s", got, time.UnixMilli(1750000000000).UTC())
	}
}

// ── the wire ─────────────────────────────────────────────────────────────────

// TestTeamKindMapping pins the whole 7-member enum onto canonicalType's closed set.
// The mapping is TOTAL by design: a member that fell through to a kind outside
// publicKinds would be dropped for an anonymous caller, which is the failure this
// door exists to prevent, so every member is asserted rather than the two that matter
// most.
func TestTeamKindMapping(t *testing.T) {
	cases := []struct{ event, kind, name string }{
		{"error", "error", ""},
		{"navigation", "pageview", ""},
		{"setUser", "identify", ""},
		{"setTag", "identify", ""},
		{"setAlias", "identify", ""},
		{"setGroup", "group", ""},
		{"customEvent", "event", "checkout_started"},
		// A member added by a NEWER SPA still lands, keeping its own tag as the name.
		{"somethingNew", "event", "somethingNew"},
	}
	for _, c := range cases {
		body := `[{"event":"` + c.event + `","properties":{"event":"checkout_started"},"timestamp":1750000000000,"distinct_id":"u"}]`
		if c.event == "somethingNew" {
			body = `[{"event":"somethingNew","properties":{},"timestamp":1750000000000,"distinct_id":"u"}]`
		}
		evs, err := decodeTeam([]byte(body))
		if err != nil {
			t.Fatalf("%s: decodeTeam: %v", c.event, err)
		}
		if evs[0].Type != c.kind {
			t.Errorf("%s -> kind %q, want %q", c.event, evs[0].Type, c.kind)
		}
		if evs[0].Event != c.name {
			t.Errorf("%s -> name %q, want %q", c.event, evs[0].Event, c.name)
		}
		// Whatever the kind, it must produce a storable row.
		if _, ok := normalizeEvent("acme", time.Now().UTC(), foldException(evs[0])); !ok {
			t.Errorf("%s did not normalize into a row", c.event)
		}
	}
}

// TestTeamErrorPropertiesAreRehomed is a SECURITY assertion, not a formatting one.
// foldException scrubs the typed Exception into properties.$exception precisely so a
// token or PII lifted from a stack frame never reaches the row or the destinations
// fan-out. A copy of the stack left behind under error_stack would route around that
// scrubber entirely, so the three error_* properties must MOVE, not be copied.
func TestTeamErrorPropertiesAreRehomed(t *testing.T) {
	evs, err := decodeTeam([]byte(teamWire))
	if err != nil {
		t.Fatalf("decodeTeam: %v", err)
	}
	e := evs[0]
	if e.Error == nil {
		t.Fatal("error event has no typed Exception")
	}
	if e.Error.Message != "boom" || e.Error.Type != "TypeError" || !strings.Contains(e.Error.Stack, "app.js:1") {
		t.Errorf("Exception = %+v, want the error_* values", *e.Error)
	}
	for _, k := range []string{"error_message", "error_type", "error_stack"} {
		if _, still := e.Properties[k]; still {
			t.Errorf("property %q survived on Properties — it bypasses scrubException", k)
		}
	}
	// The unrelated property is untouched: this moves three keys, it does not filter.
	if e.Properties["analytics_collector"] != true {
		t.Error("decodeTeam dropped an unrelated property")
	}
	// End to end: the secret in the stack must not appear raw in the stored row.
	row, ok := normalizeEvent("acme", time.Now().UTC(), foldException(e))
	if !ok {
		t.Fatal("did not normalize")
	}
	if strings.Contains(row.properties, "sk-live-DEADBEEF") {
		t.Errorf("raw secret from the stack reached the stored properties: %s", row.properties)
	}
}

// TestTeamTimestampAbsentClampsToNow: a missing/zero timestamp must stay EMPTY out of
// the decoder so clampTS anchors it to server-now — never to the Unix epoch, which
// would file every such event in 1970 and silently skew every window query.
func TestTeamTimestampAbsentClampsToNow(t *testing.T) {
	evs, err := decodeTeam([]byte(`[{"event":"error","properties":{"error_message":"x"},"distinct_id":"u"}]`))
	if err != nil {
		t.Fatalf("decodeTeam: %v", err)
	}
	if evs[0].Timestamp != "" {
		t.Fatalf("absent timestamp decoded to %q, want empty", evs[0].Timestamp)
	}
	now := time.Now().UTC()
	row, _ := normalizeEvent("acme", now, foldException(evs[0]))
	if row.timestamp.Before(now.Add(-time.Minute)) {
		t.Errorf("absent timestamp stored as %s, want ~now", row.timestamp)
	}
}

// TestTeamRetriedBatchDecodes: handleFailedEvents mutates the queued event in place and
// re-serializes it, so a RETRIED request carries an extra top-level retryCount. A
// retry is exactly when the payload matters most; it must not become a 400.
func TestTeamRetriedBatchDecodes(t *testing.T) {
	body := `[{"event":"error","properties":{"error_message":"boom"},"timestamp":1750000000000,"distinct_id":"u","retryCount":2}]`
	evs, err := decodeTeam([]byte(body))
	if err != nil {
		t.Fatalf("retried batch: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "error" {
		t.Fatalf("retried batch decoded to %+v", evs)
	}
}

// TestTeamNonArrayRefused: the SPA emits an array unconditionally, so anything else is
// a misconfigured caller. An honest 400 beats an empty receipt that reads like success.
func TestTeamNonArrayRefused(t *testing.T) {
	if _, err := decodeTeam([]byte(`{"event":"error"}`)); err == nil {
		t.Error("an object body decoded without error; want a decode failure -> 400")
	}
	// An empty body is an honest empty receipt, not an error (matches decodeIngest).
	if evs, err := decodeTeam([]byte("  \n")); err != nil || len(evs) != 0 {
		t.Errorf("empty body = (%v, %v), want (no events, no error)", evs, err)
	}
}

// ── the credential ───────────────────────────────────────────────────────────

func teamToken(t *testing.T, org, secret string, extra map[string]any, exp int64) string {
	t.Helper()
	e := map[string]any{"org": org}
	for k, v := range extra {
		e[k] = v
	}
	tok, err := token.Generate(teamAccount, "", e, exp, secret)
	if err != nil {
		t.Fatalf("token.Generate: %v", err)
	}
	return tok
}

// TestTeamTenantResolvesSignedOrg: a well-formed, unexpired, correctly-signed team
// session token resolves to the org in its SIGNED extra.org claim.
func TestTeamTenantResolvesSignedOrg(t *testing.T) {
	t.Setenv("SERVER_SECRET", "a-real-team-secret")
	app := mountApp(t)
	tok := teamToken(t, "acme", "a-real-team-secret", nil, time.Now().Add(time.Hour).Unix())

	// One admitted event reaches the write core, which has no warehouse in this
	// harness -> 503. That 503 IS the signal: the pipeline tried to STORE. Contrast
	// with the all-dropped case below, which short-circuits to 200 before the
	// datastore is ever consulted.
	code, _ := postBody(t, app, "/v1/event/collect", teamWire, tok)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("valid team token POST = %d, want 503 (reached the write core)", code)
	}
}

// TestTeamTenantRefusals is the fail-closed table. Every row must NOT resolve to a
// tenant. A resolution here is a cross-tenant write into an org the caller cannot
// prove it owns.
func TestTeamTenantRefusals(t *testing.T) {
	const secret = "a-real-team-secret"
	hour := time.Now().Add(time.Hour).Unix()
	cases := []struct {
		name   string
		env    string
		bearer func(t *testing.T) string
	}{
		{"forged: signed with another key", secret, func(t *testing.T) string {
			return teamToken(t, "victim", "attacker-secret", nil, hour)
		}},
		{"expired", secret, func(t *testing.T) string {
			return teamToken(t, "acme", secret, nil, time.Now().Add(-time.Hour).Unix())
		}},
		{"guest session", secret, func(t *testing.T) string {
			return teamToken(t, "acme", secret, map[string]any{"guest": "true"}, hour)
		}},
		{"readonly session", secret, func(t *testing.T) string {
			return teamToken(t, "acme", secret, map[string]any{"readonly": "true"}, hour)
		}},
		{"no org claim", secret, func(t *testing.T) string {
			tok, err := token.Generate(teamAccount, "", nil, hour, secret)
			if err != nil {
				t.Fatalf("token.Generate: %v", err)
			}
			return tok
		}},
		// The upstream public default. If this resolved, anyone could mint a token
		// for any org, because the key is published.
		{"public default secret", "secret", func(t *testing.T) string {
			return teamToken(t, "victim", "secret", nil, hour)
		}},
		// No secret configured on the SERVER. The token itself is perfectly valid —
		// this is the posture check: with nothing to verify against, a genuine token
		// is not trusted either. Never "no secret ⇒ skip verification".
		{"secret unset", "", func(t *testing.T) string {
			return teamToken(t, "acme", secret, nil, hour)
		}},
		{"not a token", secret, func(t *testing.T) string { return "not-a-jwt" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SERVER_SECRET", c.env)
			app := mountApp(t)
			bearer := c.bearer(t)
			// An unresolved bearer takes the ANONYMOUS lane, where the projection
			// still admits error+pageview -> the write core -> 503. What must NOT
			// happen is a resolution: assert it by checking the tenant directly.
			code, _ := postBody(t, app, "/v1/event/collect", teamWire, bearer)
			if code != http.StatusServiceUnavailable && code != http.StatusOK {
				t.Fatalf("POST = %d, want 503 or 200 (never a server error)", code)
			}
			if got := resolvedTeamOrg(t, app, bearer); got != "" {
				t.Fatalf("credential resolved to org %q; it must not resolve", got)
			}
		})
	}
}

// resolvedTeamOrg runs teamTenant against a real request context carrying the bearer,
// and returns the org it resolved to (empty when it refused). It exercises the SAME
// function eventTenant calls, so the refusal table above is a statement about
// production admission, not about a copy of it.
func resolvedTeamOrg(t *testing.T, app *zip.App, bearer string) string {
	t.Helper()
	var got string
	probe := zip.New(zip.Config{})
	probe.Post("/probe", func(c *zip.Ctx) error {
		if org, ok := teamTenant(c); ok {
			got = org
		}
		return c.JSON(http.StatusOK, map[string]string{})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader("[]"))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := probe.Fiber().Test(req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return got
}

// ── the door ─────────────────────────────────────────────────────────────────

// TestTeamDoorIsRegistered proves the route exists and is the TEAM wire, by the
// clearest discriminator available without a warehouse:
//
//	/v1/event        + the team wire -> everything dropped -> the write core is never
//	                                    reached (len(evs)==0 short-circuits) -> 200.
//	/v1/event/collect+ the team wire -> events survive admission -> the write core IS
//	                                    reached -> 503 (no warehouse in the harness).
//
// A wrong or missing route would 404/405; a route bound to the canonical decoder would
// 200 with dropped=2. Only the correct binding produces 503.
func TestTeamDoorIsRegistered(t *testing.T) {
	t.Setenv("SERVER_SECRET", "a-real-team-secret")
	app := mountApp(t)

	code, res := postBody(t, app, "/v1/event", teamWire, "")
	if code != http.StatusOK || res.Accepted != 0 || res.Dropped != 2 {
		t.Fatalf("canonical door with team wire = %d %+v, want 200 accepted=0 dropped=2", code, res)
	}
	if code, _ := postBody(t, app, "/v1/event/collect", teamWire, ""); code != http.StatusServiceUnavailable {
		t.Fatalf("team door with team wire = %d, want 503 (reached the write core)", code)
	}
}
