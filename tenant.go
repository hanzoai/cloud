package cloud

// tenant.go mints the BRAND-QUALIFIED tenant key: the value that names a tenant
// in a plane SHARED across brands.
//
// It is the third of the three org-naming doors in this package and the only one
// for a shared plane. The other two name a PHYSICAL partition, where the brand is
// already in the path:
//
//	SanitizeOrg    an org id  → an injective slug              (org_scope.go)
//	OrgNamespace   a slug     → the name of that org's file    (orgns.go)
//	Qualify        an org id  → `<brand>/<org>`, this file
//
// WHY A THIRD DOOR AND NOT A FOURTH SPELLING. An org name is unique within an
// ISSUER and not across issuers: `acme` on hanzo.id and `acme` on zoo.ngo are two
// unrelated businesses. A per-org SQLite file never confuses them — DataDir is
// per deployment, so the brand is the directory the file is in. A COLUMN in the
// shared columnar warehouse has no such directory: `org = 'acme'` there is a
// predicate over both. Every row a brand-shared table carries must therefore be
// keyed on the qualified form, and every read of it must bind the qualified form,
// or one brand's rows answer another brand's query.
//
// THE BRAND HALF NEVER COMES FROM A HEADER. It comes from Deps.Brand, which the
// binary is started with. A caller that can choose its brand has chosen which
// tenant space its org lands in, which is the collision qualification exists to
// prevent, arrived at from the other side.
//
// Tenant has no exported constructor other than Qualify and no JSON tags, so it
// cannot be decoded from a request body: an In struct cannot carry one, and a
// tenant-scoped read cannot be asserted by the caller for itself.

import (
	"strings"

	"github.com/zap-proto/zip"
)

// tenantSep separates the brand half of a tenant key from the org half. It is
// the separator the AML engine already mints and reads (luxfi/aml
// pkg/api/tenant.go), because the key crosses into that engine as an OrgID.
const tenantSep = "/"

// publicOrg is the reserved org the /v1/event door files credential-less writes
// under (apps/analytics/public.go). It is NOT a customer: an unauthenticated
// stranger writes into it. A plane that admitted it would let that stranger move
// a real tenant's statistics, so it is refused at the mint — the one place every
// qualified read has to pass.
const publicOrg = "$public"

// Tenant is a minted, brand-qualified tenant key: `<brand>/<org>`.
//
// A distinct type rather than a string, so a function requiring one cannot be
// handed a bare org by accident.
type Tenant string

// String renders the key. It is the value written to a shared plane's tenant
// column and bound in every predicate over it — one value, so the write and the
// read cannot disagree about whose row it is.
func (t Tenant) String() string { return string(t) }

// Org returns the org half: the value this package's OTHER doors take, and the
// value the source planes that predate qualification carry in their own tenant
// column. Those planes scope by brand at a different layer, so handing them the
// qualified key would put the brand in twice.
func (t Tenant) Org() string {
	_, org, _ := strings.Cut(string(t), tenantSep)
	return org
}

// Qualify mints the tenant key from the deployment's brand and a VALIDATED org.
//
// Every case below is a refusal to serve rather than a fallback, because every
// fallback available is a cross-tenant one: an empty brand puts two brands in one
// space, an empty org names no tenant at all, and an org containing the separator
// is not readable back to the institution it names (`zoo/lux/acme` does not say
// whose it is).
//
// org MUST be the validated principal value (principal.OrgFrom), never a body
// field, a query parameter or a header.
func Qualify(brand, org string) (Tenant, error) {
	brand = strings.TrimSpace(brand)
	org = strings.TrimSpace(org)
	switch {
	case brand == "":
		return "", zip.ErrForbidden("no brand is configured, so nothing vouches for this tenant")
	case strings.Contains(brand, tenantSep):
		return "", zip.ErrForbidden("the configured brand is not a single label")
	case org == "":
		return "", zip.ErrForbidden("no org, so the request acts for no tenant")
	case org == publicOrg:
		return "", zip.ErrForbidden("the anonymous lane is not a tenant")
	case strings.Contains(org, tenantSep):
		return "", zip.ErrForbidden("org contains the tenant separator, so the tenant it names is not readable back")
	}
	return Tenant(brand + tenantSep + org), nil
}

// Qualified reports whether a key is one Qualify would have produced under this
// brand. Derived from Qualify rather than restated, so there is one definition of
// the shape and a change to it cannot leave a validator behind.
func Qualified(brand string, t Tenant) bool {
	b, org, found := strings.Cut(string(t), tenantSep)
	if !found || b != strings.TrimSpace(brand) {
		return false
	}
	again, err := Qualify(b, org)
	return err == nil && again == t
}
