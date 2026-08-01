package agents

import (
	"context"
	"sort"
)

// crossorg.go holds the only two reads in this package that legitimately span
// tenants, and holding them together is the point: with one file per org a
// cross-org read is now a deliberate fold over every org's database, so it
// cannot be written by forgetting a predicate — it can only be written on
// purpose, here, where the reason it is allowed is stated next to it.
//
// Both fold state.eachStore, which enumerates the orgs that have a store ON
// DISK. The filesystem is the source of truth for "which orgs exist", so no
// derived registry can drift out of step with it, and under horizontal sharding
// each writer folds exactly the orgs routed to it.

// allLongRunning is every scheduled long-running agent across every org — the
// scheduler's work set, and the reason it may cross tenants: it is an
// in-process, trusted subsystem, not a tenant request. Each returned Agent
// carries its own Org, so every downstream action (run, gate, meter) is scoped
// back to that agent's own tenant.
//
// One org's unreadable file is logged by the caller and skipped, never fatal: a
// scheduler that stops for the whole fleet because a single org's database will
// not open is a worse failure than the one it is reporting.
//
// This costs more than the single-file query it replaces, and the cost is worth
// naming: the once-a-minute tick now touches every org's file rather than one
// indexed range. OrgStore caches, so it is an open-once then a read per org, and
// it is the same shape apps/sync's reconcile sweep has run on for as long as it
// has existed — but a deployment with very many orgs pays it every minute, and
// the answer when that hurts is a bound on open handles in OrgStore, not a
// second cross-org index here.
func (st *state) allLongRunning(ctx context.Context) ([]Agent, []error) {
	var out []Agent
	var errs []error
	ferr := st.eachStore(func(_ string, sto *Store, err error) {
		if err != nil {
			errs = append(errs, err)
			return
		}
		rows, lerr := sto.ListLongRunning(ctx)
		if lerr != nil {
			errs = append(errs, lerr)
			return
		}
		out = append(out, rows...)
	})
	if ferr != nil {
		errs = append(errs, ferr)
	}
	// Stable fleet-wide order (org, then name) so a tick's work set does not
	// depend on directory-read order — the same ordering the single-file
	// ListLongRunning query produced with ORDER BY org, name.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Org != out[j].Org {
			return out[i].Org < out[j].Org
		}
		return out[i].Name < out[j].Name
	})
	return out, errs
}

// publishedBuild is one published root session paired with the store it came
// from, so the caller can count its events without resolving that org's file a
// second time.
type publishedBuild struct {
	Session Session
	Store   *Store
}

// allPublishedBuilds is every published build across every org, most recently
// updated first, capped at limit. It is public and deliberately not
// tenant-scoped, because the predicate IS the author's publish flag: a session
// appears only because its own author set it, so cross-org visibility here is a
// grant, not a missing filter.
//
// The per-org queries each apply the same limit and the merged result is capped
// again, so the answer is identical to the single-file query's whenever fewer
// than limit builds exist fleet-wide, and is the newest limit of them otherwise.
func (st *state) allPublishedBuilds(ctx context.Context, limit int) ([]publishedBuild, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []publishedBuild
	err := st.eachStore(func(_ string, sto *Store, err error) {
		if err != nil {
			return // an org whose file will not open publishes nothing
		}
		rows, lerr := sto.ListPublishedBuilds(ctx, limit)
		if lerr != nil {
			return
		}
		for _, x := range rows {
			out = append(out, publishedBuild{Session: x, Store: sto})
		}
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Session.UpdatedAt != out[j].Session.UpdatedAt {
			return out[i].Session.UpdatedAt > out[j].Session.UpdatedAt
		}
		return out[i].Session.ID < out[j].Session.ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
