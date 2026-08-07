// Package git is Git hosting for your org: create repos, clone, push, and see what
// they cost.
//
// It mounts the Hanzo Cloud /v1/git surface: S3-backed Git hosting native in the
// unified cloud binary — Hanzo Git, the internal git host foundation agents push
// code into.
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
// A project-scoped repo names its project as a middle segment
// (/v1/git/:org/:project/:repo/…, git@host:org/project/repo.git). The scope
// otherwise rides X-Project-Id, and a git client sends no headers, so the path
// is the only channel that reaches a remote. Two names are only unique within
// one project — hanzo/hanzo-apps/ai and hanzo/hanzo-docs/ai are distinct repos.
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
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/brand"
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
	dataDir string     // base data dir; {dataDir}/orgs/{slug} is enumerated for the public explore
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
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// mounted is the active service so Shutdown can release the store. It is read by
// DETACHED lifecycle-reactor goroutines (notify / mirror-out / index-on-push) and
// written by Mount/Shutdown, so it is an atomic.Pointer: a reactor goroutine that
// outlives a Shutdown (or a test's Mount↔Shutdown cycle) reads it race-free (nil ⇒
// unmounted, no-op) instead of tearing against the Shutdown write.
var mounted atomic.Pointer[cloud.Service[state]]

// ---- HTTP response shapes ----

