package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	openai "github.com/hanzoai/go-openai"
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

// setScopeAttrs stamps the tenant scope onto a gen_ai span so o11y can filter
// telemetry PER TENANT (org) and sub-scope (project) — the emit side of per-tenant
// observability isolation. Empty values are omitted (a system call carries none).
func setScopeAttrs(span trace.Span, org, project string) {
	if org != "" {
		span.SetAttributes(attribute.String("hanzo.org", org))
	}
	if project != "" {
		span.SetAttributes(attribute.String("hanzo.project", project))
	}
}

// StartGenAISpan opens one OTel gen_ai client span on the shared cloud tracer and
// stamps the request-side semantic-convention attributes every AI call carries:
// gen_ai.system (the serving provider — "hanzo" for the Hanzo gateway, "cloudflare"
// for Workers AI, …), gen_ai.operation.name, gen_ai.request.model, plus the
// per-tenant scope (hanzo.org / hanzo.project). It is the ONE gen_ai span
// constructor: the HTTP chat/embed clients here AND any other inference path (the
// per-org Cloudflare Workers AI proxy) emit to the SAME o11y span plane through it,
// so AI telemetry is one shape with one tenant-isolation contract, never a parallel
// per-provider span. The caller defers span.End() and adds response attributes
// (tokens, status) after the call returns.
func StartGenAISpan(ctx context.Context, system, operation, model, org, project string) (context.Context, trace.Span) {
	ctx, span := aiTracer.Start(ctx, operation+" "+model, trace.WithSpanKind(trace.SpanKindClient))
	span.SetAttributes(
		attribute.String("gen_ai.system", system),
		attribute.String("gen_ai.operation.name", operation),
		attribute.String("gen_ai.request.model", model),
	)
	setScopeAttrs(span, org, project)
	return ctx, span
}

// setRunAttr names the agent run a model call was made FOR, when one was.
//
// The call already nests under that run's span, so the relationship is in the
// trace — but only as a shape, recoverable by walking parents. Carrying the id on
// the row itself is what makes "every model call this run made" a filter instead
// of a graph traversal, and it survives the cases where the shape does not: a
// parent dropped by sampling, a batch exported after the root, a query that has
// one span and needs to know whose it is.
//
// A direct API completion belongs to no run and carries no attribute, rather than
// an empty one that would read as a run whose name is "".
func setRunAttr(span trace.Span, runID string) {
	if runID != "" {
		span.SetAttributes(attribute.String("hanzo.agent.run_id", runID))
	}
}

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
	http         *http.Client // authenticated transport (static-key header or M2M oauth2), reused for /embeddings
	baseURL      string       // gateway /v1 root
	apiKey       string       // static bearer; "" when M2M (the http transport injects the Bearer)
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
	return AIHTTPOn(baseURL, apiKey, defaultModel, nil)
}

// AIHTTPOn is AIHTTPAt with the TRANSPORT stated separately from the address —
// the same client, the same OpenAI-compatible wire, reached a different way.
//
// It exists because `ai` is a plugin of this same binary running as its own
// process, and its routes ride its unix socket exactly as they ride the public
// listener ("ZAP over a unix socket is simply the address the caller dialed").
// A sibling can therefore speak the ordinary wire to a peer WITHOUT leaving the
// host: no ingress, no Cloudflare, no public address, and no token minted to
// authenticate to our own deployment.
//
// rt nil ⇒ the default transport, so AIHTTPAt is unchanged. When rt dials a
// fixed socket the base URL's HOST is inert — it names the peer for logs and
// error text, and the path prefix still matters.
func AIHTTPOn(baseURL, apiKey, defaultModel string, rt http.RoundTripper) types.AIClient {
	base := strings.TrimRight(baseURL, "/")
	hc := &http.Client{Timeout: aiHTTPTimeout, Transport: rt}
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = base
	cfg.HTTPClient = hc
	return &httpAI{
		client:       openai.NewClientWithConfig(cfg),
		http:         hc,
		baseURL:      base,
		apiKey:       apiKey,
		defaultModel: defaultModel,
	}
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
	return AIHTTPM2MOn(baseURL, tokenURL, clientID, clientSecret, defaultModel, nil)
}

