// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/team/token"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// door_honesty_test.go — the endpoint told every client it was fine while storing nothing.
//
// EVERY wire shape /v1/event publishes, posted without a resolvable tenant, answered
// 200 {"accepted":0,"dropped":1}. The projection refuses a kind it cannot name
// (publicKinds admits pageviews and errors; a log, a span and an exception envelope are
// none of those) and said so in a receipt field no client parses and no probe reads. A
// client whose key was absent, revoked or mistyped therefore lost 100% of what it sent
// with a green check beside it — the mechanism that hid an 88% log loss, a span outage
// that ran four and a half months, and a day of missing Sentry traffic.
//
// The rule now is exactly "did anything land" (answer, event.go). These tests pin both
// halves: the loss is loud, and the accepted path — including the PARTIAL batch — is
// exactly as it was.

// refused asserts the observable of a request that stored NOTHING: a 4xx naming a
// machine-readable reason, never a 200 whose receipt nobody reads. It Errorf's and
// returns false rather than failing the run, so a table reports every row.
func refused(t *testing.T, what string, status int, body []byte, wantStatus int, wantCode string) bool {
	t.Helper()
	if status != wantStatus {
		t.Errorf("%s = %d (%s), want %d — a request that stored NOTHING must say so in the "+
			"status, which is the only field a client and a probe both read", what, status, body, wantStatus)
		return false
	}
	var e struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &e); err != nil || e.Code != wantCode {
		t.Errorf("%s code = %q (%s), want %q — the reason has to be machine-readable or an SDK "+
			"is back to guessing", what, e.Code, body, wantCode)
		return false
	}
	return true
}

// refusedAnon is the common case: nothing stored, and nobody vouched for the caller.
func refusedAnon(t *testing.T, what string, status int, body []byte) bool {
	t.Helper()
	return refused(t, what, status, body, http.StatusUnauthorized, "ingest_key_required")
}

// ── 1. the defect, on every shape the endpoint publishes ─────────────────────

