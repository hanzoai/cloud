package tel

// The tenant is the validated principal's org, and nothing a client sends can move
// it. Asserted through the REAL identity boundary with a signed IAM token, because
// the property lives in how a token becomes an org — a test that sets X-Org-Id and
// X-User-Id by hand has already made that decision for the code under test.
//
// X-Org-Id is a SELECTION from the token's signed `orgs` set, the same predicate
// the edge applies (authz.Claims.EffectiveOrg). No header is needed; a header
// naming an org outside the set is ignored; a header with no credential is refused.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/iam/pkg/model"
	iamstore "github.com/hanzoai/iam/pkg/store"
	iamserver "github.com/hanzoai/iam/server"
	"github.com/hanzoai/orm"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
)

// Two tenants, each holding one number, one call and one message.
const (
	home   = "acme"
	victim = "victim"
)

func e164(org string) string {
	if org == home {
		return "+15550000001"
	}
	return "+15550000002"
}

// tenants mounts the surface on a live, funded carrier behind the identity boundary
// and seeds both tenants' records.
func tenants(t *testing.T) (*zip.App, *iamtest.Issuer0, *cloud.Service[state], *planetest.Ledger) {
	t.Helper()
	iss := iamtest.New(t)
	l := planetest.Money(t, 100000)
	s, _ := telAt(t, l)

	ctx := context.Background()
	for _, org := range []string{home, victim} {
		if err := s.State.store.PutNumber(ctx, Number{Org: org, ID: "n-" + org, E164: e164(org)}); err != nil {
			t.Fatalf("seed number: %v", err)
		}
		if err := s.State.store.PutCall(ctx, Call{Org: org, ID: "c-" + org, From: e164(org), To: "+15559999999"}); err != nil {
			t.Fatalf("seed call: %v", err)
		}
		if err := s.State.store.PutMessage(ctx, SMS{Org: org, ID: "m-" + org, From: e164(org), To: "+15559999999", Text: "hi"}); err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}

	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	app.Use(cloud.IdentityMiddleware(&cloud.Config{IAMIssuer: iamtest.Issuer, Discovery: oidctest.Discovery(iamtest.Issuer), DiscoveryClient: oidctest.Client(iamtest.Issuer, iss.URL)}))
	app.Use(cloud.Bridge())
	routes(app, s)
	return app, iss, s, l
}

// person is a signed-in member of the given orgs, home first — IAM's order.
func person(t *testing.T, iss *iamtest.Issuer0, orgs ...string) string {
	t.Helper()
	set := make([]map[string]any, 0, len(orgs))
	for _, o := range orgs {
		set = append(set, map[string]any{"org": o, "role": "member"})
	}
	return iss.Sign(t, iamtest.Claims{Sub: "alice", PreferredUsername: "alice", Owner: orgs[0], Orgs: set})
}

func do(t *testing.T, app *zip.App, method, path, bearer, org, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	// The router bounds a test request at 1s unless told otherwise, and this one does
	// real work, so on a busy runner it timed out with nothing wrong.
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

// ids reads the ids of a {data:[…]} list.
func ids(t *testing.T, body string) []string {
	t.Helper()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	got := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		got = append(got, d.ID)
	}
	return got
}

// A principal reads its own org's records and only those — with no header, with
// its own org named, and with another org named. The header never moves the tenant.
func TestAPrincipalReadsOnlyItsOwnOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, iss, _, _ := tenants(t)
		tok := person(t, iss, home)

		for _, sent := range []string{"", home, victim} {
			for path, want := range map[string]string{
				"/v1/tel/numbers":  "n-" + home,
				"/v1/tel/calls":    "c-" + home,
				"/v1/tel/messages": "m-" + home,
			} {
				code, body := do(t, app, http.MethodGet, path, tok, sent, "")
				if code != http.StatusOK {
					t.Fatalf("X-Org-Id %q: GET %s = %d %s", sent, path, code, body)
				}
				if got := ids(t, body); len(got) != 1 || got[0] != want {
					t.Errorf("X-Org-Id %q: GET %s = %v, want only [%s]", sent, path, got, want)
				}
			}
			code, body := do(t, app, http.MethodGet, "/v1/tel/summary", tok, sent, "")
			if code != http.StatusOK || !strings.Contains(body, `"numbers":1`) {
				t.Errorf("X-Org-Id %q: summary = %d %s, want this org's one number", sent, code, body)
			}
		}
	})
}

