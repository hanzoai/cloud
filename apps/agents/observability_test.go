package agents

// observability_test.go — the proof that ONE agent turn is readable afterwards.
//
// These tests run a real turn (real HTTP handler, real tool loop, real OpenAI-wire
// client against a stand-in gateway) with the real span pipeline installed, and
// assert on the spans that actually reached a sink plus the record the console
// reads. They exist because every fact below was, at one point, emitted by code
// that looked correct and observable by nobody: the run's own id was minted AFTER
// the work finished, so no span or debit the run produced could carry it.
//
// What is asserted is the operator's question, not the implementation's shape:
// what ran, for which org and which user, on which model, how many tokens, which
// tools it called, how long it took, and why it failed.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/hanzoai/cloud/clients"
	"github.com/hanzoai/cloud/types"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// spanSink captures the batches the tracer provider exports, which is the only
// honest place to assert from: a span that is created but never exported is
// exactly the failure mode this file exists to catch.
type spanSink struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (s *spanSink) export(_ context.Context, batch []sdktrace.ReadOnlySpan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spans = append(s.spans, batch...)
	return nil
}

// find returns the first captured span whose name matches, and whether there was one.
func (s *spanSink) find(name string) (sdktrace.ReadOnlySpan, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sp := range s.spans {
		if sp.Name() == name {
			return sp, true
		}
	}
	return nil, false
}

// all returns every captured span with the given name.
func (s *spanSink) all(name string) []sdktrace.ReadOnlySpan {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sdktrace.ReadOnlySpan
	for _, sp := range s.spans {
		if sp.Name() == name {
			out = append(out, sp)
		}
	}
	return out
}

// attr reads one string/int attribute off a span as text. A missing attribute is
// "", which is what the assertions below are checking for.
func attr(sp sdktrace.ReadOnlySpan, key string) string {
	for _, kv := range sp.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.Emit()
		}
	}
	return ""
}

// traced makes this process's spans observable to the test, and returns the sink
// they land in.
//
// The provider is installed ONCE per process, and only the SINK is swapped per
// test. That is not tidiness — it is the contract OTel's global actually has: a
// tracer handle taken at package init (agentTracer here, aiTracer in clients)
// binds to the FIRST provider installed and keeps it forever, so a second install
// does not rebind those handles. A test that installed its own provider and shut
// it down on cleanup therefore left every later span-asserting test in the same
// binary exporting into a dead provider, and seeing nothing. Measured: the
// six-tool test below failed with "produced no span" for spans that were in fact
// created, purely because it ran second.
//
// Export is SYNCHRONOUS (WithSyncer): a span is in the sink when End() returns, so
// an assertion never races a batch timer and no test has to sleep to be correct.
var (
	obsOnce sync.Once
	obsSink atomic.Pointer[spanSink]
)

// obsExporter forwards to whichever sink is currently installed. Nil sink means a
// test that is not asserting on spans is running; its spans are discarded rather
// than accumulated into another test's assertions.
type obsExporter struct{}

func (obsExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if s := obsSink.Load(); s != nil {
		return s.export(ctx, spans)
	}
	return nil
}

func (obsExporter) Shutdown(context.Context) error { return nil }

func traced(t *testing.T) *spanSink {
	t.Helper()
	obsOnce.Do(func() {
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(obsExporter{})))
	})
	sink := &spanSink{}
	obsSink.Store(sink)
	t.Cleanup(func() { obsSink.Store(nil) })
	return sink
}

// stubPlane is a deterministic tool plane: it offers the named tools and fails
// exactly the ones named in fail.
type stubPlane struct {
	mu     sync.Mutex
	offer  []string
	fail   map[string]bool
	called []string
	// result is what a named tool returns; anything unnamed returns a default so
	// the common case stays a one-line fixture.
	result map[string]string
}

func (p *stubPlane) catalog(context.Context, string, string, []string) []types.ToolDef {
	out := make([]types.ToolDef, 0, len(p.offer))
	for _, n := range p.offer {
		out = append(out, types.ToolDef{Name: n, Description: "d", Schema: json.RawMessage(`{"type":"object"}`)})
	}
	return out
}

