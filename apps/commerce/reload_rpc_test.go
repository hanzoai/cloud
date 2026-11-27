// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"net/http"
	"testing"

	commerceorg "github.com/hanzoai/commerce/pkg/org"
	"github.com/hanzoai/commerce/util/test/ae"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
)

// A plan's reload is held to the bounds every reload rule is: an amount from $5 to
// $5,000 and a balance to reload below from $0 to $5,000; an amount of 0 removes it.
func TestAPlansReloadIsHeldToTheReloadBounds(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	super := superAdmin(ctx)
	plan := func(r *client.PlanReload) *client.PlanSet {
		return &client.PlanSet{Slug: "ultra", Name: "Ultra", Category: "personal", Price: 19500, Interval: "month", Status: "draft", Reload: r}
	}
	for name, r := range map[string]*client.PlanReload{
		"an amount under $5":      {ThresholdCents: 1000, AmountCents: 499},
		"an amount over $5,000":   {ThresholdCents: 1000, AmountCents: 500001},
		"$5,000 typed as cents":   {ThresholdCents: 1000, AmountCents: 50000000},
		"a threshold over $5,000": {ThresholdCents: 500001, AmountCents: 5000},
		"a negative threshold":    {ThresholdCents: -1, AmountCents: 5000},
	} {
		if _, err := planeCreate(super, plan(r)); statusOf(err) != http.StatusBadRequest {
			t.Errorf("%s: %v, want 400", name, err)
		}
	}
	if row, err := planeCreate(super, plan(&client.PlanReload{ThresholdCents: 1000, AmountCents: 5000})); err != nil || row.Reload == nil {
		t.Fatalf("a $50 reload below $10: %+v, %v", row, err)
	}
	if row, err := planeSet(super, plan(&client.PlanReload{})); err != nil || row.Reload != nil {
		t.Fatalf("a reload of amount 0: %+v, %v; want it removed", row, err)
	}
}

// An auto-reload rule is its payer's: a person's wallet in the signup org holds a
// rule of its own, read and written under that wallet, apart from the org account's;
// an account outside the org is refused.
func TestAReloadRuleIsItsPayers(t *testing.T) {
	authorityApp(t)
	commerceorg.Invalidate("hanzo")
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: "alice", Name: "alice"})
	wallet := principalWallet("hanzo", "alice")

	set, err := planeAutoRechargeSet(ctx, &client.AutoRechargeSetIn{Subject: wallet,
		Edit: client.AutoRechargeEdit{ThresholdCents: 1000, AmountCents: 5000}})
	if err != nil || !set.Stored || set.ThresholdCents != 1000 || set.AmountCents != 5000 {
		t.Fatalf("set the wallet's rule: %+v, %v", set, err)
	}
	got, err := planeAutoRecharge(ctx, &client.AutoRechargeIn{Subject: wallet})
	if err != nil || !got.Stored || got.AmountCents != 5000 {
		t.Fatalf("the wallet's rule reads %+v, %v; want the rule just set", got, err)
	}
	own, err := planeAutoRecharge(ctx, &client.AutoRechargeIn{})
	if err != nil || own.Stored {
		t.Fatalf("the org account's rule reads %+v, %v; want none stored", own, err)
	}
	if _, err := planeAutoRecharge(ctx, &client.AutoRechargeIn{Subject: "acme/bob"}); err == nil {
		t.Fatal("read a rule of an account outside the org, want an error")
	}
}
