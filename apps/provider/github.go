package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/sdk/env"
)

// GitHub is a GitHub *App* provider on the SAME registry as Slack/Google/GitLab.
// The org admin is sent to the App's installation page, and GitHub records the
// consent when the App is installed there.
//
// The installation_id the callback carries is a name written in a URL, not a
// grant GitHub made to this org: the App JWT reads EVERY tenant's installation, so
// an id that resolves is no evidence it resolves to the caller's. What is evidence
// is the installer's own authorization. With "Request user authorization (OAuth)
// during installation" on, GitHub returns a code beside the id; Install trades it
// for the installer's token and binds the id only when that token reaches it
// (GET /user/installations) — GitHub's own guidance for a spoofable id. Without
// the App's OAuth client configured nothing proves the installer, and binding
// stays githubClaim (github_app.go), platform sudo.
//
// Once bound, the installation_id is the connection ExternalID; it maps the install
// back to the org (ResolveOrgByExternalID) for inbound webhooks, and a fresh
// short-lived installation token is minted on demand from the App private key
// (github_app.go) for every list/import/mirror — never stored.
//
// Sync is wired via DEDICATED routes (github_app.go / github_webhook.go), not the
// generic Provider.Sync hook: the repo list, import, and the inbound push webhook
// each need their own request/response shape.
//
// Creds are ENV-injected (KMS-synced, never in code). Until the App is configured
// (slug + app id + private key) Configured() is false: the card shows Connect but
// available=false and connect returns an honest 503 — never a fake OK.
func init() {
	register(&Provider{
		ID:           "github",
		Name:         "GitHub",
		Description:  "Install the Hanzo GitHub App to mirror your org's repositories into git.hanzo.ai and keep them in sync.",
		Category:     "Developer",
		Scopes:       nil, // GitHub Apps use installation PERMISSIONS, not OAuth scopes.
		RedirectPath: callbackPath("github"),
		MultiAccount: true, // a GitHub App is installed per account; one org may hold several
		Secrets:      nil,  // no token custodied: installation tokens are minted on demand, never sealed.
		// Installing binds a GitHub account to the org — every repository it
		// grants, and the bill for work done there — so it is the org admin's act.
		AdminOnly:  true,
		Configured: githubConfigured,
		Creds:      githubCreds,
		Authorize:  githubAuthorize,
		// The install URL is built from the app slug, not a client id, so the
		// generic OAuth readiness check does not apply: without this the connect
		// flow is refused for a credential a GitHub App install never uses.
		AuthorizeReady: githubConfigured,
		Install:        githubInstall,
		// The connect starts at GitHub's authorization page, which returns even for
		// an account the App is already on (the install page offers Configure there,
		// which returns nothing): the person chooses one of their installations on
		// the chooser page (choose.go), else the browser goes on to the install page.
		Installed:   githubInstalled,
		InstallPage: githubInstallPage,
		Manage:      githubManage,
		Revoke:      nil, // App installs are removed from GitHub, which reports it (installation.deleted).
		// Disconnecting the last org holding an account removes the App there too,
		// so connecting it again installs afresh instead of landing on Configure.
		Uninstall: githubUninstall,
	})
}

const (
	githubAppSlugEnv = "GITHUB_APP_SLUG"
	githubAppIDEnv   = "GITHUB_APP_ID"
	// githubAppKeyEnv holds the App's PEM private key (KMS-synced). Used only to
	// mint short-lived installation tokens (github_app.go); never logged.
	githubAppKeyEnv = "GITHUB_APP_PRIVATE_KEY"
	// githubWebhookSecretEnv holds the App webhook secret (KMS-synced). The inbound
	// webhook HMAC-verifies every payload with it, fail-closed (github_webhook.go).
	githubWebhookSecretEnv = "GITHUB_APP_WEBHOOK_SECRET"
	// githubClientIDEnv / githubClientSecretEnv are the App's own OAuth client
	// (KMS-synced). Install spends them to learn who installed; absent, an
	// install cannot bind itself.
	githubClientIDEnv     = "GITHUB_APP_CLIENT_ID"
	githubClientSecretEnv = "GITHUB_APP_CLIENT_SECRET"
	githubSlugKey         = "slug" // OAuthConfig.Extra key carrying the App slug
)

// githubWebBase is GitHub's web origin, where the OAuth code is exchanged. A var
// so a test can point it at a mock; never mutated in production.
var githubWebBase = "https://github.com"

// githubCreds resolves the install config from ENV: the slug builds the install
// URL, and the App's OAuth client proves who installed.
func githubCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     env.Or(githubClientIDEnv, ""),
		ClientSecret: env.Or(githubClientSecretEnv, ""),
		Extra:        map[string]string{githubSlugKey: env.Or(githubAppSlugEnv, "")},
	}
}