// Naming another org does not reach its records to change them either: its number
// cannot be released or sent from, and its call cannot be hung up.
func TestAHeaderCannotActOnAnotherOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, iss, s, _ := tenants(t)
		tok := person(t, iss, home)

		if code, body := do(t, app, http.MethodDelete, "/v1/tel/numbers/n-"+victim, tok, victim, ""); code != http.StatusNotFound {
			t.Errorf("release of another org's number = %d %s, want 404", code, body)
		}
		if code, body := do(t, app, http.MethodDelete, "/v1/tel/calls/c-"+victim, tok, victim, ""); code != http.StatusNotFound {
			t.Errorf("hangup of another org's call = %d %s, want 404", code, body)
		}
		msg := `{"from":"` + e164(victim) + `","to":"+15559999999","text":"hi"}`
		if code, body := do(t, app, http.MethodPost, "/v1/tel/messages", tok, victim, msg); code != http.StatusForbidden {
			t.Errorf("send from another org's number = %d %s, want 403", code, body)
		}
		if n, _ := s.State.store.Number(context.Background(), victim, "n-"+victim); n.ID == "" {
			t.Error("another org's number is gone")
		}
	})
}

// Spending lands on the principal's own ledger whatever org the header names.
func TestAHeaderCannotSpendAnotherOrgsCredit(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, iss, _, l := tenants(t)
		tok := person(t, iss, home)

		if code, body := do(t, app, http.MethodPost, "/v1/tel/numbers", tok, victim, `{"e164":"+15550000003"}`); code != http.StatusCreated {
			t.Fatalf("buy = %d %s", code, body)
		}
		if !planetest.Wait(func() bool { return l.Count() == 1 }) {
			t.Fatalf("debits = %d, want 1", l.Count())
		}
		if org, _, _, _ := l.Charged(); org != home {
			t.Errorf("debited %q, want the principal's own org %q", org, home)
		}
	})
}

// A header with no credential is refused — it names an org and proves nothing.
func TestAHeaderAloneIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _, _ := tenants(t)
		for _, path := range []string{"/v1/tel/numbers", "/v1/tel/summary"} {
			if code, body := do(t, app, http.MethodGet, path, "", victim, ""); code != http.StatusUnauthorized {
				t.Errorf("GET %s with only X-Org-Id = %d %s, want 401", path, code, body)
			}
		}
	})
}

// The header is a selection, and a signed membership is what makes it one: a
// member of both orgs reaches the second only by naming it.
func TestAMemberSelectsAmongItsOwnOrgs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, iss, _, _ := tenants(t)
		tok := person(t, iss, home, victim)

		for sent, want := range map[string]string{"": "n-" + home, victim: "n-" + victim} {
			code, body := do(t, app, http.MethodGet, "/v1/tel/numbers", tok, sent, "")
			if code != http.StatusOK {
				t.Fatalf("X-Org-Id %q: %d %s", sent, code, body)
			}
			if got := ids(t, body); len(got) != 1 || got[0] != want {
				t.Errorf("X-Org-Id %q: numbers = %v, want [%s]", sent, got, want)
			}
		}
	})
}

