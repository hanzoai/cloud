// Copyright © 2026 Hanzo AI. MIT License.

package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
)

func init() { gin.SetMode(gin.TestMode) }

// TestEdgeAuth_StripsSpoofedIdentity proves the core security fix: at a
// directly-exposed edge a client-supplied X-Org-Id (and friends) must be
// removed so it can't impersonate an org via the header-trust path.
func TestEdgeAuth_StripsSpoofedIdentity(t *testing.T) {
	t.Setenv("COMMERCE_EDGE_AUTH", "true")
	h := EdgeAuth()

	req := httptest.NewRequest("GET", "/v1/billing/balance?user=hanzo&currency=usd", nil)
	req.Header.Set("X-Org-Id", "hanzo")
	req.Header.Set("X-User-Permissions", "16") // spoofed admin bit
	req.Header.Set("X-User-IsAdmin", "true")

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	h(c)

	for _, k := range []string{"X-Org-Id", "X-User-Permissions", "X-User-IsAdmin"} {
		if got := c.Request.Header.Get(k); got != "" {
			t.Fatalf("EdgeAuth(enabled) must strip spoofed %s, got %q", k, got)
		}
	}
}

// TestEdgeAuth_StripsGlobalAdminSpoof proves the PLATFORM-superadmin header
// is stripped at the edge. After the global-admin/org-admin split,
// GetIAMClaims reads X-User-IsGlobalAdmin straight off the request and
// GlobalAdmin() trusts it — so if EdgeAuth did NOT strip it, an in-cluster
// caller could forge `X-User-IsGlobalAdmin: true` and pass every cross-org
// gate (the exact /_/commerce/tenants escalation). It MUST be in the strip set.
func TestEdgeAuth_StripsGlobalAdminSpoof(t *testing.T) {
	t.Setenv("COMMERCE_EDGE_AUTH", "true")
	h := EdgeAuth()

	req := httptest.NewRequest("POST", "/_/commerce/tenants", nil)
	req.Header.Set("X-Org-Id", "admin")
	req.Header.Set("X-User-IsGlobalAdmin", "true") // forged platform superadmin
	req.Header.Set("X-User-IsAdmin", "true")

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	h(c)

	for _, k := range []string{"X-Org-Id", "X-User-IsGlobalAdmin", "X-User-IsAdmin"} {
		if got := c.Request.Header.Get(k); got != "" {
			t.Fatalf("EdgeAuth must strip spoofed %s, got %q", k, got)
		}
	}
}

// TestEdgeAuth_OpaqueBearerStashesOrgNotHeader proves the bypass fix AND that
// per-org billing survives it. An opaque (non-JWT) bearer — a service token —
// names its org via X-Org-Id. EdgeAuth must:
//
//	(a) STRIP X-Org-Id from the trusted header, so IAMTokenRequired (which runs
//	    BEFORE the token is validated) can never mistake it for a verified IAM
//	    identity — restoring the header was the complete auth bypass (opaque
//	    bearer + X-Org-Id ⇒ forged principal for any org); and
//	(b) stash the org in a PRIVATE ctx key that ONLY TokenRequired's
//	    service-token branch reads, AFTER it verifies the token — so per-org
//	    billing/checkout still resolves the caller's own org.
//
// EdgeAuth never aborts an opaque bearer (not a JWT ⇒ minting skipped).
func TestEdgeAuth_OpaqueBearerStashesOrgNotHeader(t *testing.T) {
	t.Setenv("COMMERCE_EDGE_AUTH", "true")
	h := EdgeAuth()

	req := httptest.NewRequest("POST", "/v1/billing/usage", nil)
	req.Header.Set("Authorization", "Bearer st_opaque_service_token_not_a_jwt")
	req.Header.Set("X-Org-Id", "maxpower")

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	h(c)

	if c.IsAborted() {
		t.Fatal("EdgeAuth must NOT abort an opaque service-token request (money path)")
	}
	// (a) X-Org-Id MUST be stripped — never re-exposed to the trust boundary.
	if got := c.Request.Header.Get("X-Org-Id"); got != "" {
		t.Fatalf("opaque bearer: X-Org-Id must stay stripped (bypass fix), got %q", got)
	}
	// (b) org preserved out-of-band for the validated-service-token branch.
	if got := clientOrgFromContext(c); got != "maxpower" {
		t.Fatalf("opaque bearer org must be stashed for per-org billing, got %q", got)
	}
}

