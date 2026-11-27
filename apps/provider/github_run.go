package provider

// github_run.go admits a coding run on a GitHub repository that no forge mirror
// stands for, and mints the one-repository token the run pushes with.
//
// On a mirror the forge's ACL decides who may run, and the grant op beside this
// one trusts that it did (github_grant.go). A repository the forge does not hold
// has no forge ACL, so the answer here is GitHub's, asked two ways and in this
// order:
//
//	the person's own access   their connected token reads the repository and
//	                          GitHub says they may push to it
//	an org admin              the repository is in an installation their org
//	                          holds
//
// Anything else is refused and nothing is minted. The org and the person are the
// CALLER's — forwarded from the validated request or stated by the run — and
// neither is an argument, because each decides what a token is minted on.
//
// Both answers are asked AT THE MINT, never carried in. A run's completion mints
// minutes or hours after its start, from a durable record, and an admin bit
// copied into that record would outlive a demotion. So the person's access is
// read with their own token as it stands now, and whether they administer the
// org is asked of IAM as it stands now — whatever the caller says about either.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	iampeer "github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// exposeRun publishes the admission on the internal plane. Mount calls it.
func exposeRun() {
	cloud.Plane().Post("/integrations/github/run", planeGitHubRun,
		zip.WithOperationID(client.IntegrationsGitHubRun),
		zip.WithSummary("Admit a coding run on a GitHub repository and mint its one-repository token"))
}

// exposeRepo publishes the lookup on the internal plane. Mount calls it.
func exposeRepo() {
	cloud.Plane().Post("/integrations/github/repo", planeGitHubRepo,
		zip.WithOperationID(client.IntegrationsGitHubRepo),
		zip.WithSummary("Whether the caller may see one GitHub repository, and what it is"))
}

// planeGitHubRepo is one repository from the caller's own view — the view the
// paged listing answers from — or 404, whether it does not exist or the caller
// may not see it. The org, the person and the admin bit are the caller's.
func planeGitHubRepo(ctx context.Context, in *client.GitHubRepoIn) (*client.GitHubRepo, error) {
	who := cloud.Who(ctx)
	org := strings.TrimSpace(who.Org)
	if org == "" {
		return nil, zip.ErrForbidden("github repo: no org on the call")
	}
	if in == nil || !ghName.MatchString(in.Owner) || !ghName.MatchString(in.Repo) {
		return nil, zip.ErrBadRequest("github repo: owner and repo are required")
	}
	if mounted == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "github repo: integrations is not serving")
	}
	v, personal, err := view(ctx, mounted, org, strings.TrimSpace(who.User), orgAdminOf(who))
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(in.Owner + "/" + in.Repo)
	for _, r := range v.repos {
		if strings.ToLower(r.FullName) != want {
			continue
		}
		it := item(r)
		return &client.GitHubRepo{
			Owner: it.Owner, Repo: it.Name, Base: safeBranch(it.DefaultBranch), Private: it.Private,
			Installation: it.InstallationID, Personal: personal,
		}, nil
	}
	return nil, zip.ErrNotFound("github repo: not found")
}

// planeGitHubRun admits the caller to run on owner/repo and mints the token: on
// their own GitHub access when it includes push, else as an org admin on an
// installation their org holds — IAM's answer at this moment, never a bit the
// caller carried. It names whose access admitted the run, and refuses — minting
// nothing — when neither does.
func planeGitHubRun(ctx context.Context, in *client.GitHubRunIn) (*client.GitHubRun, error) {
	who := cloud.Who(ctx)
	org := strings.TrimSpace(who.Org)
	if org == "" {
		return nil, zip.ErrForbidden("github run: no org on the call")
	}
	if in == nil || !ghName.MatchString(in.Owner) || !ghName.MatchString(in.Repo) {
		return nil, zip.ErrBadRequest("github run: owner and repo are required")
	}
	if mounted == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "github run: integrations is not serving")
	}
	if !githubConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "github run: github integration is not configured on this deployment")
	}
	s := mounted
	tok, login, connected, err := personToken(ctx, s, org, strings.TrimSpace(who.User))
	if err != nil {
		return nil, err
	}
	var run *client.GitHubRun
	via := ""
	if connected {
		run, err = personRun(ctx, tok, in.Owner, in.Repo, in.Read)
		switch {
		case err == nil:
			run.Login, via = login, "person"
		case errors.Is(err, errGitHubRevoked):
			forgetPerson(ctx, s, org, strings.TrimSpace(who.User))
			err = zip.ErrForbidden("github run: GitHub no longer honours your connection; connect GitHub again")
		}
	}
	if run == nil {
		if !adminNow(ctx, s, who) {
			if connected {
				return nil, err
			}
			return nil, zip.ErrForbidden(fmt.Sprintf(
				"github run: connect your GitHub account to run on %s/%s, or ask an org admin", in.Owner, in.Repo))
		}
		if run, err = orgRun(ctx, org, in.Owner, in.Repo, in.Read); err != nil {
			return nil, err
		}
		via = "org-admin"
	}
	if err := recordMint(ctx, s, who, run, via, in.Read); err != nil {
		return nil, err
	}
	return run, nil
}

