// Package trial implements the new-signup on-ramp: a trialing subscription of
// the $20/mo entry plan funded with a single unified trial credit.
//
// The model (settled 2026-07-02):
//
//   - Entry plan is the $20/mo "starter" plan (billing/tier.Starter): its
//     allowance is metered down across usage. After the trial the subscriber is
//     on $20/mo, metered, with overage billed.
//   - A brand-new signup with NO card gets a 7-day trial; adding a card extends
//     it to 30 days (ExtendForCard). A signup that adds a card up front is just
//     the 7-day path immediately extended to 30 — ExtendForCard starts a 30-day
//     trial when none exists yet.
//   - The trial allowance is ONE unified credit spendable across compute OR AI:
//     it is an ordinary tag-scoped, expiring Deposit — the SAME mechanism as
//     billing/allotment's included credit — so it nets into the single balance
//     the gateway's prepaid gate (available > 0) reads and both compute and AI
//     usage debit. No separate buckets, no second gate.
//
// Decomplected from the plan catalog: this package never reads @hanzo/plans. The
// catalog owner (api/billing) wires the entry plan's economics once via
// SetEntryPlanResolver; here we only orchestrate subscription + credit.
package trial

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/plan"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/types"
)

const (
	// PlanSlug is the entry ($20/mo) plan trials seed from. It aligns with the
	// "starter" billing tier (billing/tier.Starter).
	PlanSlug = "starter"

	// CreditTag marks the one unified trial-credit deposit. Exactly one per
	// subject; it is both the funding record and the idempotency anchor.
	CreditTag = "trial-credit"

	// Kind is the destination kind for IAM-bound balance transactions — the
	// same kind the balance read/gate resolves (models/transaction/util).
	Kind = "iam-user"

	// NoCardTrialDays / CardTrialDays are the trial windows. A card on file
	// extends the trial from 7 to 30 days.
	NoCardTrialDays = 7
	CardTrialDays   = 30

	// ProviderType marks trial subscriptions (vs "manual_gift" / "stripe").
	ProviderType = "trial"
)

// ErrInvalidSubject is returned when the billing subject is empty.
var ErrInvalidSubject = errors.New("trial: invalid subject")

// Plan is the entry-plan projection the trial needs: the recurring price to
// bill after the trial, and the unified credit to fund during it. The catalog
// owner supplies it (see SetEntryPlanResolver).
type Plan struct {
	Slug        string
	Name        string
	Description string
	PriceCents  int64 // recurring monthly price after the trial
	CreditCents int64 // unified trial credit funded for the trial window
	Currency    string
}

// Result describes the outcome of a trial operation.
type Result struct {
	Started        bool      `json:"started"`  // this call created a new trial
	Extended       bool      `json:"extended"` // this call extended 7 -> 30 days
	Reason         string    `json:"reason,omitempty"`
	SubscriptionID string    `json:"subscriptionId,omitempty"`
	Plan           string    `json:"plan"`
	TrialDays      int       `json:"trialDays"`
	TrialEnd       time.Time `json:"trialEnd,omitempty"`
	CreditCents    int64     `json:"creditCents"`
	TransactionID  string    `json:"transactionId,omitempty"`
}

var (
	entryPlanMu sync.RWMutex
	entryPlanFn func() Plan
)

// SetEntryPlanResolver wires the catalog-backed resolver for the entry ($20)
// plan. Called once at startup by the catalog owner (api/billing). Kept as a
// resolver (not a value) so this package never imports the catalog and startup
// init-order does not matter. When unset, trial operations are safe no-ops so
// binaries that never load the catalog still build and run.
func SetEntryPlanResolver(fn func() Plan) {
	entryPlanMu.Lock()
	entryPlanFn = fn
	entryPlanMu.Unlock()
}

func resolveEntryPlan() (Plan, bool) {
	entryPlanMu.RLock()
	fn := entryPlanFn
	entryPlanMu.RUnlock()
	if fn == nil {
		return Plan{}, false
	}
	p := fn()
	if p.Slug == "" || p.CreditCents <= 0 {
		return Plan{}, false
	}
	return p, true
}