func (p *stubPlane) call(_ context.Context, _, _, name, _ string) (string, error) {
	p.mu.Lock()
	p.called = append(p.called, name)
	p.mu.Unlock()
	if p.fail[name] {
		return "", fmt.Errorf("upstream refused %s", name)
	}
	if out, ok := p.result[name]; ok {
		return out, nil
	}
	return "result of " + name, nil
}

// toolGatewayArgs answers with ONE tool call carrying the given arguments
// verbatim, then a final answer — so a test can pin what a dispatch records about
// arguments it did not choose.
func toolGatewayArgs(t *testing.T, name, args string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), `"role":"tool"`) || !strings.Contains(string(body), `"tools":[`) {
			fmt.Fprint(w, `{"id":"c2","model":"gpt-4o-mini","choices":[{"index":0,"finish_reason":"stop",`+
				`"message":{"role":"assistant","content":"the answer"}}],`+
				`"usage":{"prompt_tokens":40,"completion_tokens":9,"total_tokens":49}}`)
			return
		}
		call, _ := json.Marshal(args) // the arguments ride as a JSON *string*, as the wire has them
		fmt.Fprintf(w, `{"id":"c1","model":"gpt-4o-mini","choices":[{"index":0,"finish_reason":"tool_calls",`+
			`"message":{"role":"assistant","tool_calls":[`+
			`{"id":"tc0","type":"function","function":{"name":%q,"arguments":%s}}]}}],`+
			`"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33}}`, name, call)
	}))
}

// toolGateway answers the REQUEST rather than a call counter: a turn already
// holding tool results gets the final answer; a turn offered tools asks for all of
// them at once. Answering a counter makes the fixture depend on whoever happened
// to call the gateway first, which is not a property of the code under test.
func toolGateway(t *testing.T, ask []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req := string(body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req, `"role":"tool"`) || !strings.Contains(req, `"tools":[`) {
			fmt.Fprint(w, `{"id":"c2","model":"gpt-4o-mini","choices":[{"index":0,"finish_reason":"stop",`+
				`"message":{"role":"assistant","content":"the answer"}}],`+
				`"usage":{"prompt_tokens":40,"completion_tokens":9,"total_tokens":49}}`)
			return
		}
		calls := make([]string, 0, len(ask))
		for i, n := range ask {
			calls = append(calls, fmt.Sprintf(
				`{"id":"tc%d","type":"function","function":{"name":%q,"arguments":"{}"}}`, i, n))
		}
		fmt.Fprintf(w, `{"id":"c1","model":"gpt-4o-mini","choices":[{"index":0,"finish_reason":"tool_calls",`+
			`"message":{"role":"assistant","tool_calls":[%s]}}],`+
			`"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33}}`, strings.Join(calls, ","))
	}))
}

