package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// jwtWithOwner builds an UNSIGNED-looking JWT (header.payload.sig) whose payload
// carries the owner + isAdmin claims. The tool only DECODES these locally (the
// server verifies the signature), so a stub signature is fine for the unit test.
func jwtWithOwner(owner string, isAdmin bool) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"owner": owner, "isAdmin": isAdmin})
	body := base64.RawURLEncoding.EncodeToString(payload)
	return hdr + "." + body + ".sig"
}

func TestDecodeJWTOwner(t *testing.T) {
	owner, isAdmin, err := decodeJWTOwner(jwtWithOwner("hanzo", false))
	if err != nil || owner != "hanzo" || isAdmin {
		t.Fatalf("decodeJWTOwner = %q,%v,%v want hanzo,false,nil", owner, isAdmin, err)
	}
	if _, _, err := decodeJWTOwner("not-a-jwt"); err == nil {
		t.Fatal("decodeJWTOwner(non-jwt) should error so callers skip the local assertion")
	}
}

// loginDoer answers /v1/kms/auth/login with a JWT minted for a fixed owner.
type loginDoer struct {
	owner   string
	isAdmin bool
}

func (d loginDoer) Do(r *http.Request) (*http.Response, error) {
	body := `{"accessToken":"` + jwtWithOwner(d.owner, d.isAdmin) + `","expiresIn":3600,"tokenType":"Bearer"}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

// fixedCred returns a dummy credential for any target (the login doer ignores it).
func fixedCred(t Target) (credRef, error) {
	return credRef{ns: "hanzo", name: "cred", clientID: "cid", secret: "sec"}, nil
}

func TestTokenFunc_OwnerMustMatchOrg(t *testing.T) {
	// Credential mints owner=hanzo. A hanzo target passes; an acme target is refused
	// (misscoped credential — LOW-1 defense in depth, independent of the server guard).
	c := newKMSClient("http://cloud", loginDoer{owner: "hanzo"})
	tf := newTokenFunc(c, fixedCred, "dst")

	if _, err := tf(context.Background(), Target{Org: "hanzo"}); err != nil {
		t.Fatalf("hanzo target with hanzo-owner token: unexpected err %v", err)
	}
	_, err := tf(context.Background(), Target{Org: "acme"})
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("acme target with hanzo-owner token: err=%v, want an owner-mismatch refusal", err)
	}
}

func TestTokenFunc_AdminTokenRefused(t *testing.T) {
	// An admin-owner token must never be used for a fleet (tenant) target.
	c := newKMSClient("http://cloud", loginDoer{owner: "admin", isAdmin: true})
	tf := newTokenFunc(c, fixedCred, "dst")
	_, err := tf(context.Background(), Target{Org: "admin"})
	if err == nil || !strings.Contains(err.Error(), "ADMIN") {
		t.Fatalf("admin token: err=%v, want an admin refusal (fleet identity must be org-bound)", err)
	}
}
