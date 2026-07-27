package answer

// answer_test.go — proofs for the answer engine: the mode registry and its price
// policy, the server-side relevance ranking, the synthesis model chain, and the
// bounded loop's envelope. Hermetic: a fake AI plane and a no-network search, so
// nothing here dials a model, a search engine, or the crawl service.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/websearch"
	"github.com/hanzoai/cloud/types"
)

// ── relevance ranking (the server-side relevance fix) ────────────────────────

func TestRankRelevanceOrder(t *testing.T) {
	in := []websearch.Result{
		{URL: "https://random.example/foo", Title: "Best coffee in Portland", Content: "a cafe guide", Engine: "bing"},
		{URL: "https://en.wikipedia.org/wiki/Rich_Hickey", Title: "Rich Hickey - Wikipedia", Content: "Rich Hickey is the creator of Clojure.", Engine: "bing"},
		{URL: "https://clojure.org/about", Title: "About Clojure", Content: "Clojure was created by Rich Hickey.", Engine: "ddg"},
	}
	out := rank("who is Rich Hickey", in, 6)
	if len(out) != 3 {
		t.Fatalf("want 3 sources, got %d", len(out))
	}
	if !strings.Contains(out[0].Title, "Rich Hickey") {
		t.Fatalf("most relevant (Rich Hickey) should rank first, got %q", out[0].Title)
	}
	if out[len(out)-1].Title != "Best coffee in Portland" {
		t.Fatalf("off-topic result should rank last, got %q", out[len(out)-1].Title)
	}
	if out[0].Favicon == "" {
		t.Fatalf("source should carry a derived favicon")
	}
}

func TestRankDedupeURLAndHost(t *testing.T) {
	in := []websearch.Result{
		{URL: "https://example.com/a", Title: "A", Content: "x"},
		{URL: "https://example.com/a", Title: "A dup url", Content: "x"},
		{URL: "https://example.com/b", Title: "B same host", Content: "x"},
		{URL: "https://other.com/c", Title: "C", Content: "x"},
	}
	if out := rank("A B C", in, 10); len(out) != 2 {
		t.Fatalf("want 2 after URL+host dedupe, got %d: %+v", len(out), out)
	}
}

func TestRankCap(t *testing.T) {
	var in []websearch.Result
	for _, h := range []string{"a.com", "b.com", "c.com", "d.com", "e.com"} {
		in = append(in, websearch.Result{URL: "https://" + h + "/x", Title: h, Content: "term"})
	}
	if got := rank("term", in, 3); len(got) != 3 {
		t.Fatalf("cap not applied: want 3, got %d", len(got))
	}
}

func TestRelevanceScoreWeights(t *testing.T) {
	terms := queryTerms("clojure creator")
	titleHit := relevanceScore(terms, "clojure creator", "The Clojure creator", "unrelated body")
	bodyOnly := relevanceScore(terms, "clojure creator", "unrelated title", "the clojure creator wrote it")
	if titleHit <= bodyOnly {
		t.Fatalf("title hit (%d) must outweigh body-only hit (%d)", titleHit, bodyOnly)
	}
	if relevanceScore(terms, "clojure creator", "nothing here", "nothing either") != 0 {
		t.Fatalf("no-match must score 0")
	}
}

func TestQueryTermsStopwordsAndDedupe(t *testing.T) {
	got := queryTerms("Who is the creator of Clojure and the CREATOR")
	want := map[string]bool{"creator": true, "clojure": true}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected term %q in %v", g, got)
		}
	}
}

// ── pricing policy (money: bounded, configurable, per-mode) ───────────────────

func TestFeeCentsDefaultsAndOverrides(t *testing.T) {
	if got := feeCents("deep", modes["deep"].feeCents); got != 10 {
		t.Fatalf("deep default fee: want 10, got %d", got)
	}
	t.Setenv("CLOUD_ASK_FEE_CENTS_DEEP", "25")
	if got := feeCents("deep", modes["deep"].feeCents); got != 25 {
		t.Fatalf("per-mode override: want 25, got %d", got)
	}
	t.Setenv("CLOUD_ASK_FEE_CENTS", "7")
	if got := feeCents("search", modes["search"].feeCents); got != 7 {
		t.Fatalf("global override: want 7, got %d", got)
	}
	t.Setenv("CLOUD_ASK_FEE_CENTS_NEWS", "-3")
	t.Setenv("CLOUD_ASK_FEE_CENTS", "")
	if got := feeCents("news", modes["news"].feeCents); got != modes["news"].feeCents {
		t.Fatalf("invalid override must fall back to default, got %d", got)
	}
}

