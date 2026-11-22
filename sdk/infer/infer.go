// Package infer is the contract for buying inference: the chat, embedding, rerank and
// transcription requests a program makes, the Client that answers them, and the
// gateway client that reaches api.hanzo.ai (all AI goes through Hanzo, never an
// upstream cloud). The framework hands a Client to every app; the meter that gates and
// debits it wraps it in the host.
package infer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hanzoai/account"
)

// ChatRequest mirrors the AI subsystem's chat-completion request. Payer is who
// pays for it and Org is whose data it reads. The metering decorator wrapping
// deps.AI gates Payer's balance before the call and debits Payer after, so no
// inference runs unattributed. A request with no Org is the platform's own work
// (executed, billed to nobody); a request with an Org and no Payer is refused.
type ChatRequest struct {
	Model  string
	Prompt string
	// Org is the EFFECTIVE org — the DATA scope the inner AI client reads (BYO
	// provider keys, RAG/knowledge). It never decides who pays.
	Org string
	// Payer is the account that pays: principal.Payer or cloud.PayerOf at the act
	// that started the work, carried here unchanged. In the signup org it is the
	// person; the org's own account there is the platform's pool, and naming an org
	// where a person belongs is how a stranger's call was billed to Hanzo. A
	// SuperAdmin acting in another org pays from its own books, which Payer already
	// says.
	Payer   account.Account
	Project string

	// MaxTokens is the ceiling on the COMPLETION the caller is willing to buy.
	// A prepaid gate cannot price a chat without it: the prompt is known before
	// the call and the completion is not, so an unbounded completion is an
	// unbounded charge against a balance that was only ever checked for the
	// prompt. Zero means the caller stated no ceiling and the meter reserves the
	// policy default (CLOUD_AI_MAX_COMPLETION_TOKENS) — never zero.
	MaxTokens int

	// Messages is the conversation, when there IS one. Prompt is the degenerate
	// case of it — a single user turn — and stays the only field a caller with
	// nothing to remember fills in, so every existing call site is unchanged. A
	// caller that sets Messages owns the whole transcript including its system
	// turn; Prompt is then only text the prepaid gate prices (see Text), and the
	// wire carries Messages.
	//
	// It exists because a tool call cannot be expressed as a string. The model's
	// request to run a tool and the result it is handed back are linked by
	// ToolCall.ID, and that linkage IS the loop — flatten it into prose and the
	// model is guessing which answer belongs to which question.
	Messages []ChatMessage
	// Tools is what the model may call this turn. Empty means it may call
	// nothing, which is what every caller before tool support asked for and
	// still gets: no tools field reaches the gateway at all, so the completion is
	// the one it produced before.
	Tools []ToolDef

	// RunID names the agent run this completion belongs to, when one does. It is
	// ATTRIBUTION and nothing else: it never reaches the gateway, and it is not
	// the ledger's idempotency key (that is metering.Usage.Ref, which the server
	// mints per debit — pinning one value across a tool loop's rounds would dedup
	// eight real charges into one).
	//
	// It exists because a run's cost is the SUM of its rounds. The flat run fee is
	// one debit that apps/agents makes itself, but the per-token charges are made
	// by the metering decorator one round at a time, and without a correlation id
	// carried down to them there is no key that joins those rows back to the run
	// that caused them — so "what did this run cost" had no answer, only "what did
	// this org spend". The metered client copies it to metering.Usage.RequestID,
	// which is the field whose stated job is exactly this.
	RunID string

	// Feature names the product surface this completion serves ("translate"),
	// when one does. ATTRIBUTION and nothing else, like RunID: it never reaches
	// the gateway, and the client stamps it on the call's gen_ai span as
	// hanzo.feature, so a surface's completions can be told apart from chat on
	// the span plane. Empty is a completion no surface claims.
	Feature string

	// Actor names WHO this completion is for — "<org>/<sub>", the same audit
	// identity metering.Usage.Actor and an agent run's actor already spell this
	// way. It is ATTRIBUTION and nothing else: it never chooses the credential,
	// the balance (Payer does), or the data scope (Org does), so a caller
	// cannot move a debit by naming someone.
	//
	// It exists because a run acts FOR a person the request could not otherwise
	// state. A Slack turn reaches the runner on a detached context long after
	// the asker's own request is gone, and the only identity left on the model
	// call was the deployment's own IAM application — so the gateway recorded
	// the application as the spender and the person had no row anywhere. A run
	// therefore states its actor; a live caller has theirs read off the
	// validated identity the boundary already minted (cloud.named).
	//
	// The org half is a separate question and does not move with this: a run
	// bills the org that owns the agent, so a row may name a person in one org
	// while the ledger names the org that paid for them.
	Actor string

	// RuntimeDigest is a content digest of the agent runtime that issued this
	// completion — the build/version identity of the code making the call. It is
	// ATTRIBUTION and nothing else: it never reaches the gateway, and the client
	// stamps it on the call's gen_ai span as gen_ai.hanzo.runtime_digest so a
	// model call can be tied to the exact runtime that produced it. Empty is a
	// completion no runtime claims.
	RuntimeDigest string

	// CacheConnector names the prompt-cache connector in front of the model for
	// this completion, when one is. ATTRIBUTION and nothing else, like
	// RuntimeDigest: it never reaches the gateway, and the client stamps it on
	// the call's gen_ai span as gen_ai.hanzo.cache_connector so a cache hit can
	// be told apart from a cold call on the span plane. Empty is a completion
	// with no cache connector.
	CacheConnector string
}

