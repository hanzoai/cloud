package coding

// forge.go is how a run reaches the code, and it is the only way it does.
//
// Every git fact a run needs — where the repository is, the credential to push
// to it, whether the branch landed, where the proposal can be read — is a fact
// about THE FORGE (git.hanzo.ai), so it is asked of the forge directly rather
// than relayed through another of our processes. That relay is what this
// replaces: the same four questions used to cross the internal plane to an app
// that held bare repositories on a disk, and answering them now costs one HTTPS
// call from whichever process the run is in.
//
// It reads its credential the way apps/coding/key.go reads its own: from the
// deployment's own configuration at call time, with no injected Deps, because
// this package is a LIBRARY that two composition roots build a Dispatcher from
// (plugin/agents and plugin/integrations) and neither can hand it a service.
// KMS is reached over the plane, which every process can do.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/brand"
	"github.com/hanzoai/cloud/forge"
)

// credential holds the deployment's forge credential for this process, re-reading it
// from KMS on its own schedule so a rotation is live without a restart.
var credential forge.Source

// live tracks the grants this process is holding right now, so a shutdown can
// give them back instead of orphaning them.
//
// The forge expires nothing, so an in-flight run's key outlives the process that
// minted it. A rolling deploy is therefore the ordinary way keys are abandoned —
// not a rare crash — and this is the ordinary way they are not.
var live struct {
	sync.Mutex
	m map[int64]forge.Grant
}

// hold records a grant for the duration of its run.
func hold(g forge.Grant) {
	live.Lock()
	if live.m == nil {
		live.m = map[int64]forge.Grant{}
	}
	live.m[g.ID] = g
	live.Unlock()
}

