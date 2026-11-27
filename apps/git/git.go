package git

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/text"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// nameRE constrains a repo name to a safe identifier. The name is the
// org-unique handle AND the URL path segment AND the storage path segment,
// so this is the injection/traversal guard at the boundary. A trailing ".git"
// is stripped before matching (clients clone "<name>.git").
var nameRE = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9._-]{0,63}|\.[A-Za-z0-9][A-Za-z0-9._-]{0,62})$`)

// defaultBranchName is the default branch a fresh bare repo points HEAD at.
const defaultBranchName = "main"

// state is git's own data; shared deps live in the embedded cloud.Base (the
// clone-URL host is s.Domain).
type state struct {
	stores *cloud.OrgStore[*Store] // per-org repo-metadata DBs, opened once each
	// objects holds the history of every repository whose history is here: the objects,
	// refs and config the metadata row names (vault.go). storage below is what is left
	// of the directories those lived in, and the caches of linked repositories.
	objects *cloud.OrgStore[*objects.Store]
	vault   *vault
	storage *storage
	logs    objects.Blobs // where task logs live (logs.go); nil when the deployment has no object store
	dataDir string        // base data dir; {dataDir}/orgs/{slug} is enumerated for the public explore
	gitHost string        // the HTTPS git host (e.g. "git.hanzo.ai") named by the clone URLs a page prints
	cache   *cache        // the bound on materialized bare repos (reclaim.go)
	links   *answers      // what linked repositories' hosts said, briefly (link.go)
	// activity is the estate's year of work, as the index last wrote it
	// (activity.go).
	activity *activityCache
	// metrics is the estate's open source figures and package registry
	// (metrics.go), opened on first use like keys.
	metrics *metricsHolder
	// life ends at Shutdown. Work a request starts and does not wait for — a
	// linked repository's arrival (link.go) — runs under it. Shutdown waits for
	// arrivals, post-push maintenance and event delivery before closing stores.
	life context.Context
	// keys is the SSH key registry (sshkeys.go), opened on first use; ssh is the
	// listener when GIT_SSH_ADDR is set (ssh.go).
	keys     *keyHolder
	ssh      *sshServer
	end      context.CancelFunc
	arriving *sync.WaitGroup
}

// storeFor resolves the caller's org-scoped repo-metadata store, opening the
// per-org file ({DataDir}/orgs/{orgSlug}/git.db) once via the shared cache. git
// is org-scoped (not project-scoped) so /v1/git/usage stays a single org-wide
// rollup across every project sub-scope.
func storeFor(s *cloud.Service[state], org string) (*Store, error) {
	if !orgRE.MatchString(org) {
		return nil, fmt.Errorf("git: org %q is not an org name", org)
	}
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// stored reports whether an org a request PATH names already has a store, without
// opening or creating one. A read addressed as /v1/git/{org}/… by a caller who is
// not that org asks it first: storeFor creates a database on first touch, so an
// anonymous GET for a name that names nothing would otherwise leave an empty
// git.db behind for every name a crawler or probe invents.
func stored(s *cloud.Service[state], org string) bool {
	ns, err := principal.OrgNamespace(org, "")
	return err == nil && s.State.stores.Has(ns)
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
	// Link is where the repo's history lives when it is on another host: reads
	// are served from a cache filled from there and pushes go there. Absent for
	// a repo whose history is here.
	Link *link `json:"link,omitempty"`
	// CreatedAt is RFC 3339 UTC.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is RFC 3339 UTC, empty until the first write.
	UpdatedAt string `json:"updatedAt,omitempty"`
	// Upstream is the GitHub repository this one follows, as
	// github.com/<owner>/<repo>: what it is linked to, or what the forge mirrors
	// it from. Absent for a repository with no GitHub source.
	Upstream string `json:"upstream,omitempty"`
	// Packages are the registry packages published from Upstream, as the
	// estate's collector registered them (POST /v1/admin/git/metrics).
	Packages []packageRefView `json:"packages,omitempty"`
}

// repoList is the collection envelope every git list op answers with.
type repoList struct {
	// Data holds the repos in scope, most recently updated first.
	Data []repoView `json:"data"`
}

// cloneURL is the HTTPS remote for a repo. An org-level repo keeps the
// two-segment path it has always had; a project-scoped one names its project as
// the middle segment, because `git clone` sends no headers and the URL is the
// only place the scope can travel.
func cloneURL(s *cloud.Service[state], org, project, name string) string {
	if project == "" {
		return fmt.Sprintf("%s/v1/git/%s/%s.git", origin(s), org, name)
	}
	return fmt.Sprintf("%s/v1/git/%s/%s/%s.git", origin(s), org, project, name)
}

// origin is this host as a caller reaches it, scheme and all. One derivation,
// used by every address this plugin hands out — a clone URL, and the server and
// api addresses a running job is told (runner.go).
func origin(s *cloud.Service[state]) string {
	host := s.Domain
	if host == "" { // only a hand-built Deps; Config.Validate requires a domain
		host = brand.APIHost(brand.Default)
	}
	return "https://" + host
}

// sshURL is the scp-style SSH remote of the host a linked repository's history
// is on — git@github.com:<owner>/<name>.git, git@git.hanzo.ai:<owner>/<name>.git
// — and empty for one that is not linked: this app serves no SSH (the forge
// does, for every repository whose home it is).
func sshURL(r Repo) string {
	k, ok := linkOf(r)
	if !ok || k.Provider == "git" {
		return ""
	}
	return fmt.Sprintf("git@%s:%s/%s.git", strings.Split(host(k.CloneURL), ":")[0], k.Owner, k.Name)
}

// forgeHost is the deployment's own forge, the "git" sibling of its registrable
// apex (api.hanzo.ai → git.hanzo.ai), else the Hanzo brand's.
func forgeHost(domain string) string {
	if h := brand.Sibling(domain, "git"); h != "" {
		return h
	}
	return brand.Sibling(brand.APIHost(brand.Default), "git")
}

func toView(s *cloud.Service[state], r Repo, branches []string, head string) repoView {
	v := repoView{
		ID: r.ID, Org: r.Org, Project: r.Project, Name: r.Name, Description: r.Description,
		DefaultBranch: r.DefaultBranch, Public: r.Public, Branches: branches, Head: head,
		CloneURL:  cloneURL(s, r.Org, r.Project, r.Name),
		SSHURL:    sshURL(r),
		SizeBytes: r.SizeBytes, CreatedAt: text.Unix(r.CreatedAt), UpdatedAt: text.Unix(r.UpdatedAt),
	}
	if k, ok := linkOf(r); ok {
		v.Link = k
	}
	if up, ok := normUpstream(r.Origin); ok {
		v.Upstream = up
	}
	return v
}

// Mount wires the git surface onto app per HIP-0106.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("git: nil app")
	}
	// git registers TYPED ops, which live on the *zip.App's registry (ops.go) —
	// they are DECLARED on the /v1/git group (routes below), which resolves to
	// that same registry. Checked before anything is built so a Router that
	// cannot carry them fails the mount rather than serving a surface no
	// projection knows about.
	if cloud.DataDir() == "" {
		return fmt.Errorf("git.Use:  empty DataDir")
	}
	gitRoot := filepath.Join(cloud.DataDir(), "git")
	st, err := newStorage(gitRoot)
	if err != nil {
		return fmt.Errorf("git.Use:  open storage: %w", err)
	}
	b := cloud.NewBase(deps, "git")
	life, end := context.WithCancel(context.Background())
	s := &cloud.Service[state]{Base: b, State: state{
		stores:   cloud.NewOrgStore(b, "git", openStore),
		objects:  cloud.NewOrgStoreAt(b, "objects", objects.Opener(packBlobs())),
		vault:    newVault(),
		storage:  st,
		dataDir:  cloud.DataDir(),
		logs:     packBlobs(),
		gitHost:  forgeHost(cloud.Domain()),
		cache:    newCache(),
		links:    newAnswers(),
		activity: newActivityCache(readActivity),
		life:     life,
		keys:     &keyHolder{},
		metrics:  &metricsHolder{},
		end:      end,
		arriving: &sync.WaitGroup{},
	}}
	mounted.Store(s)

	routes(app, s)
	// Deliver what pushes have already recorded. It sweeps every org on this
	// host, because after a restart the process knows of no push and the journal
	// rows are the only record that a run is owed (journal.go).
	dispatch(s)
	// Bring in the repositories this host still keeps as directories (vault.go).
	s.State.vault.sweeping.Go(func() { s.State.vault.run(s) })
	s.State.vault.sweeping.Go(func() { s.State.vault.maintain(s) })
	// Carry the task logs this host still keeps as files into the object store, once.
	if s.State.logs != nil {
		s.State.vault.sweeping.Go(func() {
			if n, err := carryLogs(life, s.State.logs, s.State.dataDir); err != nil {
				slog.Warn("git: carrying the run logs to the object store", "carried", n, "err", err)
			} else if n > 0 {
				slog.Info("git: run logs carried to the object store", "carried", n)
			}
		})
	}
	registerLifecycleReactors()
	// Install the outbound-mirror controller so the universal sync engine's git
	// provider can ensure/remove a repo's mirror target through the SAME store the
	// mirror_out reactor pushes from — no sync⇆git cycle (mirror_control.go).
	cloud.RegisterGitMirrorController(gitMirrorController{})
	// Install the visibility subscriber so a project published on hanzo.app gets
	// its canonical repo, world-readable exactly when the project is
	// (community.go). Next to fall to the internal plane; until then the client
	// stays registered — unregistered it is a silent no-op and public projects
	// stop getting repos.
	// Visibility on the internal plane (community.go), NOT a Register* client: the
	// caller is projects, in its own process.
	exposePublish()
	// Publish the delivery inventory read on the internal plane, so apps/deploy
	// renders from a tree read instead of cloning (files.go).
	exposeFiles()
	// What a linked repository's host says landed on it (link.go), from the
	// provider app's GitHub webhook.
	exposeLanded()
	// The sync engine decides a mirror should exist; this app owns the repos and
	// the reactor that pushes them. Registered above for the co-resident case, and
	// published here for the split one (mirror_control.go).
	exposeMirror()
	// What the org keeps in git, as headline numbers — the read a caller makes
	// when it does not yet know a repo's name (figures_rpc.go).
	exposeFigures()
	// The estate's open source figures, read daily by the platform's cron
	// (collect.go).
	exposeCollect()

	if addr := env.Or("GIT_SSH_ADDR", ""); addr != "" {
		host, err := hostSigner()
		if err != nil {
			return fmt.Errorf("git.Use: ssh: %w", err)
		}
		if s.State.ssh, err = startSSH(s, addr, host); err != nil {
			return fmt.Errorf("git.Use: %w", err)
		}
		b.Log.Info("git ssh listening", "addr", s.State.ssh.addr())
	}

	b.Log.Info("git mounted", "brand", cloud.Brand(), "storage", "osfs", "root", gitRoot, "zap", "/zap")
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
	// Both bridges first: every typed op below reads its tenant off the request
	// context, and a Use only runs ahead of routes registered after it.
	//
	// cloud.Bridge is the FLEET's one client for the facts a typed signature drops
	// — the validated org, validated-ness itself, the brand, the project — so a
	// change to it reaches git like every other app. bridgePrincipal is git's own,
	// and it is not a second copy of that: it carries the two facts the shared one
	// does not, the FOLDED project scope (principal.ProjectFrom answers the header
	// verbatim; projectScope degrades default, over-long and malformed to "",
	// which is the physical key every repo row is stored under) and the acting
	// USER an SSH key is owned by (keys.go). Both ride ONE tenant value with the
	// org, so a handler cannot hold one and forget the others (ops.go).
	g.Use(cloud.Bridge(), zip.H(bridgePrincipal))

	// Control plane (JSON). Static /repos + /usage register before the
	// smart-HTTP :org/:repo params so a real org can never shadow them.
	g.Post("/repos", o.createRepo, zip.WithStatus(http.StatusCreated))
	g.Get("/repos", o.listRepos)
	g.Get("/usage", o.usage)
	// The estate's year of work across every org the forge mirrors (activity.go).
	// Public: it reads a document the index wrote and names no private repository.
	g.Get("/activity", o.activity)
	// The estate's open source figures (metrics.go), in the operator's family,
	// /v1/admin/git. SuperAdmin's: the gated group refuses first on REST, and each
	// op checks again for the ways in that pass no route (MCP, the call plane).
	a := app.Group("/v1/admin/git", cloud.Gate(cloud.Super))
	a.Use(cloud.Bridge())
	a.Post("/metrics", o.putMetrics)
	a.Get("/metrics", o.getMetrics)
	// The org's code workspace on the forge, set up for the caller (workspace.go).
	g.Post("/workspace", o.openWorkspace)
	g.Get("/repos/:name", o.getRepo)
	g.Patch("/repos/:name", o.setVisibility)
	g.Delete("/repos/:name", o.deleteRepo)
	// Push generated files without a local git client (hanzo.app builder).
	// A distinct trailing segment, so it never shadows the :org/:repo routes.
	g.Post("/repos/:name/push", o.pushFiles)
	// The retired forge push endpoint (webhook.go): a TOMBSTONE answering every
	// delivery 410 and naming platform.hanzo.ai, kept because a 404 from this
	// estate reads as "the API is switched off". A static segment that never
	// shadows the :org/:repo smart-HTTP routes; cloud.Terminal writes the 410
	// in-band so the commerce /v1 ErrorHandlerJSON (co-mounted ahead) cannot
	// flatten it to 500.
	//
	// Untyped, and the reason is ORDER rather than shape: ANY bytes answer 410
	// here, while op.invoke json-decodes a non-empty body before the handler is
	// entered (zip typed.go:485-490) and answers an undecodable one 400. The whole
	// mechanism and its measurement are in webhook.go.
	g.Raw(http.MethodPost, "/webhook", cloud.Terminal(cloud.Handle(s, webhook)))
	// Link <org>/:name to its history on another host (creates the repo on first
	// use), and link every repository of the org to its namesake on GitHub
	// (link.go). Distinct trailing segments, so they never shadow the :org/:repo
	// smart-HTTP routes below.
	g.Post("/repos/:name/link", o.linkRepo)
	g.Post("/link", o.linkRepos)
	// Move a repository whose history is here to the forge, its one home (link.go).
	g.Post("/repos/:name/home", o.homeRepo)
	// Repack a repo with a reachability bitmap + commit-graph so its next clone
	// serves fast (bitmap reuse, no full object-graph walk). Distinct trailing
	// segment, like /link — never shadows the :org/:repo smart-HTTP routes.
	g.Post("/repos/:name/gc", o.gc)

	// Repo-lifecycle config: Slack-channel subscriptions (notify.go) + downstream
	// mirror targets (mirror_out.go). Distinct trailing segments, so they never
	// shadow the :org/:repo smart-HTTP routes below. Org-scoped like every repo op.
	g.Post("/repos/:name/subscriptions", o.subscribe, zip.WithStatus(http.StatusCreated))
	g.Get("/repos/:name/subscriptions", o.listSubscriptions)
	g.Delete("/repos/:name/subscriptions/:id", o.unsubscribe)
	// The repo's OUTBOUND mirror targets. Named for the noun rather than the verb
	// because these push the repo OUT to downstream remotes, and a link (above)
	// is where a repo's history comes FROM. MirrorTarget is what the rows have
	// always been called.
	g.Post("/repos/:name/targets", o.addTarget, zip.WithStatus(http.StatusCreated))
	g.Get("/repos/:name/targets", o.listTargets)
	g.Delete("/repos/:name/targets/:id", o.deleteTarget)

	// Pull requests (pulls.go): propose a branch, read what is waiting, merge it.
	// The noun that closes the agent loop — a run pushes refs/heads/agent/<run>
	// and this is where it says what the branch is for and where a person says
	// yes. Distinct trailing segments, so they never shadow the :org/:repo
	// smart-HTTP routes below. Org-scoped like every repo op.
	g.Post("/repos/:name/pulls", o.openPull, zip.WithStatus(http.StatusCreated))
	g.Get("/repos/:name/pulls", o.listPulls)
	// Commit statuses (statuses.go): what CI and outside checks say about a commit.
	g.Post("/repos/:name/commits/:sha/statuses", o.postStatus, zip.WithStatus(http.StatusCreated))
	g.Get("/repos/:name/commits/:sha/statuses", o.listStatuses)
	g.Get("/repos/:name/pulls/:number", o.getPull)
	g.Post("/repos/:name/pulls/:number/merge", o.mergePull)

	// Read/browse surface (JSON) for the console repo-browser: refs, tree, blob,
	// commits, readme. ref + path ride as ?ref=&path= query params (the UI's own
	// convention), so a slashed branch is unambiguous. Distinct trailing segments —
	// they never shadow the :org/:repo smart-HTTP routes below. Org-scoped like every
	// repo op; the JSON twin of the HTML browser in ui.go (one set of read helpers).
	g.Get("/repos/:name/refs", o.browseRefs)
	g.Get("/repos/:name/tree", o.browseTree)
	g.Get("/repos/:name/files", o.browseFiles)
	g.Get("/repos/:name/blob", o.browseBlob)
	g.Get("/repos/:name/commits", o.browseCommits)
	g.Get("/repos/:name/readme", o.browseReadme)

	// Workflow orchestration (run.go). What a repository runs, which capacity is
	// declared to run it, what has run, and how to run one now. These are Git
	// questions asked by a person or an agent, so they answer at the Git address
	// behind the Git principal. The five machine operations a runner daemon calls
	// are a different surface with a different credential (runner.go).
	//
	// Static leaves, registered ahead of the :org/:repo pack routes below so a
	// real org can never shadow them — the same rule /repos and /usage follow.
	// SSH keys (sshkeys.go): a person's key, or — with `repo` — a repository's deploy key.
	g.Post("/keys", o.addKey, zip.WithStatus(http.StatusCreated))
	g.Get("/keys", o.listKeys)
	g.Delete("/keys/:id", o.deleteKey)

	g.Post("/pools", o.declarePool, zip.WithStatus(http.StatusCreated))
	g.Get("/pools", o.listPools)
	g.Get("/runners", o.listRunners)
	g.Get("/workflows", o.listWorkflows)
	g.Post("/runs", o.startRun, zip.WithStatus(http.StatusCreated))
	g.Get("/runs", o.listRuns)
	g.Get("/runs/:id", o.getRun)
	g.Get("/runs/:id/logs", o.readRunLogs)

	// Smart-HTTP git protocol. These live under /v1/git/:org/:repo/* so
	// `git clone https://<host>/v1/git/<org>/<repo>.git` works natively. They
	// stay raw because they speak git's own pack protocol — pkt-line
	// advertisements in, STREAMED packfiles out, JSON in neither direction.
	g.Raw(http.MethodGet, "/:org/:repo/info/refs", cloud.Handle(s, infoRefs))
	g.Raw(http.MethodPost, "/:org/:repo/git-upload-pack", cloud.Handle(s, uploadPack))
	g.Raw(http.MethodPost, "/:org/:repo/git-receive-pack", cloud.Handle(s, receivePack))

	// The same protocol one segment deeper, for a project-scoped repo. The scope
	// otherwise rides X-Project-Id, which `git clone` cannot send, so without a
	// path form a project-scoped repo has no usable remote. Distinct segment
	// count from the routes above, so the org-level form is untouched.
	g.Raw(http.MethodGet, "/:org/:project/:repo/info/refs", cloud.Handle(s, infoRefs))
	g.Raw(http.MethodPost, "/:org/:project/:repo/git-upload-pack", cloud.Handle(s, uploadPack))
	g.Raw(http.MethodPost, "/:org/:project/:repo/git-receive-pack", cloud.Handle(s, receivePack))

	// The clone address is /v1/git/<org>/<repo> and only that. There was a
	// second, root-level spelling of the six routes above — `git clone
	// https://git.hanzo.ai/<org>/<repo>.git` — matched only when the request Host
	// was s.State.gitHost, falling through on every other host. That Host is the
	// standalone forge's in production, so those routes answered nobody there;
	// and a manifest prefix must begin with a literal segment, so /:org could not
	// be declared and under the plugin host they were unreachable even in dev.
	// What is gone with them is running cloud ITSELF as a bare-URL git host,
	// which is the forge's job.

	// The product's own address answers the repositories on the forge, the one
	// home of a repository (home.go). Absolute rather than on the group: a group
	// composes "" to "/v1/git/", a different route. The forge's web UI is the
	// browser of what it holds, so this app renders no pages.
	app.Get("/v1/git", o.listHome)

	// The runner daemon's five operations, at their own root. Registered on the
	// app and not on g, because /v1/runner is not under /v1/git and must not meet
	// the principal middleware installed there: a runner presents its own
	// credential and belongs to no session (runner.go).
	if RunnerMount != nil {
		if err := RunnerMount(app, RunnerHost{s: s}); err != nil {
			panic(err)
		}
	}
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
		cloud.RegisterLifecycleSubscriber(func(ctx context.Context, ev cloud.LifecycleEvent) {
			if s := mounted.Load(); s != nil {
				skillsOnPush(s, ctx, ev)
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
	if v, ok, err := madeCreate(o.s, ctx, t, *in); ok || err != nil {
		return v, err
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

// provision materializes a repo: its metadata row plus an empty repository, in the
// objects store for one whose history is here and as a bare directory for the cache of
// a linked one (vault.go). The ONE way a repo comes into being — create and mirror both compose
// it. On a storage-init failure the metadata row is rolled back so a partial
// provision never leaves a phantom repo. Returns errConflict when the row
// already exists (the caller decides whether that is fatal).
func provision(s *cloud.Service[state], ctx context.Context, store *Store, r Repo) error {
	if err := store.Create(ctx, r); err != nil {
		return err // errConflict, or a wrapped insert error
	}
	if err := s.State.vault.init(ctx, s, r); err != nil {
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
func (o ops) listRepos(ctx context.Context, _ *cloud.Unit) (*repoList, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	out, err := coreList(o.s, ctx, t.org, t.project)
	if err != nil {
		return nil, internalErr(err)
	}
	more, err := madeList(ctx, t)
	if err != nil {
		return nil, err
	}
	return &repoList{Data: withPackages(ctx, o.s, merged(out, more))}, nil
}

// getRepo returns one repo with its live ref state: every branch name and the
// resolved HEAD commit. Both are read from the object store on each call, so an
// empty repo reports no branches and an empty head rather than failing. A repo
// outside the caller's scope is not found.
//
// A name with an owner, hanzo-inc/cloud, is the repository on the forge
// (git.hanzo.ai), its one home, read there as the caller in a forge namespace
// their org owns; the tree and blob reads take the same coordinate.
//
// Example: {"name": "hanzo-inc/cloud"}
func (o ops) getRepo(ctx context.Context, in *repoRef) (*repoView, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	view, err := coreGet(o.s, ctx, t.org, t.project, in.Name)
	if errors.Is(err, errNotFound) {
		if v, ok, err := madeGet(o.s, ctx, t, in.Name); ok || err != nil {
			if v != nil {
				v = &withPackages(ctx, o.s, []repoView{*v})[0]
			}
			return v, err
		}
		return nil, zip.ErrNotFound("repo not found")
	}
	if err != nil {
		return nil, internalErr(err)
	}
	return &withPackages(ctx, o.s, []repoView{view})[0], nil
}

// deleteRepo removes a repo's metadata and purges its storage. Answers 204 with
// no body. The metadata row is the source of truth for existence, so a storage
// purge that fails is logged and the delete still succeeds — and a second call
// is a 404, not a second delete. A repository this app does not hold is looked
// for where getRepo looks next, the org's made workspace on the forge, so a
// customer's own repository (or the copy a GitHub link made there) is one a
// delete can reach, as their list already shows it.
//
// Example: {"name": "widgets"}
func (o ops) deleteRepo(ctx context.Context, in *repoRef) (*cloud.Unit, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	err = coreDelete(o.s, ctx, t.org, t.project, in.Name)
	if errors.Is(err, errNotFound) {
		if ok, err := madeDelete(o.s, ctx, t, in.Name); ok || err != nil {
			return nil, err
		}
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
func (o ops) usage(ctx context.Context, _ *cloud.Unit) (*usageView, error) {
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
	size, err := s.State.vault.size(ctx, s, org, project, name)
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
// Best-effort: a read error — a linked repository its host does not let the
// caller read among them — yields empty state rather than failing the request.
func refState(ctx context.Context, s *cloud.Service[state], r Repo) (branches []string, head string) {
	repo, done, err := open(ctx, s, r)
	if err != nil {
		return nil, ""
	}
	defer done()
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
func projectScope(c *zip.Ctx) string { return foldProject(principal.Project(c)) }

// foldProject is that fold itself, so the typed-op path (tenantOfCaller, which
// holds a context and no request) keys repos exactly as the request path does.
func foldProject(p string) string {
	if principal.IsDefaultProject(p) || len(p) > 128 || !projectRE.MatchString(p) {
		return ""
	}
	return p
}

// Shutdown stops the SSH listener and closes every open store (per-org repo
// metadata + the SSH key registry). Idempotent.
func Shutdown() error {
	s := mounted.Load()
	if s == nil {
		return nil
	}
	s.State.end()
	if s.State.ssh != nil {
		s.State.ssh.stop()
	}
	s.State.arriving.Wait()
	s.State.vault.sweeping.Wait()
	err := errors.Join(s.State.stores.CloseAll(), s.State.objects.CloseAll(), s.State.keys.close(), s.State.metrics.close())
	mounted.Store(nil)
	return err
}
