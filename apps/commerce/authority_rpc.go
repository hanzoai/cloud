// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// authority_rpc.go — THE PLAN AUTHORITY'S ROWS, read and written where the store is.
//
// A plan row is what a card is charged and what the pricing page prints: the rows
// the boot seed reconciles to the shipped catalog, GET /v1/billing/plans serves
// while they are on sale, and the charge path prices from. A SuperAdmin adds and
// restates them from admin.hanzo.ai through the admin app's typed ops, which gate
// and audit the act and ask this process, the one that holds the store.

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	commerceplan "github.com/hanzoai/commerce/models/plan"
	"github.com/hanzoai/commerce/models/types/currency"
	commercetypes "github.com/hanzoai/commerce/types"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

// exposeAuthority publishes the plan authority's list and its two writes. Use
// calls it.
func exposeAuthority() {
	cloud.Plane().Post("/billing/plan/rows", planeRows,
		zip.WithOperationID(client.BillingPlanRows),
		zip.WithSummary("Every row of the plan authority"))
	cloud.Plane().Post("/billing/plan/create", planeCreate,
		zip.WithOperationID(client.BillingPlanCreate),
		zip.WithSummary("Add one row to the plan authority"))
	cloud.Plane().Post("/billing/plan/set", planeSet,
		zip.WithOperationID(client.BillingPlanSet),
		zip.WithSummary("Restate one row of the plan authority"))
}

// Lists every row of the plan authority, whatever its status, ordered by slug:
// what each is charged, what the pricing page prints, whether it is on sale, and
// whether a SuperAdmin decided it.
//
// It answers a SuperAdmin and the platform: a private row is a price agreed with
// one customer, so a tenant never reads the list.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRows(ctx context.Context, _ *cloud.Unit) (*client.PlanRows, error) {
	if who := cloud.Who(ctx); !who.Admin && who.Org != principal.Reserved {
		return nil, zip.ErrForbidden("plan rows: SuperAdmin or the platform")
	}
	rows := make([]*commerceplan.Plan, 0, 64)
	if _, err := commerceplan.Query(commerceplan.AuthorityDB(ctx)).GetAll(&rows); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "plan rows: %v", err)
	}
	out := &client.PlanRows{Rows: make([]client.PlanRow, 0, len(rows))}
	for _, p := range rows {
		out.Rows = append(out.Rows, planRow(p))
	}
	slices.SortFunc(out.Rows, func(a, b client.PlanRow) int { return strings.Compare(a.Slug, b.Slug) })
	return out, nil
}

// Adds one plan: a slug no row holds (1-64 of a-z, 0-9 and '-', starting with a
// letter or digit), its name, category, interval and status, what a card is charged
// for it, and what the pricing page prints. It is stored in usd, billed once per
// interval, and marked as a SuperAdmin's, so the boot seed leaves it as it stands.
// A slug a row already holds is 409; a statement the row cannot be stored as is 400.
//
// SuperAdmin only.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCreate(ctx context.Context, in *client.PlanSet) (*client.PlanRow, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("plan create: SuperAdmin required")
	}
	if err := checkPlan(in); err != nil {
		return nil, err
	}
	db := commerceplan.AuthorityDB(ctx)
	held, err := commerceplan.New(db).Query().Filter("Slug=", in.Slug).Get()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "plan create: %v", err)
	}
	if held {
		return nil, zip.Errorf(http.StatusConflict, "plan %q already exists", in.Slug)
	}
	p := commerceplan.New(db)
	p.Slug = in.Slug
	p.Currency = currency.USD
	p.IntervalCount = 1
	applyPlan(p, in)
	if err := checkOrder(p); err != nil {
		return nil, err
	}
	if err := p.Create(); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "plan create: %v", err)
	}
	row := planRow(p)
	return &row, nil
}

