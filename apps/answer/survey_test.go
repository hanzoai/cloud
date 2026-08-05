package answer

// survey_test.go — proofs for the one value the deep-research port adds: search
// and read applied to a plan and ITERATED under a bound.
//
// The three properties that make an iterated gather work, and every bound that
// stops it, are asserted here: rounds==0 is the single pass unchanged; a round's
// prose is discarded and only its move is read; the plan is carried verbatim into
// every round; `sources` is re-emitted as a cumulative snapshot; and each of
// rounds / deadline / tokenCeiling / saturation / model-said-done exits cleanly
// with the full frame sequence intact.
//
// Hermetic: a scripted AI plane, a per-query search stub on loopback, and the
// swapped crawl seam. Nothing here dials a model, a search engine, or a page.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/types"
)

// ── harness ───────────────────────────────────────────────────────────────────

// searchStub serves a Bing result page keyed by the `q` the engine asked for, so
// a test can give each round of a survey its own findings. A query with no entry
// returns an empty page (zero results), exactly like a search that found nothing.
func searchStub(t *testing.T, byQuery map[string][]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		for _, u := range byQuery[r.URL.Query().Get("q")] {
			fmt.Fprintf(&b, `<li class="b_algo"><h2><a href="%s">%s</a></h2><p>clojure rich hickey</p></li>`, u, u)
		}
		_, _ = w.Write([]byte("<html><body><ol>" + b.String() + "</ol></body></html>"))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WEBSEARCH_BING_URL", srv.URL)
	t.Setenv("WEBSEARCH_ENGINES", "bing")
}

// scriptAI is the survey's decision plane, scripted. It answers the plan prompt
// with plan, the Nth next() prompt with moves[N] (the last entry repeats forever,
// so a script cannot accidentally bound the loop the code under test must bound),
// and everything else with the answer. Every prompt is recorded.
type scriptAI struct {
	plan    string
	moves   []string
	answer  string
	tokens  int
	nexts   int
	prompts []string
}

func (s *scriptAI) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	s.prompts = append(s.prompts, req.Prompt)
	reply := s.answer
	switch {
	case strings.Contains(req.Prompt, `"next"`):
		reply = s.moves[min(s.nexts, len(s.moves)-1)]
		s.nexts++
	case strings.Contains(req.Prompt, `"plan"`):
		reply = s.plan
	case strings.Contains(req.Prompt, `"questions"`):
		reply = `{"questions":["a?","b?","c?"]}`
	}
	return &types.ChatResponse{Content: reply, TotalTokens: s.tokens}, nil
}

func (s *scriptAI) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

// nextPrompts returns just the decision prompts — the ones survey's next() sent.
func (s *scriptAI) nextPrompts() []string {
	var out []string
	for _, p := range s.prompts {
		if strings.Contains(p, `"next"`) {
			out = append(out, p)
		}
	}
	return out
}

// deepParams is research with its real dials and a deterministic single-topic plan.
func deepParams() Params {
	p := baseParams(modes["research"])
	p.followUps = false // the survey is what is under test, not the coda
	return p
}

// urlSet is a snapshot's URLs, for superset assertions.
func urlSet(s []Source) map[string]bool {
	out := make(map[string]bool, len(s))
	for _, x := range s {
		out[x.URL] = true
	}
	return out
}

// ── rounds == 0 is the single pass, unchanged ────────────────────────────────

// TestSurveySinglePassIsTheOldLoop is the regression that matters most: the fast
// modes did not change. rounds==0 searches the plan's queries ONCE, never asks
// the model what to do next, and emits exactly one sources snapshot (nothing is
// read, so nothing re-emits it).
func TestSurveySinglePassIsTheOldLoop(t *testing.T) {
	searchStub(t, map[string][]string{
		"who created clojure and why": {"https://clojure.org/about", "https://en.wikipedia.org/wiki/Rich_Hickey"},
	})
	fakeCrawl(t, nil)

	ai := &scriptAI{answer: "A grounded answer.", moves: []string{`{"done":false,"queries":["never"]}`}}
	r := &recSink{}
	p := baseParams(modes["search"])
	p.followUps = false
	newEngine(ai).Run(context.Background(), p, r)

	if ai.nexts != 0 {
		t.Fatalf("a single pass must never ask for a next move, got %d decision calls", ai.nexts)
	}
	if len(r.snaps) != 1 {
		t.Fatalf("a single pass emits exactly one sources snapshot, got %d", len(r.snaps))
	}
	if len(r.srcs) != 2 {
		t.Fatalf("want the 2 stubbed sources, got %d", len(r.srcs))
	}
	if strings.Contains(strings.Join(r.order, ","), "status:reading") {
		t.Fatalf("readTop=0 must read nothing: %v", r.order)
	}
	if r.order[len(r.order)-1] != "done" {
		t.Fatalf("done must be terminal: %v", r.order)
	}
}

