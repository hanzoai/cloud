package ai

// A delegated token through the REAL model API, end to end: cloud's identity
// boundary in front, hanzoai/ai's own handlers behind it, a scripted upstream at
// the far end. The token is what IAM's delegation grant mints for a coding run —
// the person, in the org the run bills, scope ai:inference, signed by the
// delegation key the public JWKS never carries (iam LLM.md "Delegation").
//
// ai verifies a bearer itself, against the public JWKS. Before the boundary's
// verdict was handed to it (confine, object.SetConfinedVerifier) every one of
// these calls came back 401 from ai — after the boundary had admitted it — so
// every coding run's model call failed. The test states both halves.
//
// Then who pays, on the platform's own key: the balance gate on, as production
// runs it, the host's wallet read, plan policy and debit installed as Use installs
// them, and a real ledger behind them. A funded person's call is served and lands
// on the wallet their org bills; an unfunded one is not served on the platform's
// money.

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
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	aimod "github.com/hanzoai/ai"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/authz"
	"github.com/hanzoai/sqlite"
	"github.com/zap-proto/zip"
)

const delegatedIssuer = "https://test.iam"

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func jwkSet(kid string, k *rsa.PublicKey) map[string]any {
	return map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
	}}}
}

// upstream is the vendor the model route names: it answers a chat completion,
// as a stream when asked for one, and counts what it was asked. Given a catalog,
// it answers a GET with it, as the zen service does.
type upstream struct {
	mu      sync.Mutex
	asked   int
	catalog string
}

func (u *upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.asked
}

