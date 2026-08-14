// Package forge is the client for the deployment's code forge — the Forgejo
// instance (git.hanzo.ai beside api.hanzo.ai) that holds the estate's issues and
// milestones.
//
// It exists because those work items have ONE home. The forge is where an issue
// is filed, labelled, assigned and closed; a second copy in another store would
// be a second answer to "what is the state of this work", and the two would
// drift. So nothing here mirrors or writes through to a local table: every read
// is a read OF the forge, and every write is a write TO it.
//
// The one thing held between a read and the next is a bounded STALENESS WINDOW
// on the two list endpoints that cost seconds rather than milliseconds — see
// cache.go, which states why that is not the drifting copy this paragraph
// refuses, and why its key must carry the actor. No write is cached, and a
// cached answer is only ever replaced by the forge's own.
//
// # The wire
//
// The forge answers its REST API at /v1 — NOT /api/v1, which 404s. That is the
// one fact most likely to be mis-remembered from upstream Gitea documentation,
// so it is stated once, here, as [API], and never spelled again.
//
// # The two credentials, and why there is only one
//
// A caller of this package is a REQUEST from a user, but the credential is the
// DEPLOYMENT's: one machine token, read from KMS (never an env file, never a
// browser-side PAT, never a per-user OAuth grant this process would have to
// custody). A per-user token would mean N secrets to rotate, revoke and leak;
// one token means one.
//
// One token would ordinarily mean one identity, and therefore one permission
// set — the machine's — applied to every user's request. That is the trap, and
// [Client.As] is the way out: Forgejo's Sudo lets an authorized token ACT AS a
// named user, and it DROPS PRIVILEGE to that user rather than merely relabelling
// the actor. Measured against this deployment's forge:
//
//	token alone,          GET /v1/repos/hanzo-private/patents  → 200
//	token + Sudo: deploy, GET /v1/repos/hanzo-private/patents  → 404
//	anonymous,            GET /v1/repos/hanzo-private/patents  → 404
//
// The sudoed request is byte-identical to the anonymous one: the forge's own
// ACL, not this client's good intentions, decided what came back. That is what
// makes one credential safe to hold. It also makes a write ATTRIBUTABLE — the
// issue's actor is the human, not a shared bot — which is the audit property a
// shared machine identity otherwise destroys.
//
// # Fail closed
//
// Sudo is not optional on a user-facing call and there is no "unsudoed
// fallback": [Client.As] with an empty login returns a client whose every call
// refuses ([ErrNoActor]), because falling back to the raw machine token on a
// missing actor is exactly the escalation this design exists to prevent. A
// login the forge does not know 404s, which surfaces as [ErrUnknownActor]
// rather than an empty result — an empty board and "you have no forge account"
// are different answers and must not be spelled the same way.
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// API is the forge's REST prefix.
//
// It is /v1 and not /api/v1: this deployment's forge serves the API at the apex
// of the host it owns, and /api/v1 answers 404. Stated once so no call site
// re-derives it from upstream docs.
const API = "/v1"

// Name is what the forge answers to among the sibling hosts a deployment owns,
// for brand.Sibling(domain, forge.Name) — the ONE derivation of the forge host
// from the deployment's own domain. Spelling "git.hanzo.ai" into a config is
// what makes a white-labelled deployment (lux.network, zoo.ngo) talk to another
// brand's forge.
const Name = "git"

// page is the forge's max page size for a paginated list. Fixed by the server;
// asking for more returns this many anyway.
const page = 50

// maxPages bounds every pagination loop. A forge that keeps answering full pages
// — buggy, or hostile after a compromise — must not spin this process forever.
// 50 pages x 50 items is 2,500 repos or issues, past any real org.
const maxPages = 50

// repoPage is the page size for the repository list, and it is SMALL ON PURPOSE.
//
// This is the performance fix, and it is a fix at the QUERY rather than a cache
// over a slow one. The forge's cost on /orgs/{org}/repos is per REPOSITORY
// RETURNED and serialises inside a single request, but separate requests run in
// parallel. Measured against git.hanzo.ai's 64-repo `hanzo` org:
//
//	one page of 50, then the remainder   20.7s   (what this replaced)
//	7 concurrent pages of 10              4.3s
//	13 concurrent pages of 5              2.7s
//
// Per-request cost fits ~1.2s fixed + ~0.27s per repository, so the wall time of
// a concurrent walk is set by the PAGE SIZE, not by the size of the org. Five
// keeps a 64-repo org inside one [fanout] wave while staying large enough that a
// small org is not a burst of near-empty requests.
const repoPage = 5

// maxRepoPages bounds the repository walk. Stated in pages of [repoPage] so that
// shrinking the page size did not quietly shrink the ceiling with it: this is
// the same 2,500 repositories the issue walk allows.
const maxRepoPages = 2500 / repoPage

