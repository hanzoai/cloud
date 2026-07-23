package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
)

// seedWebhookFlow creates an ENABLED flow in org whose WEBHOOK trigger is
// (provider,event) and whose single action is the test probe reading {{trigger.msg}},
// then subscribes it — the state setEnabled would leave. Returns (flowID, versionID).
func seedWebhookFlow(t *testing.T, st *Store, org, provider, event string) (string, string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	fid := "flow_" + provider + "_" + org
	vid := "ver_" + provider + "_" + org
	if _, err := st.CreateFlow(ctx, Flow{ID: fid, Org: org, Status: FlowEnabled, Created: now, Updated: now}); err != nil {
		t.Fatalf("CreateFlow: %v", err)
	}
	v := FlowVersion{
		ID: vid, Org: org, FlowID: fid, DisplayName: "t", State: VersionLocked, Valid: true,
		SchemaVersion: LatestFlowSchemaVersion, Created: now, Updated: now,
		Trigger: &FlowTrigger{
			Name: "trigger", Type: TriggerTypePiece, Strategy: StrategyWebhook,
			Settings: StepSettings{PieceName: provider, TriggerName: event},
			NextAction: &FlowAction{
				Name: "a1", Type: ActionTypePiece,
				Settings: StepSettings{PieceName: "probe", ActionName: "record", Input: map[string]any{"seen": "{{trigger.msg}}"}},
			},
		},
	}
	if _, err := st.CreateVersion(ctx, v); err != nil {
		t.Fatalf("CreateVersion: %v", err)
	}
	if err := st.UpsertTrigger(ctx, org, provider, event, fid, vid); err != nil {
		t.Fatalf("UpsertTrigger: %v", err)
	}
	return fid, vid
}

// captureStarter swaps runStarter for a recorder of the FlowRunInputs a delivery would
// start, restoring it on cleanup. It proves dispatch WITHOUT standing up the engine.
func captureStarter(t *testing.T) *[]FlowRunInput {
	t.Helper()
	var (
		mu  sync.Mutex
		got []FlowRunInput
	)
	origStarter, origReady := runStarter, engineReady
	engineReady = func() bool { return true } // the durable engine is "up" for dispatch tests
	runStarter = func(_ context.Context, in FlowRunInput) (tasksclient.WorkflowRun, error) {
		mu.Lock()
		got = append(got, in)
		mu.Unlock()
		return nil, nil
	}
	t.Cleanup(func() { runStarter = origStarter; engineReady = origReady })
	return &got
}

// TestDeliverLoopBounded is the amplification proof (RED HIGH-1): a flow whose action
// re-fires the SAME event at the next causation depth forms an in-platform cycle; the
// depth guard MUST terminate it at maxCausationDepth instead of starting unboundedly.
func TestDeliverLoopBounded(t *testing.T) {
	newApp(t)
	origStarter, origReady := runStarter, engineReady
	engineReady = func() bool { return true }
	var starts int32
	runStarter = func(ctx context.Context, in FlowRunInput) (tasksclient.WorkflowRun, error) {
		atomic.AddInt32(&starts, 1)
		// The run's action re-enters Deliver at depth+1 (the loop, propagating causation).
		_, _ = Deliver(ctx, in.Owner, TriggerEvent{Source: "loop", Name: "tick", DedupeKey: strconv.Itoa(in.Depth + 1), Depth: in.Depth + 1})
		return nil, nil
	}
	t.Cleanup(func() { runStarter = origStarter; engineReady = origReady })
	seedWebhookFlow(t, mounted.State.store, "acme", "loop", "tick")

	n, err := Deliver(context.Background(), "acme", TriggerEvent{Source: "loop", Name: "tick", DedupeKey: "0", Depth: 0})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if n != 1 {
		t.Fatalf("first hop want 1 start, got %d", n)
	}
	// Starts happen at depths 0..maxCausationDepth-1, then the guard stops the cycle.
	if got := atomic.LoadInt32(&starts); got == 0 || int(got) > maxCausationDepth {
		t.Fatalf("in-platform loop not bounded by causation depth: %d starts (max %d)", got, maxCausationDepth)
	}
}