// TestEdgeAuth_ClosesOpaqueBearerBypass is the explicit RED regression: the
// live-proved bypass was `Authorization: Bearer <opaque>` + `X-Org-Id: <target>`
// → EdgeAuth restored the client X-Org-Id → IAMTokenRequired trusted it (no
// token check) → the checkout money surface minted for the attacker's chosen
// org. After the fix, X-Org-Id is stripped and NEVER put back on the header for
// a non-service opaque bearer, so IAMTokenRequired resolves no org and the
// request cannot forge an IAM principal. The stash is inert unless the token
// later proves to be COMMERCE_SERVICE_TOKEN, which "redteamprobe" is not.
func TestEdgeAuth_ClosesOpaqueBearerBypass(t *testing.T) {
	t.Setenv("COMMERCE_EDGE_AUTH", "true")
	h := EdgeAuth()

	for _, path := range []string{
		"/v1/checkout/sessions", "/v1/checkout/charge",
		"/v1/checkout/authorize", "/v1/checkout/capture",
	} {
		req := httptest.NewRequest("POST", path, nil)
		req.Header.Set("Authorization", "Bearer redteamprobe")
		req.Header.Set("X-Org-Id", "hanzo")

		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		h(c)

		if got := c.Request.Header.Get("X-Org-Id"); got != "" {
			t.Fatalf("%s: bypass vector X-Org-Id must be stripped, got %q", path, got)
		}
	}
}

// TestEdgeAuth_DisabledIsNoOp proves gateway-fronted deployments are
// unaffected: with the flag off, identity headers pass through untouched.
func TestEdgeAuth_DisabledIsNoOp(t *testing.T) {
	t.Setenv("COMMERCE_EDGE_AUTH", "")
	h := EdgeAuth()

	req := httptest.NewRequest("GET", "/v1/billing/balance", nil)
	req.Header.Set("X-Org-Id", "hanzo")

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	h(c)

	if got := c.Request.Header.Get("X-Org-Id"); got != "hanzo" {
		t.Fatalf("EdgeAuth(disabled) must be a no-op, X-Org-Id=%q", got)
	}
}

// TestEdgeAuth_NilClientNeverMints proves fail-closed: a JWT-shaped token
// with no verifier (nil client) yields NO minted identity — the spoofed
// header is stripped and nothing is trusted in its place.
func TestEdgeAuth_NilClientNeverMints(t *testing.T) {
	t.Setenv("COMMERCE_EDGE_AUTH", "true")
	h := EdgeAuth()

	req := httptest.NewRequest("GET", "/v1/billing/balance", nil)
	req.Header.Set("Authorization", "Bearer a.b.c")
	req.Header.Set("X-Org-Id", "evil")

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	h(c)

	if got := c.Request.Header.Get("X-Org-Id"); got != "" {
		t.Fatalf("nil verifier must not mint identity, X-Org-Id=%q", got)
	}
}

// TestLockBillingSubject proves per-org isolation: the billing subject is
// pinned to the caller's own org slug, other query params are preserved.
func TestLockBillingSubject(t *testing.T) {
	req := httptest.NewRequest("GET",
		"/v1/billing/transactions?user=victim/other&userId=x&customerId=y&limit=5", nil)
	lockBillingSubject(req, "hanzo")

	q := req.URL.Query()
	for _, k := range []string{"user", "userId", "customerId"} {
		if q.Get(k) != "hanzo" {
			t.Fatalf("%s must be locked to org slug, got %q", k, q.Get(k))
		}
	}
	if q.Get("limit") != "5" {
		t.Fatalf("unrelated query param clobbered, limit=%q", q.Get("limit"))
	}
}

