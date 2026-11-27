package entitlement

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
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
	// trail records every beta grant (holds_rpc.go grant). Nil in a test.
	trail *audit.Recorder
}

var mounted *service

// Mount registers the entitlements surface on app per HIP-0106.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("entitlement: nil app")
	}
	log := luxlog.Default().New("subsystem", "entitlement")
	if cloud.DataDir() == "" {
		return fmt.Errorf("entitlement.Use:  empty DataDir")
	}
	store, err := openStore(cloud.DataDir())
	if err != nil {
		return fmt.Errorf("entitlement.Use:  open store: %w", err)
	}
	s := &service{store: store, commerce: deps.Commerce, log: log, trail: deps.Audit}
	mounted = s
	routes(app, s)
	exposeHolds()

	log.Info("entitlements surface mounted", "prefix", orgs, "brand", cloud.Brand(), "commerce", deps.Commerce != nil)
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
	og.Get("/:org", o.get)
	og.Post("/:org", o.post)

	// The ENTITLEMENT (commerce) projection the @hanzogui/shell reads — the READ
	// side of the unified paywall (projection.go), distinct from the ENABLEMENT
	// store above. ONE more op on this existing subsystem: it does NOT add a
	// Wire spec, so manifest/order_test.go's TestAppsOrderMatchesFrozen stays green.
	app.Group("/v1/entitlement").Get("", o.projection)
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
		// No validated principal, so no org is authority here and the equality
		// check below would be comparing a claim. Refuse here.
		return "", false, nil, principal.Unauthenticated()
	}
	if principal.Super(c) {
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
	org, superAdmin, _, err := o.s.resolveOrg(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	enabled, err := o.s.store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list entitlements: %v", err)
	}
	// A product in beta is the org's once it has it, and an org whose plan carries
	// early access is given it here, on its own read (held). A SuperAdmin reading
	// another org sees it as stored.
	if !superAdmin {
		for _, p := range Beta {
			if slices.Contains(enabled, p) {
				continue
			}
			on, err := o.s.held(ctx, org, p)
			if err != nil {
				return nil, err
			}
			if on {
				enabled = append(enabled, p)
			}
		}
		slices.Sort(enabled)
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

	// A product in beta is never an org's to switch on: it comes from a beta grant
	// (holds_rpc.go) or a SuperAdmin, and no plan buys it. Turning it off is the
	// org's, like any other.
	if !superAdmin {
		for _, p := range add {
			if slices.Contains(Beta, p) {
				return nil, zip.ErrForbidden(fmt.Sprintf("%q is in beta: an org has it by joining the beta at https://hanzo.ai/beta?program=beta", p))
			}
		}
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

	// A product in beta is given its Base module before it is on (install), so the
	// org holds the CRM with its collections or not at all.
	for _, p := range add {
		if slices.Contains(remove, p) {
			continue
		}
		if err := install(ctx, org, p); err != nil {
			return nil, err
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
