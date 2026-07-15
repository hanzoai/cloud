package session

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// do runs a JSON request carrying a validated principal (X-Org-Id + X-User-Id,
// the pair org() gates on). An empty org sends neither header — the anonymous
// case that must be refused.
func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

func create(t *testing.T, app *zip.App, org string, body map[string]any) sessionView {
	t.Helper()
	st, b := do(t, app, http.MethodPost, "/v1/code/sessions", org, body)
	if st != http.StatusCreated {
		t.Fatalf("create: status %d: %s", st, b)
	}
	var v sessionView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

// TestTenantIsolation proves a session created under one org is invisible and
// immutable to another: read, update, delete, and event routes all 404 across
// the org boundary, and a list never leaks the row.
func TestTenantIsolation(t *testing.T) {
	app := mount(t)
	s := create(t, app, "acme", map[string]any{"agent": "claude", "model": "zen5-pro", "host": "evo", "project": "site"})

	// acme sees its own session.
	if st, _ := do(t, app, http.MethodGet, "/v1/code/sessions/"+s.ID, "acme", nil); st != http.StatusOK {
		t.Fatalf("acme GET own: %d", st)
	}
	// evil cannot read, update, delete, or event acme's session.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/code/sessions/" + s.ID, nil},
		{http.MethodPatch, "/v1/code/sessions/" + s.ID, map[string]any{"status": "running"}},
		{http.MethodDelete, "/v1/code/sessions/" + s.ID, nil},
		{http.MethodPost, "/v1/code/sessions/" + s.ID + "/events", map[string]any{"kind": "log", "message": "x"}},
		{http.MethodGet, "/v1/code/sessions/" + s.ID + "/events", nil},
	} {
		st, b := do(t, app, tc.method, tc.path, "evil", tc.body)
		// events GET returns 200 with an empty list only if the session is
		// visible; since it is not, it must 404 like the rest.
		if st != http.StatusNotFound {
			t.Errorf("evil %s %s = %d (want 404): %s", tc.method, tc.path, st, b)
		}
	}
	// evil's list is empty; acme's holds exactly the one session.
	if st, b := do(t, app, http.MethodGet, "/v1/code/sessions", "evil", nil); st != 200 || string(bytes.TrimSpace(b)) != "[]" {
		t.Errorf("evil list = %d %s (want 200 [])", st, b)
	}
	var mine []sessionView
	_, b := do(t, app, http.MethodGet, "/v1/code/sessions", "acme", nil)
	_ = json.Unmarshal(b, &mine)
	if len(mine) != 1 || mine[0].ID != s.ID {
		t.Errorf("acme list = %v (want 1 session %s)", mine, s.ID)
	}
}

// TestAnonymousRefused proves no route serves a request without a validated
// principal.
func TestAnonymousRefused(t *testing.T) {
	app := mount(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/code/sessions"},
		{http.MethodGet, "/v1/code/sessions"},
		{http.MethodGet, "/v1/code/sessions/ses_x"},
	} {
		if st, _ := do(t, app, tc.method, tc.path, "", map[string]any{"agent": "claude"}); st != http.StatusForbidden {
			t.Errorf("anon %s %s = %d (want 403)", tc.method, tc.path, st)
		}
	}
}

// TestLifecycle walks a session from register through heartbeat, terminal
// publish, an event, and end — the launcher's real path.
func TestLifecycle(t *testing.T) {
	app := mount(t)
	s := create(t, app, "acme", map[string]any{"id": "ses_abcd1234", "agent": "codex", "host": "spark"})
	if s.ID != "ses_abcd1234" || s.Status != "starting" {
		t.Fatalf("register: %+v", s)
	}
	// publish the terminal URL + go running (heartbeat).
	st, b := do(t, app, http.MethodPatch, "/v1/code/sessions/"+s.ID, "acme",
		map[string]any{"status": "running", "terminalUrl": "https://code-x.zt.hanzo/"})
	if st != http.StatusOK {
		t.Fatalf("patch: %d %s", st, b)
	}
	var upd sessionView
	_ = json.Unmarshal(b, &upd)
	if upd.Status != "running" || upd.TerminalURL == "" {
		t.Fatalf("patch result: %+v", upd)
	}
	// a hook event lands.
	if st, b := do(t, app, http.MethodPost, "/v1/code/sessions/"+s.ID+"/events", "acme",
		map[string]any{"kind": "notification", "message": "run tests?"}); st != http.StatusCreated {
		t.Fatalf("event: %d %s", st, b)
	}
	// end it.
	if st, _ := do(t, app, http.MethodPatch, "/v1/code/sessions/"+s.ID, "acme", map[string]any{"ended": true}); st != http.StatusOK {
		t.Fatalf("end: %d", st)
	}
	// live filter now excludes it.
	var live []sessionView
	_, b = do(t, app, http.MethodGet, "/v1/code/sessions?live=1", "acme", nil)
	_ = json.Unmarshal(b, &live)
	if len(live) != 0 {
		t.Errorf("live after end = %d (want 0)", len(live))
	}
}

// TestValidation rejects an unknown agent and a malformed id.
func TestValidation(t *testing.T) {
	app := mount(t)
	if st, _ := do(t, app, http.MethodPost, "/v1/code/sessions", "acme", map[string]any{"agent": "emacs"}); st != http.StatusBadRequest {
		t.Errorf("bad agent = %d (want 400)", st)
	}
	if st, _ := do(t, app, http.MethodPost, "/v1/code/sessions", "acme", map[string]any{"agent": "claude", "id": "bad id!"}); st != http.StatusBadRequest {
		t.Errorf("bad id = %d (want 400)", st)
	}
}