// TestOneRunIsObservableEndToEnd: after one turn an operator can answer every
// question from the spans plus the run record, and can get from one to the other.
func TestOneRunIsObservableEndToEnd(t *testing.T) {
	sink := traced(t)

	plane := &stubPlane{offer: []string{"post_v1_search_query"}}
	old := runTools
	runTools = plane
	t.Cleanup(func() { runTools = old })

	gw := toolGateway(t, []string{"post_v1_search_query"})
	defer gw.Close()

	app := mountApp(t, clients.AIHTTPAt(gw.URL+"/v1", "k", "gpt-4o-mini"))
	do(t, app, http.MethodPost, "/v1/agents", "acme", map[string]any{
		"name": "a", "model": "gpt-4o-mini", "instructions": "x", "tools": []string{"post_v1_search_query"}})
	code, body := do(t, app, http.MethodPost, "/v1/agents/a/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusOK {
		t.Fatalf("run want 200, got %d (%s)", code, body)
	}

	var rec struct {
		ID               string `json:"id"`
		Status           string `json:"status"`
		Model            string `json:"model"`
		Agent            string `json:"agent"`
		Actor            string `json:"actor"`
		TraceID          string `json:"traceId"`
		PromptTokens     int    `json:"promptTokens"`
		CompletionTokens int    `json:"completionTokens"`
		ToolCalls        int    `json:"toolCalls"`
	}
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatalf("run body: %v (%s)", err, body)
	}

	// WHO and WHAT, on the record the console reads.
	if rec.Actor != "acme/u-acme" {
		t.Fatalf("run record must name the person who ran it, got actor %q", rec.Actor)
	}
	if rec.Agent != "a" {
		t.Fatalf("run record must name its agent, got %q", rec.Agent)
	}
	if rec.PromptTokens == 0 || rec.CompletionTokens == 0 {
		t.Fatalf("run record must carry the tokens the gateway reported, got %d/%d",
			rec.PromptTokens, rec.CompletionTokens)
	}
	if rec.ToolCalls != 1 {
		t.Fatalf("run record must count its tool calls, got %d", rec.ToolCalls)
	}

	// THE JOIN. Without this the run history and the trace store hold two accounts
	// of one event with no key in common, and "drill into this run" has no target.
	if rec.TraceID == "" {
		t.Fatal("run record carries no traceId: the console cannot reach this run's spans")
	}

	root, ok := sink.find("agent.run a")
	if !ok {
		t.Fatal("no agent.run span was exported for a run that happened")
	}
	if got := root.SpanContext().TraceID().String(); got != rec.TraceID {
		t.Fatalf("run record points at trace %q but the run span is in %q", rec.TraceID, got)
	}
	if got := attr(root, "hanzo.agent.run_id"); got != rec.ID {
		t.Fatalf("run span names run %q, record is %q", got, rec.ID)
	}
	if got := attr(root, "hanzo.user"); got != "u-acme" {
		t.Fatalf("run span must name the user, got %q", got)
	}
	if got := attr(root, "hanzo.org"); got != "acme" {
		t.Fatalf("run span must name the org, got %q", got)
	}

	// EVERY span the run produced names the run, so attribution never depends on
	// walking a parent chain that sampling or a truncated batch may have broken.
	for _, name := range []string{"agent.step", "agent.tool post_v1_search_query", "chat gpt-4o-mini"} {
		sp, ok := sink.find(name)
		if !ok {
			t.Fatalf("no %q span was exported", name)
		}
		if got := attr(sp, "hanzo.agent.run_id"); got != rec.ID {
			t.Fatalf("%s names run %q, want %q", name, got, rec.ID)
		}
		if got := sp.SpanContext().TraceID().String(); got != rec.TraceID {
			t.Fatalf("%s is in trace %q, want the run's %q", name, got, rec.TraceID)
		}
	}

	// The model calls carry the token usage, per call.
	for _, sp := range sink.all("chat gpt-4o-mini") {
		if attr(sp, "gen_ai.usage.input_tokens") == "" {
			t.Fatal("a gen_ai span carries no input token count")
		}
	}

	// The tool dispatch is readable as a dispatch: which tool, whose, which
	// subsystem answers for it, and how it turned out.
	tool, _ := sink.find("agent.tool post_v1_search_query")
	if got := attr(tool, "hanzo.agent.tool_subsystem"); got != "search" {
		t.Fatalf("tool span must name the owning subsystem, got %q", got)
	}
	if got := attr(tool, "hanzo.agent.tool_outcome"); got != "ok" {
		t.Fatalf("a tool that worked must say so, got outcome %q", got)
	}
	if got := attr(tool, "hanzo.user"); got != "u-acme" {
		t.Fatalf("tool span must name the actor it ran as, got %q", got)
	}
}

