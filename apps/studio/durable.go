package studio

// durable.go — a run as a durable workflow on this process's tasks engine,
// scheduled by flow/core.
//
// THE SCHEDULER IS flow/core's, IN Durable MODE. The workflow function builds the
// run's graph as a flow/core graph and hands it to flow's one Scheduler with a
// Durable backend (stepper, below): the scheduler walks the DAG in its own
// deterministic order, and the backend runs each step as activities on the engine.
// That is the seam flow/core's mode.go names — the same scheduler a GPU-resident
// backend will run the same graphs under (native), with nothing else changed.
//
// REPLAYED, NOT RESUMED. A workflow task carries the run's history; the worker
// re-runs the function from the top and every ExecuteActivity resolves from history
// by sequence number, so a step that finished is never run again — its result comes
// back from history. The function decides only from its input and recorded results:
// no clock, no store read, no randomness. Those live in the activities. Recover, at
// boot, re-dispatches every activity that was open when the process died.
//
// A STEP IS ITS PARTS. A step runs as one part, or one part per item of its each,
// all dispatched together (ExecuteActivity N times, then Get each), so a fan-out
// runs in parallel and a fan-out killed half way keeps the half that finished. A
// part also records its own result in the store before it answers, so a retried run
// (a new workflow over the same run) finds every part that already succeeded and
// neither runs nor charges it again.
//
// One namespace in the engine's root shard serves every org; the org a run acts
// for is its input, stated once from the validated caller.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	flow "github.com/hanzoai/flow/core"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	"github.com/zap-proto/zip"
)

// The engine side.
const (
	engineNS    = "studio"
	engineQueue = "studio"
	runName     = "studio.run"
	actLoad     = "studio.load"
	actPart     = "studio.part"
	actStep     = "studio.step"
	actClose    = "studio.close"
)

// Job is one execution of a run: the run, the org it acts for, and which attempt
// (0, then one more per retry).
type Job struct {
	Org     string `json:"org"`
	Run     string `json:"run"`
	Attempt int    `json:"attempt"`
}

// Plan is what a run executes, read once by studio.load and then fixed in history.
type Plan struct {
	Graph Graph               `json:"graph"`
	Deps  map[string][]string `json:"deps"`
	Input map[string]any      `json:"input"`
	Brand map[string]any      `json:"brand"`
}

// Part is one unit of one step: its op and its parameters with every reference
// resolved.
type Part struct {
	Org     string         `json:"org"`
	Run     string         `json:"run"`
	Attempt int            `json:"attempt"`
	Step    string         `json:"step"`
	Item    int            `json:"item"`
	Items   int            `json:"items"`
	Op      string         `json:"op"`
	With    map[string]any `json:"with"`
}

// Settle ends one step: its parts' results, or why it failed.
type Settle struct {
	Org     string   `json:"org"`
	Run     string   `json:"run"`
	Step    string   `json:"step"`
	Items   int      `json:"items"`
	Results []result `json:"results"`
	Fault   string   `json:"fault,omitempty"`
}

// Close ends a run.
type Close struct {
	Org    string `json:"org"`
	Run    string `json:"run"`
	Status string `json:"status"`
	Fault  string `json:"fault,omitempty"`
}

// The run statuses, and the step statuses beyond them.
const (
	queued    = "queued"
	running   = "running"
	succeeded = "succeeded"
	failed    = "failed"
	canceled  = "canceled"
	pending   = "pending"
)

// errCanceled is a part or a run that was cancelled while it ran.
const codeCanceled = "Canceled"

// runFlow is the workflow: load the plan, run its graph under flow/core's Durable
// mode, close the run.
func runFlow(ctx workflow.Context, j Job) (string, error) {
	quick := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: time.Second, BackoffCoefficient: 2,
			MaximumInterval: 30 * time.Second, MaximumAttempts: 10,
		},
	})
	var p Plan
	if err := workflow.ExecuteActivity(quick, actLoad, j).Get(quick, &p); err != nil {
		return closeRun(quick, j, failed, "the run could not be read: "+err.Error())
	}
	g, err := build(p.Graph, p.Deps, nil)
	if err != nil {
		return closeRun(quick, j, failed, err.Error())
	}
	b := &stepper{ctx: ctx, quick: quick, job: j, plan: p}
	if _, err := flow.New(b).Run(context.Background(), g, flow.Durable, nil); err != nil {
		if b.stopped {
			return canceled, nil
		}
		return closeRun(quick, j, failed, strings.TrimPrefix(err.Error(), "flow: "))
	}
	return closeRun(quick, j, succeeded, "")
}

// closeRun runs studio.close and answers the status it closed with.
func closeRun(ctx workflow.Context, j Job, status, fault string) (string, error) {
	if err := workflow.ExecuteActivity(ctx, actClose, Close{Org: j.Org, Run: j.Run, Status: status, Fault: fault}).Get(ctx, nil); err != nil {
		return "", err
	}
	return status, nil
}