// orgAdminOf is whether the caller administers the org the call is FOR, as the
// caller states it. A call re-pointed at another org — zip.ActingAs names who is
// acting — carries the admin bit of the org it came from, which says nothing
// about this one. It widens a VIEW (view) and admits a connect over the plane
// (planeConnect); a mint asks adminNow instead.
func orgAdminOf(who zip.Caller) bool { return who.OrgAdmin && who.ActedBy == "" }

// adminNow is whether the caller administers the org the call is for, asked of
// IAM at this moment: an owner or admin membership, or an admin at home — IAM's
// Admin, the grant itself, not a role that merely carries the name "System
// Manager", which a tenant may give anyone. The bit the caller carries is not
// read — it may have been copied into a durable record before a demotion. A call
// re-pointed at another org is no admin there, and an IAM that does not answer is
// no admin either.
func adminNow(ctx context.Context, s *cloud.Service[state], who zip.Caller) bool {
	org, user := strings.TrimSpace(who.Org), strings.TrimSpace(who.User)
	if who.ActedBy != "" || org == "" || user == "" {
		return false
	}
	ictx, cancel := context.WithTimeout(zip.WithCaller(context.Background(), zip.Caller{Org: org, User: user}), iamBudget)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	out, err := iampeer.IAMRoles(ictx)
	if err != nil {
		s.Log.Warn("github run: IAM did not say whether the caller administers the org; minting nothing on its installation",
			"org", org, "user", user, "err", err)
		return false
	}
	return out != nil && out.Admin
}

// iamBudget bounds the one IAM question a mint asks.
const iamBudget = 5 * time.Second

// recordMint writes the one record a minted run token leaves: who, in which
// org, admitted how, on which repository, and which live source said so —
// "github" when the person's own token read push access, "iam" when IAM said
// they administer the org. The token is the App's, so GitHub attributes the push
// to the App's bot; this is where the person behind it is kept. A trail that
// cannot be written refuses the mint, and the token already minted is revoked.
func recordMint(ctx context.Context, s *cloud.Service[state], who zip.Caller, run *client.GitHubRun, via string, read bool) error {
	access := "write"
	if read {
		access = "read"
	}
	if s.State.audit == nil {
		s.Log.Info("github run: token minted", "org", who.Org, "user", who.User, "login", run.Login,
			"repo", run.Owner+"/"+run.Repo, "via", via, "source", sourceOf(via), "access", access)
		return nil
	}
	after, _ := json.Marshal(map[string]string{
		"repo": run.Owner + "/" + run.Repo, "login": run.Login, "via": via, "source": sourceOf(via), "access": access,
	})
	_, err := s.State.audit.Append(ctx, audit.Record{
		Actor:    audit.Actor{Org: who.Org, Sub: who.User},
		Action:   "github.run.mint",
		Resource: audit.Resource{Type: "github_repository", ID: run.Owner + "/" + run.Repo},
		Auth:     audit.AuthContext{Method: "plane"},
		Outcome:  audit.Outcome{Result: "success", Status: http.StatusOK},
		After:    audit.Redact(after),
	})
	if err != nil {
		s.Log.Error("github run: audit write failed; revoking the minted token", "org", who.Org, "err", err)
		_ = revokeInstallationToken(ctx, run.Token)
		return zip.Errorf(http.StatusServiceUnavailable, "github run: the audit trail could not record the grant")
	}
	return nil
}

