// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// This file proves the unification net-invariant: there is ONE ingest handler
// implementation and ONE write core; /v1/event is the single canonical door serving
// EVERY wire shape (Event | [Event] | {batch}) and EVERY auth context (IAM bearer |
// pk_ key | site-host-forced); the other routes are thin aliases/shims delegating to
// it. The proof is decomposed to fit the harness (datastore is DOWN, so the HTTP
// path stops at requireDatastore's 503 before a row is written):
//
//   - WIRE dimension (deterministic, row layer): the SAME logical event in every
//     wire shape decodes through the ONE tolerant decoder (decodeIngest) and the ONE
//     normalizer (normalizeEvent) into a byte-identical warehouse row, tenant = the
//     server-resolved org. This is exactly the row buildEventsInsert binds.
//   - AUTH dimension (HTTP layer): each auth context is ADMITTED (503, never 403),
//     proving the canonical door resolved a tenant for it. Each pure resolver
//     (resolveKeyOrg→org, the host-forced Site.Org) is
//     unit-proven elsewhere (publishable_test, capture_keyorg_test, hostcarve_test),
//     so admission + those proofs compose into "lands in the SAME tenant".

// ── tolerant decoder: THREE shapes → the SAME []CaptureEvent ──────────────────

// TestDecodeIngest_ThreeShapes proves the ONE canonical decoder accepts a bare Event
// object, a bare [Event] array, AND the {batch:[…]} envelope, yielding equivalent
// CaptureEvents from each — no separate door is needed for any wire.
func TestDecodeIngest_ThreeShapes(t *testing.T) {
	shapes := map[string]string{
		"bareObject": `{"event":"signup_completed","distinctId":"u1","properties":{"plan":"pro"}}`,
		"bareArray":  `[{"event":"signup_completed","distinctId":"u1","properties":{"plan":"pro"}}]`,
		"batchEnv":   `{"batch":[{"event":"signup_completed","distinctId":"u1","properties":{"plan":"pro"}}]}`,
		"eventsEnv":  `{"events":[{"event":"signup_completed","distinctId":"u1","properties":{"plan":"pro"}}]}`,
	}
	for name, body := range shapes {
		evs, err := decodeIngest([]byte(body))
		if err != nil {
			t.Fatalf("%s: decodeIngest err: %v", name, err)
		}
		if len(evs) != 1 {
			t.Fatalf("%s: got %d events, want 1", name, len(evs))
		}
		e := evs[0]
		if e.Event != "signup_completed" || e.DistinctID != "u1" || e.Properties["plan"] != "pro" {
			t.Fatalf("%s: decoded = %+v", name, e)
		}
	}
}

// TestDecodeIngest_BatchEnvelopeDetectedNotEventNamedBatch guards the envelope
// discriminator: a bare Event whose NAME is "batch" is NOT mistaken for the batch
// envelope (the probe checks a top-level batch/events KEY, not the event field).
func TestDecodeIngest_BatchEnvelopeDetectedNotEventNamedBatch(t *testing.T) {
	evs, err := decodeIngest([]byte(`{"event":"batch","distinctId":"d"}`))
	if err != nil {
		t.Fatalf("decodeIngest err: %v", err)
	}
	if len(evs) != 1 || evs[0].Event != "batch" {
		t.Fatalf("event named 'batch' must stay a single bare event, got %+v", evs)
	}
}

// TestDecodeIngest_EmptyBatchIsEnvelope: {"batch":[]} is an empty ENVELOPE (zero
// events, honest empty receipt) — the key is present even though the array is empty.
func TestDecodeIngest_EmptyBatchIsEnvelope(t *testing.T) {
	evs, err := decodeIngest([]byte(`{"batch":[]}`))
	if err != nil || len(evs) != 0 {
		t.Fatalf("empty batch envelope ⇒ 0 events no error, got evs=%v err=%v", evs, err)
	}
}

// TestDecodeIngest_EmptyAndMalformed: an empty/whitespace body is a no-op receipt;
// malformed JSON is an error the handler turns into 400.
func TestDecodeIngest_EmptyAndMalformed(t *testing.T) {
	for _, b := range []string{"", "   ", "\n\t"} {
		if evs, err := decodeIngest([]byte(b)); err != nil || len(evs) != 0 {
			t.Fatalf("empty %q ⇒ evs=%v err=%v", b, evs, err)
		}
	}
	for _, b := range []string{`{"event":`, `{"batch":[`, `not json`, `[{"event":"a"},`} {
		if _, err := decodeIngest([]byte(b)); err == nil {
			t.Fatalf("malformed %q want error, got nil", b)
		}
	}
}

