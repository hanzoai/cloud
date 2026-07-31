// Package entitlements is the per-org product-enablement plane for the unified
// Hanzo Cloud binary: the /v1/orgs/:org/entitlements surface the console's paid-
// product sidebar reads to decide which products to SHOW, and org owners /
// super admins write to TURN a product on or off.
//
// Surface (all org-scoped; /v1 only):
//
//	GET  /v1/orgs/:org/entitlements   -> { "enabled": ["engine","chat",...] }
//	POST /v1/orgs/:org/entitlements   { "add":[...], "remove":[...] }  -> { "enabled":[...] }
//
// TWO AUTHORITIES, NEVER BRAIDED.
//   - ENABLEMENT (this store): which products the org has toggled on. The org's
//     intent. Durable per-org SQLite ({DataDir}/entitlements.db), (org,product) key.
//   - ENTITLEMENT (commerce): which products the org's plan/subscription grants.
//     The billing truth. Read via deps.Commerce.CheckEntitlement at WRITE time.
//
// A product may only be ENABLED if it is ENTITLED — so a non-super-admin can only
// switch on what the org already pays for; enabling never spends new money (a plan
// upgrade happens in commerce, not here). DISABLING is always allowed (turning a
// product off is never gated). A SUPER ADMIN (owner==AdminOrg) BYPASSES the commerce
// gate — the operator can comp/grant any product to any org — and may target ANY :org.
//
// ORG SCOPING mirrors clients/kms (/v1/kms/orgs/:org): {:org} must equal the
// caller's VALIDATED org (c.Org()), unless the caller is a super admin (c.IsAdmin(),
// minted only for owner==AdminOrg by SanitizeIdentity — never client-forgeable), who
// may act on any org. A bearer-less forge (X-Org-Id restored, no X-User-Id) fails the
// principal.Validated gate → 403. There is no path a caller reads or writes another
// org's entitlements.
package entitlements

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

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
		return fmt.Errorf("entitlements.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("entitlements.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "entitlements")
	if deps.DataDir == "" {
		return fmt.Errorf("entitlements.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("entitlements.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "entitlements.db"))
	if err != nil {
		return fmt.Errorf("entitlements.Mount: open store: %w", err)
	}
	s := &service{store: store, commerce: deps.Commerce, log: log}
	mounted = s

	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("entitlements.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	// The typed-op bridge FIRST — fiber runs middleware in registration order, so
	// one installed after these leaves would never run, and every op below reads
	// the request (the validated org, the super-admin bit) off the context it
	// parks. Use rather than Group: this subsystem owns TWO top-level nouns
	// (/v1/entitlements and /v1/orgs/:org/entitlements), and Use is the door that
	// is bounded by the prefixes the subsystem declared instead of by a prefix
	// spelled here. Serve installs one app-wide too; nesting is harmless, and this
	// is what makes the surface testable on a bare app.
	app.Use(cloud.Bridge())

	zip.Get(zapp, "/v1/orgs/:org/entitlements", s.get)
	zip.Post(zapp, "/v1/orgs/:org/entitlements", s.post)

	// The ENTITLEMENT (commerce) projection the @hanzogui/shell reads — the READ
	// side of the unified paywall (projection.go), distinct from the ENABLEMENT
	// store above. ONE more route on this existing subsystem: it does NOT add a
	// Wire spec, so apps/wire_test.go TestWireOrderMatchesFrozen stays green.
	zip.Get(zapp, "/v1/entitlements", s.projection)

	log.Info("entitlements surface mounted", "prefix", "/v1/orgs/:org/entitlements", "brand", deps.Brand, "commerce", deps.Commerce != nil)
	return nil
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

// resolveOrg validates the org the caller addressed and reconciles it with the
// caller's validated principal. It returns the authoritative org key and whether
// the caller is a super admin. It fails closed (caller answers the returned
// error) for a malformed org, an unvalidated principal, or a cross-org attempt
// by a non-super-admin. This is the ONE trust decision for both handlers.
//
// The addressed org arrives as the op's In field, which the URL's path segment
// binds over anything a body claimed — so `raw` is the segment the router
// matched. It is never the tenant key on its own: the check below is what makes
// it one. Off the HTTP path (the CLI's LocalInvoke) there is no request and so
// no validated principal, and the op refuses.
func (s *service) resolveOrg(ctx context.Context, raw string) (org string, superAdmin bool, err error) {
	org = strings.TrimSpace(raw)
	if !orgRE.MatchString(org) {
		return "", false, zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	c, ok := cloud.Request(ctx)
	if !ok || !principal.Validated(c) {
		// No validated principal. The identity middleware RESTORES a client
		// X-Org-Id on the bearer-less path, so c.Org() could equal a forged org
		// and defeat the equality check below. Refuse here.
		return "", false, zip.ErrForbidden("no validated principal")
	}
	if c.IsAdmin() {
		// Super admin (owner==AdminOrg, minted only by SanitizeIdentity): may act
		// on any org. The store key is the :org they targeted.
		return org, true, nil
	}
	// Non-super-admin: may only touch its OWN org. c.Org() is the validated owner
	// claim; a mismatch with :org is a cross-org attempt.
	if strings.TrimSpace(c.Org()) != org {
		return "", false, zip.ErrForbidden("caller may only access its own org's entitlements")
	}
	return org, false, nil
}

// ── GET ──────────────────────────────────────────────────────────────────────

// entitlementsView is the wire contract the console consumes. `enabled` is the
// sorted list of canonical product ids the org has turned on; it is ALWAYS a
// (possibly empty) array, never null — the console can map over it unconditionally.
type entitlementsView struct {
	// Enabled is the sorted list of canonical product ids the org has turned on,
	// empty (never null) when it has turned none on.
	Enabled []string `json:"enabled"`
}

// orgRef addresses one org's enablement record.
type orgRef struct {
	// Org is the org whose entitlements are read. It must be the caller's own
	// validated org unless the caller is a platform SuperAdmin, who may name any.
	Org string `json:"org"`
}

// get lists the products the org has turned on. The list is the org's own
// INTENT (the enablement store), not what its plan grants — GET /v1/entitlements
// answers that.
//
// Example: {"org": "acme"}
// Response: {"enabled": ["chat", "engine"]}
func (s *service) get(ctx context.Context, in *orgRef) (*entitlementsView, error) {
	org, _, err := s.resolveOrg(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	enabled, err := s.store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list entitlements: %v", err)
	}
	return &entitlementsView{Enabled: enabled}, nil
}

// ── POST ───────────────────────────────────────────────────────────────────────

// mutateReq turns products on and off for one org in a single batch.
type mutateReq struct {
	// Org is the org whose entitlements are written. It must be the caller's own
	// validated org unless the caller is a platform SuperAdmin, who may name any.
	Org string `json:"org"`
	// Add lists product ids to turn ON. Each must already be an ACTIVE
	// entitlement of the org's plan in commerce, unless the caller is a
	// SuperAdmin; anything else is refused with 402.
	Add []string `json:"add"`
	// Remove lists product ids to turn OFF. Never gated — disabling a product is
	// always allowed.
	Remove []string `json:"remove"`
}

// post turns products on and off for one org. Enabling requires the product to
// be an active entitlement of the org's plan (a SuperAdmin bypasses that gate);
// disabling is never gated. Add and remove are applied in one transaction and
// the full resulting set is returned.
//
// Example: {"org": "acme", "add": ["chat"], "remove": ["bot"]}
// Response: {"enabled": ["chat", "engine"]}
func (s *service) post(ctx context.Context, in *mutateReq) (*entitlementsView, error) {
	org, superAdmin, err := s.resolveOrg(ctx, in.Org)
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

	by := actor(ctx)
	enabled, err := s.store.Apply(ctx, org, add, remove, by, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "apply entitlements: %v", err)
	}
	s.log.Info("entitlements mutated", "org", org, "add", add, "remove", remove, "superAdmin", superAdmin, "by", by)
	return &entitlementsView{Enabled: enabled}, nil
}

// actor names who made the change, for the store's audit column. Empty off the
// HTTP path, where there is no request and so no validated user to name.
func actor(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return ""
	}
	return strings.TrimSpace(c.User())
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
