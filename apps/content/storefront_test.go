package content

import (
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/commerce/transport"
)

// storefront_test.go proves the ONE catalog client: a PUBLISHED product Asset surfaces
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

	// ProductExists behavior: exists is the set of handles the catalog "has"; existsErr,
	// when set, is returned instead (e.g. errNotConfigured to prove the gate skips).
	exists    map[string]bool
	existsErr error
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

func (f *fakeStorefront) ProductExists(_ context.Context, _ string, handle string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.exists[handle], nil
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
	code, b := req(t, app, http.MethodPost, "/v1/framework/"+DocTypeAsset.String(), org, map[string]any{
		"title": "Valentina front", "design": "valentina", "kind": "product", "role": "front",
		"file": "orgs/karma/output/valentina/product_front.png", "caption": "Valentina",
	})
	if code != http.StatusCreated {
		t.Fatalf("create asset: %d %s", code, b)
	}
	var created struct{ Name string }
	_ = json.Unmarshal(b, &created)
	name := created.Name
	tpath := "/v1/content/" + DocTypeAsset.String() + "/" + name + "/transition"

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

	code, b := req(t, app, http.MethodPost, "/v1/framework/"+DocTypeAsset.String(), org, map[string]any{
		"title": "hover", "design": "valentina", "kind": "hover",
		"file": "orgs/karma/output/valentina/hover_front.png",
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, b)
	}
	var created struct{ Name string }
	_ = json.Unmarshal(b, &created)
	tpath := "/v1/content/" + DocTypeAsset.String() + "/" + created.Name + "/transition"
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

// With no commerce edge configured (no URL, no co-resident handler), a
// published catalog asset records an honest "not_configured" storefront status and the
// status change still succeeds — never a 5xx, never a rollback.
func TestTransitionStorefrontFailClosed(t *testing.T) {
	transport.SetHandler(nil) // ensure no co-resident commerce
	t.Setenv(commerceURLEnv, "")

	app := mountContent(t)
	const org = "karma"
	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/marketing/install", org, nil); code != http.StatusOK {
		t.Fatalf("install: %d %s", code, b)
	}
	// Use the REAL storefront edge (default from Mount) — it must fail closed.
	code, b := req(t, app, http.MethodPost, "/v1/framework/"+DocTypeAsset.String(), org, map[string]any{
		"title": "v", "design": "valentina", "kind": "product",
		"file": "orgs/karma/output/valentina/product_front.png",
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, b)
	}
	var created struct{ Name string }
	_ = json.Unmarshal(b, &created)
	tpath := "/v1/content/" + DocTypeAsset.String() + "/" + created.Name + "/transition"
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
	if _, b := req(t, app, http.MethodGet, "/v1/content/board?status=published&doctype="+DocTypeAsset.String(), org, nil); !strings.Contains(string(b), created.Name) {
		t.Fatalf("asset must be published despite storefront being unconfigured: %s", b)
	}
}

// ---- integration: the REAL commerce S2S edge (over the in-process transport) ----

// TestCommerceStorefrontWire proves the real edge speaks the exact commerce contract:
// resolve the org's store (GET /v1/commerce/store/current), then upsert the product Listing
// (PUT /v1/commerce/store/:id/listing/:slug) with headerImage.url = the asset S3 URL — every
// call admin-bearer + X-Org-Id pinned to the caller's own org (tenant isolation).
func TestCommerceStorefrontWire(t *testing.T) {
	// A commerce peer on commerce's own socket — the arrangement production has,
	// now that both halves of Publish ask by name instead of re-entering
	// commerce's HTTP endpoint.
	//
	// The assertions that went with that endpoint are gone because the facts they
	// checked are no longer carried the same way: there is no Authorization
	// header to inspect, and no X-Org-Id, because identity rides the CALLER on the
	// plane. What replaces them is stronger — the op reads the org out of the
	// call itself, so a tenant leak would have to forge a capability rather than
	// a header.
	dir, err := os.MkdirTemp("", "sf")
	if err != nil {
		t.Fatalf("run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	var (
		gotStoreOrg, gotListingOrg string
		gotKey, gotStoreID         string
		gotBody                    map[string]any
	)
	app := zip.New(zip.Config{AppName: "commerce"})
	zip.Post[client.StoreIn, client.Store](app, "/store/current",
		func(ctx context.Context, _ *client.StoreIn) (*client.Store, error) {
			gotStoreOrg = cloud.Who(ctx).Org
			return &client.Store{ID: "STORE123", Name: "Karma"}, nil
		}, zip.WithOperationID(client.StoreCurrent))
	zip.Post[client.ListingIn, client.Listed](app, "/store/listing",
		func(ctx context.Context, in *client.ListingIn) (*client.Listed, error) {
			gotListingOrg = cloud.Who(ctx).Org
			gotStoreID, gotKey = in.StoreID, in.Key
			_ = json.Unmarshal(in.Patch, &gotBody)
			return &client.Listed{Existed: false}, nil
		}, zip.WithOperationID(client.StoreListing))
	go func() { _ = app.Listen(zip.SocketPath("commerce")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for range 400 {
		if c, derr := net.DialTimeout("unix", zip.SocketPath("commerce"), time.Second); derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

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
	// The tenant, on BOTH ops. It rides the capability rather than a header, so
	// this is the leak that would matter.
	if gotStoreOrg != org {
		t.Errorf("store/current ran for org %q, want %q", gotStoreOrg, org)
	}
	if gotListingOrg != org {
		t.Errorf("listing ran for org %q, want %q (tenant leak)", gotListingOrg, org)
	}
	// The listing is written to the store that was just resolved, keyed by slug.
	if gotStoreID != "STORE123" || gotKey != "valentina" {
		t.Errorf("listing targeted store %q key %q", gotStoreID, gotKey)
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
	t.Setenv(commerceURLEnv, "")
	transport.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"store":{"id":"default","name":"Default Store"}}`)
	}))
	t.Cleanup(func() { transport.SetHandler(nil) })

	_, err := newStorefront().Publish(context.Background(), "karma", StorefrontRequest{
		Design: "valentina", Kind: "product", ImageURL: "https://s3.hanzo.ai/hanzo-studio/orgs/karma/output/x.png",
	})
	if err != errNotConfigured {
		t.Fatalf("no-store must be errNotConfigured, got %v", err)
	}
}
