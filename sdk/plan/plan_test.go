package plan

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestPaidPredicate(t *testing.T) {
	if Paid(Tier{Category: "personal", Price: 0}) {
		t.Fatal("free tier must not count as paid")
	}
	if !Paid(Tier{Category: "team", Price: 2500}) {
		t.Fatal("priced team tier must count as paid")
	}
	if !Paid(Tier{Category: "enterprise", ContactSales: true}) {
		t.Fatal("contact-sales enterprise tier must count as paid")
	}
	if Paid(Tier{Category: "world", Price: 2000}) {
		t.Fatal("non-account product line must not clear cloud account paywall")
	}
}

func TestTokensFormatting(t *testing.T) {
	got := Tokens(Licence{
		Products: []string{"team", "engine"},
		Apps:     []string{"hanzo"},
		Features: []string{"inference", "inference"},
	})
	want := []string{"inference", "licensing.app:hanzo", "licensing.product:engine", "licensing.product:team"}
	if !slices.Equal(got, want) {
		t.Fatalf("Tokens = %v, want %v", got, want)
	}
}

func TestEntitlementsFailsClosedWhenUnmounted(t *testing.T) {
	RegisterEntitlements(nil)
	_, err := Entitlements(context.Background(), "pro")
	if !errors.Is(err, ErrUnmounted) {
		t.Fatalf("Entitlements err = %v, want ErrUnmounted", err)
	}
}
