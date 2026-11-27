package cloud

// A support session — an IAM impersonation token, the person's claims with the
// operator in `act` and `imp: true` — reads as the person and changes nothing
// that outlives it. These drive real tokens through SanitizeIdentity, so the
// operator they read is the one the boundary verified.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/meter"
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

func TestTheTableNamesWhatASupportSessionMayNotChange(t *testing.T) {
	for _, r := range supportRefused {
		err := confined(r.method, r.path)
		var he *zip.HTTPError
		if err == nil || !errors.As(err, &he) || he.Status != http.StatusForbidden || !strings.Contains(he.Error(), "support session") {
			t.Errorf("%s %s: %v, want a 403 naming the support session", r.method, r.path, err)
		}
		if err := confined(http.MethodGet, r.path); err != nil {
			t.Errorf("GET %s: %v, want a read", r.path, err)
		}
	}
	for _, r := range supportAllowed {
		if err := confined(r.method, r.path); err != nil {
			t.Errorf("%s %s: %v, want allowed", r.method, r.path, err)
		}
	}
	for _, path := range supportSealed {
		if err := confined(http.MethodGet, path); err == nil || !strings.Contains(err.Error(), "cannot read") {
			t.Errorf("GET %s: %v, want a refused read", path, err)
		}
	}
	if err := confined(http.MethodGet, "/v1/connection/github:work"); err != nil {
		t.Errorf("GET a connection's metadata: %v, want the person's read", err)
	}
}

