package taxonomy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountApp brings the surface up on a bare app the way the composer does, over a
// store seeded from the embedded catalogue — so every test below drives the real
// router against the real first-boot contents.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(t.Context()) })
	return app
}

// anon is a signed-out visitor; super is a platform SuperAdmin. X-User-Id is
// minted only from a verified credential, and X-User-IsAdmin only for a member of
// the reserved admin org, so these two headers are the whole difference.
var (
	anon  = map[string]string{}
	super = map[string]string{"X-User-Id": "root", "X-Org-Id": "admin", "X-User-IsAdmin": "true"}
	// member is signed in and NOT platform sudo — the case that separates
	// cloud.Super from "any validated principal".
	member = map[string]string{"X-User-Id": "dave", "X-Org-Id": "acme"}
	// orgAdmin administers its OWN org: it writes acme's rows and nothing else.
	orgAdmin = map[string]string{"X-User-Id": "dave", "X-Org-Id": "acme", "X-User-IsOrgAdmin": "true"}
	// Two tenants, to prove the boundary between them rather than assert it.
	adminA  = map[string]string{"X-User-Id": "ann", "X-Org-Id": "acme", "X-User-IsOrgAdmin": "true"}
	memberA = map[string]string{"X-User-Id": "al", "X-Org-Id": "acme"}
	adminB  = map[string]string{"X-User-Id": "bob", "X-Org-Id": "globex", "X-User-IsOrgAdmin": "true"}
	memberB = map[string]string{"X-User-Id": "bea", "X-Org-Id": "globex"}
	// An admin of the PLATFORM's own org is still not platform sudo.
	hanzoAdmin = map[string]string{"X-User-Id": "hal", "X-Org-Id": "hanzo", "X-User-IsOrgAdmin": "true"}
)

