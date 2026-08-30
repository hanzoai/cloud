// Package answer is a researched answer to a hard question, with its sources
// cited.
//
// It is the native answer engine: the bounded plan → search → read → rank →
// synthesize → cite → follow-up loop behind POST /v1/ask when a `mode`
// (search|news|research|deep) selects web grounding. It is a clean-room Hanzo
// implementation (NOT derived from any AGPL reference).
//
// ONE HOME, ONE ENDPOINT. This package is the loop's only home; /v1/ask is its
// only endpoint. `mode` is a VALUE handed to that endpoint — "deep research" is a
// mode, never a second route. The package registers no routes of its own:
// clients/ask owns the endpoint and delegates web modes here.
//
// THE SIX VALUES, one home each:
//
//	plan       → plan()        → []topic              (≤5 topics × 3–5 todos, best-effort)
//	search     → websearch.Search → []websearch.Result (in-process, keyless)
//	rank       → rank()        → []Source             (dedupe URL, host cap, relevance)
//	read       → read()        → []Source (enriched)  (the ONE crawl, apps/crawl)
//	survey     → survey()      → []Source             (search+read applied to a plan, ITERATED)
//	synthesize → synthesize()  → string               (streamed through the Sink)
//
// BOUNDED. The fast modes make ≤3 LLM calls (1 plan + 1 synthesis + 1 follow-up)
// over one gathering pass. A survey adds ONE decision call per extra round (two on
// a round the model answers unreadably), itself bounded by mode.rounds (hard-capped
// at maxRounds), mode.deadline, mode.tokenCeiling, saturation, and the client still
// being connected. It is never an open agent loop.
//
// GROUNDED, AND ONLY GROUNDED. Two properties hold against pages we did not
// author: the synthesis prompt fences every source with a per-request nonce, so a
// crawled page cannot print itself a source number the report then cites; and every
// markdown link in the answer is checked against the gathered set before it reaches
// the client, so a citation always points at a page THIS request fetched. Neither
// is a prompt instruction — a prompt is advice to a model, these are properties of
// the text that leaves the process. See ground.go.
//
// ONE REVENUE DEBIT, NOT ONE DEBIT. Every answer debits the resolved payer once
// through the per-org Meter (Base.Bill): the mode's flat fee, which is the
// product price. That is the only REVENUE charge — but not the only charge. The AI
// plane this engine is handed is itself metered (build.go wraps it in
// meteredAIClient), so each internal completion also debits the payer per token
// against the same balance. Which layer should price /v1/ask is an open decision,
// recorded here rather than claimed away.
package answer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
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
	q, webQuery string
	mode        mode
	model       string   // primary synthesis model (chain head)
	fallbacks   []string // synthesis models tried, in order, after model
	language    string
	maxSources  int
	maxQueries  int
	readTop     int
	// rounds is the survey's round budget: 0 is a SINGLE gathering pass (the fast
	// modes, unchanged), >0 iterates. hostCap is how many pages one host may
	// contribute to the ranked set. deadline and tokenCeiling are the wall clock
	// and the token spend this request may not cross — both per-mode, because a
	// research pass legitimately costs more than a search and one global constant
	// had to be sized for the cheaper of the two.
	rounds       int
	hostCap      int
	deadline     time.Duration
	tokenCeiling int
	followUps    bool
	system       string
	dataOrg      string          // effective org — data scope (RAG/BYO keys) on the ChatRequest
	payer        account.Account // the ADDRESS that PAYS: Org() the books, Subject() the account in them
	project      string          // ChatRequest attribution
	projectScope string          // meter Usage scope
	fee          int64           // cents debited for this answer (the single revenue charge)
	requestID    string
	clientIP     string
}

