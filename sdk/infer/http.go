package infer

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/supply"
	openai "github.com/hanzoai/go-openai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// aiTracer emits the LLM/agent GenAI spans (OTel gen_ai.* semantic conventions)
// shipped over the ZAP wire to o11y. One tracer name for the whole clients
// package, asked for at each span rather than held: the global provider is
// installed by the composition root after this package loads, and a tracer
// resolved first keeps reporting to whichever provider it met first.
func aiTracer() trace.Tracer { return otel.Tracer("hanzo.ai/cloud") }

// AttrTTFT is the attribute a streamed completion's gen_ai span carries for its
// time to first token, in milliseconds: from the moment the model was asked to the
// first delta of content it sent back.
//
// The two are separate facts on separate spans; a turn's latency is their sum
// only for a reader who adds them.
//
// A completion that is not streamed has no first token to see and carries no
// such attribute, rather than its whole duration under this name.
const AttrTTFT = "hanzo.ai.ttft_ms"

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
	ctx, span := aiTracer().Start(ctx, operation+" "+model, trace.WithSpanKind(trace.SpanKindClient))
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

// setFeatureAttr names the product surface a model call was made FOR, when one
// was (ChatRequest.Feature). It rides the call's own gen_ai span for the
// reason the run id does: a translate completion and a chat turn are otherwise the
// same row, and a filter beats walking every trace up to its root.
func setFeatureAttr(span trace.Span, feature string) {
	if feature != "" {
		span.SetAttributes(attribute.String("hanzo.feature", feature))
	}
}

// setRuntimeDigestAttr records the content digest of the agent runtime that issued
// a model call, when one was (ChatRequest.RuntimeDigest). It rides the call's own
// gen_ai span as gen_ai.hanzo.runtime_digest so a completion can be tied to the
// exact runtime build that produced it — a filter on the span plane rather than a
// guess from a parent walk. Empty is a completion no runtime claims, and carries
// no attribute rather than an empty one that would read as a runtime named "".
func setRuntimeDigestAttr(span trace.Span, digest string) {
	if digest != "" {
		span.SetAttributes(attribute.String("gen_ai.hanzo.runtime_digest", digest))
	}
}

// setCacheConnectorAttr names the prompt-cache connector in front of the model for
// a call, when one was (ChatRequest.CacheConnector). It rides the call's own
// gen_ai span as gen_ai.hanzo.cache_connector so a cache hit can be told apart
// from a cold call on the span plane. Empty is a call with no cache connector,
// and carries no attribute rather than an empty one.
func setCacheConnectorAttr(span trace.Span, connector string) {
	if connector != "" {
		span.SetAttributes(attribute.String("gen_ai.hanzo.cache_connector", connector))
	}
}

// httpAI is the real, in-process Client: it runs chat completions
// against an OpenAI-compatible endpoint — the Hanzo LLM gateway
// (https://api.hanzo.ai/v1). This is the ONE concrete inference client the
// agents subsystem executes runs through; without it deps.AI is the fail-closed
// stub and every POST /v1/agent/:name/run fail-closes rather than executing.
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
	defaultModel ModelFor
}

// aiHTTPTimeout is a BACKSTOP, and not the completion's deadline.
//
// The deadline belongs to the caller, because the caller is the only layer that
// knows what it is waiting for: an agent waiting on a local model sets
// modelBudget from the fact that TTFT alone can reach two minutes, and a
// scheduler tick wants something far tighter. This only stops a request whose
// caller set no deadline AT ALL from waiting forever.
//
// So it sits ABOVE any caller's bound. At 120s it sat below one, and a backstop
// that fires first is not a backstop — it is a second deadline with worse
// information, reporting `context deadline exceeded` about a completion that
// was still arriving.
const aiHTTPTimeout = 5 * time.Minute

// tokenTimeout bounds an M2M token fetch, which is a different question with a
// different answer: the identity provider replies in a moment or it is down. It
// bounds the fetch alone, never the call the token is for (HTTPM2MOn).
var tokenTimeout = 30 * time.Second

// streamIdle is how long a streamed completion may go without a frame before it is
// hung up on. A stream is bounded by its silence, not by a wall clock: one still
// producing tokens is an answer arriving, however long it is, and one that has said
// nothing for this long is not going to. It covers the wait for the first frame, so
// it sits at the two minutes a local model's TTFT can reach (aiHTTPTimeout).
var streamIdle = 2 * time.Minute

// errStreamIdle is why a stream that fell silent was hung up on.
var errStreamIdle = errors.New("no frame within the idle gap")

// ModelFor answers the ROUTE a request runs on when the caller named none: an
// ordered list, tried in order, first hop first.
//
// A list rather than a name because that is what our families ARE. `enso` is a
// router over several backends, not an alias for one, so a single name could
// only ever be the first hop of something this type can hold whole.
//
// It is a FUNCTION because AI routing is configured at runtime and read per
// request: a tier can be repriced or a backend retired without rebuilding this
// binary. package cloud supplies one that asks the platform's own `ai`
// configuration (aimodel.Route); this package holds no policy and cannot — it is
// below cloud in the import graph.
//
// An empty or nil answer means no route is configured, and the caller's own
// request model (if any) is all there is.
type ModelFor func(context.Context) []string

