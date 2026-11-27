// Copyright © 2026 Hanzo AI. MIT License.

package cron

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/env"
	tasks "github.com/hanzoai/tasks/pkg/tasks"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/commerce"
	gitpeer "github.com/hanzoai/cloud/client/git"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
	"github.com/hanzoai/tasks/pkg/sdk/activity"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
	robfig "github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// EntryInput addresses one cron entry by ConfigMap name. It is the ONLY
// thing a schedule embeds — the activity resolves the current ConfigMap at
// fire time.
type EntryInput struct {
	Name string `json:"name"`
	// Fired is what the previous release's JobWorkflow stamped: when the run began.
	// This release writes nothing here and reads it only to name a Job that a run
	// begun before the deploy already made (priorTick). Delete it with the next
	// release.
	Fired time.Time `json:"fired,omitzero"`
}

// PokeResult / JobResult surface run outcomes in the workflow result (what
// the Tasks console shows on the execution).
type (
	PokeResult struct {
		URL    string `json:"url"`
		Status int    `json:"status"`
	}
	JobResult struct {
		Job string `json:"job"`
	}
)

// PokeWorkflow runs one HTTP poke entry: a single retried activity. The
// endpoints behind pokes (billing sweep, pricing sync) are idempotent, so a
// couple of retries inside one tick are safe; across ticks the next fire is
// the retry.
func PokeWorkflow(ctx workflow.Context, in EntryInput) (PokeResult, error) {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: activityPace().retry, BackoffCoefficient: 2.0,
			MaximumInterval: time.Minute, MaximumAttempts: 3,
		},
	})
	var out PokeResult
	err := workflow.ExecuteActivity(actCtx, PokeActivity, in).Get(actCtx, &out)
	return out, err
}

// JobWorkflow runs one k8s Job entry as one activity. A Job that ran and failed is
// never re-run (a failed backup Job must fail loudly in the console; the next cron
// tick is the retry — the CronJob restartPolicy/backoffLimit inside the Job spec
// still governs pod-level retries), so that failure is final. Everything else is
// retried — the entry that could not be read, the Job the API refused to create,
// the watch that broke, the firing that reached a process not holding cron — and a
// retry cannot make a second Job, because the Job is named for its firing.
func JobWorkflow(ctx workflow.Context, in EntryInput) (JobResult, error) {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: jobTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: activityPace().retry, BackoffCoefficient: 2.0,
			MaximumInterval: time.Minute, MaximumAttempts: jobAttempts,
		},
	})
	var out JobResult
	err := workflow.ExecuteActivity(actCtx, RunJobActivity, in).Get(actCtx, &out)
	return out, err
}

// ReconcileWorkflow converges schedules onto the ConfigMap set (see
// reconcile in cron.go) as a single retried activity.
func ReconcileWorkflow(ctx workflow.Context) error {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 5 * time.Second, BackoffCoefficient: 2.0,
			MaximumInterval: 30 * time.Second, MaximumAttempts: 3,
		},
	})
	return workflow.ExecuteActivity(actCtx, ReconcileActivity).Get(actCtx, nil)
}

// pokeSpec is the poke.json payload of a poke entry, in one of two shapes.
//
// A plane poke names an app and an op: the run asks that op by name over the
// internal plane, as the platform, and there is no URL and nothing to present.
// This is how a schedule reaches work this binary already carries — the
// renewal sweep — and it replaced a POST to our own writer bearing a shared
// service token. The op must be one cron holds a client for (planeOps).
//
// An HTTP poke names a URL. bearerEnv names an env var on THIS process
// (KMS-synced) whose value becomes the Authorization bearer — the ConfigMap
// never carries a secret. It is for endpoints outside this binary.
type pokeSpec struct {
	App       string `json:"app,omitempty"`       // plane poke: the app serving the op
	Op        string `json:"op,omitempty"`        // plane poke: the operation id
	URL       string `json:"url,omitempty"`       // HTTP poke
	Method    string `json:"method,omitempty"`    // default POST
	BearerEnv string `json:"bearerEnv,omitempty"` // env var holding the bearer token
	Timeout   string `json:"timeout,omitempty"`   // Go duration, default 10m
}