func do(t *testing.T, app *zip.App, method, path string, who map[string]string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range who {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, out
}

func read(t *testing.T, app *zip.App, path string, who map[string]string) Taxonomy {
	t.Helper()
	code, body := do(t, app, http.MethodGet, path, who, nil)
	if code != http.StatusOK {
		t.Fatalf("GET %s: %d (%s)", path, code, body)
	}
	var got Taxonomy
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// ── the seed round-trips ────────────────────────────────────────────────────

// TestTheConsoleRegistryArrivesWhole is the reason this app exists: the console's
// hardcoded registry — 186 products across 14 categories — has to come out of the
// API exactly as it went in, or the move lost data. It counts what the seed
// document declares and what the SURFACE serves, and requires them equal, so a
// store or a projection that silently drops rows fails here rather than in the
// console.
func TestTheConsoleRegistryArrivesWhole(t *testing.T) {
	var doc catalogue
	if err := json.Unmarshal(seedJSON, &doc); err != nil {
		t.Fatalf("decode seed: %v", err)
	}
	if len(doc.Categories) != 14 || len(doc.Taxa) != 186 {
		t.Fatalf("the seed carries %d categories / %d taxa, not the 14 / 186 lifted from the registry",
			len(doc.Categories), len(doc.Taxa))
	}

	got := read(t, mountApp(t), "/v1/taxonomy", anon)
	if len(got.Categories) != 14 {
		t.Fatalf("the surface serves %d categories, want 14", len(got.Categories))
	}
	var served int
	for _, c := range got.Categories {
		served += len(c.Taxa)
	}
	if served != 186 {
		t.Fatalf("the surface serves %d taxa, want 186", served)
	}

	// Category BY category, not just the total: two categories that swapped 3
	// taxa each would sum to 186 and still be wrong.
	want := map[string]int{}
	for _, e := range doc.Taxa {
		want[e.Category]++
	}
	for _, c := range got.Categories {
		if len(c.Taxa) != want[c.ID] {
			t.Errorf("category %q serves %d taxa, seeded with %d", c.ID, len(c.Taxa), want[c.ID])
		}
	}
	// And every id, so a duplicate cannot hide a loss inside a matching count.
	ids := map[string]bool{}
	for _, c := range got.Categories {
		for _, e := range c.Taxa {
			if ids[e.ID] {
				t.Errorf("taxon %q served twice", e.ID)
			}
			ids[e.ID] = true
		}
	}
	for _, e := range doc.Taxa {
		if !ids[e.ID] {
			t.Errorf("taxon %q was seeded and is not served", e.ID)
		}
	}
}

// TestTheCatalogueKeepsItsOrder pins the display order, which is the whole point
// of moving the list: the categories come back in their declared order and each
// category's taxa in theirs. A store that returned insertion order, or the
// order SQLite happened to scan, would render a shuffled console.
func TestTheCatalogueKeepsItsOrder(t *testing.T) {
	got := read(t, mountApp(t), "/v1/taxonomy", anon)
	if !sort.SliceIsSorted(got.Categories, func(i, j int) bool {
		return got.Categories[i].Order < got.Categories[j].Order
	}) {
		t.Error("categories are not served in display order")
	}
	if got.Categories[0].ID != "ai" {
		t.Errorf("the first category is %q; the registry's categoryOrder opens with ai", got.Categories[0].ID)
	}
	for _, c := range got.Categories {
		if !sort.SliceIsSorted(c.Taxa, func(i, j int) bool { return c.Taxa[i].Order < c.Taxa[j].Order }) {
			t.Errorf("category %q serves its taxa out of display order", c.ID)
		}
	}
}

// TestSeedingIsFirstBootOnly is the guard on the failure mode a self-healing
// default produces: an editor deletes a product, the pod restarts, and the
// product is back. The seed fills an empty store and never speaks again.
func TestSeedingIsFirstBootOnly(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	n, err := seed(t.Context(), store)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n != 186 {
		t.Fatalf("first seed wrote %d taxa, want 186", n)
	}
	if _, err := store.DeleteTaxon(t.Context(), hanzo, "overview"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	again, err := seed(t.Context(), store)
	if err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if again != 0 {
		t.Fatalf("the seed rewrote %d taxa into a populated store", again)
	}
	taxa, err := store.Taxa(t.Context(), hanzo, "")
	if err != nil {
		t.Fatalf("taxa: %v", err)
	}
	if len(taxa) != 185 {
		t.Fatalf("after a delete + re-seed the store holds %d taxa, want 185 — the seed undid an edit", len(taxa))
	}
	_ = store.Close()
}

// ── the gate ────────────────────────────────────────────────────────────────

// TestTheCatalogueReadsSignedOut is the property the marketing landing depends on:
// no credential, a full catalogue. A read that needed a token would leave the
// public site with nothing to render.
func TestTheCatalogueReadsSignedOut(t *testing.T) {
	got := read(t, mountApp(t), "/v1/taxonomy", anon)
	if len(got.Categories) == 0 {
		t.Fatal("a signed-out read returned no categories")
	}
}

// TestAWriteNeedsAnAdmin. Reading the catalogue is open; changing it is not. A
// caller with no admin standing at all — signed out, or signed in as a plain
// member of their org — is refused every write, and nothing they send lands
// anywhere. This USED to also assert 403 for an org admin, back when there was one
// global catalogue; an org admin now has a catalogue of their own to edit, and
// TestAnOrgAdminCannotWriteThePlatform is where the line they still cannot cross
// is pinned.
func TestAWriteNeedsAnAdmin(t *testing.T) {
	app := mountApp(t)
	cat := map[string]any{"label": "Invented", "order": 99}
	taxon := map[string]any{"name": "Invented", "category": "data", "route": "/invented"}

	for _, w := range []struct {
		name string
		who  map[string]string
	}{{"anonymous", anon}, {"member", member}} {
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{http.MethodPut, "/v1/taxonomy/categories/invented", cat},
			{http.MethodDelete, "/v1/taxonomy/categories/ai", nil},
			{http.MethodPut, "/v1/taxonomy/taxa/invented", taxon},
			{http.MethodDelete, "/v1/taxonomy/taxa/overview", nil},
		} {
			code, body := do(t, app, c.method, c.path, w.who, c.body)
			if code != http.StatusForbidden {
				t.Errorf("%s %s as %s: %d (%s), want 403", c.method, c.path, w.name, code, body)
			}
		}
	}
	// Nothing above changed anything, for anyone.
	got := read(t, app, "/v1/taxonomy", anon)
	if len(got.Categories) != 14 || len(taxaOf(got)) != 186 {
		t.Fatalf("a refused write still landed: %d categories / %d taxa",
			len(got.Categories), len(taxaOf(got)))
	}
}

// TestUnpublishedIsForTheEditorAlone pins the one fact the read varies on. Hiding a
// product must actually hide it, and the person who hid it must still be able to
// see it — otherwise unpublishing is a one-way door.
func TestUnpublishedIsForTheEditorAlone(t *testing.T) {
	app := mountApp(t)
	hidden := false
	body := map[string]any{
		"name": "Staged", "description": "not yet", "category": "data",
		"icon": "Box", "route": "/staged", "published": &hidden,
	}
	if code, b := do(t, app, http.MethodPut, "/v1/taxonomy/taxa/staged", super, body); code != http.StatusOK {
		t.Fatalf("put: %d (%s)", code, b)
	}
	if has(read(t, app, "/v1/taxonomy", anon), "staged") {
		t.Error("a signed-out visitor was served an unpublished taxon")
	}
	if !has(read(t, app, "/v1/taxonomy", super), "staged") {
		t.Error("the editor cannot see the taxon it staged — unpublishing would be a one-way door")
	}
}

// ── the boundary ────────────────────────────────────────────────────────────

// TestTheQueryStringCannotRedirectAWrite is the pin on `url:"-"`. zip's binder
// fills an In field from the QUERY as well as the body, so a body field left
// URL-bindable silently starts accepting `?name=` — and a link could then rename a
// product the body never mentioned.
func TestTheQueryStringCannotRedirectAWrite(t *testing.T) {
	app := mountApp(t)
	body := map[string]any{"name": "Asked For", "category": "data", "route": "/asked"}
	code, out := do(t, app, http.MethodPut,
		"/v1/taxonomy/taxa/asked?name=Hijacked&category=web3&route=/hijacked", super, body)
	if code != http.StatusOK {
		t.Fatalf("put: %d (%s)", code, out)
	}
	var got Taxon
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "Asked For" || got.Category != "data" || got.Route != "/asked" {
		t.Fatalf("the query string overwrote the body: %+v", got)
	}
}

// TestThePathOwnsTheIdentity: the URL is the addressing authority, so a body that
// names a different id is filed under the one it was addressed by — which is also
// what lets one verb both create and replace.
func TestThePathOwnsTheIdentity(t *testing.T) {
	app := mountApp(t)
	body := map[string]any{"id": "elsewhere", "name": "Here", "category": "data", "route": "/here"}
	if code, out := do(t, app, http.MethodPut, "/v1/taxonomy/taxa/here", super, body); code != http.StatusOK {
		t.Fatalf("put: %d (%s)", code, out)
	}
	got := read(t, app, "/v1/taxonomy", super)
	if !has(got, "here") {
		t.Error("the taxon was not filed under the id in the path")
	}
	if has(got, "elsewhere") {
		t.Error("the body's id created a row: a product can be written under a name it was not addressed by")
	}
}

// TestATaxonNamesARealCategory keeps the catalogue renderable: a taxon filed
// under a category that does not exist appears nowhere and is findable by nobody.
func TestATaxonNamesARealCategory(t *testing.T) {
	app := mountApp(t)
	body := map[string]any{"name": "Orphan", "category": "nosuch", "route": "/orphan"}
	if code, out := do(t, app, http.MethodPut, "/v1/taxonomy/taxa/orphan", super, body); code != http.StatusBadRequest {
		t.Fatalf("put with an unknown category: %d (%s), want 400", code, out)
	}
}

// TestATaxonOpensExactlyOneWay. A product the console renders has a route; one
// that lives at its own domain has an href. Both is a contradiction and neither is
// a tile that does nothing when clicked.
func TestATaxonOpensExactlyOneWay(t *testing.T) {
	app := mountApp(t)
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"neither", map[string]any{"name": "X", "category": "data"}},
		{"both", map[string]any{"name": "X", "category": "data", "route": "/x", "href": "https://x.example"}},
	} {
		if code, out := do(t, app, http.MethodPut, "/v1/taxonomy/taxa/x", super, tc.body); code != http.StatusBadRequest {
			t.Errorf("%s: %d (%s), want 400", tc.name, code, out)
		}
	}
}