// TestDeliverRateCapped is the durable-budget proof (RED HIGH-1, the load-bearing one): a
// burst of DISTINCT events (distinct DedupeKey, so idempotency does not stop them) is
// bounded by the per-org run-start budget, counted from persisted rows.
func TestDeliverRateCapped(t *testing.T) {
	newApp(t)
	captureStarter(t)
	t.Setenv(runBudgetEnv, "3")
	seedWebhookFlow(t, mounted.State.store, "acme", "stripe", "charge")

	started := 0
	for i := 0; i < 10; i++ {
		n, err := Deliver(context.Background(), "acme", TriggerEvent{Source: "stripe", Name: "charge", DedupeKey: strconv.Itoa(i)})
		if err != nil && err != ErrRateLimited {
			t.Fatalf("Deliver %d: %v", i, err)
		}
		started += n
	}
	if started != 3 {
		t.Fatalf("durable per-org budget: want 3 started (cap 3), got %d", started)
	}
}

// TestDeliverEngineNotReadyRetryable is the MED-2 proof: a not-ready engine refuses BEFORE
// the run-row insert (the DedupeKey is NOT burned), so a redelivery after recovery starts.
func TestDeliverEngineNotReadyRetryable(t *testing.T) {
	newApp(t)
	origStarter, origReady := runStarter, engineReady
	engineReady = func() bool { return false } // engine down
	runStarter = func(context.Context, FlowRunInput) (tasksclient.WorkflowRun, error) {
		t.Fatal("must NOT dispatch while the engine is not ready")
		return nil, nil
	}
	t.Cleanup(func() { runStarter = origStarter; engineReady = origReady })
	st := mounted.State.store
	seedWebhookFlow(t, st, "acme", "github", "push")

	if _, err := Deliver(context.Background(), "acme", TriggerEvent{Source: "github", Name: "push", DedupeKey: "k"}); err != ErrEngineNotReady {
		t.Fatalf("engine down: want ErrEngineNotReady, got %v", err)
	}
	if rows, _ := st.ListRuns(context.Background(), "acme", "", 10); len(rows) != 0 {
		t.Fatalf("a not-ready engine must NOT burn the run id: %d rows persisted", len(rows))
	}
	// Engine recovers → the SAME key now starts (it was never burned).
	engineReady = func() bool { return true }
	started := 0
	runStarter = func(context.Context, FlowRunInput) (tasksclient.WorkflowRun, error) { started++; return nil, nil }
	n, err := Deliver(context.Background(), "acme", TriggerEvent{Source: "github", Name: "push", DedupeKey: "k"})
	if err != nil || n != 1 || started != 1 {
		t.Fatalf("redelivery after recovery: want 1 start, got n=%d started=%d err=%v", n, started, err)
	}
}

// TestInboundHookContentHashDedupe is the LOW-1 proof: two identical POSTs with NO
// idempotency key content-hash to the SAME run id, so the hook-hammer collapses to one run.
func TestInboundHookContentHashDedupe(t *testing.T) {
	app := newApp(t)
	captureStarter(t)
	seedWebhookFlow(t, mounted.State.store, "acme", "github", "push")

	body := map[string]any{"msg": "same"}
	r1 := req(t, app, http.MethodPost, "/v1/automations/hooks/github/push", "acme", body)
	r2 := req(t, app, http.MethodPost, "/v1/automations/hooks/github/push", "acme", body)
	if r1.Code != http.StatusOK || r2.Code != http.StatusOK {
		t.Fatalf("hook codes want 200/200, got %d/%d", r1.Code, r2.Code)
	}
	var m1, m2 struct {
		Matched int `json:"matched"`
	}
	_ = json.Unmarshal(r1.Body, &m1)
	_ = json.Unmarshal(r2.Body, &m2)
	if m1.Matched != 1 {
		t.Fatalf("first keyless POST want matched 1, got %d", m1.Matched)
	}
	if m2.Matched != 0 {
		t.Fatalf("identical hammer POST want matched 0 (content-hash dedup), got %d", m2.Matched)
	}
	if rows, _ := mounted.State.store.ListRuns(context.Background(), "acme", "", 10); len(rows) != 1 {
		t.Fatalf("identical keyless POSTs must collapse to ONE run, got %d", len(rows))
	}
}

