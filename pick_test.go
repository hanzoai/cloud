package cloud

// Credential-split contract for AI inference: CHAT COMPLETIONS (deps.AI, a WRITE
// endpoint) must NEVER ride a read-only publishable (pk-) key — the gateway 403s a
// pk- key on any write endpoint ("Publishable keys can only access read-only
// endpoints … use a secret key (sk-)"), which was the intermittent bot-reply
// failure. EMBEDDINGS (deps.Embed, a READ-ONLY endpoint) DO ride the pk- key, which
// is its correct least-privilege credential. These tests pin both halves on the wire.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/clients"
	luxlog "github.com/luxfi/log"
)

func TestPublishableKey(t *testing.T) {
	cases := map[string]bool{
		"pk-lf-abc123": true,  // IAM read-only publishable key — the one the gateway 403s on writes
		"  pk-abc  ":   true,  // trimmed
		"sk-lf-abc123": false, // secret key — completions-capable
		"hk-abc":       false, // IAM key, completions-capable
		"fw_live_x":    false,
		"pk_analytics": false, // analytics ingest family (underscore) — NOT the IAM read-only member
		"":             false,
	}
	for k, want := range cases {
		if got := publishableKey(k); got != want {
			t.Errorf("publishableKey(%q) = %v, want %v", k, got, want)
		}
	}
}

// gatewayStub is a fake OpenAI-compatible gateway + IAM token endpoint. It records
// the Authorization header seen at /chat/completions and /embeddings so a test can
// assert exactly which credential each path presented.
type gatewayStub struct {
	srv       *httptest.Server
	minted    string // the M2M access token the token endpoint issues
	chatAuth  string // Authorization seen at /chat/completions ("" ⇒ never called)
	embedAuth string // Authorization seen at /embeddings      ("" ⇒ never called)
}

