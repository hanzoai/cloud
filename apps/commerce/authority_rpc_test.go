// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/authz"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	commerceplan "github.com/hanzoai/commerce/models/plan"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	"github.com/hanzoai/commerce/util/test/ae"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

// superAdmin is ctx as a SuperAdmin asks it over the plane.
func superAdmin(ctx context.Context) context.Context {
	return zip.WithCaller(ctx, zip.Caller{Org: authz.AdminOrg, User: "z", Admin: true})
}

// statusOf is the HTTP status a plane refusal carries, 0 for none.
func statusOf(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

func ptr(n int) *int { return &n }

// A SuperAdmin adds a plan as a draft, and the row is the plan authority's: found by
// its slug (what checkout resolves a planId by), not on sale, and the boot seed's to
// leave alone. Restated active it is on sale; a second add of the slug is 409 and a
// restatement of a slug no row holds is 404.
func TestASuperAdminAddsAndRestatesAPlan(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	super := superAdmin(ctx)

	set := &client.PlanSet{Slug: "go", Name: "Go", Description: "Every model.", Category: "personal", Price: 800,
		Interval: "month", Status: "draft", Features: []string{"Every model"},
		Limits: &client.PlanCapacity{Agents: ptr(10), Bots: ptr(0), TeamGuests: ptr(3)}}
	row, err := planeCreate(super, set)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if row.Price != 800 || row.AnnualTotal != 0 || row.Currency != "usd" || row.Status != "draft" || !row.AdminEdited ||
		row.Limits == nil || *row.Limits.Agents != 10 || *row.Limits.Bots != 0 || !slices.Equal(row.Features, set.Features) {
		t.Fatalf("added %+v", row)
	}
	held := commerceplan.New(commerceplan.AuthorityDB(ctx))
	if err := held.GetById("go"); err != nil {
		t.Fatalf("the row is not found by its slug: %v", err)
	}
	if held.Listed() || held.PriceAnnual != 0 || !held.AdminEdited {
		t.Fatalf("stored listed=%v priceAnnual=%d adminEdited=%v, want a draft with no year", held.Listed(), held.PriceAnnual, held.AdminEdited)
	}
	if _, err := planeCreate(super, set); statusOf(err) != http.StatusConflict {
		t.Fatalf("a second add of go: %v, want 409", err)
	}

	set.Status = "active"
	set.Licensing = &client.PlanLicensing{EarlyAccess: true}
	if row, err = planeSet(super, set); err != nil || row.Status != "active" || row.Licensing == nil || !row.Licensing.EarlyAccess {
		t.Fatalf("restate: %+v, %v", row, err)
	}
	held = commerceplan.New(commerceplan.AuthorityDB(ctx))
	if err := held.GetById("go"); err != nil || !held.Listed() {
		t.Fatalf("restated active, the row is listed=%v (%v)", held.Listed(), err)
	}

	missing := *set
	missing.Slug = "no-such-plan"
	if _, err := planeSet(super, &missing); statusOf(err) != http.StatusNotFound {
		t.Fatalf("restating a slug no row holds: %v, want 404", err)
	}

	rows, err := planeRows(super, &cloud.Unit{})
	if err != nil || len(rows.Rows) != 1 || rows.Rows[0].Slug != "go" {
		t.Fatalf("rows %+v, %v", rows, err)
	}
}

// A restatement replaces what it carries and keeps the rest: the credit a plan
// grants is never moved by a plan edit, an order's spend limits and starting balance
// stay the row's, and the year is the annual total over twelve.
func TestARestatementKeepsTheCreditAPlanGrants(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	row := commerceplan.New(commerceplan.AuthorityDB(ctx))
	row.Slug, row.Name, row.Category, row.Price, row.PerSeat = "team", "Team", "team", 2500, true
	row.Limits = &commerceplan.Limits{IncludedCloudCreditsPerUser: ptr(100), MinSeats: ptr(2),
		SpendLimits: []int{20, 50}, SpendLimit: ptr(50), BalanceMonths: ptr(1), BalanceCeiling: ptr(50000)}
	if err := row.Create(); err != nil {
		t.Fatal(err)
	}
	got, err := planeSet(superAdmin(ctx), &client.PlanSet{Slug: "team", Name: "Team", Category: "team", Price: 2500, AnnualTotal: 24000,
		Interval: "month", Status: "active", Limits: &client.PlanCapacity{MinSeats: ptr(2), MaxMembers: ptr(100)}})
	if err != nil || !got.PerSeat {
		t.Fatalf("restate: %+v, %v", got, err)
	}
	held := commerceplan.New(commerceplan.AuthorityDB(ctx))
	if err := held.GetById("team"); err != nil {
		t.Fatal(err)
	}
	if held.Limits == nil || held.Limits.IncludedCloudCreditsPerUser == nil || *held.Limits.IncludedCloudCreditsPerUser != 100 ||
		*held.Limits.MaxMembers != 100 || held.PriceAnnual != 2000 || held.AnnualTotal != 24000 || !held.PerSeat {
		t.Fatalf("stored limits %+v priceAnnual %d annualTotal %d perSeat %v", held.Limits, held.PriceAnnual, held.AnnualTotal, held.PerSeat)
	}
	// An order's spend limits and starting balance are the row's, kept by a statement
	// that does not carry them.
	if l := held.Limits; !slices.Equal(l.SpendLimits, []int{20, 50}) || l.SpendLimit == nil || *l.SpendLimit != 50 ||
		l.BalanceMonths == nil || *l.BalanceMonths != 1 || l.BalanceCeiling == nil || *l.BalanceCeiling != 50000 {
		t.Fatalf("stored limits %+v, want the order's spend limits and starting balance kept", l)
	}
}

// A plan's reload bound and the reload buying it switches on are stated by the
// write, kept by every restatement that does not carry them — an archive, a price
// edit — and removed only by a reload of amount 0.
func TestAPlansReloadSurvivesARestatement(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	super := superAdmin(ctx)
	set := &client.PlanSet{Slug: "ultra", Name: "Ultra", Category: "personal", Price: 19500, Interval: "month", Status: "draft",
		ReloadsPerDay: ptr(0), Reload: &client.PlanReload{ThresholdCents: 1000, AmountCents: 5000}}
	row, err := planeCreate(super, set)
	if err != nil || row.ReloadsPerDay == nil || *row.ReloadsPerDay != 0 || row.Reload == nil || *row.Reload != (client.PlanReload{ThresholdCents: 1000, AmountCents: 5000}) {
		t.Fatalf("added %+v, %v", row, err)
	}
	stored := func() *commerceplan.Plan {
		held := commerceplan.New(commerceplan.AuthorityDB(ctx))
		if err := held.GetById("ultra"); err != nil {
			t.Fatal(err)
		}
		return held
	}
	if l := stored().Limits; l == nil || l.ReloadsPerDay == nil || *l.ReloadsPerDay != 0 || l.Reload == nil || l.Reload.AmountCents != 5000 {
		t.Fatalf("stored limits %+v, want no daily limit and a $50 reload below $10", l)
	}

	for name, edit := range map[string]client.PlanSet{
		"an archive":   {Slug: "ultra", Name: "Ultra", Category: "personal", Price: 19500, Interval: "month", Status: "archived"},
		"a price edit": {Slug: "ultra", Name: "Ultra", Category: "personal", Price: 18500, Interval: "month", Status: "draft"},
	} {
		if row, err := planeSet(super, &edit); err != nil || row.ReloadsPerDay == nil || *row.ReloadsPerDay != 0 || row.Reload == nil || row.Reload.AmountCents != 5000 {
			t.Fatalf("after %s the row is %+v, %v; want its reload kept", name, row, err)
		}
	}
	gone := client.PlanSet{Slug: "ultra", Name: "Ultra", Category: "personal", Price: 19500, Interval: "month", Status: "draft",
		Reload: &client.PlanReload{}}
	if row, err := planeSet(super, &gone); err != nil || row.Reload != nil || row.ReloadsPerDay == nil {
		t.Fatalf("a reload of amount 0 left %+v, %v; want the reload removed and the bound kept", row, err)
	}
	if l := stored().Limits; l == nil || l.Reload != nil {
		t.Fatalf("stored limits %+v, want no reload", l)
	}
}

// A personal plan is sold monthly: the write refuses an annual total or a yearly
// interval on one, in any case or spacing of its category, while a business plan
// keeps its year.
func TestAPersonalPlanIsWrittenMonthly(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	super := superAdmin(ctx)
	for name, set := range map[string]client.PlanSet{
		"an annual total": {Slug: "go", Name: "Go", Category: "personal", Price: 800, AnnualTotal: 8000, Interval: "month", Status: "draft"},
		"a yearly period": {Slug: "go", Name: "Go", Category: "personal", Price: 8000, Interval: "year", Status: "draft"},
		"Personal, cased": {Slug: "go", Name: "Go", Category: " Personal ", Price: 800, AnnualTotal: 8000, Interval: "month", Status: "draft"},
	} {
		if _, err := planeCreate(super, &set); statusOf(err) != http.StatusBadRequest {
			t.Errorf("%s on a personal plan: %v, want 400", name, err)
		}
	}
	row, err := planeCreate(super, &client.PlanSet{Slug: "crew", Name: "Crew", Category: "Team", Price: 2500, AnnualTotal: 24000, Interval: "month", Status: "draft"})
	if err != nil || row.AnnualTotal != 24000 || row.Category != "team" {
		t.Fatalf("a team plan with a year: %+v, %v; want it kept, its category team", row, err)
	}
}

// A plan that states no annual price is not sold by the year: a card purchase of a
// monthly-only row with interval year is refused 400 before any card is touched,
// never billed at a year the row does not state.
func TestAMonthlyOnlyPlanIsNotSoldByTheYear(t *testing.T) {
	authorityApp(t)
	if _, err := planeCreate(superAdmin(context.Background()), &client.PlanSet{Slug: "crew", Name: "Crew", Category: "team",
		Price: 800, Interval: "month", Status: "active"}); err != nil {
		t.Fatalf("add crew: %v", err)
	}
	commerceorg.Invalidate("hanzo")
	buyer := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: "sam", Name: "sam"})
	_, err := planeSubscribe(buyer, &client.SaleIn{
		SourceID: "cnon:card-nonce-ok", PlanID: "crew", Interval: "year", Subject: principalWallet("hanzo", "sam"),
		Currency: "usd", IdempotencyKey: fresh("crew"),
	})
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusBadRequest || !strings.Contains(he.Error(), "not sold annually") {
		t.Fatalf("a year of crew answered %v, want 400 not sold annually", err)
	}
}

