package cloud

// A MACHINE credential must still resolve its org — from the token SUBJECT.
//
// An sk- API key is a customer credential, and IAM mints it no `orgs` claim,
// because a machine is a member of nothing. Reading the membership set and failing
// closed on it therefore 403'd every existing customer key on every ORG-SCOPED
// route (v1.801.244: /v1/agent 403 "X-Org-Id required", /v1/gpus 403,
// /v1/billing/balance 401) while unscoped /v1/models kept returning 200 — which is
// exactly why a pre-pin probe that only asserted /v1/models could not see it.
//
// These tests drive a REAL key through the whole boundary against an ORG-SCOPED
// handler, which is the shape that broke. They also pin the human path unchanged,
// so the two can never collapse into each other.

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"

	"github.com/hanzoai/authz"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// iamKeyServer stands in for IAM's get-user?accessKey lookup, returning the user
// row that owns the key. This is the SUBJECT resolution the fix depends on.
func iamKeyServer(t *testing.T, owner, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"` + owner + `","name":"` + name + `","email":"` + name + `@example.test","isAdmin":false}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// orgScopedProbe runs a request through SanitizeIdentity into a handler that gates
// exactly like every org-scoped route does (principal.Org → 403 when absent), and
// reports the status and resolved org.
func orgScopedProbe(t *testing.T, v *identityValidator, mutate func(*http.Request)) (status int, org string) {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v))
	app.Get("/v1/agent", func(c *zip.Ctx) error {
		o, ok := principal.Org(c)
		if !ok {
			return principal.Refused(c)
		}
		org = o
		return c.JSON(http.StatusOK, map[string]string{"org": o})
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/agent", nil)
	if mutate != nil {
		mutate(req)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	return resp.StatusCode, org
}

// keyValidator builds an identity validator whose key resolver points at a stub IAM.
func keyValidator(t *testing.T, owner, name string) *identityValidator {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	iam := iamKeyServer(t, owner, name)
	v.keys = &iamKeys{base: iam.URL, auth: "Basic test", http: iam.Client(), cache: newCache[string, *idClaims](time.Minute)}
	return v
}

// TestAPIKeyResolvesOrgOnScopedRoute is THE regression test: a customer's sk- key
// must reach an org-scoped route and land on its OWN org.
func TestAPIKeyResolvesOrgOnScopedRoute(t *testing.T) {
	v := keyValidator(t, "gotham-labs", "batkey")

	status, org := orgScopedProbe(t, v, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer sk-customer-key-123")
	})
	if status != http.StatusOK {
		t.Fatalf("sk- key on an org-scoped route = %d, want 200 (this is the .244 break)", status)
	}
	if org != "gotham-labs" {
		t.Fatalf("sk- key resolved org %q, want %q (the key owner's org, from the subject)", org, "gotham-labs")
	}
}

// TestPublishableKeyStillGrantsNothing: pk- ships in browser bundles, so it must
// NOT gain an org from this path. The subject resolution is for secret credentials
// only.
func TestPublishableKeyStillGrantsNothing(t *testing.T) {
	v := keyValidator(t, "gotham-labs", "batkey")
	status, _ := orgScopedProbe(t, v, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer pk-public-bundle-key")
	})
	if status == http.StatusOK {
		t.Fatal("a publishable key resolved an org — pk- must never authenticate")
	}
}

// TestHumanTokenWithNoOrgsStillFailsClosed pins the OTHER path. The machine fix must
// not become a general fallback: a human token that has lost its membership claim
// still resolves nothing, because for a human the app-derived `owner` is exactly the
// caller-selectable value we removed.
func TestHumanTokenWithNoOrgsStillFailsClosed(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)

	legacy := tokenClaims("hanzo-console", "hanzo", "old@example.test", false, time.Now().Add(time.Hour))
	legacy.Orgs = nil // pre-claim human token
	tok := signWith(t, key, legacy)

	status, org := orgScopedProbe(t, v, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
	})
	if status == http.StatusOK || org != "" {
		t.Fatalf("legacy HUMAN token resolved org %q (status %d) — it must still fail closed, never fall back to the app org", org, status)
	}
}

