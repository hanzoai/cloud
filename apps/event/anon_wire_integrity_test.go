// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// anon_wire_integrity_test.go — the two ways the ONE door lost a caller's events
// while answering 200.
//
// Same observable as anon_capability_test.go: 503 ⇒ ADMITTED (reached the write core,
// no warehouse in the harness); 401 ingest_key_required ⇒ the projection refused every
// event; 403 ⇒ refused at the gate.

// ── 1. the canonical wire could not say what kind it was ─────────────────────

// TestAnonCanonicalWireCarriesItsKind: /v1/event PUBLISHES three shapes
// (openapi.OneOf{Event, []Event, CaptureBatch}) and they must MEAN the same thing.
// They did not. Event had no `type`, so toCapture left it empty, canonicalType mapped
// empty to "event", and "event" is not in publicKinds — so a bare canonical object or
// array was dropped on the anonymous lane EVERY time, with a 200 receipt, while the
// identical event inside {batch:[…]} was admitted.
//
// That is a document that lies: an SDK generated from it that picks the simplest of
// the three shapes silently loses 100% of logged-out traffic and reports success.
//
// Before the fix the first two subtests answered 200 {accepted:0,dropped:1}.
func TestAnonCanonicalWireCarriesItsKind(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, tc := range []struct{ name, body string }{
		{"bare object", `{"type":"pageview","event":"$pageview","distinctId":"anon-1","path":"/pricing"}`},
		{"bare array", `[{"type":"pageview","event":"$pageview","distinctId":"anon-1","path":"/pricing"}]`},
		{"batch envelope", `{"batch":[{"type":"pageview","event":"$pageview","distinctId":"anon-1","path":"/pricing"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := postAnon(t, app, "/v1/event", tc.body, nil)
			if code != http.StatusServiceUnavailable {
				t.Fatalf("anonymous pageview on the %s shape = %d (%s), want 503 ADMITTED — "+
					"all three published shapes of one wire must mean the same thing, or the "+
					"document lies to every generated SDK", tc.name, code, body)
			}
		})
	}
}

// TestAnonCanonicalWireStillCannotWidenItsKind: carrying `type` is a WIRE fix, never a
// capability one. The kind allowlist is still the whole anonymous surface, so the kinds
// that bind an event to a named person or open the custom product/billing surface stay
// dropped on the very shape that just learned to name them.
func TestAnonCanonicalWireStillCannotWidenItsKind(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, kind := range []string{"event", "identify", "group"} {
		body := `{"type":"` + kind + `","event":"order_completed","distinctId":"attacker",` +
			`"revenue":999999,"groupId":"victim-team","personId":"victim-person"}`
		code, got := doHost(t, app, "/v1/event", "", "", "api.hanzo.ai", body)
		if code == http.StatusServiceUnavailable {
			t.Errorf("anonymous kind %q on the bare canonical wire reached the write core — "+
				"the wire may now NAME a kind; it may not ADMIT one", kind)
			continue
		}
		refusedAnon(t, "anonymous kind "+kind, code, got)
	}
}

// ── 2. a failed platform key on the bearer carrier was filed under $public ────

// postAuth posts to a door with an Authorization header and nothing else.
func postAuth(t *testing.T, app *zip.App, path, auth, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestUnresolvableAccessKeyBearerRefuses: presented() names the carriers eventTenant
// consults so the two cannot disagree about what "presented" MEANS — and they did.
// ingestKey matches the bearer only for pk- (deliberately: an sk- bearer is IAM's
// to validate, and widening ingestKey would shadow the identity path). projectKey never
// reads Authorization at all. So an sk- bearer that FAILED to resolve fell through
// both and took the ANONYMOUS lane: 200, with the caller's rows filed under $public — a
// partition its owner cannot read.
//
// A revoked, rotated or mistyped platform key on the carrier every Hanzo caller reaches
// for FIRST is the most likely misconfiguration there is, and it answered success while
// losing everything. Same key on x-api-key already refused; now the bearer does too.
//
// Before the fix every case here answered 503 (admitted onto the anonymous lane).
func TestUnresolvableAccessKeyBearerRefuses(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	body := `{"batch":[{"type":"pageview","distinctId":"anon-1","path":"/pricing"}]}`
	for _, key := range []string{"sk-nonexistent-0001", "pk-nonexistent-0001"} {
		for _, door := range doors {
			code, got := postAuth(t, app, door.path, "Bearer "+key, body)
			if code != http.StatusForbidden {
				t.Errorf("POST %s with unresolvable %q = %d (%s), want 403 — a misconfigured "+
					"platform key must refuse, never file the caller's events under $public",
					door.path, key, code, got)
			}
		}
	}
}

// TestUnidentifiableBearerStillTakesTheAnonymousLaneAfterTheFix is the other half, and
// the reason the fix tests a PREFIX rather than "any bearer". An arbitrary bearer is not
// distinguishable from one minted for another audience — IdentityMiddleware already
// declines to 401 it — so treating its presence as "presented" would 403 every stale or
// foreign token that reaches an ingest door, a refusal on evidence we do not have.
func TestUnidentifiableBearerStillTakesTheAnonymousLaneAfterTheFix(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	body := `{"batch":[{"type":"pageview","distinctId":"anon-1","path":"/pricing"}]}`
	for _, tok := range []string{"an-opaque-string", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.sig"} {
		code, got := postAuth(t, app, "/v1/event", "Bearer "+tok, body)
		if code == http.StatusForbidden {
			t.Errorf("unidentifiable bearer %q = 403 (%s) — only a bearer carrying a "+
				"cloud.APIKeyPrefixes spelling is 'presented'; anything else must degrade", tok, got)
		}
	}
}