// maxBody bounds a single response read. The forge is a trusted service, but
// "trusted" is a statement about intent and not about compromise, and an
// unbounded io.ReadAll on a remote body is an OOM one bad response away.
const maxBody = 32 << 20 // 32 MiB

// fanout bounds the concurrent per-repo requests an org rollup makes. The forge
// has no org-level milestones API, so a rollup is N repo calls; unbounded, a
// large org would open hundreds of sockets at once and the rollup would read as
// a denial-of-service against our own forge.
//
// 16 rather than 8: a single milestone call costs 0.3-1.3s and a wave of 8
// completes in ~0.8s (measured on git.hanzo.ai), so the fan-out is cheap next to
// the repository list that opens the rollup, and halving the number of waves
// takes the cold rollup for a 64-repo org from ~8s to ~4s. Still small enough
// that a rollup is a handful of sockets, not a flood.
const fanout = 16

// maxRollup bounds the repositories one org rollup will fan out over.
//
// It is a REFUSAL threshold, not a truncation: see [Client.Milestones]. Set far
// above any real org here (the largest is 64) because the honest use of this
// number is to stop a runaway — a forge that answers a repo list wrongly, or an
// org that has genuinely outgrown a synchronous rollup — rather than to trim a
// working org down to a partial answer.
const maxRollup = 300

// Errors a caller must be able to tell apart. They are distinguished because the
// right answer differs: no actor is a bug in the CALLER (it forgot to scope),
// an unknown actor is a fact about the USER (no forge identity), and neither is
// "the board is empty".
var (
	// ErrNoActor is returned by every call on a client with no Sudo actor. It is
	// a refusal, never a fallback to the machine identity.
	ErrNoActor = errors.New("forge: no actor — a user-facing call must be scoped with As() or Machine()")

	// ErrUnknownActor means the forge does not know the actor we sudoed as.
	ErrUnknownActor = errors.New("forge: unknown actor — no forge identity for this user")

	// ErrNoToken means the machine credential is absent or empty.
	ErrNoToken = errors.New("forge: no service token")

	// ErrNotFound is a 404 on a MACHINE call, where there is no actor for it to
	// be a statement about. On a sudoed call the same status is ErrUnknownActor,
	// because there it genuinely cannot be told apart from "this user may not see
	// that" — and reading it as plain absence is how a permission failure becomes
	// a silently empty answer.
	ErrNotFound = errors.New("forge: not found")

	// ErrExists is a 409, which the create paths treat as success-by-another-name:
	// a repository that is already there is the state the caller wanted.
	ErrExists = errors.New("forge: already exists")
)

// Client talks to one forge as one actor.
//
// The zero Client is unusable; build one with [New]. A Client is safe for
// concurrent use, and [Client.As] derives a per-request actor cheaply (it copies
// a struct and shares the transport) so a request handler never mutates a shared
// one — mutating a shared actor is a cross-user attribution race, and the type
// is shaped so that it cannot be written.
type Client struct {
	base  string // scheme://host/v1
	host  string // bare host (git.hanzo.ai) — the clone/ssh remotes are built from it
	token string // machine credential from KMS — NEVER logged
	actor string // Forgejo Sudo login; empty ⇒ every call refuses
	// known is the forge's SSH host key as a known_hosts line, configured rather
	// than learned. Empty falls back to a handshake — see [Client.Known].
	known string
	// machine drops Sudo and calls as the DEPLOYMENT. It is a separate field
	// rather than a sentinel actor so that "unscoped" and "deliberately the
	// machine" cannot be spelled the same way — the whole refusal in [Client.As]
	// rests on an empty actor meaning a bug, and a magic string would erase that.
	machine bool
	http    *http.Client

	// The two list endpoints that cost seconds rather than milliseconds are
	// answered through a read cache keyed by (actor, org) — see cache.go for
	// why it is not a second source of truth, and why keying it by org alone
	// would be a cross-user read. As() shares them, which is the point: the
	// cache belongs to the FORGE CONNECTION, not to one request's actor.
	repos  *cache[[]Repo]
	rollup *cache[[]Milestone]
}

// New builds a client for the forge at `host` authenticating with `token`.
//
// host is a bare host (git.hanzo.ai) or a full origin; token is the machine
// credential, which the caller reads from KMS. An empty token is not deferred to
// the first call — a client that cannot authenticate is a configuration error
// and says so at construction.
func New(host, token string) (*Client, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("forge: empty host")
	}
	if strings.TrimSpace(token) == "" {
		return nil, ErrNoToken
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("forge: bad host: %w", err)
	}
	if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
		// The machine token rides every request. Sending it over cleartext to a
		// remote host puts it on the wire for anyone on the path; loopback is
		// exempt so tests and a local forge work without a certificate.
		return nil, fmt.Errorf("forge: refusing non-https host %q", u.Host)
	}
	return &Client{
		base:  strings.TrimSuffix(u.Scheme+"://"+u.Host, "/") + API,
		host:  u.Host,
		token: strings.TrimSpace(token),
		// A backstop, not the budget. A caller with a deadline on its context
		// binds the call tighter than this and is what actually bounds a
		// request; this only stops a call made WITHOUT one (a CLI invoke, a
		// background refresh whose budget is longer) from hanging forever.
		http:   &http.Client{Timeout: 30 * time.Second},
		repos:  newCache[[]Repo](),
		rollup: newCache[[]Milestone](),
	}, nil
}

