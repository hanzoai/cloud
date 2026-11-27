package host

// A support session — an IAM impersonation token, the person's claims with the
// operator in `act` and `imp: true` — reads as the person and changes nothing
// that outlives it. These drive real tokens through SanitizeIdentity, so the
// operator they read is the one the boundary verified.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adminpeer "github.com/hanzoai/cloud/client/admin"
	cloud "github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// supportRefused is every write the table refuses, spelled as a client addresses it.
var supportRefused = []struct{ method, path string }{
	{"POST", "/v1/account/keys"}, {"PATCH", "/v1/account/keys/ada-secret-1"}, {"DELETE", "/v1/account/keys/ada-secret-1"},
	{"POST", "/v1/commerce/store/token"},
	{"POST", "/v1/principal/keys"},
	{"POST", "/v1/agent/targets/t1/key"},
	{"POST", "/v1/o11y/gateway/ingestion_keys"}, {"PATCH", "/v1/o11y/gateway/ingestion_keys/k1"},
	{"POST", "/v1/o11y/service_accounts/s1/keys"}, {"DELETE", "/v1/o11y/service_accounts/s1/keys/f1"},
	{"POST", "/v1/webhook/w1/secret"}, {"POST", "/v1/webhook"}, {"PUT", "/v1/webhook/w1"},
	{"POST", "/v1/provider/slack/connect"}, {"POST", "/v1/provider/telegram/connect"}, {"POST", "/v1/provider/notion/disconnect"},
	{"POST", "/v1/provider/github/claim"}, {"POST", "/v1/provider/linear/claim"}, {"POST", "/v1/provider/github/user/connect"},
	{"POST", "/v1/connection"}, {"POST", "/v1/connection/github/credential"}, {"DELETE", "/v1/connection/c1"},
	{"POST", "/v1/ai/connections"}, {"DELETE", "/v1/ai/connections/openai"},
	{"POST", "/v1/ai/providers"}, {"PUT", "/v1/ai/providers/acme/openai"}, {"POST", "/v1/ai/remote-connections"},
	{"POST", "/v1/ai/signin-sessions"},
	{"PUT", "/v1/team/account/cookie"},
	{"PUT", "/v1/notify/credentials/twilio/sid"},
	{"POST", "/v1/kms/secrets"}, {"DELETE", "/v1/kms/secrets/app/DB_URL"}, {"POST", "/v1/kms/auth/login"},
	{"PUT", "/v1/environment/site/secrets/STRIPE_KEY"},
	{"POST", "/v1/iam/password"}, {"POST", "/v1/iam/memberships"}, {"POST", "/v1/iam/delete-membership"},
	{"POST", "/v1/account/orgs"}, {"POST", "/v1/account/avatar"}, {"POST", "/v1/account/appearance"},
	{"POST", "/v1/billing/methods"}, {"DELETE", "/v1/billing/methods/pm_1"}, {"POST", "/v1/billing/portal/methods"},
	{"POST", "/v1/billing/topup"}, {"POST", "/v1/billing/topup/token"}, {"POST", "/v1/billing/subscribe/card"},
	{"POST", "/v1/billing/subscriptions"}, {"POST", "/v1/billing/subscriptions/s1/cancel"},
	{"POST", "/v1/billing/recharge"}, {"PUT", "/v1/billing/settings"}, {"POST", "/v1/billing/invoices/i1/collect"},
	{"POST", "/v1/billing/payouts"}, {"POST", "/v1/billing/accounts/a1/members"},
	{"POST", "/v1/plan/subscriptions"}, {"POST", "/v1/pricing/subscriptions"}, {"PUT", "/v1/team/billing/plan"},
	{"POST", "/v1/company/payment"},
	{"POST", "/v1/domain/register"}, {"POST", "/v1/domain/renew"}, {"POST", "/v1/domain/transfer"},
	{"POST", "/v1/company/esign"}, {"POST", "/v1/company/ein"}, {"POST", "/v1/company/advance"},
	{"PUT", "/v1/tax/profile"}, {"POST", "/v1/tax/profile/certify"}, {"POST", "/v1/tax/w9"}, {"POST", "/v1/tax/w9/f1/grant"},
	{"POST", "/v1/captable/shares/transfer"}, {"POST", "/v1/captable/shares"}, {"POST", "/v1/captable/rounds/r1/close"},
	{"POST", "/v1/company/fundraise/safe"}, {"POST", "/v1/company/genesis"}, {"POST", "/v1/company/kyc"},
	{"POST", "/v1/commerce/store/s1/charge"}, {"POST", "/v1/commerce/store/s1/checkout/charge"},
	{"POST", "/v1/commerce/store/s1/authorize"}, {"POST", "/v1/commerce/store/s1/capture/o1"},
	{"POST", "/v1/commerce/store/s1/paypal/pay"}, {"POST", "/v1/commerce/transfer"},
	{"POST", "/v1/marketplace/seller/payout"}, {"POST", "/v1/marketplace/jobs/j1/release"}, {"POST", "/v1/marketplace/jobs/j1/refund"},
	{"POST", "/v1/author/connect"},
	{"POST", "/v1/wallet"}, {"POST", "/v1/wallet/w1/keys"}, {"POST", "/v1/wallet/w1/sign"},
	{"POST", "/v1/share/enable"}, {"POST", "/v1/share/rotate"}, {"POST", "/v1/dataroom/links"},
	{"POST", "/v1/dataroom/trust/requests/r1/grant"}, {"POST", "/v1/o11y/roles"}, {"POST", "/v1/o11y/service_accounts/s1/roles"},
	{"PUT", "/v1/commerce/wallet/w1"},
	{"POST", "/v1/esign/o/acme/sign/t1/complete"}, {"POST", "/v1/legal/documents/d1/sign"}, {"POST", "/v1/legal/filings"},
	{"POST", "/v1/tax/filings"}, {"POST", "/v1/tax/forms/f1/furnish"}, {"POST", "/v1/compliance/accreditation"},
	{"POST", "/v1/compliance/verifications/v1/decision"},
	// Links, tokens and invitations.
	{"POST", "/v1/agent/chat/conversations/c1/shares"}, {"DELETE", "/v1/agent/chat/conversations/c1/shares/s1"},
	{"DELETE", "/v1/agent/chat/conversations/c1/shares/s1/viewers/v1"}, {"DELETE", "/v1/agent/chat/shares/s1"},
	{"POST", "/v1/agents/chat/conversations/c1/shares"},
	{"POST", "/v1/affiliate/me/links"}, {"POST", "/v1/esign/documents/d1/recipients"}, {"POST", "/v1/esign/documents/d1/send"},
	{"POST", "/v1/sandbox/s1/preview"}, {"POST", "/v1/meet/getToken"}, {"POST", "/v1/meet/record"}, {"DELETE", "/v1/meet/record"},
	// Publication.
	{"PUT", "/v1/dataroom/trust"}, {"POST", "/v1/dataroom/trust/artifacts"}, {"PATCH", "/v1/dataroom/trust/artifacts/a1"},
	{"POST", "/v1/dataroom/trust/requests/r1/refuse"}, {"PUT", "/v1/trust/policy/p1"},
	{"POST", "/v1/marketplace/listings"}, {"PATCH", "/v1/marketplace/listings/l1"},
	{"POST", "/v1/affiliate/me/handle"}, {"PUT", "/v1/leaderboard/optin"}, {"PUT", "/v1/leaderboard/optin/org"},
	{"POST", "/v1/research/grants"}, {"PATCH", "/v1/git/repos/r1"}, {"PATCH", "/v1/projects/p1"}, {"PATCH", "/v1/project/p1"},
	{"POST", "/v1/projects/p1/publish"}, {"POST", "/v1/project/p1/publish"},
	{"POST", "/v1/projects/p1/releases/r1/activate"}, {"POST", "/v1/project/p1/releases/r1/activate"},
	{"POST", "/v1/social/posts"}, {"POST", "/v1/social/posts/p1/publish"}, {"PUT", "/v1/social/posts/p1"},
	{"POST", "/v1/marketing/calendar"}, {"POST", "/v1/content/publish"},
	{"POST", "/v1/cloudflare/workers/scripts/w1/subdomain"}, {"POST", "/v1/ad/campaigns/a1/launch"}, {"POST", "/v1/campaign/c1/launch"},
	// o11y, every write, its POST queries included.
	{"POST", "/v1/o11y/dashboards"}, {"PUT", "/v1/o11y/dashboards/d1/public"}, {"DELETE", "/v1/o11y/dashboards/d1/public"},
	{"PUT", "/v1/o11y/channels/c1"}, {"POST", "/v1/o11y/query_range"}, {"POST", "/v1/o11y/service_accounts"},
	// The router folds case and a trailing slash, and a net/http handler behind it
	// decodes and cleans, so the table does all four.
	{"POST", "/V1/Account/Keys/"}, {"POST", "/v1/o11y/gateway/ingestion%5Fkeys"},
	{"POST", "/v1/account/%6Beys"}, {"POST", "/v1/projects/../account/keys"}, {"POST", "/v1//kms/secrets"},
	{"PATCH", "/V1/Git/Repos/r1/"},
}