func (u *upstream) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && u.catalog != "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(u.catalog))
			return
		}
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.asked++
		u.mu.Unlock()
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"id":"up-1","object":"chat.completion.chunk","created":1,"model":"e2e-up","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`+"\n\n")
			fmt.Fprint(w, `data: {"id":"up-1","object":"chat.completion.chunk","created":1,"model":"e2e-up","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"up-1","object":"chat.completion","created":1,"model":"e2e-up",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestADelegatedTokenIsServedByTheRealModelAPI(t *testing.T) {
	public, delegation := rsaKey(t), rsaKey(t)
	aiobject.ResetAuthReady()
	t.Cleanup(aiobject.ResetAuthReady)
	t.Cleanup(func() { aiobject.SetConfinedVerifier(nil) })

	// The public JWKS: what ai and the boundary verify an ordinary token against.
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwkSet("test-key", &public.PublicKey))
	}))
	t.Cleanup(jwks.Close)
	der, err := x509.MarshalPKIXPublicKey(&public.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	// IAM as this process reads it: ai's application and its cert, the public JWKS,
	// and the two addresses the boundary reads for a delegated token — the
	// delegation key, to this deployment's credential only, and whether the grant
	// stands.
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/iam/applications/admin/hanzo-cloud":
			_, _ = w.Write([]byte(`{"owner":"admin","name":"hanzo-cloud","cert":"test-key"}`))
		case "/v1/iam/certs/admin/test-key":
			_ = json.NewEncoder(w).Encode(map[string]string{"owner": "admin", "name": "test-key", "certificate": certPEM})
		case "/v1/iam/.well-known/jwks":
			_ = json.NewEncoder(w).Encode(jwkSet("test-key", &public.PublicKey))
		case "/v1/iam/oauth/delegation/keys":
			if id, secret, ok := r.BasicAuth(); !ok || id != "hanzo-cloud" || secret != "cloud-secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(jwkSet("cert-delegation", &delegation.PublicKey))
		case "/v1/iam/oauth/introspect":
			_, _ = w.Write([]byte(`{"active":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(iam.Close)
	t.Setenv("IAM_URL", iam.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "cloud-secret")
	t.Setenv("IAM_APP_NAME", "hanzo-cloud")
	t.Setenv("IAM_ORG", "hanzo")
	t.Setenv("JWT_ISSUER", delegatedIssuer)
	// The platform's paid lane, as zen's catalog states it: open (ai controllers/lane.go).
	// Closed is a refusal about spending, not about who is calling, and would hide the
	// answer this test is after. The zen service also answers the free model.
	zen := &upstream{catalog: `{"object":"list","data":[{"id":"zen-e2e"}],"paid":true}`}
	t.Setenv("ZEN_URL", zen.serve(t))
	// The balance gate, on as production runs it (universe values/hanzo/cloud.yaml
	// names commerceEndpoint). The balance it weighs is the host's reader, never this
	// address.
	t.Setenv("commerceEndpoint", "http://127.0.0.1:1")
	t.Setenv("driverName", "sqlite")
	// With the store's pragmas: a bare path has no busy timeout, so under load a
	// usage write and a provider read collide, and a provider lookup that answers
	// SQLITE_BUSY sends the org's own key to the balance gate instead.
	t.Setenv("dataSourceName", sqlite.PragmaDSN(filepath.Join(t.TempDir(), "ai.db"), sqlite.DefaultPragmas))

	// The real ai app: its router, filters and handlers, bootstrapped on a store.
	sub, err := aimod.App(nil)
	if err != nil {
		t.Fatalf("ai: %v", err)
	}
	var used []aiobject.UsageEvent
	var usedMu sync.Mutex
	usage := func() []aiobject.UsageEvent {
		usedMu.Lock()
		defer usedMu.Unlock()
		return append([]aiobject.UsageEvent(nil), used...)
	}
	prev := aiobject.UsageRecorder()
	t.Cleanup(func() { aiobject.SetUsageRecorder(prev) })
	settle := func(next aiobject.UsageRecorderFunc) {
		aiobject.SetUsageRecorder(func(ctx context.Context, e aiobject.UsageEvent) error {
			usedMu.Lock()
			used = append(used, e)
			usedMu.Unlock()
			if next == nil {
				return nil
			}
			return next(ctx, e)
		})
	}
	settle(nil)

	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	cloud.Identify(app, &cloud.Config{IAMIssuer: delegatedIssuer, Discovery: oidctest.Discovery(delegatedIssuer), DiscoveryClient: oidctest.Client(delegatedIssuer, jwks.URL)})
	app.Use(sub)

	// mint is what IAM's delegation grant answers for a run: the person, in the org
	// the run bills, for model calls, acted for by hanzo-cloud.
	mint := func(org, name string) string {
		claims := authz.Claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer: delegatedIssuer, Subject: "uuid-" + name, Audience: jwt.ClaimStrings{"https://" + cloud.Domain()},
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)), IssuedAt: jwt.NewNumericDate(time.Now()),
			},
			Scope: "ai:inference", Owner: org, Organization: org, Name: name, PreferredUsername: name,
			TokenType: "access-token", Orgs: []authz.Membership{{Org: org, Role: authz.Member}},
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, struct {
			authz.Claims
			Act map[string]string `json:"act"`
		}{claims, map[string]string{"sub": "admin/hanzo-cloud", "owner": "admin", "name": "hanzo-cloud"}})
		tok.Header["kid"] = "cert-delegation"
		s, err := tok.SignedString(delegation)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	call := func(tok, path, body string) (int, http.Header, string) {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req, zip.TestConfig{Timeout: time.Minute})
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(b)
	}

	t.Run("on the org's own key", func(t *testing.T) {
		vendor := &upstream{}
		// The org's own key (BYOK): a run's model call is served on it with no paid
		// lane to seat, so what is under test is who the call is for, not the
		// platform's spending switch.
		if _, err := aiobject.AddProvider(&aiobject.Provider{
			Owner: "acme", Name: "e2e-vendor", Category: "Model", Type: "OpenAI",
			ProviderUrl: vendor.serve(t), ClientSecret: "k", State: "Active",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := aiobject.AddModelRoute(&aiobject.ModelRoute{
			Owner: "built-in", ModelName: "e2e-model", Enabled: true,
			Provider: "e2e-vendor", Upstream: "e2e-up", Priced: true,
		}); err != nil {
			t.Fatal(err)
		}
		delegated := mint("acme", "alice")
		calls := []struct{ path, body string }{
			{"/v1/chat/completions", `{"model":"e2e-model","messages":[{"role":"user","content":"hi"}]}`},
			{"/v1/responses", `{"model":"e2e-model","input":"hi"}`},
			{"/v1/messages", `{"model":"e2e-model","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
		}

		// BEFORE: ai holding no verifier for a delegated token refuses every model
		// call the boundary admitted — what every coding run met.
		aiobject.SetConfinedVerifier(nil)
		for _, c := range calls {
			if status, _, body := call(delegated, c.path, c.body); status != http.StatusUnauthorized {
				t.Errorf("without the boundary's verdict, POST %s = %d %s; want ai's 401", c.path, status, body)
			}
		}
		if n := vendor.count(); n != 0 {
			t.Fatalf("a refused call reached the vendor %d times", n)
		}

		// AFTER: the wiring Use installs.
		confine()
		for _, c := range calls {
			if status, _, body := call(delegated, c.path, c.body); status != http.StatusOK {
				t.Errorf("POST %s = %d %s; want the person's model call served", c.path, status, body)
			}
		}
		if n := vendor.count(); n != len(calls) {
			t.Fatalf("the vendor was asked %d times for %d calls", n, len(calls))
		}
		got := usage()
		if len(got) == 0 {
			t.Fatal("no usage was recorded for the served calls")
		}
		for _, e := range got {
			if e.Namespace != "acme" || e.Actor != "acme/alice" {
				t.Errorf("usage = namespace %q actor %q, want the run's org and its person", e.Namespace, e.Actor)
			}
		}
	})

	t.Run("on the platform's key", func(t *testing.T) {
		confine()
		ctx := context.Background()
		// The ledger, the meter that debits it and the plan policy that weighs it,
		// installed as Use installs them: the wallet read (balance), the policy
		// (limitAsk) and the debit (record → debit) are the host's own.
		fin := finance.New(finance.Local(t.TempDir(), testMaster))
		prevFin := finance.Current()
		finance.Publish(fin)
		t.Cleanup(func() { finance.Publish(prevFin); _ = fin.Close() })
		var funder finance.Funder = fin
		gauge, err := meter.New(meter.Config{BaseURL: "http://127.0.0.1:1"})
		if err != nil {
			t.Fatal(err)
		}
		lim, err := limits.Open(t.TempDir(),
			func(context.Context, string, string) (string, string, error) { return "", "", nil },
			func(context.Context, string, string) ([]client.Subscription, error) { return nil, nil },
			nil,
			func(ctx context.Context, subject, org string) (int64, int64, error) {
				f, err := funder.Funds(ctx, org, subject, false)
				if err != nil {
					return 0, 0, err
				}
				return limits.NanosOfAtto(f.Own().Atto()), limits.NanosOfAtto(f.Credit.Atto()), nil
			}, nil)
		if err != nil {
			t.Fatal(err)
		}
		prevBalance, prevLimits := aiobject.BalanceReader(), aiobject.Limits()
		t.Cleanup(func() { aiobject.SetBalanceReader(prevBalance); aiobject.SetLimits(prevLimits); settle(nil) })
		aiobject.SetBalanceReader(balance)
		aiobject.SetLimits(limitAsk(lim))
		settle(record(lim, debit(gauge)))

		// The platform's own key: a row the admin org holds, which no member's org
		// owns, so every call on it is the platform's money unless a wallet pays.
		vendor := &upstream{}
		if _, err := aiobject.AddProvider(&aiobject.Provider{
			Owner: "admin", Name: "e2e-platform", Category: "Model", Type: "OpenAI",
			ProviderUrl: vendor.serve(t), ClientSecret: "k", State: "Active",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := aiobject.AddModelRoute(&aiobject.ModelRoute{
			Owner: "built-in", ModelName: "e2e-paid", Enabled: true,
			Provider: "e2e-platform", Upstream: "e2e-up", Priced: true, InputPrice: 10, OutputPrice: 10,
		}); err != nil {
			t.Fatal(err)
		}

		// The two ways a person's org bills (account.Payer): a company org pools every
		// member into its one wallet; the shared signup org keeps a wallet per person.
		// Each is funded with cash a payment brought in. carol's org has no money at all.
		for _, d := range []money.DepositInput{
			{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Cash: true, Ref: "pay-acme"},
			{Org: "hanzo", Subject: "hanzo/bob", Amount: money.FromCents(500), Cash: true, Ref: "pay-bob"},
		} {
			if _, err := fin.Deposit(ctx, d); err != nil {
				t.Fatalf("deposit %s: %v", d.Subject, err)
			}
		}
		wallet := func(org, subject string) money.Amount {
			f, err := funder.Funds(ctx, org, subject, false)
			if err != nil {
				t.Fatalf("funds %s/%s: %v", org, subject, err)
			}
			return f.Total()
		}
		wallets := [][2]string{{"acme", "acme"}, {"acme", "acme/alice"}, {"hanzo", "hanzo"}, {"hanzo", "hanzo/bob"},
			{"bare", "bare"}, {"admin", "admin"}, {"admin", "admin/hanzo-cloud"}}
		snapshot := func() map[[2]string]money.Amount {
			out := map[[2]string]money.Amount{}
			for _, w := range wallets {
				out[w] = wallet(w[0], w[1])
			}
			return out
		}
		const chat = `{"model":"e2e-paid","messages":[{"role":"user","content":"hi"}]}`

		for _, p := range []struct {
			org, name string
			pays      [2]string
		}{
			{"acme", "alice", [2]string{"acme", "acme"}},
			{"hanzo", "bob", [2]string{"hanzo", "hanzo/bob"}},
		} {
			before, recorded, asked := snapshot(), len(usage()), vendor.count()
			status, _, body := call(mint(p.org, p.name), "/v1/chat/completions", chat)
			if status != http.StatusOK {
				t.Fatalf("%s/%s, funded: %d %s; want served", p.org, p.name, status, body)
			}
			if vendor.count() != asked+1 {
				t.Fatalf("%s/%s: the platform's vendor was asked %d times, want once", p.org, p.name, vendor.count()-asked)
			}
			// The debit is detached from the answer; it has landed when the wallet moved
			// by what the call recorded.
			var charged money.Amount
			deadline := time.Now().Add(10 * time.Second)
			for {
				charged = money.Zero()
				for _, e := range usage()[recorded:] {
					amt, err := money.ParseUSD(e.USD)
					if err != nil {
						t.Fatalf("usage amount %q: %v", e.USD, err)
					}
					charged = charged.Add(amt)
					if e.Namespace != p.pays[0] || e.Subject != p.pays[1] {
						t.Errorf("%s/%s: usage billed %s/%s, want %s/%s", p.org, p.name, e.Namespace, e.Subject, p.pays[0], p.pays[1])
					}
				}
				if !charged.IsZero() && before[p.pays].Sub(wallet(p.pays[0], p.pays[1])).Cmp(charged) == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s/%s: the wallet %v moved %s, want the %s the call recorded",
						p.org, p.name, p.pays, before[p.pays].Sub(wallet(p.pays[0], p.pays[1])), charged)
				}
				time.Sleep(5 * time.Millisecond)
			}
			for w, was := range before {
				if w != p.pays && wallet(w[0], w[1]).Cmp(was) != 0 {
					t.Errorf("%s/%s: wallet %v moved from %s to %s; only %v pays", p.org, p.name, w, was, wallet(w[0], w[1]), p.pays)
				}
			}
		}

		// No money: 402, saying what pays for the model and where to add it, which is
		// the line a run's harness prints when its turn fails. The platform's key is
		// not spent on carol's behalf, the deployment that acts for her pays nothing in
		// her place, and no free model answers in place of the one her run named — a
		// delegated token is a program's, and names no app (ai controllers.Apps).
		before, recorded, asked, free := snapshot(), len(usage()), vendor.count(), zen.count()
		status, header, body := call(mint("bare", "carol"), "/v1/chat/completions", chat)
		var refusal struct {
			Error struct{ Message, Code, Type string } `json:"error"`
		}
		_ = json.Unmarshal([]byte(body), &refusal)
		if status != http.StatusPaymentRequired || refusal.Error.Code != "insufficient_balance" ||
			!strings.Contains(strings.ToLower(refusal.Error.Message), "add funds") || !strings.Contains(refusal.Error.Message, "/pay") {
			t.Fatalf("unfunded: %d %s; want 402 insufficient_balance naming where to add funds", status, body)
		}
		if f := header.Get("X-Hanzo-Fallback"); f != "" || zen.count() != free {
			t.Errorf("unfunded: answered by %q in limited mode; a delegated token gets its model or the refusal", f)
		}
		if vendor.count() != asked {
			t.Errorf("an unfunded call reached the platform's vendor")
		}
		if n := len(usage()) - recorded; n != 0 {
			t.Errorf("an unfunded call recorded %d usage events", n)
		}
		time.Sleep(50 * time.Millisecond)
		for w, was := range before {
			if wallet(w[0], w[1]).Cmp(was) != 0 {
				t.Errorf("unfunded: wallet %v moved from %s to %s", w, was, wallet(w[0], w[1]))
			}
		}
	})
}