// ── the loop actually iterates ────────────────────────────────────────────────

// TestSurveyIteratesSearchAndRead proves the capability being ported: a later
// round runs a query the plan never contained, reads a page round zero did not,
// and the evidence set GROWS across rounds.
func TestSurveyIteratesSearchAndRead(t *testing.T) {
	searchStub(t, map[string][]string{
		"origins of clojure":    {"https://clojure.org/about"},
		"clojure jvm rationale": {"https://a.example/jvm"},
		"hickey talks":          {"https://b.example/talks"},
	})
	asked := fakeCrawl(t, map[string]string{
		"https://clojure.org/about": "# About\n\nRich Hickey created Clojure.",
		"https://a.example/jvm":     "# JVM\n\nHosted on the JVM by design.",
	})

	ai := &scriptAI{
		plan:   `{"plan":[{"title":"Origins","todos":["origins of clojure"]}]}`,
		answer: "Report.",
		moves: []string{
			`{"next":"look at the JVM rationale","queries":["clojure jvm rationale"],"read":["https://a.example/jvm"]}`,
			`{"next":"check his talks","queries":["hickey talks"],"done":false}`,
			`{"done":true}`,
		},
	}
	r := &recSink{}
	newEngine(ai).Run(context.Background(), deepParams(), r)

	got := urlSet(r.srcs)
	for _, want := range []string{"https://clojure.org/about", "https://a.example/jvm", "https://b.example/talks"} {
		if !got[want] {
			t.Fatalf("later rounds must add sources; %q missing from %v", want, got)
		}
	}
	// Round 0 read the top-ranked page; round 1 read exactly what the move asked for.
	if strings.Join(*asked, ",") != "https://clojure.org/about,https://a.example/jvm" {
		t.Fatalf("read must follow round 0's ranking then the move's list, got %v", *asked)
	}
	// The page text landed on the source and SURVIVED the next round's re-rank.
	for _, s := range r.srcs {
		if s.URL == "https://clojure.org/about" && !strings.Contains(s.Snippet, "Rich Hickey created Clojure") {
			t.Fatalf("round 0's page must survive re-ranking, got %q", s.Snippet)
		}
	}
	if ai.nexts != 3 {
		t.Fatalf("want 3 decision calls before done, got %d", ai.nexts)
	}
}

// TestSurveyEmitsCumulativeSourceSnapshots pins the SDK contract: every consumer
// REPLACES its source list on a `sources` event, so each frame must be the whole
// set — an incremental frame would erase what the client already had.
func TestSurveyEmitsCumulativeSourceSnapshots(t *testing.T) {
	searchStub(t, map[string][]string{
		"origins of clojure": {"https://clojure.org/about"},
		"more":               {"https://a.example/jvm"},
	})
	fakeCrawl(t, map[string]string{"https://clojure.org/about": "# About\n\nbody"})

	ai := &scriptAI{
		plan:   `{"plan":[{"title":"Origins","todos":["origins of clojure"]}]}`,
		answer: "Report.",
		moves:  []string{`{"next":"widen","queries":["more"]}`, `{"done":true}`},
	}
	r := &recSink{}
	newEngine(ai).Run(context.Background(), deepParams(), r)

	if len(r.snaps) < 3 {
		t.Fatalf("want a snapshot per rank and per read, got %d", len(r.snaps))
	}
	prev := urlSet(r.snaps[0])
	for i, s := range r.snaps[1:] {
		cur := urlSet(s)
		for u := range prev {
			if !cur[u] {
				t.Fatalf("snapshot %d dropped %q — sources frames must be cumulative", i+1, u)
			}
		}
		prev = cur
	}
}

