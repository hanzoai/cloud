// billing_plans.go holds rate, entitlement, tier, rollup, processor posture, methods, plans, coupons and promos.

package client

import "encoding/json"

// ---- billing.rate — what one unit of metered work costs ---------------------

// BillingRate answers what the platform charges for one unit of something it
// meters, from the authority an operator edits at admin.hanzo.ai.
//
// It is PLATFORM-GLOBAL and takes no subject. What a GB-month costs does not
// depend on who is storing the bytes — a plan may include some of it, and a
// wallet pays for the rest, but those are different questions asked elsewhere.
// So unlike tier and rollup this op needs no validated org, which is also what
// lets an app that is metering work ask it without one.
const BillingRate = "billing_rate"

// RateIn names the metered thing: the product it belongs to and what is metered
// inside it. Both are required — together they are the identity, and a rate keyed
// on the metered thing alone would let one product's price answer for another's.
type RateIn struct {
	Product string `json:"product"`
	Meter   string `json:"meter"`
}

// Rate is what one unit costs.
//
// FOUND IS NOT REDUNDANT WITH NANO. Zero is a real price — something the platform
// meters and gives away — and it must not read the same as "no rate is
// published", which is what sends a caller to its own floor instead. A bool
// carries the difference a number cannot.
type Rate struct {
	Product string `json:"product"`
	Meter   string `json:"meter"`
	// Unit is what ONE of the billed quantity is, so a reader can tell a
	// GB-month from a thousand characters without knowing the meter.
	Unit string `json:"unit,omitempty"`
	// Nano is the price of one Unit in nano-dollars, a billionth of a dollar.
	// Cents cannot hold these: fractions of a cent per million tokens round to
	// zero or to a hundred times the price.
	Nano  int64 `json:"nano"`
	Found bool  `json:"found"`
}

// ---- billing.entitlement — what an org's plan licenses ----------------------

// BillingEntitlement answers whether the calling org holds an active entitlement for
// a licensed product, and what its plan grants.
const BillingEntitlement = "billing_entitlement"

// EntitlementIn names the licensed product an org's entitlement is asked about.
type EntitlementIn struct {
	// Product is the licensed product: "engine", "team", a plugin id.
	Product string `json:"product"`
}

// Entitlement is whether an org holds an active entitlement for a product, and what
// its plan grants.
type Entitlement struct {
	// Product is the product asked about.
	Product string `json:"product"`
	// Active reports an entitlement that is paid and has not lapsed.
	Active bool `json:"active"`
	// Plan is the plan the entitlement resolved to ("pro", "max").
	Plan string `json:"plan,omitempty"`
	// Features are the plan's flat license features, copied into a license as they are.
	Features []string `json:"features,omitempty"`
	// Expires bounds the entitlement in unix seconds; 0 is unbounded.
	Expires int64 `json:"expires,omitempty"`
	// EarlyAccess is whether a live plan of the org carries early access, which opens
	// every product in beta to it without applying. It answers for the org, whatever
	// product was asked.
	EarlyAccess bool `json:"earlyAccess,omitempty"`
}

// ---- billing.tier and billing.rollup — what a plan allows, and what is left --

const (
	BillingTier   = "billing_tier"
	BillingRollup = "billing_rollup"
)

// TierLimits is what a tier allows. The fields are flat rather than nested
// because the shape they reproduce embeds its config struct, and an embedded
// struct marshals flat.
type TierLimits struct {
	Name              string   `json:"name"`
	DisplayName       string   `json:"displayName"`
	MaxAgents         int      `json:"maxAgents"`
	DailyCreditsCents int64    `json:"dailyCreditsCents"`
	AllowedModels     []string `json:"allowedModels"`
	// UnlimitedAgents reports that MaxAgents 0 means "no ceiling" rather than
	// "no agents" — the reading a bare zero cannot carry.
	UnlimitedAgents bool `json:"unlimitedAgents"`
}

