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
	"fmt"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/brand"
	"github.com/hanzoai/cloud/forge"
)

// credential holds the deployment's forge credential for this process, re-reading it
// from KMS on its own schedule so a rotation is live without a restart.
var credential forge.Source

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
		return forge.Grant{}, fmt.Errorf("coding: %s cannot work on %s/%s: %w", actor, owner, repo, err)
	}
	g, err := as.Grant(ctx, owner, repo, session)
	if err != nil {
		return forge.Grant{}, fmt.Errorf("coding: the forge would not delegate a push for %s: %w", repo, err)
	}
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
	return c.Machine().Revoke(ctx, g.Owner, g.Repo, g.ID)
}

// remote is the HTTPS address of a repository, for a run executing on a machine
// the customer owns and that authenticates git with its own already-held
// credentials. The sandbox path never uses it — that one clones the SSH remote
// its grant names, which is the only address its key opens.
func remote(ctx context.Context, org, repo string) string {
	c, err := client(ctx)
	if err != nil {
		return ""
	}
	owner, err := forge.Owner(org)
	if err != nil {
		return ""
	}
	r, err := c.Machine().Repo(ctx, owner, repo)
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
// It is made as the MACHINE, and the reason is a fact about the data rather than
// a preference: a run's subject is an OIDC SUBJECT (integrations.LinkedSubject
// returns the account link's subject, an opaque id), and Sudo takes a forge
// LOGIN. There is no person here to act as, and inventing one by guessing at a
// username would attribute the work to whoever happens to hold that login on the
// forge.
//
// Nothing is lost against what this replaces — that opened the pull request with
// a GitHub App installation token, which is also a bot — and nothing is hidden:
// the human is on the tracker work item this is filed beside, and the run that
// produced the branch is named in the body.
func propose(ctx context.Context, org, repo, base, head, title, body string) (string, error) {
	c, err := client(ctx)
	if err != nil {
		return "", err
	}
	owner, err := forge.Owner(org)
	if err != nil {
		return "", err
	}
	return c.Machine().Propose(ctx, owner, repo, base, head, title, body)
}
