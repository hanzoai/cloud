package tenant

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// Sep divides the key's two halves. Brand ids are a closed set and none contains
// it, so the first one always ends the brand and the key reads back to the
// organisation it names.
const Sep = "/"

// Public is the reserved org the event endpoint files CREDENTIAL-LESS writes under
// (apps/event/event.go). It is not a customer: an unauthenticated stranger
// writes into it, so a read that admitted it would let that stranger move a real
// tenant's statistics, and a dataset built over it would be a dataset of
// whatever the internet sent. It is refused at the mint, which every read passes.
const Public = "$public"

// Key is a minted, qualified tenant key.
//
// The zero Key names no tenant and every plane must refuse it — [Zero] is the
// question, and it is answerable without reaching for the string.
type Key struct{ s string }

// String renders the key. It is the store index, the row's org column and the
// derived-key salt all at once, which is what stops three planes from disagreeing
// about whose a row is.
func (k Key) String() string { return k.s }

// Zero reports whether this Key names no tenant — the value of a Key that was
// never minted.
func (k Key) Zero() bool { return k.s == "" }

// Brand is the key's brand half.
func (k Key) Brand() string {
	b, _, _ := strings.Cut(k.s, Sep)
	return b
}

// Org is the key's BARE org half — the value cloud's own clients (cloud.OrgDB, the
// meter, the source planes' own tenant column) are keyed on. Those clients scope by
// brand at a different layer, so handing them the qualified key would
// double-qualify. Every use of this method is therefore a read of a plane this
// one does not own, and is visible as such.
func (k Key) Org() string {
	_, org, _ := strings.Cut(k.s, Sep)
	return org
}

// canon is the key's brand half: lower-cased, trimmed, and REGISTERED. It is one
// function so the boot check, the per-request mint and the shape validator cannot
// drift from each other, and so there is exactly one answer to "what is the brand
// half of a key" in the fleet.
func canon(id string) (string, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	switch {
	case id == "":
		return "", zip.ErrForbidden("no brand is configured, so nothing vouches for this tenant")
	case !brand.Registered(id):
		// A brand the registry does not carry is not a brand: it has no issuer, so
		// nothing signs for its orgs, and the planes that write these surfaces
		// already refuse it. Minting one here would file rows under a tenant the
		// writer can never produce.
		return "", zip.ErrForbidden("this deployment's brand is not one the registry vouches for, so it has no tenants")
	}
	return id, nil
}

// Vouches reports whether id names a brand this process can mint keys under, as
// an error a boot path can print. Derived from [canon], so a deployment that
// would 403 every request learns it once at mount instead of once per caller.
func Vouches(id string) error {
	_, err := canon(id)
	return err
}

// Mint builds the key from the deployment's brand and a validated org. It is THE
// mint.
//
// Every branch below refuses to serve rather than falling back, because every
// fallback available is a cross-tenant one: an unvouched brand puts rows under a
// tenant no writer can produce, an empty org names no tenant at all, and an org
// containing the separator is not readable back to the institution it names
// (`zoo/lux/acme` does not say whose it is).
//
// The brand half is CANONICALISED, never merely trimmed. A case fold here is not
// a niceness: `Hanzo/acme` and `hanzo/acme` would be two tenant spaces for one
// business, and whichever spelling a deployment happened to be started with would
// decide which of them it could see.
func Mint(id, org string) (Key, error) {
	b, err := canon(id)
	if err != nil {
		return Key{}, err
	}
	org = strings.TrimSpace(org)
	switch {
	case org == "":
		return Key{}, zip.ErrForbidden("no org, so the request acts for no tenant")
	case org == Public:
		return Key{}, zip.ErrForbidden("the anonymous lane is not a tenant and has no data plane")
	case strings.Contains(org, Sep):
		return Key{}, zip.ErrForbidden("org contains the tenant separator, so the tenant it names is not readable back")
	}
	return Key{s: b + Sep + org}, nil
}

// Qualified reports whether k is a key Mint would have produced for id.
// Derived from Mint rather than restated, so there is ONE definition of the shape
// and a change to it cannot leave a validator behind.
func Qualified(id string, k Key) bool {
	b, org, found := strings.Cut(k.s, Sep)
	if !found {
		return false
	}
	want, err := canon(id)
	if err != nil || b != want {
		return false
	}
	again, err := Mint(b, org)
	return err == nil && again == k
}

// Of resolves the key a request acts for, from the VALIDATED principal and the
// deployment's own brand.
//
// FAIL CLOSED off the HTTP path. A CLI LocalInvoke has no request, so there is no
// validated principal and no tenant to act for — the same 403 a forged X-Org-Id
// gets, from the same line, with no second gate to keep in sync.
//
// TWO FACTS, COMPARED. The brand half is the DEPLOYMENT's, because that is the
// value the planes writing these surfaces key by. But which brand's IAM actually
// vouched for the caller is a SERVER-OBSERVED fact of its own — resolved from the
// token's verified `iss` — and cloud trusts every white-label brand's issuer, so
// the two can legitimately differ. When they do, this refuses: minting `hanzo/acme`
// for an org whose only attestation came from lux.id puts two unrelated businesses
// in one key space, which is precisely what qualification exists to prevent. A
// principal with no issuer to resolve (an sk- key this deployment's own IAM
// issued) carries no second fact, and nothing is compared.
func Of(ctx context.Context, deployment string) (Key, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return Key{}, err
	}
	k, err := Mint(deployment, org)
	if err != nil {
		return Key{}, err
	}
	if vouched, ok := principal.BrandFrom(ctx); ok && strings.ToLower(strings.TrimSpace(vouched)) != k.Brand() {
		return Key{}, zip.ErrForbidden(fmt.Sprintf(
			"this token was minted by the %s brand's IAM and this deployment serves %s; one brand's org is not the other's",
			vouched, k.Brand()))
	}
	return k, nil
}
