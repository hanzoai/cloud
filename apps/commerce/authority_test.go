// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// The price authority's HTTP write addresses — a rate, a first-party catalog entry —
// measured through the pipeline serve.go runs in front of them:
// the identity boundary, then the audit trail, then commerce's own mount. The
// tokens are signed and verified against a JWKS, so every identity header the
// handlers read is the one the boundary minted, never one the caller sent.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/authz"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
)

const authorityIssuer = "https://test.iam"

// authorityApp mounts the boundary, the trail and the real commerce surface, and
// returns the app, the trail, and a signer for a person whose membership set is
// orgs (home first).
func authorityApp(t *testing.T) (*zip.App, *audit.Recorder, func(sub string, orgs ...authz.Membership) string) {
	t.Helper()
	app, rec, signClaims := authorityHarness(t)
	sign := func(sub string, orgs ...authz.Membership) string {
		t.Helper()
		return signClaims(authz.Claims{
			RegisteredClaims:  jwt.RegisteredClaims{Subject: sub},
			Owner:             orgs[0].Org,
			PreferredUsername: sub,
			Email:             sub + "@example.test",
			Orgs:              orgs,
		})
	}
	return app, rec, sign
}

// authorityHarness is authorityApp with a signer for any claim set: the issuer,
// audience and lifetime are filled in, everything else is the caller's.
func authorityHarness(t *testing.T) (*zip.App, *audit.Recorder, func(authz.Claims) string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	jwks, err := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatalf("jwks: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jwks) }))
	t.Cleanup(srv.Close)

	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	t.Setenv("CLOUD_BRAND", "hanzo")
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.IdentityMiddleware(&cloud.Config{IAMIssuer: authorityIssuer, Discovery: oidctest.Discovery(authorityIssuer), DiscoveryClient: oidctest.Client(authorityIssuer, srv.URL)}))
	app.Use(cloud.AuditTrail(rec))
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if currentEmbedded() == nil {
		t.Fatal("the commerce embed did not boot, so these routes are the fail-closed wildcard")
	}

	sign := func(c authz.Claims) string {
		t.Helper()
		c.Issuer = authorityIssuer
		c.Audience = jwt.ClaimStrings{"hanzo-cloud"}
		c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
		c.IssuedAt = jwt.NewNumericDate(time.Now())
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		tok.Header["kid"] = "test-key"
		raw, err := tok.SignedString(key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return raw
	}
	return app, rec, sign
}

// drive sends one request; tok may be empty, and hdr carries whatever else the
// caller chose to send.
func drive(t *testing.T, app *zip.App, method, path, tok, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// write is one price-write address and the row it edits, created first. A plan is
// written through the admin app's typed ops, not here (authority_rpc.go).
type write struct {
	create, put string // POST path and PUT path
	row, edit   string // POST body and PUT body
}

var writes = []write{
	{"/v1/commerce/rates/entries", "/v1/commerce/rates/entries/probe/unit",
		`{"product":"probe","meter":"unit","unit":"call"}`, `{"product":"probe","meter":"unit","unit":"call","label":"edited"}`},
	{"/v1/commerce/catalog/entries", "/v1/commerce/catalog/entries/probe",
		`{"slug":"probe","name":"Probe","category":"zen"}`, `{"name":"Probe edited","category":"zen"}`},
}

// An org admin of a brand org is refused every price write — including one who
// names the admin org as the org to act in, or forges the home-org header, and
// one who also holds a membership of the admin org — so is a caller with no
// identity the boundary can validate, whatever org it names; a person whose own
// org is admin is admitted, and every attempt either way is on the trail.
func TestPriceWritesAreSuperAdminOnlyAndAudited(t *testing.T) {
	app, rec, sign := authorityApp(t)
	super := sign("z", authz.Membership{Org: "admin", Role: authz.Owner})
	brand := sign("maxpower", authz.Membership{Org: "hanzo", Role: authz.Owner})
	member := sign("maxpower", authz.Membership{Org: "hanzo", Role: authz.Owner}, authz.Membership{Org: "admin", Role: authz.Admin})

	for _, w := range writes {
		if code, body := drive(t, app, http.MethodPost, w.create, super, w.row, nil); code != http.StatusCreated {
			t.Fatalf("POST %s as SuperAdmin: %d %s", w.create, code, body)
		}

		for name, hdr := range map[string]map[string]string{
			"own org":               nil,
			"X-Org-Id admin":        {"X-Org-Id": "admin"},
			"forged X-User-Owner":   {"X-Org-Id": "admin", "X-User-Owner": "admin"},
			"forged X-User-IsAdmin": {"X-Org-Id": "admin", "X-User-IsAdmin": "true"},
		} {
			if code, body := drive(t, app, http.MethodPut, w.put, brand, w.edit, hdr); code != http.StatusForbidden {
				t.Errorf("PUT %s as a brand org admin (%s): %d %s, want 403", w.put, name, code, body)
			}
		}

		for name, hdr := range map[string]map[string]string{
			"own org":        nil,
			"X-Org-Id admin": {"X-Org-Id": "admin"},
		} {
			if code, body := drive(t, app, http.MethodPut, w.put, member, w.edit, hdr); code != http.StatusForbidden {
				t.Errorf("PUT %s as a brand owner who is a member of admin (%s): %d %s, want 403", w.put, name, code, body)
			}
		}

		for name, tok := range map[string]string{"no credential": "", "opaque bearer": "not-a-jwt"} {
			code, body := drive(t, app, http.MethodPut, w.put, tok, w.edit, map[string]string{"X-Org-Id": "admin"})
			if code != http.StatusUnauthorized && code != http.StatusForbidden {
				t.Errorf("PUT %s with %s naming the admin org: %d %s, want 401 or 403", w.put, name, code, body)
			}
		}

		if code, body := drive(t, app, http.MethodPut, w.put, super, w.edit, nil); code != http.StatusOK {
			t.Errorf("PUT %s as SuperAdmin: %d %s, want 200", w.put, code, body)
		}

		rows, _, err := rec.Query(context.Background(), audit.Filter{Limit: 1000})
		if err != nil {
			t.Fatalf("audit query: %v", err)
		}
		denied, admitted := 0, 0
		for _, r := range rows {
			if r.Method != http.MethodPut || r.Path != w.put {
				continue
			}
			switch {
			case r.Outcome.Status == http.StatusForbidden && r.Actor.Sub == "maxpower" && r.Actor.Org == "hanzo":
				denied++
			case r.Outcome.Status == http.StatusOK && r.Actor.Sub == "z" && r.Actor.Org == "admin":
				admitted++
			}
		}
		if denied != 6 || admitted != 1 {
			t.Errorf("PUT %s on the trail: %d denials by maxpower@hanzo and %d admits by z@admin, want 6 and 1",
				w.put, denied, admitted)
		}
	}
}

// A request no credential stands behind names no org, so commerce reads it as
// nobody whatever X-Org-Id it carries: the store record and its entitlement are
// the org's own, answered to the org's own principals.
func TestAnOrgAloneReadsNothing(t *testing.T) {
	app, _, sign := authorityApp(t)
	for _, path := range []string{"/v1/commerce/store/current", "/v1/commerce/store/access"} {
		for name, tok := range map[string]string{"no credential": "", "opaque bearer": "not-a-jwt"} {
			code, body := drive(t, app, http.MethodGet, path, tok, "", map[string]string{"X-Org-Id": "acme"})
			if code != http.StatusUnauthorized && code != http.StatusForbidden {
				t.Errorf("GET %s with %s naming an org: %d %s, want 401 or 403", path, name, code, body)
			}
		}
		own := sign("wile", authz.Membership{Org: "acme", Role: authz.Owner})
		if code, body := drive(t, app, http.MethodGet, path, own, "", nil); code != http.StatusOK {
			t.Errorf("GET %s as the org's own owner: %d %s, want 200", path, code, body)
		}
	}
}

// The platform's sweeps — the catalog refresh, subscription renewal, balance
// recharge — are plane operations. The internal plane is its own app on the pod's
// own socket and no route on the edge reaches it, so the org a sweep checks is a
// sibling's statement and never a client's. Asked on the edge by name and by path,
// with every SuperAdmin header forged and with or without a brand token, none of
// them answers; asked on the plane by a brand org, the scheduled sweeps refuse it.
func TestPlatformSweepsAnswerOnlyOnThePlane(t *testing.T) {
	app, _, sign := authorityApp(t)
	brand := sign("wile", authz.Membership{Org: "acme", Role: authz.Owner}, authz.Membership{Org: "admin", Role: authz.Admin})
	forged := map[string]string{
		"X-Org-Id": "admin", "X-User-Id": "admin/z", "X-User-Owner": "admin",
		"X-User-IsAdmin": "true", "X-User-IsOrgAdmin": "true",
	}
	sweeps := []struct{ op, path string }{
		{client.CatalogRefresh, "/commerce/catalog/refresh"},
		{client.BillingRenew, "/billing/renew"},
		{client.BillingRecharge, "/billing/recharge"},
	}
	for _, sw := range sweeps {
		for _, path := range []string{sw.path, "/v1" + sw.path, "/.well-known/zip/op/" + sw.op} {
			for name, tok := range map[string]string{"no credential": "", "brand token": brand} {
				code, body := drive(t, app, http.MethodPost, path, tok, "{}", forged)
				if code >= 200 && code < 300 {
					t.Errorf("POST %s on the edge (%s, forged SuperAdmin headers): %d %s — a plane sweep answered a client",
						path, name, code, body)
				}
			}
		}

	}

	// The scheduled sweeps on the plane itself, alone on a fresh one. The recharge
	// sweep's one door on the edge, /v1/billing/recharge/run-all, asks
	// principal.Super before it reaches here.
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	cloud.Plane().Post("/billing/renew", planeRenew, zip.WithOperationID(client.BillingRenew))
	exposeCatalogRefresh()
	for _, op := range []string{"/billing/renew", "/commerce/catalog/refresh"} {
		req := httptest.NewRequest(http.MethodPost, op, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", "wile")
		resp, err := cloud.Plane().Test(req, zip.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("plane %s: %v", op, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("plane %s as a brand org: %d, want 403 — the sweep is the platform's", op, resp.StatusCode)
		}
	}
}