// The population production has. Lux Tel customers sign up at console.lux.tel through
// the IAM application lux-tel, which hanzoai/universe declares in
// infra/k8s/iam/init_data.json: organization lux, signup open, and orgChoiceMode
// "create". Lux staff are members of org lux.
//
// Everything below runs through the IAM this binary embeds: each customer signs up,
// signs in, and exchanges the code for a token, and the tel surface verifies that
// token against IAM's own JWKS. Which org a customer is in is IAM's answer, not the
// test's, which is the whole point: the tests above hand-write two different orgs
// into their tokens, so they cannot see a declaration that puts every customer in
// the same one.
func TestTwoLuxTelCustomersAreTwoTenants(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		id := luxID(t, "create")
		ann, bob := id.customer(t, "ann@example.com"), id.customer(t, "bob@example.com")

		if ann.org == bob.org {
			t.Fatalf("both customers are in org %q", ann.org)
		}
		for _, c := range []luxCustomer{ann, bob} {
			if c.org == "lux" {
				t.Fatalf("a customer's home org is lux, the staff tenant")
			}
		}

		app, _ := id.tel(t, ann.org, bob.org, "lux")
		for _, c := range []struct {
			me    luxCustomer
			other string
		}{{ann, bob.org}, {bob, ann.org}} {
			for _, sent := range []string{"", c.other, "lux"} {
				code, body := do(t, app, http.MethodGet, "/v1/tel/messages", c.me.token, sent, "")
				if code != http.StatusOK {
					t.Fatalf("X-Org-Id %q: GET messages = %d %s", sent, code, body)
				}
				if got := ids(t, body); len(got) != 1 || got[0] != "m-"+c.me.org {
					t.Errorf("X-Org-Id %q: messages = %v, want only [m-%s]", sent, got, c.me.org)
				}
			}
			if code, body := do(t, app, http.MethodDelete, "/v1/tel/numbers/n-"+c.other, c.me.token, c.other, ""); code != http.StatusNotFound {
				t.Errorf("release of the other customer's number = %d %s, want 404", code, body)
			}
		}
	})
}

// Without org founding both customers still land in lux, but the identity
// boundary refuses plain membership in a platform org instead of exposing its data.
func TestLuxTelWithoutOrgFoundingCannotReadThePlatformTenant(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		id := luxID(t, "")
		ann, bob := id.customer(t, "ann@example.com"), id.customer(t, "bob@example.com")
		if ann.org != "lux" || bob.org != "lux" {
			t.Fatalf("home orgs = %q, %q; want lux for both, the shape this control reproduces", ann.org, bob.org)
		}
		app, s := id.tel(t, "lux")
		if err := s.State.store.PutMessage(context.Background(), SMS{Org: "lux", ID: "m-bob", From: "+15550000009", To: "+15559999999", Text: "hi"}); err != nil {
			t.Fatalf("seed message: %v", err)
		}
		code, body := do(t, app, http.MethodGet, "/v1/tel/messages", ann.token, "", "")
		if code != http.StatusForbidden || strings.Contains(body, `"m-bob"`) {
			t.Fatalf("messages = %d %s, want refusal without bob's message", code, body)
		}
	})
}

// luxIAM is the identity plane a Lux Tel customer meets, over one store.
type luxIAM struct {
	app  *zip.App
	jwks string
}

// luxCustomer is one person as IAM answers for them: their home org and the access
// token their sign-in minted.
type luxCustomer struct {
	org   string
	token string
}

const (
	luxTelSecret   = "lux-tel-test-secret"
	luxTelRedirect = "https://console.lux.tel/callback"
	luxPassword    = "correct horse battery staple"
)

