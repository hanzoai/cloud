package auto

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/cloud/sdk"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
)

// Durable execution on the ONE shared in-process hanzoai/tasks engine
// (cloud.EmbeddedTasks). A flow runs as a durable workflow in the OWNER's
// namespace: crash-recovering, retried, event-sourced. This mirrors ai's
// object/ingest_tasks.go per-org lazy-worker precedent exactly.

const (
	// taskQueue is this workflow family's own queue (<service>-<purpose>);
	// nothing unrelated multiplexes onto it.
	taskQueue = "automations-flow"
	// resumeSignal is the signal a paused (wait_for_approval) flow blocks on; the
	// /v1/auto/runs/:id/resume endpoint sends it.
	resumeSignal = "resume"
	// flowRunWorkflowType is the registered workflow name — MUST equal the reflected
	// Go name of FlowRunWorkflow, so a cron schedule (which starts by name) resolves it.
	flowRunWorkflowType = "FlowRunWorkflow"
)

// ErrEngineNotReady is returned when cloud.EmbeddedTasks() is still nil (the engine
// is wired after UseAll). Handlers render it as 503 "automation engine not ready".
var ErrEngineNotReady = errors.New("auto: engine not ready")

// tokenSource is the ONE entry point to per-org credential custody. It defaults to
// provider.TokenFor (KMS-sealed, fail-closed). It is a package var ONLY so a
// test can OBSERVE the (org,provider) a step tokenizes and prove the activity scopes
// every credential fetch to in.Owner — never to a client-supplied field. Production
// never reassigns it.
var tokenSource = provider.TokenFor

// ── the durable workflow ─────────────────────────────────────────────────────

// RunStartInput / RunEndInput are the durable run-bookkeeping activity payloads. The
// durable path is the SINGLE owner of run bookkeeping (MED-1): whichever entrypoint
// drives a workflow — manual /run, MCP, or a scheduled cron tick — records the run
// exactly once here, so metering/audit never double-count and every execution lands
// a FlowRun row visible to listRuns/getRun.
type (
	RunStartInput struct {
		RunID         string `json:"runId"`
		Owner         string `json:"owner"`
		FlowID        string `json:"flowId"`
		FlowVersionID string `json:"flowVersionId"`
		Exclusive     bool   `json:"exclusive,omitempty"`
		// Tick says a schedule started the run: its input names the schedule
		// ("sched-<flow>") and not the run.
		Tick bool `json:"tick,omitempty"`
		// At is when the workflow began, unix ms: for a tick, when the engine fired.
		At int64 `json:"at,omitempty"`
	}
	// RunStartResult says whether the run goes ahead. Skipped is a start of a flow
	// that runs one at a time while its previous run is live.
	RunStartResult struct {
		Skipped bool `json:"skipped,omitempty"`
	}
	RunEndInput struct {
		RunID  string `json:"runId"`
		Owner  string `json:"owner"`
		Status string `json:"status"`
	}
)

