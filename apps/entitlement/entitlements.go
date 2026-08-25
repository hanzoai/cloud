// Package entitlements is what your org may run: what the plan grants, and which of
// those products are switched on.
//
// Both authorities live here, one endpoint each, and the package's whole discipline
// is that they are never braided.
//
// Surface (/v1 only):
//
//	GET  /v1/entitlement            -> per-app booleans from the org's PLAN (projection.go)
//	GET  /v1/entitlement/orgs/:org  -> { "enabled": ["engine","chat",...] }
//	POST /v1/entitlement/orgs/:org  { "add":[...], "remove":[...] }  -> { "enabled":[...] }
//
// TWO AUTHORITIES, NEVER BRAIDED.
//   - ENABLEMENT (this store): which products the org has toggled on. The org's
//     intent. Durable SQLite — the deployment's own "entitlements" — keyed
//     (org, product).
//   - ENTITLEMENT (commerce): which products the org's plan/subscription grants.
//     The billing truth. Read via deps.Commerce.CheckEntitlement at WRITE time.
//
// A product may only be ENABLED if it is ENTITLED — so a non-super-admin can only
// switch on what the org already pays for; enabling never spends new money (a plan
// upgrade happens in commerce, not here). DISABLING is always allowed (turning a
// product off is never gated). A SUPER ADMIN (owner==AdminOrg) BYPASSES the commerce
// gate — the operator can comp/grant any product to any org — and may target ANY :org.
//
// ORG SCOPING mirrors apps/kms (/v1/kms/orgs/:org), and so does the address:
// {:org} must equal the
// caller's VALIDATED org (c.Org()), unless the caller is a super admin (c.IsAdmin(),
// minted only for owner==AdminOrg by SanitizeIdentity — never client-forgeable), who
// may act on any org. A bearer-less forge (X-Org-Id restored, no X-User-Id) fails the
// principal.Validated gate → 403. There is no path a caller reads or writes another
// org's entitlements.
package entitlement

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// orgs is the per-org enablement sub-tree. It answered at /v1/orgs/:org/entitlements
// — a root this capability does not own (HIP-0139 §3, §7) — and folds home to the
// shape apps/kms already serves, so the org scoping rule and the address now read
// the same way round. One constant, because cmd/zipdoc resolves a typed op's prefix
// from the CONSTANT VALUE of the Group argument.
const orgs = "/v1/entitlement/orgs"

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/entitlements openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

const (
	// maxProductsPerRequest bounds one add/remove batch, so a single POST cannot
	// blow up the transaction or the commerce fan-out.
	maxProductsPerRequest = 64
)

// productRE constrains a canonical product id: it is a @hanzo/products catalog
// slug that becomes a store key and a commerce productID, so this is the boundary
// guard. It matches the k8s label shape (lowercase DNS-ish) — identical to the
// settings subsystem's :product guard (DRY: one product-id shape across cloud).
var productRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// orgRE constrains the :org path segment: a DNS-1123-ish org label. It is the
// org-isolation boundary folded into the store key, validated strictly at the
// edge — the same shape clients/kms enforces.
var orgRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

// service is the composition root for the entitlements surface. It owns the store
// and holds the commerce client for the entitlement gate. commerce may be nil
// (commerce not co-resident / disabled) — a non-super-admin enable then fails
// closed (503), never open.
type service struct {
	store    *Store
	commerce cloud.CommerceClient
	log      luxlog.Logger
}

var mounted *service

// Mount registers the entitlements surface on app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("entitlement.Mount: nil app")
	}
	log := luxlog.Default().New("subsystem", "entitlement")
	if deps.DataDir == "" {
		return fmt.Errorf("entitlement.Mount: empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("entitlement.Mount: open store: %w", err)
	}
	s := &service{store: store, commerce: deps.Commerce, log: log}
	mounted = s
	routes(app, s)

	log.Info("entitlements surface mounted", "prefix", orgs, "brand", deps.Brand, "commerce", deps.Commerce != nil)
	return nil
}

// ops binds the mounted service so each op can be a method value — the only bound
// form cmd/zipdoc can lift prose from. It carries STATE and no logic: every method
// resolves its org through resolveOrg and then calls the same store.
type ops struct{ s *service }

