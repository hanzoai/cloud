// Copyright © 2026 Hanzo AI. MIT License.

package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	"github.com/zap-proto/zip"
)

// The durable engine is per-PROCESS. Each app embeds its own over its own data
// dir (durable.go says why: the engine's SQLite has one writer, so a shared store
// would be the same collision a shared port already was), which makes "the engine"
// a fact about a binary rather than about the cluster.
//
// That is fine while a namespace is written and read by one app. It is not fine
// for the BYO fleet: a worker registers its presence through THIS surface, and
// visor renders the fleet from its OWN engine — where nothing ever wrote. The
// result was silent and total: spark heartbeating every 30s, its presence row
// sitting in this app's `fleet` namespace, and /v1/compute/machines,
// /v1/compute/gpus, /v1/compute/fleet/workers and studio's node badges all
// answering "no GPUs" — so a
// connected renderer could not be seen, targeted, or reasoned about.
//
// So the engine is asked, not opened — the same shape commerce's ledger takes
// (apps/commerce/balance_rpc.go). This op is the read; the writer stays exactly
// where it is.

// exposeActivities publishes the org-scoped engine read on the internal plane.
// Mount calls it. planeActivities is a NAMED handler rather than a closure so
// that zipdoc has a doc comment to lift (workflow.go carries the directive); a
// closure is a call expression with nothing to read.
func exposeActivities() {
	cloud.Plane().Post("/workflow/activities", planeActivities,
		zip.WithOperationID(client.WorkflowActivities),
		zip.WithSummary("One page of a namespace's standalone activities"))
	cloud.Plane().Post("/workflow/enqueue", planeEnqueue,
		zip.WithOperationID(client.WorkflowEnqueue),
		zip.WithSummary("Put one render job on the GPU lane"))
	cloud.Plane().Post("/workflow/activity", planeActivity,
		zip.WithOperationID(client.WorkflowActivity),
		zip.WithSummary("Read one render job"))
	cloud.Plane().Post("/workflow/cancel", planeCancel,
		zip.WithOperationID(client.WorkflowCancel),
		zip.WithSummary("Cancel one render job"))
}

// Reads one page of one namespace out of the engine this process owns, so an app
// that has no such engine can still see what was written here.
//
// The ORG is the caller's — the gateway's assertion, or what a background job
// stated once and explicitly — and cannot be named in the input, so a caller can
// never page another tenant's activities. The NAMESPACE is the caller's to choose,
// but only inside that org's shard, which its identity already pinned.
//
// The rows cross as the engine's own JSON. Re-shaping them into a type declared
// on the plane would put a second copy of hanzoai/tasks' StandaloneActivity in a
// package that does not own it, and a relayed answer reshaped to fit a local
// struct is the wire break the typed-op migration exists to avoid.
//
// A missing engine is an ERROR, never an empty page: answering "no activities"
// from the process that owns the file is how an online fleet reads as no fleet.
func planeActivities(ctx context.Context, in *client.ActivitiesIn) (*client.Activities, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("activities: no org on the call")
	}
	eng := engine()
	if eng == nil {
		return nil, zip.ErrInternal("activities: no durable engine in the process that owns it")
	}
	rows, next, err := eng.ActivitiesPageForOrg(org, in.Namespace, in.Cursor, in.Size)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	return &client.Activities{Rows: b, Next: next}, nil
}

// The GPU lane, and the fleet job types the plane writes may put on it:
// studio.render, research.bench, research.tick, hanzod.run, fn.run, and echo.
const (
	laneNS = "gpu-jobs"
	// maxInput bounds a job's input: a graph or benchmark spec, never large binary blobs.
	maxInput = 1 << 20
	// maxWindow bounds every window a job states.
	maxWindow = 30 * 24 * time.Hour
)

var laneTypes = map[string]bool{
	"studio.render":  true,
	"research.bench": true,
	"research.tick":  true,
	"hanzod.run":     true,
	"fn.run":         true,
	"echo":           true,
}

// window parses a job window: a positive Go duration no longer than maxWindow.
func window(name, v string) error {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 || d > maxWindow {
		return zip.ErrBadRequest(fmt.Sprintf("enqueue: %s is a duration between 1s and %s", name, maxWindow))
	}
	return nil
}

