package integrations

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// authnetMock stands in for Authorize.Net's request.api: it echoes a chosen
// resultCode, optionally BOM-prefixed (real responses carry a UTF-8 BOM) or at a
// chosen status. It records the request body so a test can prove the credential rode
// the JSON body (never the URL).
func newAuthnetMock(t *testing.T, resultCode string, bom bool, status int) *string {
	t.Helper()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		body = string(b)
		if status != 0 {
			w.WriteHeader(status)
		}
		payload := `{"messages":{"resultCode":"` + resultCode + `","message":[{"code":"I00001","text":"Successful."}]}}`
		if bom {
			payload = "\xef\xbb\xbf" + payload
		}
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AUTHORIZENET_API_BASE", srv.URL)
	return &body
}

func TestAuthorizeNetVerifyAcceptsOk(t *testing.T) {
	body := newAuthnetMock(t, "Ok", true /*BOM*/, 0)
	res, err := authorizeNetVerify(context.Background(), VerifyInput{Token: "myLoginID:" + sentinel})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.Tokens[apiKeySecret] != "myLoginID:"+sentinel {
		t.Errorf("must seal the raw pair, got %q", res.Tokens[apiKeySecret])
	}
	if res.ExternalID != "myLoginID" {
		t.Errorf("external id = %q, want the login id", res.ExternalID)
	}
	// The credential rode the JSON body, and the transaction key must be present there
	// (proving the two-part split reached merchantAuthentication).
	if !strings.Contains(*body, "myLoginID") || !strings.Contains(*body, sentinel) {
		t.Errorf("auth body missing credential fields: %q", *body)
	}
}

func TestAuthorizeNetVerifyRejectsError(t *testing.T) {
	newAuthnetMock(t, "Error", false, 0)
	res, err := authorizeNetVerify(context.Background(), VerifyInput{Token: "badLogin:" + sentinel})
	if err == nil || res != nil {
		t.Fatalf("resultCode Error must fail closed, got res=%v err=%v", res, err)
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error leaked the transaction key: %v", err)
	}
}

func TestAuthorizeNetVerifyRejectsMalformedCredential(t *testing.T) {
	// No colon → rejected BEFORE any network call (splitPair is offline).
	if _, err := authorizeNetVerify(context.Background(), VerifyInput{Token: "no-colon-here"}); err == nil {
		t.Fatal("a credential without a ':' must be rejected")
	}
}

func TestAuthorizeNetVerifyFailClosedOnNon2xx(t *testing.T) {
	newAuthnetMock(t, "Ok", false, http.StatusBadGateway)
	if res, err := authorizeNetVerify(context.Background(), VerifyInput{Token: "login:" + sentinel}); err == nil || res != nil {
		t.Fatalf("a non-2xx must fail closed even with resultCode Ok in the body, got res=%v err=%v", res, err)
	}
}

func TestSplitPair(t *testing.T) {
	a, b, err := splitPair("login:key", "p", "A", "B")
	if err != nil || a != "login" || b != "key" {
		t.Errorf(`splitPair("login:key") = %q,%q,%v`, a, b, err)
	}
	for _, in := range []string{"nokey", ":key", "login:", "", "   "} {
		if _, _, err := splitPair(in, "p", "A", "B"); err == nil {
			t.Errorf("splitPair(%q): want error", in)
		}
	}
	// The error names the shape, never the value.
	if _, _, err := splitPair(sentinel, "p", "A", "B"); err == nil || strings.Contains(err.Error(), sentinel) {
		t.Errorf("splitPair error must be present and value-free: %v", err)
	}
}
