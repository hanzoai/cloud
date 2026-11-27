package provider

// github_change.go is what triage asks of a change it owns: read a pull request
// whole, merge it at the head that was read, and ask whether a release holds a
// commit. Each acts as the App on the caller's org; who may ask is decided by the
// plane, which only a sibling process reaches.

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// exposeChange publishes the three ops on the internal plane. Mount calls it.
func exposeChange() {
	p := cloud.Plane()
	p.Post("/integrations/github/pull", planeGitHubPull,
		zip.WithOperationID(client.IntegrationsGitHubPull),
		zip.WithSummary("Read one pull request: its head, its files and every check on the head"))
	p.Post("/integrations/github/merge", planeGitHubMerge,
		zip.WithOperationID(client.IntegrationsGitHubMerge),
		zip.WithSummary("Merge one pull request at the head the caller read"))
	p.Post("/integrations/github/contains", planeGitHubContains,
		zip.WithOperationID(client.IntegrationsGitHubContains),
		zip.WithSummary("Whether a ref's history holds a commit"))
}

// changeToken is the App's token for owner on the caller's org.
func changeToken(ctx context.Context, owner, repo string) (string, error) {
	org := strings.TrimSpace(cloud.Who(ctx).Org)
	switch {
	case org == "":
		return "", zip.ErrForbidden("github: no org on the call")
	case !ghName.MatchString(owner) || !ghName.MatchString(repo):
		return "", zip.ErrBadRequest("github: owner and repo are required")
	case mounted == nil:
		return "", zip.Errorf(http.StatusServiceUnavailable, "github: integrations is not serving")
	}
	tok, err := InstallationToken(ctx, org, owner)
	if err != nil {
		return "", zip.ErrForbidden(fmt.Sprintf("github: this org's GitHub does not reach %s: %v", owner, err))
	}
	return tok, nil
}

// readJSON is one GET of a repository path, decoded. A status other than 200 is
// the caller's to see: 404 is returned as such.
func readJSON(ctx context.Context, tok, path string, out any) (int, error) {
	code, raw, _, err := githubCall(ctx, tok, http.MethodGet, strings.TrimRight(githubAPIBase, "/")+path, nil)
	if err != nil {
		return 0, zip.Errorf(http.StatusBadGateway, "github: %v", err)
	}
	if code != http.StatusOK {
		return code, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return code, zip.Errorf(http.StatusBadGateway, "github: %s answered something that is not JSON", path)
	}
	return code, nil
}