func newGatewayStub(t *testing.T) *gatewayStub {
	t.Helper()
	g := &gatewayStub{minted: "m2m-access-token-xyz"}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/iam/oauth/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"` + g.minted + `","token_type":"Bearer","expires_in":3600}`))
		case "/chat/completions":
			g.chatAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chatcmpl-x", "object": "chat.completion", "model": "deepseek-v4-flash",
				"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": "pong"}, "finish_reason": "stop"}},
			})
		case "/embeddings":
			g.embedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data":   []map[string]any{{"index": 0, "embedding": []float32{0.1, 0.2}}},
			})
		default:
			t.Errorf("unexpected gateway path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// prodLikeConfig mirrors the live Hanzo deployment: a read-only publishable (pk-)
// key in CLOUD_AI_API_KEY plus the binary's IAM M2M identity, gateway pointed at the
// stub. The IAM token URL is pinned to the stub so M2M mints deterministically.
func prodLikeConfig(t *testing.T, g *gatewayStub, pkKey string) *Config {
	t.Helper()
	t.Setenv("CLOUD_AI_IAM_TOKEN_URL", g.srv.URL+"/v1/iam/oauth/token")
	return &Config{
		AIBaseURL:          g.srv.URL,
		AIAPIKey:           pkKey,
		AIDefaultModel:     "deepseek-v4-flash",
		AIAuthClientID:     "hanzo-cloud",
		AIAuthClientSecret: "s3cr3t",
	}
}

// The completions path must present the M2M Bearer, never the pk- key, when the
// static key is a read-only publishable key — the exact live wiring.
func TestPickCompletions_RefusesPublishableKey_UsesM2M(t *testing.T) {
	g := newGatewayStub(t)
	cfg := prodLikeConfig(t, g, "pk-lf-readonly-embed")

	ai := pickCompletionsClient(cfg, luxlog.New("test"))
	if _, err := ai.ChatCompletion(context.Background(), &ChatRequest{Prompt: "ping"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if g.chatAuth == "Bearer pk-lf-readonly-embed" {
		t.Fatal("completions rode the READ-ONLY pk- key — the gateway would 403 this (the bug)")
	}
	if g.chatAuth != "Bearer "+g.minted {
		t.Errorf("completions Authorization = %q, want the M2M bearer %q", g.chatAuth, "Bearer "+g.minted)
	}
}

// A completions-capable secret key set as the static key IS honored for completions.
func TestPickCompletions_HonorsSecretKey(t *testing.T) {
	g := newGatewayStub(t)
	cfg := prodLikeConfig(t, g, "sk-lf-completions-capable")

	ai := pickCompletionsClient(cfg, luxlog.New("test"))
	if _, err := ai.ChatCompletion(context.Background(), &ChatRequest{Prompt: "ping"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if g.chatAuth != "Bearer sk-lf-completions-capable" {
		t.Errorf("completions Authorization = %q, want the static secret key", g.chatAuth)
	}
}

// With ONLY a pk- key and no M2M identity, completions must fail closed — the pk-
// key is refused, never sent to the write endpoint.
func TestPickCompletions_PublishableOnly_FailsClosed(t *testing.T) {
	g := newGatewayStub(t)
	cfg := &Config{AIBaseURL: g.srv.URL, AIAPIKey: "pk-lf-readonly-embed", AIDefaultModel: "deepseek-v4-flash"}

	ai := pickCompletionsClient(cfg, luxlog.New("test"))
	_, err := ai.ChatCompletion(context.Background(), &ChatRequest{Prompt: "ping"})
	if err == nil {
		t.Fatal("expected a fail-closed error when only a read-only pk- key is available")
	}
	if !clients.IsDisabled(err) {
		t.Errorf("expected the disabled fail-closed stub, got %v", err)
	}
	if g.chatAuth != "" {
		t.Errorf("the pk- key was sent to the completions write endpoint (auth=%q) — must never happen", g.chatAuth)
	}
}

// The embed path DOES ride the pk- key — read-only is exactly what a publishable key
// may do, and this path is deliberately left on its least-privilege credential.
func TestPickEmbed_UsesPublishableKey(t *testing.T) {
	g := newGatewayStub(t)
	cfg := prodLikeConfig(t, g, "pk-lf-readonly-embed")

	em := pickEmbedClient(cfg, luxlog.New("test"))
	if _, err := em.Embed(context.Background(), &EmbedRequest{Model: "zen-embedding", Inputs: []string{"hello"}}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if g.embedAuth != "Bearer pk-lf-readonly-embed" {
		t.Errorf("embeddings Authorization = %q, want the read-only pk- key", g.embedAuth)
	}
}

// With no static embed key, embeddings fall back to the completions (M2M) resolution
// — a deploy without a dedicated embed key still indexes (no regression).
func TestPickEmbed_FallsBackToM2M(t *testing.T) {
	g := newGatewayStub(t)
	cfg := prodLikeConfig(t, g, "") // no static key

	em := pickEmbedClient(cfg, luxlog.New("test"))
	if _, err := em.Embed(context.Background(), &EmbedRequest{Model: "zen-embedding", Inputs: []string{"hello"}}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if g.embedAuth != "Bearer "+g.minted {
		t.Errorf("embeddings Authorization = %q, want the M2M bearer fallback %q", g.embedAuth, "Bearer "+g.minted)
	}
}

// End-to-end wiring through BuildDeps: deps.AI (completions) and deps.Embed
// (embeddings) resolve to DISTINCT credentials under the live prod-like config.
func TestBuildDeps_SplitsCompletionsFromEmbedCredential(t *testing.T) {
	g := newGatewayStub(t)
	cfg := prodLikeConfig(t, g, "pk-lf-readonly-embed")
	cfg.Brand = "hanzo"
	cfg.Domain = "api.hanzo.ai"
	cfg.DataDir = t.TempDir()
	cfg.Enable = []string{"ai"}

	deps := BuildDeps(cfg)
	if deps.AI == nil || deps.Embed == nil {
		t.Fatal("BuildDeps must populate BOTH deps.AI and deps.Embed")
	}
	// Org left empty ⇒ the metering wrapper does not gate (system call), isolating
	// the credential assertion from billing.
	if _, err := deps.AI.ChatCompletion(context.Background(), &ChatRequest{Prompt: "ping"}); err != nil {
		t.Fatalf("deps.AI.ChatCompletion: %v", err)
	}
	if _, err := deps.Embed.Embed(context.Background(), &EmbedRequest{Model: "zen-embedding", Inputs: []string{"x"}}); err != nil {
		t.Fatalf("deps.Embed.Embed: %v", err)
	}
	if g.chatAuth != "Bearer "+g.minted {
		t.Errorf("deps.AI completions auth = %q, want M2M bearer", g.chatAuth)
	}
	if g.embedAuth != "Bearer pk-lf-readonly-embed" {
		t.Errorf("deps.Embed embeddings auth = %q, want the pk- key", g.embedAuth)
	}
}
