// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// limit_rpc.go — THE BOUNDS STAFF SET, read and written where the store is.
//
// A limit resolves org, then plan, then default (client/limit.go). The rows live
// beside the plan authority (hanzoai/commerce models/limit) and the plan an account
// is served on is this process's to read, so the resolution is answered here and
// nowhere else: a consumer asks it, admin.hanzo.ai writes it through the admin ops,
// which gate and audit the act and ask this process, the one that holds the store.

import (
	"cmp"
	"context"
	"net/http"
	"strings"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/datastore"
	commercelimit "github.com/hanzoai/commerce/models/limit"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/lock"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

// exposeLimits publishes the limit read and its two writes. Use calls it.
func exposeLimits() {
	cloud.Plane().Post("/billing/limits", planeLimits,
		zip.WithOperationID(client.BillingLimits),
		zip.WithSummary("Every limit as it stands for this org, or for one plan"))
	cloud.Plane().Post("/billing/limit/set", planeLimitSet,
		zip.WithOperationID(client.BillingLimitSet),
		zip.WithSummary("Set one limit on one org or one plan"))
	cloud.Plane().Post("/billing/limit/drop", planeLimitDrop,
		zip.WithOperationID(client.BillingLimitDrop),
		zip.WithSummary("Remove one limit from one org or one plan"))
}

// limitWrites orders, in this process, the writes of one limit row; across replicas the
// row's lock is the database's (lock.Keyed.Across).
var limitWrites lock.Keyed

// limitKey names the row a write is to: the slug the row is stored under.
func limitKey(scope, name string) string { return "limit:" + scope + "/" + name }

// Reads every registered limit as it stands for the caller's org: for each, the
// value that holds, the level it came from — the org's row, else the row of the
// plan the account is served on, else the registry's default — and the row each
// level holds. Subject names the account in the org whose plan applies, such as a
// person in the signup org; empty is the org's own. The signup org holds no org
// rows: each person in it is held to their plan's.
//
// With Plan named it reads that plan's limits instead: its rows over the defaults.
// A plan's are read by a SuperAdmin or the platform.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeLimits(ctx context.Context, in *client.LimitsIn) (*client.LimitSheet, error) {
	db := commercelimit.AuthorityDB(ctx)
	if plan := strings.ToLower(strings.TrimSpace(in.Plan)); plan != "" {
		if who := cloud.Who(ctx); !who.Admin && who.Org != principal.Reserved {
			return nil, zip.ErrForbidden("limits: a plan's are read by a SuperAdmin or the platform")
		}
		plans, err := limitRows(db, client.LimitPlan+":"+plan)
		if err != nil {
			return nil, err
		}
		return &client.LimitSheet{Plan: plan, Values: standing(nil, plans)}, nil
	}
	name, err := callerOrg(ctx, "limits")
	if err != nil {
		return nil, err
	}
	o, err := orgOf(ctx, "limits")
	if err != nil {
		return nil, err
	}
	view, err := commercebilling.ReadTier(ctx, o, cmp.Or(strings.TrimSpace(in.Subject), name))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "limits: the plan of %s: %v", name, err)
	}
	plan := strings.ToLower(strings.TrimSpace(view.Plan))
	var orgs, plans map[string]*client.LimitRow
	if _, _, refused := client.LimitScope(client.LimitOrg + ":" + name); refused == nil {
		if orgs, err = limitRows(db, client.LimitOrg+":"+name); err != nil {
			return nil, err
		}
	}
	if plan != "" {
		if plans, err = limitRows(db, client.LimitPlan+":"+plan); err != nil {
			return nil, err
		}
	}
	return &client.LimitSheet{Org: name, Plan: plan, Values: standing(orgs, plans)}, nil
}

// Sets one registered limit on one scope — org:<org> or plan:<slug> — to a value in
// its range, or -1 where it allows no bound, with the reason it is set. The org's
// row outranks its plan's, and the plan's the default. A key not in the registry,
// a value out of range, a scope that names neither or the signup org, and a missing
// reason are 400.
//
// SuperAdmin only. The admin op that calls it puts the act on the audit trail
// before it asks.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeLimitSet(ctx context.Context, in *client.LimitSet) (*client.LimitRow, error) {
	who := cloud.Who(ctx)
	if !who.Admin {
		return nil, zip.ErrForbidden("limit set: SuperAdmin required")
	}
	l, scope, err := checkLimit(in.Key, in.Scope)
	if err != nil {
		return nil, err
	}
	if err := l.Check(in.Value); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, zip.ErrBadRequest("limit set: a reason is required")
	}
	return writeLimit(ctx, &limitWrites, l, scope, in.Value, reason)
}

