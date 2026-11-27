package account

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/iam/pkg/model"
	"github.com/hanzoai/iam/pkg/schema"
	iamstore "github.com/hanzoai/iam/pkg/store"
	iamserver "github.com/hanzoai/iam/server"
	"github.com/hanzoai/orm"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
)

// TestOnboard_OwnerReachesTokenAndSwitch drives the whole path against
// the real IAM rather than a double: a person in acme creates a second org through
// POST /v1/account/orgs, IAM records them as its owner, the token their next
// sign-in mints names the org in `orgs`, and cloud's identity boundary then accepts
// X-Org-Id for it. The token minted before the org existed does not.
func TestOnboard_OwnerReachesTokenAndSwitch(t *testing.T) {
	id := hanzoID(t)
	bob := id.signup(t, "bob@acme.test")
	if bob.home != "acme" {
		t.Fatalf("signup filed bob under %q, want acme", bob.home)
	}
	before := id.signin(t, "bob@acme.test")

	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	app.Use(cloud.IdentityMiddleware(&cloud.Config{IAMIssuer: hanzoIssuer, Discovery: oidctest.Discovery(hanzoIssuer), DiscoveryClient: oidctest.Client(hanzoIssuer, id.jwks)}))
	app.Use(cloud.Bridge())
	app.Raw(http.MethodGet, "/probe/org", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"org": c.Org()})
	})
	t.Setenv("IAM_URL", id.url)
	t.Setenv("IAM_MINT_CLIENT_ID", "hanzo-console")
	t.Setenv("IAM_MINT_CLIENT_SECRET", consoleSecret)
	t.Setenv("CLOUD_BRAND", "hanzo")
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}

	code, body := bearerCall(t, app, http.MethodPost, "/v1/account/orgs", before, "", `{"name":"Side Project"}`)
	if code != http.StatusOK {
		t.Fatalf("POST /v1/account/orgs: want 200, got %d (%s)", code, body)
	}
	var resp onboardResp
	mustJSON(t, body, &resp)
	if resp.Org != "side-project" {
		t.Fatalf("resp = %+v, want the org side-project", resp)
	}

	// The token bob held before the org existed cannot switch into it.
	if got := probeOrg(t, app, before, "side-project"); got != "acme" {
		t.Fatalf("pre-creation token switched to %q; the switch must fall back to acme", got)
	}

	// His next sign-in names the org, as its owner.
	after := id.signin(t, "bob@acme.test")
	orgs := orgsClaim(t, after)
	if orgs["acme"] == "" || orgs["side-project"] != "owner" {
		t.Fatalf("refreshed orgs claim = %v, want acme plus side-project as owner", orgs)
	}
	if got := probeOrg(t, app, after, "side-project"); got != "side-project" {
		t.Fatalf("X-Org-Id side-project resolved to %q with the refreshed token", got)
	}
}

// IAM refuses to record an account of the admin org as a customer org's member
// for a confidential client (only a SuperAdmin may change such an account). The
// refusal wrote nothing, so the org must not survive it: the real IAM ends with
// neither the org nor a membership row naming it.
func TestOnboard_RefusedOwnerLeavesNothingInIAM(t *testing.T) {
	id := hanzoID(t)
	app := mountApp(t, id.url, "hanzo-console", consoleSecret)

	operator := map[string]string{
		"X-User-Id": "root", "X-User-Name": "root", "X-User-Owner": "admin", "X-Org-Id": "acme",
	}
	code, body := callH(t, app, http.MethodPost, "/v1/account/orgs", operator, `{"name":"Side Project"}`)
	if code != http.StatusBadGateway {
		t.Fatalf("want 502 when IAM refuses the owner, got %d (%s)", code, body)
	}
	ctx := context.Background()
	if org, err := iamstore.GetOrganizationByName(ctx, id.db, "side-project"); err != nil || org != nil {
		t.Fatalf("org survived the refused owner: %v %v", org, err)
	}
	if rows, err := iamstore.MembershipsByOrg(ctx, id.db, "side-project"); err != nil || len(rows) != 0 {
		t.Fatalf("membership rows name the removed org: %v %v", rows, err)
	}
}

