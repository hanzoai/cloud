package provider

// github_user.go is a person's own GitHub, connected to their Hanzo account.
//
// An App installation is the ORG's grant: it says which accounts' repositories
// the App may touch. It cannot say which of those a PERSON may touch, so a list
// scoped to the org shows every member every repository the org's installations
// reach. GitHub's answer is the App's user access token — minted for this App,
// on the person's own authorization, reaching exactly what both the installation
// and the person reach. So each person connects their own, and what they see is
// what that token lists.
//
// It is custody, not sign-in. IAM owns who the person is; this holds a credential
// they handed over, sealed per (org, user) at the connector path every personal
// credential uses — /orgs/{org}/users/{user}/connectors/github-user/default — and
// rotated by the same single-flight refresh (refresh.go). GitHub expires the
// access token after eight hours and hands a refresh token beside it.
//
// THE CALLBACK DOES NOT COMPLETE. A signed state binds a flow to whoever started
// it, and a state is a URL anyone can hand to anyone: opened by somebody who has
// authorized the App before, a link carrying another person's state returns the
// opener's code to that person's flow with no consent screen in between. So the
// callback parks the code under an id minted AFTER GitHub answered, and sends the
// browser to the page that started the connect (back.go) or the console, whose
// signed-in session completes it. The one who
// started the flow never sees that id, and the parked code is readable only by
// the (org, user) the state names — so neither the opener nor the starter can
// finish a flow the other began.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
)

const (
	// githubUserID is the custody id of a person's GitHub connection: the
	// connections row's provider and the KMS path segment. It is not the org
	// provider "github" — that one holds App installations, one per account, and
	// every reader of it walks them as the org's; a person's token beside them
	// would be walked as an installation.
	githubUserID = "github-user"

	// githubUserPending is how long a parked authorization waits for the console
	// to complete it. GitHub's own code dies at ten minutes, so waiting longer
	// would only hold a code nothing can exchange.
	githubUserPending = 10 * time.Minute

	// githubUserCallbackPath is where GitHub returns the person. It must be one of
	// the App's registered callback URLs, exactly.
	githubUserCallbackPath = "/v1/provider/github/user/callback"
)

// githubUser is the custody shape the shared engine reads: one access token,
// and a refresh that rotates it. It is not registered — the /v1/connection
// plane neither lists it nor hands its token out; the ops here are its surface.
var githubUser = &Provider{
	ID:      githubUserID,
	Name:    "GitHub",
	Scope:   userScope,
	Secrets: []string{accessSecret},
	Refresh: githubUserRefresh,
}

// githubUserConfigured is whether a person can connect at all: the App itself,
// and its OAuth client, which is what trades their authorization for a token.
func githubUserConfigured() bool {
	c := githubCreds()
	return githubConfigured() && c.ClientID != "" && c.ClientSecret != ""
}

// ── ops ──────────────────────────────────────────────────────────────────────

// githubUserOut is a person's GitHub connection as it stands.
type githubUserOut struct {
	// Configured is whether this deployment holds the App's OAuth client. False
	// means nobody can connect here yet, and connect answers 503.
	Configured bool `json:"configured"`
	// Connected is whether the caller has connected their own GitHub account in
	// this org. False means their repositories come from nowhere: an org admin
	// still sees the org's installations, and everyone else is asked to connect.
	Connected bool `json:"connected"`
	// Login is the GitHub account they connected. Absent when not connected.
	Login string `json:"login,omitempty"`
	// ExpiresAt is when the current access token expires, RFC 3339 UTC. It is
	// rotated before it does; absent for a token GitHub does not expire.
	ExpiresAt string `json:"expiresAt,omitempty"`
	// ConnectedAt is when the connection was made, RFC 3339 UTC.
	ConnectedAt string `json:"connectedAt,omitempty"`
}

// githubUserConnectOut is where to send the person to authorize.
type githubUserConnectOut struct {
	// AuthorizeURL is GitHub's authorization page for the App. GitHub returns the
	// browser to the callback, which parks the answer and sends it on with
	// complete=github&grant=<id> — to the return page the connect named, or the
	// console's /connectors — and that page POSTs the grant to
	// /v1/provider/github/user/complete.
	AuthorizeURL string `json:"authorizeUrl"`
}

// githubUserConnectIn names where GitHub's return lands.
type githubUserConnectIn struct {
	// Return is the page the callback sends the person back to, with
	// complete=github&grant=<id> (or error=github&reason=<why>) added to its
	// query. It must name one of this deployment's return pages (on
	// api.hanzo.ai: https://hanzo.ai/?at=-/settings/integrations,
	// https://console.hanzo.ai/connectors, https://platform.hanzo.ai/connectors);
	// anything else, or nothing, returns to the console's /connectors.
	Return string `json:"return" url:"-"`
}

