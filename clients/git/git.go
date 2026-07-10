// Package git mounts the Hanzo Cloud /v1/git surface: S3-backed Git hosting
// native in the unified cloud binary — the "internal Gitea" foundation agents
// push code into.
//
// A repo is the Git LAYER (source code, buildable/deployable) that lives UNDER
// an IAM project. It is NOT the IAM project itself: `project` is org-scoping
// CONTEXT (org → project → env); a repo is scoped BY that context. Every repo
// belongs to exactly one org (the gateway-minted X-Org-Id, HIP-0026) and an
// optional project sub-scope (X-Project-Id), enforced on every query, so one
// org can never read, clone, push to, or delete another's repos.
//
// Surface:
//
//	POST   /v1/git/repos            create a bare repo            -> repoView (201)
//	GET    /v1/git/repos            list the org's repos       -> {data:[repoView]}
//	GET    /v1/git/repos/:name      repo detail (branches, HEAD)  -> repoView
//	DELETE /v1/git/repos/:name      delete + purge storage        -> 204
//	GET    /v1/git/usage            per-repo + total bytes        -> usageView
//
// Smart-HTTP git protocol (so `git clone` / `git push` work natively):
//
//	GET  /v1/git/:org/:repo/info/refs?service=git-upload-pack|git-receive-pack
//	POST /v1/git/:org/:repo/git-upload-pack     (clone/fetch)
//	POST /v1/git/:org/:repo/git-receive-pack    (push)
//
// Storage is a go-billy filesystem holding bare go-git repos; go-git's server
// transport reads/writes it for clone AND push. The billy home is osfs rooted
// under {DataDir}/git; see storage.go for the hanzoai/vfs (S3) storage seam.
//
// Billing: every repo tracks sizeBytes, re-measured on create and after each
// push. /v1/git/usage exposes per-repo + total bytes per org, and each
// measurement emits a "git.usage" log line a metering consumer can bill on.
package git

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// nameRE constrains a repo name to a safe identifier. The name is the
// org-unique handle AND the URL path segment AND the storage path segment,
// so this is the injection/traversal guard at the boundary. A trailing ".git"
// is stripped before matching (clients clone "<name>.git").
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// defaultBranchName is the default branch a fresh bare repo points HEAD at.
const defaultBranchName = "main"

// maxBody caps the git protocol request body a single POST accepts (upload-pack
// wants/haves + receive-pack pack). Bounds memory since handlers buffer the
// body; a very large monorepo push exceeding this uses git's chunked negotiation
// which stays under the cap per request.
const maxBody = 256 << 20 // 256 MiB

// state is git's own data; shared deps live in the embedded cloud.Base (the
// clone-URL host is s.Domain).
type state struct {
	stores  *cloud.OrgStore[*Store] // per-org repo-metadata DBs, opened once each
	storage *storage
}

