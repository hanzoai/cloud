package content

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/clients/commerceinproc"
)

// storefront_test.go proves the ONE catalog seam: a PUBLISHED product Asset surfaces
// as the storefront product image (the commerce Listing headerImage karma.style reads).

// ---- unit: the catalog gate (which assets are product imagery) ----

func TestStorefrontGate_OnlyCatalogAssets(t *testing.T) {
	catalog := func(kind string) map[string]any {
		return map[string]any{"design": "valentina", "kind": kind, "file": "orgs/karma/output/valentina/product_front.png", "role": "front"}
	}
	// The three catalog kinds are accepted (same set the old library pipeline consumed).
	for _, kind := range []string{"ecom", "product", "lifestyle"} {
		if req, ok := storefrontRequestFromAsset(catalog(kind)); !ok || req.Design != "valentina" || req.Kind != kind {
			t.Errorf("kind %q must be a catalog asset, got ok=%v req=%+v", kind, ok, req)
		}
	}
	// Non-catalog kinds, and assets missing the join key / image, are skipped.
	rejects := []map[string]any{
		catalog("hover"), catalog("hero"), catalog("thumbnail"), catalog(""),
		{"design": "", "kind": "product", "file": "x.png"},     // no slug join key
		{"design": "valentina", "kind": "product", "file": ""}, // no image
	}
	for i, d := range rejects {
		if _, ok := storefrontRequestFromAsset(d); ok {
			t.Errorf("reject case %d must be skipped: %+v", i, d)
		}
	}
}

// ---- unit: object key → public S3 URL ----

func TestPublicAssetURL(t *testing.T) {
	t.Setenv(assetBaseEnv, "https://s3.hanzo.ai/hanzo-studio")
	cases := map[string]string{
		"orgs/karma/output/valentina/product_front.png":      "https://s3.hanzo.ai/hanzo-studio/orgs/karma/output/valentina/product_front.png",
		"/orgs/karma/output/x.png":                           "https://s3.hanzo.ai/hanzo-studio/orgs/karma/output/x.png",
		"https://s3.lux.cloud/hanzo-studio/orgs/karma/x.png": "https://s3.lux.cloud/hanzo-studio/orgs/karma/x.png", // already absolute → passthrough
	}
	for in, want := range cases {
		if got := publicAssetURL(in); got != want {
			t.Errorf("publicAssetURL(%q) = %q, want %q", in, got, want)
		}
	}
	// Default base when env is unset.
	t.Setenv(assetBaseEnv, "")
	if got := publicAssetURL("orgs/karma/output/x.png"); got != defaultAssetBase+"/orgs/karma/output/x.png" {
		t.Errorf("default base wrong: %q", got)
	}
}

// ---- a capturing fake Storefront edge ----

type fakeStorefront struct {
	mu     sync.Mutex
	calls  []StorefrontRequest
	result StorefrontResult
	err    error
}

func (f *fakeStorefront) Publish(_ context.Context, _ string, req StorefrontRequest) (StorefrontResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if f.err != nil {
		return StorefrontResult{}, f.err
	}
	r := f.result
	if r.Status == "" {
		r = StorefrontResult{Status: "published", Slug: req.Design, ImageURL: req.ImageURL}
	}
	return r, nil
}

func (f *fakeStorefront) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

// ---- integration: transition Asset → published fans out to the storefront ----

