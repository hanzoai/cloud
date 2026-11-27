package cloud

// A delegated token (confined.go; iam LLM.md "Delegation") at the identity
// boundary: it is the person, in the one org its run bills, with the deployment
// that ran the work recorded beside them; it reaches model calls and nothing
// else; and it is verified or refused, never nobody.

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
		Scope: inferenceScope, Owner: org, Organization: org, Name: "alice", PreferredUsername: "alice",
		TokenType: "access-token", Orgs: []authz.Membership{{Org: org, Role: authz.Role(role)}},
	}
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

// keys are the rig's two signing keys: the one the public JWKS publishes (kid
// test-key), and IAM's delegation key, which only fakeIAM serves, and only to
// this deployment's own credential.
type keys struct{ public, delegation *rsa.PrivateKey }

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
		if r.URL.Path == delegationKeysPath {
			f.reads++
		}
		if f.down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case delegationKeysPath:
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
func boundaryApp(t *testing.T, addrs [][2]string) (*zip.App, *audit.Recorder, *seenBy, keys, *Boundary, *fakeIAM) {
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
	v := newIdentityValidator(testIssuer, jwksServer(t, &k.public.PublicKey).URL, 0)
	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	seen := &seenBy{}
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v))
	app.Use(AuditTrail(rec))
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

// confinedCall is through with the body, for a refusal that has to say which one it is.
func confinedCall(t *testing.T, app *zip.App, method, path, tok string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
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
		{http.MethodPost, "/v1/decisions"},
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

func TestDelegatedTokenActsAsThePersonInItsOrg(t *testing.T) {
	app, _, seen, k, _, _ := boundaryApp(t, modelAddrs)
	tok := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)))

	// It names its org; a selection of any other is not honoured.
	if got := through(t, app, http.MethodPost, "/v1/responses", tok, func(r *http.Request) { r.Header.Set("X-Org-Id", "other") }); got != 200 {
		t.Fatalf("a delegated model call answered %d", got)
	}
	if seen.org != "acme" || seen.user != aliceSub || seen.name != "alice" || seen.super {
		t.Fatalf("principal = org %q user %q name %q super %v, want the person in acme", seen.org, seen.user, seen.name, seen.super)
	}
	if seen.minted.Agent != cloudActor || seen.minted.Act.Sub != "" || seen.minted.Holder != "" || seen.minted.Kind != principal.Person {
		t.Fatalf("attestation = %+v, want the person with the deployment as agent, no operator and no holder", seen.minted)
	}
	// Money: acme's ledger, and acme pools.
	if seen.ledger != "acme" || seen.payer != account.Org("acme") {
		t.Fatalf("ledger/payer = %q/%v, want acme's pool", seen.ledger, seen.payer)
	}

	// IAM authorizes the person's standing before delegating, then strips admin
	// roles. A platform-org operator therefore arrives as a member too, but the
	// single delegated org must still fund the model call. No admin rights return.
	claims := delegatedFor("hanzo", "member", time.Now().Add(time.Hour))
	claims.BillingAccount = "org:hanzo"
	tok = k.delegated(t, claims)
	through(t, app, http.MethodPost, "/v1/chat/completions", tok)
	if seen.org != "hanzo" || seen.ledger != "hanzo" || seen.payer != account.Org("hanzo") || seen.super {
		t.Fatalf("org/ledger/payer = %q/%q/%v super=%v, want the delegated payer without admin", seen.org, seen.ledger, seen.payer, seen.super)
	}
}

