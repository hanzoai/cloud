package content

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/transport"
	"github.com/hanzoai/cloud/clients/framework"
)

// storefront.go is the CATALOG seam: when a product Asset goes live (lifecycle →
// published), its rendered image becomes the product image the storefront shows. It
// is the ONE Go-native replacement for the old library.json → karma-queue → sync-*
// → site-build pipeline — instead of a batch job copying approved shots into the
// site, the publish EDGE materializes the asset's S3 URL into the org's Hanzo
// Commerce store Listing, the SAME display layer karma.style already reads at runtime
// (site/commerce.js: GET /v1/store/:store/listing → headerImage.url).
//
// Decomplected exactly like the social Distributor (publish.go/channels.go):
//   - Storefront is the EDGE (Hanzo Commerce) — the ONLY place commerce is touched.
//   - StorefrontPublish is the stable ORCHESTRATION: read the published Asset,
//     resolve the org's store, upsert the product Listing image. It is a side effect
//     of the `published` transition, best-effort, and NEVER fails the status change.
//
// The join key is the asset's `design` == the product `slug` — the SAME key the old
// pipeline used (products.json._meta.library: designs/<slug>/<kind>_<role>.png, take
// assets where kind ∈ [ecom,product,lifestyle]). Tenant scope is the validated org
// (X-Org-Id on the S2S call); the image is referenced by its S3 URL — no copy, no
// re-host. Fail-closed by default: no COMMERCE_SERVICE_TOKEN (or no reachable
// commerce) ⇒ errNotConfigured ⇒ a publish records "not_configured" and never fails.

const (
	// commerceURLEnv / commerceTokenEnv are the SAME deployment config every cloud
	// subsystem that reaches the co-resident (or standalone) commerce handler reads
	// (clients/{account,admin,authors,affiliates,referrals}). The token is the
	// deployment-wide admin service bearer; commerce's EdgeAuth trusts the X-Org-Id
	// header ONLY behind it, so every call stays pinned to the validated tenant.
	commerceURLEnv   = "CLOUD_COMMERCE_HTTP_URL"
	commerceTokenEnv = "COMMERCE_SERVICE_TOKEN"

	// assetBaseEnv is the public object-store base the org's studio output is served
	// from; an Asset's `file` is an object KEY under it (orgs/<org>/output/...). The
	// default is the branded S3 fleet + the studio bucket — the SAME host+bucket the
	// old library manifest published (https://s3.hanzo.ai/hanzo-studio/orgs/.../output).
	assetBaseEnv     = "CONTENT_ASSET_PUBLIC_BASE"
	defaultAssetBase = "https://s3.hanzo.ai/hanzo-studio"

	// storefrontTimeout bounds a single commerce S2S call so a slow edge never wedges a
	// publish/transition. The in-process path is synchronous; this only bounds the
	// standalone/HTTP fallback.
	storefrontTimeout = 15 * time.Second
)

// catalogKinds are the Asset kinds that ARE product imagery — the exact set the old
// pipeline consumed (products.json._meta.library `consume`: kind ∈ [ecom,product,
// lifestyle]). hover/hero/thumbnail are art-direction variants, not the catalog's
// primary product shot, so they do NOT drive the storefront image.
var catalogKinds = map[string]bool{"ecom": true, "product": true, "lifestyle": true}

// Storefront is the catalog edge — the ONLY place Hanzo Commerce is touched. Publish
// materializes a published product asset's image into the org's storefront (the product
// Listing's headerImage); ProductExists is the cheap read the integrity gate uses to
// reject a content doc that names a product the org's catalog does not have.
type Storefront interface {
	Publish(ctx context.Context, org string, req StorefrontRequest) (StorefrontResult, error)
	// ProductExists reports whether the org's catalog holds a product with this handle
	// (slug). errNotConfigured when the commerce edge is not wired (no token / no
	// reachable commerce) — the integrity gate then SKIPS validation (local dev). A clean
	// (false, nil) is an authoritative "resolved, no such product" the gate fails closed on.
	ProductExists(ctx context.Context, org, handle string) (bool, error)
}

// StorefrontRequest is the provider-agnostic "this asset is the product image" the
// edge acts on. Design is the product slug (the Listing key); ImageURL is the absolute
// S3 URL of the asset file; Kind/Role/Caption are art-direction context.
type StorefrontRequest struct {
	Design   string
	Kind     string
	Role     string
	ImageURL string
	Caption  string
}