// TestFailedToolIsReadableAsSuchOnItsRun: a run that called six tools and failed
// on the fourth must be readable as exactly that — the failing dispatch names its
// round and its outcome, and the run still completes, because a tool failure is a
// fact the model acts on rather than an aborted turn.
func TestFailedToolIsReadableAsSuchOnItsRun(t *testing.T) {
	sink := traced(t)

	names := []string{
		"post_v1_search_query", "get_v1_git_repos", "post_v1_exec_run",
		"get_v1_kms_secrets", "post_v1_notify_send", "get_v1_index_docs",
	}
	plane := &stubPlane{offer: names, fail: map[string]bool{"get_v1_kms_secrets": true}}
	old := runTools
	runTools = plane
	t.Cleanup(func() { runTools = old })

	gw := toolGateway(t, names)
	defer gw.Close()

	app := mountApp(t, clients.AIHTTPAt(gw.URL+"/v1", "k", "gpt-4o-mini"))
	do(t, app, http.MethodPost, "/v1/agents", "acme", map[string]any{
		"name": "a", "model": "gpt-4o-mini", "instructions": "x", "tools": names})
	code, body := do(t, app, http.MethodPost, "/v1/agents/a/run", "acme", map[string]any{"input": "go"})
	if code != http.StatusOK {
		t.Fatalf("a run whose tool failed still answers 200, got %d (%s)", code, body)
	}

	var rec struct {
		ID        string `json:"id"`
		ToolCalls int    `json:"toolCalls"`
	}
	_ = json.Unmarshal(body, &rec)
	if rec.ToolCalls != len(names) {
		t.Fatalf("the run must count all %d dispatches, got %d", len(names), rec.ToolCalls)
	}

	// The one that failed says so, names itself, and is attributable to this run.
	bad, ok := sink.find("agent.tool get_v1_kms_secrets")
	if !ok {
		t.Fatal("the failing tool produced no span")
	}
	if got := attr(bad, "hanzo.agent.tool_outcome"); got != "error" {
		t.Fatalf("the failing dispatch must record outcome=error, got %q", got)
	}
	if bad.Status().Code.String() != "Error" {
		t.Fatalf("the failing dispatch must carry error status, got %s", bad.Status().Code)
	}
	if !strings.Contains(bad.Status().Description+fmt.Sprint(bad.Events()), "tool call failed") &&
		bad.Status().Description == "" {
		t.Fatalf("the failing dispatch records no reason")
	}
	if got := attr(bad, "hanzo.agent.run_id"); got != rec.ID {
		t.Fatalf("the failing dispatch names run %q, want %q", got, rec.ID)
	}
	if got := attr(bad, "hanzo.agent.tool_subsystem"); got != "kms" {
		t.Fatalf("the failing dispatch must name the subsystem that refused, got %q", got)
	}

	// Its five siblings succeeded, in the same run and the same round — so the
	// operator reads "six called, one failed", not "the run broke".
	okCount := 0
	for _, n := range names {
		sp, found := sink.find("agent.tool " + n)
		if !found {
			t.Fatalf("no span for dispatched tool %s", n)
		}
		if attr(sp, "hanzo.agent.tool_outcome") == "ok" {
			okCount++
		}
		if got := attr(sp, "hanzo.agent.tool_round"); got != "0" {
			t.Fatalf("%s reports round %q, want 0", n, got)
		}
	}
	if okCount != len(names)-1 {
		t.Fatalf("want %d successful dispatches beside the failure, got %d", len(names)-1, okCount)
	}
}

// TestRunIDReachesTheModelCall: the run's name is on the request the metering
// decorator prices, on EVERY round of a tool loop. That is what lets the ledger's
// per-token rows be summed back to the run that caused them — the agents-side half
// of "what did this run cost".
func TestRunIDReachesTheModelCall(t *testing.T) {
	rec := &runIDRecorder{}
	plane := &stubPlane{offer: []string{"post_v1_search_query"}}
	old := runTools
	runTools = plane
	t.Cleanup(func() { runTools = old })

	app := mountApp(t, rec)
	do(t, app, http.MethodPost, "/v1/agents", "acme", map[string]any{
		"name": "a", "model": "m", "instructions": "x", "tools": []string{"post_v1_search_query"}})
	code, body := do(t, app, http.MethodPost, "/v1/agents/a/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusOK {
		t.Fatalf("run want 200, got %d (%s)", code, body)
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &out)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.runIDs) < 2 {
		t.Fatalf("want at least 2 completion rounds, got %d", len(rec.runIDs))
	}
	for i, got := range rec.runIDs {
		if got != out.ID {
			t.Fatalf("round %d priced under run %q, want %q — its cost would not sum to this run",
				i, got, out.ID)
		}
	}
}

