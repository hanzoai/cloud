package billing

// posture.go serves the four reads that say what a customer may do and what it
// costs: the processor a browser tokenizes against, the money the org is
// transacting in, the catalog it buys from, and the tier its subscriptions
// confer — with the month measured against that tier beside it.
//
// THE TIER OVERRIDE IS DECIDED HERE, and nowhere else. A caller can NAME a tier
// rather than earn one, through an X-Tier header or an explicit ?tier=. Both are
// client input — the gateway neither mints nor strips X-Tier — and honouring
// either unconditionally lets any caller name its own entitlement, which is a
// MINT: a tier decides which models may be invoked and how many agents may run.
// So an override is read only for a caller that may mint, and an unprivileged
// one is IGNORED rather than refused — the answer it gets is simply the true
// one, which is what its unprivileged sibling on the grant path already does.

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// mountPosture registers the posture reads and the money-mode write. Called from
// routes.
func mountPosture(app cloud.Router, o ops) {
	g := app.Group("/v1/billing")
	g.Get("/settings", o.settings)
	g.Get("/tier", o.tier)
	g.Get("/usage/rollup", o.rollup)
	g.Post("/mode", o.mode)
	// The catalog is a rendered document, so it is raw for the reason the saved
	// cards are: a plan carries its limits and its licensing, all of them the
	// store's own models.
	g.Raw(http.MethodGet, "/plans", cloud.Handle(o.s, listPlans))
}

// The catalog is the one read here the wire keeps untyped, so its prose is
// declared beside the route.
func init() {
	spec.Describe("/v1/billing/plans", http.MethodGet,
		"The plan catalog, priced with whatever offer is in force",
		"Answers every plan on sale — its price, what it includes, and the limits it "+
			"carries — optionally narrowed to one `?category=`.\n\n"+
			"The prices are what the CHECKOUT will charge: any active promotion is "+
			"applied before they leave the store, so a reader never applies a discount a "+
			"second time and a quote can never disagree with the sale.\n\n"+
			"`?coupon=` quotes the catalog under a plan coupon code: each paid plan it "+
			"covers carries `couponCode`, `couponPercent`, `couponMonths` (how many monthly "+
			"periods it discounts, from the first paid one; 1 for a first-month coupon), "+
			"`couponFirstCents` (what each of those months charges under it) and "+
			"`couponEnds`; every other row carries "+
			"`couponError` — `unknown`, `expired`, `exhausted` or `not_applicable`. "+
			"Nothing is reserved by a quote. Without the parameter the catalog is "+
			"unchanged.\n\n"+
			"It is public and needs no tenant, and it is the catalog of the brand the "+
			"request's host resolves to — the same brand `/v1/commerce/org` answers there. "+
			"A brand that publishes no plans answers an empty list.")
}

// Answers the PUBLIC half of this org's processor configuration — the ids a
// browser needs to tokenize a card, and the environment it must tokenize
// against.
//
// It carries no secret: an application id is published to every checkout page by
// design. What matters is that it names the SAME processor account the charge
// will be made on, because a card vaulted against one account and charged
// against another is a card that saves and then cannot be used.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) settings(ctx context.Context, _ *cloud.Unit) (*client.PaymentConfig, error) {
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "settings", func(ctx context.Context) (*client.PaymentConfig, error) {
		return commercepeer.BillingSettings(ctx)
	})
}

// Answers which tier the caller is on, what it allows, and what is left to spend.
//
// `effectiveAvailable` is the ONLY figure to compare against zero. The others are
// its parts — prepaid money, granted credits and the daily term are three sources
// of one spend, not three balances to add up a second time.
//
// A tier that cannot be READ is an error, never Free. The router in front of the
// models maps any non-2xx to Free, so answering Free from a question nobody could
// answer would pin every paying customer to the most restrictive row with nothing
// anywhere to find.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) tier(ctx context.Context, _ *cloud.Unit) (*client.Tier, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	in := client.TierIn{Subject: subject}
	return ask(ctx, org, "tier", func(ctx context.Context) (*client.Tier, error) {
		return commercepeer.BillingTier(ctx, &in)
	})
}

// Answers the caller's month: what their plan includes, what has been consumed
// against it, and the wallet beside it.
//
// The two blocks are SEPARATE monies and are never added. One is usage a plan
// granted; the other is prepaid credit bought with a card. Their sum is not a
// number anyone holds, and a reader that formed it would be inventing a balance.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) rollup(ctx context.Context, _ *cloud.Unit) (*client.Rollup, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	plan := ""
	if c, ok := cloud.Request(ctx); ok {
		plan = strings.TrimSpace(c.Query("plan"))
	}
	return ask(ctx, org, "usage rollup", func(ctx context.Context) (*client.Rollup, error) {
		return commercepeer.BillingRollup(ctx, &client.RollupIn{Subject: subject, Plan: plan})
	})
}

// Moves this org between sandbox money and real money.
//
// It decides whether a charge hits a real card, so it is the one posture change
// that is not self-service: the platform bar, never an org owner, because an org
// that could put itself in test mode could take priced work for free.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) mode(ctx context.Context, in *client.ModeIn) (*client.Mode, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	if !mayMint(ctx) {
		return nil, zip.ErrForbidden("platform authority required to change money mode")
	}
	return ask(ctx, org, "mode", func(ctx context.Context) (*client.Mode, error) {
		return commercepeer.BillingMode(ctx, in)
	})
}

// listPlans answers the public plan catalog of the brand the host resolves to.
//
// Raw rather than typed, for the reason the saved cards are: the body is the
// store's rendered document and is forwarded whole.
func listPlans(s *cloud.Service[state], c *zip.Ctx) error {
	// The catalog is public, so this read states no tenant. cloud.For with an
	// empty org is what an unauthenticated caller has: the op takes no org and
	// scopes nothing. It names the HOST, because which catalog is on sale is the
	// serving brand's, and the store holds the table that says whose a host is.
	//
	// c.Host() is the one brand-by-host accessor, and the one the org endpoint
	// resolves: a client's X-Forwarded-Host is believed only from a trusted hop,
	// so the brand a page wears and the plans it lists cannot disagree.
	in := client.PlansIn{Host: c.Host(), Category: c.Query("category"), Coupon: c.Query("coupon")}
	out, err := ask(c.Forward(), "", "plans", func(ctx context.Context) (*client.Rendered, error) {
		return commercepeer.BillingPlans(ctx, &in)
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.Coupon) != "" {
		// A coupon quote is the code's standing NOW — its uses left, its window — so
		// no cache may keep it: a cached quote advertises a coupon already used up.
		c.SetHeader("Cache-Control", "no-store")
		c.SetHeader("CDN-Cache-Control", "no-store")
	}
	return document(c, http.StatusOK, out.Body)
}

// mayMint reports whether this caller may name an entitlement rather than earn
// one: platform authority. It is the
// same bar the store applies to the same class of client string, asked once
// here so the two halves cannot drift.
func mayMint(ctx context.Context) bool {
	c, ok := cloud.Request(ctx)
	if !ok {
		return false
	}
	return principal.Super(c)
}
