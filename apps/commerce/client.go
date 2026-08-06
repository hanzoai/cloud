// Copyright © 2026 Hanzo AI. MIT License.

// client.go is the in-process inter-subsystem commerce client — the REAL
// implementation of cloud's types.CommerceClient, absorbed here from the retired
// in-process stub that used to fail closed on entitlement. It answers cloud's licensing/entitlements tier
// with DIRECT Go calls into the embedded commerce datastore — the org's
// subscriptions, and the plan authority row each one names — no HTTP hop, no network.
//
// MONEY-SAFETY. CheckEntitlement NEVER fabricates a grant. It returns Active:true
// ONLY when a real active, unexpired subscription in the org's own datastore
// namespace holds a plan tier whose OWN authority row licenses the product. Any
// machinery it cannot resolve (commerce not co-resident, org not
// resolvable, subscription query error, plan authority unreadable) returns an
// ERROR — the entitlements gate treats an erroring client as "cannot verify ⇒ 503",
// the specified secure default, so an unverifiable product is never enabled. A
// clean "resolved, but no plan licenses this product" is a real Active:false answer
// (the gate turns it into a 402 upgrade prompt), never an error and never a grant.

package commerce

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/apps/plan"
	"github.com/hanzoai/cloud/types"
	commercemod "github.com/hanzoai/commerce"
	"github.com/hanzoai/commerce/datastore"
	commerceplan "github.com/hanzoai/commerce/models/plan"
	"github.com/hanzoai/commerce/models/subscription"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
)

// Client is the in-process inter-subsystem seam cloud's licensing/entitlements tier
// calls. It IS cloud's types.CommerceClient — one narrow interface (GetOrgConfig
// + the real CheckEntitlement), not a second copy — kept as an alias so a value
// satisfies both names with no adapter. Add methods here only when a consumer needs
// them; keep it narrow.
type Client = types.CommerceClient

// inProcessClient answers the seam with direct Go calls. brand is surfaced in
// OrgConfig (known without the store); resolve returns the live *commercemod.Embedded — nil
// when commerce is not co-resident, which makes CheckEntitlement fail closed.
type inProcessClient struct {
	brand   string
	resolve func() *commercemod.Embedded
}

// published is the process-global Embedded the lazy InProcessClient resolves. Mount
// sets it once (mirrors transport.SetHandler for the http seam) so the client
// cloud builds in BuildDeps — BEFORE MountAll — still routes to the live datastore.
var published atomic.Pointer[commercemod.Embedded]

// PublishEmbedded records the mounted Embedded as the in-process entitlement source.
// Mount calls it once; nil un-publishes (tests).
func PublishEmbedded(e *commercemod.Embedded) { published.Store(e) }

func currentEmbedded() *commercemod.Embedded { return published.Load() }

// InProcessClient returns the process-wide, lazily-resolved client cloud's
// pickCommerceClient wires as deps.Commerce. BuildDeps runs before MountAll, so it
// resolves the published Embedded per call rather than capturing one; brand answers
// OrgConfig even before Mount publishes.
func InProcessClient(brand string) Client {
	return &inProcessClient{brand: brand, resolve: currentEmbedded}
}

// GetOrgConfig answers the org's config directly — at this inter-subsystem
// layer an org's config IS its org id + the deployment brand (the two fields
// types.OrgConfig carries), both known without touching commerce state.
func (c *inProcessClient) GetOrgConfig(_ context.Context, orgID string) (*types.OrgConfig, error) {
	return &types.OrgConfig{OrgID: strings.TrimSpace(orgID), Brand: c.brand}, nil
}