// githubConfigured requires the three the install + sync need: the App slug (install
// URL), the App id, and the private key (installation-token minting). Absent ⇒ false
// ⇒ connect 503. The webhook secret is validated at webhook time (fail-closed there),
// so its absence does not hide the Connect card — inbound sync simply fails closed
// until it lands.
func githubConfigured() bool {
	return env.Or(githubAppSlugEnv, "") != "" &&
		env.Or(githubAppIDEnv, "") != "" &&
		env.Or(githubAppKeyEnv, "") != ""
}

// githubAuthorize starts an org's connect at GitHub's authorization page for the
// App, returning to the org callback. GitHub always comes back from it with a code
// and the state, also for an account the App is already on, which its install
// page cannot do: there it offers Configure, and saving that returns nothing. The
// callback binds what the person already has (githubInstalled), or sends them on to
// the install page. Without the App's OAuth client nothing could prove who
// installed, so the connect starts at the install page as it always did.
//
// GitHub skips its authorization page for anyone who authorized the App before,
// so it is asked for its account picker (githubPick): a connect after a
// Disconnect can choose another GitHub account instead of landing on the last.
func githubAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	if creds.ClientID == "" || creds.ClientSecret == "" {
		return githubInstallPage(creds, state)
	}
	q := url.Values{"client_id": {creds.ClientID}, "redirect_uri": {redirectURI}, "state": {state}, "prompt": {githubPick}}
	return strings.TrimRight(githubWebBase, "/") + "/login/oauth/authorize?" + q.Encode(), nil
}

// githubPick is the authorization page's `prompt` that always shows GitHub's
// account picker, even for one signed-in account.
const githubPick = "select_account"

// githubInstallPage builds the App installation URL:
// https://github.com/apps/{slug}/installations/new?state=… . GitHub carries the
// state param through the install and returns it (with installation_id) on the callback.
func githubInstallPage(creds OAuthConfig, state string) (string, error) {
	slug := creds.Extra[githubSlugKey]
	if slug == "" {
		return "", fmt.Errorf("github: GITHUB_APP_SLUG not set")
	}
	return "https://github.com/apps/" + url.PathEscape(slug) +
		"/installations/new?state=" + url.QueryEscape(state), nil
}

// githubManage is the installation's own settings page on GitHub, where the person
// changes which repositories the App reaches: under the organization for an
// organization's install, under their own settings for their personal account's.
func githubManage(o offered) string {
	id := url.PathEscape(o.ID)
	if strings.EqualFold(o.Kind, "User") {
		return "https://github.com/settings/installations/" + id
	}
	return "https://github.com/organizations/" + url.PathEscape(o.Login) + "/settings/installations/" + id
}

// githubInstall binds the installation GitHub just created, on the installer's
// word and not the URL's. The code is the installer's authorization; traded for
// their token, it lists the installations that person can reach, and the id the
// callback named binds only when it is one of them. The token is used for that
// one question and never stored.
func githubInstall(ctx context.Context, creds OAuthConfig, code, installation string) (*ExchangeResult, error) {
	if creds.ClientID == "" || creds.ClientSecret == "" {
		return nil, fmt.Errorf("github: the App's OAuth client is not configured, so nothing proves who installed")
	}
	id, err := strconv.ParseInt(installation, 10, 64)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("github: no installation named")
	}
	token, err := githubUserToken(ctx, creds, code)
	if err != nil {
		return nil, err
	}
	reach, err := githubUserInstallations(ctx, token)
	if err != nil {
		return nil, err
	}
	for _, in := range reach {
		if in.ID == id {
			return &ExchangeResult{ExternalID: strconv.FormatInt(id, 10), AccountLabel: in.Login}, nil
		}
	}
	return nil, fmt.Errorf("github: installation %d is not reachable by the person who installed", id)
}

