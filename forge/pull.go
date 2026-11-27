package forge

// pull.go offers a run's branch for merging, and answers where a person reads
// it.
//
// It replaces a two-backend choice, and that is the point rather than a side
// effect. The proposal used to be either a real pull request on GitHub (for a
// repository that mirrored there) or a link to a branch-browsing page (for one
// that did not) — two answers to one question, chosen by a mirror row, and the
// second was not a proposal at all: nobody can approve a page. The forge has
// native pull requests, so every repository gets the same thing and the branch
// page has nothing left to work around.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Propose opens a pull request for head into base and returns its address.
//
// It is made under the client's ACTOR, so the forge records the human who asked
// for the run as the author of the proposal — not a shared bot. That is the
// audit property worth having: a reviewer can see who wanted this.
//
// An empty base means the repository's default branch, resolved from the forge
// rather than assumed, because "main" is a convention and this has to be right
// for the repository that kept "master" or moved to "trunk".
//
// A head that already has an open pull request answers with THAT one instead of
// failing. A run retried after a crash pushes the same branch again, and a
// re-proposal is the same proposal — reporting the forge's 422 to a person who
// asked for a link would be technically true and useless.
func (c *Client) Propose(ctx context.Context, owner, repo, base, head, title, body string) (string, error) {
	if err := validSegment(owner); err != nil {
		return "", err
	}
	if err := validSegment(repo); err != nil {
		return "", fmt.Errorf("forge: repo: %w", err)
	}
	head = strings.TrimSpace(head)
	if head == "" {
		return "", fmt.Errorf("forge: a proposal needs a head branch")
	}
	if strings.TrimSpace(base) == "" {
		r, err := c.Repo(ctx, owner, repo)
		if err != nil {
			return "", err
		}
		base = r.Branch
		if base == "" {
			return "", fmt.Errorf("forge: %s/%s names no default branch", owner, repo)
		}
	}
	if strings.TrimSpace(title) == "" {
		title = head
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/pulls"

	var made struct {
		URL string `json:"html_url"`
	}
	err := c.send(ctx, sendOpts{
		method: http.MethodPost, path: path,
		body: map[string]any{"head": head, "base": base, "title": title, "body": body},
		out:  &made,
	})
	if err == nil {
		return made.URL, nil
	}
	if open, ferr := c.openPull(ctx, owner, repo, head); ferr == nil && open != "" {
		return open, nil
	}
	return "", fmt.Errorf("forge: propose %s into %s on %s/%s: %w", head, base, owner, repo, err)
}

// openPull finds the open pull request whose head is this branch OF THIS
// REPOSITORY, empty if there is none. It exists only to make a re-proposal
// idempotent, so a failure to find one is not itself an error worth surfacing —
// the caller reports the original refusal instead.
//
// The repository is half the head. A branch name is anybody's to choose, so a
// fork's pull request from a branch of the same name is somebody else's proposal,
// and answering it would hand a run's person a stranger's link as their own.
func (c *Client) openPull(ctx context.Context, owner, repo, head string) (string, error) {
	var pulls []pullJSON
	q := url.Values{"state": {"open"}, "limit": {"50"}}
	if err := c.do(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls",
		q, &pulls); err != nil {
		return "", err
	}
	for _, p := range pulls {
		if p.pull().From(owner, repo, head) {
			return p.URL, nil
		}
	}
	return "", nil
}

// Pull is a pull request as a reader of the work it proposes is shown it.
type Pull struct {
	Number int64
	URL    string
	Title  string
	// State is open, closed or merged. The forge says closed for a merged pull
	// request and merged beside it; a reader wants the one word.
	State string
	// Mergeable is the forge's own verdict that base takes head without conflict.
	Mergeable bool
	// Head is the branch it proposes, and HeadRepo the repository that branch is
	// in, owner/name — another repository's when the pull request comes from a
	// fork, and empty when the forge no longer has it.
	Head, HeadRepo string
	// Tip is the commit Head pointed at when the forge answered, and Base the
	// branch it proposes into.
	Tip, Base string
}

// From reports whether this pull request proposes head of owner/repo itself —
// the branch AND the repository, because a fork may name a branch anything,
// including a run's.
func (p Pull) From(owner, repo, head string) bool {
	return p.Head == head && strings.EqualFold(p.HeadRepo, owner+"/"+repo)
}

// Review is one review of a pull request.
type Review struct {
	// Author is the reviewer's forge login.
	Author string
	// State is the forge's verdict word, lower-cased: approved, request_changes,
	// comment, pending or request_review.
	State string
	Body  string
	// Truncated marks a Body cut at [maxReview], or left empty past [maxReviews].
	Truncated bool
	// At is when the review was submitted; zero for one that has not been.
	At time.Time
}

const (
	// maxReview is the most of one review's body kept, cut at a whole character.
	maxReview = 16 << 10
	// maxReviews is the most review text kept across one pull request's reviews.
	// A review past it keeps its author, verdict and time, and no body.
	maxReviews = 256 << 10
)

// pullJSON is a pull request as the forge sends it, the fields read of it.
type pullJSON struct {
	Number    int64  `json:"number"`
	URL       string `json:"html_url"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Merged    bool   `json:"merged"`
	Mergeable bool   `json:"mergeable"`
	Head      struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
		// Repo is null when the head's repository is gone, which names no
		// repository at all.
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p pullJSON) pull() Pull {
	state := p.State
	if p.Merged {
		state = "merged"
	}
	out := Pull{Number: p.Number, URL: p.URL, Title: p.Title, State: state, Mergeable: p.Mergeable,
		Head: p.Head.Ref, Tip: p.Head.SHA, Base: p.Base.Ref}
	if p.Head.Repo != nil {
		out.HeadRepo = p.Head.Repo.FullName
	}
	return out
}

// Pull reads one pull request by its number, as this client's actor.
func (c *Client) Pull(ctx context.Context, owner, repo string, number int64) (Pull, error) {
	path, err := pullPath(owner, repo, number)
	if err != nil {
		return Pull{}, err
	}
	var p pullJSON
	if err := c.do(ctx, path, nil, &p); err != nil {
		return Pull{}, err
	}
	return p.pull(), nil
}

// PullFor finds the pull request proposing head of owner/repo itself into base,
// in any state, as this client's actor; found is false when there is none.
//
// It reads the base's pull requests most recently touched first, one page, and
// matches the head there: a run's branch is its own and proposed once, so the one
// it has is among the latest into its base, and the forge's lookup by base and
// head takes the base as one path segment, which a slashed base is not. The head
// is matched with its repository (Pull.From), so a fork's pull request from a
// branch of the same name is never taken for the run's.
func (c *Client) PullFor(ctx context.Context, owner, repo, base, head string) (p Pull, found bool, err error) {
	if err := validSegment(owner); err != nil {
		return Pull{}, false, err
	}
	if err := validSegment(repo); err != nil {
		return Pull{}, false, fmt.Errorf("forge: repo: %w", err)
	}
	var pulls []pullJSON
	q := url.Values{"state": {"all"}, "base_branch": {base}, "sort": {"recentupdate"}, "limit": {strconv.Itoa(page)}}
	if err := c.do(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls", q, &pulls); err != nil {
		return Pull{}, false, err
	}
	for _, x := range pulls {
		if p := x.pull(); p.From(owner, repo, head) {
			return p, true, nil
		}
	}
	return Pull{}, false, nil
}

// Reviews is the first page of a pull request's reviews the actor may see,
// oldest first — the order the forge keeps them in — and whether there are more.
//
// It is ONE page because it is read for a person looking at a run, and a pull
// request can carry any number of reviews of any length: each body is kept to
// [maxReview] and all of them to [maxReviews], a cut one marked Truncated.
func (c *Client) Reviews(ctx context.Context, owner, repo string, number int64) (reviews []Review, more bool, err error) {
	path, err := pullPath(owner, repo, number)
	if err != nil {
		return nil, false, err
	}
	var batch []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		State     string    `json:"state"`
		Body      string    `json:"body"`
		Submitted time.Time `json:"submitted_at"`
	}
	q := url.Values{"limit": {strconv.Itoa(page)}, "page": {"1"}}
	h, err := c.get(ctx, path+"/reviews", q, &batch)
	if err != nil {
		return nil, false, err
	}
	// The forge's total when it says one; a full page when it does not, which is
	// the only other sign that a second one exists.
	more = len(batch) >= page
	if n, err := strconv.Atoi(h.Get("X-Total-Count")); err == nil {
		more = n > len(batch)
	}
	out, kept := make([]Review, 0, len(batch)), 0
	for _, r := range batch {
		body, cut := clip(r.Body, min(maxReview, maxReviews-kept))
		kept += len(body)
		out = append(out, Review{Author: r.User.Login, State: strings.ToLower(r.State), Body: body, Truncated: cut, At: r.Submitted})
	}
	return out, more, nil
}

// clip keeps at most n bytes of s, ending at a whole UTF-8 character, and reports
// whether anything was cut.
func clip(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	n = max(n, 0)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// Merge merges pull request number of owner/repo into its base, as this
// client's actor, and answers the pull request as the forge holds it afterwards.
//
// head is the commit the caller read the pull request at. The forge merges only
// while the branch still points there, so what was reviewed is what lands: a
// push after the read is refused as [ErrExists] rather than merged unseen.
//
// It never forces. A rule on the base the actor would have to override, a
// conflict, or a pull request that is not open is the forge's refusal
// ([ErrNotNow]), with its reason. The verdict is a READ-BACK: a 200 says the
// forge accepted the request, and only the pull request reading merged says it
// happened.
func (c *Client) Merge(ctx context.Context, owner, repo string, number int64, head, title string) (Pull, error) {
	path, err := pullPath(owner, repo, number)
	if err != nil {
		return Pull{}, err
	}
	if !hex(head) {
		return Pull{}, fmt.Errorf("forge: merge %s/%s#%d: %q is not a commit", owner, repo, number, head)
	}
	if err := c.send(ctx, sendOpts{
		method: http.MethodPost, path: path + "/merge",
		body: map[string]any{"Do": "merge", "MergeTitleField": title, "head_commit_id": head},
	}); err != nil {
		return Pull{}, fmt.Errorf("forge: merge %s/%s#%d: %w", owner, repo, number, err)
	}
	p, err := c.Pull(ctx, owner, repo, number)
	if err != nil {
		return Pull{}, fmt.Errorf("forge: confirm the merge of %s/%s#%d: %w", owner, repo, number, err)
	}
	if p.State != "merged" {
		return Pull{}, fmt.Errorf("forge: %s/%s#%d reads %s after a merge", owner, repo, number, p.State)
	}
	return p, nil
}

// Withdraw closes pull request number of owner/repo without merging it, as this
// client's actor.
func (c *Client) Withdraw(ctx context.Context, owner, repo string, number int64) error {
	path, err := pullPath(owner, repo, number)
	if err != nil {
		return err
	}
	return c.send(ctx, sendOpts{method: http.MethodPatch, path: path, body: map[string]any{"state": "closed"}})
}

// PullOf is the number of the pull request an address names on owner/repo of
// THIS forge, and false for any other address — another host's pull request, or
// another repository's, which a read of this one must not follow.
func (c *Client) PullOf(addr, owner, repo string) (int64, bool) {
	u, err := url.Parse(strings.TrimSpace(addr))
	if err != nil || !strings.EqualFold(u.Host, c.host) {
		return 0, false
	}
	rest, ok := strings.CutPrefix(strings.TrimSuffix(u.Path, "/"), "/"+owner+"/"+repo+"/pulls/")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// pullPath is the route of one pull request.
func pullPath(owner, repo string, number int64) (string, error) {
	if err := validSegment(owner); err != nil {
		return "", err
	}
	if err := validSegment(repo); err != nil {
		return "", fmt.Errorf("forge: repo: %w", err)
	}
	if number <= 0 {
		return "", fmt.Errorf("forge: pull: bad number %d", number)
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/pulls/" + strconv.FormatInt(number, 10), nil
}