// FixedModel is the one-hop route, spelled out. A caller that genuinely has a
// single model — a test, or a client pinned to one backend — says so here rather
// than getting a second constructor that takes a string.
func FixedModel(name string) ModelFor {
	return func(context.Context) []string { return []string{name} }
}

// HTTPAt returns a Client that POSTs OpenAI-compatible chat
// completions to baseURL, authenticated with apiKey. baseURL is the gateway
// /v1 root (the go-openai client appends /chat/completions). defaultModel is
// substituted when a ChatRequest carries no explicit model.
//
// apiKey is a KMS-injected secret and is NEVER logged: it lives only inside the
// go-openai client's Authorization header. Callers log the base URL and default
// model, never the key.
func HTTPAt(baseURL, apiKey string, defaultModel ModelFor) Client {
	return HTTPOn(baseURL, apiKey, defaultModel, nil)
}

// HTTPOn is HTTPAt with the TRANSPORT stated separately from the address —
// the same client, the same OpenAI-compatible wire, reached a different way.
//
// It exists because `ai` is a plugin of this same binary running as its own
// process, and its routes ride its unix socket exactly as they ride the public
// listener ("ZAP over a unix socket is simply the address the caller dialed").
// A sibling can therefore speak the ordinary wire to a peer WITHOUT leaving the
// host: no ingress, no Cloudflare, no public address, and no token minted to
// authenticate to our own deployment.
//
// rt nil ⇒ the default transport, so HTTPAt is unchanged. When rt dials a
// fixed socket the base URL's HOST is inert — it names the peer for logs and
// error text, and the path prefix still matters.
func HTTPOn(baseURL, apiKey string, defaultModel ModelFor, rt http.RoundTripper) Client {
	base := strings.TrimRight(baseURL, "/")
	// No Client.Timeout. It caps the WHOLE exchange including the body read,
	// which on a streamed completion is a wall-clock limit on how much the
	// model is allowed to say — the request is killed mid-sentence and the
	// tokens already paid for are thrown away. Every call below derives its own
	// deadline from the caller's context instead, which is the one place that
	// knows what is being waited for.
	hc := &http.Client{Transport: rt}
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

// HTTPM2M returns a Client that authenticates to the gateway with an
// IAM client-credentials (M2M) token instead of a static key. This is the
// durable Hanzo credential path: the cloud binary mints and auto-refreshes a
// short-lived token from its OWN service identity (IAM_CLIENT_ID/SECRET), so
// there is NO static key to rotate and no expiry cliff. On the Hanzo deployment
// that identity resolves to admin/hanzo-cloud, which the gateway treats as
// balance-exempt — so cloud's own per-org Meter stays the single
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
func HTTPM2M(baseURL, tokenURL, clientID, clientSecret string, defaultModel ModelFor) Client {
	return HTTPM2MOn(baseURL, tokenURL, clientID, clientSecret, defaultModel, nil)
}

// HTTPM2MOn is HTTPM2M with the TRANSPORT stated separately from the
// address, for the same reason HTTPOn exists: the inference wire can reach a
// peer over its own socket instead of the public listener. Only the INFERENCE
// leg rides rt — the token exchange keeps the default transport, because IAM is
// a different peer and naming it is a separate question.
func HTTPM2MOn(baseURL, tokenURL, clientID, clientSecret string, defaultModel ModelFor, rt http.RoundTripper) Client {
	cc := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
		// hanzo.id expects the credentials in the form body, not Basic
		// auth — matches the proven client_credentials call.
		AuthStyle: oauth2.AuthStyleInParams,
	}
	base := strings.TrimRight(baseURL, "/")
	// The token is minted over the DEFAULT transport (IAM is its own peer) by a
	// client bounded at tokenTimeout, and carried as a Bearer onto rt for the
	// inference call, which has no wall clock (see HTTPOn). Not cc.Client:
	// oauth2.NewClient copies the minting client's Timeout onto the client it
	// returns, which cut every M2M completion and stream at 30s while the model
	// went on generating for nobody.
	mint := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: tokenTimeout})
	authed := &http.Client{Transport: &oauth2.Transport{Source: cc.TokenSource(mint), Base: rt}}
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