// TierBalance is what the subject can actually spend.
//
// EffectiveAvailable is the ONLY figure a gate should compare against zero. The
// others are its parts: prepaid, credits and the daily term are three sources of
// one spend, not three balances to add up a second time.
type TierBalance struct {
	Currency           string `json:"currency"`
	PrepaidAvailable   int64  `json:"prepaidAvailable"`
	CreditsRemaining   int64  `json:"creditsRemaining"`
	DailyRemaining     int64  `json:"dailyRemaining"`
	EffectiveAvailable int64  `json:"effectiveAvailable"`
}

// TierIn asks for one subject's tier. The tier is derived from the subject's own
// subscriptions and is never named by a caller, a SuperAdmin included.
type TierIn struct {
	Subject string `json:"subject"`
}

// Tier is a subject's tier: which one they are on, what it allows, what they can
// spend, and how much of each plan window is left.
type Tier struct {
	User string `json:"user"`
	// Plan is the catalog rung the subject is served as ("" with no plan). A tier
	// is a class of rungs; a bound that differs within one class keys on this.
	Plan string `json:"plan,omitempty"`
	// Subscription is the id of the subscription row whose plan is the served rung,
	// "" when the tier is not a subscription's.
	Subscription string      `json:"subscription,omitempty"`
	Tier         TierLimits  `json:"tier"`
	Balance      TierBalance `json:"balance"`
	// PlanName is the display name of the served plan ("Unlimited"), "" with no
	// plan. A plan only the plan authority knows has no catalog name, so a reader
	// shows this, not the tier.
	PlanName string `json:"planName,omitempty"`
	// PlanHolder is the canonical account claim ("person:hanzo/james", "org:acme")
	// whose live paid subscription this org borrows through a plan link; absent when
	// the subject uses its own books or holds no plan. Appended last for the ZAP wire.
	PlanHolder string `json:"planHolder,omitempty"`
}

// RollupIn asks for one subject's month. Plan overrides which plan the figures
// are computed against; empty resolves the subject's own.
type RollupIn struct {
	Subject string `json:"subject"`
	Plan    string `json:"plan,omitempty"`
}

// RollupAllotment is the PLAN side of a month: what the plan includes, what has
// actually been granted, and what is left of it.
type RollupAllotment struct {
	MonthlyCents   int64 `json:"monthlyCents"`
	GrantedCents   int64 `json:"grantedCents"`
	ConsumedCents  int64 `json:"consumedCents"`
	RemainingCents int64 `json:"remainingCents"`
}

// RollupBalance is the WALLET side: prepaid money the holder bought.
type RollupBalance struct {
	BalanceCents   int64 `json:"balanceCents"`
	HoldsCents     int64 `json:"holdsCents"`
	AvailableCents int64 `json:"availableCents"`
}

// Rollup is a subject's month. The two blocks stay separate because they are
// separate monies — one was sold with the plan, one was bought with a card — and
// their sum is not a number anyone holds.
type Rollup struct {
	User          string          `json:"user"`
	Plan          string          `json:"plan"`
	Currency      string          `json:"currency"`
	Period        string          `json:"period"`
	Included      RollupAllotment `json:"included"`
	ConsumedCents int64           `json:"consumedCents"`
	OverageCents  int64           `json:"overageCents"`
	Balance       RollupBalance   `json:"balance"`
}

// ---- billing.settings and billing.mode — the org's processor posture --------

const (
	BillingSettings = "billing_settings"
	BillingMode     = "billing_mode"
)

// PayPalPublicConfig is the public half of an org's PayPal REST v2 posture —
// the client id the PayPal JS SDK loads with, and the environment ("live" or
// "sandbox") it transacts against. Never carries a secret.
type PayPalPublicConfig struct {
	Enabled     bool   `json:"enabled"`
	ClientID    string `json:"clientId,omitempty"`
	Environment string `json:"environment,omitempty"`
}

