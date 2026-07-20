package coding

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/agents"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
)

// routed.go is coding's durable substrate for #48 route-work: it enqueues one
// routed run as a workflow on the ONE embedded hanzoai/tasks engine
// (cloud.EmbeddedTasks) — the SAME queue index-on-push and automations use, never
// a second async system — and owns that run until an external machine reports it.
//
// ONE ENGINE, ONE IDIOM. Like index_on_push: dial the loopback engine once,
// register a worker with the workflow + its single activity, and ExecuteWorkflow
// keyed by the session id (idempotent — a re-dispatch of the same run is a no-op).
//
// DURABILITY VIA A BLOCKING DELIVERY ACTIVITY. RoutedRunWorkflow runs exactly one
// activity, DeliverRoutedRunActivity, which OFFERS the run to the live mailbox
// (clients/agents) and BLOCKS until the machine reports a result. The activity —
// not the workflow — is what re-runs on a worker/cloud restart, so on recovery it
// re-offers the run to the (now-empty) mailbox and the long-polling machine
// re-claims it: durability the mailbox alone could not give. A run never claimed
// (or a machine that dies mid-flight) trips the activity's StartToClose budget →
// the engine retries → after the attempts are spent the workflow fails, which is
// fail-closed by construction (the run never silently succeeds or runs elsewhere).

const (
	// routedQueue is this workflow family's own task queue (<service>-<purpose>).
	routedQueue = "agent-routed"

	// claimDeadline is how long a queued run waits for a machine to CLAIM it before
	// the delivery activity's budget starts being dominated by the run itself. It
	// is folded into StartToClose; a run unclaimed for the whole budget fails closed.
	claimDeadline = 2 * time.Minute

	// routedMaxConcurrent sizes the routed worker's activity pool. Each in-flight
	// delivery blocks one slot for the run's lifetime, so this caps concurrent
	// routed runs this cloud replica shepherds.
	routedMaxConcurrent = 256

	// routedHeartbeat lets the engine detect a dead worker: while this cloud is
	// alive the worker auto-heartbeats at half this interval, so a crash stops the
	// heartbeats and the engine re-dispatches the delivery to a live worker.
	routedHeartbeat = 30 * time.Second
)

var errEngineNotReady = errors.New("coding: tasks engine not ready")

// routedStartToClose is the delivery activity's total budget: the claim window
// plus the run's own timeout. Exceeding it means no machine claimed, or one
// claimed and never reported — either way the run is re-queued then failed closed.
func routedStartToClose(timeoutSeconds int) time.Duration {
	return claimDeadline + time.Duration(timeoutOr(timeoutSeconds))*time.Second
}

// RoutedRunWorkflow is the durable owner of one routed run. Exported for worker
// registration; not called directly.
func RoutedRunWorkflow(ctx workflow.Context, in agents.RoutedRun) (agents.RoutedResult, error) {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: routedStartToClose(in.TimeoutSeconds),
		HeartbeatTimeout:    routedHeartbeat,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})
	var res agents.RoutedResult
	err := workflow.ExecuteActivity(actCtx, DeliverRoutedRunActivity, in).Get(actCtx, &res)
	return res, err
}

// routedFinalizeTimeout bounds the cloud-side completion (verify ref + file PR +
// close session) that runs once the machine reports. Generous for a couple of local
// reads + writes, but finite so a wedged seam can never hold the activity open.
const routedFinalizeTimeout = 60 * time.Second

// DeliverRoutedRunActivity offers the run to the live mailbox and blocks until the
// machine reports a terminal result or the budget elapses. It derives an internal
// deadline from the same budget so the goroutine can never outlive the activity
// even if the engine does not cancel the passed ctx exactly at StartToClose.
// Exported for worker registration; not called directly.
//
// COMPLETION PARITY (#48): once the report is in hand, it runs the cloud-side
// completion (routedFinalizer) — VerifyRef the pushed branch landed, file the native
// PR, and CLOSE THE SESSION (the machine never closes it) — the SAME steps the local
// keystone path runs after a sandbox push. It runs on a cancel-immune, bounded
// context so a run near its deadline still transitions to terminal, and only AFTER a
// real report (never on the re-offer/timeout path), so a completed run is never
// re-executed by a retry.
func DeliverRoutedRunActivity(ctx context.Context, in agents.RoutedRun) (agents.RoutedResult, error) {
	ctx, cancel := context.WithTimeout(ctx, routedStartToClose(in.TimeoutSeconds))
	defer cancel()
	off := agents.OfferRoutedRun(in)
	defer off.Close()
	res, ok := off.Await(ctx)
	if !ok {
		return agents.RoutedResult{}, fmt.Errorf("routed run %s was not completed before its deadline", in.SessionID)
	}
	if routedFinalizer != nil {
		fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), routedFinalizeTimeout)
		routedFinalizer(fctx, in, res)
		fcancel()
	}
	return res, nil
}