// AIHTTPM2MOn is AIHTTPM2M with the TRANSPORT stated separately from the
// address, for the same reason AIHTTPOn exists: the inference wire can reach a
// peer over its own socket instead of the public listener. Only the INFERENCE
// leg rides rt — the token exchange keeps the default transport, because IAM is
// a different peer and naming it is a separate question.
func AIHTTPM2MOn(baseURL, tokenURL, clientID, clientSecret, defaultModel string, rt http.RoundTripper) types.AIClient {
	cc := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
		// hanzo.id expects the credentials in the form body, not Basic
		// auth — matches the proven client_credentials call.
		AuthStyle: oauth2.AuthStyleInParams,
	}
	base := strings.TrimRight(baseURL, "/")
	// The oauth2 client mints over the DEFAULT transport (IAM is its own peer) and
	// carries the Bearer onto rt for the inference call itself.
	authed := cc.Client(context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: aiHTTPTimeout}))
	if rt != nil {
		authed.Transport = &oauth2.Transport{Source: cc.TokenSource(context.Background()), Base: rt}
	}
	cfg := openai.DefaultConfig("") // empty authToken → go-openai adds no header
	cfg.BaseURL = base
	cfg.HTTPClient = authed
	return &httpAI{
		client:       openai.NewClientWithConfig(cfg),
		http:         authed, // same authed transport → /embeddings inherits the M2M Bearer
		baseURL:      base,
		defaultModel: defaultModel,
	}
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
	ctx, span := StartGenAISpan(ctx, "hanzo", "chat", model, req.Org, req.Project)
	setRunAttr(span, req.RunID)
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, aiHTTPTimeout)
	defer cancel()

	resp, err := a.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    model,
		Messages: wireMessages(req),
		// The ceiling the prepaid gate RESERVED. Sending it is what makes the
		// reservation binding: without it the provider picks its own limit and
		// can return a completion nobody paid for.
		MaxTokens: req.MaxTokens,
		// Absent unless the caller offered tools, so a request that offers none
		// is the same bytes on the wire it always was.
		Tools: wireTools(req.Tools),
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "chat completion failed")
		// Tag a transient overload (429/5xx/"overloaded") with types.ErrUpstreamBusy
		// so the agent runner can retry/fail over; a permanent error is left untagged
		// and fails fast. The message is preserved either way.
		if transientChat(err) {
			err = fmt.Errorf("%w: %w", err, types.ErrUpstreamBusy)
		}
		return nil, fmt.Errorf("cloud: chat completion (model %q): %w", model, err)
	}
	span.SetAttributes(
		attribute.String("gen_ai.response.model", resp.Model),
		attribute.Int("gen_ai.usage.input_tokens", resp.Usage.PromptTokens),
		attribute.Int("gen_ai.usage.output_tokens", resp.Usage.CompletionTokens),
	)
	if len(resp.Choices) == 0 {
		// A 200 with no choices is the gateway's "overloaded, no capacity" shape —
		// transient, so tag it busy for retry/failover rather than dropping the reply.
		span.SetStatus(codes.Error, "no choices")
		return nil, fmt.Errorf("cloud: chat completion (model %q): upstream returned no choices: %w", model, types.ErrUpstreamBusy)
	}
	choice := resp.Choices[0]
	return &types.ChatResponse{
		Content:          choice.Message.Content,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		ToolCalls:        readToolCalls(choice.Message.ToolCalls),
		FinishReason:     string(choice.FinishReason),
	}, nil
}

// wireMessages renders a request's conversation for the gateway. A request that
// carries no Messages is the single user turn this client has always sent —
// identical bytes, so the no-tools path is untouched.
//
// An assistant turn that called tools is sent back with its ToolCalls and a
// RoleTool turn with its ToolCallID, because that pair is what the gateway
// matches a result to its call by; drop either and the model is answered with an
// orphan.
func wireMessages(req *types.ChatRequest) []openai.ChatCompletionMessage {
	if len(req.Messages) == 0 {
		return []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: req.Prompt}}
	}
	out := make([]openai.ChatCompletionMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msg := openai.ChatCompletionMessage{
			Role:       m.Role,
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{
				ID:       tc.ID,
				Type:     openai.ToolTypeFunction,
				Function: openai.FunctionCall{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		out = append(out, msg)
	}
	return out
}

// wireTools renders the offered tools as OpenAI function definitions. The schema
// crosses as json.RawMessage — the tool plane's own inputSchema, verbatim — so
// nothing here has an opinion about what a tool's arguments look like. A tool
// that declares no schema is offered as taking an empty object rather than as
// taking nothing, which is what a model needs to emit valid arguments for it.
func wireTools(defs []types.ToolDef) []openai.Tool {
	if len(defs) == 0 {
		return nil // absent field, not an empty array: an empty one is a refusal to use tools
	}
	out := make([]openai.Tool, 0, len(defs))
	for _, d := range defs {
		schema := d.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  schema,
			},
		})
	}
	return out
}

