package billing

// The money gates driven through the REAL identity boundary: a signed IAM-shaped
// token verified by cloud.IdentityMiddleware, with every authority header a
// client could forge sent beside it. What the boundary mints from the token, and
// only that, decides who may change what an org pays.

import (
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
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

const testIssuer = "https://iam.test"

// iam signs tokens the way IAM does and serves the key that verifies them.
type iam struct {
	key  *rsa.PrivateKey
	jwks *httptest.Server
}

func newIAM(t *testing.T) *iam {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "k", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(srv.Close)
	return &iam{key: key, jwks: srv}
}

type seat struct{ org, role string }

// sign is a token for name, home org first in orgs as IAM writes it. `owner` is
// the application's org, as IAM signs it, and decides nothing.
func (i *iam) sign(t *testing.T, name string, isAdmin bool, billing string, orgs ...seat) string {
	t.Helper()
	set := make([]map[string]string, 0, len(orgs))
	for _, m := range orgs {
		set = append(set, map[string]string{"org": m.org, "role": m.role})
	}
	claims := jwt.MapClaims{
		"iss": testIssuer, "sub": "uuid-" + name, "aud": []string{"hanzo-console"},
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"owner": "hanzo", "name": name, "preferred_username": name, "email": name + "@example.com",
		"orgs": set,
	}
	if isAdmin {
		claims["isAdmin"] = true
	}
	if billing != "" {
		claims["billing_account"] = billing
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "k"
	raw, err := tok.SignedString(i.key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (i *iam) mount(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv(account.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("CLOUD_BRAND", "hanzo")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.IdentityMiddleware(&cloud.Config{IAMIssuer: testIssuer, Discovery: oidctest.Discovery(testIssuer), DiscoveryClient: oidctest.Client(testIssuer, i.jwks.URL)}))
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app
}

// as drives one request with tok and any extra headers.
func as(t *testing.T, app *zip.App, method, path, body, tok string, extra map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Every authority header a client could send. The boundary strips them all.
var forged = map[string]string{
	"X-User-IsOrgAdmin":    "true",
	"X-User-IsAdmin":       "true",
	"X-Billing-Account-Id": "person:acme/carol",
	"X-User-Owner":         "acme",
	"X-User-Name":          "dana",
}

func with(base map[string]string, more map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range more {
		out[k] = v
	}
	return out
}

// Carol owns her personal org and carries the IAM isAdmin bit, and is a plain
// member of acme. Switched into acme, with every authority header forged, she is
// refused every money write before commerce is asked, and lists only acme's own.
func TestASwitchedMemberIsRefusedWhateverItSends(t *testing.T) {
	s := standIn(t)
	id := newIAM(t)
	app := id.mount(t)
	tok := id.sign(t, "carol", true, "org:carol-co", seat{"carol-co", "owner"}, seat{"acme", "member"})
	hdr := with(forged, map[string]string{"X-Org-Id": "acme"})
	for _, w := range writes {
		before := s.count(w.op)
		status, body := as(t, app, w.method, w.path, w.body, tok, hdr)
		if status != http.StatusForbidden || !strings.Contains(body, "Only an admin of acme can "+w.says+".") {
			t.Errorf("%s %s: %d %s, want 403 naming acme", w.method, w.path, status, clip(body))
		}
		if s.count(w.op) != before {
			t.Errorf("%s %s reached commerce", w.method, w.path)
		}
	}
	if status, body := as(t, app, http.MethodGet, "/v1/billing/subscriptions?userId=acme/dana", "", tok, hdr); status != 200 {
		t.Fatalf("list: %d %s", status, clip(body))
	}
	if in, _ := s.last(client.BillingSubscriptions).(client.SubsIn); in.UserID != "acme" {
		t.Errorf("a member's list asked commerce for %q, want acme's own", in.UserID)
	}
}

// The same person in the personal org she founded is its admin: an org of one.
func TestAPersonalOrgsOwnerIsItsAdmin(t *testing.T) {
	s := standIn(t)
	id := newIAM(t)
	app := id.mount(t)
	tok := id.sign(t, "carol", false, "org:carol-co", seat{"carol-co", "owner"}, seat{"acme", "member"})
	for _, w := range writes {
		before := s.count(w.op)
		if status, body := as(t, app, w.method, w.path, w.body, tok, map[string]string{"X-Org-Id": "carol-co"}); status/100 != 2 || s.count(w.op) != before+1 {
			t.Errorf("%s %s as the personal org's owner: %d %s", w.method, w.path, status, clip(body))
		}
	}
	if ref, _ := s.last(client.BillingSubscriptionReactivate).(client.SubscriptionRef); !ref.Privileged || ref.Subject != "carol-co" {
		t.Errorf("the owner's resume reached commerce as %+v", ref)
	}
}

// A SuperAdmin stepped into acme (IAM assume adds acme to their set as an admin),
// and one selecting it without stepping in, pass every write, privileged.
func TestASuperAdminInSupportModePasses(t *testing.T) {
	s := standIn(t)
	id := newIAM(t)
	app := id.mount(t)
	for name, tok := range map[string]string{
		"stepped in": id.sign(t, "woo", false, "org:admin", seat{"admin", "admin"}, seat{"acme", "admin"}),
		"selecting":  id.sign(t, "woo", false, "org:admin", seat{"admin", "admin"}),
	} {
		for _, w := range writes {
			before := s.count(w.op)
			if status, body := as(t, app, w.method, w.path, w.body, tok, map[string]string{"X-Org-Id": "acme"}); status/100 != 2 || s.count(w.op) != before+1 {
				t.Errorf("SuperAdmin %s: %s %s: %d %s", name, w.method, w.path, status, clip(body))
			}
		}
		if ref, _ := s.last(client.BillingSubscriptionCancel).(client.SubscriptionRef); !ref.Privileged {
			t.Errorf("SuperAdmin %s: the cancel reached commerce unprivileged: %+v", name, ref)
		}
	}
}

// An admin of the shared signup org is not an admin of its money: its members
// are strangers paying from their own wallets and its account is the platform's.
// A cancel of a stranger's plan never reaches commerce, and the list is theirs.
func TestTheSignupOrgsAdminHoldsNoStranger(t *testing.T) {
	s := standIn(t)
	id := newIAM(t)
	app := id.mount(t)
	tok := id.sign(t, "staff", false, "org:hanzo", seat{"hanzo", "admin"})
	for _, w := range writes {
		before := s.count(w.op)
		status, body := as(t, app, w.method, w.path, w.body, tok, nil)
		if status != http.StatusForbidden || !strings.Contains(body, "In hanzo, only a platform admin can "+strings.Replace(w.says, " its ", " the ", 1)+".") {
			t.Errorf("%s %s as the signup org's admin: %d %s, want 403 naming the platform", w.method, w.path, status, clip(body))
		}
		if s.count(w.op) != before {
			t.Errorf("%s %s reached commerce", w.method, w.path)
		}
	}
	if status, body := as(t, app, http.MethodGet, "/v1/billing/subscriptions?userId=hanzo/bob", "", tok, nil); status != 200 {
		t.Fatalf("list: %d %s", status, clip(body))
	}
	if in, _ := s.last(client.BillingSubscriptions).(client.SubsIn); in.UserID != "hanzo" {
		t.Errorf("the signup org's admin listed %q, want only the wallet they pay from", in.UserID)
	}
}

// A legacy signup-only membership opens no platform tenant. A forged switch
// into acme cannot make its billing operations reachable either.
func TestASignupMemberCannotSpendWithoutATenant(t *testing.T) {
	s := standIn(t)
	id := newIAM(t)
	app := id.mount(t)
	tok := id.sign(t, "alice", false, "", seat{"hanzo", "member"})
	hdr := with(forged, map[string]string{"X-Org-Id": "acme"})
	before := s.count(client.BillingSubscriptionCancel)
	if status, body := as(t, app, http.MethodPost, "/v1/billing/subscriptions/sub_1/cancel", `{}`, tok, hdr); status != http.StatusUnauthorized {
		t.Fatalf("cancel without a tenant: %d %s", status, clip(body))
	}
	if s.count(client.BillingSubscriptionCancel) != before {
		t.Error("a caller without tenancy reached commerce")
	}
	reloads := s.count(client.BillingAutoRechargeSet)
	if status, body := as(t, app, http.MethodPut, "/v1/billing/recharge", `{"enabled":true,"thresholdCents":500,"amountCents":2500}`, tok, hdr); status != http.StatusUnauthorized {
		t.Fatalf("auto-reload without a tenant: %d %s", status, clip(body))
	}
	if s.count(client.BillingAutoRechargeSet) != reloads {
		t.Error("an auto-reload without tenancy reached commerce")
	}
}

// A credential billed from a project's account is not its holder's own wallet:
// the project's plan and cards are the org's admins'.
func TestAProjectWalletIsTheOrgsAdmins(t *testing.T) {
	s := standIn(t)
	id := newIAM(t)
	app := id.mount(t)
	tok := id.sign(t, "carol", false, "project:acme/site", seat{"acme", "member"})
	before := s.count(client.BillingSubscriptionCancel)
	if status, body := as(t, app, http.MethodPost, "/v1/billing/subscriptions/sub_1/cancel", `{}`, tok, map[string]string{"X-Org-Id": "acme"}); status != http.StatusForbidden {
		t.Fatalf("a project-billed member cancelled: %d %s", status, clip(body))
	}
	if s.count(client.BillingSubscriptionCancel) != before {
		t.Fatal("the cancel reached commerce")
	}
}

// Nobody signed in, or a bearer that is not a token, is 401 at every write; a
// member is a 403 problem whose detail says who to ask.
func TestTheRefusalsSayWhatIsMissing(t *testing.T) {
	s := standIn(t)
	id := newIAM(t)
	app := id.mount(t)
	carol := id.sign(t, "carol", false, "", seat{"acme", "member"})
	for _, w := range writes {
		for name, tc := range map[string]struct {
			tok  string
			want int
		}{
			"anonymous": {"", http.StatusUnauthorized},
			"garbage":   {"not.a.jwt", http.StatusUnauthorized},
			"member":    {carol, http.StatusForbidden},
		} {
			before := s.count(w.op)
			status, body := as(t, app, w.method, w.path, w.body, tc.tok, nil)
			var p struct {
				Status int    `json:"status"`
				Detail string `json:"detail"`
			}
			_ = json.Unmarshal([]byte(body), &p)
			if status != tc.want || p.Status != tc.want {
				t.Errorf("%s %s as %s: %d %s, want a %d problem", w.method, w.path, name, status, clip(body), tc.want)
			}
			if name == "member" && !strings.HasPrefix(p.Detail, "Only an admin of acme can ") {
				t.Errorf("%s %s: the member's detail is %q", w.method, w.path, p.Detail)
			}
			if s.count(w.op) != before {
				t.Errorf("%s %s reached commerce as %s", w.method, w.path, name)
			}
		}
	}
}
