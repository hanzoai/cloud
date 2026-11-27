package projects

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/apps/template"
	ns "github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// compose installs what the program's composer installs — cloud.Bridge, once at
// the app root. A subsystem never installs its own, so a test app owes the same
// root install; without it every org-scoped op answers a 403 no production
// program would produce.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// mountApp mounts the projects surface on a fresh in-memory app with a temp
// store, exactly as the unified binary does. The fork route reads the embedded
// templates catalog (template.Lookup) — no template fixture needed.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{MasterKey: testMaster, KMS: testKMS}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
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
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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
// /v1/project/fork with a real gallery slug creates an org-scoped Project seeded
// from the template (name=title, framework mapped, repo=the org's copy of the
// source), and the same record is then readable via GET /v1/project/:slug.
func TestForkCreatesProjectFromTemplate(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, w := mountForking(t)

		// Fork "synapse" (Next.js 14.2 + TS) into maxpower's org, default slug/name.
		code, body := do(t, app, http.MethodPost, "/v1/project/fork", "maxpower",
			map[string]any{"slug": "synapse"})
		if code != http.StatusCreated {
			t.Fatalf("fork synapse want 201, got %d (%s)", code, body)
		}
		var p projectsProject
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
		// The project builds from the org's own COPY of the template, and the copy
		// is made from the REPOSITORY the template lives in: hanzo-apps/<slug>,
		// the layout every static gallery source was verified against, so a fork
		// clones something.
		if p.Repo.URL != "https://"+strings.TrimPrefix(w.URL, "http://")+"/maxpower/synapse.git" {
			t.Fatalf("fork repo url want the org's copy, got %q", p.Repo.URL)
		}
		if p.Repo.Provider != "git" {
			t.Fatalf("fork repo provider want git, got %q", p.Repo.Provider)
		}
		w.in(func() {
			if c := w.repos["maxpower/synapse"]; c == nil || c.source != "https://github.com/hanzo-apps/synapse" {
				t.Fatalf("the copy is %+v, want one made from the template repository", c)
			}
		})
		if p.Status != "draft" {
			t.Fatalf("fork status want draft, got %q", p.Status)
		}

		// The forked project is a real record: readable via the normal GET.
		code, body = do(t, app, http.MethodGet, "/v1/project/synapse", "maxpower", nil)
		if code != http.StatusOK {
			t.Fatalf("get forked project want 200, got %d (%s)", code, body)
		}
	})
}

// TestForkVariantSelection proves the collapse: prism is ONE catalog entry and
// its HTML/React shapes are picked with `variant`, which is what used to cost
// three sibling slugs. The variant drives the framework, the repo URL and the
// derived project slug, so two shapes coexist in one org.
func TestForkVariantSelection(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, w := mountForking(t)

		// The React variant of prism is "React 18 + Vite" → vite, not react.
		code, body := do(t, app, http.MethodPost, "/v1/project/fork", "maxpower",
			map[string]any{"slug": "prism", "variant": "react"})
		if code != http.StatusCreated {
			t.Fatalf("fork prism/react want 201, got %d (%s)", code, body)
		}
		var p projectsProject
		_ = json.Unmarshal(body, &p)
		if p.Framework != "vite" {
			t.Fatalf("prism/react framework want vite (Vite over React), got %q", p.Framework)
		}
		if p.Slug != "prism-react" { // a non-default shape carries its id into the slug
			t.Fatalf("prism/react slug want prism-react, got %q", p.Slug)
		}
		if !strings.HasSuffix(p.Repo.URL, "/maxpower/prism-react.git") {
			t.Fatalf("prism/react repo want the org's copy, got %q", p.Repo.URL)
		}
		w.in(func() {
			if c := w.repos["maxpower/prism-react"]; c == nil || c.source != "https://github.com/hanzo-apps/prism-react" {
				t.Fatalf("the copy is %+v, want one made from the variant source", c)
			}
		})
		if p.ForkedFrom != "prism" { // lineage is the template, not the shape
			t.Fatalf("prism/react lineage want prism, got %q", p.ForkedFrom)
		}

		// No preference → the template's default shape, under the bare slug.
		code, body = do(t, app, http.MethodPost, "/v1/project/fork", "maxpower",
			map[string]any{"slug": "prism"})
		if code != http.StatusCreated {
			t.Fatalf("fork prism want 201, got %d (%s)", code, body)
		}
		_ = json.Unmarshal(body, &p)
		if p.Slug != "prism" || p.Framework != "next" { // "Next.js 13.3 + TS" → next
			t.Fatalf("prism default want prism/next, got %q/%q", p.Slug, p.Framework)
		}

		// A template whose NAME is a reserved subdomain still forks in one click:
		// the derived slug is a default, so it takes the suffix its demo carries.
		code, body = do(t, app, http.MethodPost, "/v1/project/fork", "maxpower",
			map[string]any{"slug": "metrics"})
		if code != http.StatusCreated {
			t.Fatalf("fork metrics want 201, got %d (%s)", code, body)
		}
		_ = json.Unmarshal(body, &p)
		if p.Slug != "metrics-template" {
			t.Fatalf("reserved template slug want metrics-template, got %q", p.Slug)
		}

		// An unknown variant is a 404, not a silent fall back to the default.
		if code, _ := do(t, app, http.MethodPost, "/v1/project/fork", "maxpower",
			map[string]any{"slug": "prism", "variant": "cobol"}); code != http.StatusNotFound {
			t.Fatalf("unknown variant want 404, got %d", code)
		}
	})
}

