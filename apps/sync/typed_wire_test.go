package sync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// The property going typed could have broken silently on this surface, pinned so
// breaking it goes RED rather than quiet.
//
// The OTHER one — that cloud.Terminal still wraps these ops, so a reject keeps its
// real 4xx under the commerce /v1 flatten filter instead of becoming a 500 — is
// already pinned by TestSyncRejectsSurviveCommerceFlatten (flatten_test.go), which
// exercises all six routes behind that filter. It is not restated here.

// TestPatchSync_NullIsAbsent pins the pointer-carrier decision in patchSyncIn.
// encoding/json sets a POINTER field to nil for an explicit JSON null WITHOUT
// calling its UnmarshalJSON, so `{"direction":null}` and `{}` arrive IDENTICALLY —
// which is safe HERE only because neither means "clear the column": every policy
// field either takes a new legal value or keeps the stored one. If a future field on
// this route ever has to distinguish the two, a pointer would turn a clear into a
// no-op, and this test is where that shows up.
//
// It also pins the second half: `{"direction":null}` must NOT be read as the empty
// string and rejected as an illegal direction — a null must leave the row alone, not
// 400.
func TestPatchSync_NullIsAbsent(t *testing.T) {
	app := mountSync(t)

	code, body := do(t, app, http.MethodPost, "/v1/sync", "acme", map[string]any{
		"source":    map[string]any{"provider": "github", "locator": widgetsURL},
		"direction": "pull",
		"trigger":   "manual",
		"actor":     "bot",
	})
	if code != http.StatusOK {
		t.Fatalf("create want 200, got %d (%s)", code, body)
	}
	var created syncView
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create json: %v (%s)", err, body)
	}

	// An explicit null on every mutable field must be a no-op, byte for byte the same
	// answer the empty patch gives.
	nulled := patchRaw(t, app, created.ID, `{"direction":null,"trigger":null,"actor":null}`)
	absent := patchRaw(t, app, created.ID, `{}`)

	for _, got := range []syncView{nulled, absent} {
		if got.Direction != "pull" || got.Trigger != "manual" || got.Actor != "bot" {
			t.Fatalf("null/absent must leave policy untouched, got %+v", got)
		}
	}
	if nulled.Direction != absent.Direction || nulled.Trigger != absent.Trigger || nulled.Actor != absent.Actor {
		t.Fatalf("null and absent must be indistinguishable: %+v vs %+v", nulled, absent)
	}

	// And a real value still lands, so the no-op above is not the whole route being inert.
	if got := patchRaw(t, app, created.ID, `{"direction":"push"}`); got.Direction != "push" {
		t.Fatalf("explicit value must apply, got %+v", got)
	}
}

// patchRaw PATCHes one sync with a VERBATIM body (not a marshalled map), because the
// distinction this file pins — an explicit null versus an absent key — cannot be
// expressed through a Go map.
func patchRaw(t *testing.T, app *zip.App, id, body string) syncView {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/v1/sync/"+id, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u_acme")
	resp, err := app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("PATCH %s: %v", body, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH %s want 200, got %d", body, resp.StatusCode)
	}
	var v syncView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("PATCH %s json: %v", body, err)
	}
	return v
}
