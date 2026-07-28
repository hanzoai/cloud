package kms_test

// RED — ADVERSARIAL ISOLATION PROOF for the org-in-token reshape.
//
// The org left the URL and became a property of the validated principal
// (mount.go reqOrg → ctx.Org(), minted only by SanitizeIdentity from a signed
// claim). Every pre-reshape isolation test asserted a STATUS on a URL that named
// the victim; those URLs no longer exist, so those assertions now describe
// nothing. This file re-proves the isolation property itself, from scratch,
// against the shape that actually ships.
//
// WHY BODY, NOT STATUS. A status-only assertion cannot tell "acme was refused
// maxpower's secret" from "acme read its OWN secret at the same URL" — after the
// reshape both callers spell the identical path, so the stale tests' `want 403`
// was firing on a 200 that returned the CALLER'S OWN value. Isolation is a claim
// about PLAINTEXT, so every probe here asserts the body: no response to an
// unauthorized principal may contain a foreign org's secret, whatever its status.
// Two orgs are seeded with DISTINCT values at the IDENTICAL coordinate so a leak
// is unambiguous — the returned string names its owner.
//
// The pipeline is the real one (cloud.IdentityMiddleware → the real /v1/kms
// guard), so the org under test is derived from a signed claim exactly as in
// production. Header-injection harnesses (kms_test's do()) cannot probe the
// identity boundary itself and are not used here.

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gojose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	model "github.com/hanzoai/iam/pkg/model"
	"github.com/zap-proto/zip"
)

// isoValueB is acme's secret, sealed at the coordinate paasEnvPath addresses —
// the SAME coordinate paasValueA occupies in maxpower's store. Two distinct
// plaintexts at one coordinate is what makes a leak self-identifying: whichever
// string comes back names the org it belongs to.
const isoValueB = "s3kr3t-of-acme-ISO"

// isoClaims is the full claim surface the identity boundary reads: owner, the
// SIGNED membership set, isAdmin, and IAM's account `type`. redClaims (the V6
// file) pins Orgs to [owner] and carries no `type`, so it cannot express the
// three shapes this file attacks with — a NON-MEMBER org selection, a multi-org
// member, and a client_credentials MACHINE identity.
type isoClaims struct {
	jwt.Claims
	Owner   string         `json:"owner"`
	IsAdmin bool           `json:"isAdmin"`
	Type    string         `json:"type"`
	Orgs    []model.OrgRef `json:"orgs"`
}

// isoTok is one attacker-chosen token shape. Every field is a lever the attacker
// controls in the threat model: it may ask IAM for any app (aud), may hold any
// membership (orgs), and — for the residual cases — the test grants it claims IAM
// would not mint, to prove cloud denies on its own rather than on IAM's restraint.
type isoTok struct {
	owner   string   // the `owner` claim (the APP's org)
	orgs    []string // signed membership set, home first; nil ⇒ [owner]
	aud     []string // audience set; nil ⇒ none
	isAdmin bool
	typ     string // IAM account kind; "application" ⇒ machine principal
	expired bool
}

