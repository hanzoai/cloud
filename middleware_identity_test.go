package cloud

// Tests for the in-binary identity trust boundary. They drive real requests
// through the zip/fiber stack against a real RSA-signed JWKS (httptest), so the
// whole path runs end-to-end: header strip -> token extract -> JWKS verify ->
// re-mint. The crux assertions: a raw X-User-IsAdmin never grants admin, and an
// ORG admin can never escalate to the SuperAdmin authority or another org.

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
	model "github.com/hanzoai/iam/pkg/model"
	"github.com/zap-proto/zip"
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

// tokenClaims builds an IAM-shaped claim set for the ORDINARY case: the minting
// app's org and the user's own org AGREE, so `owner` and orgs[0] carry the same
// value.
//
// The `orgs` seed is not decoration — it is what makes this fixture resemble a real
// token. IAM has minted the claim since v1.33.0, and cloud reads the USER's org from
// it (idClaims.homeOrg), never from `owner`, which carries the APPLICATION's org.
// Every fixture here previously set `owner` alone, so the whole suite exercised a
// token shape production no longer emits — and, because the two values were always
// equal in the fixtures, no assertion could distinguish them. That is exactly how a
// caller-selectable tenant and a caller-selectable SuperAdmin gate survived.
//
// Tests that need the two to DISAGREE (the cross-app cases in
// middleware_identity_homeorg_test.go) override Orgs explicitly, as does any test
// modelling a pre-claim legacy token by clearing it.
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
		Orgs:    []model.OrgRef{{Org: owner, Role: "member"}},
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
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)

	superAdmin := signWith(t, key, tokenClaims("hanzo-console", "admin", "z@hanzo.ai", true, future))
	orgAdmin := signWith(t, key, tokenClaims("hanzo-console", "acme", "dave@acme.io", true, future))
	normalUser := signWith(t, key, tokenClaims("hanzo-console", "acme", "joe@acme.io", false, future))
	expiredAdmin := signWith(t, key, tokenClaims("hanzo-console", "admin", "z@hanzo.ai", true, past))
	wrongKeyAdmin := signWith(t, otherKey, tokenClaims("hanzo-console", "admin", "z@hanzo.ai", true, future))
	arbitraryAudAdmin := signWith(t, key, tokenClaims("some-other-first-party-app", "admin", "z@hanzo.ai", true, future))
	// M1: a GENERIC admin-org MACHINE token — type=="application", ordinary app aud
	// (NOT -platform-kms), isAdmin=true. It must be DENIED SuperAdmin (type is the
	// discriminator once aud is no longer a gate).
	adminMachine := tokenClaims("some-admin-app", "admin", "z@hanzo.ai", true, future)
	adminMachine.Type = "application"
	adminMachineTok := signWith(t, key, adminMachine)
	// Owners whose IAM name carries whitespace — the RED CRIT-2 residual vector.
	// The whitespace rides in the JWT `owner` claim (JSON-preserved, so it is
	// transport-independent, unlike a header which fasthttp OWS-trims), so a
	// fold-sibling org "acme " / "ac me" would previously collapse onto org
	// "acme". The middleware must refuse to grant org-scoping from it.
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
			name:      "valid SuperAdmin bearer grants admin, pinned to admin org",
			mutate:    bearer(superAdmin),
			wantAdmin: true,
			wantOrg:   "admin",
		},
		{
			name: "SuperAdmin honors org switch",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+superAdmin)
				r.Header.Set("X-Org-Id", "maxpower")
			},
			wantAdmin: true,
			wantOrg:   "maxpower",
		},
		{
			name: "ORG admin cannot escalate to SuperAdmin nor cross org",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+orgAdmin)
				r.Header.Set("X-Org-Id", "victim") // attempt to read another org
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
			// Audience is not an access gate: an admin-org token validates whatever app
			// minted it (aud is informational), so a real admin gets SuperAdmin from ANY
			// first-party app — owner==adminOrg is the authority, not the aud. The only
			// admin-org token DENIED SuperAdmin is a KMS-sync machine principal (next case).
			name:      "admin token with an arbitrary audience still gets SuperAdmin",
			mutate:    bearer(arbitraryAudAdmin),
			wantAdmin: true,
			wantOrg:   "admin",
		},
		{
			// A KMS-sync MACHINE principal (aud=<owner>-platform-kms) in the admin org
			// with isAdmin=true VALIDATES like any token but is DENIED SuperAdmin —
			// isKMSMachinePrincipal gates it out, pinned to its own org, never cross-org.
			// A client_credentials machine identity must never wield platform-admin.
			name:      "admin-org KMS-machine principal is denied SuperAdmin",
			mutate:    bearer(signWith(t, key, tokenClaims("admin-platform-kms", "admin", "z@hanzo.ai", true, future))),
			wantAdmin: false,
			wantOrg:   "admin",
		},
		{
			// M1 (red): a GENERIC admin-org machine app (type=="application", ordinary
			// aud, isAdmin=true) is DENIED SuperAdmin — isMachinePrincipal catches it via
			// `type`, not just the -platform-kms audience. It falls through org-scoped.
			name:      "admin-org application-type token is denied SuperAdmin (M1)",
			mutate:    bearer(adminMachineTok),
			wantAdmin: false,
			wantOrg:   "admin",
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
			// whitespace fold-sibling of a real org grants NO org-scoping — it must not
			// collapse onto org "acme".
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
			// A SuperAdmin cannot org-switch INTO a whitespace-bearing org (the
			// switch target is refused); they fall back to their own admin org.
			name: "SuperAdmin org-switch to a whitespace org is refused",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+superAdmin)
				r.Header.Set("X-Org-Id", "ac me") // internal space survives transport
			},
			wantAdmin: true,
			wantOrg:   "admin",
		},
		{
			name:      "SuperAdmin via session cookie",
			mutate:    func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "access_token", Value: superAdmin}) },
			wantAdmin: true,
			wantOrg:   "admin",
		},
		{
			name: "SuperAdmin via HTTP Basic password (go/.netrc idiom)",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("z@hanzo.ai:"+superAdmin)))
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