// luxID stands up the embedded IAM holding org lux and the lux-tel application with
// the given orgChoiceMode, and serves its JWKS where the tel surface looks.
func luxID(t *testing.T, orgChoiceMode string) *luxIAM {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keys := t.TempDir()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(keys, "cert-lux"), pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IAM_SIGNING_KEYS", keys)
	t.Setenv("IAM_ISSUER", "https://lux.id")

	db, err := iamstore.Open("sqlite", t.TempDir()+"/iam.db", "")
	if err != nil {
		t.Fatalf("open identity store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	cert := orm.New[model.Cert](db)
	cert.Owner, cert.Name = "admin", "cert-lux"
	cert.Type, cert.CryptoAlgorithm, cert.BitSize = "x509", "RS256", 2048
	cert.SetId("admin/cert-lux")
	if err := cert.CreateCtx(ctx); err != nil {
		t.Fatalf("seed cert: %v", err)
	}
	org := orm.New[model.Organization](db)
	org.Owner, org.Name, org.DisplayName = "admin", "lux", "Lux"
	org.SetId("admin/lux")
	if err := org.CreateCtx(ctx); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	a := orm.New[model.Application](db)
	a.Owner, a.Name, a.ClientId, a.ClientSecret = "admin", "lux-tel", "lux-tel", luxTelSecret
	a.Organization, a.Cert = "lux", "cert-lux"
	a.EnablePassword, a.EnableSignUp, a.OrgChoiceMode = true, true, orgChoiceMode
	a.RedirectUris = []string{luxTelRedirect}
	a.GrantTypes = []string{"authorization_code", "refresh_token"}
	a.ExpireInHours = 1
	a.SetId("admin/lux-tel")
	if err := a.CreateCtx(ctx); err != nil {
		t.Fatalf("seed application: %v", err)
	}

	// The issuer is installed at boot, before the App is composed, as a host does it.
	if err := iamserver.RequireIssuer(); err != nil {
		t.Fatalf("install the issuer: %v", err)
	}
	id := &luxIAM{app: iamserver.NewApp(db)}
	res := id.call(t, httptest.NewRequest(http.MethodGet, "/.well-known/jwks", nil))
	jwks, _ := io.ReadAll(res.Body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jwks) }))
	t.Cleanup(srv.Close)
	id.jwks = srv.URL
	return id
}

func (id *luxIAM) call(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	req.Host = "lux.id"
	// IAM hashes a password on signup, which a busy runner takes past the router's
	// 1s default for a test request.
	res, err := id.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func (id *luxIAM) post(t *testing.T, path string, body map[string]string) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	var out map[string]any
	if err := json.NewDecoder(id.call(t, req).Body).Decode(&out); err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	if out["status"] != "ok" {
		t.Fatalf("POST %s = %v", path, out)
	}
	return out
}

// customer signs a person up through lux-tel exactly as console.lux.tel does, signs
// them in, and returns the org IAM filed them in with the token it minted.
func (id *luxIAM) customer(t *testing.T, email string) luxCustomer {
	t.Helper()
	signup := id.post(t, "/v1/iam/signup", map[string]string{
		"application": "lux-tel", "clientId": "lux-tel", "organization": "lux",
		"email": email, "password": luxPassword,
	})
	data, _ := signup["data"].(map[string]any)
	owner, _ := data["owner"].(string)

	login := id.post(t, "/v1/iam/login", map[string]string{
		"type": "code", "application": "lux-tel", "clientId": "lux-tel", "organization": "lux",
		"username": email, "password": luxPassword, "redirectUri": luxTelRedirect, "scope": "openid",
	})
	code, _ := login["data"].(string)

	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {luxTelRedirect},
		"client_id": {"lux-tel"}, "client_secret": {luxTelSecret},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/iam/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(id.call(t, req).Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("token exchange for %s: %v", email, err)
	}
	return luxCustomer{org: owner, token: tok.AccessToken}
}

// tel mounts the surface behind the identity boundary, trusting this IAM, and gives
// each named org one number and one message.
func (id *luxIAM) tel(t *testing.T, orgs ...string) (*zip.App, *cloud.Service[state]) {
	t.Helper()
	s, _ := telAt(t, planetest.Money(t, 100000))
	ctx := context.Background()
	for i, org := range orgs {
		e164 := "+1555000010" + string(rune('0'+i))
		if err := s.State.store.PutNumber(ctx, Number{Org: org, ID: "n-" + org, E164: e164}); err != nil {
			t.Fatalf("seed number: %v", err)
		}
		if err := s.State.store.PutMessage(ctx, SMS{Org: org, ID: "m-" + org, From: e164, To: "+15559999999", Text: "hi"}); err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	app.Use(cloud.IdentityMiddleware(&cloud.Config{IAMIssuer: "https://lux.id", Discovery: oidctest.Discovery("https://lux.id"), DiscoveryClient: oidctest.Client("https://lux.id", id.jwks)}))
	app.Use(cloud.Bridge())
	routes(app, s)
	return app, s
}