// TestDeletingAFullCategoryIsRefused. Removing the label off a group must not take
// the products wearing it, and must not leave them naming a category that is gone.
func TestDeletingAFullCategoryIsRefused(t *testing.T) {
	app := mountApp(t)
	code, out := do(t, app, http.MethodDelete, "/v1/taxonomy/categories/data", super, nil)
	if code != http.StatusConflict {
		t.Fatalf("delete a populated category: %d (%s), want 409", code, out)
	}
	if !strings.Contains(string(out), "9") {
		t.Errorf("the refusal does not say how many taxa stand in the way: %s", out)
	}
	got := read(t, app, "/v1/taxonomy", anon)
	var n int
	for _, c := range got.Categories {
		n += len(c.Taxa)
	}
	if n != 186 {
		t.Fatalf("the refused delete still removed rows: %d taxa", n)
	}
}

// TestAnEmptyCategoryDeletes — the other half, so the refusal above is a rule and
// not a wall.
func TestAnEmptyCategoryDeletes(t *testing.T) {
	app := mountApp(t)
	if code, out := do(t, app, http.MethodPut, "/v1/taxonomy/categories/spare",
		super, map[string]any{"label": "Spare"}); code != http.StatusOK {
		t.Fatalf("put: %d (%s)", code, out)
	}
	if code, out := do(t, app, http.MethodDelete, "/v1/taxonomy/categories/spare", super, nil); code != http.StatusOK {
		t.Fatalf("delete an empty category: %d (%s)", code, out)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/taxonomy/categories/spare", super, nil); code != http.StatusNotFound {
		t.Error("deleting a category twice does not 404")
	}
}

