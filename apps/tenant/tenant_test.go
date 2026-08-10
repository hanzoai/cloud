package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/brand"
)

// TestMintRefusesEveryUnkeyableInput enumerates the whole refusal surface. Each
// case is a way two tenants could end up sharing one key, which is the one
// failure this type exists to prevent.
func TestMintRefusesEveryUnkeyableInput(t *testing.T) {
	for _, tc := range []struct{ name, brand, org string }{
		{"no brand", "", "acme"},
		{"blank brand", "   ", "acme"},
		{"brand carries the separator", "hanzo/zoo", "acme"},
		{"a brand no registry vouches for", "notabrand", "acme"},
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
	rt := reflect.TypeFor[Key]()
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

// ── one mint, or a reader and a writer disagree about whose a row is ─────────

// writerMint is the mint used by the plane that WRITES the surfaces this key
// reads — apps/risk's `qualify`, reproduced here verbatim as an ORACLE.
//
// It is a copy on purpose. The two live in one binary but not in one package,
// and the property that matters is not "they share code" but "they produce the
// same bytes": hanzo.risk_feature.org is written with the writer's key and read
// with this one, so a single character of divergence means the dataset plane
// reads a tenant nobody ever wrote — an empty answer that looks exactly like a
// quiet customer. Pinning the writer's algorithm here makes that a test failure
// instead of a support ticket.
func writerMint(brandID, org string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(brandID))
	if id == "" || brand.For(id).ID != id {
		return "", fmt.Errorf("no brand is registered as %q", brandID)
	}
	org = strings.TrimSpace(org)
	switch {
	case org == "":
		return "", fmt.Errorf("no org")
	case org == Public:
		return "", fmt.Errorf("the anonymous lane is not an organisation")
	case strings.Contains(org, Sep):
		return "", fmt.Errorf("org contains the separator")
	}
	return id + Sep + org, nil
}

// TestTheMintAgreesWithTheWriterOfTheSourceTable, byte for byte, over every
// brand the registry carries and every spelling a deployment might be started
// with.
//
// CLOUD_BRAND=Hanzo is the case that broke it: the writer lower-cased and this
// mint only trimmed, so the rollup filed rows under `hanzo/acme` while the
// dataset plane asked for `Hanzo/acme` and got nothing, forever, with no error
// anywhere. An unregistered brand is the other half — the writer refuses it and
// the reader used to mint it happily.
func TestTheMintAgreesWithTheWriterOfTheSourceTable(t *testing.T) {
	orgs := []string{"acme", "a", "ORG", "org-with-dashes", "", Public, "lux/acme", " padded "}
	spellings := []string{"hanzo", "Hanzo", "HANZO", " hanzo ", "lux", "Lux", "zoo", "pars", "bootnode", "", "notabrand", "hanzo/zoo"}
	var agreed int
	for _, b := range spellings {
		for _, org := range orgs {
			want, wantErr := writerMint(b, org)
			got, gotErr := Mint(b, org)
			if (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("Mint(%q, %q): the writer %v and the reader %v disagree about whether this is a tenant at all",
					b, org, wantErr, gotErr)
			}
			if wantErr != nil {
				continue
			}
			if got.String() != want {
				t.Fatalf("Mint(%q, %q) = %q and the writer of hanzo.risk_feature writes %q — the reader would read a tenant nobody wrote",
					b, org, got.String(), want)
			}
			agreed++
		}
	}
	if agreed < 20 {
		t.Fatalf("only %d keys were compared; the table no longer covers the surface", agreed)
	}
}

// TestTheBrandHalfIsCanonical states the same property from the other side: two
// spellings of one brand are ONE tenant space, not two.
func TestTheBrandHalfIsCanonical(t *testing.T) {
	a, err := Mint("Hanzo", "acme")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	b, err := Mint("hanzo", "acme")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if a != b {
		t.Fatalf("Hanzo/acme minted %q and hanzo/acme minted %q — one business, two key spaces", a.String(), b.String())
	}
	if a.String() != "hanzo/acme" {
		t.Fatalf("the canonical key is %q, want %q", a.String(), "hanzo/acme")
	}
	if !Qualified("Hanzo", a) {
		t.Fatal("Qualified does not canonicalise the brand it is handed")
	}
}

// TestNoRegisteredBrandCarriesTheSeparator. The brand half is a single label
// because the registry's ids are, which is why the mint checks membership rather
// than shape. If a brand id ever contains the separator, `<brand>/<org>` stops
// reading back to the org it names — so the registry is asserted, not assumed.
func TestNoRegisteredBrandCarriesTheSeparator(t *testing.T) {
	for _, id := range []string{"hanzo", "lux", "zoo", "pars", "bootnode"} {
		if !brand.Registered(id) {
			t.Fatalf("%q is no longer a registered brand; this test's table is stale", id)
		}
		if strings.Contains(id, Sep) {
			t.Fatalf("brand %q carries the tenant separator", id)
		}
	}
}

// TestVouchesIsTheBootCheckAndIsDerivedFromTheMint. A deployment whose brand
// cannot mint a key would 403 every request; Mount asks this once instead, and it
// must answer for exactly the brands the mint accepts — a second opinion at boot
// is a second definition of "a brand that vouches".
func TestVouchesIsTheBootCheckAndIsDerivedFromTheMint(t *testing.T) {
	for _, id := range []string{"hanzo", "Hanzo", "lux", "zoo", "pars", "bootnode", "", "  ", "notabrand", "hanzo/zoo"} {
		_, mintErr := Mint(id, "acme")
		vouchErr := Vouches(id)
		if (mintErr == nil) != (vouchErr == nil) {
			t.Fatalf("brand %q: Mint says %v and Vouches says %v", id, mintErr, vouchErr)
		}
	}
}

// The cross-brand refusal in [Of] is proved end to end, through the real request
// boundary that mints the vouching brand, in apps/dataset —
// TestATokenFromAnotherBrandIsNotThisBrandsTenant. Reaching it here would mean
// writing the context values by hand, which is the one thing the unexported
// context keys exist to prevent.

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
