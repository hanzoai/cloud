package meet

// meet's mint route is UNTYPED BY DESIGN and its health route is a typed op that
// declares both of its statuses. This file MEASURES both answers rather than
// asserting them: the mint refusal is recorded at its registration in Mount and
// re-proved here, and the health test is the conversion's parity ledger — the
// exact pair of answers the typed op must keep.
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
	req.Header.Set("Authorization", "Bearer "+session(t, workspaceA, teamSecret, nil, time.Now().Add(time.Hour).Unix()))

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

// TestHealthCarriesABodyAtBOTHStatuses is the health conversion's parity proof.
// The route answers 200 with a body when meet can mint and 503 WITH THE SAME BODY
// when it cannot, and ready is the whole dashboard fact in both. That pair once
// kept it raw; the typed op now declares WithStatus(200, 503) and the report's own
// StatusCode picks between them, so the degraded answer keeps its body — and this
// test is what goes red if a change ever drops it back to an error envelope.
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
