// Package iam is the admin cockpit's typed reader for the Hanzo IAM management
// surface (/v1/iam/ native routes). IAM runs as its own deployment (not fused into this
// binary), so these are HTTP calls, not Go method dispatch. Every call REPLAYS
// THE CALLER'S OWN credential (session cookie + Authorization), so IAM authorizes
// the read as the same principal the gateway already validated as a SuperAdmin.
// admin adds NO service credential of its own here: it never widens what the
// caller could read directly, and IAM's own IsSuperAdmin gate stays the second
// line of defense.
//
// The reads split two orthogonal ways: TYPED domain reads the cockpit folds into
// its own rows — Orgs/Users (paginated lists), Org/User (one row), SetUser (the
// one write) — and a generic verbatim List the cockpit forwards field-for-field
// (roles, applications, audit records). An unwired IAM (no base) is not Ready and
// every read reports the honest not-configured error.
package iam

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client reads the IAM management surface (/v1/iam/ native routes) on behalf of a verified
// SuperAdmin caller.
type Client struct {
	base string // e.g. http://iam.hanzo.svc.cluster.local:8000
	http *http.Client
}

// New builds an IAM client for base (empty base → not Ready).
func New(base string) *Client {
	return &Client{
		base: strings.TrimRight(strings.TrimSpace(base), "/"),
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

// Ready reports whether an IAM endpoint is wired on this deployment.
func (c *Client) Ready() bool { return c != nil && c.base != "" }

// Creds is the caller's replayed authorization context: the raw Cookie header
// and Authorization bearer captured off the inbound request. IAM authenticates
// exactly as it does for the browser (credentials: 'include').
type Creds struct {
	Cookie string
	Auth   string
}

// Org is the IAM Organization subset the aggregators fold over.
type Org struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	CreatedTime string `json:"createdTime"`
}

// User is the IAM User subset mapped into OperatorUser. AccessKey is decoded
// ONLY to derive API-key PRESENCE (hasApiKey) for the customer detail — its VALUE
// is never surfaced in any admin response (the key is a credential, not a
// display field), so no secret leaves this binary.
type User struct {
	Owner          string `json:"owner"`
	Name           string `json:"name"`
	Email          string `json:"email"`
	DisplayName    string `json:"displayName"`
	Tag            string `json:"tag"`
	CreatedTime    string `json:"createdTime"`
	LastSigninTime string `json:"lastSigninTime"`
	IsAdmin        bool   `json:"isAdmin"`
	IsForbidden    bool   `json:"isForbidden"`
	AccessKey      string `json:"accessKey"`
}

// List is a decoded read of one collection: the raw rows and the backend total.
type List struct {
	Rows  json.RawMessage
	Total int
}

// List reads one IAM collection and returns its rows + total — the
// verbatim-forward primitive (roles, applications, audit records reach the
// operator field-for-field). `rows` is the key IAM names the array by, which is
// its entity's own plural: there is no generic `data` any more, so the caller
// states which collection it asked for and a body that does not carry it is an
// error rather than an empty page.
func (c *Client) List(ctx context.Context, cr Creds, path, rows string, q url.Values) (List, error) {
	body, err := c.get(ctx, cr, path, q)
	if err != nil {
		return List{}, err
	}
	return decodeList(body, rows)
}

// Orgs lists organizations (GET /v1/iam/organizations).
func (c *Client) Orgs(ctx context.Context, cr Creds, q url.Values) (List, error) {
	return c.List(ctx, cr, "/v1/iam/organizations", "organizations", q)
}

// Users lists users (GET /v1/iam/users).
func (c *Client) Users(ctx context.Context, cr Creds, q url.Values) (List, error) {
	return c.List(ctx, cr, "/v1/iam/users", "users", q)
}

// Org fetches ONE organization row (GET /v1/iam/organizations/get?owner=&name=)
// as the typed Org subset the scoped read panels fold over. Replays the caller's
// own credential, so IAM authorizes the read as the same validated principal — a
// non-super caller can only ever read their OWN org this way (the second line of the
// tenant-scope defense). Best-effort by design: the scoped-orgs fan-in tolerates an
// error and falls back to a name-only row.
func (c *Client) Org(ctx context.Context, cr Creds, owner, name string) (Org, error) {
	q := url.Values{"owner": {owner}, "name": {name}}
	body, err := c.get(ctx, cr, "/v1/iam/organizations/get", q)
	if err != nil {
		return Org{}, err
	}
	var org Org
	if err := json.Unmarshal(body, &org); err != nil {
		return Org{}, fmt.Errorf("iam organizations/get decode: %w", err)
	}
	return org, nil
}

// User fetches ONE user as its FULL wire object (GET /v1/iam/users/get?owner=&name=),
// preserving every field. The suspend/reactivate action reads the whole object,
// flips isForbidden, and writes it back — the update REPLACES the row, so
// operating on the full object (not a typed subset) is what keeps every other
// field intact. Replays the caller's own credential, so IAM authorizes the read
// as the same validated SuperAdmin.
func (c *Client) User(ctx context.Context, cr Creds, owner, name string) (map[string]any, error) {
	q := url.Values{"owner": {owner}, "name": {name}}
	body, err := c.get(ctx, cr, "/v1/iam/users/get", q)
	if err != nil {
		return nil, err
	}
	var user map[string]any
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, fmt.Errorf("iam users/get decode: %w", err)
	}
	if user == nil {
		return nil, fmt.Errorf("iam users/get %s/%s: empty", owner, name)
	}
	return user, nil
}

