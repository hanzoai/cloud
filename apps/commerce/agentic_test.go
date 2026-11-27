// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// The agent checkout protocols (agentic.go): nothing is reachable until the operator
// configures a protocol, each answers on its own configuration alone, the purchase
// discovery names only the ones that mounted, a configuration this code cannot honour
// is refused at start, and the buyer is the caller cloud validated. Every key here is
// minted by the test and every value is a test value; nothing is charged.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/commerce/agentic/acp"
	"github.com/hanzoai/commerce/agentic/ucp"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/readiness"
	"github.com/hanzoai/cloud/sdk/secret"
)

const (
	testOrigin = "https://api.example.test"
	testKeyRef = "orgs/hanzo/ucp/TEST_SIGNING_KEY"
)

// testKMS is a secret plane holding fixed values.
type testKMS map[string][]byte

func (k testKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	if v, ok := k[ref]; ok {
		return v, nil
	}
	return nil, secret.ErrNone
}
func (testKMS) PutSecret(context.Context, string, []byte) error { return errors.New("read only") }
func (testKMS) DeleteSecret(context.Context, string) error      { return errors.New("read only") }
func (testKMS) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("no signing")
}

// testSigningKey is a P-256 key minted for this test, as the PKCS#8 PEM KMS holds.
func testSigningKey(t *testing.T) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// acpConfig is an operator's ACP file with test values, edited by each mutation.
func acpConfig(t *testing.T, mutate ...func(map[string]any)) []byte {
	t.Helper()
	m := map[string]any{
		"origin":            testOrigin,
		"handler":           map[string]any{"id": "card_test", "psp": "stripe", "merchantId": "acct_test"},
		"order":             "https://console.example.test/billing/orders/{id}",
		"retryAfterSeconds": 2,
		"maxBodyBytes":      65536,
		"links":             []map[string]any{{"type": "terms_of_use", "url": "https://example.test/terms"}},
	}
	for _, f := range mutate {
		f(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ucpConfig is an operator's UCP file with test values, edited by each mutation.
func ucpConfig(t *testing.T, mutate ...func(map[string]any)) []byte {
	t.Helper()
	m := map[string]any{
		"origin": testOrigin,
		"handlers": map[string]any{"com.example.shared_payment_token": []map[string]any{{
			"id": "spt_test", "version": ucp.Version, "config": map[string]any{"network_id": "profile_test"},
			"available_instruments": []map[string]any{{"type": "card"}},
		}}},
		"handler":          "spt_test",
		"order":            "https://console.example.test/billing/orders/{id}",
		"orders":           true,
		"signingKey":       map[string]any{"kid": "ucp-test-1", "secret": testKeyRef},
		"trusted":          []string{"https://platform.example.test/profile.json"},
		"requireSignature": true,
		"signatures":       map[string]any{"windowSeconds": 300, "skewSeconds": 30},
		"fetch":            map[string]any{"timeoutSeconds": 5, "entries": 64, "maxTtlSeconds": 3600, "concurrent": 4},
		"maxBodyBytes":     65536,
	}
	for _, f := range mutate {
		f(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// configure writes raw to a file and names it in env, as an operator would.
func configure(t *testing.T, env string, raw []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(env, path)
}

// agenticApp mounts the whole commerce surface through the real Use, embed included,
// with the secret plane given.
func agenticApp(t *testing.T, kms secret.Store) *zip.App {
	t.Helper()
	agenticLinks.Store(nil)
	t.Cleanup(func() { agenticLinks.Store(nil) })
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	t.Setenv("CLOUD_BRAND", "hanzo")
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{KMS: kms}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if currentEmbedded() == nil {
		t.Fatal("the commerce embed did not boot, so nothing agentic was asked to mount")
	}
	return app
}

// hit makes one request of app and answers its status, headers and body.
func hit(t *testing.T, app *zip.App, method, path, body string, header map[string]string) (int, http.Header, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Host = "api.example.test"
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

// purchaseLinks are the links the purchase discovery (GET /v1/billing/discovery) answers.
func purchaseLinks(t *testing.T) map[string]string {
	t.Helper()
	out, err := planeDiscovery(context.Background(), nil)
	if err != nil {
		t.Fatalf("purchase discovery: %v", err)
	}
	var doc struct {
		Links map[string]string `json:"links"`
	}
	if err := json.Unmarshal(out.Body, &doc); err != nil {
		t.Fatalf("purchase discovery: %v: %s", err, out.Body)
	}
	if doc.Links["checkouts"] == "" {
		t.Fatalf("purchase discovery lost its own links: %s", out.Body)
	}
	return doc.Links
}

// acpHeaders are what an ACP agent sends on a POST, less the credential.
func acpHeaders(extra map[string]string) map[string]string {
	h := map[string]string{
		"API-Version":     acp.Version,
		"Authorization":   "Bearer sk-test",
		"Content-Type":    "application/json",
		"Idempotency-Key": "idem-test-0001",
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// protocolAddresses are one request at every address a protocol would answer.
var protocolAddresses = []struct{ method, path string }{
	{http.MethodGet, "/.well-known/acp.json"},
	{http.MethodPost, "/v1/commerce/acp/checkout_sessions"},
	{http.MethodGet, "/v1/commerce/acp/checkout_sessions/cs_1"},
	{http.MethodGet, "/.well-known/ucp"},
	{http.MethodPost, "/v1/commerce/ucp/checkout-sessions"},
	{http.MethodGet, "/v1/commerce/ucp/orders/ord_1"},
}

// With no protocol configured, as in production today, nothing the protocols would
// serve answers: every address is 404, with or without a credential, and the purchase
// discovery names no protocol.
func TestNoProtocolIsReachableUntilConfigured(t *testing.T) {
	t.Setenv(acpConfigEnv, "")
	t.Setenv(ucpConfigEnv, "")
	app := agenticApp(t, testKMS{testKeyRef: testSigningKey(t)})
	for _, a := range protocolAddresses {
		for name, h := range map[string]map[string]string{
			"anonymous": nil,
			"signed in": acpHeaders(map[string]string{"X-User-Id": "wile", "X-Org-Id": "acme"}),
		} {
			code, _, body := hit(t, app, a.method, a.path, `{}`, h)
			if code != http.StatusNotFound {
				t.Errorf("%s %s (%s) with no protocol configured: %d %s, want 404", a.method, a.path, name, code, body)
			}
		}
	}
	links := purchaseLinks(t)
	for _, p := range []string{"acp", "ucp"} {
		if l, ok := links[p]; ok {
			t.Errorf("the purchase discovery names %s at %q with nothing configured", p, l)
		}
	}
}

// Each protocol mounts on its own configuration and on nothing else: ACP alone serves
// ACP and leaves UCP's addresses 404, and the other way round, and the purchase
// discovery names only the one that mounted.
func TestEachProtocolAnswersOnItsOwnConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		acp, ucp bool
	}{{"acp only", true, false}, {"ucp only", false, true}, {"both", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(acpConfigEnv, "")
			t.Setenv(ucpConfigEnv, "")
			if tc.acp {
				configure(t, acpConfigEnv, acpConfig(t))
			}
			if tc.ucp {
				configure(t, ucpConfigEnv, ucpConfig(t))
			}
			app := agenticApp(t, testKMS{testKeyRef: testSigningKey(t)})
			links := purchaseLinks(t)

			code, hdr, body := hit(t, app, http.MethodGet, "/.well-known/acp.json", "", nil)
			if !tc.acp {
				if code != http.StatusNotFound {
					t.Errorf("acp.json without ACP configured: %d %s, want 404", code, body)
				}
				if _, ok := links["acp"]; ok {
					t.Errorf("the purchase discovery names ACP, which is not configured: %v", links)
				}
			} else {
				var doc acp.Discovery
				if code != http.StatusOK || json.Unmarshal(body, &doc) != nil {
					t.Fatalf("acp.json: %d %s", code, body)
				}
				if doc.APIBaseURL != testOrigin+"/v1/commerce/acp" || doc.Protocol.Name != "acp" || doc.Protocol.Version != acp.Version {
					t.Errorf("acp.json = %s", body)
				}
				if cc := hdr.Get("Cache-Control"); !strings.Contains(cc, "public") {
					t.Errorf("acp.json Cache-Control = %q, want public", cc)
				}
				if links["acp"] != "/.well-known/acp.json" {
					t.Errorf("the purchase discovery does not name ACP: %v", links)
				}
				// The API answers in ACP's own words: a request with no validated
				// caller is ACP's 401, never a 404 or another app's refusal.
				code, _, body = hit(t, app, http.MethodPost, "/v1/commerce/acp/checkout_sessions",
					`{"line_items":[{"id":"credit-1000"}],"currency":"usd","capabilities":{}}`, acpHeaders(nil))
				var f struct {
					Type, Code string
				}
				if code != http.StatusUnauthorized || json.Unmarshal(body, &f) != nil || f.Code != "unauthorized" {
					t.Errorf("ACP create with no validated caller: %d %s, want ACP's 401 unauthorized", code, body)
				}
			}

			code, hdr, body = hit(t, app, http.MethodGet, "/.well-known/ucp", "", nil)
			if !tc.ucp {
				if code != http.StatusNotFound {
					t.Errorf("the UCP profile without UCP configured: %d %s, want 404", code, body)
				}
				if _, ok := links["ucp"]; ok {
					t.Errorf("the purchase discovery names UCP, which is not configured: %v", links)
				}
			} else {
				var p ucp.Profile
				if code != http.StatusOK || json.Unmarshal(body, &p) != nil {
					t.Fatalf("the UCP profile: %d %s", code, body)
				}
				svc := p.UCP.Services[ucp.ServiceShopping]
				if len(svc) != 1 || svc[0].Endpoint != testOrigin+"/v1/commerce/ucp" {
					t.Errorf("the profile's REST endpoint = %+v, want %s/v1/commerce/ucp", svc, testOrigin)
				}
				if len(p.Keys) != 1 || p.Keys[0].KID != "ucp-test-1" || p.Keys[0].CRV != "P-256" {
					t.Errorf("the profile publishes keys %+v, want the business key ucp-test-1", p.Keys)
				}
				if _, ok := p.UCP.Capabilities[ucp.CapOrder]; !ok {
					t.Errorf("orders: true, and the profile does not advertise %s", ucp.CapOrder)
				}
				if hs := p.UCP.PaymentHandlers["com.example.shared_payment_token"]; len(hs) != 1 || hs[0].ID != "spt_test" {
					t.Errorf("the profile advertises handlers %+v", p.UCP.PaymentHandlers)
				}
				if hdr.Get("ETag") == "" {
					t.Error("the profile carries no ETag")
				}
				if links["ucp"] != "/.well-known/ucp" {
					t.Errorf("the purchase discovery does not name UCP: %v", links)
				}
				code, _, body = hit(t, app, http.MethodPost, "/v1/commerce/ucp/checkout-sessions", `{}`,
					map[string]string{"Content-Type": "application/json"})
				var e ucp.Error
				if code != http.StatusUnauthorized || json.Unmarshal(body, &e) != nil || e.Code != "unauthorized" {
					t.Errorf("UCP create with no validated caller: %d %s, want UCP's 401 unauthorized", code, body)
				}
			}
		})
	}
}

// A file the code cannot honour is refused at start: the protocol is not mounted, so
// its addresses stay 404 and the purchase discovery does not name it, and the
// deployment reports it degraded on /v1/health, which the release smoke refuses.
func TestABadConfigurationIsRefusedAtStart(t *testing.T) {
	t.Setenv(acpConfigEnv, "")
	t.Setenv(ucpConfigEnv, "")
	configure(t, acpConfigEnv, acpConfig(t, func(m map[string]any) { m["retryAfter"] = 2 })) // a misspelled key
	configure(t, ucpConfigEnv, ucpConfig(t))
	// No secret plane: the business key cannot be read, so UCP cannot sign.
	app := agenticApp(t, nil)
	for _, p := range []string{"/.well-known/acp.json", "/.well-known/ucp"} {
		if code, _, body := hit(t, app, http.MethodGet, p, "", nil); code != http.StatusNotFound {
			t.Errorf("%s from a refused configuration: %d %s, want 404", p, code, body)
		}
	}
	links := purchaseLinks(t)
	for _, p := range []string{"acp", "ucp"} {
		if _, ok := links[p]; ok {
			t.Errorf("the purchase discovery names %s, whose configuration was refused", p)
		}
		if why := readiness.Degradations()["commerce/"+p]; why == "" {
			t.Errorf("commerce/%s is not reported degraded", p)
		}
	}
}

// Every value an operator must supply is refused when it is left out or cannot be
// honoured; none is defaulted (HIP-1223 §1.3).
func TestEveryRequiredValueIsTheOperators(t *testing.T) {
	drop := func(k string) func(map[string]any) { return func(m map[string]any) { delete(m, k) } }
	set := func(k string, v any) func(map[string]any) { return func(m map[string]any) { m[k] = v } }
	nop := func(error) {}

	for name, mutate := range map[string]func(map[string]any){
		"no origin":            drop("origin"),
		"an http origin":       set("origin", "http://api.example.test"),
		"an origin with path":  set("origin", testOrigin+"/v1"),
		"no handler":           drop("handler"),
		"a handler not stripe": set("handler", map[string]any{"id": "card_test", "psp": "adyen", "merchantId": "acct_test"}),
		"no merchant account":  set("handler", map[string]any{"id": "card_test", "psp": "stripe"}),
		"no order page":        drop("order"),
		"an order page no id":  set("order", "https://console.example.test/orders"),
		"no retry interval":    drop("retryAfterSeconds"),
		"no body bound":        drop("maxBodyBytes"),
		"an unknown field":     set("retryAfter", 2),
		"a link over http":     set("links", []map[string]any{{"type": "terms_of_use", "url": "http://example.test/terms"}}),
	} {
		if _, err := newACP(acpConfig(t, mutate), acp.Commerce{}, testLedger{}, nop); err == nil {
			t.Errorf("ACP accepted a file with %s", name)
		}
	}
	if _, err := newACP(acpConfig(t), acp.Commerce{}, testLedger{}, nop); err != nil {
		t.Fatalf("ACP refused the complete test file: %v", err)
	}

	kms := testKMS{testKeyRef: testSigningKey(t)}
	fault := func(string, error) {}
	ctx := context.Background()
	for name, mutate := range map[string]func(map[string]any){
		"no origin":             drop("origin"),
		"no handlers":           drop("handlers"),
		"a handler not offered": set("handler", "gpay_test"),
		"no order page":         drop("order"),
		"no trusted platforms":  drop("trusted"),
		"an http platform":      set("trusted", []string{"http://platform.example.test/profile.json"}),
		"no signing key":        drop("signingKey"),
		"a key KMS lacks":       set("signingKey", map[string]any{"kid": "k", "secret": "orgs/hanzo/ucp/MISSING"}),
		"no signature window":   drop("signatures"),
		"no fetch bounds":       drop("fetch"),
		"a cache under 60s":     set("fetch", map[string]any{"timeoutSeconds": 5, "entries": 64, "maxTtlSeconds": 30, "concurrent": 4}),
		"no body bound":         drop("maxBodyBytes"),
		"webhooks, no orders": func(m map[string]any) {
			m["orders"] = false
			m["webhooks"] = map[string]any{"attempts": 3, "backoffSeconds": 1, "timeoutSeconds": 5}
		},
		"an unknown field": set("trust", []string{"https://platform.example.test/profile.json"}),
	} {
		if _, err := newUCP(ctx, ucpConfig(t, mutate), kms, ucp.Idempotency{}, fault); err == nil {
			t.Errorf("UCP accepted a file with %s", name)
		}
	}
	if _, err := newUCP(ctx, ucpConfig(t), nil, ucp.Idempotency{}, fault); err == nil {
		t.Error("UCP mounted with no secret plane to read its signing key from")
	}
	if _, err := newUCP(ctx, ucpConfig(t), kms, ucp.Idempotency{}, fault); err != nil {
		t.Fatalf("UCP refused the complete test file: %v", err)
	}
}

// testLedger claims every key afresh and keeps nothing.
type testLedger struct{}

func (testLedger) Begin(context.Context, string, string, string) (acp.Verdict, acp.Stored, acp.Lease, error) {
	return acp.Fresh, acp.Stored{}, testLease{}, nil
}

type testLease struct{}

func (testLease) Settle(context.Context, acp.Stored) error { return nil }
func (testLease) Release(context.Context) error            { return nil }

// testBackend records who opened a checkout and sells one test item.
type testBackend struct{ opened []acp.Caller }

func (*testBackend) Currency() string { return "usd" }
func (b *testBackend) Open(_ context.Context, who acp.Caller, item string) (*acp.Checkout, error) {
	b.opened = append(b.opened, who)
	return &acp.Checkout{ID: "cs_test_1", State: acp.Open, Item: acp.Product{ID: item, Name: "$10.00 USD credit"}, Total: 1000}, nil
}
func (*testBackend) Change(context.Context, acp.Caller, string, string) (*acp.Checkout, error) {
	return nil, acp.ErrNoItem
}
func (*testBackend) Read(context.Context, acp.Caller, string) (*acp.Checkout, error) {
	return nil, acp.ErrNoItem
}
func (*testBackend) Pay(context.Context, acp.Caller, string, string, acp.Charge) (*acp.Checkout, error) {
	return nil, errors.New("the test backend charges nothing")
}
func (*testBackend) Cancel(context.Context, acp.Caller, string) (*acp.Checkout, error) {
	return nil, acp.ErrNoItem
}

// The buyer is the caller cloud's boundary validated, handed to the protocol through
// the request: no validated principal, or an org with no user behind it, reaches the
// backend as nobody and is refused in the protocol's 401; a validated caller reaches
// it as that org, the wallet within it and the host it addressed. A UCP platform the
// operator did not list is refused before its profile is fetched.
func TestTheBuyerIsTheValidatedCaller(t *testing.T) {
	backend := &testBackend{}
	a, err := newACP(acpConfig(t), backend, testLedger{}, func(err error) { t.Errorf("acp reported: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	u, err := newUCP(context.Background(), ucpConfig(t), testKMS{testKeyRef: testSigningKey(t)}, ucp.Idempotency{},
		func(op string, err error) { t.Errorf("ucp %s: %v", op, err) })
	if err != nil {
		t.Fatal(err)
	}
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger(), ErrorHandler: cloud.ErrorHandler})
	app.Use(cloud.Bridge())
	a.mount(app)
	u.mount(app)

	create := `{"line_items":[{"id":"credit-1000"}],"currency":"usd","capabilities":{}}`
	for name, who := range map[string]map[string]string{
		"no principal":           nil,
		"an org and no user":     {"X-Org-Id": "acme"},
		"a user with no org yet": {"X-User-Id": "wile"},
	} {
		code, _, body := hit(t, app, http.MethodPost, "/v1/commerce/acp/checkout_sessions", create, acpHeaders(who))
		if code != http.StatusUnauthorized {
			t.Errorf("ACP create, %s: %d %s, want 401", name, code, body)
		}
	}
	if len(backend.opened) != 0 {
		t.Fatalf("the backend opened a checkout for an unvalidated caller: %+v", backend.opened)
	}

	code, _, body := hit(t, app, http.MethodPost, "/v1/commerce/acp/checkout_sessions", create,
		acpHeaders(map[string]string{"X-User-Id": "wile", "X-Org-Id": "acme"}))
	if code != http.StatusCreated {
		t.Fatalf("ACP create as a validated caller: %d %s", code, body)
	}
	if len(backend.opened) != 1 {
		t.Fatalf("the backend opened %d checkouts, want 1", len(backend.opened))
	}
	if who := backend.opened[0]; who.Org != "acme" || who.Subject == "" || who.Host != "api.example.test" {
		t.Errorf("the backend was asked for %+v, want org acme, a wallet within it, host api.example.test", who)
	}
	var session acp.CheckoutSession
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatal(err)
	}
	hs := session.Capabilities.Payment
	if hs == nil || len(hs.Handlers) != 1 || hs.Handlers[0].ID != "card_test" || hs.Handlers[0].PSP != "stripe" ||
		hs.Handlers[0].Config["merchant_id"] != "acct_test" {
		t.Errorf("the session declares handlers %+v, want the operator's card_test at stripe for acct_test", hs)
	}

	code, _, body = hit(t, app, http.MethodPost, "/v1/commerce/ucp/checkout-sessions", `{}`, map[string]string{
		"Content-Type": "application/json", "X-User-Id": "wile", "X-Org-Id": "acme",
		ucp.HeaderAgent: `profile="https://stranger.example.test/profile.json"`,
	})
	var e ucp.Error
	if code != http.StatusForbidden || json.Unmarshal(body, &e) != nil || e.Code != ucp.CodeProfileNotTrusted {
		t.Errorf("UCP create from a platform the operator did not list: %d %s, want 403 %s", code, body, ucp.CodeProfileNotTrusted)
	}
}

// Configured, ACP runs on commerce's own checkout, end to end through the embed: a
// validated caller opens a checkout for a catalog credit pack, reads it back as the same
// buyer, and cancels it. Nothing is completed, so nothing is charged. Another org's
// caller cannot read it.
func TestAnACPCheckoutIsCommercesCheckout(t *testing.T) {
	t.Setenv(acpConfigEnv, "")
	t.Setenv(ucpConfigEnv, "")
	configure(t, acpConfigEnv, acpConfig(t))
	app := agenticApp(t, nil)
	buyerHeaders := func(key string) map[string]string {
		return acpHeaders(map[string]string{"X-User-Id": "wile", "X-Org-Id": "acme", "Idempotency-Key": key})
	}

	code, _, body := hit(t, app, http.MethodPost, "/v1/commerce/acp/checkout_sessions",
		`{"line_items":[{"id":"credit-1000"}],"currency":"usd","capabilities":{}}`, buyerHeaders("idem-open-0001"))
	var s acp.CheckoutSession
	if code != http.StatusCreated || json.Unmarshal(body, &s) != nil || s.ID == "" {
		t.Fatalf("ACP create for credit-1000: %d %s", code, body)
	}
	if s.Status != acp.StatusReadyForPayment || s.Currency != "usd" || len(s.LineItems) != 1 || s.LineItems[0].Item.ID != "credit-1000" {
		t.Errorf("the session = %s", body)
	}
	var total int64 = -1
	for _, x := range s.Totals {
		if x.Type == "total" {
			total = x.Amount
		}
	}
	if total != 1000 {
		t.Errorf("the session totals %d, want the catalog's 1000: %s", total, body)
	}

	code, _, body = hit(t, app, http.MethodGet, "/v1/commerce/acp/checkout_sessions/"+s.ID, "", buyerHeaders(""))
	if code != http.StatusOK || !strings.Contains(string(body), `"id":"`+s.ID+`"`) {
		t.Errorf("the buyer reading their own checkout: %d %s", code, body)
	}
	code, _, body = hit(t, app, http.MethodGet, "/v1/commerce/acp/checkout_sessions/"+s.ID, "",
		acpHeaders(map[string]string{"X-User-Id": "road", "X-Org-Id": "runner"}))
	if code == http.StatusOK {
		t.Errorf("another org read the checkout: %d %s", code, body)
	}

	code, _, body = hit(t, app, http.MethodPost, "/v1/commerce/acp/checkout_sessions/"+s.ID+"/cancel", "", buyerHeaders("idem-cancel-0001"))
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"`+acp.StatusCanceled+`"`) {
		t.Errorf("cancel: %d %s", code, body)
	}
}

// The light host routes by manifest.Apps, so an address commerce serves and the host
// hands to another app answers that app's 404. Every protocol address reaches commerce,
// and commerce's row claims no well-known file it does not serve.
func TestTheProtocolDocumentsAreRoutedToCommerce(t *testing.T) {
	for _, p := range append(slices.Clone(WellKnown), acpBase+"/checkout_sessions/cs_1/complete", ucpBase+"/checkout-sessions", ucpBase+"/orders/ord_1") {
		if got := manifest.OwnerOf(p); got != "commerce" {
			t.Errorf("%s routes to %q, want commerce", p, got)
		}
	}
	var claimed []string
	for _, p := range manifest.PrefixesFor("commerce") {
		if strings.HasPrefix(p, "/.well-known/") {
			claimed = append(claimed, p)
		}
	}
	slices.Sort(claimed)
	want := slices.Sorted(slices.Values(WellKnown))
	if !slices.Equal(claimed, want) {
		t.Errorf("commerce's row claims the well-known files %v; commerce serves %v", claimed, want)
	}
}

type fakeUCPSweeper struct {
	mu    sync.Mutex
	calls int
	limit int
	n     int
	err   error
	hit   chan struct{}
}

func (f *fakeUCPSweeper) Sweep(_ context.Context, limit int) (int, error) {
	f.mu.Lock()
	f.calls++
	f.limit = limit
	hit := f.hit
	f.mu.Unlock()
	if hit != nil {
		select {
		case hit <- struct{}{}:
		default:
		}
	}
	return f.n, f.err
}

func TestUCPSweepRunsWithBoundedBatch(t *testing.T) {
	sw := &fakeUCPSweeper{n: 3}
	sweepUCPOnce(context.Background(), nil, luxlog.New("test"), sw, ucpSweepBatch)
	if sw.calls != 1 || sw.limit != ucpSweepBatch {
		t.Fatalf("sweepUCPOnce calls=%d limit=%d, want 1 and %d", sw.calls, sw.limit, ucpSweepBatch)
	}
}

func TestStartUCPSweepTicksAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sw := &fakeUCPSweeper{n: 1, hit: make(chan struct{}, 2)}
	startUCPSweep(ctx, nil, luxlog.New("test"), sw, 5*time.Millisecond, 25)
	select {
	case <-sw.hit:
	case <-time.After(time.Second):
		t.Fatal("startUCPSweep did not tick within 1s")
	}
	cancel()
	sw.mu.Lock()
	limit := sw.limit
	sw.mu.Unlock()
	if limit != 25 {
		t.Fatalf("startUCPSweep limit=%d, want 25", limit)
	}
}