// Reuse points c at prev's read caches, so REFRESHING THE MACHINE CREDENTIAL
// does not throw away a warm repository list.
//
// The caller re-reads the token periodically to make rotation real, and builds
// a new Client each time it does. Without this, every rotation would drop the
// cache and hand the next board load the full cold-path wait — the credential's
// lifetime would silently become the cache's, which is two unrelated policies
// braided into one number.
//
// Safe because the caches are keyed by ACTOR and the identity of the machine
// credential does not change across a rotation of its secret: the same actor
// sees the same repositories before and after.
func (c *Client) Reuse(prev *Client) {
	if prev == nil || c == nil {
		return
	}
	if prev.repos != nil {
		c.repos = prev.repos
	}
	if prev.rollup != nil {
		c.rollup = prev.rollup
	}
}

// key is the cache coordinate of a per-actor list.
//
// The ACTOR leads and the separator is a byte neither half can contain, so no
// two (actor, org) pairs can spell one key. That is the whole tenancy argument
// for the cache: see cache.go.
func (c *Client) key(org string) string {
	if c.machine {
		// The machine is a distinct reader with a distinct visible set, and it must
		// not share a cache entry with the user whose actor is empty — which is
		// nobody, but would be spelled the same way.
		return "\x00machine\x00" + org
	}
	return c.actor + "\x00" + org
}

// As returns a client that acts as the forge user `login`, dropping privilege to
// that user's own permissions for every call made through it (see the package
// comment for the measurement).
//
// The receiver is not modified: the returned client is a copy sharing the same
// transport, so concurrent requests each hold their own actor and no two can
// interleave. A blank login yields a client that REFUSES rather than one that
// falls back to the machine identity.
func (c *Client) As(login string) *Client {
	cp := *c
	cp.actor, cp.machine = strings.TrimSpace(login), false
	return &cp
}

// Machine returns a client that calls as the DEPLOYMENT itself, with no Sudo.
//
// It is the identity behind the machine token, which Forgejo requires to be a
// site administrator for Sudo to work at all (routers/api/v1/api.go sudo()), so
// it can do anything on this forge. That is why it is a NAMED, deliberate step
// and never something [Client.As] falls back to.
//
// It is for the operations that have no human behind them and cannot borrow
// one: provisioning a run's credential (Forgejo requires repo-ADMIN to add a
// deploy key, which an ordinary engineer with push rights does not have), and
// confirming that a ref a sandbox claims to have pushed really landed. Both are
// acts of the platform, not of a person, and pretending otherwise by sudoing as
// whoever triggered the run would attribute a platform decision to them.
//
// A machine call is not an unchecked one. The caller authorizes the HUMAN
// separately — see [Client.Writable] — so the forge's own ACL still decides
// whether that person may write the repository before the machine acts for them.
func (c *Client) Machine() *Client {
	cp := *c
	cp.actor, cp.machine = "", true
	return &cp
}

// Actor is the forge login this client acts as, empty if unscoped or machine.
// For logs and errors — the actor is an identity, not a credential, and is safe
// to record.
func (c *Client) Actor() string { return c.actor }

// Host is the bare forge host (git.hanzo.ai). It is what the clone and SSH
// remotes are built from, so a caller never spells the host itself.
func (c *Client) Host() string { return c.host }

// do issues one authenticated, sudoed GET and decodes JSON into out.
//
// It is the ONE place the credential is attached and the ONE place Sudo is
// enforced, so neither can be forgotten by a call site: every method below is
// written in terms of this.
func (c *Client) do(ctx context.Context, path string, q url.Values, out any) error {
	_, err := c.get(ctx, path, q, out)
	return err
}

