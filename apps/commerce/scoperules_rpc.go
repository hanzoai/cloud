// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// scoperules_rpc.go — the org's per-scope rate ceilings, published on the
// internal plane.
//
// The rows are spend-alert rows and they live HERE, in the process that mounts
// commerce. Their reader is cloud's ScopeRateLimit, an EDGE middleware in
// whatever process serves the request — so it used to fetch them with a
// service-token GET /v1/billing/alerts through the commerce transport. That
// transport dispatches in-process by publishing the WHOLE shared app, so the
// fetch re-ran the entire edge chain, including ScopeRateLimit, whose cache is
// still cold because it is only filled after the fetch returns. It asked again,
// and again, to the transport's depth guard: 502, ~135 of them per half hour on
// the live pod. A split deploy was no better — the same GET went back out to the
// public edge and recursed over the network instead.
//
// A plane op has no edge chain on it. The socket reaches this app's own ops and
// nothing else (see cloud/plane.go), so the read cannot re-enter the middleware
// that made it — the recursion is absent rather than bounded.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	commercedatastore "github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/spendalert"
	commercensctx "github.com/hanzoai/commerce/util/nscontext"
	"github.com/zap-proto/zip"
)

// scopeRuleLimit bounds the row scan, matching commerce's own cap on the write
// side (CreateSpendAlert) and on its internal read (loadOrgScopes): an org that
// could inflate its row set could slow every request it makes.
const scopeRuleLimit = 200

// exposeScopeRules publishes the rate-limit config read. Mount calls it.
func exposeScopeRules() {
	zip.Post[struct{}, plane.ScopeRules](cloud.Plane(), "/finance/scope-rules", planeScopeRules,
		zip.WithOperationID(plane.FinanceScopeRules),
		zip.WithSummary("Per-scope request-rate ceilings from this org's spend-alert rows"))
}

// Lists the caller org's per-scope request-rate ceilings, so the edge limiter in
// another process can enforce a budget whose rows it cannot open.
//
// It takes NO input: the org rides the caller and there is nothing else to name,
// so one org can never read another's ceilings. Only rows that SET a ceiling are
// returned — a spend-alert row with no rate limit is a spend cap, a different
// policy answered by a different op, and shipping it here would make the limiter
// weigh rules that bind nothing.
//
// The row scan is the org's WHOLE policy set — the same query commerce's own cap
// verdict runs (loadOrgScopes), bounded the same way. The retired HTTP read went
// through the per-subject list instead, so the rate ceiling and the spend cap
// could in principle bind on different rows; one query is what makes that
// impossible rather than merely unlikely.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeScopeRules(ctx context.Context, _ *struct{}) (*plane.ScopeRules, error) {
	org, err := callerOrg(ctx, "scope rules")
	if err != nil {
		return nil, err
	}
	db := commercedatastore.New(commercensctx.WithNamespace(ctx, org))
	rows := make([]*spendalert.SpendAlert, 0)
	if _, err := spendalert.Query(db).
		Ancestor(db.NewKey("synckey", "", 1, nil)).
		Limit(scopeRuleLimit).
		GetAll(&rows); err != nil {
		return nil, err
	}
	out := plane.ScopeRules{Rules: make([]plane.ScopeRule, 0, len(rows))}
	for _, r := range rows {
		if r.RateLimitRpm <= 0 {
			continue
		}
		out.Rules = append(out.Rules, plane.ScopeRule{
			Project:      r.Project,
			Service:      r.Service,
			RateLimitRpm: r.RateLimitRpm,
		})
	}
	return &out, nil
}
