// Copyright © 2026 Hanzo AI. MIT License.

package marketing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
	luxlog "github.com/luxfi/log"
)

// drip.go binds the email drip engine to the ONE durable clock: the embedded
// hanzoai/tasks engine (cloud.EmbeddedTasks). It registers a single recurring
// schedule that fires DripSweepWorkflow every minute; the workflow runs one
// activity that calls processDue (sequences.go) to advance every due enrollment.
//
// This is the clients/cron pattern verbatim — engine owns time (its 5s sweep
// fires the schedule; runs are durable workflows visible in the Tasks console),
// state owns the schedule (each enrollment's next_run_at in SQLite). There is no
// bespoke ticker and no second queue. Fail-soft: the engine is wired after
// MountAll, so start() waits for it in the background; no engine simply means the
// drip is idle (state is preserved and resumes when an engine appears), never a
// blocked boot.

const (
	dripTaskQueue  = "cloud-marketing-drip"
	dripNamespace  = "default"
	dripScheduleID = "marketing-drip-sweep"
	// dripEvery is the sweep cadence — the worst-case delay between a step
	// coming due and being sent. Minute granularity is ample for drips spaced
	// minutes-to-days apart.
	dripEvery = "* * * * *"
	// dripBatch bounds how many due enrollments one sweep advances.
	dripBatch = 500
	// dripEngineWait bounds how long start() waits for the shared engine.
	dripEngineWait = 5 * time.Minute
)

// dripWorker + dripClient hold the running poller so it is not collected and can
// be drained cleanly on shutdown. Guarded by dripMu (start runs in a goroutine;
// Shutdown may race it).
var (
	dripMu     sync.Mutex
	dripWorker tasksworker.Worker
	dripClient tasksclient.Client
)

// dripOrg is the org shard the drip schedule lives in (for Tasks-console
// visibility). The sweep itself is platform-wide and sends org-scoped; this only
// picks where the ONE schedule record is stored.
func dripOrg() string {
	if v := os.Getenv("MARKETING_CRON_ORG"); v != "" {
		return v
	}
	return "hanzo"
}

// startDrip waits for the shared engine, registers the namespace + worker, and
// upserts the single recurring sweep schedule. Runs in a background goroutine
// from Mount.
func startDrip(ctx context.Context, log luxlog.Logger) {
	eng := waitDripEngine(ctx)
	if eng == nil {
		log.Warn("tasks engine never became ready — drip idle", "waited", dripEngineWait)
		return
	}
	view := eng.View(dripOrg())
	if err := view.RegisterNamespace(tasksengine.Namespace{
		NamespaceInfo: tasksengine.NamespaceInfo{Name: dripNamespace},
	}); err != nil {
		log.Error("drip: register namespace", "err", err)
		return
	}
	cli, err := tasksclient.Dial(tasksclient.Options{
		HostPort:  fmt.Sprintf("127.0.0.1:%d", eng.ZAPPort()),
		Namespace: dripNamespace,
	})
	if err != nil {
		log.Error("drip: dial engine", "err", err)
		return
	}
	w := tasksworker.New(cli, dripTaskQueue, tasksworker.Options{})
	w.RegisterWorkflow(DripSweepWorkflow)
	w.RegisterActivity(DripSweepActivity)
	if err := w.Start(); err != nil {
		cli.Close()
		log.Error("drip: worker start", "err", err)
		return
	}
	dripMu.Lock()
	dripWorker, dripClient = w, cli
	dripMu.Unlock()
	if err := upsertDripSchedule(view); err != nil {
		log.Error("drip: sweep schedule", "err", err)
		return
	}
	log.Info("email drip engine live on the durable tasks engine",
		"org", dripOrg(), "namespace", dripNamespace, "queue", dripTaskQueue, "cadence", dripEvery)
}

// stopDrip drains the drip poller and closes its engine client. Idempotent —
// safe when the engine never came up (worker/client stay nil). Called from
// Shutdown.
func stopDrip() {
	dripMu.Lock()
	w, cli := dripWorker, dripClient
	dripWorker, dripClient = nil, nil
	dripMu.Unlock()
	if w != nil {
		w.Stop()
	}
	if cli != nil {
		cli.Close()
	}
}

func waitDripEngine(ctx context.Context) *tasksengine.Embedded {
	deadline := time.Now().Add(dripEngineWait)
	for {
		if eng := cloud.EmbeddedTasks(); eng != nil {
			return eng
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// upsertDripSchedule registers the recurring sweep, only when absent or drifted
// (CreateSchedule resets the fire anchor, so rewriting an unchanged schedule
// every boot would push its next fire forward forever).
func upsertDripSchedule(view tasksengine.View) error {
	want := tasksengine.Schedule{
		ScheduleId: dripScheduleID,
		Namespace:  dripNamespace,
		Spec:       tasksengine.ScheduleSpec{CronString: []string{dripEvery}},
		Action: tasksengine.ScheduleAction{
			WorkflowType: tasksengine.TypeRef{Name: "DripSweepWorkflow"},
			TaskQueue:    dripTaskQueue,
		},
	}
	if cur, ok, _ := view.DescribeSchedule(want.Namespace, want.ScheduleId); ok && cur != nil {
		if len(cur.Spec.CronString) == 1 && cur.Spec.CronString[0] == dripEvery &&
			cur.Action.WorkflowType.Name == want.Action.WorkflowType.Name &&
			cur.Action.TaskQueue == want.Action.TaskQueue {
			return nil
		}
	}
	return view.CreateSchedule(want)
}

// SweepResult surfaces a sweep's outcome in the Tasks console execution.
type SweepResult struct {
	Advanced  int `json:"advanced"`  // drip steps advanced
	Published int `json:"published"` // calendar posts published
}

// DripSweepWorkflow runs one drip sweep as a single retried activity. The sweep
// is idempotent (per-step claim), so a retry within a tick is safe; across ticks
// the next minute's fire is the natural retry.
func DripSweepWorkflow(ctx workflow.Context) (SweepResult, error) {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 5 * time.Second, BackoffCoefficient: 2.0,
			MaximumInterval: 30 * time.Second, MaximumAttempts: 3,
		},
	})
	var out SweepResult
	err := workflow.ExecuteActivity(actCtx, DripSweepActivity).Get(actCtx, &out)
	return out, err
}

// DripSweepActivity advances every due enrollment by one step. It resolves the
// live mounted service (the same one serving HTTP) so the sweep shares the store
// and KMS with the request path — one engine, one store, one send gate.
func DripSweepActivity(ctx context.Context) (SweepResult, error) {
	s := mounted
	if s == nil || s.State.store == nil {
		return SweepResult{}, fmt.Errorf("marketing: not mounted")
	}
	now := time.Now().Unix()
	advanced, derr := processDue(ctx, s, now, dripBatch)
	published, cerr := processDueCalendar(ctx, s, now, dripBatch)
	return SweepResult{Advanced: advanced, Published: published}, errors.Join(derr, cerr)
}
