package provider

// github_figures.go answers the estate's open source collector (#456): what
// GitHub says of public repositories, read with the App's own installation.
//
// The App key never leaves and no credential crosses. What leaves is counts and
// manifest text of repositories GitHub publishes. Any installation's token reads
// public data anywhere, so the first installation the App holds serves; a
// repository GitHub does not publish is reported missing and nothing of it is
// sent.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

const (
	// maxFigureRepos bounds one question: one GraphQL query of this many repositories.
	maxFigureRepos = 100
	// maxManifest bounds one manifest sent back. A larger file is no package's own.
	maxManifest = 256 << 10
	// maxFigureBody bounds GitHub's answer.
	maxFigureBody = 32 << 20
)

// figureFiles are the manifests read at a repository's root, one alias each.
var figureFiles = []string{"package.json", "pyproject.toml", "setup.cfg", "Cargo.toml"}

// figureQuery asks n repositories for their counts and root manifests. Every
// name rides as a variable, never in the query text.
func figureQuery(n int) string {
	var b strings.Builder
	b.WriteString("query(")
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "$o%d:String!,$n%d:String!", i, i)
	}
	b.WriteString("){")
	for i := range n {
		fmt.Fprintf(&b, "r%d:repository(owner:$o%d,name:$n%d){...F}", i, i, i)
	}
	b.WriteString("}fragment F on Repository{nameWithOwner isPrivate stargazerCount forkCount watchers{totalCount} issues(states:OPEN){totalCount}")
	for i, f := range figureFiles {
		fmt.Fprintf(&b, " f%d:object(expression:%q){...B}", i, "HEAD:"+f)
	}
	b.WriteString("}fragment B on Blob{text isTruncated}")
	return b.String()
}

type figureBlob struct {
	Text        string `json:"text"`
	IsTruncated bool   `json:"isTruncated"`
}

type figureNode struct {
	NameWithOwner string `json:"nameWithOwner"`
	IsPrivate     bool   `json:"isPrivate"`
	Stars         int64  `json:"stargazerCount"`
	Forks         int64  `json:"forkCount"`
	Watchers      struct {
		Total int64 `json:"totalCount"`
	} `json:"watchers"`
	Issues struct {
		Total int64 `json:"totalCount"`
	} `json:"issues"`
	F0 *figureBlob `json:"f0"`
	F1 *figureBlob `json:"f1"`
	F2 *figureBlob `json:"f2"`
	F3 *figureBlob `json:"f3"`
}

// figureInstall is the installation that last served, so a run of queries lists
// the App's installations once.
var figureInstall atomic.Int64

// publicToken is an installation token for reading public data.
func publicToken(ctx context.Context) (string, error) {
	if id := figureInstall.Load(); id != 0 {
		if tok, err := ghApp.installationToken(ctx, id); err == nil {
			return tok, nil
		}
		figureInstall.Store(0)
	}
	ins, err := appInstallations(ctx)
	if err != nil {
		return "", err
	}
	if len(ins) == 0 {
		return "", fmt.Errorf("the GitHub App is installed nowhere")
	}
	var last error
	for _, in := range ins[:min(len(ins), 3)] {
		tok, err := ghApp.installationToken(ctx, in.ID)
		if err == nil {
			figureInstall.Store(in.ID)
			return tok, nil
		}
		last = err
	}
	return "", last
}

// graphqlFigures asks GitHub for repos (owner/name pairs) with token and answers
// one node per repository, in order. A repository GitHub cannot show — not found,
// or behind an organization's SSO — is a nil node; any other error fails the
// question.
func graphqlFigures(ctx context.Context, token string, repos []string) (nodes []*figureNode, err error) {
	vars := make(map[string]any, 2*len(repos))
	for i, r := range repos {
		o, n, _ := strings.Cut(r, "/")
		vars["o"+strconv.Itoa(i)], vars["n"+strconv.Itoa(i)] = o, n
	}
	body, err := json.Marshal(map[string]any{"query": figureQuery(len(repos)), "variables": vars})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(githubAPIBase, "/")+"/graphql", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFigureBody))
	if err != nil {
		return nil, fmt.Errorf("github read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, githubStatus{code: resp.StatusCode, body: truncateBody(raw)}
	}
	var out struct {
		Data   map[string]*figureNode `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Path    []any  `json:"path"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("github decode: %w", err)
	}
	for _, e := range out.Errors {
		if len(e.Path) == 1 && (e.Type == "NOT_FOUND" || e.Type == "FORBIDDEN") {
			continue
		}
		return nil, fmt.Errorf("github graphql %s: %s", e.Type, truncateBody([]byte(e.Message)))
	}
	nodes = make([]*figureNode, len(repos))
	for i := range repos {
		nodes[i] = out.Data["r"+strconv.Itoa(i)]
	}
	return nodes, nil
}

// exposeFigures publishes the figures read on the internal plane. Mount calls it.
func exposeFigures() {
	cloud.Plane().Post("/integrations/github/figures", planeGitHubFigures,
		zip.WithOperationID(client.IntegrationsGitHubFigures),
		zip.WithSummary("What GitHub says of up to 100 public repositories"))
}

// planeGitHubFigures answers what GitHub says of up to 100 repositories, named
// owner/name: their stars, forks, open issues and watchers, and the package
// manifests at their roots.
//
// It is the platform's and nobody else's: the estate's collector asks it, as the
// platform, and any other caller is refused. The repositories are an argument
// because they name no tenant; the App's key stays here and no credential is
// returned. A repository GitHub does not publish — gone, renamed away or private
// — is listed as missing and nothing of it is read. A manifest GitHub truncates,
// or larger than 256 KiB, is left out.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeGitHubFigures(ctx context.Context, in *client.GitHubFiguresIn) (*client.GitHubFigures, error) {
	if cloud.Who(ctx).Org != authz.AdminOrg {
		return nil, zip.ErrForbidden("github figures: the platform asks")
	}
	if in == nil || len(in.Repos) == 0 || len(in.Repos) > maxFigureRepos {
		return nil, zip.ErrBadRequest(fmt.Sprintf("github figures: 1 to %d repositories", maxFigureRepos))
	}
	for _, r := range in.Repos {
		o, n, ok := splitFullName(r)
		if !ok || !ghName.MatchString(o) || !ghName.MatchString(n) {
			return nil, zip.ErrBadRequest("github figures: repositories are owner/name")
		}
	}
	if mounted == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "github figures: integrations is not serving")
	}
	tok, err := publicToken(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github figures: %v", err)
	}
	nodes, err := graphqlFigures(ctx, tok, in.Repos)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github figures: %v", err)
	}
	out := &client.GitHubFigures{Repos: []client.GitHubFigure{}}
	for i, n := range nodes {
		if n == nil || n.IsPrivate {
			out.Missing = append(out.Missing, in.Repos[i])
			continue
		}
		f := client.GitHubFigure{Repo: n.NameWithOwner, Stars: n.Stars, Forks: n.Forks, Issues: n.Issues.Total, Watchers: n.Watchers.Total}
		for j, b := range []*figureBlob{n.F0, n.F1, n.F2, n.F3} {
			if b != nil && !b.IsTruncated && len(b.Text) <= maxManifest {
				f.Files = append(f.Files, client.GitHubFile{Path: figureFiles[j], Text: b.Text})
			}
		}
		out.Repos = append(out.Repos, f)
	}
	slices.SortFunc(out.Repos, func(a, b client.GitHubFigure) int { return strings.Compare(a.Repo, b.Repo) })
	return out, nil
}
