// Package forge is the client for the deployment's code forge — the Forgejo
// instance (git.hanzo.ai beside api.hanzo.ai) that holds the estate's issues and
// milestones.
//
// It exists because those work items have ONE home. The forge is where an issue
// is filed, labelled, assigned and closed; a second copy in another store would
// be a second answer to "what is the state of this work", and the two would
// drift. So nothing here caches, mirrors or writes through to a local table:
// every read is a read OF the forge, and every write is a write TO it.
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

// maxBody bounds a single response read. The forge is a trusted service, but
// "trusted" is a statement about intent and not about compromise, and an
// unbounded io.ReadAll on a remote body is an OOM one bad response away.
const maxBody = 32 << 20 // 32 MiB

// fanout bounds the concurrent per-repo requests an org rollup makes. The forge
// has no org-level milestones API, so a rollup is N repo calls; unbounded, a
// large org would open hundreds of sockets at once and the rollup would read as
// a denial-of-service against our own forge.
const fanout = 8

// Errors a caller must be able to tell apart. They are distinguished because the
// right answer differs: no actor is a bug in the CALLER (it forgot to scope),
// an unknown actor is a fact about the USER (no forge identity), and neither is
// "the board is empty".
var (
	// ErrNoActor is returned by every call on a client with no Sudo actor. It is
	// a refusal, never a fallback to the machine identity.
	ErrNoActor = errors.New("forge: no actor — a user-facing call must be scoped with As()")

	// ErrUnknownActor means the forge does not know the actor we sudoed as.
	ErrUnknownActor = errors.New("forge: unknown actor — no forge identity for this user")

	// ErrNoToken means the machine credential is absent or empty.
	ErrNoToken = errors.New("forge: no service token")
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
	token string // machine credential from KMS — NEVER logged
	actor string // Forgejo Sudo login; empty ⇒ every call refuses
	http  *http.Client
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
		token: strings.TrimSpace(token),
		http:  &http.Client{Timeout: 30 * time.Second},
	}, nil
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
	cp.actor = strings.TrimSpace(login)
	return &cp
}

// Actor is the forge login this client acts as, empty if unscoped. For logs and
// errors — the actor is an identity, not a credential, and is safe to record.
func (c *Client) Actor() string { return c.actor }

// do issues one authenticated, sudoed GET and decodes JSON into out.
//
// It is the ONE place the credential is attached and the ONE place Sudo is
// enforced, so neither can be forgotten by a call site: every method below is
// written in terms of this.
func (c *Client) do(ctx context.Context, path string, q url.Values, out any) error {
	if c.actor == "" {
		return ErrNoActor
	}
	if c.token == "" {
		return ErrNoToken
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("forge: build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+c.token)
	// Sudo as a HEADER, never as a ?sudo= query parameter. Both work, but a query
	// parameter lands in access logs and proxy traces, so the actor of every
	// request would be written into logs the forge and every hop keep. The header
	// form keeps attribution out of URL telemetry.
	req.Header.Set("Sudo", c.actor)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// The URL is safe to surface (it names a path, not a secret) but the error
		// from the transport can embed the request URL only — never a header — so
		// the token cannot ride out in an error string.
		return fmt.Errorf("forge: GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// With Sudo set, the forge answers 404 both for "the actor does not exist"
		// and for "this actor cannot see that". Neither is an error the caller can
		// fix by retrying, and both must read as "no access", never as an empty
		// success — a 404 rendered as an empty list is how a board silently lies.
		return fmt.Errorf("%w: %s", ErrUnknownActor, c.actor)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("forge: %s: credential rejected (%d)", path, resp.StatusCode)
	default:
		return fmt.Errorf("forge: %s: unexpected status %d", path, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("forge: read %s: %w", path, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("forge: decode %s: %w", path, err)
	}
	return nil
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
	Open     int    `json:"open_issues_count"`
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
func (c *Client) Repos(ctx context.Context, org string) ([]Repo, error) {
	if err := validOrg(org); err != nil {
		return nil, err
	}
	var all []Repo
	for p := 1; p <= maxPages; p++ {
		var batch []Repo
		q := url.Values{"limit": {strconv.Itoa(page)}, "page": {strconv.Itoa(p)}}
		if err := c.do(ctx, "/orgs/"+url.PathEscape(org)+"/repos", q, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < page {
			break
		}
	}
	return all, nil
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
func (c *Client) Milestones(ctx context.Context, org string) ([]Milestone, error) {
	repos, err := c.Repos(ctx, org)
	if err != nil {
		return nil, err
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
	if c.actor == "" {
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
	req.Header.Set("Sudo", c.actor)
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
		return fmt.Errorf("%w: %s", ErrUnknownActor, c.actor)
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
