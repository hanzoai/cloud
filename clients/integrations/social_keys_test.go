package integrations

// social_keys_test.go proves the security properties of the two ORG apikey/token
// connectors (warpcast/Farcaster + whatsapp) against the REAL plane (real store, real
// KMS seal/open): verify-before-store, KMS custody round-trip, tenant isolation, the
// org-admin gate, and a token-free response — the same bar cloudflare_test.go sets.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	warpcastKey  = "neynar-key-SECRET-deadbeef01234567"
	whatsappTok  = "EAAG-waba-system-user-token-SECRET-0123456789"
	whatsappPhon = "109876543210987"
)

// newNeynarMock stands in for Neynar's Farcaster API: 200 iff the x-api-key header
// carries the expected key, else 401.
func newNeynarMock(t *testing.T, valid bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !valid || r.Header.Get("x-api-key") != warpcastKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users":[{"fid":3,"username":"acme"}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("NEYNAR_API_BASE", srv.URL)
}

func TestWarpcastConnectSealsAndIsolates(t *testing.T) {
	newNeynarMock(t, true)
	kc := newKMS(t)
	app := newApp(t, kc)

	res := cfReq(t, app, http.MethodPost, "/v1/connectors/warpcast/connect", "acme", true, map[string]any{"token": warpcastKey})
	if res.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", res.Code, res.Body)
	}
	if bytes.Contains(res.Body, []byte(warpcastKey)) {
		t.Fatal("the api key leaked into the connect response")
	}
	got, ok := kmsSecret(t, kc, "acme", "warpcast", apiKeySecret)
	if !ok || string(got) != warpcastKey {
		t.Fatalf("key must seal in acme's namespace, got %q ok=%v", got, ok)
	}
	// Isolation: org B has nothing and the seam refuses it.
	if _, ok := kmsSecret(t, kc, "orgb", "warpcast", apiKeySecret); ok {
		t.Fatal("orgb must not have a credential")
	}
	if _, err := TokenFor(context.Background(), "orgb", "warpcast", apiKeySecret); err == nil {
		t.Fatal("TokenFor must refuse a non-connected org")
	}
}

func TestWarpcastRejectsInvalidKeyAndStoresNothing(t *testing.T) {
	newNeynarMock(t, false)
	kc := newKMS(t)
	app := newApp(t, kc)
	res := cfReq(t, app, http.MethodPost, "/v1/connectors/warpcast/connect", "acme", true, map[string]any{"token": warpcastKey})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("invalid key want 400, got %d (%s)", res.Code, res.Body)
	}
	if _, ok := kmsSecret(t, kc, "acme", "warpcast", apiKeySecret); ok {
		t.Fatal("verify-before-store violated: an invalid key was sealed")
	}
}

func TestWarpcastRequiresOrgAdmin(t *testing.T) {
	newNeynarMock(t, true)
	app := newApp(t, newKMS(t))
	res := cfReq(t, app, http.MethodPost, "/v1/connectors/warpcast/connect", "acme", false, map[string]any{"token": warpcastKey})
	if res.Code != http.StatusForbidden {
		t.Fatalf("member connect want 403, got %d (%s)", res.Code, res.Body)
	}
}

// newWhatsappMock stands in for the WhatsApp Cloud API (Graph): 200 iff the Bearer
// token matches and the path addresses the phone number id, else 401.
func newWhatsappMock(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+whatsappTok || !strings.Contains(r.URL.Path, whatsappPhon) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"verified_name":"Acme Inc","display_phone_number":"+1 555 0100","id":"` + whatsappPhon + `"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WHATSAPP_API_BASE", srv.URL)
}

func TestWhatsappConnectSealsWithPhoneID(t *testing.T) {
	newWhatsappMock(t)
	kc := newKMS(t)
	app := newApp(t, kc)

	res := cfReq(t, app, http.MethodPost, "/v1/connectors/whatsapp/connect", "acme", true,
		map[string]any{"token": whatsappTok, "accountId": whatsappPhon})
	if res.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", res.Code, res.Body)
	}
	if bytes.Contains(res.Body, []byte(whatsappTok)) {
		t.Fatal("the token leaked into the connect response")
	}
	got, ok := kmsSecret(t, kc, "acme", "whatsapp", apiKeySecret)
	if !ok || string(got) != whatsappTok {
		t.Fatalf("token must seal, got %q ok=%v", got, ok)
	}
	// The phone number id is echoed as the connection ExternalID (never the token).
	conn, ok := ConnectionFor("acme", "whatsapp")
	if !ok || conn.ExternalID != whatsappPhon {
		t.Fatalf("connection ExternalID must be the phone id, got %+v ok=%v", conn, ok)
	}
}

func TestWhatsappRejectsWrongToken(t *testing.T) {
	newWhatsappMock(t)
	kc := newKMS(t)
	app := newApp(t, kc)
	res := cfReq(t, app, http.MethodPost, "/v1/connectors/whatsapp/connect", "acme", true,
		map[string]any{"token": "wrong-token-xxxxxxxxxxxxxxxx", "accountId": whatsappPhon})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("wrong token want 400, got %d (%s)", res.Code, res.Body)
	}
	if _, ok := kmsSecret(t, kc, "acme", "whatsapp", apiKeySecret); ok {
		t.Fatal("verify-before-store violated: a rejected token was sealed")
	}
}
