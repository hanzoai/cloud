package host

// A delegated token (confined.go; iam LLM.md "Delegation") at the identity
// boundary: it is the person, in the one org its run bills, with the deployment
// that ran the work recorded beside them; it reaches model calls and nothing
// else; and it is verified or refused, never nobody.

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	adminpeer "github.com/hanzoai/cloud/client/admin"
	cloud "github.com/hanzoai/cloud/sdk"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

const (
	aliceSub   = "9b1d0a2e-alice"
	cloudActor = "admin/hanzo-cloud"
)

// delegatedFor is the claim set IAM's delegation grant signs: the person, their
// one org, ai:inference, an https audience and no authorized party.
func delegatedFor(org, role string, exp time.Time) authz.Claims {
	return authz.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: testIssuer, Subject: aliceSub, Audience: jwt.ClaimStrings{"https://api.hanzo.ai"},
			ExpiresAt: jwt.NewNumericDate(exp), IssuedAt: jwt.NewNumericDate(time.Now()),
		},
		Scope: "ai:inference", Owner: org, Organization: org, Name: "alice", PreferredUsername: "alice",
		TokenType: "access-token", Orgs: []authz.Membership{{Org: org, Role: authz.Role(role)}},
	}
}

// keys are the rig's two signing keys: the one the public JWKS publishes (kid
// test-key), and IAM's delegation key, which only fakeIAM serves, and only to
// this deployment's own credential.
type keys struct{ public, delegation *rsa.PrivateKey }

// fakeIAM is the two IAM addresses the boundary reads: the delegation key, to
// hanzo-cloud's credential only, and introspection.
type fakeIAM struct {
	srv   *httptest.Server
	mu    sync.Mutex
	dead  map[string]bool // tokens revoked: introspection answers them inactive
	down  bool            // every address answers 503
	asks  int             // introspections answered
	reads int             // delegation-key reads attempted
}

func serveIAM(t *testing.T, delegation *rsa.PublicKey) *fakeIAM {
	t.Helper()
	f := &fakeIAM{dead: map[string]bool{}}
	jwk := map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "cert-delegation", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(delegation.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(delegation.E)).Bytes()),
	}}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/v1/iam/oauth/delegation/keys" {
			f.reads++
		}
		if f.down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/iam/oauth/delegation/keys":
			if id, secret, ok := r.BasicAuth(); !ok || id != "hanzo-cloud" || secret != "cloud-secret" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(jwk)
		case "/v1/iam/oauth/introspect":
			_ = r.ParseForm()
			if r.PostForm.Get("client_id") != "hanzo-cloud" || r.PostForm.Get("client_secret") != "cloud-secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			f.asks++
			_ = json.NewEncoder(w).Encode(map[string]any{"active": !f.dead[r.PostForm.Get("token")]})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	t.Setenv("IAM_URL", f.srv.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "cloud-secret")
	return f
}

// seenBy is what a handler behind the boundary observed.
type seenBy struct {
	reached         bool
	org, user, name string
	super           bool
	minted          principal.Principal
	payer           account.Account
	ledger          string
}

// boundaryApp is the production order — SanitizeIdentity, then AuditTrail —
// in front of a handler at every address the tests ask about, with IAM's two
// addresses served by a fake.
func boundaryApp(t *testing.T, addrs [][2]string) (*zip.App, *audit.Recorder, *seenBy, keys, *cloud.Boundary, *fakeIAM) {
	t.Helper()
	var k keys
	for _, p := range []**rsa.PrivateKey{&k.public, &k.delegation} {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		*p = key
	}
	iam := serveIAM(t, &k.delegation.PublicKey)
	v := cloud.BoundaryOf(&cloud.Config{IAMIssuer: testIssuer, Discovery: oidctest.Discovery(testIssuer), DiscoveryClient: oidctest.Client(testIssuer, jwksServer(t, &k.public.PublicKey).URL)})
	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	seen := &seenBy{}
	app := zip.New(zip.Config{})
	app.Use(cloud.SanitizeIdentity(v))
	app.Use(cloud.AuditTrail(rec))
	h := func(c *zip.Ctx) error {
		seen.reached = true
		seen.org, seen.user, seen.name = c.Org(), c.User(), c.Header(authz.HeaderUserName)
		seen.super = principal.Super(c)
		seen.minted, _ = principal.Minted(c)
		seen.payer = meter.Payer(c)
		seen.ledger, _ = principal.BillingOrg(c)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	}
	for _, a := range addrs {
		app.Raw(a[0], a[1], h)
	}
	return app, rec, seen, k, v, iam
}

