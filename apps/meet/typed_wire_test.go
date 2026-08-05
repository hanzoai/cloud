package meet

// Both of meet's routes are UNTYPED BY DESIGN, and this file MEASURES the reasons
// rather than asserting them. The reasons are recorded at each registration in
// Mount; what follows is the same two facts, driven through the real router, so
// that:
//
//   - typing either route without first closing the zip gap goes RED here, and
//   - when zip CAN express them, this is the exact ledger a conversion must keep.
//
// A stale refusal is worse than none: nobody re-checks a route whose reason is
// only prose.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestGetTokenAnswersARawTokenAsText is refusal #1. The office client reads the
// answer with res.text() — the body IS the token, not JSON carrying one. A typed op
// always marshals its Out through c.JSON, so converting this route would ship
// `"<token>"` under application/json and break every published bundle in the field.
func TestGetTokenAnswersARawTokenAsText(t *testing.T) {
	app := mount(t, teamSecret, apiKey, apiSecret)
	body, _ := json.Marshal(request{RoomName: roomIn(workspaceA), ParticipantName: "Ada"})
	req := httptest.NewRequest(http.MethodPost, "/v1/meet/getToken", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+workspaceToken(t, workspaceA, teamSecret, nil, time.Now().Add(time.Hour).Unix()))

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST /v1/meet/getToken: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mint = %d (%s), want 200", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain — a typed op would answer application/json", ct)
	}
	// The body is the token itself: three dot-separated segments, unquoted.
	if strings.HasPrefix(string(b), `"`) {
		t.Errorf("body is JSON-quoted (%s) — the client reads it with res.text()", b)
	}
	if strings.Count(string(b), ".") != 2 {
		t.Errorf("body = %q, want the raw <header>.<payload>.<sig> token", b)
	}
}

// TestHealthCarriesABodyAtBOTHStatuses is refusal #2. This route answers 200 with a
// body when meet can mint and 503 WITH THE SAME BODY when it cannot, and ready is
// the whole dashboard fact in both. zip's WithStatus takes ONE unconditional 2xx;
// the only way a typed op sends 503 is by returning an error, which renders as zip's
// flat {status,code,error} — so ready:false would vanish from the degraded answer,
// which is the one a probe actually reads. That is the multi-status gap (#78).
func TestHealthCarriesABodyAtBOTHStatuses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		key, sec   string
		wantStatus int
		wantReady  bool
		wantState  string
	}{
		{"configured", apiKey, apiSecret, http.StatusOK, true, "ok"},
		{"unconfigured", "", "", http.StatusServiceUnavailable, false, "degraded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := mount(t, teamSecret, tc.key, tc.sec)
			resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/meet/health", nil))
			if err != nil {
				t.Fatalf("GET /v1/meet/health: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, tc.wantStatus, b)
			}
			var got map[string]any
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("body not JSON at %d: %s", resp.StatusCode, b)
			}
			if got["ready"] != tc.wantReady || got["status"] != tc.wantState {
				t.Errorf("body = %v, want ready:%v status:%q — the SAME shape at both statuses is "+
					"exactly what a single declared success status cannot express", got, tc.wantReady, tc.wantState)
			}
			if got["service"] != "meet" {
				t.Errorf("service = %v, want meet", got["service"])
			}
		})
	}
}
