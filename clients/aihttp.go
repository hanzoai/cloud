package clients

import (
	"context"
	"fmt"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/hanzoai/cloud/types"
)

// aiTracer emits the LLM/agent GenAI spans (OTel gen_ai.* semantic conventions)
// shipped over the ZAP wire to o11y. One tracer for the whole clients package.
var aiTracer = otel.Tracer("hanzo.ai/cloud")

// httpAI is the real, in-process types.AIClient: it runs chat completions
// against an OpenAI-compatible endpoint — the Hanzo LLM gateway
// (https://api.hanzo.ai/v1). This is the ONE concrete inference client the
// agents subsystem executes runs through; without it deps.AI is the fail-closed
// stub and every POST /v1/agents/:name/run fail-closes rather than executing.
//
// Model routing is the gateway's job. The only cloud-side fallback is: an empty
// request model → the operator-configured default. There is deliberately NO
// in-code model aliasing (e.g. a "zen" → "zen3-nano" map) — that is config in
// code, and the gateway already owns model resolution across its served set.
type httpAI struct {
	client       *openai.Client
	defaultModel string
}

// aiHTTPTimeout bounds a single completion so a hung upstream cannot wedge an
// agent run (or a scheduler tick) indefinitely. It is applied as a derived
// deadline on the caller's context, so a caller carrying a tighter deadline
// still wins — this is only a ceiling.
const aiHTTPTimeout = 120 * time.Second

// AIHTTPAt returns a types.AIClient that POSTs OpenAI-compatible chat
// completions to baseURL, authenticated with apiKey. baseURL is the gateway
// /v1 root (the go-openai client appends /chat/completions). defaultModel is
// substituted when a ChatRequest carries no explicit model.
//
// apiKey is a KMS-injected secret and is NEVER logged: it lives only inside the
// go-openai client's Authorization header. Callers log the base URL and default
// model, never the key.
func AIHTTPAt(baseURL, apiKey, defaultModel string) types.AIClient {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = strings.TrimRight(baseURL, "/")
	return &httpAI{client: openai.NewClientWithConfig(cfg), defaultModel: defaultModel}
}

// AIHTTPM2M returns a types.AIClient that authenticates to the gateway with an
// IAM client-credentials (M2M) token instead of a static key. This is the
// durable Hanzo credential path: the cloud binary mints and auto-refreshes a
// short-lived token from its OWN service identity (IAM_CLIENT_ID/SECRET), so
// there is NO static key to rotate and no expiry cliff. On the Hanzo deployment
// that identity resolves to admin/hanzo-cloud, which the gateway treats as
// balance-exempt — so cloud's own per-org ResourceMeter stays the single
// revenue debit (no double-bill).
//
// tokenURL is the IAM token endpoint ({issuer}/v1/iam/oauth/token). clientSecret
// is a KMS-injected secret and is NEVER logged: it lives only inside the oauth2
// token source. The token is fetched lazily on first use (boot never blocks on
// IAM) and cached+refreshed automatically by the oauth2 client.
//
// go-openai sets its own Authorization header only when its authToken is
// non-empty; here it is empty, so the sole auth header is the fresh Bearer the
// oauth2 transport injects on every request.
func AIHTTPM2M(baseURL, tokenURL, clientID, clientSecret, defaultModel string) types.AIClient {
	cc := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
		// hanzo.id (Casdoor) expects the credentials in the form body, not Basic
		// auth — matches the proven client_credentials call.
		AuthStyle: oauth2.AuthStyleInParams,
	}
	cfg := openai.DefaultConfig("") // empty authToken → go-openai adds no header
	cfg.BaseURL = strings.TrimRight(baseURL, "/")
	cfg.HTTPClient = cc.Client(context.Background()) // caches + auto-refreshes
	return &httpAI{client: openai.NewClientWithConfig(cfg), defaultModel: defaultModel}
}

// ChatCompletion maps a types.ChatRequest to a single user-message chat
// completion and returns the assistant content. On a transport failure, a
// non-2xx upstream status, or a response with no choices it returns an explicit
// wrapped error — executeRun records that as an honest error-status run, never a
// fabricated "ok". The error text names the model but never the key or prompt.
func (a *httpAI) ChatCompletion(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = a.defaultModel
	}

	// GenAI client span (OTel semantic conventions) — one span per LLM call,
	// nested under any active agent-run span carried on ctx.
	ctx, span := aiTracer.Start(ctx, "chat "+model, trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	span.SetAttributes(
		attribute.String("gen_ai.system", "hanzo"),
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", model),
	)

	ctx, cancel := context.WithTimeout(ctx, aiHTTPTimeout)
	defer cancel()

	resp, err := a.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: req.Prompt},
		},
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "chat completion failed")
		return nil, fmt.Errorf("cloud: chat completion (model %q): %w", model, err)
	}
	span.SetAttributes(
		attribute.String("gen_ai.response.model", resp.Model),
		attribute.Int("gen_ai.usage.input_tokens", resp.Usage.PromptTokens),
		attribute.Int("gen_ai.usage.output_tokens", resp.Usage.CompletionTokens),
	)
	if len(resp.Choices) == 0 {
		span.SetStatus(codes.Error, "no choices")
		return nil, fmt.Errorf("cloud: chat completion (model %q): upstream returned no choices", model)
	}
	return &types.ChatResponse{Content: resp.Choices[0].Message.Content}, nil
}
