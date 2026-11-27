package billing

// POST /v1/billing/usage, driven end to end: a REAL RSA-signed IAM token through
// the REAL identity boundary (cloud.IdentityMiddleware), into the typed op, down
// the one meter, and into a real finance ledger — then read back through GET
// /v1/billing/balance with the same credential. Nothing between the token and the
// ledger row is stubbed, because the properties under test live in that gap: which
// org a machine acts in, whose wallet the debit draws, and whether a retry moves
// money twice.

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
	csrfkey "github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/finance"
	credit "github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
	"github.com/hanzoai/authz"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const recordIssuer = "https://record.test.iam"

// issuer signs tokens the boundary under test will verify.
type issuer struct {
	t   *testing.T
	key *rsa.PrivateKey
	url string
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	body, err := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "record-key", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatalf("jwks: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(srv.Close)
	return &issuer{t: t, key: key, url: srv.URL}
}

func (is *issuer) sign(c authz.Claims) string {
	is.t.Helper()
	c.Issuer = recordIssuer
	c.IssuedAt = jwt.NewNumericDate(time.Now())
	c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	tok.Header["kid"] = "record-key"
	raw, err := tok.SignedString(is.key)
	if err != nil {
		is.t.Fatalf("sign: %v", err)
	}
	return raw
}

// application is the token IAM mints on the client_credentials grant: the app is
// the subject, `type` is Program, `owner` is its own org, `billing_account` its
// own org's account, and `orgs` only the memberships other orgs granted it.
func (is *issuer) application(name, owner string, granted ...string) string {
	c := authz.Claims{
		Owner: owner, Organization: owner, Azp: owner + "-" + name,
		PreferredUsername: owner + "-" + name,
		Type:              authz.Program,
		BillingAccount:    account.Org(owner).String(),
	}
	c.Subject = "admin/" + owner + "-" + name
	c.Audience = jwt.ClaimStrings{owner + "-" + name}
	for _, org := range granted {
		c.Orgs = append(c.Orgs, authz.Membership{Org: org, Role: authz.Member})
	}
	return is.sign(c)
}

// person is an ordinary signed-in human, owner of their org.
func (is *issuer) person(name, org string) string {
	c := authz.Claims{
		Owner: org, Organization: org, PreferredUsername: name,
		Orgs: []authz.Membership{{Org: org, Role: authz.Owner}},
	}
	c.Subject = org + "/" + name
	c.Audience = jwt.ClaimStrings{"hanzo-console"}
	return is.sign(c)
}

// books publishes a real per-org finance ledger for the test.
func books(t *testing.T) credit.FinanceClient {
	t.Helper()
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	return fin
}

// fund deposits cents into org's pool.
func fund(t *testing.T, fin credit.FinanceClient, org string, cents int64) {
	t.Helper()
	if _, err := fin.Deposit(context.Background(), credit.DepositInput{
		Org: org, Subject: org, Amount: credit.FromCents(cents), Currency: "usd", Ref: "fund-" + org,
	}); err != nil {
		t.Fatalf("fund %s: %v", org, err)
	}
}

func pool(t *testing.T, fin credit.FinanceClient, org string) int64 {
	t.Helper()
	bal, err := fin.Balance(context.Background(), org, org, "usd", false)
	if err != nil {
		t.Fatalf("balance %s: %v", org, err)
	}
	return bal.Cents()
}

// recordApp is the billing surface behind the real identity boundary, with the
// one meter every surface debits through.
func recordApp(t *testing.T, is *issuer) *zip.App {
	t.Helper()
	t.Setenv(csrfkey.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("CLOUD_BRAND", "hanzo")
	m, err := meter.New(meter.Config{})
	if err != nil {
		t.Fatalf("meter: %v", err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.IdentityMiddleware(&cloud.Config{IAMIssuer: recordIssuer, Discovery: oidctest.Discovery(recordIssuer), DiscoveryClient: oidctest.Client(recordIssuer, is.url)}))
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{Metering: m}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app
}

func send(t *testing.T, app *zip.App, method, path, token, org, body string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	out["_raw"] = string(raw)
	return resp.StatusCode, out
}

func report(id, org, decimal string) string {
	return `{"id":"` + id + `","org":"` + org + `","amount":{"decimal":"` + decimal + `","currency":"usd"},"service":"compute","model":"gpu-h100x1"}`
}

// An application reports usage against its OWN org: the debit lands in that
// org's pool, the receipt names the pool, and a retry of the same act moves no
// more money.
func TestAnApplicationRecordsUsageAgainstItsOwnOrg(t *testing.T) {
	fin := books(t)
	fund(t, fin, "hanzo", 1000)
	is := newIssuer(t)
	app := recordApp(t, is)
	tok := is.application("visor", "hanzo")

	code, out := send(t, app, http.MethodPost, "/v1/billing/usage", tok, "", report("compute-m1-2026092314", "hanzo", "0.70"))
	if code != http.StatusOK {
		t.Fatalf("record: %d %v", code, out["_raw"])
	}
	if out["org"] != "hanzo" || out["account"] != "hanzo" || out["id"] != "compute-m1-2026092314" {
		t.Fatalf("receipt names the wrong books: %v", out["_raw"])
	}
	if got := pool(t, fin, "hanzo"); got != 930 {
		t.Fatalf("pool after one 70¢ act = %d, want 930", got)
	}

	// The retry: same act, same amount — the same receipt and no second debit.
	code, again := send(t, app, http.MethodPost, "/v1/billing/usage", tok, "", report("compute-m1-2026092314", "hanzo", "0.70"))
	if code != http.StatusOK || again["_raw"] != out["_raw"] {
		t.Fatalf("replay: %d %v, want the first receipt %v", code, again["_raw"], out["_raw"])
	}
	if got := pool(t, fin, "hanzo"); got != 930 {
		t.Fatalf("pool after a replay = %d, want 930 — a retry moved money", got)
	}

	// The same name for a different amount is a different charge under a taken
	// name: refused, and nothing moves.
	code, conflict := send(t, app, http.MethodPost, "/v1/billing/usage", tok, "", report("compute-m1-2026092314", "hanzo", "5.00"))
	if code != http.StatusConflict {
		t.Fatalf("reused id for another amount: %d %v, want 409", code, conflict["_raw"])
	}
	if got := pool(t, fin, "hanzo"); got != 930 {
		t.Fatalf("pool after a refused conflict = %d, want 930", got)
	}
}

// A tenant org that GRANTED the application membership is billed when the
// application selects it — and the balance the application reads for that org is
// the wallet the debit drew from.
func TestAGrantedOrgIsBilledAndReadsTheSameWallet(t *testing.T) {
	fin := books(t)
	fund(t, fin, "acme", 5000)
	fund(t, fin, "hanzo", 5000)
	is := newIssuer(t)
	app := recordApp(t, is)
	tok := is.application("visor", "hanzo", "acme")

	code, out := send(t, app, http.MethodPost, "/v1/billing/usage", tok, "acme", report("compute-m7-2026092315", "acme", "1.25"))
	if code != http.StatusOK || out["org"] != "acme" || out["account"] != "acme" {
		t.Fatalf("record for acme: %d %v", code, out["_raw"])
	}
	if got := pool(t, fin, "acme"); got != 4875 {
		t.Fatalf("acme pool = %d, want 4875", got)
	}
	if got := pool(t, fin, "hanzo"); got != 5000 {
		t.Fatalf("the application's own org paid for acme's usage: hanzo pool = %d", got)
	}

	code, bal := send(t, app, http.MethodGet, "/v1/billing/balance", tok, "acme", "")
	if code != http.StatusOK || bal["account"] != "acme" || bal["available"] != float64(4875) {
		t.Fatalf("balance read for acme: %d %v, want account acme available 4875", code, bal["_raw"])
	}
}

// The refusals, each before money moves: a person, a foreign org, no credential,
// and a malformed report.
func TestUsageIsRefusedToAnyoneButTheApplicationItsOrgGranted(t *testing.T) {
	fin := books(t)
	for _, org := range []string{"hanzo", "acme", "victim"} {
		fund(t, fin, org, 1000)
	}
	is := newIssuer(t)
	app := recordApp(t, is)
	visor := is.application("visor", "hanzo", "acme")

	for _, tc := range []struct {
		name, token, selected, body string
		want                        int
	}{
		{"no credential", "", "", report("a1", "hanzo", "0.10"), http.StatusUnauthorized},
		{"a person, even an owner of the org", is.person("alice", "acme"), "acme", report("a2", "acme", "0.10"), http.StatusForbidden},
		// The org never granted this application membership, so the selection is
		// discarded at the boundary and the token acts for its own org — which must
		// not be silently billed in the victim's place.
		{"an org that never granted the application", visor, "victim", report("a3", "victim", "0.10"), http.StatusForbidden},
		{"naming a granted org without selecting it", visor, "", report("a4", "acme", "0.10"), http.StatusForbidden},
		{"another application's tenant", is.application("bot", "bots"), "acme", report("a5", "acme", "0.10"), http.StatusForbidden},
		{"a zero amount", visor, "", report("a6", "hanzo", "0"), http.StatusBadRequest},
		{"a negative amount", visor, "", report("a7", "hanzo", "-1.00"), http.StatusBadRequest},
		{"no id", visor, "", report("", "hanzo", "0.10"), http.StatusBadRequest},
		{"a service that is not a slug", visor, "", `{"id":"a8","org":"hanzo","amount":{"decimal":"0.10","currency":"usd"},"service":"Compute Hours"}`, http.StatusBadRequest},
		{"another currency", visor, "", `{"id":"a9","org":"hanzo","amount":{"decimal":"0.10","currency":"eur"}}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := send(t, app, http.MethodPost, "/v1/billing/usage", tc.token, tc.selected, tc.body)
			if code != tc.want {
				t.Fatalf("status %d %v, want %d", code, out["_raw"], tc.want)
			}
		})
	}
	for _, org := range []string{"hanzo", "acme", "victim"} {
		if got := pool(t, fin, org); got != 1000 {
			t.Errorf("%s pool = %d after only refusals, want 1000 — a refused report moved money", org, got)
		}
	}
}

// Two applications reporting into one wallet under the same act name are two
// acts: the name is scoped to the application that chose it.
func TestAnActNameIsScopedToTheApplicationThatChoseIt(t *testing.T) {
	fin := books(t)
	fund(t, fin, "acme", 1000)
	is := newIssuer(t)
	app := recordApp(t, is)

	for _, tok := range []string{is.application("visor", "hanzo", "acme"), is.application("studio", "hanzo", "acme")} {
		if code, out := send(t, app, http.MethodPost, "/v1/billing/usage", tok, "acme", report("job-1", "acme", "1.00")); code != http.StatusOK {
			t.Fatalf("record: %d %v", code, out["_raw"])
		}
	}
	if got := pool(t, fin, "acme"); got != 800 {
		t.Fatalf("acme pool = %d, want 800 — one application's act name swallowed another's", got)
	}
}