// TestBrandNarrowsTheCatalogue proves the per-brand scope survived the move: a
// sovereign-chain console shows the web3/admin sections and none of the AI-cloud
// ones, which is exactly what BRAND_CATEGORIES said in the TypeScript.
func TestBrandNarrowsTheCatalogue(t *testing.T) {
	app := mountApp(t)
	lux := read(t, app, "/v1/taxonomy?brand=lux", anon)
	var ids []string
	for _, c := range lux.Categories {
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	want := []string{"dev", "network", "security", "settings", "web3"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("brand=lux shows %v, want %v", ids, want)
	}
	// Taxon scope is the orthogonal half: the Web3 category holds both the Lux and
	// the Zoo launch tiles, and neither brand may see the other's.
	for _, c := range lux.Categories {
		for _, e := range c.Taxa {
			if len(e.Brands) > 0 && !shows("lux", e.Brands) {
				t.Errorf("taxon %q leaked into brand=lux with scope %v", e.ID, e.Brands)
			}
		}
	}
	if n := len(read(t, app, "/v1/taxonomy", anon).Categories); n != 14 {
		t.Fatalf("an unscoped read shows %d categories, want all 14", n)
	}
}

// TestIdsAreSlugs — an id is a URL segment, a console route and a JSON key at
// once, so it is validated at the door rather than escaped at three call sites.
func TestIdsAreSlugs(t *testing.T) {
	app := mountApp(t)
	// %20 rather than a literal space: the space is what must be refused, and an
	// unencoded one is refused by net/http before the router ever sees it.
	for _, bad := range []string{"has%20space", "UPPER", "punc!", "under_score", "dot.dot"} {
		code, _ := do(t, app, http.MethodPut, "/v1/taxonomy/taxa/"+bad, super,
			map[string]any{"name": "X", "category": "data", "route": "/x"})
		if code != http.StatusBadRequest {
			t.Errorf("id %q was accepted (%d)", bad, code)
		}
	}
}

// ── the projections ─────────────────────────────────────────────────────────

// surfaceOps reads BOTH projections of the LIVE router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry with prose. Reading the router rather than the source is what
// makes this a gate and not a promise.
func surfaceOps(t *testing.T) (served map[string]bool, typed map[string]*openapi.Operation) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "taxonomy", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/taxonomy") }
	served, typed = map[string]bool{}, map[string]*openapi.Operation{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op
		}
	}
	return served, typed
}

