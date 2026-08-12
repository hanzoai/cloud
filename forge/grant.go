package forge

// grant.go is the credential ONE coding run holds, and it is deliberately not an
// identity.
//
// # What a grant is
//
// A bounded permission to push to ONE repository, until it is withdrawn. It
// authenticates no person: it is a keypair minted for a run, registered on that
// repository as a WRITE DEPLOY KEY, and deleted when the run ends. The
// containment is structural rather than a list of checks — a deploy key is
// resolved by the forge against one repository row (models/asymkey), so it
// addresses that repository and nothing else, and it opens no API surface at
// all.
//
// # Why a deploy key and not a token
//
// Because this forge has nothing else that is repo-scoped, and the alternative
// is not merely weaker, it is unbuildable:
//
//   - Forgejo's access-token scopes are CATEGORIES, not repositories
//     (models/auth/access_token_scope.go). `write:repository` grants write to
//     every repository its user can reach, so a token handed to a sandbox is a
//     token over the whole tenant's code.
//   - Minting one at all needs the target's PASSWORD: POST /users/{u}/tokens is
//     behind reqBasicOrRevProxyAuth, and a token presented as a Basic password
//     sets the access_token method rather than the basic one, so the machine
//     credential cannot mint a token even for itself.
//
// A deploy key needs neither. It is SSH-ONLY — the HTTP path resolves permission
// from the authenticated USER and has no deploy-key branch (services/auth/basic.go)
// — which is why a run's remote is the forge's ssh_url and not its clone_url.
//
// # The two controls
//
// [Client.Grant] makes two independent checks, and neither is the other's
// backstop:
//
//	the ACTOR must be able to push        read as the human, so the forge's own
//	                                      ACL decides, not our intent
//	the KEY is minted by the MACHINE      Forgejo requires repo-ADMIN to add a
//	                                      deploy key (api.go reqAdmin), which an
//	                                      ordinary engineer with push rights does
//	                                      not have
//
// So the human's right to write is established by the forge, and the platform
// then acts on it. Dropping either leaves a hole: without the first, any caller
// who can name a repository gets a key to it; without the second, only repo
// admins could ever run an agent.
//
// # Lifetime
//
// The run withdraws it (apps/coding/start.go releases on every exit including a
// panic), and the forge is where it lives in the meantime — so unlike a table in
// one process's memory, a grant survives a cloud restart and is still withdrawn.
// The failure direction is the honest one: a withdrawal that does not happen
// leaves a key that opens ONE repository and is visible in that repository's own
// settings, where [Client.Grants] can find it.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Grant is a run's push credential, and everything the run needs to use it.
//
// The private key is the only secret here and it is the one thing that must
// reach the sandbox; every other field is an address. It is spelled `Key` and
// never logged — see the scrubbing in apps/coding/sandboxrunner.go, which takes
// it out of anything leaving the sandbox.
type Grant struct {
	// ID is the forge's deploy-key id — the handle [Client.Revoke] withdraws by.
	// It is not a credential and is safe to carry back and to log.
	ID int64
	// Owner and Repo are what this grant addresses, and the only thing it can.
	Owner, Repo string
	// Remote is the SSH remote a run clones and pushes. It comes from the FORGE's
	// own ssh_url, so a non-default port or a rewritten host arrives correct
	// rather than being re-derived here.
	Remote string
	// Key is the OpenSSH private key, PEM. NEVER logged, never persisted.
	Key string
	// Known is the forge's host key as a known_hosts line, learned by CLOUD (see
	// [Client.Known]). The sandbox pins to it, so an attacker on the sandbox's
	// own path cannot present a substitute host.
	Known string
	// Made is when the forge recorded the key. It is set only by [Client.Grants]
	// — a grant just minted knows its own age — and it is what [Client.sweep]
	// reads to tell a leftover from a live one.
	Made time.Time
}

// grantTitle is the prefix every grant's deploy-key title carries.
//
// It is the only way an operator reading a repository's key list can tell a
// run's ephemeral key from a real deploy key somebody added on purpose, and it
// is what [Client.Grants] matches on when sweeping keys a crashed process never
// withdrew.
const grantTitle = "hanzo-run-"

