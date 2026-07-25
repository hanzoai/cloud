package integrations

// chrome_test.go proves the Chrome (browser-extension pairing) connector against
// the REAL plane (real store, real KMS seal/open): it registers on the ORG
// /v1/integrations surface, verify-before-store holds, the pairing token seals to
// the org's KMS namespace and never leaks, tenant isolation holds, and the
// org-admin gate is enforced — the same bar social_keys_test.go / cloudflare_test.go
// set. Unlike those, Chrome has NO remote verify endpoint (the extension bridge is
// local), so the checks are structural + custody, not an httptest mock.

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"
)

// chromePairToken is a well-formed pairing token (base64url/JWT charset) that
// chromeVerify accepts. It is a test fixture, not a real credential.
const chromePairToken = "hzb.eyJ0IjoicGFpciJ9.Zm9vYmFyYmF6cXV4LS_deadbeef01234567"

func chromeConnect(t *testing.T, app *zip.App, org string, admin bool, token string) httpResult {
	t.Helper()
	return cfReq(t, app, http.MethodPost, "/v1/integrations/chrome/connect", org, admin,
		map[string]any{"token": token})
}

// TestChromeListedOnIntegrations asserts Chrome is a real card on the org-plane
// /v1/integrations surface both hanzo.app and the console render.
func TestChromeListedOnIntegrations(t *testing.T) {
	app := newApp(t, newKMS(t))
	res := cfReq(t, app, http.MethodGet, "/v1/integrations/chrome", "acme", false, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("GET chrome want 200, got %d (%s)", res.Code, res.Body)
	}
	// A card with available=true (the local-pairing path is always configured).
	for _, want := range [][]byte{[]byte(`"id":"chrome"`), []byte(`"name":"Chrome"`), []byte(`"available":true`)} {
		if !bytes.Contains(res.Body, want) {
			t.Fatalf("chrome card missing %s in %s", want, res.Body)
		}
	}
}

// TestChromeConnectSealsAndIsolates: a well-formed pairing token verifies, seals to
// the org's KMS namespace, and never leaks into the response; another org has
// nothing and the token seam refuses it.
func TestChromeConnectSealsAndIsolates(t *testing.T) {
	kc := newKMS(t)
	app := newApp(t, kc)

	res := chromeConnect(t, app, "acme", true, chromePairToken)
	if res.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", res.Code, res.Body)
	}
	if bytes.Contains(res.Body, []byte(chromePairToken)) {
		t.Fatal("the pairing token leaked into the connect response")
	}
	got, ok := kmsSecret(t, kc, "acme", "chrome", apiKeySecret)
	if !ok || string(got) != chromePairToken {
		t.Fatalf("token must seal in acme's namespace, got %q ok=%v", got, ok)
	}
	// The connection row carries a non-secret device id, never the token.
	conn, ok := ConnectionFor("acme", "chrome")
	if !ok || conn.AccountLabel != "Hanzo Browser Extension" {
		t.Fatalf("connection metadata missing/wrong: %+v ok=%v", conn, ok)
	}
	if bytes.Contains([]byte(conn.ExternalID+conn.AccountLabel), []byte(chromePairToken)) {
		t.Fatal("token leaked into the connection row metadata")
	}
	// Tenant isolation.
	if _, ok := kmsSecret(t, kc, "orgb", "chrome", apiKeySecret); ok {
		t.Fatal("orgb must not have a credential")
	}
	if _, err := TokenFor(context.Background(), "orgb", "chrome", apiKeySecret); err == nil {
		t.Fatal("TokenFor must refuse a non-connected org")
	}
}

// TestChromeRejectsMalformedTokenAndStoresNothing: a token with an illegal char is
// refused offline and NOTHING is sealed (verify-before-store).
func TestChromeRejectsMalformedTokenAndStoresNothing(t *testing.T) {
	kc := newKMS(t)
	app := newApp(t, kc)
	res := chromeConnect(t, app, "acme", true, "not a valid pairing token!!")
	if res.Code != http.StatusBadRequest {
		t.Fatalf("malformed token want 400, got %d (%s)", res.Code, res.Body)
	}
	if _, ok := kmsSecret(t, kc, "acme", "chrome", apiKeySecret); ok {
		t.Fatal("verify-before-store violated: a malformed token was sealed")
	}
	if _, ok := ConnectionFor("acme", "chrome"); ok {
		t.Fatal("verify-before-store violated: a malformed token created a row")
	}
}

// TestChromeRequiresOrgAdmin: pairing a browser-automation credential is an
// org-admin action; a plain member is refused before any store.
func TestChromeRequiresOrgAdmin(t *testing.T) {
	kc := newKMS(t)
	app := newApp(t, kc)
	res := chromeConnect(t, app, "acme", false, chromePairToken)
	if res.Code != http.StatusForbidden {
		t.Fatalf("member connect want 403, got %d (%s)", res.Code, res.Body)
	}
	if _, ok := kmsSecret(t, kc, "acme", "chrome", apiKeySecret); ok {
		t.Fatal("a non-admin connect must not reach KMS")
	}
}
