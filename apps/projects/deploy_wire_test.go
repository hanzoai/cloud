package projects

// deploy_wire_test.go pins the split of the deploy route, and the cache policy
// that split made server-owned.
//
// `POST .../deploy` used to be TWO operations at one address, chosen by
// Content-Type: an archive body answered 200, a JSON git descriptor answered 202
// with a write grant. That is why neither could ever be a typed op — a typed
// registration declares ONE input and ONE success status — and why the whole
// surface was published as a bare address with no request schema, no response
// schema and no CLI command. Two shell scripts existed to hand-roll it, one per
// half, each with its own credential and each carrying its own copy of the
// server's cache rule.
//
// The enqueue is now `POST .../deployments` (startDeployment), a typed op. These
// tests assert the wire that split produced, because prose cannot fail.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/zap-proto/zip"
)

// TestDeploymentStartIsItsOwnOperation is the split itself. The address that
// opens a deployment answers 202 and carries the server-derived destination; the
// archive address no longer answers 202 to anything at all, which is what makes
// it a single operation and therefore describable.
func TestDeploymentStartIsItsOwnOperation(t *testing.T) {
	app := mountApp(t)
	newSite(t, app, "acme", "handbook")

	code, b := do(t, app, http.MethodPost, "/v1/projects/handbook/deployments", "acme", map[string]any{"commit": "abc123"})
	if code != http.StatusAccepted {
		t.Fatalf("start deployment want 202, got %d %s", code, b)
	}
	var d projectsDeployment
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("decode: %v (%s)", err, b)
	}
	if d.ID == "" || d.Status != "queued" {
		t.Fatalf("want a queued deployment with an id, got %+v", d)
	}
	if d.Commit != "abc123" {
		t.Fatalf("commit not recorded: %+v", d)
	}
	// The destination is SERVER-derived. A caller that guessed it would write
	// where nothing is served the moment an org or slug changed, so the answer
	// carrying it is the whole point of the 202.
	if d.Prefix != sitePrefix("acme", "handbook") {
		t.Fatalf("prefix must be server-derived, got %q want %q", d.Prefix, sitePrefix("acme", "handbook"))
	}
	if d.Bucket == "" {
		t.Fatalf("deployment must carry its bucket at create, else the live site 404s")
	}

	// The archive address is now ONE operation: a JSON body is no longer a second
	// way in, so it cannot answer 202 here any more.
	if code, b := do(t, app, http.MethodPost, "/v1/projects/handbook/deploy", "acme", map[string]any{"commit": "abc"}); code == http.StatusAccepted {
		t.Fatalf("the archive address must not enqueue; got 202 %s", b)
	}
}

// TestStartNeedsNoLinkedRepo pins a deliberate widening. The predecessor refused
// a project with no linked repo (400 "project has no linked repo"), which is why
// the deploy script had to invent a repo URL to link — an override existed
// solely to satisfy it. A deployment start needs a DESTINATION, not a source: the
// grant is minted from the site's own prefix and the bytes come from whoever
// holds it. Nothing about the repo was ever read.
func TestStartNeedsNoLinkedRepo(t *testing.T) {
	app := mountApp(t)
	newSite(t, app, "acme", "norepo")

	code, b := do(t, app, http.MethodPost, "/v1/projects/norepo/deployments", "acme", map[string]any{})
	if code != http.StatusAccepted {
		t.Fatalf("a site with no linked repo must still open a deployment, got %d %s", code, b)
	}
}

