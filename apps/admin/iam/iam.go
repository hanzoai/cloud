// Package iam is the admin cockpit's typed reader for the Hanzo IAM management
// surface (/v1/iam/get-*). IAM runs as its own deployment (not fused into this
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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client reads the IAM management surface (/v1/iam/get-*) on behalf of a verified
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
// is never surfaced in any admin response (the hk- key is a credential, not a
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

// List is a decoded paginated read: the raw rows and the backend total.
type List struct {
	Rows  json.RawMessage
	Total int
}

// List calls an IAM get-* endpoint and returns the raw data array + data2 total —
// the verbatim-forward primitive (roles, applications, audit records reach the
// operator field-for-field). A non-ok envelope is an error (surfaced honestly to
// the operator).
func (c *Client) List(ctx context.Context, cr Creds, path string, q url.Values) (List, error) {
	env, err := c.get(ctx, cr, path, q)
	if err != nil {
		return List{}, err
	}
	total := envTotal(env.Data2, env.Data)
	return List{Rows: env.Data, Total: total}, nil
}

// Orgs lists organizations (GET /v1/iam/get-organizations).
func (c *Client) Orgs(ctx context.Context, cr Creds, q url.Values) (List, error) {
	return c.List(ctx, cr, "/v1/iam/get-organizations", q)
}

// Users lists users (GET /v1/iam/get-users).
func (c *Client) Users(ctx context.Context, cr Creds, q url.Values) (List, error) {
	return c.List(ctx, cr, "/v1/iam/get-users", q)
}

// Org fetches ONE organization row (GET /v1/iam/get-organization?id=owner/name)
// as the typed Org subset the scoped read panels fold over. Replays the caller's
// own credential, so IAM authorizes the read as the same validated principal — a
// non-super caller can only ever read their OWN org this way (the second line of the
// tenant-scope defense). Best-effort by design: the scoped-orgs fan-in tolerates an
// error and falls back to a name-only row.
func (c *Client) Org(ctx context.Context, cr Creds, id string) (Org, error) {
	q := url.Values{"id": {id}}
	env, err := c.get(ctx, cr, "/v1/iam/get-organization", q)
	if err != nil {
		return Org{}, err
	}
	var org Org
	if err := json.Unmarshal(env.Data, &org); err != nil {
		return Org{}, fmt.Errorf("iam get-organization decode: %w", err)
	}
	return org, nil
}

// User fetches ONE user as its FULL wire object (GET /v1/iam/get-user?id=
// owner/name), preserving every field. The suspend/reactivate action reads the
// whole object, flips isForbidden, and writes it back — update-user REPLACES the
// row, so operating on the full object (not a typed subset) is what keeps every
// other field intact. Replays the caller's own credential, so IAM authorizes the
// read as the same validated SuperAdmin.
func (c *Client) User(ctx context.Context, cr Creds, id string) (map[string]any, error) {
	q := url.Values{"id": {id}}
	env, err := c.get(ctx, cr, "/v1/iam/get-user", q)
	if err != nil {
		return nil, err
	}
	var user map[string]any
	if err := json.Unmarshal(env.Data, &user); err != nil {
		return nil, fmt.Errorf("iam get-user decode: %w", err)
	}
	if user == nil {
		return nil, fmt.Errorf("iam get-user %q: empty", id)
	}
	return user, nil
}

// SetUser writes a full user object back (POST /v1/iam/update-user?id=owner/name).
// The caller's replayed credential is a VALIDATED SuperAdmin, whom IAM's
// CheckPermissionForUpdateUser admits to set privileged fields (isForbidden) on any
// user — a tenant/org-admin is refused by IAM itself, so this can never be abused to
// suspend across a boundary the caller couldn't already cross. admin adds no service
// credential of its own; IAM re-checks IsSuperAdmin.
func (c *Client) SetUser(ctx context.Context, cr Creds, id string, user map[string]any) error {
	q := url.Values{"id": {id}}
	body, err := json.Marshal(user)
	if err != nil {
		return err
	}
	_, err = c.post(ctx, cr, "/v1/iam/update-user", q, body)
	return err
}

// envelope is the uniform /v1 response shape every /v1/iam handler returns.
// data is the payload; data2 the list total (paginated reads).
type envelope struct {
	Status string          `json:"status"`
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
	Data2  json.RawMessage `json:"data2"`
}

// get performs one authenticated GET and decodes the /v1 envelope.
func (c *Client) get(ctx context.Context, cr Creds, path string, q url.Values) (envelope, error) {
	if !c.Ready() {
		return envelope{}, fmt.Errorf("iam endpoint not configured")
	}
	u := c.base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return envelope{}, err
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
		return envelope{}, fmt.Errorf("iam unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return envelope{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return envelope{}, fmt.Errorf("iam denied (%d)", resp.StatusCode)
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return envelope{}, fmt.Errorf("iam non-envelope response (%d)", resp.StatusCode)
	}
	if env.Status != "ok" {
		msg := env.Msg
		if msg == "" {
			msg = fmt.Sprintf("iam status %d", resp.StatusCode)
		}
		return envelope{}, fmt.Errorf("iam: %s", msg)
	}
	return env, nil
}

// post performs one authenticated POST (JSON body) replaying the caller's cookie +
// bearer, and decodes the /v1 envelope. A non-ok envelope (or an IAM 401/403) is
// an error the mutation surfaces honestly + records as a failed audited attempt.
func (c *Client) post(ctx context.Context, cr Creds, path string, q url.Values, body []byte) (envelope, error) {
	if !c.Ready() {
		return envelope{}, fmt.Errorf("iam endpoint not configured")
	}
	u := c.base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return envelope{}, err
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
		return envelope{}, fmt.Errorf("iam unreachable: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return envelope{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return envelope{}, fmt.Errorf("iam denied (%d)", resp.StatusCode)
	}
	var env envelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		return envelope{}, fmt.Errorf("iam non-envelope response (%d)", resp.StatusCode)
	}
	if env.Status != "ok" {
		msg := env.Msg
		if msg == "" {
			msg = fmt.Sprintf("iam status %d", resp.StatusCode)
		}
		return envelope{}, fmt.Errorf("iam: %s", msg)
	}
	return env, nil
}

// envTotal reads data2 as the list total when present, else counts data rows.
func envTotal(data2, data json.RawMessage) int {
	if n, ok := asInt(data2); ok {
		return n
	}
	var rows []json.RawMessage
	if json.Unmarshal(data, &rows) == nil {
		return len(rows)
	}
	return 0
}

// asInt decodes a JSON number (data2 may arrive as a bare int).
func asInt(raw json.RawMessage) (int, bool) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return 0, false
	}
	if n, err := strconv.Atoi(t); err == nil {
		return n, true
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int(f), true
	}
	return 0, false
}