// TestForkFrameworkMappingAndOverrides exercises the framework mapping and the
// name/target overrides through the real route.
func TestForkFrameworkMappingAndOverrides(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountForking(t)

		// "Next.js 14.2 + TS" → next. Override name + target slug.
		code, body := do(t, app, http.MethodPost, "/v1/project/fork", "maxpower",
			map[string]any{"slug": "saas-landing", "name": "My Landing", "target": "landing-1"})
		if code != http.StatusCreated {
			t.Fatalf("fork saas-landing want 201, got %d (%s)", code, body)
		}
		var p projectsProject
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
		code, body = do(t, app, http.MethodPost, "/v1/project/fork", "maxpower",
			map[string]any{"slug": "loop", "variant": "html"})
		if code != http.StatusCreated {
			t.Fatalf("fork html want 201, got %d (%s)", code, body)
		}
		_ = json.Unmarshal(body, &p)
		if p.Framework != "static" {
			t.Fatalf("html template framework want static, got %q", p.Framework)
		}
	})
}

func TestEstateTemplateForkAllocatesAnotherName(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountForking(t)
		first := forked(t, app, "acme", map[string]any{"slug": "synapse"})
		second := forked(t, app, "hanzo", map[string]any{"slug": "synapse"})
		third := forked(t, app, "hanzo", map[string]any{"slug": "synapse"})
		if first.Slug != "synapse" || second.Slug != "synapse-2" || third.Slug != "synapse-3" {
			t.Fatalf("template names = %q, %q, %q", first.Slug, second.Slug, third.Slug)
		}
		for _, project := range []projectsProject{second, third} {
			if project.ForkedFrom != "synapse" || !strings.Contains(project.Repo.URL, "hanzo-apps/synapse") {
				t.Fatalf("linked template lineage or source lost: %+v", project)
			}
		}
		for n := 4; n <= copyTries; n++ {
			forked(t, app, "hanzo", map[string]any{"slug": "synapse", "target": Slug("synapse", n)})
		}
		if project := forked(t, app, "hanzo", map[string]any{"slug": "synapse"}); project.Slug != Slug("synapse", copyTries+1) {
			t.Fatalf("template name after %d claims = %q", copyTries, project.Slug)
		}
	})
}

// TestForkOrgScopingAndErrors covers invalid requests and cross-org isolation.
func TestForkOrgScopingAndErrors(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountForking(t)

		// No org → 401 (org-scoped exactly like the other routes).
		if code, _ := do(t, app, http.MethodPost, "/v1/project/fork", "", map[string]any{"slug": "synapse"}); code != http.StatusUnauthorized {
			t.Fatalf("no-org fork want 401, got %d", code)
		}
		// Missing template slug → 400.
		if code, _ := do(t, app, http.MethodPost, "/v1/project/fork", "maxpower", map[string]any{}); code != http.StatusBadRequest {
			t.Fatalf("no-slug fork want 400, got %d", code)
		}
		// Unknown template → 404.
		if code, _ := do(t, app, http.MethodPost, "/v1/project/fork", "maxpower", map[string]any{"slug": "does-not-exist"}); code != http.StatusNotFound {
			t.Fatalf("unknown template fork want 404, got %d", code)
		}

		// maxpower forks synapse.
		if code, _ := do(t, app, http.MethodPost, "/v1/project/fork", "maxpower", map[string]any{"slug": "synapse"}); code != http.StatusCreated {
			t.Fatalf("maxpower fork want 201, got %d", code)
		}
		// A second fork of the same template into the SAME org is a second copy,
		// under the next number.
		if p := forked(t, app, "maxpower", map[string]any{"slug": "synapse"}); p.Slug != "synapse-2" {
			t.Fatalf("a second copy is %q, want synapse-2", p.Slug)
		}
		// A slug the caller NAMES is taken as named: one the org holds → 409.
		if code, _ := do(t, app, http.MethodPost, "/v1/project/fork", "maxpower", map[string]any{"slug": "synapse", "target": "synapse"}); code != http.StatusConflict {
			t.Fatalf("a named slug the org holds want 409, got %d", code)
		}
		// A DIFFERENT org can fork the same template, under the next free name: names
		// are one namespace across every org.
		if p := forked(t, app, "acme", map[string]any{"slug": "synapse"}); p.Slug != "synapse-3" {
			t.Fatalf("acme's copy is %q, want synapse-3", p.Slug)
		}
		// acme cannot see maxpower's forked project.
		if code, _ := do(t, app, http.MethodGet, "/v1/project/synapse", "acme", nil); code != http.StatusNotFound {
			t.Fatalf("acme reads maxpower's synapse: %d", code)
		}
	})
}

