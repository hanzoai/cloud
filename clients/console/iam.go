// iam.go is the ONE HTTP path from the console subsystem to Hanzo IAM, acting as
// the confidential first-party `hanzo-console` client (client_secret_basic). It
// ports the privileged IAM primitives that console2's server-only
// src/lib/server/identity.ts drove — mint/revoke/get the per-user `hk-` key and
// create/read/update an organization — so those standalone Next server routes can
// be retired and console2 statically exported (task #41, "True 1-binary FE").
//
// WHY A CONFIDENTIAL CLIENT (and not the caller's own token). These ops are
// privileged: `mint-user-keys` writes a user's AccessKey, `add-organization`
// creates a tenant and moves the user in. IAM authorizes them for an app that is
// allow-listed (IAM_KEY_MINT_ALLOWED_APPS / IAM_ORG_ADMIN_APPS /
// IAM_USER_ADMIN_APPS) — the `hanzo-console` client — NOT for an arbitrary user
// bearer. So this client authenticates as that app (Basic id:secret) and always
// targets the ALREADY-VALIDATED caller (the handler resolves the principal from
// the gateway-minted X-User-Id/X-Org-Id before calling here); the caller can only
// ever act on their OWN id, never a third party's.
//
// CREDENTIALS come from server-only env (IAM_MINT_CLIENT_ID / IAM_MINT_CLIENT_SECRET,
// sourced from KMS by the deployment), never a NEXT_PUBLIC value and never the
// browser. When they are unset the subsystem is honestly "not configured" (501),
// exactly as identity.ts's mintConfigured() gate behaved — no fabricated key/org.
package console

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// defaultIAMBase is the in-cluster IAM service; overridable by IAM_URL for other
// environments and by tests (an httptest.Server URL). Mirrors identity.ts's IAM_URL.
const defaultIAMBase = "http://iam.hanzo.svc.cluster.local:8000"

// iamMaxBody bounds an IAM response read — these are small JSON envelopes (a key,
// a user row, an org row), never blobs.
const iamMaxBody = 4 << 20

// iamClient is the confidential-client caller. clientID/clientSecret authenticate
// as the `hanzo-console` app; an empty pair means "not configured" (handlers 501).
type iamClient struct {
	base         string
	clientID     string
	clientSecret string
	http         *http.Client
}

func newIAMClient() *iamClient {
	base := strings.TrimRight(strings.TrimSpace(getenv("IAM_URL", defaultIAMBase)), "/")
	return &iamClient{
		base:         base,
		clientID:     strings.TrimSpace(os.Getenv("IAM_MINT_CLIENT_ID")),
		clientSecret: strings.TrimSpace(os.Getenv("IAM_MINT_CLIENT_SECRET")),
		http:         &http.Client{Timeout: 15 * time.Second},
	}
}

// configured reports whether the confidential client is wired. Handlers 501 when
// false — the deployment simply lacks the `hanzo-console` credential (the honest
// "not configured on this deployment" state, never a fabricated result).
func (c *iamClient) configured() bool { return c != nil && c.clientID != "" && c.clientSecret != "" }

// basicAuth is the client_secret_basic header for the confidential client.
func (c *iamClient) basicAuth() string {
	return "Basic " + basicToken(c.clientID, c.clientSecret)
}

// iamEnvelope is the uniform /v1/iam response shape ({status,msg,data}). A non-ok
// status is an error surfaced honestly to the caller.
type iamEnvelope struct {
	Status string          `json:"status"`
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
}

// do performs one authenticated IAM request and decodes the /v1 envelope. body is
// an optional JSON payload (nil for GET/param-only POST). A 401/403 from IAM maps
// to a distinct denied error; a non-envelope or non-ok status is an error with the
// upstream msg. The response body is size-bounded and never logged (it may carry a
// freshly-minted key).
func (c *iamClient) do(ctx context.Context, method, path string, q url.Values, body []byte) (iamEnvelope, error) {
	if !c.configured() {
		return iamEnvelope{}, errNotConfigured
	}
	u := c.base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return iamEnvelope{}, err
	}
	req.Header.Set("Authorization", c.basicAuth())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return iamEnvelope{}, fmt.Errorf("iam unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, iamMaxBody))
	if err != nil {
		return iamEnvelope{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return iamEnvelope{}, fmt.Errorf("iam denied (%d)", resp.StatusCode)
	}
	var env iamEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return iamEnvelope{}, fmt.Errorf("iam non-envelope response (%d)", resp.StatusCode)
	}
	if env.Status != "ok" {
		msg := env.Msg
		if msg == "" {
			msg = fmt.Sprintf("iam status %d", resp.StatusCode)
		}
		return iamEnvelope{}, fmt.Errorf("iam: %s", msg)
	}
	return env, nil
}

// ── the `hk-` Cloud API key (per-user) ───────────────────────────────────────

// userKey is the subset of an IAM user row the key surface reads: the `hk-` access
// key and the last time the key row changed (updatedTime).
type userKey struct {
	AccessKey   string `json:"accessKey"`
	UpdatedTime string `json:"updatedTime"`
}

