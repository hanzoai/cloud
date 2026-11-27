package provider

// github_issue.go is the one door cloud writes a GitHub issue through: open one,
// edit it, hand it to somebody, label it, comment on it.
//
// The App writes, because it is the only GitHub identity this process holds —
// so GitHub shows hanzo-platform[bot] as the author, and the audit trail here is
// where the PERSON behind each write is kept. Who may write is decided before
// the call arrives (the forge's ACL on a mirror, the caller's GitHub view on a
// repository no mirror stands for); the org decides which installations there
// are to write through, and it is the caller's, never an argument.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/client"
	iampeer "github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// me is the assignee that means the caller.
const me = "me"

// exposeIssue publishes the write on the internal plane. Mount calls it.
func exposeIssue() {
	cloud.Plane().Post("/integrations/github/issue", planeGitHubIssue,
		zip.WithOperationID(client.IntegrationsGitHubIssue),
		zip.WithSummary("Open, edit, assign, label or comment on one GitHub issue through the App"))
}

// planeGitHubIssue makes one write to one issue and answers the issue as GitHub
// then holds it. The comment goes last, so a comment that says "assigned to you"
// is posted after the assignment it reports.
func planeGitHubIssue(ctx context.Context, in *client.GitHubIssueIn) (*client.GitHubIssue, error) {
	who := cloud.Who(ctx)
	org := strings.TrimSpace(who.Org)
	if org == "" {
		return nil, zip.ErrForbidden("github issue: no org on the call")
	}
	if in == nil || !ghName.MatchString(in.Owner) || !ghName.MatchString(in.Repo) || in.Number < 0 {
		return nil, zip.ErrBadRequest("github issue: owner and repo are required")
	}
	if in.Number == 0 && strings.TrimSpace(in.Title) == "" {
		return nil, zip.ErrBadRequest("github issue: a new issue needs a title")
	}
	if in.State != "" && in.State != "open" && in.State != "closed" {
		return nil, zip.ErrBadRequest("github issue: state is open or closed")
	}
	if mounted == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "github issue: integrations is not serving")
	}
	tok, err := InstallationToken(ctx, org, in.Owner)
	if err != nil {
		return nil, zip.ErrForbidden(fmt.Sprintf("github issue: this org's GitHub does not reach %s: %v", in.Owner, err))
	}
	var holders *[]string
	if in.Assignee != nil {
		held := []string{}
		switch login := strings.TrimPrefix(strings.TrimSpace(*in.Assignee), "@"); {
		case login == "":
		case strings.EqualFold(login, me):
			mine, err := githubLogin(ctx, tok)
			if err != nil {
				return nil, err
			}
			held = append(held, mine)
		default:
			held = append(held, login)
		}
		holders = &held
	}

	base := "/repos/" + url.PathEscape(in.Owner) + "/" + url.PathEscape(in.Repo) + "/issues"
	var is githubIssue
	num := in.Number
	if num == 0 {
		open := map[string]any{"title": in.Title, "body": in.Body}
		if len(in.Labels) > 0 {
			open["labels"] = in.Labels
		}
		if holders != nil {
			open["assignees"] = *holders
		}
		if err := githubWrite(ctx, tok, http.MethodPost, base, open, &is); err != nil {
			return nil, err
		}
		num = is.Number
	} else {
		edit := map[string]any{}
		for k, v := range map[string]string{"title": in.Title, "body": in.Body, "state": in.State} {
			if v != "" {
				edit[k] = v
			}
		}
		if holders != nil {
			edit["assignees"] = *holders
		}
		at := base + "/" + strconv.Itoa(num)
		if len(edit) > 0 {
			if err := githubWrite(ctx, tok, http.MethodPatch, at, edit, &is); err != nil {
				return nil, err
			}
		}
		if len(in.Labels) > 0 {
			if err := githubWrite(ctx, tok, http.MethodPost, at+"/labels", map[string]any{"labels": in.Labels}, nil); err != nil {
				return nil, err
			}
			is.Number = 0 // the labels moved; the answer is read back below
		}
		if is.Number == 0 {
			if err := githubGet(ctx, tok, at, &is); err != nil {
				return nil, zip.Errorf(http.StatusBadGateway, "github issue: read %s/%s#%d: %v", in.Owner, in.Repo, num, err)
			}
		}
	}
	if c := strings.TrimSpace(in.Comment); c != "" {
		if err := githubWrite(ctx, tok, http.MethodPost, base+"/"+strconv.Itoa(num)+"/comments", map[string]string{"body": c}, nil); err != nil {
			return nil, err
		}
	}
	recordIssueWrite(ctx, who, in.Owner+"/"+in.Repo, num)
	// The board row, and the thread it is bound to: mirrored here rather than left
	// to the webhook, which races this answer and names no room. The upsert binds
	// the room once, whichever of the two arrives first.
	owner, repo := named(is, in.Owner, in.Repo)
	res, err := mirrorGitHubIssue(ctx, org, repo, owner+"/"+repo, is, in.Channel)
	if err != nil {
		mounted.Log.Warn("github issue: the board row was not written", "org", org, "issue", owner+"/"+repo, "err", err)
	}
	out := &client.GitHubIssue{
		Owner: owner, Repo: repo, Number: is.Number, Title: is.Title, Body: is.Body,
		State: is.State, URL: is.HTMLURL, Assignee: assigneeOf(is), Labels: []string{}, Channel: res.Channel,
	}
	for _, l := range is.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	return out, nil
}

