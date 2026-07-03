package console

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zap-proto/zip"
)

// callH drives a request with arbitrary VALIDATED-identity headers (the gateway sets
// these only from a verified credential). Mirrors console_test.go's `call` but lets a
// test inject X-User-Email / X-User-IsAdmin, which the ported routes read.
func callH(t *testing.T, app *zip.App, method, path string, headers map[string]string, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// fakeWaitlist is the Base waitlist plugin: it records the {waitlist,email} it
// received (so a test proves the email was bound to the SESSION, not the body) and
// returns a canned verdict.
type fakeWaitlist struct {
	mu       sync.Mutex
	gotEmail string
	gotList  string
}

func (f *fakeWaitlist) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/waitlist/join" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var body struct{ Waitlist, Email string }
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.gotEmail, f.gotList = body.Email, body.Waitlist
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"rank":7,"total":42}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWaitlist_RequiresValidatedPrincipal(t *testing.T) {
	f := &fakeWaitlist{}
	t.Setenv("WAITLIST_URL", f.server(t).URL)
	app := mountApp(t, "http://iam.invalid", "", "")

	// No X-User-Id → 403, even though the waitlist is configured (not an open relay).
	code, _ := callH(t, app, http.MethodPost, "/v1/console/waitlist", nil, `{"waitlist":"erp","email":"x@y.com"}`)
	if code != http.StatusForbidden {
		t.Fatalf("no principal: want 403, got %d", code)
	}
	if f.gotEmail != "" {
		t.Fatalf("backend must not be reached without a principal, got email=%q", f.gotEmail)
	}
}

func TestWaitlist_NotConfigured_501(t *testing.T) {
	t.Setenv("WAITLIST_URL", "")
	app := mountApp(t, "http://iam.invalid", "", "")
	code, _ := callH(t, app, http.MethodPost, "/v1/console/waitlist",
		map[string]string{"X-User-Id": "alice", "X-User-Email": "alice@acme.com"}, `{"waitlist":"erp"}`)
	if code != http.StatusNotImplemented {
		t.Fatalf("unconfigured waitlist: want 501, got %d", code)
	}
}

func TestWaitlist_BindsEmailToSession(t *testing.T) {
	f := &fakeWaitlist{}
	t.Setenv("WAITLIST_URL", f.server(t).URL)
	app := mountApp(t, "http://iam.invalid", "", "")

	// The session email is alice@acme.com; the body tries to enroll evil@x.com. The
	// handler MUST forward the session email — a signed-in user can't enroll a third
	// party — and pass the backend verdict through.
	code, body := callH(t, app, http.MethodPost, "/v1/console/waitlist",
		map[string]string{"X-User-Id": "alice", "X-Org-Id": "acme", "X-User-Email": "alice@acme.com"},
		`{"waitlist":"erp","email":"evil@x.com"}`)
	if code != http.StatusOK {
		t.Fatalf("join: want 200, got %d (%s)", code, body)
	}
	if f.gotEmail != "alice@acme.com" {
		t.Fatalf("email must be bound to the session, got %q (must NOT be evil@x.com)", f.gotEmail)
	}
	if f.gotList != "erp" {
		t.Fatalf("waitlist slug: want erp, got %q", f.gotList)
	}
	var v struct {
		OK   bool `json:"ok"`
		Rank int  `json:"rank"`
	}
	mustJSON(t, body, &v)
	if !v.OK || v.Rank != 7 {
		t.Fatalf("backend verdict not passed through: %s", body)
	}
}

func TestWaitlist_FallsBackToBodyEmailWhenClaimHasNone(t *testing.T) {
	f := &fakeWaitlist{}
	t.Setenv("WAITLIST_URL", f.server(t).URL)
	app := mountApp(t, "http://iam.invalid", "", "")

	// No X-User-Email on the claim → the client-supplied email is the fallback.
	code, _ := callH(t, app, http.MethodPost, "/v1/console/waitlist",
		map[string]string{"X-User-Id": "bob"}, `{"waitlist":"cms","email":"bob@self.com"}`)
	if code != http.StatusOK {
		t.Fatalf("fallback join: want 200, got %d", code)
	}
	if f.gotEmail != "bob@self.com" {
		t.Fatalf("fallback email: want bob@self.com, got %q", f.gotEmail)
	}
}

func TestWaitlist_InvalidEmail_400(t *testing.T) {
	f := &fakeWaitlist{}
	t.Setenv("WAITLIST_URL", f.server(t).URL)
	app := mountApp(t, "http://iam.invalid", "", "")

	// No session email and a junk body email → 400, backend untouched.
	code, _ := callH(t, app, http.MethodPost, "/v1/console/waitlist",
		map[string]string{"X-User-Id": "bob"}, `{"waitlist":"cms","email":"not-an-email"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("invalid email: want 400, got %d", code)
	}
	if f.gotEmail != "" {
		t.Fatalf("backend must not be reached on an invalid email, got %q", f.gotEmail)
	}
}

func TestWaitlist_MissingWaitlist_400(t *testing.T) {
	f := &fakeWaitlist{}
	t.Setenv("WAITLIST_URL", f.server(t).URL)
	app := mountApp(t, "http://iam.invalid", "", "")
	code, _ := callH(t, app, http.MethodPost, "/v1/console/waitlist",
		map[string]string{"X-User-Id": "alice", "X-User-Email": "alice@acme.com"}, `{"email":"alice@acme.com"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("missing waitlist: want 400, got %d", code)
	}
}
