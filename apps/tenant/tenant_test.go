package tenant

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// TestMintRefusesEveryUnkeyableInput enumerates the whole refusal surface. Each
// case is a way two tenants could end up sharing one key, which is the one
// failure this type exists to prevent.
func TestMintRefusesEveryUnkeyableInput(t *testing.T) {
	for _, tc := range []struct{ name, brand, org string }{
		{"no brand", "", "acme"},
		{"blank brand", "   ", "acme"},
		{"brand carries the separator", "hanzo/zoo", "acme"},
		{"no org", "hanzo", ""},
		{"blank org", "hanzo", "  "},
		{"the anonymous lane", "hanzo", Public},
		{"org carries the separator", "hanzo", "lux/acme"},
		{"org is only a separator", "hanzo", "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, err := Mint(tc.brand, tc.org)
			if err == nil {
				t.Fatalf("Mint(%q, %q) minted %q; it must refuse", tc.brand, tc.org, k.String())
			}
			if !k.Zero() {
				t.Fatalf("a refused Mint returned a non-zero key %q", k.String())
			}
		})
	}
}

func TestMintQualifies(t *testing.T) {
	k, err := Mint(" hanzo ", " acme ")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if got, want := k.String(), "hanzo/acme"; got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
	if got, want := k.Brand(), "hanzo"; got != want {
		t.Fatalf("brand = %q, want %q", got, want)
	}
	if got, want := k.Org(), "acme"; got != want {
		t.Fatalf("org = %q, want %q", got, want)
	}
	if k.Zero() {
		t.Fatal("a minted key reports itself zero")
	}
}

// TestTwoBrandsOneOrgAreTwoTenants is the collision the qualification exists to
// prevent, asserted directly.
func TestTwoBrandsOneOrgAreTwoTenants(t *testing.T) {
	a, err := Mint("hanzo", "acme")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Mint("zoo", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("hanzo/acme and zoo/acme minted the same key %q", a.String())
	}
	if !Qualified("hanzo", a) || Qualified("hanzo", b) {
		t.Fatal("Qualified does not hold a key to the brand that minted it")
	}
}

// TestKeyCannotArriveOffTheWire is the structural control, not a convention: a
// Key has no exported field, so encoding/json can neither populate one nor be
// tricked into producing a non-zero one. If someone adds an exported field this
// test fails, which is the point.
func TestKeyCannotArriveOffTheWire(t *testing.T) {
	rt := reflect.TypeOf(Key{})
	for i := range rt.NumField() {
		if rt.Field(i).IsExported() {
			t.Fatalf("Key.%s is exported — a Key can now be decoded from a request body", rt.Field(i).Name)
		}
	}
	var k Key
	if err := json.Unmarshal([]byte(`{"s":"hanzo/evil","S":"hanzo/evil"}`), &k); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !k.Zero() {
		t.Fatalf("a Key was decoded from a request body: %q", k.String())
	}
}

// TestOfFailsClosedWithNoPrincipal is the off-HTTP path: no validated principal,
// no key, no read.
func TestOfFailsClosedWithNoPrincipal(t *testing.T) {
	k, err := Of(context.Background(), "hanzo")
	if err == nil {
		t.Fatalf("Of minted %q with no validated principal", k.String())
	}
	if !k.Zero() {
		t.Fatal("a refused Of returned a non-zero key")
	}
}

// TestQualifiedIsDerivedFromMint proves the validator cannot drift from the mint:
// every key Mint produces passes, and a hand-built string that Mint would have
// refused does not (it cannot even be constructed outside this package, which is
// why the loop below goes through Mint).
func TestQualifiedIsDerivedFromMint(t *testing.T) {
	for _, org := range []string{"acme", "a", "org-with-dashes", "ORG"} {
		k, err := Mint("hanzo", org)
		if err != nil {
			t.Fatalf("Mint(%q): %v", org, err)
		}
		if !Qualified("hanzo", k) {
			t.Fatalf("Mint produced %q and Qualified refuses it", k.String())
		}
	}
	if Qualified("hanzo", Key{}) {
		t.Fatal("the zero key qualifies")
	}
}