// TestSurveyCarriesThePlanIntoEveryRound proves the checklist property: the plan
// is injected verbatim into every decision prompt, which is what keeps a long
// gather on the question instead of drifting to whatever the last page was about.
func TestSurveyCarriesThePlanIntoEveryRound(t *testing.T) {
	searchStub(t, map[string][]string{"origins of clojure": {"https://clojure.org/about"}, "q2": {"https://a.example/x"}})
	fakeCrawl(t, nil)

	ai := &scriptAI{
		plan:   `{"plan":[{"title":"Origins","todos":["origins of clojure","why immutability"]}]}`,
		answer: "Report.",
		moves:  []string{`{"next":"widen","queries":["q2"]}`, `{"done":true}`},
	}
	newEngine(ai).Run(context.Background(), deepParams(), &recSink{})

	prompts := ai.nextPrompts()
	if len(prompts) < 2 {
		t.Fatalf("want at least 2 decision prompts, got %d", len(prompts))
	}
	for i, p := range prompts {
		for _, want := range []string{`"Origins"`, `"why immutability"`} {
			if !strings.Contains(p, want) {
				t.Fatalf("decision prompt %d must carry the plan verbatim (%s missing)", i, want)
			}
		}
	}
}

// TestSurveyEmitsNextStepAsPlanningDetail proves the visible reasoning: the
// model's one-sentence next step reaches the client as a `planning` status
// detail — inside the union, never as a fifth stage and never as answer text.
func TestSurveyEmitsNextStepAsPlanningDetail(t *testing.T) {
	searchStub(t, map[string][]string{"origins of clojure": {"https://clojure.org/about"}, "q2": {"https://a.example/x"}})
	fakeCrawl(t, nil)

	ai := &scriptAI{
		plan:   `{"plan":[{"title":"Origins","todos":["origins of clojure"]}]}`,
		answer: "Report.",
		moves:  []string{`{"next":"Compare the JVM hosting rationale","queries":["q2"]}`, `{"done":true}`},
	}
	r := &recSink{}
	newEngine(ai).Run(context.Background(), deepParams(), r)

	var found bool
	for _, d := range r.details["planning"] {
		if d == "Compare the JVM hosting rationale" {
			found = true
		}
		if len([]rune(d)) > maxNextStep {
			t.Fatalf("next step must stay under %d runes, got %q", maxNextStep, d)
		}
	}
	if !found {
		t.Fatalf("the move's next step must be emitted as a planning detail, got %v", r.details["planning"])
	}
}

// TestSurveyDiscardsTheRoundsProse is the separation Scira buys with a forced
// tool call and we buy with a parser: a round that answers in prose contributes
// NOTHING to the answer text — only its move is read, and it has none, so the
// survey ends.
func TestSurveyDiscardsTheRoundsProse(t *testing.T) {
	searchStub(t, map[string][]string{"origins of clojure": {"https://clojure.org/about"}})
	fakeCrawl(t, nil)

	ai := &scriptAI{
		plan:   `{"plan":[{"title":"Origins","todos":["origins of clojure"]}]}`,
		answer: "The synthesized report.",
		moves:  []string{"Sure! I think we should look into the JVM next. Let me search for that."},
	}
	r := &recSink{}
	newEngine(ai).Run(context.Background(), deepParams(), r)

	if strings.Contains(r.buf.String(), "Sure! I think") {
		t.Fatalf("a round's prose must never reach the answer text, got %q", r.buf.String())
	}
	if r.answer != "The synthesized report." {
		t.Fatalf("the answer must come from synthesis alone, got %q", r.answer)
	}
	if ai.nexts != 1 {
		t.Fatalf("an unparseable move must end the survey, not loop; %d decision calls", ai.nexts)
	}
	if r.order[len(r.order)-1] != "done" {
		t.Fatalf("done must still be terminal: %v", r.order)
	}
}

// ── the bounds ────────────────────────────────────────────────────────────────

// boundCase drives one bound to its exit and asserts the frame sequence survived.
type boundCase struct {
	name  string
	tune  func(*Params)
	moves []string
	nexts int // decision calls the bound must permit
}