// ChatCompletion maps a ChatRequest to a single user-message chat
// completion and returns the assistant content. On a transport failure, a
// non-2xx upstream status, or a response with no choices it returns an explicit
// wrapped error — executeRun records that as an honest error-status run, never a
// fabricated "ok". The error text names the model but never the key or prompt.
// ChatCompletion answers on the first hop of the route that can.
//
// A caller who NAMED a model gets that model and no substitution: they asked for
// something specific and quietly answering from somewhere else is a lie about
// what produced the text. A caller who named none gets the configured route, and
// a hop that cannot serve falls through to the next — so a paid tier being
// unavailable or unaffordable degrades to the free one instead of failing.
//
// Only a MODEL-shaped refusal moves to the next hop (see routeOn). A bad prompt,
// a missing credential or a transient overload are answered as themselves: the
// first is the caller's, the second is ours, and the third already has a retry
// that does not need to change which model is being asked.
func (a *httpAI) ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	route := a.route(ctx, req.Model)
	var lastErr error
	for i, model := range route {
		resp, err := a.chatOnce(ctx, req, model)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if i == len(route)-1 || !routeOn(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// route is the ordered list of models to try: the caller's own if they named one,
// otherwise whatever the platform has configured.
func (a *httpAI) route(ctx context.Context, named string) []string {
	if m := strings.TrimSpace(named); m != "" {
		return []string{m}
	}
	if a.defaultModel == nil {
		return nil
	}
	return a.defaultModel(ctx)
}

// routeOn reports whether a failure is about the MODEL rather than the request,
// so the next hop is worth trying.
//
// It matches the two shapes a tier that cannot serve produces: the gateway
// refusing a model it does not carry, and the money plane refusing one this
// caller cannot afford. Anything else stops the walk — retrying a malformed
// prompt on a cheaper model just fails twice and bills for both.
func routeOn(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, shape := range []string{
		"is not available",     // gateway: model not in this deployment's catalog
		"model_not_found",      // OpenAI-compatible spelling of the same
		"unknown model",        //
		"insufficient_balance", // money plane: this tier is unaffordable
		"spend_cap_exceeded",   //
		"payment required",     //
		supply.Code,            // a provider account behind this model is spent; another model may be served from another
	} {
		if strings.Contains(s, shape) {
			return true
		}
	}
	return false
}

func (a *httpAI) chatOnce(ctx context.Context, req *ChatRequest, model string) (*ChatResponse, error) {
	// GenAI client span (OTel semantic conventions) — one span per LLM call,
	// nested under any active agent-run span carried on ctx.
	ctx, span := StartGenAISpan(ctx, "hanzo", "chat", model, req.Org, req.Project)
	setRunAttr(span, req.RunID)
	setFeatureAttr(span, req.Feature)
	setRuntimeDigestAttr(span, req.RuntimeDigest)
	setCacheConnectorAttr(span, req.CacheConnector)
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, aiHTTPTimeout)
	defer cancel()

	resp, err := a.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    model,
		Messages: openaiMessages(req),
		// The ceiling the prepaid gate RESERVED, raised internally so hidden
		// channel/thought reasoning (stripped below) does not eat the caller's
		// visible max_tokens budget and truncate the answer (issue #782). The
		// reservation still binds: the cap is never left to the provider.
		MaxTokens: internalMaxTokens(req.MaxTokens),
		// Absent unless the caller offered tools, so a request that offers none
		// is the same bytes on the wire it always was.
		Tools: openaiTools(req.Tools),
		// WHO this completion is for. `user` is the OpenAI-compatible field for
		// exactly that, so this invents no vocabulary and the gateway already
		// parses it. Omitted when there is nobody to name (omitempty), which is
		// how a system call stays a system call.
		User: req.Actor,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "chat completion failed")
		// Tag a transient overload (429/5xx/"overloaded") with ErrUpstreamBusy
		// so the agent runner can retry/fail over; a provider budget stop with its
		// own class; a permanent error is left untagged and fails fast. The message
		// is preserved either way.
		return nil, fmt.Errorf("cloud: chat completion (model %q): %w", model, tagUpstream(err))
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
		return nil, fmt.Errorf("cloud: chat completion (model %q): upstream returned no choices: %w", model, ErrUpstreamBusy)
	}
	choice := resp.Choices[0]
	// WHY the model stopped, which is the difference between an answer and a
	// sentence that ran out of room. It was read off the wire and returned to the
	// caller from the first day and recorded nowhere, so a truncated reply
	// ("length") and a finished one ("stop") were the same span, and a run that
	// ended mid-tool-call was indistinguishable from one that chose to stop. The
	// convention spells it as a list because a multi-choice response has one per
	// choice; this client reads choice 0, so the list has one element and says so
	// rather than flattening to a scalar the reader would have to special-case.
	if fr := string(choice.FinishReason); fr != "" {
		span.SetAttributes(attribute.StringSlice("gen_ai.response.finish_reasons", []string{fr}))
	}

	if parsed := parseToolCallsFromContent(choice.Message.Content); len(parsed) > 0 {
		span.SetAttributes(attribute.String("hanzo.ai.parsed_inline", "true"))
		return &ChatResponse{
			Content:          "",
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			ToolCalls:        parsed,
			FinishReason:     "tool_calls",
		}, nil
	}
	if marker := unparsed(choice.Message.Content); marker != "" {
		span.SetAttributes(attribute.String("hanzo.ai.unparsed", marker))
		span.SetStatus(codes.Error, "unparsed tool call")
		return nil, fmt.Errorf("cloud: chat completion (model %q): upstream returned a tool call it had not parsed: %w", model, ErrUpstreamBusy)
	}
	visible, reasoning := stripReasoning(choice.Message.Content)
	if reasoning != "" {
		span.SetAttributes(attribute.String("hanzo.ai.reasoning_stripped", "true"))
	}
	return &ChatResponse{
		Content:          visible,
		Reasoning:        reasoning,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		ToolCalls:        readToolCalls(choice.Message.ToolCalls),
		FinishReason:     string(choice.FinishReason),
	}, nil
}