// ── net invariant: SAME warehouse row + SAME tenant across every wire ──────────

// TestUnifiedIngest_SameRowSameTenant is THE unification proof at the row layer: one
// logical event, expressed as every wire shape the canonical door and its aliases
// accept, decoded through the ONE decoder and normalized with the SAME server org,
// yields a byte-identical warehouse row (modulo the randomly-minted id) whose tenant
// is that org. Since buildEventsInsert binds eventRow.args() positionally, identical
// args() == identical warehouse row.
func TestUnifiedIngest_SameRowSameTenant(t *testing.T) {
	const org = "acme"
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	// Every wire shape carrying the SAME logical event. bareObject/bareArray are the
	// canonical Event wire; batchEnv/eventsEnv are the CaptureBatch envelope the
	// Segment/beacon/publishable paths speak.
	bodies := map[string]string{
		"bareObject": `{"event":"signup_completed","distinctId":"u1","properties":{"plan":"pro"}}`,
		"bareArray":  `[{"event":"signup_completed","distinctId":"u1","properties":{"plan":"pro"}}]`,
		"batchEnv":   `{"batch":[{"event":"signup_completed","distinctId":"u1","properties":{"plan":"pro"}}]}`,
		"eventsEnv":  `{"events":[{"event":"signup_completed","distinctId":"u1","properties":{"plan":"pro"}}]}`,
	}

	rows := map[string][]any{}
	for name, body := range bodies {
		evs, err := decodeIngest([]byte(body))
		if err != nil || len(evs) != 1 {
			t.Fatalf("%s: decodeIngest evs=%v err=%v", name, evs, err)
		}
		// foldException is a no-op for non-error events — part of the ONE core path.
		row, ok := normalizeEvent(org, now, foldException(evs[0]))
		if !ok {
			t.Fatalf("%s: normalize dropped a routable event", name)
		}
		rows[name] = row.args()
	}

	// The tenant column (index 2) is the server org for EVERY wire — never the body.
	for name, a := range rows {
		if a[2] != org {
			t.Fatalf("%s: tenant arg = %v, want %q (server-stamped)", name, a[2], org)
		}
	}
	// Every wire yields the identical warehouse row, comparing all columns except the
	// minted id (index 0) — the only non-deterministic column when messageId is absent.
	ref := rows["bareObject"]
	for name, a := range rows {
		assertRowArgsEqualExceptID(t, name, ref, a)
	}
}

// assertRowArgsEqualExceptID compares two positional row-arg slices column-by-column,
// skipping index 0 (the randomly-minted id). A mismatch names the differing column.
func assertRowArgsEqualExceptID(t *testing.T, name string, want, got []any) {
	t.Helper()
	if len(want) != len(got) || len(got) != len(eventColumns) {
		t.Fatalf("%s: arg width = %d, want %d", name, len(got), len(eventColumns))
	}
	for i := 1; i < len(got); i++ {
		if !reflect.DeepEqual(want[i], got[i]) {
			t.Fatalf("%s: column %q differs: %v (%T) != %v (%T)",
				name, eventColumns[i], got[i], got[i], want[i], want[i])
		}
	}
}

// ── pluggable auth on the ONE door: IAM's pk- folded into /v1/event ──────────

// stubKeyOrg points the ONE IAM key seam at a table for the test's duration.
// resolveKeyOrg is a var precisely so the seam can be swapped without a network;
// these exercise the DOOR, not IAM's resolution.
func stubKeyOrg(t *testing.T, table map[string]string) {
	t.Helper()
	prev := resolveKeyOrg
	resolveKeyOrg = func(_ context.Context, key string) (string, bool) {
		org, ok := table[key]
		return org, ok
	}
	t.Cleanup(func() { resolveKeyOrg = prev })
}

