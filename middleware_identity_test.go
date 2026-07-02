package cloud

// Tests for the in-binary identity trust boundary. They drive real requests
// through the zip/fiber stack against a real RSA-signed JWKS (httptest), so the
// whole path runs end-to-end: header strip -> token extract -> JWKS verify ->
// re-mint. The crux assertions: a raw X-User-IsAdmin never grants admin, and an
// ORG admin can never escalate to the GLOBAL-admin authority or another tenant.

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gojose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/hanzoai/zip"
)

const testIssuer = "https://test.iam"

// jwksServer serves a single-key JWKS (kid=test-key) for pub.
func jwksServer(t *testing.T, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	set := gojose.JSONWebKeySet{Keys: []gojose.JSONWebKey{{
		Key: pub, KeyID: "test-key", Algorithm: "RS256", Use: "sig",
	}}}
	body, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// signWith signs claims with key, stamping kid=test-key.
func signWith(t *testing.T, key *rsa.PrivateKey, c idClaims) string {
	t.Helper()
	signer, err := gojose.NewSigner(
		gojose.SigningKey{Algorithm: gojose.RS256, Key: key},
		(&gojose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	raw, err := jwt.Signed(signer).Claims(c).Serialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return raw
}

// tokenClaims builds an IAM-shaped claim set.
func tokenClaims(aud, owner, email string, isAdmin bool, exp time.Time) idClaims {
	return idClaims{
		Claims: jwt.Claims{
			Issuer:   testIssuer,
			Subject:  "u-" + owner,
			Audience: jwt.Audience{aud},
			Expiry:   jwt.NewNumericDate(exp),
			IssuedAt: jwt.NewNumericDate(time.Now()),
		},
		Owner:   owner,
		Email:   email,
		IsAdmin: isAdmin,
	}
}

// captured records the identity a downstream handler observes.
type captured struct {
	org, user string
	admin     bool
}

// newIdentityApp wires SanitizeIdentity (adminOrg="admin") in front of a probe
// handler that records the validated identity.
func newIdentityApp(t *testing.T, v *identityValidator) (*zip.App, *captured) {
	t.Helper()
	got := &captured{}
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v, "admin"))
	app.Get("/probe", func(c *zip.Ctx) error {
		got.org = c.Org()
		got.user = c.User()
		got.admin = c.IsAdmin()
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	return app, got
}

func probe(t *testing.T, app *zip.App, mutate func(*http.Request)) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if mutate != nil {
		mutate(req)
	}
	if _, err := app.Fiber().Test(req); err != nil {
		t.Fatalf("Test request: %v", err)
	}
}

func bearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func TestSanitizeIdentity(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey2: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, []string{"hanzo-console"}, 0)
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)

	globalAdmin := signWith(t, key, tokenClaims("hanzo-console", "admin", "z@hanzo.ai", true, future))
	orgAdmin := signWith(t, key, tokenClaims("hanzo-console", "acme", "dave@acme.io", true, future))
	normalUser := signWith(t, key, tokenClaims("hanzo-console", "acme", "joe@acme.io", false, future))
	expiredAdmin := signWith(t, key, tokenClaims("hanzo-console", "admin", "z@hanzo.ai", true, past))
	wrongKeyAdmin := signWith(t, otherKey, tokenClaims("hanzo-console", "admin", "z@hanzo.ai", true, future))
	wrongAudAdmin := signWith(t, key, tokenClaims("evil-app", "admin", "z@hanzo.ai", true, future))
	// Owners whose IAM name carries whitespace — the RED CRIT-2 residual vector.
	// The whitespace rides in the JWT `owner` claim (JSON-preserved, so it is
	// transport-independent, unlike a header which fasthttp OWS-trims), so a
	// fold-sibling org "acme " / "ac me" would previously collapse onto tenant
	// "acme". The middleware must refuse to grant tenancy from it.
	trailWSOwner := signWith(t, key, tokenClaims("hanzo-console", "acme ", "sneak@acme.io", false, future))
	internalWSOwner := signWith(t, key, tokenClaims("hanzo-console", "ac me", "sneak@acme.io", false, future))

	cases := []struct {
		name      string
		mutate    func(*http.Request)
		wantAdmin bool
		wantOrg   string
	}{
		{
			name:      "forged admin header alone grants nothing (THE P0)",
			mutate:    func(r *http.Request) { r.Header.Set("X-User-IsAdmin", "true") },
			wantAdmin: false,
			wantOrg:   "",
		},
		{
			name: "forged admin + forged org: admin dead, org passes through (Phase-1 data)",
			mutate: func(r *http.Request) {
				r.Header.Set("X-User-IsAdmin", "true")
				r.Header.Set("X-Org-Id", "victim")
			},
			wantAdmin: false,
			wantOrg:   "victim",
		},
		{
			name:      "valid GLOBAL admin bearer grants admin, pinned to admin org",
			mutate:    bearer(globalAdmin),
			wantAdmin: true,
			wantOrg:   "admin",
		},
		{
			name: "GLOBAL admin honors org switch",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+globalAdmin)
				r.Header.Set("X-Org-Id", "maxpower")
			},
			wantAdmin: true,
			wantOrg:   "maxpower",
		},
		{
			name: "ORG admin cannot escalate to global admin nor cross tenant",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+orgAdmin)
				r.Header.Set("X-Org-Id", "victim") // attempt to read another tenant
			},
			wantAdmin: false,  // isAdmin bool true, but owner != adminOrg
			wantOrg:   "acme", // pinned to their own org, never "victim"
		},
		{
			name: "normal user pinned to own org, ignores forged org",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+normalUser)
				r.Header.Set("X-Org-Id", "victim")
			},
			wantAdmin: false,
			wantOrg:   "acme",
		},
		{
			name: "expired admin token is anonymous",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+expiredAdmin)
				r.Header.Set("X-User-IsAdmin", "true")
			},
			wantAdmin: false,
			wantOrg:   "",
		},
		{
			name: "wrong-key (forged) admin token is anonymous",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+wrongKeyAdmin)
				r.Header.Set("X-User-IsAdmin", "true")
			},
			wantAdmin: false,
			wantOrg:   "",
		},
		{
			name:      "wrong-audience admin token is anonymous",
			mutate:    bearer(wrongAudAdmin),
			wantAdmin: false,
			wantOrg:   "",
		},
		{
			name: "opaque hk- API key is not a JWT; no admin",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer hk-deadbeef")
				r.Header.Set("X-User-IsAdmin", "true")
			},
			wantAdmin: false,
			wantOrg:   "",
		},
		{
			// RED CRIT-2 residual: a validated principal whose org name is a
			// whitespace fold-sibling of a real org grants NO tenancy — it must not
			// collapse onto tenant "acme".
			name:      "trailing-whitespace owner grants no org (injective boundary)",
			mutate:    bearer(trailWSOwner),
			wantAdmin: false,
			wantOrg:   "",
		},
		{
			name:      "internal-whitespace owner grants no org (injective boundary)",
			mutate:    bearer(internalWSOwner),
			wantAdmin: false,
			wantOrg:   "",
		},
		{
			// A global admin cannot org-switch INTO a whitespace-bearing org (the
			// switch target is refused); they fall back to their own admin org.
			name: "global admin org-switch to a whitespace org is refused",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+globalAdmin)
				r.Header.Set("X-Org-Id", "ac me") // internal space survives transport
			},
			wantAdmin: true,
			wantOrg:   "admin",
		},
		{
			name:      "GLOBAL admin via session cookie",
			mutate:    func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "access_token", Value: globalAdmin}) },
			wantAdmin: true,
			wantOrg:   "admin",
		},
		{
			name: "GLOBAL admin via HTTP Basic password (go/.netrc idiom)",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("z@hanzo.ai:"+globalAdmin)))
			},
			wantAdmin: true,
			wantOrg:   "admin",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, got := newIdentityApp(t, v)
			probe(t, app, tc.mutate)
			if got.admin != tc.wantAdmin {
				t.Errorf("IsAdmin() = %v, want %v", got.admin, tc.wantAdmin)
			}
			if got.org != tc.wantOrg {
				t.Errorf("Org() = %q, want %q", got.org, tc.wantOrg)
			}
		})
	}
}

