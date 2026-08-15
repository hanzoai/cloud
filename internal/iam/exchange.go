package iam

// Token exchange — how this binary hands a process the CALLER'S OWN identity.
//
// RFC 8693. The client presents the caller's token as the `subject_token` and
// receives a fresh one for the SAME subject: shorter-lived, no refresh token, and
// stamped with `azp` naming who exchanged it. Possession of the caller's token is
// the whole authorization, so this can never reach an identity that did not just
// call us — there is no "act as user X" here, only "act as the user who is on the
// phone".
//
// SHORTER, NEVER LONGER. `lifetime` asks IAM to clamp the token to the life of
// whatever it is being handed to. IAM applies it one way, so asking for more than
// the application grants changes nothing.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// exchangeGrant is the RFC 8693 grant URI and accessTokenType its token type.
const (
	exchangeGrant    = "urn:ietf:params:oauth:grant-type:token-exchange"
	accessTokenType  = "urn:ietf:params:oauth:token-type:access_token"
	exchangeMaxBody  = 1 << 20
	exchangeDeadline = 10 * time.Second
)

// Session is one exchanged credential and the identity IAM stamped on it.
//
// The identity fields are read from the token's own claims because that is the
// only account of it both ends agree on: whatever the resource server will
// resolve from this token is what these say. They are display values — a commit
// author, a shell prompt — and never an authorization input; every service that
// accepts the token verifies it again and decides for itself.
type Session struct {
	Token   string
	Expiry  time.Time
	Owner   string // the org the subject belongs to
	User    string // the username within that org
	Email   string
	Display string
}

// Exchange trades subject for a token of the same subject, living at most life.
//
// clientID/clientSecret authenticate the confidential client IAM allow-lists for
// exchange (IAM_MINT_CLIENT_ID / IAM_MINT_CLIENT_SECRET). An empty pair, or an
// empty subject, is "not configured" rather than an error to guess at — the
// caller decides whether that is fatal.
func Exchange(ctx context.Context, base, clientID, clientSecret, subject string, life time.Duration) (Session, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || clientID == "" || clientSecret == "" {
		return Session{}, fmt.Errorf("iam exchange: no client credential")
	}
	if strings.TrimSpace(subject) == "" {
		return Session{}, fmt.Errorf("iam exchange: no subject token")
	}
	form := url.Values{
		"grant_type":           {exchangeGrant},
		"client_id":            {clientID},
		"client_secret":        {clientSecret},
		"subject_token":        {subject},
		"subject_token_type":   {accessTokenType},
		"requested_token_type": {accessTokenType},
	}
	if life > 0 {
		form.Set("lifetime", fmt.Sprint(int(life.Seconds())))
	}
	ctx, cancel := context.WithTimeout(ctx, exchangeDeadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/iam/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Session{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Session{}, fmt.Errorf("iam unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, exchangeMaxBody))
	if err != nil {
		return Session{}, err
	}
	// The body carries a credential, so it is never logged and never echoed: a
	// failure reports IAM's error CODE and the status, which is what an operator
	// needs, and nothing that was minted.
	var out struct {
		Access    string `json:"access_token"`
		ExpiresIn int    `json:"expires_in"`
		Error     string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || out.Access == "" {
		reason := out.Error
		if reason == "" {
			reason = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return Session{}, fmt.Errorf("iam exchange refused: %s", reason)
	}
	s := Session{Token: out.Access}
	if out.ExpiresIn > 0 {
		s.Expiry = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	s.Owner, s.User, s.Email, s.Display = subjectOf(out.Access)
	return s, nil
}

// subjectOf reads the display identity off a token IAM has just handed back.
//
// It does NOT verify the signature, and does not need to: the bytes came from the
// issuer over this deployment's own IAM address, and what is read is used to fill
// in a git author and a shell identity. The token's AUTHORITY is established by
// every service that receives it, each of which verifies it in full.
func subjectOf(token string) (owner, user, email, display string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", "", ""
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", "", ""
	}
	var c struct {
		Owner       string `json:"owner"`
		Name        string `json:"name"`
		Preferred   string `json:"preferred_username"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return "", "", "", ""
	}
	user = c.Name
	if user == "" {
		user = c.Preferred
	}
	display = c.DisplayName
	if display == "" {
		display = user
	}
	return c.Owner, user, c.Email, display
}