// openaiMessages renders a request's conversation for the gateway. A request that
// carries no Messages is the single user turn this client has always sent —
// identical bytes, so the no-tools path is untouched.
//
// An assistant turn that called tools is sent back with its ToolCalls and a
// RoleTool turn with its ToolCallID, because that pair is what the gateway
// matches a result to its call by; drop either and the model is answered with an
// orphan.
func openaiMessages(req *ChatRequest) []openai.ChatCompletionMessage {
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
		// A turn that shows pictures is content parts: the words, then each
		// picture. go-openai refuses a message carrying both forms.
		if len(m.Images) > 0 {
			msg.Content = ""
			if m.Content != "" {
				msg.MultiContent = append(msg.MultiContent, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: m.Content})
			}
			for _, u := range m.Images {
				msg.MultiContent = append(msg.MultiContent, openai.ChatMessagePart{
					Type:     openai.ChatMessagePartTypeImageURL,
					ImageURL: &openai.ChatMessageImageURL{URL: u},
				})
			}
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{
				ID:       tc.ID,
				Type:     openai.ToolTypeFunction,
				Function: openai.FunctionCall{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		// Providers like Anthropic require strict alternation of roles ("user" / "assistant").
		// If two consecutive messages have the same role and neither is a tool call / tool response,
		// merge their content so the request is valid across all upstreams.
		if len(out) > 0 && out[len(out)-1].Role == msg.Role && len(msg.ToolCalls) == 0 && len(out[len(out)-1].ToolCalls) == 0 && msg.ToolCallID == "" && out[len(out)-1].ToolCallID == "" {
			prev := &out[len(out)-1]
			if prev.Content != "" && msg.Content != "" {
				prev.Content += "\n\n" + msg.Content
			} else if msg.Content != "" {
				prev.Content = msg.Content
			}
			if len(msg.MultiContent) > 0 {
				prev.MultiContent = append(prev.MultiContent, msg.MultiContent...)
			}
			continue
		}
		out = append(out, msg)
	}
	return out
}

// openaiTools renders the offered tools as OpenAI function definitions. The schema
// crosses as json.RawMessage — the tool plane's own inputSchema, verbatim — so
// nothing here has an opinion about what a tool's arguments look like. A tool
// that declares no schema is offered as taking an empty object rather than as
// taking nothing, which is what a model needs to emit valid arguments for it.
func openaiTools(defs []ToolDef) []openai.Tool {
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

var mistralUnparsedMarker = string([]byte{91, 84, 79, 79, 76, 95, 67, 65, 76, 76, 83, 93})

// unparsed names the tool-call markup a completion's CONTENT carries, or "" for
// content that carries none.
//
// These strings are SPECIAL TOKENS. A model emits them so the stack serving it
// The set is the families this gateway routes to, each token taken from that
// family's own chat template.
// mistralUnparsedMarker is the mistral tool-call fence (opening bracket, TOOL,
// underscore, CALLS, closing bracket). Kept as bytes so editors cannot rewrite
// a marker literal in source.

func unparsed(content string) string {
	for _, marker := range []string{
		"<｜DSML｜tool_calls>",   // U+FF5C — deepseek's agentic markup
		"<｜tool▁call▁begin｜>",  // U+FF5C, U+2581 — deepseek's fenced form
		"<｜tool▁calls▁begin｜>", //   …and its plural opener
		"<tool_call>",          // qwen / hermes
		mistralUnparsedMarker,  // mistral
		"<|python_tag|>",       // llama
		"<|tool_call>",         // gemma
	} {
		if strings.Contains(content, marker) {
			return marker
		}
	}
	return ""
}

// channelRE matches the channel/thought markup a reasoning model wraps its
// chain-of-thought in (issue #782). The block is hidden reasoning, not part of
// the visible reply, so it is stripped out of Content and reported separately
// in Reasoning. The opening and closing tags are optional — a family may omit
// one — and whitespace either side of the thought belongs to the markup, not
// the answer.
// Pattern handles: <|channel>thought</|channel>, <|channel>thought... </|channel>, and split tags.
var channelRE = regexp.MustCompile(`(?s)<\|channel>thought(?:</\|channel>)?\s*(.*?)\s*(?:<\|channel>|</\|channel>|<channel\|>)\s*`)

// stripReasoning separates a completion's visible answer from its hidden
// channel/thought reasoning. It returns the visible content with the markup
// removed and the reasoning text that was inside it. Content with no channel
// markup is returned unchanged with an empty reasoning, so ordinary
// completions are byte-for-byte untouched.
func stripReasoning(content string) (visible, reasoning string) {
	m := channelRE.FindStringSubmatch(content)
	if m == nil {
		return content, ""
	}
	return strings.ReplaceAll(content, m[0], ""), strings.TrimSpace(m[1])
}

// internalMaxTokens is the cap the gateway actually sends upstream when the
// caller asked for max_tokens (issue #782). Hidden channel/thought reasoning
// is stripped from what the client sees, so it must never consume the visible
// budget and truncate the answer. The gateway raises its own internal cap so
// reasoning finishes before the answer, then strips the thought block before
// returning — the public meaning of max_tokens is unchanged: it bounds the
// visible reply. Zero stays zero (the caller stated no ceiling).
// The raised cap is bounded by max + max/4 + 256 so it can never silently spend
// unbounded tokens — the headroom is a fixed multiple of the user's budget.
func internalMaxTokens(maxTokens int) int {
	if maxTokens <= 0 {
		return 0
	}
	// Headroom above the caller's visible budget so reasoning + answer both
	// fit. 25% plus a fixed allowance keeps a short answer from being cut
	// solely because a long reasoning block arrived first.
	return maxTokens + maxTokens/4 + 1024
}

// readToolCalls lifts the model's tool calls off a choice. Only function calls
// are carried: they are the only kind this gateway serves, and a call of some
// other type has no arguments this side could dispatch.
func readToolCalls(calls []openai.ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, c := range calls {
		if c.Type != "" && c.Type != openai.ToolTypeFunction {
			continue
		}
		out = append(out, ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	return out
}

// parseToolCallsFromContent extracts structured tool calls from assistant content
// that carries markup the upstream did not parse. It handles:
//   - DSML: <｜DSML｜tool_calls><｜DSML｜invoke name="tool">...
//   - Begin Patch: *** Begin Patch ... *** End Patch
//
// Returns nil if no recognized markup or parsing failed.
func parseToolCallsFromContent(content string) []ToolCall {

	// 1. DSML format: <｜DSML｜tool_calls>...<｜DSML｜invoke name="tool">...
	if strings.Contains(content, "<｜DSML｜tool_calls>") {
		return parseDSMLToolCalls(content)
	}
	// 2. Begin Patch format: *** Begin Patch ... *** End Patch
	if strings.Contains(content, "*** Begin Patch") && strings.Contains(content, "*** End Patch") {
		return parseBeginPatch(content)
	}
	return nil
}

// parseDSMLToolCalls parses DeepSeek's DSML tool call markup.
// Example: <U+FF5C DSML U+FF5C tool_calls>
// <U+FF5C DSML U+FF5C invoke name="exec_command">
// <U+FF5C DSML U+FF5C parameter name="cmd" string="true">ls -la</U+FF5C DSML U+FF5C parameter>
// </U+FF5C DSML U+FF5C invoke>
// </U+FF5C DSML U+FF5C tool_calls>
func parseDSMLToolCalls(content string) []ToolCall {

	start := strings.Index(content, "<\uFF5CDSML\uFF5Ctool_calls>")
	if start == -1 {
		return nil
	}
	end := strings.Index(content[start:], "</\uFF5CDSML\uFF5Ctool_calls>")
	if end == -1 {
		return nil
	}
	block := content[start : start+end+len("</\uFF5CDSML\uFF5Ctool_calls>")]

	var calls []ToolCall
	invokePattern := regexp.MustCompile("(?s)<\uFF5CDSML\uFF5Cinvoke name=\"([^\"]+)\">(.*?)</\uFF5CDSML\uFF5Cinvoke>")
	matches := invokePattern.FindAllStringSubmatch(block, -1)

	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		name := m[1]
		params := m[2]
		args := parseDSMLParameters(params)
		calls = append(calls, ToolCall{
			ID:        "call_" + fmt.Sprintf("%d", len(calls)+1),
			Name:      name,
			Arguments: args,
		})
	}
	return calls
}

// parseDSMLParameters extracts parameters from DSML invoke content.
// Example: <U+FF5C DSML U+FF5C parameter name="cmd" string="true">ls -la</U+FF5C DSML U+FF5C parameter>
func parseDSMLParameters(params string) string {
	paramPattern := regexp.MustCompile("(?s)<\uFF5CDSML\uFF5Cparameter name=\"([^\"]+)\"[^>]*>(.*?)</\uFF5CDSML\uFF5Cparameter>")
	matches := paramPattern.FindAllStringSubmatch(params, -1)
	args := make(map[string]any)
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		name := m[1]
		value := m[2]
		var parsed any
		if json.Unmarshal([]byte(value), &parsed) == nil {
			args[name] = parsed
		} else {
			args[name] = value
		}
	}
	if len(args) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(args)
	return string(b)
}

// parseBeginPatch extracts a patch tool call from Begin Patch markup.
// Example: *** Begin Patch
// *** Update File: handler.go
// @@ func handle() {
// - return 1
// + return 2
// *** End Patch
func parseBeginPatch(content string) []ToolCall {
	start := strings.Index(content, "*** Begin Patch")
	if start == -1 {
		return nil
	}
	end := strings.Index(content[start:], "*** End Patch")
	if end == -1 {
		return nil
	}
	patch := content[start : start+end+len("*** End Patch")]

	return []ToolCall{{
		ID:        "call_patch_1",
		Name:      "patch",
		Arguments: fmt.Sprintf(`{"patch": %q}`, patch),
	}}
}

// ChatStream is the StreamCompleter capability: the SAME completion as
// ChatCompletion, delivered delta by delta as the model produces it. It is one
// upstream call with stream:true (+ stream_options.include_usage so the terminal
// frame still carries real token counts), so the returned ChatResponse is the
// identical value ChatCompletion would have returned — streaming is a delivery
// property, never a different result.
//
// A delta that arrives after emit returns an error (a disconnected client) stops
// the read and returns what was accumulated: the client hung up, the completion
// did not fail. Transport failures are tagged ErrUpstreamBusy on the same
// terms as ChatCompletion, so a caller's model-failover logic is unchanged.
// ChatStream walks the same route ChatCompletion does, with one difference that
// is forced by the wire: a hop is only retryable while NOTHING has been emitted.
// Once a delta has reached the client, the answer has started, and starting again
// on another model would splice two completions into one stream.
func (a *httpAI) ChatStream(ctx context.Context, req *ChatRequest, emit func(delta string) error) (*ChatResponse, error) {
	route := a.route(ctx, req.Model)
	var lastErr error
	for i, model := range route {
		var sent bool
		resp, err := a.streamOnce(ctx, req, model, func(d string) error { sent = true; return emit(d) })
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if sent || i == len(route)-1 || !routeOn(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (a *httpAI) streamOnce(ctx context.Context, req *ChatRequest, model string, emit func(delta string) error) (*ChatResponse, error) {
	ctx, span := StartGenAISpan(ctx, "hanzo", "chat", model, req.Org, req.Project)
	setRunAttr(span, req.RunID)
	setFeatureAttr(span, req.Feature)
	setRuntimeDigestAttr(span, req.RuntimeDigest)
	setCacheConnectorAttr(span, req.CacheConnector)
	defer span.End()

	// The idle gap, not a wall clock (streamIdle): every frame re-arms it, and a
	// stream that falls silent is cancelled, which closes the connection and stops
	// the model upstream.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	idle := time.AfterFunc(streamIdle, func() { cancel(errStreamIdle) })
	defer idle.Stop()

	// The model is asked HERE, so its time to first token starts here.
	asked := time.Now()
	stream, err := a.client.CreateChatCompletionStream(ctx, openai.ChatCompletionRequest{
		Model:    model,
		Messages: openaiMessages(req),
		// Same ceiling the gate reserved — streaming must not be a way to buy
		// more completion than was paid for. Raised internally so hidden
		// channel/thought reasoning does not eat the visible max_tokens budget
		// and truncate the answer (issue #782).
		MaxTokens: internalMaxTokens(req.MaxTokens),
		// Same person, same field: streaming must not be a way to buy inference
		// nobody is named for.
		User:          req.Actor,
		StreamOptions: &openai.StreamOptions{IncludeUsage: true},
	})
	if err != nil {
		if cause := context.Cause(ctx); errors.Is(cause, errStreamIdle) {
			err = fmt.Errorf("%w after %s: %w", cause, streamIdle, err)
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "chat stream failed")
		return nil, fmt.Errorf("cloud: chat stream (model %q): %w", model, tagUpstream(err))
	}
	defer stream.Close()

	var content strings.Builder
	emittedVis := 0
	ttftSet := false
	out := &ChatResponse{}
	// What the STREAM says about itself, accumulated as it arrives. Both facts
	// come in frames the delta loop below otherwise skips — the model on any
	// frame, the finish reason on a terminal one that carries no text — so they
	// are read before the "no content, move on" shortcuts rather than after.
	// Falling back to the requested model is honest: a gateway that never names
	// one has not told us it served something else.
	streamModel := model
	for {
		frame, err := stream.Recv()
		idle.Reset(streamIdle)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A mid-stream failure with content already delivered is NOT a failure:
			// the caller keeps (and has already shown) what the model produced.
			if content.Len() > 0 {
				break
			}
			if cause := context.Cause(ctx); errors.Is(cause, errStreamIdle) {
				err = fmt.Errorf("%w after %s: %w", cause, streamIdle, err)
			}
			span.RecordError(err)
			span.SetStatus(codes.Error, "chat stream failed")
			return nil, fmt.Errorf("cloud: chat stream (model %q): %w", model, tagUpstream(err))
		}
		if u := frame.Usage; u != nil {
			out.PromptTokens, out.CompletionTokens, out.TotalTokens = u.PromptTokens, u.CompletionTokens, u.TotalTokens
		}
		if m := strings.TrimSpace(frame.Model); m != "" {
			streamModel = m
		}
		if len(frame.Choices) == 0 {
			continue // usage-only terminal frame
		}
		// The stop reason rides a frame whose delta is empty, which the shortcut
		// below skips — so it is read here or not at all. Without it a stream that
		// was CUT at the token ceiling returned exactly like one that finished.
		if fr := strings.TrimSpace(string(frame.Choices[0].FinishReason)); fr != "" {
			out.FinishReason = fr
		}
		delta := frame.Choices[0].Delta.Content
		if delta == "" {
			continue
		}
		content.WriteString(delta)
		rawSoFar := content.String()
		// Hold emission while an unclosed <|channel>thought block is still streaming,
		// so thought tokens split across SSE frames never leak to emit() (#782).
		trimmed := strings.TrimLeft(rawSoFar, " \t\r\n")
		if strings.HasPrefix(trimmed, "<|channel>thought") && !channelRE.MatchString(rawSoFar) {
			continue
		}
		visSoFar, _ := stripReasoning(rawSoFar)
		if len(visSoFar) > emittedVis {
			chunkVis := visSoFar[emittedVis:]
			emittedVis = len(visSoFar)
			if !ttftSet && chunkVis != "" {
				ttftSet = true
				span.SetAttributes(attribute.Float64(AttrTTFT, float64(time.Since(asked))/float64(time.Millisecond)))
			}
			if emit != nil {
				if err := emit(chunkVis); err != nil {
					break // client gone — keep what we have
				}
			}
		}
	}
	// After the stream ends, strip channel/thought reasoning from the FULL
	// assembled content so tags split across deltas are also removed.
	// The visible answer and separated reasoning are assigned once.
	raw := content.String()
	out.Content, out.Reasoning = stripReasoning(raw)

	if strings.TrimSpace(out.Content) == "" {
		// No content at all is the gateway's "overloaded, no capacity" shape —
		// transient, so tag it busy for retry/failover, matching ChatCompletion.
		span.SetStatus(codes.Error, "no content")
		return nil, fmt.Errorf("cloud: chat stream (model %q): upstream returned no content: %w", model, ErrUpstreamBusy)
	}
	// Parse tool calls the upstream did not emit as structured calls (DSML,
	// Begin Patch). A parsed call is executed by the coding loop; it is not
	// the end of the turn.
	if parsed := parseToolCallsFromContent(out.Content); len(parsed) > 0 {
		span.SetAttributes(attribute.String("hanzo.ai.parsed_inline", "true"))
		out.ToolCalls = parsed
		out.Content = ""
		out.FinishReason = "tool_calls"
		return out, nil
	}
	// The same refusal as ChatCompletion, at the other exit of the same boundary:
	// streaming is a delivery property, so a completion that is internals rather
	// than an answer is one here too. The deltas have already been emitted, which
	// is the honest limit of a net placed after the fact — returning the error
	// still stops this text from being stored and replayed as the run's answer.
	if marker := unparsed(out.Content); marker != "" {
		span.SetAttributes(attribute.String("hanzo.ai.unparsed", marker))
		span.SetStatus(codes.Error, "unparsed tool call")
		return nil, fmt.Errorf("cloud: chat stream (model %q): upstream returned a tool call it had not parsed: %w", model, ErrUpstreamBusy)
	}
	span.SetAttributes(
		attribute.Int("gen_ai.usage.input_tokens", out.PromptTokens),
		attribute.Int("gen_ai.usage.output_tokens", out.CompletionTokens),
		// The model that ANSWERED, on the streaming path too. The buffered path has
		// always recorded it; a stream did not, so the one shape a reader uses to
		// tell "the model I asked for" from "the model that served me" was missing
		// from exactly the calls a user watches arrive. streamModel falls back to
		// the requested name when the frames never name one, which is honest: a
		// gateway that does not say is not a gateway that served something else.
		attribute.String("gen_ai.response.model", streamModel),
	)
	if out.FinishReason != "" {
		span.SetAttributes(attribute.StringSlice("gen_ai.response.finish_reasons", []string{out.FinishReason}))
	}
	return out, nil
}

// transientChat reports whether an upstream chat-completion error is a transient
// overload the agent runner may safely retry or fail over on: HTTP
// 429/500/502/503/504, or a gateway that surfaces "overloaded" without a typed
// status, or an upstream model provider refusal (e.g. 400/422 with provider errors
// or context length limits) that the fallback model can answer.
func transientChat(err error) bool {
	if code, ok := httpStatus(err); ok {
		switch code {
		case http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			msg := strings.ToLower(err.Error())
			return strings.Contains(msg, "provider returned error") ||
				strings.Contains(msg, "could not serve this request") ||
				strings.Contains(msg, "context") ||
				strings.Contains(msg, "token limit") ||
				strings.Contains(msg, "maximum context") ||
				strings.Contains(msg, "overloaded")
		default:
			return false
		}
	}
	// No typed status (a bare transport/overload message) — treat an explicit
	// "overloaded" or provider refusal as transient; anything else is not classifiable.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "overloaded") ||
		strings.Contains(msg, "provider returned error") ||
		strings.Contains(msg, "could not serve this request")
}

// tagUpstream classifies a failed model call for the caller: a provider budget
// stop carries its class (supply.Exhausted), a transient overload carries
// ErrUpstreamBusy, anything else is returned as it came. The stop is decided first
// because it shares its statuses with an overload — a vendor out of quota answers
// 429 — and retrying it on the same model cannot help until the account is funded
// or its period turns.
func tagUpstream(err error) error {
	if ex, ok := budgetStop(err); ok {
		return fmt.Errorf("%w: %w", err, ex)
	}
	if transientChat(err) {
		return fmt.Errorf("%w: %w", err, ErrUpstreamBusy)
	}
	return err
}

// budgetStop reads a failed model call as a provider budget stop when the answer
// says so in words a provider budget is said in: the fleet's own class
// (budget_exhausted, from the model API or any sibling rendering it), a vendor out
// of quota (insufficient_quota), or a vendor out of credit ("credit balance is too
// low"). The caller's OWN money — insufficient_balance, spend_cap_exceeded — is
// not a provider's budget and never matches: that is the caller's to fix and is
// already its own refusal.
//
// It does not report the stop: the model API counted and said it where the vendor
// refused (its drained accounts, sdk/supply), and an in-process caller saying it
// again would count one stop twice.
func budgetStop(err error) (*supply.Exhausted, bool) {
	status, ok := httpStatus(err)
	if !ok {
		return nil, false
	}
	var code, typ, msg, layer string
	var apiErr *openai.APIError
	var reqErr *openai.RequestError
	switch {
	case errors.As(err, &reqErr):
		// A body go-openai could not read as its error shape — the fleet's RFC 9457
		// problem ({"code":…,"error":"…","layer":…}) is one. Asked BEFORE the
		// APIError, because such a RequestError wraps an empty APIError left over
		// from the failed decode, and that would read as nothing.
		var p map[string]any
		_ = json.Unmarshal(reqErr.Body, &p)
		code, _ = p["code"].(string)
		layer, _ = p["layer"].(string)
		switch e := p["error"].(type) {
		case string:
			msg = e
		case map[string]any:
			if c, ok := e["code"].(string); ok && code == "" {
				code = c
			}
			typ, _ = e["type"].(string)
			msg, _ = e["message"].(string)
		}
	case errors.As(err, &apiErr):
		code, typ, msg = fmt.Sprint(apiErr.Code), apiErr.Type, apiErr.Message
	default:
		return nil, false
	}
	stopped := code == supply.Code || typ == supply.Code ||
		code == "insufficient_quota" || typ == "insufficient_quota" ||
		strings.Contains(strings.ToLower(msg), "credit balance is too low")
	if !stopped {
		return nil, false
	}
	l := supply.Model
	if named, ok := supply.ParseLayer(layer); ok {
		l = named
	}
	return &supply.Exhausted{Provider: "hanzo", Layer: l, Upstream: status, Msg: msg}, true
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
func (a *httpAI) Embed(ctx context.Context, req *EmbedRequest) ([][]float32, error) {
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

// Rerank scores documents against a query through the gateway's Cohere-shaped
// /rerank, on the same transport, credential and span conventions as Embed. It
// asks for every document (top_n = all) so the answer aligns with the input,
// and refuses a response that does not cover the input rather than guessing.
func (a *httpAI) Rerank(ctx context.Context, req *RerankRequest) ([]float64, error) {
	if req == nil || len(req.Documents) == 0 {
		return nil, nil
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return nil, fmt.Errorf("cloud: rerank: empty model")
	}
	ctx, span := StartGenAISpan(ctx, "hanzo", "rerank", model, req.Org, req.Project)
	defer span.End()
	span.SetAttributes(attribute.Int("gen_ai.request.input_count", len(req.Documents)))

	ctx, cancel := context.WithTimeout(ctx, aiHTTPTimeout)
	defer cancel()

	body, _ := json.Marshal(map[string]any{
		"model": model, "query": req.Query, "documents": req.Documents,
		"top_n": len(req.Documents), "return_documents": false,
	})
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	resp, err := a.http.Do(hreq)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "rerank transport failed")
		return nil, fmt.Errorf("cloud: rerank (model %q): %w", model, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		span.SetStatus(codes.Error, "rerank non-2xx")
		trunc := raw
		if len(trunc) > 200 {
			trunc = trunc[:200]
		}
		return nil, fmt.Errorf("cloud: rerank (model %q): status %d: %s", model, resp.StatusCode, string(trunc))
	}
	var decoded struct {
		Results []struct {
			Index int     `json:"index"`
			Score float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("cloud: rerank decode: %w", err)
	}
	scores := make([]float64, len(req.Documents))
	seen := make([]bool, len(req.Documents))
	for _, r := range decoded.Results {
		if r.Index < 0 || r.Index >= len(scores) || seen[r.Index] {
			return nil, fmt.Errorf("cloud: rerank: result index %d outside %d documents", r.Index, len(scores))
		}
		scores[r.Index], seen[r.Index] = r.Score, true
	}
	if len(decoded.Results) != len(scores) {
		return nil, fmt.Errorf("cloud: rerank: %d scores for %d documents", len(decoded.Results), len(scores))
	}
	return scores, nil
}

// Transcribe implements Transcriber: the recording posted to the
// gateway's /audio/transcriptions as multipart form data, on the same
// transport, credential and span conventions as Embed, answering the
// transcript's text.
func (a *httpAI) Transcribe(ctx context.Context, req *TranscribeRequest) (string, error) {
	if req == nil || len(req.Audio) == 0 {
		return "", fmt.Errorf("cloud: transcribe: no audio")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return "", fmt.Errorf("cloud: transcribe: empty model")
	}
	ctx, span := StartGenAISpan(ctx, "hanzo", "transcriptions", model, req.Org, req.Project)
	defer span.End()
	span.SetAttributes(attribute.Int("gen_ai.request.audio_bytes", len(req.Audio)))

	ctx, cancel := context.WithTimeout(ctx, aiHTTPTimeout)
	defer cancel()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("model", model)
	_ = form.WriteField("response_format", "json")
	part, err := form.CreateFormFile("file", cmp.Or(path.Base(req.Name), "audio"))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(req.Audio); err != nil {
		return "", err
	}
	if err := form.Close(); err != nil {
		return "", err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		span.RecordError(err)
		return "", err
	}
	hreq.Header.Set("Content-Type", form.FormDataContentType())
	if a.apiKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	resp, err := a.http.Do(hreq)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "transcriptions transport failed")
		return "", fmt.Errorf("cloud: transcriptions (model %q): %w", model, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		span.SetStatus(codes.Error, "transcriptions non-2xx")
		trunc := raw
		if len(trunc) > 200 {
			trunc = trunc[:200]
		}
		return "", fmt.Errorf("cloud: transcriptions (model %q): status %d: %s", model, resp.StatusCode, string(trunc))
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", fmt.Errorf("cloud: transcriptions decode: %w", err)
	}
	return decoded.Text, nil
}

// Models implements ModelLister: it returns the ids of the models this
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

// Via returns a copy of the gateway client c that sends through rt instead of the
// network, keeping its base URL, and says whether it could. Only an M2M gateway client
// (one that holds no static key) can be re-routed: a static key is the operator's
// credential and stays where it is named, and so does the disabled Client.
func Via(c Client, rt http.RoundTripper) (Client, bool) {
	h, ok := c.(*httpAI)
	if !ok || h.apiKey != "" {
		return c, false
	}
	cfg := openai.DefaultConfig("") // no key: the body is all that crosses
	cfg.BaseURL = h.baseURL
	trip := &http.Client{Transport: rt}
	cfg.HTTPClient = trip
	p := *h
	p.client = openai.NewClientWithConfig(cfg)
	p.http = trip
	return &p, true
}
