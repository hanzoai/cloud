package agents

// The budget, exercised the way a customer meets it: over HTTP, on the shipped
// routes, with billing answered by a fake commerce that has plenty of money —
// so every refusal below is the AGENT'S budget and never the org's balance.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	cloud "github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// tokenAI answers every completion with a fixed token count, so what the run
// spends is arithmetic and not a guess.
type tokenAI struct {
	prompt, completion int
	calls              int
}

func (f *tokenAI) ChatCompletion(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
	f.calls++
	return &types.ChatResponse{Content: "done", PromptTokens: f.prompt, CompletionTokens: f.completion}, nil
}
func (f *tokenAI) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) { return nil, nil }
func (f *tokenAI) Rerank(context.Context, *types.RerankRequest) ([]float64, error) { return nil, nil }

// rich is a commerce balance no test below can exhaust: the org is never the
// thing that refuses.
const rich = 1_000_000_000

func createBudgeted(t *testing.T, app *zip.App, org, name string, cap, task int64, period string) {
	t.Helper()
	code, body := do(t, app, http.MethodPost, "/v1/agent", org, map[string]any{
		"name": name, "model": "gpt-4o-mini", "instructions": "x",
		"cap_micro_usd": cap, "max_task_micro_usd": task, "period": period,
	})
	if code != http.StatusCreated {
		t.Fatalf("create %s want 201, got %d (%s)", name, code, body)
	}
}

func TestAgentIsNotCreatableWithoutABudget(t *testing.T) {
	app := mountApp(t, &tokenAI{prompt: 10, completion: 10})
	code, body := do(t, app, http.MethodPost, "/v1/agent", "acme",
		map[string]any{"name": "a", "model": "gpt-4o-mini", "instructions": "x"})
	if code != http.StatusBadRequest {
		t.Fatalf("no budget want 400, got %d (%s)", code, body)
	}
	if !strings.Contains(string(body), "cap_micro_usd") {
		t.Fatalf("the refusal must name the field, got %s", body)
	}
	for _, bad := range []map[string]any{
		{"cap_micro_usd": 100, "max_task_micro_usd": 0, "period": "day"},
		{"cap_micro_usd": 100, "max_task_micro_usd": 200, "period": "day"},
		{"cap_micro_usd": 100, "max_task_micro_usd": 50, "period": "fortnight"},
	} {
		bad["name"], bad["model"], bad["instructions"] = "a", "gpt-4o-mini", "x"
		if code, body := do(t, app, http.MethodPost, "/v1/agent", "acme", bad); code != http.StatusBadRequest {
			t.Fatalf("%v want 400, got %d (%s)", bad, code, body)
		}
	}
}

func TestACallOverTheCapIsRefusedAndNothingIsBilled(t *testing.T) {
	bs := &billServer{available: rich}
	ai := &tokenAI{prompt: 10, completion: 10}
	app := mountBilled(t, bs.start(t), ai)
	// One micro-USD of cap: the first quote is thousands.
	createBudgeted(t, app, "acme", "tight", 1, 1, "day")
	code, body := do(t, app, http.MethodPost, "/v1/agent/tight/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusPaymentRequired {
		t.Fatalf("over cap want 402, got %d (%s)", code, body)
	}
	var refused struct {
		Code   string         `json:"code"`
		Detail map[string]any `json:"detail"`
	}
	_ = json.Unmarshal(body, &refused)
	if !strings.Contains(string(body), codeBudgetExceeded) {
		t.Fatalf("refusal must carry %q, got %s", codeBudgetExceeded, body)
	}
	// WHICH SCOPE REFUSED, in the message. A completion route answers in the
	// OpenAI error envelope — `{"error":{code,message}}` — which has nowhere to
	// put an extension member, so `Detail` renders only where zip writes an RFC
	// 9457 document. The scope therefore rides the sentence, and the structured
	// event reaches the agent on the session stream, which is the in-band channel
	// and is measured below.
	if !strings.Contains(string(body), "agent budget exceeded") {
		t.Fatalf("the refusal names the scope that refused, got %s", body)
	}
	if ai.calls != 0 {
		t.Fatalf("a refused call must not reach the model, got %d calls", ai.calls)
	}
	if bs.debits() != 0 {
		t.Fatalf("a refused call must not debit, got %d", bs.debits())
	}
	// And the ledger agrees: nothing was consumed.
	code, body = do(t, app, http.MethodGet, "/v1/agent/tight/spend", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("spend want 200, got %d (%s)", code, body)
	}
	var sp spendView
	_ = json.Unmarshal(body, &sp)
	if sp.ConsumedMicroUSD != 0 || len(sp.ByComponent) != 0 {
		t.Fatalf("nothing should be consumed after a refusal, got %+v", sp)
	}
}