// githubUserCompleteIn names the parked authorization to finish.
type githubUserCompleteIn struct {
	// Grant is the id the callback handed the console in its redirect. It is
	// single-use, lives ten minutes, and is readable only by the person who
	// started the flow.
	Grant string `json:"grant" url:"-"`
}

// githubUserStatus reports whether the caller has connected their own GitHub
// account in the org they act in, and as whom.
//
// Response: {"configured":true,"connected":true,"login":"octocat","expiresAt":"2026-09-24T20:00:00Z","connectedAt":"2026-09-24T12:00:00Z"}
func (o ops) githubUserStatus(ctx context.Context, _ *cloud.Unit) (*githubUserOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	return githubUserView(ctx, o.s, org, user)
}

// githubUserView is the one rendering of a person's connection.
func githubUserView(ctx context.Context, s *cloud.Service[state], org, user string) (*githubUserOut, error) {
	out := &githubUserOut{Configured: githubUserConfigured()}
	conn, found, err := s.State.store.Get(ctx, org, user, githubUserID, defaultLabel)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if found {
		out.Connected = true
		out.Login = conn.AccountLabel
		out.ExpiresAt = text.Unix(conn.ExpiresAt)
		out.ConnectedAt = text.Unix(conn.ConnectedAt)
	}
	return out, nil
}

// githubUserConnect begins connecting the caller's own GitHub account: it answers
// the App's authorization page, carrying a state signed for this org and this
// person. Every member may connect their own; it grants nothing the person does
// not already hold on GitHub. The page shows GitHub's account picker (githubPick),
// so a connect after a Disconnect can choose another account: GitHub otherwise
// skips the page for anyone who authorized the App before.
//
// Example: {"return":"https://hanzo.ai/?at=-/settings/integrations"}
// Response: {"authorizeUrl":"https://github.com/login/oauth/authorize?client_id=Iv23li3SYLoq40ExR6EN&prompt=select_account&redirect_uri=...&state=..."}
func (o ops) githubUserConnect(ctx context.Context, in *githubUserConnectIn) (*githubUserConnectOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if !githubUserConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"connecting GitHub is not configured on this deployment: the App's OAuth client secret is missing")
	}
	// What a connect leaves behind is swept by the next one, so starting flows
	// and abandoning them cannot grow either table without bound.
	now := time.Now().Unix()
	if _, err := o.s.State.store.GCNonces(ctx, staleNonceCutoff()); err != nil {
		o.s.Log.Warn("github user: nonce sweep failed", "err", err)
	}
	if _, err := o.s.State.store.GCGrants(ctx, now); err != nil {
		o.s.Log.Warn("github user: grant sweep failed", "err", err)
	}
	nonce, err := genToken()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	if err := o.s.State.store.PutNonce(ctx, nonce, org, githubUserID, checkedBack(o.s, in.Return)); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "state: %v", err)
	}
	st, err := sign(o.s, org, user, githubUserID, nonce)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "state: %v", err)
	}
	q := url.Values{
		"client_id":    {githubCreds().ClientID},
		"redirect_uri": {githubUserRedirect(o.s)},
		"state":        {st},
		"prompt":       {githubPick},
	}
	return &githubUserConnectOut{
		AuthorizeURL: strings.TrimRight(githubWebBase, "/") + "/login/oauth/authorize?" + q.Encode(),
	}, nil
}

// githubUserRedirect is the callback address GitHub is told to return to.
func githubUserRedirect(s *cloud.Service[state]) string {
	return "https://" + s.Domain + githubUserCallbackPath
}

