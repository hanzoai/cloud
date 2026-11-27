package forge

// push.go is the repository as something you PUSH TO.
//
// Every other file here reads and writes the forge over REST, which is how the
// forge answers questions ABOUT a repository. This one is about the second
// address a repository has — its git remote — and about there being a
// repository at that address to receive a push.
//
// It is small on purpose. The git protocol itself is not spoken here: a pack is
// megabytes to gigabytes and is streamed by the git CLI under a hardened
// environment, which is the caller's job (apps/sync). What this file supplies is
// the ADDRESS and the CREDENTIAL, because those are facts about the forge, and a
// caller deriving either one itself is a second spelling of the one thing that
// has to be right.
//
// # Born empty, born writable, born advance-only, born quiet
//
// [Client.Init] is deliberately not [Client.Ensure]. Ensure makes a repository
// for a coding run: it auto-inits a first commit so a clone has a HEAD, and it
// protects the default branch so a run's key cannot rewrite it. Both are exactly
// wrong for a repository that exists to receive an upstream:
//
//   - an auto-init commit is a commit the upstream does not have, so the FIRST
//     fast-forward push of that branch is a divergence — the repository would be
//     born in conflict and stay there;
//   - a protected default branch refuses every direct push including ours
//     (enable_push false is read for everyone, site administrator included), so
//     the sync could never land the branch it exists to land.
//
// So a sync's repository is created empty and pushable, and the two creation
// paths stay separate rather than one growing flags. What it is NOT is
// unguarded: every branch is made advance-only ([Client.monotone]), which leaves
// the push open and refuses the two things that lose history — a force and a
// delete. The client that fills this repository never asks for either; the
// server refusing them is what makes that true of every OTHER client too.
//
// Nor does it run anything ([Client.quiet]). Its workflows are the upstream's,
// and GitHub already runs them at home; a synced push landing here would run
// them a second time on our runners.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Remote is one repository's git address and the credential that opens it.
//
// Token is THE MACHINE CREDENTIAL. It is handed out because git is a subprocess
// and a subprocess needs it, and it carries the same obligation everywhere it
// goes: it rides an env-injected http.extraHeader, never a URL, never argv,
// never a log line. A credential on argv is readable by every process on the
// box; a credential in a URL is written to the reflog and to any error the
// transport prints.
type Remote struct {
	// URL is the address git clones and pushes: https, or http to a cluster
	// service when [TransferEnv] names one.
	URL string
	// User is the basic-auth username the token is presented with. The forge
	// reads a token from either half of a Basic credential; the estate spells it
	// x-access-token everywhere, so it is spelled that way here too.
	User string
	// Token is the machine credential. Never log this.
	Token string
}

// pushUser is the basic-auth username a forge token is presented with.
//
// The forge accepts a token as either half of a Basic credential, so this could
// be anything; it is the estate's one spelling of "the username is not the
// secret, the password is" (apps/sync's outbound push and the retired mirror
// path both write it), and one spelling is the point.
const pushUser = "x-access-token"

// Remote is the git address of owner/repo on this forge, with the credential.
//
// The SCHEME and HOST come from the client's own base — or [TransferEnv], the
// same forge reached from inside its cluster — not from a literal here:
// a deployment talking to a forge on another host — a developer box, a staging
// forge — must push where it reads, and a second derivation is how those two
// come apart.
//
// It is NOT a statement that the repository exists. [Client.Init] is.
func (c *Client) Remote(owner, repo string) (Remote, error) {
	if err := validSegment(owner); err != nil {
		return Remote{}, err
	}
	if err := validSegment(repo); err != nil {
		return Remote{}, fmt.Errorf("forge: repo: %w", err)
	}
	if c.token == "" {
		return Remote{}, ErrNoToken
	}
	origin, err := c.transfer()
	if err != nil {
		return Remote{}, err
	}
	return Remote{
		URL:   origin + "/" + owner + "/" + repo + ".git",
		User:  pushUser,
		Token: c.token,
	}, nil
}

// TransferEnv names the origin git transfers reach the forge by when that is not
// its public one: in a cluster that runs the forge, its service
// (http://hanzo-git.hanzo.svc). The public origin sits behind an edge that cuts
// a request at 100 MB, and a pack is routinely larger: every import of a big
// repository failed there, was retried on the next pass, and left its partial
// upload on the forge's disk.
const TransferEnv = "CLOUD_FORGE_GIT"

