package provider

// google_user.go is a person's own Google account, connected to their Hanzo
// account so mail can go out from their own Gmail, as them.
//
// The org's Google connector (google.go) is the ORG's grant: one token per org,
// Drive and Sheets, read by imports and the Sheets connector. A message sent as a
// PERSON needs that person's own grant, on their own consent screen, reaching
// their own mailbox and nothing else. So each person connects their own, exactly
// as they connect their own GitHub (github_user.go), and the grant asks for one
// thing: gmail.send — to send, never to read.
//
// It is custody, not sign-in. IAM owns who the person is; this holds a credential
// they handed over, sealed per (org, user) at the connector path every personal
// credential uses — /orgs/{org}/users/{user}/connectors/google-user/default — and
// rotated by the same single-flight refresh (refresh.go). Google expires the
// access token after an hour; the refresh token, granted offline, lives until the
// person revokes it, and Google may hand a new one with a refresh.
//
// THE CALLBACK DOES NOT COMPLETE, for the reason github_user.go gives: a state is
// a URL anyone can hand to anyone. The callback parks the code under an id minted
// after Google answered and sends the browser to the page that started the
// connect, whose signed-in session completes it.
//
// The OAuth client is the org connector's (GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET,
// KMS-synced): one Google project, one consent screen naming every scope Hanzo
// asks for, and its redirect URIs list this callback beside the org one.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
)

const (
	// googleUserID is the custody id of a person's Google connection: the
	// connections row's provider and the KMS path segment. It is not the org
	// provider "google", whose one token per org every importer reads as the org's.
	googleUserID = "google-user"

	// googleUserPending is how long a parked authorization waits for the page to
	// complete it. Google's own code dies at ten minutes.
	googleUserPending = 10 * time.Minute

	// googleUserCallbackPath is where Google returns the person. It must be one of
	// the OAuth client's authorized redirect URIs, exactly.
	googleUserCallbackPath = "/v1/provider/google/user/callback"

	// gmailSendScope is the one Gmail scope a person grants: send as them. It
	// reads nothing in their mailbox.
	gmailSendScope = "https://www.googleapis.com/auth/gmail.send"
)

// googleUserScopes is what a person's consent screen asks for: who they are (the
// From a message carries), and sending mail as them.
var googleUserScopes = []string{"openid", "email", gmailSendScope}

// googleUser is the custody shape the shared engine reads: one access token, and
// a refresh that rotates it. Not registered — the /v1/connection plane neither
// lists it nor hands its token out; the ops here and the send are its surface.
var googleUser = &Provider{
	ID:      googleUserID,
	Name:    "Google",
	Scope:   userScope,
	Secrets: []string{accessSecret},
	Refresh: googleUserRefresh,
}

// googleUserConfigured is whether a person can connect at all: the OAuth client's
// id and its secret, which is what trades their authorization for a token.
func googleUserConfigured() bool {
	c := googleCreds()
	return c.ClientID != "" && c.ClientSecret != ""
}

// ── ops ──────────────────────────────────────────────────────────────────────

// googleUserOut is a person's Google connection as it stands.
type googleUserOut struct {
	// Configured is whether this deployment holds the OAuth client. False means
	// nobody can connect here yet, and connect answers 503.
	Configured bool `json:"configured"`
	// Connected is whether the caller has connected their own Google account in
	// this org.
	Connected bool `json:"connected"`
	// Email is the Google account they connected: the From of every message sent
	// as them. Absent when not connected.
	Email string `json:"email,omitempty"`
	// Send is whether the grant lets Hanzo send mail as them. A connection is only
	// kept when it does, so this is Connected unless the person later narrowed
	// the grant at Google.
	Send bool `json:"send"`
	// ExpiresAt is when the current access token expires, RFC 3339 UTC. It is
	// rotated before it does.
	ExpiresAt string `json:"expiresAt,omitempty"`
	// ConnectedAt is when the connection was made, RFC 3339 UTC.
	ConnectedAt string `json:"connectedAt,omitempty"`
}

// googleUserConnectOut is where to send the person to authorize.
type googleUserConnectOut struct {
	// AuthorizeURL is Google's consent page. Google returns the browser to the
	// callback, which parks the answer and sends it on with
	// complete=google&grant=<id> — to the return page the connect named, or the
	// console's /connectors — and that page POSTs the grant to
	// /v1/provider/google/user/complete.
	AuthorizeURL string `json:"authorizeUrl"`
}