// Restates one plan: every field the statement carries replaces the row's, and
// what it does not carry — the slug, currency, the credit a plan grants, and seat
// pricing, contact sales, the reload, the seat bounds and the order figures when
// absent — is kept as stored; a seat bound or an order figure of 0 removes it. The
// seat floor is 1 or more and no more than the ceiling, which is 1 or more or -1 for
// none. The order figures are whole dollars, sold per seat: spend limits ascending
// from 1 to 1000000, a starting spend limit among them, starting-balance months of 1
// or more bounded by a balance ceiling from 1 to 1000000. The row is marked as a
// SuperAdmin's, so the boot seed leaves it as it stands. A slug no row holds is 404;
// a statement the row cannot be stored as is 400.
//
// SuperAdmin only.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSet(ctx context.Context, in *client.PlanSet) (*client.PlanRow, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("plan set: SuperAdmin required")
	}
	if err := checkPlan(in); err != nil {
		return nil, err
	}
	p := commerceplan.New(commerceplan.AuthorityDB(ctx))
	ok, err := p.Query().Filter("Slug=", in.Slug).Get()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "plan set: %v", err)
	}
	if !ok {
		return nil, zip.ErrNotFound("no plan " + in.Slug)
	}
	applyPlan(p, in)
	if err := checkOrder(p); err != nil {
		return nil, err
	}
	if err := p.Update(); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "plan set: %v", err)
	}
	row := planRow(p)
	return &row, nil
}

// slugShape is what a plan slug may be: it is a URL segment, a flag key suffix and
// the id every subscription records.
var slugShape = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// statuses are the lifecycles a row may be stated in.
var statuses = []string{commerceplan.StatusActive, commerceplan.StatusDraft, commerceplan.StatusPrivate, commerceplan.StatusArchived}

// checkPlan refuses a statement the row cannot be stored as, trimming it first.
func checkPlan(in *client.PlanSet) error {
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Name = strings.TrimSpace(in.Name)
	in.Category = strings.ToLower(strings.TrimSpace(in.Category))
	in.Interval = strings.TrimSpace(in.Interval)
	in.Status = strings.TrimSpace(in.Status)
	switch {
	case !slugShape.MatchString(in.Slug):
		return zip.ErrBadRequest("slug: 1-64 of a-z, 0-9 and '-', starting with a letter or digit")
	case !flags.Keyed(in.Slug):
		return zip.ErrBadRequest("slug: may not begin window- or hours-")
	case in.Name == "":
		return zip.ErrBadRequest("name is required")
	case in.Category == "":
		return zip.ErrBadRequest("category is required")
	case in.Interval != string(commercetypes.Monthly) && in.Interval != string(commercetypes.Yearly):
		return zip.ErrBadRequest("interval: month or year")
	case !slices.Contains(statuses, in.Status):
		return zip.ErrBadRequest("status: " + strings.Join(statuses, ", "))
	case in.Price < 0 || in.AnnualTotal < 0:
		return zip.ErrBadRequest("price and annualTotal are cents, 0 or more")
	case in.Category == monthly && (in.AnnualTotal > 0 || in.Interval == string(commercetypes.Yearly)):
		return zip.ErrBadRequest("personal plans are sold monthly: state no annual total and interval month")
	case in.ReloadsPerDay != nil && *in.ReloadsPerDay < 0:
		return zip.ErrBadRequest("reloadsPerDay is 0 (no daily limit) or more")
	}
	// A reload a plan switches on is a card charge with nobody there, so its figures
	// are held to the bounds every reload rule is (amountCents 0 removes it).
	if r := in.Reload; r != nil && r.AmountCents != 0 {
		if err := commercebilling.CheckReload(r.ThresholdCents, r.AmountCents); err != nil {
			return zip.ErrBadRequest(err.Error())
		}
	}
	if _, err := grants(in.Entitlements); err != nil {
		return err
	}
	if in.Multiplier != nil && *in.Multiplier < 0 {
		return zip.ErrBadRequest("multiplier is 1 or more, or 0 to remove it")
	}
	in.UsagePolicy = strings.ToLower(strings.TrimSpace(in.UsagePolicy))
	if in.UsagePolicy != "" && in.UsagePolicy != "daily" && in.UsagePolicy != "metered" && in.UsagePolicy != "unlimited" {
		return zip.ErrBadRequest("usagePolicy: daily, metered, or unlimited")
	}
	if _, err := capabilitiesOf(in.Capabilities); err != nil {
		return err
	}
	if len(in.Replaces) > maxReplaces {
		return zip.ErrBadRequest("replaces: at most 16 slugs")
	}
	for i, slug := range in.Replaces {
		slug = strings.ToLower(strings.TrimSpace(slug))
		if !slugShape.MatchString(slug) || slug == in.Slug || slices.Contains(in.Replaces[:i], slug) {
			return zip.ErrBadRequest("replaces: slugs other than the plan's own, each once")
		}
		in.Replaces[i] = slug
	}
	return nil
}

