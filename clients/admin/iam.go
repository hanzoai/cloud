package admin

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

// iamClient reads the IAM management surface (/v1/iam/get-*) on behalf of a
// verified global-admin caller. IAM runs as its own deployment (not fused into
// this binary — see subsystems.go), so these are HTTP calls, not Go method
// dispatch. Every call REPLAYS THE CALLER'S OWN credential (session cookie +
// Authorization), so IAM authorizes the read as the same principal the gateway
// already validated as a global admin. admin adds NO service credential of
// its own here: it never widens what the caller could read directly, and IAM's
// own IsGlobalAdmin gate stays the second line of defense.
type iamClient struct {
	base string // e.g. http://iam.hanzo.svc.cluster.local:8000
	http *http.Client
}

func newIAMClient(base string) *iamClient {
	return &iamClient{
		base: strings.TrimRight(strings.TrimSpace(base), "/"),
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *iamClient) configured() bool { return c != nil && c.base != "" }

// creds is the caller's replayed authorization context: the raw Cookie header
// and Authorization bearer captured off the inbound request. IAM authenticates
// exactly as it does for the browser (credentials: 'include').
type creds struct {
	cookie string
	auth   string
}

// envelope is the uniform /v1 response shape every /v1/iam handler returns.
// data is the payload; data2 the list total (paginated reads).
type envelope struct {
	Status string          `json:"status"`
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
	Data2  json.RawMessage `json:"data2"`
}

// listResult is a decoded paginated read: the raw rows and the backend total.
type listResult struct {
	rows  json.RawMessage
	total int
}

// getList calls an IAM get-* endpoint and returns the raw data array + data2
// total. A non-ok envelope is an error (surfaced honestly to the operator).
func (c *iamClient) getList(ctx context.Context, cr creds, path string, q url.Values) (listResult, error) {
	env, err := c.get(ctx, cr, path, q)
	if err != nil {
		return listResult{}, err
	}
	total := envTotal(env.Data2, env.Data)
	return listResult{rows: env.Data, total: total}, nil
}

// get performs one authenticated GET and decodes the /v1 envelope.
func (c *iamClient) get(ctx context.Context, cr creds, path string, q url.Values) (envelope, error) {
	if !c.configured() {
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
	if cr.cookie != "" {
		req.Header.Set("Cookie", cr.cookie)
	}
	if cr.auth != "" {
		req.Header.Set("Authorization", cr.auth)
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

// getUserRaw fetches ONE user as its FULL wire object (GET /v1/iam/get-user?id=
// owner/name), preserving every field. The suspend/reactivate action reads the
// whole object, flips isForbidden, and writes it back — update-user REPLACES the
// row, so operating on the full object (not a typed subset) is what keeps every
// other field intact. Replays the caller's own credential, so IAM authorizes the
// read as the same validated global admin.
func (c *iamClient) getUserRaw(ctx context.Context, cr creds, id string) (map[string]any, error) {
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

// updateUserRaw writes a full user object back (POST /v1/iam/update-user?id=
// owner/name). The caller's replayed credential is a VALIDATED global admin, whom
// IAM's CheckPermissionForUpdateUser admits to set privileged fields (isForbidden)
// on any user — a tenant/org-admin is refused by IAM itself, so this can never be
// abused to suspend across a boundary the caller couldn't already cross. admin
// adds no service credential of its own; IAM re-checks IsGlobalAdmin.
func (c *iamClient) updateUserRaw(ctx context.Context, cr creds, id string, user map[string]any) error {
	q := url.Values{"id": {id}}
	body, err := json.Marshal(user)
	if err != nil {
		return err
	}
	_, err = c.post(ctx, cr, "/v1/iam/update-user", q, body)
	return err
}

// post performs one authenticated POST (JSON body) replaying the caller's cookie +
// bearer, and decodes the /v1 envelope. A non-ok envelope (or an IAM 401/403) is
// an error the mutation surfaces honestly + records as a failed audited attempt.
func (c *iamClient) post(ctx context.Context, cr creds, path string, q url.Values, body []byte) (envelope, error) {
	if !c.configured() {
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
	if cr.cookie != "" {
		req.Header.Set("Cookie", cr.cookie)
	}
	if cr.auth != "" {
		req.Header.Set("Authorization", cr.auth)
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
