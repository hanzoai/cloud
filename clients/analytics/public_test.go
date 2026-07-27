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

// ── harness ─────────────────────────────────────────────────────────────────

// postAnon issues an ANONYMOUS POST — no X-User-Id, no X-Org-Id, no key of any kind —
// with optional extra headers, returning the status and body. This is exactly the
// shape a logged-out marketing page emits.
func postAnon(t *testing.T, app *zip.App, path, body string, hdr map[string]string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// receipt decodes the {accepted,dropped} contract.
func receipt(t *testing.T, body []byte) CaptureResult {
	t.Helper()
	var r CaptureResult
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("receipt not valid json: %v (%s)", err, body)
	}
	return r
}

// tightenPublicRate installs fresh anonymous-ingest counters with the given limits and
// restores the package pair afterwards, so a rate test is hermetic and cannot leak a
// spent bucket into another test.
func tightenPublicRate(t *testing.T, client, peer int) {
	t.Helper()
	origClient, origPeer := publicRate, publicPeerRate
	publicRate = newCounter(client, publicRateWindow)
	publicPeerRate = newCounter(peer, publicRateWindow)
	t.Cleanup(func() { publicRate, publicPeerRate = origClient, origPeer })
}

// The marketing wire: @hanzo/event batches {batch:[…]} and a logged-out page sends it
// with no credentials at all.
const anonPageview = `{"batch":[{"type":"pageview","event":"$pageview","distinctId":"anon-1",` +
	`"sessionId":"s1","product":"site","url":"https://hanzo.ai/cloud","path":"/cloud",` +
	`"referrer":"https://google.com/","utm":{"source":"google","medium":"organic"},` +
	`"library":"@hanzo/event","libraryVersion":"0.3.3"}]}`

// ── admitPublic: the pure attribution decision ──────────────────────────────

// TestAdmitPublic_TenantIsAlwaysPublic is the ISOLATION PROOF. admitPublic takes no
// request, so no header/query/body can reach it; here we additionally hand it every
// org-naming field the wire has and confirm the tenant it returns is the reserved
// constant every time.
func TestAdmitPublic_TenantIsAlwaysPublic(t *testing.T) {
	hostile := [][]CaptureEvent{
		{{Type: "pageview"}},
		{{Type: "pageview", GroupID: "maxpower"}},
		{{Type: "pageview", PersonID: "victim-person"}},
		{{Type: "error", GroupID: "maxpower", PersonID: "victim", Error: &Exception{Message: "boom"}}},
		{{Type: "pageview", Properties: map[string]any{"org": "maxpower", "tenant_id": "maxpower"}}},
		{},
		nil,
	}
	for i, evs := range hostile {
		org, _, _ := admitPublic(evs)
		if org != publicTenant {
			t.Fatalf("case %d: admitPublic org = %q, want the reserved %q", i, org, publicTenant)
		}
	}
}

// TestPublicTenantOutsideOrgNamespace pins WHY the sentinel is safe: an IAM org slug is
// lowercase ASCII alphanumerics and '-' (that is all the IAM slugifier emits), so a
// '$'-carrying tenant cannot collide with a real org.
func TestPublicTenantOutsideOrgNamespace(t *testing.T) {
	if !strings.HasPrefix(publicTenant, "$") {
		t.Fatalf("publicTenant %q must carry the reserved '$' so no IAM slug can collide", publicTenant)
	}
	for _, r := range publicTenant[1:] {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			t.Fatalf("publicTenant %q: unexpected byte %q", publicTenant, r)
		}
	}
}

// TestAdmitPublic_ForeignOrgFieldsDropped: a foreign org can be NAMED in a body, and
// the projection must not carry it forward. groupId (a group/org) and personId (a
// person) are the two fields that bind an event to someone else's identity.
func TestAdmitPublic_ForeignOrgFieldsDropped(t *testing.T) {
	_, out, _ := admitPublic([]CaptureEvent{{
		Type:       "pageview",
		GroupID:    "maxpower",
		PersonID:   "victim-person",
		RefCode:    "r1",
		Channel:    "paid",
		SignupWeek: "2026-W30",
		ProductID:  "prod_1",
		Quantity:   7,
		Revenue:    999.99,
		Currency:   "USD",
		Properties: map[string]any{"org": "maxpower", "email": "v@example.com"},
	}})
	if len(out) != 1 {
		t.Fatalf("want 1 admitted event, got %d", len(out))
	}
	got := out[0]
	if got.GroupID != "" || got.PersonID != "" {
		t.Fatalf("identity fields survived the projection: groupId=%q personId=%q", got.GroupID, got.PersonID)
	}
	if got.Revenue != 0 || got.ProductID != "" || got.Quantity != 0 || got.Currency != "" {
		t.Fatalf("commerce fields survived: %+v", got)
	}
	if got.RefCode != "" || got.Channel != "" || got.SignupWeek != "" {
		t.Fatalf("attribution/cohort fields survived: %+v", got)
	}
	if len(got.Properties) != 0 {
		t.Fatalf("client property bag survived the projection: %v", got.Properties)
	}
}