const (
	hanzoIssuer   = "https://hanzo.id"
	consoleSecret = "console-test-secret"
	acmeSecret    = "acme-test-secret"
	acmeRedirect  = "https://acme.test/callback"
	testPassword  = "correct horse battery staple"
)

// realIAM is the embedded IAM over one store, reachable both in process and over
// HTTP (the account client dials IAM_URL).
type realIAM struct {
	app  *zip.App
	db   orm.DB
	url  string
	jwks string
}

type person struct{ home string }

// hanzoID stands up IAM with the brand org hanzo, the customer org acme, the
// sign-in application acme-app, and the confidential client hanzo-console, which
// holds the org and user capabilities the way the deployment grants them.
func hanzoID(t *testing.T) *realIAM {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keys := t.TempDir()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(keys, "cert-hanzo"), pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IAM_SIGNING_KEYS", keys)
	t.Setenv("IAM_ISSUER", hanzoIssuer)
	t.Setenv("IAM_ORG_ADMIN_APPS", "hanzo-console")
	t.Setenv("IAM_USER_ADMIN_APPS", "hanzo-console")
	t.Setenv("IAM_KEY_MINT_ALLOWED_APPS", "hanzo-console")

	db, err := iamstore.Open("sqlite", t.TempDir()+"/iam.db", "")
	if err != nil {
		t.Fatalf("open identity store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	cert := orm.New[model.Cert](db)
	cert.Owner, cert.Name = "admin", "cert-hanzo"
	cert.Type, cert.CryptoAlgorithm, cert.BitSize = "x509", "RS256", 2048
	cert.SetId("admin/cert-hanzo")
	if err := cert.CreateCtx(ctx); err != nil {
		t.Fatalf("seed cert: %v", err)
	}
	for _, name := range []string{"hanzo", "acme"} {
		org := orm.New[model.Organization](db)
		org.Owner, org.Name, org.DisplayName = "admin", name, name
		org.SetId("admin/" + name)
		if err := org.CreateCtx(ctx); err != nil {
			t.Fatalf("seed org %s: %v", name, err)
		}
	}
	signin := orm.New[model.Application](db)
	signin.Owner, signin.Name, signin.ClientId, signin.ClientSecret = "admin", "acme-app", "acme-app", acmeSecret
	signin.Organization, signin.Cert = "acme", "cert-hanzo"
	signin.EnablePassword, signin.EnableSignUp = true, true
	signin.RedirectUris = []string{acmeRedirect}
	signin.GrantTypes = []string{"authorization_code", "refresh_token"}
	signin.ExpireInHours = 1
	signin.SetId("admin/acme-app")
	if err := signin.CreateCtx(ctx); err != nil {
		t.Fatalf("seed acme-app: %v", err)
	}
	console := orm.New[model.Application](db)
	console.Owner, console.Name, console.ClientId, console.ClientSecret = "admin", "hanzo-console", "hanzo-console", consoleSecret
	console.Organization, console.Cert = "hanzo", "cert-hanzo"
	console.GrantTypes = []string{"client_credentials"}
	console.SetId("admin/hanzo-console")
	if err := console.CreateCtx(ctx); err != nil {
		t.Fatalf("seed hanzo-console: %v", err)
	}

	// The issuer is installed at boot, before the App is composed, as a host does it.
	if err := iamserver.RequireIssuer(); err != nil {
		t.Fatalf("install the issuer: %v", err)
	}
	id := &realIAM{app: iamserver.NewApp(db), db: db}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := httptest.NewRequest(r.Method, r.URL.RequestURI(), r.Body)
		req.Header = r.Header.Clone()
		res := id.call(t, req)
		for k, vs := range res.Header {
			w.Header()[k] = vs
		}
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}))
	t.Cleanup(srv.Close)
	id.url = srv.URL

	jwks, _ := io.ReadAll(id.call(t, httptest.NewRequest(http.MethodGet, "/.well-known/jwks", nil)).Body)
	js := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jwks) }))
	t.Cleanup(js.Close)
	id.jwks = js.URL
	return id
}