// runIDRecorder answers one tool call then a final answer, recording the RunID it
// was asked under on every round.
type runIDRecorder struct {
	mu     sync.Mutex
	runIDs []string
	rounds int
}

func (r *runIDRecorder) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runIDs = append(r.runIDs, req.RunID)
	r.rounds++
	if r.rounds == 1 {
		return &types.ChatResponse{
			ToolCalls:    []types.ToolCall{{ID: "tc0", Name: "post_v1_search_query", Arguments: "{}"}},
			PromptTokens: 5, CompletionTokens: 6, TotalTokens: 11,
		}, nil
	}
	return &types.ChatResponse{Content: "done", PromptTokens: 7, CompletionTokens: 8, TotalTokens: 15}, nil
}

func (r *runIDRecorder) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

// TestToolSubsystemReadsTheNameNotAnIndex pins the derivation: the owner is a fact
// the operation name already states, so it answers the same in a fused binary and
// in a single-app plugin process — where a mount-index lookup would answer "" for
// every sibling's tool.
func TestToolSubsystemReadsTheNameNotAnIndex(t *testing.T) {
	cases := map[string]string{
		"post_v1_search_query":   "search",
		"get_v1_git_repos":       "git",
		"delete_v1_kms_secrets":  "kms",
		"get_v1_agents_sessions": "agents",
		"http":                   "", // a registry-local tool owns no subsystem
		"":                       "",
		"v1":                     "", // "v1" with nothing after it names nothing
	}
	for in, want := range cases {
		if got := toolSubsystem(in); got != want {
			t.Fatalf("toolSubsystem(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestToolCallRecordsWhatItDidWithoutItsCredential is the "exactly what steps it
// took" contract, and the one place where getting observability right and getting
// security right are the same edit.
//
// A dispatch used to record WHICH tool ran and nothing about what it ran WITH, so
// a trace could say an agent called post_v1_exec_run six times and never say what
// it executed. Recording the arguments closes that. But a tool argument routinely
// carries a live credential — a clone URL with a token in its userinfo is the
// ordinary shape apps/coding builds — and a span store is built to be queried and
// kept, so a trace that records one is worse than no trace.
//
// Both halves are asserted here together, because either alone is a bug: capture
// with no redaction leaks, redaction with no capture is the silence we started
// from.
func TestToolCallRecordsWhatItDidWithoutItsCredential(t *testing.T) {
	sink := traced(t)

	const token = "ghp_LIVE_TOKEN_VALUE"
	const args = `{"cloneUrl":"https://x-access-token:` + token + `@github.com/acme/repo","branch":"main"}`

	// The tool hands back a credential too — a result is as capable of carrying one
	// as an argument, and it is recorded on the same span.
	plane := &stubPlane{offer: []string{"post_v1_exec_run"}, result: map[string]string{
		"post_v1_exec_run": `{"ok":true,"remote":"https://deploy:s3cr3t@git.example.com/x"}`,
	}}
	old := runTools
	runTools = plane
	t.Cleanup(func() { runTools = old })

	gw := toolGatewayArgs(t, "post_v1_exec_run", args)
	defer gw.Close()

	app := mountApp(t, clients.AIHTTPAt(gw.URL+"/v1", "k", "gpt-4o-mini"))
	do(t, app, http.MethodPost, "/v1/agents", "acme", map[string]any{
		"name": "a", "model": "gpt-4o-mini", "instructions": "x", "tools": []string{"post_v1_exec_run"}})
	if code, body := do(t, app, http.MethodPost, "/v1/agents/a/run", "acme", map[string]any{"input": "go"}); code != http.StatusOK {
		t.Fatalf("run want 200, got %d (%s)", code, body)
	}

	sp, ok := sink.find("agent.tool post_v1_exec_run")
	if !ok {
		t.Fatal("the dispatch produced no span")
	}

	// CAPTURED: the call is legible as a call.
	gotArgs := attr(sp, "gen_ai.tool.call.arguments")
	if gotArgs == "" {
		t.Fatal("the dispatch recorded no arguments — the trace cannot say what the tool was called with")
	}
	if !strings.Contains(gotArgs, "github.com/acme/repo") || !strings.Contains(gotArgs, "main") {
		t.Fatalf("the recorded arguments lost the detail that makes them worth reading: %s", gotArgs)
	}
	gotResult := attr(sp, "gen_ai.tool.call.result")
	if gotResult == "" {
		t.Fatal("the dispatch recorded no result — the trace cannot say what came back")
	}

	// REDACTED: no credential reached the span, from either half.
	for _, secret := range []string{token, "s3cr3t"} {
		for _, where := range []struct{ name, v string }{{"arguments", gotArgs}, {"result", gotResult}} {
			if strings.Contains(where.v, secret) {
				t.Fatalf("a live credential reached the span in %s: %s", where.name, where.v)
			}
		}
	}
	// The non-secret half of the userinfo survives — it names HOW the call
	// authenticated, which is worth reading and is not the secret.
	if !strings.Contains(gotArgs, "x-access-token") {
		t.Fatalf("redaction removed the authenticating user, not just its secret: %s", gotArgs)
	}
}

// TestRecordedValueIsCutAndSaysSo: a tool argument larger than a span records is
// bounded, and the value itself states that it was — a silently clipped argument
// reads as the whole one, which is how an operator concludes a tool was called
// with something it never saw.
func TestRecordedValueIsCutAndSaysSo(t *testing.T) {
	big := `{"blob":"` + strings.Repeat("x", maxRecorded*2) + `"}`
	got := recordable(big)
	if len(got) > maxRecorded+64 {
		t.Fatalf("recorded value is %d bytes, want it bounded near %d", len(got), maxRecorded)
	}
	if !strings.Contains(got, "cut") {
		t.Fatalf("a cut value must say it was cut, got tail %q", got[max(0, len(got)-40):])
	}
	if !utf8.ValidString(got) {
		t.Fatal("cutting produced invalid UTF-8")
	}
	// A value that fits is returned whole, with no marker inviting a reader to
	// wonder what is missing.
	if small := recordable(`{"a":"b"}`); small != `{"a":"b"}` {
		t.Fatalf("a value within the bound must pass through unchanged, got %q", small)
	}
}

// TestEverySpanOfARunIsFiledUnderItsTenant is the one that decides whether any of
// the rest is visible.
//
// The trace plane files each row under ONE attribute — hanzo.org — read per span
// by apps/o11y/planesink.go planeOrg, which falls back to the PLATFORM's own org
// ("hanzo") when it is absent. OTel children do not inherit a parent's
// attributes, so a correctly stamped HTTP server span above the run buys its
// children nothing: every span states its own tenant or is filed under the
// platform.
//
// A run whose spans miss it fails twice over. The tenant opens the console and
// sees a trace with a hole where its agent was, because the org-scoped read never
// returns those rows; and the tenant's tool names, run ids and user subjects sit
// in the platform's bucket instead. That is why this asserts on EVERY exported
// span rather than on the root: one unstamped span is one missing branch of the
// waterfall.
func TestEverySpanOfARunIsFiledUnderItsTenant(t *testing.T) {
	sink := traced(t)

	plane := &stubPlane{offer: []string{"post_v1_search_query"}}
	old := runTools
	runTools = plane
	t.Cleanup(func() { runTools = old })

	gw := toolGateway(t, []string{"post_v1_search_query"})
	defer gw.Close()

	app := mountApp(t, clients.AIHTTPAt(gw.URL+"/v1", "k", "gpt-4o-mini"))
	do(t, app, http.MethodPost, "/v1/agents", "acme", map[string]any{
		"name": "a", "model": "gpt-4o-mini", "instructions": "x", "tools": []string{"post_v1_search_query"}})
	if code, body := do(t, app, http.MethodPost, "/v1/agents/a/run", "acme", map[string]any{"input": "hi"}); code != http.StatusOK {
		t.Fatalf("run want 200, got %d (%s)", code, body)
	}

	// The spans a run IS. Named explicitly so a span that stops being produced
	// fails here rather than silently shrinking the set under assertion.
	want := []string{"agent.run a", "agent.step", "agent.tool post_v1_search_query", "chat gpt-4o-mini"}
	for _, name := range want {
		spans := sink.all(name)
		if len(spans) == 0 {
			t.Fatalf("no %q span was exported", name)
		}
		for _, sp := range spans {
			if got := attr(sp, "hanzo.org"); got != "acme" {
				t.Fatalf("%s carries hanzo.org=%q, want \"acme\" — the plane files this span "+
					"under the platform org, so the tenant cannot see its own run", name, got)
			}
		}
	}
}

// TestOneOrgsRunNeverCarriesAnothersTenant proves the org stamp PARTITIONS the
// spans: two tenants running the same agent name against the same process produce
// two disjoint sets of rows, and neither names the other.
//
// Tenancy on the trace plane is exactly this attribute — the org-scoped read binds
// it as its first predicate — so a run that stamped the wrong tenant, or stamped
// none and fell back to the platform, would disclose one org's tool calls and user
// subjects to a reader scoped to another. The gate is fail-closed downstream; this
// asserts the value it closes on is right at the source.
func TestOneOrgsRunNeverCarriesAnothersTenant(t *testing.T) {
	sink := traced(t)

	plane := &stubPlane{offer: []string{"post_v1_search_query"}}
	old := runTools
	runTools = plane
	t.Cleanup(func() { runTools = old })

	gw := toolGateway(t, []string{"post_v1_search_query"})
	defer gw.Close()

	app := mountApp(t, clients.AIHTTPAt(gw.URL+"/v1", "k", "gpt-4o-mini"))
	for _, org := range []string{"acme", "globex"} {
		do(t, app, http.MethodPost, "/v1/agents", org, map[string]any{
			"name": "a", "model": "gpt-4o-mini", "instructions": "x", "tools": []string{"post_v1_search_query"}})
		if code, body := do(t, app, http.MethodPost, "/v1/agents/a/run", org, map[string]any{"input": "hi"}); code != http.StatusOK {
			t.Fatalf("%s run want 200, got %d (%s)", org, code, body)
		}
	}

	// Every span belongs to exactly one of the two tenants, and the user subject it
	// names is that tenant's own. A span filed under the platform default is
	// counted as neither and fails the total below.
	seen := map[string]int{}
	for _, name := range []string{"agent.run a", "agent.step", "agent.tool post_v1_search_query", "chat gpt-4o-mini"} {
		for _, sp := range sink.all(name) {
			org := attr(sp, "hanzo.org")
			seen[org]++
			if org != "acme" && org != "globex" {
				t.Fatalf("%s is filed under %q — neither tenant ran it", name, org)
			}
			// The person is scoped to the same tenant. "acme" carrying globex's
			// subject would be a cross-tenant disclosure inside a correctly
			// stamped row, which the org assertion alone cannot catch.
			if sub := attr(sp, "hanzo.user"); sub != "" && sub != "u-"+org {
				t.Fatalf("%s is filed under org %q but names user %q", name, org, sub)
			}
		}
	}
	if seen["acme"] == 0 || seen["globex"] == 0 {
		t.Fatalf("want spans from both tenants, got acme=%d globex=%d", seen["acme"], seen["globex"])
	}
}