// TestAdmitPublic_ForeignOrgNeverStamped drives the projection through the REAL
// normalizer — the one function that stamps tenant_id — and proves the stored row
// carries the public tenant, not the org the body named.
func TestAdmitPublic_ForeignOrgNeverStamped(t *testing.T) {
	org, out, _ := admitPublic([]CaptureEvent{{Type: "pageview", GroupID: "maxpower"}})
	row, ok := normalizeEvent(org, time.Now(), out[0])
	if !ok {
		t.Fatal("want routable")
	}
	if row.tenant != publicTenant {
		t.Fatalf("row.tenant = %q, want %q — an anonymous write must never land in a real org", row.tenant, publicTenant)
	}
	if row.tenant == "maxpower" || row.groupID == "maxpower" {
		t.Fatalf("the body-named org reached the row: tenant=%q group=%q", row.tenant, row.groupID)
	}
}

// TestAdmitPublic_KindAllowlist: pageview and error are admitted; identify, group, and
// every custom event are dropped and counted. This is the allowlist, not a denylist —
// an unknown type folds to "event" (canonicalType) and is therefore dropped too.
func TestAdmitPublic_KindAllowlist(t *testing.T) {
	for _, kind := range []string{"pageview", "page", "error"} {
		_, out, dropped := admitPublic([]CaptureEvent{{Type: kind, Error: &Exception{Message: "m"}}})
		if len(out) != 1 || dropped != 0 {
			t.Fatalf("kind %q must be admitted, got out=%d dropped=%d", kind, len(out), dropped)
		}
	}
	for _, kind := range []string{"identify", "group", "event", "", "purchase", "metering", "BILLING"} {
		_, out, dropped := admitPublic([]CaptureEvent{{Type: kind, Event: "whatever"}})
		if len(out) != 0 || dropped != 1 {
			t.Fatalf("kind %q must be dropped, got out=%d dropped=%d", kind, len(out), dropped)
		}
	}
}

// TestAdmitPublic_NameIsServerChosen: the anonymous name space is CLOSED to two values.
// A caller cannot introduce a new event name into the read lenses, nor unbounded
// cardinality into the table's ORDER BY key.
func TestAdmitPublic_NameIsServerChosen(t *testing.T) {
	cases := []struct{ kind, sent, want string }{
		{"pageview", "attacker_chosen_name", "$pageview"},
		{"pageview", "", "$pageview"},
		{"error", strings.Repeat("x", 4096), "$error"},
		{"error", "", "$error"},
	}
	for _, c := range cases {
		_, out, _ := admitPublic([]CaptureEvent{{Type: c.kind, Event: c.sent}})
		if len(out) != 1 {
			t.Fatalf("kind %q: want 1 admitted", c.kind)
		}
		if got := resolveEventName(out[0]); got != c.want {
			t.Fatalf("kind %q sent name %q ⇒ stored %q, want %q", c.kind, truncate(c.sent), got, c.want)
		}
	}
}

func truncate(s string) string {
	if len(s) > 24 {
		return s[:24] + "…"
	}
	return s
}