// A slug that would key another plan's switches is refused.
func TestASlugThatKeysAnotherPlansSwitchesIsRefused(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	for _, slug := range []string{"window-go", "hours-go"} {
		if _, err := planeCreate(superAdmin(ctx), &client.PlanSet{Slug: slug, Name: "X", Category: "personal", Price: 800, Interval: "month", Status: "draft"}); statusOf(err) != http.StatusBadRequest {
			t.Errorf("%s: %v, want 400", slug, err)
		}
	}
}

// Only a SuperAdmin writes a plan; the platform reads the rows and a tenant reads
// nothing. A statement the row cannot be stored as is 400.
func TestOnlyASuperAdminWritesAPlan(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	ok := &client.PlanSet{Slug: "go", Name: "Go", Category: "personal", Price: 800, Interval: "month", Status: "draft"}
	tenant := zip.WithCaller(ctx, zip.Caller{Org: "acme", User: "wile", OrgAdmin: true})
	platform := cloud.For(ctx, principal.Reserved)
	for name, c := range map[string]context.Context{"a tenant's owner": tenant, "the platform": platform} {
		if _, err := planeCreate(c, ok); statusOf(err) != http.StatusForbidden {
			t.Errorf("%s added a plan: %v, want 403", name, err)
		}
		if _, err := planeSet(c, ok); statusOf(err) != http.StatusForbidden {
			t.Errorf("%s restated a plan: %v, want 403", name, err)
		}
	}
	if _, err := planeRows(tenant, &cloud.Unit{}); statusOf(err) != http.StatusForbidden {
		t.Errorf("a tenant read the rows: %v, want 403", err)
	}
	if _, err := planeRows(platform, &cloud.Unit{}); err != nil {
		t.Errorf("the platform read the rows: %v", err)
	}
	for name, bad := range map[string]client.PlanSet{
		"a slug with a space": {Slug: "go now", Name: "Go", Category: "personal", Interval: "month", Status: "draft"},
		"no name":             {Slug: "go", Category: "personal", Interval: "month", Status: "draft"},
		"no category":         {Slug: "go", Name: "Go", Interval: "month", Status: "draft"},
		"a weekly interval":   {Slug: "go", Name: "Go", Category: "personal", Interval: "week", Status: "draft"},
		"an unknown status":   {Slug: "go", Name: "Go", Category: "personal", Interval: "month", Status: "gone"},
		"a negative price":    {Slug: "go", Name: "Go", Category: "personal", Interval: "month", Status: "draft", Price: -1},
	} {
		if _, err := planeCreate(superAdmin(ctx), &bad); statusOf(err) != http.StatusBadRequest {
			t.Errorf("%s: %v, want 400", name, err)
		}
	}
}