func TestDelegatedTokenReachesModelCallsOnly(t *testing.T) {
	app, _, seen, k, _, _ := boundaryApp(t, append(append([][2]string{}, modelAddrs...), otherAddrs...))
	tok := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)))
	for _, a := range [][2]string{
		{http.MethodPost, "/v1/responses"}, {http.MethodPost, "/v1/chat/completions"},
		{http.MethodPost, "/v1/messages"}, {http.MethodPost, "/v1/messages/count_tokens"},
		{http.MethodPost, "/v1/decisions"},
		{http.MethodGet, "/v1/models"}, {http.MethodGet, "/v1/models/zen5"}, {http.MethodPost, "/V1/Responses/"},
	} {
		if got := through(t, app, a[0], a[1], tok); got != 200 {
			t.Errorf("%s %s = %d, want 200", a[0], a[1], got)
		}
	}
	for _, a := range [][2]string{
		{http.MethodGet, "/v1/kms/secrets"}, {http.MethodPost, "/v1/kms/secrets"},
		{http.MethodPost, "/v1/account/keys"}, {http.MethodGet, "/v1/billing/balance"},
		{http.MethodPost, "/v1/billing/topup"}, {http.MethodGet, "/v1/iam/users"},
		{http.MethodGet, "/v1/responses"}, {http.MethodPost, "/v1/models/zen5/access"},
		{http.MethodPost, "/v1/embeddings"}, {http.MethodPost, "/v1/chat"},
		{http.MethodPost, "/v1/coding/runs"}, {http.MethodPost, manifest.MCPPath},
		{http.MethodPost, zip.CallPath + "kms.secret.get"},
	} {
		seen.reached = false
		if got := through(t, app, a[0], a[1], tok); got != 403 || seen.reached {
			t.Errorf("%s %s = %d (handler reached: %v), want 403 at the boundary", a[0], a[1], got, seen.reached)
		}
	}
	// The same person's own token is not confined: the rule is the scope.
	if got := through(t, app, http.MethodGet, "/v1/kms/secrets", k.own(t, delegatedFor("acme", "owner", time.Now().Add(time.Hour)), "openid profile")); got != 200 {
		t.Errorf("an ordinary token was confined: %d", got)
	}
}

// A confined token is verified against IAM's delegation key and nothing else, is
// addressed to this API and names the actor that delegated it, and each way it
// can fail says which.
func TestDelegatedTokenIsVerifiedOrRefused(t *testing.T) {
	app, _, seen, k, _, _ := boundaryApp(t, append(append([][2]string{}, modelAddrs...), otherAddrs...))
	live := func(exp time.Time) authz.Claims { return delegatedFor("acme", "member", exp) }
	hour := time.Now().Add(time.Hour)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	elsewhere := live(hour)
	elsewhere.Audience = jwt.ClaimStrings{"https://admin.hanzo.ai"}
	noOrg := live(hour)
	noOrg.Orgs = nil
	multipleOrgs := live(hour)
	multipleOrgs.Orgs = append(multipleOrgs.Orgs, authz.Membership{Org: "other", Role: "member"})
	for name, tc := range map[string]struct {
		tok  string
		want string
	}{
		"expired":                {k.delegated(t, live(time.Now().Add(-10*time.Minute))), "has expired"},
		"a stranger's key":       {signed(t, other, "cert-delegation", live(hour), cloudAct), "does not verify"},
		"the PUBLIC key":         {k.own(t, live(hour), inferenceScope), "does not verify"},
		"published key, actor":   {signed(t, k.public, "test-key", live(hour), cloudAct), "does not verify"},
		"another API":            {k.delegated(t, elsewhere), "does not verify"},
		"no organization":        {k.delegated(t, noOrg), "does not verify"},
		"multiple organizations": {k.delegated(t, multipleOrgs), "does not verify"},
		"no delegating actor":    {signed(t, k.delegation, "cert-delegation", live(hour), nil), "does not verify"},
	} {
		for _, a := range [][2]string{{http.MethodPost, "/v1/responses"}, {http.MethodGet, "/v1/models"}, {http.MethodGet, "/v1/kms/secrets"}} {
			seen.reached = false
			if got, body := confinedCall(t, app, a[0], a[1], tc.tok); got != 401 || seen.reached || !strings.Contains(body, tc.want) {
				t.Errorf("%s: %s %s = %d %s (reached %v), want 401 saying %q", name, a[0], a[1], got, body, seen.reached, tc.want)
			}
		}
	}
	// An ordinary expired token still goes on as nobody, as it always has.
	seen.reached = false
	if got := through(t, app, http.MethodGet, "/v1/models", k.own(t, live(time.Now().Add(-10*time.Minute)), "openid")); got != 200 || seen.user != "" {
		t.Errorf("an expired ordinary token = %d user %q, want anonymous 200", got, seen.user)
	}
}

