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
	"github.com/hanzoai/cloud/clients/principal"
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
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("entitlements.Mount: nil zip.App")
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

	app.Get("/v1/orgs/:org/entitlements", s.get)
	app.Post("/v1/orgs/:org/entitlements", s.post)

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

// resolveOrg validates the :org param and reconciles it with the caller's
// validated principal. It returns the authoritative org key and whether the
// caller is a super admin. It fails closed (caller answers the returned error)
// for a malformed org, an unvalidated principal, or a cross-org attempt by a
// non-super-admin. This is the ONE trust decision for both handlers.
func (s *service) resolveOrg(c *zip.Ctx) (org string, superAdmin bool, err error) {
	org = strings.TrimSpace(c.Param("org"))
	if !orgRE.MatchString(org) {
		return "", false, zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	if !principal.Validated(c) {
		// No validated principal. The identity middleware RESTORES a client
		// X-Org-Id on the bearer-less path, so c.Org() could equal a forged :org
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
	Enabled []string `json:"enabled"`
}

func (s *service) get(c *zip.Ctx) error {
	org, _, err := s.resolveOrg(c)
	if err != nil {
		return err
	}
	enabled, err := s.store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list entitlements: %v", err)
	}
	return c.JSON(http.StatusOK, entitlementsView{Enabled: enabled})
}

// ── POST ───────────────────────────────────────────────────────────────────────

type mutateReq struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

func (s *service) post(c *zip.Ctx) error {
	org, superAdmin, err := s.resolveOrg(c)
	if err != nil {
		return err
	}
	var body mutateReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	add, err := cleanProducts(body.Add)
	if err != nil {
		return err
	}
	remove, err := cleanProducts(body.Remove)
	if err != nil {
		return err
	}
	if len(add) == 0 && len(remove) == 0 {
		return zip.ErrBadRequest("add or remove must be non-empty")
	}

	// ENTITLEMENT GATE — only for a non-super-admin ADD. Every product being
	// enabled must be an ACTIVE entitlement of the org's plan/subscription in
	// commerce. A super admin bypasses (operator comp/grant). Disabling is never
	// gated. Commerce unavailable ⇒ fail closed (a non-super-admin cannot enable
	// what cannot be verified) — never open.
	if !superAdmin && len(add) > 0 {
		if s.commerce == nil {
			return zip.Errorf(http.StatusServiceUnavailable, "entitlement service unavailable; cannot verify plan")
		}
		for _, p := range add {
			ent, cErr := s.commerce.CheckEntitlement(c.Context(), org, p)
			if cErr != nil {
				return zip.Errorf(http.StatusServiceUnavailable, "check entitlement for %q: %v", p, cErr)
			}
			if ent == nil || !ent.Active {
				// 402 Payment Required: the org's plan does not grant this product.
				// The console routes this to an upgrade/purchase prompt.
				return zip.Errorf(http.StatusPaymentRequired, "product %q is not in this org's plan; upgrade in commerce to enable it", p)
			}
		}
	}

	enabled, err := s.store.Apply(c.Context(), org, add, remove, strings.TrimSpace(c.User()), time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "apply entitlements: %v", err)
	}
	s.log.Info("entitlements mutated", "org", org, "add", add, "remove", remove, "superAdmin", superAdmin, "by", strings.TrimSpace(c.User()))
	return c.JSON(http.StatusOK, entitlementsView{Enabled: enabled})
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
