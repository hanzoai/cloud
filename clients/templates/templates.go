// Package templates mounts /v1/templates — the read-only Hanzo starter-kit
// gallery: deployable app/site scaffolds (source of truth: hanzoai/gallery),
// vendored so the unified `cloud` binary ships the catalog with no external
// dependency. This is REFERENCE content, not org data — there is no per-tenant
// storage; a user forks a template into a project via the deploy flow, not by
// mutating this catalog. It mirrors the prompts starter catalog exactly (one
// embedded JSON, validated once, served under /v1).
//
// Surface:
//
//	GET /v1/templates          list the starter-kit catalog        -> {data:[Template]}
//	GET /v1/templates/:slug    one template by slug (404 if absent) -> Template
package templates

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

//go:embed catalog.json
var catalogJSON []byte

// Template is one starter kit as the console2 gallery browser consumes it. The
// `Source`/`Preview` URLs point at the live gallery (gallery.hanzo.ai), the real
// fork/deploy + screenshot surfaces.
type Template struct {
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Framework   string   `json:"framework"`
	Features    []string `json:"features"`
	UseCase     string   `json:"useCase"`
	Tier        *int     `json:"tier,omitempty"`
	Rating      *float64 `json:"rating,omitempty"`
	Source      string   `json:"source"`
	Preview     string   `json:"preview"`
}

// catalog decodes and validates the embedded gallery once (drops entries with no
// slug/title so a browse row can never be a dead card).
var catalog = sync.OnceValues(func() ([]Template, error) {
	var all []Template
	if err := json.Unmarshal(catalogJSON, &all); err != nil {
		return nil, fmt.Errorf("templates: decode embedded catalog: %w", err)
	}
	out := make([]Template, 0, len(all))
	for _, t := range all {
		if t.Slug == "" || t.Title == "" {
			continue
		}
		if t.Features == nil {
			t.Features = []string{}
		}
		out = append(out, t)
	}
	return out, nil
})

type svc struct{}

// Mount registers the read-only templates surface. No store, no DataDir — the
// catalog is embedded reference content.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("templates.Mount: nil zip.App")
	}
	if _, err := catalog(); err != nil {
		return err
	}
	s := &svc{}
	app.Get("/v1/templates", s.list)
	app.Get("/v1/templates/:slug", s.get)
	if deps.Logger != nil {
		deps.Logger.New("subsystem", "templates").Info("templates mounted", "brand", deps.Brand)
	}
	return nil
}

// List returns the validated starter-kit catalog (the SAME slice the HTTP GET
// serves). Exported so other subsystems — e.g. projectsvc's fork flow — read the
// ONE embedded catalog instead of vendoring a second copy. Read-only reference
// content; callers must not mutate the returned slice.
func List() ([]Template, error) { return catalog() }

// Get returns the template with the given slug, or (Template{}, false) if none.
// This is the single accessor projectsvc uses to seed a Project from a template,
// so the template→project mapping reads the catalog through one door.
func Get(slug string) (Template, bool) {
	cat, err := catalog()
	if err != nil {
		return Template{}, false
	}
	for _, t := range cat {
		if t.Slug == slug {
			return t, true
		}
	}
	return Template{}, false
}

func (s *svc) list(c *zip.Ctx) error {
	cat, err := catalog()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "templates: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": cat})
}

func (s *svc) get(c *zip.Ctx) error {
	if _, err := catalog(); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "templates: %v", err)
	}
	t, ok := Get(c.Param("slug"))
	if !ok {
		return zip.ErrNotFound("template not found")
	}
	return c.JSON(http.StatusOK, t)
}

func init() {
	cloud.Register("templates", 129, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("templates.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}