// TestLockBillingSubjectBody is the write-side counterpart to
// TestLockBillingSubject: a /billing/ write whose JSON body names a FOREIGN
// billing subject must have it pinned to the caller's own slug before the
// handler binds it — the exact gap that let a body customerId of "hanzo/z"
// orphan a payment method that every (query-locked) read could never see.
func TestLockBillingSubjectBody(t *testing.T) {
	// amount is 2^53+1: a value float64 cannot hold exactly, so a clean
	// round-trip proves the rewrite preserves numeric precision (json.Number).
	const body = `{"customerId":"victim/other","amount":9007199254740993,"currency":"usd","brand":"visa"}`
	req := httptest.NewRequest("POST", "/v1/billing/payment-methods", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	lockBillingSubjectBody(req, "hanzo")

	raw, _ := io.ReadAll(req.Body)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("rewritten body must stay valid JSON: %v (%s)", err, raw)
	}
	if m["customerId"] != "hanzo" {
		t.Fatalf("body customerId must be pinned to caller slug, got %v", m["customerId"])
	}
	if n, _ := m["amount"].(json.Number); n.String() != "9007199254740993" {
		t.Fatalf("numeric precision lost in rewrite, amount=%v", m["amount"])
	}
	if m["currency"] != "usd" || m["brand"] != "visa" {
		t.Fatalf("unrelated fields clobbered: %v", m)
	}
	if got := req.Header.Get("Content-Length"); got != strconv.Itoa(len(raw)) {
		t.Fatalf("Content-Length must track rewritten body (%d), got %q", len(raw), got)
	}
}

