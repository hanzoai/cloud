package coding

// forge.go is how a run reaches the code, and it is the only way it does.
//
// Every git fact a run needs — where the repository is, the credential to push
// to it, whether the branch landed, where the proposal can be read — is a fact
// about THE FORGE (git.hanzo.ai), so it is asked of the forge directly rather
// than relayed through another of our processes. That relay is what this
// replaces: the same four questions used to cross the internal plane to an app
// that held bare repositories on a disk, and answering them now costs one HTTPS
// call from whichever process the run is in. A repository the forge only
// mirrors is the exception: its facts belong to GitHub (github.go).
//
// It reads its credential the way apps/coding/key.go reads its own: from the
// deployment's own configuration at call time, with no injected Deps, because
// this package is a LIBRARY that two composition roots build a Dispatcher from
// (plugin/agents and plugin/providers) and neither can hand it a service.
// KMS is reached over the plane, which every process can do.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
)

// live tracks the grants this process is holding right now, so a shutdown can
// give them back instead of orphaning them.
//
// The forge expires nothing, so an in-flight run's key outlives the process that
// minted it. A rolling deploy is therefore the ordinary way keys are abandoned —
// not a rare crash — and this is the ordinary way they are not.
var live struct {
	sync.Mutex
	m map[*grant]struct{}
}

// grant is a run's access to its repository's home for the life of the run: a
// deploy key on the forge, or a token on GitHub when the forge holds a mirror
// of a GitHub repository (github.go). Either way Remote, Key and Known are what
// the sandbox is handed.
type grant struct {
	forge.Grant
	hub *hub // nil when the forge is home
}

// String names what the grant reaches and never its secret. A log line takes
// this, never the grant, whose embedded Key would marshal with it.
func (g *grant) String() string {
	if g.hub != nil {
		return g.hub.String()
	}
	return fmt.Sprintf("forge key %d on %s/%s", g.ID, g.Owner, g.Repo)
}

// hold records a grant for the duration of its run.
func hold(g *grant) {
	live.Lock()
	if live.m == nil {
		live.m = map[*grant]struct{}{}
	}
	live.m[g] = struct{}{}
	live.Unlock()
}

// drop forgets a grant that has already been withdrawn.
func drop(g *grant) {
	live.Lock()
	delete(live.m, g)
	live.Unlock()
}

// Drain withdraws every grant this process is still holding.
//
// The composition root calls it on shutdown. It is bounded by its own context
// rather than the caller's, because the caller's is usually already cancelled by
// the time a shutdown reaches here, and a withdrawal that does not happen is a
// live push credential rather than an untidy log line.
//
// Best-effort: SweepOrg is the backstop for whatever this misses (a SIGKILL, a
// forge that will not answer), so this only has to cover the common case.
func Drain(ctx context.Context) int {
	live.Lock()
	held := make([]*grant, 0, len(live.m))
	for g := range live.m {
		held = append(held, g)
	}
	live.m = nil
	live.Unlock()

	done := 0
	for _, g := range held {
		if err := withdraw(ctx, "", g); err == nil {
			done++
		}
	}
	return done
}

// Sweep withdraws run grants abandoned by an earlier process, across the whole
// forge org this deployment works in. A GitHub token needs no sweep: GitHub
// expires it within the hour.
//
// Startup is the right moment because a restart is what produces them: the run
// that was in flight when the old process went away holds a key nobody will ever
// withdraw. Called once, in the background, by the composition root — its
// failure is a stale key, never a process that will not start.
//
// It walks an estate org's namespace. The workspaces the platform made are
// walked by the projects app when it mounts (apps/projects workspace.go
// reclaim), because the record of which namespaces were made is that app's.
func Sweep(ctx context.Context, org string) (int, error) {
	c, err := forgeClient(ctx)
	if err != nil {
		return 0, err
	}
	owner, err := forge.Owner(org)
	if err != nil {
		return 0, err
	}
	return c.Machine().SweepOrg(ctx, owner)
}

// Pinned reports whether the forge host key is configured rather than learned,
// for a composition root to say once at startup.
func Pinned() (bool, string) { return forge.Pinned() }

// client is the process's forge client, UNSCOPED — the caller states who it acts
// as. An unreachable KMS or an empty credential is an error and never a client:
// a run that proceeded without one would fail at the clone, minutes later, with
// a sandbox already leased and a session already open.
func forgeClient(ctx context.Context) (*forge.Client, error) {
	c, err := forge.Dial(ctx, cloud.KMSPeer{})
	if err != nil {
		return nil, fmt.Errorf("coding: the forge is unreachable: %w", err)
	}
	// A key is withdrawn only for a run no pod is executing: this process's, and
	// every other pod's.
	return c.Live(agents.Held), nil
}

func isLocalDev() bool {
	return os.Getenv("CLOUD_LOCAL_DEV") == "1" || os.Getenv("LOCAL_DEV") == "1" || os.Getenv("DEV") == "1"
}