// sourceOf names the live source that admitted a mint: the person's own GitHub
// token, or IAM's answer that they administer the org.
func sourceOf(via string) string {
	if via == "person" {
		return "github"
	}
	return "iam"
}

// revokeInstallationToken ends a minted installation token now, with itself.
func revokeInstallationToken(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(githubAPIBase, "/")+"/installation/token", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("github revoke: request failed")
	}
	_ = resp.Body.Close()
	return nil
}

// personRun admits a run on the person's own GitHub access: their token must
// read the repository — which it can only where this App is installed — and
// GitHub must say they may push to it.
func personRun(ctx context.Context, tok, owner, repo string, read bool) (*client.GitHubRun, error) {
	var r struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Permissions   struct {
			Admin    bool `json:"admin"`
			Maintain bool `json:"maintain"`
			Push     bool `json:"push"`
		} `json:"permissions"`
	}
	if err := githubGet(ctx, tok, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo), &r); err != nil {
		if errors.Is(err, errGitHubRevoked) {
			return nil, err
		}
		return nil, zip.ErrForbidden(fmt.Sprintf("github run: your GitHub access does not reach %s/%s through this App", owner, repo))
	}
	o, n, found := strings.Cut(r.FullName, "/")
	if !found || !ghName.MatchString(o) || !ghName.MatchString(n) {
		return nil, zip.Errorf(http.StatusBadGateway, "github run: GitHub named no repository")
	}
	if !r.Permissions.Push && !r.Permissions.Maintain && !r.Permissions.Admin {
		return nil, zip.ErrForbidden(fmt.Sprintf("github run: your GitHub access to %s/%s is read-only; a run pushes a branch", o, n))
	}
	id, err := ghApp.installationOf(ctx, o, n)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github run: %v", err)
	}
	token, err := ghApp.grant(ctx, id, o, n, permsFor(read))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return &client.GitHubRun{Token: token, Owner: o, Repo: n, Base: safeBranch(r.DefaultBranch)}, nil
}

// orgRun admits an org admin's run on an installation their org holds.
func orgRun(ctx context.Context, org, owner, repo string, read bool) (*client.GitHubRun, error) {
	o, n, base, err := canonical(ctx, org, owner, repo)
	if err != nil {
		return nil, zip.ErrForbidden(fmt.Sprintf("github run: %s/%s is in no installation this org holds", owner, repo))
	}
	conn, err := githubConnection(org, o)
	if err != nil {
		return nil, zip.ErrForbidden(err.Error())
	}
	id, err := parseInstallation(conn.ExternalID)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "github run: invalid installation id")
	}
	token, err := ghApp.grant(ctx, id, o, n, permsFor(read))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%v", err)
	}
	return &client.GitHubRun{Token: token, Owner: o, Repo: n, Base: safeBranch(base)}, nil
}

// ghBranch is a branch name GitHub may answer with that is safe to hand to git
// as an argument: git refuses a ref that begins with a dash, and a base that
// did would read as a flag. Anything else is dropped rather than passed on, and
// the caller falls back to the repository's own default.
var ghBranch = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]{0,254}$`)

// safeBranch is b when it is a safe branch name, else "".
func safeBranch(b string) string {
	if !ghBranch.MatchString(b) || strings.Contains(b, "..") {
		return ""
	}
	return b
}

// installationOf is the installation of this App that holds owner/repo, as the
// App itself sees it.
func (g *ghAppState) installationOf(ctx context.Context, owner, repo string) (int64, error) {
	tr, err := g.transport()
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(githubAPIBase, "/")+"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/installation", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Transport: tr, Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0, fmt.Errorf("installation of %s/%s: %w", owner, repo, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		ID int64 `json:"id"`
	}
	if resp.StatusCode != http.StatusOK || decodeJSON(resp, &out) != nil || out.ID <= 0 {
		return 0, fmt.Errorf("the App is not installed where %s/%s lives (%d)", owner, repo, resp.StatusCode)
	}
	return out.ID, nil
}

// parseInstallation reads a connection's installation id.
func parseInstallation(external string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(external), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid installation id")
	}
	return id, nil
}

// decodeJSON reads a bounded JSON answer into out.
func decodeJSON(resp *http.Response, out any) error {
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