// FlowRunWorkflow walks the flow's flattened step chain. Each side-effecting step
// runs as a retried activity (ExecuteStepActivity); a core.wait_for_approval step
// is a durable PAUSE that blocks on the resume signal. Prior step outputs are
// threaded so later steps can reference them ({{step.field}}). Deterministic: it
// executes steps strictly in order, Get-ing each before dispatching the next.
//
// It also owns run bookkeeping (MED-1): the per-EXECUTION run id is the workflow id
// (workflow.GetInfo) — manual runs set it to the run id; a scheduled cron mints a
// fresh one per tick — so a run-start activity persists+meters+audits the run
// EXACTLY once, and a run-end activity records the terminal status.
func FlowRunWorkflow(ctx workflow.Context, in FlowRunInput) (FlowRunResult, error) {
	// The workflow id keys exactly-once bookkeeping. NOT in.RunID: a schedule embeds
	// one fixed FlowRunInput, so every tick shares in.RunID but gets a distinct
	// workflow id — the id that makes each tick its own metered run.
	runID := workflow.GetInfo(ctx).WorkflowID
	if runID == "" {
		runID = in.RunID
	}

	bkCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: time.Second, BackoffCoefficient: 2.0,
			MaximumInterval: 10 * time.Second, MaximumAttempts: 5,
		},
	})
	// Run-start bookkeeping is best-effort: a failure never blocks execution (the
	// activity is idempotent + retried, so transient errors recover on their own).
	// The one answer it gives that does stop the run is a skip: a flow that runs
	// one at a time was started while its previous run was live, and the start is
	// recorded as skipped instead of run.
	//
	// An automation fails closed instead: its bookkeeping IS its one-at-a-time
	// rule, its once-a-day rule and its gates, so a start that could not be
	// recorded does not run.
	var begun RunStartResult
	if err := workflow.ExecuteActivity(bkCtx, RecordRunStartActivity, RunStartInput{
		RunID: runID, Owner: in.Owner, FlowID: in.FlowID, FlowVersionID: in.FlowVersionID, Exclusive: in.Exclusive,
		Tick: in.RunID != runID, At: workflow.Now(ctx).UnixMilli(),
	}).Get(bkCtx, &begun); err != nil && in.Exclusive {
		_ = workflow.ExecuteActivity(bkCtx, RecordRunEndActivity, RunEndInput{RunID: runID, Owner: in.Owner, Status: string(RunFailed)}).Get(bkCtx, nil)
		return FlowRunResult{RunID: runID, Status: RunFailed}, err
	}
	if begun.Skipped {
		return FlowRunResult{RunID: runID, Status: RunSkipped}, nil
	}

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		// A step (HTTP call, provider API) can take a while; cap at 10m (never above).
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    3,
		},
	})

	outputs := make(map[string]any, len(in.Steps)+1)
	// Seed the inbound event so every downstream action can reference it as
	// {{trigger.field}} — the IFTTT data-flow: the event that fired the flow is the
	// first "output" the action chain reads. Nil for manual/cron starts.
	if in.Trigger != nil {
		outputs["trigger"] = in.Trigger
	}
	for _, step := range in.Steps {
		// wait_for_approval is the manual waitpoint: block on the durable resume
		// signal rather than executing an activity. The engine persists the signal,
		// so a resume survives a worker crash.
		if step.ProviderName == coreProvider && step.ActionName == "wait_for_approval" {
			var payload any
			workflow.GetSignalChannel(ctx, resumeSignal).Receive(ctx, &payload)
			outputs[step.Name] = payload
			continue
		}
		// agent starts a Dev run and waits for it durably (agent.go). It is not an
		// activity of its own: the start, each wait and the end are separate
		// activities, so a worker restart resumes the wait rather than starting a
		// second Dev run.
		//
		// Only in an automation: its person defined its instructions, it runs one
		// at a time, and its schedule is one the page validated. A step reached
		// any other way — a webhook flow whose instructions an event fills in —
		// would put someone else's words in its person's Dev run.
		if step.ProviderName == coreProvider && step.ActionName == agentAction {
			if !in.Exclusive {
				err := errors.New("an agent step runs only in an automation (/v1/auto/automations)")
				_ = workflow.ExecuteActivity(bkCtx, RecordRunEndActivity, RunEndInput{RunID: runID, Owner: in.Owner, Status: string(RunFailed)}).Get(bkCtx, nil)
				return FlowRunResult{RunID: runID, Status: RunFailed, Outputs: outputs, Steps: len(outputs)}, err
			}
			out, err := runAgent(ctx, in, runID, step)
			outputs[step.Name] = out
			if errors.Is(err, errRefused) {
				_ = workflow.ExecuteActivity(bkCtx, RecordRunEndActivity, RunEndInput{RunID: runID, Owner: in.Owner, Status: string(RunRefused)}).Get(bkCtx, nil)
				return FlowRunResult{RunID: runID, Status: RunRefused, Outputs: outputs, Steps: len(outputs)}, nil
			}
			if errors.Is(err, errUnvouched) {
				_ = workflow.ExecuteActivity(bkCtx, RecordRunEndActivity, RunEndInput{RunID: runID, Owner: in.Owner, Status: string(RunSkipped)}).Get(bkCtx, nil)
				return FlowRunResult{RunID: runID, Status: RunSkipped, Outputs: outputs, Steps: len(outputs)}, nil
			}
			if err != nil {
				_ = workflow.ExecuteActivity(bkCtx, RecordRunEndActivity, RunEndInput{RunID: runID, Owner: in.Owner, Status: string(RunFailed)}).Get(bkCtx, nil)
				return FlowRunResult{RunID: runID, Status: RunFailed, Outputs: outputs, Steps: len(outputs)}, err
			}
			continue
		}

		// post publishes the agent step's answer (post.go), only in an automation:
		// the accounts it posts to are its person's org's, chosen with its words.
		if step.ProviderName == coreProvider && step.ActionName == postAction {
			if !in.Exclusive {
				err := errors.New("a post step runs only in an automation (/v1/auto/automations)")
				_ = workflow.ExecuteActivity(bkCtx, RecordRunEndActivity, RunEndInput{RunID: runID, Owner: in.Owner, Status: string(RunFailed)}).Get(bkCtx, nil)
				return FlowRunResult{RunID: runID, Status: RunFailed, Outputs: outputs, Steps: len(outputs)}, err
			}
			out, status, err := runPost(ctx, in, runID, step, outputs)
			outputs[step.Name] = out
			_ = workflow.ExecuteActivity(bkCtx, RecordRunEndActivity, RunEndInput{RunID: runID, Owner: in.Owner, Status: string(status)}).Get(bkCtx, nil)
			return FlowRunResult{RunID: runID, Status: status, Outputs: outputs, Steps: len(outputs)}, err
		}

		var out StepOutput
		si := StepInput{
			Owner:        in.Owner, // the isolation boundary — copied verbatim, never widened
			RunID:        runID,
			Name:         step.Name,
			ProviderName: step.ProviderName,
			ActionName:   step.ActionName,
			Input:        step.Input,
			PrevOutputs:  outputs,
		}
		if err := workflow.ExecuteActivity(actCtx, ExecuteStepActivity, si).Get(actCtx, &out); err != nil {
			_ = workflow.ExecuteActivity(bkCtx, RecordRunEndActivity, RunEndInput{RunID: runID, Owner: in.Owner, Status: string(RunFailed)}).Get(bkCtx, nil)
			return FlowRunResult{RunID: runID, Status: RunFailed, Outputs: outputs, Steps: len(outputs)}, err
		}
		outputs[step.Name] = out.Output
	}
	_ = workflow.ExecuteActivity(bkCtx, RecordRunEndActivity, RunEndInput{RunID: runID, Owner: in.Owner, Status: string(RunSucceeded)}).Get(bkCtx, nil)
	return FlowRunResult{RunID: runID, Status: RunSucceeded, Outputs: outputs, Steps: len(outputs)}, nil
}