// TestAdmitPublic_OnlyServerProperties: an anonymous row's properties are exactly what
// the SERVER put there — the $exception the shared tail folds and the $source the write
// core stamps. No key the caller chose can be persisted. This walks the SAME composition
// the handler does: admitPublic (projection) → foldException (ingestDecoded's tail) →
// withSource (write core) → normalizeEvent (the row).
func TestAdmitPublic_OnlyServerProperties(t *testing.T) {
	_, out, _ := admitPublic([]CaptureEvent{{
		Type:       "error",
		Error:      &Exception{Type: "TypeError", Message: "x is not a function"},
		Properties: map[string]any{"password": "hunter2", "custom": "junk", "$release": "v1"},
	}})
	if len(out) != 1 {
		t.Fatal("want 1 admitted error event")
	}
	// The projection itself carries NO properties — the client's bag is gone entirely.
	if len(out[0].Properties) != 0 {
		t.Fatalf("the client property bag survived the projection: %v", out[0].Properties)
	}
	// The shared tail folds the typed error, and nothing else appears.
	folded := foldException(out[0])
	if len(folded.Properties) != 1 {
		t.Fatalf("after the shared fold, properties must hold only $exception, got %v", folded.Properties)
	}
	if _, ok := folded.Properties["$exception"]; !ok {
		t.Fatalf("the typed error must be folded to $exception, got %v", folded.Properties)
	}
	// Through the real normalizer the stored JSON carries $exception + $source, nothing else.
	row, ok := normalizeEvent(publicTenant, time.Now(), CaptureEvent{
		Type: folded.Type, Properties: withSource(folded.Properties, sourceEvent),
	})
	if !ok {
		t.Fatal("want routable")
	}
	stored := decodeProps(t, row.properties)
	if len(stored) != 2 || stored["$source"] != "event" {
		t.Fatalf("stored properties = %v, want exactly {$exception,$source}", stored)
	}
}

// ── the route: POST /v1/event with no credential at all ─────────────────────
//
// Observable proxy, as everywhere in this package: 403 ⇒ refused at the gate; 503 ⇒
// ADMITTED and reached requireDatastore (no warehouse in the harness). A 200 means the
// door answered without needing the warehouse — an all-dropped batch or an opt-out.

// TestPublic_AnonymousPageviewAccepted is the headline: the exact logged-out marketing
// wire, no bearer and no key, is ADMITTED. Before this change it was 403 and every
// marketing pageview was lost.
func TestPublic_AnonymousPageviewAccepted(t *testing.T) {
	app := mountApp(t)
	code, body := postAnon(t, app, "/v1/event", anonPageview, nil)
	if code == http.StatusForbidden {
		t.Fatalf("anonymous pageview must be admitted on the canonical door, got 403 (%s)", body)
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("anonymous pageview want 503 (admitted, datastore down), got %d (%s)", code, body)
	}
}

// TestPublic_AnonymousErrorAccepted: the other half of what marketing needs — a browser
// error from a logged-out page.
func TestPublic_AnonymousErrorAccepted(t *testing.T) {
	app := mountApp(t)
	code, body := postAnon(t, app, "/v1/event",
		`{"batch":[{"type":"error","error":{"type":"TypeError","message":"boom"},"path":"/cloud"}]}`, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("anonymous error want 503 (admitted, datastore down), got %d (%s)", code, body)
	}
}

// TestPublic_ForeignOrgClaimBuysNothing: an anonymous caller that names a foreign org
// EVERY way the wire allows — the X-Org-Id header, a body org/tenant field, groupId —
// is not refused (it is anonymous traffic) but gains nothing: the only kind it sent is
// non-allowlisted, so the receipt is 200 accepted:0 dropped:1 and no row exists to
// carry `maxpower`. The tenant it would have landed under is proven by
// TestAdmitPublic_ForeignOrgNeverStamped.
func TestPublic_ForeignOrgClaimBuysNothing(t *testing.T) {
	app := mountApp(t)
	code, body := postAnon(t, app, "/v1/event",
		`{"org":"maxpower","tenant_id":"maxpower","batch":[{"type":"event","event":"steal","groupId":"maxpower"}]}`,
		map[string]string{"X-Org-Id": "maxpower"})
	if code != http.StatusOK {
		t.Fatalf("forged-org anonymous batch want 200 (all dropped, no warehouse needed), got %d (%s)", code, body)
	}
	if r := receipt(t, body); r.Accepted != 0 || r.Dropped != 1 {
		t.Fatalf("receipt = %+v, want accepted:0 dropped:1", r)
	}
}