// readToolCalls lifts the model's tool calls off a choice. Only function calls
// are carried: they are the only kind this gateway serves, and a call of some
// other type has no arguments this side could dispatch.
func readToolCalls(calls []openai.ToolCall) []types.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]types.ToolCall, 0, len(calls))
	for _, c := range calls {
		if c.Type != "" && c.Type != openai.ToolTypeFunction {
			continue
		}
		out = append(out, types.ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	return out
}

// ChatStream is the types.StreamCompleter capability: the SAME completion as
// ChatCompletion, delivered delta by delta as the model produces it. It is one
// upstream call with stream:true (+ stream_options.include_usage so the terminal
// frame still carries real token counts), so the returned ChatResponse is the
// identical value ChatCompletion would have returned — streaming is a delivery
// property, never a different result.
//
// A delta that arrives after emit returns an error (a disconnected client) stops
// the read and returns what was accumulated: the client hung up, the completion
// did not fail. Transport failures are tagged types.ErrUpstreamBusy on the same
// terms as ChatCompletion, so a caller's model-failover logic is unchanged.
func (a *httpAI) ChatStream(ctx context.Context, req *types.ChatRequest, emit func(delta string) error) (*types.ChatResponse, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = a.defaultModel
	}

	ctx, span := StartGenAISpan(ctx, "hanzo", "chat", model, req.Org, req.Project)
	setRunAttr(span, req.RunID)
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, aiHTTPTimeout)
	defer cancel()

	stream, err := a.client.CreateChatCompletionStream(ctx, openai.ChatCompletionRequest{
		Model:    model,
		Messages: wireMessages(req),
		// Same ceiling the gate reserved — streaming must not be a way to buy
		// more completion than was paid for.
		MaxTokens:     req.MaxTokens,
		StreamOptions: &openai.StreamOptions{IncludeUsage: true},
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "chat stream failed")
		if transientChat(err) {
			err = fmt.Errorf("%w: %w", err, types.ErrUpstreamBusy)
		}
		return nil, fmt.Errorf("cloud: chat stream (model %q): %w", model, err)
	}
	defer stream.Close()

	var content strings.Builder
	out := &types.ChatResponse{}
	for {
		frame, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A mid-stream failure with content already delivered is NOT a failure:
			// the caller keeps (and has already shown) what the model produced.
			if content.Len() > 0 {
				break
			}
			span.RecordError(err)
			span.SetStatus(codes.Error, "chat stream failed")
			if transientChat(err) {
				err = fmt.Errorf("%w: %w", err, types.ErrUpstreamBusy)
			}
			return nil, fmt.Errorf("cloud: chat stream (model %q): %w", model, err)
		}
		if u := frame.Usage; u != nil {
			out.PromptTokens, out.CompletionTokens, out.TotalTokens = u.PromptTokens, u.CompletionTokens, u.TotalTokens
		}
		if len(frame.Choices) == 0 {
			continue // usage-only terminal frame
		}
		delta := frame.Choices[0].Delta.Content
		if delta == "" {
			continue
		}
		content.WriteString(delta)
		if emit != nil {
			if err := emit(delta); err != nil {
				break // client gone — keep what we have
			}
		}
	}

	out.Content = content.String()
	if strings.TrimSpace(out.Content) == "" {
		// No content at all is the gateway's "overloaded, no capacity" shape —
		// transient, so tag it busy for retry/failover, matching ChatCompletion.
		span.SetStatus(codes.Error, "no content")
		return nil, fmt.Errorf("cloud: chat stream (model %q): upstream returned no content: %w", model, types.ErrUpstreamBusy)
	}
	span.SetAttributes(
		attribute.Int("gen_ai.usage.input_tokens", out.PromptTokens),
		attribute.Int("gen_ai.usage.output_tokens", out.CompletionTokens),
	)
	return out, nil
}