// RecordRunStartActivity persists the run row + meters + audits EXACTLY once
// (idempotent by run id via the store's metered-flag claim). The single owner of run
// bookkeeping for every entrypoint. mounted==nil (an engine-only test without Mount)
// is a no-op — nothing to record against.
func RecordRunStartActivity(ctx context.Context, in RunStartInput) (RunStartResult, error) {
	if mounted == nil {
		return RunStartResult{}, nil
	}
	return recordRunStart(mounted, ctx, in)
}

// RecordRunEndActivity records a run's terminal status so listRuns reflects it
// without a getRun refresh. Best-effort; mounted==nil is a no-op.
func RecordRunEndActivity(ctx context.Context, in RunEndInput) error {
	if mounted == nil {
		return nil
	}
	return recordRunEnd(mounted, ctx, in)
}

// ExecuteStepActivity is the side-effecting body of ONE step. It is THE isolation
// boundary: provider.RunContext.Token is bound to in.Owner (the VALIDATED org set at
// flow-start) and the step's own connector id, so a flow authored by org A can
// never reach org B's connection nor another provider's secret. in.Owner is used
// for token custody and NOTHING from in.Input can change it.
func ExecuteStepActivity(ctx context.Context, in StepInput) (StepOutput, error) {
	_, action, err := provider.ActionFor(in.ProviderName, in.ActionName)
	if err != nil {
		return StepOutput{}, err
	}
	// Resolve {{step.field}} references against the threaded prior outputs before the
	// connector sees its input.
	resolved, _ := resolveRefs(in.Input, in.PrevOutputs).(map[string]any)

	owner := in.Owner // capture: the SOLE credential scope for this step
	rc := provider.RunContext{
		Org:         owner,
		Input:       resolved,
		PrevOutputs: in.PrevOutputs,
		Token: func(secretName string) ([]byte, error) {
			return tokenSource(ctx, owner, in.ProviderName, secretName)
		},
	}
	out, err := action.Run(ctx, rc)
	if err != nil {
		return StepOutput{}, err
	}
	return StepOutput{Name: in.Name, Output: out}, nil
}