// supportRig is the boundary and Witness (AuditTrail, Contain) in serve's order, in front of
// a terminal handler that answers 200 for any address.
type supportRig struct {
	app     *zip.App
	key     *rsa.PrivateKey
	trail   *audit.Recorder
	reached atomic.Int32
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

// Each entry is the ONLY refusal of at least one case above, so deleting any one
// of them lets a listed write through and fails TestTheTableNames…: the cases are
// the mutation proof of the table, entry by entry.
func TestEveryEntryIsTheOnlyRefusalOfACase(t *testing.T) {
	sole := func(table []struct{ at, what string }, method, path string) int {
		hit := -1
		for i, s := range table {
			for _, at := range spellings(path) {
				if fits(method, at, s.at) {
					if hit >= 0 && hit != i {
						return -1
					}
					hit = i
				}
			}
		}
		return hit
	}
	alone := map[string]bool{}
	for _, c := range supportRefused {
		if i := sole(support, c.method, c.path); i >= 0 {
			alone[support[i].at] = true
		}
	}
	for _, s := range support {
		if !alone[s.at] {
			t.Errorf("support entry %q is the only refusal of no case in supportRefused: removing it would pass unseen", s.at)
		}
	}
	alone = map[string]bool{}
	for _, p := range supportSealed {
		if i := sole(custody, http.MethodGet, p); i >= 0 {
			alone[custody[i].at] = true
		}
	}
	for _, s := range custody {
		if !alone[s.at] {
			t.Errorf("custody entry %q is the only refusal of no case in supportSealed", s.at)
		}
	}
}

// An emit point that builds its own Actor knows the person, not the operator; the
// recorder names the operator from the call the record is appended for.
func TestTheTrailNamesTheOperatorOfAHandBuiltRecord(t *testing.T) {
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	trail.Acting(ActOf)
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-acme", ActedBy: "u-op"})
	got, err := trail.Append(ctx, audit.Record{Actor: audit.Actor{Org: "acme", Sub: "u-acme"}, Action: "account.keys.revoke",
		Outcome: audit.Outcome{Result: "success", Status: 200}})
	if err != nil || got.Actor.Act.Sub != "u-op" {
		t.Fatalf("hand-built record %+v %v, want the operator stamped", got.Actor, err)
	}
	got, err = trail.Append(context.Background(), audit.Record{Actor: audit.Actor{Org: "acme", Sub: "u-acme"}, Action: "x",
		Outcome: audit.Outcome{Result: "success", Status: 200}})
	if err != nil || got.Actor.Act != (audit.Act{}) {
		t.Fatalf("a record with nobody behind it %+v %v, want no operator", got.Actor, err)
	}
}

// A resource meter's charge authorized for a support session settles to nobody
// and gives its hold back.
func TestASupportSessionsChargeDebitsNothing(t *testing.T) {
	fc := &fakeCommerce{balanceBody: `{"available":5000}`}
	rm := &Meter{m: mustClient(t, fc.server(t).URL), provider: "probe"}
	h := meter.HoldOf(&rm.inflight, "acme", 5)
	h.Commit(context.Background())
	ch := &Charge{meter: rm, payer: Payer{Operator: "u-op"}, kind: "probe"}
	ch.hold.Store(h)
	ch.Debit(meter.Usage{AmountCents: 5})
	time.Sleep(100 * time.Millisecond)
	if n := fc.usages(); n != 0 {
		t.Fatalf("support session charge: %d debits, want none", n)
	}
	if p := rm.inflight.Pending("acme"); p != 0 {
		t.Fatalf("hold kept %d after the charge settled, want it given back", p)
	}
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
	r.app.Use(SanitizeIdentity(newIdentityValidator(testIssuer, jwksServer(t, &key.PublicKey).URL, 0)))
	r.app.Use(Bridge())
	Witness(r.app, trail)
	r.app.Use(zip.Handler(func(c *zip.Ctx) error {
		r.reached.Add(1)
		return c.JSON(http.StatusOK, map[string]string{"user": c.User()})
	}))
	return r
}

// Every supportRefused write is supportRefused to a support session and to nobody else; its
// reads and every other write reach the handler as the person.
func TestASupportSessionChangesNothingThatOutlivesIt(t *testing.T) {
	r := newSupportRig(t)
	support, own := r.tokens(t)
	for _, w := range supportRefused {
		if code, body := r.do(t, w.method, w.path, support); code != http.StatusForbidden || !strings.Contains(body, "support session") {
			t.Errorf("support session %s %s: %d %s, want 403", w.method, w.path, code, body)
		}
		if code, body := r.do(t, http.MethodGet, w.path, support); code != http.StatusOK || !strings.Contains(body, "u-acme") {
			t.Errorf("support session GET %s: %d %s, want the person's read", w.path, code, body)
		}
		if code, body := r.do(t, w.method, w.path, own); code != http.StatusOK {
			t.Errorf("the person's own %s %s: %d %s, want it unaffected", w.method, w.path, code, body)
		}
	}
	for _, w := range supportAllowed {
		if code, body := r.do(t, w.method, w.path, support); code != http.StatusOK {
			t.Errorf("support session %s %s: %d %s, want allowed", w.method, w.path, code, body)
		}
	}
	for _, path := range supportSealed {
		if code, body := r.do(t, http.MethodGet, path, support); code != http.StatusForbidden {
			t.Errorf("support session GET %s: %d %s, want 403", path, code, body)
		}
		if code, body := r.do(t, http.MethodGet, path, own); code != http.StatusOK {
			t.Errorf("the person's own GET %s: %d %s, want it unaffected", path, code, body)
		}
	}
}

// The containment lists themselves, walked: every entry, at its own address and
// under it (a method-prefixed one at exactly its operation), answers 403 to a
// support session on every method it covers and is the person's own to call.
func TestEveryContainedAddressRefusesASupportSessionAndNobodyElse(t *testing.T) {
	r := newSupportRig(t)
	imp, own := r.tokens(t)
	concrete := func(pattern string) (methods []string, paths []string) {
		verb, addr, one := strings.Cut(pattern, " ")
		if !one {
			addr = verb
		}
		addr = strings.ReplaceAll(addr, "*", "x1")
		if one {
			return []string{verb}, []string{addr}
		}
		return []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}, []string{addr, addr + "/x2"}
	}
	for _, s := range support {
		methods, paths := concrete(s.at)
		for _, m := range methods {
			for _, p := range paths {
				if code, body := r.do(t, m, p, imp); code != http.StatusForbidden || !strings.Contains(body, s.what) {
					t.Errorf("%q: support session %s %s: %d %s, want 403 naming %s", s.at, m, p, code, body, s.what)
				}
				if code, body := r.do(t, m, p, own); code != http.StatusOK {
					t.Errorf("%q: the person's own %s %s: %d %s, want it unaffected", s.at, m, p, code, body)
				}
			}
		}
	}
	for _, s := range custody {
		_, paths := concrete(s.at)
		for _, p := range paths {
			if code, body := r.do(t, http.MethodGet, p, imp); code != http.StatusForbidden || !strings.Contains(body, "cannot read") {
				t.Errorf("%q: support session GET %s: %d %s, want 403", s.at, p, code, body)
			}
			if code, body := r.do(t, http.MethodGet, p, own); code != http.StatusOK {
				t.Errorf("%q: the person's own GET %s: %d %s, want it unaffected", s.at, p, code, body)
			}
		}
	}
}