// StorefrontResult is the honest outcome recorded on a transition. Status is
// "published" (the product image was set) | "not_configured" (no commerce edge, a
// fail-closed no-op) | "failed" (the edge was reachable but errored) — never fatal.
type StorefrontResult struct {
	Status   string `json:"status"`
	Slug     string `json:"slug,omitempty"`
	Store    string `json:"store,omitempty"`
	ImageURL string `json:"imageUrl,omitempty"`
}

// notConfiguredStorefront is the fail-closed default — used only when a deployment
// explicitly disables the edge (or in tests). The real edge (commerceStorefront) is
// wired at Mount and fail-closes per-call when the service token is absent, exactly
// like the social Distributor; so a production mount always carries the real edge.
type notConfiguredStorefront struct{}

func (notConfiguredStorefront) Publish(context.Context, string, StorefrontRequest) (StorefrontResult, error) {
	return StorefrontResult{}, errNotConfigured
}

func (notConfiguredStorefront) ProductExists(context.Context, string, string) (bool, error) {
	return false, errNotConfigured
}

// commerceStorefront is the REAL Storefront over the Hanzo Commerce store API. It opens
// no store; the catalog state is commerce's. base is deployment config (in-process when
// commerce is co-resident, else the standalone URL); token is the admin S2S bearer,
// read at call time so a late-provisioned deployment arms without a remount; http is the
// self-routing commerce transport (in-process dispatch when co-resident).
type commerceStorefront struct {
	base  string
	token func() string
	http  *http.Client
}

// newStorefront builds the real commerce Storefront. Wired at Mount as the ONE place
// the edge is selected. No secret is captured — the admin token is read from env (which
// a deployment sources from KMS) on every call, so it is never held in a manifest.
func newStorefront() Storefront {
	return commerceStorefront{
		base:  transport.BaseURL(strings.TrimSpace(os.Getenv(commerceURLEnv))),
		token: func() string { return strings.TrimSpace(os.Getenv(commerceTokenEnv)) },
		http:  transport.Client(storefrontTimeout),
	}
}

// Publish upserts the org's product Listing so its headerImage points at the asset's
// S3 URL. It resolves the org's default store, then PUTs the listing keyed by the
// product slug (== design). A missing token or unreachable commerce is the honest
// fail-closed (errNotConfigured); an auth rejection is also not_configured (this
// deployment's token is not admin on the store); any other non-2xx is errUpstream.
func (s commerceStorefront) Publish(ctx context.Context, org string, req StorefrontRequest) (StorefrontResult, error) {
	token := s.token()
	if s.base == "" || token == "" {
		return StorefrontResult{}, errNotConfigured
	}

	storeID, err := s.currentStore(ctx, org, token)
	if err != nil {
		return StorefrontResult{}, err
	}

	// The listing is a DISPLAY OVERRIDE keyed by slug; PUT decodes onto the existing
	// listing (pointer fields), so setting headerImage preserves curated name/price/
	// copy and never clobbers them. slug is set so the listing is self-consistent even
	// when commerce creates it fresh (201).
	body, _ := json.Marshal(map[string]any{
		"slug": req.Design,
		"headerImage": map[string]any{
			"type": "image",
			"url":  req.ImageURL,
			"alt":  req.Caption,
		},
	})
	path := "/v1/store/" + url.PathEscape(storeID) + "/listing/" + url.PathEscape(req.Design)
	status, _, err := s.do(ctx, http.MethodPut, path, org, token, body)
	if err != nil {
		return StorefrontResult{}, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return StorefrontResult{}, errNotConfigured
	}
	if status < 200 || status >= 300 {
		return StorefrontResult{}, fmt.Errorf("%w: listing upsert %d", errUpstream, status)
	}
	return StorefrontResult{Status: "published", Slug: req.Design, Store: storeID, ImageURL: req.ImageURL}, nil
}

// ProductExists resolves a product handle (slug) against the org's catalog via
// GET /v1/product/<handle> (the generic product REST get resolves by slug), pinned to
// the caller org by X-Org-Id behind the admin service token. 2xx ⇒ exists; 404 ⇒
// resolved-but-absent (a real dangling handle); a missing token / unreachable commerce
// or an auth rejection ⇒ errNotConfigured (the gate skips); anything else ⇒ errUpstream.
func (s commerceStorefront) ProductExists(ctx context.Context, org, handle string) (bool, error) {
	token := s.token()
	if s.base == "" || token == "" {
		return false, errNotConfigured
	}
	status, _, err := s.do(ctx, http.MethodGet, "/v1/product/"+url.PathEscape(handle), org, token, nil)
	if err != nil {
		return false, err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return false, errNotConfigured
	case status == http.StatusNotFound:
		return false, nil // resolved: the org's catalog has no such product
	case status >= 200 && status < 300:
		return true, nil
	default:
		return false, fmt.Errorf("%w: product lookup %d", errUpstream, status)
	}
}