// The delegation key is read from IAM with this deployment's credential. With
// none readable a confined token cannot be verified, and the answer says so —
// a retry, not a run that is over.
func TestDelegationKeysUnreachableIsSaid(t *testing.T) {
	app, _, seen, k, _, iam := boundaryApp(t, modelAddrs)
	tok := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)))
	iam.mu.Lock()
	iam.down = true
	iam.mu.Unlock()
	if got, body := confinedCall(t, app, http.MethodPost, "/v1/responses", tok); got != 503 || seen.reached || !strings.Contains(body, "unreachable") {
		t.Fatalf("keys down = %d %s, want 503 naming IAM", got, body)
	}
	// A credential IAM refuses reads no key either.
	iam.mu.Lock()
	iam.down = false
	iam.mu.Unlock()
	t.Setenv("IAM_CLIENT_SECRET", "wrong")
	v := newIdentityValidator(testIssuer, "http://127.0.0.1:1", 0)
	if _, err := v.validate(tok); !errors.Is(err, errDelegationKeys) {
		t.Fatalf("a refused credential read a delegation key: %v", err)
	}
}

func TestDelegatedTokenIsNoSessionAndHoldsNoSecret(t *testing.T) {
	_, _, _, k, v, _ := boundaryApp(t, nil)
	tok := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)))
	if _, err := (&TokenValidator{v: v}).Validate(tok); err == nil {
		t.Fatal("a delegated token validated as a session")
	}
	c, err := v.validate(tok)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if h, _ := holderOf(c); h != "" {
		t.Fatalf("a delegated token holds %q", h)
	}
	if !c.confined || c.agent != cloudActor {
		t.Fatalf("confined %v agent %q", c.confined, c.agent)
	}
}

// The kill switch: a confined token is honoured only while IAM still holds its
// grant. One question per token per window, a revocation refused once the
// window turns, and an IAM that cannot be asked refuses rather than admits.
func TestARevokedDelegatedTokenIsRefused(t *testing.T) {
	app, _, seen, k, v, iam := boundaryApp(t, modelAddrs)
	tok := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)))
	if got := through(t, app, http.MethodPost, "/v1/responses", tok); got != 200 {
		t.Fatalf("a live delegated token = %d", got)
	}
	iam.mu.Lock()
	iam.dead[tok] = true
	iam.mu.Unlock()
	// Inside the window the last answer stands: IAM is not asked per request.
	if got := through(t, app, http.MethodPost, "/v1/responses", tok); got != 200 || iam.asks != 1 {
		t.Fatalf("second call = %d after %d introspections, want 200 after 1", got, iam.asks)
	}
	// Past it, the revocation is honoured.
	v.live.mu.Lock()
	for key, e := range v.live.seen {
		e.at = e.at.Add(-livenessTTL)
		v.live.seen[key] = e
	}
	v.live.mu.Unlock()
	seen.reached = false
	if got, body := confinedCall(t, app, http.MethodPost, "/v1/responses", tok); got != 401 || seen.reached || !strings.Contains(body, "revoked") {
		t.Fatalf("a revoked delegated token = %d %s", got, body)
	}
	// IAM that cannot be asked: refused, and said to be a retry.
	fresh := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(2*time.Hour)))
	iam.mu.Lock()
	iam.down = true
	iam.mu.Unlock()
	if got, body := confinedCall(t, app, http.MethodPost, "/v1/responses", fresh); got != 503 || !strings.Contains(body, "cannot confirm") {
		t.Fatalf("liveness unknown = %d %s, want 503", got, body)
	}
}

// A confined model call may not ask the gateway to search the org's documents
// into its prompt, in any spelling ai reads; an ordinary call keeps doing so.
func TestADelegatedTokenReadsNoDocuments(t *testing.T) {
	app, _, seen, k, _, _ := boundaryApp(t, modelAddrs)
	tok := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)))
	post := func(tok, body string, hdr ...string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	plain := `{"model":"zen5","messages":[{"role":"user","content":"hi"}],"store":false}`
	if got := post(tok, plain); got != 200 {
		t.Fatalf("a plain delegated completion = %d", got)
	}
	for name, ask := range map[string][]string{
		"X-Retrieval":       {plain, "X-Retrieval", "1"},
		"X-Retrieval-Store": {plain, "X-Retrieval-Store", "docs"},
		"body retrieval":    {`{"model":"zen5","retrieval":true}`},
		"body Retrieval":    {`{"model":"zen5","Retrieval":true}`},
		"body store":        {`{"model":"zen5","retrieval_store":"docs"}`},
	} {
		seen.reached = false
		if got := post(tok, ask[0], ask[1:]...); got != 403 || seen.reached {
			t.Errorf("%s: %d (reached %v), want 403 at the boundary", name, got, seen.reached)
		}
	}
	// ai's own answer decides: an explicit X-Retrieval: 0 turns it off there, so it
	// is no ask here either.
	if got := post(tok, `{"model":"zen5","retrieval":true}`, "X-Retrieval", "0"); got != 200 {
		t.Errorf("retrieval switched off by its header = %d, want 200", got)
	}
	own := k.own(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)), "openid")
	if got := post(own, `{"model":"zen5","retrieval":true}`); got != 200 {
		t.Errorf("an ordinary token's retrieval = %d", got)
	}
}

