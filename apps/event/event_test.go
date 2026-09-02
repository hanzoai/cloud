// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

import (
	"net/http"
	"testing"
	"time"
)

// ── decodeEvents: Event | []Event (single + batch) ──────────────────────────

func TestDecodeBare_Single(t *testing.T) {
	evs, err := decodeBare([]byte(`{"event":"signup","distinctId":"u1","properties":{"plan":"pro"}}`))
	if err != nil {
		t.Fatalf("decode single: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("single want 1 event, got %d", len(evs))
	}
	if evs[0].Event != "signup" || evs[0].DistinctID != "u1" {
		t.Fatalf("single decoded = %+v", evs[0])
	}
	if evs[0].Properties["plan"] != "pro" {
		t.Fatalf("single properties = %v", evs[0].Properties)
	}
}

func TestDecodeBare_Batch(t *testing.T) {
	evs, err := decodeBare([]byte(`[{"event":"a","distinctId":"d"},{"event":"b","distinctId":"d"}]`))
	if err != nil {
		t.Fatalf("decode batch: %v", err)
	}
	if len(evs) != 2 || evs[0].Event != "a" || evs[1].Event != "b" {
		t.Fatalf("batch decoded = %+v", evs)
	}
}

func TestDecodeBare_BatchLeadingWhitespace(t *testing.T) {
	// The array is detected past leading whitespace, not only at byte 0.
	evs, err := decodeBare([]byte("  \n\t [{\"event\":\"a\"}]"))
	if err != nil {
		t.Fatalf("decode ws-batch: %v", err)
	}
	if len(evs) != 1 || evs[0].Event != "a" {
		t.Fatalf("ws-batch decoded = %+v", evs)
	}
}

func TestDecodeBare_EmptyIsNoEvents(t *testing.T) {
	// Empty / whitespace-only body ⇒ zero events, NOT an error (honest empty receipt).
	for _, b := range []string{"", "   ", "\n\t"} {
		evs, err := decodeBare([]byte(b))
		if err != nil || len(evs) != 0 {
			t.Fatalf("empty %q ⇒ evs=%v err=%v", b, evs, err)
		}
	}
}

func TestDecodeBare_Malformed(t *testing.T) {
	for _, b := range []string{`{"event":`, `[{"event":"a"},`, `not json`} {
		if _, err := decodeBare([]byte(b)); err == nil {
			t.Fatalf("malformed %q want error, got nil", b)
		}
	}
}

// TestEventNormalizeThroughCore: a canonical event, normalized, yields
// a fact stamped with the SERVER org and the resolved event name.
func TestEventNormalizeThroughCore(t *testing.T) {
	f, ok := normalize("acme", time.Now(), CaptureEvent{Event: "signup", DistinctID: "u1"})
	if !ok {
		t.Fatal("want routable")
	}
	if f.org != "acme" || f.name != "signup" || f.signal != signalAct || f.kind != kindTrack {
		t.Fatalf("fact = org %q name %q signal %q kind %q", f.org, f.name, f.signal, f.kind)
	}
}

// TestInsightsAdapterNormalization: the PostHog wire adapter lifts well-known
// $-properties to columns (that is its job) — the OTHER adapter feeding the ONE
// core, distinct from the canonical Event wire.
func TestInsightsAdapterNormalization(t *testing.T) {
	ce := insightsEvent{
		Event:      "$pageview",
		DistinctID: "d",
		Properties: map[string]any{"$current_url": "https://x/y", "$session_id": "s1"},
	}.toCapture()
	if ce.Type != "pageview" {
		t.Fatalf("posthog $pageview ⇒ type pageview, got %q", ce.Type)
	}
	if ce.URL != "https://x/y" || ce.SessionID != "s1" {
		t.Fatalf("posthog $-props ⇒ columns: url=%q session=%q", ce.URL, ce.SessionID)
	}
}

// TestCaptureBatchAdapter: the Segment/beacon adapter prefers `batch`, falling
// back to `events`.
func TestCaptureBatchAdapter(t *testing.T) {
	if got := (CaptureBatch{Batch: []CaptureEvent{{Event: "a"}}, Events: []CaptureEvent{{Event: "b"}}}).events(); len(got) != 1 || got[0].Event != "a" {
		t.Fatalf("batch preferred over events, got %+v", got)
	}
	if got := (CaptureBatch{Events: []CaptureEvent{{Event: "b"}}}).events(); len(got) != 1 || got[0].Event != "b" {
		t.Fatalf("events fallback, got %+v", got)
	}
}

// ── source tagging (the $source property, one-table origin discriminator) ────

func TestWithSource(t *testing.T) {
	// stamps $source
	got := withSource(nil, sourceEvent)
	if got["$source"] != "event" {
		t.Fatalf("withSource(nil,event) = %v", got)
	}
	// does not mutate the caller's map, and preserves existing keys
	orig := map[string]any{"a": 1}
	out := withSource(orig, sourcePostHog)
	if out["a"] != 1 || out["$source"] != "posthog" {
		t.Fatalf("withSource copy = %v", out)
	}
	if _, leaked := orig["$source"]; leaked {
		t.Fatalf("withSource mutated the caller's map: %v", orig)
	}
	// empty source is a no-op passthrough (same map)
	if got := withSource(orig, ""); got["$source"] != nil {
		t.Fatalf("empty source must not stamp, got %v", got)
	}
}

// TestSourceStampedIntoAttributes: source flows through withSource → normalize
// → the fact's attributes map, so the ONE event.event table carries origin
// WITHOUT a schema column.
func TestSourceStampedIntoAttributes(t *testing.T) {
	e := CaptureEvent{Event: "x", Properties: withSource(nil, sourceEvent)}
	f, ok := normalize("acme", time.Now(), e)
	if !ok {
		t.Fatal("want routable")
	}
	if f.attributes["$source"] != "event" {
		t.Fatalf("attributes $source = %v (attrs=%v)", f.attributes["$source"], f.attributes)
	}
}

// ── POST /v1/event: IAM-only, fail-closed auth ──────────────────────────────
//
// Observable proxy (mirrors capture_keyorg_test): a REFUSED request is 403; an
// ADMITTED one reaches requireDatastore and returns 503 (no datastore in tests).
// So "not 403" ⇒ the tenant gate admitted the request.

// TestEvent_NoPrincipalNoKeyIsRefused: a caller with NO principal and NO key is
// refused AT THE GATE — 401 ingest_key_required, whatever it sent. A pageview is not
// a special case any more: there is no lane that stores an unattributable event.
// What a PRESENTED but unresolvable credential gets is 403
// (TestEvent_UnresolvableKeyFailsClosedEvenOnBrandHost).
func TestEvent_NoPrincipalNoKeyIsRefused(t *testing.T) {
	app := mountApp(t)
	for _, body := range []string{
		`{"event":"e","distinctId":"d"}`,
		`{"batch":[{"type":"pageview"}]}`,
	} {
		code, got := doBody(t, app, http.MethodPost, "/v1/event", "", "", body)
		refusedAnon(t, "no-principal no-key "+body, code, got)
	}
}

func TestEvent_BearerPrincipalAdmitted(t *testing.T) {
	app := mountApp(t)
	// A validated principal (X-User/X-Org) is admitted → 503 (datastore down), not 403.
	if code, _ := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", `{"event":"signup","distinctId":"u1"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("bearer /v1/event want 503 (admitted, datastore down), got %d", code)
	}
	// A batch body is admitted the same way.
	if code, _ := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", `[{"event":"a","distinctId":"d"},{"event":"b","distinctId":"d"}]`); code != http.StatusServiceUnavailable {
		t.Fatalf("bearer /v1/event batch want 503, got %d", code)
	}
}