// A support session may end itself and nothing else under /v1/iam: revoke the very
// bearer it presents (RFC 7009), or sign out naming no token but its own. Another
// token, another method, another IAM write, another spelling: refused.
func TestASupportSessionEndsItselfAndNothingElse(t *testing.T) {
	r := newSupportRig(t)
	support, own := r.tokens(t)
	form := "application/x-www-form-urlencoded"
	op := map[string]string{"sub": "u-op", "owner": "admin", "name": "z"}
	idToken := func(sub string, act map[string]string) string {
		c := tokenClaims("hanzo-cloud", "acme", "ada@acme.test", false, time.Now().Add(15*time.Minute))
		c.Subject, c.TokenType = sub, "ID-token"
		return signImp(t, r.key, impClaims{Claims: c.Claims, Imp: act != nil, Act: act})
	}
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"revoke its own bearer", "POST", "/v1/iam/oauth/revoke", "client_id=hanzo-cloud&token=" + support, http.StatusOK},
		{"revoke its own bearer, by query", "POST", "/v1/iam/oauth/revoke?token=" + support, "client_id=hanzo-cloud", http.StatusOK},
		{"revoke the person's own token", "POST", "/v1/iam/oauth/revoke", "token=" + own, http.StatusForbidden},
		{"revoke nothing", "POST", "/v1/iam/oauth/revoke", "client_id=hanzo-cloud", http.StatusForbidden},
		{"its own in the body, another in the query IAM reads first", "POST", "/v1/iam/oauth/revoke?token=" + own, "token=" + support, http.StatusForbidden},
		{"revoke by another method", "PUT", "/v1/iam/oauth/revoke", "token=" + support, http.StatusForbidden},
		{"revoke spelled as another address", "POST", "/v1/iam/oauth/revoke/../../keys", "token=" + support, http.StatusForbidden},
		{"revoke percent-encoded", "POST", "/v1/iam/oauth/%72evoke", "token=" + support, http.StatusForbidden},
		{"sign out", "POST", "/v1/iam/oauth/logout", "client_id=hanzo-cloud", http.StatusOK},
		{"sign out with its own id_token", "POST", "/v1/iam/oauth/logout", "id_token_hint=" + idToken("u-acme", op), http.StatusOK},
		{"sign out naming the person's own sign-in", "POST", "/v1/iam/oauth/logout", "id_token_hint=" + idToken("u-acme", nil), http.StatusForbidden},
		{"sign out naming someone else", "POST", "/v1/iam/oauth/logout", "id_token_hint=" + idToken("u-bob", op), http.StatusForbidden},
		{"sign out naming another operator", "POST", "/v1/iam/oauth/logout",
			"id_token_hint=" + idToken("u-acme", map[string]string{"sub": "u-op2", "owner": "admin", "name": "y"}), http.StatusForbidden},
		{"sign out with a hint that is no token", "POST", "/v1/iam/oauth/logout", "id_token_hint=garbage", http.StatusForbidden},
		{"any other IAM write", "POST", "/v1/iam/oauth/token", "grant_type=refresh_token&token=" + support, http.StatusForbidden},
		{"another IAM write carrying its own token", "POST", "/v1/iam/keys", "token=" + support, http.StatusForbidden},
	} {
		if code, body := r.send(t, tc.method, tc.path, support, form, tc.body); code != tc.want {
			t.Errorf("%s: %s %s: %d %s, want %d", tc.name, tc.method, tc.path, code, body, tc.want)
		}
	}
	// The person's own sign-in is unaffected either way.
	if code, body := r.send(t, http.MethodPost, "/v1/iam/oauth/revoke", own, form, "token="+support); code != http.StatusOK {
		t.Errorf("the person's own revoke: %d %s, want it unaffected", code, body)
	}
	// Ending is on the trail too, with its operator.
	rows, _, err := r.trail.Query(t.Context(), audit.Filter{Impersonated: true})
	if err != nil {
		t.Fatal(err)
	}
	var ended bool
	for _, row := range rows {
		ended = ended || row.Path == "/v1/iam/oauth/revoke" && row.Outcome.Status == http.StatusOK && row.Actor.Act.Sub == "u-op"
	}
	if !ended {
		t.Fatalf("no trail row for the session's own revoke among %d", len(rows))
	}
}