func TestACallUnderTheCapProceedsAndIsMetered(t *testing.T) {
	bs := &billServer{available: rich}
	ai := &tokenAI{prompt: 100, completion: 50}
	app := mountBilled(t, bs.start(t), ai)
	createBudgeted(t, app, "acme", "roomy", 10_000_000, 1_000_000, "month")
	code, body := do(t, app, http.MethodPost, "/v1/agent/roomy/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusOK {
		t.Fatalf("under cap want 200, got %d (%s)", code, body)
	}
	want := cloud.InferenceMicros(150)
	if want <= 0 {
		t.Fatalf("the inference rate must price 150 tokens above zero, got %d", want)
	}
	var run struct {
		MicroUSD int64 `json:"micro_usd"`
	}
	_ = json.Unmarshal(body, &run)
	if run.MicroUSD != want {
		t.Fatalf("run must carry what it spent: want %d micro-USD, got %d (%s)", want, run.MicroUSD, body)
	}
	code, body = do(t, app, http.MethodGet, "/v1/agent/roomy/spend?by=component", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("spend want 200, got %d (%s)", code, body)
	}
	var sp spendView
	_ = json.Unmarshal(body, &sp)
	if sp.ConsumedMicroUSD != want {
		t.Fatalf("consumed want %d, got %d", want, sp.ConsumedMicroUSD)
	}
	if sp.RemainingMicroUSD != 10_000_000-want {
		t.Fatalf("remaining want %d, got %d", 10_000_000-want, sp.RemainingMicroUSD)
	}
	if sp.ByComponent[cloud.ComponentModel] != want {
		t.Fatalf("model component want %d, got %v", want, sp.ByComponent)
	}
	if _, has := sp.ByComponent[cloud.ComponentComputer]; has {
		t.Fatalf("a component with no spend must be absent, got %v", sp.ByComponent)
	}
}

func TestTheTaskCeilingRefusesWhatThePeriodWouldAllow(t *testing.T) {
	bs := &billServer{available: rich}
	app := mountBilled(t, bs.start(t), &tokenAI{prompt: 10, completion: 10})
	createBudgeted(t, app, "acme", "capped", 10_000_000, 1, "week")
	code, body := do(t, app, http.MethodPost, "/v1/agent/capped/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusPaymentRequired || !strings.Contains(string(body), "task budget exceeded") {
		t.Fatalf("task ceiling want 402 naming the task scope, got %d (%s)", code, body)
	}
}

