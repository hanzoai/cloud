package dev

// durable.go — a turn's work, in the background, as a workflow on this process's
// tasks engine (HIP-1330: durability is hanzoai/tasks and nothing else).
//
// WHO KEEPS WHAT. The ledger (apps/dev/loop) keeps every fact about what happened:
// which action ids were dispatched, what each answered, the core's snapshot and the
// work it has asked for and not yet had done. It is what makes an effect happen
// once. The workflow keeps one thing the ledger cannot: that somebody must come
// back. A turn is submitted into the ledger first, and then a workflow is started
// whose single activity drives the session's pending work until none is left. If
// the process dies, or the model gateway is down, or a lease is refused, the engine
// runs the activity again with backoff, in this process or its successor, and the
// ledger answers every id already done from its row. The workflow decides nothing
// and the ledger runs nothing.
//
// A drive that finds the session held by another drive, or the platform failing,
// returns an error and is retried. A drive that finds the session stopped,
// suspended or gone has nothing to do and ends.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/dev/loop"
)

const (
	engineNS    = "dev"
	engineQueue = "dev"
	driveName   = "dev.drive"
)

type driveIn struct {
	Org     string `json:"org"`
	Owner   string `json:"owner"`
	Session string `json:"session"`
	// Who pays for the drive's model calls and cells: the username, home org, platform
	// authority and billing account of the person whose turn it is, as the request that
	// set it going stated them (actingAs). An input written before they were carried
	// names nobody, and its calls are refused for want of a payer, as they were then.
	Name    string `json:"name,omitempty"`
	Home    string `json:"home,omitempty"`
	Admin   bool   `json:"admin,omitempty"`
	Account string `json:"account,omitempty"`
}

// who is the person a drive is for, as the plane states them.
func (in driveIn) who() zip.Caller {
	return zip.Caller{User: in.Owner, Name: in.Name, Owner: in.Home, Admin: in.Admin, Account: in.Account}
}

// driving is the drive of a session for subject, paid for as ctx's caller is.
func driving(ctx context.Context, org, subject, session string) driveIn {
	c := zip.CallerOf(ctx)
	return driveIn{Org: org, Owner: subject, Session: session,
		Name: c.Name, Home: c.Owner, Admin: c.Admin, Account: c.Account}
}

// driveFlow runs the one activity, retried until the session has nothing pending.
func driveFlow(ctx workflow.Context, in driveIn) error {
	opts := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 5 * time.Second, BackoffCoefficient: 2,
			MaximumInterval: 5 * time.Minute, MaximumAttempts: 20,
		},
	})
	return workflow.ExecuteActivity(opts, driveName, in).Get(opts, nil)
}

type durable struct {
	rt     *Runtime
	engine func() *tasks.Embedded
	ctx    context.Context
	quit   context.CancelFunc

	mu  sync.Mutex
	cli tasksclient.Client
	w   tasksworker.Worker
}

func newDurable(rt *Runtime, engine func() *tasks.Embedded) *durable {
	ctx, quit := context.WithCancel(context.Background())
	return &durable{rt: rt, engine: engine, ctx: ctx, quit: quit}
}

// boot waits for the engine, which the process installs after mounting, then starts
// the worker, which resumes every drive the engine recovered.
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
		d.rt.log("dev: the drive worker did not start; turns wait for it", err)
	}
}

func (d *durable) attach(eng *tasks.Embedded) error {
	cli, err := tasksclient.Dial(tasksclient.Options{Address: eng.Address(), Namespace: engineNS, Identity: "dev"})
	if err != nil {
		return fmt.Errorf("dial engine: %w", err)
	}
	w := tasksworker.New(cli, engineQueue, tasksworker.Options{Identity: "dev"})
	w.RegisterWorkflowWithOptions(driveFlow, tasksworker.RegisterWorkflowOptions{Name: driveName})
	w.RegisterActivityWithOptions(d.drive, tasksworker.RegisterActivityOptions{Name: driveName})
	if err := w.Start(); err != nil {
		cli.Close()
		return fmt.Errorf("start worker: %w", err)
	}
	d.mu.Lock()
	d.cli, d.w = cli, w
	d.mu.Unlock()
	return nil
}

// drive is the activity: do the session's pending work through the host.
func (d *durable) drive(ctx context.Context, in driveIn) error {
	err := d.rt.loop.Drive(ctx, in.Org, in.Owner, in.Session, d.rt.hostFor(in.Org, in.who(), in.Session))
	switch {
	case errors.Is(err, loop.ErrSuspended), errors.Is(err, loop.ErrNoSession):
		return nil
	}
	return err
}

func (d *durable) client() (tasksclient.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cli == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "dev: the turn is recorded and will run once the worker is up")
	}
	return d.cli, nil
}

// start sets a drive going and answers a handle to wait on.
func (d *durable) start(ctx context.Context, in driveIn) (tasksclient.WorkflowRun, error) {
	cli, err := d.client()
	if err != nil {
		return nil, err
	}
	return cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{
		ID:        fmt.Sprintf("%s.%d", in.Session, time.Now().UnixNano()),
		TaskQueue: engineQueue,
	}, driveName, in)
}

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

// client2 is the engine client, or nil until the worker is up.
func (d *durable) client2() tasksclient.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cli
}