// TestForkPublishedProjectRecordsLineage is the creator loop end to end: a
// first-party example published as a LIVE project is forkable BY SLUG (the same
// name it serves under at <slug>.hanzo.app), the fork lands in the forker's own
// org under their own slug carrying the parent's source, and the parent is
// recorded on the child as forkedFrom so the attribution survives the rename.
// Authorship moves with the org: a fork of a Hanzo example IS the forker's app,
// because the forker's org is the one paying for it.
func TestForkPublishedProjectRecordsLineage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)

		ex := mkProject("hanzo", "example-kanban", "Example Kanban")
		ex.Status, ex.Framework = "live", "vite"
		ex.RepoURL, ex.RepoBranch = "https://github.com/hanzo-templates/kanban-board", "main"
		if err := mounted.State.store.CreateProject(context.Background(), ex); err != nil {
			t.Fatalf("seed example: %v", err)
		}

		code, body := do(t, app, http.MethodPost, "/v1/project/fork", "acme",
			map[string]any{"slug": "example-kanban", "target": "my-board", "name": "My Board"})
		if code != http.StatusCreated {
			t.Fatalf("fork published example want 201, got %d (%s)", code, body)
		}
		var p projectsProject
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
		// Whose app this is now is its Org, and the fork moved it: acme's. There is no
		// authorship field to inherit or fail to clear.
		if p.Visibility != Public {
			t.Fatalf("a fork must land public by default, got %q", p.Visibility)
		}

		// A DRAFT example is not published, so it is not forkable — you can only fork
		// what you can browse.
		dr := mkProject("hanzo", "example-draft", "Draft")
		if err := mounted.State.store.CreateProject(context.Background(), dr); err != nil {
			t.Fatalf("seed draft: %v", err)
		}
		if code, _ := do(t, app, http.MethodPost, "/v1/project/fork", "acme", map[string]any{"slug": "example-draft"}); code != http.StatusNotFound {
			t.Fatalf("fork of a draft want 404, got %d", code)
		}
	})
}

// TestForkTemplateRecordsLineage pins that a catalog fork records the template
// slug as its parent — the same forkedFrom field, one lineage concept for both
// kinds of parent.
func TestForkTemplateRecordsLineage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountForking(t)
		code, body := do(t, app, http.MethodPost, "/v1/project/fork", "acme", map[string]any{"slug": "synapse"})
		if code != http.StatusCreated {
			t.Fatalf("fork want 201, got %d (%s)", code, body)
		}
		var p projectsProject
		_ = json.Unmarshal(body, &p)
		if p.ForkedFrom != "synapse" {
			t.Fatalf("template lineage = %q, want synapse", p.ForkedFrom)
		}
	})
}

// mountTemplates mounts the gallery beside projects the way the template plugin
// does: this host's identity and its "templates" store.
func mountTemplates(t *testing.T, app *zip.App) {
	t.Helper()
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	err := template.Mount(app, template.Deps{
		Log:    luxlog.Default().New("subsystem", "template"),
		Org:    principal.OrgFrom,
		Acting: principal.Acting,
		Open: func(ctx context.Context, migrate func(context.Context, *sql.Tx) error) (*sql.DB, error) {
			return locus.Use(ctx, nil, ns.System(), "templates", cloud.DataDir(), func(db *sql.DB) (*sql.DB, error) {
				if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error { return migrate(ctx, tx) }); err != nil {
					return nil, err
				}
				return db, nil
			})
		},
	})
	if err != nil {
		t.Fatalf("template.Mount: %v", err)
	}
	t.Cleanup(func() { _ = template.Shutdown(t.Context()) })
}

