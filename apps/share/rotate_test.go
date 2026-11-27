package share

// rotate_test.go pins rotating a tunnel account: the old credential opens nothing
// at the controller afterwards, everything it enabled is ended, the account
// itself is kept, an account the controller holds under another credential is
// refused rather than reported done, and only the account's holder rotates it —
// an admin of the org for the org's shared account, the person for their own.
// It also pins that no call in a held org is served the org's shared account.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// zrok is one account on a controller, answering the five calls this surface
// makes the way specs/zrok.yml says the real one does.
type zrok struct {
	mu              sync.Mutex
	email, password string
	token           string
	envs            []string
	disabledWith    []string // the token every disable arrived on
	probed          int      // how many times grants was asked
}

func (z *zrok) serve(t *testing.T) *httpController {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		z.mu.Lock()
		defer z.mu.Unlock()
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		authed := r.Header.Get(accountHeader) == z.token
		switch r.URL.Path {
		case "/v1/login":
			if in["email"] != z.email || in["password"] != z.password {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(z.token)
		case "/v1/grants":
			z.probed++
			if r.Header.Get(adminHeader) != "admin" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if in["email"] != z.email {
				w.WriteHeader(http.StatusNotFound)
			}
		case "/v1/regenerateAccountToken":
			if !authed {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if in["emailAddress"] != z.email {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			z.token += "-next"
			_ = json.NewEncoder(w).Encode(map[string]string{"accountToken": z.token})
		case "/v1/overview":
			if !authed {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			envs := []map[string]any{}
			for _, id := range z.envs {
				envs = append(envs, map[string]any{"environment": map[string]string{"zId": id}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environments": envs})
		case "/v1/disable":
			z.disabledWith = append(z.disabledWith, r.Header.Get(accountHeader))
			if !authed || !slices.Contains(z.envs, in["identity"]) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			z.envs = slices.DeleteFunc(z.envs, func(e string) bool { return e == in["identity"] })
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &httpController{base: srv.URL, adminToken: "admin", secret: []byte("k"), cc: srv.Client()}
}

// A ROTATED ACCOUNT'S OLD CREDENTIAL OPENS NOTHING. The token every member who
// enabled before was handed is refused from the moment of the rotation, each
// environment it enabled is ended under the new token, and enable hands back the
// new one from the same deterministic login.
func TestARotatedAccountsOldTokenOpensNothing(t *testing.T) {
	z := &zrok{token: "tok", envs: []string{"z-staff", "z-stranger"}}
	c := z.serve(t)
	h := holding{org: "hanzo"}
	z.email, z.password = c.credential(h)
	ctx := context.Background()

	ended, err := c.rotate(ctx, h)
	if err != nil || ended != 2 {
		t.Fatalf("rotate = %d, %v; want both environments ended", ended, err)
	}
	if _, err := c.overview(ctx, "tok"); err == nil {
		t.Fatal("the old token still reads the account")
	}
	if len(z.envs) != 0 {
		t.Fatalf("environments the old token enabled survived: %v", z.envs)
	}
	if !slices.Equal(z.disabledWith, []string{z.token, z.token}) {
		t.Fatalf("environments were ended on %v, want the new token", z.disabledWith)
	}
	got, err := c.token(ctx, h, false)
	if err != nil || got != z.token || got == "tok" {
		t.Fatalf("enable after a rotation hands %q (%v), want the new token", got, err)
	}
	// The account's own login worked, so the admin probe — which writes to the
	// account's public shares — was never asked.
	if z.probed != 0 {
		t.Fatalf("a rotation whose login worked asked grants %d times", z.probed)
	}

	// An account never provisioned has nothing to rotate, and says so.
	if _, err := c.rotate(ctx, holding{org: "acme"}); !errors.Is(err, errNoAccount) {
		t.Fatalf("rotating an account nobody provisioned = %v, want errNoAccount", err)
	}
}

// AN ACCOUNT OUT OF REACH IS NEVER ROTATED IN NAME ONLY. The controller refuses a
// login alike for an address nobody holds and for a password that is not the
// account's, and only the first has nothing to rotate. An account that is there
// and refuses the derived credential keeps every token it handed out live, so
// the rotation is an error, and nothing on the account is touched.
func TestARotationThatCannotReachItsAccountSaysSo(t *testing.T) {
	z := &zrok{token: "tok", envs: []string{"z-staff"}}
	c := z.serve(t)
	h := holding{org: "hanzo"}
	z.email, _ = c.credential(h)
	z.password = "set at the controller by hand"

	if n, err := c.rotate(context.Background(), h); !errors.Is(err, errForeign) || n != 0 {
		t.Fatalf("rotating an account under another credential = %d, %v; want errForeign", n, err)
	}
	if z.token != "tok" || len(z.envs) != 1 {
		t.Fatalf("a refused rotation touched the account: token %q, envs %v", z.token, z.envs)
	}

	// Both refusals are a 503 with a code, never a 502: Cloudflare replaces an
	// origin's 502 with its own page, and "it was not rotated" is the one thing
	// the admin who asked must read.
	for _, tc := range []struct {
		err  error
		code string
	}{
		{errForeign, codeAccountOutOfReach},
		{errors.New("dial tcp: lookup zrok-controller.hanzo-zt.svc.cluster.local: no such host"), codeControllerUnavailable},
	} {
		app := mountShare(t, &fakeController{configuredV: true, rotateErr: tc.err})
		req := httptest.NewRequest(http.MethodPost, "/v1/share/rotate", nil)
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", "sub-boss")
		req.Header.Set("X-User-IsOrgAdmin", "true")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), tc.code) ||
			!strings.Contains(string(body), "not rotated") {
			t.Fatalf("rotate refused by %v = %d %s, want 503 %s saying it was not rotated", tc.err, resp.StatusCode, body, tc.code)
		}
	}
}

// ONLY THE HOLDER ROTATES. An org's shared account is an admin's to rotate — a
// member who holds its token for their tunnels does not end everyone else's — and
// in a held org a member rotates the account that is their own. An admin of a
// held org or a SuperAdmin rotates their own and, after it, the org's shared one,
// which nobody there holds: the rollout call.
func TestOnlyTheHolderRotatesAnAccount(t *testing.T) {
	own := func(sub string) holding {
		h, _ := holder(zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: sub}), "hanzo")
		return h
	}
	for _, c := range []struct {
		name    string
		org     string
		admin   string // the header that makes the caller an admin, if any
		code    int
		rotated []holding
	}{
		{"a member of a customer org", "acme", "", http.StatusForbidden, nil},
		{"an admin of a customer org", "acme", "X-User-IsOrgAdmin", http.StatusOK, []holding{{org: "acme"}}},
		{"a SuperAdmin in a customer org", "acme", "X-User-IsAdmin", http.StatusOK, []holding{{org: "acme"}}},
		{"a member of a held org", "hanzo", "", http.StatusOK, []holding{own("sub-ada")}},
		{"an admin of a held org", "hanzo", "X-User-IsOrgAdmin", http.StatusOK, []holding{own("sub-ada"), {org: "hanzo"}}},
		{"a SuperAdmin in a held org", "hanzo", "X-User-IsAdmin", http.StatusOK, []holding{own("sub-ada"), {org: "hanzo"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeController{configuredV: true, tokenV: "tok"}
			app := mountShare(t, f)
			req := httptest.NewRequest(http.MethodPost, "/v1/share/rotate", nil)
			req.Header.Set("X-Org-Id", c.org)
			req.Header.Set("X-User-Id", "sub-ada")
			if c.admin != "" {
				req.Header.Set(c.admin, "true")
			}
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != c.code {
				t.Fatalf("rotate = %d (%s), want %d", resp.StatusCode, body, c.code)
			}
			if !slices.Equal(f.rotated, c.rotated) {
				t.Fatalf("rotated %v, want %v", f.rotated, c.rotated)
			}
			if c.code != http.StatusOK {
				return
			}
			var out rotated
			if err := json.Unmarshal(body, &out); err != nil || out.Ended != 2*len(c.rotated) {
				t.Fatalf("answer %s, want ended %d", body, 2*len(c.rotated))
			}
		})
	}

	app := mountShare(t, &fakeController{configuredV: true})
	if code, _ := call(t, app, http.MethodPost, "/v1/share/rotate", ""); code != http.StatusUnauthorized {
		t.Fatalf("an anonymous rotate = %d, want 401", code)
	}
	if code, _ := call(t, mountShare(t, &fakeController{}), http.MethodPost, "/v1/share/rotate", "acme"); code != http.StatusServiceUnavailable {
		t.Fatalf("rotate on a deployment with no controller = %d, want 503", code)
	}
}

// THE ROLLOUT DEPENDS ON NOTHING ELSE. An admin of a held org whose credential
// names no subject holds no account of their own there, and still rotates the
// org's shared one; a member whose credential names none rotates nothing.
func TestTheRolloutNeedsNoAccountOfTheCallers(t *testing.T) {
	for _, c := range []struct {
		name    string
		admin   bool
		code    int
		rotated []holding
	}{
		{"an admin", true, http.StatusOK, []holding{{org: "hanzo"}}},
		{"a member", false, http.StatusForbidden, nil},
	} {
		f := &fakeController{configuredV: true}
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		// The identity boundary, for a credential that names no subject.
		app.Use(zip.Handler(func(zc *zip.Ctx) error {
			principal.Mint(zc, principal.Principal{Org: zc.Org(), User: zc.User()})
			return zc.Next()
		}))
		compose(app)
		routes(app, &cloud.Service[state]{Base: cloud.NewBase(cloud.Deps{}, "share"), State: state{cl: f}})
		req := httptest.NewRequest(http.MethodPost, "/v1/share/rotate", nil)
		req.Header.Set("X-Org-Id", "hanzo")
		req.Header.Set("X-User-Id", "ada")
		if c.admin {
			req.Header.Set("X-User-IsOrgAdmin", "true")
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != c.code || !slices.Equal(f.rotated, c.rotated) {
			t.Fatalf("%s naming no subject: %d, rotated %v; want %d, %v", c.name, resp.StatusCode, f.rotated, c.code, c.rotated)
		}
	}
}

// NO CALL IN A HELD ORG IS SERVED THE ORG'S SHARED ACCOUNT. Enable and the list
// ask the controller for the caller's own account — an admin's and a
// SuperAdmin's included — and a customer's org is served its shared one, as ever.
func TestAHeldOrgServesNobodyItsSharedAccount(t *testing.T) {
	for _, c := range []struct {
		name, org, admin string
		shared           bool
	}{
		{"a member of a held org", "hanzo", "", false},
		{"an admin of a held org", "hanzo", "X-User-IsOrgAdmin", false},
		{"a SuperAdmin in a held org", "hanzo", "X-User-IsAdmin", false},
		{"a member of a customer org", "acme", "", true},
	} {
		for _, op := range []struct{ method, path string }{{http.MethodPost, "/v1/share/enable"}, {http.MethodGet, "/v1/share"}} {
			f := &fakeController{configuredV: true, tokenV: "tok"}
			app := mountShare(t, f)
			req := httptest.NewRequest(op.method, op.path, nil)
			req.Header.Set("X-Org-Id", c.org)
			req.Header.Set("X-User-Id", "sub-ada")
			if c.admin != "" {
				req.Header.Set(c.admin, "true")
			}
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || len(f.asked) != 1 || (f.asked[0].person == "") != c.shared || f.asked[0].org != c.org {
				t.Fatalf("%s %s %s: %d, asked for %v", c.name, op.method, op.path, resp.StatusCode, f.asked)
			}
		}
	}
}

// A 404 SAYS "NO ACCOUNT" ONLY IN ZROK'S OWN WORDS. The controller answers grants
// 404 with no body for an address no account holds. A route it does not serve
// answers 404 with its router's JSON, and a proxy in front of it with a page of
// its own; neither says anything about the account, so a rotation that meets one
// is an error, never "nothing to rotate" — which would leave a live account
// reported done.
func TestOnlyZroksOwnNotFoundSaysNoAccount(t *testing.T) {
	for name, body := range map[string]string{
		"zrok's own":                "",
		"a route it does not serve": `{"code":404,"message":"path /v1/grants was not found"}`,
		"a proxy's page":            "404 page not found",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/login":
				w.WriteHeader(http.StatusUnauthorized)
			default:
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, body)
			}
		}))
		c := &httpController{base: srv.URL, adminToken: "admin", secret: []byte("k"), cc: srv.Client()}
		_, err := c.rotate(context.Background(), holding{org: "acme"})
		srv.Close()
		switch {
		case body == "" && !errors.Is(err, errNoAccount):
			t.Fatalf("%s: rotate = %v, want errNoAccount", name, err)
		case body != "" && (err == nil || errors.Is(err, errNoAccount) || errors.Is(err, errForeign)):
			t.Fatalf("%s: rotate = %v, want an error that is neither no account nor foreign", name, err)
		}
	}
}
