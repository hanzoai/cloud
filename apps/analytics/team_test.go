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
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/team/token"
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
// Its captured millis (June 2025) are older than maxBackdate, so the write core anchors
// these rows to server-now. That is correct and it is why no test below asserts the
// stored instant from this fixture — a verbatim capture ages, and the bound is measured
// against now. TestTeamWireKeepsIdentityOnFullLane computes a fresh value for that.
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
	resp, err := app.Test(req)
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

// TestCanonicalDoorDecodesTeamBatch pins the fold that retired the /collect
// door as a distinct wire: the canonical decode now dispatches the team SPA's
// bare snake_case array by shape (isTeamArray), so a repoint of
// ANALYTICS_COLLECTOR_URL at the canonical door loses nothing. This INVERTS the
// old TestCanonicalWireSilentlyDropsTeamBatch, which pinned the
// accepted-then-dropped failure the fold fixed: the person id, the timestamp
// and the kind all survive now.
func TestCanonicalDoorDecodesTeamBatch(t *testing.T) {
	evs, err := decodeIngest([]byte(teamWire))
	if err != nil {
		t.Fatalf("decodeIngest: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("decodeIngest events = %d, want 2", len(evs))
	}
	if evs[0].DistinctID == "" {
		t.Error("the team person id (distinct_id) must survive the canonical decode")
	}
	if evs[0].Timestamp == "" {
		t.Error("the team epoch-millis timestamp must survive the canonical decode")
	}
	if evs[0].Type == "" {
		t.Error("the team kind must be named — an unnamed kind is dropped whole by admitPublic")
	}
}

// TestTeamWireLands is the other half: the SAME bytes through the team door survive
// admission AND normalize into real facts. It walks the WHOLE pipeline offline —
// decode, the anonymous projection, the exception fold, then normalize (fact.go),
// which is the last function before the publish. A fact out of normalize with
// ok==true is what "the event landed" means everywhere else in this package. The
// error lands on event.error (its own table), the navigation on event.event as
// kind=page — the plane vocabulary, not the wide table's $-names.
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
	want := []struct {
		sig  signal
		kind string
	}{{signalError, ""}, {signalAct, kindPage}}
	for i, ev := range admitted {
		f, ok := normalize("acme", now, foldException(ev))
		if !ok {
			t.Fatalf("event %d did not normalize into a fact", i)
		}
		if f.signal != want[i].sig || f.kind != want[i].kind {
			t.Errorf("event %d = (%s,%q), want (%s,%q)", i, f.signal, f.kind, want[i].sig, want[i].kind)
		}
		if f.org != "acme" {
			t.Errorf("event %d org = %q, want acme", i, f.org)
		}
	}
}

