// Package grant implements manual subscription grants — used for gifting Pro
// subscriptions, comp'ing beta users, and admin overrides.
//
// A grant creates an active subscription directly, bypassing payment flow.
// The subscription is marked with ProviderType="manual_gift" and the supplied
// reason is stored in metadata.
//
// This package deliberately has no HTTP surface — it is consumed by the
// cmd/grant CLI (and future admin tooling) and tested directly against an
// in-memory datastore.
package grant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/plan"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	types "github.com/hanzoai/cloud/clients/commerce/types"
)

// ErrPlanNotFound is returned when the requested plan slug does not exist in
// the catalog or in the org's datastore.
var ErrPlanNotFound = errors.New("grant: plan not found")

// ErrInvalidUser is returned when the user identifier is missing or malformed.
var ErrInvalidUser = errors.New("grant: invalid user id")

// PlanCatalog is the minimal interface into the static plan catalog. The CLI
// passes billing.LookupStaticPlan; tests pass a map-backed fake.
type PlanCatalog interface {
	Lookup(slug string) *CatalogPlan
}

// CatalogPlan is the catalog projection used by Grant for seeding missing
// plans into the org datastore.
type CatalogPlan struct {
	Slug        string
	Name        string
	Description string
	PriceCents  int64
	Currency    string // e.g. "usd"
}

// Request captures the parameters for a manual grant.
type Request struct {
	// UserId is the IAM subject — typically "owner/name" format.
	UserId string

	// PlanSlug is the plan slug (e.g. "world-pro").
	PlanSlug string

	// Duration specifies how long the subscription lasts. Defaults to 12 months
	// when zero.
	Duration time.Duration

	// Reason is a short operator-supplied description (e.g. "beta gift").
	// Stored in subscription.Metadata for audit.
	Reason string

	// GrantedBy is the operator identifier (email or name) for audit.
	GrantedBy string
}

// Result describes what the grant produced.
type Result struct {
	SubscriptionID string
	PlanSlug       string
	UserId         string
	PeriodEnd      time.Time
	Reason         string
	GrantedBy      string
}

// Grant creates a manual subscription for the given user on the given plan.
//
// The operation is idempotent per (UserId, PlanSlug): if an active manual grant
// already exists, it is extended rather than duplicated.
//
// The caller supplies an org-scoped datastore (db) and a catalog lookup. Plan
// records are seeded into the org's datastore on first use so the subscription
// can reference them by Id.
func Grant(ctx context.Context, db *datastore.Datastore, catalog PlanCatalog, req Request) (*Result, error) {
	if strings.TrimSpace(req.UserId) == "" {
		return nil, ErrInvalidUser
	}
	if strings.TrimSpace(req.PlanSlug) == "" {
		return nil, fmt.Errorf("grant: plan slug required")
	}

	// Resolve plan: prefer DB record (keeps Ids stable across grants), fall
	// back to static catalog and seed into DB on first use.
	pln := plan.New(db)
	found, qErr := pln.Query().Filter("Slug=", req.PlanSlug).Get()
	if qErr != nil {
		return nil, fmt.Errorf("grant: lookup plan %s: %w", req.PlanSlug, qErr)
	}
	if !found {
		// Not in DB — pull from catalog.
		cp := catalog.Lookup(req.PlanSlug)
		if cp == nil {
			return nil, fmt.Errorf("%w: %s", ErrPlanNotFound, req.PlanSlug)
		}
		pln.Slug = cp.Slug
		pln.Name = cp.Name
		pln.Description = cp.Description
		pln.Price = currency.Cents(cp.PriceCents)
		curr := cp.Currency
		if curr == "" {
			curr = "usd"
		}
		pln.Currency = currency.Type(curr)
		pln.Interval = types.Monthly
		pln.IntervalCount = 1
		if err := pln.Create(); err != nil {
			return nil, fmt.Errorf("grant: seed plan %s: %w", req.PlanSlug, err)
		}
	}

	// Idempotency: existing active manual grant for the same user+plan?
	existing := subscription.New(db)
	foundExisting, _ := existing.Query().
		Filter("UserId=", req.UserId).
		Filter("PlanId=", pln.Id()).
		Filter("Status=", string(subscription.Active)).
		Get()
	if foundExisting && existing.Id() != "" {
		// Extend period end if the new grant pushes it out.
		newEnd := endFromDuration(req.Duration)
		if newEnd.After(existing.PeriodEnd) {
			existing.PeriodEnd = newEnd
			if existing.Metadata == nil {
				existing.Metadata = make(map[string]interface{})
			}
			existing.Metadata["last_grant_reason"] = req.Reason
			existing.Metadata["last_grant_by"] = req.GrantedBy
			existing.Metadata["last_grant_at"] = time.Now().UTC().Format(time.RFC3339)
			if err := existing.Update(); err != nil {
				return nil, fmt.Errorf("grant: extend subscription: %w", err)
			}
		}
		return &Result{
			SubscriptionID: existing.Id(),
			PlanSlug:       pln.Slug,
			UserId:         existing.UserId,
			PeriodEnd:      existing.PeriodEnd,
			Reason:         req.Reason,
			GrantedBy:      req.GrantedBy,
		}, nil
	}

	// Create new subscription.
	sub := subscription.New(db)
	sub.UserId = req.UserId
	sub.ProviderType = "manual_gift"
	sub.Quantity = 1
	sub.Metadata = map[string]interface{}{
		"grant_reason":   req.Reason,
		"granted_by":     req.GrantedBy,
		"granted_at":     time.Now().UTC().Format(time.RFC3339),
		"billing_reason": "manual_gift",
	}

	// StartSubscription fills Plan/PlanId/PeriodStart/PeriodEnd/Status from
	// the plan's billing cadence. We then override PeriodEnd to reflect the
	// gift duration.
	engine.StartSubscription(sub, pln)
	sub.PeriodEnd = endFromDuration(req.Duration)

	if err := sub.Create(); err != nil {
		return nil, fmt.Errorf("grant: create subscription: %w", err)
	}

	return &Result{
		SubscriptionID: sub.Id(),
		PlanSlug:       pln.Slug,
		UserId:         sub.UserId,
		PeriodEnd:      sub.PeriodEnd,
		Reason:         req.Reason,
		GrantedBy:      req.GrantedBy,
	}, nil
}

// endFromDuration returns the grant's current_period_end.
// Default duration is 12 months.
func endFromDuration(d time.Duration) time.Time {
	now := time.Now().UTC()
	if d <= 0 {
		return now.AddDate(1, 0, 0)
	}
	return now.Add(d)
}