// TestPublic_NonAllowlistedKindRejected: a custom/product/billing event is refused
// storage anonymously and reported in the honest receipt. A mixed batch keeps its
// allowlisted events and drops the rest, which is why marketing telemetry lands while
// the arbitrary surface stays shut.
func TestPublic_NonAllowlistedKindRejected(t *testing.T) {
	app := mountApp(t)
	for _, body := range []string{
		`{"batch":[{"type":"event","event":"order_completed","revenue":99.5}]}`,
		`{"batch":[{"type":"identify","distinctId":"victim"}]}`,
		`{"batch":[{"type":"group","groupId":"maxpower"}]}`,
	} {
		code, got := postAnon(t, app, "/v1/event", body, nil)
		if code != http.StatusOK {
			t.Fatalf("non-allowlisted kind %s want 200 all-dropped, got %d (%s)", body, code, got)
		}
		if r := receipt(t, got); r.Accepted != 0 || r.Dropped != 1 {
			t.Fatalf("non-allowlisted kind %s receipt = %+v, want accepted:0 dropped:1", body, r)
		}
	}
	// Mixed batch: the pageview survives (so the request reaches the warehouse → 503),
	// the custom event does not.
	code, got := postAnon(t, app, "/v1/event",
		`{"batch":[{"type":"pageview"},{"type":"event","event":"order_completed"}]}`, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("mixed batch want 503 (the pageview is admitted), got %d (%s)", code, got)
	}
}

// TestPublic_OversizedRejected: both bounds REFUSE rather than truncate — a body past
// maxPublicBytes is 413 and a batch past maxPublicBatch is 400. Neither is silently
// trimmed, so the receipt can never overstate what was stored.
func TestPublic_OversizedRejected(t *testing.T) {
	app := mountApp(t)

	// Over the BYTE cap: one event whose URL alone exceeds maxPublicBytes.
	huge := `{"batch":[{"type":"pageview","url":"` + strings.Repeat("a", maxPublicBytes+1024) + `"}]}`
	if len(huge) <= maxPublicBytes {
		t.Fatalf("test body must exceed maxPublicBytes (%d), got %d", maxPublicBytes, len(huge))
	}
	code, body := postAnon(t, app, "/v1/event", huge, nil)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized anonymous body want 413, got %d (%s)", code, body)
	}

	// Over the COUNT cap, while staying comfortably under the byte cap: many tiny events.
	evs := make([]string, maxPublicBatch+1)
	for i := range evs {
		evs[i] = `{"type":"pageview"}`
	}
	many := `{"batch":[` + strings.Join(evs, ",") + `]}`
	if len(many) > maxPublicBytes {
		t.Fatalf("count-cap body must stay under the byte cap, got %d", len(many))
	}
	code, body = postAnon(t, app, "/v1/event", many, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("over-long anonymous batch want 400, got %d (%s)", code, body)
	}

	// At the caps the request is admitted, so the bounds are not off by one.
	ok := make([]string, maxPublicBatch)
	for i := range ok {
		ok[i] = `{"type":"pageview"}`
	}
	code, body = postAnon(t, app, "/v1/event", `{"batch":[`+strings.Join(ok, ",")+`]}`, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("batch AT the cap want 503 (admitted), got %d (%s)", code, body)
	}
}

// TestPublic_RateLimited: anonymous ingest is capped per client IP, and the cap is
// charged even when the caller supplies no forwarding header (an unkeyable caller
// shares one bucket rather than being exempt).
func TestPublic_RateLimited(t *testing.T) {
	app := mountApp(t)
	tightenPublicRate(t, 1, 1000)

	if code, body := postAnon(t, app, "/v1/event", anonPageview,
		map[string]string{"X-Forwarded-For": "203.0.113.7"}); code != http.StatusServiceUnavailable {
		t.Fatalf("first anonymous request want 503 (admitted), got %d (%s)", code, body)
	}
	if code, _ := postAnon(t, app, "/v1/event", anonPageview,
		map[string]string{"X-Forwarded-For": "203.0.113.7"}); code != http.StatusTooManyRequests {
		t.Fatalf("second anonymous request from the same IP want 429, got %d", code)
	}
	// A different client IP has its own bucket.
	if code, _ := postAnon(t, app, "/v1/event", anonPageview,
		map[string]string{"X-Forwarded-For": "198.51.100.4"}); code != http.StatusServiceUnavailable {
		t.Fatalf("a different client IP must have its own bucket, got %d", code)
	}
}

// TestPublic_PeerCeiling: the socket-peer bucket is charged independently, so a flood
// that ROTATES X-Forwarded-For (the header is caller-settable) still meets a ceiling.
func TestPublic_PeerCeiling(t *testing.T) {
	app := mountApp(t)
	tightenPublicRate(t, 1000, 1)

	if code, _ := postAnon(t, app, "/v1/event", anonPageview,
		map[string]string{"X-Forwarded-For": "203.0.113.1"}); code != http.StatusServiceUnavailable {
		t.Fatalf("first request want 503 (admitted), got %d", code)
	}
	if code, _ := postAnon(t, app, "/v1/event", anonPageview,
		map[string]string{"X-Forwarded-For": "203.0.113.2"}); code != http.StatusTooManyRequests {
		t.Fatalf("a rotated X-Forwarded-For must still meet the peer ceiling, got %d", code)
	}
}