// TestEveryRouteIsATypedOp — a typed op is ONE registry entry that is at once the
// route, the OpenAPI operation, the MCP tool, the CLI command and the SDK method.
// An untyped route is a route and nothing else.
func TestEveryRouteIsATypedOp(t *testing.T) {
	served, typed := surfaceOps(t)
	if len(served) != 5 {
		t.Fatalf("the surface serves %d operations, not the 5 this ledger knows: %v", len(served), keys(served))
	}
	for key := range served {
		if _, ok := typed[key]; !ok {
			t.Errorf("%s has no registry entry — this API publishes a route and nothing else", key)
		}
	}
}

// TestEveryTypedOpIsDescribed — the fleet's describe gate refuses a document whose
// operation says nothing about itself. This catches it in the package that owns the
// handler, where the doc comment that fixes it lives.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := surfaceOps(t)
	if len(typed) != 5 {
		t.Fatalf("the registry carries %d operations, want 5", len(typed))
	}
	for key, op := range typed {
		if strings.TrimSpace(op.Description) == "" {
			t.Errorf("%s publishes NO description — the OpenAPI prose and the MCP tool description are both empty", key)
		}
		if strings.TrimSpace(op.Summary) == "" {
			t.Errorf("%s publishes NO summary — the CLI help and every SDK docstring are empty", key)
		}
		if strings.Contains(op.Summary, "\n") {
			t.Errorf("%s has a line break in its one-line summary: %q", key, op.Summary)
		}
	}
}

// TestTheCollectionRootHasNoTrailingSlash. Registering the root as an empty leaf on
// the group would name /v1/taxonomy/ — a path this API has never served — in the
// document, the operationId, the MCP tool and every generated SDK's URL.
func TestTheCollectionRootHasNoTrailingSlash(t *testing.T) {
	served, _ := surfaceOps(t)
	if !served["GET /v1/taxonomy"] {
		t.Errorf("the catalogue read is not served at /v1/taxonomy: %v", keys(served))
	}
	for key := range served {
		if strings.HasSuffix(key, "/") {
			t.Errorf("%s publishes a trailing slash", key)
		}
	}
}