// StripePublicConfig is the public half of an org's Stripe posture — the
// publishable key Stripe's card form mounts with and the environment it lives
// in. Enabled says some currency is paid through Stripe's card form here. Never
// carries a secret.
type StripePublicConfig struct {
	Enabled        bool   `json:"enabled"`
	PublishableKey string `json:"publishableKey,omitempty"`
	Environment    string `json:"environment,omitempty"`
}

// PaymentConfig is the PUBLIC half of an org's processor configuration — the ids
// a browser needs to tokenize a card, and the environment it must tokenize
// against. It carries no secret: an application id is published to every
// checkout page by design.
type PaymentConfig struct {
	Provider      string `json:"provider"`
	ApplicationID string `json:"applicationId"`
	LocationID    string `json:"locationId"`
	Environment   string `json:"environment"`
	Live          bool   `json:"live"`
	// PayPal is the public PayPal REST v2 posture alongside Square (or in place
	// of Square in countries Square does not serve). Appended last for ZAP wire.
	PayPal *PayPalPublicConfig `json:"paypal,omitempty"`
	// Stripe is the public Stripe.js posture, and Cards says per currency whose
	// card form takes it ("square" or "stripe"). A browser reads these two to pick
	// the form; without them it cannot tell Stripe is the processor. Appended last
	// for ZAP wire.
	Stripe *StripePublicConfig `json:"stripe,omitempty"`
	// Cards is the per-currency card form, as a slice because a map has no layout
	// on the plane. MarshalJSON renders the object the browser reads —
	// {"usd":"stripe"} — once, here, as CreditBreakdown renders its tags.
	Cards []PaymentCard `json:"-"`
}

// PaymentCard says whose card form takes one currency: Processor is "square" or
// "stripe". A currency no card processor takes has no entry.
type PaymentCard struct {
	Currency  string `json:"currency"`
	Processor string `json:"processor"`
}

// MarshalJSON renders Cards as the object checkout reads on
// GET /v1/billing/settings: {"cards": {"usd": "stripe"}}. Every other field keeps
// its tag.
func (c PaymentConfig) MarshalJSON() ([]byte, error) {
	type plain PaymentConfig
	var by map[string]string
	if len(c.Cards) > 0 {
		by = make(map[string]string, len(c.Cards))
		for _, e := range c.Cards {
			by[e.Currency] = e.Processor
		}
	}
	return json.Marshal(struct {
		plain
		Cards map[string]string `json:"cards,omitempty"`
	}{plain: plain(c), Cards: by})
}

// ModeIn moves an org between test and live money.
type ModeIn struct {
	TestMode bool `json:"testMode"`
}

// Mode is which money an org is transacting in. Live and TestMode are one fact
// stated twice, in the two vocabularies its readers use; they are always
// opposites, and the store is the single authority for both.
type Mode struct {
	OrgID    string `json:"orgId"`
	OrgName  string `json:"orgName"`
	Live     bool   `json:"live"`
	TestMode bool   `json:"testMode"`
}

// ---- billing.methods and billing.plans — two rendered documents -------------

const (
	BillingMethods       = "billing_methods"
	BillingMethodSave    = "billing_method_save"
	BillingMethodUpdate  = "billing_method_update"
	BillingMethodDefault = "billing_method_default"
	BillingMethodDetach  = "billing_method_detach"

	BillingPlans = "billing_plans"
)

// Rendered is a document the store produced and the endpoint serves without
// reading.
//
// It exists for the two families whose wire is a deep tree of the STORE'S OWN
// models — a saved payment method carries five kinds of processor detail plus a
// billing address, and a plan carries its limits and its licensing — where a
// mirror in this package would be sixty fields that only ever have to agree with
// something else. Carrying the rendered bytes is byte-exact by construction,
// which is the property a hand-kept mirror can only approximate.
//
// It is the same distinction the money endpoint already draws between its typed
// ledger read and its raw balance read, and it is deliberately narrow: a value
// the endpoint DECIDES on is typed, and only a document it forwards is bytes.
type Rendered struct {
	Body []byte `json:"body"`
	// Created reports that the act made a NEW row rather than answering with one
	// that already existed. It rides here because only the store can tell the two
	// apart — saving a card already on file answers with the row that holds it —
	// and the endpoint has to know which happened to answer 201 or 200. Absent on a
	// read, where nothing was created and the zero value is the truth.
	Created bool `json:"created,omitempty"`
}