// named is the owner and repository as GitHub spells them now, read off the
// issue's own repository address; the caller's spelling when it carries none. A
// mirror row is keyed by GitHub's spelling, so a row written under the caller's
// would be a second row for one issue.
func named(is githubIssue, owner, repo string) (string, string) {
	rest, ok := strings.CutPrefix(is.RepositoryURL, strings.TrimRight(githubAPIBase, "/")+"/repos/")
	o, n, found := strings.Cut(rest, "/")
	if !ok || !found || !ghName.MatchString(o) || !ghName.MatchString(n) {
		return owner, repo
	}
	return o, n
}

// githubLogin is the caller's GitHub login: the account IAM linked to them when
// they signed in with GitHub, by the numeric id IAM holds, named as GitHub names
// it now. No table here maps people to accounts; IAM's row is the only one.
func githubLogin(ctx context.Context, tok string) (string, error) {
	out, err := iampeer.IAMLinked(ctx, &client.LinkedIn{Provider: "github"})
	if err != nil {
		return "", zip.Errorf(http.StatusBadGateway, "github issue: could not ask IAM who you are on GitHub: %v", err)
	}
	id := ""
	if out != nil {
		id = strings.TrimSpace(out.Subject)
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		if ghName.MatchString(id) {
			var u struct {
				Login string `json:"login"`
			}
			if err := githubGet(ctx, tok, "/users/"+url.PathEscape(id), &u); err == nil && ghName.MatchString(u.Login) {
				return u.Login, nil
			}
		}
		return "", zip.ErrForbidden("github issue: your Hanzo account has no GitHub linked — sign in to hanzo.id with GitHub once, then ask again")
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := githubGet(ctx, tok, "/user/"+id, &u); err != nil || !ghName.MatchString(u.Login) {
		return "", zip.Errorf(http.StatusBadGateway, "github issue: GitHub did not name the account linked to you")
	}
	return u.Login, nil
}

// githubWrite sends one JSON write to a path under the API and decodes GitHub's
// answer into out. A refusal carries GitHub's own words, with anything shaped
// like a secret taken out, because "the label does not exist" is something a
// person can act on.
func githubWrite(ctx context.Context, tok, method, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	code, raw, _, err := githubCall(ctx, tok, method, strings.TrimRight(githubAPIBase, "/")+path, b)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "github issue: %v", err)
	}
	if code/100 != 2 {
		status := http.StatusBadGateway
		if code == http.StatusNotFound || code == http.StatusForbidden || code == http.StatusUnprocessableEntity {
			status = code
		}
		return zip.Errorf(status, "github issue: %d %s", code, truncateBody(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return zip.Errorf(http.StatusBadGateway, "github issue: decode: %v", err)
		}
	}
	return nil
}

// recordIssueWrite keeps the person behind a write GitHub attributes to the
// App. Best-effort: the write has already happened on GitHub, and a trail that
// cannot be written is said in the log rather than undoing it.
func recordIssueWrite(ctx context.Context, who zip.Caller, repo string, number int) {
	s := mounted
	if s == nil {
		return
	}
	id := repo + "#" + strconv.Itoa(number)
	if s.State.audit == nil {
		s.Log.Info("github issue: written", "org", who.Org, "user", who.User, "issue", id)
		return
	}
	if _, err := s.State.audit.Append(ctx, audit.Record{
		Actor:    audit.Actor{Org: who.Org, Sub: who.User},
		Action:   "github.issue.write",
		Resource: audit.Resource{Type: "github_issue", ID: id},
		Auth:     audit.AuthContext{Method: "plane"},
		Outcome:  audit.Outcome{Result: "success", Status: http.StatusOK},
	}); err != nil {
		s.Log.Warn("github issue: audit write failed", "org", who.Org, "issue", id, "err", err)
	}
}