// TestTheQueryStringCannotRedirectACommit is the `url:"-"` guard. zip binds the
// query string OVER a decoded body, so a converted POST silently starts
// accepting `?commit=` — recording a sha the caller never sent, on a row whose
// whole job is to say which source a live site was built from.
func TestTheQueryStringCannotRedirectACommit(t *testing.T) {
	app := mountApp(t)
	newSite(t, app, "acme", "pinned")

	req := httptest.NewRequest(http.MethodPost, "/v1/projects/pinned/deployments?commit=attacker", strings.NewReader(`{"commit":"real"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u_acme")
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var d projectsDeployment
	if err := json.NewDecoder(res.Body).Decode(&d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Commit != "real" {
		t.Fatalf("the body owns this field; query gave %q", d.Commit)
	}
}

// TestOneNounServesTheWholeLifecycle is the reconciliation, finished. Deployments
// used to exist ONLY under /v1/projects while publish and releases existed ONLY
// under /v1/sites, so shipping a site meant straddling two names for one resource
// — which is exactly what the two scripts did. There is one noun now, and open →
// list → complete runs end to end on it.
func TestOneNounServesTheWholeLifecycle(t *testing.T) {
	app := mountApp(t)
	const base, slug = "/v1/projects", "alpha"
	newSite(t, app, "acme", slug)

	code, b := do(t, app, http.MethodPost, base+"/"+slug+"/deployments", "acme", map[string]any{})
	if code != http.StatusAccepted {
		t.Fatalf("start want 202, got %d %s", code, b)
	}
	var d projectsDeployment
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if code, b := do(t, app, http.MethodGet, base+"/"+slug+"/deployments", "acme", nil); code != http.StatusOK {
		t.Fatalf("list want 200, got %d %s", code, b)
	}
	if code, b := do(t, app, http.MethodPost, base+"/"+slug+"/deployments/"+d.ID+"/complete", "acme",
		map[string]any{"status": "live"}); code != http.StatusOK {
		t.Fatalf("complete want 200, got %d %s", code, b)
	}
}

// TestTheGrantIsOnlyOnTheStart pins the one property that keeps a write grant
// from outliving its build: it is minted onto the 202 and stored nowhere, so a
// later read of the same deployment cannot hand it out again.
func TestTheGrantIsOnlyOnTheStart(t *testing.T) {
	app := mountApp(t)
	newSite(t, app, "acme", "once")

	_, b := do(t, app, http.MethodPost, "/v1/projects/once/deployments", "acme", map[string]any{})
	var d projectsDeployment
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	code, rb := do(t, app, http.MethodGet, "/v1/projects/once/deployments/"+d.ID, "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("read back: %d %s", code, rb)
	}
	var got projectsDeployment
	if err := json.Unmarshal(rb, &got); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if got.Upload != nil {
		t.Fatalf("a grant must never be replayed on a read; got %+v", got.Upload)
	}
}

// TestTheStartReachesEveryProjection is the point of typing it at all.
//
// A typed op is ONE registry entry with N projections: the REST route, the
// OpenAPI operation's schemas, the MCP tool and the CLI command all come from
// it. An UNTYPED route gets a route and nothing else — no schema, no prose, no
// MCP tool, no CLI command, no SDK method — which is exactly why two shell
// scripts had to hand-roll this call. So assert the registry carries it rather
// than assuming the document is the whole story.
func TestTheStartReachesEveryProjection(t *testing.T) {
	app := mountApp(t)
	want := map[string]bool{
		"POST /v1/projects/:slug/deployments": false,
		"POST /v1/projects/:slug/publish":     false,
	}
	for _, c := range app.Commands() {
		key := c.Method + " " + c.Path
		if _, ok := want[key]; ok {
			want[key] = true
			if c.Summary == "" {
				t.Errorf("%s reaches the CLI with no summary — a nameless command is what an untyped route already gave us", key)
			}
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("%s is not in the typed registry, so it reaches no SDK, no CLI and no MCP tool", key)
		}
	}
}

// TestTheStartFailsClosedOffTheHTTPPath is the security half of making it a
// projection. zip publishes every typed op as an MCP tool AND a CLI command, and
// NEITHER runs the route's middleware — a tools/call invokes the op directly. So
// the op's own gate has to be what refuses, not a chain it may not be behind.
// This one opens a billable deployment and mints a write grant, so an invocation
// with no attested caller must be the same 403 an anonymous REST call gets.
func TestTheStartFailsClosedOffTheHTTPPath(t *testing.T) {
	app := mountApp(t)
	for _, c := range app.Commands() {
		if c.Method != http.MethodPost || !strings.HasSuffix(c.Path, "/deployments") {
			continue
		}
		_, err := zip.LocalInvoke(t.Context(), c, nil, []byte(`{"slug":"anything"}`))
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusForbidden {
			t.Errorf("%s off the HTTP path: err=%v, want 403", c.Path, err)
		}
	}
}

// TestTheSiteCarriesItsOwnCachePolicy is the wiring half of the cache fix.
//
// The serving path composes ONE rule — sites.CacheControlFor(key,
// site.CacheControl) — instead of reading a Cache-Control header back off the
// stored object. That is only correct if the per-project override reaches the
// resolver, so this asserts the field is carried rather than dropped. Dropping
// it would not fail anything else: every asset would simply serve the default
// policy, silently, and a site that had configured a document TTL would stop
// honouring it with nothing to show for it.
func TestTheSiteCarriesItsOwnCachePolicy(t *testing.T) {
	const override = "public, max-age=15"
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateProject(t.Context(), Project{
		ID: "prj_cached", Org: "acme", Slug: "cached", Name: "cached",
		Status: "live", Bucket: "hanzo-sites", CacheControl: override,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	site, ok, err := (siteResolver{store: st}).ResolveOrg(t.Context(), "acme", "cached")
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if site.CacheControl != override {
		t.Fatalf("Site.CacheControl = %q, want %q — the override never reaches the serving rule", site.CacheControl, override)
	}
	// And the composition the serving path performs: the override governs
	// DOCUMENTS only, while build output stays immutable by its own rule. This is
	// the exact asset class the client-side copy of this rule got wrong.
	if got := sites.CacheControlFor("index.html", site.CacheControl); got != override {
		t.Fatalf("document policy = %q, want the site override %q", got, override)
	}
	if got := sites.CacheControlFor("_next/static/chunk.js", site.CacheControl); got != "public, max-age=31536000, immutable" {
		t.Fatalf("build output = %q, want immutable — this is the asset the shell copy stamped max-age=3600", got)
	}
}