// planeOps are the ops a schedule may ask over the plane, by "app/op", each
// called through its peer's generated client.
//
// AN OP'S ANSWER HAS A TYPE, AND ONLY ITS CLIENT NAMES IT. In this process the
// plane hands the op's own value back and asserts it is the type the caller
// asked for (zip.Here); over a socket it decodes the reply into that type, which
// must be a struct (zapenc). A call made by name alone cannot name it. The one
// this made asked for a map, which fails both ways, and fails after the op has
// run: from 2026-11-06 the renewal sweep ran every hour and every run read as
// failed, and the engine retried each one twice more (hanzo-inc/cloud#828).
//
// So an entry may name only an op listed here; any other is refused before it
// is asked. The answer is not kept: the run's result is that the op answered.
var planeOps = map[string]func(context.Context) error{
	gitpeer.App + "/" + client.GitMetricsCollect: func(ctx context.Context) error {
		_, err := gitpeer.GitMetricsCollect(ctx)
		return err
	},
	commerce.App + "/" + client.BillingRenew: func(ctx context.Context) error {
		_, err := commerce.BillingRenew(ctx)
		return err
	},
}

// httpDo is swappable for tests; production is a plain client (the per-poke
// timeout comes from the request context).
var httpDo = http.DefaultClient.Do

// PokeActivity resolves the entry's current poke.json and performs the HTTP
// call. Non-2xx is an error so the run fails visibly in the console.
// PokeActivity runs the entry's poke and reports what happened (see report).
func PokeActivity(ctx context.Context, in EntryInput) (PokeResult, error) {
	var (
		out PokeResult
		err error
	)
	if herr := held(ctx, func(ctx context.Context) { out, err = poke(ctx, in) }); herr != nil {
		err = fmt.Errorf("cron %q: %w; the process holding cron runs it", in.Name, herr)
	}
	report("poke", in.Name, out.URL, err)
	record(ctx, "poke", in.Name, out.URL, err)
	return out, err
}

// held runs act while this process holds cron, and answers why it did not
// otherwise. A firing that reaches a process not holding cron is an error the
// engine retries, never a success: the run says it ran only where it ran.
func held(ctx context.Context, act func(context.Context)) error {
	if lead == nil { // a process that never mounted cron: a test driving an activity
		act(ctx)
		return nil
	}
	return lead.Do(ctx, act)
}

