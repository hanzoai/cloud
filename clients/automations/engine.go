package automations

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
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
	// automationsTaskQueue is this workflow family's own queue (<service>-<purpose>);
	// nothing unrelated multiplexes onto it.
	automationsTaskQueue = "automations-flow"
	// resumeSignal is the signal a paused (wait_for_approval) flow blocks on; the
	// /v1/automations/runs/:id/resume endpoint sends it.
	resumeSignal = "resume"
	// flowRunWorkflowType is the registered workflow name — MUST equal the reflected
	// Go name of FlowRunWorkflow, so a cron schedule (which starts by name) resolves it.
	flowRunWorkflowType = "FlowRunWorkflow"
)

// ErrEngineNotReady is returned when cloud.EmbeddedTasks() is still nil (the engine
// is wired after MountAll). Handlers render it as 503 "automation engine not ready".
var ErrEngineNotReady = errors.New("automations: engine not ready")

// tokenSource is the ONE door to per-org credential custody. It defaults to
// integrations.TokenFor (KMS-sealed, fail-closed). It is a package var ONLY so a
// test can OBSERVE the (org,provider) a step tokenizes and prove the activity scopes
// every credential fetch to in.Owner — never to a client-supplied field. Production
// never reassigns it.
var tokenSource = integrations.TokenFor

// ── the durable workflow ─────────────────────────────────────────────────────

// FlowRunWorkflow walks the flow's flattened step chain. Each side-effecting step
// runs as a retried activity (ExecuteStepActivity); a core.wait_for_approval step
// is a durable PAUSE that blocks on the resume signal. Prior step outputs are
// threaded so later steps can reference them ({{step.field}}). Deterministic: it
// executes steps strictly in order, Get-ing each before dispatching the next.
func FlowRunWorkflow(ctx workflow.Context, in FlowRunInput) (FlowRunResult, error) {
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

	outputs := make(map[string]any, len(in.Steps))
	for _, step := range in.Steps {
		// wait_for_approval is the manual waitpoint: block on the durable resume
		// signal rather than executing an activity. The engine persists the signal,
		// so a resume survives a worker crash.
		if step.PieceName == corePiece && step.ActionName == "wait_for_approval" {
			var payload any
			workflow.GetSignalChannel(ctx, resumeSignal).Receive(ctx, &payload)
			outputs[step.Name] = payload
			continue
		}

		var out StepOutput
		si := StepInput{
			Owner:       in.Owner, // the isolation boundary — copied verbatim, never widened
			RunID:       in.RunID,
			Name:        step.Name,
			PieceName:   step.PieceName,
			ActionName:  step.ActionName,
			Input:       step.Input,
			PrevOutputs: outputs,
		}
		if err := workflow.ExecuteActivity(actCtx, ExecuteStepActivity, si).Get(actCtx, &out); err != nil {
			return FlowRunResult{RunID: in.RunID, Status: RunFailed, Outputs: outputs, Steps: len(outputs)}, err
		}
		outputs[step.Name] = out.Output
	}
	return FlowRunResult{RunID: in.RunID, Status: RunSucceeded, Outputs: outputs, Steps: len(outputs)}, nil
}

// ExecuteStepActivity is the side-effecting body of ONE step. It is THE isolation
// boundary: RunContext.Token is bound to in.Owner (the VALIDATED org set at
// flow-start) and the step's own connector id, so a flow authored by org A can
// never reach org B's connection nor another provider's secret. in.Owner is used
// for token custody and NOTHING from in.Input can change it.
func ExecuteStepActivity(ctx context.Context, in StepInput) (StepOutput, error) {
	_, action, err := lookupAction(in.PieceName, in.ActionName)
	if err != nil {
		return StepOutput{}, err
	}
	// Resolve {{step.field}} references against the threaded prior outputs before the
	// connector sees its input.
	resolved, _ := resolveRefs(in.Input, in.PrevOutputs).(map[string]any)

	owner := in.Owner // capture: the SOLE credential scope for this step
	rc := RunContext{
		Org:         owner,
		Input:       resolved,
		PrevOutputs: in.PrevOutputs,
		Token: func(secretName string) ([]byte, error) {
			return tokenSource(ctx, owner, in.PieceName, secretName)
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
	orgWorkers sync.Map // org → tasksclient.Client (its automations worker already started)
)

// orgEngineClient returns the org's tasks client, lazily provisioning its per-org
// automations worker on first use (CONTRACT §6: one client+worker per active org).
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
		HostPort:  fmt.Sprintf("127.0.0.1:%d", eng.ZAPPort()),
		Namespace: org,
	})
	if err != nil {
		return nil, fmt.Errorf("automations dial engine: %w", err)
	}
	w := tasksworker.New(cli, automationsTaskQueue, tasksworker.Options{})
	w.RegisterWorkflow(FlowRunWorkflow)
	w.RegisterActivity(ExecuteStepActivity)
	if err := w.Start(); err != nil {
		cli.Close()
		return nil, fmt.Errorf("automations worker start: %w", err)
	}
	orgWorkers.Store(org, cli)
	return cli, nil
}

// executeFlow starts a FlowRunWorkflow in the owner's namespace and returns the
// handle immediately (the caller never blocks on the run). in.Owner is the
// per-namespace scope; in.RunID is the workflow id (resume/describe address it).
func executeFlow(ctx context.Context, in FlowRunInput) (tasksclient.WorkflowRun, error) {
	cli, err := orgEngineClient(in.Owner)
	if err != nil {
		return nil, err
	}
	return cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{
		ID:        in.RunID,
		TaskQueue: automationsTaskQueue,
	}, FlowRunWorkflow, in)
}

// signalResume delivers the resume signal (with an optional approval payload) to a
// paused run in the org's namespace.
func signalResume(ctx context.Context, org, workflowID string, payload any) error {
	cli, err := orgEngineClient(org)
	if err != nil {
		return err
	}
	return cli.SignalWorkflow(ctx, workflowID, "", resumeSignal, payload)
}

// describeRunStatus reads the engine's current status for a run and maps it onto a
// FlowRunStatus. Scoped to the org's namespace, so it can only observe that org's runs.
func describeRunStatus(ctx context.Context, org, workflowID string) (FlowRunStatus, error) {
	cli, err := orgEngineClient(org)
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
	cli, err := orgEngineClient(org)
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
		TaskQueue:       automationsTaskQueue,
		Input:           []any{in},
	})
}

func disableSchedule(ctx context.Context, org, scheduleID string) error {
	cli, err := orgEngineClient(org)
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
