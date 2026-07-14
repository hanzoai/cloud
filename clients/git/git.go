// Package git mounts the Hanzo Cloud /v1/git surface: S3-backed Git hosting
// native in the unified cloud binary — Hanzo Git, the internal git host
// foundation agents push code into.
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
// Storage is bare git repos on a real filesystem (osfs) rooted under
// {DataDir}/git; go-git initializes + reads them, while the heavy clone/push/
// mirror paths stream through the `git` CLI (gitexec.go) so multi-GB packs stay
// bounded in memory. See storage.go for the hanzoai/vfs (S3) storage seam.
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

// state is git's own data; shared deps live in the embedded cloud.Base (the
// clone-URL host is s.Domain).
type state struct {
	stores  *cloud.OrgStore[*Store] // per-org repo-metadata DBs, opened once each
	storage *storage
	sshHost string     // for sshUrl construction (e.g. "git.hanzo.ai")
	gitHost string     // the HTTPS git host (e.g. "git.hanzo.ai"); root smart-HTTP is served only for this Host
	ssh     *sshServer // Git SSH transport listener
	keys    *keyStore  // SSH public-key registry (global fingerprint index)
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
	SSHURL        string   `json:"sshUrl"`
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

// sshURL is the scp-style Git SSH remote: git@<sshHost>:<org>/<repo>.git. The
// colon (not slash) after the host is the canonical scp-like syntax `git clone`
// accepts; the org/repo tail is the same path the SSH exec handler parses.
func sshURL(s *cloud.Service[state], org, name string) string {
	host := s.State.sshHost
	if host == "" {
		host = defaultSSHHost(s.Domain)
	}
	return fmt.Sprintf("git@%s:%s/%s.git", host, org, name)
}

func toView(s *cloud.Service[state], r Repo, branches []string, head string) repoView {
	return repoView{
		ID: r.ID, Org: r.Org, Project: r.Project, Name: r.Name, Description: r.Description,
		DefaultBranch: r.DefaultBranch, Branches: branches, Head: head,
		CloneURL:  cloneURL(s, r.Org, r.Name),
		SSHURL:    sshURL(s, r.Org, r.Name),
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
	gitRoot := filepath.Join(deps.DataDir, "git")
	st, err := newStorage(gitRoot)
	if err != nil {
		return fmt.Errorf("git.Mount: open storage: %w", err)
	}
	// The SSH public-key registry: ONE global file (the PublicKeyCallback runs
	// before any org is known, so auth is a single fingerprint lookup).
	keys, err := openKeyStore(filepath.Join(gitRoot, "ssh_keys.db"))
	if err != nil {
		return fmt.Errorf("git.Mount: open ssh key store: %w", err)
	}
	b := cloud.NewBase(deps, "git")
	s := &cloud.Service[state]{Base: b, State: state{
		stores:  cloud.NewOrgStore(deps.DataDir, "git", openStore),
		storage: st,
		sshHost: gitSSHHost(deps.Domain),
		gitHost: defaultSSHHost(deps.Domain),
		keys:    keys,
	}}
	mounted = s

	routes(app, s)

	// SSH transport: `git clone git@<sshHost>:<org>/<repo>.git`. The listener is
	// a per-process goroutine started here and stopped by Shutdown. The host key
	// is loaded from KMS/env (GIT_SSH_HOST_KEY) or generated + persisted
	// under the git data root.
	sshSrv, err := newSSHServer(s, sshConfig(deps, gitRoot))
	if err != nil {
		_ = keys.Close()
		return fmt.Errorf("git.Mount: init ssh: %w", err)
	}
	s.State.ssh = sshSrv
	if err := sshSrv.start(); err != nil {
		_ = keys.Close()
		return fmt.Errorf("git.Mount: start ssh: %w", err)
	}

	b.Log.Info("git mounted", "brand", deps.Brand, "storage", "osfs", "root", gitRoot,
		"sshHost", s.State.sshHost, "sshAddr", sshSrv.addr(), "zap", "/zap")
	return nil
}

// routes registers the git control plane + smart-HTTP + SSH-key + ZAP surface.
func routes(app *zip.App, s *cloud.Service[state]) {
	// Control plane (JSON). Static /repos + /usage register before the
	// smart-HTTP :org/:repo params so a real org can never shadow them.
	app.Post("/v1/git/repos", cloud.Handle(s, create))
	app.Get("/v1/git/repos", cloud.Handle(s, list))
	app.Get("/v1/git/usage", cloud.Handle(s, usage))
	app.Get("/v1/git/repos/:name", cloud.Handle(s, get))
	app.Delete("/v1/git/repos/:name", cloud.Handle(s, del))
	// Push generated files without a local git client (hanzo.app builder).
	// A distinct trailing segment, so it never shadows the :org/:repo routes.
	app.Post("/v1/git/repos/:name/push", cloud.Handle(s, pushFiles))
	// SSH public-key registry (per-user keys for `git clone git@…`).
	app.Post("/v1/git/keys", cloud.Handle(s, registerKey))
	app.Get("/v1/git/keys", cloud.Handle(s, listKeys))
	app.Delete("/v1/git/keys/:id", cloud.Handle(s, deleteKey))
	// Mirror an external repo into <org>/:name (creates the repo on first use).
	// A distinct trailing segment, so it never shadows the :org/:repo smart-HTTP
	// routes below.
	app.Post("/v1/git/repos/:name/mirror", cloud.Handle(s, mirror))
	// Repack a repo with a reachability bitmap + commit-graph so its next clone
	// serves fast (bitmap reuse, no full object-graph walk). Distinct trailing
	// segment, like /mirror — never shadows the :org/:repo smart-HTTP routes.
	app.Post("/v1/git/repos/:name/gc", cloud.Handle(s, maintain))

	// Smart-HTTP git protocol. These live under /v1/git/:org/:repo/* so
	// `git clone https://<host>/v1/git/<org>/<repo>.git` works natively.
	app.Get("/v1/git/:org/:repo/info/refs", cloud.Handle(s, infoRefs))
	app.Post("/v1/git/:org/:repo/git-upload-pack", cloud.Handle(s, uploadPack))
	app.Post("/v1/git/:org/:repo/git-receive-pack", cloud.Handle(s, receivePack))

	// Root-level smart-HTTP on the git host so `git clone
	// https://git.hanzo.ai/<org>/<repo>.git` works with the canonical git URL
	// (no /v1/git prefix). Guarded to s.State.gitHost via onGitHost: on the
	// api/console hosts these fall through (c.Next()), so a root :org/:repo can
	// never shadow another surface. Same handlers, same :org/:repo params.
	onGit := onGitHost(s.State.gitHost)
	app.Get("/:org/:repo/info/refs", onGit(cloud.Handle(s, infoRefs)))
	app.Post("/:org/:repo/git-upload-pack", onGit(cloud.Handle(s, uploadPack)))
	app.Post("/:org/:repo/git-receive-pack", onGit(cloud.Handle(s, receivePack)))

	// Browser UI — Hanzo Git's web surface (repo list/browse/blob/commits) at
	// /git/*, Hanzo Git's native web surface (ui.go).
	uiRoutes(app, s)

	// ZAP transport — the SAME control-plane core, reachable by browsers/services
	// that speak ZAP instead of REST, over the shared /zap plane. See zap.go.
	mountZAP(app, s)
}

// onGitHost gates a handler on the request Host matching the git host
// (e.g. git.hanzo.ai). The root-level smart-HTTP routes (/:org/:repo/*) are
// registered globally, so on the api/console hosts they must fall through
// (c.Next()) rather than serve — otherwise a bare /:org/:repo could shadow
// another surface. An empty git host disables the match entirely (always falls
// through), leaving /v1/git the only reachable smart-HTTP path.
func onGitHost(host string) func(zip.Handler) zip.Handler {
	return func(h zip.Handler) zip.Handler {
		return func(c *zip.Ctx) error {
			if host == "" || !strings.EqualFold(c.Fiber().Hostname(), host) {
				return c.Next()
			}
			return h(c)
		}
	}
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
	var body createReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	view, err := coreCreate(s, c.Context(), org, projectScope(c), body)
	if err != nil {
		return createErr(err)
	}
	return c.JSON(http.StatusCreated, view)
}

// createErr maps a coreCreate error to its HTTP status. The ONE mapping the REST
// adapter applies; the ZAP adapter (zapErr) maps the SAME sentinels to its own
// wire shape.
func createErr(err error) error {
	switch {
	case errors.Is(err, errBadInput):
		return zip.ErrBadRequest(strings.TrimPrefix(err.Error(), "git: invalid input: "))
	case errors.Is(err, errConflict):
		return zip.ErrConflict("repo name already exists in this scope")
	default:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
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
	out, err := coreList(s, c.Context(), org, projectScope(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

func get(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	view, err := coreGet(s, c.Context(), org, projectScope(c), c.Param("name"))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("repo not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return c.JSON(http.StatusOK, view)
}

func del(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	err := coreDelete(s, c.Context(), org, projectScope(c), c.Param("name"))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("repo not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
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
	out, err := coreUsage(s, c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
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

// orgRE constrains the org identifier to a safe path segment: alnum-led, no '/',
// no '\', no leading '.', so it can never be ".", "..", or "../x". The org
// becomes a storage PATH segment (absRepoPath), so this is the traversal guard at
// the git boundary — a SuperAdmin (or any caller) presenting X-Org-Id
// "../../etc" is rejected here, never reaching the filesystem. 128 chars covers
// every real IAM org slug.
var orgRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// org resolves the org — the org isolation KEY — from the gateway-minted
// X-Org-Id (HIP-0026), never lowercased or transformed (normalizing would
// collapse distinct owners into one bucket). Empty OR path-unsafe org is a true
// 403; there is no magic bucket. The orgRE gate makes absRepoPath's
// "can never traverse" invariant true for every entry point (HTTP handlers, the
// SSH key→org binding, mirror/push/create). Mirrors clients/prompts.org.
func org(c *zip.Ctx) (string, bool) {
	o, ok := principal.Org(c)
	if !ok || !orgRE.MatchString(o) {
		return "", false
	}
	return o, true
}

// projectScope resolves the optional X-Project-Id sub-scope through principal.Project
// (the ONE project accessor). The default scope — an absent header OR the literal
// "default" (principal.IsDefaultProject) — keys with NO project segment, so today's
// org-level repo keys stay un-suffixed. A non-default project is validated to a safe
// identifier; an invalid header degrades to the default scope rather than 400 (it is
// an OPTIONAL narrowing).
func projectScope(c *zip.Ctx) string {
	p := principal.Project(c)
	if principal.IsDefaultProject(p) || len(p) > 128 || !projectRE.MatchString(p) {
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

// Shutdown stops the SSH listener and closes every open store (per-org repo
// metadata + the SSH key registry). Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	if mounted.State.ssh != nil {
		mounted.State.ssh.stop()
	}
	err := mounted.State.stores.CloseAll()
	if mounted.State.keys != nil {
		if kerr := mounted.State.keys.Close(); kerr != nil && err == nil {
			err = kerr
		}
	}
	mounted = nil
	return err
}