// TestSanitizeIdentity_OrgAdminHeader locks the X-User-IsOrgAdmin signal GuardScoped gates
// on. The boundary mints it for ANY validated isAdmin principal — a SuperAdmin (admin of
// the admin org) AND an ORG admin (admin of their own org) — but NEVER for a validated
// non-admin member, NEVER for a KMS-sync machine principal, and it is UNFORGEABLE: like
// every authority header it is stripped on ingress and re-injected only from validated
// claims, so a client-sent copy never survives (mirrors the X-User-IsAdmin strip). This
// is what closes the same-tenant over-visibility gap: a plain member gets no bit, so
// GuardScoped refuses it.
func TestSanitizeIdentity_OrgAdminHeader(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	future := time.Now().Add(time.Hour)

	var gotAdmin, gotOrgAdmin, gotOrg string
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v, "admin"))
	app.Get("/probe", func(cx *zip.Ctx) error {
		gotAdmin = cx.Header("X-User-IsAdmin")
		gotOrgAdmin = cx.Header("X-User-IsOrgAdmin")
		gotOrg = cx.Org()
		return cx.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	cases := []struct {
		name                             string
		mutate                           func(*http.Request)
		wantAdmin, wantOrgAdmin, wantOrg string
	}{
		{
			// An ORG admin (isAdmin=true, owner != adminOrg) gets the org-admin bit but
			// NEVER the SuperAdmin bit — it can reach its OWN org's scoped panels, never
			// the cross-tenant platform surface.
			name:         "org admin gets org-admin bit, not SuperAdmin",
			mutate:       bearer(signWith(t, key, tokenClaims("hanzo-console", "acme", "dave@acme.io", true, future))),
			wantAdmin:    "",
			wantOrgAdmin: "true",
			wantOrg:      "acme",
		},
		{
			// The gap-closing case: a validated NON-admin member of an org gets NEITHER
			// bit, so GuardScoped refuses it from the org-scoped admin panels.
			name:         "validated non-admin member gets neither bit",
			mutate:       bearer(signWith(t, key, tokenClaims("hanzo-console", "acme", "joe@acme.io", false, future))),
			wantAdmin:    "",
			wantOrgAdmin: "",
			wantOrg:      "acme",
		},
		{
			// A SuperAdmin is also an org admin of its own (admin) org — it gets BOTH.
			name:         "SuperAdmin gets both bits",
			mutate:       bearer(signWith(t, key, tokenClaims("hanzo-console", "admin", "z@hanzo.ai", true, future))),
			wantAdmin:    "true",
			wantOrgAdmin: "true",
			wantOrg:      "admin",
		},
		{
			// A KMS-sync MACHINE principal in the admin org validates (V6 accepts the
			// machine aud) but must get NEITHER global NOR org admin — the machine path
			// stays decoupled from admin, so the audience widening is never an admin bypass.
			name:         "admin-org machine principal gets neither bit",
			mutate:       bearer(signWith(t, key, tokenClaims("admin-platform-kms", "admin", "z@hanzo.ai", true, future))),
			wantAdmin:    "",
			wantOrgAdmin: "",
			wantOrg:      "admin",
		},
		{
			// Forge-resistance (bearer path): a client-sent X-User-IsOrgAdmin riding a
			// valid NON-admin bearer is stripped on ingress and never re-injected.
			name: "forged org-admin header on a non-admin bearer is stripped",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+signWith(t, key, tokenClaims("hanzo-console", "acme", "joe@acme.io", false, future)))
				r.Header.Set("X-User-IsOrgAdmin", "true") // forged — must not survive
			},
			wantAdmin:    "",
			wantOrgAdmin: "",
			wantOrg:      "acme",
		},
		{
			// Forge-resistance (anonymous path): a raw X-User-IsOrgAdmin with no credential
			// is stripped, mirroring the X-User-IsAdmin P0 strip. The Phase-1 org passthrough
			// still restores X-Org-Id, but it carries NO org-admin authority.
			name: "forged org-admin header on anonymous request is stripped",
			mutate: func(r *http.Request) {
				r.Header.Set("X-User-IsOrgAdmin", "true")
				r.Header.Set("X-Org-Id", "victim")
			},
			wantAdmin:    "",
			wantOrgAdmin: "",
			wantOrg:      "victim",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAdmin, gotOrgAdmin, gotOrg = "", "", ""
			probe(t, app, tc.mutate)
			if gotOrgAdmin != tc.wantOrgAdmin {
				t.Errorf("X-User-IsOrgAdmin = %q, want %q", gotOrgAdmin, tc.wantOrgAdmin)
			}
			if gotAdmin != tc.wantAdmin {
				t.Errorf("X-User-IsAdmin = %q, want %q", gotAdmin, tc.wantAdmin)
			}
			if gotOrg != tc.wantOrg {
				t.Errorf("Org() = %q, want %q", gotOrg, tc.wantOrg)
			}
		})
	}
}