// TestDeliverFiresMatchingFlowThreaded is the IFTTT happy path: an inbound event fires
// the ONE subscribed flow, with the org as the credential scope and the event payload
// threaded in as the trigger input.
func TestDeliverFiresMatchingFlowThreaded(t *testing.T) {
	newApp(t) // sets the package `mounted` Deliver reads
	st := mounted.State.store
	fid, vid := seedWebhookFlow(t, st, "acme", "github", "push")
	got := captureStarter(t)

	payload := map[string]any{"msg": "hello", "ref": "refs/heads/main"}
	n, err := Deliver(context.Background(), "acme", TriggerEvent{Source: "github", Name: "push", DedupeKey: "sha1", Payload: payload})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if n != 1 {
		t.Fatalf("started want 1, got %d", n)
	}
	if len(*got) != 1 {
		t.Fatalf("runStarter calls want 1, got %d", len(*got))
	}
	in := (*got)[0]
	if in.Owner != "acme" {
		t.Fatalf("owner (credential scope) want acme, got %q", in.Owner)
	}
	if in.FlowID != fid || in.FlowVersionID != vid {
		t.Fatalf("flow/version want %s/%s, got %s/%s", fid, vid, in.FlowID, in.FlowVersionID)
	}
	if in.Trigger["msg"] != "hello" {
		t.Fatalf("trigger payload not threaded into the run: %+v", in.Trigger)
	}
	if len(in.Steps) != 1 || in.Steps[0].PieceName != "probe" {
		t.Fatalf("steps not flattened from the flow: %+v", in.Steps)
	}
	if _, err := st.GetRun(context.Background(), "acme", in.RunID); err != nil {
		t.Fatalf("run row must be persisted for listRuns/idempotency: %v", err)
	}
}

// TestDeliverTenantIsolation proves an event delivered for one org can NEVER start
// another org's flow: the match index is physically org-scoped, and Deliver refuses to
// widen the credential scope.
func TestDeliverTenantIsolation(t *testing.T) {
	newApp(t)
	st := mounted.State.store
	seedWebhookFlow(t, st, "acme", "github", "push")
	got := captureStarter(t)

	// An event for a DIFFERENT org matches nothing and starts nothing.
	n, err := Deliver(context.Background(), "globex", TriggerEvent{Source: "github", Name: "push", DedupeKey: "x"})
	if err != nil {
		t.Fatalf("Deliver globex: %v", err)
	}
	if n != 0 {
		t.Fatalf("cross-tenant event started %d runs (must be 0)", n)
	}
	if len(*got) != 0 {
		t.Fatalf("runStarter fired %d times for a foreign org", len(*got))
	}
	// The store index is physically org-scoped.
	if subs, _ := st.MatchTriggers(context.Background(), "globex", "github", "push"); len(subs) != 0 {
		t.Fatalf("globex matched %d of acme's subscriptions", len(subs))
	}
	if subs, _ := st.MatchTriggers(context.Background(), "acme", "github", "push"); len(subs) != 1 {
		t.Fatalf("acme matched %d subscriptions (want 1)", len(subs))
	}
	// The OWNING org does fire.
	n, err = Deliver(context.Background(), "acme", TriggerEvent{Source: "github", Name: "push", DedupeKey: "y"})
	if err != nil {
		t.Fatalf("Deliver acme: %v", err)
	}
	if n != 1 {
		t.Fatalf("owner event started %d runs (want 1)", n)
	}
}

// TestDeliverIdempotent proves a re-delivered event (same DedupeKey) fires a flow AT
// MOST ONCE — the run-row insert is the atomic gate.
func TestDeliverIdempotent(t *testing.T) {
	newApp(t)
	st := mounted.State.store
	seedWebhookFlow(t, st, "acme", "stripe", "charge")
	got := captureStarter(t)

	ev := TriggerEvent{Source: "stripe", Name: "charge", DedupeKey: "evt_123", Payload: map[string]any{"amount": 100}}
	n1, err := Deliver(context.Background(), "acme", ev)
	if err != nil {
		t.Fatalf("first Deliver: %v", err)
	}
	n2, err := Deliver(context.Background(), "acme", ev)
	if err != nil {
		t.Fatalf("second Deliver: %v", err)
	}
	if n1 != 1 {
		t.Fatalf("first delivery started %d (want 1)", n1)
	}
	if n2 != 0 {
		t.Fatalf("re-delivery started %d (want 0 — idempotent)", n2)
	}
	if len(*got) != 1 {
		t.Fatalf("runStarter fired %d times for a re-delivered event (want 1)", len(*got))
	}
	rows, _ := st.ListRuns(context.Background(), "acme", "", 100)
	if len(rows) != 1 {
		t.Fatalf("run rows want 1, got %d", len(rows))
	}
}