func TestSurveyBoundsExitCleanly(t *testing.T) {
	// Every round finds something new, so ONLY the bound under test can stop it.
	fresh := map[string][]string{"origins of clojure": {"https://clojure.org/about"}}
	for i := range 10 {
		fresh[fmt.Sprintf("q%d", i)] = []string{fmt.Sprintf("https://r%d.example/x", i)}
	}
	var moves []string
	for i := range 10 {
		moves = append(moves, fmt.Sprintf(`{"next":"widen %d","queries":["q%d"]}`, i, i))
	}

	cases := []boundCase{
		// The mode's round budget: N rounds of gathering means N-1 decisions.
		{name: "rounds", tune: func(p *Params) { p.rounds = 3 }, moves: moves, nexts: 2},
		// The hard ceiling wins over any mode that asks for more.
		{name: "maxRounds", tune: func(p *Params) { p.rounds = maxRounds }, moves: moves, nexts: maxRounds - 1},
		// The model says the evidence is complete.
		{name: "done", moves: []string{`{"next":"widen","queries":["q0"]}`, `{"done":true}`}, nexts: 2},
		// Saturation: a model that keeps proposing a query it already ran adds no
		// evidence, and the loop must notice rather than spin out its budget.
		{name: "saturation", moves: []string{`{"next":"again","queries":["origins of clojure"]}`}, nexts: 1},
		// An empty move is a decision to stop.
		{name: "empty move", moves: []string{`{"next":"nothing","queries":[],"read":[]}`}, nexts: 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			searchStub(t, fresh)
			fakeCrawl(t, nil)
			ai := &scriptAI{
				plan:   `{"plan":[{"title":"Origins","todos":["origins of clojure"]}]}`,
				answer: "Report.",
				moves:  c.moves,
			}
			p := deepParams()
			if c.tune != nil {
				c.tune(&p)
			}
			r := &recSink{}
			newEngine(ai).Run(context.Background(), p, r)

			if ai.nexts != c.nexts {
				t.Fatalf("bound %q: want %d decision calls, got %d", c.name, c.nexts, ai.nexts)
			}
			assertTerminal(t, r)
		})
	}
}

// TestSurveyTokenCeilingStopsTheGather proves the cost bound is real: once the
// running token total crosses the mode's ceiling the survey stops deciding, even
// though rounds remain and every round is still finding new evidence.
func TestSurveyTokenCeilingStopsTheGather(t *testing.T) {
	searchStub(t, map[string][]string{
		"origins of clojure": {"https://clojure.org/about"},
		"q0":                 {"https://r0.example/x"},
		"q1":                 {"https://r1.example/x"},
	})
	fakeCrawl(t, nil)

	ai := &scriptAI{
		plan:   `{"plan":[{"title":"Origins","todos":["origins of clojure"]}]}`,
		answer: "Report.",
		tokens: 5_000,
		moves:  []string{`{"next":"widen","queries":["q0"]}`, `{"next":"widen","queries":["q1"]}`},
	}
	p := deepParams()
	p.tokenCeiling = 1_000 // one decision call's usage already blows it
	r := &recSink{}
	newEngine(ai).Run(context.Background(), p, r)

	if ai.nexts != 1 {
		t.Fatalf("the token ceiling must stop the gather after one decision, got %d", ai.nexts)
	}
	assertTerminal(t, r)
}

// TestSurveyDeadlineStopsTheGather proves a client disconnect or an expired wall
// clock ends the gather at the round boundary — and STILL produces the terminal
// frames, because the answer is synthesized from whatever was gathered.
func TestSurveyDeadlineStopsTheGather(t *testing.T) {
	searchStub(t, map[string][]string{"origins of clojure": {"https://clojure.org/about"}, "q0": {"https://r0.example/x"}})
	fakeCrawl(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	ai := &scriptAI{
		plan:   `{"plan":[{"title":"Origins","todos":["origins of clojure"]}]}`,
		answer: "Report.",
		moves:  []string{`{"next":"widen","queries":["q0"]}`},
	}
	r := &recSink{}
	// Expire the budget the moment the first round's evidence lands.
	newEngine(ai).Run(ctx, deepParams(), &cancelOn{Sink: r, at: 1, cancel: cancel})
	defer cancel()

	if ai.nexts != 0 {
		t.Fatalf("an expired budget must stop before the next decision, got %d", ai.nexts)
	}
	assertTerminal(t, r)
}

// cancelOn expires the run's context after the nth sources frame, so a test can
// place the deadline exactly at a round boundary.
type cancelOn struct {
	Sink
	at, seen int
	cancel   context.CancelFunc
}

func (c *cancelOn) sources(s []Source) {
	c.Sink.sources(s)
	if c.seen++; c.seen == c.at {
		c.cancel()
	}
}

// assertTerminal checks the envelope survived whatever ended the survey: the
// answer was synthesized and streamed, and `done` is the last frame.
func assertTerminal(t *testing.T, r *recSink) {
	t.Helper()
	joined := strings.Join(r.order, ",")
	for _, want := range []string{"status:searching", "sources", "status:answering", "text", "done"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q after the survey ended: %s", want, joined)
		}
	}
	if r.order[len(r.order)-1] != "done" {
		t.Fatalf("done must be terminal: %s", joined)
	}
	if r.answer == "" {
		t.Fatal("a bounded survey must still produce an answer")
	}
}

