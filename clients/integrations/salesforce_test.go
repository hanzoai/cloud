package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newSalesforceMock stands in for the Salesforce token endpoint. It reuses the shared
// fakeIDToken helper (microsoft_test.go). It returns tokens +
// a plausible https instance_url + a decode-only id_token. Repoints the login base via
// the env seam.
func newSalesforceMock(t *testing.T, instanceURL string) {
	t.Helper()
	idToken := fakeIDToken(t, map[string]any{"user_id": "005x00000012345", "email": "admin@acme.com"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/services/oauth2/token") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"SF-ACCESS-SECRET","refresh_token":"SF-REFRESH-SECRET","instance_url":"` +
			instanceURL + `","id":"https://login.salesforce.com/id/00Dx/005x","id_token":"` + idToken + `","token_type":"Bearer","scope":"api refresh_token openid"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(salesforceLoginBaseEnv, srv.URL)
}

func TestSalesforceAuthorizeScope(t *testing.T) {
	raw, err := salesforceAuthorize(OAuthConfig{ClientID: "cid"}, "https://api.hanzo.ai/v1/integrations/salesforce/callback", "st8")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != "cid" || q.Get("state") != "st8" {
		t.Fatalf("authorize params wrong: %s", raw)
	}
	scope := q.Get("scope")
	for _, want := range []string{"api", "refresh_token", "openid"} {
		if !strings.Contains(scope, want) {
			t.Errorf("scope %q missing %q", scope, want)
		}
	}
	// Least privilege: never the broad grants.
	for _, forbidden := range []string{"full", "web", "chatter_api"} {
		for _, s := range strings.Fields(scope) {
			if s == forbidden {
				t.Errorf("scope must not include %q: %q", forbidden, scope)
			}
		}
	}
}

func TestSalesforceExchangeSealsTokensAndInstance(t *testing.T) {
	newSalesforceMock(t, "https://acme.my.salesforce.com")
	res, err := salesforceExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "csecret"},
		"https://api.hanzo.ai/v1/integrations/salesforce/callback", "code")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.Tokens[accessSecret] != "SF-ACCESS-SECRET" || res.Tokens[refreshSecret] != "SF-REFRESH-SECRET" {
		t.Fatalf("tokens not sealed: %+v", res.Tokens)
	}
	if res.Tokens[salesforceInstanceURL] != "https://acme.my.salesforce.com" {
		t.Fatalf("instance_url must be custodied, got %q", res.Tokens[salesforceInstanceURL])
	}
	if res.ExternalID != "005x00000012345" || res.AccountLabel != "admin@acme.com" {
		t.Fatalf("account from id_token wrong: id=%q label=%q", res.ExternalID, res.AccountLabel)
	}
}

// TestSalesforceInstanceGuard is the SSRF gate: a non-https, credential-bearing, or
// empty instance_url must be REFUSED — the reader custodies this as its API host.
func TestSalesforceInstanceGuard(t *testing.T) {
	for _, bad := range []string{
		"", "   ",
		"http://acme.my.salesforce.com",         // not https
		"https://user:pass@acme.salesforce.com", // embedded credential
		"ftp://acme.salesforce.com",             // wrong scheme
		"https://",                              // no host
		"not a url",
	} {
		if _, err := salesforceInstance(bad); err == nil {
			t.Errorf("salesforceInstance(%q): want reject", bad)
		}
	}
	if got, err := salesforceInstance("https://acme.my.salesforce.com/services"); err != nil || got != "https://acme.my.salesforce.com" {
		t.Errorf("valid instance normalized wrong: %q %v", got, err)
	}
	// An exchange whose token response omits a usable instance_url FAILS (a Salesforce
	// connection with no API host is broken; refusing it blocks SSRF injection).
	newSalesforceMock(t, "http://evil.internal")
	if _, err := salesforceExchange(context.Background(), OAuthConfig{ClientID: "c", ClientSecret: "s"}, "redir", "code"); err == nil {
		t.Fatal("a non-https instance_url must fail the exchange")
	}
}

func TestSalesforceExchangeRequiresSecret(t *testing.T) {
	_, err := salesforceExchange(context.Background(), OAuthConfig{ClientID: "cid"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "SALESFORCE_CLIENT_SECRET") {
		t.Fatalf("want a specific not-configured error, got %v", err)
	}
}

func TestSalesforceExchangeTokenFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"expired authorization code"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(salesforceLoginBaseEnv, srv.URL)
	_, err := salesforceExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "top-secret-value"}, "redir", "code")
	if err == nil {
		t.Fatal("a token endpoint error must fail the exchange")
	}
	if strings.Contains(err.Error(), "top-secret-value") {
		t.Fatalf("exchange error leaked the client secret: %v", err)
	}
}

// TestSalesforceE2ESealsAndIsolates drives connect(admin)→callback(signed state) and
// proves the three secrets seal ONLY in the connecting org's KMS namespace, the row
// holds no secret, and a second org sees nothing.
func TestSalesforceE2ESealsAndIsolates(t *testing.T) {
	newSalesforceMock(t, "https://acme.my.salesforce.com")
	t.Setenv(salesforceClientIDEnv, "cid")
	t.Setenv(salesforceClientSecretEnv, "csecret")
	kc := newKMS(t)
	app := newApp(t, kc)

	cb := oauthCallbackViaState(t, app, "salesforce", "acme", "authcode")
	if cb.Code != http.StatusFound || !strings.Contains(cb.Location, "connected=salesforce") {
		t.Fatalf("callback want 302 connected, got %d loc=%q body=%s", cb.Code, cb.Location, cb.Body)
	}
	for name, want := range map[string]string{
		accessSecret:          "SF-ACCESS-SECRET",
		refreshSecret:         "SF-REFRESH-SECRET",
		salesforceInstanceURL: "https://acme.my.salesforce.com",
	} {
		got, ok := kmsSecret(t, kc, "acme", "salesforce", name)
		if !ok || string(got) != want {
			t.Fatalf("%s must be sealed for acme, got %q ok=%v", name, got, ok)
		}
	}
	// Tenant isolation: org B has nothing and the token seam refuses it.
	if _, ok := kmsSecret(t, kc, "orgb", "salesforce", accessSecret); ok {
		t.Fatal("orgb must not have a credential")
	}
	if _, err := TokenFor(context.Background(), "orgb", "salesforce", accessSecret); err == nil {
		t.Fatal("TokenFor must refuse a non-connected org")
	}
}
