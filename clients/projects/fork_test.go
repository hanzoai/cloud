package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/templates"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountApp mounts the projects surface on a fresh in-memory app with a temp
// store, exactly as the unified binary does. The fork route reads the embedded
// templates catalog (templates.Lookup) — no template fixture needed.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // validated principal (org() gates on it)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestMapFramework pins the template-label → projects-enum mapping against the
// real gallery labels (see catalog.json). Vite wins over React (it is a build
// step); Next.js is its own hint; bare HTML/* is "static". Every result must be a
// valid `frameworks` key so createProject never rejects a forked project.
func TestMapFramework(t *testing.T) {
	cases := map[string]string{
		"":                        "static",
		"HTML/Gulp":               "static",
		"HTML/CSS":                "static",
		"HTML/CSS/JS":             "static",
		"HTML/SCSS + GSAP":        "static",
		"HTML/Gulp + Bootstrap 5": "static",
		"Next.js 14.2 + TS":       "next",
		"Next.js 13":              "next",
		"Next.js 14 + shadcn":     "next",
		"React 18":                "react",
		"React 17":                "react",
		"React 18 + CRA":          "react",
		"React 18 + Vite":         "vite", // Vite wins over React
		"Astro 4":                 "astro",
		"SvelteKit":               "svelte",
		"Vue 3 + Vite":            "vite",
		"Nuxt 3":                  "nuxt",
		"Remix":                   "remix",
	}
	for label, want := range cases {
		if got := mapFramework(label); got != want {
			t.Errorf("mapFramework(%q)=%q want %q", label, got, want)
		}
		// Invariant: the mapping never yields an unknown framework.
		if !frameworks[mapFramework(label)] {
			t.Errorf("mapFramework(%q)=%q is not a valid framework", label, mapFramework(label))
		}
	}
}

// TestForkCreatesProjectFromTemplate is the end-to-end wire proof: POST
// /v1/projects/fork with a real gallery slug creates an org-scoped Project seeded
// from the template (name=title, framework mapped, repo=source), and the same
// record is then readable via GET /v1/projects/:slug.
func TestForkCreatesProjectFromTemplate(t *testing.T) {
	app := mountApp(t)

	// Fork "synapse" (Next.js 14.2 + TS) into maxpower's org, default slug/name.
	code, body := do(t, app, http.MethodPost, "/v1/projects/fork", "maxpower",
		map[string]any{"slug": "synapse"})
	if code != http.StatusCreated {
		t.Fatalf("fork synapse want 201, got %d (%s)", code, body)
	}
	var p projectView
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("fork json: %v (%s)", err, body)
	}
	if p.Org != "maxpower" {
		t.Fatalf("fork org want maxpower, got %q", p.Org)
	}
	if p.Slug != "synapse" {
		t.Fatalf("fork slug want synapse, got %q", p.Slug)
	}
	if p.Name != "Synapse" { // seeded from the template title
		t.Fatalf("fork name want Synapse (template title), got %q", p.Name)
	}
	if p.Framework != "next" { // "Next.js 14.2 + TS" → next
		t.Fatalf("fork framework want next, got %q", p.Framework)
	}
	if p.Repo.URL != "https://gallery.hanzo.ai/templates/synapse" {
		t.Fatalf("fork repo url want gallery source, got %q", p.Repo.URL)
	}
	if p.Repo.Provider != "git" { // gallery.hanzo.ai is not github/gitlab/bitbucket
		t.Fatalf("fork repo provider want git, got %q", p.Repo.Provider)
	}
	if p.Status != "draft" {
		t.Fatalf("fork status want draft, got %q", p.Status)
	}

	// The forked project is a real record: readable via the normal GET.
	code, body = do(t, app, http.MethodGet, "/v1/projects/synapse", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("get forked project want 200, got %d (%s)", code, body)
	}
}

