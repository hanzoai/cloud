package main

// client.go — the KMS REST client for ONE face (the standalone source OR the
// cloud destination), plus the transport seam that lets tests drive cloud's REAL
// in-process /v1/kms (via zip's app.Fiber().Test) without a network listener.
//
// The tool authenticates with a per-CR org-bound bearer (owner==projectSlug),
// brokered at /v1/kms/auth/login. The SAME token is reused for the standalone GET
// and the cloud POST: after G1, cloud accepts the fleet's audiences, and both
// faces gate reads/writes on owner==:org — so the tool never holds a cross-tenant
// super-credential. A bug cannot cross tenants because the token itself is
// org-bound.
//
// Secret plaintext returned by getSecret lives in a []byte in memory only; it is
// never logged and never written to disk.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// doer is the transport seam. Production is *http.Client; tests inject an adapter
// that routes requests to cloud's in-process zip app (app.Fiber().Test), which has
// the identical Do(*http.Request) (*http.Response, error) signature.
type doer interface {
	Do(*http.Request) (*http.Response, error)
}

// errSecretNotFound distinguishes a 404 (absent record) from a transport/5xx error
// so callers can report "missing at source" rather than fail the whole run.
var errSecretNotFound = errors.New("kmsreseal: secret not found")

const (
	maxRespBytes = 4 << 20 // 4 MiB cap on any KMS response body
	httpTimeout  = 30 * time.Second
)

// kmsClient talks to one KMS face at base via the injected doer.
type kmsClient struct {
	base string
	do   doer
}

func newKMSClient(base string, d doer) *kmsClient {
	if d == nil {
		d = &http.Client{Timeout: httpTimeout}
	}
	return &kmsClient{base: strings.TrimRight(strings.TrimSpace(base), "/"), do: d}
}

// login brokers clientId/clientSecret at {base}/v1/kms/auth/login and returns the
// owner-scoped IAM bearer. The credential transits this one request; it is never
// logged. Both the standalone and cloud expose this broker.
func (c *kmsClient) login(ctx context.Context, clientID, clientSecret string) (string, error) {
	body, _ := json.Marshal(map[string]string{"clientId": clientID, "clientSecret": clientSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/kms/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	raw, status, err := c.send(req)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("login failed: status %d", status)
	}
	var resp struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("login: decode: %w", err)
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("login: empty accessToken")
	}
	return resp.AccessToken, nil
}

// getSecret GETs one record's plaintext at (org, path, env, key). It parses BOTH
// response shapes — the standalone's {"secret":{"value":...}} and cloud's
// {"value":...} — so the same method reads either face. 404 → errSecretNotFound.
func (c *kmsClient) getSecret(ctx context.Context, token string, org, path, env, key string) ([]byte, error) {
	u := c.base + "/v1/kms/secrets/" + escapeRest(restOf(path, key)) +
		"?env=" + url.QueryEscape(env)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	auth(req, token)
	raw, status, err := c.send(req)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, errSecretNotFound
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get %s/%s: status %d", path, key, status)
	}
	return decodeSecretValue(raw)
}

// listFolder LISTs the metadata (names only) at (org, path, env) — used to resolve
// folder-sync CRs (empty keys[]) into concrete keys at RUN time. Never returns a
// value (the list surface is keys-only by construction on both faces).
func (c *kmsClient) listFolder(ctx context.Context, token, org, path, env string) ([]string, error) {
	u := c.base + "/v1/kms/secrets?env=" + url.QueryEscape(env)
	if path != "" {
		u += "&path=" + url.QueryEscape(path)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	auth(req, token)
	raw, status, err := c.send(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list %s: status %d", path, status)
	}
	var resp struct {
		Secrets []struct {
			Name string `json:"name"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("list %s: decode: %w", path, err)
	}
	out := make([]string, 0, len(resp.Secrets))
	for _, s := range resp.Secrets {
		if s.Name != "" {
			out = append(out, s.Name)
		}
	}
	return out, nil
}

// putSecret POSTs (org, path, env, key, value) so CLOUD seals it under a fresh
// per-secret DEK. Idempotent (cloud upserts). env is ALWAYS explicit — cloud
// refuses an empty env. Returns nil on 200.
func (c *kmsClient) putSecret(ctx context.Context, token string, org, path, env, key string, value []byte) error {
	body, _ := json.Marshal(map[string]string{
		"path":  path,
		"name":  key,
		"env":   env,
		"value": string(value),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/kms/secrets", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	auth(req, token)
	raw, status, err := c.send(req)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("put %s/%s: status %d: %s", path, key, status, snippet(raw))
	}
	return nil
}

// health GETs /v1/kms/health and returns the status code (200 ready, 503
// health-only). Used by preflight to prove reachability.
func (c *kmsClient) health(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/kms/health", nil)
	if err != nil {
		return 0, err
	}
	_, status, err := c.send(req)
	return status, err
}

// probeStatus issues one authenticated (or unauthenticated) request to a secrets
// path and returns the status — used by preflight/verify's auth matrix. token=""
// sends no Authorization header.
func (c *kmsClient) probeStatus(ctx context.Context, method, token, org, path, env, key string) (int, error) {
	u := c.base + "/v1/kms/secrets/" + escapeRest(restOf(path, key)) +
		"?env=" + url.QueryEscape(env)
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return 0, err
	}
	auth(req, token)
	_, status, err := c.send(req)
	return status, err
}

// send performs the request through the doer, reading a bounded body.
func (c *kmsClient) send(req *http.Request) ([]byte, int, error) {
	resp, err := c.do.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	return raw, resp.StatusCode, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────────

func auth(req *http.Request, token string) {
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// restOf joins a path and key into the trailing wildcard the secrets route reads:
// "path/key" (a normal secret) or "key" (a root secret). The standalone requires a
// path segment before the name, so a root key is resolved via the folder LIST, not
// constructed here — but the join is defined so verification of a listed root key
// (whose real path the LIST returned) is exact.
func restOf(path, key string) string {
	if path == "" {
		return key
	}
	return path + "/" + key
}

// escapeRest percent-escapes each segment of a "/"-joined rest while preserving the
// separators, so a key or path segment can never smuggle a query/route character.
func escapeRest(rest string) string {
	segs := strings.Split(rest, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// decodeSecretValue extracts the plaintext from either KMS face's GET response:
// the standalone's {"secret":{"value":...}} or cloud's {"value":...}.
func decodeSecretValue(raw []byte) ([]byte, error) {
	var body struct {
		Value  string `json:"value"`
		Secret struct {
			Value string `json:"value"`
		} `json:"secret"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode secret: %w", err)
	}
	if body.Secret.Value != "" {
		return []byte(body.Secret.Value), nil
	}
	return []byte(body.Value), nil
}

// snippet returns a short, value-free hint from an error body for a failure log
// (status text only; a secret value is never in an error body from either face).
func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