// TestForkPrivateTemplateIsOwnerOnly is the per-org template loop end to end:
// acme publishes a template PRIVATE to acme, forks it, and gets a project seeded
// from it with owner-qualified lineage — while globex, asking for the exact same
// slug, gets a 404. template.Lookup binds the caller's org, so another org's
// template is not merely filtered out of the fork, it is unreachable from it.
func TestForkPrivateTemplateIsOwnerOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		mountTemplates(t, app)

		if code, body := do(t, app, http.MethodPost, "/v1/template", "acme", map[string]any{
			"slug": "acme-portal", "title": "Acme Portal", "framework": "React 18 + Vite",
			"source": "https://git.acme.example/portal",
		}); code != http.StatusCreated {
			t.Fatalf("publish private template want 201, got %d (%s)", code, body)
		}

		code, body := do(t, app, http.MethodPost, "/v1/project/fork", "acme", map[string]any{"slug": "acme-portal"})
		if code != http.StatusCreated {
			t.Fatalf("owner fork of own template want 201, got %d (%s)", code, body)
		}
		var p projectsProject
		_ = json.Unmarshal(body, &p)
		if p.Org != "acme" || p.Framework != "vite" || p.Repo.URL != "https://git.acme.example/portal" {
			t.Fatalf("fork did not seed from the private template: org=%s framework=%s repo=%s", p.Org, p.Framework, p.Repo.URL)
		}
		if p.ForkedFrom != "acme/acme-portal" {
			t.Fatalf("private-template lineage = %q, want acme/acme-portal", p.ForkedFrom)
		}

		if code, body := do(t, app, http.MethodPost, "/v1/project/fork", "globex", map[string]any{"slug": "acme-portal"}); code != http.StatusNotFound {
			t.Fatalf("cross-org fork of a private template want 404, got %d (%s)", code, body)
		}
	})
}

// TestPublishingIsUngated is the rule the community runs on: an ordinary tenant,
// with no admin and no funding, publishes PUBLIC — at create and after the fact.
// Nothing has to be granted to it, because a community you must be admitted to
// does not grow. This is the exact inverse of the admin-gated `official` badge it
// replaced, which gated the way IN and so filed the platform's own script-published
// apps as somebody else's work.
func TestPublishingIsUngated(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)

		code, body := do(t, app, http.MethodPost, "/v1/project", "acme",
			map[string]any{"name": "Newcomer", "slug": "newcomer"})
		if code != http.StatusCreated {
			t.Fatalf("create want 201, got %d (%s)", code, body)
		}
		var p projectsProject
		_ = json.Unmarshal(body, &p)
		if p.Visibility != Public {
			t.Fatalf("a project must default to public, got %q (%s)", p.Visibility, body)
		}
		if p.Hidden {
			t.Fatal("a new project must not be born moderated")
		}

		// Asking for it explicitly is the same answer, not a different code path.
		code, body = do(t, app, http.MethodPatch, "/v1/project/newcomer", "acme",
			map[string]any{"visibility": "public"})
		if code != http.StatusOK {
			t.Fatalf("patch public want 200, got %d (%s)", code, body)
		}
		_ = json.Unmarshal(body, &p)
		if p.Visibility != Public {
			t.Fatalf("visibility = %q, want public", p.Visibility)
		}

		// Anything that is neither is refused rather than quietly coerced: a caller
		// that misspells "private" must not be handed a public project.
		if code, body := do(t, app, http.MethodPost, "/v1/project", "acme",
			map[string]any{"name": "Typo", "slug": "typo", "visibility": "secret"}); code != http.StatusBadRequest {
			t.Fatalf("unknown visibility want 400, got %d (%s)", code, body)
		}
	})
}