// Grant delegates push to ONE repository for the life of one run.
//
// The receiver must be scoped to the human the run acts for ([Client.As]): that
// actor's own right to push is the first control, checked by the forge, and an
// unscoped client refuses here rather than minting on the machine's authority.
//
// name distinguishes this run's key from another's in the repository's key list.
// It is not a secret and is shown in the forge UI, so it should be the session
// the run is for.
func (c *Client) Grant(ctx context.Context, owner, repo, name string) (Grant, error) {
	if err := validOrg(owner); err != nil {
		return Grant{}, err
	}
	if err := validOrg(repo); err != nil {
		return Grant{}, fmt.Errorf("forge: repo: %w", err)
	}
	if strings.TrimSpace(name) == "" {
		return Grant{}, fmt.Errorf("forge: a grant needs a name")
	}
	// CONTROL ONE, and it runs first so that a caller who may not write learns
	// nothing about the repository and leaves nothing behind on it. It is a
	// SUDOED read, so the answer is the forge's ACL applied to the human — a
	// caller that reached here on the machine client would be asking whether a
	// site administrator may push, which is true everywhere and is therefore no
	// control at all.
	r, err := c.Writable(ctx, owner, repo)
	if err != nil {
		return Grant{}, err
	}
	// CONTROL TWO: the repository must REFUSE this key on its default branch
	// before one exists. The credential is per-repository, so without a rule the
	// forge enforces, a run can rewrite main — and on a repository carrying
	// Actions workflows on self-hosted runners, that is code execution rather
	// than a bad commit. The orchestrator's single refspec does not substitute:
	// the push is SSH straight to the forge, with none of our code in the path.
	//
	// REFUSED rather than protected in passing, because silently changing an
	// existing repository's branch policy is not this code's decision (protect.go
	// says why). A repository this package created is already protected.
	guarded, err := c.Protected(ctx, owner, repo)
	if err != nil {
		return Grant{}, fmt.Errorf("forge: %s/%s: cannot read branch protection: %w", owner, repo, err)
	}
	if !guarded {
		return Grant{}, fmt.Errorf("%w: %s/%s (protect %s against direct and force pushes first)",
			ErrOpen, owner, repo, r.Branch)
	}

	priv, pub, err := keypair()
	if err != nil {
		return Grant{}, err
	}
	known, err := c.Known(ctx)
	if err != nil {
		return Grant{}, err
	}
	// Take back what an earlier run did not. A key on the forge has no expiry —
	// which is what makes it survive a cloud restart and still be withdrawable —
	// so the backstop the in-memory credential got from a TTL has to be an act
	// instead. Doing it HERE means no timer goroutine has to exist for it to
	// happen, and leftovers are bounded by "one run budget past the last run on
	// this repository" rather than by nothing at all.
	c.sweep(ctx, owner, repo)

	// CONTROL TWO. Machine, because Forgejo gates deploy keys on repo-ADMIN and
	// the human this run acts for is ordinarily a writer, not an admin. The
	// authority to be here was established above.
	var out struct {
		ID int64 `json:"id"`
	}
	if err := c.Machine().send(ctx, sendOpts{
		method: http.MethodPost,
		path:   "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/keys",
		body: map[string]any{
			"title": grantTitle + name,
			"key":   pub,
			// A run PUSHES. Read-only would clone and fail at the one step the
			// whole run exists to reach.
			"read_only": false,
		},
		out: &out,
	}); err != nil {
		return Grant{}, fmt.Errorf("forge: delegate push to %s/%s: %w", owner, repo, err)
	}
	if out.ID == 0 {
		return Grant{}, fmt.Errorf("forge: %s/%s accepted a key and named no id", owner, repo)
	}
	return Grant{
		ID: out.ID, Owner: owner, Repo: repo,
		Remote: r.SSH, Key: priv, Known: known,
	}, nil
}