func TestTransitionPublishesCatalogAssetImage(t *testing.T) {
	app := mountContent(t)
	const org = "karma"
	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/marketing/install", org, nil); code != http.StatusOK {
		t.Fatalf("install marketing: %d %s", code, b)
	}

	// Inject a capturing storefront edge (the real one is wired at Mount).
	fake := &fakeStorefront{}
	mounted.State.sf = fake

	// A rendered product Asset (design == the karma product slug), born draft.
	code, b := req(t, app, http.MethodPost, "/v1/framework/Asset", org, map[string]any{
		"title": "Valentina front", "design": "valentina", "kind": "product", "role": "front",
		"file": "orgs/karma/output/valentina/product_front.png", "caption": "Valentina",
	})
	if code != http.StatusCreated {
		t.Fatalf("create asset: %d %s", code, b)
	}
	var created struct{ Name string }
	_ = json.Unmarshal(b, &created)
	name := created.Name
	tpath := "/v1/content/Asset/" + name + "/transition"

	// Walk the legal path to published. Only the final edge distributes.
	var last TransitionResult
	for _, to := range []string{StatusInReview, StatusApproved, StatusPublished} {
		code, b := req(t, app, http.MethodPost, tpath, org, map[string]any{"to": to})
		if code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, b)
		}
		_ = json.Unmarshal(b, &last)
	}

	// The published edge fanned the asset out to the storefront exactly once.
	if fake.count() != 1 {
		t.Fatalf("storefront must be called exactly once on publish, got %d", fake.count())
	}
	gotReq := fake.calls[0]
	if gotReq.Design != "valentina" {
		t.Errorf("storefront req slug wrong: %+v", gotReq)
	}
	wantURL := "https://s3.hanzo.ai/hanzo-studio/orgs/karma/output/valentina/product_front.png"
	if gotReq.ImageURL != wantURL {
		t.Errorf("storefront req image url = %q, want %q", gotReq.ImageURL, wantURL)
	}
	if last.Storefront == nil || last.Storefront.Status != "published" {
		t.Fatalf("transition must record a published storefront result, got: %+v", last.Storefront)
	}
}

// A non-catalog Asset (kind not in the catalog set) is NOT pushed to the storefront —
// the transition simply attaches no storefront result.
func TestTransitionSkipsNonCatalogAsset(t *testing.T) {
	app := mountContent(t)
	const org = "karma"
	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/marketing/install", org, nil); code != http.StatusOK {
		t.Fatalf("install: %d %s", code, b)
	}
	fake := &fakeStorefront{}
	mounted.State.sf = fake

	code, b := req(t, app, http.MethodPost, "/v1/framework/Asset", org, map[string]any{
		"title": "hover", "design": "valentina", "kind": "hover",
		"file": "orgs/karma/output/valentina/hover_front.png",
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, b)
	}
	var created struct{ Name string }
	_ = json.Unmarshal(b, &created)
	tpath := "/v1/content/Asset/" + created.Name + "/transition"
	var last TransitionResult
	for _, to := range []string{StatusInReview, StatusApproved, StatusPublished} {
		_, b := req(t, app, http.MethodPost, tpath, org, map[string]any{"to": to})
		_ = json.Unmarshal(b, &last)
	}
	if fake.count() != 0 {
		t.Fatalf("non-catalog asset must NOT hit the storefront, got %d calls", fake.count())
	}
	if last.Storefront != nil {
		t.Fatalf("non-catalog transition must attach no storefront result, got %+v", last.Storefront)
	}
}

// With no commerce edge configured (no service token, no co-resident handler), a
// published catalog asset records an honest "not_configured" storefront status and the
// status change still succeeds — never a 5xx, never a rollback.
func TestTransitionStorefrontFailClosed(t *testing.T) {
	commerceinproc.SetHandler(nil) // ensure no co-resident commerce
	t.Setenv(commerceTokenEnv, "")
	t.Setenv(commerceURLEnv, "")

	app := mountContent(t)
	const org = "karma"
	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/marketing/install", org, nil); code != http.StatusOK {
		t.Fatalf("install: %d %s", code, b)
	}
	// Use the REAL storefront edge (default from Mount) — it must fail closed.
	code, b := req(t, app, http.MethodPost, "/v1/framework/Asset", org, map[string]any{
		"title": "v", "design": "valentina", "kind": "product",
		"file": "orgs/karma/output/valentina/product_front.png",
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, b)
	}
	var created struct{ Name string }
	_ = json.Unmarshal(b, &created)
	tpath := "/v1/content/Asset/" + created.Name + "/transition"
	var last TransitionResult
	for _, to := range []string{StatusInReview, StatusApproved, StatusPublished} {
		code, b := req(t, app, http.MethodPost, tpath, org, map[string]any{"to": to})
		if code != http.StatusOK {
			t.Fatalf("transition →%s must still succeed, got %d %s", to, code, b)
		}
		_ = json.Unmarshal(b, &last)
	}
	if last.Storefront == nil || last.Storefront.Status != "not_configured" {
		t.Fatalf("fail-closed must record not_configured, got %+v", last.Storefront)
	}
	// The status change committed regardless.
	if _, b := req(t, app, http.MethodGet, "/v1/content/board?status=published&doctype=Asset", org, nil); !strings.Contains(string(b), created.Name) {
		t.Fatalf("asset must be published despite storefront being unconfigured: %s", b)
	}
}