// TestModerationIsAdminOnlyAndSubtractive pins the one admin-gated field. A
// tenant sending hidden:true is ignored; an admin can hide with a reason and lift
// it again. Hiding must NOT rewrite the publisher's own visibility, so lifting
// restores exactly what they asked for with no second write to get wrong.
func TestModerationIsAdminOnlyAndSubtractive(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		if code, body := do(t, app, http.MethodPost, "/v1/project", "acme",
			map[string]any{"name": "Spam", "slug": "spam"}); code != http.StatusCreated {
			t.Fatalf("create want 201, got %d (%s)", code, body)
		}

		code, body := do(t, app, http.MethodPatch, "/v1/project/spam", "acme",
			map[string]any{"hidden": true, "hiddenReason": "self-moderated"})
		if code != http.StatusOK {
			t.Fatalf("tenant patch want 200, got %d (%s)", code, body)
		}
		var p projectsProject
		_ = json.Unmarshal(body, &p)
		if p.Hidden {
			t.Fatalf("a tenant must not be able to set moderation state: %s", body)
		}

		adminPatch := func(t *testing.T, in map[string]any) projectsProject {
			t.Helper()
			b, _ := json.Marshal(in)
			req := httptest.NewRequest(http.MethodPatch, "/v1/project/spam", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Org-Id", "acme")
			req.Header.Set("X-User-Id", "u_admin")
			req.Header.Set("X-User-IsAdmin", "true")
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatalf("admin patch: %v", err)
			}
			rb, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("admin patch want 200, got %d (%s)", resp.StatusCode, rb)
			}
			var out projectsProject
			_ = json.Unmarshal(rb, &out)
			return out
		}

		hid := adminPatch(t, map[string]any{"hidden": true, "hiddenReason": "spam"})
		if !hid.Hidden || hid.HiddenReason != "spam" {
			t.Fatalf("admin hide = %v/%q, want true/spam", hid.Hidden, hid.HiddenReason)
		}
		if hid.Visibility != Public {
			t.Fatalf("moderation must not rewrite the publisher's choice, got %q", hid.Visibility)
		}

		lifted := adminPatch(t, map[string]any{"hidden": false})
		if lifted.Hidden {
			t.Fatal("admin must be able to lift a moderation")
		}
		if lifted.HiddenReason != "" {
			t.Fatalf("a lifted moderation must leave no stale reason, got %q", lifted.HiddenReason)
		}
		if lifted.Visibility != Public {
			t.Fatalf("lifting must restore exactly what the publisher asked for, got %q", lifted.Visibility)
		}
	})
}

// TestPrivateAndModeratedProjectsLeaveNoCatalogRow is the whole point of the two
// fields: LiveSites is the ONE cross-org read, so the rule is enforced in its
// query and a consumer that forgets to filter cannot leak anything. A live site
// that is private, or public-but-moderated, must not appear.
func TestPrivateAndModeratedProjectsLeaveNoCatalogRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		ctx := context.Background()

		seed := func(slug, vis string, hidden bool) {
			p := mkProject("acme", slug, slug)
			p.Status, p.Visibility, p.Hidden = "live", vis, hidden
			if err := mounted.State.store.CreateProject(ctx, p); err != nil {
				t.Fatalf("seed %s: %v", slug, err)
			}
		}
		seed("shown", Public, false)
		seed("privately", Private, false)
		seed("moderated", Public, true)

		sites, err := LiveSites(ctx)
		if err != nil {
			t.Fatalf("LiveSites: %v", err)
		}
		got := map[string]bool{}
		for _, s := range sites {
			got[s.Slug] = true
		}
		if !got["shown"] {
			t.Fatal("a live public project must appear in the catalogue")
		}
		for _, slug := range []string{"privately", "moderated"} {
			if got[slug] {
				t.Fatalf("%q reached the cross-org catalogue", slug)
			}
		}
	})
}

// TestCreditIsUngated pins the remaining half of provenance. Upstream/License say
// "somebody ELSE made this", which can only cost the publisher credit — so anyone
// may say it about their own project, with no admin. Authorship needs no
// counterpart field: it is the org that pays, which no request can forge.
func TestCreditIsUngated(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		code, body := do(t, app, http.MethodPost, "/v1/project", "acme", map[string]any{
			"name": "Fitness Pro", "slug": "kinetic",
			"upstream": "UI8 — Fitness Pro: Website UI Kit", "license": "UI8 commercial licence",
		})
		if code != http.StatusCreated {
			t.Fatalf("create want 201, got %d (%s)", code, body)
		}
		var p projectsProject
		_ = json.Unmarshal(body, &p)
		if p.Upstream != "UI8 — Fitness Pro: Website UI Kit" || p.License != "UI8 commercial licence" {
			t.Fatalf("a publisher must be able to credit its upstream: %s", body)
		}

		// Settable after the fact too: the demos that most need crediting are the ones
		// already live. And a credit is rendered on a card, so it stays single-line.
		code, body = do(t, app, http.MethodPatch, "/v1/project/kinetic", "acme",
			map[string]any{"upstream": "  UI8\nnewline  ", "license": "MIT"})
		if code != http.StatusOK {
			t.Fatalf("patch want 200, got %d (%s)", code, body)
		}
		_ = json.Unmarshal(body, &p)
		if p.Upstream != "UI8 newline" || p.License != "MIT" {
			t.Fatalf("credit must be trimmed and single-line, got %q/%q", p.Upstream, p.License)
		}
	})
}