// Start begins a trial for a brand-new signup: a trialing subscription of the
// entry plan plus the single unified trial credit. cardPresent selects the
// window (7 days without a card, 30 with one). isTest routes the credit to the
// test ledger (org.TestMode()).
//
// New-signup only: if the subject already has a subscription OR any prior
// balance deposit (an existing/ comped user, or a repeat call), Start is a
// no-op (Started=false, Reason="not_new"). This is what leaves existing users —
// and Dave's real $10k ledger — untouched, and makes Start idempotent.
func Start(db *datastore.Datastore, subject string, cardPresent bool, isTest bool) (Result, error) {
	subject = normalizeSubject(subject)
	if subject == "" {
		return Result{}, ErrInvalidSubject
	}

	p, ok := resolveEntryPlan()
	if !ok {
		return Result{Plan: PlanSlug, Reason: "trial_not_configured"}, nil
	}

	days := NoCardTrialDays
	if cardPresent {
		days = CardTrialDays
	}
	res := Result{Plan: p.Slug, TrialDays: days, CreditCents: p.CreditCents}

	has, err := subjectHasBillingHistory(db, subject)
	if err != nil {
		return res, err
	}
	if has {
		res.Reason = "not_new"
		return res, nil
	}

	sub, err := createTrialSubscription(db, subject, p, days)
	if err != nil {
		return res, err
	}
	res.SubscriptionID = sub.Id()
	res.TrialEnd = sub.TrialEnd

	dep, err := grantTrialCredit(db, subject, p, sub.TrialEnd, isTest)
	if err != nil {
		return res, err
	}
	res.TransactionID = dep.Id()
	res.Started = true
	return res, nil
}

// ExtendForCard extends a subject's trial to 30 days when a card is added.
//
//   - Trialing entry-plan subscription found and shorter than 30 days: extend
//     the trial window (from its original start) and stretch the trial credit's
//     expiry to match. (Extended=true)
//   - No trial yet: start a fresh 30-day trial (the "signup WITH a card" path),
//     which is a no-op for existing users via Start's new-signup gate.
//   - Trial already >= 30 days, or subject has no eligible trial: no-op.
func ExtendForCard(db *datastore.Datastore, subject string, isTest bool) (Result, error) {
	subject = normalizeSubject(subject)
	if subject == "" {
		return Result{}, ErrInvalidSubject
	}

	p, ok := resolveEntryPlan()
	if !ok {
		return Result{Plan: PlanSlug, Reason: "trial_not_configured"}, nil
	}

	sub, found, err := findTrialSubscription(db, subject, p.Slug)
	if err != nil {
		return Result{Plan: p.Slug}, err
	}
	if !found {
		// No trial to extend — a card added up front means "signup WITH a
		// card": start a 30-day trial (no-op for existing users).
		return Start(db, subject, true, isTest)
	}

	res := Result{Plan: p.Slug, SubscriptionID: sub.Id(), TrialDays: CardTrialDays, CreditCents: p.CreditCents}

	base := sub.TrialStart
	if base.IsZero() {
		base = sub.Start
	}
	newEnd := base.AddDate(0, 0, CardTrialDays)

	if !sub.TrialEnd.Before(newEnd) {
		res.TrialEnd = sub.TrialEnd
		res.Reason = "already_extended"
		return res, nil
	}

	sub.TrialEnd = newEnd
	sub.PeriodStart = newEnd
	sub.PeriodEnd = newEnd.AddDate(0, 1, 0) // monthly cadence
	if err := sub.Update(); err != nil {
		return res, fmt.Errorf("trial: extend subscription: %w", err)
	}
	res.TrialEnd = newEnd

	dep, ok, err := findTrialCredit(db, subject)
	if err != nil {
		return res, err
	}
	if ok && dep.ExpiresAt.Before(newEnd) {
		dep.ExpiresAt = newEnd
		if err := dep.Update(); err != nil {
			return res, fmt.Errorf("trial: extend trial credit: %w", err)
		}
		res.TransactionID = dep.Id()
	}

	res.Extended = true
	return res, nil
}