// googleUserConnectIn names where Google's return lands.
type googleUserConnectIn struct {
	// Return is the page the callback sends the person back to, with
	// complete=google&grant=<id> (or error=google&reason=<why>) added to its
	// query. It must name one of this deployment's return pages; anything else,
	// or nothing, returns to the console's /connectors.
	Return string `json:"return" url:"-"`
}

// googleUserCompleteIn names the parked authorization to finish.
type googleUserCompleteIn struct {
	// Grant is the id the callback handed the page in its redirect. It is
	// single-use, lives ten minutes, and is readable only by the person who
	// started the flow.
	Grant string `json:"grant" url:"-"`
}

// googleUserStatus reports whether the caller has connected their own Google
// account in the org they act in, and as whom.
//
// Response: {"configured":true,"connected":true,"email":"ada@example.com","send":true,"expiresAt":"2026-10-26T13:00:00Z","connectedAt":"2026-10-26T12:00:00Z"}
func (o ops) googleUserStatus(ctx context.Context, _ *cloud.Unit) (*googleUserOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	return googleUserView(ctx, o.s, org, user)
}

// googleUserView is the one rendering of a person's connection.
func googleUserView(ctx context.Context, s *cloud.Service[state], org, user string) (*googleUserOut, error) {
	out := &googleUserOut{Configured: googleUserConfigured()}
	conn, found, err := s.State.store.Get(ctx, org, user, googleUserID, defaultLabel)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if found {
		out.Connected = true
		out.Email = conn.AccountLabel
		out.Send = slices.Contains(conn.Scopes, gmailSendScope)
		out.ExpiresAt = text.Unix(conn.ExpiresAt)
		out.ConnectedAt = text.Unix(conn.ConnectedAt)
	}
	return out, nil
}

// googleUserConnect begins connecting the caller's own Google account: it answers
// Google's consent page, asking offline access to send mail as them, carrying a
// state signed for this org and this person. Every member may connect their own;
// it grants nothing the person does not already hold at Google.
//
// Example: {"return":"https://hanzo.ai/?at=-/settings/integrations"}
// Response: {"authorizeUrl":"https://accounts.google.com/o/oauth2/v2/auth?client_id=...&scope=openid+email+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fgmail.send&state=..."}
func (o ops) googleUserConnect(ctx context.Context, in *googleUserConnectIn) (*googleUserConnectOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if !googleUserConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"connecting Google is not configured on this deployment: the OAuth client secret is missing")
	}
	now := time.Now().Unix()
	if _, err := o.s.State.store.GCNonces(ctx, staleNonceCutoff()); err != nil {
		o.s.Log.Warn("google user: nonce sweep failed", "err", err)
	}
	if _, err := o.s.State.store.GCGrants(ctx, now); err != nil {
		o.s.Log.Warn("google user: grant sweep failed", "err", err)
	}
	nonce, err := genToken()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	if err := o.s.State.store.PutNonce(ctx, nonce, org, googleUserID, checkedBack(o.s, in.Return)); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "state: %v", err)
	}
	st, err := sign(o.s, org, user, googleUserID, nonce)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "state: %v", err)
	}
	// offline + consent: Google hands a refresh token only on a consent it shows,
	// and a send scheduled for later needs one.
	q := url.Values{
		"client_id":     {googleCreds().ClientID},
		"redirect_uri":  {googleUserRedirect(o.s)},
		"response_type": {"code"},
		"scope":         {strings.Join(googleUserScopes, " ")},
		"state":         {st},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
	}
	return &googleUserConnectOut{AuthorizeURL: googleAuthURL + "?" + q.Encode()}, nil
}

// googleUserRedirect is the callback address Google is told to return to.
func googleUserRedirect(s *cloud.Service[state]) string {
	return "https://" + s.Domain + googleUserCallbackPath
}