func poke(ctx context.Context, in EntryInput) (PokeResult, error) {
	e, err := activityKube().getEntry(ctx, in.Name)
	if err != nil {
		return PokeResult{}, err
	}
	if e.Kind != kindPoke {
		return PokeResult{}, fmt.Errorf("cron entry %q is not a poke", in.Name)
	}
	p := e.Poke
	method := p.Method
	if method == "" {
		method = http.MethodPost
	}
	timeout := 10 * time.Minute
	if p.Timeout != "" {
		if d, err := time.ParseDuration(p.Timeout); err == nil && d > 0 {
			timeout = d
		}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if p.Op != "" {
		ask, ok := planeOps[p.App+"/"+p.Op]
		if !ok {
			// Asked anyway, the op would run and its answer could not be read, so
			// the run would fail after the work was done. Nothing to retry.
			return PokeResult{}, temporal.NewError(fmt.Sprintf(
				"poke %q: cron holds no client for %s/%s; add it to planeOps", in.Name, p.App, p.Op), "UnknownOp", true)
		}
		// The platform, stated once: a scheduled run acts for the fleet, and the
		// reserved admin org is the name the fleet's own work carries.
		if err := ask(cloud.For(cctx, authz.AdminOrg)); err != nil {
			return PokeResult{}, fmt.Errorf("poke %q: %s/%s: %w", in.Name, p.App, p.Op, err)
		}
		return PokeResult{URL: "plane:" + p.App + "/" + p.Op, Status: http.StatusOK}, nil
	}
	req, err := http.NewRequestWithContext(cctx, method, p.URL, strings.NewReader("{}"))
	if err != nil {
		return PokeResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.BearerEnv != "" {
		tok := env.Or(p.BearerEnv, "")
		if tok == "" {
			return PokeResult{}, fmt.Errorf("poke %q: bearer env %s is empty", in.Name, p.BearerEnv)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := httpDo(req)
	if err != nil {
		return PokeResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return PokeResult{}, fmt.Errorf("poke %q: %s %s → %d: %s", in.Name, method, p.URL, resp.StatusCode, body)
	}
	return PokeResult{URL: p.URL, Status: resp.StatusCode}, nil
}

// RunJobActivity resolves the entry's current job.yaml, enforces Forbid
// concurrency (an active run of the same entry skips this tick, matching the
// retired CronJobs' concurrencyPolicy), creates the Job, and blocks until it
// finishes. A failed Job fails the activity — and so the run in the console.
// RunJobActivity creates the entry's Job, waits for it, and reports what
// happened (see report).
func RunJobActivity(ctx context.Context, in EntryInput) (JobResult, error) {
	out, err := runJob(ctx, in)
	report("job", in.Name, out.Job, err)
	record(ctx, "job", in.Name, out.Job, err)
	return out, err
}

func runJob(ctx context.Context, in EntryInput) (JobResult, error) {
	k := activityKube()
	// ONE TICK IS ONE JOB. The Job is named for the firing (firing): a schedule's
	// own fire by the tick the engine stamped on it, so an attempt the engine retries
	// — after a create that landed and reported an error, or a process that stopped
	// while it watched — and the same tick fired late or by another pod's engine all
	// find the Job it made and watch that one; an operator's trigger by its own run.
	var (
		jobName string
		active  bool
		err     error
	)
	// Creating the Job is the act cron holds the right to; watching it is not.
	if herr := held(ctx, func(ctx context.Context) {
		var e entry
		if e, err = k.getEntry(ctx, in.Name); err != nil {
			return
		}
		if e.Kind != kindJob {
			err = fmt.Errorf("cron entry %q is not a job", in.Name)
			return
		}
		var fire string
		if fire, err = firing(ctx, e.Schedule, in.Fired); err != nil {
			return
		}
		jobName = jobNameFor(in.Name, fire)
		if active, err = k.hasActiveJob(ctx, in.Name, jobName); err != nil || active {
			return
		}
		err = k.createJob(ctx, in.Name, jobName, e.JobYAML)
	}); herr != nil {
		return JobResult{}, fmt.Errorf("cron %q: %w; the process holding cron runs it", in.Name, herr)
	}
	if err != nil {
		return JobResult{}, err
	}
	if active {
		// Forbid: another firing's run is still going; this tick is a no-op.
		return JobResult{Job: "(skipped: previous run still active)"}, nil
	}
	// A Job that ran and failed is the run's answer, and is never run again.
	t := time.NewTicker(activityPace().watch)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return JobResult{Job: jobName}, ctx.Err()
		case <-t.C:
			done, succeeded, err := k.jobDone(ctx, jobName)
			if apierrors.IsNotFound(err) {
				// This firing made it and it is gone: it ran, and something removed it
				// before its end was read. Running it again is the one wrong answer.
				return JobResult{Job: jobName}, temporal.NewError(fmt.Sprintf("job %s is gone before its end was read; it ran, and how it ended is unknown", jobName), "JobRan", true)
			}
			if err != nil {
				return JobResult{Job: jobName}, err
			}
			if !done {
				continue
			}
			if !succeeded {
				msg := fmt.Sprintf("job %s failed", jobName)
				if why := k.jobFailure(ctx, jobName); why != "" {
					msg += ": " + why
				}
				return JobResult{Job: jobName}, temporal.NewError(msg, "JobRan", true)
			}
			return JobResult{Job: jobName}, nil
		}
	}
}

// The Job activity's bounds, which the Job's own lifetime must outlast (jobTTL).
const (
	jobTimeout  = 2 * time.Hour
	jobAttempts = 4
)

// firing names the run this activity belongs to. A schedule's own fire carries the
// tick it was for (the engine stamps it, tasks.ScheduledStart), the same on every
// attempt, when the fire was late, and on every pod whose engine fired it. Any
// other run — an operator's trigger, an activity driven outside its workflow — is
// its own firing, so re-running a tick whose Job failed runs the Job again rather
// than adopting the failure. A run the engine cannot describe is not named at all:
// the attempt fails and is retried, since a guess could name a second Job.
func firing(ctx context.Context, schedule string, fired time.Time) (string, error) {
	run := activity.GetInfo(ctx).WorkflowExecution
	name := run.WorkflowID + "/" + run.RunID
	if run.WorkflowID == "" && run.RunID == "" {
		name = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	var wf *tasks.WorkflowExecution
	if eng := currentEngine(); eng != nil && run.WorkflowID != "" {
		var err error
		if wf, _, err = eng.View(tasks.Org(org())).DescribeWorkflow(namespace, run.WorkflowID, run.RunID); err != nil {
			return "", fmt.Errorf("cron: reading run %s: %w", name, err)
		}
	}
	return firingOf(wf, name, schedule, fired), nil
}

// firingOf is the name of one run's firing: the tick the engine stamped on a
// schedule's own fire, else the tick the previous release named for a run it
// began (priorTick), else the run's own name.
func firingOf(wf *tasks.WorkflowExecution, run, schedule string, fired time.Time) string {
	if at, ok := tasks.ScheduledStart(wf); ok {
		return at.UTC().Format(time.RFC3339)
	}
	if at, ok := priorTick(schedule, fired); ok {
		return at.Format(time.RFC3339)
	}
	return run
}

// priorTick is the tick the previous release named a run's Job by: the schedule's
// last tick at or before the run began, when it began within fireWindow of it. A
// run that release began and this one retries carries no stamped tick, only the
// start its workflow recorded (EntryInput.Fired), and must find the Job it made.
// Delete it with EntryInput.Fired.
func priorTick(schedule string, fired time.Time) (time.Time, bool) {
	if fired.IsZero() {
		return time.Time{}, false
	}
	last, ok := lastTick(schedule, fired.UTC())
	return last, ok && fired.UTC().Sub(last) <= fireWindow
}

// cronSpec reads a schedule the way the engine fires it.
var cronSpec = robfig.NewParser(robfig.SecondOptional | robfig.Minute | robfig.Hour | robfig.Dom | robfig.Month | robfig.Dow | robfig.Descriptor)

// fireWindow is how soon after its tick the previous release took a run to be the
// tick's own fire.
const fireWindow = time.Minute

// lastTick is schedule's last tick at or before at.
func lastTick(schedule string, at time.Time) (time.Time, bool) {
	sched, err := cronSpec.Parse(schedule)
	if err != nil {
		return time.Time{}, false
	}
	for _, back := range []time.Duration{time.Hour, 48 * time.Hour, 400 * 24 * time.Hour} {
		var last time.Time
		for t, n := at.Add(-back), 0; n < 100000; n++ {
			next := sched.Next(t)
			if next.IsZero() || next.After(at) {
				break
			}
			last, t = next, next
		}
		if !last.IsZero() {
			return last, true
		}
	}
	return time.Time{}, false
}

// ReconcileActivity is the scheduled half of reconcile (boot runs it inline).
func ReconcileActivity(ctx context.Context) error {
	eng := currentEngine()
	if eng == nil {
		return fmt.Errorf("tasks engine not ready")
	}
	return reconcile(ctx, eng.View(tasks.Org(org())), activityKube(), activityLog())
}