// MethodsIn lists one subject's saved payment methods, optionally of one kind.
type MethodsIn struct {
	Subject string `json:"subject"`
	Kind    string `json:"kind,omitempty"`
}

// MethodSaveIn saves a payment method for a subject.
//
// Email is the caller's own address, resolved at the endpoint from its
// credential: it names the processor's customer profile, and the store must
// never read an identity it was not handed. Body is the method to save, exactly
// as the caller sent it, so the vaulting path sees what the browser produced.
type MethodSaveIn struct {
	Subject string `json:"subject"`
	Email   string `json:"email,omitempty"`
	Body    []byte `json:"body"`
	// Name is the caller's own name, off the identity record IAM holds for them: the
	// name on the processor's customer. It is never the body's, and never the billing key.
	Name string `json:"name,omitempty"`
}

// MethodRef names one saved method to remove.
//
// Privileged marks the service or admin caller that may act on any subject
// inside the org. It is the ENDPOINT'S determination and travels as one, because
// authority decided twice is authority that eventually disagrees with itself.
type MethodRef struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"`
	Privileged bool   `json:"privileged,omitempty"`
}

// MethodUpdateIn updates billing address or metadata on one saved payment method.
type MethodUpdateIn struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"`
	Privileged bool   `json:"privileged,omitempty"`
	Body       []byte `json:"body"`
}

// Detachment is what a removal answers with.
type Detachment struct {
	// Deleted is whether the method was actually removed. False with no error
	// means it was already gone, which is a successful detach rather than a
	// failure — a retry must not be an error.
	Deleted bool `json:"deleted"`
	// ID is the method that was detached, echoed so a caller batching several can
	// tell the answers apart.
	ID string `json:"id"`
}

// PlansIn names whose public plan catalog to read, optionally narrowed to one
// category.
type PlansIn struct {
	// Host is the customer-facing host the catalog is read on. The brand it
	// resolves to decides which catalog is on sale; empty is the deployment's
	// default brand.
	Host     string `json:"host,omitempty"`
	Category string `json:"category,omitempty"`
	// Coupon is a plan coupon code to quote the catalog under. Empty reads the
	// catalog exactly as it is; a code annotates each row with what each month it
	// discounts charges under the coupon and how many months that is, or why the
	// coupon does not apply.
	Coupon string `json:"coupon,omitempty"`
}

// ---- billing.plan — the plan authority's rows, as a SuperAdmin keeps them ---

const (
	// BillingPlanRows lists every row of the plan authority, whatever its status;
	// BillingPlanCreate adds one; BillingPlanSet restates one. The list answers a
	// SuperAdmin and the platform; the two writes answer a SuperAdmin only.
	BillingPlanRows   = "billing_plan_rows"
	BillingPlanCreate = "billing_plan_create"
	BillingPlanSet    = "billing_plan_set"
)