// open issues one authenticated, sudoed GET and hands the LIVE BODY back.
//
// It is the ONE place the credential is attached and the ONE place Sudo is
// enforced. Everything that reads the forge is written in terms of it —
// [Client.get] for the JSON endpoints, tree.go for the archive, which is gzip
// and cannot be decoded as JSON — so no second call site can attach the token
// itself and forget the actor with it.
//
// THE CALLER CLOSES THE BODY, and only on the success path: every refusal here
// closes it before returning, so a caller that checks the error first can never
// leak a connection.
func (c *Client) open(ctx context.Context, path string, q url.Values, accept string) (*http.Response, error) {
	if c.actor == "" && !c.machine {
		return nil, ErrNoActor
	}
	if c.token == "" {
		return nil, ErrNoToken
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("forge: build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+c.token)
	// Sudo as a HEADER, never as a ?sudo= query parameter. Both work, but a query
	// parameter lands in access logs and proxy traces, so the actor of every
	// request would be written into logs the forge and every hop keep. The header
	// form keeps attribution out of URL telemetry.
	if !c.machine {
		req.Header.Set("Sudo", c.actor)
	}
	req.Header.Set("Accept", accept)

	resp, err := c.http.Do(req)
	if err != nil {
		// The URL is safe to surface (it names a path, not a secret) but the error
		// from the transport can embed the request URL only — never a header — so
		// the token cannot ride out in an error string.
		return nil, fmt.Errorf("forge: GET %s: %w", path, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusNotFound:
		// With Sudo set, the forge answers 404 both for "the actor does not exist"
		// and for "this actor cannot see that". Neither is an error the caller can
		// fix by retrying, and both must read as "no access", never as an empty
		// success — a 404 rendered as an empty list is how a board silently lies.
		// A machine call has no actor for it to be a statement about, so there the
		// same status is plain absence.
		resp.Body.Close()
		if c.machine {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("%w: %s", ErrUnknownActor, c.actor)
	case http.StatusUnauthorized, http.StatusForbidden:
		resp.Body.Close()
		return nil, fmt.Errorf("forge: %s: credential rejected (%d)", path, resp.StatusCode)
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("forge: %s: unexpected status %d", path, resp.StatusCode)
	}
}

// get is [Client.do] with the response headers surfaced.
//
// Only the repository walk needs them, and it needs exactly one: X-Total-Count,
// which is what lets it fetch its pages CONCURRENTLY instead of discovering the
// end of the list one serial page at a time. Everything else calls do and stays
// unaware that a response has headers at all.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) (http.Header, error) {
	resp, err := c.open(ctx, path, q, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("forge: read %s: %w", path, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("forge: decode %s: %w", path, err)
	}
	return resp.Header, nil
}

// ── the wire shapes ──────────────────────────────────────────────────────────
//
// Only the fields the tracker renders are declared. A struct that mirrored every
// forge field would be a second schema to maintain against an upstream we do not
// control, and would publish fields our surface never promised.

// Repo is one repository under an org.
type Repo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	Archived bool   `json:"archived"`
	Empty    bool   `json:"empty"`
	Open     int    `json:"open_issues_count"`
	Branch   string `json:"default_branch"`
	Size     int64  `json:"size"` // KiB, as the forge reports it

	// UpdatedAt is when the forge last saw this repository move.
	//
	// A time and not the string the wire carries, because it is COMPARED against
	// a window and FORMATTED as a date and never echoed — parsing it at each call
	// site is the same rule written twice, and the second spelling is the one that
	// gets the layout wrong. The zero value means the forge sent none, which reads
	// as "has not moved" rather than as "moved at the epoch".
	UpdatedAt time.Time `json:"updated_at"`

	// SSH is the remote a run clones and pushes, taken from the forge rather than
	// built here: it already carries a non-default port and any host rewrite the
	// deployment has, and re-deriving it would be a second spelling of the one
	// address that has to be right.
	SSH string `json:"ssh_url"`

	// Perm is what the READING actor may do here, as the forge computed it. It
	// is present on a repository read and absent from a list, and it is how a
	// caller asks "may this person write?" without reimplementing the ACL —
	// see [Client.Writable].
	Perm *Perm `json:"permissions,omitempty"`
}

// Perm is the forge's own verdict on one actor's access to one repository.
type Perm struct {
	Admin bool `json:"admin"`
	Push  bool `json:"push"`
	Pull  bool `json:"pull"`
}

// User is a forge account, as an issue's author or assignee.
type User struct {
	Login  string `json:"login"`
	Avatar string `json:"avatar_url"`
}

// Label is a forge label. It carries the board's column: the tracker's status is
// a label set on the forge, not a column in a table here (see [Issues]).
type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Milestone is a forge milestone, always repo-scoped — the forge has no
// org-level milestone. [Client.Milestones] is the org rollup.
type Milestone struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Open   int    `json:"open_issues"`
	Closed int    `json:"closed_issues"`
	Due    string `json:"due_on,omitempty"`

	// Repo is the repository this milestone belongs to. The forge does not send
	// it — a repo-scoped list has no reason to — and the rollup fills it in, so a
	// caller merging N repos' milestones can still tell them apart.
	Repo string `json:"repo"`
}

// Issue is one work item. The forge's issues-search answers labels, milestone
// and assignees INLINE, so a board renders from one request rather than one
// request per card.
type Issue struct {
	ID        int64      `json:"id"`
	Number    int64      `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"`
	URL       string     `json:"html_url"`
	Labels    []Label    `json:"labels"`
	Milestone *Milestone `json:"milestone,omitempty"`
	User      *User      `json:"user,omitempty"`
	Assignees []User     `json:"assignees"`
	Created   string     `json:"created_at"`
	Updated   string     `json:"updated_at"`

	// PullRequest is non-nil when the row is a PR rather than an issue. The forge
	// returns both from one search; the tracker's Kind is read from this.
	PullRequest *struct {
		Merged bool `json:"merged"`
	} `json:"pull_request,omitempty"`

	// Repository names the repo the issue lives in. Present on issues-search
	// (which spans repos) and the only way to address the issue afterwards.
	Repository *struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Owner    string `json:"owner"`
	} `json:"repository,omitempty"`
}

// ── the reads ────────────────────────────────────────────────────────────────

// Repos lists the repositories of `org` that this client's actor can see.
//
// The actor's own visibility is what bounds the answer: a user who is not a
// member of a private org gets that org's public repos and nothing else, decided
// by the forge rather than by a filter here.
// This is the expensive one — the forge computes permissions and statistics per
// repository, so it costs 10-22s for a 64-repo org against ~1s for a 1-repo org
// (measured on git.hanzo.ai). It is therefore READ THROUGH A CACHE keyed by
// (actor, org); cache.go states why that key must carry the actor, and why a
// bounded staleness window on a read is not the mirrored copy this package
// otherwise refuses.
func (c *Client) Repos(ctx context.Context, org string) ([]Repo, error) {
	if err := validOrg(org); err != nil {
		return nil, err
	}
	// Ahead of the cache, so an unscoped client refuses rather than taking a
	// slot in it. Same refusal do() would make, made before anything is stored.
	if c.actor == "" && !c.machine {
		return nil, ErrNoActor
	}
	got, err := c.repos.do(ctx, c.key(org), func(ctx context.Context) ([]Repo, error) {
		return c.listRepos(ctx, org)
	})
	if err != nil {
		return nil, err
	}
	// A copy: the cached slice is shared by every caller holding this entry, and
	// one that sorted or filtered it in place would rewrite what the next caller
	// reads.
	return append([]Repo(nil), got...), nil
}

// Inventory is [Client.Repos] read from the forge NOW, with no cache.
//
// The two are the same question asked for different purposes. A board being
// rendered can take an answer minutes old — that is what the staleness window in
// cache.go buys, and what makes a board load in three seconds instead of twenty.
// A JUDGEMENT cannot: the visibility audit closes every repository no live
// project permits to be open, and a stale list would have it act on a repository
// that has since changed and, worse, silently skip one that has since been
// opened. So the caller that decides something reads the forge.
//
// It neither reads nor fills the cache, so an audit cannot displace what a board
// is being served from, and cannot be served what a board left behind.
func (c *Client) Inventory(ctx context.Context, org string) ([]Repo, error) {
	if err := validOrg(org); err != nil {
		return nil, err
	}
	if c.actor == "" && !c.machine {
		return nil, ErrNoActor
	}
	return c.listRepos(ctx, org)
}

// listRepos is the uncached walk of the org's repository pages.
//
// It fetches SMALL PAGES CONCURRENTLY, which is the whole reason a board that
// used to take twenty seconds takes three. See [repoPage] for the measurement
// and the cost model behind it.
//
// The shape is: one page to learn the size of the list, then the rest at once.
// X-Total-Count is what makes that possible — without it the only way to find
// the end of a list is to keep asking until a page comes back short, and that is
// inherently serial.
func (c *Client) listRepos(ctx context.Context, org string) ([]Repo, error) {
	path := "/orgs/" + url.PathEscape(org) + "/repos"
	fetch := func(ctx context.Context, p int) ([]Repo, http.Header, error) {
		var batch []Repo
		q := url.Values{"limit": {strconv.Itoa(repoPage)}, "page": {strconv.Itoa(p)}}
		h, err := c.get(ctx, path, q, &batch)
		return batch, h, err
	}

	first, hdr, err := fetch(ctx, 1)
	if err != nil {
		return nil, err
	}
	total, err := strconv.Atoi(strings.TrimSpace(hdr.Get("X-Total-Count")))
	if err != nil || total <= len(first) {
		// Either the whole list arrived, or this forge does not count its lists.
		// Nothing more to fetch in the first case; in the second, fall through to
		// the serial walk, which is SLOWER BUT CORRECT — a degradation, not a
		// second design.
		if err == nil {
			return first, nil
		}
		return c.walkRepos(ctx, path, first)
	}

	pages := (total + repoPage - 1) / repoPage
	if pages > maxRepoPages {
		pages = maxRepoPages
	}
	// Page 1 is already in hand; the rest go out together.
	out := make([][]Repo, pages)
	out[0] = first
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		bad  error
		sem  = make(chan struct{}, fanout)
		once sync.Once
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for p := 2; p <= pages; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			batch, _, err := fetch(ctx, p)
			if err != nil {
				// One page's failure fails the walk. A repository list silently
				// missing the pages that errored is a wrong answer presented as a
				// complete one, and downstream it reads as "those boards do not
				// exist" — the same rule the milestone rollup follows.
				once.Do(func() {
					mu.Lock()
					bad = err
					mu.Unlock()
					cancel()
				})
				return
			}
			mu.Lock()
			out[p-1] = batch
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	if bad != nil {
		return nil, bad
	}
	// Concatenated in PAGE ORDER, not completion order, so the list a caller
	// sees does not reshuffle between two identical reads.
	all := make([]Repo, 0, total)
	for _, b := range out {
		all = append(all, b...)
	}
	return all, nil
}

// walkRepos finishes the list one page at a time, for a forge that does not send
// X-Total-Count. Correct and slow: it is what the concurrent walk above replaced,
// kept only for the case that makes the fast path impossible.
func (c *Client) walkRepos(ctx context.Context, path string, first []Repo) ([]Repo, error) {
	all := first
	for p := 2; p <= maxRepoPages; p++ {
		var batch []Repo
		q := url.Values{"limit": {strconv.Itoa(repoPage)}, "page": {strconv.Itoa(p)}}
		if err := c.do(ctx, path, q, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < repoPage {
			break
		}
	}
	return all, nil
}

// Repo reads ONE repository by name.
//
// The board-detail page needs a single repository, and finding it by listing the
// org's inventory is what made that page cost twenty seconds: the list endpoint
// charges per repository RETURNED (see [repoPage]), so 249 of the 250 it returns
// are waste. A direct read is ~1s no matter how large the org is.
//
// A repository the actor cannot see is 404 and surfaces as [ErrUnknownActor],
// the same as one that does not exist — which is the right answer for a named
// board, and does not tell a caller whether a private repo is there.
func (c *Client) Repo(ctx context.Context, org, name string) (Repo, error) {
	if err := validOrg(org); err != nil {
		return Repo{}, err
	}
	if err := validOrg(name); err != nil {
		return Repo{}, fmt.Errorf("forge: repo: %w", err)
	}
	var r Repo
	err := c.do(ctx, "/repos/"+url.PathEscape(org)+"/"+url.PathEscape(name), nil, &r)
	return r, err
}

// ReposWarm returns the repository inventory ONLY if it is already held, and
// starts filling it behind the caller when it is not.
//
// It exists so a caller that can do without the inventory never waits for it.
// The board list is assembled from issues-search (~1.5s for this forge's largest
// org, where the inventory is ~100s); the inventory, when warm, only adds the
// boards that have no work on them yet. Blocking on it would trade a complete
// answer for one nobody stays to see.
func (c *Client) ReposWarm(ctx context.Context, org string) ([]Repo, bool) {
	if validOrg(org) != nil || c.actor == "" {
		return nil, false
	}
	got, ok := c.repos.warm(ctx, c.key(org), func(ctx context.Context) ([]Repo, error) {
		return c.listRepos(ctx, org)
	})
	if !ok {
		return nil, false
	}
	return append([]Repo(nil), got...), true
}

// IssueFilter narrows an issue search. Every field is OPTIONAL and none of them
// carries tenancy: the org is a separate, non-optional argument to [Client.Issues]
// precisely so it can never arrive as part of a caller-supplied filter.
type IssueFilter struct {
	// State is "open", "closed" or "all". Empty means the forge's default (open).
	State string
	// Labels selects issues carrying ALL of these labels.
	Labels []string
	// Milestone selects issues in a milestone, by title.
	Milestone string
	// Type is "issues" or "pulls"; empty returns both.
	Type string
	// Limit caps the rows returned. Zero means every page up to [maxPages].
	Limit int
}

// Issues searches every repo of `org` the actor can see, in one call per page.
//
// The forge's /repos/issues/search answers labels, milestone, assignees and the
// owning repository inline, which is what lets the board render columns without
// an N+1 walk: a column is a label, a card is one of these rows, and moving a
// card is a relabel of the same row.
func (c *Client) Issues(ctx context.Context, org string, f IssueFilter) ([]Issue, error) {
	if err := validOrg(org); err != nil {
		return nil, err
	}
	var all []Issue
	for p := 1; p <= maxPages; p++ {
		lim := page
		if f.Limit > 0 && f.Limit-len(all) < lim {
			lim = f.Limit - len(all)
		}
		if lim <= 0 {
			break
		}
		q := url.Values{
			// owner is the TENANCY of this call. It is set from the org argument,
			// which every caller resolves from a validated principal — never from a
			// filter field, and never from a request body.
			"owner": {org},
			"limit": {strconv.Itoa(lim)},
			"page":  {strconv.Itoa(p)},
		}
		if f.State != "" {
			q.Set("state", f.State)
		}
		if len(f.Labels) > 0 {
			q.Set("labels", strings.Join(f.Labels, ","))
		}
		if f.Milestone != "" {
			q.Set("milestones", f.Milestone)
		}
		if f.Type != "" {
			q.Set("type", f.Type)
		}
		var batch []Issue
		if err := c.do(ctx, "/repos/issues/search", q, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < lim {
			break
		}
		if f.Limit > 0 && len(all) >= f.Limit {
			break
		}
	}
	return all, nil
}

// Milestones is the ORG ROLLUP the forge does not offer.
//
// Forgejo scopes milestones to a repository and publishes no org-level list, so
// the rollup is a fan-out: list the org's repos, ask each for its milestones,
// merge. It runs HERE, server-side, rather than in the browser, for three
// reasons — a client-side fan-out would issue N cross-origin requests per board
// load, would need the forge reachable from the browser (and therefore a
// browser-held credential, which is the thing this design refuses), and would
// make the actor's visibility a client-side filter instead of a server-side ACL.
//
// Concurrency is bounded by [fanout], and one repo's failure fails the rollup:
// a milestone list silently missing the repos that errored is a wrong answer
// presented as a complete one.
//
// The whole rollup is cached like [Client.Repos] and on the same key, because
// it is the same shape of expense: its opening move IS that call, and the
// fan-out behind it is another N requests. Cold, the two together are the
// slowest read this package makes.
func (c *Client) Milestones(ctx context.Context, org string) ([]Milestone, error) {
	if err := validOrg(org); err != nil {
		return nil, err
	}
	if c.actor == "" {
		return nil, ErrNoActor
	}
	got, err := c.rollup.do(ctx, c.key(org), func(ctx context.Context) ([]Milestone, error) {
		return c.rollupMilestones(ctx, org)
	})
	if err != nil {
		return nil, err
	}
	return append([]Milestone(nil), got...), nil
}

// rollupMilestones is the uncached fan-out.
func (c *Client) rollupMilestones(ctx context.Context, org string) ([]Milestone, error) {
	repos, err := c.Repos(ctx, org)
	if err != nil {
		return nil, err
	}
	live := 0
	for _, r := range repos {
		if !r.Archived {
			live++
		}
	}
	// REFUSE rather than truncate. An org past this cannot be rolled up inside
	// any sane request budget, and returning the first [maxRollup] repos'
	// milestones would be a partial answer presented as a complete one — the
	// same wrong answer the fail-on-first-error rule above exists to prevent,
	// arrived at by a different route. The message names the cap so the operator
	// reading it knows what to change.
	if live > maxRollup {
		return nil, fmt.Errorf("forge: org %s has %d live repositories, past the %d this rollup fans out over", org, live, maxRollup)
	}
	// Cancel the remaining fan-out as soon as one leg fails; without this a large
	// org keeps issuing requests whose result is already discarded.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu   sync.Mutex
		out  []Milestone
		bad  error
		sem  = make(chan struct{}, fanout)
		wg   sync.WaitGroup
		once sync.Once
	)
	for _, r := range repos {
		if r.Archived {
			continue // an archived repo's milestones are not live work
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			var ms []Milestone
			q := url.Values{"state": {"all"}, "limit": {strconv.Itoa(page)}}
			path := "/repos/" + url.PathEscape(org) + "/" + url.PathEscape(name) + "/milestones"
			if err := c.do(ctx, path, q, &ms); err != nil {
				once.Do(func() {
					mu.Lock()
					bad = fmt.Errorf("milestones %s/%s: %w", org, name, err)
					mu.Unlock()
					cancel()
				})
				return
			}
			mu.Lock()
			for i := range ms {
				ms[i].Repo = name
				out = append(out, ms[i])
			}
			mu.Unlock()
		}(r.Name)
	}
	wg.Wait()
	if bad != nil {
		return nil, bad
	}
	return out, nil
}

// ── the writes ───────────────────────────────────────────────────────────────
//
// Every write is made under the caller's Sudo actor, so the forge records the
// HUMAN as the author of the issue and of each label change — not a shared bot.
// That is the audit property a machine identity would destroy, and it is the
// reason the write path is worth building on Sudo rather than on a second
// per-user credential.

// send issues one authenticated, sudoed request carrying a JSON body.
type sendOpts struct {
	method string
	path   string
	body   any
	out    any
}

func (c *Client) send(ctx context.Context, o sendOpts) error {
	if c.actor == "" && !c.machine {
		return ErrNoActor
	}
	if c.token == "" {
		return ErrNoToken
	}
	var rdr io.Reader
	if o.body != nil {
		b, err := json.Marshal(o.body)
		if err != nil {
			return fmt.Errorf("forge: encode body: %w", err)
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, o.method, c.base+o.path, rdr)
	if err != nil {
		return fmt.Errorf("forge: build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+c.token)
	if !c.machine {
		req.Header.Set("Sudo", c.actor)
	}
	req.Header.Set("Accept", "application/json")
	if o.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("forge: %s %s: %w", o.method, o.path, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		if c.machine {
			return fmt.Errorf("%w: %s", ErrNotFound, o.path)
		}
		return fmt.Errorf("%w: %s", ErrUnknownActor, c.actor)
	case resp.StatusCode == http.StatusConflict:
		// Named rather than lumped into the generic failure below, because the
		// create paths treat it as the state they wanted: a repository that is
		// already there does not have to be made again.
		return fmt.Errorf("%w: %s", ErrExists, o.path)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		// A sudoed write the ACTOR may not make lands here. It is the forge
		// enforcing its own ACL on the human, which is the point of Sudo.
		return fmt.Errorf("forge: %s %s: refused (%d)", o.method, o.path, resp.StatusCode)
	case resp.StatusCode >= 300:
		return fmt.Errorf("forge: %s %s: unexpected status %d", o.method, o.path, resp.StatusCode)
	}
	if o.out == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("forge: read %s: %w", o.path, err)
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, o.out); err != nil {
		return fmt.Errorf("forge: decode %s: %w", o.path, err)
	}
	return nil
}

// NewIssue is the issue to open. Labels carry the board column, so a card
// created into a column is one call.
type NewIssue struct {
	Title  string   `json:"title"`
	Body   string   `json:"body,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// CreateIssue opens an issue on org/repo as the actor.
func (c *Client) CreateIssue(ctx context.Context, org, repo string, n NewIssue) (Issue, error) {
	if err := validOrg(org); err != nil {
		return Issue{}, err
	}
	if err := validOrg(repo); err != nil {
		return Issue{}, fmt.Errorf("forge: repo: %w", err)
	}
	if strings.TrimSpace(n.Title) == "" {
		return Issue{}, errors.New("forge: empty title")
	}
	var out Issue
	err := c.send(ctx, sendOpts{
		method: http.MethodPost,
		path:   "/repos/" + url.PathEscape(org) + "/" + url.PathEscape(repo) + "/issues",
		body:   n, out: &out,
	})
	return out, err
}

// IssuePatch changes an issue. A nil field is left alone — the forge treats an
// absent key as "unchanged", so this must not marshal zero values.
// Labels are NOT here: they move through [Client.SetLabels], which REPLACES the
// set, because the board's column is a label and "move this card" must be one
// unambiguous operation rather than a field on a general-purpose patch.
type IssuePatch struct {
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
	State *string `json:"state,omitempty"`
}

// PatchIssue edits an issue's fields as the actor.
func (c *Client) PatchIssue(ctx context.Context, org, repo string, number int64, p IssuePatch) error {
	if err := validOrg(org); err != nil {
		return err
	}
	if err := validOrg(repo); err != nil {
		return fmt.Errorf("forge: repo: %w", err)
	}
	return c.send(ctx, sendOpts{
		method: http.MethodPatch,
		path:   fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(org), url.PathEscape(repo), number),
		body:   p,
	})
}

// SetLabels REPLACES an issue's label set, which is how a card moves between
// columns: the column is a label, so moving it is a relabel and not an update to
// a status column that a forge-side change could contradict.
func (c *Client) SetLabels(ctx context.Context, org, repo string, number int64, labels []string) error {
	if err := validOrg(org); err != nil {
		return err
	}
	if err := validOrg(repo); err != nil {
		return fmt.Errorf("forge: repo: %w", err)
	}
	if labels == nil {
		labels = []string{}
	}
	return c.send(ctx, sendOpts{
		method: http.MethodPut,
		path:   fmt.Sprintf("/repos/%s/%s/issues/%d/labels", url.PathEscape(org), url.PathEscape(repo), number),
		body:   map[string]any{"labels": labels},
	})
}

// isMissing reports whether err is the forge saying "there is no such thing",
// in either of the two spellings a 404 can arrive in — as plain absence on a
// machine call, or as an unreadable/unknown actor on a sudoed one.
//
// It exists so that the callers for which absence IS the wanted state (a
// withdrawal, a create-if-absent) can say so once instead of each remembering
// that the same status has two names.
func isMissing(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnknownActor)
}

// validOrg refuses an org that is empty or not a forge path segment.
//
// The org reaches this package from a validated principal, so a bad value is a
// bug rather than an attack — but it is interpolated into a URL PATH, and a
// value bearing "/" or ".." would address a different endpoint than the one the
// call site wrote. Refusing here means no call site can be the place that
// forgot.
func validOrg(org string) error {
	if strings.TrimSpace(org) == "" {
		return errors.New("forge: empty org")
	}
	if org != strings.TrimSpace(org) {
		return fmt.Errorf("forge: org %q has surrounding space", org)
	}
	if strings.ContainsAny(org, "/\\?#%") || strings.Contains(org, "..") {
		return fmt.Errorf("forge: org %q is not a path segment", org)
	}
	return nil
}