func TestClampPositiveBounds(t *testing.T) {
	if got := clampPositive(0, 6); got != 6 {
		t.Fatalf("zero → default: want 6, got %d", got)
	}
	if got := clampPositive(3, 6); got != 3 {
		t.Fatalf("fewer allowed: want 3, got %d", got)
	}
	if got := clampPositive(99, 6); got != 6 {
		t.Fatalf("more than ceiling clamps: want 6, got %d", got)
	}
}

// ── mode registry + query building ────────────────────────────────────────────

func TestIsModeAndResolve(t *testing.T) {
	for _, m := range []string{"search", "news", "research", "deep", "DEEP", "Research"} {
		if !IsMode(m) {
			t.Fatalf("%q must be an answer-engine mode", m)
		}
	}
	for _, m := range []string{"", "books", "figures", "bogus"} {
		if IsMode(m) {
			t.Fatalf("%q must NOT be an answer-engine mode (advisor path)", m)
		}
	}
	if resolveMode("DEEP").name != "deep" || !resolveMode("deep").plan {
		t.Fatal("deep must resolve and plan")
	}
	if resolveMode("research").maxQueries != 4 {
		t.Fatal("research maxQueries")
	}
}

// TestModeReadBudget pins the read stage's per-mode budget: the fast modes fetch
// NOTHING (snippets keep them inside a tight latency budget), the deep ones read
// pages, and no mode may exceed the hard ceiling.
func TestModeReadBudget(t *testing.T) {
	want := map[string]int{"search": 0, "news": 0, "research": 4, "deep": 6}
	for name, m := range modes {
		if m.readTop != want[name] {
			t.Fatalf("mode %q readTop = %d, want %d", name, m.readTop, want[name])
		}
		if m.readTop > maxRead {
			t.Fatalf("mode %q readTop %d exceeds the ceiling %d", name, m.readTop, maxRead)
		}
	}
}

func TestBuildQuery(t *testing.T) {
	if q := buildQuery("clojure", modes["news"], nil); !strings.Contains(q, "latest news") {
		t.Fatalf("news mode must recency-bias, got %q", q)
	}
	q := buildQuery("clojure", modes["search"], []string{"github", "bogus", "reddit"})
	if !strings.Contains(q, "github") || !strings.Contains(q, "reddit") {
		t.Fatalf("known hints must append, got %q", q)
	}
	if strings.Contains(q, "bogus") {
		t.Fatalf("unknown hint must be dropped, got %q", q)
	}
}

// ── synthesis model chain (the availability fix) ──────────────────────────────

func TestSynthModelsPerModeDefaults(t *testing.T) {
	// research/deep lead with a strong model; search/news with a fast one.
	if got := synthModels("", modes["research"], ""); got[0] != "zen5" {
		t.Fatalf("research must lead with zen5, got %v", got)
	}
	if got := synthModels("", modes["deep"], ""); got[0] != "zen5" {
		t.Fatalf("deep must lead with zen5, got %v", got)
	}
	if got := synthModels("", modes["search"], ""); got[0] != "zen5-flash" {
		t.Fatalf("search must lead with zen5-flash, got %v", got)
	}
	// every mode default carries a fallback, so a primary outage has somewhere to go.
	if got := synthModels("", modes["research"], ""); len(got) < 2 {
		t.Fatalf("research chain must carry a fallback, got %v", got)
	}
}

func TestSynthModelsCallerAndEnvOverride(t *testing.T) {
	// caller's explicit model wins outright — their single choice.
	if got := synthModels("anthropic/claude-opus-4.8", modes["research"], "zen5"); len(got) != 1 || got[0] != "anthropic/claude-opus-4.8" {
		t.Fatalf("caller model must win outright, got %v", got)
	}
	// per-mode env override heads the chain, ahead of the mode default.
	t.Setenv("CLOUD_ASK_MODEL_RESEARCH", "zen5-pro")
	if got := synthModels("", modes["research"], "zen5"); got[0] != "zen5-pro" {
		t.Fatalf("per-mode env override must head the chain, got %v", got)
	}
	// global env override applies when no per-mode override is set.
	t.Setenv("CLOUD_ASK_MODEL_RESEARCH", "")
	t.Setenv("CLOUD_ASK_MODEL", "enso")
	if got := synthModels("", modes["search"], "zen5"); got[0] != "enso" {
		t.Fatalf("global env override must head the chain, got %v", got)
	}
}