// SetUser writes a full user object back (POST /v1/iam/users/update).
//
// The row travels NESTED under `user`, and which row is written comes from that
// object's own owner/name — there is no id parameter to address it by, so the
// object read is the object written and the two cannot name different people.
// The caller's replayed credential is a VALIDATED SuperAdmin, whom IAM admits to
// set privileged fields (isForbidden) on any user — a tenant/org-admin is refused
// by IAM itself, so this can never be abused to suspend across a boundary the
// caller couldn't already cross. admin adds no service credential of its own.
func (c *Client) SetUser(ctx context.Context, cr Creds, user map[string]any) error {
	body, err := json.Marshal(map[string]any{"user": user})
	if err != nil {
		return err
	}
	_, err = c.post(ctx, cr, "/v1/iam/users/update", nil, body)
	return err
}

// get performs one authenticated GET and returns the resource IAM answered with.
func (c *Client) get(ctx context.Context, cr Creds, path string, q url.Values) (json.RawMessage, error) {
	if !c.Ready() {
		return nil, fmt.Errorf("iam endpoint not configured")
	}
	u := c.base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if cr.Cookie != "" {
		req.Header.Set("Cookie", cr.Cookie)
	}
	if cr.Auth != "" {
		req.Header.Set("Authorization", cr.Auth)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iam unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	return answer(resp.StatusCode, body)
}

// answer is what a typed IAM route ANSWERS WITH: the resource itself, under the
// HTTP status. There is no envelope left to unwrap — the operation's outcome IS
// the status and the body IS the value — so success returns the bytes and a
// refusal is rendered in IAM's own words.
//
// A body that is not JSON is NOT an error here: only a refusal reads it, and a
// proxy's HTML error page is more useful in the message than the status alone.
func answer(status int, body []byte) (json.RawMessage, error) {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, fmt.Errorf("iam denied (%d)", status)
	}
	if status < 200 || status >= 300 {
		var refusal struct {
			Error string `json:"error"`
			Msg   string `json:"msg"`
		}
		_ = json.Unmarshal(body, &refusal)
		return nil, fmt.Errorf("iam: %s", cmp.Or(refusal.Error, refusal.Msg,
			fmt.Sprintf("iam status %d", status)))
	}
	return json.RawMessage(body), nil
}

// post performs one authenticated POST (JSON body) replaying the caller's cookie +
// bearer, and returns the resource IAM answered with. A refusal (or an IAM
// 401/403) is an error the mutation surfaces honestly + records as a failed
// audited attempt.
func (c *Client) post(ctx context.Context, cr Creds, path string, q url.Values, body []byte) (json.RawMessage, error) {
	if !c.Ready() {
		return nil, fmt.Errorf("iam endpoint not configured")
	}
	u := c.base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if cr.Cookie != "" {
		req.Header.Set("Cookie", cr.Cookie)
	}
	if cr.Auth != "" {
		req.Header.Set("Authorization", cr.Auth)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iam unreachable: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	return answer(resp.StatusCode, respBody)
}

// decodeList reads one collection answer: the array IAM names for its entity,
// plus how many rows there are in total.
//
// The size is spelled two ways and both are read HERE, once, rather than taught
// to every caller — users, roles and audit logs answer `total` (a real unpaged
// count), organizations answer `count`, and applications answer neither, where
// the rows themselves are the whole set. An absent rows key is an ERROR: IAM
// always writes it, so a body without it is a different shape than the one asked
// for, and reporting that as an empty page is how a full directory renders blank.
func decodeList(body json.RawMessage, rows string) (List, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return List{}, fmt.Errorf("iam %s decode: %w", rows, err)
	}
	raw, ok := fields[rows]
	if !ok {
		return List{}, fmt.Errorf("iam %s: answer carries no %q", rows, rows)
	}
	var page []json.RawMessage
	if err := json.Unmarshal(raw, &page); err != nil {
		return List{}, fmt.Errorf("iam %s decode: %w", rows, err)
	}
	// An empty collection is the JSON `null` of a nil slice; the operator's own
	// envelope promises a list, so it is normalized once here.
	if page == nil {
		raw = json.RawMessage("[]")
	}
	total := len(page)
	for _, key := range []string{"total", "count"} {
		var n int
		if v, ok := fields[key]; ok && json.Unmarshal(v, &n) == nil {
			total = n
			break
		}
	}
	return List{Rows: raw, Total: total}, nil
}
