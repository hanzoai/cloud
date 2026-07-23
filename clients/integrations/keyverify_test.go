package integrations

// keyverify_test.go proves the ONE key-based verify mechanism end to end against
// httptest stand-ins (zero live network): every credential placement lands where
// the provider expects it, verify is fail-closed (only 2xx stores), errors are
// token-free on EVERY failure path (including the queryAt key-in-URL case),
// offline rejects never touch the network, account-derived origins/paths resolve,
// and the whole ecommerce+AI catalog is well-formed for Mount and /v1/connectors.

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// sentinel is the credential value that must NEVER appear in an error string.
const sentinel = "SENTINEL-KEY-DO-NOT-LEAK-9137"

// capture records the last request a verify made, for wire assertions.
type capture struct {
	mu       sync.Mutex
	calls    int
	method   string
	path     string
	rawQuery string
	auth     string
	headers  http.Header
	body     string
}

func (c *capture) snap() capture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return capture{calls: c.calls, method: c.method, path: c.path, rawQuery: c.rawQuery, auth: c.auth, headers: c.headers, body: c.body}
}

// captureServer returns a server that records each request and replies `status`
// (0 => 200). t.Cleanup closes it.
func captureServer(t *testing.T, status int) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{headers: http.Header{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		c.mu.Lock()
		c.calls++
		c.method, c.path, c.rawQuery = r.Method, r.URL.Path, r.URL.RawQuery
		c.auth, c.headers, c.body = r.Header.Get("Authorization"), r.Header.Clone(), string(b)
		c.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

// fixedOrigin ignores the input and always serves url — the synthetic-spec seam.
func fixedOrigin(url string) func(VerifyInput) (string, error) {
	return func(VerifyInput) (string, error) { return url, nil }
}

func decodeBasic(t *testing.T, auth string) string {
	t.Helper()
	if !strings.HasPrefix(auth, "Basic ") {
		t.Fatalf("not a Basic header: %q", auth)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
	if err != nil {
		t.Fatalf("basic decode: %v", err)
	}
	return string(raw)
}

// TestPlacements drives each credential placement to a capture server and asserts
// the credential arrives exactly where the provider expects it, and that a 200
// seals the key under the right secret slot.
func TestPlacements(t *testing.T) {
	srv, cap := captureServer(t, 0)
	ctx := context.Background()

	t.Run("bearer", func(t *testing.T) {
		v := keyVerify(keySpec{provider: "t", origin: fixedOrigin(srv.URL), path: "/x", place: bearer})
		res, err := v(ctx, VerifyInput{Token: sentinel})
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if got := cap.snap().auth; got != "Bearer "+sentinel {
			t.Errorf("auth = %q", got)
		}
		if res.Tokens[apiKeySecret] != sentinel {
			t.Errorf("sealed = %q", res.Tokens[apiKeySecret])
		}
	})

	t.Run("token", func(t *testing.T) {
		v := keyVerify(keySpec{provider: "t", origin: fixedOrigin(srv.URL), path: "/x", place: tokenAuth})
		if _, err := v(ctx, VerifyInput{Token: sentinel}); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if got := cap.snap().auth; got != "Token "+sentinel {
			t.Errorf("auth = %q", got)
		}
	})

	t.Run("header-raw", func(t *testing.T) {
		v := keyVerify(keySpec{provider: "t", origin: fixedOrigin(srv.URL), path: "/x", place: headerAt, name: "Api-Key"})
		if _, err := v(ctx, VerifyInput{Token: sentinel}); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if got := cap.snap().headers.Get("Api-Key"); got != sentinel {
			t.Errorf("Api-Key = %q", got)
		}
	})

	t.Run("header-scheme", func(t *testing.T) {
		v := keyVerify(keySpec{provider: "t", origin: fixedOrigin(srv.URL), path: "/x", place: headerAt, name: "Authorization", scheme: "Klaviyo-API-Key"})
		if _, err := v(ctx, VerifyInput{Token: sentinel}); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if got := cap.snap().auth; got != "Klaviyo-API-Key "+sentinel {
			t.Errorf("auth = %q", got)
		}
	})

	t.Run("query", func(t *testing.T) {
		v := keyVerify(keySpec{provider: "t", origin: fixedOrigin(srv.URL), path: "/x", place: queryAt, name: "key"})
		if _, err := v(ctx, VerifyInput{Token: sentinel}); err != nil {
			t.Fatalf("verify: %v", err)
		}
		s := cap.snap()
		if !strings.Contains(s.rawQuery, "key="+sentinel) {
			t.Errorf("rawQuery = %q", s.rawQuery)
		}
		if s.auth != "" {
			t.Errorf("query placement set an Authorization header: %q", s.auth)
		}
	})

	t.Run("basic-user", func(t *testing.T) {
		v := keyVerify(keySpec{provider: "t", origin: fixedOrigin(srv.URL), path: "/x", place: basicAt, name: "hanzo"})
		if _, err := v(ctx, VerifyInput{Token: sentinel}); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if got := decodeBasic(t, cap.snap().auth); got != "hanzo:"+sentinel {
			t.Errorf("basic = %q", got)
		}
	})

	t.Run("basic-account", func(t *testing.T) {
		v := keyVerify(keySpec{provider: "t", origin: fixedOrigin(srv.URL), path: "/x", place: basicAt, name: accountToken})
		if _, err := v(ctx, VerifyInput{Token: sentinel, AccountID: "AC123"}); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if got := decodeBasic(t, cap.snap().auth); got != "AC123:"+sentinel {
			t.Errorf("basic = %q", got)
		}
	})

	t.Run("basic-raw", func(t *testing.T) {
		v := keyVerify(keySpec{provider: "t", origin: fixedOrigin(srv.URL), path: "/x", place: basicRaw})
		if _, err := v(ctx, VerifyInput{Token: "clientid:secretvalue"}); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if got := decodeBasic(t, cap.snap().auth); got != "clientid:secretvalue" {
			t.Errorf("basic-raw = %q", got)
		}
	})
}

// TestFailClosedTokenFree asserts every non-2xx and transport failure returns an
// error, stores nothing, and NEVER echoes the credential — checked on a bearer
// header AND on queryAt (where the key rides the URL and a wrapped error leaks).
func TestFailClosedTokenFree(t *testing.T) {
	ctx := context.Background()

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError, http.StatusTeapot} {
		srv, _ := captureServer(t, status)
		for _, place := range []placement{bearer, queryAt} {
			v := keyVerify(keySpec{provider: "t", origin: fixedOrigin(srv.URL), path: "/x", place: place, name: "key"})
			res, err := v(ctx, VerifyInput{Token: sentinel})
			if err == nil || res != nil {
				t.Fatalf("status %d place %d: want fail-closed, got res=%v err=%v", status, place, res, err)
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatalf("status %d place %d: error leaked the credential: %v", status, place, err)
			}
		}
	}

	// Transport failure: point at a closed server. queryAt is the sharp case —
	// the key is in the URL, so a wrapped *url.Error would leak it.
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := closed.URL
	closed.Close()
	for _, place := range []placement{bearer, queryAt} {
		v := keyVerify(keySpec{provider: "t", origin: fixedOrigin(url), path: "/x", place: place, name: "key"})
		_, err := v(ctx, VerifyInput{Token: sentinel})
		if err == nil {
			t.Fatalf("place %d: want transport error", place)
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Fatalf("place %d: transport error leaked the credential: %v", place, err)
		}
	}
}

// TestRedirectFailsClosed proves the shared authClient never follows a 3xx: a
// provider answering with a redirect (e.g. bad auth -> 302 -> 200 login page)
// surfaces the 3xx as the final response, so verify fails closed and seals
// nothing — closing the redirect fail-open amplifier and custom-header
// forwarding across a cross-host hop.
func TestRedirectFailsClosed(t *testing.T) {
	ctx := context.Background()
	landing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // the 200 that must NEVER be reached
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(landing.Close)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, landing.URL, http.StatusFound)
	}))
	t.Cleanup(redir.Close)

	v := keyVerify(keySpec{provider: "t", origin: fixedOrigin(redir.URL), path: "/x", place: bearer})
	if res, err := v(ctx, VerifyInput{Token: sentinel}); err == nil || res != nil {
		t.Fatalf("redirect must fail closed: res=%v err=%v", res, err)
	}
}

