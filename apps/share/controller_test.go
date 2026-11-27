package share

// The controller's answers, read off the wire. "No account" is what the
// controller says when it refuses the credential, and nothing else: a controller
// that cannot be reached has said nothing about the account, and enable must not
// go on to bill a provision it cannot make.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/planetest"
)

// zrokAt is a controller whose login answers status, with a token on 200.
func zrokAt(t *testing.T, status int) (*httpController, *int) {
	t.Helper()
	creates := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/login":
			w.WriteHeader(status)
			if status == http.StatusOK {
				_, _ = w.Write([]byte(`"acct-tok"`))
			}
		case "/v1/account":
			creates++
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &httpController{base: srv.URL, adminToken: "admin", secret: []byte("k"), cc: srv.Client()}, &creates
}

// unreachable is a controller at an address nothing listens on, as one that is
// configured but not deployed is.
func unreachable(t *testing.T) *httpController {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	return &httpController{base: base, adminToken: "admin", secret: []byte("k"), cc: http.DefaultClient}
}

func TestARefusedLoginIsNoAccount(t *testing.T) {
	c, _ := zrokAt(t, http.StatusUnauthorized)
	if _, err := c.token(context.Background(), holding{org: "acme"}, false); !errors.Is(err, errNoAccount) {
		t.Fatalf("a 401 login = %v, want errNoAccount", err)
	}
}

func TestAnUnreachableControllerIsNotNoAccount(t *testing.T) {
	_, err := unreachable(t).token(context.Background(), holding{org: "acme"}, false)
	if err == nil || errors.Is(err, errNoAccount) {
		t.Fatalf("an unreachable controller = %v, want an error that is not errNoAccount", err)
	}
}

func TestAFailingControllerIsNotNoAccount(t *testing.T) {
	c, creates := zrokAt(t, http.StatusBadGateway)
	_, err := c.token(context.Background(), holding{org: "acme"}, true)
	if err == nil || errors.Is(err, errNoAccount) {
		t.Fatalf("a 502 login = %v, want an error that is not errNoAccount", err)
	}
	if *creates != 0 {
		t.Fatalf("a failing login went on to create %d accounts, want 0", *creates)
	}
}

func TestALoginReturnsTheAccountToken(t *testing.T) {
	c, _ := zrokAt(t, http.StatusOK)
	tok, err := c.token(context.Background(), holding{org: "acme"}, false)
	if err != nil || tok != "acct-tok" {
		t.Fatalf("login = %q, %v, want the controller's token", tok, err)
	}
}

// An unfunded caller enabling a share while the controller is down is told the
// share controller is unavailable, never that their balance is short, and no
// provision is reserved or billed.
//
// 503 with a code, never 502: Cloudflare, in front of api.hanzo.ai, replaces an
// origin's 502 with its own "error code: 502", so a caller reads nothing cloud
// said. That is how #765 went nine tries with no reason: cloud could not resolve
// a controller that had not been deployed beside it, and said so to nobody.
func TestEnableWithTheControllerDownIsUnavailableNotUnpaid(t *testing.T) {
	for _, balance := range []int64{0, 100000} {
		l := planetest.Money(t, balance)
		app := billedShare(t, l, unreachable(t))
		code, body := call(t, app, http.MethodPost, "/v1/share/enable", "acme")
		if code != http.StatusServiceUnavailable || !strings.Contains(string(body), codeControllerUnavailable) ||
			!strings.Contains(string(body), "nothing was charged") {
			t.Fatalf("balance %d: enable with the controller down = %d (%s), want 503 %s saying nothing was charged",
				balance, code, body, codeControllerUnavailable)
		}
		if n := l.Count(); n != 0 {
			t.Fatalf("balance %d: debits = %d for a provision that never happened, want 0", balance, n)
		}
	}
}

// A controller that hangs is the same typed 503, bounded by the client's own timeout.
func TestEnableWithAHangingControllerIsUnavailable(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	c := &httpController{base: srv.URL, adminToken: "admin", secret: []byte("k"), cc: &http.Client{Timeout: 300 * time.Millisecond}}

	l := planetest.Money(t, 100000)
	start := time.Now()
	code, body := call(t, billedShare(t, l, c), http.MethodPost, "/v1/share/enable", "acme")
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("enable took %v against a hanging controller, want it inside the client timeout", took)
	}
	if code != http.StatusServiceUnavailable || !strings.Contains(string(body), codeControllerUnavailable) {
		t.Fatalf("enable against a hanging controller = %d (%s), want 503 %s", code, body, codeControllerUnavailable)
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a provision that never happened, want 0", n)
	}
}