func TestSynthModelsBackstopAndDedupe(t *testing.T) {
	// the cloud-wide default is appended as a final backstop...
	got := synthModels("", modes["search"], "deepseek-v4-flash")
	if got[len(got)-1] != "deepseek-v4-flash" {
		t.Fatalf("cloud default must be the final backstop, got %v", got)
	}
	// ...and a default already present in the mode chain is not duplicated.
	for _, m := range []string{"zen5", "zen5-flash"} {
		chain := synthModels("", modes["research"], m)
		n := 0
		for _, g := range chain {
			if g == m {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("model %q must appear exactly once, chain=%v", m, chain)
		}
	}
	// never empty, even with no env override and a blank default.
	if got := synthModels("", modes["research"], ""); len(got) == 0 {
		t.Fatal("chain must never be empty")
	}
}

// ── text + parsing helpers ────────────────────────────────────────────────────

func TestChunkText(t *testing.T) {
	if got := chunkText("", 10); got != nil {
		t.Fatalf("empty → nil, got %v", got)
	}
	if got := chunkText("short", 10); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short → one chunk, got %v", got)
	}
	got := chunkText("the quick brown fox jumps over", 10)
	if len(got) < 2 {
		t.Fatalf("long text must chunk, got %v", got)
	}
	if strings.Join(strings.Fields(strings.Join(got, " ")), " ") != "the quick brown fox jumps over" {
		t.Fatalf("chunks must preserve words, got %v", got)
	}
	for _, c := range got {
		if len([]rune(c)) > 10 {
			t.Fatalf("chunk exceeds size: %q", c)
		}
	}
}

func TestParseStringList(t *testing.T) {
	cases := []struct {
		in, key string
		want    int
	}{
		{`{"queries":["a","b","c"]}`, "queries", 3},
		{"```json\n{\"questions\":[\"x\",\"y\"]}\n```", "questions", 2},
		{"Sure! Here you go:\n{\"queries\": [\"only\"]}\nHope that helps.", "queries", 1},
		{`["bare","array"]`, "queries", 2},
		{`{"queries":["ok",""," "]}`, "queries", 1},
		{"no json at all", "queries", 0},
	}
	for _, c := range cases {
		if got := parseStringList(c.in, c.key); len(got) != c.want {
			t.Fatalf("parseStringList(%q,%q): want %d, got %v", c.in, c.key, c.want, got)
		}
	}
}

func TestSourcesBlockNumbering(t *testing.T) {
	if !strings.Contains(sourcesBlock(nil), "no web sources") {
		t.Fatal("empty sources must yield the no-sources note")
	}
	b := sourcesBlock([]Source{{Title: "T1", URL: "u1", Snippet: "s1"}, {Title: "T2", URL: "u2", Snippet: "s2"}})
	if !strings.Contains(b, "[1] T1") || !strings.Contains(b, "[2] T2") {
		t.Fatalf("sources must be numbered, got %q", b)
	}
}

// ── the bounded loop (hermetic: fake AI + no-network search + no crawl) ───────

// loopAI returns plan/follow-up JSON for those prompts and a fixed answer otherwise,
// counting calls so a test can assert the loop is bounded.
type loopAI struct {
	answer string
	calls  int
}

func (f *loopAI) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	f.calls++
	switch {
	case strings.Contains(req.Prompt, `"queries"`):
		return &types.ChatResponse{Content: `{"queries":["clojure creator","rich hickey"]}`, PromptTokens: 6, CompletionTokens: 4, TotalTokens: 10}, nil
	case strings.Contains(req.Prompt, `"questions"`):
		return &types.ChatResponse{Content: `{"questions":["What is the JVM?","Why immutability?","What is an atom?"]}`, TotalTokens: 8}, nil
	default:
		return &types.ChatResponse{Content: f.answer, PromptTokens: 40, CompletionTokens: 10, TotalTokens: 50}, nil
	}
}
func (f *loopAI) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) { return nil, nil }

// streamAI is loopAI plus the optional types.StreamCompleter capability: the
// synthesis reply arrives as real per-word deltas.
type streamAI struct {
	loopAI
	deltas []string
}

