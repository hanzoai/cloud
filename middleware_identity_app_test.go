package cloud

// The credential's KIND, stated by the only thing that can state it.
//
// An APPLICATION acting as itself — a client_credentials identity — carries an
// organization it cannot select: IAM mints such a token no membership set, so the
// org-switch admits nothing and the effective org is always the application's own
// owner. That makes it an org-scoped credential in the strongest sense available,
// and it is what CI presents at the build door.
//
// It is not an admin of anything, and must not become one: authz.Claims.OrgAdmin
// refuses every application by construction, because an app is issued for a purpose
// and not handed an org's self-service surface. So a door whose act IS a purpose
// needs a different question, and these tests pin the fact it asks — that the
// boundary mints it from validated claims alone, and that no other principal kind
// picks it up.

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// appToken signs a client_credentials token as IAM mints one: the client is the
// subject ("<org>/<app>"), the sole audience, and the authorized party, with the
// application's own organization in `owner` and no membership set.
func appToken(t *testing.T, key *rsa.PrivateKey, app, owner string) string {
	t.Helper()
	c := idClaims{Claims: authz.Claims{Owner: owner, Organization: owner, Azp: app}}
	c.Issuer = testIssuer
	c.Subject = owner + "/" + app
	c.Audience = jwt.ClaimStrings{app}
	c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
	c.IssuedAt = jwt.NewNumericDate(time.Now())
	return signWith(t, key, c)
}

// observed is what a handler behind the boundary sees about the caller.
type observed struct {
	org      string
	isApp    bool
	admin    bool
	orgAdmin bool
}

// kindProbe runs a request through SanitizeIdentity into a handler that reads the
// principal accessors every door reads.
func kindProbe(t *testing.T, v *identityValidator, mutate func(*http.Request)) observed {
	t.Helper()
	var got observed
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v))
	app.Get("/probe", func(c *zip.Ctx) error {
		got.org, _ = principal.Org(c)
		got.isApp = principal.IsApp(c)
		got.admin = principal.IsSuperAdmin(c)
		got.orgAdmin = principal.IsOrgAdmin(c)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if mutate != nil {
		mutate(req)
	}
	if _, err := app.Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return got
}

func appValidator(t *testing.T) (*identityValidator, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	return newIdentityValidator(testIssuer, jwks.URL, 0), key
}

// An application's own token resolves its organization AND says what it is — and
// holds neither admin scope, which is the whole reason the kind has to be stated
// separately from the role.
func TestAppPrincipalCarriesItsOrgAndNoAdminScope(t *testing.T) {
	v, key := appValidator(t)
	got := kindProbe(t, v, bearer(appToken(t, key, "hanzo-kms", "hanzo")))

	if got.org != "hanzo" {
		t.Fatalf("org=%q, want hanzo — an application's org is its owner", got.org)
	}
	if !got.isApp {
		t.Fatal("an application acting as itself was not recognised as one, so no door can tell it from an anonymous caller with an org")
	}
	if got.admin || got.orgAdmin {
		t.Fatalf("an application holds NO admin scope (super=%v org=%v)", got.admin, got.orgAdmin)
	}
}

// A PERSON is never an application, whatever else their token carries. This is the
// direction that matters: reading a human as an application would hand every member
// of an org whatever an org's own machine identity may do.
func TestHumanIsNeverAnApp(t *testing.T) {
	v, key := appValidator(t)
	for _, tc := range []struct {
		name string
		tok  string
	}{
		{"ordinary member", signWith(t, key, tokenClaims("hanzo-console", "acme", "joe@acme.io", false, time.Now().Add(time.Hour)))},
		{"org admin", signWith(t, key, tokenClaims("hanzo-console", "acme", "dave@acme.io", true, time.Now().Add(time.Hour)))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if kindProbe(t, v, bearer(tc.tok)).isApp {
				t.Fatal("a person read as an application")
			}
		})
	}
}

// An sk- API KEY is a PERSON'S credential, resolved to that person's IAM row, and
// is deliberately not an application: admitting it here would hand every member of
// an org whatever an org's own machine identity may do. It carries no membership
// set either — which is exactly why authz reads it as a machine and why the kind
// cannot be derived from an empty `orgs` claim.
func TestAPIKeyIsNotAnApp(t *testing.T) {
	v := keyValidator(t, "acme", "joe")
	got := kindProbe(t, v, bearer("sk-live-abc123"))

	if got.org != "acme" {
		t.Fatalf("org=%q, want acme — a key's org comes from the subject it resolved", got.org)
	}
	if got.isApp {
		t.Fatal("an API key read as an application — every member of the org inherits the machine identity's reach")
	}
}

// THE FORGERY. X-User-IsApp is authority: it is stripped on ingress and re-injected
// only from validated claims, so a client copy never survives — with no credential
// at all, and alongside a perfectly valid human token.
func TestForgedAppHeaderNeverSurvives(t *testing.T) {
	v, key := appValidator(t)
	human := signWith(t, key, tokenClaims("hanzo-console", "acme", "joe@acme.io", false, time.Now().Add(time.Hour)))

	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"no credential", func(r *http.Request) {
			r.Header.Set("X-User-IsApp", "true")
			r.Header.Set("X-Org-Id", "hanzo")
		}},
		{"riding a valid human token", func(r *http.Request) {
			bearer(human)(r)
			r.Header.Set("X-User-IsApp", "true")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if kindProbe(t, v, tc.mutate).isApp {
				t.Fatal("a client-sent X-User-IsApp survived the boundary")
			}
		})
	}
}