// TestDeliverFailsClosedWithoutOrg proves Deliver refuses a missing/invalid org and
// starts nothing (the org is the sole tenant key; it must never default).
func TestDeliverFailsClosedWithoutOrg(t *testing.T) {
	newApp(t)
	got := captureStarter(t)
	for _, org := range []string{"", "bad/org", "a b"} {
		if _, err := Deliver(context.Background(), org, TriggerEvent{Source: "github", Name: "push"}); err != ErrNoOrg {
			t.Fatalf("org %q must return ErrNoOrg, got %v", org, err)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("runStarter fired %d times on a fail-closed org", len(*got))
	}
}

// TestTriggerPayloadThreadsThroughDurableRun proves the OTHER half end-to-end on a REAL
// tasks engine: the seeded {{trigger.field}} resolves to the delivered event's value in
// a downstream action (the workflow seeds outputs["trigger"] = in.Trigger).
func TestTriggerPayloadThreadsThroughDurableRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	srv, err := tasksengine.Embed(ctx, tasksengine.EmbedConfig{ZAPPort: port, Namespace: "acme", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	defer func() { _ = srv.Stop(context.Background()) }()

	cli, err := tasksclient.Dial(tasksclient.Options{
		HostPort: fmt.Sprintf("127.0.0.1:%d", port), Namespace: "acme",
		DialTimeout: 5 * time.Second, CallTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()

	w := tasksworker.New(cli, automationsTaskQueue, tasksworker.Options{})
	w.RegisterWorkflow(FlowRunWorkflow)
	w.RegisterActivity(ExecuteStepActivity)
	w.RegisterActivity(RecordRunStartActivity)
	w.RegisterActivity(RecordRunEndActivity)
	if err := w.Start(); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	defer w.Stop()

	probeReset()
	in := FlowRunInput{
		Owner: "acme", FlowID: "f1", FlowVersionID: "v1", RunID: "run_trigger_1",
		Trigger: map[string]any{"msg": "charged"},
		Steps: []FlowStep{
			{Name: "probe1", PieceName: "probe", ActionName: "record", Input: map[string]any{"seen": "{{trigger.msg}}"}},
		},
	}
	run, err := cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{ID: in.RunID, TaskQueue: automationsTaskQueue}, FlowRunWorkflow, in)
	if err != nil {
		t.Fatalf("execute workflow: %v", err)
	}
	if err := run.Get(ctx, nil); err != nil {
		t.Fatalf("flow did not reach SUCCEEDED: %v", err)
	}
	if seen := probeSnapshot(); len(seen) != 1 || seen[0] != "charged" {
		t.Fatalf("trigger payload not threaded to the action: probe saw %v", seen)
	}
}

// TestInboundHookOrgGatedAndDispatches proves the HTTP sink is org-gated (anonymous →
// 403) and, for a validated caller, dispatches to the matching flow.
func TestInboundHookOrgGatedAndDispatches(t *testing.T) {
	app := newApp(t)
	seedWebhookFlow(t, mounted.State.store, "acme", "github", "push")
	captureStarter(t)

	if got := req(t, app, http.MethodPost, "/v1/automations/hooks/github/push", "", map[string]any{"msg": "x"}); got.Code != http.StatusForbidden {
		t.Fatalf("anonymous hook want 403, got %d", got.Code)
	}
	got := req(t, app, http.MethodPost, "/v1/automations/hooks/github/push", "acme", map[string]any{"msg": "hi"})
	if got.Code != http.StatusOK {
		t.Fatalf("authenticated hook want 200, got %d: %s", got.Code, got.Body)
	}
	var resp struct {
		Matched int `json:"matched"`
	}
	if err := json.Unmarshal(got.Body, &resp); err != nil || resp.Matched != 1 {
		t.Fatalf("hook matched want 1, got %d (err=%v, body=%s)", resp.Matched, err, got.Body)
	}
}

// TestEnableDisableMaintainsWebhookSubscription proves the full lifecycle over HTTP:
// create → enable subscribes → deliver fires → disable unsubscribes → deliver is a no-op.
func TestEnableDisableMaintainsWebhookSubscription(t *testing.T) {
	app := newApp(t)
	captureStarter(t)

	create := map[string]any{
		"displayName": "on push",
		"trigger": map[string]any{
			"name": "trigger", "type": TriggerTypePiece, "strategy": string(StrategyWebhook),
			"settings": map[string]any{"pieceName": "github", "triggerName": "push"},
			"nextAction": map[string]any{
				"name": "a1", "type": ActionTypePiece,
				"settings": map[string]any{"pieceName": "probe", "actionName": "record", "input": map[string]any{"seen": "{{trigger.ref}}"}},
			},
		},
	}
	res := req(t, app, http.MethodPost, "/v1/automations/flows", "acme", create)
	if res.Code != http.StatusCreated {
		t.Fatalf("create flow want 201, got %d: %s", res.Code, res.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(res.Body, &created); err != nil || created.ID == "" {
		t.Fatalf("bad create response: %s", res.Body)
	}
	st := mounted.State.store

	// Before enable: no subscription.
	if subs, _ := st.MatchTriggers(context.Background(), "acme", "github", "push"); len(subs) != 0 {
		t.Fatalf("disabled flow is already subscribed: %d", len(subs))
	}
	// Enable → subscribes; delivery fires.
	if r := req(t, app, http.MethodPost, "/v1/automations/flows/"+created.ID+"/enable", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("enable want 200, got %d: %s", r.Code, r.Body)
	}
	if subs, _ := st.MatchTriggers(context.Background(), "acme", "github", "push"); len(subs) != 1 {
		t.Fatalf("enable did not subscribe the webhook trigger: %d", len(subs))
	}
	if n, err := Deliver(context.Background(), "acme", TriggerEvent{Source: "github", Name: "push", DedupeKey: "s1"}); err != nil || n != 1 {
		t.Fatalf("enabled delivery want 1, got %d (err=%v)", n, err)
	}
	// Disable → unsubscribes; delivery is a no-op.
	if r := req(t, app, http.MethodPost, "/v1/automations/flows/"+created.ID+"/disable", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("disable want 200, got %d: %s", r.Code, r.Body)
	}
	if subs, _ := st.MatchTriggers(context.Background(), "acme", "github", "push"); len(subs) != 0 {
		t.Fatalf("disable did not unsubscribe: %d", len(subs))
	}
	if n, err := Deliver(context.Background(), "acme", TriggerEvent{Source: "github", Name: "push", DedupeKey: "s2"}); err != nil || n != 0 {
		t.Fatalf("disabled delivery want 0, got %d (err=%v)", n, err)
	}
}

// TestWebhookKey unit-checks the trigger-strategy gate: only WEBHOOK/APP_WEBHOOK with a
// non-empty (piece,trigger) subscribes; polling/manual/nil/load-error do not.
func TestWebhookKey(t *testing.T) {
	mk := func(strategy TriggerStrategy, piece, trig string) FlowVersion {
		return FlowVersion{Trigger: &FlowTrigger{Strategy: strategy, Settings: StepSettings{PieceName: piece, TriggerName: trig}}}
	}
	cases := []struct {
		name   string
		v      FlowVersion
		verr   error
		wantOK bool
		p, e   string
	}{
		{"webhook", mk(StrategyWebhook, "github", "push"), nil, true, "github", "push"},
		{"app_webhook", mk(StrategyAppWebhook, "slack", "message"), nil, true, "slack", "message"},
		{"polling is not a webhook", mk(StrategyPolling, "core", "schedule"), nil, false, "", ""},
		{"manual is not a webhook", mk(StrategyManual, "core", "manual"), nil, false, "", ""},
		{"webhook missing piece", mk(StrategyWebhook, "", "push"), nil, false, "", ""},
		{"webhook missing event", mk(StrategyWebhook, "github", ""), nil, false, "", ""},
		{"load error", mk(StrategyWebhook, "github", "push"), errNotFound, false, "", ""},
		{"nil trigger", FlowVersion{}, nil, false, "", ""},
	}
	for _, tc := range cases {
		p, e, ok := webhookKey(tc.v, tc.verr)
		if ok != tc.wantOK || p != tc.p || e != tc.e {
			t.Fatalf("%s: got (%q,%q,%v) want (%q,%q,%v)", tc.name, p, e, ok, tc.p, tc.e, tc.wantOK)
		}
	}
}
