package o11y

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// availability_test.go — the contract of the read that REPLACED the SuperAdmin
// VictoriaMetrics proxy. Two of these tests are the whole reason the replacement
// exists rather than being a deletion: the platform-sudo boundary the proxy owned
// has to survive the store it guarded, and a store that cannot be read has to say
// so out loud instead of answering an empty board.

// adminReq carries the validated-SuperAdmin gateway claim (X-User-IsAdmin: true —
// what SanitizeIdentity sets ONLY for the reserved admin org). X-User-Id makes the
// caller validated, as scopeReq does.
func adminReq(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-User-Id", "u_admin")
	req.Header.Set("X-User-IsAdmin", "true")
	return req
}

// ── the gate: fleet availability is platform sudo, exactly as the proxy was ────

// The retired proxy refused every customer because `up{}` was the whole fleet's
// inventory rather than tenant data. The measurement moved stores; the reason did
// not, so neither did the gate. A validated tenant is still not a platform admin.
func TestAvailabilityRequiresSuperAdmin(t *testing.T) {
	app := scopeApp(t)
	if code, body := do(t, app, scopeReq("GET", "/v1/o11y/availability", "acme")); code != http.StatusForbidden {
		t.Fatalf("validated tenant: want 403, got %d %s", code, body)
	}
	// And an unvalidated caller, who has no principal at all.
	if code, body := do(t, app, httptest.NewRequest("GET", "/v1/o11y/availability", nil)); code != http.StatusForbidden {
		t.Fatalf("unvalidated caller: want 403, got %d %s", code, body)
	}
}

// ── an unreadable store is 503, NEVER an empty 200 ────────────────────────────

// This is the failure mode the whole endpoint is shaped around. A 200 carrying an
// empty inventory and an empty trend renders as a board of zeroes, which is
// pixel-identical to a fleet that is entirely down — the most expensive lie a
// status surface can tell. With no datastore wired (the unit case, and the real
// case when the store is unreachable) the answer must be an explicit 503.
func TestAvailabilityRefusesRatherThanAnsweringEmpty(t *testing.T) {
	code, body := do(t, scopeApp(t), adminReq("GET", "/v1/o11y/availability"))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no datastore: want 503, got %d %s", code, body)
	}
	// Specifically NOT the shape of a successful-but-empty read.
	var resp availabilityResponse
	if json.Unmarshal(body, &resp) == nil && resp.Total == 0 && len(resp.Series) == 0 && code == http.StatusOK {
		t.Fatal("an unreadable store answered an empty board instead of refusing")
	}
}

// ── the vendor's routes are gone and must not come back ───────────────────────

// /v1/o11y/vm/{query,query_range} named VictoriaMetrics in the route table and
// spoke its Prometheus envelope. Both are retired with the store. A 404 here is
// the assertion that no future change quietly reintroduces a reader for a store
// this estate does not run.
func TestRetiredVMRoutesAreGone(t *testing.T) {
	app := scopeApp(t)
	for _, path := range []string{
		"/v1/o11y/vm/query?query=up",
		"/v1/o11y/vm/query_range?query=sum(up)&start=1&end=2&step=15",
	} {
		if code, _ := do(t, app, adminReq("GET", path)); code != http.StatusNotFound {
			t.Fatalf("%s: want 404 (route retired with VictoriaMetrics), got %d", path, code)
		}
	}
}

// ── the window is clamped server-side, and the caller never names the metric ──

// The proxy's second gate validated start/end/step before forwarding, because an
// unbounded step over an unbounded window is a DoS whether or not the query is
// allowlisted. The typed op keeps that boundary in the one place the scoped reads
// already keep it — boundRangeSec/stepFor — so an absurd window is clamped rather
// than refused, and there is no query parameter to allowlist at all.
func TestAvailabilityClampsItsWindow(t *testing.T) {
	for _, tc := range []struct {
		name                string
		rangeSec, stepSec   int
		wantRange, wantStep int
	}{
		{"absent takes the default", 0, 0, defaultRangeSec, defaultRangeSec / 60},
		{"a week is the ceiling", 99999999, 0, maxRangeSec, maxStepSec},
		{"a one-second step floors", 3600, 1, 3600, minStepSec},
		{"a one-day step ceilings", 3600, 86400, 3600, maxStepSec},
		{"negatives take the default", -5, -5, defaultRangeSec, defaultRangeSec / 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotRange := boundRangeSec(tc.rangeSec)
			gotStep := stepFor(gotRange, tc.stepSec)
			if gotRange != tc.wantRange || gotStep != tc.wantStep {
				t.Fatalf("range=%d step=%d → (%d,%d), want (%d,%d)",
					tc.rangeSec, tc.stepSec, gotRange, gotStep, tc.wantRange, tc.wantStep)
			}
		})
	}
}

// ── the trend read refuses a nonsense window before it reaches the store ──────

func TestGaugeSeriesRejectsNonPositiveWindow(t *testing.T) {
	for _, tc := range []struct {
		name              string
		rangeSec, stepSec int
	}{
		{"zero range", 0, 30},
		{"zero step", 3600, 0},
		{"negative step", 3600, -1},
	} {
		if _, err := gaugeSeries(t.Context(), upMetric, tc.rangeSec, tc.stepSec); err == nil {
			t.Fatalf("%s: want an error, got nil", tc.name)
		}
	}
	if _, err := gaugeSeries(t.Context(), "", 3600, 30); err == nil {
		t.Fatal("empty metric name: want an error, got nil")
	}
}