// Never having read the delegation key, the boundary asks IAM again only after a
// short floor: a burst of confined calls while IAM is down is one read, not one
// per call.
func TestAnUnreadDelegationKeyIsNotAskedForPerRequest(t *testing.T) {
	app, _, _, k, _, iam := boundaryApp(t, modelAddrs)
	iam.mu.Lock()
	iam.down = true
	iam.mu.Unlock()
	tok := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)))
	for range 5 {
		if got := through(t, app, http.MethodPost, "/v1/responses", tok); got != 503 {
			t.Fatalf("keys down = %d, want 503", got)
		}
	}
	iam.mu.Lock()
	defer iam.mu.Unlock()
	if iam.reads != 1 {
		t.Fatalf("five calls read IAM's delegation key %d times, want 1", iam.reads)
	}
}

// A token past its own expiry — inside the verifier's two minutes of clock
// leeway, where IAM, which allows none, already answers it inactive — ran out. It
// was not revoked, and the refusal says which.
func TestAnExpiredDelegatedTokenIsNotCalledRevoked(t *testing.T) {
	app, _, _, k, _, iam := boundaryApp(t, modelAddrs)
	tok := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(-30*time.Second)))
	iam.mu.Lock()
	iam.dead[tok] = true
	iam.mu.Unlock()
	if got, body := confinedCall(t, app, http.MethodPost, "/v1/responses", tok); got != 401 || !strings.Contains(body, "has expired") {
		t.Fatalf("an expired delegated token = %d %s, want 401 saying it expired", got, body)
	}
}

// Confined is the boundary's verdict, read by the model API behind it: the same
// verifier, asked about the same bytes.
func TestConfinedIsTheBoundarysVerdict(t *testing.T) {
	app, _, _, k, _, _ := boundaryApp(t, modelAddrs)
	tok := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)))
	if got := through(t, app, http.MethodPost, "/v1/responses", tok); got != 200 {
		t.Fatalf("the boundary refused a delegated token: %d", got)
	}
	if err := Confined(tok); err != nil {
		t.Fatalf("the boundary admitted it and Confined refuses it: %v", err)
	}
	if err := Confined(k.own(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)), "openid")); err == nil {
		t.Fatal("Confined vouched for a token that is not delegated")
	}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	if err := Confined(signed(t, other, "cert-delegation", delegatedFor("acme", "member", time.Now().Add(time.Hour)), cloudAct)); err == nil {
		t.Fatal("Confined vouched for a token under a stranger's key")
	}
}

