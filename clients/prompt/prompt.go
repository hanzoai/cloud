// Package promptsvc mounts /v1/prompts/* — a cloud-native, org-scoped, versioned
// prompt registry backed by SQLite (hanzoai/base pattern). NO proxy: the console
// (Langfuse) deliberately redirects /api/public/v2/prompts → /v1/prompts, so cloud
// OWNS prompts here. Proxying back to the console would loop (307 → /v1/prompts →
// promptsvc → …); serving from the local store is the correct, all-SQLite design.
//
// Surface (mirrors the console2 Prompts module):
//   GET  /v1/prompts          → list prompt metadata (one row per name)
//   GET  /v1/prompts/:name    → all versions of a named prompt (newest first)
//   POST /v1/prompts          → create the next version of a prompt
//
// Order 144: binds before the AI subsystem's /v1/* catch-all (150). The
// composition root auto-registers GET /v1/prompts/health.
package prompt

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
)

type service struct {
	store *Store
	log   luxlog.Logger
}

// Mount opens the prompts store and registers the /v1/prompts/* surface.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("prompt.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("prompt.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("prompt.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("prompt.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "prompts.db"))
	if err != nil {
		return fmt.Errorf("prompt.Mount: open store: %w", err)
	}
	s := &service{store: store, log: deps.Logger.New("subsystem", "prompts")}

	app.Get("/v1/prompts", s.list)
	app.Post("/v1/prompts", s.create)
	app.Get("/v1/prompts/:name", s.get)

	s.log.Info("prompts surface mounted", "store", "sqlite", "brand", deps.Brand)
	return nil
}

// list → { data: [PromptMeta], meta: {...} } (Langfuse-compatible envelope the
// console2 Prompts module already accepts).
func (s *service) list(c *zip.Ctx) error {
	metas, err := s.store.List(tenant(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "prompts: list: %v", err)
	}
	return s.json(c, http.StatusOK, map[string]any{
		"data": metas,
		"meta": map[string]any{"totalItems": len(metas), "page": 1},
	})
}

// get → the version list for a name (404 when the prompt has no versions).
func (s *service) get(c *zip.Ctx) error {
	name := c.Fiber().Params("name")
	versions, err := s.store.Get(tenant(c), name)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "prompts: get: %v", err)
	}
	if len(versions) == 0 {
		return zip.Errorf(http.StatusNotFound, "prompts: %q not found", name)
	}
	return s.json(c, http.StatusOK, map[string]any{"name": name, "versions": versions})
}

// create → store the next version; the body is a single PromptVersion.
func (s *service) create(c *zip.Ctx) error {
	var in PromptVersion
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.Errorf(http.StatusBadRequest, "prompts: invalid body: %v", err)
	}
	if in.Name == "" {
		return zip.Errorf(http.StatusBadRequest, "prompts: name required")
	}
	out, err := s.store.Create(tenant(c), in)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "prompts: create: %v", err)
	}
	return s.json(c, http.StatusOK, out)
}

func (s *service) json(c *zip.Ctx, status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "prompts: encode: %v", err)
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(status, b)
}

// tenant resolves the org slug prompts are scoped by — the canonical X-Project-Id
// sub-scope (what console2 stamps), then X-Org-Id, then the session org.
func tenant(c *zip.Ctx) string {
	if v := c.Header("X-Project-Id"); v != "" {
		return v
	}
	if v := c.Header("X-Org-Id"); v != "" {
		return v
	}
	return c.Org()
}

func init() {
	cloud.Register("prompts", 144, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("prompt.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}