// TestOfflineReject asserts minLen and prefix reject before ANY network call.
func TestOfflineReject(t *testing.T) {
	srv, cap := captureServer(t, 0)
	ctx := context.Background()

	short := keyVerify(keySpec{provider: "t", origin: fixedOrigin(srv.URL), path: "/x", place: bearer, minLen: 32})
	if _, err := short(ctx, VerifyInput{Token: "tiny"}); err == nil {
		t.Fatal("minLen: want reject")
	}
	badPrefix := keyVerify(keySpec{provider: "t", origin: fixedOrigin(srv.URL), path: "/x", place: bearer, prefix: "hf_"})
	if _, err := badPrefix(ctx, VerifyInput{Token: "sk-not-a-hf-key"}); err == nil {
		t.Fatal("prefix: want reject")
	}
	empty := keyVerify(keySpec{provider: "t", origin: fixedOrigin(srv.URL), path: "/x", place: bearer})
	if _, err := empty(ctx, VerifyInput{Token: "   "}); err == nil {
		t.Fatal("empty: want reject")
	}
	if n := cap.snap().calls; n != 0 {
		t.Fatalf("offline rejects reached the network: %d calls", n)
	}
}

// TestAccountResolution covers {account} path templating (required) and
// echoAccount (ExternalID from AccountID).
func TestAccountResolution(t *testing.T) {
	srv, cap := captureServer(t, 0)
	ctx := context.Background()

	tmpl := keyVerify(keySpec{provider: "twilio", origin: fixedOrigin(srv.URL), path: "/Accounts/{account}.json", place: basicAt, name: accountToken, echoAccount: true})

	if _, err := tmpl(ctx, VerifyInput{Token: sentinel}); err == nil {
		t.Fatal("missing account: want reject")
	}
	if n := cap.snap().calls; n != 0 {
		t.Fatalf("missing-account reached the network: %d", n)
	}

	res, err := tmpl(ctx, VerifyInput{Token: sentinel, AccountID: "AC42"})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p := cap.snap().path; p != "/Accounts/AC42.json" {
		t.Errorf("path = %q", p)
	}
	if res.ExternalID != "AC42" {
		t.Errorf("ExternalID = %q", res.ExternalID)
	}
}