// stepper is flow/core's Durable backend for one run: each step it is handed runs
// as its parts' activities, then settles.
type stepper struct {
	ctx     workflow.Context
	quick   workflow.Context
	job     Job
	plan    Plan
	stopped bool
}

// Mode is Durable: every step checkpoints on the engine.
func (b *stepper) Mode() flow.Mode { return flow.Durable }

// Exec runs one step. Deterministic: it reads only the plan and its inputs, which
// are upstream results from history.
func (b *stepper) Exec(_ context.Context, s flow.Step, in flow.Inputs) (flow.Outputs, error) {
	spec := s.(node).spec
	ups := make(map[string]result, len(in))
	for k, v := range in {
		ups[k] = v.(result)
	}
	sc := scope{input: b.plan.Input, brand: b.plan.Brand, steps: ups}
	settle := Settle{Org: b.job.Org, Run: b.job.Run, Step: spec.ID}
	parts, err := b.parts(spec, sc)
	if err != nil {
		settle.Fault = err.Error()
		_ = workflow.ExecuteActivity(b.quick, actStep, settle).Get(b.quick, nil)
		return nil, fmt.Errorf("%s: %w", spec.ID, err)
	}
	settle.Items = len(parts)
	opts := workflow.WithActivityOptions(b.ctx, partOptions(spec.Op))
	futures := make([]workflow.Future, len(parts))
	for i, p := range parts {
		futures[i] = workflow.ExecuteActivity(opts, actPart, p)
	}
	results := make([]result, len(parts))
	var first error
	for i, f := range futures {
		if err := f.Get(opts, &results[i]); err != nil && first == nil {
			first = err
		}
	}
	if first != nil {
		var te *temporal.Error
		if errors.As(first, &te) && te.Code == codeCanceled {
			b.stopped = true
			return nil, first
		}
		settle.Fault = cause(first)
		_ = workflow.ExecuteActivity(b.quick, actStep, settle).Get(b.quick, nil)
		return nil, fmt.Errorf("%s: %s", spec.ID, settle.Fault)
	}
	settle.Results = results
	if err := workflow.ExecuteActivity(b.quick, actStep, settle).Get(b.quick, nil); err != nil {
		return nil, err
	}
	return flow.Outputs{"out": merge(spec, results)}, nil
}

// parts is a step resolved into the units it runs as.
func (b *stepper) parts(spec Spec, sc scope) ([]Part, error) {
	base := Part{Org: b.job.Org, Run: b.job.Run, Attempt: b.job.Attempt, Step: spec.ID, Op: spec.Op}
	if spec.Each == "" {
		with, err := sc.resolve(spec.With)
		if err != nil {
			return nil, err
		}
		p := base
		p.Items = 1
		p.With, _ = with.(map[string]any)
		return []Part{p}, nil
	}
	items, err := sc.items(spec.Each)
	if err != nil {
		return nil, err
	}
	parts := make([]Part, len(items))
	for i, item := range items {
		isc := sc
		isc.item, isc.index, isc.fan = item, i, true
		with, err := isc.resolve(spec.With)
		if err != nil {
			return nil, err
		}
		p := base
		p.Item, p.Items = i, len(items)
		p.With, _ = with.(map[string]any)
		parts[i] = p
	}
	return parts, nil
}

// merge is a step's result from its parts': one part's as it is, a fan-out's
// assets in item order, its texts as JSON, one per item.
func merge(spec Spec, rs []result) result {
	if spec.Each == "" && len(rs) == 1 {
		return rs[0]
	}
	out := result{}
	texts := make([]any, len(rs))
	for i, r := range rs {
		out.Assets = append(out.Assets, r.Assets...)
		if r.JSON != nil {
			texts[i] = r.JSON
		} else {
			texts[i] = r.Text
		}
	}
	out.JSON = texts
	return out
}

// partOptions are an op's activity windows. A gateway call is bounded by the
// model's own clock; a lane part follows a job that may wait for a worker, so it
// runs as long as the job's own windows allow, and retries only when this process
// went away under it.
func partOptions(name string) workflow.ActivityOptions {
	switch known[name].executor {
	case gpu:
		return workflow.ActivityOptions{
			StartToCloseTimeout: gpuStart + gpuClose,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval: 5 * time.Second, BackoffCoefficient: 2,
				MaximumInterval: time.Minute, MaximumAttempts: 20,
			},
		}
	case cpu:
		return workflow.ActivityOptions{
			StartToCloseTimeout: cpuStart + cpuClose*cpuAttempts,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval: 5 * time.Second, BackoffCoefficient: 2,
				MaximumInterval: time.Minute, MaximumAttempts: 20,
			},
		}
	}
	return workflow.ActivityOptions{
		StartToCloseTimeout: 20 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 3 * time.Second, BackoffCoefficient: 2,
			MaximumInterval: time.Minute, MaximumAttempts: 3,
		},
	}
}