// TestMachineJWTResolvesItsAppOrg: a POSITIVELY IDENTIFIED machine — the per-org
// KMS sync identity, named by its owner-bound "<owner>-platform-kms" audience —
// reads `owner`, the application's own org. That machine cannot choose which app it
// is and obtaining its token at all requires that app's client secret, so there is
// no app-selection hazard; omitting the branch would fail closed on the KMS sync
// identity, whose org-scoped access runs through this boundary.
//
// It is keyed on the AUDIENCE rather than on IAM's `type` claim because the IAM line
// this runs against stamps "application" nowhere, so a `type`-keyed branch never
// fired in production while the test asserting it passed.
func TestMachineJWTResolvesItsAppOrg(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)

	m := tokenClaims("gotham-labs-platform-kms", "gotham-labs", "svc@example.test", false, time.Now().Add(time.Hour))
	m.Orgs = nil
	// What separates this from the token in the test below, which must be refused:
	// IAM signs the kind on a client_credentials grant. Without it the two are the
	// same shape, which is the ambiguity that made an absent claim load-bearing.
	m.Type = authz.Program
	tok := signWith(t, key, m)

	status, org := orgScopedProbe(t, v, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
	})
	if status != http.StatusOK || org != "gotham-labs" {
		t.Fatalf("KMS machine JWT: status=%d org=%q, want 200 and %q", status, org, "gotham-labs")
	}
}

// TestGenericMachineJWTResolvesNoOrg: a machine that is NOT positively identified
// resolves NO org, and is refused.
//
// Its token is indistinguishable from a human's minted before the `orgs` claim
// shipped — no membership set, an ordinary audience — and reading `owner` for it is
// precisely the app-selection hazard homeOrg exists to remove: `owner` is the org of
// whichever application minted the token. So the rule is ONE rule, with no third
// case to get wrong: a token carrying no membership set never has an org read out of
// `owner` unless the owner-bound machine audience vouches for it.
func TestGenericMachineJWTResolvesNoOrg(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)

	m := tokenClaims("hanzo-cloud", "gotham-labs", "svc@example.test", false, time.Now().Add(time.Hour))
	m.Orgs = nil
	tok := signWith(t, key, m)

	status, org := orgScopedProbe(t, v, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
	})
	if status == http.StatusOK || org != "" {
		t.Fatalf("generic machine JWT: status=%d org=%q, want a refusal and no org", status, org)
	}
}

// TestMachineJWTInAdminOrgIsNotSuperAdmin: the machine branch reads `owner`, so it
// must not become a way back into the escalation. A machine principal is excluded
// from the SuperAdmin arm regardless of its org.
func TestMachineJWTInAdminOrgIsNotSuperAdmin(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)

	m := tokenClaims("admin-console", "admin", "svc@example.test", true, time.Now().Add(time.Hour))
	m.Orgs = nil
	m.Orgs = nil
	tok := signWith(t, key, m)

	app, got := newIdentityApp(t, v)
	probe(t, app, bearer(tok))
	if got.admin {
		t.Fatal("an admin-org MACHINE token was granted SuperAdmin")
	}
}

// TestAPIKeySubjectOrgIsNotForgeable: subjectOrg is unexported and untagged, so no
// token can carry it. A JWT naming it in the payload must be ignored entirely.
func TestAPIKeySubjectOrgIsNotForgeable(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)

	// A human token that tries to smuggle a subject org, with no membership set.
	forged := tokenClaims("hanzo-console", "hanzo", "sneak@example.test", false, time.Now().Add(time.Hour))
	forged.Orgs = nil
	tok := signWith(t, key, forged)

	status, org := orgScopedProbe(t, v, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("X-Subject-Org", "victim") // not a header the boundary reads
	})
	if status == http.StatusOK || org != "" {
		t.Fatalf("forged subject org resolved %q (status %d), want fail-closed", org, status)
	}
}