// A nil validator (unconfigured) must still STRIP a forged admin header — the
// sanitizer never fails open to admin, even with no JWKS wired.
func TestSanitizeIdentity_NilValidatorStillStripsAdmin(t *testing.T) {
	app, got := newIdentityApp(t, nil)
	probe(t, app, func(r *http.Request) {
		r.Header.Set("X-User-IsAdmin", "true")
		r.Header.Set("X-Org-Id", "victim")
	})
	if got.admin {
		t.Fatal("nil validator must not honor a forged X-User-IsAdmin")
	}
	if got.org != "victim" {
		t.Errorf("Org() = %q, want %q (Phase-1 passthrough preserved)", got.org, "victim")
	}
}

// TestSanitizeIdentity_AnonPathHasNoUserId locks the invariant the s3 + provisioning
// data-plane fixes depend on (RED): on the no-principal path, SanitizeIdentity
// RESTORES a forged X-Org-Id (Phase-1 passthrough) but a forged X-User-Id does
// NOT survive — X-User-Id is in authorityHeaders and only re-set for a validated
// principal. So ctx.User()=="" is the reliable "no validated principal" signal
// those subsystems gate on. If a refactor ever restored X-User-Id here, the
// gate would silently reopen — this test fails first.
func TestSanitizeIdentity_AnonPathHasNoUserId(t *testing.T) {
	// nil validator = the no-principal path for ANY request (no JWKS wired), which
	// is the same header-restore branch a bad/absent bearer takes.
	app, got := newIdentityApp(t, nil)
	probe(t, app, func(r *http.Request) {
		r.Header.Set("X-Org-Id", "victim")
		r.Header.Set("X-User-Id", "forged-user") // client-supplied — must be stripped
	})
	if got.user != "" {
		t.Fatalf("User() = %q on the anon path, want \"\" — a client-forged X-User-Id survived, reopening the data-plane forge!", got.user)
	}
	if got.org != "victim" {
		t.Errorf("Org() = %q, want %q (Phase-1 org passthrough is intentional; the fix is that User() is empty)", got.org, "victim")
	}
}