// repoView is one repo as the control plane reports it.
type repoView struct {
	// ID is the repo's stable, prefixed identifier ("repo_" + 128 random bits).
	ID string `json:"id"`
	// Org owns the repo — the gateway-minted X-Org-Id, and the isolation key.
	Org string `json:"org"`
	// Project is the optional sub-scope the repo lives in; absent for the org's
	// default scope.
	Project string `json:"project,omitempty"`
	// Name is the org-unique handle, and the last path segment of both URLs below.
	Name string `json:"name"`
	// Description is the caller-supplied blurb (max 4KiB).
	Description string `json:"description,omitempty"`
	// DefaultBranch is where HEAD points on a fresh repo ("main").
	DefaultBranch string `json:"defaultBranch"`
	// Public grants ANONYMOUS read (fetch) only; push and the whole control plane
	// stay org-authed.
	Public bool `json:"public"`
	// Branches are the repo's branch names. Read live, so the detail view carries
	// them and a list row does not.
	Branches []string `json:"branches,omitempty"`
	// Head is the resolved HEAD commit, empty on an empty repo.
	Head string `json:"head,omitempty"`
	// CloneURL is the HTTPS smart-HTTP remote `git clone` takes.
	CloneURL string `json:"cloneUrl"`
	// SSHURL is the scp-style SSH remote (git@host:org/repo.git).
	SSHURL string `json:"sshUrl"`
	// SizeBytes is the repo's measured on-disk size, re-measured on create, after
	// each push, and after a gc. This is the number billing meters.
	SizeBytes int64 `json:"sizeBytes"`
	// CreatedAt is RFC 3339 UTC.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is RFC 3339 UTC, empty until the first write.
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// repoList is the collection envelope every git list op answers with.
type repoList struct {
	// Data holds the repos in scope, most recently updated first.
	Data []repoView `json:"data"`
}

func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

// cloneURL is the HTTPS remote for a repo. An org-level repo keeps the
// two-segment path it has always had; a project-scoped one names its project as
// the middle segment, because `git clone` sends no headers and the URL is the
// only place the scope can travel.
func cloneURL(s *cloud.Service[state], org, project, name string) string {
	host := s.Domain
	if host == "" { // only a hand-built Deps; Config.Validate requires a domain
		host = brand.APIHost(brand.Default)
	}
	if project == "" {
		return fmt.Sprintf("https://%s/v1/git/%s/%s.git", host, org, name)
	}
	return fmt.Sprintf("https://%s/v1/git/%s/%s/%s.git", host, org, project, name)
}

// sshURL is the scp-style Git SSH remote: git@<sshHost>:<org>/<repo>.git. The
// colon (not slash) after the host is the canonical scp-like syntax `git clone`
// accepts; the org/repo tail is the same path the SSH exec handler parses.
func sshURL(s *cloud.Service[state], org, project, name string) string {
	host := s.State.sshHost
	if host == "" {
		host = defaultSSHHost(s.Domain)
	}
	if project == "" {
		return fmt.Sprintf("git@%s:%s/%s.git", host, org, name)
	}
	return fmt.Sprintf("git@%s:%s/%s/%s.git", host, org, project, name)
}

func toView(s *cloud.Service[state], r Repo, branches []string, head string) repoView {
	return repoView{
		ID: r.ID, Org: r.Org, Project: r.Project, Name: r.Name, Description: r.Description,
		DefaultBranch: r.DefaultBranch, Public: r.Public, Branches: branches, Head: head,
		CloneURL:  cloneURL(s, r.Org, r.Project, r.Name),
		SSHURL:    sshURL(s, r.Org, r.Project, r.Name),
		SizeBytes: r.SizeBytes, CreatedAt: rfc3339(r.CreatedAt), UpdatedAt: rfc3339(r.UpdatedAt),
	}
}

// Mount wires the git surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("git.Mount: nil app")
	}
	// git registers TYPED ops, which live on the *zip.App's registry (ops.go) —
	// they are DECLARED on the /v1/git group (routes below), which resolves to
	// that same registry. Checked before anything is built so a Router that
	// cannot carry them fails the mount rather than serving a surface no
	// projection knows about.
	if cloud.ZipApp(app) == nil {
		return fmt.Errorf("git.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
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
	keys, err := openKeyStore(gitRoot)
	if err != nil {
		return fmt.Errorf("git.Mount: open ssh key store: %w", err)
	}
	b := cloud.NewBase(deps, "git")
	s := &cloud.Service[state]{Base: b, State: state{
		stores:  cloud.NewOrgStore(b, "git", openStore),
		storage: st,
		dataDir: deps.DataDir,
		sshHost: gitSSHHost(deps.Domain),
		gitHost: defaultSSHHost(deps.Domain),
		keys:    keys,
	}}
	mounted.Store(s)

	routes(app, s)
	registerLifecycleReactors()
	// Install the git object-plane importer so the integrations plane (GitHub App)
	// can create + mirror-in + fast-forward-sync repos with no integrations⇄git cycle.
	cloud.RegisterGitImporter(githubImporter{})
	// Install the outbound-mirror controller so the universal sync engine's git
	// provider can ensure/remove a repo's mirror target through the SAME store the
	// mirror_out reactor pushes from — no sync⇆git cycle (mirror_control.go).
	cloud.RegisterGitMirrorController(gitMirrorController{})
	// Install the visibility subscriber so a project published on hanzo.app gets
	// its canonical repo, world-readable exactly when the project is
	// (community.go). Next to fall to the internal plane; until then the seam
	// stays registered — unregistered it is a silent no-op and public projects
	// stop getting repos.
	// Visibility on the internal plane (community.go), NOT a Register* seam: the
	// caller is projects, in its own process.
	exposePublish()
	// Publish the delivery inventory read on the internal plane, so apps/deploy
	// renders from a tree read instead of cloning (files.go).
	exposeFiles()
	// Import, inbound sync AND repo status — one seam, three ops (import_plane.go).
	exposeImport()
	// The sync engine decides a mirror should exist; this app owns the repos and
	// the reactor that pushes them. Registered above for the co-resident case, and
	// published here for the split one (mirror_control.go).
	exposeMirror()
	// Delegate ONE ref write to a process running untrusted work, so it does not
	// have to hold a credential that opens the rest of the tenant (grant.go).
	exposeGrant()
	// What the org keeps in git, as headline numbers — the read a caller makes
	// when it does not yet know a repo's name (figures_rpc.go).
	exposeFigures()

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

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the git control plane + smart-HTTP + SSH-key + ZAP surface.
//
// Two registrars, one router. zip.<Verb>(g, …) registers a TYPED op — a route
// plus the registry entry OpenAPI / MCP / the CLI are projected from (ops.go).
// It is declared on the GROUP, so the /v1/git prefix lives in exactly one place
// and each op's path is the prefix composed with its leaf, the same composition
// the router does and the identity every projection keys on (cmd/zipdoc resolves
// it the same way as of zip v1.18.3). g.<Verb>(…) stays for the routes a typed op
// cannot express: a raw pack stream, an HTML page, a tombstone with no In and no
// Out, a ZAP envelope. The two are interleaved in the ORIGINAL order
// because fiber resolves by registration order, and that order is load-bearing
// here (see below).
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	g := app.Group("/v1/git")
	// The principal bridge first: every typed op below reads its tenant off the
	// request context, and a Use only runs ahead of routes registered after it.
	g.Use(zip.H(bridgePrincipal))

	// Control plane (JSON). Static /repos + /usage register before the
	// smart-HTTP :org/:repo params so a real org can never shadow them.
	zip.Post(g, "/repos", o.createRepo, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/repos", o.listRepos)
	zip.Get(g, "/usage", o.usage)
	zip.Get(g, "/repos/:name", o.getRepo)
	zip.Patch(g, "/repos/:name", o.setVisibility)
	zip.Delete(g, "/repos/:name", o.deleteRepo)
	// Push generated files without a local git client (hanzo.app builder).
	// A distinct trailing segment, so it never shadows the :org/:repo routes.
	zip.Post(g, "/repos/:name/push", o.pushFiles)
	// The retired forge push door (webhook.go): a TOMBSTONE answering every
	// delivery 410 and naming platform.hanzo.ai, kept because a 404 from this
	// estate reads as "the API is switched off". A static segment that never
	// shadows the :org/:repo smart-HTTP routes; cloud.Terminal writes the 410
	// in-band so the commerce /v1 ErrorHandlerJSON (co-mounted ahead) cannot
	// flatten it to 500.
	//
	// Untyped, deliberately: this is the door a forge's own webhook protocol
	// delivered to, and it now reads no request and returns no value — a typed
	// op is built from an In or an Out, and a tombstone has neither.
	g.Post("/webhook", cloud.Terminal(cloud.Handle(s, webhook)))
	// SSH public-key registry (per-user keys for `git clone git@…`).
	zip.Post(g, "/keys", o.registerKey, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/keys", o.listKeys)
	zip.Delete(g, "/keys/:id", o.deleteKey)
	// Mirror an external repo into <org>/:name (creates the repo on first use).
	// A distinct trailing segment, so it never shadows the :org/:repo smart-HTTP
	// routes below.
	zip.Post(g, "/repos/:name/mirror", o.mirror)
	// Repack a repo with a reachability bitmap + commit-graph so its next clone
	// serves fast (bitmap reuse, no full object-graph walk). Distinct trailing
	// segment, like /mirror — never shadows the :org/:repo smart-HTTP routes.
	zip.Post(g, "/repos/:name/gc", o.gc)

	// Repo-lifecycle config: Slack-channel subscriptions (notify.go) + downstream
	// mirror targets (mirror_out.go). Distinct trailing segments, so they never
	// shadow the :org/:repo smart-HTTP routes below. Org-scoped like every repo op.
	zip.Post(g, "/repos/:name/subscriptions", o.subscribe, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/repos/:name/subscriptions", o.listSubscriptions)
	zip.Delete(g, "/repos/:name/subscriptions/:id", o.unsubscribe)
	zip.Post(g, "/repos/:name/mirrors", o.addMirror, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/repos/:name/mirrors", o.listMirrors)
	zip.Delete(g, "/repos/:name/mirrors/:id", o.deleteMirror)

	// Pull requests (pulls.go): propose a branch, read what is waiting, merge it.
	// The noun that closes the agent loop — a run pushes refs/heads/agent/<run>
	// and this is where it says what the branch is for and where a person says
	// yes. Distinct trailing segments, so they never shadow the :org/:repo
	// smart-HTTP routes below. Org-scoped like every repo op.
	zip.Post(g, "/repos/:name/pulls", o.openPull, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/repos/:name/pulls", o.listPulls)
	zip.Get(g, "/repos/:name/pulls/:number", o.getPull)
	zip.Post(g, "/repos/:name/pulls/:number/merge", o.mergePull)

	// Read/browse surface (JSON) for the console repo-browser: refs, tree, blob,
	// commits, readme. ref + path ride as ?ref=&path= query params (the UI's own
	// convention), so a slashed branch is unambiguous. Distinct trailing segments —
	// they never shadow the :org/:repo smart-HTTP routes below. Org-scoped like every
	// repo op; the JSON twin of the HTML browser in ui.go (one set of read helpers).
	zip.Get(g, "/repos/:name/refs", o.browseRefs)
	zip.Get(g, "/repos/:name/tree", o.browseTree)
	zip.Get(g, "/repos/:name/files", o.browseFiles)
	zip.Get(g, "/repos/:name/blob", o.browseBlob)
	zip.Get(g, "/repos/:name/commits", o.browseCommits)
	zip.Get(g, "/repos/:name/readme", o.browseReadme)

	// Smart-HTTP git protocol. These live under /v1/git/:org/:repo/* so
	// `git clone https://<host>/v1/git/<org>/<repo>.git` works natively. They
	// stay raw because they speak git's own pack protocol — pkt-line
	// advertisements in, STREAMED packfiles out, JSON in neither direction.
	g.Get("/:org/:repo/info/refs", cloud.Handle(s, infoRefs))
	g.Post("/:org/:repo/git-upload-pack", cloud.Handle(s, uploadPack))
	g.Post("/:org/:repo/git-receive-pack", cloud.Handle(s, receivePack))

	// The same protocol one segment deeper, for a project-scoped repo. The scope
	// otherwise rides X-Project-Id, which `git clone` cannot send, so without a
	// path form a project-scoped repo has no usable remote. Distinct segment
	// count from the routes above, so the org-level form is untouched.
	g.Get("/:org/:project/:repo/info/refs", cloud.Handle(s, infoRefs))
	g.Post("/:org/:project/:repo/git-upload-pack", cloud.Handle(s, uploadPack))
	g.Post("/:org/:project/:repo/git-receive-pack", cloud.Handle(s, receivePack))

	// Root-level smart-HTTP on the git host so `git clone
	// https://git.hanzo.ai/<org>/<repo>.git` works with the canonical git URL
	// (no /v1/git prefix). Guarded to s.State.gitHost via onGitHost: on the
	// api/console hosts these fall through (c.Next()), so a root :org/:repo can
	// never shadow another surface. Same handlers, same params.
	onGit := onGitHost(s.State.gitHost)
	app.Get("/:org/:repo/info/refs", onGit(cloud.Handle(s, infoRefs)))
	app.Post("/:org/:repo/git-upload-pack", onGit(cloud.Handle(s, uploadPack)))
	app.Post("/:org/:repo/git-receive-pack", onGit(cloud.Handle(s, receivePack)))
	app.Get("/:org/:project/:repo/info/refs", onGit(cloud.Handle(s, infoRefs)))
	app.Post("/:org/:project/:repo/git-upload-pack", onGit(cloud.Handle(s, uploadPack)))
	app.Post("/:org/:project/:repo/git-receive-pack", onGit(cloud.Handle(s, receivePack)))

	// Browser UI — Hanzo Git's web surface (repo list/browse/blob/commits) at
	// /git/*, Hanzo Git's native web surface (ui.go).
	uiRoutes(app, s)

	// ZAP transport — the SAME control-plane core, reachable by browsers/services
	// that speak ZAP instead of REST, over the shared /zap plane. See zap.go.
	mountZAP(app, s)
}

// lifecycleOnce guards the ONE registration of git's lifecycle reactors into the
// cloud fan-out. Registration is process-once (the list APPENDS, unlike the
// single-registrant push builder), so a repeated Mount — e.g. across tests — can
// never stack duplicate reactors; both closures read the current `mounted` at event
// time and no-op when the subsystem is unmounted.
var lifecycleOnce sync.Once

// registerLifecycleReactors wires git's three subscribers onto the cloud lifecycle
// stream: the Slack-notifier (notify.go), the outbound mirror (mirror_out.go), and
// the code-index reactor (index_on_push.go). All run detached + best-effort
// (EmitLifecycle dispatches each in its own goroutine), so none can block or fail
// the git/deploy path.
func registerLifecycleReactors() {
	lifecycleOnce.Do(func() {
		cloud.RegisterLifecycleSubscriber(func(ctx context.Context, ev cloud.LifecycleEvent) {
			if s := mounted.Load(); s != nil {
				notifyLifecycle(s, ctx, ev)
			}
		})
		cloud.RegisterLifecycleSubscriber(func(ctx context.Context, ev cloud.LifecycleEvent) {
			if s := mounted.Load(); s != nil {
				mirrorOutbound(s, ctx, ev)
			}
		})
		cloud.RegisterLifecycleSubscriber(func(ctx context.Context, ev cloud.LifecycleEvent) {
			if s := mounted.Load(); s != nil {
				indexOnPush(s, ctx, ev)
			}
		})
		// Native CI/CD orchestrator (build_on_push.go): a push on native git →
		// build+deploy on our executor, NO GitHub Actions. Ships DORMANT (no-op
		// unless CLOUD_NATIVE_CICD_ENABLED + the enqueue token are set), so linking
		// it here is inert until the operator arms it on the git App CR.
		cloud.RegisterLifecycleSubscriber(func(ctx context.Context, ev cloud.LifecycleEvent) {
			if s := mounted.Load(); s != nil {
				buildOnPush(s, ctx, ev)
			}
		})
	})
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

// createReq is the create-repo request body, and the In of createRepo. It is the
// ONE shape both transports decode into — the ZAP procedure (zap.go) fills it
// from its own envelope and hands it to the same core func.
type createReq struct {
	// Name is the repo's handle, unique within the scope, and the last segment of
	// both clone URLs. Must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$; a trailing
	// ".git" is stripped first. Required.
	Name string `json:"name"`
	// Project narrows the repo to a sub-scope of the org. Omit it to use the
	// caller's own X-Project-Id scope; it can never widen past the caller's org.
	Project string `json:"project"`
	// Description is a free-form blurb, max 4KiB.
	Description string `json:"description"`
	// Public grants ANONYMOUS read (fetch) only; push and the whole control plane
	// stay org-authed. Defaults to false.
	Public bool `json:"public"`
}

// createRepo provisions an empty bare repository in the caller's scope and
// returns it with its clone URLs. Answers 201. The name must be unique within
// the scope — a repeat is a 409, never a silent overwrite of an existing repo.
// The org comes from the validated principal, so a repo is always born owned by
// the caller's own tenant.
//
// Example: {"name": "widgets", "description": "the widget service"}
func (o ops) createRepo(ctx context.Context, in *createReq) (*repoView, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	view, err := coreCreate(o.s, ctx, t.org, t.project, *in)
	if err != nil {
		return nil, createErr(err)
	}
	return &view, nil
}

// patchIn carries the mutable repo settings. Public is a pointer so "absent" and
// "false" are distinguishable and a PATCH changes exactly what the caller sent.
type patchIn struct {
	// Name is the repo to update, from the :name path segment.
	Name string `json:"name"`
	// Public flips anonymous read access. Omit it and the request is refused —
	// there is nothing else to update yet.
	Public *bool `json:"public"`
}

// setVisibility flips a repo's public bit, the one mutable repo setting today.
// Public grants ANONYMOUS fetch only; push and the whole control plane stay
// org-authed. Returns the updated repo.
//
// Example: {"name": "widgets", "public": true}
func (o ops) setVisibility(ctx context.Context, in *patchIn) (*repoView, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if in.Public == nil {
		return nil, zip.ErrBadRequest("nothing to update (supported: public)")
	}
	view, err := coreSetVisibility(o.s, ctx, t.org, t.project, in.Name, *in.Public)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("repo not found")
	}
	if err != nil {
		return nil, internalErr(err)
	}
	return &view, nil
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

// listRepos returns the repos in the caller's scope, most recently updated
// first. The scope is the request principal's — the gateway-minted org and its
// optional project — never anything off the wire, so a caller only ever sees its
// own. Rows carry no branches or HEAD; read one repo for those.
//
// Example: {}
//
//	Response: {"data": [{"id": "repo_9f3c", "org": "acme", "name": "widgets",
//		"defaultBranch": "main", "public": false,
//		"cloneUrl": "https://api.hanzo.ai/v1/git/acme/widgets.git",
//		"sshUrl": "git@git.hanzo.ai:acme/widgets.git", "sizeBytes": 4096,
//		"createdAt": "2026-07-01T10:00:00Z"}]}
func (o ops) listRepos(ctx context.Context, _ *noInput) (*repoList, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	out, err := coreList(o.s, ctx, t.org, t.project)
	if err != nil {
		return nil, internalErr(err)
	}
	return &repoList{Data: out}, nil
}

// getRepo returns one repo with its live ref state: every branch name and the
// resolved HEAD commit. Both are read from the object store on each call, so an
// empty repo reports no branches and an empty head rather than failing. A repo
// outside the caller's scope is not found.
//
// Example: {"name": "widgets"}
func (o ops) getRepo(ctx context.Context, in *repoRef) (*repoView, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	view, err := coreGet(o.s, ctx, t.org, t.project, in.Name)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("repo not found")
	}
	if err != nil {
		return nil, internalErr(err)
	}
	return &view, nil
}

// deleteRepo removes a repo's metadata and purges its storage. Answers 204 with
// no body. The metadata row is the source of truth for existence, so a storage
// purge that fails is logged and the delete still succeeds — and a second call
// is a 404, not a second delete.
//
// Example: {"name": "widgets"}
func (o ops) deleteRepo(ctx context.Context, in *repoRef) (*noContent, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	err = coreDelete(o.s, ctx, t.org, t.project, in.Name)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("repo not found")
	}
	if err != nil {
		return nil, internalErr(err)
	}
	return nil, nil
}

// ---- usage / billing ----

// usageRepo is one repo's share of the org's storage bill.
type usageRepo struct {
	// Name is the repo's org-unique handle.
	Name string `json:"name"`
	// Project is the sub-scope the repo lives in; absent for the default scope.
	Project string `json:"project,omitempty"`
	// SizeBytes is the repo's on-disk size at its last measurement.
	SizeBytes int64 `json:"sizeBytes"`
}

// usageView is the org's storage rollup.
type usageView struct {
	// Org the rollup is for.
	Org string `json:"org"`
	// TotalBytes is the sum over Repos — the org's whole git footprint.
	TotalBytes int64 `json:"totalBytes"`
	// Repos is every repo the org owns, across every project sub-scope.
	Repos []usageRepo `json:"repos"`
}

// usage returns per-repo and total storage bytes for the caller's org — the
// queryable, per-tenant number commerce and o11y meter on. It spans EVERY
// project sub-scope, unlike the repo list, so a billing consumer sees the whole
// tenant footprint in one call. Sizes are last-measured values (create, push,
// mirror and gc each re-measure), not a live walk of the disk.
//
// Example: {}
//
//	Response: {"org": "acme", "totalBytes": 12288,
//		"repos": [{"name": "widgets", "sizeBytes": 4096},
//			{"name": "site", "project": "web", "sizeBytes": 8192}]}
func (o ops) usage(ctx context.Context, _ *noInput) (*usageView, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	out, err := coreUsage(o.s, ctx, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	return &out, nil
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
func refState(ctx context.Context, s *cloud.Service[state], org, project, name string) (branches []string, head string) {
	repo, err := openRepository(s, Repo{Org: org, Project: project, Name: name})
	if err != nil {
		return nil, ""
	}
	if rev, _, err := repo.Resolve(ctx, ""); err == nil {
		head = rev.String()
	}
	bs, _, err := repo.Refs(ctx)
	if err != nil {
		return branches, head
	}
	for _, b := range bs {
		branches = append(branches, b.Name)
	}
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
	s := mounted.Load()
	if s == nil {
		return nil
	}
	if s.State.ssh != nil {
		s.State.ssh.stop()
	}
	err := s.State.stores.CloseAll()
	if s.State.keys != nil {
		if kerr := s.State.keys.Close(); kerr != nil && err == nil {
			err = kerr
		}
	}
	mounted.Store(nil)
	return err
}