// Puts one render job on the org's GPU lane — namespace gpu-jobs, type
// studio.render — for a fleet worker to claim.
//
// This is the write the activities read pages back. A worker claims from THIS
// process's engine (it reaches it through /v1/workflow), so work enqueued in any
// other app's engine is work no worker will ever see; an app that renders on the
// fleet enqueues here instead.
//
// The ORG is the caller's and cannot be named in the input. The job states its
// attempts (at least one), its lease, its run window and its wait window; its input
// is JSON of at most 1 MiB. The lane is the one a worker creates when it links, and
// an org with none is refused 409 rather than given one. The job's run is its id and
// the id is its request id: an enqueue of an id that exists answers that job
// unchanged, so a retried enqueue never resets a claimed job and never runs twice.
func planeEnqueue(ctx context.Context, in *client.EnqueueIn) (*client.Activity, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("enqueue: no org on the call")
	}
	if in.Namespace != laneNS || !laneTypes[in.Type] {
		return nil, zip.ErrBadRequest("enqueue: unsupported job type on the " + laneNS + " lane")
	}
	if in.ID == "" || in.Queue == "" {
		return nil, zip.ErrBadRequest("enqueue: id and queue are required")
	}
	if in.Attempts < 1 {
		return nil, zip.ErrBadRequest("enqueue: attempts is at least 1")
	}
	for _, w := range []struct{ name, v string }{{"heartbeat", in.Heartbeat}, {"start", in.Start}, {"close", in.Close}} {
		if err := window(w.name, w.v); err != nil {
			return nil, err
		}
	}
	if len(in.Input) > maxInput {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "enqueue: input is at most %d bytes", maxInput)
	}
	if !json.Valid(in.Input) {
		return nil, zip.ErrBadRequest("enqueue: input is not JSON")
	}
	eng := engine()
	if eng == nil {
		return nil, zip.ErrInternal("enqueue: no durable engine in the process that owns it")
	}
	// The check for the job and the start that fills it are one step in the engine,
	// under a lock every replica holds (StartActivity, with the id as its request id),
	// so two replicas that enqueue one id at once make one job and a retry never resets
	// a claimed one. A mutex here would exclude only this process.
	v := eng.View(tasks.Org(org))
	if a, ok, err := v.DescribeActivity(in.Namespace, in.ID, in.ID); err != nil {
		return nil, err
	} else if ok {
		return row(a)
	}
	a, err := v.StartActivity(in.Namespace, in.ID, in.ID, tasks.TypeRef{Name: in.Type}, in.Queue, json.RawMessage(in.Input),
		&tasks.RetryPolicy{MaximumAttempts: in.Attempts}, "", in.Start, in.Close, in.Heartbeat, cloud.Who(ctx).User, in.ID)
	if err != nil && err.Error() == fmt.Sprintf("namespace %q not registered", in.Namespace) {
		return nil, zip.ErrConflict("enqueue: this org has no GPU lane — no machine has linked in it")
	}
	if err != nil {
		return nil, err
	}
	return row(a)
}

// Reads one render job off the org's GPU lane: its status, its claimant, and once it
// ends its result or the cause it failed with. A job the lane does not hold is 404,
// never an empty row. The job's input is left out: the caller wrote it, and a reader
// following a job reads its state every few seconds.
func planeActivity(ctx context.Context, in *client.ActivityIn) (*client.Activity, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("activity: no org on the call")
	}
	if in.Namespace != laneNS {
		return nil, zip.ErrBadRequest("activity: only the " + laneNS + " lane")
	}
	eng := engine()
	if eng == nil {
		return nil, zip.ErrInternal("activity: no durable engine in the process that owns it")
	}
	a, ok, err := eng.View(tasks.Org(org)).DescribeActivity(in.Namespace, in.ID, in.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, zip.ErrNotFound("activity: no such activity")
	}
	a.Input = nil
	return row(a)
}

// Cancels one render job on the org's GPU lane. A claimed one stops being the
// claimant's to complete: its report is refused, and nothing waits on it. A job the
// lane does not hold is 404; one that already ended is 409.
func planeCancel(ctx context.Context, in *client.CancelIn) (*client.Unit, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("cancel: no org on the call")
	}
	if in.Namespace != laneNS {
		return nil, zip.ErrBadRequest("cancel: only the " + laneNS + " lane")
	}
	eng := engine()
	if eng == nil {
		return nil, zip.ErrInternal("cancel: no durable engine in the process that owns it")
	}
	a, ok, err := eng.View(tasks.Org(who.Org)).DescribeActivity(in.Namespace, in.ID, in.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, zip.ErrNotFound("cancel: no such activity")
	}
	switch a.Status {
	case "ACTIVITY_TASK_STATE_COMPLETED", "ACTIVITY_TASK_STATE_FAILED", "ACTIVITY_TASK_STATE_CANCELED":
		return nil, zip.ErrConflict("cancel: the activity already ended (" + a.Status + ")")
	}
	if err := eng.CancelActivityForOrg(who.Org, in.Namespace, in.ID, in.ID, in.Reason, who.User); err != nil {
		return nil, err
	}
	return &client.Unit{}, nil
}

// row relays one activity as the engine's own JSON.
func row(a *tasks.StandaloneActivity) (*client.Activity, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return &client.Activity{Row: b}, nil
}
