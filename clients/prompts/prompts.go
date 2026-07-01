// Package prompts mounts the Hanzo Cloud /v1/prompts surface: a per-org,
// versioned prompt library. Every prompt belongs to exactly one org (the
// gateway-minted X-Org-Id, HIP-0026); tenant isolation is the org column,
// enforced on every query, so one tenant can never read or mutate another's
// prompts. Creating a prompt whose name already exists appends a new version —
// real, inspectable history, never a fabricated rollup.
//
// Surface (all org-scoped; the shape console2's PromptsModule consumes):
//
//	GET    /v1/prompts            list current prompts for the org   -> {data:[PromptMeta]}
//	POST   /v1/prompts            create or add-a-version            -> PromptDetail
//	GET    /v1/prompts/metrics    real per-prompt stats              -> {data:[...]}
//	GET    /v1/prompts/:name      prompt detail + version history    -> PromptDetail
//	DELETE /v1/prompts/:name      delete a prompt (+ its versions)
//
// The store is SQLite in deps.DataDir (Base/SQLite-only mandate); it holds only
// template text + taxonomy, never a secret.
package prompts

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
)

// nameRE constrains a prompt name to a safe identifier. The name is the
// org-unique handle AND the URL path segment (/v1/prompts/:name), so this is
// the injection/traversal guard at the boundary.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// reserved names would collide with static sub-routes under /v1/prompts.
var reserved = map[string]bool{"metrics": true, "new": true}

type svc struct {
	store *Store
	log   luxlog.Logger
}

// mounted is the active service so Shutdown can release the store.
var mounted *svc

// ---- HTTP response shapes (the published contract console2 consumes) ----

// promptMeta is the list-row shape (console2 PromptMeta): name + version
// numbers + taxonomy + last-updated.
type promptMeta struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Versions      []int    `json:"versions"`
	Labels        []string `json:"labels"`
	Tags          []string `json:"tags"`
	LastUpdatedAt string   `json:"lastUpdatedAt"`
}

// promptDetail is the single-prompt shape: current content + full history.
type promptDetail struct {
	Name      string        `json:"name"`
	Type      string        `json:"type"`
	Prompt    string        `json:"prompt"`
	Version   int           `json:"version"`
	Labels    []string      `json:"labels"`
	Tags      []string      `json:"tags"`
	Versions  []versionView `json:"versionHistory"`
	CreatedAt string        `json:"createdAt"`
	UpdatedAt string        `json:"lastUpdatedAt"`
}

type versionView struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	Prompt    string `json:"prompt"`
	CreatedAt string `json:"createdAt"`
}

func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func versionNums(vs []Version) []int {
	out := make([]int, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Version)
	}
	return out
}

func toMeta(p Prompt, versions []int) promptMeta {
	return promptMeta{
		Name: p.Name, Type: p.Type, Versions: versions,
		Labels: nonNil(p.Labels), Tags: nonNil(p.Tags), LastUpdatedAt: rfc3339(p.UpdatedAt),
	}
}

func toDetail(p Prompt, vs []Version) promptDetail {
	hist := make([]versionView, 0, len(vs))
	for _, v := range vs {
		hist = append(hist, versionView{Version: v.Version, Type: v.Type, Prompt: v.Content, CreatedAt: rfc3339(v.CreatedAt)})
	}
	return promptDetail{
		Name: p.Name, Type: p.Type, Prompt: p.Content, Version: p.Version,
		Labels: nonNil(p.Labels), Tags: nonNil(p.Tags), Versions: hist,
		CreatedAt: rfc3339(p.CreatedAt), UpdatedAt: rfc3339(p.UpdatedAt),
	}
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// Mount wires the prompts surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("prompts.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("prompts.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "prompts")
	if deps.DataDir == "" {
		return fmt.Errorf("prompts.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("prompts.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "prompts.db"))
	if err != nil {
		return fmt.Errorf("prompts.Mount: open store: %w", err)
	}
	s := &svc{store: store, log: log}
	mounted = s

	// Static sub-routes are registered before the :name param route so a real
	// prompt can never shadow /metrics (and "metrics"/"new" are reserved names).
	app.Get("/v1/prompts", s.list)
	app.Post("/v1/prompts", s.create)
	app.Get("/v1/prompts/metrics", s.metrics)
	app.Get("/v1/prompts/:name", s.get)
	app.Delete("/v1/prompts/:name", s.del)

	log.Info("prompts mounted", "brand", deps.Brand)
	return nil
}