// TestParentRevocationDoesNotRevokeChildDelegatedToken proves that revoking a
// parent run's delegated token (A) immediately returns 401 revoked for A while
// an independent child run's delegated token (B) for the same org and person
// keeps returning 200 until B itself is revoked at the end of the child run.
func TestParentRevocationDoesNotRevokeChildDelegatedToken(t *testing.T) {
	app, _, _, k, v, iam := boundaryApp(t, modelAddrs)
	parentTok := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)))
	childTok := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(2*time.Hour)))
	if parentTok == childTok {
		t.Fatal("parent and child delegated tokens collided")
	}
	if got := through(t, app, http.MethodPost, "/v1/responses", parentTok); got != 200 {
		t.Fatalf("live parent token = %d, want 200", got)
	}
	if got := through(t, app, http.MethodPost, "/v1/responses", childTok); got != 200 {
		t.Fatalf("live child token = %d, want 200", got)
	}

	// Parent run ends and revokes token A.
	iam.mu.Lock()
	iam.dead[parentTok] = true
	iam.mu.Unlock()
	v.live.mu.Lock()
	for key, e := range v.live.seen {
		e.at = e.at.Add(-livenessTTL)
		v.live.seen[key] = e
	}
	v.live.mu.Unlock()

	if got, body := confinedCall(t, app, http.MethodPost, "/v1/responses", parentTok); got != 401 || !strings.Contains(body, "this delegated token was revoked") {
		t.Fatalf("revoked parent token = %d %s, want 401 revoked", got, body)
	}
	// Child token B still succeeds after fresh IAM introspection.
	if got, body := confinedCall(t, app, http.MethodPost, "/v1/responses", childTok); got != 200 {
		t.Fatalf("live child token after parent revocation = %d %s, want 200", got, body)
	}

	// Child run finishes and revokes token B.
	iam.mu.Lock()
	iam.dead[childTok] = true
	iam.mu.Unlock()
	v.live.mu.Lock()
	for key, e := range v.live.seen {
		e.at = e.at.Add(-livenessTTL)
		v.live.seen[key] = e
	}
	v.live.mu.Unlock()

	if got, body := confinedCall(t, app, http.MethodPost, "/v1/responses", childTok); got != 401 || !strings.Contains(body, "this delegated token was revoked") {
		t.Fatalf("revoked child token = %d %s, want 401 revoked", got, body)
	}
}

// TestLivenessDoesNotNegativeCacheTransientInactive verifies that a transient
// inactive answer from IAM (e.g. read-after-write lag or DB blip) still fails
// closed on that request (401) without poisoning the 30s cache for retries,
// while a genuinely revoked token keeps returning 401 on every subsequent call.
func TestLivenessDoesNotNegativeCacheTransientInactive(t *testing.T) {
	app, _, _, k, _, iam := boundaryApp(t, modelAddrs)
	tok := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)))

	// Simulate transient false-negative at IAM (not yet visible / DB hiccup).
	iam.mu.Lock()
	iam.dead[tok] = true
	iam.mu.Unlock()
	if got, body := confinedCall(t, app, http.MethodPost, "/v1/responses", tok); got != 401 || !strings.Contains(body, "revoked") {
		t.Fatalf("first call during transient inactive = %d %s, want 401", got, body)
	}

	// IAM recovers immediately; the retry MUST succeed without waiting livenessTTL.
	iam.mu.Lock()
	delete(iam.dead, tok)
	iam.mu.Unlock()
	if got, body := confinedCall(t, app, http.MethodPost, "/v1/responses", tok); got != 200 {
		t.Fatalf("retry after transient inactive = %d %s, want 200 (negative cache poisoned retry)", got, body)
	}

	// A genuinely revoked token is still refused on every call.
	revoked := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(2*time.Hour)))
	iam.mu.Lock()
	iam.dead[revoked] = true
	iam.mu.Unlock()
	for i := range 2 {
		if got, body := confinedCall(t, app, http.MethodPost, "/v1/responses", revoked); got != 401 || !strings.Contains(body, "revoked") {
			t.Fatalf("revoked call %d = %d %s, want 401 revoked", i+1, got, body)
		}
	}
}