// drop forgets a grant that has already been withdrawn.
func drop(id int64) {
	live.Lock()
	delete(live.m, id)
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
	held := make([]forge.Grant, 0, len(live.m))
	for _, g := range live.m {
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
// forge org this deployment works in.
//
// Startup is the right moment because a restart is what produces them: the run
// that was in flight when the old process went away holds a key nobody will ever
// withdraw. Called once, in the background, by the composition root — its
// failure is a stale key, never a process that will not start.
func Sweep(ctx context.Context, org string) (int, error) {
	c, err := client(ctx)
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
func Pinned() (bool, string) { return credential.Pinned() }

// client is the process's forge client, UNSCOPED — the caller states who it acts
// as. An unreachable KMS or an empty credential is an error and never a client:
// a run that proceeded without one would fail at the clone, minutes later, with
// a sandbox already leased and a session already open.
func client(ctx context.Context) (*forge.Client, error) {
	c, err := credential.Client(ctx, cloud.KMSPeer{}, domain())
	if err != nil {
		return nil, fmt.Errorf("coding: the forge is unreachable: %w", err)
	}
	return c, nil
}

// domain is this deployment's own public host, from which the forge host is
// derived. Same resolution apps/sites uses, so a white-labelled deployment reads
// its OWN forge rather than another brand's.
func domain() string {
	if d := strings.TrimSpace(os.Getenv("CLOUD_DOMAIN")); d != "" {
		return d
	}
	b := strings.TrimSpace(os.Getenv("CLOUD_BRAND"))
	if b == "" {
		b = brand.Default
	}
	return brand.APIHost(b)
}

// resolveActor is the forge login this run acts as.
//
// The resolution itself is forge.Client.Caller — subject to confirmed address to
// the login the forge agrees is theirs — and it lives there because the TRACKER
// asks the same question of the same forge. Two spellings of "who is this
// person here" is one of them drifting, and the one that drifted would be a
// privilege escalation: this path spent four passes closing exactly that.
func resolveActor(ctx context.Context) (string, error) {
	c, err := client(ctx)
	if err != nil {
		return "", err
	}
	login, err := c.Caller(ctx)
	if err != nil {
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
// by the door from a validated principal, never from a field — through a CLOSED
// table (forge.Owner), so an org with no forge namespace is refused rather than
// becoming one. There is no argument a run could carry to reach another tenant.
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
// The repository is ENSURED rather than merely looked up, because the two hosts
// were migrated one repository at a time and a run pointed at one that has not
// arrived yet would otherwise discover it as a 404 from `git clone`, after a
// sandbox was leased and a session opened. It is ensured AS THE ACTOR too, so a
// repository is only ever created by someone entitled to create it.
//
// Only the mint itself is the machine's: Forgejo gates deploy keys on
// repo-ADMIN (routers/api/v1/api.go reqAdmin), which an engineer with push
// rights does not have, so authorizing on the human there would mean only
// repository admins could ever run an agent. The human's entitlement is
// established first; the platform then acts on it.
func delegate(ctx context.Context, org, actor, repo, session string) (forge.Grant, error) {
	c, err := client(ctx)
	if err != nil {
		return forge.Grant{}, err
	}
	owner, err := forge.Owner(org)
	if err != nil {
		return forge.Grant{}, fmt.Errorf("coding: %w", err)
	}
	if strings.TrimSpace(actor) == "" {
		// A run with nobody to act as is refused, and the message says which door
		// it came in by, because the two doors fail for different reasons. The
		// HTTP door carries an authenticated caller; the Slack door does not —
		// it resolves a linked ACCOUNT SUBJECT from its own table and states only
		// the org on the hop, so there is no forge login to drop privilege to.
		// Minting on the machine's authority instead would hand a write key to a
		// request whose human was never established, which is the hole this
		// closes.
		return forge.Grant{}, fmt.Errorf(
			"coding: this run has no forge identity to act as, so its access to %s cannot be established", repo)
	}
	as := c.As(actor)
	if _, err := as.Ensure(ctx, owner, repo, "created for a Hanzo agent run"); err != nil {
		// AN UNKNOWN ACTOR IS A MISSING FORGE ACCOUNT, and it says so.
		//
		// Sudo resolves a LOGIN against a user row and does not create one
		// (routers/api/v1/api.go sudo → GetUserByName, 404 when absent), and the
		// forge only provisions a user at an interactive SSO sign-in. So a person
		// whose first touch of the forge is a coding run has no row to act as, and
		// the generic refusal reads as "you cannot work on this repository" when
		// the truth is "you have never signed in".
		//
		// Cloud does NOT create the row. The forge links an SSO login to an
		// existing name only under ACCOUNT_LINKING=auto, and that setting defaults
		// to `login` — a password prompt, which an IAM-native forge has no answer
		// for — so pre-creating users would lock out exactly the people it was
		// meant to help. One sign-in is the fix, and it is theirs to make.
		if errors.Is(err, forge.ErrUnknownActor) {
			return forge.Grant{}, fmt.Errorf(
				"coding: %s has no account on the forge yet — sign in to %s once, then start the run",
				actor, c.Host())
		}
		return forge.Grant{}, fmt.Errorf("coding: %s cannot work on %s/%s: %w", actor, owner, repo, err)
	}
	g, err := as.Grant(ctx, owner, repo, session)
	if err != nil {
		return forge.Grant{}, fmt.Errorf("coding: the forge would not delegate a push for %s: %w", repo, err)
	}
	hold(g)
	return g, nil
}

// withdraw takes the run's push access back, so a grant's life is the RUN's life.
//
// Best-effort in the sense that a run which already finished must not fail
// because the forge was slow to hear about it — but NOT best-effort in the sense
// of optional: unlike the credential this replaces, nothing expires it, so a
// missed withdrawal leaves a live key. It is called on every exit including a
// panic, and a failure is surfaced to the caller's log rather than dropped.
func withdraw(ctx context.Context, org string, g forge.Grant) error {
	if g.ID == 0 {
		return nil
	}
	c, err := client(ctx)
	if err != nil {
		return err
	}
	if err := c.Machine().Revoke(ctx, g.Owner, g.Repo, g.ID); err != nil {
		return err
	}
	drop(g.ID)
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
	c, err := client(ctx)
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

// landed reports the commit a branch points at on the forge, and whether the
// branch is there at all.
//
// It is the INTEGRITY gate: cloud trusts the ref IT can read, not the runner's
// claim to have pushed one. Unreadable is treated as absent, which costs the run
// its pull request — the honest direction, because a proposal for a branch
// nobody can fetch is worse than no proposal.
func landed(ctx context.Context, org, repo, branch string) (string, bool) {
	c, err := client(ctx)
	if err != nil {
		return "", false
	}
	owner, err := forge.Owner(org)
	if err != nil {
		return "", false
	}
	sha, ok, err := c.Machine().Tip(ctx, owner, repo, branch)
	if err != nil {
		return "", false
	}
	return sha, ok
}

// propose offers the run's branch for merging and answers where a person reads
// it.
//
// It is made AS THE HUMAN the run acts for, so the forge records who asked for
// the work rather than a shared bot. That became possible once every run had to
// name a forge login it could act as: the same identity whose push right was
// established before a key was ever minted opens the proposal, and a machine
// that proposed on their behalf would be a second, wider authority for an act
// they can already perform.
func propose(ctx context.Context, org, actor, repo, base, head, title, body string) (string, error) {
	c, err := client(ctx)
	if err != nil {
		return "", err
	}
	owner, err := forge.Owner(org)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(actor) == "" {
		return "", fmt.Errorf("coding: a proposal needs the person it is for")
	}
	return c.As(actor).Propose(ctx, owner, repo, base, head, title, body)
}