// githubInstalled answers the installations of the App that the person whose code
// this is controls: their own account, or an organization where they administer
// every repository the installation grants. Binding one then gives the org
// nothing that person could not already administer. Which of them another org
// holds is the callback's question (bind), not GitHub's. The token is used for
// these questions and never stored.
func githubInstalled(ctx context.Context, creds OAuthConfig, code string) ([]*ExchangeResult, error) {
	if creds.ClientID == "" || creds.ClientSecret == "" {
		return nil, fmt.Errorf("github: the App's OAuth client is not configured, so nothing proves who is connecting")
	}
	token, err := githubUserToken(ctx, creds, code)
	if err != nil {
		return nil, err
	}
	var (
		me struct {
			Login string `json:"login"`
		}
		reach    []githubInstallation
		meErr    error
		reachErr error
		initWG   sync.WaitGroup
	)
	initWG.Add(2)
	go func() {
		defer initWG.Done()
		meErr = githubGet(ctx, token, "/user", &me)
	}()
	go func() {
		defer initWG.Done()
		reach, reachErr = githubUserInstallations(ctx, token)
	}()
	initWG.Wait()
	if meErr != nil {
		return nil, meErr
	}
	if reachErr != nil {
		return nil, reachErr
	}
	keep := make([]bool, len(reach))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, in := range reach {
		wg.Add(1)
		go func(i int, in githubInstallation) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// An installation that cannot be checked is not one the person controls:
			// it is left for the install page, and the rest are still answered.
			if ok, err := githubControls(ctx, token, me.Login, in); err == nil && ok {
				keep[i] = true
			}
		}(i, in)
	}
	wg.Wait()
	var out []*ExchangeResult
	for i, in := range reach {
		if keep[i] {
			out = append(out, &ExchangeResult{ExternalID: strconv.FormatInt(in.ID, 10), AccountLabel: in.Login, Kind: in.Type})
		}
	}
	return out, nil
}

// githubControls says whether the person signed in as login controls in: their
// own account, or an organization where they administer every repository the
// installation grants (and it grants one; an empty installation proves nothing,
// and repositories added to it later would arrive unasked).
func githubControls(ctx context.Context, token, login string, in githubInstallation) (bool, error) {
	switch {
	case strings.EqualFold(in.Type, "User"):
		return login != "" && strings.EqualFold(in.Login, login), nil
	case !strings.EqualFold(in.Type, "Organization"):
		return false, nil
	}
	var (
		granted    []githubRepo
		grantedErr error
		mine       []githubPermittedRepo
		mineErr    error
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		inst, err := ghApp.installationToken(ctx, in.ID)
		if err != nil {
			grantedErr = err
			return
		}
		granted, grantedErr = grantedRepos(ctx, in.ID, inst)
	}()
	go func() {
		defer wg.Done()
		mine, mineErr = walk(ctx, func(ctx context.Context, page int) ([]githubPermittedRepo, int, error) {
			var p struct {
				TotalCount   int                   `json:"total_count"`
				Repositories []githubPermittedRepo `json:"repositories"`
			}
			if err := githubGet(ctx, token, fmt.Sprintf("/user/installations/%d/repositories?per_page=100&page=%d", in.ID, page), &p); err != nil {
				return nil, 0, err
			}
			return p.Repositories, p.TotalCount, nil
		})
	}()
	wg.Wait()
	if grantedErr != nil || len(granted) == 0 {
		return false, grantedErr
	}
	if mineErr != nil {
		return false, mineErr
	}
	administers := make(map[string]bool, len(mine))
	for _, r := range mine {
		if r.Permissions.Admin {
			administers[strings.ToLower(r.FullName)] = true
		}
	}
	for _, r := range granted {
		if !administers[strings.ToLower(r.FullName)] {
			return false, nil
		}
	}
	return true, nil
}

// githubPermittedRepo is a repository as a person's own listing names it, with
// what that person may do there.
type githubPermittedRepo struct {
	FullName    string `json:"full_name"`
	Permissions struct {
		Admin bool `json:"admin"`
	} `json:"permissions"`
}

// githubUserToken trades the installer's OAuth code for their user token.
func githubUserToken(ctx context.Context, creds OAuthConfig, code string) (string, error) {
	form := url.Values{"client_id": {creds.ClientID}, "client_secret": {creds.ClientSecret}, "code": {code}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(githubWebBase, "/")+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("github oauth: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("github oauth http %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("github oauth decode: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("github oauth: %s", cmp.Or(out.Error, "no access_token"))
	}
	return out.AccessToken, nil
}

// githubUserInstallations lists the App's installations a user token reaches.
func githubUserInstallations(ctx context.Context, token string) ([]githubInstallation, error) {
	const perPage = 100
	const maxPages = 10
	var all []githubInstallation
	for page := 1; page <= maxPages; page++ {
		endpoint := fmt.Sprintf("%s/user/installations?per_page=%d&page=%d",
			strings.TrimRight(githubAPIBase, "/"), perPage, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := githubHTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("github call: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("github http %d: %s", resp.StatusCode, truncateBody(body))
		}
		var pageResp struct {
			Installations []struct {
				ID      int64 `json:"id"`
				Account struct {
					Login string `json:"login"`
					Type  string `json:"type"`
				} `json:"account"`
			} `json:"installations"`
		}
		if err := json.Unmarshal(body, &pageResp); err != nil {
			return nil, fmt.Errorf("github decode: %w", err)
		}
		for _, it := range pageResp.Installations {
			all = append(all, githubInstallation{ID: it.ID, Login: it.Account.Login, Type: it.Account.Type})
		}
		if len(pageResp.Installations) < perPage {
			break
		}
	}
	return all, nil
}