func TestDelegatedAutonomousScienceScopesAdmitResearchAndGPUFleetOnly(t *testing.T) {
	scienceAddrs := [][2]string{
		{http.MethodGet, "/v1/research/standard"},
		{http.MethodPost, "/v1/research/experiments"},
		{http.MethodGet, "/v1/research/runs"},
		{http.MethodPost, "/v1/research/hypotheses"},
		{http.MethodPost, "/v1/research/claims"},
		{http.MethodPost, "/v1/research/grants"},
		{http.MethodPost, "/v1/research/actions/:id/authorize"},
		{http.MethodPost, "/v1/workflow/namespaces/gpu-jobs/activities"},
		{http.MethodPost, "/v1/workflow/namespaces/gpu-jobs/activities/claim"},
		{http.MethodGet, "/v1/workflow/namespaces"},
		{http.MethodPost, "/v1/workflow/namespaces/default/workflows"},
		{http.MethodGet, "/v1/compute/fleet"},
		{http.MethodGet, "/v1/compute/fleet/workers"},
		{http.MethodPost, "/v1/compute/fleet/jobs"},
		{http.MethodGet, "/v1/compute/fleet/jobs/:id"},
		{http.MethodDelete, "/v1/compute/machines/:id"},
		{http.MethodPost, "/v1/compute/clusters"},
	}
	app, _, seen, k, _, _ := boundaryApp(t, append(append([][2]string{}, modelAddrs...), append(scienceAddrs, otherAddrs...)...))

	// Plain ai:inference stays strictly model-only.
	plain := k.delegated(t, delegatedFor("acme", "member", time.Now().Add(time.Hour)))
	for _, a := range [][2]string{
		{http.MethodGet, "/v1/research/standard"},
		{http.MethodPost, "/v1/workflow/namespaces/gpu-jobs/activities"},
		{http.MethodGet, "/v1/compute/fleet"},
	} {
		seen.reached = false
		if got := through(t, app, a[0], a[1], plain); got != 403 || seen.reached {
			t.Fatalf("plain ai:inference reached %s %s: status=%d reached=%v", a[0], a[1], got, seen.reached)
		}
	}

	// Delegated science + GPU scopes admit org-scoped research, gpu-jobs, and fleet endpoints.
	sciClaims := delegatedFor("acme", "member", time.Now().Add(time.Hour))
	sciClaims.Scope = "ai:inference research workflow:gpu"
	sciTok := k.delegated(t, sciClaims)

	for _, a := range [][2]string{
		{http.MethodPost, "/v1/decisions"},
		{http.MethodGet, "/v1/research/standard"},
		{http.MethodPost, "/v1/research/experiments"},
		{http.MethodGet, "/v1/research/runs"},
		{http.MethodPost, "/v1/research/hypotheses"},
		{http.MethodPost, "/v1/research/claims"},
		{http.MethodPost, "/v1/workflow/namespaces/gpu-jobs/activities"},
		{http.MethodPost, "/v1/workflow/namespaces/gpu-jobs/activities/claim"},
		{http.MethodGet, "/v1/compute/fleet"},
		{http.MethodGet, "/v1/compute/fleet/workers"},
		{http.MethodPost, "/v1/compute/fleet/jobs"},
		{http.MethodGet, "/v1/compute/fleet/jobs/job-1"},
	} {
		seen.reached = false
		if got := through(t, app, a[0], a[1], sciTok, func(r *http.Request) { r.Header.Set("X-Org-Id", "other") }); got != 200 || !seen.reached || seen.org != "acme" {
			t.Errorf("science token %s %s: status=%d reached=%v org=%q, want 200 in acme", a[0], a[1], got, seen.reached, seen.org)
		}
	}

	// Retrieval remains forbidden on science-scoped endpoints just as on model calls.
	for _, path := range []string{"/v1/decisions", "/v1/research/experiments", "/v1/workflow/namespaces/gpu-jobs/activities"} {
		seen.reached = false
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"retrieval":true}`))
		req.Header.Set("Authorization", "Bearer "+sciTok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 403 || seen.reached {
			t.Errorf("science token retrieval on %s = %d (reached %v), want 403", path, resp.StatusCode, seen.reached)
		}
	}

	// Visibility grants, human action authorization, non-gpu-jobs workflow namespaces,
	// machine/cluster mutations, KMS, IAM, billing, and MCP/call doors stay strictly 403.
	for _, a := range [][2]string{
		{http.MethodPost, "/v1/research/grants"},
		{http.MethodPost, "/v1/research/actions/act-1/authorize"},
		{http.MethodGet, "/v1/workflow/namespaces"},
		{http.MethodPost, "/v1/workflow/namespaces/default/workflows"},
		{http.MethodDelete, "/v1/compute/machines/m-1"},
		{http.MethodPost, "/v1/compute/clusters"},
		{http.MethodGet, "/v1/kms/secrets"},
		{http.MethodGet, "/v1/iam/users"},
		{http.MethodGet, "/v1/billing/balance"},
		{http.MethodPost, manifest.MCPPath},
		{http.MethodPost, zip.CallPath + "kms.secret.get"},
	} {
		seen.reached = false
		if got := through(t, app, a[0], a[1], sciTok); got != 403 || seen.reached {
			t.Errorf("science token must be refused at %s %s: status=%d reached=%v", a[0], a[1], got, seen.reached)
		}
	}
}
