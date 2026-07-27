// Package answer is the native answer engine: the bounded plan → search → read →
// rank → synthesize → cite → follow-up loop behind POST /v1/ask when a `mode`
// (search|news|research|deep) selects web grounding. It is a clean-room Hanzo
// implementation (NOT derived from any AGPL reference).
//
// ONE HOME, ONE DOOR. This package is the loop's only home; /v1/ask is its only
// door. `mode` is a VALUE handed to that door — "deep research" is a mode, never a
// second route. The package registers no routes of its own: clients/ask owns the
// door and delegates web modes here.
//
// THE FIVE VALUES, one home each:
//
//	plan       → plan()        → []string            (≤ maxQueries, best-effort)
//	search     → websearch.Search → []websearch.Result (in-process, keyless)
//	rank       → rank()        → []Source             (dedupe URL+host, relevance, cap)
//	read       → read()        → []Source (enriched)  (the ONE crawl, ai/object)
//	synthesize → synthesize()  → string               (streamed through the Sink)
//
// BOUNDED. ≤3 LLM calls (1 plan + 1 synthesis + 1 follow-up), ≤maxQueries search
// passes, ≤maxRead page fetches, a 90s wall clock, and a token ceiling past which
// the optional follow-up call is skipped. It is never an open agent loop.
//
// METERED ONCE. Every answer debits the resolved payer through the per-org
// ResourceMeter (Base.Bill) — the ONE revenue debit, since the in-process AI path
// runs on the binary's balance-exempt M2M identity.
package answer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/metering"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/websearch"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

const (
	// timeout bounds the whole loop so a hung upstream cannot wedge a request.
	timeout = 90 * time.Second
	// maxTotalTokens is the loop's token ceiling: once the running LLM token total
	// crosses it, the optional follow-up call is skipped. With the fixed call count
	// (≤1 plan + 1 synth + 1 follow-up) this keeps one request's cost bounded — the
	// "can't run away" guard.
	maxTotalTokens = 120_000
)

// Engine is the answer engine value: the shared Base (logger + the ONE per-org
// meter) plus the AI plane it synthesizes with and the deployment's default model.
// The mounting package constructs it per request from what it already holds — the
// engine owns no state of its own.
type Engine struct {
	cloud.Base
	AI    cloud.AIClient
	Model string
}

// Request is the answer engine's slice of the /v1/ask body. Every field is
// optional; mode selects the profile and the rest bound or override it.
type Request struct {
	Mode       string   // search|news|research|deep
	Sources    []string // @hints appended to the web query: web,news,academic,github,reddit,x
	Model      string   // override the synthesis model
	Stream     *bool    // force SSE (else Accept: text/event-stream / ?stream=1)
	Language   string   // web-search language (BCP-47-ish)
	MaxSources int
	MaxQueries int
	FollowUps  *bool // default true
	System     string
}

// Params is the fully-owned per-request plan handed to Run(): safe to use after
// the Ctx is recycled (SSE) and retained by the async meter.
type Params struct {
	q, webQuery  string
	mode         mode
	model        string   // primary synthesis model (chain head)
	fallbacks    []string // synthesis models tried, in order, after model
	language     string
	maxSources   int
	maxQueries   int
	readTop      int
	followUps    bool
	system       string
	dataOrg      string // effective org — data scope (RAG/BYO keys) on the ChatRequest
	payer        string // home org — the ledger that PAYS (the meter debits this)
	project      string // ChatRequest attribution
	projectScope string // meter Usage scope
	fee          int64  // cents debited for this answer (the single revenue charge)
	requestID    string
	clientIP     string
}