// googleUserCallback is where Google returns the person. It verifies the state,
// burns its nonce, and parks the code for the return page to complete; it
// exchanges nothing. PUBLIC and state-authed, like every OAuth return, and raw
// because it answers a browser with a redirect.
func googleUserCallback(s *cloud.Service[state], c *zip.Ctx) error {
	payload, err := verify(s, c.Query("state"), googleUserID)
	if err != nil {
		return failRedirect(s, c, "", "google", "invalid state", err)
	}
	if !validUser(payload.User) {
		return failRedirect(s, c, "", "google", "invalid state", fmt.Errorf("org %s: the state names no person", payload.Org))
	}
	back, consumed, err := s.State.store.ConsumeNonce(c.Context(), payload.Nonce, payload.Org, googleUserID)
	if err != nil {
		return failRedirect(s, c, "", "google", "state error", err)
	}
	if !consumed {
		return failRedirect(s, c, "", "google", "state already used or expired",
			fmt.Errorf("org %s: nonce not present (replayed, or GC'd after %s)", payload.Org, stateTTL))
	}
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		return failRedirect(s, c, back, "google", "authorization denied",
			fmt.Errorf("org %s: google reported %q", payload.Org, truncate(e, 200)))
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		return failRedirect(s, c, back, "google", "missing authorization code", fmt.Errorf("org %s: no code", payload.Org))
	}
	if len(code) > maxCodeLen {
		return failRedirect(s, c, back, "google", "authorization code too large",
			fmt.Errorf("org %s: %d bytes exceeds %d", payload.Org, len(code), maxCodeLen))
	}
	id, err := genToken()
	if err != nil {
		return failRedirect(s, c, back, "google", "state error", err)
	}
	now := time.Now()
	if err := s.State.store.PutGrant(c.Context(), Grant{
		ID: id, Org: payload.Org, User: payload.User, Provider: googleUserID, Label: defaultLabel,
		Code: code, CreatedAt: now.Unix(), ExpiresAt: now.Add(googleUserPending).Unix(),
	}); err != nil {
		return failRedirect(s, c, back, "google", "state error", err)
	}
	return redirect(c, returnURL(s.State.consoleURL, back, "complete", "google", "grant", id))
}

// googleUserComplete finishes connecting the caller's Google account: it takes
// the authorization the callback parked, trades it for the person's tokens, and
// seals them. Only the person who started the flow can complete it — another
// person's grant id is simply not found — and a grant completes once. A grant
// that leaves out sending mail, or offline access, is refused and nothing is
// kept: a connection that cannot send is not one this surface offers.
//
// Example: {"grant":"9f2c1e7a4b8d0e3f6a5b2c1d0e9f8a7b"}
// Response: {"configured":true,"connected":true,"email":"ada@example.com","send":true,"expiresAt":"2026-10-26T13:00:00Z","connectedAt":"2026-10-26T12:00:00Z"}
func (o ops) googleUserComplete(ctx context.Context, in *googleUserCompleteIn) (*googleUserOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if !googleUserConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "connecting Google is not configured on this deployment")
	}
	s := o.s
	if !kmsReady(s) {
		return nil, kmsUnavailable()
	}
	g, found, err := s.State.store.TakeGrant(ctx, strings.TrimSpace(in.Grant), org, user)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "grant: %v", err)
	}
	if !found || g.Provider != googleUserID {
		return nil, zip.ErrNotFound("no pending Google connection with that grant")
	}
	res, err := googleUserExchange(ctx, googleCreds(), googleUserRedirect(s), g.Code)
	if err != nil {
		s.Log.Warn("google user: exchange failed", "org", org, "user", user, "err", err)
		// 424, not 502: the edge answers a 502 with its own page and no CORS
		// headers, and the page that completes this could then say nothing.
		return nil, zip.Errorf(http.StatusFailedDependency, "Google would not exchange the authorization; connect again")
	}
	if !slices.Contains(res.Scopes, gmailSendScope) {
		_ = googleRevoke(context.WithoutCancel(ctx), googleCreds(), grantToken(res))
		return nil, zip.Errorf(http.StatusFailedDependency, "sending mail was not allowed; connect again and allow Hanzo to send email as you")
	}
	if res.Tokens[refreshSecret] == "" {
		_ = googleRevoke(context.WithoutCancel(ctx), googleCreds(), grantToken(res))
		return nil, zip.Errorf(http.StatusFailedDependency, "Google granted no offline access; connect again")
	}
	if _, err := saveUser(ctx, s, org, user, defaultLabel, googleUser, res); err != nil {
		return nil, err
	}
	if err := recordGoogleUser(ctx, s, org, user, "provider.google_user.connect", res.AccountLabel); err != nil {
		// A grant the trail did not take is not kept: dropped here, and dead at Google.
		_ = googleRevoke(context.WithoutCancel(ctx), googleCreds(), grantToken(res))
		if derr := dropGoogle(context.WithoutCancel(ctx), s, org, user); derr != nil {
			s.Log.Warn("google user: drop after an unrecorded connect failed", "org", org, "user", user, "err", derr)
		}
		return nil, err
	}
	return googleUserView(ctx, s, org, user)
}

// grantToken is the token that revokes a grant whole: the refresh token when there is
// one, else the access token.
func grantToken(res *ExchangeResult) string {
	if t := res.Tokens[refreshSecret]; t != "" {
		return t
	}
	return res.Tokens[accessSecret]
}

