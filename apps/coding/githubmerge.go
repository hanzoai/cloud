package coding

// githubmerge.go — a run's pull request on GitHub, read and merged there.
//
// A repository whose home is GitHub has its pull requests on GitHub, and the
// forge, which mirrors only its code, cannot read them. Pull already opened one
// there and answered the address it saved; Merge and the pull request a change
// answers read through the forge alone, so Merge said "no pull request yet" of
// hanzoai/social#16 and the change showed it with no title, no state and not
// mergeable. These read and merge it where it is.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// ghPull is one pull request as GitHub states it.
type ghPull struct {
	Number    int64  `json:"number"`
	URL       string `json:"html_url"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Merged    bool   `json:"merged"`
	Draft     bool   `json:"draft"`
	Mergeable *bool  `json:"mergeable"`
	NodeID    string `json:"node_id"`
	Head      struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// state is the pull request's state in the words the forge's answers use.
func (p ghPull) state() string {
	switch {
	case p.Merged:
		return "merged"
	case p.State == "closed":
		return "closed"
	}
	return "open"
}

// from reports whether the pull request proposes branch of this repository, and
// not a branch of the same name in a fork.
func (p ghPull) from(owner, name, branch string) bool {
	return p.Head.Ref == branch && strings.EqualFold(p.Head.Repo.FullName, owner+"/"+name)
}

// errGitHub is a refusal GitHub stated, in its own words.
var errGitHub = errors.New("github")

// pullNumbered is pull request n, and false when there is none.
func (h *hub) pullNumbered(ctx context.Context, n int64) (ghPull, bool, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	var p ghPull
	st, err := h.call(ctx, http.MethodGet, h.path(fmt.Sprintf("pulls/%d", n)), nil, &p)
	switch {
	case err != nil:
		return ghPull{}, false, fmt.Errorf("github: read pull request %d on %s: %w", n, h, err)
	case st == http.StatusNotFound:
		return ghPull{}, false, nil
	case st != http.StatusOK:
		return ghPull{}, false, fmt.Errorf("github: read pull request %d on %s: %d", n, h, st)
	}
	return p, true, nil
}

// pullFor is the open pull request proposing branch, and false when there is none.
func (h *hub) pullFor(ctx context.Context, branch string) (ghPull, bool, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	var open []ghPull
	q := "?state=open&head=" + url.QueryEscape(h.owner+":"+branch)
	st, err := h.call(ctx, http.MethodGet, h.path("pulls")+q, nil, &open)
	if err != nil || st != http.StatusOK {
		return ghPull{}, false, fmt.Errorf("github: find the pull request for %s on %s: %d %v", branch, h, st, err)
	}
	if len(open) == 0 {
		return ghPull{}, false, nil
	}
	return h.pullNumbered(ctx, open[0].Number)
}

// ready marks a draft pull request ready for review: asked to merge, a draft is
// what its person means to merge. GitHub's REST API has no route for it, so it is
// the one GraphQL mutation that does.
func (h *hub) ready(ctx context.Context, node string) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	q := `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } } }`
	st, err := h.call(ctx, http.MethodPost, "/graphql", map[string]any{"query": q, "variables": map[string]string{"id": node}}, &out)
	switch {
	case err != nil:
		return fmt.Errorf("github: mark ready on %s: %w", h, err)
	case st != http.StatusOK:
		return fmt.Errorf("%w: mark ready on %s: %d", errGitHub, h, st)
	case len(out.Errors) > 0:
		return fmt.Errorf("%w: %s", errGitHub, out.Errors[0].Message)
	}
	return nil
}

// mergeNumbered merges pull request n at the head GitHub said it holds, so a push
// after that read is refused rather than merged unseen.
func (h *hub) mergeNumbered(ctx context.Context, n int64, sha, title string) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	var out struct {
		Merged  bool   `json:"merged"`
		Message string `json:"message"`
	}
	st, err := h.call(ctx, http.MethodPut, h.path(fmt.Sprintf("pulls/%d/merge", n)), map[string]any{"sha": sha, "commit_title": title}, &out)
	switch {
	case err != nil:
		return fmt.Errorf("github: merge pull request %d on %s: %w", n, h, err)
	case st == http.StatusOK && out.Merged:
		return nil
	case st == http.StatusConflict:
		return zip.ErrConflict("coding: the run's branch moved since it was read; read it again before merging")
	case st == http.StatusMethodNotAllowed || st == http.StatusUnprocessableEntity:
		return zip.ErrConflict("coding: GitHub would not merge it: " + cmp.Or(strings.TrimSpace(out.Message), "not mergeable"))
	}
	return fmt.Errorf("github: merge pull request %d on %s: %d %s", n, h, st, out.Message)
}

// hubPull is the run's pull request on GitHub: the one its session recorded, else
// the open one proposing its branch. False when it has none.
func (r reading) hubPull(ctx context.Context, h *hub) (ghPull, bool, error) {
	if p := recorded(r.pr); p != nil && p.Number > 0 {
		got, found, err := h.pullNumbered(ctx, p.Number)
		if err != nil || !found {
			return ghPull{}, false, err
		}
		return got, got.from(h.owner, h.name, r.branch), nil
	}
	return h.pullFor(ctx, r.branch)
}

// onHub admits a read or a merge of the run's GitHub home as its caller, and gives
// the token back after. ok is false when the repository's home is not GitHub.
func (r reading) onHub(ctx context.Context, read bool, use func(*hub) error) (ok bool, err error) {
	o, n, home := r.repo.Home()
	if !home {
		return false, nil
	}
	h, _, _, err := admit(ctx, o, n, read)
	if err != nil {
		err = notAdmitted(o+"/"+n, err)
		if errors.Is(err, ErrNotAdmitted) {
			return true, zip.ErrForbidden(err.Error())
		}
		return true, zip.Errorf(http.StatusBadGateway, "%s", err.Error())
	}
	defer func() { _ = h.revoke(context.WithoutCancel(ctx)) }()
	return true, use(h)
}

// pullOnHub is the run's pull request as GitHub states it, for a change's answer.
func (r reading) pullOnHub(ctx context.Context) (*client.CodingPull, bool, error) {
	var out *client.CodingPull
	ok, err := r.onHub(ctx, true, func(h *hub) error {
		p, found, err := r.hubPull(ctx, h)
		if err != nil || !found {
			return err
		}
		out = &client.CodingPull{Number: p.Number, URL: p.URL, Title: p.Title, State: p.state(),
			Mergeable: p.Mergeable != nil && *p.Mergeable, Reviews: []client.CodingReview{}}
		return nil
	})
	return out, ok, err
}

// mergeOnHub merges the run's pull request on GitHub, and answers it after.
func (r reading) mergeOnHub(ctx context.Context) (*client.CodingMerged, bool, error) {
	var out *client.CodingMerged
	ok, err := r.onHub(ctx, false, func(h *hub) error {
		m, err := r.mergeWith(ctx, h)
		out = m
		return err
	})
	return out, ok, err
}

// mergeWith merges the run's pull request through h: one already merged answers
// as it is, a closed one or one that conflicts is refused, and a draft is marked
// ready first, since asked to merge it is what its person means to merge.
func (r reading) mergeWith(ctx context.Context, h *hub) (*client.CodingMerged, error) {
	p, found, err := r.hubPull(ctx, h)
	switch {
	case err != nil:
		return nil, refusal(err, "the run's pull request")
	case !found:
		return nil, zip.ErrConflict("coding: that run has no pull request yet; POST /v1/agent/coding/{session}/pull opens one")
	}
	merged := &client.CodingMerged{Number: p.Number, URL: p.URL, State: "merged", Base: p.Base.Ref}
	switch p.state() {
	case "merged":
		return merged, nil
	case "closed":
		return nil, zip.ErrConflict("coding: the run's pull request was closed without merging")
	}
	if p.Mergeable != nil && !*p.Mergeable {
		return nil, zip.ErrConflict("coding: the run's branch conflicts with " + p.Base.Ref + "; ask a run to bring it up to date")
	}
	if p.Draft {
		if err := h.ready(ctx, p.NodeID); err != nil {
			return nil, zip.ErrConflict("coding: the pull request is a draft and could not be marked ready: " + err.Error())
		}
	}
	if err := h.mergeNumbered(ctx, p.Number, p.Head.SHA, p.Title); err != nil {
		return nil, err
	}
	return merged, nil
}
