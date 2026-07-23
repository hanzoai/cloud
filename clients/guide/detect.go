package guide

import (
	"context"
	"fmt"
	"strings"

	aiobject "github.com/hanzoai/ai/object"
)

// Detector reports whether a step's done-criterion is satisfied by the org's real
// state. It is the auto-detect seam: a step names a Signal, the engine looks the
// detector up by that name and runs it. A detector MUST be honest — a data source
// it cannot reach returns an error (treated as "not present"), never a spurious
// true.
type Detector func(ctx context.Context, org string, step Step) (bool, error)

// newDetectors wires the auto-detect signals. storeFor resolves the caller's per-org
// store for the self-ledger "acted" signal; sig is the injected cross-subsystem read
// seam the growth signals probe (Signals, bound at the composition root) — guide
// imports no sibling subsystem, so a nil seam field honest-degrades its signal to
// not-present, never a spurious true.
//
// Self-contained + analytics:
//   - "acted":     a prior "do it for me" call on the step's tool succeeded, so the
//     real effect (e.g. a Content draft) landed. Always available.
//   - "analytics": insights events exist for the org in the shared analytics
//     warehouse ("analytics is emitting"). Honest-degrading when the
//     warehouse is unreachable/uninitialised.
//
// Growth signals (read the platform's OWN truth, org-scoped, honest-degrading) —
// registered under their KIND so a parameterized step signal resolves here via
// lookupDetector, ONE detector serving every param:
//   - "module":    module:<name>     the org installed a framework module (Signals.ModuleInstalled).
//   - "connected": connected:<prov>  the org connected an integration provider — boolean, never the token.
//   - "funnel":    funnel:<stage>    the org's analytics funnel reached a stage (visitors/signups/orders).
//   - "deployed":                    the org has a live deployment (Signals.HasDeployment).
//   - "revenue":                     the org has recorded revenue > 0 (Signals.RevenueCents).
//   - "customers": customers:>=<N>   the org's own record count crossed a threshold (Signals.RecordCount).
//
// The map is THE coupling point: tests inject their own detectors (or a fake sig) to
// exercise the logic deterministically, and a curriculum can name a new signal the
// day its detector is registered here.
func newDetectors(storeFor func(ctx context.Context, org string) (*Store, error), sig Signals) map[string]Detector {
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

		kindModule: func(ctx context.Context, org string, step Step) (bool, error) {
			_, name, _ := strings.Cut(step.Signal, ":")
			if sig.ModuleInstalled == nil || name == "" {
				return false, nil
			}
			return sig.ModuleInstalled(ctx, org, name), nil
		},
		kindConnected: func(ctx context.Context, org string, step Step) (bool, error) {
			_, provider, _ := strings.Cut(step.Signal, ":")
			if sig.ConnectorPresent == nil || provider == "" {
				return false, nil
			}
			return sig.ConnectorPresent(ctx, org, provider), nil
		},
		kindFunnel: func(ctx context.Context, org string, step Step) (bool, error) {
			_, stage, _ := strings.Cut(step.Signal, ":")
			return funnelStagePresent(analyticsFunnel(ctx, org), stage), nil
		},
		SignalDeployed: func(ctx context.Context, org string, _ Step) (bool, error) {
			if sig.HasDeployment == nil {
				return false, nil
			}
			return sig.HasDeployment(ctx, org)
		},
		SignalRevenue: func(ctx context.Context, org string, _ Step) (bool, error) {
			if sig.RevenueCents == nil {
				return false, nil
			}
			cents, err := sig.RevenueCents(ctx, org)
			if err != nil {
				return false, err
			}
			return cents > 0, nil
		},
		kindCustomers: func(ctx context.Context, org string, step Step) (bool, error) {
			if sig.RecordCount == nil {
				return false, nil
			}
			n, err := sig.RecordCount(ctx, org)
			if err != nil {
				return false, err
			}
			return n >= parseThreshold(step.Signal, customersThreshold), nil
		},
	}
}

// lookupDetector resolves a step's signal to its detector: an EXACT registered
// signal (acted, analytics, deployed, revenue) or a parameterized growth signal
// "<kind>:<param>" (module:cms, connected:stripe, funnel:signups, customers:>=100)
// whose KIND is registered. The parameterized detector reads its param from
// step.Signal, so the Detector map stays the single coupling point even as the
// vocabulary grows. Exact wins over kind, so a curriculum could still register a
// fully-specified override signal without shadowing the family.
func lookupDetector(dets map[string]Detector, signal string) (Detector, bool) {
	if d, ok := dets[signal]; ok {
		return d, true
	}
	if kind, _, ok := strings.Cut(signal, ":"); ok {
		if d, ok := dets[kind]; ok {
			return d, true
		}
	}
	return nil, false
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
		det, ok := lookupDetector(dets, s.Signal)
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
