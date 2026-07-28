package git

import (
	"context"
	"fmt"
	"sync"

	"github.com/hanzoai/cloud"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
)

// reactor.go is the ONE durable seam every git-lifecycle reactor rides.
//
// cloud.EmitLifecycle fans a fact out to subscribers in-process, which is the right
// shape for deciding WHETHER to act — but not for the acting. Work done inline in a
// subscriber dies with the process, so a deploy landing during a rolling upgrade lost
// its Slack notice with nothing to retry it. Every reactor therefore ENQUEUES onto the
// ONE embedded hanzoai/tasks engine (cloud.EmbeddedTasks) — the unified durable queue,
// there is no second async system (tasks/CONTRACT) — and a worker drains it with
// retry/backoff.
//
// This file holds the part that is identical for every reactor: dial the engine,
// register the queue's workflow + activities on a worker, start it once per process,
// and memoize the client. A reactor contributes only what differs — its queue name,
// its workflow, its activities, and the idempotency key for one fact.

// errEngineNotReady is the fail-soft signal: the embedded engine is nil until
// wireDurableIngest runs (after MountAll). A reactor that sees it does its work inline
// instead — the same contract ai's durable ingest uses — so no reactor is ever dark;
// the engine only upgrades it from best-effort-inline to durable-retryable.
var errEngineNotReady = fmt.Errorf("tasks engine not ready")

// queue is one reactor's durable task queue: its name, the workflow it starts, and the
// activities that workflow executes. Declare one package-level value per reactor; the
// zero value is not usable.
type queue struct {
	name string // <service>-<purpose>
	wf   any    // the workflow func this queue starts
	acts []any  // the activity funcs that workflow executes

	mu  sync.Mutex
	cli tasksclient.Client
}

// start begins the queue's workflow for one fact, keyed by id. ExecuteWorkflow is
// idempotent on the id, so the same fact enqueued twice is ONE execution and a
// redelivery is a no-op — every caller keys id on the fact itself, never on time.
func (q *queue) start(ctx context.Context, id string, in any) error {
	cli, err := q.client()
	if err != nil {
		return err
	}
	_, err = cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{
		ID:        id,
		TaskQueue: q.name,
	}, q.wf, in)
	return err
}

// client lazily dials the embedded engine and starts this queue's worker, once per
// process, memoizing the client.
//
// Dialed on the always-registered `default` namespace: the embedded engine registers
// only `default` at boot and does not lazily create namespaces, so dialing a per-org
// namespace would leave the worker polling one that does not exist. Isolation rides in
// the workflow INPUT instead — every input carries Org — exactly as ai's durable
// ingest does.
func (q *queue) client() (tasksclient.Client, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cli != nil {
		return q.cli, nil
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
		return nil, fmt.Errorf("%s dial engine: %w", q.name, err)
	}
	w := tasksworker.New(cli, q.name, tasksworker.Options{})
	w.RegisterWorkflow(q.wf)
	for _, a := range q.acts {
		w.RegisterActivity(a)
	}
	if err := w.Start(); err != nil {
		cli.Close()
		return nil, fmt.Errorf("%s worker start: %w", q.name, err)
	}
	q.cli = cli
	return cli, nil
}