// Serve answers one web-mode /v1/ask: resolve the billing subject → GATE the
// balance → run the bounded loop, streaming the SearchEvent envelope (SSE) or
// returning it as one JSON object. The caller is already gated as a validated
// principal by the door; here we additionally resolve the payer and gate spend
// BEFORE any work, so an out-of-funds caller gets a clean 402, never a half stream.
func (e Engine) Serve(c *zip.Ctx, in Request, q string) error {
	dataOrg, _ := principal.Org(c) // gated non-empty by the door
	payer := principal.Ledger(c)
	if payer == "" {
		payer = dataOrg
	}
	capProject, capValidated := principal.ValidatedProject(c)

	m := resolveMode(in.Mode)
	fee := feeCents(m.name, m.feeCents)

	// MONEY GATE — refuse the whole request if the payer cannot cover the fee.
	if err := e.Bill.Gate(c.Context(), payer, capProject, capValidated, "web", fee); err != nil {
		return cloud.DenyResource(c, err)
	}

	// SYNTHESIS MODEL CHAIN — the mode's capable default (research/deep → a strong
	// model, search/news → a fast one), caller/env-overridable, with fallbacks so a
	// single model's outage degrades to the next available model, never a weak or
	// empty answer. models is guaranteed non-empty by synthModels.
	models := synthModels(in.Model, m, e.Model)

	// Everything the loop needs, OWNED (cloned): the SendStreamWriter callback
	// outlives the recycled Ctx, and the async meter retains these past the request.
	// principal.Org/HomeOrg/Project/ProjectScope already clone; clone the rest.
	p := Params{
		q:            q,
		webQuery:     buildQuery(q, m, in.Sources),
		mode:         m,
		model:        models[0],
		fallbacks:    models[1:],
		language:     strings.TrimSpace(in.Language),
		maxSources:   clampPositive(in.MaxSources, m.maxSources),
		maxQueries:   clampPositive(in.MaxQueries, m.maxQueries),
		readTop:      m.readTop,
		followUps:    in.FollowUps == nil || *in.FollowUps,
		system:       pickSystem(in.System, m.system),
		dataOrg:      dataOrg,
		payer:        payer,
		project:      principal.Project(c),
		projectScope: principal.ProjectScope(c),
		fee:          fee,
		requestID:    strings.Clone(strings.TrimSpace(c.Header("X-Request-Id"))),
		clientIP:     strings.Clone(cloud.ClientIP(c)),
	}

	if wantsStream(c, in) {
		setStreamHeaders(c)
		return c.SendStreamWriter(func(w *bufio.Writer) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			_, _ = w.WriteString(": ask stream open\n\n")
			_ = w.Flush()
			e.Run(ctx, p, &sseSink{w: w})
		})
	}

	ctx, cancel := context.WithTimeout(c.Context(), timeout)
	defer cancel()
	buf := &bufferSink{}
	e.Run(ctx, p, buf)
	// The web domain's answer: source-grounded prose, the deduped sources, and
	// follow-ups. figures is empty (web grounds on sources, not ledger figures);
	// domain/mode identify the grounding path. followups mirrors follow_ups for the
	// advisor contract. no-store: an answer is per-caller and must not be cached.
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{
		"answer":     buf.answer,
		"sources":    nonNilSrc(buf.srcs),
		"follow_ups": nonNilStr(buf.follow),
		"followups":  nonNilStr(buf.follow),
		"figures":    []any{},
		"domain":     "web",
		"mode":       p.mode.name,
		"model":      p.model,
	})
}