func through(t *testing.T, app *zip.App, method, path, tok string, edit ...func(*http.Request)) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for _, e := range edit {
		e(req)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp.StatusCode
}

var (
	modelAddrs = [][2]string{
		{http.MethodPost, "/v1/responses"},
		{http.MethodPost, "/v1/chat/completions"},
		{http.MethodPost, "/v1/messages"},
		{http.MethodPost, "/v1/messages/count_tokens"},
		{http.MethodGet, "/v1/models"},
		{http.MethodGet, "/v1/models/:id"},
	}
	otherAddrs = [][2]string{
		{http.MethodGet, "/v1/kms/secrets"},
		{http.MethodPost, "/v1/kms/secrets"},
		{http.MethodPost, "/v1/account/keys"},
		{http.MethodGet, "/v1/billing/balance"},
		{http.MethodPost, "/v1/billing/topup"},
		{http.MethodGet, "/v1/iam/users"},
		{http.MethodGet, "/v1/responses"},
		{http.MethodPost, "/v1/models/:id/access"},
		{http.MethodPost, "/v1/embeddings"},
		{http.MethodPost, "/v1/chat"},
		{http.MethodPost, "/v1/coding/runs"},
		{http.MethodPost, manifest.MCPPath},
		{http.MethodPost, zip.CallPath + "kms.secret.get"},
	}
)

// Every model call a delegated token makes is on the trail as the person, in the
// run's org, with the deployment that acted for them — its begun record and its
// outcome both — and the chain still verifies across rows that carry an agent and
// rows that do not.
func TestAuditNamesTheAgent(t *testing.T) {
	app, rec, _, k, _, _ := boundaryApp(t, modelAddrs)
	through(t, app, http.MethodPost, "/v1/responses", k.own(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)), "openid"))
	through(t, app, http.MethodPost, "/v1/responses", k.delegated(t, delegatedFor("acme", "member", time.Now().Add(time.Hour))))

	rows, total, err := rec.Query(t.Context(), audit.Filter{})
	if err != nil || total != 4 {
		t.Fatalf("rows %d err %v, want each call's begun record and its outcome", total, err)
	}
	agents := map[string]bool{}
	for _, r := range rows {
		if r.Actor.Org != "acme" || r.Actor.Sub != aliceSub || r.Actor.Act.Sub != "" {
			t.Errorf("actor = %+v", r.Actor)
		}
		agents[r.Actor.Agent] = true
		if w := r.ToWire(); w.Agent != r.Actor.Agent {
			t.Errorf("wire agent = %q, want %q", w.Agent, r.Actor.Agent)
		}
		if back := adminpeer.TrailRecord(adminpeer.PlaneRecord(r)); back.Actor.Agent != r.Actor.Agent {
			t.Errorf("the plane lost the agent: %q", back.Actor.Agent)
		}
	}
	if !agents[""] || !agents[cloudActor] {
		t.Fatalf("agents = %v, want one row with none and one naming %s", agents, cloudActor)
	}
	if iv, _ := rec.Verify(t.Context()); iv.Verdict != audit.Intact {
		t.Fatalf("chain broke at %d (%s)", iv.BrokenAt, iv.Reason)
	}
}

// delegated is a token the delegation key signed: what IAM's grant answers.
func (k keys) delegated(t *testing.T, c authz.Claims) string {
	return signed(t, k.delegation, "cert-delegation", c, cloudAct)
}

// own is the person's own token under the public key: no actor, and the scope
// the caller gives it.
func (k keys) own(t *testing.T, c authz.Claims, scope string) string {
	c.Scope = scope
	return signed(t, k.public, "test-key", c, nil)
}

// signed signs c with `act` beside it, as IAM does, under kid; authz.Claims has
// no field for `act`, which is why the boundary reads it off the verified payload.
func signed(t *testing.T, key *rsa.PrivateKey, kid string, c authz.Claims, act map[string]string) string {
	t.Helper()
	payload := struct {
		authz.Claims
		Act map[string]string `json:"act,omitempty"`
	}{c, act}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, payload)
	tok.Header["kid"] = kid
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

var cloudAct = map[string]string{"sub": cloudActor, "owner": "admin", "name": "hanzo-cloud"}
