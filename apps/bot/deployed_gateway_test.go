package bot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/sdk"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The run plane against the gateway that is DEPLOYED, not the one on hanzoai/bot
// main. Production pins oci.hanzo.ai/hanzoai/bot:2026.7.23 (universe
// charts/app/values/hanzo/bot-gateway.yaml), built from tag v2026.7.23, which is
// not on main. Its run surface (src/gateway/bots-http.ts at that tag) differs
// from main's in the one place #780 turns on:
//
//   - The gate is authorizeHttpGatewayConnect in auth mode "iam" with clientId
//     "hanzo-bot". validateIamToken there is @hanzo/iam 0.13.6 validateToken,
//     which pins `audience: config.clientId` and answers "iam_audience_invalid"
//     for any other audience. The gateway retries only "iam_signature_invalid",
//     so a person's token minted for another client (the CLI's "hanzo-cli", the
//     console's) falls through to the shared-token compare and is refused:
//     sendUnauthorized, 401 {"error":{"message":"Unauthorized","type":"unauthorized"}}.
//     Main dropped that audience gate (eb5f4f7d5, "The audience is not a gate, as
//     in cloud"); the deployed tag predates it.
//   - Past the gate the org is X-Org-Id (getIamIdentity), not the token's.
//
// So a person signed in through the CLI asks cloud, cloud relays their token
// (cloud.CallerBearer), the runtime answers 401, and cloud used to answer 502 —
// which Cloudflare, in front of api.hanzo.ai, replaces with its own "Bad gateway,
// try again in a few minutes" page. Neither the reason nor cloud's request id
// reached the person. That is #780.

// gatewayClient is the IAM client the deployed gateway validates tokens for.
const gatewayClient = "hanzo-bot"

// tokenFor stands for a hanzo.id token a person signed in to org obtained through
// client, which IAM stamps as its `aud`. Not an API key, so cloud relays it.
func tokenFor(client, org string) string { return "eyJ." + client + "." + org }

type deployedGateway struct {
	mu   sync.Mutex
	runs map[string][]map[string]any // org -> rows
	auth []string                    // the Authorization of every call it was sent
}

func (g *deployedGateway) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	authz := r.Header.Get("Authorization")
	g.auth = append(g.auth, authz)
	if r.URL.Path != "/v1/bots" && !stopPath.MatchString(r.URL.Path) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not Found"))
		return
	}
	// The gate: an IAM token for this gateway's client, else the shared token.
	tok, _ := strings.CutPrefix(authz, "Bearer ")
	if !strings.HasPrefix(tok, "eyJ."+gatewayClient+".") && tok != serviceToken {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Unauthorized","type":"unauthorized"}}`))
		return
	}
	org := r.Header.Get("X-Org-Id")
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/v1/bots" {
		rows := g.runs[org]
		if rows == nil {
			rows = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"bots": rows})
		return
	}
	runID := stopPath.FindStringSubmatch(r.URL.Path)[1]
	for i, row := range g.runs[org] {
		if row["runId"] == runID {
			g.runs[org] = append(g.runs[org][:i], g.runs[org][i+1:]...)
			_ = json.NewEncoder(w).Encode(map[string]any{"runId": runID, "status": "stopped"})
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"no such bot for this org"}`))
}

func (g *deployedGateway) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.auth...)
}

func (g *deployedGateway) has(org, runID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, row := range g.runs[org] {
		if row["runId"] == runID {
			return true
		}
	}
	return false
}

func mountOverDeployed(t *testing.T) (*zip.App, *deployedGateway) {
	t.Helper()
	g := &deployedGateway{runs: map[string][]map[string]any{
		"acme": {{"runId": "run_acme", "task": "acme work", "surface": "desktop", "status": "running", "startedAt": "2026-11-16T22:00:00Z"}},
	}}
	srv := httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(srv.Close)
	t.Setenv("BOT_GATEWAY_URL", srv.URL)
	t.Setenv("BOT_GATEWAY_TOKEN", serviceToken)
	t.Setenv(gatewayURLEnv, "https://bot.example.test")
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	compose(app)
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("bot.Use: %v", err)
	}
	return app, g
}

type problem struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func readProblem(t *testing.T, body []byte) problem {
	t.Helper()
	var p problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, body)
	}
	return p
}

// The deployed gateway lists for a token minted for its own client, so the double
// is the gate and not a gateway that refuses everyone.
func TestTheDeployedGatewayListsForItsOwnClient(t *testing.T) {
	app, _ := mountOverDeployed(t)

	code, body := ask(t, app, http.MethodGet, "/v1/bot/runs", "acme", tokenFor(gatewayClient, "acme"))
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	if ids := listRunIDs(t, body); len(ids) != 1 || ids[0] != "run_acme" {
		t.Fatalf("acme must see its own run, got %v (%s)", ids, body)
	}
}

// #780: `hanzo bot runs list` signs in through hanzo-cli. The deployed gateway
// refuses that token, and cloud must say so in an answer Cloudflare passes on:
// a 503 with a code, never a 502, which reaches the person as Cloudflare's page.
func TestARefusedSignInListsAsAReasonNotABadGateway(t *testing.T) {
	app, g := mountOverDeployed(t)
	cli := tokenFor("hanzo-cli", "acme")

	code, body := ask(t, app, http.MethodGet, "/v1/bot/runs", "acme", cli)
	if code == http.StatusBadGateway || code == http.StatusGatewayTimeout {
		t.Fatalf("a refused sign-in answered %d, which Cloudflare replaces with its own page (%s)", code, body)
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("list want 503, got %d (%s)", code, body)
	}
	p := readProblem(t, body)
	if p.Code != codeRuntimeRefused {
		t.Fatalf("list want code %q, got %q (%s)", codeRuntimeRefused, p.Code, body)
	}
	if !strings.Contains(p.Detail, "sign-in") {
		t.Fatalf("the detail must name the refused sign-in, got %q", p.Detail)
	}
	if strings.Contains(string(body), "unauthorized") {
		t.Fatalf("the runtime's own refusal body must not be echoed to the caller: %s", body)
	}
	// Asked once, as the person: no second ask with the shared token, which would
	// answer for whatever org X-Org-Id names on this gateway and for none on main.
	if auth := g.seen(); len(auth) != 1 || auth[0] != "Bearer "+cli {
		t.Fatalf("the runtime must be asked once with the person's own token, saw %v", auth)
	}
}

// A stop the runtime refused stopped nothing, and the answer says that too.
func TestARefusedSignInStopsNothingAndSaysSo(t *testing.T) {
	app, g := mountOverDeployed(t)
	cli := tokenFor("hanzo-cli", "acme")

	code, body := ask(t, app, http.MethodPost, "/v1/bot/runs/run_acme/stop", "acme", cli)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("stop want 503, got %d (%s)", code, body)
	}
	p := readProblem(t, body)
	if p.Code != codeRuntimeRefused || !strings.Contains(p.Detail, "NOT stopped") {
		t.Fatalf("stop want code %q saying the run was NOT stopped, got %+v", codeRuntimeRefused, p)
	}
	if !g.has("acme", "run_acme") {
		t.Fatal("a refused stop must leave the run where it was")
	}
	if auth := g.seen(); len(auth) != 1 || auth[0] != "Bearer "+cli {
		t.Fatalf("the runtime must be asked once with the person's own token, saw %v", auth)
	}
}