// A nil validator (unconfigured) must still STRIP a forged admin header — the
// sanitizer never fails open to admin, even with no JWKS wired.
// TestSanitizeIdentity_StampsUserName proves the validated IAM username is stamped
// as X-User-Name, DISTINCT from X-User-Id (the JWT subject). The direct-Bearer hk-
// mint depends on this split: IAM's user-key ops parse <owner>/<username>, while
// X-User-Id is a UUID subject that fails that lookup. Both must be present and
// distinct on the direct path.
func TestSanitizeIdentity_StampsUserName(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)

	c := tokenClaims("hanzo-console", "hanzo", "z@hanzo.ai", false, time.Now().Add(time.Hour))
	c.Subject = "2d4d67ab-30f1-474e-b81f-f60461852259" // the JWT subject: a UUID
	c.Name = "z"                                       // the IAM username
	tok := signWith(t, key, c)

	var gotName, gotID, gotOrg string
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v, "admin"))
	app.Get("/probe", func(cx *zip.Ctx) error {
		gotName = cx.Header("X-User-Name")
		gotID = cx.User()
		gotOrg = cx.Org()
		return cx.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	probe(t, app, bearer(tok))

	if gotID != "2d4d67ab-30f1-474e-b81f-f60461852259" {
		t.Fatalf("X-User-Id: want the subject UUID, got %q", gotID)
	}
	if gotName != "z" {
		t.Fatalf("X-User-Name: want the IAM username \"z\", got %q", gotName)
	}
	if gotOrg != "hanzo" {
		t.Fatalf("X-Org-Id: want owner \"hanzo\", got %q", gotOrg)
	}
}

// TestSanitizeIdentity_UserNameForgeryStripped proves a client-forged X-User-Name is
// deleted on ingress and only the validated claim's username survives — it is an
// authority header, not client input.
func TestSanitizeIdentity_UserNameForgeryStripped(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)

	c := tokenClaims("hanzo-console", "hanzo", "z@hanzo.ai", false, time.Now().Add(time.Hour))
	c.Name = "z"
	tok := signWith(t, key, c)

	var gotName string
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v, "admin"))
	app.Get("/probe", func(cx *zip.Ctx) error {
		gotName = cx.Header("X-User-Name")
		return cx.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	probe(t, app, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("X-User-Name", "victim-admin") // forged — must be overwritten
	})
	if gotName != "z" {
		t.Fatalf("X-User-Name: forged value survived (got %q) — want the validated username \"z\"", gotName)
	}
}

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
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
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

