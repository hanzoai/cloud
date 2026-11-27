package provider

// github_ref.go answers whether a ref of a GitHub repository is one its
// maintainers publish from.
//
// A build that is handed a credential runs the Dockerfile at the ref it was
// asked for, so the ref decides whose code holds the credential. The default
// branch, a protected branch and a tag are written by the repository's own
// maintainers; a pull request's ref, an unprotected branch and a commit only a
// fork holds are written by whoever opened them. The answer is GitHub's, read
// with the App's own installation on the repository, so it is the same for
// every caller and nothing a caller says moves it.

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// maxProtected bounds how many protected branches a commit is looked for on. A
// repository publishes from a handful; the bound keeps one question from
// becoming a hundred GitHub calls.
const maxProtected = 10

// fullSHA is a whole commit id. An abbreviated one names nothing GitHub can
// compare, and a build refuses it before asking.
var fullSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// refName is a ref as a build takes it: git's own shape, alnum-led so it can
// never read as a flag, with no "..".
var refName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

// exposeRef publishes the ref check on the internal plane. Mount calls it.
func exposeRef() {
	cloud.Plane().Post("/integrations/github/ref", planeGitHubRef,
		zip.WithOperationID(client.IntegrationsGitHubRef),
		zip.WithSummary("Whether a ref of a GitHub repository is one it publishes from"))
}

// planeGitHubRef answers whether ref is the repository's default branch, a
// protected branch, a tag, or a full commit one of those branches contains. It
// reads with the App's installation on the repository, never the caller's.
func planeGitHubRef(ctx context.Context, in *client.GitHubRefIn) (*client.GitHubRef, error) {
	if cloud.Who(ctx).Org == "" {
		return nil, zip.ErrForbidden("github ref: no org on the call")
	}
	if in == nil || !ghName.MatchString(in.Owner) || !ghName.MatchString(in.Repo) || !refName.MatchString(in.Ref) || strings.Contains(in.Ref, "..") {
		return nil, zip.ErrBadRequest("github ref: owner, repo and a ref are required")
	}
	if !githubConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "github ref: github integration is not configured on this deployment")
	}
	id, err := ghApp.installationOf(ctx, in.Owner, in.Repo)
	if err != nil {
		return nil, zip.ErrForbidden("github ref: " + err.Error())
	}
	tok, err := ghApp.installationToken(ctx, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github ref: %v", err)
	}
	kind, err := trustedRef(ctx, tok, in.Owner, in.Repo, in.Ref)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github ref: %v", err)
	}
	return &client.GitHubRef{Trusted: kind != "", Kind: kind}, nil
}

// trustedRef names why ref is one owner/repo publishes from, or "" when it is
// not. An error is GitHub not answering, which is never read as trusted.
//
// A bare name is judged the way buildkit resolves it: as refs/<name> first,
// then as a branch, then as a tag. So a name under pull/ — which resolves to a
// pull request — is refused outright, and a name that is a branch is judged as
// that branch even when a tag carries the same name.
func trustedRef(ctx context.Context, tok, owner, repo, ref string) (string, error) {
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := githubGet(ctx, tok, base, &r); err != nil {
		return "", err
	}
	switch {
	case fullSHA.MatchString(ref):
		return contained(ctx, tok, base, r.DefaultBranch, ref)
	case strings.HasPrefix(ref, "refs/heads/"):
		return branchKind(ctx, tok, base, r.DefaultBranch, strings.TrimPrefix(ref, "refs/heads/"))
	case strings.HasPrefix(ref, "refs/tags/"):
		return tagKind(ctx, tok, base, strings.TrimPrefix(ref, "refs/tags/"))
	case strings.HasPrefix(ref, "refs/"):
		return "", nil // refs/pull/* and every ref outside heads and tags
	}
	switch first, _, _ := strings.Cut(ref, "/"); first {
	case "pull", "heads", "tags", "remotes":
		return "", nil // a bare name buildkit would resolve as refs/<name>
	}
	kind, found, err := branch(ctx, tok, base, r.DefaultBranch, ref)
	if err != nil || found {
		return kind, err
	}
	return tagKind(ctx, tok, base, ref)
}

// branchKind is "default" or "protected" for a branch, "" for any other.
func branchKind(ctx context.Context, tok, base, def, name string) (string, error) {
	kind, _, err := branch(ctx, tok, base, def, name)
	return kind, err
}

// branch reads one branch: found is whether it exists, and kind is "default"
// or "protected" when it is one of those.
func branch(ctx context.Context, tok, base, def, name string) (kind string, found bool, err error) {
	if name == def && def != "" {
		return "default", true, nil
	}
	var b struct {
		Protected bool `json:"protected"`
	}
	err = githubGet(ctx, tok, base+"/branches/"+segments(name), &b)
	switch {
	case statusOf(err) == http.StatusNotFound:
		return "", false, nil
	case err != nil:
		return "", false, err
	case b.Protected:
		return "protected", true, nil
	}
	return "", true, nil
}

// tagKind is "tag" when the repository carries that tag.
func tagKind(ctx context.Context, tok, base, name string) (string, error) {
	err := githubGet(ctx, tok, base+"/git/ref/tags/"+segments(name), nil)
	switch {
	case statusOf(err) == http.StatusNotFound:
		return "", nil
	case err != nil:
		return "", err
	}
	return "tag", nil
}

// contained is "commit" when sha is on the default branch or on one of the
// repository's protected branches, which a commit from a fork or a pull
// request is not until a maintainer merges it.
func contained(ctx context.Context, tok, base, def, sha string) (string, error) {
	branches := []string{def}
	var prot []struct {
		Name string `json:"name"`
	}
	if err := githubGet(ctx, tok, base+"/branches?protected=true&per_page="+strconv.Itoa(maxProtected), &prot); err != nil {
		return "", err
	}
	for _, p := range prot {
		if p.Name != def {
			branches = append(branches, p.Name)
		}
	}
	for _, b := range branches {
		if b == "" {
			continue
		}
		var c struct {
			Status string `json:"status"`
		}
		err := githubGet(ctx, tok, base+"/compare/"+segments(b)+"..."+sha, &c)
		switch {
		case statusOf(err) == http.StatusNotFound:
			continue
		case err != nil:
			return "", err
		case c.Status == "identical" || c.Status == "behind":
			return "commit", nil
		}
	}
	return "", nil
}