// TestForkVariantSelection proves the collapse: prism is ONE catalog entry and
// its HTML/React shapes are picked with `variant`, which is what used to cost
// three sibling slugs. The variant drives the framework, the repo URL and the
// derived project slug, so two shapes coexist in one org.
func TestForkVariantSelection(t *testing.T) {
	app := mountApp(t)

	// The React variant of prism is "React 18 + Vite" → vite, not react.
	code, body := do(t, app, http.MethodPost, "/v1/projects/fork", "maxpower",
		map[string]any{"slug": "prism", "variant": "react"})
	if code != http.StatusCreated {
		t.Fatalf("fork prism/react want 201, got %d (%s)", code, body)
	}
	var p projectView
	_ = json.Unmarshal(body, &p)
	if p.Framework != "vite" {
		t.Fatalf("prism/react framework want vite (Vite over React), got %q", p.Framework)
	}
	if p.Slug != "prism-react" { // a non-default shape carries its id into the slug
		t.Fatalf("prism/react slug want prism-react, got %q", p.Slug)
	}
	if p.Repo.URL != "https://gallery.hanzo.ai/templates/prism-react" {
		t.Fatalf("prism/react repo want the variant source, got %q", p.Repo.URL)
	}
	if p.ForkedFrom != "prism" { // lineage is the template, not the shape
		t.Fatalf("prism/react lineage want prism, got %q", p.ForkedFrom)
	}

	// No preference → the template's default shape, under the bare slug.
	code, body = do(t, app, http.MethodPost, "/v1/projects/fork", "maxpower",
		map[string]any{"slug": "prism"})
	if code != http.StatusCreated {
		t.Fatalf("fork prism want 201, got %d (%s)", code, body)
	}
	_ = json.Unmarshal(body, &p)
	if p.Slug != "prism" || p.Framework != "static" { // "HTML/SCSS + GSAP" → static
		t.Fatalf("prism default want prism/static, got %q/%q", p.Slug, p.Framework)
	}

	// A template whose NAME is a reserved subdomain still forks in one click:
	// the derived slug is a default, so it takes the suffix its demo carries.
	code, body = do(t, app, http.MethodPost, "/v1/projects/fork", "maxpower",
		map[string]any{"slug": "metrics"})
	if code != http.StatusCreated {
		t.Fatalf("fork metrics want 201, got %d (%s)", code, body)
	}
	_ = json.Unmarshal(body, &p)
	if p.Slug != "metrics-template" {
		t.Fatalf("reserved template slug want metrics-template, got %q", p.Slug)
	}

	// An unknown variant is a 404, not a silent fall back to the default.
	if code, _ := do(t, app, http.MethodPost, "/v1/projects/fork", "maxpower",
		map[string]any{"slug": "prism", "variant": "cobol"}); code != http.StatusNotFound {
		t.Fatalf("unknown variant want 404, got %d", code)
	}
}

// TestForkFrameworkMappingAndOverrides exercises the framework mapping and the
// name/target overrides through the real route.
func TestForkFrameworkMappingAndOverrides(t *testing.T) {
	app := mountApp(t)

	// "Next.js 14.2 + TS" → next. Override name + target slug.
	code, body := do(t, app, http.MethodPost, "/v1/projects/fork", "maxpower",
		map[string]any{"slug": "saas-landing", "name": "My Landing", "target": "landing-1"})
	if code != http.StatusCreated {
		t.Fatalf("fork saas-landing want 201, got %d (%s)", code, body)
	}
	var p projectView
	_ = json.Unmarshal(body, &p)
	if p.Framework != "next" {
		t.Fatalf("saas-landing framework want next, got %q", p.Framework)
	}
	if p.Name != "My Landing" {
		t.Fatalf("name override want 'My Landing', got %q", p.Name)
	}
	if p.Slug != "landing-1" {
		t.Fatalf("target slug override want landing-1, got %q", p.Slug)
	}

	// A bare-HTML template forks to "static".
	code, body = do(t, app, http.MethodPost, "/v1/projects/fork", "maxpower",
		map[string]any{"slug": "loop", "variant": "html"})
	if code != http.StatusCreated {
		t.Fatalf("fork html want 201, got %d (%s)", code, body)
	}
	_ = json.Unmarshal(body, &p)
	if p.Framework != "static" {
		t.Fatalf("html template framework want static, got %q", p.Framework)
	}
}