// transfer is the origin c's git clones and pushes go to: [TransferEnv] when set,
// the forge's public origin otherwise. A configured origin that could carry the
// machine credential in clear to another machine is refused, not ignored, so a
// typo cannot quietly send every pack back through the edge.
func (c *Client) transfer() (string, error) {
	raw := strings.TrimSpace(os.Getenv(TransferEnv))
	if raw == "" {
		return strings.TrimSuffix(c.base, API), nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("forge: %s=%q is not an origin", TransferEnv, raw)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && Local(u.Hostname())) {
		return "", fmt.Errorf("forge: %s=%q would send the credential in clear off the cluster", TransferEnv, raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

// Local reports whether host is reachable only from inside this machine or this
// cluster: loopback, or a Kubernetes service name. Those are the hosts a
// credential may travel to over plain http.
func Local(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".svc") || strings.HasSuffix(h, ".svc.cluster.local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Init ensures an EMPTY, pushable, advance-only repository exists at owner/repo.
//
// Empty because its content arrives by push and an auto-init commit would put a
// commit in it that the upstream does not have — see the file comment. Private
// because a repository is born closed and opening it is a decision. Idempotent:
// a repository that is already there is the state the caller wanted, which the
// forge reports as 409, and the rule is (re)stated either way — a repository
// created before this deployment learned to ask for it is brought into line by
// the next import rather than staying open forever.
//
// QUIET before the first push lands: the Actions unit is off, so no push this
// sync makes starts a run. The create call takes no units, so it is a second
// write, restated on every Init like the rule below.
//
// ADVANCE-ONLY is the second half of the same sentence and not an extra: a
// synced repository holds a canonical copy whose whole value is that it only
// ever gains history. The client that writes it never forces and never deletes,
// but a client's discipline protects nothing from the next client — so the forge
// is asked to refuse both. See [Client.monotone].
//
// It acts as the MACHINE. There is no human behind a sync — it runs on a
// schedule and on a webhook — and sudoing as whoever last touched the sync would
// attribute a platform decision to them. The namespace is still not the caller's
// to choose: owner reaches this from [Owner], a closed table.
func (c *Client) Init(ctx context.Context, owner, repo, description string) error {
	if err := validSegment(owner); err != nil {
		return err
	}
	if err := validSegment(repo); err != nil {
		return fmt.Errorf("forge: repo: %w", err)
	}
	err := c.Machine().send(ctx, sendOpts{
		method: http.MethodPost,
		path:   "/orgs/" + url.PathEscape(owner) + "/repos",
		body: map[string]any{
			"name":        repo,
			"description": description,
			"private":     true,
			// FALSE, and it is the load-bearing field in this call.
			"auto_init":      false,
			"default_branch": "main",
		},
	})
	if err != nil && !errors.Is(err, ErrExists) {
		return fmt.Errorf("forge: create %s/%s: %w", owner, repo, err)
	}
	if err := c.quiet(ctx, owner, repo); err != nil {
		return err
	}
	return c.monotone(ctx, owner, repo)
}

// quiet turns the Actions unit off on owner/repo, so a push to it starts no run.
func (c *Client) quiet(ctx context.Context, owner, repo string) error {
	err := c.Machine().send(ctx, sendOpts{
		method: http.MethodPatch,
		path:   "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo),
		body:   map[string]any{"has_actions": false},
	})
	if err != nil {
		return fmt.Errorf("forge: keep %s/%s quiet: %w", owner, repo, err)
	}
	return nil
}

// Default points owner/repo's HEAD at branch, so a clone resolves the same
// default the upstream has.
//
// The branch must already be there — the forge refuses to point HEAD at a ref it
// does not hold — so a caller sets this AFTER the branch has landed, which is
// also the only moment it is a true statement.
func (c *Client) Default(ctx context.Context, owner, repo, branch string) error {
	if err := validSegment(owner); err != nil {
		return err
	}
	if err := validSegment(repo); err != nil {
		return fmt.Errorf("forge: repo: %w", err)
	}
	if strings.TrimSpace(branch) == "" {
		return fmt.Errorf("forge: empty branch")
	}
	return c.Machine().send(ctx, sendOpts{
		method: http.MethodPatch,
		path:   "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo),
		body:   map[string]any{"default_branch": branch},
	})
}