// CheckEntitlement resolves "does org `orgID` hold an active entitlement for product
// `productID`, and what does its plan grant" from commerce's OWN subscription models
// + the @hanzo/plans vocabulary. See the file header for the money-safety contract.
func (c *inProcessClient) CheckEntitlement(ctx context.Context, orgID, productID string) (*types.LicenseEntitlement, error) {
	orgID = strings.TrimSpace(orgID)
	productID = strings.TrimSpace(productID)
	if orgID == "" || productID == "" {
		return nil, fmt.Errorf("commerce.CheckEntitlement: empty orgID or productID")
	}

	e := c.resolve()
	if e == nil || e.App() == nil {
		// Commerce is not co-resident / not booted — the ledger is unreadable, so the
		// plan cannot be verified. Fail closed; never fabricate a grant or a deny.
		return nil, fmt.Errorf("commerce.CheckEntitlement: commerce not co-resident; cannot verify entitlement (org=%s product=%s)", orgID, productID)
	}

	// 1. Resolve the org → its datastore namespace via commerce's OWN canonical,
	//    KV-cached, secret-name-guarded resolver — the SAME binding the money path
	//    (iammiddleware) uses to map a gateway X-Org-Id to an Organization. The
	//    namespace is therefore derived from a real commerce Organization record,
	//    never blindly from the caller's string, so a read can never cross orgs.
	o, err := commerceorg.Resolve(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("commerce.CheckEntitlement: resolve org %q: %w", orgID, err)
	}

	// 2. Load every ACTIVE subscription in that org's namespace. The namespace IS the
	//    org boundary, so any active subscription here belongs to this org
	//    (org-pooled or personal) — we scan them all rather than assume a buyer subject.
	//    A plain (non-ancestor) Status filter is deliberate: it matches commerce's own
	//    subscription-read idiom (billing/grant/grant.go's idempotency lookup) so a sub
	//    is found regardless of how it was created (payment flow or manual grant), never
	//    missed by an ancestor-key mismatch.
	ds := datastore.New(o.Namespaced(ctx))
	var subs []*subscription.Subscription
	if _, err := subscription.Query(ds).
		Filter("Status=", string(subscription.Active)).
		GetAll(&subs); err != nil {
		return nil, fmt.Errorf("commerce.CheckEntitlement: query subscriptions for org %q: %w", orgID, err)
	}

	// 3. For each active, unexpired subscription resolve what its plan tier LICENSES
	//    from the tier's OWN authority row, and check whether that names productID.
	//    Product scoping is the presence of the "licensing.product:<id>" token, so
	//    only a plan that actually names the product grants it.
	//
	//    The row, not the catalog. The catalog lists what is ON SALE TODAY: ask it
	//    about a tier that has since been retired and it answers "licenses nothing",
	//    definitively, so the answer reads as a refusal rather than as the missing
	//    record it is — and the subscriber, who is still being charged, loses every
	//    product they bought. Commerce keeps the row resolvable precisely so that
	//    cannot happen ("retiring a tier stops new sales; it never strands a
	//    subscriber"), and the row now carries its licensing block for the same
	//    reason it carries Category and Price. This is the licensing half of the cut
	//    Paid already makes for the paywall: classify on what you bought, not on
	//    what is for sale.
	now := time.Now()
	want := "licensing.product:" + productID
	var bestPlan string
	for _, s := range subs {
		if s == nil {
			continue
		}
		if !s.PeriodEnd.IsZero() && !s.PeriodEnd.After(now) {
			continue // expired despite an active status — never grant on it
		}
		slug := strings.TrimSpace(s.Plan.Slug)
		if slug == "" {
			continue // no resolvable plan tier on this sub — cannot grant from it
		}
		row, found, rerr := tier(ctx, slug)
		if rerr != nil {
			// MACHINERY failure (the plan authority is unreadable): cannot resolve the
			// tier ⇒ cannot verify ⇒ fail closed. Never deny-by-guess on an outage.
			return nil, fmt.Errorf("commerce.CheckEntitlement: resolve plan %q: %w", slug, rerr)
		}
		if !found {
			// No authority row for this tier at all — nothing to read a licence from.
			// Skip (conservative: never grants) and keep scanning the org's other subs.
			continue
		}
		if bestPlan == "" {
			bestPlan = slug
		}
		if row.Licensing == nil {
			continue // a tier that licenses nothing licenses nothing.
		}
		features := plan.Tokens(plan.Licence{
			Products: row.Licensing.Products,
			Apps:     row.Licensing.Apps,
			Features: row.Licensing.Features,
		})
		if containsFeature(features, want) {
			return &types.LicenseEntitlement{
				ProductID:   productID,
				Active:      true,
				Plan:        slug,
				Features:    features,
				ExpiresUnix: unixOrZero(s.PeriodEnd),
			}, nil
		}
	}

	// Resolution SUCCEEDED, but no active subscription's plan licenses productID — a
	// real "not entitled" answer (Active:false), NOT a machinery failure. The
	// entitlements gate turns it into a 402 upgrade prompt, never a grant.
	return &types.LicenseEntitlement{ProductID: productID, Active: false, Plan: bestPlan}, nil
}

// tier resolves the plan authority row for slug. The authority is platform-global
// (commerce models/plan, the "system" namespace) — the SAME rows the boot seed
// reconciles, admin.hanzo.ai edits and the charge path prices from — so there is one
// answer to "what is this tier", and it keeps answering after the tier is retired.
func tier(ctx context.Context, slug string) (*commerceplan.Plan, bool, error) {
	p := commerceplan.New(commerceplan.AuthorityDB(ctx))
	ok, err := p.Query().Filter("Slug=", slug).Get()
	if err != nil {
		return nil, false, err
	}
	return p, ok, nil
}

func containsFeature(features []string, want string) bool {
	for _, f := range features {
		if f == want {
			return true
		}
	}
	return false
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