func has(tx Taxonomy, id string) bool {
	for _, c := range tx.Categories {
		for _, e := range c.Taxa {
			if e.ID == id {
				return true
			}
		}
	}
	return false
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestACategoryWriteAnswersWithWhatIsFiledUnderIt. A PUT returns the row as
// stored, and a category's row is only half the truth — an editor that renamed a
// full category and was told it now holds nothing would render it empty.
func TestACategoryWriteAnswersWithWhatIsFiledUnderIt(t *testing.T) {
	app := mountApp(t)
	code, out := do(t, app, http.MethodPut, "/v1/taxonomy/categories/data", super,
		map[string]any{"label": "Data (renamed)", "order": 3})
	if code != http.StatusOK {
		t.Fatalf("put: %d (%s)", code, out)
	}
	var got Category
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Label != "Data (renamed)" {
		t.Fatalf("label not stored: %q", got.Label)
	}
	if len(got.Taxa) != 8 {
		t.Fatalf("the write answered with %d taxa; data holds 8", len(got.Taxa))
	}
}

// TestALaunchTileGoesSomewhere. A route resolves against whatever page the reader
// is on unless it is rooted, and an href that is not absolute is a tile that opens
// nothing — both render as a dead link long after the write succeeded. The seed
// carries the same rule, which is what caught `ext.api` arriving as its own
// expression text rather than the URL it names.
func TestALaunchTileGoesSomewhere(t *testing.T) {
	app := mountApp(t)
	for _, tc := range []struct{ name, field, value string }{
		{"unrooted route", "route", "vector"},
		{"relative href", "href", "docs.hanzo.ai/api"},
		{"unresolved reference", "href", "ext.api"},
	} {
		body := map[string]any{"name": "X", "category": "data", tc.field: tc.value}
		if code, out := do(t, app, http.MethodPut, "/v1/taxonomy/taxa/x", super, body); code != http.StatusBadRequest {
			t.Errorf("%s: %d (%s), want 400", tc.name, code, out)
		}
	}
	// Every seeded external tile is an absolute URL, and every module a rooted path.
	for _, c := range read(t, app, "/v1/taxonomy", anon).Categories {
		for _, e := range c.Taxa {
			if e.Href != "" && !strings.HasPrefix(e.Href, "https://") {
				t.Errorf("seeded taxon %q has href %q", e.ID, e.Href)
			}
			if e.Route != "" && !strings.HasPrefix(e.Route, "/") {
				t.Errorf("seeded taxon %q has route %q", e.ID, e.Route)
			}
		}
	}
}

// ── tenancy ─────────────────────────────────────────────────────────────────

// twoTenants seeds one row for org acme and one for org globex, each written by
// that org's own admin through the real surface — so the rows exist the only way a
// customer row ever comes to exist.
func twoTenants(t *testing.T) *zip.App {
	t.Helper()
	app := mountApp(t)
	for _, w := range []struct {
		who  map[string]string
		id   string
		name string
	}{{adminA, "acme-crm", "Acme CRM"}, {adminB, "globex-crm", "Globex CRM"}} {
		body := map[string]any{"name": w.name, "category": "data", "route": "/" + w.id}
		if code, out := do(t, app, http.MethodPut, "/v1/taxonomy/taxa/"+w.id, w.who, body); code != http.StatusOK {
			t.Fatalf("seed %s: %d (%s)", w.id, code, out)
		}
	}
	return app
}

// TestATenantNeverReadsAnotherTenant is the boundary, and it is the whole point of
// the org column. Each org sees the platform catalogue plus its OWN row and never
// the other's — checked in both directions so a rule that happens to work one way
// round cannot pass.
func TestATenantNeverReadsAnotherTenant(t *testing.T) {
	app := twoTenants(t)
	for _, tc := range []struct {
		name       string
		who        map[string]string
		mine, hers string
	}{
		{"acme admin", adminA, "acme-crm", "globex-crm"},
		{"acme member", memberA, "acme-crm", "globex-crm"},
		{"globex admin", adminB, "globex-crm", "acme-crm"},
		{"globex member", memberB, "globex-crm", "acme-crm"},
	} {
		got := read(t, app, "/v1/taxonomy", tc.who)
		if !has(got, tc.mine) {
			t.Errorf("%s cannot see its own %q", tc.name, tc.mine)
		}
		if has(got, tc.hers) {
			t.Errorf("TENANCY BREACH: %s was served another org's %q", tc.name, tc.hers)
		}
		if !has(got, "overview") {
			t.Errorf("%s lost the platform catalogue", tc.name)
		}
	}
	// A signed-out visitor gets the platform catalogue and NEITHER tenant's rows.
	anonRead := read(t, app, "/v1/taxonomy", anon)
	if has(anonRead, "acme-crm") || has(anonRead, "globex-crm") {
		t.Error("TENANCY BREACH: a signed-out visitor was served a customer's rows")
	}
}

// TestATenantNeverWritesAnotherTenant. There is no request that says whose row to
// write — the owner is derived from the validated principal — so naming another
// org's id writes the caller's OWN row and leaves the other org's untouched. That
// is checked here rather than a 403, because a 403 would mean org B's namespace
// blocks org A's, which is itself an observation of B: A would learn B holds that
// id. The property that matters is that B's row does not move, and it does not.
func TestATenantNeverWritesAnotherTenant(t *testing.T) {
	app := twoTenants(t)

	body := map[string]any{"name": "Stolen", "category": "data", "route": "/stolen"}
	if code, out := do(t, app, http.MethodPut, "/v1/taxonomy/taxa/globex-crm", adminA, body); code != http.StatusOK {
		t.Fatalf("acme writing its own row under globex's id: %d (%s)", code, out)
	}
	// Globex still reads its own, unchanged.
	for _, e := range taxaOf(read(t, app, "/v1/taxonomy", adminB)) {
		if e.ID == "globex-crm" {
			if e.Name != "Globex CRM" || e.Owner != "globex" {
				t.Fatalf("TENANCY BREACH: acme's write reached globex's row: %+v", e)
			}
		}
	}
	// And acme reads ITS row under that id, owned by acme.
	var seen bool
	for _, e := range taxaOf(read(t, app, "/v1/taxonomy", adminA)) {
		if e.ID == "globex-crm" {
			seen = true
			if e.Owner != "acme" || e.Name != "Stolen" {
				t.Fatalf("acme's own row is wrong: %+v", e)
			}
		}
	}
	if !seen {
		t.Error("acme's write landed nowhere")
	}

	// A DELETE naming the other org's id removes nothing of theirs.
	if code, _ := do(t, app, http.MethodDelete, "/v1/taxonomy/taxa/acme-crm", adminB, nil); code != http.StatusNotFound {
		t.Errorf("globex deleting acme's id: %d, want 404 — globex holds no such row", code)
	}
	if !has(read(t, app, "/v1/taxonomy", adminA), "acme-crm") {
		t.Fatal("TENANCY BREACH: globex's delete removed acme's row")
	}
}

// TestAnOrgAdminCannotWriteThePlatform is the escalation case. Org-scoped admin
// and platform sudo are different scopes, and an admin who could reach a platform
// row would rename a category for every other tenant. The admin of the PLATFORM's
// OWN org is included deliberately: being an admin of hanzo is still not being a
// SuperAdmin.
func TestAnOrgAdminCannotWriteThePlatform(t *testing.T) {
	app := mountApp(t)
	before := read(t, app, "/v1/taxonomy", anon)

	// hanzo's org admin is refused outright — every write, because their org IS the
	// platform org and only platform sudo edits it.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/v1/taxonomy/categories/ai", map[string]any{"label": "Hijacked"}},
		{http.MethodDelete, "/v1/taxonomy/categories/ai", nil},
		{http.MethodPut, "/v1/taxonomy/taxa/overview", map[string]any{"name": "Hijacked", "category": "ai", "route": "/x"}},
		{http.MethodDelete, "/v1/taxonomy/taxa/overview", nil},
	} {
		if code, out := do(t, app, c.method, c.path, hanzoAdmin, c.body); code != http.StatusForbidden {
			t.Errorf("ESCALATION: hanzo org admin %s %s: %d (%s), want 403", c.method, c.path, code, out)
		}
	}

	// A customer's admin writing a platform id gets their OWN row, and the platform's
	// is untouched — for everyone else, and for a signed-out visitor.
	if code, out := do(t, app, http.MethodPut, "/v1/taxonomy/taxa/overview", adminA,
		map[string]any{"name": "Our Overview", "category": "ai", "route": "/ours"}); code != http.StatusOK {
		t.Fatalf("acme customising a platform id: %d (%s)", code, out)
	}
	after := read(t, app, "/v1/taxonomy", anon)
	if len(taxaOf(after)) != len(taxaOf(before)) {
		t.Fatalf("a customer's write changed the platform catalogue: %d -> %d taxa",
			len(taxaOf(before)), len(taxaOf(after)))
	}
	for _, e := range taxaOf(after) {
		if e.ID == "overview" && (e.Name == "Our Overview" || e.Owner != "hanzo") {
			t.Fatalf("ESCALATION: a customer's row is being served as the platform's: %+v", e)
		}
	}
	// acme sees its own version of that id, and exactly once.
	var n int
	for _, e := range taxaOf(read(t, app, "/v1/taxonomy", adminA)) {
		if e.ID == "overview" {
			n++
			if e.Owner != "acme" || e.Name != "Our Overview" {
				t.Errorf("acme should see its OWN overview, got %+v", e)
			}
		}
	}
	if n != 1 {
		t.Errorf("acme sees the id %q %d times; a shadowed id must resolve to exactly one row", "overview", n)
	}
}