// ---- integration: the REAL commerce S2S edge (over the in-process transport) ----

// TestCommerceStorefrontWire proves the real edge speaks the exact commerce contract:
// resolve the org's store (GET /v1/store/current), then upsert the product Listing
// (PUT /v1/store/:id/listing/:slug) with headerImage.url = the asset S3 URL — every
// call admin-bearer + X-Org-Id pinned to the caller's own org (tenant isolation).
func TestCommerceStorefrontWire(t *testing.T) {
	t.Setenv(commerceTokenEnv, "svc-admin-token")
	t.Setenv(commerceURLEnv, "") // force the in-process placeholder base

	var (
		gotCurrentOrg, gotCurrentAuth string
		gotListingPath, gotListingOrg string
		gotBody                       map[string]any
	)
	commerceinproc.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/store/current":
			gotCurrentOrg = r.Header.Get("X-Org-Id")
			gotCurrentAuth = r.Header.Get("Authorization")
			_, _ = io.WriteString(w, `{"store":{"id":"STORE123","name":"Karma"}}`)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/store/STORE123/listing/"):
			gotListingPath = r.URL.Path
			gotListingOrg = r.Header.Get("X-Org-Id")
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &gotBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"unexpected"}`)
		}
	}))
	t.Cleanup(func() { commerceinproc.SetHandler(nil) })

	const org = "karma"
	const imgURL = "https://s3.hanzo.ai/hanzo-studio/orgs/karma/output/valentina/product_front.png"
	res, err := newStorefront().Publish(context.Background(), org, StorefrontRequest{
		Design: "valentina", Kind: "product", Role: "front", ImageURL: imgURL, Caption: "Valentina",
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.Status != "published" || res.Store != "STORE123" || res.Slug != "valentina" || res.ImageURL != imgURL {
		t.Fatalf("result wrong: %+v", res)
	}
	// Store resolution: admin bearer + caller org.
	if gotCurrentAuth != "Bearer svc-admin-token" {
		t.Errorf("store/current auth = %q, want admin bearer", gotCurrentAuth)
	}
	if gotCurrentOrg != org {
		t.Errorf("store/current X-Org-Id = %q, want %q", gotCurrentOrg, org)
	}
	// Listing upsert: keyed by slug, pinned to the caller org.
	if gotListingPath != "/v1/store/STORE123/listing/valentina" {
		t.Errorf("listing path = %q", gotListingPath)
	}
	if gotListingOrg != org {
		t.Errorf("listing X-Org-Id = %q, want %q (tenant leak)", gotListingOrg, org)
	}
	if gotBody["slug"] != "valentina" {
		t.Errorf("listing body slug = %v", gotBody["slug"])
	}
	hi, _ := gotBody["headerImage"].(map[string]any)
	if hi == nil || hi["url"] != imgURL || hi["type"] != "image" {
		t.Fatalf("listing headerImage wrong: %v", gotBody["headerImage"])
	}
}

// An org with no provisioned store (store/current returns the "default" placeholder)
// fails closed — there is nowhere to attach the image, so it is not_configured, not a 5xx.
func TestCommerceStorefrontNoStore(t *testing.T) {
	t.Setenv(commerceTokenEnv, "svc-admin-token")
	t.Setenv(commerceURLEnv, "")
	commerceinproc.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"store":{"id":"default","name":"Default Store"}}`)
	}))
	t.Cleanup(func() { commerceinproc.SetHandler(nil) })

	_, err := newStorefront().Publish(context.Background(), "karma", StorefrontRequest{
		Design: "valentina", Kind: "product", ImageURL: "https://s3.hanzo.ai/hanzo-studio/orgs/karma/output/x.png",
	})
	if err != errNotConfigured {
		t.Fatalf("no-store must be errNotConfigured, got %v", err)
	}
}
