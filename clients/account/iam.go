// iam.go is the ONE HTTP path from the console subsystem to Hanzo IAM, acting as
// the confidential first-party `hanzo-console` client (client_secret_basic). It
// ports the privileged IAM primitives that console's server-only
// src/lib/server/identity.ts drove — mint/revoke/get the per-user Cloud API key and
// create/read/update an organization — so those standalone Next server routes can
// be retired and console statically exported (task #41, "True 1-binary FE").
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
package account

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
	serviceToken string // IAM_SERVICE_TOKEN — the Bearer for the admin provision endpoint
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
		serviceToken: strings.TrimSpace(os.Getenv("IAM_SERVICE_TOKEN")),
		http:         &http.Client{Timeout: 15 * time.Second},
	}
}

// provisionResult is the /v1/iam/admin/provision response: the converged org and its
// hashed credential (accessSecret shown ONCE on first mint). The org starts at a zero
// balance — usage is pre-paid, no signup grant.
type provisionResult struct {
	Org          string `json:"org"`
	AccessKey    string `json:"accessKey"`
	AccessSecret string `json:"accessSecret"`
	Error        string `json:"error"`
}

// provisionReady reports whether the service-token provisioning path is wired.
func (c *iamClient) provisionReady() bool { return c != nil && c.serviceToken != "" }

// provision drives the ONE atomic IAM onboarding op: create the org, move the named
// user in as its admin, and mint its hashed org-scoped credential — the service-token
// endpoint that replaces the create-org + move-user pair, so there is no orphan
// between two writes and a mid-flight retry converges. orgSlug is the caller's
// already-resolved slug (IAM honors it verbatim). The org starts at a zero balance.
func (c *iamClient) provision(ctx context.Context, owner, name, orgSlug string, personal bool) (provisionResult, error) {
	if !c.provisionReady() {
		return provisionResult{}, errNotConfigured
	}
	body, err := json.Marshal(map[string]any{
		"owner": owner, "name": name, "orgSlug": orgSlug, "personal": personal,
	})
	if err != nil {
		return provisionResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/iam/admin/provision", strings.NewReader(string(body)))
	if err != nil {
		return provisionResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.serviceToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return provisionResult{}, fmt.Errorf("iam unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, iamMaxBody))
	if err != nil {
		return provisionResult{}, err
	}
	var out provisionResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return provisionResult{}, fmt.Errorf("iam provision non-json response (%d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || out.Error != "" {
		msg := out.Error
		if msg == "" {
			msg = fmt.Sprintf("iam status %d", resp.StatusCode)
		}
		return provisionResult{}, fmt.Errorf("iam provision: %s", msg)
	}
	return out, nil
}

// userRow is the subset of an IAM user the onboarding path reads to resolve the
// caller's authoritative (owner, name) — a zero-org caller's owner is not on its
// token, so provision needs it from the row.
type userRow struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// getUserRow resolves the user by the caller's id (the same read the move did) into
// its authoritative (owner, name).
func (c *iamClient) getUserRow(ctx context.Context, id string) (userRow, error) {
	raw, err := c.getUser(ctx, id)
	if err != nil {
		return userRow{}, err
	}
	var row userRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return userRow{}, fmt.Errorf("iam get-user: decode: %w", err)
	}
	return row, nil
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

// ── the Cloud API key (per-user) ─────────────────────────────────────────────

// iamScopePublish is IAM's storage value for a PUBLISHABLE key
// (schema.KeyScopePublish). It is the one string the mint, the resolver and the
// ingest door already agree on; cloud reads it to tell a key's type apart.
const iamScopePublish = "publish"

// userKey is the subset of an IAM key row the key surface reads: the publishable
// identifier, the access class, and when the row last changed. NO confidential
// half — IAM masks it (schema.Key.Mask), so there is nothing here to leak.
type userKey struct {
	Name        string `json:"name"`
	AccessKey   string `json:"accessKey"`
	Scope       string `json:"scope"`
	User        string `json:"user"`
	UpdatedTime string `json:"updatedTime"`
}

// userKeys lists the keys `user` holds in `owner`, AUTHORITATIVELY from IAM.
//
// It reads the KEY ROWS, which is where a minted key actually lives. Reading the
// USER row instead was the "key never listed" bug in its second incarnation: the
// mint moved to a key row (because that is the only thing the resolvers read) while
// the read still looked at User.AccessKey, so GET reported "no key" immediately
// after a successful POST — the mint and the read never met.
//
// Owner-scoped and then filtered to the target user, because a key is filed under
// (owner, name) and the caller may only ever see their own.
func (c *iamClient) userKeys(ctx context.Context, owner, user string) ([]userKey, error) {
	if strings.TrimSpace(owner) == "" {
		return nil, nil // an owner-less (first-run) user holds no keys yet
	}
	env, err := c.do(ctx, http.MethodGet, "/v1/iam/keys", url.Values{"owner": {owner}}, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Keys []userKey `json:"keys"`
	}
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return nil, fmt.Errorf("iam keys: decode: %w", err)
	}
	mine := make([]userKey, 0, len(out.Keys))
	for _, k := range out.Keys {
		if keyBelongsTo(k, owner, user) {
			mine = append(mine, k)
		}
	}
	return mine, nil
}

// keyBelongsTo reports whether an IAM key row is the given user's. IAM files the
// row's User as a bare username or as "<owner>/<name>"; both mean the same user
// within the key's own owner (IAM refuses a cross-owner reference at write time),
// so both are accepted and nothing else is.
func keyBelongsTo(k userKey, owner, user string) bool {
	if user == "" {
		return false
	}
	return k.User == user || k.User == owner+"/"+user
}

// mintUserKey (re)generates the user's key of `typ` and returns it — shown ONCE to
// the caller (POST /v1/keys), never echoed again. IAM binds the key to `id`, so a
// caller can only ever mint their OWN.
//
// The type rides as a FIELD on the one mint. A secret key returns its confidential
// sk- half; a publishable key returns its pk- (and IAM stores no secret for it at
// all), which is the credential a browser bundle carries.
func (c *iamClient) mintUserKey(ctx context.Context, id, typ string) (string, error) {
	env, err := c.do(ctx, http.MethodPost, "/v1/iam/mint-user-keys", url.Values{"id": {id}, "type": {typ}}, nil)
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

// revokeUserKey clears the user's key of `typ` (immediate revoke; the gateway key
// cache lapses within ~5m). Scoped by the same field the mint takes.
func (c *iamClient) revokeUserKey(ctx context.Context, id, typ string) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/iam/revoke-user-keys", url.Values{"id": {id}, "type": {typ}}, nil)
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