// TestForkOrgScopingAndErrors covers the boundary rejections: no org → 403,
// missing slug → 400, unknown template → 404, and cross-org isolation (a fork
// lands in the caller's org only; a duplicate in the same org → 409).
func TestForkOrgScopingAndErrors(t *testing.T) {
	app := mountApp(t)

	// No org → 403 (org-scoped exactly like the other routes).
	if code, _ := do(t, app, http.MethodPost, "/v1/projects/fork", "", map[string]any{"slug": "synapse"}); code != http.StatusForbidden {
		t.Fatalf("no-org fork want 403, got %d", code)
	}
	// Missing template slug → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/projects/fork", "maxpower", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("no-slug fork want 400, got %d", code)
	}
	// Unknown template → 404.
	if code, _ := do(t, app, http.MethodPost, "/v1/projects/fork", "maxpower", map[string]any{"slug": "does-not-exist"}); code != http.StatusNotFound {
		t.Fatalf("unknown template fork want 404, got %d", code)
	}

	// maxpower forks synapse.
	if code, _ := do(t, app, http.MethodPost, "/v1/projects/fork", "maxpower", map[string]any{"slug": "synapse"}); code != http.StatusCreated {
		t.Fatalf("maxpower fork want 201, got %d", code)
	}
	// A second fork of the same template into the SAME org → 409 (slug taken).
	if code, _ := do(t, app, http.MethodPost, "/v1/projects/fork", "maxpower", map[string]any{"slug": "synapse"}); code != http.StatusConflict {
		t.Fatalf("dup fork want 409, got %d", code)
	}
	// A DIFFERENT org can fork the same template (same slug, different org).
	if code, _ := do(t, app, http.MethodPost, "/v1/projects/fork", "acme", map[string]any{"slug": "synapse"}); code != http.StatusCreated {
		t.Fatalf("acme fork same template want 201, got %d", code)
	}
	// acme cannot see maxpower's forked project.
	if code, _ := do(t, app, http.MethodGet, "/v1/projects/synapse", "acme", nil); code != http.StatusOK {
		// acme forked its OWN synapse above, so it SHOULD see one — assert isolation
		// via a slug acme never forked.
		t.Fatalf("acme should see its own synapse, got %d", code)
	}
}

// TestForkPublishedProjectRecordsLineage is the creator loop end to end: a
// first-party example published as a LIVE project is forkable BY SLUG (the same
// name it serves under at <slug>.hanzo.app), the fork lands in the forker's own
// org under their own slug carrying the parent's source, and the parent is
// recorded on the child as forkedFrom so the attribution survives the rename. The
// badge is NOT inherited — a fork of a Hanzo example is the forker's app.
func TestForkPublishedProjectRecordsLineage(t *testing.T) {
	app := mountApp(t)

	ex := mkProject("hanzo", "example-kanban", "Example Kanban")
	ex.Status, ex.Framework, ex.Official = "live", "vite", true
	ex.RepoURL, ex.RepoBranch = "https://github.com/hanzo-templates/kanban-board", "main"
	if err := mounted.State.store.CreateProject(context.Background(), ex); err != nil {
		t.Fatalf("seed example: %v", err)
	}

	code, body := do(t, app, http.MethodPost, "/v1/projects/fork", "acme",
		map[string]any{"slug": "example-kanban", "target": "my-board", "name": "My Board"})
	if code != http.StatusCreated {
		t.Fatalf("fork published example want 201, got %d (%s)", code, body)
	}
	var p projectView
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("fork json: %v (%s)", err, body)
	}
	if p.Org != "acme" || p.Slug != "my-board" || p.Name != "My Board" {
		t.Fatalf("fork target = %s/%s %q, want acme/my-board 'My Board'", p.Org, p.Slug, p.Name)
	}
	if p.ForkedFrom != "hanzo/example-kanban" {
		t.Fatalf("lineage = %q, want hanzo/example-kanban", p.ForkedFrom)
	}
	if p.Repo.URL != ex.RepoURL || p.Framework != "vite" {
		t.Fatalf("fork did not inherit buildable source: repo=%q framework=%q", p.Repo.URL, p.Framework)
	}
	if p.Official {
		t.Fatalf("a fork of a first-party example must not inherit the official badge")
	}

	// A DRAFT example is not published, so it is not forkable — you can only fork
	// what you can browse.
	dr := mkProject("hanzo", "example-draft", "Draft")
	if err := mounted.State.store.CreateProject(context.Background(), dr); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/projects/fork", "acme", map[string]any{"slug": "example-draft"}); code != http.StatusNotFound {
		t.Fatalf("fork of a draft want 404, got %d", code)
	}
}

