package billing

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

//go:embed plans/subscription.json
var subscriptionJSON embed.FS

//go:embed plans/dns.json
var dnsJSON embed.FS

// canonicalPlan is the JSON shape from @hanzo/plans/*.json.
//
// Bundles: a plan can grant entitlement to other plans without
// charging separately for them. Example: subscribing to "pro"
// auto-grants "world-pro" because `"bundles": ["world-pro"]` is set on
// the pro tier. The reverse view `includedIn` lets product surfaces
// (world.hanzo.ai, chat, etc.) tell users "this plan is included in
// these higher tiers" so the upsell story is symmetric.
type canonicalPlan struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	PriceMonthly *float64 `json:"priceMonthly"` // dollars per month (null for custom)
	PriceAnnual  *float64 `json:"priceAnnual"`  // dollars per month billed annually (null for custom)
	Category     string   `json:"category"`
	Popular      bool     `json:"popular,omitempty"`
	ContactSales bool     `json:"contactSales,omitempty"`
	// TrialPeriodDays is the base (no-card) free-trial length advertised for the
	// plan. The actual on-ramp length is decided at signup by billing/trial
	// (7 days without a card, 30 with one) — this only surfaces the base offer
	// on GET /v1/billing/plans.
	TrialPeriodDays *int     `json:"trialPeriodDays,omitempty"`
	Features        []string `json:"features"`
	Bundles      []string `json:"bundles,omitempty"`    // slugs of plans whose entitlement this plan also grants
	IncludedIn   []string `json:"includedIn,omitempty"` // slugs of plans that include this plan as a bundle
	Limits       *struct {
		// Subscription (API) limits
		RequestsPerMinute *int `json:"requestsPerMinute,omitempty"`
		TokensPerMinute   *int `json:"tokensPerMinute,omitempty"`
		FreeCredit        *int `json:"freeCredit,omitempty"`
		MaxMembers        *int `json:"maxMembers,omitempty"`

		// IncludedCreditUsd is a legacy alias (entitlements["commerce.included_credit_usd"]);
		// no published @hanzo/plans plan sets it. The credit plans ACTUALLY declare is
		// the cloud allowance below.
		IncludedCreditUsd *int `json:"includedCreditUsd,omitempty"`

		// IncludedCloudCredits is the recurring monthly cloud-credit allowance in
		// whole USD — the canonical entitlement cloud.included_credits_usd, published
		// by @hanzo/plans as limits.includedCloudCredits (flat) or
		// includedCloudCreditsPerUser (per seat). This is what the monthly allotment
		// grants (e.g. the $200 tier's sold $100/mo).
		IncludedCloudCredits        *int `json:"includedCloudCredits,omitempty"`
		IncludedCloudCreditsPerUser *int `json:"includedCloudCreditsPerUser,omitempty"`

		// DNS limits
		Zones          *int `json:"zones,omitempty"`
		RecordsPerZone *int `json:"recordsPerZone,omitempty"`
		QueriesPerDay  *int `json:"queriesPerDay,omitempty"`
	} `json:"limits,omitempty"`
	Payouts *struct {
		IdleResalePercent int    `json:"idleResalePercent"`
		Description       string `json:"description"`
	} `json:"payouts,omitempty"`
}