// Archiving a seeded rung takes it out of the catalog GET /v1/billing/plans serves,
// and the shipped catalog does not stand in for it; the row still resolves by slug,
// so its subscribers' renewals still price.
func TestAnArchivedRungLeavesTheCatalog(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	if _, _, err := commercebilling.SeedPlans(ctx); err != nil {
		t.Fatalf("SeedPlans: %v", err)
	}
	served := func() []string {
		views, err := commercebilling.ReadPlans(ctx, "hanzo.ai", "personal", nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, v := range views {
			out = append(out, v.Slug)
		}
		return out
	}
	if !slices.Contains(served(), "go") {
		t.Fatalf("the seeded catalog serves %v, want go among it", served())
	}
	if _, err := planeSet(superAdmin(ctx), &client.PlanSet{Slug: "go", Name: "Go", Category: "personal", Price: 800,
		Interval: "month", Status: "archived"}); err != nil {
		t.Fatalf("archive go: %v", err)
	}
	if got := served(); slices.Contains(got, "go") || !slices.Contains(got, "pro-3x") {
		t.Fatalf("after archiving go the catalog serves %v, want pro-3x and not go", got)
	}
	held := commerceplan.New(commerceplan.AuthorityDB(ctx))
	if err := held.GetById("go"); err != nil || held.Price != 800 {
		t.Fatalf("an archived go resolves as %+v (%v), want its row", held, err)
	}
}

// The plan authority has one door for writes, the admin app's typed ops: commerce's
// HTTP plan writes are not mounted, even for a SuperAdmin, while its raw-row read is.
func TestThePlanAuthorityHasNoHTTPWrite(t *testing.T) {
	app, _, sign := authorityApp(t)
	super := sign("z", authz.Membership{Org: "admin", Role: authz.Owner})
	for _, w := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/commerce/plans/entries", `{"slug":"probe","name":"Probe"}`},
		{http.MethodPut, "/v1/commerce/plans/entries/dev", `{"price":1}`},
		{http.MethodDelete, "/v1/commerce/plans/entries/dev", ""},
		{http.MethodPost, "/v1/commerce/plans/seed", ""},
	} {
		if code, body := drive(t, app, w.method, w.path, super, w.body, nil); code < 400 || code == http.StatusForbidden {
			t.Errorf("%s %s as SuperAdmin: %d %s, want no such route", w.method, w.path, code, body)
		}
	}
	if code, body := drive(t, app, http.MethodGet, "/v1/commerce/plans/entries", super, "", nil); code != http.StatusOK {
		t.Errorf("GET /v1/commerce/plans/entries as SuperAdmin: %d %s, want 200", code, body)
	}
}