// Focused validator unit tests: issuer, audience, expiry, signature, and a
// missing issuer are all enforced.
func TestIdentityValidator(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, []string{"hanzo-console"}, 0)
	future := time.Now().Add(time.Hour)

	t.Run("valid token", func(t *testing.T) {
		c, err := v.validate(signWith(t, key, tokenClaims("hanzo-console", "admin", "z@hanzo.ai", true, future)))
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		if c.Owner != "admin" || !c.IsAdmin || c.userID() != "u-admin" {
			t.Errorf("claims = %+v, want owner=admin isAdmin=true sub=u-admin", c)
		}
	})
	t.Run("wrong issuer rejected", func(t *testing.T) {
		c := tokenClaims("hanzo-console", "admin", "", true, future)
		c.Issuer = "https://evil.iam"
		if _, err := v.validate(signWith(t, key, c)); err == nil {
			t.Fatal("wrong issuer must be rejected")
		}
	})
	t.Run("missing issuer rejected", func(t *testing.T) {
		c := tokenClaims("hanzo-console", "admin", "", true, future)
		c.Issuer = ""
		if _, err := v.validate(signWith(t, key, c)); err == nil {
			t.Fatal("missing issuer must be rejected")
		}
	})
	t.Run("wrong audience rejected", func(t *testing.T) {
		if _, err := v.validate(signWith(t, key, tokenClaims("evil-app", "admin", "", true, future))); err == nil {
			t.Fatal("wrong audience must be rejected")
		}
	})
	t.Run("expired rejected", func(t *testing.T) {
		if _, err := v.validate(signWith(t, key, tokenClaims("hanzo-console", "admin", "", true, time.Now().Add(-time.Hour)))); err == nil {
			t.Fatal("expired token must be rejected")
		}
	})
	t.Run("missing expiry rejected", func(t *testing.T) {
		// go-jose only enforces exp when present; a token with NO exp would never
		// expire. We reject it explicitly.
		c := tokenClaims("hanzo-console", "admin", "", true, future)
		c.Expiry = nil
		if _, err := v.validate(signWith(t, key, c)); err == nil {
			t.Fatal("token without exp must be rejected")
		}
	})
	t.Run("bad signature rejected", func(t *testing.T) {
		if _, err := v.validate(signWith(t, other, tokenClaims("hanzo-console", "admin", "", true, future))); err == nil {
			t.Fatal("token signed by an unknown key must be rejected")
		}
	})
}