// ── per-org lazy worker (mirror ai/object/ingest_tasks.go) ───────────────────

var (
	provMu     sync.Mutex
	orgWorkers sync.Map // org → tasksclient.Client (its engine worker already started)
)

// orgEngineClient returns the org's tasks client, lazily provisioning its per-org
// engine worker on first use (CONTRACT §6: one client+worker per active org).
// provMu serializes first-touch so an org never gets two workers; the fast path is a
// lock-free sync.Map hit. Fails closed with ErrEngineNotReady until the shared engine
// is wired.
func orgEngineClient(org string) (tasksclient.Client, error) {
	if c, ok := orgWorkers.Load(org); ok {
		return c.(tasksclient.Client), nil
	}
	provMu.Lock()
	defer provMu.Unlock()
	if c, ok := orgWorkers.Load(org); ok {
		return c.(tasksclient.Client), nil
	}
	eng := cloud.EmbeddedTasks()
	if eng == nil {
		return nil, ErrEngineNotReady
	}
	cli, err := tasksclient.Dial(tasksclient.Options{
		Address:   eng.Address(),
		Namespace: org,
	})
	if err != nil {
		return nil, fmt.Errorf("auto: dial engine: %w", err)
	}
	w := tasksworker.New(cli, taskQueue, tasksworker.Options{})
	w.RegisterWorkflow(FlowRunWorkflow)
	w.RegisterActivity(ExecuteStepActivity)
	w.RegisterActivity(RecordRunStartActivity)
	w.RegisterActivity(RecordRunEndActivity)
	w.RegisterActivity(StartAgentActivity)
	w.RegisterActivity(AwaitAgentActivity)
	w.RegisterActivity(FinishAgentActivity)
	w.RegisterActivity(HoldPostActivity)
	w.RegisterActivity(PostActivity)
	w.RegisterActivity(NoteRunActivity)
	if err := w.Start(); err != nil {
		cli.Close()
		return nil, fmt.Errorf("auto: worker start: %w", err)
	}
	orgWorkers.Store(org, cli)
	return cli, nil
}

// resumeWorkers starts the engine worker of every org the engine may call on
// before anyone asks: one with an armed trigger, or a run still going. A worker is
// otherwise started by the first request that touches its org's engine, so after
// a restart a schedule fired into a namespace nobody polled, and its tick — or a
// Dev run's wait — sat until someone happened to open the page. It waits for the
// engine, which the process installs after Use (cloud serve.go
// installDurableIngest), and stops with the app.
func resumeWorkers(ctx context.Context, s *cloud.Service[state]) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for cloud.EmbeddedTasks() == nil {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
	orgs, err := s.State.store.ActiveOrgs(ctx)
	if err != nil {
		s.Log.Error("auto: the orgs to resume could not be read; their schedules wait for a request", "err", err)
		return
	}
	for _, org := range orgs {
		if _, err := engineClient(org); err != nil {
			s.Log.Error("auto: an org's worker did not start; its schedules wait for a request", "org", org, "err", err)
		}
	}
	s.Log.Info("auto: workers resumed", "orgs", len(orgs))
}

// engineClient is the org's engine client. It is orgEngineClient; it is a package
// var ONLY so a test can hand the org an engine of its own (mirrors runStarter).
// It is set in init because the worker orgEngineClient starts runs activities
// that disarm a schedule through it (agent.go turnOff), which a var initialiser
// cannot refer back to.
var engineClient func(org string) (tasksclient.Client, error)

func init() { engineClient = orgEngineClient }

// executeFlow starts a FlowRunWorkflow in the owner's namespace and returns the
// handle immediately (the caller never blocks on the run). in.Owner is the
// per-namespace scope; in.RunID is the workflow id (resume/describe address it).
func executeFlow(ctx context.Context, in FlowRunInput) (tasksclient.WorkflowRun, error) {
	cli, err := engineClient(in.Owner)
	if err != nil {
		return nil, err
	}
	return cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{
		ID:        in.RunID,
		TaskQueue: taskQueue,
	}, FlowRunWorkflow, in)
}

// runStarter is the ONE entry point Deliver uses to start a durable run. It defaults
// to executeFlow (the real engine). It is a package var ONLY so a test can OBSERVE the
// FlowRunInput a delivered event would start — proving org scope, payload threading,
// and exactly-once dispatch — without standing up the engine. Production never
// reassigns it. (Mirrors tokenSource above.)
var runStarter = executeFlow