// Revoke withdraws a grant, so its life is the RUN's life.
//
// Machine for the same reason [Client.Grant] is: the key it deletes was minted
// under repo-admin and only repo-admin can remove it. An id belonging to no key
// is not an error — a withdrawal is idempotent, and a run that already lost its
// key must not fail trying to give it back twice.
func (c *Client) Revoke(ctx context.Context, owner, repo string, id int64) error {
	if err := validOrg(owner); err != nil {
		return err
	}
	if err := validOrg(repo); err != nil {
		return fmt.Errorf("forge: repo: %w", err)
	}
	if id <= 0 {
		return nil
	}
	err := c.Machine().send(ctx, sendOpts{
		method: http.MethodDelete,
		path: fmt.Sprintf("/repos/%s/%s/keys/%d",
			url.PathEscape(owner), url.PathEscape(repo), id),
	})
	if isMissing(err) {
		return nil
	}
	return err
}

// Grants lists the run keys currently registered on a repository.
//
// It exists so that keys a crashed process never withdrew are FINDABLE rather
// than merely improbable: an operator, or a sweep, can ask a repository what
// grants it is still carrying and revoke them. Only keys minted by [Client.Grant]
// are returned — a deploy key somebody added deliberately is not this function's
// to report or to remove.
func (c *Client) Grants(ctx context.Context, owner, repo string) ([]Grant, error) {
	if err := validOrg(owner); err != nil {
		return nil, err
	}
	if err := validOrg(repo); err != nil {
		return nil, fmt.Errorf("forge: repo: %w", err)
	}
	var keys []struct {
		ID      int64     `json:"id"`
		Title   string    `json:"title"`
		Created time.Time `json:"created_at"`
	}
	// PAGED. The forge answers a bounded page (30 by default) and a repository
	// that has accumulated abandoned keys is exactly the one with more than a
	// page of them — so an unpaged read would stop seeing the keys precisely when
	// there are enough to matter.
	q := url.Values{"limit": {strconv.Itoa(page)}, "page": {"1"}}
	if err := c.Machine().do(ctx,
		"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/keys", q, &keys); err != nil {
		return nil, err
	}
	for p := 2; len(keys)%page == 0 && len(keys) > 0 && p <= maxPages; p++ {
		var more []struct {
			ID      int64     `json:"id"`
			Title   string    `json:"title"`
			Created time.Time `json:"created_at"`
		}
		q := url.Values{"limit": {strconv.Itoa(page)}, "page": {strconv.Itoa(p)}}
		if err := c.Machine().do(ctx,
			"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/keys", q, &more); err != nil {
			return nil, err
		}
		if len(more) == 0 {
			break
		}
		keys = append(keys, more...)
	}
	var out []Grant
	for _, k := range keys {
		// The title must be one THIS package writes: the prefix and a session id
		// after it, with no spaces. A human is free to name a key "hanzo-run-mine",
		// and a sweep that deleted it because the prefix matched would be this code
		// removing somebody else's access.
		name, ok := strings.CutPrefix(k.Title, grantTitle)
		if !ok || name == "" || strings.ContainsAny(name, " \t/") {
			continue
		}
		out = append(out, Grant{ID: k.ID, Owner: owner, Repo: repo, Made: k.Created})
	}
	return out, nil
}

// stale is how long past a run's longest possible life a grant may survive
// before [Client.sweep] takes it back.
//
// It is the engine's own ceiling (apps/coding maxRunBudget is 30 minutes) with
// room for a push at the very end of one, and it is deliberately generous: this
// is the backstop for a process that DIED, not the ordinary path. The ordinary
// path is the run withdrawing its own grant, and a sweep that raced a live run
// would break working runs to tidy up after broken ones.
const stale = time.Hour

// sweep withdraws run grants on this repository that outlived any possible run.
//
// Best-effort by construction: it is housekeeping in front of the act the caller
// actually asked for, and a forge that will not answer this must not stop a run
// from starting. A leftover key is visible in the repository's own settings and
// will be swept by the next run on it.
func (c *Client) sweep(ctx context.Context, owner, repo string) {
	old, err := c.Grants(ctx, owner, repo)
	if err != nil {
		return
	}
	cut := time.Now().Add(-stale)
	for _, g := range old {
		if !g.Made.IsZero() && g.Made.Before(cut) {
			_ = c.Revoke(ctx, owner, repo, g.ID)
		}
	}
}

