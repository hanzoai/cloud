package host

// Credential contract for AI inference. CHAT COMPLETIONS (deps.AI, a WRITE endpoint)
// must NEVER ride a read-only publishable (pk-) key — the gateway 403s a pk- key on
// any write endpoint ("Publishable keys can only access read-only endpoints … use a
// secret key (sk-)"), which was the intermittent bot-reply failure. Completions,
// embeddings and reranks all take the deployment's own identity wherever it has one
// and cross the plane for the org they are for; a static key serves only where there
// is no identity. These tests pin it on the wire.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"

	openai "github.com/hanzoai/go-openai"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/kit"
)

func TestPublishableKey(t *testing.T) {
	cases := map[string]bool{
		"pk-lf-abc123": true,  // IAM read-only publishable key — the one the gateway 403s on writes
		"  pk-abc  ":   true,  // trimmed
		"sk-lf-abc123": false, // secret key — completions-capable
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
func prodLikeConfig(t *testing.T, g *gatewayStub, pkKey string) *cloud.Config {
	t.Helper()
	t.Setenv("CLOUD_AI_IAM_TOKEN_URL", g.srv.URL+"/v1/iam/oauth/token")
	return &cloud.Config{
		AIBaseURL:          g.srv.URL,
		AIAPIKey:           pkKey,
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
	if _, err := ai.ChatCompletion(context.Background(), &infer.ChatRequest{Prompt: "ping"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if g.chatAuth == "Bearer pk-lf-readonly-embed" {
		t.Fatal("completions rode the READ-ONLY pk- key — the gateway would 403 this (the bug)")
	}
	if g.chatAuth != "Bearer "+g.minted {
		t.Errorf("completions Authorization = %q, want the M2M bearer %q", g.chatAuth, "Bearer "+g.minted)
	}
}

// A secret key beside the deployment's own identity is the EMBED credential, and
// completions take the identity — which is what crosses the plane carrying the org a
// completion is for. The live deployment is exactly this shape: an sk- embed key and
// IAM_CLIENT_ID together, and the key used to buy every org's completions as its own
// service account.
func TestPickCompletions_IdentityBuysBesideASecretKey(t *testing.T) {
	g := newGatewayStub(t)
	cfg := prodLikeConfig(t, g, "sk-lf-embed-service-account")

	ai := pickCompletionsClient(cfg, luxlog.New("test"))
	if _, err := ai.ChatCompletion(context.Background(), &infer.ChatRequest{Prompt: "ping"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if g.chatAuth != "Bearer "+g.minted {
		t.Errorf("completions Authorization = %q, want the M2M bearer %q", g.chatAuth, "Bearer "+g.minted)
	}
	if em := pickEmbedClient(cfg, luxlog.New("test")); em != nil {
		if _, err := em.Embed(context.Background(), &infer.EmbedRequest{Model: "zen-embedding", Inputs: []string{"x"}}); err != nil {
			t.Fatalf("Embed: %v", err)
		}
	}
	if g.embedAuth != "Bearer "+g.minted {
		t.Errorf("embeddings Authorization = %q, want the M2M bearer %q", g.embedAuth, "Bearer "+g.minted)
	}
}

// With no IAM identity, a completions-capable secret key is the one credential there
// is, and it buys completions.
func TestPickCompletions_SecretKeyWithoutIdentity(t *testing.T) {
	g := newGatewayStub(t)
	ai := pickCompletionsClient(&cloud.Config{AIBaseURL: g.srv.URL, AIAPIKey: "sk-operator"}, luxlog.New("test"))
	if _, err := ai.ChatCompletion(context.Background(), &infer.ChatRequest{Prompt: "ping"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if g.chatAuth != "Bearer sk-operator" {
		t.Errorf("completions Authorization = %q, want the operator's key", g.chatAuth)
	}
}

// With ONLY a pk- key and no M2M identity, completions must fail closed — the pk-
// key is refused, never sent to the write endpoint.
func TestPickCompletions_PublishableOnly_FailsClosed(t *testing.T) {
	g := newGatewayStub(t)
	cfg := &cloud.Config{AIBaseURL: g.srv.URL, AIAPIKey: "pk-lf-readonly-embed"}

	ai := pickCompletionsClient(cfg, luxlog.New("test"))
	_, err := ai.ChatCompletion(context.Background(), &infer.ChatRequest{Prompt: "ping"})
	if err == nil {
		t.Fatal("expected a fail-closed error when only a read-only pk- key is available")
	}
	if !errors.Is(err, infer.ErrDisabled) {
		t.Errorf("expected the disabled fail-closed stub, got %v", err)
	}
	if g.chatAuth != "" {
		t.Errorf("the pk- key was sent to the completions write endpoint (auth=%q) — must never happen", g.chatAuth)
	}
}

// A placeholder key (e.g. from an unseeded secret) must never be treated as a
// static secret key and sent to the write endpoint, and neither may the old agent
// key: CLOUD_AGENT_KEY belonged to an IAM user, and a deleted user left every
// completion arriving as a key IAM refused. Both fall through to M2M.
func TestPickCompletions_PlaceholderFallsBackToM2MNotTheAgentKey(t *testing.T) {
	g := newGatewayStub(t)
	t.Setenv("CLOUD_AGENT_KEY", "sk-agent-service-key")
	cfg := prodLikeConfig(t, g, "placeholder-c962ae3797733288")

	ai := pickCompletionsClient(cfg, luxlog.New("test"))
	if _, err := ai.ChatCompletion(context.Background(), &infer.ChatRequest{Prompt: "ping"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if g.chatAuth != "Bearer "+g.minted {
		t.Errorf("completions Authorization = %q, want the M2M bearer %q", g.chatAuth, "Bearer "+g.minted)
	}
}

// Embeddings take the deployment's identity before the embed key, as completions
// do: the identity is what crosses the plane carrying the org a passage belongs to,
// and the key reached the gateway as its own service account for every org's files.
func TestPickEmbed_IdentityBeforeAKey(t *testing.T) {
	g := newGatewayStub(t)
	cfg := prodLikeConfig(t, g, "pk-lf-readonly-embed")

	em := pickEmbedClient(cfg, luxlog.New("test"))
	if _, err := em.Embed(context.Background(), &infer.EmbedRequest{Model: "zen-embedding", Inputs: []string{"hello"}}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if g.embedAuth != "Bearer "+g.minted {
		t.Errorf("embeddings Authorization = %q, want the M2M bearer %q", g.embedAuth, "Bearer "+g.minted)
	}
}

// With no IAM identity, the embed key is the one credential there is, and a read-only
// publishable key may serve: embeddings are a read.
func TestPickEmbed_AKeyWithoutIdentity(t *testing.T) {
	g := newGatewayStub(t)
	em := pickEmbedClient(&cloud.Config{AIBaseURL: g.srv.URL, AIAPIKey: "pk-lf-readonly-embed"}, luxlog.New("test"))
	if _, err := em.Embed(context.Background(), &infer.EmbedRequest{Model: "zen-embedding", Inputs: []string{"hello"}}); err != nil {
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
	if _, err := em.Embed(context.Background(), &infer.EmbedRequest{Model: "zen-embedding", Inputs: []string{"hello"}}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if g.embedAuth != "Bearer "+g.minted {
		t.Errorf("embeddings Authorization = %q, want the M2M bearer fallback %q", g.embedAuth, "Bearer "+g.minted)
	}
}

// End-to-end wiring through BuildDeps: under the live prod-like config deps.AI
// (completions) and deps.Embed (embeddings) both take the identity, and a fleet with
// no ai sends them to the gateway carrying it.
func TestBuildDeps_CompletionsAndEmbeddingsTakeTheIdentity(t *testing.T) {
	g := newGatewayStub(t)
	servePlaneAI(t, nil) // no ai in this fleet: a completion falls back to the gateway
	cfg := prodLikeConfig(t, g, "pk-lf-readonly-embed")
	cfg.Brand = "hanzo"
	cfg.Domain = "api.hanzo.ai"
	cfg.DataDir = t.TempDir()
	cfg.Enable = []string{"ai"}

	deps := buildDeps(cfg)
	if deps.AI == nil || deps.Embed == nil {
		t.Fatal("BuildDeps must populate BOTH deps.AI and deps.Embed")
	}
	// Org left empty ⇒ the metering wrapper does not gate (system call), isolating
	// the credential assertion from billing.
	if _, err := deps.AI.ChatCompletion(context.Background(), &infer.ChatRequest{Prompt: "ping"}); err != nil {
		t.Fatalf("deps.AI.ChatCompletion: %v", err)
	}
	if _, err := deps.Embed.Embed(context.Background(), &infer.EmbedRequest{Model: "zen-embedding", Inputs: []string{"x"}}); err != nil {
		t.Fatalf("deps.Embed.Embed: %v", err)
	}
	if g.chatAuth != "Bearer "+g.minted {
		t.Errorf("deps.AI completions auth = %q, want M2M bearer", g.chatAuth)
	}
	if g.embedAuth != "Bearer "+g.minted {
		t.Errorf("deps.Embed embeddings auth = %q, want M2M bearer", g.embedAuth)
	}
}

// servePlaneAI serves the completion, embeddings and rerank ops as app "ai" on this
// process's plane, with answer standing in for the model API, or points the plane
// at an empty run dir when answer is nil — a fleet that runs no ai.
func servePlaneAI(t *testing.T, answer func(ctx context.Context, in *client.ChatIn) (*client.Chat, error)) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", kit.Sockets(t))
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	if answer == nil {
		return
	}
	cloud.Plane().Post("/ai/chat", answer, zip.WithOperationID(client.AIChat))
	cloud.Plane().Post("/ai/embed", answer, zip.WithOperationID(client.AIEmbed))
	cloud.Plane().Post("/ai/rerank", answer, zip.WithOperationID(client.AIRerank))
	stop, err := cloud.ServePlane("ai", luxlog.NewNoOpLogger())
	if err != nil {
		t.Fatalf("serve plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
}

// A COMPLETION CROSSES THE PLANE CARRYING THE PERSON IT IS FOR, and carries no key.
//
// A Slack turn runs on a detached context: no request, so nothing to forward. The
// person is on the request value (Actor, "<org>/<sub>"), and the plane states it
// as the caller, which is what reaches the model API as X-User-Id. Through the
// router the identity boundary deleted it and the usage row named nobody.
func TestCompletionsCrossThePlaneCarryingTheCaller(t *testing.T) {
	g := newGatewayStub(t)
	type caller struct{ org, user, name string }
	seen := make(chan caller, 1)
	servePlaneAI(t, func(ctx context.Context, in *client.ChatIn) (*client.Chat, error) {
		seen <- caller{cloud.Who(ctx).Org, cloud.Who(ctx).User, cloud.Who(ctx).Name}
		return &client.Chat{Status: http.StatusOK, Body: []byte(`{"id":"c1","object":"chat.completion","model":"m",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)}, nil
	})

	ai := overPlane(pickCompletionsClient(prodLikeConfig(t, g, "pk-lf-readonly-embed"), luxlog.New("test")))
	resp, err := ai.ChatCompletion(cloud.For(context.Background(), "acme"),
		&infer.ChatRequest{Model: "m", Prompt: "ping", Org: "acme", Actor: "acme/u-7"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Content != "pong" {
		t.Errorf("answer = %q, want the model API's own", resp.Content)
	}
	if got := <-seen; got != (caller{"acme", "u-7", ""}) {
		t.Errorf("ai saw caller %+v, want acme/u-7", got)
	}
	if g.chatAuth != "" {
		t.Errorf("the completion reached the gateway carrying %q; it crosses the plane", g.chatAuth)
	}

	// An actor in another org is not this caller's to name.
	if _, err := ai.ChatCompletion(context.Background(),
		&infer.ChatRequest{Model: "m", Prompt: "ping", Org: "acme", Actor: "globex/u-9"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got := <-seen; got != (caller{"acme", "", ""}) {
		t.Errorf("ai saw caller %+v, want acme and nobody", got)
	}

	// A name crosses with its own user and never with the actor's.
	bob := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-1", Name: "bob"})
	for actor, want := range map[string]caller{
		"acme/u-1": {"acme", "u-1", "bob"},
		"acme/u-7": {"acme", "u-7", ""},
	} {
		if _, err := ai.ChatCompletion(bob, &infer.ChatRequest{Model: "m", Prompt: "ping", Org: "acme", Actor: actor}); err != nil {
			t.Fatalf("ChatCompletion: %v", err)
		}
		if got := <-seen; got != want {
			t.Errorf("actor %s: ai saw caller %+v, want %+v", actor, got, want)
		}
	}
}

// The project a completion is billed to crosses with its org, so the model API and
// zen can name it; a caller's own project never crosses into another org's call.
func TestACompletionCrossesWithItsProject(t *testing.T) {
	g := newGatewayStub(t)
	seen := make(chan zip.Caller, 1)
	servePlaneAI(t, func(ctx context.Context, in *client.ChatIn) (*client.Chat, error) {
		seen <- cloud.Who(ctx)
		return &client.Chat{Status: http.StatusOK, Body: []byte(`{"id":"c1","object":"chat.completion","model":"m",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)}, nil
	})
	ai := overPlane(pickCompletionsClient(prodLikeConfig(t, g, "sk-embed"), luxlog.New("test")))
	crossed := func() zip.Caller {
		t.Helper()
		select {
		case who := <-seen:
			return who
		case <-time.After(5 * time.Second):
			t.Fatalf("the completion never crossed the plane (gateway saw %q)", g.chatAuth)
			return zip.Caller{}
		}
	}

	if _, err := ai.ChatCompletion(context.Background(), &infer.ChatRequest{Model: "m", Prompt: "ping", Org: "acme", Project: "p1"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got := crossed(); got.Org != "acme" || got.Project != "p1" {
		t.Errorf("ai saw org %q project %q, want acme / p1", got.Org, got.Project)
	}
	globex := zip.WithCaller(context.Background(), zip.Caller{Org: "globex", Project: "secret"})
	if _, err := ai.ChatCompletion(globex, &infer.ChatRequest{Model: "m", Prompt: "ping", Org: "acme"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got := crossed(); got.Org != "acme" || got.Project != "" {
		t.Errorf("ai saw org %q project %q, want acme and no project", got.Org, got.Project)
	}
	if g.chatAuth != "" {
		t.Errorf("the completion reached the gateway carrying %q; it crosses the plane", g.chatAuth)
	}
}

// A STATIC KEY BUYS ITS OWN COMPLETIONS. The operator named the credential, so the
// key's owner is whose ledger and providers pay. Over the plane ai would buy the
// completion as the deployment instead, and a deployment with no IAM identity
// could not buy it at all.
func TestAStaticKeyBuysItsOwnCompletions(t *testing.T) {
	g := newGatewayStub(t)
	servePlaneAI(t, func(context.Context, *client.ChatIn) (*client.Chat, error) {
		return nil, zip.ErrInternal("ai: this deployment holds no IAM identity")
	})

	ai := overPlane(pickCompletionsClient(&cloud.Config{AIBaseURL: g.srv.URL, AIAPIKey: "sk-operator"}, luxlog.New("test")))
	if _, err := ai.ChatCompletion(cloud.For(context.Background(), "acme"),
		&infer.ChatRequest{Model: "m", Prompt: "ping", Org: "acme", Actor: "acme/u-7"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if g.chatAuth != "Bearer sk-operator" {
		t.Errorf("completions Authorization = %q, want the operator's key", g.chatAuth)
	}
}

// A refusal crosses as the model API wrote it, and reads as the gateway's own: a
// 402 is the same *openai.APIError over the plane as over HTTP, so the route walk
// and the retry above cannot tell the transports apart.
func TestAPlaneRefusalReadsAsTheGatewaysOwn(t *testing.T) {
	g := newGatewayStub(t)
	servePlaneAI(t, func(context.Context, *client.ChatIn) (*client.Chat, error) {
		return &client.Chat{Status: http.StatusPaymentRequired,
			Body: []byte(`{"error":{"message":"insufficient_balance: add funds","type":"billing_error","code":"insufficient_balance"}}`)}, nil
	})

	ai := overPlane(pickCompletionsClient(prodLikeConfig(t, g, ""), luxlog.New("test")))
	_, err := ai.ChatCompletion(context.Background(), &infer.ChatRequest{Model: "m", Prompt: "ping", Org: "acme"})
	var refusal *openai.APIError
	if !errors.As(err, &refusal) || refusal.HTTPStatusCode != http.StatusPaymentRequired {
		t.Fatalf("ChatCompletion = %v, want the model API's 402 as an *openai.APIError", err)
	}
	if !strings.Contains(refusal.Message, "insufficient_balance") {
		t.Errorf("message = %q, want the model API's own", refusal.Message)
	}
	if g.chatAuth != "" {
		t.Errorf("a refused completion fell through to the gateway carrying %q", g.chatAuth)
	}
}

// EMBEDDINGS AND RERANKS CROSS THE PLANE CARRYING THE ORG AND PROJECT THEY ARE FOR,
// as a completion does. Through the gateway they rode the embed key, so every org's
// passages and queries were named after that key's service account.
func TestEmbeddingsAndReranksCrossThePlaneCarryingTheirOrg(t *testing.T) {
	g := newGatewayStub(t)
	type crossing struct {
		who  zip.Caller
		body map[string]any
	}
	seen := make(chan crossing, 1)
	servePlaneAI(t, func(ctx context.Context, in *client.ChatIn) (*client.Chat, error) {
		var body map[string]any
		_ = json.Unmarshal(in.Request, &body)
		seen <- crossing{cloud.Who(ctx), body}
		if _, ok := body["query"]; ok {
			return &client.Chat{Status: http.StatusOK, Body: []byte(`{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}]}`)}, nil
		}
		return &client.Chat{Status: http.StatusOK, Body: []byte(`{"object":"list","data":[{"index":0,"embedding":[0.5,0.25]}]}`)}, nil
	})
	em := overPlane(pickEmbedClient(prodLikeConfig(t, g, "pk-lf-readonly-embed"), luxlog.New("test")))
	crossed := func() crossing {
		t.Helper()
		select {
		case c := <-seen:
			return c
		case <-time.After(5 * time.Second):
			t.Fatalf("the call never crossed the plane (gateway saw %q)", g.embedAuth)
			return crossing{}
		}
	}

	vecs, err := em.Embed(context.Background(), &infer.EmbedRequest{Model: "zen-embedding", Inputs: []string{"hello"}, Org: "acme", Project: "p1"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 2 || vecs[0][0] != 0.5 {
		t.Errorf("vectors = %v, want the model API's own", vecs)
	}
	if c := crossed(); c.who.Org != "acme" || c.who.Project != "p1" || c.body["model"] != "zen-embedding" {
		t.Errorf("embeddings crossed as org %q project %q body %v, want acme / p1 / zen-embedding", c.who.Org, c.who.Project, c.body)
	}

	globex := zip.WithCaller(context.Background(), zip.Caller{Org: "globex", Project: "secret", User: "u-9"})
	scores, err := em.Rerank(globex, &infer.RerankRequest{Model: "zen-rerank", Query: "q", Documents: []string{"a", "b"}, Org: "acme"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != 2 || scores[1] != 0.9 {
		t.Errorf("scores = %v, want the model API's own", scores)
	}
	if c := crossed(); c.who.Org != "acme" || c.who.Project != "" || c.who.User != "" || c.body["query"] != "q" {
		t.Errorf("rerank crossed as %+v body %v, want acme with no project and nobody", c.who, c.body)
	}
	if g.embedAuth != "" {
		t.Errorf("an embedding reached the gateway carrying %q; it crosses the plane", g.embedAuth)
	}
}
