package commerce

import (
	"context"
	"testing"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/commerce/api/promo"
	"github.com/hanzoai/commerce/models/plan"
	"github.com/hanzoai/commerce/models/types/currency"
	"github.com/hanzoai/commerce/util/test/ae"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
)

// mintOf mints one coupon as the platform and reads it back where commerce keeps it.
func mintOf(t *testing.T, ctx context.Context, o couponOps, in *client.CouponMint) *client.Coupon {
	t.Helper()
	got, err := o.mintCoupon(cloud.For(ctx, authz.AdminOrg), in)
	if err != nil {
		t.Fatalf("mint %+v: %v", in, err)
	}
	return got
}

// TestMintCarriesItsMonths: a mint states how many months its code discounts,
// and a mint that states none discounts the first one.
func TestMintCarriesItsMonths(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	adb := plan.AuthorityDB(ctx)
	row := plan.New(adb)
	row.Slug, row.Name, row.Price = "dev", "dev", currency.Cents(2000)
	if err := row.Create(); err != nil {
		t.Fatal(err)
	}
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	o := couponOps{trail: trail}

	for _, c := range []struct{ months, want int }{{3, 3}, {0, 1}, {36, 36}} {
		got := mintOf(t, ctx, o, &client.CouponMint{Prefix: "beta", Percent: 50, Months: c.months})
		if got.Months != c.want {
			t.Errorf("a mint for %d months stored %d, want %d", c.months, got.Months, c.want)
		}
		held, err := promo.ReadCoupon(ctx, got.Code)
		if err != nil || held.Months != c.want || held.Limit != 1 {
			t.Errorf("%s reads back as %+v, %v; want %d months, one use", got.Code, held, err, c.want)
		}
	}

	_, err = o.mintCoupon(cloud.For(ctx, authz.AdminOrg), &client.CouponMint{Prefix: "beta", Percent: 50, Months: 37})
	if err == nil {
		t.Fatal("a mint for 37 months was stored, want refused")
	}
}