func TestThePeriodResets(t *testing.T) {
	sto := testStore(t)
	ctx := context.Background()
	a := mk("acme", "nightly")
	a.CapMicroUSD, a.MaxTaskMicroUSD, a.Period = 1_000, 1_000, PeriodDay
	a.ConsumedMicroUSD = 900
	a.PeriodStartedAt = periodStart(time.Now().Add(-48*time.Hour), PeriodDay)
	if err := sto.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	// 900 of 1,000 spent in a window that has since closed: a quote of 500 fits
	// the NEW window, and the old spend does not follow it in.
	st := &state{}
	if err := st.afford(ctx, sto, &a, &runTally{ID: "r"}, nil, quote{Micros: 500, Component: cloud.ComponentModel}); err != nil {
		t.Fatalf("a fresh window must admit the call, got %v", err)
	}
	if a.ConsumedMicroUSD != 0 || a.PeriodStartedAt != periodStart(time.Now(), PeriodDay) {
		t.Fatalf("window not reset: consumed=%d started=%d", a.ConsumedMicroUSD, a.PeriodStartedAt)
	}
	got, err := sto.Get(ctx, "acme", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if got.ConsumedMicroUSD != 0 {
		t.Fatalf("the store must hold the reset too, got %d", got.ConsumedMicroUSD)
	}
	// The same window, now 900 spent again: 500 more is refused.
	st.settle(ctx, sto, &a, &runTally{ID: "r"}, nil, cloud.ComponentModel, 900)
	if err := st.afford(ctx, sto, &a, &runTally{ID: "r"}, nil, quote{Micros: 500, Component: cloud.ComponentModel}); !isBudgetRefusal(err) {
		t.Fatalf("over the cap in the current window must refuse, got %v", err)
	}
}

func TestSessionBudgetRules(t *testing.T) {
	app := mountApp(t, &tokenAI{prompt: 1, completion: 1})
	newSession := func(budget int64) string {
		t.Helper()
		body := map[string]any{"agent": "worker", "title": "t"}
		if budget > 0 {
			body["budget_micro_usd"] = budget
		}
		code, out := do(t, app, http.MethodPost, "/v1/agent/sessions", "acme", body)
		if code != http.StatusCreated {
			t.Fatalf("register want 201, got %d (%s)", code, out)
		}
		var v struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(out, &v)
		return v.ID
	}
	set := func(id string, body map[string]any) (int, []byte) {
		return do(t, app, http.MethodPost, "/v1/agent/sessions/"+id+"/budget", "acme", body)
	}

	// A raise takes; a cap at or below what was consumed does not.
	id := newSession(100)
	sto, err := mounted.State.storeFor("acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := sto.Consume(context.Background(), "acme", "worker", "", id, cloud.ComponentModel, 60); err != nil {
		t.Fatal(err)
	}
	if code, out := set(id, map[string]any{"budget_micro_usd": 60}); code != http.StatusBadRequest {
		t.Fatalf("a cap not above consumed want 400, got %d (%s)", code, out)
	}
	if code, out := set(id, map[string]any{"budget_micro_usd": 500}); code != http.StatusOK || !strings.Contains(string(out), `"budget_micro_usd":500`) {
		t.Fatalf("raise want 200 at 500, got %d (%s)", code, out)
	}
	// Removal is one-way.
	if code, out := set(id, map[string]any{"budget_micro_usd": nil}); code != http.StatusOK || !strings.Contains(string(out), `"budget_removed":true`) {
		t.Fatalf("remove want 200 removed, got %d (%s)", code, out)
	}
	if code, out := set(id, map[string]any{"budget_micro_usd": 900}); code != http.StatusConflict {
		t.Fatalf("a cap after removal want 409, got %d (%s)", code, out)
	}
	// A session born without a cap cannot be given one.
	bare := newSession(0)
	if code, out := set(bare, map[string]any{"budget_micro_usd": 10}); code != http.StatusConflict {
		t.Fatalf("a cap on a capless session want 409, got %d (%s)", code, out)
	}
}

func TestASessionPausesAtItsCapAndResumesWhenRaised(t *testing.T) {
	app := mountApp(t, &tokenAI{prompt: 1, completion: 1})
	code, out := do(t, app, http.MethodPost, "/v1/agent/sessions", "acme",
		map[string]any{"agent": "worker", "title": "t", "budget_micro_usd": 10})
	if code != http.StatusCreated {
		t.Fatalf("register want 201, got %d (%s)", code, out)
	}
	var v struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(out, &v)
	sto, err := mounted.State.storeFor("acme")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sess, err := sto.GetSession(ctx, "acme", v.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := mk("acme", "worker")
	a.CapMicroUSD, a.MaxTaskMicroUSD, a.Period = 1_000_000, 1_000_000, PeriodMonth
	a.PeriodStartedAt = periodStart(time.Now(), PeriodMonth)
	if err := sto.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	st := &mounted.State
	err = st.afford(ctx, sto, &a, &runTally{ID: "r"}, &sess, quote{Micros: 20, Component: cloud.ComponentModel})
	if !isBudgetRefusal(err) || !strings.Contains(err.Error(), "session") {
		t.Fatalf("over the session cap must refuse for the session, got %v", err)
	}
	// Paused, and told.
	got, _ := sto.GetSession(ctx, "acme", v.ID)
	if got.Status != StatusPaused {
		t.Fatalf("session must pause at its cap, got %s", got.Status)
	}
	last, ok, _ := sto.LastEvent(ctx, "acme", v.ID)
	if !ok || !strings.Contains(last.Payload, eventBudgetExceeded) {
		t.Fatalf("the session stream must carry %s, got %+v", eventBudgetExceeded, last)
	}
	// Raised: running again.
	code, out = do(t, app, http.MethodPost, "/v1/agent/sessions/"+v.ID+"/budget", "acme", map[string]any{"budget_micro_usd": 100})
	if code != http.StatusOK || !strings.Contains(string(out), `"status":"running"`) {
		t.Fatalf("raise must resume, got %d (%s)", code, out)
	}
}