// Run is the bounded loop, parameterized by mode — the ONE code path for
// search/news/research/deep. It emits the SearchEvent envelope through out, then
// meters the caller ONCE. A failed step degrades (fewer sources, snippets instead
// of pages, an honest note) rather than aborting the stream.
func (e Engine) Run(ctx context.Context, p Params, out Sink) {
	var tok tokens

	// 1) PLAN — research/deep expand the question into focused sub-queries; search/
	// news use the single (news-biased) query. Bounded by p.maxQueries.
	queries := []string{p.webQuery}
	if p.mode.plan {
		out.status("planning", "")
		qs, u := e.plan(ctx, p)
		tok.add(u)
		if len(qs) > 0 {
			queries = qs
		}
	}

	// 2) SEARCH — run each query through the in-process native meta-search seam
	// (no HTTP loopback). Emit progress per query so the live UI shows the plan.
	var found []websearch.Result
	for _, q := range queries {
		out.status("searching", q)
		found = append(found, websearch.Search(ctx, q, p.language)...)
	}

	// 3) RANK — dedupe (one per URL and per host) and rank by query relevance
	// server-side, then cap. Off-topic hits sink before anything is fetched.
	srcs := rank(p.q, found, p.maxSources)
	out.sources(srcs)

	// 4) READ — fetch the top pages so the deeper modes ground on the PAGE, not a
	// 600-char snippet. search/news read nothing (readTop 0) and stay fast; a dead
	// crawl service degrades to the snippets and never breaks the stream.
	if p.readTop > 0 && len(srcs) > 0 {
		out.status("reading", "")
		srcs = read(ctx, srcs, p.readTop)
	}

	// 5) SYNTHESIZE — one grounded completion over the numbered sources, with
	// inline markdown citations, streamed to the client as it is produced.
	out.status("answering", "")
	answer, synth := e.synthesize(ctx, p, srcs, out.text)
	tok.add(synth)

	// 6) FOLLOW-UPS — one cheap call, skipped past the token ceiling (cost guard).
	if p.followUps && tok.total < maxTotalTokens {
		qs, fu := e.followUpQuestions(ctx, p, answer)
		tok.add(fu)
		if len(qs) > 0 {
			out.followUps(qs)
		}
	}

	// 7) DONE — terminal envelope frame with the accumulated answer + sources.
	out.done(answer, srcs)

	// 8) METER — the SINGLE revenue debit for this answer, on the caller's ledger,
	// ONLY when a real answer was synthesized (synth != nil). The internal AI calls
	// were balance-exempt (binary M2M), so this is the only charge; a model outage
	// degrades to an honest note and is NOT billed. Token counts ride along.
	if synth != nil {
		e.meter(p, tok)
	}
}

// meter records the one per-answer debit on the payer's ledger via Base.Bill. The
// amount is the mode's flat fee (a configurable policy price) set to cover the
// bounded token cost; token counts are recorded for per-scope attribution.
// MeterUsage forces User/Org to the payer, so a caller can never bill another org.
func (e Engine) meter(p Params, tok tokens) {
	e.Bill.MeterUsage(p.payer, "web", metering.Usage{
		Model:            p.mode.name,
		AmountCents:      p.fee,
		Project:          p.projectScope,
		PromptTokens:     tok.prompt,
		CompletionTokens: tok.completion,
		TotalTokens:      tok.total,
		RequestID:        p.requestID,
		ClientIP:         p.clientIP,
	})
}

// tokens accumulates LLM token usage across the loop's calls.
type tokens struct{ prompt, completion, total int }

func (t *tokens) add(r *cloud.ChatResponse) {
	if r == nil {
		return
	}
	t.prompt += r.PromptTokens
	t.completion += r.CompletionTokens
	t.total += r.TotalTokens
}

// plan asks the model to break the question into up to maxQueries focused
// web-search queries. On any failure it returns nil, so the caller falls back to
// the single original query — planning is best-effort, never a hard dependency.
func (e Engine) plan(ctx context.Context, p Params) ([]string, *cloud.ChatResponse) {
	prompt := fmt.Sprintf(
		"Break the user's question into up to %d focused web-search queries that together cover it. "+
			"Reply ONLY as compact JSON: {\"queries\":[\"...\"]}.\n\nQuestion: %s",
		p.maxQueries, p.q)
	resp := e.chat(ctx, p, p.model, prompt, nil)
	if resp == nil {
		return nil, nil
	}
	qs := parseStringList(resp.Content, "queries")
	if len(qs) > p.maxQueries {
		qs = qs[:p.maxQueries]
	}
	return qs, resp
}

// synthesize produces the grounded answer over the numbered sources, trying the
// primary model then each fallback until one returns a real completion, and emits
// the answer through emit as it is produced. A model outage — a transport error OR
// an empty completion — advances to the next capable model instead of aborting, so
// a single model being down never degrades the answer (the whole point of the
// chain). A model that emitted nothing also emitted no text frames, so failing over
// can never duplicate output. Only when EVERY model fails does it return an honest
// note (never a fabricated answer) and NIL usage, so Run bills no charge for a
// non-answer — and that note is emitted too, so a streaming client still sees it.
func (e Engine) synthesize(ctx context.Context, p Params, srcs []Source, emit func(string)) (string, *cloud.ChatResponse) {
	prompt := p.system +
		"\nToday is " + time.Now().UTC().Format("2006-01-02") + "." +
		"\n\nQuestion: " + p.q +
		"\n\nWeb sources:\n" + sourcesBlock(srcs)
	for _, model := range append([]string{p.model}, p.fallbacks...) {
		if resp := e.chat(ctx, p, model, prompt, emit); resp != nil && strings.TrimSpace(resp.Content) != "" {
			return resp.Content, resp
		}
	}
	note := "I couldn't generate an answer right now — the model is unavailable. Please try again."
	emit(note)
	return note, nil
}