// supportSealed is every read that hands a credential's value out.
var supportSealed = []string{"/v1/connection/github:work/token", "/v1/o11y/cloud_integrations/aws/credentials",
	"/v1/o11y/cloud_integrations/aws/%63redentials", "/v1/iam/tokens"}

// supportAllowed is what a support session still does as the person: work that ends with it.
var supportAllowed = []struct{ method, path string }{
	{"POST", "/v1/projects"}, {"POST", "/v1/commerce/store/s1/product"}, {"POST", "/v1/projects/p1/purge"},
	{"POST", "/v1/provider/github/search"}, {"POST", "/v1/provider/slack/run"}, {"POST", "/v1/company/structure"},
	{"POST", "/v1/marketplace/jobs/j1/accept"}, {"POST", "/v1/kmsx"}, {"POST", "/v1/chat/completions"},
	{"PATCH", "/v1/agent/targets/t1"},
	{"POST", "/v1/esign/documents"}, {"POST", "/v1/dataroom/datarooms"},
	// Beside a contained operation, and not it: a viewer opening a shared chat, a
	// repo's push and deletion, a staged release, a 30-second terminal ticket, a
	// 5-minute presigned upload.
	{"POST", "/v1/agent/chat/shares/read"}, {"POST", "/v1/git/repos/r1/push"}, {"DELETE", "/v1/git/repos/r1"},
	{"POST", "/v1/projects/p1/releases"}, {"POST", "/v1/sandbox/s1/terminal/ticket"}, {"POST", "/v1/s3/buckets/b1/objects"},
	{"POST", "/v1/meetx"}, {"POST", "/v1/trustx"},
}