// getUserKey reads a user's CURRENT `hk-` key AUTHORITATIVELY from IAM (get-user).
// The session claim can lag a freshly-minted key (it returns '') — so GET /keys
// must read IAM, not the claim (the "key never listed" bug identity.ts documents).
// `id` is the `<owner>/<name>` composite IAM parses.
func (c *iamClient) getUserKey(ctx context.Context, id string) (userKey, error) {
	env, err := c.do(ctx, http.MethodGet, "/v1/iam/get-user", url.Values{"id": {id}}, nil)
	if err != nil {
		return userKey{}, err
	}
	var u userKey
	if err := json.Unmarshal(env.Data, &u); err != nil {
		return userKey{}, fmt.Errorf("iam get-user: decode: %w", err)
	}
	return u, nil
}

// mintUserKey (re)generates the user's `hk-` key and returns the new secret — shown
// ONCE to the caller (POST /keys), never echoed again. IAM binds the key to `id`,
// so a caller can only ever mint their OWN.
func (c *iamClient) mintUserKey(ctx context.Context, id string) (string, error) {
	env, err := c.do(ctx, http.MethodPost, "/v1/iam/mint-user-keys", url.Values{"id": {id}}, nil)
	if err != nil {
		return "", err
	}
	var out struct {
		AccessKey string `json:"accessKey"`
	}
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return "", fmt.Errorf("iam mint-user-keys: decode: %w", err)
	}
	if out.AccessKey == "" {
		return "", fmt.Errorf("iam did not return an access key")
	}
	return out.AccessKey, nil
}

// revokeUserKey clears the user's `hk-` key (immediate revoke; the gateway key
// cache lapses within ~5m).
func (c *iamClient) revokeUserKey(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/iam/revoke-user-keys", url.Values{"id": {id}}, nil)
	return err
}

// ── organizations (onboarding) ───────────────────────────────────────────────

// iamOrg is the subset of an IAM organization the onboarding surface reads/clones:
// password + locale settings, so a created org is well-formed and a moved user's
// login is unaffected.
type iamOrg struct {
	Owner                  string   `json:"owner"`
	Name                   string   `json:"name"`
	DisplayName            string   `json:"displayName"`
	PasswordType           string   `json:"passwordType,omitempty"`
	PasswordSalt           string   `json:"passwordSalt,omitempty"`
	PasswordObfuscatorType string   `json:"passwordObfuscatorType,omitempty"`
	PasswordObfuscatorKey  string   `json:"passwordObfuscatorKey,omitempty"`
	PasswordOptions        []string `json:"passwordOptions,omitempty"`
	CountryCodes           []string `json:"countryCodes,omitempty"`
	Languages              []string `json:"languages,omitempty"`
	DefaultAvatar          string   `json:"defaultAvatar,omitempty"`
	IsPersonal             bool     `json:"isPersonal,omitempty"`
	CreatedTime            string   `json:"createdTime,omitempty"`
}

// getOrganization reads an org (owned by the `admin` org) by slug; (nil,nil) when
// absent so a caller can test availability. A transport error (unreachable IAM)
// propagates so onboarding never mistakes "unreachable" for "available" and creates
// a duplicate.
func (c *iamClient) getOrganization(ctx context.Context, slug string) (*iamOrg, error) {
	env, err := c.do(ctx, http.MethodGet, "/v1/iam/get-organization", url.Values{"id": {adminOrg + "/" + slug}}, nil)
	if err != nil {
		// IAM returns status!=ok / empty data for a missing org; do() maps a not-ok
		// envelope to an "iam:" error — that means the org does not exist. A transport
		// failure ("iam unreachable"/"iam denied") is a real error and propagates.
		if strings.HasPrefix(err.Error(), "iam:") {
			return nil, nil
		}
		return nil, err
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil, nil
	}
	var o iamOrg
	if err := json.Unmarshal(env.Data, &o); err != nil {
		return nil, fmt.Errorf("iam get-organization: decode: %w", err)
	}
	return &o, nil
}

// createOrganization creates a customer org owned by the `admin` org, cloning
// password + locale settings from the caller's current org (so the org is
// well-formed and a moved user's login is unaffected). Mirrors identity.ts's
// createOrganization.
func (c *iamClient) createOrganization(ctx context.Context, o iamOrg) error {
	body, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, "/v1/iam/add-organization", nil, body)
	return err
}

// getUser reads a full user row (for the move: update-user re-submits it whole).
func (c *iamClient) getUser(ctx context.Context, id string) (json.RawMessage, error) {
	env, err := c.do(ctx, http.MethodGet, "/v1/iam/get-user", url.Values{"id": {id}}, nil)
	if err != nil {
		return nil, err
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil, errNotFound
	}
	return env.Data, nil
}

// moveUserToOrg makes the zero-org user an admin of `slug`: it re-submits the user
// row with owner=slug + isAdmin=true (update-user takes the whole row). The user's
// password travels with the row (IAM verifies against user.PasswordType first), so
// the move never locks them out. `id` is the caller's CURRENT `<owner>/<name>`.
func (c *iamClient) moveUserToOrg(ctx context.Context, id, slug string) error {
	rowRaw, err := c.getUser(ctx, id)
	if err != nil {
		return err
	}
	var row map[string]any
	if err := json.Unmarshal(rowRaw, &row); err != nil {
		return fmt.Errorf("iam get-user: decode: %w", err)
	}
	row["owner"] = slug
	row["isAdmin"] = true
	body, err := json.Marshal(row)
	if err != nil {
		return err
	}
	// update-user is keyed by the ORIGINAL id (the row's current owner/name).
	_, err = c.do(ctx, http.MethodPost, "/v1/iam/update-user", url.Values{"id": {id}}, body)
	return err
}