// A pk- is a first-class auth mode ON the canonical door: admitted (503,
// datastore down), so a pk- caller uses /v1/event directly.
func TestEvent_PkKeyAdmitted(t *testing.T) {
	stubKeyOrg(t, map[string]string{"pk-acme": "acme"})
	app := mountApp(t)
	code := postKeyed(t, app, "/v1/event", "", `{"batch":[{"type":"event","event":"signup_completed"}]}`,
		map[string]string{"Authorization": "Bearer pk-acme"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("pk- on /v1/event want 503 (admitted, datastore down), got %d", code)
	}
}

// The tenant a pk- writes into is the org IAM resolves it to, never a body or
// header claim — the key's org wins over a forged one.
func TestEvent_PkKeyForgedOrgIgnored(t *testing.T) {
	stubKeyOrg(t, map[string]string{"pk-acme": "acme"})
	app := mountApp(t)
	code := postKeyed(t, app, "/v1/event", "hanzo.ai",
		`{"batch":[{"type":"pageview"}],"org":"attacker"}`,
		map[string]string{"Authorization": "Bearer pk-acme", "X-Org-Id": "attacker"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("pk- door with forged org want 503 (ingested as key org), got %d", code)
	}
}

// A pk- IAM does not resolve, with no other auth, is refused 403 — fail closed.
func TestEvent_BadPkKeyFailsClosed(t *testing.T) {
	stubKeyOrg(t, map[string]string{})
	app := mountApp(t)
	code := postKeyed(t, app, "/v1/event", "hanzo.ai", `{"batch":[{"type":"pageview"}]}`,
		map[string]string{"Authorization": "Bearer pk-deadbeef"})
	if code != http.StatusForbidden {
		t.Fatalf("unresolvable pk- on /v1/event want 403 (fail closed), got %d", code)
	}
}

// ── /v1/ingest is now a THIN ALIAS of the ONE handler ─────────────────────────

// TestIngestAlias_DelegatesToEventHandler proves /v1/ingest is the SAME
// implementation as /v1/event: a pk- caller is admitted (unchanged), AND — because it
// delegates to eventHandle — it ALSO admits an IAM bearer and takes the SAME anonymous
// lane, under the same public tenant and the same allowlist. One implementation reached
// through two routes: the alias cannot drift from the canonical door, which is the whole
// point of it being an alias.
func TestIngestAlias_DelegatesToEventHandler(t *testing.T) {
	stubKeyOrg(t, map[string]string{"pk-acme": "acme"})
	app := mountApp(t)

	// pk- — the historical /v1/ingest auth — still works.
	if code := postKeyed(t, app, "/v1/ingest", "", `{"batch":[{"type":"pageview"}]}`,
		map[string]string{"Authorization": "Bearer pk-acme"}); code != http.StatusServiceUnavailable {
		t.Fatalf("pk- on /v1/ingest want 503 (admitted), got %d", code)
	}
	// IAM bearer — admitted too, because the alias IS eventHandle now.
	if code, _ := doBody(t, app, http.MethodPost, "/v1/ingest", "user-dave", "acme",
		`[{"event":"signup_completed","distinctId":"u1"}]`); code != http.StatusServiceUnavailable {
		t.Fatalf("bearer on /v1/ingest want 503 (admitted via eventHandle), got %d", code)
	}
	// No auth at all — the anonymous lane, exactly like the canonical door: a pageview
	// is admitted (503, datastore down) under the public tenant.
	if code, body := doBody(t, app, http.MethodPost, "/v1/ingest", "", "",
		`{"batch":[{"type":"pageview"}]}`); code != http.StatusServiceUnavailable {
		t.Fatalf("no-auth /v1/ingest want 503 (anonymous lane, admitted), got %d (%s)", code, body)
	}
	// A non-allowlisted kind is dropped anonymously here too, not stored.
	if code, body := doBody(t, app, http.MethodPost, "/v1/ingest", "", "",
		`{"batch":[{"type":"event","event":"order_completed"}]}`); code != http.StatusOK {
		t.Fatalf("no-auth /v1/ingest custom kind want 200 all-dropped, got %d (%s)", code, body)
	}
	// A presented pk- that does NOT resolve still fails closed — never downgraded into
	// the anonymous lane, so a misconfigured key surfaces instead of silently filing
	// its events where its owner cannot read them.
	if code := postKeyed(t, app, "/v1/ingest", "", `{"batch":[{"type":"pageview"}]}`,
		map[string]string{"Authorization": "Bearer pk-nosuch"}); code != http.StatusForbidden {
		t.Fatalf("unresolvable pk- on /v1/ingest want 403 (fail closed), got %d", code)
	}
}