// writeLimit sets one limit on one scope as the only write of that row, on every replica:
// the row is found by its slug and created or updated, and two sets that each found none
// would create two rows, which a drop would then remove one of. The limit rows are the
// commerce module's datastore's and this package has no store of its own to hold the
// lock on, so it is held on the process's lock pool (locus.Locks) under the slug.
func writeLimit(ctx context.Context, k *lock.Keyed, l client.Limit, scope string, value int64, reason string) (*client.LimitRow, error) {
	who := cloud.Who(ctx)
	release, err := k.Across(ctx, nil, limitKey(scope, l.Key))
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "limit set: %s is being written elsewhere; send it again", l.Key)
	}
	defer release()
	db := commercelimit.AuthorityDB(ctx)
	row := commercelimit.New(db)
	found, err := row.Query().Filter("Slug=", scope+"/"+l.Key).Get()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "limit set: %v", err)
	}
	if !found {
		row = commercelimit.New(db)
	}
	row.Name, row.Scope, row.Value, row.Reason = l.Key, scope, value, reason
	row.UpdatedBy = cmp.Or(who.Email, who.User)
	row.Bind()
	if found {
		err = row.Update()
	} else {
		err = row.Create()
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "limit set: %v", err)
	}
	toldCap(scope)
	return limitWire(row), nil
}

// Removes one limit's row from one scope and answers the row removed, so the scope
// falls back to the next level: an org to its plan's row, a plan to the default.
// A scope with no row for the limit is 404.
//
// SuperAdmin only. The admin op that calls it puts the act on the audit trail
// before it asks.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeLimitDrop(ctx context.Context, in *client.LimitDrop) (*client.LimitRow, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("limit drop: SuperAdmin required")
	}
	l, scope, err := checkLimit(in.Key, in.Scope)
	if err != nil {
		return nil, err
	}
	return dropLimit(ctx, &limitWrites, l, scope)
}

// dropLimit removes one limit's row as the only write of that row, on every replica.
func dropLimit(ctx context.Context, k *lock.Keyed, l client.Limit, scope string) (*client.LimitRow, error) {
	release, err := k.Across(ctx, nil, limitKey(scope, l.Key))
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "limit drop: %s is being written elsewhere; send it again", l.Key)
	}
	defer release()
	row := commercelimit.New(commercelimit.AuthorityDB(ctx))
	found, err := row.Query().Filter("Slug=", scope+"/"+l.Key).Get()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "limit drop: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("no " + l.Key + " is set on " + scope)
	}
	if err := row.Delete(); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "limit drop: %v", err)
	}
	toldCap(scope)
	return limitWire(row), nil
}

// checkLimit is the registered limit key names and its scope, normalized, or the
// 400 that refuses them.
func checkLimit(key, scope string) (client.Limit, string, error) {
	l, ok := client.LimitOf(strings.TrimSpace(key))
	if !ok {
		return client.Limit{}, "", zip.ErrBadRequest("no limit " + key + " is registered")
	}
	kind, name, err := client.LimitScope(scope)
	if err != nil {
		return client.Limit{}, "", zip.ErrBadRequest(err.Error())
	}
	if kind == client.LimitPlan {
		name = strings.ToLower(name)
	}
	return l, kind + ":" + name, nil
}

// limitRows is every row on one scope, by key.
func limitRows(db *datastore.Datastore, scope string) (map[string]*client.LimitRow, error) {
	var rows []*commercelimit.Limit
	if _, err := commercelimit.Query(db).Filter("Scope=", scope).GetAll(&rows); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "limits %s: %v", scope, err)
	}
	out := make(map[string]*client.LimitRow, len(rows))
	for _, r := range rows {
		out[r.Name] = limitWire(r)
	}
	return out, nil
}

// standing is every registered limit as it stands over the org's rows and the
// plan's: the org's row, else the plan's, else the default. A row for a key the
// registry no longer names is not read.
func standing(orgs, plans map[string]*client.LimitRow) []client.LimitValue {
	out := make([]client.LimitValue, 0, len(client.Limits))
	for _, l := range client.Limits {
		v := client.LimitValue{Key: l.Key, Value: l.Default, Source: client.LimitDefault, Org: orgs[l.Key], Plan: plans[l.Key]}
		if v.Plan != nil {
			v.Value, v.Source = v.Plan.Value, client.LimitPlan
		}
		if v.Org != nil {
			v.Value, v.Source = v.Org.Value, client.LimitOrg
		}
		out = append(out, v)
	}
	return out
}

// limitWire is a stored row as the plane carries it.
func limitWire(m *commercelimit.Limit) *client.LimitRow {
	return &client.LimitRow{Key: m.Name, Scope: m.Scope, Value: m.Value, Reason: m.Reason,
		UpdatedBy: m.UpdatedBy, UpdatedAt: m.UpdatedAt.Unix()}
}