// Chat roles — the OpenAI-compatible vocabulary the gateway speaks.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ChatMessage is one turn of a conversation. An assistant turn that decided to
// call tools carries ToolCalls and usually no Content; the result of one such
// call comes back as a RoleTool turn whose ToolCallID names the call it answers.
type ChatMessage struct {
	Role    string // RoleSystem | RoleUser | RoleAssistant | RoleTool
	Content string
	// ToolCalls are the calls an assistant turn asked for; only ever set on a
	// RoleAssistant message.
	ToolCalls []ToolCall
	// ToolCallID names the ToolCall this RoleTool message answers. A tool result
	// without it is a result no call claims, and the gateway refuses it.
	ToolCallID string
	// Name is the tool a RoleTool message answers for — legibility in a
	// transcript, optional on the wire.
	Name string
	// Images are pictures a RoleUser turn shows the model, each an https URL
	// or a data: URI. A turn with any crosses as content parts — its words,
	// then one image_url part per picture — which is how a vision model is
	// asked about a picture; a turn with none crosses as a plain string.
	Images []string
}

// ToolDef is one tool offered to the model: its name, the prose it decides on,
// and the JSON Schema of its arguments. Schema is passed through VERBATIM — it is
// the tool plane's own inputSchema, never a re-derivation of it here.
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage
	// Reads says the tool changes nothing: the server that offers it marks it
	// read-only (MCP readOnlyHint). False is a tool that may change something.
	Reads bool
}

// ToolCall is the model asking for one tool to run. Arguments is the raw JSON
// object it emitted, UNPARSED: it belongs to the tool that declared the schema,
// which is the only thing that knows how to read it, and a model that emits
// malformed JSON is told so rather than having its intent guessed at here.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ChatResponse mirrors the AI subsystem's chat-completion response. The token
// counts are surfaced so the metering decorator debits the EXACT inference cost
// rather than an estimate; they are zero when the gateway omits usage.
type ChatResponse struct {
	Content          string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	// Reasoning is the chain-of-thought / channel markup the upstream hid from
	// the visible reply (issue #782). It is stripped from Content so the client
	// never sees it, and it never consumes the visible max_tokens budget: the
	// gateway raises its own internal cap so reasoning finishes before the
	// answer. Empty when the completion carried no channel markup.
	Reasoning string
	// ToolCalls are the calls the model wants run before it can answer. Non-empty
	// means this is NOT the final answer: the caller runs them, appends each
	// result as a RoleTool message, and completes again.
	ToolCalls []ToolCall
	// FinishReason is why the model stopped — "stop", "tool_calls", "length", …
	// Empty when the gateway omits it.
	FinishReason string
	// Billed is what the call was charged, in nano-USD: the price the model API
	// told for it, set by the meter that charged it. Nil when nothing priced the
	// call, which is a different fact from a call priced at nothing.
	Billed *int64
}