// TestPublic_OptOutHonored: DNT / Sec-GPC on the wire means nothing is stored. The
// client's own consent gate is the host app's `enabled` flag, so an opted-out visitor
// usually sends nothing at all; this is the independent server-side line for when the
// header arrives anyway.
func TestPublic_OptOutHonored(t *testing.T) {
	app := mountApp(t)
	for _, h := range []map[string]string{{"DNT": "1"}, {"Sec-GPC": "1"}} {
		code, body := postAnon(t, app, "/v1/event", anonPageview, h)
		if code != http.StatusOK {
			t.Fatalf("opt-out %v want 200 (stored nothing, no warehouse touched), got %d (%s)", h, code, body)
		}
		if r := receipt(t, body); r.Accepted != 0 || r.Dropped != 1 {
			t.Fatalf("opt-out %v receipt = %+v, want accepted:0 dropped:1", h, r)
		}
	}
	// DNT:0 is NOT an opt-out — only the exact "1" signal is.
	if code, _ := postAnon(t, app, "/v1/event", anonPageview, map[string]string{"DNT": "0"}); code != http.StatusServiceUnavailable {
		t.Fatalf("DNT:0 must not suppress capture, got %d", code)
	}
}

// TestPublic_CaptureFlagOff: CLOUD_ANALYTICS_PUBLIC_CAPTURE is the ONE existing
// anonymous-capture switch, and turning it off restores the strict principal-only door.
func TestPublic_CaptureFlagOff(t *testing.T) {
	t.Setenv(publicCaptureEnv, "false")
	app := mountApp(t)
	if code, body := postAnon(t, app, "/v1/event", anonPageview, nil); code != http.StatusForbidden {
		t.Fatalf("public capture off ⇒ anonymous /v1/event want 403, got %d (%s)", code, body)
	}
}

// TestPublic_PresentedKeyStillFailsClosed: the anonymous lane is for a caller that
// presented NOTHING. A presented-but-unresolvable ingest key is still refused, never
// downgraded into the public bucket — a misconfigured key must not silently file its
// events where its owner cannot read them.
func TestPublic_PresentedKeyStillFailsClosed(t *testing.T) {
	app := mountApp(t)
	stubResolver(t, func(string) (string, bool) { return "", false })
	if code := postKeyed(t, app, "/v1/event", "hanzo.ai",
		`{"api_key":"hk-bad","batch":[{"type":"pageview"}]}`, nil); code != http.StatusForbidden {
		t.Fatalf("presented-but-unresolvable api_key want 403 (fail closed, not anonymous), got %d", code)
	}
	// Same for a publishable key IAM cannot resolve, on the sendBeacon-friendly carriers.
	if code := postKeyed(t, app, "/v1/event", "hanzo.ai", anonPageview,
		map[string]string{"x-hanzo-ingest-key": "pk-nosuch"}); code != http.StatusForbidden {
		t.Fatalf("presented-but-unresolvable pk- want 403 (fail closed, not anonymous), got %d", code)
	}
	if code := postKeyed(t, app, "/v1/event?ingest_key=pk-nosuch", "hanzo.ai", anonPageview,
		nil); code != http.StatusForbidden {
		t.Fatalf("presented-but-unresolvable ?ingest_key= want 403 (fail closed, not anonymous), got %d", code)
	}
}

// ── the authenticated lane is UNCHANGED ─────────────────────────────────────

