package iam

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// token builds a JWT-shaped string carrying claims. Only the payload matters here:
// Exchange reads the identity off a token the issuer has just handed it, and every
// service that receives that token verifies it in full for itself.
func token(claims map[string]any) string {
	body, _ := json.Marshal(claims)
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(body) + ".sig"
}

// The exchange asks for the caller's OWN subject and for a life no longer than
// what it is being handed to, and it reads the identity off what comes back.
func TestExchangeCarriesTheSubjectAndTheLifeAsked(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": token(map[string]any{
				"owner": "acme", "name": "kim", "email": "kim@acme.test", "displayName": "Kim",
			}),
			"expires_in": 900,
			"token_type": "Bearer",
		})
	}))
	defer srv.Close()

	s, err := Exchange(context.Background(), srv.URL, "hanzo-console", "shh", "caller.token.here", 15*time.Minute)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if got := form.Get("grant_type"); got != exchangeGrant {
		t.Errorf("grant_type = %q, want %q", got, exchangeGrant)
	}
	if got := form.Get("subject_token"); got != "caller.token.here" {
		t.Errorf("subject_token = %q — the caller's own token IS the authorization", got)
	}
	if got := form.Get("lifetime"); got != "900" {
		t.Errorf("lifetime = %q, want 900", got)
	}
	if s.Owner != "acme" || s.User != "kim" || s.Email != "kim@acme.test" || s.Display != "Kim" {
		t.Errorf("identity = %+v, want the claims the issuer stamped", s)
	}
	if d := time.Until(s.Expiry); d > 15*time.Minute+time.Second || d < 14*time.Minute {
		t.Errorf("expiry in %v, want the 900s IAM answered", d)
	}
}

// A refusal yields NO token and names the reason without echoing anything that was
// minted — the body of this call carries a credential.
func TestExchangeRefusalYieldsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"unauthorized_client","access_token":"leaked.if.read"}`))
	}))
	defer srv.Close()

	s, err := Exchange(context.Background(), srv.URL, "id", "secret", "caller.token", time.Hour)
	if err == nil {
		t.Fatal("a refused exchange returned no error")
	}
	if s.Token != "" {
		t.Fatal("a refused exchange returned a token")
	}
	if !strings.Contains(err.Error(), "unauthorized_client") {
		t.Errorf("error = %v, want IAM's own reason", err)
	}
	if strings.Contains(err.Error(), "leaked.if.read") {
		t.Errorf("the error echoes the body: %v", err)
	}
}

// Nothing is exchanged without a client credential or without a subject, and the
// refusal is local — no request is made at all.
func TestExchangeNeedsBothAClientAndASubject(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	for _, c := range []struct{ id, secret, subject string }{
		{"", "secret", "tok"},
		{"id", "", "tok"},
		{"id", "secret", ""},
		{"id", "secret", "   "},
	} {
		if _, err := Exchange(context.Background(), srv.URL, c.id, c.secret, c.subject, time.Hour); err == nil {
			t.Errorf("%+v was exchanged", c)
		}
	}
	if called {
		t.Error("an incomplete exchange still reached IAM")
	}
}

// A token whose payload says nothing about the user yields an empty identity
// rather than a guess. The credential is still usable; only the display values are
// absent, and the caller writes nothing it was not told.
func TestExchangeInventsNoIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "not-a-jwt", "expires_in": 60})
	}))
	defer srv.Close()

	s, err := Exchange(context.Background(), srv.URL, "id", "secret", "caller.token", time.Minute)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if s.Owner != "" || s.User != "" || s.Email != "" || s.Display != "" {
		t.Errorf("identity = %+v, want nothing invented", s)
	}
	if s.Token != "not-a-jwt" {
		t.Errorf("token = %q, want what IAM answered", s.Token)
	}
}