func (s *streamAI) ChatStream(ctx context.Context, req *types.ChatRequest, emit func(string) error) (*types.ChatResponse, error) {
	resp, err := s.ChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	for i, w := range strings.SplitAfter(resp.Content, " ") {
		if w == "" {
			continue
		}
		s.deltas = append(s.deltas, w)
		if err := emit(w); err != nil {
			return nil, fmt.Errorf("emit delta %d: %w", i, err)
		}
	}
	return resp, nil
}

// modelAI answers only for models in ok (model→content); any other model returns a
// transport error, so a test drives synthesize's fallback chain by choosing which
// models "work". An empty-string content models an available model that returns a
// blank completion (also a skip). tried records the order models were attempted.
type modelAI struct {
	ok    map[string]string
	tried []string
}

func (m *modelAI) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	m.tried = append(m.tried, req.Model)
	if content, hit := m.ok[req.Model]; hit {
		return &types.ChatResponse{Content: content, PromptTokens: 20, CompletionTokens: 8, TotalTokens: 28}, nil
	}
	return nil, fmt.Errorf("model %q unavailable", req.Model)
}
func (m *modelAI) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) { return nil, nil }

// TestSynthesizeFallsOverToAvailableModel proves the availability fix: when the
// primary model errors AND the first fallback returns an empty completion, the loop
// advances to the next model rather than emitting the degraded note.
func TestSynthesizeFallsOverToAvailableModel(t *testing.T) {
	ai := &modelAI{ok: map[string]string{"zen5-flash": "", "deepseek-v4-flash": "A grounded, well-cited report."}}
	p := baseParams(modes["research"])
	p.model = "zen5"                                          // errors (not in ok)
	p.fallbacks = []string{"zen5-flash", "deepseek-v4-flash"} // empty, then real
	got, usage := newEngine(ai).synthesize(context.Background(), p, nil, func(string) {})
	if usage == nil {
		t.Fatal("a working fallback must yield billable usage")
	}
	if got != "A grounded, well-cited report." {
		t.Fatalf("must return the fallback's answer, got %q", got)
	}
	if want := []string{"zen5", "zen5-flash", "deepseek-v4-flash"}; strings.Join(ai.tried, ",") != strings.Join(want, ",") {
		t.Fatalf("must try the chain in order until one works: tried %v", ai.tried)
	}
}

// TestSynthesizeAllModelsDownDegradesHonestly proves the loop still degrades
// honestly — and bills nothing (nil usage) — only when EVERY model is down. The
// honest note is EMITTED too, so a streaming client is never left with no text.
func TestSynthesizeAllModelsDownDegradesHonestly(t *testing.T) {
	p := baseParams(modes["research"])
	p.model, p.fallbacks = "zen5", []string{"zen5-flash"}
	var emitted strings.Builder
	got, usage := newEngine(&modelAI{ok: map[string]string{}}).
		synthesize(context.Background(), p, nil, func(s string) { emitted.WriteString(s) })
	if usage != nil {
		t.Fatal("no model available must yield nil usage (not billed)")
	}
	if !strings.Contains(got, "unavailable") {
		t.Fatalf("must degrade to the honest note, got %q", got)
	}
	if emitted.String() != got {
		t.Fatalf("the honest note must be streamed too, emitted %q", emitted.String())
	}
}

type recSink struct {
	order  []string
	srcs   []Source
	buf    strings.Builder
	texts  []string
	follow []string
	answer string
}

func (r *recSink) status(stage, _ string) { r.order = append(r.order, "status:"+stage) }
func (r *recSink) sources(s []Source)     { r.order = append(r.order, "sources"); r.srcs = s }
func (r *recSink) text(d string) {
	r.order = append(r.order, "text")
	r.texts = append(r.texts, d)
	r.buf.WriteString(d)
}
func (r *recSink) followUps(qs []string)     { r.order = append(r.order, "follow_ups"); r.follow = qs }
func (r *recSink) done(a string, _ []Source) { r.order = append(r.order, "done"); r.answer = a }

// noNetworkSearch points bing at a local server returning empty HTML, so
// websearch.Search resolves to zero sources instantly (the loop degrades cleanly).
func noNetworkSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body></body></html>"))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WEBSEARCH_BING_URL", srv.URL)
	t.Setenv("WEBSEARCH_ENGINES", "bing")
}

// newEngine builds the engine over a fake AI plane with a ZERO Base: Run()/meter()
// never touch Log, and a nil Bill makes MeterUsage a safe no-op — the loop's
// envelope is exercised without a billing backend.
func newEngine(ai types.AIClient) Engine { return Engine{AI: ai, Model: "test-model"} }