// githubUserCallback is where GitHub returns the person. It verifies the state,
// burns its nonce, and parks the code for the return page to complete; it
// exchanges nothing. PUBLIC and state-authed, like every OAuth return, and raw because it
// answers a browser with a redirect.
func githubUserCallback(s *cloud.Service[state], c *zip.Ctx) error {
	payload, err := verify(s, c.Query("state"), githubUserID)
	if err != nil {
		return failRedirect(s, c, "", "github", "invalid state", err)
	}
	if !validUser(payload.User) {
		return failRedirect(s, c, "", "github", "invalid state", fmt.Errorf("org %s: the state names no person", payload.Org))
	}
	back, consumed, err := s.State.store.ConsumeNonce(c.Context(), payload.Nonce, payload.Org, githubUserID)
	if err != nil {
		return failRedirect(s, c, "", "github", "state error", err)
	}
	if !consumed {
		return failRedirect(s, c, "", "github", "state already used or expired",
			fmt.Errorf("org %s: nonce not present (replayed, or GC'd after %s)", payload.Org, stateTTL))
	}
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		return failRedirect(s, c, back, "github", "authorization denied",
			fmt.Errorf("org %s: github reported %q", payload.Org, truncate(e, 200)))
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		return failRedirect(s, c, back, "github", "missing authorization code", fmt.Errorf("org %s: no code", payload.Org))
	}
	if len(code) > maxCodeLen {
		return failRedirect(s, c, back, "github", "authorization code too large",
			fmt.Errorf("org %s: %d bytes exceeds %d", payload.Org, len(code), maxCodeLen))
	}
	id, err := genToken()
	if err != nil {
		return failRedirect(s, c, back, "github", "state error", err)
	}
	now := time.Now()
	if err := s.State.store.PutGrant(c.Context(), Grant{
		ID: id, Org: payload.Org, User: payload.User, Provider: githubUserID, Label: defaultLabel,
		Code: code, CreatedAt: now.Unix(), ExpiresAt: now.Add(githubUserPending).Unix(),
	}); err != nil {
		return failRedirect(s, c, back, "github", "state error", err)
	}
	return redirect(c, returnURL(s.State.consoleURL, back, "complete", "github", "grant", id))
}

// githubUserComplete finishes connecting the caller's GitHub account: it takes
// the authorization the callback parked, trades it for the person's token, and
// seals it. Only the person who started the flow can complete it — another
// person's grant id is simply not found — and a grant completes once.
//
// Example: {"grant":"9f2c1e7a4b8d0e3f6a5b2c1d0e9f8a7b"}
// Response: {"configured":true,"connected":true,"login":"octocat","expiresAt":"2026-09-24T20:00:00Z","connectedAt":"2026-09-24T12:00:00Z"}
func (o ops) githubUserComplete(ctx context.Context, in *githubUserCompleteIn) (*githubUserOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if !githubUserConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "connecting GitHub is not configured on this deployment")
	}
	s := o.s
	id := strings.TrimSpace(in.Grant)
	if !kmsReady(s) {
		return nil, kmsUnavailable()
	}
	// Taken and burned in one statement BEFORE the exchange: one parked code is
	// one attempt, and of any number of racing completions exactly one gets it.
	g, found, err := s.State.store.TakeGrant(ctx, id, org, user)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "grant: %v", err)
	}
	if !found || g.Provider != githubUserID {
		return nil, zip.ErrNotFound("no pending GitHub connection with that grant")
	}
	res, err := githubUserExchange(ctx, githubCreds(), githubUserRedirect(s), g.Code)
	if err != nil {
		s.Log.Warn("github user: exchange failed", "org", org, "user", user, "err", err)
		// 424, not 502: the edge answers a 502 with its own page and no CORS
		// headers, and the page that completes this could then say nothing.
		return nil, zip.Errorf(http.StatusFailedDependency, "GitHub would not exchange the authorization; connect again")
	}
	if err := githubUserSave(ctx, s, org, user, res); err != nil {
		return nil, err
	}
	return githubUserView(ctx, s, org, user)
}

// githubUserDisconnectOut is what disconnecting did.
type githubUserDisconnectOut struct {
	// Disconnected is always true: the sealed token and the connection are gone
	// from this deployment. A custody failure is an HTTP error instead.
	Disconnected bool `json:"disconnected"`
	// Revoked is whether GitHub confirmed the token is dead. False means GitHub
	// could not be told — the token lives until it expires or is revoked from
	// the person's GitHub settings — or there was no token to revoke.
	Revoked bool `json:"revoked"`
}

// githubUserDisconnect forgets the caller's GitHub connection in this org: the
// token is revoked at GitHub, and the sealed secrets and the row go. It says
// whether GitHub confirmed the revocation rather than reporting a success it did
// not see. Disconnecting what is not connected succeeds.
//
// Response: {"disconnected":true,"revoked":true}
func (o ops) githubUserDisconnect(ctx context.Context, _ *cloud.Unit) (*githubUserDisconnectOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	if !kmsReady(s) {
		// The row is the only handle on the sealed token; dropping it while the
		// token stays sealed would orphan a live credential.
		return nil, kmsUnavailable()
	}
	path, err := userPath(org, user, githubUserID, defaultLabel)
	if err != nil {
		return nil, err
	}
	out := &githubUserDisconnectOut{Disconnected: true}
	if tok, gerr := kmsGet(s, path, accessSecret); gerr == nil && len(tok) > 0 && githubUserConfigured() {
		if rerr := githubUserRevoke(ctx, githubCreds(), string(tok)); rerr != nil {
			s.Log.Warn("github user: revoke failed", "org", org, "user", user, "err", rerr)
		} else {
			out.Revoked = true
		}
	}
	if err := dropPerson(ctx, s, org, user); err != nil {
		return nil, err
	}
	return out, nil
}