// A provision the controller refuses — the account cannot be made at any
// generation — is the same readable 503, after the reservation is released: the
// caller is told it was not made and that nothing was charged.
func TestAProvisionTheControllerRefusesIsReadableAndUnbilled(t *testing.T) {
	l := planetest.Money(t, 100000)
	c, creates := zrokAt(t, http.StatusUnauthorized)
	code, body := call(t, billedShare(t, l, c), http.MethodPost, "/v1/share/enable", "acme")
	if code != http.StatusServiceUnavailable || !strings.Contains(string(body), codeControllerUnavailable) ||
		!strings.Contains(string(body), "nothing was charged") {
		t.Fatalf("enable whose provision failed = %d (%s), want 503 %s saying nothing was charged",
			code, body, codeControllerUnavailable)
	}
	if *creates == 0 {
		t.Fatal("the controller was never asked to create the account; this test did not reach the provision")
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a provision that failed, want 0", n)
	}
}

// A soft-deleted zrok account row burns generation 0's email in zrok's unique
// index (login -> 401, create -> 500, grants -> 404 empty body). Provisioning
// advances deterministically to generation 1, subsequent enable calls read
// generation 1 back for free, and rotate rotates generation 1 cleanly.
func TestABurnedPrimaryEmailProvisionsOnNextGenerationAndReadsBackFree(t *testing.T) {
	c := &httpController{adminToken: "admin", secret: []byte("k")}
	h := holding{org: "acme"}
	burnedEmail, _ := c.credentialGen(h, 0)

	type acctRow struct {
		password string
		token    string
	}
	store := map[string]*acctRow{}
	creates := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/v1/login":
			row, ok := store[body["email"]]
			if !ok || row.password != body["password"] {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(row.token)
		case "/v1/account":
			creates++
			if body["email"] == burnedEmail {
				// Soft-deleted row still occupies zrok's unique accounts.email index.
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if _, exists := store[body["email"]]; exists {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			store[body["email"]] = &acctRow{password: body["password"], token: "tok-gen1"}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"accountToken": "tok-gen1"})
		case "/v1/grants":
			// Soft-deleted accounts are excluded by FindAccountWithEmail -> bare 404.
			w.WriteHeader(http.StatusNotFound)
		case "/v1/regenerateAccountToken":
			row := store[body["emailAddress"]]
			if row == nil || r.Header.Get(accountHeader) != row.token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			row.token = "tok-gen1-rotated"
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"accountToken": row.token})
		case "/v1/overview":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"environments":[{"environment":{"zId":"env-1"}}]}`))
		case "/v1/disable":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c.base = srv.URL
	c.cc = srv.Client()

	tok, err := c.token(context.Background(), h, true)
	if err != nil || tok != "tok-gen1" {
		t.Fatalf("token(create=true) = %q, %v, want tok-gen1", tok, err)
	}
	if creates != 2 {
		t.Fatalf("creates = %d, want 2 (gen 0 burned + gen 1 created)", creates)
	}

	// Read-back finds generation 1 without creating another account.
	tok2, err := c.token(context.Background(), h, false)
	if err != nil || tok2 != "tok-gen1" {
		t.Fatalf("token(create=false) = %q, %v, want tok-gen1", tok2, err)
	}
	if creates != 2 {
		t.Fatalf("read-back triggered create: creates = %d, want 2", creates)
	}

	// Rotation rotates generation 1 and ends its active environment.
	ended, err := c.rotate(context.Background(), h)
	if err != nil || ended != 1 {
		t.Fatalf("rotate = (%d, %v), want (1, nil)", ended, err)
	}
}

// A single dropped keep-alive connection or upstream 502 on login retries once
// instead of immediately failing enable with 502.
func TestATransientLoginDropRecovers(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/login" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if attempts.Add(1) == 1 {
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`"acct-tok"`))
	}))
	t.Cleanup(srv.Close)

	c := &httpController{base: srv.URL, adminToken: "admin", secret: []byte("k"), cc: srv.Client()}
	tok, err := c.token(context.Background(), holding{org: "acme"}, false)
	if err != nil || tok != "acct-tok" {
		t.Fatalf("transient drop login = %q, %v, want acct-tok", tok, err)
	}
}