// routes registers the entitlements surface as TYPED ops — one registry entry per
// route, from which the REST route, the OpenAPI operation, the MCP tool, the CLI
// command and every generated SDK method follow. It is the ONE registration: Mount
// calls it, and so do the package's tests, so a test can never exercise a router
// this binary does not serve.
func routes(app cloud.Router, s *service) {
	// cloud.Bridge is not installed here: the composer installs it once at the
	// root, after the identity check that mints the validated org and before any
	// subsystem registers a route — an order only the whole program can assert.
	// The ops below read what it parks (the validated org, and the request their
	// admin gates inspect) off the context.

	// Declared on GROUPS: the op's path is the prefix composed with the leaf, which
	// is the identity every projection keys on, and cmd/zipdoc resolves the prefix
	// the same way, so the prose reaches the document and the tool list.
	o := ops{s: s}
	og := app.Group(orgs)
	zip.Get(og, "/:org", o.get)
	zip.Post(og, "/:org", o.post)

	// The ENTITLEMENT (commerce) projection the @hanzogui/shell reads — the READ
	// side of the unified paywall (projection.go), distinct from the ENABLEMENT
	// store above. ONE more op on this existing subsystem: it does NOT add a
	// Wire spec, so manifest/order_test.go's TestAppsOrderMatchesFrozen stays green.
	//
	// Declared on the /v1 PARENT with a non-empty leaf: zip.Get(g, "") on a
	// /v1/entitlement group would normalise to "/v1/entitlement/", and op.Path is
	// the identity every projection keys on.
	zip.Get(app.Group("/v1"), "/entitlement", o.projection)
}

// Shutdown releases the entitlements store. Idempotent.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	var err error
	if mounted.store != nil {
		err = mounted.store.Close()
	}
	mounted = nil
	return err
}

// ── org gate ────────────────────────────────────────────────────────────────