// dropPerson removes a person's GitHub custody in one org: both secrets, then
// the row, then their cached view. A secret that will not delete stops it with
// the row still standing, so the credential never outlives its handle.
func dropPerson(ctx context.Context, s *cloud.Service[state], org, user string) error {
	path, err := userPath(org, user, githubUserID, defaultLabel)
	if err != nil {
		return err
	}
	for _, name := range []string{accessSecret, refreshSecret} {
		if derr := kmsDelete(s, path, name); derr != nil {
			s.Log.Warn("github user: kms delete failed", "org", org, "user", user, "secret", name, "err", derr)
			return kmsUnavailable()
		}
	}
	if _, err := s.State.store.Delete(ctx, org, user, githubUserID, defaultLabel); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	repoIndex.forgetUser(org, user)
	return nil
}

// forgetPerson drops a connection GitHub has stopped honouring — revoked, or a
// refresh token past its life — so it reads as not connected everywhere at once
// and is not retried on every read with the App's client secret.
func forgetPerson(ctx context.Context, s *cloud.Service[state], org, user string) {
	s.Log.Warn("github user: GitHub no longer honours the connection; dropping it", "org", org, "user", user)
	if err := dropPerson(context.WithoutCancel(ctx), s, org, user); err != nil {
		s.Log.Warn("github user: drop failed", "org", org, "user", user, "err", err)
	}
}

// githubUserSave seals a verified token into the person's custody, dropping a
// refresh token the new grant does not carry so a stale one cannot outlive it.
func githubUserSave(ctx context.Context, s *cloud.Service[state], org, user string, res *ExchangeResult) error {
	if _, ok := res.Tokens[refreshSecret]; !ok {
		if path, err := userPath(org, user, githubUserID, defaultLabel); err == nil {
			_ = kmsDelete(s, path, refreshSecret)
		}
	}
	if _, err := saveUser(ctx, s, org, user, defaultLabel, githubUser, res); err != nil {
		return err
	}
	repoIndex.forgetUser(org, user)
	return nil
}

// errGitHubRevoked is a person's token GitHub no longer honours: revoked, or its
// refresh token expired. Every reader answers it as "not connected".
var errGitHubRevoked = errors.New("github: the connected account's token is no longer accepted")

// personToken is the caller's live GitHub token and login, rotated first
// when it is within a minute of expiring. ok is false when they have not
// connected, or when what they connected no longer opens anything — one answer
// to every reader: ask them to connect. An error is custody that could not be
// read, which is the deployment's fault and never reads as "not connected".
func personToken(ctx context.Context, s *cloud.Service[state], org, user string) (tok, login string, ok bool, err error) {
	if !validUser(user) {
		return "", "", false, nil
	}
	conn, found, err := s.State.store.Get(ctx, org, user, githubUserID, defaultLabel)
	if err != nil {
		return "", "", false, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if !found {
		return "", "", false, nil
	}
	if !kmsReady(s) {
		return "", "", false, kmsUnavailable()
	}
	next, raw, err := fresh(ctx, s, githubUser, conn, false)
	switch {
	case errors.Is(err, errGitHubRevoked):
		// GitHub refused the refresh token itself: the connection is dead.
		forgetPerson(ctx, s, org, user)
		return "", "", false, nil
	case errors.Is(err, errRefreshFailed):
		// GitHub did not answer. The connection stands; this read cannot.
		return "", "", false, zip.Errorf(http.StatusBadGateway, "GitHub could not rotate the connected account's token just now; try again")
	case err != nil:
		if he, isHTTP := httpErr(err); isHTTP && he.Status == http.StatusBadRequest {
			// Expired and holding no refresh token: nothing can rotate it.
			forgetPerson(ctx, s, org, user)
			return "", "", false, nil
		}
		return "", "", false, zip.Errorf(http.StatusServiceUnavailable, "%s", errCredentialStore)
	}
	return string(raw), next.AccountLabel, true, nil
}

// ── GitHub's OAuth endpoints ─────────────────────────────────────────────────

// githubUserExchange trades the person's authorization code for their token,
// and reads who they are with it — the read is also the proof the token works.
func githubUserExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	return githubUserGrant(ctx, url.Values{
		"client_id": {creds.ClientID}, "client_secret": {creds.ClientSecret},
		"code": {code}, "redirect_uri": {redirectURI},
	}, true)
}