// A call delegated with As states the caller afresh, so it must carry the
// operator: the callee in another process reads only what crossed the plane, and
// without it a support session's call there is uncontained, charged to nobody's
// knowledge and on the trail as the person alone.
func TestADelegatedCallCarriesTheOperator(t *testing.T) {
	r := newSupportRig(t)
	var delegated, plain atomic.Value
	r.app = zip.New(zip.Config{})
	r.app.Use(SanitizeIdentity(newIdentityValidator(testIssuer, jwksServer(t, &r.key.PublicKey).URL, 0)))
	r.app.Use(zip.Handler(func(c *zip.Ctx) error {
		delegated.Store(principal.ActFrom(As(c, "acme")).Sub)
		plain.Store(principal.ActFrom(As(c, "")).Sub)
		return c.JSON(http.StatusOK, map[string]string{})
	}))
	support, own := r.tokens(t)
	r.do(t, http.MethodGet, "/v1/projects", support)
	if delegated.Load() != "u-op" || plain.Load() != "u-op" {
		t.Fatalf("delegated %v / %v, want the operator on both", delegated.Load(), plain.Load())
	}
	r.do(t, http.MethodGet, "/v1/projects", own)
	if delegated.Load() != "" || plain.Load() != "" {
		t.Fatalf("the person's own delegated call names %v / %v, want nobody", delegated.Load(), plain.Load())
	}
}

// Over a door the address is the door, so the operation is asked: the call plane
// to a key mint is supportRefused to a support session, the rule composes it, and the
// same call is the person's own to make.
func TestASupportSessionIsAskedAboutTheOperationADoorReached(t *testing.T) {
	r := newSupportRig(t)
	support, own := r.tokens(t)
	type unit struct {
		OK bool `json:"ok"`
	}
	run := func(context.Context, *unit) (*unit, error) { return &unit{OK: true}, nil }
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(newIdentityValidator(testIssuer, jwksServer(t, &r.key.PublicKey).URL, 0)))
	app.Use(Bridge())
	app.Authorize(Rule(nil, nil))
	app.Group("/v1/account").Post("/keys", run)
	app.Group("/v1/projects").Post("/", run)
	ids := map[string]string{}
	for _, o := range app.Registry() {
		ids[o.Path] = o.OperationID
	}
	call := func(path, bearer string) int {
		t.Helper()
		id := ids[path]
		if id == "" {
			t.Fatalf("no operation at %s: %v", path, ids)
		}
		req := httptest.NewRequest(http.MethodPost, zip.CallPath+id, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := call("/v1/account/keys", support); code != http.StatusForbidden {
		t.Errorf("support session minting a key on the call plane: %d, want 403", code)
	}
	if code := call("/v1/account/keys", own); code != http.StatusOK {
		t.Errorf("the person minting their own key on the call plane: %d, want 200", code)
	}
	if code := call("/v1/projects", support); code != http.StatusOK {
		t.Errorf("support session on an unrefused operation: %d, want 200", code)
	}
}