// supportRig is the boundary and Witness (AuditTrail, Contain) in serve's order, in front of
// a terminal handler that answers 200 for any address.
type supportRig struct {
	app     *zip.App
	key     *rsa.PrivateKey
	trail   *audit.Recorder
	reached atomic.Int32
}

// Every request a support session makes is on the trail, reads included, naming
// the operator beside the person; a refusal is too. The person's own requests
// carry no operator, and their reads stay off the trail as before.
func TestEveryRequestOfASupportSessionNamesItsOperator(t *testing.T) {
	r := newSupportRig(t)
	support, own := r.tokens(t)
	r.do(t, http.MethodGet, "/v1/projects", support)
	r.do(t, http.MethodPost, "/v1/account/keys", support)
	r.do(t, http.MethodGet, "/v1/projects", own)
	r.do(t, http.MethodPost, "/v1/projects", own)

	// Each change is on the trail twice — begun before it ran, then its outcome — so
	// the outcomes are read here.
	all, _, err := r.trail.Query(t.Context(), audit.Filter{})
	rows := outcomes(all)
	if err != nil || len(rows) != 3 || len(all) != 5 {
		t.Fatalf("trail rows = %d (%d outcomes), %v; want 5 and 3 (the person's own read is not audited)", len(all), len(rows), err)
	}
	op := audit.Act{Sub: "u-op", Owner: "admin", Name: "z"}
	own1, refusal, read := rows[0], rows[1], rows[2] // newest first
	if read.Method != http.MethodGet || read.Actor.Sub != "u-acme" || read.Actor.Act != op {
		t.Fatalf("support read row %+v, want the person as actor and the operator in act", read)
	}
	if refusal.Outcome.Status != http.StatusForbidden || refusal.Actor.Act != op || refusal.Path != "/v1/account/keys" {
		t.Fatalf("refusal row %+v, want a 403 naming the operator", refusal)
	}
	if own1.Actor.Act != (audit.Act{}) || own1.Method != http.MethodPost {
		t.Fatalf("the person's own write %+v carries an operator", own1)
	}
	imp, _, err := r.trail.Query(t.Context(), audit.Filter{Impersonated: true})
	if err != nil || len(outcomes(imp)) != 2 || len(imp) != 3 {
		t.Fatalf("impersonated rows = %d, %v; want the support session's 2 outcomes and its write's begun record", len(imp), err)
	}
	if iv, err := r.trail.Verify(t.Context()); err != nil || iv.Verdict != audit.Intact {
		t.Fatalf("chain %+v %v, want intact", iv, err)
	}
	// What crosses the plane to the trail's holder carries the operator whole.
	if back := adminpeer.TrailRecord(adminpeer.PlaneRecord(read)); back.Actor.Act != op {
		t.Fatalf("plane round trip lost the operator: %+v", back.Actor)
	}
}