// TestShopHost pins the Shopify store-domain normalizer: bare handle, full
// domain, and URL all resolve; junk and non-myshopify hosts are rejected.
func TestShopHost(t *testing.T) {
	ok := map[string]string{
		"acme":                       "acme.myshopify.com",
		"acme.myshopify.com":         "acme.myshopify.com",
		"https://acme.myshopify.com": "acme.myshopify.com",
		"acme.myshopify.com/admin":   "acme.myshopify.com",
		"ACME":                       "acme.myshopify.com",
	}
	for in, want := range ok {
		got, err := shopHost(in)
		if err != nil || got != want {
			t.Errorf("shopHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "acme.example.com", "evil.com/../x", "a b.myshopify.com"} {
		if _, err := shopHost(in); err == nil {
			t.Errorf("shopHost(%q): want error", in)
		}
	}
}

// TestDatacenter pins the Mailchimp data-center parse.
func TestDatacenter(t *testing.T) {
	if dc, err := datacenter("abcdef0123456789-us21"); err != nil || dc != "us21" {
		t.Errorf(`datacenter("...-us21") = %q, %v`, dc, err)
	}
	for _, in := range []string{"nodatacenter", "trailing-", ""} {
		if _, err := datacenter(in); err == nil {
			t.Errorf("datacenter(%q): want error", in)
		}
	}
}

// catalogIDs is the ecommerce+AI-startup connector set this file guards.
var catalogIDs = []string{
	"stripe", "paypal", "square", "shopify",
	"gemini", "groq", "mistral", "cohere", "together", "replicate",
	"huggingface", "openrouter", "xai", "fireworks", "deepseek", "pinecone",
	"sendgrid", "resend", "postmark", "mailchimp", "klaviyo", "twilio",
	"hubspot", "notion", "linear", "airtable",
}

// TestCatalogWellFormed asserts every registered connector satisfies Mount's
// user-scope contract (Verify present, secrets + category set, NO org-plane
// fields) so boot cannot panic and /v1/connectors derives method "token".
func TestCatalogWellFormed(t *testing.T) {
	for _, id := range catalogIDs {
		p, ok := registry[id]
		if !ok {
			t.Errorf("%s: not registered", id)
			continue
		}
		if p.Scope != userScope {
			t.Errorf("%s: Scope = %q, want userScope", id, p.Scope)
		}
		if p.Verify == nil {
			t.Errorf("%s: nil Verify", id)
		}
		if len(p.Secrets) == 0 {
			t.Errorf("%s: no Secrets", id)
		}
		if p.Category == "" {
			t.Errorf("%s: no Category", id)
		}
		if p.Authorize != nil || p.Exchange != nil || p.Revoke != nil || p.Configured != nil || p.Creds != nil {
			t.Errorf("%s: org-plane field set on a user-scoped provider (Mount would reject)", id)
		}
	}
}

// TestCatalogWiring drives a few real registrations through their env seam to a
// capture server, proving the registration is wired to keyVerify with the right
// path and placement — not just that the metadata is present.
func TestCatalogWiring(t *testing.T) {
	ctx := context.Background()

	t.Run("stripe-bearer", func(t *testing.T) {
		srv, cap := captureServer(t, 0)
		t.Setenv("STRIPE_API_BASE", srv.URL)
		if _, err := registry["stripe"].Verify(ctx, VerifyInput{Token: "sk_test_" + sentinel}); err != nil {
			t.Fatalf("verify: %v", err)
		}
		s := cap.snap()
		if s.path != "/v1/balance" || s.auth != "Bearer sk_test_"+sentinel {
			t.Errorf("stripe wire: path=%q auth=%q", s.path, s.auth)
		}
	})

	t.Run("gemini-query", func(t *testing.T) {
		srv, cap := captureServer(t, 0)
		t.Setenv("GEMINI_API_BASE", srv.URL)
		if _, err := registry["gemini"].Verify(ctx, VerifyInput{Token: sentinel + "0123456789abcdef"}); err != nil {
			t.Fatalf("verify: %v", err)
		}
		s := cap.snap()
		if s.path != "/v1beta/models" || !strings.Contains(s.rawQuery, "key=") {
			t.Errorf("gemini wire: path=%q query=%q", s.path, s.rawQuery)
		}
	})

	t.Run("twilio-basic-account", func(t *testing.T) {
		srv, cap := captureServer(t, 0)
		t.Setenv("TWILIO_API_BASE", srv.URL)
		res, err := registry["twilio"].Verify(ctx, VerifyInput{Token: sentinel + "0123456789abcdef", AccountID: "AC777"})
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		s := cap.snap()
		if s.path != "/2010-04-01/Accounts/AC777.json" {
			t.Errorf("twilio path = %q", s.path)
		}
		if user := strings.SplitN(decodeBasic(t, s.auth), ":", 2)[0]; user != "AC777" {
			t.Errorf("twilio basic user = %q", user)
		}
		if res.Tokens["auth_token"] == "" || res.ExternalID != "AC777" {
			t.Errorf("twilio seal: secret=%q ext=%q", res.Tokens["auth_token"], res.ExternalID)
		}
	})

	t.Run("paypal-post-basicraw", func(t *testing.T) {
		srv, cap := captureServer(t, 0)
		t.Setenv("PAYPAL_API_BASE", srv.URL)
		if _, err := registry["paypal"].Verify(ctx, VerifyInput{Token: "clientid:secret"}); err != nil {
			t.Fatalf("verify: %v", err)
		}
		s := cap.snap()
		if s.method != http.MethodPost || s.path != "/v1/oauth2/token" || s.body != "grant_type=client_credentials" {
			t.Errorf("paypal wire: method=%q path=%q body=%q", s.method, s.path, s.body)
		}
		if decodeBasic(t, s.auth) != "clientid:secret" {
			t.Errorf("paypal basic = %q", s.auth)
		}
	})
}