// githubUserRefresh rotates a person's token. GitHub hands back a new refresh
// token with it and retires the old one, which is why the engine single-flights
// every rotation.
func githubUserRefresh(ctx context.Context, refresh string) (*ExchangeResult, error) {
	creds := githubCreds()
	if creds.ClientID == "" || creds.ClientSecret == "" {
		return nil, fmt.Errorf("github: the App's OAuth client is not configured")
	}
	return githubUserGrant(ctx, url.Values{
		"client_id": {creds.ClientID}, "client_secret": {creds.ClientSecret},
		"grant_type": {"refresh_token"}, "refresh_token": {refresh},
	}, false)
}

// githubUserGrant is one call to GitHub's token endpoint and one to /user. Its
// errors name statuses and GitHub's error code, never a body: a token endpoint's
// body is the one place a credential could ride into a log.
//
// identify reads /user with the new token — the proof it works, and the login a
// NEW connection is labelled with. A rotation skips it: GitHub retired the old
// refresh token the moment it answered, so a /user read that failed afterwards
// would throw away the only credential left, and the account is already known.
func githubUserGrant(ctx context.Context, form url.Values, identify bool) (*ExchangeResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(githubWebBase, "/")+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github oauth: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("github oauth: http %d", resp.StatusCode)
	}
	var out struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		ExpiresIn int64  `json:"expires_in"`
		Error     string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return nil, fmt.Errorf("github oauth: malformed answer")
	}
	if out.Access == "" {
		code := truncate(sanitizeMeta(out.Error), 64)
		if code == "bad_refresh_token" {
			// The refresh token itself is spent, expired or revoked — the one
			// refusal nothing retried brings back. Every other code names OUR side
			// (incorrect_client_credentials is a client secret an operator
			// rotated) and must not cost any person their connection.
			return nil, fmt.Errorf("github oauth: %s: %w", code, errGitHubRevoked)
		}
		return nil, fmt.Errorf("github oauth: %s", cmp.Or(code, "no access token"))
	}
	res := &ExchangeResult{Tokens: map[string]string{accessSecret: out.Access}}
	if identify {
		var who struct {
			Login string `json:"login"`
		}
		if err := githubGet(ctx, out.Access, "/user", &who); err != nil || who.Login == "" {
			return nil, fmt.Errorf("github oauth: the new token could not read its own account (%d)", statusOf(err))
		}
		res.AccountLabel = who.Login
	}
	if out.Refresh != "" {
		res.Tokens[refreshSecret] = out.Refresh
	}
	if out.ExpiresIn > 0 {
		res.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second).Unix()
	}
	return res, nil
}

// githubUserRevoke ends a person's token at GitHub. Authenticated as the App's
// OAuth client, because a token cannot revoke itself.
func githubUserRevoke(ctx context.Context, creds OAuthConfig, token string) error {
	body, err := json.Marshal(map[string]string{"access_token": token})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		strings.TrimRight(githubAPIBase, "/")+"/applications/"+url.PathEscape(creds.ClientID)+"/token",
		strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.SetBasicAuth(creds.ClientID, creds.ClientSecret)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("github revoke: request failed")
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("github revoke: http %d", resp.StatusCode)
	}
	return nil
}

// githubGet is one authenticated read of the GitHub API into out. A status
// other than 200 is a githubStatus carrying it; a 401 is also errGitHubRevoked,
// GitHub no longer honouring the credential. The token rides the header and
// nothing else, so no error built here can carry it.
func githubGet(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(githubAPIBase, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := githubHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("github call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return githubStatus{code: resp.StatusCode, body: truncateBody(b)}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("github decode: %w", err)
	}
	return nil
}

// githubStatus is a GitHub answer that was not 200.
type githubStatus struct {
	code int
	body string
}

func (e githubStatus) Error() string { return fmt.Sprintf("github http %d: %s", e.code, e.body) }

// Is makes a 401 errGitHubRevoked, whoever asks.
func (e githubStatus) Is(target error) bool {
	return target == errGitHubRevoked && e.code == http.StatusUnauthorized
}

// statusOf is the HTTP status a GitHub read answered, or 0 when it never did.
func statusOf(err error) int {
	var st githubStatus
	if errors.As(err, &st) {
		return st.code
	}
	return 0
}
