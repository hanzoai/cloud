// Package tenant mints the key every per-org read and write is bound by, and
// is the only way to obtain one.
//
// THE KEY IS `<brand>/<org>`, NOT `<org>`. An org name is unique within an
// issuer and not across issuers: `acme` on hanzo.id and `acme` on zoo.ngo are
// two unrelated businesses. Keyed on the bare org they become one — one set of
// rows, one dataset, one model, and one derived-key salt — so one brand's
// subjects would silently collide with the other's. The engine states the same
// shape at luxfi/aml pkg/api/tenant.go and every OrgID it carries is qualified;
// a plane that minted a bare org would be handing the engine a key it documents
// as invalid.
//
// The brand half NEVER comes from a header or a body. It is the deployment's
// own brand, for the same reason the org half is taken from the validated
// principal and not from a field: a caller that can choose its brand has chosen
// which tenant space its org lands in, which is the collision qualification
// exists to prevent, arrived at from the other side.
//
// WHY [Key] IS A STRUCT AND NOT A STRING. Its single field is unexported, so:
// no other package can write a Key literal, encoding/json cannot decode one (it
// has no exported field to decode into), and therefore no request body, query
// parameter or path segment can ever carry one. The only non-zero Key in the
// process came out of [Mint], and [Of] is the only caller of Mint that a request
// path can reach. "This read is tenant-scoped" is a fact the compiler checks
// rather than a convention a reviewer checks.
//
// It is its own package rather than a file inside one subsystem because the key
// is not any one subsystem's: the dataset plane, the scoring plane and the
// compliance plane must agree on it BYTE FOR BYTE or a dataset is keyed
// differently from the model fitted on it. One definition, imported.
package tenant

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// Sep divides the key's two halves. Brand ids are a closed set and none contains
// it, so the first one always ends the brand and the key reads back to the
// organisation it names.
const Sep = "/"

// Public is the reserved org the event door files CREDENTIAL-LESS writes under
// (apps/analytics/event.go). It is not a customer: an unauthenticated stranger
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

// Org is the key's BARE org half — the value cloud's own seams (cloud.OrgDB, the
// meter, the source planes' own tenant column) are keyed on. Those seams scope by
// brand at a different layer, so handing them the qualified key would
// double-qualify. Every use of this method is therefore a read of a plane this
// one does not own, and is visible as such.
func (k Key) Org() string {
	_, org, _ := strings.Cut(k.s, Sep)
	return org
}

// Mint builds the key from the deployment's brand and a validated org. It is THE
// mint.
//
// Every branch below refuses to serve rather than falling back, because every
// fallback available is a cross-tenant one: an empty brand puts two brands in one
// space, an empty org names no tenant at all, and an org containing the separator
// is not readable back to the institution it names (`zoo/lux/acme` does not say
// whose it is).
func Mint(brand, org string) (Key, error) {
	brand = strings.TrimSpace(brand)
	org = strings.TrimSpace(org)
	switch {
	case brand == "":
		return Key{}, zip.ErrForbidden("no brand is configured, so nothing vouches for this tenant")
	case strings.Contains(brand, Sep):
		return Key{}, zip.ErrForbidden("the configured brand is not a single label")
	case org == "":
		return Key{}, zip.ErrForbidden("no org, so the request acts for no tenant")
	case org == Public:
		return Key{}, zip.ErrForbidden("the anonymous lane is not a tenant and has no data plane")
	case strings.Contains(org, Sep):
		return Key{}, zip.ErrForbidden("org contains the tenant separator, so the tenant it names is not readable back")
	}
	return Key{s: brand + Sep + org}, nil
}

// Qualified reports whether k is a key Mint would have produced for brand.
// Derived from Mint rather than restated, so there is ONE definition of the shape
// and a change to it cannot leave a validator behind.
func Qualified(brand string, k Key) bool {
	b, org, found := strings.Cut(k.s, Sep)
	if !found || b != strings.TrimSpace(brand) {
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
func Of(ctx context.Context, brand string) (Key, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return Key{}, zip.ErrForbidden("no validated principal")
	}
	return Mint(brand, org)
}
