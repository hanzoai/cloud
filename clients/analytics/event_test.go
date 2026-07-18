// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"net/http"
	"testing"
	"time"
)

// ── decodeEvents: Event | []Event (single + batch) ──────────────────────────

func TestDecodeEvents_Single(t *testing.T) {
	evs, err := decodeEvents([]byte(`{"event":"signup","distinctId":"u1","properties":{"plan":"pro"}}`))
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

func TestDecodeEvents_Batch(t *testing.T) {
	evs, err := decodeEvents([]byte(`[{"event":"a","distinctId":"d"},{"event":"b","distinctId":"d"}]`))
	if err != nil {
		t.Fatalf("decode batch: %v", err)
	}
	if len(evs) != 2 || evs[0].Event != "a" || evs[1].Event != "b" {
		t.Fatalf("batch decoded = %+v", evs)
	}
}

func TestDecodeEvents_BatchLeadingWhitespace(t *testing.T) {
	// The array is detected past leading whitespace, not only at byte 0.
	evs, err := decodeEvents([]byte("  \n\t [{\"event\":\"a\"}]"))
	if err != nil {
		t.Fatalf("decode ws-batch: %v", err)
	}
	if len(evs) != 1 || evs[0].Event != "a" {
		t.Fatalf("ws-batch decoded = %+v", evs)
	}
}

func TestDecodeEvents_EmptyIsNoEvents(t *testing.T) {
	// Empty / whitespace-only body ⇒ zero events, NOT an error (honest empty receipt).
	for _, b := range []string{"", "   ", "\n\t"} {
		evs, err := decodeEvents([]byte(b))
		if err != nil || len(evs) != 0 {
			t.Fatalf("empty %q ⇒ evs=%v err=%v", b, evs, err)
		}
	}
}

func TestDecodeEvents_Malformed(t *testing.T) {
	for _, b := range []string{`{"event":`, `[{"event":"a"},`, `not json`} {
		if _, err := decodeEvents([]byte(b)); err == nil {
			t.Fatalf("malformed %q want error, got nil", b)
		}
	}
}

// ── adapter → Event/CaptureEvent normalization ──────────────────────────────

// TestEventToCapture: the canonical Event maps onto CaptureEvent with ONLY the
// four core fields promoted; Type is left empty (⇒ "event") and everything else
// stays in Properties (nothing is lifted to a column).
func TestEventToCapture(t *testing.T) {
	e := Event{
		Event:      "purchase",
		DistinctID: "u9",
		Time:       "2026-07-18T00:00:00Z",
		Properties: map[string]any{"amount": 42, "$current_url": "https://x/y"},
	}
	ce := e.toCapture()
	if ce.Event != "purchase" || ce.DistinctID != "u9" || ce.Timestamp != "2026-07-18T00:00:00Z" {
		t.Fatalf("core fields = %+v", ce)
	}
	if ce.Type != "" {
		t.Fatalf("Type must be empty (⇒ canonicalType event), got %q", ce.Type)
	}
	if ce.URL != "" {
		t.Fatalf("no $-property is promoted to a column on the canonical wire; URL=%q", ce.URL)
	}
	// non-core stays in properties
	if ce.Properties["amount"] != 42 || ce.Properties["$current_url"] != "https://x/y" {
		t.Fatalf("properties passthrough = %v", ce.Properties)
	}
}

// TestEventNormalizeThroughCore: a canonical Event, adapted and normalized, yields
// a row stamped with the SERVER org and the resolved event name.
func TestEventNormalizeThroughCore(t *testing.T) {
	row, ok := normalizeEvent("acme", time.Now(), Event{Event: "signup", DistinctID: "u1"}.toCapture())
	if !ok {
		t.Fatal("want routable")
	}
	if row.tenant != "acme" || row.event != "signup" || row.eventType != "event" {
		t.Fatalf("row = tenant %q event %q type %q", row.tenant, row.event, row.eventType)
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

// TestSourceStampedIntoProperties: source flows through withSource → normalizeEvent
// → the stored properties JSON, so the ONE hanzo.events table carries origin
// WITHOUT a schema column.
func TestSourceStampedIntoProperties(t *testing.T) {
	e := CaptureEvent{Event: "x", Properties: withSource(nil, sourceEvent)}
	row, ok := normalizeEvent("acme", time.Now(), e)
	if !ok {
		t.Fatal("want routable")
	}
	props := decodeProps(t, row.properties)
	if props["$source"] != "event" {
		t.Fatalf("row.properties $source = %v (props=%v)", props["$source"], props)
	}
}

// ── POST /v1/event: IAM-only, fail-closed auth ──────────────────────────────
//
// Observable proxy (mirrors capture_keyorg_test): a REFUSED request is 403; an
// ADMITTED one reaches requireDatastore and returns 503 (no datastore in tests).
// So "not 403" ⇒ the tenant gate admitted the request.

func TestEvent_NoPrincipalNoKeyForbidden(t *testing.T) {
	app := mountApp(t)
	if code, _ := doBody(t, app, http.MethodPost, "/v1/event", "", "", `{"event":"e","distinctId":"d"}`); code != http.StatusForbidden {
		t.Fatalf("no-principal no-key /v1/event want 403, got %d", code)
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
	code := postKeyed(t, app, "/v1/event", "", `{"api_key":"hk-k","event":"e","distinctId":"d"}`, nil)
	if *got != "hk-k" {
		t.Fatalf("resolver handed key %q, want hk-k", *got)
	}
	if code == http.StatusForbidden {
		t.Fatalf("a resolved access key must pass the /v1/event gate, got 403")
	}
}

func TestEvent_UnresolvableKeyFailsClosedEvenOnBrandHost(t *testing.T) {
	app := mountApp(t)
	stubResolver(t, func(string) (string, bool) { return "", false })
	code := postKeyed(t, app, "/v1/event", "hanzo.ai", `{"api_key":"hk-bad","event":"e","distinctId":"d"}`, nil)
	if code != http.StatusForbidden {
		t.Fatalf("presented-but-unresolvable key on /v1/event must 403 (fail closed), got %d", code)
	}
}

// TestEvent_NoBrandHostFallback is THE distinguishing invariant: anonymous traffic
// on a recognized brand host is ADMITTED by the deprecated /v1/analytics alias
// (brand-public partition) but REFUSED by the canonical /v1/event — IAM is the
// only tenant authority on the canonical door.
func TestEvent_NoBrandHostFallback(t *testing.T) {
	app := mountApp(t)
	body := `{"event":"e","distinctId":"d"}`
	if code, _ := doHost(t, app, "/v1/event", "", "", "hanzo.ai", body); code != http.StatusForbidden {
		t.Fatalf("anonymous brand-host /v1/event must 403 (no brand fallback), got %d", code)
	}
	// Contrast: the deprecated alias still admits the same anonymous brand-host
	// traffic (503 = admitted, datastore down), proving the difference is by design.
	if code, _ := doHost(t, app, "/v1/analytics", "", "", "hanzo.ai", `{"batch":[{"type":"pageview"}]}`); code != http.StatusServiceUnavailable {
		t.Fatalf("deprecated alias still brand-admits (want 503), got %d", code)
	}
}