// Serve answers one web-mode /v1/ask: resolve the billing subject → GATE the
// balance → run the bounded loop, streaming the SearchEvent envelope (SSE) or
// returning it as one JSON object. The caller is already gated as a validated
// principal by the endpoint; here we additionally resolve the payer and gate spend
// BEFORE any work, so an out-of-funds caller gets a clean 402, never a half stream.
func (e Engine) Serve(c *zip.Ctx, in Request, q string) error {
	dataOrg, _ := principal.Org(c) // gated non-empty by the endpoint
	payer := principal.Payer(c)
	if payer.Zero() {
		payer = account.PayerOf("", dataOrg)
	}
	capProject, capValidated := principal.ValidatedProject(c)

	m := resolveMode(in.Mode)
	fee := feeCents(m.name, m.feeCents)

	// MONEY GATE — refuse the whole request if the payer cannot cover the fee.
	if err := e.Bill.Authorize(c.Context(), payer, capProject, capValidated, "web", fee); err != nil {
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
		rounds:       min(m.rounds, maxRounds), // NOT clampPositive: 0 rounds is a single pass, not "unset"
		hostCap:      m.hostCap,
		deadline:     m.deadline,
		tokenCeiling: m.tokenCeiling,
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
		// The caller, resolved while the request still exists. The callback below
		// runs after this handler returns and the Ctx is recycled, so anything read
		// off it there is another request's bytes — which is why the detach is
		// correct. But a detached context carries no identity, and the loop inside
		// spends real money: it searches (paid engines) and renders pages (a browser
		// pod). Without this the streaming answer bought both for free while the
		// identical non-streaming request paid, which is money as a property of the
		// transport. See cloud.Detach.
		caller := cloud.Detach(context.Background(), c)
		return c.SendStreamWriter(func(w *bufio.Writer) {
			ctx, cancel := context.WithTimeout(caller, p.deadline)
			defer cancel()
			_, _ = w.WriteString(": ask stream open\n\n")
			_ = w.Flush()
			e.Run(ctx, p, &sseSink{w: w})
		})
	}

	ctx, cancel := context.WithTimeout(c.Context(), p.deadline)
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

// Report is one researched answer held as a VALUE: the grounded prose and the
// sources it cites. It carries what Serve writes as JSON minus the two keys that
// only mean anything to the advisor contract — `figures`, which the web domain
// leaves empty by construction, and `domain`, which is always "web" here.
type Report struct {
	// Answer is the grounded prose, with inline markdown citations. Every link in
	// it points at a page in Sources: the citation check runs on the text before
	// it leaves the engine, so a cited URL is one THIS call fetched.
	Answer string `json:"answer"`
	// Sources are the pages the answer was written from, deduplicated and ranked.
	// Always an array, never null.
	Sources []Source `json:"sources"`
	// FollowUps are the questions worth asking next. Best-effort — an empty list
	// is a normal outcome, not a fault.
	FollowUps []string `json:"follow_ups"`
	// Mode is the profile that ran: search, news, research or deep.
	Mode string `json:"mode"`
	// Model is the model that synthesized the answer.
	Model string `json:"model"`
}

// Answer runs the SAME bounded loop Serve runs and hands back its outcome as a
// value instead of writing it to a response.
//
// It exists because the loop's only entry point was an HTTP handler, and a handler
// is the one shape an agent cannot reach: a typed op — and so an MCP tool, a CLI
// command and an SDK method — is invoked with a context and no request at all.
// So the engine that already separated Run (transport-free) from Serve (HTTP)
// gains its second transport-free entry point rather than a second engine. Serve
// keeps streaming; this returns one value; Run is still the only loop.
//
// The identity facts Serve reads off the request are read off the CONTEXT here,
// which is where cloud.Bridge parks the server-minted ones. There is no ledger
// claim on a context, so the caller's own org pays — the same fallback Serve
// takes when no billing org was minted.
func (e Engine) Answer(ctx context.Context, in Request, q string) (*Report, error) {
	org, _ := principal.OrgFrom(ctx)
	project := principal.ProjectFrom(ctx)
	// PayerFrom is the context-side twin of the request-side Payer, so a call that
	// arrives as a typed op resolves the SAME address a call over HTTP does. Falling
	// back to the caller's own org keeps the transport-free path billable when the
	// boundary minted no ledger.
	payer := principal.PayerFrom(ctx)
	if payer.Zero() {
		payer = account.PayerOf("", org)
	}

	m := resolveMode(in.Mode)
	fee := feeCents(m.name, m.feeCents)

	// MONEY GATE — the same refusal Serve makes, before any work, so an
	// out-of-funds caller is told so rather than handed a half answer.
	capValidated := principal.ValidatedFrom(ctx) && !principal.IsDefaultProject(project)
	if err := e.Bill.Authorize(ctx, payer, project, capValidated, "web", fee); err != nil {
		return nil, err
	}

	models := synthModels(in.Model, m, e.Model)
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
		rounds:       min(m.rounds, maxRounds),
		hostCap:      m.hostCap,
		deadline:     m.deadline,
		tokenCeiling: m.tokenCeiling,
		followUps:    in.FollowUps == nil || *in.FollowUps,
		system:       pickSystem(in.System, m.system),
		dataOrg:      org,
		payer:        payer,
		project:      project,
		projectScope: project,
		fee:          fee,
	}

	rctx, cancel := context.WithTimeout(ctx, p.deadline)
	defer cancel()
	buf := &bufferSink{}
	e.Run(rctx, p, buf)

	return &Report{
		Answer:    buf.answer,
		Sources:   nonNilSrc(buf.srcs),
		FollowUps: nonNilStr(buf.follow),
		Mode:      p.mode.name,
		Model:     p.model,
	}, nil
}