// TestEveryWireShapeRefusesWhenNothingLands is the regression, stated once per SHAPE
// because the endpoint dispatches on shape: a fix that only reached the canonical decoder
// would leave the PostHog and team wires lying exactly as before, and those carry the
// SDK traffic that went missing.
//
// Every row here answered 200 {"accepted":0,"dropped":1} before this file existed.
func TestEveryWireShapeRefusesWhenNothingLands(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, tc := range []struct{ name, body string }{
		{"canonical object", `{"event":"app.log","distinctId":"d1","properties":{"msg":"hi"}}`},
		{"canonical object with time", `{"event":"app.log","distinctId":"d1","time":"2026-07-31T00:00:00Z"}`},
		{"canonical array", `[{"event":"app.log","distinctId":"d1"}]`},
		{"batch envelope", `{"batch":[{"type":"event","event":"app.log","distinctId":"d1"}]}`},
		{"posthog single", `{"event":"app.log","distinct_id":"d1","properties":{"msg":"hi"}}`},
		{"posthog batch", `{"batch":[{"event":"app.log","distinct_id":"d1","properties":{"msg":"hi"}}]}`},
		{"team bare array", `[{"event":"app.log","distinct_id":"d1","timestamp":1750000000000}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := doHost(t, app, "/v1/event", "", "", "api.hanzo.ai", tc.body)
			refusedAnon(t, tc.name, code, body)
		})
	}
}

// ── 2. what must NOT change ──────────────────────────────────────────────────

// TestAcceptedPathIsUnchanged: an anonymous pageview is still ADMITTED and still
// reaches the write core (503 in this warehouse-less harness). The fix is about the
// request that stored NOTHING; a request that stores something was never the problem.
func TestAcceptedPathIsUnchanged(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, tc := range []struct{ name, body string }{
		{"bare object", `{"type":"pageview","event":"$pageview","distinctId":"anon-1","path":"/pricing"}`},
		{"bare array", `[{"type":"pageview","event":"$pageview","distinctId":"anon-1","path":"/pricing"}]`},
		{"batch envelope", `{"batch":[{"type":"pageview","event":"$pageview","distinctId":"anon-1"}]}`},
	} {
		code, body := postAnon(t, app, "/v1/event", tc.body, nil)
		if code != http.StatusServiceUnavailable {
			t.Errorf("anonymous pageview (%s) = %d (%s), want 503 ADMITTED — the fix must not "+
				"narrow what the endpoint accepts", tc.name, code, body)
		}
	}
}

// TestPartialBatchStillSucceeds is the OTHER half of "only a wholly-unattributable
// request is an error", and the reason the rule is `accepted == 0` and not `dropped > 0`.
// A batch mixing a storable pageview with a kind the projection refuses must still 200,
// with counts that add up — failing it whole would take a client's good events down with
// its bad one, which is a worse outage than the one being fixed.
func TestPartialBatchStillSucceeds(t *testing.T) {
	roomyRate(t)
	fakeWarehouse(t)
	app := mountApp(t)
	const mixed = `{"batch":[` +
		`{"type":"pageview","event":"$pageview","distinctId":"anon-1","path":"/pricing"},` +
		`{"type":"event","event":"order_completed","revenue":99}]}`
	code, body := postAnon(t, app, "/v1/event", mixed, nil)
	if code != http.StatusOK {
		t.Fatalf("partial batch = %d (%s), want 200 — some events landing is a success", code, body)
	}
	if r := receipt(t, body); r.Accepted != 1 || r.Dropped != 1 {
		t.Fatalf("partial batch receipt = %+v, want accepted:1 dropped:1 — the counts still have "+
			"to total what was sent", r)
	}
}

// TestEmptyBodyStillSucceeds: nothing sent is not something lost. An empty body drops
// zero events, so there is no failure to report and the honest empty receipt stands.
func TestEmptyBodyStillSucceeds(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, body := range []string{``, `   `, `{"batch":[]}`, `[]`} {
		code, got := postAnon(t, app, "/v1/event", body, nil)
		if code != http.StatusOK {
			t.Errorf("empty body %q = %d (%s), want 200 — dropping nothing is not losing anything",
				body, code, got)
		}
	}
}

// TestOptedOutStillSucceeds: DNT is the ONE total drop that is not a failure. The client
// asked not to be tracked and the server obeyed, so there is nothing to fix and nothing
// to page on — turning it into a 4xx would make every privacy-respecting browser look
// like an outage.
func TestOptedOutStillSucceeds(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, h := range []map[string]string{{"DNT": "1"}, {"Sec-GPC": "1"}} {
		code, body := postAnon(t, app, "/v1/event",
			`{"batch":[{"type":"pageview","event":"$pageview","distinctId":"anon-1"}]}`, h)
		if code != http.StatusOK {
			t.Errorf("opt-out %v = %d (%s), want 200 — an honored opt-out is not a failed ingest",
				h, code, body)
		}
		if r := receipt(t, body); r.Accepted != 0 || r.Dropped != 1 {
			t.Errorf("opt-out %v receipt = %+v, want accepted:0 dropped:1", h, r)
		}
	}
}

// ── 3. the reason names what the CALLER can do about it ──────────────────────

// TestFullCapabilityUnroutableBodyIs400: a caller holding a real credential is not
// missing a key, so 401 would send it after a second one to hit the identical wall. Its
// batch landed nothing because nothing in it was routable — a metric has no writer to
// drain it (plane_test.go) — so the BODY is the thing to change, and that is 400.
func TestFullCapabilityUnroutableBodyIs400(t *testing.T) {
	fakeWarehouse(t)
	app := mountApp(t)
	code, body := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme",
		`{"batch":[{"type":"metric","metric":{"name":"page_load_ms","value":812}}]}`)
	refused(t, "authenticated unroutable batch", code, body, http.StatusBadRequest, "unroutable_events")
}

// TestGuestRefusalIsCapabilityNotCredential: a guest's space token RESOLVED. It has
// a credential and the credential is not the problem — it lacks capability into an org
// it was invited into for one channel. 401 "key required" would be a false instruction;
// 403 is the true one, and the code says which.
func TestGuestRefusalIsCapabilityNotCredential(t *testing.T) {
	t.Setenv("SERVER_SECRET", "a-real-team-secret")
	app := mountApp(t)
	guest := teamToken(t, "acme", "a-real-team-secret",
		map[string]any{"role": token.RoleGuest}, time.Now().Add(time.Hour).Unix())
	code, body := postAnon(t, app, "/v1/event",
		`[{"event":"customEvent","properties":{"revenue":99999},"timestamp":1750000000000,"distinct_id":"u"}]`,
		map[string]string{"Authorization": "Bearer " + guest})
	refused(t, "guest whose every event was projected away", code, body,
		http.StatusForbidden, "insufficient_capability")
}

// ── 4. the drop is READABLE, not merely true ─────────────────────────────────

// TestDropIsVisibleToAnAlert: a drop nobody can see is the defect in another costume.
// The receipt already carried an accurate count and three outages still ran unnoticed,
// so "the number is correct" is not the property that matters — "something watching can
// read it" is. hanzo_ingest_dropped_total is what an ingest-drop alert rule reads, and
// this collects it rather than trusting that an instrument nobody exercises works.
//
// The instrument binds through a sync.Once (production has one composition root that
// installs the provider before serving), so the test installs its own provider and
// resets the Once — the same substitution this package already makes for publicRate and
// the warehouse client.
func TestDropIsVisibleToAnAlert(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	dropOnce, dropCounter = sync.Once{}, nil
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		dropOnce, dropCounter = sync.Once{}, nil
	})

	roomyRate(t)
	app := mountApp(t)
	postAnon(t, app, "/v1/event", `{"event":"app.log","distinctId":"d1"}`, nil)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, s := range rm.ScopeMetrics {
		for _, m := range s.Metrics {
			if m.Name != "hanzo_ingest_dropped_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok || len(sum.DataPoints) != 1 {
				t.Fatalf("hanzo_ingest_dropped_total = %#v, want one int64 data point", m.Data)
			}
			dp := sum.DataPoints[0]
			reason, _ := dp.Attributes.Value("reason")
			if dp.Value != 1 || reason.AsString() != "unattributable" {
				t.Fatalf("drop point = %d reason=%q, want 1 unattributable", dp.Value, reason.AsString())
			}
			return
		}
	}
	t.Fatal("hanzo_ingest_dropped_total was never recorded — an ingest-drop alert would have " +
		"nothing to read, which is how a silent drop stays silent")
}

// ── 5. shape still wins over key ─────────────────────────────────────────────

// TestShapeDispatchSurvivesTheFix: isInsightsWire MUST keep running BEFORE the canonical
// object branch. Both wires spell a batch envelope `batch`, so routing on the key alone
// hands a PostHog batch to the CaptureBatch decoder — which cannot see `distinct_id` and
// yields events with no person and no kind, i.e. the very silent drop this file exists to
// end, reintroduced by the fix for it. The proof is at the decoder, where the dispatch
// lives: a PostHog body must come back carrying its person.
func TestShapeDispatchSurvivesTheFix(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"posthog single", `{"event":"$pageview","distinct_id":"ph-1"}`},
		{"posthog batch", `{"batch":[{"event":"$pageview","distinct_id":"ph-1"}]}`},
	} {
		evs, err := decodeIngest([]byte(tc.body))
		if err != nil || len(evs) != 1 {
			t.Fatalf("decodeIngest(%s) = %d events, %v; want 1, nil", tc.name, len(evs), err)
		}
		if evs[0].DistinctID != "ph-1" {
			t.Errorf("decodeIngest(%s) person = %q, want %q — the canonical decoder ate a PostHog "+
				"body, which is exactly how a batch becomes personless and gets dropped whole",
				tc.name, evs[0].DistinctID, "ph-1")
		}
	}
}