// PlanCapacity is how many of each thing a plan may hold: agents, bots, workspace
// guests, org members, the seat floor of a per-seat plan, and the DNS quotas. Nil is
// unstated, -1 is unlimited and 0 is none. A restatement keeps the seat bounds and
// the four order figures it leaves nil, and a 0 removes one.
type PlanCapacity struct {
	// Agents is how many agents the plan may run.
	Agents *int `json:"agents,omitempty"`
	// Bots is how many resident bots the plan may run.
	Bots *int `json:"bots,omitempty"`
	// TeamGuests is how many guests the holder may invite to its team workspace.
	TeamGuests *int `json:"teamGuests,omitempty"`
	// MaxMembers is the most members an org on the plan may hold, and the most
	// seats a card buys of a per-seat plan: 1 or more, or -1 for no ceiling.
	MaxMembers *int `json:"maxMembers,omitempty"`
	// MinSeats is the fewest seats a per-seat plan bills: 1 or more, and no more
	// than MaxMembers.
	MinSeats *int `json:"minSeats,omitempty"`
	// Zones is how many DNS zones the plan may hold.
	Zones *int `json:"zones,omitempty"`
	// RecordsPerZone is how many records each DNS zone may hold.
	RecordsPerZone *int `json:"recordsPerZone,omitempty"`
	// QueriesPerDay is how many DNS queries the plan answers in a day.
	QueriesPerDay *int `json:"queriesPerDay,omitempty"`
	// SpendLimits are the monthly usage limits per member an organization's order
	// offers, in whole US dollars from 1 to 1000000, ascending. A plan stating any of
	// the four order figures is sold per seat, to an organization's own account only.
	SpendLimits []int `json:"spendLimits,omitempty"`
	// SpendLimit is the per-member monthly limit an order starts on, in whole US
	// dollars: one of SpendLimits.
	SpendLimit *int `json:"spendLimit,omitempty"`
	// BalanceMonths is the starting usage balance an order starts with unless the
	// buyer says otherwise, in months of its seats' price: 1 or more, on a plan that
	// states a BalanceCeiling.
	BalanceMonths *int `json:"balanceMonths,omitempty"`
	// BalanceCeiling is the most a starting balance may be, in whole US dollars from
	// 1 to 1000000; a card sale above it is refused, and a plan stating none sells no
	// balance.
	BalanceCeiling *int `json:"balanceCeiling,omitempty"`
	// InvoiceDays is how many days an invoice for the plan gives to pay it, 1 to
	// 365; a plan stating none is not sold by invoice. After BalanceCeiling, for
	// the ZAP wire.
	InvoiceDays *int `json:"invoiceDays,omitempty"`
	// GraceDays is how many days past due an unpaid invoice stands before it is
	// voided, 1 to 365.
	GraceDays *int `json:"graceDays,omitempty"`
}

// PlanLicensing is what a plan licenses: the products, the engine app builds and
// capability tokens, the licensed seats (-1 unlimited, nil unstated), and whether
// its holders open every product in beta without applying.
type PlanLicensing struct {
	// Products are the proprietary products the plan entitles, such as engine.
	Products []string `json:"products,omitempty"`
	// Apps are the engine app builds it licenses, such as hanzo.
	Apps []string `json:"apps,omitempty"`
	// Features are the engine capability tokens it grants, such as inference.
	Features []string `json:"features,omitempty"`
	// Seats is the named users or devices the licence covers; -1 unlimited.
	Seats *int `json:"seats,omitempty"`
	// EarlyAccess opens every product in beta to a holder's org without applying.
	EarlyAccess bool `json:"earlyAccess,omitempty"`
}

// PlanReload is the automatic reload buying a plan switches on for the payer: its
// balance is topped up by AmountCents, charged to its card on file, whenever it falls
// below ThresholdCents.
type PlanReload struct {
	// ThresholdCents is the balance, in cents, the reload fires below.
	ThresholdCents int64 `json:"thresholdCents"`
	// AmountCents is what one reload charges and adds, in cents.
	AmountCents int64 `json:"amountCents"`
}