// keypair mints the run's identity: an ed25519 key, in memory, used once.
//
// ed25519 because it is the shortest thing every git and every OpenSSH in the
// estate accepts, and because generating one costs microseconds — a per-run key
// is only affordable if minting it is free.
func keypair() (priv, pub string, err error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("forge: rng: %w", err)
	}
	blk, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		return "", "", fmt.Errorf("forge: marshal key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(public)
	if err != nil {
		return "", "", fmt.Errorf("forge: public key: %w", err)
	}
	return string(pem.EncodeToMemory(blk)), string(ssh.MarshalAuthorizedKey(sshPub)), nil
}

// ── the host key ─────────────────────────────────────────────────────────────

// hostKeys is what CLOUD has learned about a forge's SSH host key, per host.
//
// Learned once and held for the life of the process: a host key is the forge's
// identity and does not rotate on a request's timescale, and re-learning it per
// run would put a TCP handshake in front of every dispatch for no new fact.
var hostKeys struct {
	sync.Mutex
	m map[string]string
}

// Known is the forge's SSH host key as a known_hosts line.
//
// It is CONFIGURED where the deployment has recorded it ([HostKeyRef]), and
// learned here otherwise — either way by cloud, and handed to the run.
// A sandbox told to accept whatever key answers (StrictHostKeyChecking=no, or
// accept-new) trusts its own network, and the sandbox's network is the one place
// in this system that runs untrusted output. Pinning to what cloud saw means an
// attacker has to be on CLOUD's path to the forge instead, which is the path
// already carrying the machine credential.
//
// It is a read of the forge's PUBLIC identity, so it needs no credential and
// makes no authenticated call — the handshake is abandoned as soon as the key is
// in hand.
func (c *Client) Known(ctx context.Context) (string, error) {
	// THE CONFIGURED PIN WINS. It is the deployment stating a fact it owns, and
	// it removes the first-use window entirely; everything below is the fallback
	// for a deployment that has not recorded it yet (see [HostKeyRef]).
	if k := strings.TrimSpace(c.known); k != "" {
		return k, nil
	}
	host := c.host
	if host == "" {
		return "", fmt.Errorf("forge: no host")
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "22")
	}
	hostKeys.Lock()
	if line, ok := hostKeys.m[host]; ok {
		hostKeys.Unlock()
		return line, nil
	}
	hostKeys.Unlock()

	line, err := scan(ctx, host)
	if err != nil {
		return "", err
	}
	hostKeys.Lock()
	if hostKeys.m == nil {
		hostKeys.m = map[string]string{}
	}
	hostKeys.m[host] = line
	hostKeys.Unlock()
	return line, nil
}

// scan opens one SSH handshake far enough to read the server's host key, then
// drops it. There is no login: the key is presented before authentication, which
// is what makes this a read of a public fact rather than a use of a credential.
func scan(ctx context.Context, hostport string) (string, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return "", fmt.Errorf("forge: reach %s: %w", hostport, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	}

	var key ssh.PublicKey
	// The handshake is EXPECTED to fail, at authentication, one step after the
	// host key arrives — there is no account to log into and none is wanted. So
	// the key is captured in the callback and the returned error is only
	// interesting when the callback never ran.
	_, _, _, err = ssh.NewClientConn(conn, hostport, &ssh.ClientConfig{
		User: "git",
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			key = k
			return nil
		},
		Timeout: 10 * time.Second,
	})
	if key == nil {
		return "", fmt.Errorf("forge: %s offered no host key: %w", hostport, err)
	}
	// The hostname form known_hosts wants: bare for port 22, bracketed with the
	// port otherwise, which is what OpenSSH itself writes.
	h, p, _ := net.SplitHostPort(hostport)
	name := h
	if p != "22" {
		name = "[" + h + "]:" + p
	}
	return name + " " + string(ssh.MarshalAuthorizedKey(key)), nil
}
