package content

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/commerce/transport"
	"github.com/hanzoai/cloud/apps/framework"
)

// catalog_test.go covers the two live edges of the catalog join (design == slug):
// the Campaign→product integrity gate (enforceCatalogRefs), which fails closed on a
// dangling handle and skips cleanly when commerce is unconfigured, and the real S2S
// product lookup behind it.
//
// It used to open with the REVERSE half of the storefront loop —
// EnsureCatalogAsset, five tests of a mapping whose only caller was the catalogsync
// app. That loop was open at BOTH ends in every deployment: commerce publishes
// product.created only when PUBSUB_URL is set, which nothing sets, and the consumer
// ran in its own process where content's singleton is nil, so the render it existed
// to trigger refused every time. The mapping went with the app. The FORWARD edge
// (storefront.go: a published Asset becomes the product image) is untouched — it
// crosses to commerce over the transport rather than through a package global,
// which is exactly why it works.

// fakeGenerator is a Generator stub that returns a canned Asset field map (or an
// error), so a render can be exercised without a studio backend.
type fakeGenerator struct {
	err   error
	draft map[string]any
}

func (f fakeGenerator) Draft(_ context.Context, _ string, in GenerateInput) (map[string]any, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.draft != nil {
		return f.draft, nil
	}
	return map[string]any{
		"title":  "Asset " + in.Design,
		"design": in.Design,
		"kind":   in.Kind,
		"file":   "orgs/karma/output/" + in.Design + "/ecom.png",
	}, nil
}

// ---- enforceCatalogRefs: Campaign.product integrity gate ----

func TestEnforceCatalogRefs(t *testing.T) {
	_ = mountWith(t, cloud.Deps{})
	sf := &fakeStorefront{exists: map[string]bool{"valentina": true}}
	mounted.State.sf = sf
	ctx := context.Background()

	campaign := func(product string) *framework.Document {
		d := map[string]any{"title": "Spring"}
		if product != "" {
			d["product"] = product
		}
		return &framework.Document{Data: d}
	}

	// Empty product → nothing to validate.
	if err := enforceCatalogRefs(ctx, &framework.Event{Org: "karma", DocType: DocTypeCampaign, Doc: campaign("")}); err != nil {
		t.Errorf("empty product must pass: %v", err)
	}
	// Resolvable product → pass.
	if err := enforceCatalogRefs(ctx, &framework.Event{Org: "karma", DocType: DocTypeCampaign, Doc: campaign("valentina")}); err != nil {
		t.Errorf("resolvable product must pass: %v", err)
	}
	// Dangling product (resolved, absent) → fail closed.
	if err := enforceCatalogRefs(ctx, &framework.Event{Org: "karma", DocType: DocTypeCampaign, Doc: campaign("ghost")}); err == nil {
		t.Error("dangling product must be rejected")
	}
	// Unchanged product on update → the handle is not re-validated (a later catalog change
	// must never wedge an existing campaign's lifecycle), so it passes even though "ghost"
	// does not resolve.
	if err := enforceCatalogRefs(ctx, &framework.Event{Org: "karma", DocType: DocTypeCampaign, Doc: campaign("ghost"), Prev: campaign("ghost")}); err != nil {
		t.Errorf("unchanged product on update must not be re-validated: %v", err)
	}

	// Commerce not configured → skip validation (never block).
	sf.existsErr = errNotConfigured
	if err := enforceCatalogRefs(ctx, &framework.Event{Org: "karma", DocType: DocTypeCampaign, Doc: campaign("ghost")}); err != nil {
		t.Errorf("not-configured commerce must skip validation: %v", err)
	}
	// A transient commerce edge error (not not-configured) also skips — an outage never
	// wedges content authoring (nil Logger is tolerated by the gate).
	sf.existsErr = errUpstream
	if err := enforceCatalogRefs(ctx, &framework.Event{Org: "karma", DocType: DocTypeCampaign, Doc: campaign("ghost")}); err != nil {
		t.Errorf("commerce outage must skip validation, not block: %v", err)
	}
}

// The gate is wired for real: a Campaign create through the framework surface that names a
// dangling product is rejected (422), while one that names a resolvable product succeeds.
func TestCampaignDanglingProductRejectedOnWrite(t *testing.T) {
	app := mountWith(t, cloud.Deps{})
	const org = "karma"
	install(t, app, org)
	mounted.State.sf = &fakeStorefront{exists: map[string]bool{"valentina": true}}

	if code, b := req(t, app, http.MethodPost, "/v1/framework/Campaign", org, map[string]any{
		"title": "Ghost drop", "product": "ghost",
	}); code == http.StatusCreated {
		t.Fatalf("dangling product must be rejected at write, got 201: %s", b)
	}
	if code, b := req(t, app, http.MethodPost, "/v1/framework/Campaign", org, map[string]any{
		"title": "Valentina drop", "product": "valentina",
	}); code != http.StatusCreated {
		t.Fatalf("resolvable product must be accepted, got %d %s", code, b)
	}
}

// ---- commerceStorefront.ProductExists: the real S2S lookup (in-process transport) ----

func TestCommerceStorefrontProductExists(t *testing.T) {
	t.Setenv(commerceTokenEnv, "svc-admin-token")
	t.Setenv(commerceURLEnv, "") // force the in-process placeholder base

	var gotPath, gotOrg, gotAuth string
	transport.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		gotPath, gotOrg, gotAuth = r.URL.Path, r.Header.Get("X-Org-Id"), r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/v1/product/valentina":
			_, _ = io.WriteString(w, `{"slug":"valentina"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not found"}`)
		}
	}))
	t.Cleanup(func() { transport.SetHandler(nil) })

	sf := newStorefront()
	ok, err := sf.ProductExists(context.Background(), "karma", "valentina")
	if err != nil || !ok {
		t.Fatalf("valentina must exist: ok=%v err=%v", ok, err)
	}
	// Tenant-pinned + admin-bearer, exactly like the publish edge.
	if gotPath != "/v1/product/valentina" || gotOrg != "karma" || gotAuth != "Bearer svc-admin-token" {
		t.Errorf("lookup not correctly pinned: path=%q org=%q auth=%q", gotPath, gotOrg, gotAuth)
	}

	// 404 ⇒ resolved-but-absent (a real dangling handle), not an error.
	ok, err = sf.ProductExists(context.Background(), "karma", "ghost")
	if err != nil || ok {
		t.Fatalf("ghost must resolve-absent (ok=false,nil): ok=%v err=%v", ok, err)
	}

	// No service token ⇒ errNotConfigured ⇒ the integrity gate skips.
	t.Setenv(commerceTokenEnv, "")
	if _, err := newStorefront().ProductExists(context.Background(), "karma", "valentina"); err != errNotConfigured {
		t.Fatalf("no token must be errNotConfigured, got %v", err)
	}
}