// currentStore resolves the org's default store id via GET /v1/store/current (the same
// endpoint the admin dashboard uses). The org is pinned by the X-Org-Id header behind
// the admin service token, so the resolved store is always the caller's own.
func (s commerceStorefront) currentStore(ctx context.Context, org, token string) (string, error) {
	status, raw, err := s.do(ctx, http.MethodGet, "/v1/store/current", org, token, nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", errNotConfigured
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("%w: store/current %d", errUpstream, status)
	}
	var out struct {
		Store struct {
			ID string `json:"id"`
		} `json:"store"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("%w: decode store/current: %v", errUpstream, err)
	}
	id := strings.TrimSpace(out.Store.ID)
	if id == "" || id == "default" {
		// No real store provisioned for this org yet — nothing to attach the image to.
		// Fail-closed (not a 5xx): the org must have a commerce store first.
		return "", errNotConfigured
	}
	return id, nil
}

// do performs one S2S commerce request: admin bearer + X-Org-Id (commerce trusts the
// org header ONLY behind the service token), over the self-routing commerce transport.
// Mirrors clients/account/topup.go commerceDo — the ONE way cloud reaches commerce.
func (s commerceStorefront) do(ctx context.Context, method, path, org, token string, body []byte) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	reqHTTP, err := http.NewRequestWithContext(ctx, method, s.base+path, rdr)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errUpstream, err)
	}
	reqHTTP.Header.Set("Accept", "application/json")
	if body != nil {
		reqHTTP.Header.Set("Content-Type", "application/json")
	}
	reqHTTP.Header.Set("Authorization", "Bearer "+token)
	reqHTTP.Header.Set("X-Org-Id", org)

	resp, err := s.http.Do(reqHTTP)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, nil
}

// StorefrontPublish is the ONE catalog-publish path — the side effect the `published`
// transition of an Asset fires. It returns nil when the item is NOT product imagery
// (not an Asset, no design, or a non-catalog kind) so a transition attaches a
// storefront result ONLY when it is meaningful. A configured/edge error is recorded on
// the result (not returned) — like distribution, it is best-effort and never rolls back
// the status change. org MUST be the validated tenant.
func StorefrontPublish(ctx context.Context, org, doctype, name string) *StorefrontResult {
	s := mounted
	if s == nil {
		return nil
	}
	if doctype != DocTypeAsset {
		return nil // only rendered assets become product imagery
	}
	doc, err := framework.Get(ctx, org, doctype, name)
	if err != nil {
		return nil // the transition already read/updated it; a re-read miss is not fatal
	}
	req, ok := storefrontRequestFromAsset(doc.Data)
	if !ok {
		return nil // not a catalog asset (no design, or kind not in the catalog set)
	}

	res, err := s.State.sf.Publish(ctx, org, req)
	if err != nil {
		return &StorefrontResult{Status: distributionState(err), Slug: req.Design, ImageURL: req.ImageURL}
	}
	return &res
}

// storefrontRequestFromAsset extracts the catalog-publish request from an Asset
// document, or reports ok=false when the asset is not product imagery. The gate is the
// SAME the old pipeline used: a non-empty design (the product slug join key) and a
// catalog kind (ecom/product/lifestyle). The image URL is the asset's `file` object key
// resolved to its public S3 URL.
func storefrontRequestFromAsset(data map[string]any) (StorefrontRequest, bool) {
	design := strings.TrimSpace(dataString(data, "design"))
	kind := strings.TrimSpace(dataString(data, "kind"))
	file := strings.TrimSpace(dataString(data, "file"))
	if design == "" || file == "" || !catalogKinds[kind] {
		return StorefrontRequest{}, false
	}
	return StorefrontRequest{
		Design:   design,
		Kind:     kind,
		Role:     strings.TrimSpace(dataString(data, "role")),
		ImageURL: publicAssetURL(file),
		Caption:  strings.TrimSpace(dataString(data, "caption")),
	}, true
}

// publicAssetURL resolves an Asset `file` to a browser-fetchable URL. A stored value is
// an object KEY under the org's studio output prefix (orgs/<org>/output/...); it is
// joined to the public object-store base. A value that is ALREADY an absolute https URL
// (an externally-hosted render) is returned unchanged.
func publicAssetURL(file string) string {
	if strings.HasPrefix(file, "https://") || strings.HasPrefix(file, "http://") {
		return file
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(assetBaseEnv)), "/")
	if base == "" {
		base = defaultAssetBase
	}
	return base + "/" + strings.TrimLeft(file, "/")
}