// Text is everything this request will be charged tokens for, flattened: the
// prompt plus every message's content and every tool call it carries. The
// prepaid gate estimates against it, so a conversation reserves for what it
// actually sends rather than for a Prompt field a tool loop leaves empty.
func (r *ChatRequest) Text() string {
	if r == nil {
		return ""
	}
	if len(r.Messages) == 0 {
		return r.Prompt
	}
	var b strings.Builder
	b.WriteString(r.Prompt)
	for _, m := range r.Messages {
		b.WriteString(m.Content)
		b.WriteByte('\n')
		for _, tc := range m.ToolCalls {
			b.WriteString(tc.Name)
			b.WriteString(tc.Arguments)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// ErrUpstreamBusy marks a TRANSIENT upstream inference failure that a caller may
// safely retry — an overloaded gateway (HTTP 429/5xx) or an empty-choices /
// "Platform overloaded" response. A completion has NO side effect until it
// succeeds, so retrying it never double-charges. The AI client tags such
// failures with this sentinel (errors.Is-detectable); the agent runner tests for
// it to decide whether to retry the same model and, if still throttled, fail over
// to a reliable one. A NON-transient failure (400, auth, an unserved model) is
// never tagged, so it fails fast rather than burning retries that will repeat it.
var ErrUpstreamBusy = errors.New("upstream busy")

// EmbedRequest is the embeddings call. Inputs are embedded in order; the result is
// one vector per input, aligned by index. Org, Payer and Project mean what they
// mean on ChatRequest, so embeddings meter and observe through the same path.
type EmbedRequest struct {
	Model   string
	Inputs  []string
	Org     string
	Payer   account.Account
	Project string
}

// RerankRequest is one query against the documents to order. Org, Payer and
// Project scope it exactly as ChatRequest's do.
type RerankRequest struct {
	Model     string
	Query     string
	Documents []string
	Org       string
	Payer     account.Account
	Project   string
}

// TranscribeRequest is one recording to put into words. Org, Payer and Project
// scope it exactly as ChatRequest's do.
type TranscribeRequest struct {
	Model string
	// Audio is the recording's bytes, as stored.
	Audio []byte
	// Name is the recording's file name; its extension tells the speech
	// service which container the bytes are in.
	Name    string
	Org     string
	Payer   account.Account
	Project string
}

// ErrNoTranscriber is a transcription asked of a client with no speech path:
// the disabled stub, or a transport that cannot reach /audio/transcriptions.
var ErrNoTranscriber = errors.New("ai: this client cannot transcribe")

// Transcriber is an OPTIONAL capability a Client may ALSO implement, like
// ModelLister: speech to text through the gateway's /audio/transcriptions, on
// the same transport and credential as Embed. It answers the transcript's
// words; a recording with no speech in it answers "".
type Transcriber interface {
	Transcribe(ctx context.Context, req *TranscribeRequest) (string, error)
}

// Client is the inter-subsystem interface to AI: what a program in this process buys
// inference through.
type Client interface {
	ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	// Embed returns one vector per input text, aligned by index, from the SAME
	// gateway + credential as ChatCompletion. Embeddings therefore authenticate,
	// meter, and observe through the ONE org/project-aligned path — never a static
	// side-channel key. The EmbedRequest carries the billing scope (Org/Project)
	// exactly like ChatRequest; an empty Inputs slice returns (nil, nil).
	Embed(ctx context.Context, req *EmbedRequest) ([][]float32, error)
	// Rerank scores each document's relevance to the query, one float per
	// document aligned by index, higher being more relevant — the gateway's
	// /rerank, reached with the same credential and metered on the same path
	// as Embed. Documents is what a cross-encoder reads, so a caller passes text,
	// not ids; an empty Documents returns (nil, nil).
	Rerank(ctx context.Context, req *RerankRequest) ([]float64, error)
}

// ModelLister is an OPTIONAL capability a Client may ALSO implement: it
// enumerates the model ids the gateway currently serves (its standard
// /v1/models catalog). The agents subsystem uses it to reject a non-catalog
// model at agent create/update time with a clean 400, instead of letting the run
// surface a confusing gateway 502 for a model this gateway never served. An
// AIClient that cannot enumerate models (the disabled stub, the ZAP-RPC client)
// simply does not implement it, and callers fall back to skipping the check —
// so model validation is a best-effort UX guard, never a hard dependency.
type ModelLister interface {
	Models(ctx context.Context) ([]string, error)
}

// StreamCompleter is an OPTIONAL capability a Client may ALSO implement,
// exactly like ModelLister: the SAME chat completion, delivered as the model
// produces it. emit is called once per content delta, in order; returning an
// error from emit stops the stream (a disconnected client), which is NOT a
// failure of the completion. The returned ChatResponse carries the FULL
// accumulated content plus usage, so a caller that only wants the answer gets
// the identical value ChatCompletion would have returned — streaming is a
// delivery property, never a different result.
//
// A Client that cannot stream (the disabled stub, the ZAP-RPC client) simply
// does not implement it, and callers fall back to ChatCompletion — so token
// streaming is a progressive-delivery upgrade, never a hard dependency.
type StreamCompleter interface {
	ChatStream(ctx context.Context, req *ChatRequest, emit func(delta string) error) (*ChatResponse, error)
}