// Every paid rung the shipped catalog sells, in a category whose plans include usage,
// includes its own: a seed, so the boot seed can never put a rung on sale whose payers
// the limiter reads as on no plan. A plan added later is held to the same rule by the
// admin op, against the usage policy.
func TestEveryRungTheSeedSellsIncludesItsUsage(t *testing.T) {
	rows := commercebilling.SeedRows()
	covered := map[string]bool{}
	for _, r := range rows {
		if _, ok := flags.CarriedPlans[r.Slug]; ok {
			covered[r.Category] = true
		}
	}
	for _, r := range rows {
		if !r.Listed() || r.Price <= 0 || r.ContactSales || !covered[r.Category] {
			continue
		}
		if _, ok := flags.CarriedPlans[r.Slug]; !ok {
			t.Errorf("the seed sells %q (%s, %d¢) with no usage seed: its payers would be read as on no plan", r.Slug, r.Category, r.Price)
		}
	}
	if len(covered) == 0 {
		t.Fatal("no seeded rung is in the shipped catalog, so nothing was checked")
	}
}

// What a plan grants and the slugs it replaces are the row's, stated by a
// SuperAdmin: an entitlement block is stored as stated and read back, a
// restatement that carries none keeps the row's, "{}" states that the plan grants
// nothing, and a block that is not an object of namespaced keys is refused. A
// boot after the edit leaves every one of them as stated.
func TestASuperAdminStatesWhatAPlanGrants(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	super := superAdmin(ctx)

	set := &client.PlanSet{Slug: "studio", Name: "Studio", Category: "personal", Price: 4000, Interval: "month", Status: "draft",
		Entitlements: `{"ai.priority_inference":true,"team.guests":7,"ai.models":["tier:max"]}`, Replaces: []string{"Atelier "}}
	row, err := planeCreate(super, set)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(row.Entitlements), &got); err != nil || got["team.guests"] != float64(7) || got["ai.priority_inference"] != true {
		t.Fatalf("added entitlements %q (%v)", row.Entitlements, err)
	}
	if !slices.Equal(row.Replaces, []string{"atelier"}) {
		t.Fatalf("added replaces %v, want the slug as a slug", row.Replaces)
	}

	// A restatement that states neither keeps both; the price moves.
	if row, err = planeSet(super, &client.PlanSet{Slug: "studio", Name: "Studio", Category: "personal", Price: 4500, Interval: "month", Status: "draft"}); err != nil {
		t.Fatalf("restate: %v", err)
	}
	if row.Price != 4500 || !strings.Contains(row.Entitlements, `"team.guests":7`) || !slices.Equal(row.Replaces, []string{"atelier"}) {
		t.Fatalf("a restatement that states no entitlements left %+v", row)
	}

	// The boot seed over a store that holds rows writes none of it back.
	if _, _, err := commercebilling.SeedPlans(ctx); err != nil {
		t.Fatalf("boot: %v", err)
	}
	rows, err := planeRows(super, &cloud.Unit{})
	if err != nil || len(rows.Rows) != 1 || rows.Rows[0].Price != 4500 || !strings.Contains(rows.Rows[0].Entitlements, `"team.guests":7`) {
		t.Fatalf("after a boot the authority holds %+v (%v), want the one row as stated", rows, err)
	}

	// "{}" grants nothing, and is recorded as that.
	if row, err = planeSet(super, &client.PlanSet{Slug: "studio", Name: "Studio", Category: "personal", Price: 4500, Interval: "month",
		Status: "draft", Entitlements: "{}", Replaces: []string{}}); err != nil || row.Entitlements != "{}" || len(row.Replaces) != 0 {
		t.Fatalf("stating that the plan grants nothing: %+v, %v", row, err)
	}

	for name, bad := range map[string]*client.PlanSet{
		"an array":            {Entitlements: `["ai.agents"]`},
		"not JSON":            {Entitlements: `ai.agents=1`},
		"a key with no name":  {Entitlements: `{"agents":1}`},
		"its own slug":        {Replaces: []string{"studio"}},
		"a slug twice":        {Replaces: []string{"old", "old"}},
		"not a slug":          {Replaces: []string{"Old Plan!"}},
		"seventeen old slugs": {Replaces: strings.Split("a,b,c,d,e,f,g,h,i,j,k,l,m,n,o,p,q", ",")},
	} {
		bad.Slug, bad.Name, bad.Category, bad.Interval, bad.Status = "studio", "Studio", "personal", "month", "draft"
		if _, err := planeSet(super, bad); statusOf(err) != http.StatusBadRequest {
			t.Errorf("%s: %v, want 400", name, err)
		}
	}
}