// TestLockBillingSubjectBody_PinsAllSubjectKeys: every billing-subject field in
// the body (user/userId/customerId) is pinned, mirroring the query lock's set.
func TestLockBillingSubjectBody_PinsAllSubjectKeys(t *testing.T) {
	const body = `{"user":"a/b","userId":"c/d","customerId":"e/f","note":"keep"}`
	req := httptest.NewRequest("PUT", "/v1/billing/payment-methods/123", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	lockBillingSubjectBody(req, "hanzo")

	var m map[string]any
	if err := json.NewDecoder(req.Body).Decode(&m); err != nil {
		t.Fatalf("rewritten body must stay valid JSON: %v", err)
	}
	for _, k := range []string{"user", "userId", "customerId"} {
		if m[k] != "hanzo" {
			t.Fatalf("body %s must be pinned to caller slug, got %v", k, m[k])
		}
	}
	if m["note"] != "keep" {
		t.Fatalf("unrelated field clobbered, note=%v", m["note"])
	}
}

// TestLockBillingSubjectBody_ShapeAgnostic proves the rewrite is surgical: a
// body that is not a JSON object, carries none of the subject keys, or is
// unparseable passes through byte-for-byte — EdgeAuth never rejects or mangles
// an unexpected shape, it only ever pins the three known keys when present.
func TestLockBillingSubjectBody_ShapeAgnostic(t *testing.T) {
	for _, body := range []string{
		`["a","b"]`,                       // JSON array, not an object
		`{"amount":100,"currency":"usd"}`, // object without any subject key
		`not json at all`,                 // unparseable
		``,                                // empty
	} {
		req := httptest.NewRequest("POST", "/v1/billing/topup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		lockBillingSubjectBody(req, "hanzo")
		if got, _ := io.ReadAll(req.Body); string(got) != body {
			t.Fatalf("body %q must pass through untouched, got %q", body, got)
		}
	}
}

// TestLockBillingSubjectBody_NonWriteMethodIgnored: reads (GET/DELETE) carry the
// subject in the query (locked by lockBillingSubject), so the body lock — which
// would consume the stream — must leave non-write methods untouched.
func TestLockBillingSubjectBody_NonWriteMethodIgnored(t *testing.T) {
	const body = `{"customerId":"victim/other"}`
	req := httptest.NewRequest("GET", "/v1/billing/balance", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	lockBillingSubjectBody(req, "hanzo")
	if got, _ := io.ReadAll(req.Body); string(got) != body {
		t.Fatalf("GET body must be untouched, got %q", got)
	}
}

// TestLockBillingSubjectBody_NonJSONIgnored: a non-JSON content type carries no
// billing-subject fields to pin, so the body is left untouched (and unread).
func TestLockBillingSubjectBody_NonJSONIgnored(t *testing.T) {
	const body = `customerId=victim%2Fother&amount=100`
	req := httptest.NewRequest("POST", "/v1/billing/payment-methods", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lockBillingSubjectBody(req, "hanzo")
	if got, _ := io.ReadAll(req.Body); string(got) != body {
		t.Fatalf("non-JSON body must be untouched, got %q", got)
	}
}

func TestLooksLikeJWTAndBearer(t *testing.T) {
	cases := map[string]bool{"a.b.c": true, "hk-abc123": false, "": false, "a.b": false}
	for tok, want := range cases {
		if got := looksLikeJWT(tok); got != want {
			t.Fatalf("looksLikeJWT(%q)=%v want %v", tok, got, want)
		}
	}
	if got := bearerToken("Bearer xyz"); got != "xyz" {
		t.Fatalf("bearerToken Bearer parse = %q", got)
	}
	if got := bearerToken("xyz"); got != "" {
		t.Fatalf("bearerToken non-bearer must be empty, got %q", got)
	}
}

// TestIsGlobalAdmin proves the global-admin gate: only the explicit
// isGlobalAdmin claim or membership in the "admin" org qualifies. A plain
// org-level IsAdmin (e.g. an org owner) must NOT — trusting it would let any
// org owner read another org via ?org=.
func TestIsGlobalAdmin(t *testing.T) {
	cases := []struct {
		name   string
		claims *auth.IAMClaims
		want   bool
	}{
		{"nil", nil, false},
		{"admin-org", &auth.IAMClaims{Owner: "admin"}, true},
		{"admin-org-mixedcase", &auth.IAMClaims{Owner: "Admin"}, true},
		{"global-flag", &auth.IAMClaims{Owner: "hanzo", IsGlobalAdmin: true}, true},
		{"org-admin-not-global", &auth.IAMClaims{Owner: "maxpower", IsAdmin: true}, false},
		{"plain-user", &auth.IAMClaims{Owner: "hanzo"}, false},
	}
	for _, tc := range cases {
		if got := isGlobalAdmin(tc.claims); got != tc.want {
			t.Fatalf("%s: isGlobalAdmin=%v want %v", tc.name, got, tc.want)
		}
	}
}

// TestPermsHeader_AdminBitIsGlobalOnly is the money-hole regression. It pins the
// exact X-User-Permissions bit.Field permsHeader mints for the three principal
// classes, in the wire units commerce parses (permission.Live=4, Admin=16). The
// invariant: the Admin (money/admin) bit is minted ONLY for a global admin; an
// org-level admin (an org owner) gets Live but NOT Admin, so
// TokenRequired(permission.Admin) on the credit-creating / card-charging billing
// endpoints denies them. Before the fix an org owner minted Admin|Live=20 and
// satisfied every admin money gate → unlimited free balance + platform-wide card
// charging (live-proven as Dave/maxpower).
func TestPermsHeader_AdminBitIsGlobalOnly(t *testing.T) {
	const (
		live      = 4  // permission.Live  (1 << 2)
		admin     = 16 // permission.Admin (1 << 4)
		adminLive = admin | live
	)
	cases := []struct {
		name   string
		claims *auth.IAMClaims
		want   int64
	}{
		// Plain authenticated user: no admin authority, no live mode. Zero bits.
		{"plain-user", &auth.IAMClaims{Owner: "hanzo"}, 0},
		// Org owner (maxpower): live mode YES (real Square top-up must work),
		// Admin money bit NO. This is the exact principal that exploited the hole.
		{"org-admin-live-not-admin", &auth.IAMClaims{Owner: "maxpower", IsAdmin: true}, live},
		// Global admin via the admin org: full Admin|Live.
		{"global-admin-org", &auth.IAMClaims{Owner: "admin", IsAdmin: true}, adminLive},
		// Global admin via the explicit claim (any org): full Admin|Live.
		{"global-admin-flag", &auth.IAMClaims{Owner: "hanzo", IsGlobalAdmin: true, IsAdmin: true}, adminLive},
		// Global-admin flag WITHOUT org-level IsAdmin still carries Admin (the
		// money authority), and also Live is absent (IsAdmin gates Live) —
		// documents the orthogonality: authority (Admin) and mode (Live) are
		// independent signals.
		{"global-flag-no-orgadmin", &auth.IAMClaims{Owner: "hanzo", IsGlobalAdmin: true}, admin},
	}
	for _, tc := range cases {
		got, err := strconv.ParseInt(permsHeader(tc.claims), 10, 64)
		if err != nil {
			t.Fatalf("%s: permsHeader returned non-integer: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: permsHeader=%d want %d (admin bit set iff global admin)", tc.name, got, tc.want)
		}
		// Belt-and-suspenders: the Admin bit is present iff the caller is a global admin.
		hasAdmin := got&admin != 0
		if hasAdmin != isGlobalAdmin(tc.claims) {
			t.Fatalf("%s: Admin bit=%v but isGlobalAdmin=%v — must match exactly", tc.name, hasAdmin, isGlobalAdmin(tc.claims))
		}
	}
}

// TestResolveBillingSubject_AdminOverride: a global admin may retarget the
// billing view to another org via ?org=, and the namespace follows it.
func TestResolveBillingSubject_AdminOverride(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/billing/balance?user=admin/z&org=hanzo&currency=usd", nil)
	subject, override := resolveBillingSubject(req, &auth.IAMClaims{Owner: "admin", IsGlobalAdmin: true})
	if subject != "hanzo" || !override {
		t.Fatalf("admin ?org=hanzo => subject=%q override=%v, want hanzo,true", subject, override)
	}
	if req.URL.Query().Has("org") {
		t.Fatalf("?org must be stripped from the query, got %q", req.URL.RawQuery)
	}
	if req.URL.Query().Get("currency") != "usd" {
		t.Fatalf("unrelated query param clobbered: %q", req.URL.RawQuery)
	}
}

// TestResolveBillingSubject_NonAdminIgnoresOverride is the isolation proof:
// a non-global-admin cannot use ?org= to read another org. The override is
// stripped and the subject stays pinned to the caller's own org.
func TestResolveBillingSubject_NonAdminIgnoresOverride(t *testing.T) {
	// Dave: org owner with IsAdmin=true (org-level), NOT a global admin.
	req := httptest.NewRequest("GET", "/v1/billing/balance?org=hanzo&currency=usd", nil)
	subject, override := resolveBillingSubject(req, &auth.IAMClaims{Owner: "maxpower", IsAdmin: true})
	if subject != "maxpower" || override {
		t.Fatalf("non-admin ?org=hanzo => subject=%q override=%v, want maxpower,false (isolation)", subject, override)
	}
	if req.URL.Query().Has("org") {
		t.Fatalf("?org must be stripped even for non-admins, got %q", req.URL.RawQuery)
	}
}

// TestResolveBillingSubject_NoOverride: without ?org, every caller (incl. a
// global admin) defaults to their own org and the namespace is untouched.
func TestResolveBillingSubject_NoOverride(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/billing/balance?currency=usd", nil)
	subject, override := resolveBillingSubject(req, &auth.IAMClaims{Owner: "admin", IsGlobalAdmin: true})
	if subject != "admin" || override {
		t.Fatalf("no ?org => subject=%q override=%v, want admin,false", subject, override)
	}
}

// TestResolveBillingSubject_RejectsBadSlug: a malformed ?org is discarded
// (treated as absent), so even an admin stays on their own org rather than a
// smuggled value.
func TestResolveBillingSubject_RejectsBadSlug(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/billing/balance?org=hanzo%2Fevil", nil)
	subject, override := resolveBillingSubject(req, &auth.IAMClaims{Owner: "admin", IsGlobalAdmin: true})
	if subject != "admin" || override {
		t.Fatalf("bad ?org slug => subject=%q override=%v, want admin,false", subject, override)
	}
}

func TestValidOrgSlug(t *testing.T) {
	good := []string{"hanzo", "admin", "max-power", "a", "org123"}
	bad := []string{"", "-lead", "Caps", "has space", "slash/x", "under_score", "dot.org"}
	for _, s := range good {
		if !validOrgSlug(s) {
			t.Fatalf("validOrgSlug(%q)=false, want true", s)
		}
	}
	for _, s := range bad {
		if validOrgSlug(s) {
			t.Fatalf("validOrgSlug(%q)=true, want false", s)
		}
	}
}
