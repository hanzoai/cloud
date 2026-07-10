package checkout

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

func cents(v int64) *currency.Cents { c := currency.Cents(v); return &c }
func strp(s string) *string        { return &s }
func boolp(b bool) *bool           { return &b }

func listings() store.Listings {
	return store.Listings{
		"tshirt": store.Listing{
			ProductId: "prod_tshirt",
			Slug:      "classic-tee",
			SKU:       "TEE-BLK-L",
			Name:      strp("Classic Tee"),
			Price:     cents(2500),
		},
		"hidden": store.Listing{
			Slug:   "secret",
			Name:   strp("Secret"),
			Price:  cents(9900),
			Hidden: boolp(true),
		},
		"nopricelisting": store.Listing{
			Slug: "coming-soon",
			Name: strp("Coming Soon"),
		},
	}
}

func TestCatalogPrice_ByProductId(t *testing.T) {
	name, amt, ok := catalogPrice(listings(), checkoutSessionItem{ProductId: "prod_tshirt", Quantity: 1})
	if !ok || amt != 2500 || name != "Classic Tee" {
		t.Fatalf("productId lookup: got (%q,%d,%v), want (Classic Tee,2500,true)", name, amt, ok)
	}
}

func TestCatalogPrice_BySlugAndSku(t *testing.T) {
	if _, amt, ok := catalogPrice(listings(), checkoutSessionItem{ProductSlug: "classic-tee"}); !ok || amt != 2500 {
		t.Fatalf("slug lookup failed: %d %v", amt, ok)
	}
	if _, amt, ok := catalogPrice(listings(), checkoutSessionItem{VariantSku: "TEE-BLK-L"}); !ok || amt != 2500 {
		t.Fatalf("sku lookup failed: %d %v", amt, ok)
	}
}

func TestCatalogPrice_ServerAuthoritative_IgnoresClientUnitPrice(t *testing.T) {
	// A shopper who submits a bogus UnitPrice still gets the real listing price.
	_, amt, ok := catalogPrice(listings(), checkoutSessionItem{ProductId: "prod_tshirt", UnitPrice: 0.01})
	if !ok || amt != 2500 {
		t.Fatalf("client UnitPrice must be ignored: got %d", amt)
	}
}

func TestCatalogPrice_HiddenRejected(t *testing.T) {
	if _, _, ok := catalogPrice(listings(), checkoutSessionItem{ProductSlug: "secret"}); ok {
		t.Fatal("hidden listing must not be sellable")
	}
}

func TestCatalogPrice_NoPriceRejected(t *testing.T) {
	if _, _, ok := catalogPrice(listings(), checkoutSessionItem{ProductSlug: "coming-soon"}); ok {
		t.Fatal("listing without a price must not be sellable")
	}
}

func TestCatalogPrice_UnknownRejected(t *testing.T) {
	if _, _, ok := catalogPrice(listings(), checkoutSessionItem{ProductSlug: "does-not-exist"}); ok {
		t.Fatal("unknown catalog ref must be rejected (not silently mispriced)")
	}
	if _, _, ok := catalogPrice(nil, checkoutSessionItem{ProductId: "x"}); ok {
		t.Fatal("nil catalog must reject")
	}
}

func TestHasCatalogRef(t *testing.T) {
	if hasCatalogRef(checkoutSessionItem{Hat: checkoutSessionHat{Text: "hi"}}) {
		t.Fatal("pure hat item must not be a catalog ref (legacy pricing path)")
	}
	if !hasCatalogRef(checkoutSessionItem{ProductSlug: "classic-tee"}) {
		t.Fatal("slug item must be a catalog ref")
	}
}
