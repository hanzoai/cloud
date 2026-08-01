package risk

// tenant.go is the ONE place a tenant key is minted, and the only type the rest
// of this package can use to reach anything a tenant owns.
//
// THE KEY IS `<brand>/<org>`, NOT `<org>`. An org name is unique within an
// issuer and not across issuers: `acme` on hanzo.id and `acme` on zoo.ngo are two
// unrelated businesses. Keyed on the bare org they become one — one set of
// decisions, one set of rules, one model, and one derived-key salt, so one
// brand's subjects would collide with the other's. The engine states this at
// luxfi/aml pkg/api/tenant.go (qualify) and every OrgID in pkg/types carries the
// qualified form; cloud must mint the same shape or the core is being handed a
// key it documents as invalid.
//
// The brand half NEVER comes from a header. It comes from deps.Brand, which the
// binary is started with — for the same reason the engine takes it from the
// request Host and not from X-Forwarded-Host: a caller that can choose its brand
// can choose which tenant space its org lands in, which is the collision
// qualification exists to prevent, arrived at from the other side.
//
// Tenant has no exported constructor and its underlying type is unexported to
// the wire: it cannot be decoded from a request body, so no In struct can carry
// one. The only way to obtain one is tenantOf(ctx), which reads the VALIDATED
// principal cloud.Bridge parked. That makes "this read is tenant-scoped" a fact
// the compiler checks rather than a convention a reviewer checks.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// sep separates the brand half of a tenant key from the org half. It is the
// engine's separator (luxfi/aml pkg/api/tenant.go) because the key crosses into
// the engine as types.Transaction.OrgID and anomaly's per-tenant model index.
const sep = "/"

// publicTenant is the reserved org the event door files credential-less writes
// under (apps/analytics/event.go). It is NOT a customer: an unauthenticated
// stranger writes into it, so a feature read that included it would let anyone
// move a real tenant's statistics, and a baseline that counted it would let
// anyone move the network's. It is refused at the mint, which is the one place
// every read has to pass.
const publicTenant = "$public"

// Tenant is a minted, qualified tenant key: `<brand>/<org>`.
//
// It is a distinct type rather than a string so that a function requiring one
// cannot be handed a bare org by accident, and it is unexported-by-shape (no
// json tags anywhere, no exported field) so it cannot arrive off the wire.
type Tenant string

// String renders the key. Used for the model index, the history org column and
// the store partition — all three are the SAME value by construction, which is
// what stops the three planes from disagreeing about who a row belongs to.
func (t Tenant) String() string { return string(t) }

// org returns the org half of the key — the value cloud's own seams
// (cloud.OrgDB, ResourceMeter.Meter, the /v1/event copy) are keyed on. Those
// seams do their own brand scoping at a different layer, so handing them the
// qualified key would double-qualify.
func (t Tenant) org() string {
	_, org, _ := strings.Cut(string(t), sep)
	return org
}

// qualify mints the tenant key from the deployment's brand and a validated org.
//
// It is the ONE mint. Every refusal here is a refusal to serve rather than a
// fallback, because every fallback available is a cross-tenant one: an empty
// brand puts two brands in one space, an empty org names no tenant at all, and
// an org containing the separator is not readable back to the institution it
// names (`zoo/lux/acme` does not say whose it is).
func qualify(brand, org string) (Tenant, error) {
	brand = strings.TrimSpace(brand)
	org = strings.TrimSpace(org)
	switch {
	case brand == "":
		return "", zip.ErrForbidden("no brand is configured, so nothing vouches for this tenant")
	case strings.Contains(brand, sep):
		return "", zip.ErrForbidden("the configured brand is not a single label")
	case org == "":
		return "", zip.ErrForbidden("no org, so the request acts for no tenant")
	case org == publicTenant:
		return "", zip.ErrForbidden("the anonymous lane is not a tenant and has no risk surface")
	case strings.Contains(org, sep):
		return "", zip.ErrForbidden("org contains the tenant separator, so the tenant it names is not readable back")
	}
	return Tenant(brand + sep + org), nil
}

// qualified reports whether a key is one qualify would have produced. Derived
// from qualify rather than restated, so there is one definition of the shape and
// a change to it cannot leave a validator behind.
func qualified(brand string, key Tenant) bool {
	b, org, found := strings.Cut(string(key), sep)
	if !found || b != strings.TrimSpace(brand) {
		return false
	}
	again, err := qualify(b, org)
	return err == nil && again == key
}

// scope is everything a typed op needs to act for one tenant: the minted key,
// the bare org and project the cloud seams take, and whether the project is
// bound to a validated claim (which decides whether a project-scoped spend cap
// may hard-enforce).
type scope struct {
	tenant   Tenant
	org      string
	project  string
	validate bool
	request  string
	clientIP string
}

// tenantOf resolves the caller's scope from the validated principal.
//
// FAIL CLOSED off the HTTP path. A CLI LocalInvoke has no request, so there is
// no validated principal and no tenant to act for — the same 403 a forged
// X-Org-Id gets, from the same line, with no second gate to keep in sync.
func tenantOf(ctx context.Context, brand string) (scope, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return scope{}, zip.ErrForbidden("no validated principal")
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok || strings.TrimSpace(org) == "" {
		return scope{}, zip.ErrForbidden("no validated principal")
	}
	t, err := qualify(brand, org)
	if err != nil {
		return scope{}, err
	}
	project, validated := principal.ValidatedProject(c)
	return scope{
		tenant:   t,
		org:      org,
		project:  project,
		validate: validated,
		request:  c.RequestID(),
		clientIP: cloud.ClientIP(c),
	}, nil
}
