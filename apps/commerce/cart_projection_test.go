// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"testing"

	"github.com/hanzoai/commerce/datastore"
	cartmodel "github.com/hanzoai/commerce/models/cart"
	"github.com/hanzoai/commerce/models/lineitem"
	"github.com/hanzoai/commerce/models/types/currency"
	pcv "github.com/hanzoai/commerce/models/types/productcachedvalues"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
)

// The cart's WIRE PROJECTION, which every cart address answers through and none
// of which had a test.
//
// These are the functions that decide what a buyer is told they owe. They hold no
// store and reach nothing, so the money can be checked exactly — and money that
// is only checked through four handlers is money whose arithmetic nobody has
// stated.

// storedCart is a real cart from a real store, because the projection reads the
// model's own identity and timestamps and a bare struct has neither.
func storedCart(t *testing.T) *cartmodel.Cart {
	t.Helper()
	boot(t)
	ctx := context.Background()
	o, err := commerceorg.Resolve(ctx, "carttest")
	if err != nil {
		t.Fatalf("resolve org: %v", err)
	}
	return cartmodel.New(datastore.New(o.Namespaced(ctx)))
}

// TestEveryAmountReachesTheWireUnchanged is the claim worth making about a cart:
// the six amounts are carried, each to its OWN field, and none is derived here.
// Deriving one would put a second opinion about a total in the answer, and the
// stored cart is the one that priced it.
func TestEveryAmountReachesTheWireUnchanged(t *testing.T) {
	c := storedCart(t)
	c.Currency, c.LineTotal, c.Discount = currency.USD, 12000, 2000
	c.Subtotal, c.Shipping, c.Tax, c.Total = 10000, 599, 875, 11474

	got := cartAnswer(c)

	for _, want := range []struct {
		name string
		got  int64
		cent int64
	}{
		{"line total", got.LineTotalCents, 12000},
		{"discount", got.DiscountCents, 2000},
		{"subtotal", got.SubtotalCents, 10000},
		{"shipping", got.ShippingCents, 599},
		{"tax", got.TaxCents, 875},
		{"total", got.TotalCents, 11474},
	} {
		if want.got != want.cent {
			t.Errorf("%s = %d cents, want %d", want.name, want.got, want.cent)
		}
	}
	if got.Currency != "usd" {
		t.Errorf("currency = %q, want the stored one", got.Currency)
	}
}

// TestADiscountIsCarriedNotSubtracted guards the arithmetic this projection must
// NOT do. A cart whose stored total already accounts for its discount would be
// discounted twice if this subtracted again, and the buyer would be quoted less
// than the cart charges.
func TestADiscountIsCarriedNotSubtracted(t *testing.T) {
	c := storedCart(t)
	c.LineTotal, c.Discount, c.Subtotal, c.Total = 5000, 1000, 4000, 4000

	got := cartAnswer(c)

	if got.TotalCents != 4000 {
		t.Errorf("total = %d, want the stored 4000 — this projection prices nothing", got.TotalCents)
	}
	if got.DiscountCents != 1000 {
		t.Errorf("discount = %d, want it carried as its own field", got.DiscountCents)
	}
}

// TestAnEmptyCartIsAnEmptyListAndNotNull pins the JSON a client parses. A null
// items field is a different shape from an empty one, and a buyer's cart is
// legitimately empty all the time.
func TestAnEmptyCartIsAnEmptyListAndNotNull(t *testing.T) {
	got := cartAnswer(storedCart(t))

	if got.Items == nil {
		t.Error("an empty cart must project [] and not null")
	}
	if len(got.Items) != 0 {
		t.Errorf("items = %v, want none", got.Items)
	}
}

// TestATimestampIsAbsentRatherThanTheZeroInstant guards a cart that was never
// written: the zero time formatted as RFC 3339 is the year one, which reads as a
// real date to anything that parses it.
func TestATimestampIsAbsentRatherThanTheZeroInstant(t *testing.T) {
	got := cartAnswer(storedCart(t))

	if got.CreatedAt != "" || got.UpdatedAt != "" {
		t.Errorf("unset timestamps projected as %q / %q, want empty", got.CreatedAt, got.UpdatedAt)
	}
}