// subjectHasBillingHistory reports whether the subject already has a
// subscription or any deposit — i.e. is not a brand-new signup.
func subjectHasBillingHistory(db *datastore.Datastore, subject string) (bool, error) {
	// Subscriptions are keyed by UserId without the synckey ancestor (the
	// canonical create path, billing/grant.Grant, queries them ancestor-less);
	// transactions live under the synckey ancestor. Query each accordingly.
	subs := make([]*subscription.Subscription, 0, 1)
	if _, err := subscription.Query(db).
		Filter("UserId=", subject).Limit(1).GetAll(&subs); err != nil {
		return false, err
	}
	if len(subs) > 0 {
		return true, nil
	}

	// Any deposit credited to the subject — starter credit, prior trial, a
	// manual comp (Dave's $10k), a top-up, an allotment … all count as "already
	// funded", so Start leaves them untouched.
	rootKey := db.NewKey("synckey", "", 1, nil)
	deps := make([]*transaction.Transaction, 0, 1)
	if _, err := transaction.Query(db).Ancestor(rootKey).
		Filter("DestinationId=", subject).Limit(1).GetAll(&deps); err != nil {
		return false, err
	}
	return len(deps) > 0, nil
}

func createTrialSubscription(db *datastore.Datastore, subject string, p Plan, days int) (*subscription.Subscription, error) {
	planRec, err := seedPlan(db, p)
	if err != nil {
		return nil, err
	}

	// The 7-vs-30 trial length varies per subject, so ride it on a copy — never
	// mutate the shared plan record.
	pc := *planRec
	pc.TrialPeriodDays = days

	sub := subscription.New(db)
	sub.UserId = subject
	sub.ProviderType = ProviderType
	sub.Quantity = 1
	sub.Metadata = types.Map{
		"trial":          true,
		"trial_days":     days,
		"billing_reason": "trial",
	}

	// StartSubscription sets Status=Trialing, TrialStart/TrialEnd, and the first
	// post-trial period from the plan's cadence + TrialPeriodDays.
	engine.StartSubscription(sub, &pc)

	if err := sub.Create(); err != nil {
		return nil, fmt.Errorf("trial: create subscription: %w", err)
	}
	return sub, nil
}

// seedPlan returns the entry plan's DB record, creating it on first use so the
// subscription references a stable Id (mirrors billing/grant.Grant).
func seedPlan(db *datastore.Datastore, p Plan) (*plan.Plan, error) {
	pl := plan.New(db)
	found, err := pl.Query().Filter("Slug=", p.Slug).Get()
	if err != nil {
		return nil, fmt.Errorf("trial: lookup plan %s: %w", p.Slug, err)
	}
	if found {
		return pl, nil
	}

	pl.Slug = p.Slug
	pl.Name = p.Name
	pl.Description = p.Description
	pl.Price = currency.Cents(p.PriceCents)
	pl.Currency = currency.Type(planCurrency(p))
	pl.Interval = types.Monthly
	pl.IntervalCount = 1
	if err := pl.Create(); err != nil {
		return nil, fmt.Errorf("trial: seed plan %s: %w", p.Slug, err)
	}
	return pl, nil
}

