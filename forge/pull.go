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
	"strings"
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
	if err := validOrg(owner); err != nil {
		return "", err
	}
	if err := validOrg(repo); err != nil {
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

// openPull finds the open pull request whose head is this branch, empty if there
// is none. It exists only to make a re-proposal idempotent, so a failure to find
// one is not itself an error worth surfacing — the caller reports the original
// refusal instead.
func (c *Client) openPull(ctx context.Context, owner, repo, head string) (string, error) {
	var pulls []struct {
		URL  string `json:"html_url"`
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	q := url.Values{"state": {"open"}, "limit": {"50"}}
	if err := c.do(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls",
		q, &pulls); err != nil {
		return "", err
	}
	for _, p := range pulls {
		if p.Head.Ref == head {
			return p.URL, nil
		}
	}
	return "", nil
}