// storeFor resolves the caller's org-scoped repo-metadata store, opening the
// per-org file ({DataDir}/orgs/{orgSlug}/git.db) once via the shared cache. git
// is org-scoped (not project-scoped) so /v1/git/usage stays a single org-wide
// rollup across every project sub-scope.
func storeFor(s *cloud.Service[state], org string) (*Store, error) {
	return s.State.stores.For(org, "")
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// ---- HTTP response shapes ----

type repoView struct {
	ID            string   `json:"id"`
	Org           string   `json:"org"`
	Project       string   `json:"project,omitempty"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	DefaultBranch string   `json:"defaultBranch"`
	Branches      []string `json:"branches,omitempty"`
	Head          string   `json:"head,omitempty"`
	CloneURL      string   `json:"cloneUrl"`
	SizeBytes     int64    `json:"sizeBytes"`
	CreatedAt     string   `json:"createdAt"`
	UpdatedAt     string   `json:"updatedAt,omitempty"`
}

func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func cloneURL(s *cloud.Service[state], org, name string) string {
	host := s.Domain
	if host == "" {
		host = "api.hanzo.ai"
	}
	return fmt.Sprintf("https://%s/v1/git/%s/%s.git", host, org, name)
}

func toView(s *cloud.Service[state], r Repo, branches []string, head string) repoView {
	return repoView{
		ID: r.ID, Org: r.Org, Project: r.Project, Name: r.Name, Description: r.Description,
		DefaultBranch: r.DefaultBranch, Branches: branches, Head: head,
		CloneURL:  cloneURL(s, r.Org, r.Name),
		SizeBytes: r.SizeBytes, CreatedAt: rfc3339(r.CreatedAt), UpdatedAt: rfc3339(r.UpdatedAt),
	}
}

// Mount wires the git surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("git.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("git.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("git.Mount: empty DataDir")
	}
	st, err := newStorage(filepath.Join(deps.DataDir, "git"))
	if err != nil {
		return fmt.Errorf("git.Mount: open storage: %w", err)
	}
	b := cloud.NewBase(deps, "git")
	s := &cloud.Service[state]{Base: b, State: state{
		stores:  cloud.NewOrgStore(deps.DataDir, "git", openStore),
		storage: st,
	}}
	mounted = s

	routes(app, s)

	b.Log.Info("git mounted", "brand", deps.Brand, "storage", "osfs", "root", filepath.Join(deps.DataDir, "git"))
	return nil
}

// routes registers the git control plane + smart-HTTP surface.
func routes(app *zip.App, s *cloud.Service[state]) {
	// Control plane (JSON). Static /repos + /usage register before the
	// smart-HTTP :org/:repo params so a real org can never shadow them.
	app.Post("/v1/git/repos", cloud.Handle(s, create))
	app.Get("/v1/git/repos", cloud.Handle(s, list))
	app.Get("/v1/git/usage", cloud.Handle(s, usage))
	app.Get("/v1/git/repos/:name", cloud.Handle(s, get))
	app.Delete("/v1/git/repos/:name", cloud.Handle(s, del))
	// Mirror an external repo into <org>/:name (creates the repo on first use).
	// A distinct trailing segment, so it never shadows the :org/:repo smart-HTTP
	// routes below.
	app.Post("/v1/git/repos/:name/mirror", cloud.Handle(s, mirror))

	// Smart-HTTP git protocol. These live under /v1/git/:org/:repo/* so
	// `git clone https://<host>/v1/git/<org>/<repo>.git` works natively.
	app.Get("/v1/git/:org/:repo/info/refs", cloud.Handle(s, infoRefs))
	app.Post("/v1/git/:org/:repo/git-upload-pack", cloud.Handle(s, uploadPack))
	app.Post("/v1/git/:org/:repo/git-receive-pack", cloud.Handle(s, receivePack))
}

// ---- control-plane handlers ----

type createReq struct {
	Name        string `json:"name"`
	Project     string `json:"project"`
	Description string `json:"description"`
}

func create(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	var body createReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := normalizeName(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	if !nameRE.MatchString(name) {
		return zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	// Project sub-scope: explicit body value wins, else the header sub-scope.
	project := strings.TrimSpace(body.Project)
	if project == "" {
		project = projectScope(c)
	} else if !projectRE.MatchString(project) {
		return zip.ErrBadRequest("project must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	if len(body.Description) > 4096 {
		return zip.ErrBadRequest("description too large (max 4KiB)")
	}

	id, err := genID("repo")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	r := Repo{
		ID: id, Org: org, Project: project, Name: name,
		Description: strings.TrimSpace(body.Description), DefaultBranch: defaultBranchName,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := provision(s, c.Context(), store, r); err != nil {
		if errors.Is(err, errConflict) {
			return zip.ErrConflict("repo name already exists in this scope")
		}
		return zip.Errorf(http.StatusInternalServerError, "provision: %v", err)
	}
	// Record initial storage size (billing hook: an empty bare repo is a few KiB).
	r.SizeBytes = recordUsage(s, c.Context(), org, project, name)
	return c.JSON(http.StatusCreated, toView(s, r, nil, ""))
}

// provision materializes a repo: its metadata row plus an empty bare repo on
// storage. The ONE way a repo comes into being — create and mirror both compose
// it. On a storage-init failure the metadata row is rolled back so a partial
// provision never leaves a phantom repo. Returns errConflict when the row
// already exists (the caller decides whether that is fatal).
func provision(s *cloud.Service[state], ctx context.Context, store *Store, r Repo) error {
	if err := store.Create(ctx, r); err != nil {
		return err // errConflict, or a wrapped insert error
	}
	if err := s.State.storage.initBare(r.Org, r.Project, r.Name, r.DefaultBranch); err != nil {
		_, _ = store.Delete(ctx, r.Org, r.Project, r.Name)
		return fmt.Errorf("init repo: %w", err)
	}
	return nil
}

func list(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.List(c.Context(), org, projectScope(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]repoView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toView(s, r, nil, ""))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

func get(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	name := normalizeName(c.Param("name"))
	project := projectScope(c)
	r, err := store.Get(c.Context(), org, project, name)
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("repo not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	branches, head := refState(s, org, project, name)
	return c.JSON(http.StatusOK, toView(s, r, branches, head))
}

func del(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	name := normalizeName(c.Param("name"))
	project := projectScope(c)
	deleted, err := store.Delete(c.Context(), org, project, name)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("repo not found")
	}
	// Purge storage. Metadata is already gone, so a purge failure must not
	// resurrect the repo — log and continue.
	if err := s.State.storage.remove(org, project, name); err != nil {
		s.Log.Warn("purge repo storage failed (continuing)", "org", org, "project", project, "repo", name, "err", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- usage / billing ----

type usageRepo struct {
	Name      string `json:"name"`
	Project   string `json:"project,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
}

type usageView struct {
	Org        string      `json:"org"`
	TotalBytes int64       `json:"totalBytes"`
	Repos      []usageRepo `json:"repos"`
}

// usage returns per-repo + total storage bytes for the tenant — the queryable,
// per-tenant number commerce/o11y meter on. Org-wide (across every project) so
// a billing consumer sees the whole tenant footprint in one call.
func usage(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.ListOrg(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "usage: %v", err)
	}
	out := usageView{Org: org, Repos: make([]usageRepo, 0, len(rows))}
	for _, r := range rows {
		out.Repos = append(out.Repos, usageRepo{Name: r.Name, Project: r.Project, SizeBytes: r.SizeBytes})
		out.TotalBytes += r.SizeBytes
	}
	return c.JSON(http.StatusOK, out)
}

// recordUsage re-measures a repo's on-disk size, persists it, and emits the
// meterable "git.usage" log line consumed by commerce/metering. Returns the
// measured size (0 on any error; usage is best-effort and never fails the
// caller's operation).
func recordUsage(s *cloud.Service[state], ctx context.Context, org, project, name string) int64 {
	size, err := s.State.storage.sizeBytes(org, project, name)
	if err != nil {
		s.Log.Warn("measure repo size failed", "org", org, "project", project, "repo", name, "err", err)
		return 0
	}
	store, err := storeFor(s, org)
	if err != nil {
		s.Log.Warn("record repo size failed (open store)", "org", org, "project", project, "repo", name, "err", err)
		return size
	}
	if err := store.SetSize(ctx, org, project, name, size, time.Now().Unix()); err != nil {
		s.Log.Warn("record repo size failed", "org", org, "project", project, "repo", name, "err", err)
	}
	s.Log.Info("git.usage", "org", org, "project", project, "repo", name, "bytes", size)
	return size
}

// refState reads the repo's branches and resolved HEAD for the detail view.
// Best-effort: a read error yields empty state rather than failing the request.
func refState(s *cloud.Service[state], org, project, name string) (branches []string, head string) {
	st, err := s.State.storage.storer(org, project, name)
	if err != nil {
		return nil, ""
	}
	repo, err := gogit.Open(st, nil)
	if err != nil {
		return nil, ""
	}
	if h, err := repo.Head(); err == nil {
		head = h.Hash().String()
	}
	iter, err := repo.Branches()
	if err != nil {
		return branches, head
	}
	defer iter.Close()
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		branches = append(branches, ref.Name().Short())
		return nil
	})
	return branches, head
}

// ---- helpers ----

// projectRE mirrors nameRE — a project sub-scope is a safe identifier.
var projectRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// normalizeName trims whitespace and a trailing ".git" (clients clone
// "<name>.git"). The result is validated by nameRE at the create boundary.
func normalizeName(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimSuffix(s, ".git")
}

// repoNameParam extracts and validates the :repo path segment for smart-HTTP
// routes, stripping the ".git" suffix git clients append.
func repoNameParam(c *zip.Ctx) (string, error) {
	name := normalizeName(c.Param("repo"))
	if name == "" || !nameRE.MatchString(name) {
		return "", zip.ErrBadRequest("invalid repo name")
	}
	return name, nil
}

// org resolves the org — the org isolation KEY — from the gateway-minted
// X-Org-Id (HIP-0026), never lowercased or transformed (normalizing would
// collapse distinct owners into one bucket). Empty org is a true 403; there is
// no magic bucket. Mirrors clients/prompts.org.
func org(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// projectScope resolves the optional X-Project-Id sub-scope. Empty is valid
// (an org-level repo). Validated to a safe identifier; an invalid header is
// treated as no sub-scope rather than 400 (it is an OPTIONAL narrowing).
func projectScope(c *zip.Ctx) string {
	p := strings.TrimSpace(c.Header("X-Project-Id"))
	if p == "" || len(p) > 128 || !projectRE.MatchString(p) {
		return ""
	}
	return p
}

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// Shutdown closes every open per-org git store. Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.stores.CloseAll()
	mounted = nil
	return err
}