// googleUserDisconnectOut is what disconnecting did.
type googleUserDisconnectOut struct {
	// Disconnected is always true: the sealed tokens and the connection are gone
	// from this deployment. A custody failure is an HTTP error instead.
	Disconnected bool `json:"disconnected"`
	// Revoked is whether Google confirmed the grant is dead. False means Google
	// could not be told — the grant lives until the person removes it from their
	// Google account — or there was nothing to revoke.
	Revoked bool `json:"revoked"`
}

// googleUserDisconnect forgets the caller's Google connection in this org: the
// grant is revoked at Google, and the sealed secrets and the row go. It says
// whether Google confirmed the revocation rather than reporting a success it did
// not see. Disconnecting what is not connected succeeds.
//
// Response: {"disconnected":true,"revoked":true}
func (o ops) googleUserDisconnect(ctx context.Context, _ *cloud.Unit) (*googleUserDisconnectOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	if !kmsReady(s) {
		// The row is the only handle on the sealed tokens; dropping it while they
		// stay sealed would orphan a live credential.
		return nil, kmsUnavailable()
	}
	path, err := userPath(org, user, googleUserID, defaultLabel)
	if err != nil {
		return nil, err
	}
	_, found, err := s.State.store.Get(ctx, org, user, googleUserID, defaultLabel)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	out := &googleUserDisconnectOut{Disconnected: true}
	tok, _ := kmsGet(s, path, refreshSecret)
	if len(tok) == 0 {
		tok, _ = kmsGet(s, path, accessSecret)
	}
	if len(tok) > 0 {
		if rerr := googleRevoke(ctx, googleCreds(), string(tok)); rerr != nil {
			s.Log.Warn("google user: revoke failed", "org", org, "user", user, "err", rerr)
		} else {
			out.Revoked = true
		}
	}
	if err := dropGoogle(ctx, s, org, user); err != nil {
		return nil, err
	}
	if found {
		// The connection is gone either way: a trail that did not take the record
		// is said in the log, and the disconnect stands.
		if err := recordGoogleUser(ctx, s, org, user, "provider.google_user.disconnect", ""); err != nil {
			s.Log.Error("google user: disconnect not recorded", "org", org, "user", user, "err", err)
		}
	}
	return out, nil
}

// dropGoogle removes a person's Google custody in one org: both secrets, then the
// row. A secret that will not delete stops it with the row still standing, so the
// credential never outlives its handle.
func dropGoogle(ctx context.Context, s *cloud.Service[state], org, user string) error {
	path, err := userPath(org, user, googleUserID, defaultLabel)
	if err != nil {
		return err
	}
	for _, name := range []string{accessSecret, refreshSecret} {
		if derr := kmsDelete(s, path, name); derr != nil {
			s.Log.Warn("google user: kms delete failed", "org", org, "user", user, "secret", name, "err", derr)
			return kmsUnavailable()
		}
	}
	if _, err := s.State.store.Delete(ctx, org, user, googleUserID, defaultLabel); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	return nil
}

// forgetGoogle drops a connection Google has stopped honouring — revoked, or a
// refresh token past its life — so it reads as not connected everywhere at once
// and is not retried with the client secret on every send.
func forgetGoogle(ctx context.Context, s *cloud.Service[state], org, user string) {
	s.Log.Warn("google user: Google no longer honours the connection; dropping it", "org", org, "user", user)
	if err := dropGoogle(context.WithoutCancel(ctx), s, org, user); err != nil {
		s.Log.Warn("google user: drop failed", "org", org, "user", user, "err", err)
	}
}

// recordGoogleUser writes the one record a connect or disconnect leaves: who, in
// which org, and the account. A process holding no trail logs it instead.
func recordGoogleUser(ctx context.Context, s *cloud.Service[state], org, user, action, email string) error {
	if s.State.audit == nil {
		s.Log.Info("google user: "+action, "org", org, "user", user, "account", email)
		return nil
	}
	after, _ := json.Marshal(map[string]string{"account": email, "scope": gmailSendScope})
	if _, err := s.State.audit.Append(ctx, audit.Record{
		Actor:    audit.Actor{Org: org, Sub: user},
		Action:   action,
		Resource: audit.Resource{Type: "connection", ID: googleUserID},
		Auth:     audit.AuthContext{Method: "session"},
		Outcome:  audit.Outcome{Result: "success", Status: http.StatusOK},
		After:    audit.Redact(after),
	}); err != nil {
		s.Log.Error("google user: audit write failed", "org", org, "user", user, "action", action, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "the audit trail could not record the connection")
	}
	return nil
}