// TestTeamWireKeepsIdentityOnFullLane: on the vouched-for lane there is no projection,
// so the two fields the canonical wire lost must both survive — the snake_case person
// id and the epoch-millis timestamp converted to the instant the SPA meant.
//
// The millis here are COMPUTED from now, not taken from the teamWire capture, and that
// is load-bearing: the capture's fixed 1750000000000 is June 2025, which is outside
// maxBackdate, so asserting it survived verbatim would have been asserting that no past
// bound exists. Real SPA traffic is minutes old; a fresh value is the fixture that
// actually exercises millis→instant.
func TestTeamWireKeepsIdentityOnFullLane(t *testing.T) {
	want := time.Now().UTC().Add(-90 * time.Second).Truncate(time.Second)
	wire := fmt.Sprintf(
		`[{"event":"error","properties":{"error_message":"boom","$anonymous_id":"anon_1"},"timestamp":%d,"distinct_id":"user@hanzo.ai"}]`,
		want.UnixMilli())
	evs, err := decodeTeam([]byte(wire))
	if err != nil {
		t.Fatalf("decodeTeam: %v", err)
	}
	f, ok := normalize("acme", time.Now().UTC(), foldException(evs[0]))
	if !ok {
		t.Fatal("did not normalize")
	}
	if f.distinct != "user@hanzo.ai" {
		t.Errorf("distinct = %q, want user@hanzo.ai", f.distinct)
	}
	if f.anonymous != "anon_1" {
		t.Errorf("anonymous = %q, want anon_1", f.anonymous)
	}
	if got := f.time.UTC(); !got.Equal(want) {
		t.Errorf("time = %s, want %s", got, want)
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
		// Whatever the kind, it must produce a storable fact.
		if _, ok := normalize("acme", time.Now().UTC(), foldException(evs[0])); !ok {
			t.Errorf("%s did not normalize into a fact", c.event)
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
	// End to end: the secret in the stack must not appear raw in the stored fact —
	// neither in the attributes map nor in the fault's own free text.
	f, ok := normalize("acme", time.Now().UTC(), foldException(e))
	if !ok {
		t.Fatal("did not normalize")
	}
	stored := fmt.Sprintf("%v", f.attributes) + " " + f.message + " " + f.class
	for _, fr := range f.frames {
		stored += " " + fr.file + " " + fr.function
	}
	if strings.Contains(stored, "sk-live-DEADBEEF") {
		t.Errorf("raw secret from the stack reached the stored fact: %s", stored)
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
	f, _ := normalize("acme", now, foldException(evs[0]))
	if f.time.Before(now.Add(-time.Minute)) {
		t.Errorf("absent timestamp stored as %s, want ~now", f.time)
	}
}

// TestTeamEpochMillisCannotReach1970 is the same 1970 hazard from the other side, and it
// is the reason the past bound lives in clampTS rather than in each decoder. teamTime is
// correct about ZERO (it returns "" so the write core anchors to server-now) and has no
// opinion about ONE: `"timestamp":1` is a well-formed epoch-millis that decodes to
// 1970-01-01, which the write core used to accept verbatim into the time half of
// ORDER BY. That is the widest possible key range from the smallest possible request, on
// the one event door.
func TestTeamEpochMillisCannotReach1970(t *testing.T) {
	evs, err := decodeTeam([]byte(`[{"event":"error","properties":{"error_message":"x"},"timestamp":1,"distinct_id":"u"}]`))
	if err != nil {
		t.Fatalf("decodeTeam: %v", err)
	}
	// The decoder is not where this is fixed: it faithfully renders the millis it was
	// given, and that is its job.
	if evs[0].Timestamp != "1970-01-01T00:00:00Z" {
		t.Fatalf("decodeTeam rendered %q, want the epoch — this test no longer exercises the hazard it names", evs[0].Timestamp)
	}
	now := time.Now().UTC()
	f, ok := normalize("acme", now, foldException(evs[0]))
	if !ok {
		t.Fatal("did not normalize")
	}
	if f.time.Before(now.Add(-maxBackdate)) {
		t.Errorf("the stored fact sits at %s — a 1-byte timestamp bought a key range back to the epoch", f.time)
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
	// role defaults to member — the ordinary caller selectWorkspace mints. Pass
	// extra{"role":"guest"} for a guest, extra{"role":""} for a token that never
	// proved a role.
	e := map[string]any{"org": org, "role": token.RoleMember}
	maps.Copy(e, extra)
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

	// The batch must be something ONLY full capability can store. error+navigation is
	// not: both kinds are in publicKinds, so the projection stores them identically and
	// deleting the teamTenant clause entirely would have gone unnoticed.
	//
	// A customEvent is the discriminator. canonicalType is "event", which is NOT in
	// publicKinds, so the projection drops it and the write core is never reached
	// (401, nothing stored) — while a resolved member writes it and hits the absent
	// warehouse (503).
	const custom = `[{"event":"customEvent","properties":{"event":"checkout_started","revenue":42},"timestamp":1750000000000,"distinct_id":"u"}]`

	code, res := postBody(t, app, "/v1/event", custom, tok)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("member POST = %d %+v, want 503 (full capability reached the write core)", code, res)
	}
	// Same bytes, NO credential: dropped by the projection, never reaching the store.
	if code, res := postBody(t, app, "/v1/event", custom, ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous POST = %d %+v, want 401 (projection dropped it, nothing stored)", code, res)
	}
	// And the resolution itself names the signed org at full capability.
	org, ok := resolvedTeamOrg(t, app, tok)
	if !ok || org != "acme" {
		t.Fatalf("teamTenant = (%q, %v), want (acme, true)", org, ok)
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SERVER_SECRET", c.env)
			app := mountApp(t)
			bearer := c.bearer(t)
			// A team token that does not resolve is REFUSED, not downgraded. This
			// assertion used to accept "503 or 200", which did not merely miss the
			// weakness — it ENFORCED it: the fix (naming the team bearer in
			// presented()) turns these into 403 and would have failed the old test.
			//
			// 200-with-rows-under-$public is the exact pathology this door exists to
			// prevent: the caller sees success and the org cannot read its own data.
			code, _ := postBody(t, app, "/v1/event", teamWire, bearer)
			if code != http.StatusForbidden {
				t.Fatalf("POST = %d, want 403 (a presented team credential that does not resolve is refused)", code)
			}
			org, ok := resolvedTeamOrg(t, app, bearer)
			if ok {
				t.Fatalf("credential resolved to org %q; it must not resolve", org)
			}
		})
	}
}

// resolvedTeamOrg runs teamTenant against a real request context carrying the bearer,
// and returns the org it resolved to (empty when it refused). It exercises the SAME
// function eventTenant calls, so the refusal table above is a statement about
// production admission, not about a copy of it.
func resolvedTeamOrg(t *testing.T, app *zip.App, bearer string) (string, bool) {
	t.Helper()
	var got string
	var resolved bool
	probe := zip.New(zip.Config{})
	probe.Post("/probe", func(c *zip.Ctx) error {
		a, ok := teamTenant(c)
		// Record ok INDEPENDENTLY of the org. The previous version assigned only when
		// ok, so a mutant returning ("", true) — which normalize would store as
		// an empty tenant column and fanOut would forward under an empty org — was
		// indistinguishable from a clean refusal.
		got, resolved = a.org, ok
		return c.JSON(http.StatusOK, map[string]string{})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader("[]"))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := probe.Test(req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return got, resolved
}

// ── the door ─────────────────────────────────────────────────────────────────

// TestTeamDoorIsRegistered proves the canonical door carries the team wire:
// /v1/event dispatches the team array by shape (isTeamArray), so team events
// survive admission and REACH the write core — 503 in this warehouse-less
// harness. A wrong or missing route would 404/405; a decode regression that
// silently dropped the batch would answer 200 dropped=2.
func TestTeamDoorIsRegistered(t *testing.T) {
	t.Setenv("SERVER_SECRET", "a-real-team-secret")
	app := mountApp(t)
	tok := teamToken(t, "acme", "a-real-team-secret", nil, time.Now().Add(time.Hour).Unix())

	if code, res := postBody(t, app, "/v1/event", teamWire, tok); code != http.StatusServiceUnavailable {
		t.Fatalf("/v1/event with team wire = %d %+v, want 503 (events must survive admission and reach the write core)", code, res)
	}
}

// ── capability: the guest lane, and the trust order ──────────────────────────

// TestGuestWritesProjectedIntoItsOwnOrg is the F1 fix, stated positively. A guest is
// neither trusted with the org's whole custom/billing surface nor exiled to $public:
// it writes into its OWN org, through the projection.
//
// The attack this closes: accept a guest invite, lift presentation.metadata.Token out
// of the tab, POST an `order_completed` with a revenue figure, and it landed as an
// unprojected row in the host org — plus a fanOut to that org's GA4/Meta CAPI.
func TestGuestWritesProjectedIntoItsOwnOrg(t *testing.T) {
	t.Setenv("SERVER_SECRET", "a-real-team-secret")
	app := mountApp(t)
	hour := time.Now().Add(time.Hour).Unix()
	guest := teamToken(t, "acme", "a-real-team-secret", map[string]any{"role": token.RoleGuest}, hour)
	member := teamToken(t, "acme", "a-real-team-secret", nil, hour)

	// The forgeable payload: a custom event carrying revenue. kind "event" is not in
	// publicKinds, so the projection drops it whole.
	const revenue = `[{"event":"customEvent","properties":{"event":"order_completed","revenue":99999},"timestamp":1750000000000,"distinct_id":"u"}]`
	// 403, not 401: the guest's token RESOLVED. It holds a credential and it is not the
	// problem — it lacks capability — so telling it a key is required would send it to
	// mint a second one and hit the identical wall (cannotWrite, event.go).
	code, res := postBody(t, app, "/v1/event", revenue, guest)
	if code != http.StatusForbidden {
		t.Fatalf("guest revenue POST = %d %+v, want 403 (projected away, nothing stored)", code, res)
	}
	// A member CAN write it — so the refusal is about the role, not the payload.
	if code, _ := postBody(t, app, "/v1/event", revenue, member); code != http.StatusServiceUnavailable {
		t.Fatalf("member revenue POST = %d, want 503 (reached the write core)", code)
	}

	// But the guest is NOT silenced: its errors/pageviews still land, and they land in
	// ITS OWN org — not $public, which acme could never read.
	code, res = postBody(t, app, "/v1/event", teamWire, guest)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("guest error/pageview POST = %d %+v, want 503 (admitted, reached the store)", code, res)
	}
	a, ok := teamAdmission(t, guest)
	if !ok || a.org != "acme" || a.full {
		t.Fatalf("guest admission = %+v ok=%v, want org=acme full=false", a, ok)
	}
	a, ok = teamAdmission(t, member)
	if !ok || a.org != "acme" || !a.full {
		t.Fatalf("member admission = %+v ok=%v, want org=acme full=true", a, ok)
	}
}

// TestUnprovenRoleIsNotPrivileged: fail-closed on the claim's absence. A token that
// never proved a workspace role gets the projection, not the benefit of the doubt.
func TestUnprovenRoleIsNotPrivileged(t *testing.T) {
	t.Setenv("SERVER_SECRET", "a-real-team-secret")
	hour := time.Now().Add(time.Hour).Unix()
	for _, role := range []string{"", "observer", "GUEST", "Member", "owner ,admin"} {
		tok := teamToken(t, "acme", "a-real-team-secret", map[string]any{"role": role}, hour)
		a, ok := teamAdmission(t, tok)
		if !ok {
			t.Fatalf("role %q did not resolve at all; it should resolve at reduced capability", role)
		}
		if a.full {
			t.Errorf("role %q was treated as PRIVILEGED; only owner/admin/member are", role)
		}
	}
	// The three that ARE privileged, so the allowlist is not vacuously empty.
	for _, role := range []string{token.RoleOwner, token.RoleAdmin, token.RoleMember} {
		tok := teamToken(t, "acme", "a-real-team-secret", map[string]any{"role": role}, hour)
		if a, ok := teamAdmission(t, tok); !ok || !a.full {
			t.Errorf("role %q = %+v ok=%v, want full", role, a, ok)
		}
	}
	// Surrounding whitespace IS trimmed, deliberately: reading a claim has ONE spelling
	// across this package and clients/meet, which is the fix for the two guards that
	// used to disagree about it. That is safe here because the role is not
	// caller-supplied — selectWorkspace signs it from a validInviteRole-checked DB
	// column, so " member " has no production path. Case is NOT folded ("Member" above
	// is unprivileged), so the allowlist stays exact where it can be.
	tok := teamToken(t, "acme", "a-real-team-secret", map[string]any{"role": " member "}, hour)
	if a, ok := teamAdmission(t, tok); !ok || !a.full {
		t.Errorf("a whitespace-padded role = %+v ok=%v; claim reads are trimmed by design", a, ok)
	}
}

// TestInertClaimsGrantAndReduceNothing pins the dead claims as dead. extra.guest and
// extra.readonly were the old guards' inputs and NOTHING mints them; asserting they are
// inert stops someone "restoring" the guards and believing they protect anything.
func TestInertClaimsGrantAndReduceNothing(t *testing.T) {
	t.Setenv("SERVER_SECRET", "a-real-team-secret")
	hour := time.Now().Add(time.Hour).Unix()
	// On a member they do not REDUCE.
	tok := teamToken(t, "acme", "a-real-team-secret",
		map[string]any{"guest": "true", "readonly": "true"}, hour)
	if a, ok := teamAdmission(t, tok); !ok || !a.full {
		t.Errorf("inert claims reduced a member: %+v ok=%v", a, ok)
	}
	// On a guest they do not ELEVATE.
	tok = teamToken(t, "acme", "a-real-team-secret",
		map[string]any{"role": token.RoleGuest, "guest": "false", "readonly": "false"}, hour)
	if a, ok := teamAdmission(t, tok); !ok || a.full {
		t.Errorf("inert claims elevated a guest: %+v ok=%v", a, ok)
	}
}

// TestTrustOrderPrefersTheApiCredential makes the documented ordering OBSERVABLE.
// eventTenant's comment says the team token is last so a request holding both is
// attributed to the deliberate API credential — but nothing tested it, so swapping the
// order was a free mutation.
func TestTrustOrderPrefersTheApiCredential(t *testing.T) {
	t.Setenv("SERVER_SECRET", "a-real-team-secret")
	// Stand in for IAM's key seam: this key belongs to org "keyorg".
	prev := resolveKeyOrg
	resolveKeyOrg = func(_ context.Context, key string) (string, bool) {
		if key == "sk-the-key" {
			return "keyorg", true
		}
		return "", false
	}
	defer func() { resolveKeyOrg = prev }()

	tok := teamToken(t, "teamorg", "a-real-team-secret", nil, time.Now().Add(time.Hour).Unix())
	got, ok := tenantWith(t, map[string]string{
		"Authorization": "Bearer " + tok, // team token -> teamorg
		"x-api-key":     "sk-the-key",    // API key    -> keyorg
	})
	if !ok {
		t.Fatal("nothing resolved with both credentials present")
	}
	if got.org != "keyorg" {
		t.Fatalf("resolved org = %q, want keyorg — the API credential must win over a tab's session", got.org)
	}
	// With ONLY the team token, it does resolve — so the assertion above is about
	// precedence, not about the team token being ignored.
	if got, ok := tenantWith(t, map[string]string{"Authorization": "Bearer " + tok}); !ok || got.org != "teamorg" {
		t.Fatalf("team-only resolved (%+v, %v), want teamorg", got, ok)
	}
}

// teamAdmission runs teamTenant against a real request context carrying the bearer.
func teamAdmission(t *testing.T, bearer string) (admission, bool) {
	t.Helper()
	return runTenant(t, map[string]string{"Authorization": "Bearer " + bearer}, func(c *zip.Ctx) (admission, bool) {
		return teamTenant(c)
	})
}

// tenantWith runs the FULL eventTenant trust order over a set of headers.
func tenantWith(t *testing.T, headers map[string]string) (admission, bool) {
	t.Helper()
	return runTenant(t, headers, eventTenant)
}

func runTenant(t *testing.T, headers map[string]string, fn func(*zip.Ctx) (admission, bool)) (admission, bool) {
	t.Helper()
	var got admission
	var ok bool
	probe := zip.New(zip.Config{})
	probe.Post("/probe", func(c *zip.Ctx) error {
		got, ok = fn(c)
		return c.JSON(http.StatusOK, map[string]string{})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader("[]"))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := probe.Test(req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return got, ok
}

// TestUnidentifiableBearerIsNotPresented is the reason presented() names the team
// bearer STRUCTURALLY rather than treating every Bearer as presented. A stale or
// foreign JWT — no `account` claim — is not evidence of a credential, so it reads as
// "presented nothing": 401, telling the caller to get a key. A 403 would assert its
// key is broken, on evidence we do not have.
func TestUnidentifiableBearerIsNotPresented(t *testing.T) {
	t.Setenv("SERVER_SECRET", "a-real-team-secret")
	app := mountApp(t)
	// A well-formed JWT with no `account` claim (an IAM-shaped bearer).
	foreign := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJzdWIiOiJ1c2VyLTEiLCJpc3MiOiJodHRwczovL2hhbnpvLmlkIn0.c2ln"
	if code, _ := postBody(t, app, "/v1/event", teamWire, foreign); code != http.StatusUnauthorized {
		t.Fatalf("foreign bearer = %d, want 401 (not presented), NOT 403", code)
	}
	if teamPresented2(t, foreign) {
		t.Error("a bearer with no account claim was counted as a presented team credential")
	}
	// A team-shaped bearer IS counted, which is what makes the 403 path fire.
	tok := teamToken(t, "acme", "another-secret", nil, time.Now().Add(time.Hour).Unix())
	if !teamPresented2(t, tok) {
		t.Error("a team-shaped bearer was not counted as presented")
	}
}

func teamPresented2(t *testing.T, bearer string) bool {
	t.Helper()
	var got bool
	probe := zip.New(zip.Config{})
	probe.Post("/probe", func(c *zip.Ctx) error {
		got = teamPresented(c)
		return c.JSON(http.StatusOK, map[string]string{})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader("[]"))
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, _ := probe.Test(req)
	defer func() { _ = resp.Body.Close() }()
	return got
}

// ── the reduced lane, observed at the write core ─────────────────────────────

// TestGuestRowsLandInItsOwnOrg binds the reduced lane to where the row ACTUALLY
// lands. TestGuestWritesProjectedIntoItsOwnOrg checks the org on teamAdmission — a
// pure function — and then uses a 503 as its end-to-end proof, which the absent
// warehouse produces either way. With the fake warehouse the tenant column is
// directly observable.
func TestGuestRowsLandInItsOwnOrg(t *testing.T) {
	t.Setenv("SERVER_SECRET", "a-real-team-secret")
	roomyRate(t)
	w := fakeWarehouse(t)
	app := mountApp(t)
	guest := teamToken(t, "acme", "a-real-team-secret",
		map[string]any{"role": token.RoleGuest}, time.Now().Add(time.Hour).Unix())

	// One event per request; tenants() reports per committed FACT, so two requests
	// are two facts. Both kinds are exercised, one request each — the error lands on
	// event.error and the navigation on event.event, under the same org.
	for _, body := range []string{
		`[{"event":"error","properties":{"error_message":"boom"},"timestamp":1750000000000,"distinct_id":"u"}]`,
		`[{"event":"navigation","properties":{"path":"/pricing"},"timestamp":1750000000000,"distinct_id":"u"}]`,
	} {
		code, res := postBody(t, app, "/v1/event", body, guest)
		if code != http.StatusOK || res.Accepted != 1 {
			t.Fatalf("guest POST = %d %+v, want 200 accepted:1 (admitted and written)", code, res)
		}
	}
	got := w.tenants(t)
	if len(got) != 2 {
		t.Fatalf("wrote %d statements, want 2", len(got))
	}
	for _, g := range got {
		if g != "acme" {
			t.Errorf("tenant = %q, want acme (the SIGNED org) — a guest's rows must land where "+
				"its own org can read them", g)
		}
	}
}

// TestReducedLaneAttributesToTheSignedAccount is N-5. The projection strips revenue,
// personId, groupId and the event name — but distinct_id survives, because it is the
// join key that makes the lane useful. The team SPA puts an ACCOUNT IDENTIFIER there, so
// on a real org a guest could attribute pageviews and errors to a named colleague. The
// fix is not to strip it but to stop taking it from the caller.
func TestReducedLaneAttributesToTheSignedAccount(t *testing.T) {
	t.Setenv("SERVER_SECRET", "a-real-team-secret")
	roomyRate(t)
	w := fakeWarehouse(t)
	app := mountApp(t)
	guest := teamToken(t, "acme", "a-real-team-secret",
		map[string]any{"role": token.RoleGuest}, time.Now().Add(time.Hour).Unix())

	// The forgery: claim a colleague as the person, and a colleague's anonymous alias.
	const victim = "ada@acme.example"
	body := `[{"event":"navigation","properties":{"path":"/salaries","$anonymous_id":"anon-of-ada"},` +
		`"timestamp":1750000000000,"distinct_id":"` + victim + `"}]`
	if code, res := postBody(t, app, "/v1/event", body, guest); code != http.StatusOK || res.Accepted != 1 {
		t.Fatalf("guest POST = %d %+v, want 200 accepted:1", code, res)
	}
	if got := w.facts[0].distinct; got == victim {
		t.Fatalf("the guest attributed its pageview to %q — person-level forgery inside a real tenant", victim)
	}
	if got := w.facts[0].distinct; got != teamAccount {
		t.Errorf("distinct_id = %v, want the SIGNED account %q", got, teamAccount)
	}
	// The pre-login alias is cleared: it exists to stitch an anonymous session to a
	// person later, and there is nothing to stitch when the person is already known.
	if got := w.facts[0].anonymous; got != "" {
		t.Errorf("anonymous_id = %v, want empty on the attributed lane", got)
	}

	// The FULL lane is unchanged: a member is trusted to attribute its own writes, so
	// the distinct_id it sends is the one stored. Without this, the test above would
	// pass for a version that clobbered identity everywhere.
	member := teamToken(t, "acme", "a-real-team-secret", nil, time.Now().Add(time.Hour).Unix())
	w2 := fakeWarehouse(t)
	if code, res := postBody(t, app, "/v1/event", body, member); code != http.StatusOK || res.Accepted != 1 {
		t.Fatalf("member POST = %d %+v, want 200 accepted:1", code, res)
	}
	if got := w2.facts[0].distinct; got != victim {
		t.Errorf("member distinct_id = %v, want %q — the full lane must not be rewritten", got, victim)
	}
}

// TestAnonymousWritesNothing: a credential-less beacon is refused and reaches the
// warehouse not at all. There is no anonymous tenant to file it under, so the
// identity question the projection used to answer for it does not arise.
func TestAnonymousWritesNothing(t *testing.T) {
	t.Setenv("SERVER_SECRET", "a-real-team-secret")
	roomyRate(t)
	w := fakeWarehouse(t)
	app := mountApp(t)
	body := `[{"event":"navigation","properties":{"path":"/pricing"},"timestamp":1750000000000,"distinct_id":"visitor-7"}]`
	code, res := postBody(t, app, "/v1/event", body, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous POST = %d %+v, want 401", code, res)
	}
	if got := w.tenants(t); len(got) != 0 {
		t.Fatalf("anonymous wrote tenants %v, want none", got)
	}
}