// ── the pure pieces ───────────────────────────────────────────────────────────

func TestParseMove(t *testing.T) {
	m := parseMove("```json\n{\"next\":\"look \\n at  logs\",\"queries\":[\"a\",\" \",\"b\"],\"read\":[\"u1\"],\"done\":true}\n```")
	if m.Next != "look at logs" {
		t.Fatalf("next must collapse to one line, got %q", m.Next)
	}
	if strings.Join(m.Queries, ",") != "a,b" {
		t.Fatalf("blank queries must drop, got %v", m.Queries)
	}
	if len(m.Read) != 1 || !m.Done {
		t.Fatalf("read/done mis-parsed: %+v", m)
	}
	// A long next step is clipped, not emitted whole.
	long := parseMove(`{"next":"` + strings.Repeat("x", 200) + `"}`)
	if len([]rune(long.Next)) != maxNextStep {
		t.Fatalf("next step must clip to %d, got %d", maxNextStep, len([]rune(long.Next)))
	}
	// Prose, empty, and malformed all yield the zero move — which ends the survey.
	for _, in := range []string{"I'll search for more", "", "{not json}", "{}"} {
		if got := parseMove(in); got.Done || len(got.Queries) > 0 || len(got.Read) > 0 {
			t.Fatalf("unparseable %q must yield the zero move, got %+v", in, got)
		}
	}
}

func TestParsePlanShapesAndFallback(t *testing.T) {
	got := parsePlan(`{"plan":[{"title":"Origins","todos":["a","","b"]},{"title":"","todos":["c"]},{"title":"Empty","todos":[]}]}`)
	if len(got) != 2 {
		t.Fatalf("a topic with no todos must drop, got %+v", got)
	}
	if strings.Join(got[0].Todos, ",") != "a,b" {
		t.Fatalf("blank todos must drop, got %v", got[0].Todos)
	}
	if got[1].Title != "c" {
		t.Fatalf("a titleless topic must take its first todo as its title, got %q", got[1].Title)
	}
	// The flat shape still yields a usable plan — one topic per query.
	flat := parsePlan(`{"queries":["x","y"]}`)
	if len(flat) != 2 || flat[0].Todos[0] != "x" {
		t.Fatalf("the flat queries shape must still plan, got %+v", flat)
	}
	if parsePlan("no json here") != nil {
		t.Fatal("an unplannable reply must yield nil so the caller seeds its own")
	}
}

func TestFlattenCoversTopicsBreadthFirst(t *testing.T) {
	plan := []topic{
		{Title: "A", Todos: []string{"a1", "a2", "a3"}},
		{Title: "B", Todos: []string{"b1", "b2"}},
	}
	if got := flatten(plan, 4); strings.Join(got, ",") != "a1,b1,a2,b2" {
		t.Fatalf("the opening round must cover every topic before any topic's depth, got %v", got)
	}
	if got := flatten(plan, 2); len(got) != 2 {
		t.Fatalf("flatten must respect the query budget, got %v", got)
	}
	if got := flatten([]topic{{Todos: []string{"x", "x", " "}}}, 5); len(got) != 1 {
		t.Fatalf("duplicate and blank todos must collapse, got %v", got)
	}
	if flatten(plan, 0) != nil {
		t.Fatal("a zero budget yields no queries")
	}
}

func TestUnreadNeverFetchesTwice(t *testing.T) {
	fetched := map[string]bool{}
	first := unread([]string{"u1", " u2 ", "", "u1"}, fetched)
	if strings.Join(first, ",") != "u1,u2" {
		t.Fatalf("unread must trim, drop blanks, and dedupe: %v", first)
	}
	if got := unread([]string{"u1", "u2", "u3"}, fetched); strings.Join(got, ",") != "u3" {
		t.Fatalf("a url already read must never be fetched again, got %v", got)
	}
}

func TestTopURLs(t *testing.T) {
	s := srcs("https://a.com/1", "https://b.com/2", "https://c.com/3")
	if got := topURLs(s, 2); strings.Join(got, ",") != "https://a.com/1,https://b.com/2" {
		t.Fatalf("topURLs must take the best-ranked n, got %v", got)
	}
	if got := topURLs(s, 99); len(got) != 3 {
		t.Fatalf("topURLs must not ask for more than exist, got %v", got)
	}
	if topURLs(s, 0) != nil {
		t.Fatal("readTop=0 must read nothing")
	}
}