// transientChat reports whether an upstream chat-completion error is a transient
// overload the agent runner may safely retry or fail over on: HTTP
// 429/500/502/503/504, or a gateway that surfaces "overloaded" without a typed
// status. A permanent failure — a 4xx that is not 429 (bad request, auth, an
// unserved model) — returns false so it fails fast instead of burning retries.
func transientChat(err error) bool {
	if code, ok := httpStatus(err); ok {
		switch code {
		case http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	// No typed status (a bare transport/overload message) — treat an explicit
	// "overloaded" as transient; anything else is not classifiable, so not retried.
	return strings.Contains(strings.ToLower(err.Error()), "overloaded")
}

// httpStatus extracts the upstream HTTP status from a go-openai error
// (*openai.APIError for a decoded error body, *openai.RequestError for a raw
// non-2xx), returning ok=false when the error carries no status.
func httpStatus(err error) (int, bool) {
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) && apiErr.HTTPStatusCode > 0 {
		return apiErr.HTTPStatusCode, true
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) && reqErr.HTTPStatusCode > 0 {
		return reqErr.HTTPStatusCode, true
	}
	return 0, false
}

// Embed returns one vector per input from the gateway's OpenAI-compatible
// /embeddings, authenticated by the SAME credential as ChatCompletion — so the
// semantic tier bills and meters through the ONE org/project-aligned path, never
// a static side-channel key. It emits a gen_ai client span (OTel semantic
// conventions) exactly like chat, so embeddings are observable end to end. The
// raw POST reuses a.http (the static-key or M2M-authenticated transport); go-
// openai's typed embeddings path is bypassed because its EmbeddingModel enum
// rejects gateway-served models like "bge-m3".
func (a *httpAI) Embed(ctx context.Context, req *types.EmbedRequest) ([][]float32, error) {
	if req == nil || len(req.Inputs) == 0 {
		return nil, nil
	}
	model := strings.TrimSpace(req.Model)
	inputs := req.Inputs
	if model == "" {
		return nil, fmt.Errorf("cloud: embed: empty model")
	}

	ctx, span := StartGenAISpan(ctx, "hanzo", "embeddings", model, req.Org, req.Project)
	defer span.End()
	span.SetAttributes(attribute.Int("gen_ai.request.input_count", len(inputs)))

	ctx, cancel := context.WithTimeout(ctx, aiHTTPTimeout)
	defer cancel()

	body, _ := json.Marshal(map[string]any{"model": model, "input": inputs})
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" { // M2M leaves this empty; its transport injects the Bearer
		hreq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}

	resp, err := a.http.Do(hreq)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "embeddings transport failed")
		return nil, fmt.Errorf("cloud: embeddings (model %q): %w", model, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		span.SetStatus(codes.Error, "embeddings non-2xx")
		trunc := raw
		if len(trunc) > 200 {
			trunc = trunc[:200]
		}
		return nil, fmt.Errorf("cloud: embeddings (model %q): status %d: %s", model, resp.StatusCode, string(trunc))
	}
	var decoded struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("cloud: embeddings decode: %w", err)
	}
	if len(decoded.Data) != len(inputs) {
		return nil, fmt.Errorf("cloud: embeddings: got %d vectors for %d inputs", len(decoded.Data), len(inputs))
	}
	sort.Slice(decoded.Data, func(i, j int) bool { return decoded.Data[i].Index < decoded.Data[j].Index })
	out := make([][]float32, len(decoded.Data))
	for i, d := range decoded.Data {
		out[i] = d.Embedding
	}
	span.SetAttributes(attribute.Int("gen_ai.response.embeddings_count", len(out)))
	return out, nil
}

// Models implements types.ModelLister: it returns the ids of the models this
// gateway currently serves — the OpenAI-compatible GET /v1/models list (the same
// served set the run path resolves a model against). The agents subsystem calls
// it to reject a non-catalog model at create time. The call is bounded by
// aiHTTPTimeout so a slow gateway cannot wedge a create, and never returns the
// key: only model ids leave this method.
func (a *httpAI) Models(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, aiHTTPTimeout)
	defer cancel()

	list, err := a.client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloud: list models: %w", err)
	}
	ids := make([]string, 0, len(list.Models))
	for _, m := range list.Models {
		if id := strings.TrimSpace(m.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