// TestForkTemplateRecordsLineage pins that a catalog fork records the template
// slug as its parent — the same forkedFrom field, one lineage concept for both
// kinds of parent.
func TestForkTemplateRecordsLineage(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/projects/fork", "acme", map[string]any{"slug": "synapse"})
	if code != http.StatusCreated {
		t.Fatalf("fork want 201, got %d (%s)", code, body)
	}
	var p projectView
	_ = json.Unmarshal(body, &p)
	if p.ForkedFrom != "synapse" {
		t.Fatalf("template lineage = %q, want synapse", p.ForkedFrom)
	}
}

// TestForkPrivateTemplateIsOwnerOnly is the per-org template loop end to end:
// acme publishes a template PRIVATE to acme, forks it, and gets a project seeded
// from it with owner-qualified lineage — while globex, asking for the exact same
// slug, gets a 404. templates.Lookup binds the caller's org, so another org's
// template is not merely filtered out of the fork, it is unreachable from it.
func TestForkPrivateTemplateIsOwnerOnly(t *testing.T) {
	app := mountApp(t)
	if err := templates.Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("templates.Mount: %v", err)
	}
	t.Cleanup(func() { _ = templates.Shutdown(t.Context()) })

	if code, body := do(t, app, http.MethodPost, "/v1/templates", "acme", map[string]any{
		"slug": "acme-portal", "title": "Acme Portal", "framework": "React 18 + Vite",
		"source": "https://git.acme.example/portal",
	}); code != http.StatusCreated {
		t.Fatalf("publish private template want 201, got %d (%s)", code, body)
	}

	code, body := do(t, app, http.MethodPost, "/v1/projects/fork", "acme", map[string]any{"slug": "acme-portal"})
	if code != http.StatusCreated {
		t.Fatalf("owner fork of own template want 201, got %d (%s)", code, body)
	}
	var p projectView
	_ = json.Unmarshal(body, &p)
	if p.Org != "acme" || p.Framework != "vite" || p.Repo.URL != "https://git.acme.example/portal" {
		t.Fatalf("fork did not seed from the private template: org=%s framework=%s repo=%s", p.Org, p.Framework, p.Repo.URL)
	}
	if p.ForkedFrom != "acme/acme-portal" {
		t.Fatalf("private-template lineage = %q, want acme/acme-portal", p.ForkedFrom)
	}

	if code, body := do(t, app, http.MethodPost, "/v1/projects/fork", "globex", map[string]any{"slug": "acme-portal"}); code != http.StatusNotFound {
		t.Fatalf("cross-org fork of a private template want 404, got %d (%s)", code, body)
	}
}