// PlanRow is one row of the plan authority: what a card is charged, what the pricing
// page prints, and whether it is on sale.
type PlanRow struct {
	// Slug is the plan's id, which every subscription on it records.
	Slug string `json:"slug"`
	// Name is what the pricing page and checkout call the plan.
	Name string `json:"name"`
	// Description is the plan's summary on its card and at checkout.
	Description string `json:"description"`
	// Category is the ladder the plan sits on, such as personal, team or dns.
	Category string `json:"category"`
	// Price is what a card is charged each interval, in cents; per seat on a
	// per-seat plan.
	Price int64 `json:"price"`
	// AnnualTotal is what a card is charged once for a year, in cents; 0 when the
	// plan is not sold by the year.
	AnnualTotal int64 `json:"annualTotal"`
	// Currency is the ISO code the prices are in, lower-cased.
	Currency string `json:"currency"`
	// Interval is the billing period: month or year.
	Interval string `json:"interval"`
	// PerSeat says Price is per seat.
	PerSeat bool `json:"perSeat"`
	// ContactSales says the plan is sold by quote, not at checkout.
	ContactSales bool `json:"contactSales"`
	// Popular marks the card the pricing page draws forward.
	Popular bool `json:"popular"`
	// Status is active (on sale), draft, private or archived; only active is sold.
	Status string `json:"status"`
	// Features are the card's lines, in order.
	Features []string `json:"features"`
	// Limits are the plan's capacities; absent when it states none.
	Limits *PlanCapacity `json:"limits,omitempty"`
	// Licensing is what the plan licenses; absent when it licenses nothing.
	Licensing *PlanLicensing `json:"licensing,omitempty"`
	// ReloadsPerDay is the most automatic reloads the plan allows a payer in one UTC
	// day, 0 for no daily limit; absent when the row states none (3).
	ReloadsPerDay *int `json:"reloadsPerDay,omitempty"`
	// Reload is the automatic reload buying the plan switches on; absent when none.
	Reload *PlanReload `json:"reload,omitempty"`
	// AdminEdited says a SuperAdmin stated the row.
	AdminEdited bool `json:"adminEdited"`
	// Entitlements is the canonical entitlement block the plan grants, a JSON
	// object of namespaced keys ("ai.priority_inference", "team.guests") to the
	// value granted, -1 for unlimited. Empty when no one has recorded it.
	Entitlements string `json:"entitlements,omitempty"`
	// Replaces are the slugs the plan was sold under before it was renamed.
	Replaces []string `json:"replaces,omitempty"`
	// Multiplier is the plan's usage multiple relative to the 1× Pro baseline
	// (1, 5, 10, 25; nil for Free and Unlimited). Appended last for ZAP wire.
	Multiplier *int `json:"multiplier,omitempty"`
	// UsagePolicy distinguishes "daily" (Free), "metered" (Pro..Ultimate), and
	// "unlimited" (Unlimited) so nil Multiplier is never ambiguous.
	UsagePolicy string `json:"usagePolicy,omitempty"`
	// Capabilities is the JSON object of admin-editable comparison matrix cells
	// (agentExecution, parallelExecution, inferencePriority, researchCapacity, support).
	Capabilities string `json:"capabilities,omitempty"`
}

// PlanRows is every row of the plan authority, ordered by slug.
type PlanRows struct {
	// Rows are the plan authority's rows.
	Rows []PlanRow `json:"rows"`
}

