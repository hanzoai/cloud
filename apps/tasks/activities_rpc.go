// Copyright © 2026 Hanzo AI. MIT License.

package tasks

import (
	"context"
	"encoding/json"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
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
// sitting in this app's `fleet` namespace, and /v1/machines, /v1/gpus,
// /v1/fleet/workers and studio's node badges all answering "no GPUs" — so a
// connected renderer could not be seen, targeted, or reasoned about.
//
// So the engine is asked, not opened — the same shape commerce's ledger takes
// (apps/commerce/balance_rpc.go). This op is the read; the writer stays exactly
// where it is.

// exposeActivities publishes the org-scoped engine read on the internal plane.
// Mount calls it.
func exposeActivities() {
	zip.Post[plane.ActivitiesIn, plane.Activities](cloud.Plane(), "/tasks/activities", planeActivities,
		zip.WithOperationID(plane.TasksActivities),
		zip.WithSummary("One page of a namespace's standalone activities"))
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
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeActivities(ctx context.Context, in *plane.ActivitiesIn) (*plane.Activities, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("activities: no org on the call")
	}
	eng := cloud.EmbeddedTasks()
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
	return &plane.Activities{Rows: b, Next: next}, nil
}