func TestEvent_ResolvedKeyAdmitted(t *testing.T) {
	app := mountApp(t)
	got := stubResolver(t, func(string) (string, bool) { return "acme", true })
	code := postKeyed(t, app, "/v1/event", "", `{"api_key":"sk-k","event":"e","distinctId":"d"}`, nil)
	if *got != "sk-k" {
		t.Fatalf("resolver handed key %q, want sk-k", *got)
	}
	if code == http.StatusForbidden {
		t.Fatalf("a resolved access key must pass the /v1/event gate, got 403")
	}
}

func TestEvent_UnresolvableKeyFailsClosedEvenOnBrandHost(t *testing.T) {
	app := mountApp(t)
	stubResolver(t, func(string) (string, bool) { return "", false })
	code := postKeyed(t, app, "/v1/event", "hanzo.ai", `{"api_key":"sk-bad","event":"e","distinctId":"d"}`, nil)
	if code != http.StatusForbidden {
		t.Fatalf("presented-but-unresolvable key on /v1/event must 403 (fail closed), got %d", code)
	}
}

// TestEvent_NoBrandHostFallback is THE invariant: the request Host NEVER selects a
// tenant. It used to — the deprecated aliases resolved anonymous traffic on a
// recognized brand host to that BRAND's REAL org, a real tenant picked by a
// caller-settable header.
//
// Now a Host buys nothing anywhere because there is nothing to buy: without a
// credential every endpoint refuses, brand host or not.
func TestEvent_NoBrandHostFallback(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := mountApp(t)
	pageview := `{"batch":[{"type":"pageview"}]}`
	commerce := `{"batch":[{"type":"event","event":"order_completed","revenue":999}]}`
	path := canonDoor
	for _, host := range []string{"hanzo.ai", "zoo.ngo", "evil.example.com"} {
		code, body := doHost(t, app, path, "", "", host, pageview)
		refusedAnon(t, "anonymous pageview "+path+" on host "+host, code, body)
		code, body = doHost(t, app, path, "", "", host, commerce)
		refusedAnon(t, "anonymous commerce "+path+" on host "+host, code, body)
	}
}