// staticPlan is the wire type returned by GET /billing/plans.
// Fields match the Plan type in the billing frontend's commerce-client.ts.
type staticPlan struct {
	Slug            string   `json:"slug"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Category        string   `json:"category"`
	Price           int64    `json:"price"`         // monthly price in cents (0 = free)
	PriceAnnual     int64    `json:"priceAnnual"`   // annual price in cents per month
	Currency        string   `json:"currency"`
	Interval        string   `json:"interval"`
	IntervalCount   int      `json:"intervalCount"`
	TrialPeriodDays int      `json:"trialPeriodDays"`
	ContactSales    bool     `json:"contactSales,omitempty"`
	Popular         bool     `json:"popular,omitempty"`
	Features        []string `json:"features,omitempty"`
	Bundles         []string `json:"bundles,omitempty"`    // see canonicalPlan.Bundles
	IncludedIn      []string `json:"includedIn,omitempty"` // see canonicalPlan.IncludedIn
	Limits          *struct {
		// Subscription (API) limits
		RequestsPerMinute *int `json:"requestsPerMinute,omitempty"`
		TokensPerMinute   *int `json:"tokensPerMinute,omitempty"`
		FreeCredit        *int `json:"freeCredit,omitempty"`
		MaxMembers        *int `json:"maxMembers,omitempty"`

		// IncludedCreditUsd: legacy alias; IncludedCloudCredits(+PerUser) is what
		// @hanzo/plans publishes and the monthly allotment grants.
		IncludedCreditUsd           *int `json:"includedCreditUsd,omitempty"`
		IncludedCloudCredits        *int `json:"includedCloudCredits,omitempty"`
		IncludedCloudCreditsPerUser *int `json:"includedCloudCreditsPerUser,omitempty"`

		// DNS limits
		Zones          *int `json:"zones,omitempty"`
		RecordsPerZone *int `json:"recordsPerZone,omitempty"`
		QueriesPerDay  *int `json:"queriesPerDay,omitempty"`
	} `json:"limits,omitempty"`
}

// hanzoPlans contains all plans loaded at init from embedded JSON files.
// Subscription plans have category "personal", "team", or "enterprise".
// DNS plans have category "dns".
var hanzoPlans []staticPlan

// dnsPlans is a filtered view containing only DNS plans for the /dns/plans endpoint.
var dnsPlans []staticPlan

func init() {
	hanzoPlans = loadPlansFromEmbed(subscriptionJSON, "plans/subscription.json")

	dns := loadPlansFromEmbed(dnsJSON, "plans/dns.json")
	dnsPlans = dns
	hanzoPlans = append(hanzoPlans, dns...)
}

// loadPlansFromEmbed reads an embedded JSON file and converts canonical plans
// to the staticPlan wire format. Panics on failure because plan data is required
// for the service to operate.
func loadPlansFromEmbed(fs embed.FS, path string) []staticPlan {
	data, err := fs.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("billing: failed to read embedded %s: %v", path, err))
	}

	var canonical []canonicalPlan
	if err := json.Unmarshal(data, &canonical); err != nil {
		panic(fmt.Sprintf("billing: failed to parse %s: %v", path, err))
	}

	plans := make([]staticPlan, len(canonical))
	for i, cp := range canonical {
		sp := staticPlan{
			Slug:          cp.ID,
			Name:          cp.Name,
			Description:   cp.Description,
			Category:      cp.Category,
			Currency:      "usd",
			Interval:      "monthly",
			IntervalCount: 1,
			ContactSales:  cp.ContactSales,
			Popular:       cp.Popular,
			Features:      cp.Features,
			Bundles:       cp.Bundles,
			IncludedIn:    cp.IncludedIn,
		}

		if cp.PriceMonthly != nil {
			sp.Price = int64(math.Round(*cp.PriceMonthly * 100))
		}
		if cp.PriceAnnual != nil {
			sp.PriceAnnual = int64(math.Round(*cp.PriceAnnual * 100))
		}
		if cp.TrialPeriodDays != nil {
			sp.TrialPeriodDays = *cp.TrialPeriodDays
		}

		if cp.Limits != nil {
			sp.Limits = &struct {
				RequestsPerMinute           *int `json:"requestsPerMinute,omitempty"`
				TokensPerMinute             *int `json:"tokensPerMinute,omitempty"`
				FreeCredit                  *int `json:"freeCredit,omitempty"`
				MaxMembers                  *int `json:"maxMembers,omitempty"`
				IncludedCreditUsd           *int `json:"includedCreditUsd,omitempty"`
				IncludedCloudCredits        *int `json:"includedCloudCredits,omitempty"`
				IncludedCloudCreditsPerUser *int `json:"includedCloudCreditsPerUser,omitempty"`
				Zones                       *int `json:"zones,omitempty"`
				RecordsPerZone              *int `json:"recordsPerZone,omitempty"`
				QueriesPerDay               *int `json:"queriesPerDay,omitempty"`
			}{
				RequestsPerMinute:           cp.Limits.RequestsPerMinute,
				TokensPerMinute:             cp.Limits.TokensPerMinute,
				FreeCredit:                  cp.Limits.FreeCredit,
				MaxMembers:                  cp.Limits.MaxMembers,
				IncludedCreditUsd:           cp.Limits.IncludedCreditUsd,
				IncludedCloudCredits:        cp.Limits.IncludedCloudCredits,
				IncludedCloudCreditsPerUser: cp.Limits.IncludedCloudCreditsPerUser,
				Zones:                       cp.Limits.Zones,
				RecordsPerZone:              cp.Limits.RecordsPerZone,
				QueriesPerDay:               cp.Limits.QueriesPerDay,
			}
		}

		plans[i] = sp
	}

	return plans
}

// ListPlans returns the list of available plans, optionally filtered by category.
// Data is loaded at startup from embedded JSON plan definitions.
//
//	GET /v1/billing/plans
//	GET /v1/billing/plans?category=dns
func ListPlans(c *gin.Context) {
	category := c.Query("category")
	if category == "" {
		c.JSON(200, hanzoPlans)
		return
	}

	filtered := make([]staticPlan, 0)
	for _, p := range hanzoPlans {
		if p.Category == category {
			filtered = append(filtered, p)
		}
	}
	c.JSON(200, filtered)
}

// GetPlan returns a single plan by slug.
//
//	GET /v1/billing/plans/:id
func GetPlan(c *gin.Context) {
	id := c.Param("id")
	for _, p := range hanzoPlans {
		if p.Slug == id {
			c.JSON(200, p)
			return
		}
	}
	http.Fail(c, 404, "plan not found", nil)
}

// lookupPlan finds a plan by slug across all loaded plans.
// Returns nil if not found.
func lookupPlan(slug string) *staticPlan {
	for i := range hanzoPlans {
		if hanzoPlans[i].Slug == slug {
			return &hanzoPlans[i]
		}
	}
	return nil
}

// IncludedMonthlyCents returns the recurring monthly included-usage allotment
// for a plan slug, in cents. Returns 0 when the plan is unknown or declares no
// included allotment. This is the single catalog-derived input to the monthly
// allotment grant — the dollar value is the plan's declared cloud credit
// (@hanzo/plans limits.includedCloudCredits / includedCloudCreditsPerUser,
// i.e. the cloud.included_credits_usd entitlement).
func IncludedMonthlyCents(slug string) int64 {
	p := lookupPlan(slug)
	if p == nil || p.Limits == nil {
		return 0
	}
	// The monthly allotment grants the plan's declared cloud-credit allowance:
	// @hanzo/plans publishes it as limits.includedCloudCredits (flat) or
	// includedCloudCreditsPerUser (per seat) — the canonical
	// cloud.included_credits_usd entitlement. includedCreditUsd is a legacy alias
	// no published plan sets. Prefer the real fields, in that order.
	usd := p.Limits.IncludedCloudCredits
	if usd == nil {
		usd = p.Limits.IncludedCloudCreditsPerUser
	}
	if usd == nil {
		usd = p.Limits.IncludedCreditUsd
	}
	if usd == nil || *usd <= 0 {
		return 0
	}
	return int64(*usd) * 100
}

// Plan is the exported snapshot used by external seeders (e.g. the
// Stripe parity seed in commerce.go). It mirrors the subset of fields
// the seed populates onto seed.Plan, with field names that match the
// caller's expectations (PriceMonth / PriceYear are cent-denominated
// monthly + annual prices). Internal callers stick with staticPlan;
// this type exists so the public surface doesn't leak the unexported
// shape and so we can evolve them independently.
type Plan struct {
	Slug        string
	Name        string
	Description string
	Category    string
	PriceMonth  int64
	PriceYear   int64
	Currency    string
}

// StaticPlans returns a snapshot of the embedded plan catalog as the
// exported Plan shape. The slice is freshly allocated so callers may
// mutate freely without bleeding into the canonical hanzoPlans var.
func StaticPlans() []Plan {
	out := make([]Plan, len(hanzoPlans))
	for i, p := range hanzoPlans {
		out[i] = toPlan(&p)
	}
	return out
}

// LookupStaticPlan resolves a single plan by slug from the embedded
// catalog and returns it in the exported Plan shape. Returns nil when
// the slug is unknown. It is the single-plan analogue of StaticPlans
// and shares the same staticPlan -> Plan projection, so external
// seeders (e.g. cmd/grant) never touch the unexported wire type.
func LookupStaticPlan(slug string) *Plan {
	sp := lookupPlan(slug)
	if sp == nil {
		return nil
	}
	p := toPlan(sp)
	return &p
}

// toPlan projects the internal staticPlan wire type onto the exported
// Plan shape. Single source of truth for the field mapping used by both
// StaticPlans and LookupStaticPlan.
func toPlan(p *staticPlan) Plan {
	return Plan{
		Slug:        p.Slug,
		Name:        p.Name,
		Description: p.Description,
		Category:    p.Category,
		PriceMonth:  p.Price,
		PriceYear:   p.PriceAnnual,
		Currency:    p.Currency,
	}
}