// The edge gate weighs a support session's call as the person's and debits
// nothing; the same call made by the person is debited.
func TestASupportSessionIsNotChargedAtTheEdge(t *testing.T) {
	fc := &fakeCommerce{balanceBody: `{"available":5000}`}
	m := mustClient(t, fc.server(t).URL)
	for _, tc := range []struct {
		act  principal.Act
		want int32
	}{
		{principal.Act{Sub: "u-op", Owner: "admin", Name: "z"}, 0},
		{principal.Act{}, 1},
	} {
		before := fc.usages()
		var ran atomic.Bool
		app := zip.New(zip.Config{})
		app.Use(zip.Handler(func(c *zip.Ctx) error {
			principal.Mint(c, principal.Principal{Org: "hanzo", User: "alice", Act: tc.act})
			return c.Next()
		}))
		app.Use(BillingGate(m, func(method, path string) int64 { return 5 }))
		app.Raw(http.MethodPost, "/v1/agent/run", func(c *zip.Ctx) error {
			ran.Store(true)
			return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
		})
		if resp := doReq(t, app); resp.StatusCode != http.StatusOK || !ran.Load() {
			t.Fatalf("operator %q: %d ran=%v, want served", tc.act.Sub, resp.StatusCode, ran.Load())
		}
		if tc.want > 0 && !waitFor(func() bool { return fc.usages()-before == tc.want }, time.Second) {
			t.Fatalf("the person's call: %d debits, want %d", fc.usages()-before, tc.want)
		}
		if tc.want == 0 {
			time.Sleep(100 * time.Millisecond)
			if n := fc.usages() - before; n != 0 {
				t.Fatalf("support session: %d debits, want none", n)
			}
		}
	}
}

// The op toll weighs a support session's priced operation as the person's and
// debits nothing; the person's own call to it is debited.
func TestASupportSessionIsNotChargedAtTheToll(t *testing.T) {
	index(t, &cloud.Config{}, cloud.Plugin{Name: "probe", Price: 25})
	fc := &fakeCommerce{balanceBody: `{"available":5000}`}
	m := mustClient(t, fc.server(t).URL)
	type unit struct {
		OK bool `json:"ok"`
	}
	for _, tc := range []struct {
		act  principal.Act
		want int32
	}{
		{principal.Act{Sub: "u-op", Owner: "admin", Name: "z"}, 0},
		{principal.Act{}, 1},
	} {
		before := fc.usages()
		var ran atomic.Bool
		app := zip.New(zip.Config{})
		app.Use(zip.Handler(func(c *zip.Ctx) error {
			principal.Mint(c, principal.Principal{Org: "acme", User: "u-acme", Act: tc.act})
			return c.Next()
		}))
		app.Use(cloud.Bridge())
		app.Authorize(Toll(m, nil))
		app.Post("/v1/probe/run", func(context.Context, *unit) (*unit, error) { ran.Store(true); return &unit{OK: true}, nil },
			zip.WithOperationID("probe_run"))
		req := httptest.NewRequest(http.MethodPost, zip.CallPath+"probe_run", nil)
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", "u-acme")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !ran.Load() {
			t.Fatalf("operator %q: %d ran=%v, want served", tc.act.Sub, resp.StatusCode, ran.Load())
		}
		if n := fc.usages() - before; n != tc.want {
			t.Fatalf("operator %q: %d debits, want %d", tc.act.Sub, n, tc.want)
		}
	}
}

// ada is acme's person; op is the SuperAdmin IAM names in `act`.
func (r *supportRig) tokens(t *testing.T) (support, own string) {
	t.Helper()
	ada := tokenClaims("hanzo-cloud", "acme", "ada@acme.test", false, time.Now().Add(15*time.Minute))
	ada.TokenType, ada.PreferredUsername = "access-token", "ada"
	support = signImp(t, r.key, impClaims{Claims: ada.Claims, Imp: true,
		Act: map[string]string{"sub": "u-op", "owner": "admin", "name": "z"}})
	return support, signWith(t, r.key, ada)
}

func (r *supportRig) do(t *testing.T, method, path, bearer string) (int, string) {
	t.Helper()
	return r.send(t, method, path, bearer, "application/json", `{}`)
}

func (r *supportRig) send(t *testing.T, method, path, bearer, kind, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", kind)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := r.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func newSupportRig(t *testing.T) *supportRig {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	r := &supportRig{key: key, trail: trail}
	r.app = zip.New(zip.Config{})
	r.app.Use(cloud.SanitizeIdentity(cloud.BoundaryOf(&cloud.Config{IAMIssuer: testIssuer, Discovery: oidctest.Discovery(testIssuer), DiscoveryClient: oidctest.Client(testIssuer, jwksServer(t, &key.PublicKey).URL)})))
	r.app.Use(cloud.Bridge())
	cloud.Witness(r.app, trail)
	r.app.Use(zip.Handler(func(c *zip.Ctx) error {
		r.reached.Add(1)
		return c.JSON(http.StatusOK, map[string]string{"user": c.User()})
	}))
	return r
}
