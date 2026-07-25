package ask

// web.go — the WEB grounding domain of the advisor: the native agentic
// search/deep-research loop behind /v1/ask when a web `mode` is set. It is a
// clean-room Hanzo implementation of the answer-engine capability (NOT derived from
// any AGPL reference): plan → in-process web search → rank → synthesize → cite →
// follow up. The loop is BOUNDED (≤3 LLM calls + a capped set of web-search passes,
// never an open agent loop) and every web answer METERS the resolved caller through
// the advisor's own per-org ResourceMeter (Base.Bill) — the ONE revenue debit,
// since the in-process deps.AI path runs on the binary's balance-exempt M2M
// identity. This closes the advisor's money gap for the web domain: the figure
// (books) path narrates for free today, but a web answer does real, billable work.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/metering"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/websearch"
	"github.com/zap-proto/zip"
)

const (
	// webTimeout bounds the whole loop so a hung upstream cannot wedge a request.
	webTimeout = 90 * time.Second
	// maxTotalTokens is the loop's token ceiling: once the running LLM token total
	// crosses it, the optional follow-up call is skipped. With the fixed call count
	// (≤1 plan + 1 synth + 1 follow-up) this keeps one request's cost bounded — the
	// "can't run away" guard.
	maxTotalTokens = 120_000
)

// serveWeb answers a web-mode /v1/ask: resolve the billing subject → GATE the
// balance → run the bounded agentic loop, streaming the SearchEvent envelope (SSE)
// or returning it as one JSON object. The caller is already gated as a validated
// principal by askHandler; here we additionally resolve the payer and gate spend
// BEFORE any work, so an out-of-funds caller gets a clean 402, never a half stream.
func serveWeb(s *cloud.Service[*state], c *zip.Ctx, in AskRequest, q string) error {
	dataOrg, _ := principal.Org(c) // gated non-empty at askHandler entry
	payer := principal.HomeOrg(c)
	if payer == "" {
		payer = dataOrg
	}
	capProject, capValidated := principal.ValidatedProject(c)

	m := resolveWebMode(in.Mode)
	fee := feeCents(m.name, m.feeCents)

	// MONEY GATE — refuse the whole request if the payer cannot cover the fee.
	if err := s.Bill.Gate(c.Context(), payer, capProject, capValidated, "web", fee); err != nil {
		return cloud.DenyResource(c, err)
	}

	// Everything the loop needs, OWNED (cloned): the SendStreamWriter callback
	// outlives the recycled Ctx, and the async meter retains these past the request.
	// principal.Org/HomeOrg/Project/ProjectScope already clone; clone the rest.
	p := webParams{
		q:            q,
		webQuery:     buildWebQuery(q, m, in.Sources),
		mode:         m,
		model:        pickModel(in.Model, s.State.model),
		language:     strings.TrimSpace(in.Language),
		maxSources:   clampPositive(in.MaxSources, m.maxSources),
		maxQueries:   clampPositive(in.MaxQueries, m.maxQueries),
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
			ctx, cancel := context.WithTimeout(context.Background(), webTimeout)
			defer cancel()
			_, _ = w.WriteString(": ask stream open\n\n")
			_ = w.Flush()
			runWeb(s, ctx, p, &sseSink{w: w})
		})
	}

	ctx, cancel := context.WithTimeout(c.Context(), webTimeout)
	defer cancel()
	buf := &bufferSink{}
	runWeb(s, ctx, p, buf)
	// The web domain's answer: source-grounded prose, the deduped sources, and
	// follow-ups. figures is empty (web grounds on sources, not ledger figures);
	// domain/mode identify the grounding path. followups mirrors follow_ups for the
	// advisor contract. no-store: an answer is per-caller and must not be cached.
	return askJSON(c, map[string]any{
		"answer":     buf.answer,
		"sources":    nonNilSrc(buf.srcs),
		"follow_ups": nonNilStr(buf.follow),
		"followups":  nonNilStr(buf.follow),
		"figures":    []Fact{},
		"domain":     "web",
		"mode":       p.mode.name,
		"model":      p.model,
	})
}