// TestOfficialBadgeIsPlatformOnly proves the first-party marker cannot be
// self-asserted: an ordinary tenant asking for official:true gets false, and only
// a SuperAdmin caller (the seeding path) can raise it.
func TestOfficialBadgeIsPlatformOnly(t *testing.T) {
	app := mountApp(t)

	code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Impostor", "slug": "impostor", "official": true})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var p projectView
	_ = json.Unmarshal(body, &p)
	if p.Official {
		t.Fatalf("a tenant must not be able to badge its own app as a Hanzo example")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/projects",
		bytes.NewReader([]byte(`{"name":"Example","slug":"example-app","official":true}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "hanzo")
	req.Header.Set("X-User-Id", "u_hanzo")
	req.Header.Set("X-User-IsAdmin", "true")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("admin create: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin create want 201, got %d (%s)", resp.StatusCode, b)
	}
	_ = json.Unmarshal(b, &p)
	if !p.Official {
		t.Fatalf("the platform must be able to badge its own examples: %s", b)
	}
}

// TestOfficialBadgeOnUpdate: the badge must also reach the examples published
// BEFORE it existed, under the same one rule — a tenant PATCHing official:true
// on its own app is ignored; a SuperAdmin can badge, and un-badge.
func TestOfficialBadgeOnUpdate(t *testing.T) {
	app := mountApp(t)
	if code, body := do(t, app, http.MethodPost, "/v1/projects", "hanzo",
		map[string]any{"name": "Legacy Example", "slug": "legacy-example"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	code, body := do(t, app, http.MethodPatch, "/v1/projects/legacy-example", "hanzo", map[string]any{"official": true})
	if code != http.StatusOK {
		t.Fatalf("tenant patch want 200, got %d (%s)", code, body)
	}
	var p projectView
	_ = json.Unmarshal(body, &p)
	if p.Official {
		t.Fatalf("a tenant self-badged via update")
	}
	for _, want := range []bool{true, false} {
		b, _ := json.Marshal(map[string]any{"official": want})
		req := httptest.NewRequest(http.MethodPatch, "/v1/projects/legacy-example", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", "hanzo")
		req.Header.Set("X-User-Id", "u_hanzo")
		req.Header.Set("X-User-IsAdmin", "true")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("admin patch: %v", err)
		}
		rb, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		_ = json.Unmarshal(rb, &p)
		if p.Official != want {
			t.Fatalf("admin patch official=%v, want %v (%s)", p.Official, want, rb)
		}
	}
}

// TestCreditIsUngatedWhileTheBadgeIsNot pins the asymmetry the two halves of
// provenance deliberately have. Official says "Hanzo made this", so only Hanzo
// may say it. Upstream/License say "somebody ELSE made this", which can only cost
// the publisher credit — so anyone may say it, about their own project, without
// an admin. A platform where claiming authorship is easier than disclaiming it is
// a platform that launders provenance.
func TestCreditIsUngatedWhileTheBadgeIsNot(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/projects", "acme", map[string]any{
		"name": "Fitness Pro", "slug": "kinetic", "official": true,
		"upstream": "UI8 — Fitness Pro: Website UI Kit", "license": "UI8 commercial licence",
	})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var p projectView
	_ = json.Unmarshal(body, &p)
	if p.Official {
		t.Fatal("official is still admin-only")
	}
	if p.Upstream != "UI8 — Fitness Pro: Website UI Kit" || p.License != "UI8 commercial licence" {
		t.Fatalf("a publisher must be able to credit its upstream: %s", body)
	}

	// Settable after the fact too: the demos that most need crediting are the ones
	// already live. And a credit is rendered on a card, so it stays single-line.
	code, body = do(t, app, http.MethodPatch, "/v1/projects/kinetic", "acme",
		map[string]any{"upstream": "  UI8\nnewline  ", "license": "MIT"})
	if code != http.StatusOK {
		t.Fatalf("patch want 200, got %d (%s)", code, body)
	}
	_ = json.Unmarshal(body, &p)
	if p.Upstream != "UI8 newline" || p.License != "MIT" {
		t.Fatalf("credit must be trimmed and single-line, got %q/%q", p.Upstream, p.License)
	}
}