// Run is the bounded loop, parameterized by mode — the ONE code path for
// search/news/research/deep. It emits the SearchEvent envelope through out, then
// meters the caller ONCE. A failed step degrades (fewer sources, snippets instead
// of pages, an honest note) rather than aborting the stream.
func (e Engine) Run(ctx context.Context, p Params, out Sink) {
	var tok tokens

	// 1) PLAN — research expands the question into titled topics with concrete
	// todos; search/news carry the single (news-biased) query as a one-topic plan,
	// so the survey below has exactly ONE shape to consume. Best-effort: a failed
	// plan call falls back to that same seed.
	plan := []topic{{Title: p.q, Todos: []string{p.webQuery}}}
	if p.mode.plan {
		out.status("planning", "")
		got, u := e.plan(ctx, p)
		tok.add(u)
		if len(got) > 0 {
			plan = got
			// The plan is what makes a three-minute wait legible. "Origins ·
			// Design rationale · Reception" tells the reader what the engine is
			// working on; a bare `planning` frame tells them only that it is busy.
			out.status("planning", titles(plan))
		}
	}

	// 2) SURVEY — search and read applied to the plan and iterated under the mode's
	// bounds. rounds==0 is the single pass the fast modes always did; rounds>0 is
	// deep research. ONE code path, parameterized — never a second engine.
	//
	// The gather gets a FRACTION of the request's clock, not all of it. Synthesis
	// runs on this same ctx and is the part the user actually receives: a survey
	// allowed to spend the last millisecond would hand a full corpus to a
	// completion that cannot start, and the answer would be "the model is
	// unavailable" — the exact outcome the deadline exists to prevent.
	gctx, stopGather := gather(ctx)
	srcs, stok := e.survey(gctx, p, plan, tok.total, out)
	stopGather()
	tok.merge(stok)

	// A client that hung up during the gather gets no further work: the calls that
	// remain would produce an answer nobody receives, and billing for an answer
	// nobody received is billing for nothing.
	if !out.alive() {
		return
	}

	// 3) SYNTHESIZE — one grounded completion over the numbered sources, with
	// inline markdown citations, streamed to the client as it is produced.
	out.status("answering", "")
	answer, synth := e.synthesize(ctx, p, srcs, out.text)
	tok.add(synth)

	// 4) FOLLOW-UPS — one cheap call, skipped past the token ceiling (cost guard).
	if p.followUps && tok.total < p.tokenCeiling {
		qs, fu := e.followUpQuestions(ctx, p, answer)
		tok.add(fu)
		if len(qs) > 0 {
			out.followUps(qs)
		}
	}

	// 5) DONE — terminal envelope frame with the accumulated answer + sources.
	out.done(answer, srcs)

	// 6) METER — the SINGLE revenue debit for this answer, on the caller's ledger,
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
// Record forces User/Org to the payer, so a caller can never bill another org.
func (e Engine) meter(p Params, tok tokens) {
	e.Bill.Record(p.payer, "web", metering.Usage{
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

// gatherShare is the fraction (in tenths) of the request's remaining wall clock
// the survey may spend. The rest is synthesis' — it is the only stage whose output
// the caller actually reads, and it cannot borrow time the gather already spent.
const gatherShare = 7

// gather derives the survey's context from the request's: the same cancellation,
// a shorter deadline. A parent with no deadline (a test, a non-timed caller)
// yields a plain cancellable child — there is no clock to divide.
func gather(ctx context.Context) (context.Context, context.CancelFunc) {
	d, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	left := time.Until(d)
	if left <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, left*gatherShare/10)
}

// titles renders the plan's topic headings as one status detail — what the engine
// is about to research, in the reader's words rather than the loop's.
func titles(plan []topic) string {
	out := make([]string, 0, len(plan))
	for _, t := range plan {
		if s := strings.TrimSpace(oneLine(t.Title)); s != "" {
			out = append(out, s)
		}
	}
	return clip(strings.Join(out, " · "), maxPlanDetail)
}

// maxPlanDetail bounds the plan headline on the wire: a legible line, not the
// whole plan re-serialized into a status frame.
const maxPlanDetail = 200

// warn logs a degradation. Log is nil on some construction paths (and in every
// test), and a nil deref inside an error path would turn a contained failure into
// a process kill at the worst possible moment.
func (e Engine) warn(msg string, kv ...any) {
	if e.Log != nil {
		e.Log.Warn(msg, kv...)
	}
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

// merge folds a sub-loop's accumulated usage in, so the one debit still prices
// every call the request made.
func (t *tokens) merge(o tokens) {
	t.prompt += o.prompt
	t.completion += o.completion
	t.total += o.total
}

// topic is one strand of the research plan: what to establish, and the concrete
// todos that establish it. The plan is carried VERBATIM into every survey round's
// decision prompt, which is what keeps a long gather on the question instead of
// drifting into whatever the last page happened to be about.
type topic struct {
	Title string   `json:"title"`
	Todos []string `json:"todos"`
}

// plan asks the model to break the question into a few titled research topics
// with concrete todos. On any failure it returns nil, so the caller falls back to
// the single original query — planning is best-effort, never a hard dependency.
func (e Engine) plan(ctx context.Context, p Params) ([]topic, *cloud.ChatResponse) {
	prompt := fmt.Sprintf(
		"Break the question into 1–%d research topics, each with 3–5 concrete todos "+
			"(each todo phrased as a web-search query). "+
			"Reply ONLY as compact JSON: {\"plan\":[{\"title\":\"...\",\"todos\":[\"...\"]}]}.\n\nQuestion: %s",
		maxTopics, p.q)
	resp := e.chat(ctx, p, p.model, prompt, nil)
	if resp == nil {
		return nil, nil
	}
	got := parsePlan(resp.Content)
	if len(got) > maxTopics {
		got = got[:maxTopics]
	}
	return got, resp
}

// maxTopics bounds the plan's breadth. Five strands is as wide as a bounded
// survey can actually cover; more only dilutes the round budget.
const maxTopics = 5

// parsePlan leniently reads the plan out of a reply that may be fenced or chatty.
// It also accepts the flat {"queries":[...]} shape — one topic per query — so a
// model that answers in the older form still produces a usable plan rather than
// none.
func parsePlan(content string) []topic {
	if obj := sliceBetween(content, '{', '}'); obj != "" {
		var wrapper struct {
			Plan []topic `json:"plan"`
		}
		if json.Unmarshal([]byte(obj), &wrapper) == nil {
			out := make([]topic, 0, len(wrapper.Plan))
			for _, t := range wrapper.Plan {
				todos := trimAll(t.Todos)
				if len(todos) == 0 {
					continue
				}
				title := strings.TrimSpace(t.Title)
				if title == "" {
					title = todos[0]
				}
				out = append(out, topic{Title: title, Todos: todos})
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	var out []topic
	for _, q := range parseStringList(content, "queries") {
		out = append(out, topic{Title: q, Todos: []string{q}})
	}
	return out
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
	// The fence and the allow-set are the two halves of grounding (ground.go): what
	// counts as a source, and what counts as a citation. Both are derived once, per
	// request, and both are enforced on the text rather than asked of the model.
	fence := nonce()
	allow := cited(srcs)
	prompt := p.system +
		"\nToday is " + time.Now().UTC().Format("2006-01-02") + "." +
		"\n\n" + fenceRule(fence) +
		"\n\nQuestion: " + p.q +
		"\n\nWeb sources:\n" + sourcesBlock(srcs, fence)
	// The joiner holds a markdown link back until its closing paren arrives, so a
	// citation never renders as raw `[title](htt` mid-stream — and, holding it
	// whole, can apply the same citation check the finished answer gets. It wraps
	// emit HERE and nowhere else: it is a delivery property of the answer text.
	j := &joiner{emit: emit, allow: allow}
	for _, model := range append([]string{p.model}, p.fallbacks...) {
		if resp := e.chat(ctx, p, model, prompt, j.write); resp != nil && strings.TrimSpace(resp.Content) != "" {
			j.flush()
			return cite(resp.Content, allow), resp
		}
		// A model that failed mid-link must not leak its half-frame into the next
		// model's stream; its Content was discarded, so its buffer is too.
		j.reset()
	}
	note := "I couldn't generate an answer right now — the model is unavailable. Please try again."
	emit(note)
	return note, nil
}

// followUpQuestions asks for a few distinct next questions. Best-effort: empty on failure.
func (e Engine) followUpQuestions(ctx context.Context, p Params, answer string) ([]string, *cloud.ChatResponse) {
	prompt := "Given a question and its answer, propose 3 to 5 concise, distinct follow-up questions a curious user would ask next. " +
		"Reply ONLY as compact JSON: {\"questions\":[\"...\"]}.\n\nQuestion: " + p.q +
		"\n\nAnswer:\n" + clip(answer, 4000)
	resp := e.chat(ctx, p, p.model, prompt, nil)
	if resp == nil {
		return nil, nil
	}
	qs := parseStringList(resp.Content, "questions")
	if len(qs) > maxFollowUps {
		qs = qs[:maxFollowUps]
	}
	return qs, resp
}

// maxFollowUps caps the out-of-band next-question list at five.
const maxFollowUps = 5

// chat runs ONE completion with the given model, carrying the billing/data scope.
// Org is the effective (data) org, BillingOrg the payer, Project the attribution
// scope. The transport runs on the binary's M2M identity (balance-exempt); the
// revenue debit is meter(), not this call. A nil AI plane (dev/offline) yields nil,
// and every step degrades honestly. An error yields nil so synthesize can advance
// to the next model in the chain.
//
// emit is the ONE delivery client and it binds twice, never forking the code path:
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
		BillingOrg: p.payer.Subject(),
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
		e.warn("answer: completion failed (falling through)", "model", model, "err", err)
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