// webParams is the fully-owned per-request plan handed to runWeb(): safe to use
// after the Ctx is recycled (SSE) and retained by the async meter.
type webParams struct {
	q, webQuery  string
	mode         webMode
	model        string
	language     string
	maxSources   int
	maxQueries   int
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

// runWeb is the bounded agentic loop, parameterized by mode — the ONE code path for
// search/news/research/deep. It emits the SearchEvent envelope through sink, then
// meters the caller ONCE. A failed step degrades (fewer sources, an honest note)
// rather than aborting the stream.
func runWeb(s *cloud.Service[*state], ctx context.Context, p webParams, out sink) {
	var tok tokens

	// 1) PLAN — research/deep expand the question into focused sub-queries; search/
	// news use the single (news-biased) query. Bounded by p.maxQueries.
	queries := []string{p.webQuery}
	if p.mode.plan {
		out.status("planning", "")
		qs, u := planQueries(s, ctx, p)
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
	// server-side, then cap. This is the relevance fix: off-topic hits sink.
	srcs := rankSources(p.q, found, p.maxSources)
	out.sources(srcs)

	// 4) SYNTHESIZE — one grounded completion over the numbered sources, with inline
	// markdown citations. Streamed to the client in progressive chunks (the
	// in-process AI client returns a whole completion; chunking gives the UI a live
	// feel without a token-stream dependency on the shared AI interface).
	out.status("answering", "")
	answer, synth := synthesize(s, ctx, p, srcs)
	tok.add(synth)
	for _, chunk := range chunkText(answer, maxAnswerChunk) {
		out.text(chunk)
	}

	// 5) FOLLOW-UPS — one cheap call, skipped past the token ceiling (cost guard).
	if p.followUps && tok.total < maxTotalTokens {
		qs, fu := webFollowUps(s, ctx, p, answer)
		tok.add(fu)
		if len(qs) > 0 {
			out.followUps(qs)
		}
	}

	// 6) DONE — terminal envelope frame with the accumulated answer + sources.
	out.done(answer, srcs)

	// 7) METER — the SINGLE revenue debit for this answer, on the caller's ledger,
	// ONLY when a real answer was synthesized (synth != nil). The internal deps.AI
	// calls were balance-exempt (binary M2M), so this is the only charge; a model
	// outage degrades to an honest note and is NOT billed. Token counts ride along.
	if synth != nil {
		webMeter(s, p, tok)
	}
}

// webMeter records the one per-answer debit on the payer's ledger via Base.Bill.
// The amount is the mode's flat fee (a configurable policy price) set to cover the
// bounded token cost; token counts are recorded for per-scope attribution.
// MeterUsage forces User/Org to the payer, so a caller can never bill another org.
func webMeter(s *cloud.Service[*state], p webParams, tok tokens) {
	s.Bill.MeterUsage(p.payer, "web", metering.Usage{
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

// planQueries asks the model to break the question into up to maxQueries focused
// web-search queries. On any failure it returns nil, so the caller falls back to
// the single original query — planning is best-effort, never a hard dependency.
func planQueries(s *cloud.Service[*state], ctx context.Context, p webParams) ([]string, *cloud.ChatResponse) {
	prompt := fmt.Sprintf(
		"Break the user's question into up to %d focused web-search queries that together cover it. "+
			"Reply ONLY as compact JSON: {\"queries\":[\"...\"]}.\n\nQuestion: %s",
		p.maxQueries, p.q)
	resp := chat(s, ctx, p, prompt)
	if resp == nil {
		return nil, nil
	}
	qs := parseStringList(resp.Content, "queries")
	if len(qs) > p.maxQueries {
		qs = qs[:p.maxQueries]
	}
	return qs, resp
}

// synthesize produces the grounded answer over the numbered sources. On failure it
// returns an honest degraded message (never a fabricated answer) and nil usage.
func synthesize(s *cloud.Service[*state], ctx context.Context, p webParams, srcs []webSource) (string, *cloud.ChatResponse) {
	prompt := p.system +
		"\nToday is " + time.Now().UTC().Format("2006-01-02") + "." +
		"\n\nQuestion: " + p.q +
		"\n\nWeb sources:\n" + webSourcesBlock(srcs)
	resp := chat(s, ctx, p, prompt)
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return "I couldn't generate an answer right now — the model is unavailable. Please try again.", resp
	}
	return resp.Content, resp
}

// webFollowUps asks for a few distinct next questions. Best-effort: empty on failure.
func webFollowUps(s *cloud.Service[*state], ctx context.Context, p webParams, answer string) ([]string, *cloud.ChatResponse) {
	prompt := "Given a question and its answer, propose 3 concise, distinct follow-up questions a curious user would ask next. " +
		"Reply ONLY as compact JSON: {\"questions\":[\"...\"]}.\n\nQuestion: " + p.q +
		"\n\nAnswer:\n" + clip(answer, 4000)
	resp := chat(s, ctx, p, prompt)
	if resp == nil {
		return nil, nil
	}
	qs := parseStringList(resp.Content, "questions")
	if len(qs) > 4 {
		qs = qs[:4]
	}
	return qs, resp
}

// chat runs one in-process completion carrying the billing/data scope. Org is the
// effective (data) org, BillingOrg the payer, Project the attribution scope. The
// transport runs on the binary's M2M identity (balance-exempt); the revenue debit
// is webMeter(), not this call. A nil ai plane (dev/offline) yields nil, and every
// step degrades honestly. Empty content or an error also yields nil.
func chat(s *cloud.Service[*state], ctx context.Context, p webParams, prompt string) *cloud.ChatResponse {
	if s.State.ai == nil {
		return nil
	}
	resp, err := s.State.ai.ChatCompletion(ctx, &cloud.ChatRequest{
		Model:      p.model,
		Prompt:     prompt,
		Org:        p.dataOrg,
		BillingOrg: p.payer,
		Project:    p.project,
	})
	if err != nil {
		return nil
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