// TestSuperWritesThePlatform — the positive half, so the refusals above are a rule
// and not a wall. A SuperAdmin's write lands on the platform rows, which is what
// every tenant then reads.
func TestSuperWritesThePlatform(t *testing.T) {
	app := mountApp(t)
	if code, out := do(t, app, http.MethodPut, "/v1/taxonomy/taxa/newthing", super,
		map[string]any{"name": "New Thing", "category": "data", "route": "/newthing"}); code != http.StatusOK {
		t.Fatalf("super write: %d (%s)", code, out)
	}
	var got Taxon
	for _, e := range taxaOf(read(t, app, "/v1/taxonomy", anon)) {
		if e.ID == "newthing" {
			got = e
		}
	}
	if got.Owner != hanzo {
		t.Fatalf("a SuperAdmin's write is owned by %q, want the platform org %q", got.Owner, hanzo)
	}
	// Every tenant sees it, because it is platform state.
	for _, who := range []map[string]string{adminA, adminB, memberA, anon} {
		if !has(read(t, app, "/v1/taxonomy", who), "newthing") {
			t.Error("a platform row is not visible to every caller")
		}
	}
}

// TestStagingIsScopedToWhoeverCanUnstageIt. An unpublished row is shown to the
// person who can publish it and to nobody else — including no other tenant, which
// is the tenancy rule again applied to the one field the read varies on.
func TestStagingIsScopedToWhoeverCanUnstageIt(t *testing.T) {
	app := mountApp(t)
	hidden := false
	if code, out := do(t, app, http.MethodPut, "/v1/taxonomy/taxa/acme-secret", adminA,
		map[string]any{"name": "Secret", "category": "data", "route": "/secret", "published": &hidden}); code != http.StatusOK {
		t.Fatalf("put: %d (%s)", code, out)
	}
	if !has(read(t, app, "/v1/taxonomy", adminA), "acme-secret") {
		t.Error("acme's admin cannot see the row it staged — unpublishing would be a one-way door")
	}
	for _, tc := range []struct {
		name string
		who  map[string]string
	}{{"another tenant", adminB}, {"a plain member of the same org", memberA},
		{"a signed-out visitor", anon}, {"a SuperAdmin", super}} {
		if has(read(t, app, "/v1/taxonomy", tc.who), "acme-secret") {
			t.Errorf("%s was served acme's unpublished row", tc.name)
		}
	}
}

// TestThePlatformCatalogueStillRoundTrips — the 186/14 property from the original
// move, now asserted for a caller who has an org but no rows of their own. Adding
// tenancy must not have narrowed what the platform serves.
func TestThePlatformCatalogueStillRoundTrips(t *testing.T) {
	app := twoTenants(t)
	for _, tc := range []struct {
		name string
		who  map[string]string
		want int
	}{
		{"signed out", anon, 186},
		{"a tenant with no rows of its own", map[string]string{"X-User-Id": "zed", "X-Org-Id": "initech"}, 186},
		{"acme, which has one", adminA, 187},
	} {
		got := read(t, app, "/v1/taxonomy", tc.who)
		if len(got.Categories) != 14 {
			t.Errorf("%s sees %d categories, want 14", tc.name, len(got.Categories))
		}
		if n := len(taxaOf(got)); n != tc.want {
			t.Errorf("%s sees %d taxa, want %d", tc.name, n, tc.want)
		}
	}
}

// taxaOf flattens the projection, which most tenancy assertions are about.
func taxaOf(tx Taxonomy) []Taxon {
	var out []Taxon
	for _, c := range tx.Categories {
		out = append(out, c.Taxa...)
	}
	return out
}