// errGoogleRevoked is a person's grant Google no longer honours: revoked, or its
// refresh token expired. Every reader answers it as "not connected".
var errGoogleRevoked = errors.New("google: the connected account's grant is no longer accepted")

// personGoogle is the caller's live Google token and address, rotated first when
// it is within a minute of expiring, or rotated whatever its expiry when force is
// set (Google refused it). ok is false when they have not connected, or when what
// they connected no longer opens anything. An error is custody that could not be
// read, or Google not answering, and never reads as "not connected".
func personGoogle(ctx context.Context, s *cloud.Service[state], org, user string, force bool) (tok, email string, ok bool, err error) {
	if !validUser(user) {
		return "", "", false, nil
	}
	conn, found, err := s.State.store.Get(ctx, org, user, googleUserID, defaultLabel)
	if err != nil {
		return "", "", false, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if !found {
		return "", "", false, nil
	}
	if !kmsReady(s) {
		return "", "", false, kmsUnavailable()
	}
	next, raw, err := fresh(ctx, s, googleUser, conn, force)
	switch {
	case errors.Is(err, errGoogleRevoked):
		forgetGoogle(ctx, s, org, user)
		return "", "", false, nil
	case errors.Is(err, errRefreshFailed):
		return "", "", false, zip.Errorf(http.StatusBadGateway, "Google could not rotate the connected account's token just now; try again")
	case err != nil:
		if he, isHTTP := httpErr(err); isHTTP && he.Status == http.StatusBadRequest {
			// Expired and holding no refresh token: nothing can rotate it.
			forgetGoogle(ctx, s, org, user)
			return "", "", false, nil
		}
		return "", "", false, zip.Errorf(http.StatusServiceUnavailable, "%s", errCredentialStore)
	}
	return string(raw), next.AccountLabel, true, nil
}

// ── Google's OAuth endpoints ─────────────────────────────────────────────────

// googleUserExchange trades the person's authorization code for their tokens and
// reads who they are with the new access token — the read is also the proof it
// works, and the address a message is sent from.
func googleUserExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	res, err := googleUserGrant(ctx, url.Values{
		"client_id": {creds.ClientID}, "client_secret": {creds.ClientSecret},
		"code": {code}, "redirect_uri": {redirectURI}, "grant_type": {"authorization_code"},
	})
	if err != nil {
		return nil, err
	}
	id, email := googleAccount(ctx, res.Tokens[accessSecret])
	if email == "" {
		return nil, fmt.Errorf("google oauth: the new token could not read its own account")
	}
	res.ExternalID, res.AccountLabel = id, email
	return res, nil
}

// googleUserRefresh rotates a person's access token. Google may hand a new refresh
// token with it; when it does, it is sealed in the old one's place.
func googleUserRefresh(ctx context.Context, refresh string) (*ExchangeResult, error) {
	creds := googleCreds()
	if creds.ClientID == "" || creds.ClientSecret == "" {
		return nil, fmt.Errorf("google: the OAuth client is not configured")
	}
	return googleUserGrant(ctx, url.Values{
		"client_id": {creds.ClientID}, "client_secret": {creds.ClientSecret},
		"grant_type": {"refresh_token"}, "refresh_token": {refresh},
	})
}

// googleUserGrant is one call to Google's token endpoint. Its errors name a status
// and Google's error code, never a body: a token endpoint's body is the one place a
// credential could ride into a log.
func googleUserGrant(ctx context.Context, form url.Values) (*ExchangeResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := googleHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google oauth: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	var out googleTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil && resp.StatusCode/100 == 2 {
		return nil, fmt.Errorf("google oauth: malformed answer")
	}
	if out.AccessToken == "" {
		code := truncate(sanitizeMeta(out.Error), 64)
		if code == "invalid_grant" {
			// The refresh token itself is spent, expired or revoked — the one
			// refusal nothing retried brings back. Every other code names OUR side
			// (invalid_client is a secret an operator rotated) and must not cost any
			// person their connection.
			return nil, fmt.Errorf("google oauth: %s: %w", code, errGoogleRevoked)
		}
		if code == "" {
			code = fmt.Sprintf("http %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("google oauth: %s", code)
	}
	res := &ExchangeResult{Tokens: map[string]string{accessSecret: out.AccessToken}}
	if out.Scope != "" {
		// A refresh that names no scope keeps the connection's (fresh adopts a nil one).
		res.Scopes = strings.Fields(out.Scope)
	}
	if out.RefreshToken != "" {
		res.Tokens[refreshSecret] = out.RefreshToken
	}
	if out.ExpiresIn > 0 {
		res.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second).Unix()
	}
	return res, nil
}