// mint signs tok against the same kid=test-key JWKS the e2e harness serves.
func (tk isoTok) mint(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	signer, err := gojose.NewSigner(
		gojose.SigningKey{Algorithm: gojose.RS256, Key: key},
		(&gojose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	orgs := tk.orgs
	if orgs == nil {
		orgs = []string{tk.owner}
	}
	refs := make([]model.OrgRef, 0, len(orgs))
	for _, o := range orgs {
		refs = append(refs, model.OrgRef{Org: o})
	}
	exp := time.Now().Add(time.Hour)
	if tk.expired {
		exp = time.Now().Add(-time.Hour)
	}
	raw, err := jwt.Signed(signer).Claims(isoClaims{
		Claims: jwt.Claims{
			Issuer:   e2eIssuer,
			Subject:  tk.owner + "/principal", // non-empty ⇒ X-User-Id set ⇒ principal.Validated
			Audience: jwt.Audience(tk.aud),
			Expiry:   jwt.NewNumericDate(exp),
			IssuedAt: jwt.NewNumericDate(time.Now()),
		},
		Owner:   tk.owner,
		IsAdmin: tk.isAdmin,
		Type:    tk.typ,
		Orgs:    refs,
	}).Serialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return raw
}

// isoProbe is one attack's FULL observable — status AND body. Isolation is a
// statement about the body, so both are captured together and asserted together;
// a status-only observable is precisely what let a caller reading its OWN secret
// be scored as a cross-org 200.
type isoProbe struct {
	what   string
	status int
	body   string
}

// isoGet issues one probe against the real pipeline with attacker-chosen headers.
func isoGet(t *testing.T, app *zip.App, what, path, token string, hdr map[string]string) isoProbe {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("%s: GET %s: %v", what, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	p := isoProbe{what: what, status: resp.StatusCode, body: strings.TrimSpace(string(b))}
	t.Logf("PROBE %-58s → %d %s", what, p.status, p.body)
	return p
}

// noLeak is the ONE assertion this file's isolation claim rests on: the response
// carries none of the named foreign plaintexts. A status is not a proof — a 403
// with the secret in the body is still a breach, and a 200 that returns the
// caller's OWN secret is not one.
func (p isoProbe) noLeak(t *testing.T, foreign ...string) isoProbe {
	t.Helper()
	for _, f := range foreign {
		if strings.Contains(p.body, f) {
			t.Fatalf("LEAK [%s]: status=%d body contains foreign plaintext %q: %s", p.what, p.status, f, p.body)
		}
	}
	return p
}

// isValue asserts the response IS a 200 carrying exactly want in the `value`
// field — the positive half, so a test that proves "no leak" by breaking the
// endpoint outright cannot pass silently.
func (p isoProbe) isValue(t *testing.T, want string) isoProbe {
	t.Helper()
	if p.status != 200 {
		t.Fatalf("[%s]: status=%d, want 200 carrying %q: %s", p.what, p.status, want, p.body)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(p.body), &m); err != nil {
		t.Fatalf("[%s]: body is not JSON: %v: %s", p.what, err, p.body)
	}
	if got, _ := m["value"].(string); got != want {
		t.Fatalf("[%s]: value=%q, want %q", p.what, got, want)
	}
	return p
}

// isStatus asserts the refusal code, so a reconciled expectation stays pinned and
// a future change from 404 back to 200 fails loudly rather than silently.
func (p isoProbe) isStatus(t *testing.T, want int) isoProbe {
	t.Helper()
	if p.status != want {
		t.Fatalf("[%s]: status=%d, want %d: %s", p.what, p.status, want, p.body)
	}
	return p
}

// isoWorld stands up the real pipeline with maxpower and acme seeded at the SAME
// coordinate with DISTINCT values, plus the admin org's own secret. Returned
// alongside the one URL every caller — victim, attacker, admin — must spell,
// because the org is no longer nameable in a URL.
func isoWorld(t *testing.T) (*zip.App, *rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := e2eJWKS(t, &key.PublicKey)
	app, deps := newAppWithIdentity(t, e2eCfg(t, jwks.URL)) // AdminOrg="admin"
	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA)   // maxpower
	sealPlatformSecret(t, deps.KMS, paasOrgB, isoValueB)    // acme, SAME coordinate
	sealPlatformSecret(t, deps.KMS, "admin", "s3kr3t-of-admin-ISO")
	return app, key, "/v1/kms" + paasEnvPath
}

// ── (a) a valid token for org B asking for the NAME org A used ─────────────────
//
// The post-reshape cross-org attempt: acme cannot SPELL maxpower's secret, so the
// strongest thing it can do is present its own valid credential at the exact URL
// maxpower uses. The org folded into the store path comes from acme's claim, so
// the request resolves to acme's OWN record — same URL, different tenant, and the
// body proves which one answered.
func TestRedIso_A_SameNameOtherOrg(t *testing.T) {
	app, key, path := isoWorld(t)

	isoGet(t, app, "(a) acme token, maxpower's URL", path, isoTok{owner: paasOrgB}.mint(t, key), nil).
		noLeak(t, paasValueA).
		isValue(t, isoValueB)

	// The victim itself still reads its own — the boundary admits, it does not
	// blanket-deny (a test that broke the endpoint would fail here).
	isoGet(t, app, "(a) maxpower token, own URL", path, isoTok{owner: paasOrgA}.mint(t, key), nil).
		isValue(t, paasValueA)

	// A third org with NO secret at that coordinate gets not-found — the same
	// answer a cross-org attempt gets, which is the point: absence and
	// unauthorized-elsewhere are indistinguishable.
	isoGet(t, app, "(a) third org, no such secret", path, isoTok{owner: "nobody"}.mint(t, key), nil).
		noLeak(t, paasValueA, isoValueB).
		isStatus(t, 404)
}

// ── (b) forged X-Org-Id with no matching validated principal ───────────────────
//
// X-Org-Id is a client header. SanitizeIdentity strips it on ingress and restores
// it un-validated on the bearer-less path, so an off-gateway caller can still
// present `X-Org-Id: maxpower`. Two defenses must hold: the guard refuses a
// request with no validated principal at all, and a VALIDATED principal's
// selection is honored only inside its signed membership set.
func TestRedIso_B_ForgedOrgHeader(t *testing.T) {
	app, key, path := isoWorld(t)

	// No credential, forged org — the anonymous-forge signature.
	isoGet(t, app, "(b) forged X-Org-Id, NO bearer", path, "", map[string]string{"X-Org-Id": paasOrgA}).
		noLeak(t, paasValueA).
		isStatus(t, 403)

	// Forged org + forged AUTHORITY headers. These are authorityHeaders: stripped
	// on ingress and re-minted only from validated claims, so asserting them
	// client-side grants nothing.
	isoGet(t, app, "(b) forged org + forged X-User-Id + IsAdmin", path, "", map[string]string{
		"X-Org-Id": paasOrgA, "X-User-Id": "u-attacker", "X-User-IsAdmin": "true", "X-User-IsOrgAdmin": "true",
	}).noLeak(t, paasValueA).isStatus(t, 403)

	// A VALIDATED acme principal selecting maxpower. isMember(claims.Orgs, "maxpower")
	// is false, so the selection is DISCARDED (not honored, not refused) and the
	// request continues in acme's own org — the body is acme's secret.
	isoGet(t, app, "(b) acme token + X-Org-Id:maxpower (non-member)", path,
		isoTok{owner: paasOrgB}.mint(t, key), map[string]string{"X-Org-Id": paasOrgA}).
		noLeak(t, paasValueA).
		isValue(t, isoValueB)

	// An expired credential is anonymous, so the forged org has no principal to
	// ride on — the same refusal as no credential at all.
	isoGet(t, app, "(b) EXPIRED acme token + X-Org-Id:maxpower", path,
		isoTok{owner: paasOrgB, expired: true}.mint(t, key), map[string]string{"X-Org-Id": paasOrgA}).
		noLeak(t, paasValueA, isoValueB).
		isStatus(t, 403)

	// A garbage bearer never validates, so it never mints a principal.
	isoGet(t, app, "(b) junk bearer + X-Org-Id:maxpower", path, "hk-not-a-jwt",
		map[string]string{"X-Org-Id": paasOrgA}).noLeak(t, paasValueA).isStatus(t, 403)
}

// ── (c) admin / superadmin attempting cross-org traversal ──────────────────────
//
// TWO DISTINCT MECHANISMS, one removed and one retained — they must not be
// conflated, and this test pins both.
//
//	URL TRAVERSAL — REMOVED. No route accepts an org, so no spelling of one
//	reaches another tenant. Admin or not, the router answers 404/400.
//
//	CLAIM-BOUND ORG-SWITCH — RETAINED. SanitizeIdentity's SuperAdmin arm honors
//	X-Org-Id as the effective org (middleware_identity.go: `effOrg = cliOrg`),
//	gated on home-org membership of the reserved admin org AND !isMachinePrincipal.
//	That is platform sudo, and it is exactly the "explicit impersonation via a
//	token claim, never URL traversal" shape — see the decision note on
//	TestRESTRoundtripOrgScoped. This test proves the gate is the CLAIM: every
//	principal that is not a human SuperAdmin is refused the switch.
func TestRedIso_C_AdminCrossOrg(t *testing.T) {
	app, key, path := isoWorld(t)
	superAdmin := isoTok{owner: "admin", isAdmin: true}.mint(t, key)

	// URL traversal, admin credential — every spelling of the victim's org. The
	// dotted forms are refused by ValidSubpath; the DOTLESS ones (an absolute
	// "/orgs/maxpower/…" injected into the wildcard, raw or percent-encoded) carry
	// nothing for ValidSubpath to reject and are the sharper attack. They fail
	// structurally instead: orgPath ALWAYS prefixes "/orgs/{caller-org}", so the
	// injected text can only ever extend the caller's OWN subtree
	// ("/orgs/admin/orgs/maxpower/…"), never replace it.
	for _, p := range []string{
		"/v1/kms/orgs/" + paasOrgA + paasEnvPath,
		"/v1/kms/admin/orgs/" + paasOrgA + paasEnvPath,
		"/v1/kms/secrets/../orgs/" + paasOrgA + "/platform/api/DB_PASSWORD?env=default",
		"/v1/kms/secrets/%2e%2e%2forgs%2f" + paasOrgA + "%2fplatform%2fapi%2fDB_PASSWORD?env=default",
		"/v1/kms/secrets/..%2f..%2forgs%2f" + paasOrgA + "%2fplatform%2fapi%2fDB_PASSWORD?env=default",
		"/v1/kms/secrets//orgs/" + paasOrgA + "/platform/api/DB_PASSWORD?env=default",
		"/v1/kms/secrets/%2forgs%2f" + paasOrgA + "%2fplatform%2fapi%2fDB_PASSWORD?env=default",
		"/v1/kms/secrets/orgs/" + paasOrgA + "/platform/api/DB_PASSWORD?env=default",
	} {
		isoGet(t, app, "(c) SuperAdmin URL traversal "+p, p, superAdmin, nil).noLeak(t, paasValueA)
	}

	// SuperAdmin with NO switch header reads its OWN org, like anyone else.
	isoGet(t, app, "(c) SuperAdmin, no switch header", path, superAdmin, nil).
		noLeak(t, paasValueA).
		isValue(t, "s3kr3t-of-admin-ISO")

	// A MACHINE principal in the admin org, even asserting isAdmin=true, is denied
	// the SuperAdmin arm (isMachinePrincipal) and so cannot switch — it stays
	// pinned to its own org. Both machine discriminators are exercised: IAM's
	// `type` claim, and the owner-bound <owner>-platform-kms audience.
	for _, tk := range []isoTok{
		{owner: "admin", isAdmin: true, typ: "application"},
		{owner: "admin", isAdmin: true, aud: []string{"admin-platform-kms"}},
		{owner: "admin", isAdmin: true, aud: []string{"hanzo-console", "admin-platform-kms"}},
		{owner: "admin", isAdmin: true, aud: []string{"admin-platform-kms", "hanzo-console"}},
	} {
		isoGet(t, app, "(c) admin-org MACHINE + X-Org-Id:maxpower", path, tk.mint(t, key),
			map[string]string{"X-Org-Id": paasOrgA}).
			noLeak(t, paasValueA).
			isValue(t, "s3kr3t-of-admin-ISO")
	}

	// An ORG admin (isAdmin=true) of a NON-admin org is not platform sudo: the
	// switch is not offered to it, and its non-member selection is discarded.
	isoGet(t, app, "(c) acme ORG-admin + X-Org-Id:maxpower", path,
		isoTok{owner: paasOrgB, isAdmin: true}.mint(t, key), map[string]string{"X-Org-Id": paasOrgA}).
		noLeak(t, paasValueA).
		isValue(t, isoValueB)

	// A principal whose `owner` claim says "admin" but whose SIGNED membership set
	// says otherwise is NOT a SuperAdmin: the gate reads the membership set
	// (claims.homeOrg), never the app-selected `owner`.
	isoGet(t, app, "(c) owner=admin but orgs=[acme] + X-Org-Id:maxpower", path,
		isoTok{owner: "admin", orgs: []string{paasOrgB}, isAdmin: true}.mint(t, key),
		map[string]string{"X-Org-Id": paasOrgA}).
		noLeak(t, paasValueA).
		isValue(t, isoValueB)

	// THE RETAINED CAPABILITY, pinned. A HUMAN member of the reserved admin org
	// switches into maxpower and reads it. This is platform sudo by design
	// (principal.go: "a SuperAdmin acting in another org"), it is claim-bound, and
	// it is the answer to "where did admin cross-org read go?" — it did not
	// disappear with the URL, it moved onto the identity. Pinned so that if the
	// product decision is to REMOVE it, this test fails and forces the choice to
	// be made explicitly rather than drifting.
	isoGet(t, app, "(c) HUMAN SuperAdmin + X-Org-Id:maxpower [RETAINED CAPABILITY]", path,
		superAdmin, map[string]string{"X-Org-Id": paasOrgA}).
		isValue(t, paasValueA)
}

// ── (d) mismatched / absent aud ────────────────────────────────────────────────
//
// A DIFFERENT AXIS from org scoping. Audience is deliberately NOT an access gate
// (auth_identity.go validate: trust is signature + issuer + expiry; `aud` merely
// names which of IAM's own apps minted the token). The security property that
// therefore MUST hold is that aud never WIDENS reach: whatever an attacker puts
// in the audience set — the victim's machine aud, a console aud, nothing at all —
// the reachable org stays the one the signed claims name.
func TestRedIso_D_AudNeverWidensReach(t *testing.T) {
	app, key, path := isoWorld(t)

	for _, tc := range []struct {
		what string
		aud  []string
	}{
		{"victim's machine aud", []string{paasOrgA + "-platform-kms"}},
		{"multi-value w/ victim machine aud", []string{"hanzo-console", paasOrgA + "-platform-kms"}},
		{"victim machine aud FIRST", []string{paasOrgA + "-platform-kms", "hanzo-console"}},
		{"absent aud", nil},
		{"empty-string aud", []string{""}},
		{"never-registered aud", []string{"a-brand-new-app-never-registered"}},
		{"admin-org machine aud", []string{"admin-platform-kms"}},
	} {
		// acme presents it: reach stays acme's, never maxpower's.
		isoGet(t, app, "(d) acme aud="+tc.what, path,
			isoTok{owner: paasOrgB, aud: tc.aud}.mint(t, key), nil).
			noLeak(t, paasValueA).
			isValue(t, isoValueB)

		// …and with a forged org selection stacked on top, still acme's.
		isoGet(t, app, "(d) acme aud="+tc.what+" + X-Org-Id:maxpower", path,
			isoTok{owner: paasOrgB, aud: tc.aud}.mint(t, key), map[string]string{"X-Org-Id": paasOrgA}).
			noLeak(t, paasValueA).
			isValue(t, isoValueB)
	}

	// The converse, so this is a proof about reach and not about the endpoint
	// being broken: maxpower carrying ACME's machine aud still reads maxpower.
	isoGet(t, app, "(d) maxpower carrying acme's machine aud", path,
		isoTok{owner: paasOrgA, aud: []string{paasOrgB + "-platform-kms"}}.mint(t, key), nil).
		isValue(t, paasValueA)
}

// ── (e) case-fold and unsafe-rune org folding ──────────────────────────────────
//
// The org is folded into a store PATH and, one layer down, into a per-org FILE via
// cloud.SanitizeOrg. Two distinct IAM owners that fold onto one namespace would be
// a cross-tenant break with no forged header required, so the fold must be
// injective end to end.
func TestRedIso_E_OrgFoldIsInjective(t *testing.T) {
	app, key, path := isoWorld(t)

	// Case-distinct owners are DISTINCT tenants: "Maxpower" cannot reach
	// "maxpower". (SanitizeOrg is the identity only on clean lowercase labels;
	// anything else gets a SHA-256-derived suffix, so the files never alias.)
	for _, o := range []string{"Maxpower", "MAXPOWER", "maxPower"} {
		isoGet(t, app, "(e) case-variant owner "+o, path, isoTok{owner: o}.mint(t, key), nil).
			noLeak(t, paasValueA).
			isStatus(t, 404)
	}

	// Trim-collapsible / zero-width owners must not fold onto the victim.
	// OrgHasUnsafeRune zeroes such an owner at the boundary, so the request
	// resolves org-less and the guard refuses it — 400 (the org fails the
	// DNS-1123 edge check) or 403 (no org at all), never the victim's value.
	for _, o := range []string{"maxpower ", " maxpower", "maxpower\t", "maxpower​", "maxpower "} {
		p := isoGet(t, app, "(e) unsafe-rune owner "+strings.ReplaceAll(o, "​", "<zwsp>"), path,
			isoTok{owner: o}.mint(t, key), nil).noLeak(t, paasValueA)
		if p.status != 400 && p.status != 403 {
			t.Fatalf("[%s]: status=%d, want 400 or 403 (org-less fail-closed)", p.what, p.status)
		}
	}

	// The reserved platform partition ("_platform" routes to PlatformDB, where
	// cloud's own org-less facade secrets live) must not be reachable as a tenant
	// org. It shares a FILE, but the row's `path` column is the boundary and it is
	// unspellable: an org-less facade ref keys at "/", a tenant keys at
	// "/orgs/_platform", so no query crosses.
	isoGet(t, app, "(e) org=_platform reaching the reserved partition", path,
		isoTok{owner: "_platform"}.mint(t, key), nil).
		noLeak(t, paasValueA, isoValueB, "s3kr3t-of-admin-ISO").
		isStatus(t, 404)
}

// ── (f) the write + list + delete faces, same boundary ─────────────────────────
//
// GET is not the whole surface. A write that lands in another org's namespace, a
// list that enumerates it, or a delete that destroys it are all cross-org breaks;
// each folds the org through the same orgPath, and each is probed here.
func TestRedIso_F_WriteListDeleteAreScopedToo(t *testing.T) {
	app, key, path := isoWorld(t)
	acme := isoTok{owner: paasOrgB}.mint(t, key)

	post := func(what, body string, hdr map[string]string) isoProbe {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/kms/secrets", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+acme)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		p := isoProbe{what: what, status: resp.StatusCode, body: strings.TrimSpace(string(b))}
		t.Logf("PROBE %-58s → %d %s", what, p.status, p.body)
		return p
	}

	// A subpath that tries to climb out of acme's namespace into maxpower's.
	for _, sub := range []string{
		"../" + paasOrgA + "/platform/api",
		"../../orgs/" + paasOrgA + "/platform/api",
		"./../" + paasOrgA,
	} {
		body, _ := json.Marshal(map[string]string{
			"name": "DB_PASSWORD", "value": "OVERWRITTEN-BY-ACME", "env": "default", "path": sub,
		})
		post("(f) acme POST path="+sub, string(body), nil).isStatus(t, 400)
	}

	// A subpath naming the victim WITHOUT a climb lands strictly under acme —
	// "/orgs/acme/orgs/maxpower/..." — so it can never overwrite maxpower's record.
	body, _ := json.Marshal(map[string]string{
		"name": "DB_PASSWORD", "value": "OVERWRITTEN-BY-ACME", "env": "default",
		"path": "orgs/" + paasOrgA + "/platform/api",
	})
	post("(f) acme POST path=orgs/maxpower/... (no climb)", string(body), nil).isStatus(t, 200)

	// The victim's record is untouched — the write went to acme's own subtree.
	isoGet(t, app, "(f) maxpower reads its own after acme's write", path,
		isoTok{owner: paasOrgA}.mint(t, key), nil).isValue(t, paasValueA)

	// LIST: acme enumerating maxpower's path is impossible to spell; its own list
	// at the victim's coordinate is empty, and a climbing ?path= is refused.
	isoGet(t, app, "(f) acme LIST at maxpower's coordinate", "/v1/kms/secrets?path=platform/api&env=default",
		acme, nil).noLeak(t, paasValueA).isStatus(t, 200)
	isoGet(t, app, "(f) acme LIST ?path=../maxpower climb", "/v1/kms/secrets?path=../"+paasOrgA+"&env=default",
		acme, nil).noLeak(t, paasValueA).isStatus(t, 400)

	// DELETE is destructive, so its scoping is proven in three steps rather than by
	// a single status. acme DELETEs the shared coordinate: it succeeds, because
	// acme HAS a record there — that 200 is acme destroying its OWN secret, not
	// maxpower's. The next two steps are what make it a proof: maxpower's record
	// survives, and acme's SECOND delete is 404 because acme's reach is now empty
	// while maxpower's record sits at the same URL, untouched and invisible.
	del := func(what, p string) isoProbe {
		t.Helper()
		req := httptest.NewRequest("DELETE", p, nil)
		req.Header.Set("Authorization", "Bearer "+acme)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		pr := isoProbe{what: what, status: resp.StatusCode, body: strings.TrimSpace(string(b))}
		t.Logf("PROBE %-58s → %d %s", what, pr.status, pr.body)
		return pr
	}
	del("(f) acme DELETE shared coordinate (hits acme's OWN)", "/v1/kms"+paasEnvPath).isStatus(t, 200)
	isoGet(t, app, "(f) maxpower reads its own after acme's DELETE", path,
		isoTok{owner: paasOrgA}.mint(t, key), nil).isValue(t, paasValueA)
	del("(f) acme DELETE again — own gone, maxpower's unreachable", "/v1/kms"+paasEnvPath).isStatus(t, 404)
	isoGet(t, app, "(f) maxpower STILL reads its own after acme's 2nd DELETE", path,
		isoTok{owner: paasOrgA}.mint(t, key), nil).isValue(t, paasValueA)
}

// ── (g) no cross-org existence oracle ──────────────────────────────────────────
//
// The reshape's isolation signal is NOT-FOUND, which is strictly stronger than the
// FORBIDDEN it replaced — but only if it is uniform. If "exists in another org"
// answered differently from "does not exist anywhere", the 404 would itself be the
// oracle it is supposed to close. Probe both and require them identical, in status
// AND in body.
func TestRedIso_G_NoExistenceOracle(t *testing.T) {
	app, key, path := isoWorld(t)
	attacker := isoTok{owner: "attacker"}.mint(t, key)

	exists := isoGet(t, app, "(g) probe a name that EXISTS in maxpower", path, attacker, nil).
		noLeak(t, paasValueA)
	missing := isoGet(t, app, "(g) probe a name that exists NOWHERE",
		"/v1/kms/secrets/platform/api/NO_SUCH_SECRET?env=default", attacker, nil)

	if exists.status != missing.status || exists.body != missing.body {
		t.Fatalf("EXISTENCE ORACLE: existing→(%d %s) vs missing→(%d %s) differ — the attacker learns "+
			"which secret NAMES other tenants use", exists.status, exists.body, missing.status, missing.body)
	}
	if exists.status != http.StatusNotFound {
		t.Fatalf("cross-org probe status=%d, want 404 (org unspellable ⇒ not-found, existence hidden)", exists.status)
	}
	t.Logf("no existence oracle: both cross-org probes = %d %s", exists.status, exists.body)
}