func repoPath(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

// planeGitHubPull answers the pull request as GitHub holds it.
func planeGitHubPull(ctx context.Context, in *client.GitHubPullIn) (*client.GitHubPull, error) {
	tok, err := changeToken(ctx, in.Owner, in.Repo)
	if err != nil {
		return nil, err
	}
	base := repoPath(in.Owner, in.Repo)
	var pr struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		Merged  bool   `json:"merged"`
		Draft   bool   `json:"draft"`
		Changed int    `json:"changed_files"`
		Head    struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	switch code, err := readJSON(ctx, tok, base+"/pulls/"+strconv.Itoa(in.Number), &pr); {
	case err != nil:
		return nil, err
	case code == http.StatusNotFound:
		return nil, zip.ErrNotFound("github: no such pull request")
	case code != http.StatusOK:
		return nil, zip.Errorf(http.StatusBadGateway, "github: read pull request %d: %d", in.Number, code)
	}
	out := &client.GitHubPull{
		Number: pr.Number, Title: pr.Title, State: pr.State, Draft: pr.Draft,
		Head: pr.Head.SHA, Branch: pr.Head.Ref, Base: pr.Base.Ref, Changed: pr.Changed,
		Files: []string{}, Checks: []client.GitHubCheck{},
	}
	if pr.Merged {
		out.State = "merged"
	}
	// A page is a hundred files, and GitHub lists at most three thousand.
	read := 0
	for page := 1; read < pr.Changed && page <= 30; page++ {
		var files []struct {
			Name string `json:"filename"`
			Was  string `json:"previous_filename"`
		}
		if code, err := readJSON(ctx, tok, fmt.Sprintf("%s/pulls/%d/files?per_page=100&page=%d", base, in.Number, page), &files); err != nil || code != http.StatusOK || len(files) == 0 {
			break
		}
		read += len(files)
		for _, f := range files {
			out.Files = append(out.Files, f.Name)
			if f.Was != "" {
				out.Files = append(out.Files, f.Was)
			}
		}
	}
	var combined struct {
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if _, err := readJSON(ctx, tok, base+"/commits/"+url.PathEscape(pr.Head.SHA)+"/status", &combined); err != nil {
		return nil, err
	}
	for _, s := range combined.Statuses {
		state := map[string]string{"success": "success", "pending": "pending"}[s.State]
		out.Checks = append(out.Checks, client.GitHubCheck{Name: s.Context, State: cmp.Or(state, "failure")})
	}
	var runs struct {
		Total int `json:"total_count"`
		Runs  []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if _, err := readJSON(ctx, tok, base+"/commits/"+url.PathEscape(pr.Head.SHA)+"/check-runs?per_page=100", &runs); err != nil {
		return nil, err
	}
	for _, r := range runs.Runs {
		state := "failure"
		switch {
		case r.Status != "completed":
			state = "pending"
		case r.Conclusion == "success" || r.Conclusion == "neutral" || r.Conclusion == "skipped":
			state = "success"
		}
		out.Checks = append(out.Checks, client.GitHubCheck{Name: r.Name, State: state})
	}
	if runs.Total > len(runs.Runs) {
		out.Checks = append(out.Checks, client.GitHubCheck{Name: "(checks not read)", State: "pending"})
	}
	return out, nil
}

// planeGitHubMerge merges the pull request at the head the caller read. A branch
// that moved, a pull request GitHub will not merge and a draft are conflicts, said
// in GitHub's own words.
func planeGitHubMerge(ctx context.Context, in *client.GitHubMergeIn) (*client.GitHubMerged, error) {
	tok, err := changeToken(ctx, in.Owner, in.Repo)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]string{"sha": in.Sha})
	code, raw, _, err := githubCall(ctx, tok, http.MethodPut,
		strings.TrimRight(githubAPIBase, "/")+repoPath(in.Owner, in.Repo)+"/pulls/"+strconv.Itoa(in.Number)+"/merge", body)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github: %v", err)
	}
	var out struct {
		Merged  bool   `json:"merged"`
		SHA     string `json:"sha"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &out)
	switch {
	case code == http.StatusOK && out.Merged:
		return &client.GitHubMerged{Sha: out.SHA}, nil
	case code == http.StatusMethodNotAllowed || code == http.StatusConflict || code == http.StatusUnprocessableEntity:
		return nil, zip.ErrConflict("github: " + cmp.Or(strings.TrimSpace(out.Message), "not mergeable"))
	}
	return nil, zip.Errorf(http.StatusBadGateway, "github: merge pull request %d: %d", in.Number, code)
}

// planeGitHubContains says whether ref's history holds sha: the comparison of the
// two reads ahead or identical. A ref GitHub does not know holds nothing.
func planeGitHubContains(ctx context.Context, in *client.GitHubContainsIn) (*client.GitHubContains, error) {
	tok, err := changeToken(ctx, in.Owner, in.Repo)
	if err != nil {
		return nil, err
	}
	var cmpd struct {
		Status string `json:"status"`
	}
	code, err := readJSON(ctx, tok, repoPath(in.Owner, in.Repo)+"/compare/"+url.PathEscape(in.Sha)+"..."+url.PathEscape(in.Ref), &cmpd)
	if err != nil {
		return nil, err
	}
	return &client.GitHubContains{Contains: code == http.StatusOK && (cmpd.Status == "ahead" || cmpd.Status == "identical")}, nil
}