func (id *realIAM) call(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	req.Host = "hanzo.id"
	// A real IAM hashes a password and writes its store on signup and sign-in, which
	// a busy test runner does not finish inside the router's one-second test default.
	res, err := id.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func (id *realIAM) post(t *testing.T, path string, body map[string]string) map[string]any {
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

// signup registers a person through acme-app and reports the org IAM filed them in.
func (id *realIAM) signup(t *testing.T, email string) person {
	t.Helper()
	out := id.post(t, "/v1/iam/signup", map[string]string{
		"application": "acme-app", "clientId": "acme-app", "organization": "acme",
		"email": email, "password": testPassword,
	})
	data, _ := out["data"].(map[string]any)
	home, _ := data["owner"].(string)
	return person{home: home}
}

// signin runs the authorization-code flow and returns the access token IAM mints.
func (id *realIAM) signin(t *testing.T, email string) string {
	t.Helper()
	login := id.post(t, "/v1/iam/login", map[string]string{
		"type": "code", "application": "acme-app", "clientId": "acme-app", "organization": "acme",
		"username": email, "password": testPassword, "redirectUri": acmeRedirect, "scope": "openid",
	})
	code, _ := login["data"].(string)
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {acmeRedirect},
		"client_id": {"acme-app"}, "client_secret": {acmeSecret},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/iam/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(id.call(t, req).Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("token exchange for %s: %v", email, err)
	}
	return tok.AccessToken
}

// orgsClaim reads the `orgs` claim of a token IAM minted, as org → role. The
// signature is cloud's identity boundary's to check; this reads what it will read.
func orgsClaim(t *testing.T, token string) map[string]string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims struct {
		Orgs []struct{ Org, Role string } `json:"orgs"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	out := map[string]string{}
	for _, o := range claims.Orgs {
		out[o.Org] = o.Role
	}
	return out
}

func bearerCall(t *testing.T, app *zip.App, method, path, token, org, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}) // it reaches the real IAM above
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

// probeOrg reports the org cloud's identity boundary resolved for token asking to
// act in org.
func probeOrg(t *testing.T, app *zip.App, token, org string) string {
	t.Helper()
	code, body := bearerCall(t, app, http.MethodGet, "/probe/org", token, org, "")
	if code != http.StatusOK {
		t.Fatalf("probe: %d %s", code, body)
	}
	var out struct{ Org string }
	mustJSON(t, body, &out)
	return out.Org
}

// cloudOver mounts the account surface behind cloud's identity boundary, trusting
// id, and talking to id as hanzo-console.
func cloudOver(t *testing.T, id *realIAM) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	app.Use(cloud.IdentityMiddleware(&cloud.Config{IAMIssuer: hanzoIssuer, Discovery: oidctest.Discovery(hanzoIssuer), DiscoveryClient: oidctest.Client(hanzoIssuer, id.jwks)}))
	app.Use(cloud.Bridge())
	app.Raw(http.MethodGet, "/probe/org", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"org": c.Org()})
	})
	t.Setenv("IAM_URL", id.url)
	t.Setenv("IAM_MINT_CLIENT_ID", "hanzo-console")
	t.Setenv("IAM_MINT_CLIENT_SECRET", consoleSecret)
	t.Setenv("CLOUD_BRAND", "hanzo")
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app
}

// Two people creating one name at once: one org, one owner, and the other person
// is told the name is taken. IAM's create writes only when the name is free, so
// the second create cannot overwrite the first or make its caller an owner too.
func TestOnboard_RacingCreatorsLeaveOneOwner(t *testing.T) {
	id := hanzoID(t)
	id.signup(t, "bob@acme.test")
	id.signup(t, "carol@acme.test")
	tokens := []string{id.signin(t, "bob@acme.test"), id.signin(t, "carol@acme.test")}
	app := cloudOver(t, id)

	for round := range 20 {
		name := fmt.Sprintf("race-%d", round)
		var wg sync.WaitGroup
		start := make(chan struct{})
		codes := make([]int, len(tokens))
		for i, tok := range tokens {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i], _ = bearerCall(t, app, http.MethodPost, "/v1/account/orgs", tok, "", `{"name":"`+name+`"}`)
			}()
		}
		close(start)
		wg.Wait()

		slices.Sort(codes)
		if !slices.Equal(codes, []int{http.StatusOK, http.StatusConflict}) {
			t.Fatalf("%s: answers %v, want one 200 and one 409", name, codes)
		}
		rows, err := iamstore.MembershipsByOrg(context.Background(), id.db, name)
		if err != nil || len(rows) != 1 || rows[0].Role != "owner" {
			t.Fatalf("%s: roster %v (%v), want exactly one owner", name, rows, err)
		}
	}
}

// Only a person holding an access token founds an org. An application acting as
// itself and an id_token are refused before anything reaches IAM.
func TestOnboard_OnlyAPersonFoundsAnOrg(t *testing.T) {
	id := hanzoID(t)
	ctx := context.Background()
	bot := orm.New[model.Application](id.db)
	bot.Owner, bot.Name, bot.ClientId, bot.ClientSecret = "admin", "acme-bot", "acme-bot", "bot-secret"
	bot.Organization, bot.Cert = "acme", "cert-hanzo"
	bot.GrantTypes = []string{"client_credentials"}
	bot.SetId("admin/acme-bot")
	if err := bot.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	id.signup(t, "bob@acme.test")
	app := cloudOver(t, id)

	for _, tc := range []struct{ name, token, org string }{
		{"application", id.token(t, url.Values{
			"grant_type": {"client_credentials"}, "client_id": {"acme-bot"}, "client_secret": {"bot-secret"},
		}, "access_token"), "bot-org"},
		{"id_token", id.codeToken(t, "bob@acme.test", "id_token"), "via-id-token"},
	} {
		code, body := bearerCall(t, app, http.MethodPost, "/v1/account/orgs", tc.token, "", `{"name":"`+tc.org+`"}`)
		if code != http.StatusForbidden {
			t.Fatalf("%s: want 403, got %d (%s)", tc.name, code, body)
		}
		if org, _ := iamstore.GetOrganizationByName(ctx, id.db, tc.org); org != nil {
			t.Fatalf("%s: org %s was created", tc.name, tc.org)
		}
		if rows, _ := iamstore.MembershipsByOrg(ctx, id.db, tc.org); len(rows) != 0 {
			t.Fatalf("%s: membership rows written: %v", tc.name, rows)
		}
	}
}

// Memberships left by a removed org hold its name. A new org of that name would
// seat their holders in it, so creating one is refused and the caller is not
// added to the old roster.
func TestOnboard_LeftoverMembershipsHoldTheName(t *testing.T) {
	id := hanzoID(t)
	ctx := context.Background()
	id.signup(t, "bob@acme.test")
	bob := id.signin(t, "bob@acme.test")
	if _, err := iamstore.EnsureMembership(ctx, id.db, "acme/mallory", "widgets", "owner"); err != nil {
		t.Fatal(err)
	}
	app := cloudOver(t, id)

	code, body := bearerCall(t, app, http.MethodPost, "/v1/account/orgs", bob, "", `{"name":"Widgets"}`)
	if code != http.StatusConflict {
		t.Fatalf("want 409, got %d (%s)", code, body)
	}
	if org, _ := iamstore.GetOrganizationByName(ctx, id.db, "widgets"); org != nil {
		t.Fatalf("org widgets was created over another org's leftover rows")
	}
	rows, _ := iamstore.MembershipsByOrg(ctx, id.db, "widgets")
	if len(rows) != 1 || rows[0].User != "acme/mallory" {
		t.Fatalf("roster = %v, want only the leftover row", rows)
	}
}

// token posts form to IAM's token endpoint and returns the named token field.
func (id *realIAM) token(t *testing.T, form url.Values, field string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/iam/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out map[string]any
	if err := json.NewDecoder(id.call(t, req).Body).Decode(&out); err != nil {
		t.Fatalf("token: %v", err)
	}
	tok, _ := out[field].(string)
	if tok == "" {
		t.Fatalf("token endpoint returned no %s: %v", field, out)
	}
	return tok
}

// codeToken signs email in through the authorization-code flow and returns the
// named token field of the exchange.
func (id *realIAM) codeToken(t *testing.T, email, field string) string {
	t.Helper()
	login := id.post(t, "/v1/iam/login", map[string]string{
		"type": "code", "application": "acme-app", "clientId": "acme-app", "organization": "acme",
		"username": email, "password": testPassword, "redirectUri": acmeRedirect, "scope": "openid",
	})
	code, _ := login["data"].(string)
	return id.token(t, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {acmeRedirect},
		"client_id": {"acme-app"}, "client_secret": {acmeSecret},
	}, field)
}

// An org its people still live in cannot be deleted, even by its own admin: the
// name would be free with them inside, and the next person to create it would
// find the old admin administering their org and the old credential working in
// it. The name stays taken.
func TestOnboard_DeletingAnOrgWithPeopleInItIsRefused(t *testing.T) {
	id := hanzoID(t)
	ctx := context.Background()
	id.signup(t, "mallory@acme.test")
	id.signup(t, "bob@acme.test")
	app := cloudOver(t, id)

	// mallory lives in widgets, as its admin: the shape a signup at an application
	// that founds an org per account leaves.
	mallory := userByEmail(t, id, "mallory@acme.test")
	widgets := orm.New[model.Organization](id.db)
	widgets.Owner, widgets.Name, widgets.DisplayName, widgets.Founder = "admin", "widgets", "widgets", mallory.Model.Id()
	widgets.SetId("admin/widgets")
	if err := widgets.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	mallory.Owner, mallory.IsAdmin = "widgets", true
	if err := mallory.UpdateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	signin := orm.New[model.Application](id.db)
	signin.Owner, signin.Name, signin.ClientId, signin.ClientSecret = "admin", "widgets-app", "widgets-app", "w-secret"
	signin.Organization, signin.Cert = "widgets", "cert-hanzo"
	signin.EnablePassword = true
	signin.RedirectUris = []string{"https://widgets.test/cb"}
	signin.GrantTypes = []string{"authorization_code"}
	signin.ExpireInHours = 1
	signin.SetId("admin/widgets-app")
	if err := signin.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	mal := id.signinVia(t, "widgets-app", "w-secret", "https://widgets.test/cb", "widgets", "mallory@acme.test")

	if code, body := iamReq(t, id, http.MethodDelete, "/v1/iam/organizations/admin/widgets", mal, ""); code != http.StatusConflict {
		t.Fatalf("home admin deleting an org they live in: %d %s, want 409", code, body)
	}
	if o, _ := iamstore.GetOrganizationByName(ctx, id.db, "widgets"); o == nil {
		t.Fatal("org removed with its people still in it")
	}
	bob := id.signin(t, "bob@acme.test")
	if code, body := bearerCall(t, app, http.MethodPost, "/v1/account/orgs", bob, "", `{"name":"Widgets"}`); code != http.StatusConflict {
		t.Fatalf("another person creating the name: %d %s, want 409", code, body)
	}
}

// A brand org holds the platform's own people, keys and applications, and the seed
// restores only its row. Its own admin and the org-capable console run it; neither
// deletes it.
func TestOnboard_BrandOrgIsDeletedBySuperAdminOnly(t *testing.T) {
	id := hanzoID(t)
	ctx := context.Background()
	hanzo, err := iamstore.GetOrganizationByName(ctx, id.db, "hanzo")
	if err != nil || hanzo == nil {
		t.Fatalf("hanzo org: %v", err)
	}
	hanzo.Platform = true // what the seed stamps on every org it declares
	if err := hanzo.UpdateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	id.brandApp(t)
	owned := orm.New[model.Application](id.db)
	owned.Owner, owned.Name, owned.ClientId, owned.Organization = "hanzo", "hanzo-finance", "hanzo-finance", "hanzo"
	owned.SetId("hanzo/hanzo-finance")
	if err := owned.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	id.brandStaff(t, "staff@hanzo.test")

	console := id.token(t, url.Values{"grant_type": {"client_credentials"}, "client_id": {"hanzo-console"},
		"client_secret": {consoleSecret}}, "access_token")
	for name, bearer := range map[string]string{
		"brand org admin": id.brandSignin(t, "staff@hanzo.test"),
		"hanzo-console":   console,
	} {
		if code, body := iamReq(t, id, http.MethodDelete, "/v1/iam/organizations/admin/hanzo", bearer, ""); code != http.StatusForbidden {
			t.Fatalf("%s deleting hanzo: %d %s, want 403", name, code, body)
		}
	}
	if o, _ := iamstore.GetOrganizationByName(ctx, id.db, "hanzo"); o == nil {
		t.Fatal("brand org removed")
	}
	if a, _ := orm.Get[model.Application](id.db, "hanzo/hanzo-finance"); a == nil {
		t.Fatal("brand-owned application removed")
	}
}

// The people of a brand org create their org at POST /v1/account/orgs — the
// address the console and hanzo.ai call. Nobody is moved: an admin of the brand
// org and a person who only signed up into it each own the org they create by
// membership and keep the account they signed in with. The person who signed up
// signs in again with the same address and password afterwards — the lockout this
// holds shut: moving them re-keyed their account into the new org, and the
// sign-in, which looks the address up in the signup org, found nobody and offered
// them a second account.
func TestOnboard_BrandOrgPeopleCreateTheirOrg(t *testing.T) {
	id := hanzoID(t)
	ctx := context.Background()
	id.brandApp(t)
	staff := id.brandStaff(t, "staff@hanzo.test")
	id.post(t, "/v1/iam/signup", map[string]string{"application": "hanzo-app", "clientId": "hanzo-app",
		"organization": "hanzo", "email": "newcomer@hanzo.test", "password": testPassword})
	newcomer := userByEmail(t, id, "newcomer@hanzo.test")
	app := cloudOver(t, id)

	code, body := bearerCall(t, app, http.MethodPost, "/v1/account/orgs", id.brandSignin(t, "staff@hanzo.test"), "", `{"name":"Staff Side"}`)
	if code != http.StatusOK {
		t.Fatalf("brand org admin creating an org: %d %s, want 200", code, body)
	}
	var side onboardResp
	mustJSON(t, body, &side)
	if side.Org != "staff-side" {
		t.Fatalf("brand org admin got %+v, want the org staff-side", side)
	}
	rows, err := iamstore.MembershipsByOrg(ctx, id.db, "staff-side")
	if err != nil || len(rows) != 1 || rows[0].User != "hanzo/"+staff.Name || rows[0].Role != "owner" {
		t.Fatalf("staff-side roster = %v (%v), want hanzo/%s as its owner", rows, err, staff.Name)
	}
	if u := userByEmail(t, id, "staff@hanzo.test"); u.Owner != "hanzo" {
		t.Fatalf("brand org admin moved to %q; creating an org moves nobody", u.Owner)
	}

	access, renew := id.brandSession(t, "newcomer@hanzo.test")
	code, body = bearerCall(t, app, http.MethodPost, "/v1/account/orgs", access, "", `{"name":"Newco"}`)
	if code != http.StatusOK {
		t.Fatalf("brand org signup creating their org: %d %s, want 200", code, body)
	}
	var made onboardResp
	mustJSON(t, body, &made)
	if made.Org != "newco" || made.AccessSecret == "" {
		t.Fatalf("brand org signup got %+v, want the org newco and its key, once", made)
	}
	if org, _ := iamstore.GetOrganizationByName(ctx, id.db, "newco"); org == nil {
		t.Fatal("org newco is not in IAM")
	}
	if u := userByEmail(t, id, "newcomer@hanzo.test"); u.Owner != "hanzo" || u.Name != newcomer.Name || u.IsAdmin {
		t.Fatalf("the creator's account is %s/%s admin=%v, want hanzo/%s unchanged", u.Owner, u.Name, u.IsAdmin, newcomer.Name)
	}
	rows, err = iamstore.MembershipsByOrg(ctx, id.db, "newco")
	if err != nil || len(rows) != 1 || rows[0].User != "hanzo/"+newcomer.Name || rows[0].Role != "owner" {
		t.Fatalf("newco roster = %v (%v), want hanzo/%s as its owner", rows, err, newcomer.Name)
	}
	if k, _ := orm.TypedQuery[schema.Key](id.db).Filter("Owner=", "newco").Filter("Name=", "default").First(); k == nil || k.User != "hanzo/"+newcomer.Name {
		t.Fatalf("newco's default key = %+v, want it held by hanzo/%s", k, newcomer.Name)
	}

	// The same address and password sign them in again, and that token carries
	// the org they created; so does the session they already hold, renewed.
	again := id.brandSignin(t, "newcomer@hanzo.test")
	if orgs := orgsClaim(t, again); orgs["hanzo"] == "" || orgs["newco"] != "owner" {
		t.Fatalf("signing in again: orgs claim = %v, want hanzo plus newco as owner", orgs)
	}
	if got := probeOrg(t, app, again, "newco"); got != "newco" {
		t.Fatalf("X-Org-Id newco resolved to %q for the creator", got)
	}
	renewed := id.token(t, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {renew},
		"client_id": {"hanzo-app"}, "client_secret": {"h-secret"},
	}, "access_token")
	if orgs := orgsClaim(t, renewed); orgs["newco"] != "owner" {
		t.Fatalf("renewed orgs claim = %v, want newco as owner", orgs)
	}
}

// brandApp registers hanzo-app, the sign-in application of the brand org hanzo.
func (id *realIAM) brandApp(t *testing.T) {
	t.Helper()
	brand := orm.New[model.Application](id.db)
	brand.Owner, brand.Name, brand.ClientId, brand.ClientSecret = "admin", "hanzo-app", "hanzo-app", "h-secret"
	brand.Organization, brand.Cert = "hanzo", "cert-hanzo"
	brand.EnablePassword, brand.EnableSignUp = true, true
	brand.RedirectUris = []string{"https://hanzo.test/cb"}
	brand.GrantTypes = []string{"authorization_code", "refresh_token"}
	brand.ExpireInHours, brand.RefreshExpireInHours = 1, 24
	brand.SetId("admin/hanzo-app")
	if err := brand.CreateCtx(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// brandStaff signs email up through hanzo-app and makes them an admin of the
// brand org — not a SuperAdmin.
func (id *realIAM) brandStaff(t *testing.T, email string) *model.User {
	t.Helper()
	id.post(t, "/v1/iam/signup", map[string]string{"application": "hanzo-app", "clientId": "hanzo-app",
		"organization": "hanzo", "email": email, "password": testPassword})
	staff := userByEmail(t, id, email)
	staff.IsAdmin = true
	if err := staff.UpdateCtx(context.Background()); err != nil {
		t.Fatal(err)
	}
	return staff
}

// brandSignin signs email in through hanzo-app and returns the access token.
func (id *realIAM) brandSignin(t *testing.T, email string) string {
	t.Helper()
	return id.signinVia(t, "hanzo-app", "h-secret", "https://hanzo.test/cb", "hanzo", email)
}

// brandSession signs email in through hanzo-app and returns the access token and
// the refresh token the exchange issued.
func (id *realIAM) brandSession(t *testing.T, email string) (access, refresh string) {
	t.Helper()
	login := id.post(t, "/v1/iam/login", map[string]string{
		"type": "code", "application": "hanzo-app", "clientId": "hanzo-app", "organization": "hanzo",
		"username": email, "password": testPassword, "redirectUri": "https://hanzo.test/cb", "scope": "openid offline_access",
	})
	code, _ := login["data"].(string)
	req := httptest.NewRequest(http.MethodPost, "/v1/iam/oauth/token", strings.NewReader(url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"https://hanzo.test/cb"},
		"client_id": {"hanzo-app"}, "client_secret": {"h-secret"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(id.call(t, req).Body).Decode(&out); err != nil || out.AccessToken == "" || out.RefreshToken == "" {
		t.Fatalf("hanzo-app session for %s: %v %+v", email, err, out)
	}
	return out.AccessToken, out.RefreshToken
}

// iamReq sends one request to the embedded IAM with a bearer and an optional JSON
// body, and returns the status and body.
func iamReq(t *testing.T, id *realIAM, method, path, bearer, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res := id.call(t, req)
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// signinVia signs email in through app and returns the access token.
func (id *realIAM) signinVia(t *testing.T, app, secret, redirect, org, email string) string {
	t.Helper()
	login := id.post(t, "/v1/iam/login", map[string]string{
		"type": "code", "application": app, "clientId": app, "organization": org,
		"username": email, "password": testPassword, "redirectUri": redirect, "scope": "openid",
	})
	code, _ := login["data"].(string)
	return id.token(t, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {app}, "client_secret": {secret},
	}, "access_token")
}

func userByEmail(t *testing.T, id *realIAM, email string) *model.User {
	t.Helper()
	u, err := orm.TypedQuery[model.User](id.db).Filter("Email=", email).First()
	if err != nil || u == nil {
		t.Fatalf("no user row for %s: %v", email, err)
	}
	return u
}