// PlanSet is a SuperAdmin's statement of one plan: every field here replaces the
// row's, the reload, seat-pricing and contact-sales fields only when present, and
// every field it does not carry (currency, the credit a plan grants) is kept as
// stored. Price and AnnualTotal are cents, as
// on [PlanRow]; Status is active, draft, private or archived.
type PlanSet struct {
	// Slug is the plan's id; it never changes once added.
	Slug string `json:"slug"`
	// Name is what the pricing page and checkout call the plan.
	Name string `json:"name"`
	// Description is the plan's summary on its card and at checkout.
	Description string `json:"description"`
	// Category is the ladder the plan sits on, such as personal, team or dns.
	Category string `json:"category"`
	// Price is what a card is charged each interval, in cents.
	Price int64 `json:"price"`
	// AnnualTotal is what a card is charged once for a year, in cents; 0 sells no
	// year.
	AnnualTotal int64 `json:"annualTotal"`
	// Interval is the billing period: month or year.
	Interval string `json:"interval"`
	// Status is active, draft, private or archived.
	Status string `json:"status"`
	// Popular marks the card the pricing page draws forward.
	Popular bool `json:"popular"`
	// Features are the card's lines, in order.
	Features []string `json:"features"`
	// Limits are the plan's capacities.
	Limits *PlanCapacity `json:"limits,omitempty"`
	// Licensing is what the plan licenses.
	Licensing *PlanLicensing `json:"licensing,omitempty"`
	// ReloadsPerDay is the most automatic reloads a day, 0 for no daily limit; absent
	// keeps the row's.
	ReloadsPerDay *int `json:"reloadsPerDay,omitempty"`
	// Reload is the automatic reload buying the plan switches on; absent keeps the
	// row's, and an AmountCents of 0 removes it.
	Reload *PlanReload `json:"reload,omitempty"`
	// PerSeat says Price is per seat; absent keeps the row's. After Reload, for
	// the ZAP wire.
	PerSeat *bool `json:"perSeat,omitempty"`
	// ContactSales says the plan is sold by quote and never by card; absent keeps
	// the row's.
	ContactSales *bool `json:"contactSales,omitempty"`
	// Entitlements is the canonical entitlement block the plan grants, a JSON
	// object of namespaced keys to the value granted; empty keeps the row's, and
	// "{}" states that the plan grants nothing.
	Entitlements string `json:"entitlements,omitempty"`
	// Replaces are the slugs the plan was sold under before it was renamed; absent
	// keeps the row's, and an empty list removes them.
	Replaces []string `json:"replaces,omitempty"`
	// Multiplier is the usage multiple relative to the 1× Pro baseline; absent
	// keeps the row's, 0 clears it. Appended last for ZAP wire.
	Multiplier *int `json:"multiplier,omitempty"`
	// UsagePolicy is "daily", "metered", or "unlimited"; empty keeps the row's.
	UsagePolicy string `json:"usagePolicy,omitempty"`
	// Capabilities is a JSON object of string->string capability cells; empty
	// keeps the row's, "{}" clears it.
	Capabilities string `json:"capabilities,omitempty"`
}

// ---- billing.coupon — the plan coupons a SuperAdmin keeps ------------------

const (
	// BillingCoupon reads one plan coupon; BillingCouponSet creates or replaces
	// one; BillingCouponMint draws a new single-use one under a fresh code;
	// BillingCouponExtend grants a coupon's months to the subscriptions already
	// bought with it. All four are platform authority only.
	BillingCoupon       = "billing_coupon"
	BillingCouponSet    = "billing_coupon_set"
	BillingCouponMint   = "billing_coupon_mint"
	BillingCouponExtend = "billing_coupon_extend"
)

// CouponRef names one plan coupon by its code (case-insensitive).
type CouponRef struct {
	Code string `json:"code"`
}

// CouponSet is a SuperAdmin's whole statement of one plan coupon: a percent
// off each of the first Months monthly periods of a plan purchase, the window it
// is redeemable in, how many uses it has in all (0 is no limit), the plan slugs
// it covers and whether it is on.
//
// Start and End are RFC3339 text, empty for an open end, for the reason every
// timestamp on this plane is text (see [BillingInvoice]).
type CouponSet struct {
	Code    string   `json:"code"`
	Percent int      `json:"percent"`
	Start   string   `json:"start,omitempty"`
	End     string   `json:"end,omitempty"`
	Limit   int      `json:"limit"`
	Plans   []string `json:"plans"`
	Enabled bool     `json:"enabled"`
	// Campaign names the offer the code is one of: an org and a card redeem one
	// code of a campaign. Empty is a code on its own. Last, so the fields before
	// it keep their places on the ZAP wire.
	Campaign string `json:"campaign,omitempty"`
	// Months is how many monthly periods the coupon discounts, from the first
	// paid one: 1 to 36, and 0 is 1, a first-month coupon. After Campaign, for the
	// same reason.
	Months int `json:"months,omitempty"`
}

// CouponMint asks for one new single-use plan coupon under a fresh code,
// Prefix-XXXXXXXXXX: Percent off the first Months months of every paid plan on
// sale (none is one), redeemable until End (RFC3339; empty never closes), one of
// Campaign's codes.
type CouponMint struct {
	Prefix   string `json:"prefix"`
	Percent  int    `json:"percent"`
	Months   int    `json:"months,omitempty"`
	End      string `json:"end,omitempty"`
	Campaign string `json:"campaign,omitempty"`
}