// resolveActor is the forge login this run acts as.
//
// The resolution itself is forge.Client.Caller — subject to confirmed address to
// the login the forge agrees is theirs — and it lives there because the TODO
// asks the same question of the same forge. Two spellings of "who is this
// person here" is one of them drifting, and the one that drifted would be a
// privilege escalation: this path spent four passes closing exactly that.
func resolveActor(ctx context.Context) (string, error) {
	c, err := forgeClient(ctx)
	if err != nil {
		if isLocalDev() {
			return "local", nil
		}
		return "", err
	}
	login, err := c.Caller(ctx)
	if err != nil {
		if isLocalDev() {
			return "local", nil
		}
		return "", fmt.Errorf("coding: %w", err)
	}
	return login, nil
}

// delegate gives ONE run push access to ONE repository, and hands back
// everything the sandbox needs to use it.
//
// TWO INDEPENDENT CONTROLS, and neither is the other's backstop.
//
// THE ORG decides which namespace. It is derived from the run's org — resolved
// by the endpoint from a validated principal, never from a field — as its
// workspace (workspace.go): the estate's CLOSED table, or the namespace the
// platform made for that org and recorded under it. There is no argument a run
// could carry to reach another tenant.
//
// THE ACTOR decides which repository, and it is the control that was missing.
// This used to authorize with the machine client, whose identity is a site
// administrator: forge.Writable then asked "may the site admin push here", which
// is true of every repository on the forge, so any validated member of the org —
// including one with read-only access — could name any repository in the
// namespace and be handed a write key to it. Asking as the HUMAN makes the
// forge's own ACL the answer.
//
// Sudo needs a forge LOGIN, so a run without one is refused rather than falling
// back to the machine. That is the same refusal forge.Client.As makes and for
// the same reason: the fallback IS the escalation.
//
// The repository was RESOLVED before this (repo.go), as the same actor, and is
// only READ here; nothing creates one. It used to be ENSURED — created when
// absent — which turned whatever name a model passed into a new, empty
// repository that the run then cloned, edited and proposed against.
//
// Only the mint itself is the machine's: Forgejo gates deploy keys on
// repo-ADMIN (routers/api/v1/api.go reqAdmin), which an engineer with push
// rights does not have, so authorizing on the human there would mean only
// repository admins could ever run an agent. The human's entitlement is
// established first; the platform then acts on it.
//
// THE HOME IS RESOLVED HERE, ONCE, from the repository the entitlement check
// already read. A mirror of a GitHub repository is a cache of it, so the run is
// handed a token to the origin instead of a key to the copy (github.go); the
// forge's ACL on the copy still decides whether this person may run at all.
//
// A PLAN (read) is handed a credential that can only read: a read-only deploy
// key, or a GitHub token with contents:read. The entitlement is the same — a
// plan is for someone who could build — and only what is minted differs.
//
// IN A WORKSPACE THE PLATFORM MADE there is no forge login to act as, and none
// is looked for: the entitlement is the org's own — the workspace was found by
// the run's validated org, which is the whole of who may reach it — so the
// repository is read and the key minted by the machine.
func delegate(ctx context.Context, ws forge.Workspace, actor, repo, session string, read bool) (*grant, error) {
	c, err := forgeClient(ctx)
	if err != nil {
		if isLocalDev() {
			return &grant{Grant: forge.Grant{
				ID:     1,
				Remote: "local",
				Key:    "",
				Known:  "git.hanzo.ai ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample",
				Owner:  ws.Owner,
				Repo:   repo,
			}}, nil
		}
		return nil, err
	}
	owner := ws.Owner
	if owner == "" {
		return nil, fmt.Errorf("coding: %w", forge.ErrNoOwner)
	}
	if !ws.Made && strings.TrimSpace(actor) == "" {
		// A run with nobody to act as is refused, and the message says which
		// endpoint it came in by, because the two endpoints fail for different
		// reasons. The HTTP endpoint carries an authenticated caller; the Slack
		// endpoint does not — it resolves a linked ACCOUNT SUBJECT from its own
		// table and states only the org on the hop, so there is no forge login to
		// drop privilege to. Minting on the machine's authority instead would hand
		// a write key to a request whose human was never established, which is the
		// hole this closes.
		return nil, fmt.Errorf(
			"coding: this run has no forge identity to act as, so its access to %s cannot be established", repo)
	}
	// Writable, as the actor: the repository must be one they may push, and one
	// that is there. A reader is refused before anything is minted anywhere.
	as := acting(c, ws, actor)
	r, err := as.Writable(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("coding: %s cannot work on %s/%s: %w", party(ws, actor), owner, repo, err)
	}
	if o, n, ok := r.Home(); ok {
		h, err := mint(ctx, o, n, read)
		if err != nil {
			return nil, fmt.Errorf("coding: %s/%s mirrors github.com/%s/%s, and GitHub would not grant this run a push there: %w",
				owner, repo, o, n, err)
		}
		g := &grant{Grant: forge.Grant{Owner: owner, Repo: repo, Remote: h.remote(), Key: h.token}, hub: h}
		hold(g)
		return g, nil
	}
	fg, err := as.Grant(ctx, owner, repo, session, !read)
	if err != nil {
		return nil, fmt.Errorf("coding: the forge would not delegate a push for %s/%s: %w", owner, repo, err)
	}
	g := &grant{Grant: fg}
	hold(g)
	return g, nil
}