// grantTrialCredit funds the single unified trial credit as a tag-deduped,
// expiring Deposit. One per subject: a second call returns the existing one.
func grantTrialCredit(db *datastore.Datastore, subject string, p Plan, expiresAt time.Time, isTest bool) (*transaction.Transaction, error) {
	if dep, ok, err := findTrialCredit(db, subject); err != nil {
		return nil, err
	} else if ok {
		return dep, nil
	}

	trans := transaction.New(db)
	trans.Type = transaction.Deposit
	trans.DestinationId = subject
	trans.DestinationKind = Kind
	trans.Currency = currency.Type(planCurrency(p))
	trans.Amount = currency.Cents(p.CreditCents)
	trans.Notes = fmt.Sprintf(
		"Trial credit: %s plan ($%.2f, one balance across compute & AI, expires %s)",
		p.Slug, float64(p.CreditCents)/100, expiresAt.Format("2006-01-02"),
	)
	trans.Tags = CreditTag
	trans.ExpiresAt = expiresAt
	trans.Metadata = types.Map{
		"creditType": "trial",
		"plan":       p.Slug,
	}
	trans.Test = isTest

	if err := trans.Create(); err != nil {
		return nil, fmt.Errorf("trial: fund trial credit: %w", err)
	}
	return trans, nil
}

// findTrialSubscription returns the newest trialing subscription of the given
// plan for the subject.
func findTrialSubscription(db *datastore.Datastore, subject, slug string) (*subscription.Subscription, bool, error) {
	// Subscriptions are keyed ancestor-less by UserId (see subjectHasBillingHistory).
	subs := make([]*subscription.Subscription, 0)
	if _, err := subscription.Query(db).
		Filter("UserId=", subject).GetAll(&subs); err != nil {
		return nil, false, err
	}

	var best *subscription.Subscription
	for _, s := range subs {
		if s.Status != subscription.Trialing || s.Plan.Slug != slug {
			continue
		}
		if best == nil || s.TrialStart.After(best.TrialStart) {
			best = s
		}
	}
	if best == nil {
		return nil, false, nil
	}
	// Reload a New(db)-bound instance: models from a package-level GetAll carry
	// no datastore key/handle, so mutating+Update on them is a no-op. GetById
	// returns a fully-wired model (matches billing/grant's create/mutate path).
	bound := subscription.New(db)
	if err := bound.GetById(best.Id()); err != nil {
		return nil, false, err
	}
	return bound, true, nil
}

func findTrialCredit(db *datastore.Datastore, subject string) (*transaction.Transaction, bool, error) {
	// Load straight into a New(db)-bound model via .Get() (grant.Grant's proven
	// mutate pattern): the returned model carries both the datastore handle AND
	// the full synckey-ancestor key, so a later Update() re-saves in place.
	// (GetAll yields db-less copies; GetById drops the ancestor — both break the
	// subsequent Update for ancestor-scoped ledger rows.)
	// Single-filter query then match the tag in-memory: two-equality-filter
	// queries (DestinationId + Tags) need a composite index not guaranteed across
	// datastore backends. Then reload the hit by its full key (which retains the
	// synckey ancestor) into a New(db)-bound model, so Update() persists in place.
	rootKey := db.NewKey("synckey", "", 1, nil)
	ts := make([]*transaction.Transaction, 0, 4)
	keys, err := transaction.Query(db).Ancestor(rootKey).
		Filter("DestinationId=", subject).GetAll(&ts)
	if err != nil {
		return nil, false, err
	}
	for i, t := range ts {
		if t.Tags != CreditTag {
			continue
		}
		// Reload the row into a fresh New(db)-bound model under its FULL
		// synckey-parented key. A raw GetAll element has no datastore handle and
		// (crucially for ancestor-scoped ledger rows) no reload baseline, so a
		// later Update silently no-ops the field change. Reconstructing the
		// parented key + Get() gives a properly-loaded model whose Update()
		// persists in place under synckey. keys[i] pairs with ts[i].
		if i >= len(keys) {
			break
		}
		fullKey := db.NewKey("transaction", keys[i].StringID(), keys[i].IntID(), rootKey)
		bound := transaction.New(db)
		if err := bound.Get(fullKey); err != nil {
			return nil, false, err
		}
		return bound, true, nil
	}
	return nil, false, nil
}

func planCurrency(p Plan) string {
	if p.Currency == "" {
		return "usd"
	}
	return p.Currency
}

func normalizeSubject(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