// Coupon is a plan coupon as stored, with how many uses it has had.
type Coupon struct {
	Code    string   `json:"code"`
	Percent int      `json:"percent"`
	Start   string   `json:"start,omitempty"`
	End     string   `json:"end,omitempty"`
	Limit   int      `json:"limit"`
	Plans   []string `json:"plans"`
	Enabled bool     `json:"enabled"`
	Used    int      `json:"used"`
	// Campaign is the offer the code is one of; absent for a code on its own.
	Campaign string `json:"campaign,omitempty"`
	// Months is how many monthly periods the coupon discounts, from the first
	// paid one; 1 is a first-month coupon.
	Months int `json:"months"`
}

// CouponExtend asks for a plan coupon's months to be granted to every live
// subscription already bought with it. Apply false answers what it would do and
// changes nothing.
type CouponExtend struct {
	Code  string `json:"code"`
	Apply bool   `json:"apply"`
}

// CouponExtension is what extending one coupon found and did: every org holding
// a use of it, and each subscription of those orgs bought with it.
type CouponExtension struct {
	// Code is the coupon, upper-cased as stored.
	Code string `json:"code"`
	// Months is how many monthly periods the coupon discounts now.
	Months int `json:"months"`
	// Applied is false on a dry run.
	Applied bool `json:"applied"`
	// Orgs is every org holding a use of the coupon.
	Orgs []string `json:"orgs"`
	// Subscriptions is each subscription of those orgs bought with the coupon.
	Subscriptions []CouponExtended `json:"subscriptions"`
}

// CouponExtended is one subscription bought with the coupon and the grant it
// holds after the extension (or, on a dry run, would hold).
type CouponExtended struct {
	// Org is the org that holds the subscription.
	Org string `json:"org"`
	// SubscriptionID is the subscription; absent when the org could not be read.
	SubscriptionID string `json:"subscriptionId,omitempty"`
	// UserID is the subject that pays for it.
	UserID string `json:"userId,omitempty"`
	// Status is the subscription's state: active, past_due, trialing, canceled.
	Status string `json:"status,omitempty"`
	// Test marks a sandbox subscription.
	Test bool `json:"test,omitempty"`
	// Percent is what the coupon takes off each month it discounts, as sold.
	Percent int `json:"percent,omitempty"`
	// Months is how many billing cycles the coupon discounts, from From.
	Months int `json:"months,omitempty"`
	// From is the start of the first paid period, where the cycles are counted
	// from, RFC3339.
	From string `json:"from,omitempty"`
	// Billed is how many cycles have been invoiced since From, discounted or not.
	Billed int `json:"billed"`
	// Changed is whether the grant moved (or, on a dry run, would).
	Changed bool `json:"changed"`
	// Missed counts the cycles among the first Months invoiced without the coupon,
	// paid or still owed: renewals that ran before it was extended, for a person
	// to settle, since an extension moves no money.
	Missed int `json:"missed,omitempty"`
	// Error is why the subscription was left as it was.
	Error string `json:"error,omitempty"`
}

// ---- billing.promo — the platform plan promo a SuperAdmin keeps -------------

const (
	// BillingPromo reads the platform plan promo; BillingPromoSet replaces it.
	// Both are platform authority only.
	BillingPromo    = "billing_promo"
	BillingPromoSet = "billing_promo_set"
)

// PromoIn asks for the platform plan promo. It names nothing: there is one.
type PromoIn struct{}

// Promo is the platform plan promo: PercentOff (0-100) off every plan it covers
// (Plans; empty is every paid plan) between Start and End, while Active. Start
// and End are RFC3339 text, empty for an open bound, for the reason every
// timestamp on this plane is text (see [BillingInvoice]).
type Promo struct {
	PercentOff int      `json:"percentOff"`
	Start      string   `json:"start,omitempty"`
	End        string   `json:"end,omitempty"`
	Plans      []string `json:"plans"`
	Active     bool     `json:"active"`
}