// withdraw takes the run's push access back, so a grant's life is the RUN's life.
//
// Best-effort in the sense that a run which already finished must not fail
// because the forge was slow to hear about it — but NOT best-effort in the sense
// of optional: unlike the credential this replaces, nothing expires a forge key,
// so a missed withdrawal leaves a live one. It is called on every exit including
// a panic, and a failure is surfaced to the caller's log rather than dropped.
//
// A GitHub token is revoked the same way and for the same reason, though GitHub
// would expire it within the hour. The run is then over, so the forge's copy is
// told to follow its home now; nothing waits on that (forge.Client.Sync).
func withdraw(ctx context.Context, org string, g *grant) error {
	if g == nil {
		return nil
	}
	if g.hub != nil {
		if err := g.hub.revoke(ctx); err != nil {
			return err
		}
		drop(g)
		// A mirror follows its home now. A run on GitHub alone names no forge copy,
		// so there is nothing to tell.
		if g.Owner != "" && g.Repo != "" {
			if c, err := forgeClient(ctx); err == nil {
				_ = c.Sync(ctx, g.Owner, g.Repo)
			}
		}
		return nil
	}
	if g.ID == 0 {
		return nil
	}
	c, err := forgeClient(ctx)
	if err != nil {
		return err
	}
	if err := c.Machine().Revoke(ctx, g.Owner, g.Repo, g.ID); err != nil {
		return err
	}
	drop(g)
	return nil
}

// remote is the HTTPS address of a repository, for a run executing on a machine
// the customer owns and that authenticates git with its own already-held
// credentials. The sandbox path never uses it — that one clones the SSH remote
// its grant names, which is the only address its key opens.
//
// The ACTOR is what decides whether there is an address at all, for the same
// reason delegate asks as the human: the executing machine's reach is wider
// than the caller's, and an address is the whole of what a routed run needs.
func remote(ctx context.Context, org, actor, repo string) string {
	c, err := forgeClient(ctx)
	if err != nil {
		return ""
	}
	owner, err := forge.Owner(org)
	if err != nil {
		return ""
	}
	if strings.TrimSpace(actor) == "" {
		return ""
	}
	// AS THE ACTOR, not the machine. This address is handed to a machine that
	// holds the org's own credentials, so resolving it as a site administrator
	// would let a caller reach a repository through that machine which they
	// cannot open themselves. A repository the actor cannot see has no address.
	r, err := c.As(actor).Repo(ctx, owner, repo)
	if err != nil || strings.TrimSpace(r.FullName) == "" {
		return ""
	}
	return "https://" + c.Host() + "/" + r.FullName + ".git"
}

// landed reports the commit a branch points at on the forge, in the namespace
// owner, and whether the branch is there at all.
//
// It is the INTEGRITY gate: cloud trusts the ref IT can read, not the runner's
// claim to have pushed one. Unreadable is treated as absent, which costs the run
// its pull request — the honest direction, because a proposal for a branch
// nobody can fetch is worse than no proposal.
func landed(ctx context.Context, owner, repo, branch string) (string, bool) {
	if owner == "" {
		return "", false
	}
	c, err := forgeClient(ctx)
	if err != nil {
		return "", false
	}
	sha, ok, err := c.Machine().Tip(ctx, owner, repo, branch)
	if err != nil {
		return "", false
	}
	return sha, ok
}

// propose offers the run's branch for merging, in the org's workspace ws, and
// answers where a person reads it.
//
// It is made AS THE HUMAN the run acts for, so the forge records who asked for
// the work rather than a shared bot. That became possible once every run had to
// name a forge login it could act as: the same identity whose push right was
// established before a key was ever minted opens the proposal, and a machine
// that proposed on their behalf would be a second, wider authority for an act
// they can already perform.
//
// In a workspace the platform made nobody has a forge login, so the machine
// opens it, and the proposal's body names the person (prBody). There every run
// proposes into the repository's default branch, whatever branch it started
// from: nobody there holds a forge account to land a stack of proposals one onto
// another, so a follow-up's proposal carries its predecessors' commits and one
// merge lands the work (Merge).
func propose(ctx context.Context, ws forge.Workspace, actor, repo, base, head, title, body string) (string, error) {
	if ws.Made {
		base = ""
	}
	c, err := forgeClient(ctx)
	if err != nil {
		return "", err
	}
	if ws.Owner == "" {
		return "", fmt.Errorf("coding: %w", forge.ErrNoOwner)
	}
	if !ws.Made && strings.TrimSpace(actor) == "" {
		return "", fmt.Errorf("coding: a proposal needs the person it is for")
	}
	return acting(c, ws, actor).Propose(ctx, ws.Owner, repo, base, head, title, body)
}