func init() {
	cloud.Register("prompts", 126, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("prompts.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// ---- handlers ----

type createReq struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Prompt string   `json:"prompt"`
	Labels []string `json:"labels"`
	Tags   []string `json:"tags"`
}

func (s *svc) create(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body createReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	if reserved[strings.ToLower(name)] {
		return zip.ErrBadRequest("name is reserved")
	}
	if !nameRE.MatchString(name) {
		return zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	typ := strings.TrimSpace(body.Type)
	if typ == "" {
		typ = "text"
	}
	id, err := genID("prompt")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	p := Prompt{
		ID: id, Org: org, Name: name, Type: typ, Content: body.Prompt,
		Labels: cleanList(body.Labels), Tags: cleanList(body.Tags), UpdatedAt: now,
	}
	saved, err := s.store.Upsert(c.Context(), p)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	vs, err := s.store.Versions(c.Context(), org, name)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "versions: %v", err)
	}
	return c.JSON(http.StatusCreated, toDetail(saved, vs))
}

func (s *svc) list(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]promptMeta, 0, len(rows))
	for _, p := range rows {
		vs, err := s.store.Versions(c.Context(), org, p.Name)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "versions: %v", err)
		}
		out = append(out, toMeta(p, versionNums(vs)))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

func (s *svc) get(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := nameParam(c)
	p, err := s.store.Get(c.Context(), org, name)
	if err == errNotFound {
		return zip.ErrNotFound("prompt not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	vs, err := s.store.Versions(c.Context(), org, name)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "versions: %v", err)
	}
	return c.JSON(http.StatusOK, toDetail(p, vs))
}

func (s *svc) del(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	deleted, err := s.store.Delete(c.Context(), org, nameParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("prompt not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// metricRow is a real per-prompt statistic (never fabricated): the number of
// versions, taxonomy, and timestamps for the org's prompts.
type metricRow struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	Versions      int    `json:"versions"`
	CurrentVer    int    `json:"currentVersion"`
	CreatedAt     string `json:"createdAt"`
	LastUpdatedAt string `json:"lastUpdatedAt"`
}

func (s *svc) metrics(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "metrics: %v", err)
	}
	out := make([]metricRow, 0, len(rows))
	for _, p := range rows {
		vs, err := s.store.Versions(c.Context(), org, p.Name)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "versions: %v", err)
		}
		out = append(out, metricRow{
			Name: p.Name, Type: p.Type, Versions: len(vs), CurrentVer: p.Version,
			CreatedAt: rfc3339(p.CreatedAt), LastUpdatedAt: rfc3339(p.UpdatedAt),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// ---- helpers ----

func nameParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("name")) }

// tenant resolves the org for a request. Empty org is allowed only for admins
// (bucketed under "admin"), matching every other native subsystem. The gateway
// strips client-supplied identity headers and sets X-Org-Id only on the
// JWT-validated path (HIP-0026), so it is not spoofable from the edge.
func tenant(c *zip.Ctx) (string, bool) {
	org := sanitizeOrg(c.Org())
	if org != "" {
		return org, true
	}
	if c.IsAdmin() {
		return "admin", true
	}
	return "", false
}

func sanitizeOrg(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 32 {
		out = strings.Trim(out[:32], "-")
	}
	return out
}

// cleanList trims, drops empties, caps each element, and de-dups a taxonomy
// slice so labels/tags stay tidy identifiers.
func cleanList(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" || len(x) > 64 || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
		if len(out) >= 32 {
			break
		}
	}
	return out
}

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// Shutdown closes the prompts store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.store == nil {
		return nil
	}
	err := mounted.store.Close()
	mounted = nil
	return err
}