// TestAVariantLineAnswersAsTheVariant is the identity rule a caller depends on:
// what comes back is what setCartItem accepts, so a line that IS a variant must
// say so and carry the variant's name.
func TestAVariantLineAnswersAsTheVariant(t *testing.T) {
	got := cartLine(lineitem.LineItem{
		ProductName:         "Notebook",
		VariantId:           "var_1",
		VariantName:         "Notebook, A5",
		Quantity:            2,
		ProductCachedValues: pcv.ProductCachedValues{Price: 1500},
	})

	if got.Kind != "variant" {
		t.Errorf("kind = %q, want variant", got.Kind)
	}
	if got.Name != "Notebook, A5" {
		t.Errorf("name = %q, want the variant's", got.Name)
	}
	if got.Quantity != 2 || got.PriceCents != 1500 {
		t.Errorf("line = %d @ %d, want 2 @ 1500", got.Quantity, got.PriceCents)
	}
}

// TestAVariantWithNoNameKeepsTheProductsName is the fallback. A variant row that
// never got a label must not answer with an empty name — the buyer would see a
// blank line in their cart.
func TestAVariantWithNoNameKeepsTheProductsName(t *testing.T) {
	got := cartLine(lineitem.LineItem{ProductName: "Notebook", VariantId: "var_1", Quantity: 1})

	if got.Kind != "variant" {
		t.Errorf("kind = %q, want variant — the id is what makes it one", got.Kind)
	}
	if got.Name != "Notebook" {
		t.Errorf("name = %q, want the product's as the fallback", got.Name)
	}
}

// TestALineWithNoVariantIsAProduct is the other half of that rule.
func TestALineWithNoVariantIsAProduct(t *testing.T) {
	got := cartLine(lineitem.LineItem{ProductName: "Notebook", Quantity: 1, ProductCachedValues: pcv.ProductCachedValues{Price: 900}})

	if got.Kind != "product" {
		t.Errorf("kind = %q, want product", got.Kind)
	}
	if got.Name != "Notebook" {
		t.Errorf("name = %q, want the product's", got.Name)
	}
}

// TestAFreeLineSaysSo pins the flag that decides whether a line is charged for.
// Dropping it would bill a buyer for something the cart gave them.
func TestAFreeLineSaysSo(t *testing.T) {
	if got := cartLine(lineitem.LineItem{ProductName: "Sticker", Quantity: 1, Free: true}); !got.Free {
		t.Error("a free line must project as free")
	}
	if got := cartLine(lineitem.LineItem{ProductName: "Sticker", Quantity: 1, ProductCachedValues: pcv.ProductCachedValues{Price: 300}}); got.Free {
		t.Error("a priced line must not project as free")
	}
}

// TestEveryLineIsProjected guards the loop: a cart answers with all of its lines,
// in the order it holds them, because a buyer checks the list against what they
// added.
func TestEveryLineIsProjected(t *testing.T) {
	c := storedCart(t)
	c.Items = []lineitem.LineItem{
		{ProductName: "A", Quantity: 1, ProductCachedValues: pcv.ProductCachedValues{Price: 100}},
		{ProductName: "B", Quantity: 2, ProductCachedValues: pcv.ProductCachedValues{Price: 200}},
		{ProductName: "C", Quantity: 3, ProductCachedValues: pcv.ProductCachedValues{Price: 300}},
	}

	got := cartAnswer(c)

	if len(got.Items) != 3 {
		t.Fatalf("projected %d lines, want 3", len(got.Items))
	}
	for i, want := range []string{"A", "B", "C"} {
		if got.Items[i].Name != want {
			t.Errorf("line %d = %q, want %q — the order a buyer added them in", i, got.Items[i].Name, want)
		}
	}
}

// TestACartWithNoCurrencyIsDollars pins the default, and that it is applied to
// what a caller SENT rather than to what is stored: an amount with no currency is
// not a neutral amount, and picking one silently later is how a cart gets charged
// in the wrong denomination.
func TestACartWithNoCurrencyIsDollars(t *testing.T) {
	for _, sent := range []string{"", "   ", "\t"} {
		if got := normalizeCartCurrency(sent); got != currency.USD {
			t.Errorf("normalizeCartCurrency(%q) = %q, want the default", sent, got)
		}
	}
}

// TestACurrencyIsReadCaseInsensitively is the shape every ISO code arrives in
// from a form or a header. "USD" and "usd" are one currency, and treating them as
// two would split a store's carts across denominations that do not differ.
func TestACurrencyIsReadCaseInsensitively(t *testing.T) {
	for _, sent := range []string{"EUR", "eur", "Eur", "  EUR  "} {
		if got := normalizeCartCurrency(sent); string(got) != "eur" {
			t.Errorf("normalizeCartCurrency(%q) = %q, want eur", sent, got)
		}
	}
}