// followUpQuestions asks for a few distinct next questions. Best-effort: empty on failure.
func (e Engine) followUpQuestions(ctx context.Context, p Params, answer string) ([]string, *cloud.ChatResponse) {
	prompt := "Given a question and its answer, propose 3 concise, distinct follow-up questions a curious user would ask next. " +
		"Reply ONLY as compact JSON: {\"questions\":[\"...\"]}.\n\nQuestion: " + p.q +
		"\n\nAnswer:\n" + clip(answer, 4000)
	resp := e.chat(ctx, p, p.model, prompt, nil)
	if resp == nil {
		return nil, nil
	}
	qs := parseStringList(resp.Content, "questions")
	if len(qs) > 4 {
		qs = qs[:4]
	}
	return qs, resp
}

// chat runs ONE completion with the given model, carrying the billing/data scope.
// Org is the effective (data) org, BillingOrg the payer, Project the attribution
// scope. The transport runs on the binary's M2M identity (balance-exempt); the
// revenue debit is meter(), not this call. A nil AI plane (dev/offline) yields nil,
// and every step degrades honestly. An error yields nil so synthesize can advance
// to the next model in the chain.
//
// emit is the ONE delivery seam and it binds twice, never forking the code path:
// when the AI plane implements types.StreamCompleter the model's real token deltas
// go straight to emit; when it does not, the finished completion is chunked at word
// boundaries. Either way the returned Content is the whole answer, so a streamed
// and a chunked run produce an identical `done` frame. A nil emit (plan,
// follow-ups) is a plain non-streaming completion.
func (e Engine) chat(ctx context.Context, p Params, model, prompt string, emit func(string)) *cloud.ChatResponse {
	if e.AI == nil {
		return nil
	}
	req := &cloud.ChatRequest{
		Model:      model,
		Prompt:     prompt,
		Org:        p.dataOrg,
		BillingOrg: p.payer,
		Project:    p.project,
	}
	if emit != nil {
		if sc, ok := e.AI.(types.StreamCompleter); ok {
			resp, err := sc.ChatStream(ctx, req, func(delta string) error {
				emit(delta)
				return nil
			})
			if err != nil {
				return nil
			}
			return resp
		}
	}
	resp, err := e.AI.ChatCompletion(ctx, req)
	if err != nil {
		return nil
	}
	if emit != nil && resp != nil {
		for _, chunk := range chunkText(resp.Content, maxAnswerChunk) {
			emit(chunk)
		}
	}
	return resp
}

// parseStringList leniently extracts a []string under key from a model reply that
// may be fenced or chatty: it finds the first JSON object and reads key, else falls
// back to the first bare JSON array. Non-string / blank entries are dropped.
func parseStringList(content, key string) []string {
	obj := sliceBetween(content, '{', '}')
	if obj != "" {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(obj), &m) == nil {
			if raw, ok := m[key]; ok {
				if out := decodeStrings(raw); len(out) > 0 {
					return out
				}
			}
		}
	}
	if arr := sliceBetween(content, '[', ']'); arr != "" {
		return decodeStrings(json.RawMessage(arr))
	}
	return nil
}

func decodeStrings(raw json.RawMessage) []string {
	var xs []string
	if json.Unmarshal(raw, &xs) != nil {
		return nil
	}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if t := strings.TrimSpace(x); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// sliceBetween returns the substring from the first open byte through the matching
// (last) close byte, inclusive — a tolerant extractor for a JSON blob embedded in
// prose or ``` fences. Empty when either delimiter is absent or misordered.
func sliceBetween(s string, open, closeb byte) string {
	i := strings.IndexByte(s, open)
	if i < 0 {
		return ""
	}
	j := strings.LastIndexByte(s, closeb)
	if j <= i {
		return ""
	}
	return s[i : j+1]
}