// cause is an activity error as a person reads it.
func cause(err error) string {
	var te *temporal.Error
	if errors.As(err, &te) && te.Message != "" {
		return te.Message
	}
	return err.Error()
}

// fatal is an error no retry can change: the engine stops retrying the part.
func fatal(format string, args ...any) error {
	return temporal.NewError(fmt.Sprintf(format, args...), "StudioError", true)
}

// stop is a part that found its run cancelled.
func stop() error { return temporal.NewError("the run was cancelled", codeCanceled, true) }

// ── the engine ───────────────────────────────────────────────────────────

// durable is Studio's hold on this process's engine: the client that starts and
// stops runs, and the one worker that runs every org's.
type durable struct {
	st     *state
	engine func() *tasks.Embedded
	ctx    context.Context
	quit   context.CancelFunc

	mu  sync.Mutex
	cli tasksclient.Client
	w   tasksworker.Worker
}

func newDurable(st *state, engine func() *tasks.Embedded) *durable {
	ctx, quit := context.WithCancel(context.Background())
	return &durable{st: st, engine: engine, ctx: ctx, quit: quit}
}

// boot waits for the engine — the process installs it after Use — then registers
// the render lane and starts the worker, which resumes every run the engine
// recovered.
func (d *durable) boot() {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for d.engine() == nil {
		select {
		case <-d.ctx.Done():
			return
		case <-tick.C:
		}
	}
	if err := d.attach(d.engine()); err != nil {
		d.st.log.Error("studio: the run worker did not start; runs wait for it", "err", err)
	}
}

func (d *durable) attach(eng *tasks.Embedded) error {
	if err := laneNamespace(eng); err != nil {
		return fmt.Errorf("render lane: %w", err)
	}
	cli, err := tasksclient.Dial(tasksclient.Options{Address: eng.Address(), Namespace: engineNS, Identity: "studio"})
	if err != nil {
		return fmt.Errorf("dial engine: %w", err)
	}
	w := tasksworker.New(cli, engineQueue, tasksworker.Options{
		Identity: "studio", Logger: d.st.log, MaxConcurrentActivityExecutionSize: 64,
	})
	w.RegisterWorkflowWithOptions(runFlow, tasksworker.RegisterWorkflowOptions{Name: runName})
	w.RegisterActivityWithOptions(d.st.load, tasksworker.RegisterActivityOptions{Name: actLoad})
	w.RegisterActivityWithOptions(d.st.part, tasksworker.RegisterActivityOptions{Name: actPart})
	w.RegisterActivityWithOptions(d.st.step, tasksworker.RegisterActivityOptions{Name: actStep})
	w.RegisterActivityWithOptions(d.st.close, tasksworker.RegisterActivityOptions{Name: actClose})
	if err := w.Start(); err != nil {
		cli.Close()
		return fmt.Errorf("start worker: %w", err)
	}
	d.mu.Lock()
	d.cli, d.w = cli, w
	d.mu.Unlock()
	d.st.log.Info("studio: run worker up", "namespace", engineNS)
	return nil
}

// client is the engine client, or 503 until the worker is up.
func (d *durable) client() (tasksclient.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cli == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "studio is starting; try again in a moment")
	}
	return d.cli, nil
}

// execution is the workflow id one attempt of a run executes as.
func execution(run string, attempt int) string {
	if attempt == 0 {
		return run
	}
	return fmt.Sprintf("%s.%d", run, attempt)
}

// start executes one attempt of a run.
func (d *durable) start(ctx context.Context, j Job) error {
	cli, err := d.client()
	if err != nil {
		return err
	}
	_, err = cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{ID: execution(j.Run, j.Attempt), TaskQueue: engineQueue}, runName, j)
	return err
}

// end terminates one attempt's workflow. The run already says it stopped; this
// only keeps the engine from advancing it.
func (d *durable) end(ctx context.Context, run string, attempt int, why string) {
	cli, err := d.client()
	if err != nil {
		return
	}
	if err := cli.TerminateWorkflow(ctx, execution(run, attempt), "", why); err != nil {
		d.st.log.Debug("studio: terminate", "run", run, "err", err)
	}
}

// stop ends the worker. In-flight activities see their context end; the engine
// re-dispatches them on the next start.
func (d *durable) stop() {
	d.quit()
	d.mu.Lock()
	cli, w := d.cli, d.w
	d.cli, d.w = nil, nil
	d.mu.Unlock()
	if w != nil {
		w.Stop()
	}
	if cli != nil {
		cli.Close()
	}
}