// routedFinalizer is the completion seam the delivery activity runs when a routed run
// reports terminal: verify the pushed ref, file the PR, and close the session. It is
// injected once at the composition root (NewDispatcher binds it to THIS dispatcher's
// git/tracker/session seams), so the free-function activity reaches those seams
// without coding holding global Dispatcher state — the same injected-seam shape
// index_on_push uses. Nil (unwired, e.g. a direct-Dispatcher unit test that fakes the
// Route seam) simply skips the cloud-side completion.
var routedFinalizer func(ctx context.Context, in agents.RoutedRun, res agents.RoutedResult)

func setRoutedFinalizer(fn func(ctx context.Context, in agents.RoutedRun, res agents.RoutedResult)) {
	routedFinalizer = fn
}

// finalizeRoutedDurable adapts the durable agents types to coding's and runs the
// cloud-side completion. It is what NewDispatcher binds as the routedFinalizer seam.
func (d Dispatcher) finalizeRoutedDurable(ctx context.Context, in agents.RoutedRun, res agents.RoutedResult) {
	d.finalizeRouted(ctx, RoutedRun{
		Org: in.Org, TargetID: in.TargetID, SessionID: in.SessionID, Repo: in.Repo,
		Project: in.Project, Base: in.Base, Branch: in.Branch, Prompt: in.Prompt,
		Actor: in.Actor, AgentRef: in.AgentRef,
	}, RoutedResult{
		OK: res.OK, Changed: res.Changed, Branch: res.Branch,
		CommitSha: res.CommitSha, Diffstat: res.Diffstat, Error: res.Error,
	})
}

var (
	routedClientMu sync.Mutex
	routedClient   tasksclient.Client
)

// routedEngineClient lazily dials the ONE embedded engine and registers the
// routed worker once (process-lifetime), memoizing the client. Dialed on the
// always-registered `default` namespace — isolation rides in the workflow input
// (agents.RoutedRun.Org) exactly as index-on-push does. Nil engine (before
// wireDurableIngest, or an embed failure) => errEngineNotReady, which the
// dispatcher renders as an honest failure (a routed run NEVER falls back to
// local execution).
func routedEngineClient() (tasksclient.Client, error) {
	routedClientMu.Lock()
	defer routedClientMu.Unlock()
	if routedClient != nil {
		return routedClient, nil
	}
	eng := cloud.EmbeddedTasks()
	if eng == nil {
		return nil, errEngineNotReady
	}
	cli, err := tasksclient.Dial(tasksclient.Options{
		HostPort:  fmt.Sprintf("127.0.0.1:%d", eng.ZAPPort()),
		Namespace: "default",
	})
	if err != nil {
		return nil, fmt.Errorf("routed dial engine: %w", err)
	}
	w := tasksworker.New(cli, routedQueue, tasksworker.Options{
		MaxConcurrentActivityExecutionSize: routedMaxConcurrent,
	})
	w.RegisterWorkflow(RoutedRunWorkflow)
	w.RegisterActivity(DeliverRoutedRunActivity)
	if err := w.Start(); err != nil {
		cli.Close()
		return nil, fmt.Errorf("routed worker start: %w", err)
	}
	routedClient = cli
	return cli, nil
}

// enqueueRoutedRun is the Route seam's production binding: it starts the durable
// RoutedRunWorkflow keyed by the session id (idempotent) and returns immediately.
// No local execution, no new queue.
func enqueueRoutedRun(ctx context.Context, run RoutedRun) error {
	cli, err := routedEngineClient()
	if err != nil {
		return err
	}
	in := agents.RoutedRun{
		Org: run.Org, TargetID: run.TargetID, SessionID: run.SessionID,
		Repo: run.Repo, Project: run.Project, Base: run.Base, Branch: run.Branch,
		Prompt: run.Prompt, CloneURL: run.CloneURL, TimeoutSeconds: run.TimeoutSeconds,
		Actor: run.Actor, AgentRef: run.AgentRef,
	}
	_, err = cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{
		ID:        run.SessionID,
		TaskQueue: routedQueue,
	}, RoutedRunWorkflow, in)
	return err
}