// TestAuthenticated_KeepsFullCapability is the additivity proof. Every restriction the
// anonymous lane imposes — the kind allowlist, the batch cap, the field projection —
// must NOT reach a validated principal. Each case below is refused or trimmed
// anonymously and admitted whole with a bearer.
func TestAuthenticated_KeepsFullCapability(t *testing.T) {
	app := mountApp(t)

	// A custom product event with commerce fields and a property bag: dropped
	// anonymously, admitted (503, datastore down) for a real principal.
	commerce := `{"batch":[{"type":"event","event":"order_completed","revenue":99.5,` +
		`"productId":"prod_1","quantity":2,"currency":"USD","groupId":"acme-team",` +
		`"personId":"p1","properties":{"plan":"pro"}}]}`
	if code, body := postAnon(t, app, "/v1/event", commerce, nil); code != http.StatusOK {
		t.Fatalf("precondition: the commerce event must be dropped anonymously, got %d (%s)", code, body)
	}
	if code, body := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", commerce); code != http.StatusServiceUnavailable {
		t.Fatalf("bearer commerce event want 503 (admitted, full capability), got %d (%s)", code, body)
	}

	// identify/group name a person and a group: dropped anonymously, admitted with a bearer.
	for _, body := range []string{
		`{"batch":[{"type":"identify","distinctId":"u1","personId":"p1"}]}`,
		`{"batch":[{"type":"group","groupId":"acme-team"}]}`,
	} {
		if code, got := postAnon(t, app, "/v1/event", body, nil); code != http.StatusOK {
			t.Fatalf("precondition: %s must be dropped anonymously, got %d (%s)", body, code, got)
		}
		if code, got := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", body); code != http.StatusServiceUnavailable {
			t.Fatalf("bearer %s want 503 (admitted), got %d (%s)", body, code, got)
		}
	}

	// The public batch cap is NOT the authenticated cap: maxPublicBatch+1 events are
	// 400 anonymously and admitted with a bearer (the authenticated bound is maxBatch).
	evs := make([]string, maxPublicBatch+1)
	for i := range evs {
		evs[i] = `{"type":"pageview"}`
	}
	over := `{"batch":[` + strings.Join(evs, ",") + `]}`
	if code, _ := postAnon(t, app, "/v1/event", over, nil); code != http.StatusBadRequest {
		t.Fatalf("precondition: over-cap batch must be 400 anonymously, got %d", code)
	}
	if code, got := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", over); code != http.StatusServiceUnavailable {
		t.Fatalf("bearer over-public-cap batch want 503 (public cap does not apply), got %d (%s)", code, got)
	}
}

// TestAuthenticated_NotRateLimitedByPublicCounter: the anonymous flood cap must never
// throttle a validated principal. With the anonymous buckets exhausted, a bearer still
// gets through.
func TestAuthenticated_NotRateLimitedByPublicCounter(t *testing.T) {
	app := mountApp(t)
	tightenPublicRate(t, 0, 0)
	if code, _ := postAnon(t, app, "/v1/event", anonPageview, nil); code != http.StatusTooManyRequests {
		t.Fatalf("precondition: a zero-limit anonymous bucket must 429, got %d", code)
	}
	if code, body := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", anonPageview); code != http.StatusServiceUnavailable {
		t.Fatalf("bearer must be unaffected by the anonymous rate cap, got %d (%s)", code, body)
	}
}

// TestAuthenticated_OptOutNotHonoredForPrincipal: the DNT/GPC gate belongs to the
// anonymous lane. An authenticated product client sending the header is unchanged —
// first-party product telemetry is governed by the org's own consent, not by this door.
func TestAuthenticated_OptOutNotHonoredForPrincipal(t *testing.T) {
	app := mountApp(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/event", strings.NewReader(anonPageview))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", "user-dave")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("DNT", "1")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("bearer + DNT want 503 (authenticated lane untouched), got %d", resp.StatusCode)
	}
}

// ── the fan-out seam refuses the public tenant ──────────────────────────────

// TestFanOut_PublicTenantNeverReachesDestinations: destinations receive the RAW
// pre-scrub event and forward it to an org's connected ad platforms, so an unattested
// event must never reach one.
func TestFanOut_PublicTenantNeverReachesDestinations(t *testing.T) {
	var gotOrgs []string
	SetSink(func(org string, _ []SinkEvent) { gotOrgs = append(gotOrgs, org) })
	t.Cleanup(func() { SetSink(nil) })

	fanOut(publicTenant, []CaptureEvent{{Type: "pageview", Event: "$pageview"}})
	// The sink dispatches on a goroutine; give a real fan-out time to land.
	time.Sleep(50 * time.Millisecond)
	if len(gotOrgs) != 0 {
		t.Fatalf("the public tenant must never fan out to destinations, got orgs %v", gotOrgs)
	}

	// A real org still fans out — the guard is scoped to the sentinel, not a regression.
	fanOut("acme", []CaptureEvent{{Type: "pageview", Event: "$pageview"}})
	deadline := time.Now().Add(2 * time.Second)
	for len(gotOrgs) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(gotOrgs) != 1 || gotOrgs[0] != "acme" {
		t.Fatalf("a real org must still fan out, got %v", gotOrgs)
	}
}
