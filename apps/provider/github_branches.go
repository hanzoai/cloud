package provider

// github_branches.go is the branches of one repository the caller may see: the
// default first, then the rest by name, narrowed by prefix on GitHub's side.
//
// The repository is looked up in the caller's own index (github_index.go), so a
// repository they cannot list is one they cannot read the branches of, and the
// two answer the same 404 — an address that exists and one that does not look
// alike. The read goes out with the credential that listed it: the person's own
// token, or the installation's for an org admin's view.

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// githubBranchesIn names the repository and narrows its branches.
type githubBranchesIn struct {
	// Owner is the GitHub account that holds the repository.
	Owner string `json:"owner"`
	// Repo is the repository's name within that account.
	Repo string `json:"repo"`
	// Q keeps branches whose name starts with it, matched by GitHub.
	Q string `json:"q"`
	// Limit is the page size: 1 to 100, and 50 when absent or unreadable.
	Limit int `json:"limit"`
	// After is the `next` of the previous page.
	After string `json:"after"`
}

// githubBranch is one branch.
type githubBranch struct {
	// Name is the branch's short name ("main", "feat/login").
	Name string `json:"name"`
	// Commit is the sha the branch points at.
	Commit string `json:"commit"`
	// Default is whether it is the repository's default branch, which always
	// leads the first page when it matches.
	Default bool `json:"default"`
}

// githubBranchesOut is one page of a repository's branches.
type githubBranchesOut struct {
	// Branches is this page: the default branch first, then the rest by name.
	// Never null.
	Branches []githubBranch `json:"branches"`
	// Next is the cursor for the page after this one; empty on the last page.
	Next string `json:"next"`
	// Total is how many branches match q, across every page.
	Total int `json:"total"`
}

// githubBranches lists a repository's branches, the default first, with prefix
// search. The repository must be one the caller may see — the same set
// GET /v1/provider/github/repos lists — or it is 404.
//
// Example: {"owner": "hanzoai", "repo": "cloud", "q": "feat/"}
// Response: {"branches":[{"name":"main","commit":"9f2c1e7a","default":true},{"name":"feat/login","commit":"4b8d0e3f","default":false}],"next":"","total":2}
func (o ops) githubBranches(ctx context.Context, in *githubBranchesIn) (*githubBranchesOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	owner, name := strings.TrimSpace(in.Owner), strings.TrimSpace(in.Repo)
	if !ghName.MatchString(owner) || !ghName.MatchString(name) {
		return nil, zip.ErrBadRequest("owner and repo must be GitHub names")
	}
	q := strings.TrimSpace(in.Q)
	if len(q) > 255 || strings.Contains(q, "..") {
		return nil, zip.ErrBadRequest("q is not a branch prefix")
	}
	r, found, personal, err := lookup(ctx, o.s, org, owner, name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, zip.ErrNotFound("repository not found")
	}
	tok, err := readToken(ctx, o, org, r, personal)
	if err != nil {
		return nil, err
	}
	refs, err := matchingHeads(ctx, tok, r.FullName, q)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return nil, zip.ErrNotFound("repository not found")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "list branches: %v", err)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultPage
	}
	limit = min(limit, maxPage)
	after := ""
	if a := strings.TrimSpace(in.After); a != "" {
		raw, err := base64.RawURLEncoding.DecodeString(a)
		if err != nil || len(raw) == 0 {
			return nil, zip.ErrBadRequest("after is not a cursor this listing issued")
		}
		after = string(raw)
	}
	all := orderBranches(refs, r.DefaultBranch)
	out := &githubBranchesOut{Branches: []githubBranch{}, Total: len(all)}
	start := 0
	if after != "" {
		// The default leads page one and no other; every later page resumes in
		// the sorted rest, after the name the last page ended on — or at its
		// head, when the last page ended on the default itself.
		start = len(all)
		for i, b := range all {
			if !b.Default && (after == afterDefault || b.Name > after) {
				start = i
				break
			}
		}
	}
	end := min(start+limit, len(all))
	out.Branches = append(out.Branches, all[start:end]...)
	if end < len(all) {
		last := all[end-1].Name
		if all[end-1].Default {
			last = afterDefault
		}
		out.Next = base64.RawURLEncoding.EncodeToString([]byte(last))
	}
	return out, nil
}

// afterDefault is the cursor of a page that ended on the default branch. No
// branch can be named it: git refuses a NUL in a ref.
const afterDefault = "\x00"

// orderBranches is the default branch first, then the rest by name.
func orderBranches(refs []githubRef, def string) []githubBranch {
	out := make([]githubBranch, 0, len(refs))
	for _, r := range refs {
		name, ok := strings.CutPrefix(r.Ref, "refs/heads/")
		if !ok || name == "" {
			continue
		}
		out = append(out, githubBranch{Name: name, Commit: r.Object.SHA, Default: name == def})
	}
	slices.SortFunc(out, func(a, b githubBranch) int {
		switch {
		case a.Default && !b.Default:
			return -1
		case b.Default && !a.Default:
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// githubRef is one row of GitHub's matching-refs answer.
type githubRef struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

// matchingHeads is every branch of fullName whose name starts with prefix, as
// GitHub matches it. Each segment of the prefix is escaped on its own, so a
// prefix is a ref and never a path.
func matchingHeads(ctx context.Context, tok, fullName, prefix string) ([]githubRef, error) {
	owner, name, _ := strings.Cut(fullName, "/")
	ref := "heads"
	if prefix != "" {
		ref = "heads/" + segments(prefix)
	}
	refs, _, err := read(ctx, func(ctx context.Context, _ int) ([]githubRef, int, error) {
		var refs []githubRef
		err := githubGet(ctx, tok, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/git/matching-refs/"+ref, &refs)
		return refs, 0, err
	}, 1)
	return refs, err
}

// segments escapes a ref one path segment at a time, keeping its slashes.
func segments(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// readToken is the credential a read of r goes out with: the caller's own token
// when the view it came from is their own, else the installation's for an org
// admin's.
func readToken(ctx context.Context, o ops, org string, r githubRepo, personal bool) (string, error) {
	if personal {
		tok, _, ok, err := personToken(ctx, o.s, org, factsOf(ctx).user)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", zip.ErrNotFound("repository not found")
		}
		return tok, nil
	}
	owner, _, _ := splitFullName(r.FullName)
	return githubTokenFor(ctx, org, owner)
}
