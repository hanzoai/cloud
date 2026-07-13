package content

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	"github.com/hanzoai/cloud/clients/framework"
)

// catalog_test.go proves the REVERSE half of the storefront loop: a commerce product
// slug maps to ONE ecom Asset render (EnsureCatalogAsset), idempotently and quietly, and
// the Campaign→product integrity gate (enforceCatalogRefs) fails closed on a dangling
// handle while skipping cleanly when commerce is unconfigured.

// fakeGenerator is a Generator stub that returns a canned Asset field map (or an error),
// so EnsureCatalogAsset can be exercised without a studio backend.
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

// ---- EnsureCatalogAsset: render-on-product-created, idempotent, quiet skips ----

func TestEnsureCatalogAssetCreatesThenIdempotent(t *testing.T) {
	app := mountWith(t, cloud.Deps{})
	const org = "karma"
	install(t, app, org)
	mounted.State.gen = fakeGenerator{}

	res, err := EnsureCatalogAsset(context.Background(), org, "valentina")
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if !res.Created || res.Name == "" || res.Skipped != "" {
		t.Fatalf("first call must create the ecom asset, got %+v", res)
	}

	// The asset is a real draft with the ecom kind, keyed by design == slug.
	doc, err := framework.Get(context.Background(), org, DocTypeAsset, res.Name)
	if err != nil {
		t.Fatalf("read created asset: %v", err)
	}
	if doc.Data["design"] != "valentina" || doc.Data["kind"] != catalogAssetKind {
		t.Fatalf("asset not keyed to product+kind: %+v", doc.Data)
	}

	// Second call is a no-op: a non-archived ecom asset already exists → skip "exists".
	res2, err := EnsureCatalogAsset(context.Background(), org, "valentina")
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if res2.Created || res2.Skipped != "exists" {
		t.Fatalf("second call must be idempotent skip, got %+v", res2)
	}
	if res2.Name != res.Name {
		t.Errorf("idempotent skip should name the existing asset: %q vs %q", res2.Name, res.Name)
	}
}

// An archived asset does NOT suppress a fresh render — a retired shot is not "the asset".
func TestEnsureCatalogAssetIgnoresArchived(t *testing.T) {
	app := mountWith(t, cloud.Deps{})
	const org = "karma"
	install(t, app, org)
	mounted.State.gen = fakeGenerator{}

	first, err := EnsureCatalogAsset(context.Background(), org, "valentina")
	if err != nil || !first.Created {
		t.Fatalf("seed asset: %+v err=%v", first, err)
	}
	// Walk it to archived (draft → archived is a legal edge).
	if _, err := Transition(context.Background(), org, DocTypeAsset, first.Name, StatusArchived, ""); err != nil {
		t.Fatalf("archive: %v", err)
	}
	res, err := EnsureCatalogAsset(context.Background(), org, "valentina")
	if err != nil {
		t.Fatalf("ensure after archive: %v", err)
	}
	if !res.Created || res.Name == first.Name {
		t.Fatalf("archived asset must not block a fresh render, got %+v", res)
	}
}

// A studio that fail-closes (errNotConfigured from Generate) is a quiet SKIP, never an
// error the driving consumer would retry forever.
func TestEnsureCatalogAssetSkipsWhenStudioUnconfigured(t *testing.T) {
	app := mountWith(t, cloud.Deps{})
	const org = "karma"
	install(t, app, org)
	mounted.State.gen = fakeGenerator{err: errNotConfigured}

	res, err := EnsureCatalogAsset(context.Background(), org, "valentina")
	if err != nil {
		t.Fatalf("errNotConfigured must be a quiet skip, got err: %v", err)
	}
	if res.Created || res.Skipped != "not_configured" {
		t.Fatalf("expected not_configured skip, got %+v", res)
	}
}

// A product event for an org that never installed the marketing lane is a quiet skip.
func TestEnsureCatalogAssetSkipsWhenLaneNotInstalled(t *testing.T) {
	_ = mountWith(t, cloud.Deps{})
	res, err := EnsureCatalogAsset(context.Background(), "noinstall", "valentina")
	if err != nil {
		t.Fatalf("not-installed must be a quiet skip, got: %v", err)
	}
	if res.Skipped != "not_installed" {
		t.Fatalf("expected not_installed skip, got %+v", res)
	}
}

// Empty org/slug is a caller error (a real error the consumer logs), not a silent skip.
func TestEnsureCatalogAssetRejectsEmptyInputs(t *testing.T) {
	_ = mountWith(t, cloud.Deps{})
	if _, err := EnsureCatalogAsset(context.Background(), "", "valentina"); err == nil {
		t.Error("empty org must error")
	}
	if _, err := EnsureCatalogAsset(context.Background(), "karma", ""); err == nil {
		t.Error("empty slug must error")
	}
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
	commerceinproc.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	t.Cleanup(func() { commerceinproc.SetHandler(nil) })

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
