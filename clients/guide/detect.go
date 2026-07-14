package guide

import (
	"context"
	"fmt"

	aiobject "github.com/hanzoai/ai/object"
)

// Detector reports whether a step's done-criterion is satisfied by the org's real
// state. It is the auto-detect seam: a step names a Signal, the engine looks the
// detector up by that name and runs it. A detector MUST be honest — a data source
// it cannot reach returns an error (treated as "not present"), never a spurious
// true.
type Detector func(ctx context.Context, org string, step Step) (bool, error)

// newDetectors wires the shipped auto-detect signals. storeFor resolves the
// caller's per-org store for the self-ledger "acted" signal; external detectors
// ignore it and probe sibling subsystem state directly.
//
//   - "acted":     a prior "do it for me" call on the step's tool succeeded, so the
//     real effect (e.g. a Content draft) landed. Self-contained and
//     always available.
//   - "analytics": insights events exist for the org in the shared analytics
//     warehouse ("analytics is emitting"). Honest-degrading when the
//     warehouse is unreachable/uninitialised.
//
// The map is the ONLY coupling point: tests inject their own detectors to exercise
// the reconcile logic deterministically, and a future curriculum can name a new
// signal the day a new detector is registered here.
func newDetectors(storeFor func(ctx context.Context, org string) (*Store, error)) map[string]Detector {
	return map[string]Detector{
		"acted": func(ctx context.Context, org string, step Step) (bool, error) {
			if step.Tool == "" {
				return false, nil
			}
			st, err := storeFor(ctx, org)
			if err != nil {
				return false, err
			}
			return st.HasSuccessfulAction(ctx, step.Tool)
		},
		"analytics": detectAnalyticsEmitting,
	}
}

// eventsTable is the shared analytics warehouse's insights-event table, keyed on
// tenant_id (== the IAM org slug) — the SAME table + tenancy column clients/analytics
// reads (query.go eventsWhere).
const eventsTable = "hanzo.events"

// detectAnalyticsEmitting reports whether insights events exist for the org. It
// binds the org POSITIONALLY (nothing user-derived is interpolated) against the
// shared datastore the analytics subsystem already opens. An error (warehouse down,
// datastore not initialised in this deployment) is returned so the reconcile loop
// leaves the step untouched — auto-detect is best-effort and never a false done.
func detectAnalyticsEmitting(ctx context.Context, org string, _ Step) (bool, error) {
	rows, err := aiobject.DatastoreQuery(ctx, "SELECT count() AS n FROM "+eventsTable+" WHERE tenant_id = ?", org)
	if err != nil {
		return false, err
	}
	return firstCount(rows) > 0, nil
}

// firstCount reads the "n" column of the first row across the numeric types a
// datastore count() can surface (UInt64/Int64/Float64/string).
func firstCount(rows []map[string]any) int64 {
	if len(rows) == 0 {
		return 0
	}
	switch v := rows[0]["n"].(type) {
	case int64:
		return v
	case uint64:
		return int64(v)
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case string:
		var n int64
		_, _ = fmt.Sscan(v, &n)
		return n
	default:
		return 0
	}
}

// reconcile runs each step's auto-detect signal and marks a non-terminal step done
// (via mark, which records source "auto") when its detector reports the org's real
// state present. It never downgrades a terminal state, skips steps with no signal
// or an unregistered signal, and treats a detector error as "not present" — so one
// unreachable data source can neither flip a step nor fail the whole request. The
// states map is updated in place so the caller sees the post-detect view.
func reconcile(ctx context.Context, org string, c Curriculum, states map[string]State, dets map[string]Detector, mark func(stepID string) error) error {
	for _, s := range c.Steps {
		if s.Signal == "" || terminal(stateOf(states, s.ID)) {
			continue
		}
		det, ok := dets[s.Signal]
		if !ok {
			continue
		}
		present, err := det(ctx, org, s)
		if err != nil || !present {
			continue
		}
		if err := mark(s.ID); err != nil {
			return err
		}
		states[s.ID] = StateDone
	}
	return nil
}
