package content

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/clients/framework"
)

// catalog.go is the REVERSE half of the storefront loop: a commerce catalog product
// coming into existence should get its ecom asset rendered, so the forward edge
// (storefront.go) then has an image to publish. The commerce→content transport is the
// COMMERCE event stream; a consumer subsystem (clients/catalogsync) turns each
// product.created into ONE call here.
//
// EnsureCatalogAsset is the ONE mapping: product slug → an ecom Asset draft, keyed by the
// SAME join convention the whole loop uses (design == slug). It is idempotent (a product
// that already has a non-archived ecom asset is a no-op) and quiet by design — a
// deployment without a reachable studio, or an org that has not installed the marketing
// lane, is a SKIP, never an error the consumer would retry forever.

// catalogAssetKind is the asset kind the reverse loop renders for a new product: the
// primary ecommerce catalog shot (one of storefront.go's catalogKinds). The idempotency
// check is scoped to this kind, so it never suppresses a differently-kinded render.
const catalogAssetKind = "ecom"

// CatalogAssetResult reports the outcome of EnsureCatalogAsset. Exactly one of Created or
// a non-empty Skipped is set; Name is the created or pre-existing Asset (empty on skip).
type CatalogAssetResult struct {
	Org     string `json:"org"`
	Slug    string `json:"slug"`
	Name    string `json:"name,omitempty"`
	Created bool   `json:"created"`
	Skipped string `json:"skipped,omitempty"` // "exists" | "not_installed" | "not_configured"
}

// EnsureCatalogAsset renders (if needed) the ecom Asset for a commerce product, keyed by
// design == slug. org MUST be the validated tenant. It returns a benign result with nil
// error for every foreseeable skip — an org without the marketing lane, a product that
// already has a non-archived ecom asset, or a studio that is not configured/reachable —
// so a driving consumer ACKs and moves on. It returns a non-nil error ONLY for a genuine
// store fault the consumer should retry.
func EnsureCatalogAsset(ctx context.Context, org, slug string) (CatalogAssetResult, error) {
	org = strings.TrimSpace(org)
	slug = strings.TrimSpace(slug)
	res := CatalogAssetResult{Org: org, Slug: slug}
	if org == "" || slug == "" {
		return res, fmt.Errorf("content: EnsureCatalogAsset requires a non-empty org and slug")
	}
	if mounted == nil {
		return res, errNotMounted
	}
	// marketing is always-on, so the Asset lane resolves for every org (no per-org
	// install gate). The reverse loop is still best-effort: an org with no configured
	// studio quietly skips at the Generate step below (errNotConfigured), never erroring.

	// Idempotency: a non-archived ecom Asset already keyed to this product ⇒ nothing to do.
	name, exists, err := existingCatalogAsset(ctx, org, slug)
	if err != nil {
		return res, err // a genuine store fault — let the consumer retry
	}
	if exists {
		res.Name, res.Skipped = name, "exists"
		return res, nil
	}

	gen, err := Generate(ctx, org, GenerateInput{DocType: DocTypeAsset, Design: slug, Kind: catalogAssetKind})
	if err != nil {
		// A studio that is not configured/reachable (or the lane not installed) is a
		// SKIP, not an error — the reverse loop is best-effort; a human generate covers it.
		if errors.Is(err, errNotConfigured) || errors.Is(err, errModuleNotInstalled) {
			res.Skipped = "not_configured"
			return res, nil
		}
		return res, err
	}
	res.Name, res.Created = gen.Name, true
	return res, nil
}

// existingCatalogAsset reports whether the org already holds a non-archived ecom Asset for
// the product slug (design == slug). Archived assets are ignored so a retired render never
// suppresses a fresh one. It is the idempotency read EnsureCatalogAsset gates on.
func existingCatalogAsset(ctx context.Context, org, slug string) (string, bool, error) {
	docs, err := framework.Search(ctx, org, DocTypeAsset, map[string]string{
		"design": slug,
		"kind":   catalogAssetKind,
	}, 200)
	if err != nil {
		return "", false, err
	}
	for _, d := range docs {
		// Re-check the join fields in Go: the framework filter narrows the scan, but the
		// idempotency guarantee must not depend on its exact filter fidelity.
		if dataString(d.Data, "design") != slug || dataString(d.Data, "kind") != catalogAssetKind {
			continue
		}
		if dataString(d.Data, StatusField) == StatusArchived {
			continue
		}
		return d.Name, true, nil
	}
	return "", false, nil
}
