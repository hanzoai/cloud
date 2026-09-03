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

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// mountPosture registers the posture reads and the money-mode write. Called from
// routes.
func mountPosture(app cloud.Router, o ops) {
	zapp := cloud.ZipApp(app)
	zip.Get(zapp, "/v1/billing/settings", o.settings)
	zip.Get(zapp, "/v1/billing/tier", o.tier)
	zip.Get(zapp, "/v1/billing/usage/rollup", o.rollup)
	zip.Post(zapp, "/v1/billing/mode", o.mode)
	// The catalog is a rendered document, so it is raw for the reason the saved
	// cards are: a plan carries its limits and its licensing, all of them the
	// store's own models.
	app.Get("/v1/billing/plans", cloud.Handle(o.s, listPlans))
}

// The catalog is the one read here the wire keeps untyped, so its prose is
// declared beside the route.
func init() {
	openapi.Describe("/v1/billing/plans", http.MethodGet,
		"The plan catalog, priced with whatever offer is in force",
		"Answers every plan on sale — its price, what it includes, and the limits it "+
			"carries — optionally narrowed to one `?category=`.\n\n"+
			"The prices are what the CHECKOUT will charge: any active promotion is "+
			"applied before they leave the store, so a reader never applies a discount a "+
			"second time and a quote can never disagree with the sale.\n\n"+
			"It is the public catalog and needs no tenant: this is what anyone may buy.")
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
func (o ops) settings(ctx context.Context, _ *cloud.Unit) (*plane.PaymentConfig, error) {
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "settings", func(ctx context.Context) (*plane.PaymentConfig, error) {
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
func (o ops) tier(ctx context.Context, _ *cloud.Unit) (*plane.Tier, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	in := plane.TierIn{Subject: subject, Tier: mintedTier(ctx)}
	return ask(ctx, org, "tier", func(ctx context.Context) (*plane.Tier, error) {
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
func (o ops) rollup(ctx context.Context, _ *cloud.Unit) (*plane.Rollup, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	plan := ""
	if c, ok := cloud.Request(ctx); ok {
		plan = strings.TrimSpace(c.Query("plan"))
	}
	return ask(ctx, org, "usage rollup", func(ctx context.Context) (*plane.Rollup, error) {
		return commercepeer.BillingRollup(ctx, &plane.RollupIn{Subject: subject, Plan: plan})
	})
}

// Moves this org between sandbox money and real money.
//
// It decides whether a charge hits a real card, so it is the one posture change
// that is not self-service: the platform bar, never an org owner, because an org
// that could put itself in test mode could take priced work for free.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) mode(ctx context.Context, in *plane.ModeIn) (*plane.Mode, error) {
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
	return ask(ctx, org, "mode", func(ctx context.Context) (*plane.Mode, error) {
		return commercepeer.BillingMode(ctx, in)
	})
}

// listPlans answers the public plan catalog.
//
// Raw rather than typed, for the reason the saved cards are: the body is the
// store's rendered document and is forwarded whole.
func listPlans(s *cloud.Service[state], c *zip.Ctx) error {
	// The catalog is public, so this read states no tenant. cloud.For with an
	// empty org is what an unauthenticated caller has: the op takes no org and
	// scopes nothing.
	out, err := ask(c.Context(), "", "plans", func(ctx context.Context) (*plane.Rendered, error) {
		return commercepeer.BillingPlans(ctx, &plane.PlansIn{Category: c.Query("category")})
	})
	if err != nil {
		return err
	}
	return document(c, http.StatusOK, out.Body)
}

// mintedTier reads a tier the caller NAMED, and answers empty for anyone who may
// not name one.
//
// Both carriers are client input — the gateway neither mints X-Tier nor strips
// it — so honouring either unconditionally is how `X-Tier: enterprise` on a free
// subject once returned enterprise. An unprivileged override is ignored rather
// than refused: refusing would break readers that pass a hint they are not
// entitled to, for no gain, since the answer they get is the true one.
func mintedTier(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok || !mayMint(ctx) {
		return ""
	}
	if h := strings.TrimSpace(c.Header("X-Tier")); h != "" {
		return h
	}
	return strings.TrimSpace(c.Query("tier"))
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
	return principal.IsSuperAdmin(c)
}