const (
	// maxGrants bounds an entitlement block: its bytes and its keys.
	maxGrants    = 16 << 10
	maxGrantKeys = 128
	maxReplaces  = 16
)

// grantKey is an entitlement key: a namespace, a dot, a name ("ai.agents").
var grantKey = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_:.-]*$`)

// grants reads a stated entitlement block: nil when none is stated (the row's is
// kept), else the object, which may be empty. Anything but an object of
// namespaced keys is refused.
func grants(stated string) (map[string]any, error) {
	stated = strings.TrimSpace(stated)
	if stated == "" {
		return nil, nil
	}
	if len(stated) > maxGrants {
		return nil, zip.ErrBadRequest("entitlements: at most 16 KiB")
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(stated), &out); err != nil || out == nil {
		return nil, zip.ErrBadRequest("entitlements: a JSON object of entitlement keys")
	}
	if len(out) > maxGrantKeys {
		return nil, zip.ErrBadRequest("entitlements: at most 128 keys")
	}
	for k := range out {
		if !grantKey.MatchString(k) {
			return nil, zip.ErrBadRequest("entitlements: key " + strconv.Quote(k) + " is not namespace.name")
		}
	}
	return out, nil
}

// capabilitiesOf reads a stated capability differentiation map: nil when omitted
// (keeps the row's), empty map for "{}" (clears it), else trimmed key/value pairs.
func capabilitiesOf(stated string) (map[string]string, error) {
	stated = strings.TrimSpace(stated)
	if stated == "" {
		return nil, nil
	}
	if len(stated) > maxGrants {
		return nil, zip.ErrBadRequest("capabilities: at most 16 KiB")
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(stated), &out); err != nil || out == nil {
		return nil, zip.ErrBadRequest("capabilities: a JSON object of string values")
	}
	if len(out) > maxGrantKeys {
		return nil, zip.ErrBadRequest("capabilities: at most 128 keys")
	}
	clean := make(map[string]string, len(out))
	for k, v := range out {
		k = strings.TrimSpace(k)
		if k == "" {
			return nil, zip.ErrBadRequest("capabilities: key must not be empty")
		}
		clean[k] = strings.TrimSpace(v)
	}
	return clean, nil
}

// maxDollars is the most an order figure states, in whole dollars: past what a card
// sale is for, and far inside int64 once commerce reads it in cents.
const maxDollars = 1_000_000

// checkOrder refuses the seat bounds and order figures a row would hold, once a
// statement is laid over it (applyPlan keeps what the statement omits), when a
// checkout could not sell them: a seat floor below 1, a seat ceiling below 1 other
// than -1 (none), a floor above the ceiling; an order on a plan not priced per seat;
// spend limits that are not whole dollars from 1 to maxDollars in ascending order,
// a starting spend limit that is not one of them, starting-balance months below 1,
// a balance ceiling outside 1 to maxDollars, and months with no ceiling to bound them.
func checkOrder(p *commerceplan.Plan) error {
	l := p.Limits
	if l == nil {
		return nil
	}
	for i, d := range l.SpendLimits {
		if d <= 0 || d > maxDollars || (i > 0 && d <= l.SpendLimits[i-1]) {
			return zip.ErrBadRequest("limits.spendLimits: whole dollars from 1 to 1000000, ascending, each once")
		}
	}
	switch {
	case l.MinSeats != nil && *l.MinSeats < 1:
		return zip.ErrBadRequest("limits.minSeats is 1 or more")
	case l.MaxMembers != nil && *l.MaxMembers < 1 && *l.MaxMembers != -1:
		return zip.ErrBadRequest("limits.maxMembers is 1 or more, or -1 for no ceiling")
	case l.MinSeats != nil && l.MaxMembers != nil && *l.MaxMembers > 0 && *l.MinSeats > *l.MaxMembers:
		return zip.ErrBadRequest("limits.minSeats is no more than limits.maxMembers")
	case l.Ordered() && !p.PerSeat:
		return zip.ErrBadRequest("an organization's order is sold per seat: state perSeat true")
	case l.SpendLimit != nil && !slices.Contains(l.SpendLimits, *l.SpendLimit):
		return zip.ErrBadRequest("limits.spendLimit is one of limits.spendLimits")
	case l.BalanceMonths != nil && *l.BalanceMonths < 1:
		return zip.ErrBadRequest("limits.balanceMonths is 1 or more, or 0 to remove it")
	case l.BalanceCeiling != nil && (*l.BalanceCeiling < 1 || *l.BalanceCeiling > maxDollars):
		return zip.ErrBadRequest("limits.balanceCeiling is whole dollars from 1 to 1000000, or 0 to remove it")
	case l.BalanceMonths != nil && l.BalanceCeiling == nil:
		return zip.ErrBadRequest("limits.balanceMonths needs a limits.balanceCeiling to bound it")
	case l.InvoiceDays != nil && (*l.InvoiceDays < 1 || *l.InvoiceDays > 365):
		return zip.ErrBadRequest("limits.invoiceDays is 1 to 365 days, or 0 to remove it")
	case l.GraceDays != nil && (*l.GraceDays < 1 || *l.GraceDays > 365):
		return zip.ErrBadRequest("limits.graceDays is 1 to 365 days, or 0 to remove it")
	case l.GraceDays != nil && l.InvoiceDays == nil:
		return zip.ErrBadRequest("limits.graceDays needs limits.invoiceDays: a plan not sold by invoice has nothing to void")
	}
	return nil
}

// monthly is the category sold by the month alone: a personal plan is never sold by
// the year.
const monthly = "personal"

// applyPlan writes a statement onto a row. The annual price a page shows is the
// annual total over twelve, rounded to the cent, so the two cannot disagree. The
// capacities replace the row's; every other figure of the row's limits is kept —
// the credit a plan grants, because a plan edit never mints money; the seat floor
// and ceiling, an order's spend limits, starting-balance months and balance ceiling,
// the reload bound and the reload are replaced only when the statement carries them,
// so an archive, a price edit or a save from a page that draws none of them keeps
// them; and seat pricing and contact sales likewise.
func applyPlan(p *commerceplan.Plan, in *client.PlanSet) {
	p.Name = in.Name
	p.Description = strings.TrimSpace(in.Description)
	p.Category = in.Category
	p.Price = currency.Cents(in.Price)
	p.AnnualTotal = currency.Cents(in.AnnualTotal)
	p.PriceAnnual = currency.Cents((in.AnnualTotal + 6) / 12)
	p.Interval = commercetypes.Interval(in.Interval)
	p.Status = in.Status
	p.Popular = in.Popular
	p.Features = slices.Clone(in.Features)
	p.Licensing = nil
	if l := in.Licensing; l != nil {
		p.Licensing = &commerceplan.Licensing{Products: l.Products, Apps: l.Apps, Features: l.Features, Seats: l.Seats, EarlyAccess: l.EarlyAccess}
	}
	// Every figure the statement does not carry stays as stored — the credit a plan
	// grants, the seat bounds, an order's spend limits and starting balance — and the
	// capacities it does carry replace the row's.
	kept := commerceplan.Limits{}
	if p.Limits != nil {
		kept = *p.Limits
	}
	var c client.PlanCapacity
	if in.Limits != nil {
		c = *in.Limits
	}
	kept.Agents, kept.Bots, kept.TeamGuests = c.Agents, c.Bots, c.TeamGuests
	kept.Zones, kept.RecordsPerZone, kept.QueriesPerDay = c.Zones, c.RecordsPerZone, c.QueriesPerDay
	kept.MinSeats, kept.MaxMembers = restated(c.MinSeats, kept.MinSeats), restated(c.MaxMembers, kept.MaxMembers)
	if in.ReloadsPerDay != nil {
		kept.ReloadsPerDay = in.ReloadsPerDay
	}
	switch r := in.Reload; {
	case r == nil:
	case r.AmountCents == 0:
		kept.Reload = nil
	default:
		kept.Reload = &commerceplan.Reload{ThresholdCents: r.ThresholdCents, AmountCents: r.AmountCents}
	}
	// An organization's order figures are replaced only when the statement
	// carries them, as the reload is: a save from a page that does not draw them
	// keeps the row's, so the plan stays an organization's and its ceiling holds.
	// An empty list or a 0 removes one.
	if c.SpendLimits != nil {
		kept.SpendLimits = nil
		if len(c.SpendLimits) > 0 {
			kept.SpendLimits = slices.Clone(c.SpendLimits)
		}
	}
	kept.SpendLimit = restated(c.SpendLimit, kept.SpendLimit)
	kept.BalanceMonths = restated(c.BalanceMonths, kept.BalanceMonths)
	kept.BalanceCeiling = restated(c.BalanceCeiling, kept.BalanceCeiling)
	kept.InvoiceDays = restated(c.InvoiceDays, kept.InvoiceDays)
	kept.GraceDays = restated(c.GraceDays, kept.GraceDays)
	p.Limits = nil
	if !reflect.ValueOf(kept).IsZero() {
		p.Limits = &kept
	}
	if in.PerSeat != nil {
		p.PerSeat = *in.PerSeat
	}
	if in.ContactSales != nil {
		p.ContactSales = *in.ContactSales
	}
	if e, _ := grants(in.Entitlements); e != nil {
		p.Entitlements = e
	}
	if in.Replaces != nil {
		p.Replaces = nil
		if len(in.Replaces) > 0 {
			p.Replaces = slices.Clone(in.Replaces)
		}
	}
	p.Multiplier = restated(in.Multiplier, p.Multiplier)
	if in.UsagePolicy != "" {
		p.UsagePolicy = in.UsagePolicy
	}
	if caps, _ := capabilitiesOf(in.Capabilities); caps != nil {
		if len(caps) == 0 {
			p.Capabilities = nil
		} else {
			p.Capabilities = caps
		}
	}
	p.AdminEdited, p.Managed = true, true
}

// restated is a kept figure under a statement: absent keeps the row's, 0 removes it,
// and any other value replaces it.
func restated(stated, held *int) *int {
	switch {
	case stated == nil:
		return held
	case *stated == 0:
		return nil
	}
	return stated
}

// planRow is a row as the plane carries it. An empty status is active, as
// commerce reads it.
func planRow(p *commerceplan.Plan) client.PlanRow {
	r := client.PlanRow{
		Slug: p.Slug, Name: p.Name, Description: p.Description, Category: p.Category,
		Price: int64(p.Price), AnnualTotal: int64(p.AnnualTotal), Currency: string(p.Currency),
		Interval: string(p.Interval), PerSeat: p.PerSeat, ContactSales: p.ContactSales, Popular: p.Popular,
		Status: cmp.Or(p.Status, commerceplan.StatusActive), Features: p.Features, AdminEdited: p.AdminEdited,
	}
	if r.Features == nil {
		r.Features = []string{}
	}
	if p.Entitlements != nil {
		if raw, err := json.Marshal(p.Entitlements); err == nil {
			r.Entitlements = string(raw)
		}
	}
	r.Replaces = p.Replaces
	r.Multiplier = p.Multiplier
	r.UsagePolicy = p.UsagePolicy
	if len(p.Capabilities) > 0 {
		if raw, err := json.Marshal(p.Capabilities); err == nil {
			r.Capabilities = string(raw)
		}
	}
	if l := p.Limits; l != nil {
		c := client.PlanCapacity{Agents: l.Agents, Bots: l.Bots, TeamGuests: l.TeamGuests, MaxMembers: l.MaxMembers,
			MinSeats: l.MinSeats, Zones: l.Zones, RecordsPerZone: l.RecordsPerZone, QueriesPerDay: l.QueriesPerDay,
			SpendLimits: l.SpendLimits, SpendLimit: l.SpendLimit, BalanceMonths: l.BalanceMonths, BalanceCeiling: l.BalanceCeiling,
			InvoiceDays: l.InvoiceDays, GraceDays: l.GraceDays}
		if !reflect.ValueOf(c).IsZero() {
			r.Limits = &c
		}
	}
	if l := p.Licensing; l != nil {
		r.Licensing = &client.PlanLicensing{Products: l.Products, Apps: l.Apps, Features: l.Features, Seats: l.Seats, EarlyAccess: l.EarlyAccess}
	}
	if l := p.Limits; l != nil {
		r.ReloadsPerDay = l.ReloadsPerDay
		if l.Reload != nil {
			r.Reload = &client.PlanReload{ThresholdCents: l.Reload.ThresholdCents, AmountCents: l.Reload.AmountCents}
		}
	}
	return r
}