// signalResume delivers the resume signal (with an optional approval payload) to a
// paused run in the org's namespace.
func signalResume(ctx context.Context, org, workflowID string, payload any) error {
	cli, err := engineClient(org)
	if err != nil {
		return err
	}
	return cli.SignalWorkflow(ctx, workflowID, "", resumeSignal, payload)
}

// describeRunStatus reads the engine's current status for a run and maps it onto a
// FlowRunStatus. Scoped to the org's namespace, so it can only observe that org's runs.
func describeRunStatus(ctx context.Context, org, workflowID string) (FlowRunStatus, error) {
	cli, err := engineClient(org)
	if err != nil {
		return "", err
	}
	info, err := cli.DescribeWorkflow(ctx, workflowID, "")
	if err != nil {
		return "", err
	}
	return mapWorkflowStatus(info.Status), nil
}

// enableSchedule registers a cron schedule that starts FlowRunWorkflow each tick in
// the org's namespace. disableSchedule removes it. NO bespoke ticker — the engine
// owns the cron.
func enableSchedule(ctx context.Context, org, scheduleID, cron string, in FlowRunInput) error {
	cli, err := engineClient(org)
	if err != nil {
		return err
	}
	// WorkflowID is left unset so the engine mints a unique id per tick (a fixed id
	// would make every tick collide on one execution). Input carries the flattened
	// run so each tick starts an identical FlowRunWorkflow.
	return cli.CreateSchedule(ctx, tasksclient.CreateScheduleOptions{
		ID:              scheduleID,
		CronExpressions: []string{cron},
		WorkflowType:    flowRunWorkflowType,
		TaskQueue:       taskQueue,
		Input:           []any{in},
	})
}

func disableSchedule(ctx context.Context, org, scheduleID string) error {
	cli, err := engineClient(org)
	if err != nil {
		return err
	}
	return cli.DeleteSchedule(ctx, scheduleID)
}

// mapWorkflowStatus maps the engine's workflow status onto the flow-run status.
func mapWorkflowStatus(s tasksclient.WorkflowStatus) FlowRunStatus {
	switch s {
	case tasksclient.WorkflowStatusCompleted:
		return RunSucceeded
	case tasksclient.WorkflowStatusFailed:
		return RunFailed
	case tasksclient.WorkflowStatusCanceled, tasksclient.WorkflowStatusTerminated:
		return RunCanceled
	case tasksclient.WorkflowStatusTimedOut:
		return RunTimeout
	default:
		return RunRunning
	}
}

// ── reference threading (data-mapper) ────────────────────────────────────────

// refRE matches a {{ step.path }} reference. The path is a dot-walk into the prior
// step outputs (e.g. "http1.status", "code.user.name").
var refRE = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

// resolveRefs deep-resolves {{step.path}} references in a value against the threaded
// prior outputs. A string that is EXACTLY one reference is replaced by the referenced
// VALUE (type preserved); a string with embedded references gets string substitution.
// Maps and slices are resolved recursively. Non-string scalars pass through.
func resolveRefs(v any, outputs map[string]any) any {
	switch x := v.(type) {
	case string:
		return resolveStringRefs(x, outputs)
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[k] = resolveRefs(val, outputs)
		}
		return m
	case []any:
		s := make([]any, len(x))
		for i, val := range x {
			s[i] = resolveRefs(val, outputs)
		}
		return s
	default:
		return v
	}
}

func resolveStringRefs(s string, outputs map[string]any) any {
	m := refRE.FindStringSubmatch(s)
	// Whole-string single reference → return the resolved value with its type.
	if m != nil && strings.TrimSpace(s) == m[0] {
		if val, ok := lookupPath(outputs, m[1]); ok {
			return val
		}
		return s
	}
	// Embedded references → string substitution.
	if m == nil {
		return s
	}
	return refRE.ReplaceAllStringFunc(s, func(match string) string {
		sub := refRE.FindStringSubmatch(match)
		if val, ok := lookupPath(outputs, sub[1]); ok {
			return fmt.Sprintf("%v", val)
		}
		return match
	})
}

// lookupPath walks a dot path ("step.field.sub") into a nested output map.
func lookupPath(outputs map[string]any, path string) (any, bool) {
	parts := strings.Split(strings.TrimSpace(path), ".")
	var cur any = outputs
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}