func baseParams(m mode) Params {
	return Params{
		q: "who created clojure and why", webQuery: "who created clojure and why",
		mode: m, model: "test-model",
		maxSources: m.maxSources, maxQueries: m.maxQueries, readTop: m.readTop,
		followUps: true, system: m.system, payer: "acme", dataOrg: "acme",
	}
}

func TestRunResearchEnvelopeAndBoundedCalls(t *testing.T) {
	noNetworkSearch(t)
	ai := &loopAI{answer: "Clojure was created by Rich Hickey to bring practical, immutable functional programming to the JVM."}
	r := &recSink{}
	newEngine(ai).Run(context.Background(), baseParams(modes["research"]), r)

	joined := strings.Join(r.order, ",")
	for _, want := range []string{"status:planning", "status:searching", "sources", "status:answering", "text", "follow_ups", "done"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in envelope order: %s", want, joined)
		}
	}
	if r.order[len(r.order)-1] != "done" {
		t.Fatalf("done must be terminal, order: %s", joined)
	}
	if r.answer != ai.answer {
		t.Fatalf("answer mismatch: %q", r.answer)
	}
	if strings.TrimSpace(r.buf.String()) != strings.TrimSpace(ai.answer) {
		t.Fatalf("streamed text must reconstruct the answer, got %q", r.buf.String())
	}
	if len(r.follow) != 3 {
		t.Fatalf("want 3 follow-ups, got %v", r.follow)
	}
	if ai.calls != 3 {
		t.Fatalf("research must make exactly 3 LLM calls (plan+synth+followup), got %d", ai.calls)
	}
}

func TestRunSearchModeSkipsPlanning(t *testing.T) {
	noNetworkSearch(t)
	ai := &loopAI{answer: "A grounded answer."}
	r := &recSink{}
	newEngine(ai).Run(context.Background(), baseParams(modes["search"]), r)
	if strings.Contains(strings.Join(r.order, ","), "status:planning") {
		t.Fatal("search mode must NOT plan")
	}
	if ai.calls != 2 {
		t.Fatalf("search must make 2 LLM calls (synth+followup), got %d", ai.calls)
	}
}

func TestRunFollowUpsDisabled(t *testing.T) {
	noNetworkSearch(t)
	ai := &loopAI{answer: "A."}
	p := baseParams(modes["search"])
	p.followUps = false
	r := &recSink{}
	newEngine(ai).Run(context.Background(), p, r)
	if strings.Contains(strings.Join(r.order, ","), "follow_ups") {
		t.Fatal("follow-ups disabled must emit none")
	}
	if ai.calls != 1 {
		t.Fatalf("with follow-ups off, search makes 1 LLM call (synth), got %d", ai.calls)
	}
}

// TestRunStreamedAndChunkedAgree is the token-streaming proof: an AI plane that
// implements StreamCompleter delivers the model's REAL deltas, one that does not
// delivers the same answer chunked at word boundaries — and the terminal `done`
// answer is IDENTICAL either way. Streaming is a delivery property, never a
// different result.
func TestRunStreamedAndChunkedAgree(t *testing.T) {
	noNetworkSearch(t)
	const text = "Clojure was created by Rich Hickey to bring practical, immutable functional programming to the JVM."

	streamed := &recSink{}
	sai := &streamAI{loopAI: loopAI{answer: text}}
	newEngine(sai).Run(context.Background(), baseParams(modes["search"]), streamed)

	chunked := &recSink{}
	newEngine(&loopAI{answer: text}).Run(context.Background(), baseParams(modes["search"]), chunked)

	if streamed.answer != chunked.answer || streamed.answer != text {
		t.Fatalf("done.answer must be identical: streamed=%q chunked=%q", streamed.answer, chunked.answer)
	}
	if streamed.buf.String() != text {
		t.Fatalf("streamed deltas must reconstruct the answer exactly, got %q", streamed.buf.String())
	}
	if strings.Join(streamed.texts, "") != strings.Join(sai.deltas, "") {
		t.Fatalf("text frames must be the model's own deltas, got %v", streamed.texts)
	}
	if len(streamed.texts) <= len(chunked.texts) {
		t.Fatalf("real token streaming must be finer-grained than post-hoc chunking: %d vs %d",
			len(streamed.texts), len(chunked.texts))
	}
}

func TestMeterNilBillNoPanic(t *testing.T) {
	newEngine(&loopAI{}).meter(baseParams(modes["search"]), tokens{prompt: 1, completion: 2, total: 3})
}