// resolveOrg validates the :org path segment and reconciles it with the caller's
// validated principal. It returns the authoritative org key, whether the caller is a
// super admin, and the request (for the actor a write is attributed to). It fails
// closed (caller answers the returned error) for a malformed org, an unvalidated
// principal, or a cross-org attempt by a non-super-admin. This is the ONE trust
// decision for both ops.
//
// The org key it returns is the URL's, but it is never TRUSTED as one: the equality
// below is against the VALIDATED principal, so the URL only ever names an org the
// caller has already been proven to hold. That is why `org` may be an input field —
// it is an ADDRESS the gate re-checks, not an assertion the gate believes.
//
// It reads the request rather than principal.OrgFrom because it needs two facts the
// parked org does not carry: SuperAdmin-ness (X-User-IsAdmin, minted only by
// SanitizeIdentity) and the validated owner claim to compare the URL against. Fails
// closed off the HTTP path: no request, no attested principal, no access.
func (s *service) resolveOrg(ctx context.Context, param string) (org string, superAdmin bool, c *zip.Ctx, err error) {
	org = strings.TrimSpace(param)
	if !orgRE.MatchString(org) {
		return "", false, nil, zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	c, ok := cloud.Request(ctx)
	if !ok || !principal.Validated(c) {
		// No validated principal. The identity middleware RESTORES a client
		// X-Org-Id on the bearer-less path, so c.Org() could equal a forged :org
		// and defeat the equality check below. Refuse here.
		return "", false, nil, zip.ErrForbidden("no validated principal")
	}
	if c.IsAdmin() {
		// Super admin (owner==AdminOrg, minted only by SanitizeIdentity): may act
		// on any org. The store key is the :org they targeted.
		return org, true, c, nil
	}
	// Non-super-admin: may only touch its OWN org. c.Org() is the validated owner
	// claim; a mismatch with :org is a cross-org attempt.
	if strings.TrimSpace(c.Org()) != org {
		return "", false, nil, zip.ErrForbidden("caller may only access its own org's entitlements")
	}
	return org, false, c, nil
}

// ── GET ──────────────────────────────────────────────────────────────────────

// entitlementsView is the wire contract the console consumes. `enabled` is the
// sorted list of canonical product ids the org has turned on; it is ALWAYS a
// (possibly empty) array, never null — the console can map over it unconditionally.
type entitlementsView struct {
	// Enabled is the org's turned-on product ids, sorted. Always an array, never null.
	Enabled []string `json:"enabled"`
}

// orgRef addresses one org's enablement row. The org is URL-borne only: `json:"-"`
// keeps it out of the published request body, and `url:"org"` binds it from the path
// segment the router matched on.
type orgRef struct {
	// Org is the org whose entitlements are being read, from the path. It must be the
	// caller's own validated org unless the caller is a platform super admin.
	Org string `json:"-" url:"org"`
}

// Get lists the products an org has ENABLED — its own intent, which the console's
// paid-product sidebar reads to decide what to show. It is distinct from what the
// org's plan ENTITLES it to (that is GET /v1/entitlement, resolved from commerce).
//
// A caller may only read its OWN org's row; a platform super admin may read any.
func (o ops) get(ctx context.Context, in *orgRef) (*entitlementsView, error) {
	org, _, _, err := o.s.resolveOrg(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	enabled, err := o.s.store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list entitlements: %v", err)
	}
	return &entitlementsView{Enabled: enabled}, nil
}

// ── POST ───────────────────────────────────────────────────────────────────────

// mutateReq turns products on and off for one org. At least one of add or remove
// must be non-empty; a batch is capped at 64 product ids.
type mutateReq struct {
	// Org is the org being changed, from the path. It must be the caller's own
	// validated org unless the caller is a platform super admin.
	Org string `json:"-" url:"org"`
	// Add is the product ids to turn ON. Each must already be an ACTIVE entitlement
	// of the org's plan, unless the caller is a platform super admin.
	Add []string `json:"add"`
	// Remove is the product ids to turn OFF. Disabling is never gated.
	Remove []string `json:"remove"`
}

// Post turns products on or off for an org and returns the enabled set afterwards.
//
// A product may only be ENABLED if the org's plan already ENTITLES it, so enabling
// never spends new money — a product the plan does not grant answers 402 and the
// console routes that to an upgrade prompt. DISABLING is never gated. A platform
// super admin bypasses the plan check (operator comp/grant) and may target any org;
// everyone else may only change their own. Commerce unreachable is a 503, never an
// implicit yes.
//
// Example: {"add": ["chat"], "remove": ["engine"]}
func (o ops) post(ctx context.Context, in *mutateReq) (*entitlementsView, error) {
	org, superAdmin, c, err := o.s.resolveOrg(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	add, err := cleanProducts(in.Add)
	if err != nil {
		return nil, err
	}
	remove, err := cleanProducts(in.Remove)
	if err != nil {
		return nil, err
	}
	s := o.s
	if len(add) == 0 && len(remove) == 0 {
		return nil, zip.ErrBadRequest("add or remove must be non-empty")
	}

	// ENTITLEMENT GATE — only for a non-super-admin ADD. Every product being
	// enabled must be an ACTIVE entitlement of the org's plan/subscription in
	// commerce. A super admin bypasses (operator comp/grant). Disabling is never
	// gated. Commerce unavailable ⇒ fail closed (a non-super-admin cannot enable
	// what cannot be verified) — never open.
	if !superAdmin && len(add) > 0 {
		if s.commerce == nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "entitlement service unavailable; cannot verify plan")
		}
		for _, p := range add {
			ent, cErr := s.commerce.CheckEntitlement(ctx, org, p)
			if cErr != nil {
				return nil, zip.Errorf(http.StatusServiceUnavailable, "check entitlement for %q: %v", p, cErr)
			}
			if ent == nil || !ent.Active {
				// 402 Payment Required: the org's plan does not grant this product.
				// The console routes this to an upgrade/purchase prompt.
				return nil, zip.Errorf(http.StatusPaymentRequired, "product %q is not in this org's plan; upgrade in commerce to enable it", p)
			}
		}
	}

	// The actor a change is ATTRIBUTED to is the validated user id (X-User-Id), which
	// the parked org does not carry — resolveOrg hands back the request for exactly
	// this. It is an attribution, never an authority: every gate above already ran.
	actor := strings.TrimSpace(c.User())
	enabled, err := s.store.Apply(ctx, org, add, remove, actor, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "apply entitlements: %v", err)
	}
	s.log.Info("entitlements mutated", "org", org, "add", add, "remove", remove, "superAdmin", superAdmin, "by", actor)
	return &entitlementsView{Enabled: enabled}, nil
}

// cleanProducts trims, drops empties, validates the slug shape, de-duplicates
// (order-preserving), and bounds the batch. It returns a specific 400 on the
// first malformed id so a bad request never reaches the store or commerce.
func cleanProducts(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if !productRE.MatchString(p) {
			return nil, zip.ErrBadRequest("product id must match ^[a-z0-9][a-z0-9._-]{0,62}$: " + p)
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
		if len(out) > maxProductsPerRequest {
			return nil, zip.ErrBadRequest("too many products in one request (max 64)")
		}
	}
	return out, nil
}
