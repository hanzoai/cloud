// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.

package commerce

// subs_rpc.go — the org's subscriptions, over the internal plane.
//
// Who is subscribed is commerce's fact, and every process that is not commerce
// used to ask for it by re-entering commerce's own HTTP door: a GET of
// /v1/billing/subscriptions through CLOUD_COMMERCE_HTTP_URL, which production
// points at commerce.hanzo.svc:8001 — a Service selecting
// `app.kubernetes.io/name: cloud` on targetPort 8000, this pod's own public
// edge. The request left the process and came back through the front door, which
// is the re-entry apps/commerce/transport's maxDepth counter exists to survive.
//
// A call by name cannot express that mistake, which is the whole argument for
// the plane over any URL.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	billingapi "github.com/hanzoai/commerce/api/billing"
	"github.com/zap-proto/zip"
)

// planeSubs lists the caller org's subscriptions — which plan, what state, and
// when it began — filtered by user and status the same way the HTTP door filters
// them, because it IS the same query: commerce exports it as a value-taking core
// (billing.ListSubscriptions) and both callers ask that one function. Deriving
// the filter again here would be a second implementation of one question, and
// two copies of a subscription filter is how a billing page and a plan gate come
// to disagree about who is subscribed.
//
// The org comes from the CALLER, never the payload — SubsIn has no org field, so
// one tenant cannot read another's subscriptions. It sends rows rather than a
// rendered envelope: the HTTP surface builds its own from these, and putting the
// renderer next to the datastore is the import cycle that shape implies.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSubs(ctx context.Context, in *plane.SubsIn) (*plane.Subs, error) {
	org, err := payingOrg(ctx, "subs")
	if err != nil {
		return nil, err
	}
	var userID, status string
	if in != nil {
		userID, status = strings.TrimSpace(in.UserID), strings.TrimSpace(in.Status)
	}
	subs, err := billingapi.ListSubscriptions(ctx, org, userID, status)
	if err != nil {
		return nil, zip.Errorf(502, "subs: list subscriptions: %v", err)
	}
	rows := make([]plane.Sub, 0, len(subs))
	for _, s := range subs {
		if s == nil {
			continue
		}
		// The plan SLUG is what a caller deciding "is this a paid tier" reads.
		// It lives on the joined Plan when one is loaded and on the id otherwise,
		// which is the same fallback the HTTP renderer applies.
		slug := s.Plan.Slug
		if slug == "" {
			slug = s.PlanId
		}
		rows = append(rows, plane.Sub{
			ID:       s.Id(),
			UserID:   s.UserId,
			PlanID:   s.PlanId,
			PlanSlug: slug,
			Status:   string(s.Status),
			// PeriodStart, not a created stamp: the model carries no CreatedAt, and
			// the period is what a reader asking "since when" means here. It is the
			// same field the HTTP renderer publishes as currentPeriodStart.
			StartedAt: s.PeriodStart.Unix(),
		})
	}
	return &plane.Subs{Rows: rows}, nil
}

// exposeSubs publishes the subscriptions read. Mount calls it.
func exposeSubs() {
	zip.Post[plane.SubsIn, plane.Subs](cloud.Plane(), "/finance/subs", planeSubs,
		zip.WithOperationID(plane.FinanceSubs),
		zip.WithSummary("Subscriptions for this org"))
}