// TestSuperAdminGate_RequiresAdminOrgAndIsAdmin locks the exact SuperAdmin
// invariant the cloud admin surfaces (incl. /v1/admin/treasury/*) gate on:
//
//	SuperAdmin  ⟺  validated principal with (owner == adminOrg) AND isAdmin
//
// adminOrg is DEPLOYMENT config (IAM_ADMIN_ORG): the operator pins it to the org
// its platform operators actually live in. Hanzo pins it to "hanzo" — the org the
// sole operator, founder z@hanzo.ai, is minted into as owner=hanzo, isAdmin=true
// (task #51). This test pins it to "admin" hermetically; the CODE it exercises is
// identical for any adminOrg value, so it proves the invariant, not the choice.
//
// It drives real JWKS-validated bearer tokens through the SAME SanitizeIdentity
// boundary and reads the re-minted c.IsAdmin() a downstream admin gate sees. The
// two security properties it locks — independent of which org adminOrg names:
//   - isAdmin is REQUIRED: a NON-admin sitting in the admin org gets nothing, so
//     the gate is never "owner == adminOrg" alone.
//   - owner is REQUIRED: an admin of ANY OTHER org gets nothing, so an org-admin of
//     one org can never reach the global (all-orgs) admin surface — only the
//     operator org's admins do. This is what keeps SuperAdmin the operator's, and
//     what "isAdmin required" keeps from being every operator-org user.
//
// TestSuperAdminGate_IsAdminOrgMembership locks THE ONE predicate:
// SuperAdmin ⟺ the principal's org IS the reserved admin org (owner == adminOrg).
// The same equality IAM's canonical User.IsSuperAdmin() uses — cloud adds no second
// signal. The `admin` org exists to hold ONLY SuperAdmins (a SuperAdmin is PROVISIONED
// into it, never promoted), so membership IS the fact; the per-user isAdmin bit is the
// ORTHOGONAL "admin of my own org" scope (X-User-IsOrgAdmin), never a super gate.
// (A KMS machine principal is excluded separately — see TestKMSMachine*.)
func TestSuperAdminGate_IsAdminOrgMembership(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	app, got := newIdentityApp(t, v) // adminOrg = "admin"
	future := time.Now().Add(time.Hour)

	cases := []struct {
		name      string
		owner     string
		email     string
		isAdmin   bool
		wantAdmin bool
	}{
		// A member of the configured admin org IS the SuperAdmin (in prod: z@hanzo.ai).
		{"member of the admin org is SuperAdmin", "admin", "z@hanzo.ai", true, true},
		// Membership ALONE decides — the isAdmin bit is NOT consulted for super. A user
		// in the admin org is a SuperAdmin because the admin org holds only SuperAdmins
		// (provisioned in, never promoted). ONE predicate, no second signal.
		{"member of the admin org is SuperAdmin without the isAdmin bit", "admin", "ops@hanzo.ai", false, true},
		// An ADMIN of a DIFFERENT org is NOT a SuperAdmin: proves owner==adminOrg is the
		// REQUIRED fact — an org-admin of one org never reaches the all-orgs surface.
		// (Their isAdmin bit makes them an ORG admin — the orthogonal scope.)
		{"admin of another org is not SuperAdmin", "globex", "boss@globex.io", true, false},
		// A normal user of another org: not elevated.
		{"normal user of another org is not SuperAdmin", "globex", "joe@globex.io", false, false},
		// A cross-org admin of yet another org: not elevated.
		{"cross-org admin is not SuperAdmin", "acme", "dave@acme.io", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			*got = captured{}
			tok := signWith(t, key, tokenClaims("hanzo-console", tc.owner, tc.email, tc.isAdmin, future))
			probe(t, app, bearer(tok))
			if got.admin != tc.wantAdmin {
				t.Errorf("SuperAdmin for owner=%q isAdmin=%v: got %v, want %v",
					tc.owner, tc.isAdmin, got.admin, tc.wantAdmin)
			}
		})
	}
}
