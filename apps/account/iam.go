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

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/iam"
)

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
	base := cloud.IAMBase()
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
// token, so provision needs it from the row — and whether they ADMIN the org they
// are in, which is what tells a home org from a place they merely landed.
type userRow struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
	// IsAdmin is IAM's org-admin bit: standing in Owner, as opposed to mere
	// membership of it. It is read from the ROW and never from a header — the
	// decision it feeds moves a user between organizations, so a caller must not
	// be able to elect their own move.
	IsAdmin bool `json:"isAdmin"`
}

// getUserRow resolves the user by the caller's id (the same read the move did) into
// its authoritative (owner, name).
func (c *iamClient) getUserRow(ctx context.Context, id string) (userRow, error) {
	owner, name := splitID(id)
	raw, err := c.getUser(ctx, owner, name)
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

// iamEnvelope carries what one IAM call answered. Only Data is read; the wire
// shape it arrived in is iam.Answer's business, not this client's.
type iamEnvelope struct {
	Data json.RawMessage `json:"data"`
}

// do performs one authenticated IAM request and hands the body to iam.Answer,
// which owns reading IAM's two wire shapes. body is an optional JSON payload (nil
// for GET/param-only POST). The response body is size-bounded and never logged
// (it may carry a freshly-minted key).
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
	data, err := iam.Answer(resp.StatusCode, raw)
	if err != nil {
		return iamEnvelope{}, err
	}
	return iamEnvelope{Data: data}, nil
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
func (c *iamClient) mintUserKey(ctx context.Context, id, typ, scope string) (string, error) {
	form := url.Values{"id": {id}, "type": {typ}}
	// Sent only when there is one: an empty scope would OVERWRITE the class IAM
	// derives for a publishable key, turning a browser key into a key that resolves
	// to a principal.
	if scope != "" {
		form.Set("scope", scope)
	}
	env, err := c.do(ctx, http.MethodPost, "/v1/iam/mint-user-keys", form, nil)
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
	// The PREFIX must match the type that was asked for. A key's prefix is what every
	// downstream reader dispatches on — a pk- resolves to an org, an sk- resolves to
	// the USER — so a mismatch is not a labelling nit, it is a session-equivalent
	// secret handed to a caller who asked for something to put in a browser bundle.
	//
	// It is reachable without anyone making a mistake: an IAM that predates the type
	// field ignores an unknown query parameter and answers with the sk- it always
	// minted. So this refuses rather than trusting deploy order, and the failure is a
	// 502 the caller sees instead of a credential in the wrong place.
	if want := prefixForType(typ); !strings.HasPrefix(out.AccessKey, want) {
		return "", fmt.Errorf("iam returned a key that is not %s (expected the %s prefix); it may not support the type field yet", typ, want)
	}
	return out.AccessKey, nil
}

// prefixForType is the one place the wire type and the credential prefix are tied
// together: publishable keys are pk-, secret keys are sk-.
func prefixForType(typ string) string {
	if typ == keyTypePublishable {
		return "pk-"
	}
	return "sk-"
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
	env, err := c.do(ctx, http.MethodGet, "/v1/iam/organizations/get", url.Values{"id": {adminOrg + "/" + slug}}, nil)
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

// iamApplication is the subset of IAM's Application this creates. IAM fills the
// rest, including the client credentials — which is the point: nothing here
// invents a secret, and nothing here reads one back.
type iamApplication struct {
	Owner        string   `json:"owner"`
	Name         string   `json:"name"`
	DisplayName  string   `json:"displayName"`
	Organization string   `json:"organization"`
	GrantTypes   []string `json:"grantTypes"`
}

// agentAppName is what an org's agent identity is called: <org>-agent, the
// <org>-<app> convention every application here follows.
func agentAppName(org string) string { return org + "-agent" }

// ensureAgentApplication gives an org the identity its sandboxed agents run as.
//
// It is created HERE because this is where an org's IAM objects are created, by
// the client that already holds the authority to create them. There is no
// bootstrap problem to solve: the credential doing the provisioning is cloud's
// own, it already exists, and creating IAM objects for an org is what it is for.
//
// PROVISIONED, NEVER PROMOTED. This makes a new application owned by the org.
// Nothing is elevated, and an agent's identity is therefore bounded by the org
// that owns it — which is what makes cross-tenant reach impossible rather than
// merely unlikely.
//
// client_credentials ALONE. An agent is a machine: it has no user to redirect,
// no code to exchange, no refresh to hold. Granting only that is what keeps the
// application from being usable as a login.
//
// IDEMPOTENT BY CONFLICT. IAM refuses a duplicate name (409) rather than
// overwriting it, so an org created twice — a retry, a re-run — finds the
// application already there and that is success, not an error. Overwriting would
// rotate a live agent's credentials as a side effect of a retry.
//
// It does NOT read the secret back, and no caller of this ever sees one. Issuing
// a token for a run is a separate act, at a separate door, reviewed separately.
func (c *iamClient) ensureAgentApplication(ctx context.Context, org string) error {
	name := agentAppName(org)
	body, err := json.Marshal(iamApplication{
		Owner:        org,
		Name:         name,
		DisplayName:  name,
		Organization: org,
		GrantTypes:   []string{"client_credentials"},
	})
	if err != nil {
		return err
	}
	// ASK FIRST. Idempotence comes from reading, not from parsing an error
	// string: this client surfaces failures as plain messages with no status, so
	// matching "already exists" would be a guess about IAM's prose.
	if got, gerr := c.do(ctx, http.MethodGet,
		"/v1/iam/applications/get", url.Values{"id": {org + "/" + name}}, nil); gerr == nil && len(got.Data) > 2 {
		return nil // already provisioned; that is the desired state
	}

	if _, err = c.do(ctx, http.MethodPost, "/v1/iam/applications", nil, body); err != nil {
		// The read above closes the ordinary case; this closes the race where two
		// creations of one org overlap. IAM refuses the duplicate rather than
		// overwriting it, which is the behaviour worth having — an overwrite would
		// rotate a live agent's credentials as a side effect of a retry.
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return err
	}
	return nil
}

// getUser reads a full user row (for the move: update-user re-submits it whole).
// It takes owner and name SEPARATELY because that is what the endpoint wants.
// Sending the `<owner>/<name>` composite as `id` — which this did — answers
// `400 field "owner" is required` for EVERY id, measured against the running
// IAM:
//
//	?id=hanzo/2d4d67ab-…  400 field "owner" is required
//	?id=hanzo/z           400 field "owner" is required
//	?owner=hanzo&name=z   200
//
// So no caller of this ever read a user row: the avatar write surfaced it
// ("photo stored but the profile could not be updated"), and moveUserToOrg has
// the same fault silently. `name` is the USERNAME — the row's own `name` field,
// "z" — not the UUID that `sub` carries.
// splitID splits the `<owner>/<name>` composite the callers carry into the two
// fields IAM's user ops actually want. A bare name (a first-run, org-less user)
// yields an empty owner, which IAM refuses with its own message rather than
// being guessed at here.
func splitID(id string) (owner, name string) {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i], id[i+1:]
	}
	return "", id
}

// nameOf resolves a user's NAME from its id within an org, for the callers whose
// only handle is the UUID `sub`. One roster read, used only after the direct
// lookup has already failed — never on the happy path.
func (c *iamClient) nameOf(ctx context.Context, owner, id string) (string, error) {
	env, err := c.do(ctx, http.MethodGet, "/v1/iam/get-users", url.Values{"owner": {owner}}, nil)
	if err != nil {
		return "", err
	}
	var rows []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(env.Data, &rows); err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.ID == id {
			return r.Name, nil
		}
	}
	return "", errNotFound
}

func (c *iamClient) getUser(ctx context.Context, owner, name string) (json.RawMessage, error) {
	// An org-less caller (first-run onboarding) has no owner to send, and this is
	// the ONE read that must still be attempted for them — resolving their
	// authoritative (owner, name) is the whole point of the call. The composite
	// form is kept for exactly that case rather than refused here, so onboarding
	// behaves as it always did; every caller that HAS an owner now sends the
	// shape IAM actually accepts.
	q := url.Values{"id": {name}}
	if owner != "" {
		q = url.Values{"owner": {owner}, "name": {name}}
	}
	env, err := c.do(ctx, http.MethodGet, "/v1/iam/users/get", q, nil)
	if err != nil && owner != "" {
		// `name` was not a username. On the direct-Bearer path the only user
		// handle a token carries is the UUID `sub`, and IAM addresses a row by
		// its NAME — so the lookup that just failed asked for a user that does
		// not exist under that spelling. The org's roster carries both, so the
		// id resolves to the name and the read is retried once.
		if n, rerr := c.nameOf(ctx, owner, name); rerr == nil && n != "" && n != name {
			env, err = c.do(ctx, http.MethodGet, "/v1/iam/users/get",
				url.Values{"owner": {owner}, "name": {n}}, nil)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil, errNotFound
	}
	return env.Data, nil
}

// setAvatar records the user's profile photo URL on their IAM row. IAM is the
// system of record for `avatar` — the console, the session claims and every other
// surface already read it from there — so this one write is what makes a new photo
// appear everywhere at once.
//
// Same whole-row re-submit as moveUserToOrg: update-user takes the entire row, so
// it is read, ONE field is changed, and it goes back. Reading first is not
// optional — a partial row would blank every field it omitted, including the
// password hash.
func (c *iamClient) setAvatar(ctx context.Context, id, photo string) error {
	owner, name := splitID(id)
	rowRaw, err := c.getUser(ctx, owner, name)
	if err != nil {
		return err
	}
	var row map[string]any
	if err := json.Unmarshal(rowRaw, &row); err != nil {
		return fmt.Errorf("iam get-user: decode: %w", err)
	}
	row["avatar"] = photo
	// avatarType tells IAM the photo is ours rather than a federated provider's, so
	// a later sign-in through GitHub does not silently overwrite what the user chose.
	row["avatarType"] = "custom"
	body, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, "/v1/iam/update-user", url.Values{"id": {id}}, body)
	return err
}

// moveUserToOrg makes the zero-org user an admin of `slug`: it re-submits the user
// row with owner=slug + isAdmin=true (update-user takes the whole row). The user's
// password travels with the row (IAM verifies against user.PasswordType first), so
// the move never locks them out. `id` is the caller's CURRENT `<owner>/<name>`.
func (c *iamClient) moveUserToOrg(ctx context.Context, id, slug string) error {
	owner, name := splitID(id)
	rowRaw, err := c.getUser(ctx, owner, name)
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

// EXISTING ORGS DO NOT HAVE ONE. This runs where an org is CREATED, so every org
// that already existed when it shipped — including hanzo's own — has no agent
// identity and will never grow one from here.
//
// That is deliberate rather than forgotten: ensureAgentApplication is idempotent,
// so the right place to also call it is wherever the identity is first READ, and
// that read does not exist yet (it belongs with issuing a run token). A boot-time
// sweep over every org would be the wrong shape — it creates applications for
// orgs that may never run an agent, and it does it on a path where a failure
// blocks startup.
//
// So: whoever writes the token issuance calls this first. It costs one read when
// the application is already there, and it means an org that predates this is
// indistinguishable from one that does not.
//
// giveOrgAnAgent provisions an org's agent identity, best-effort and LOUD.
//
// Best-effort because the org itself is the thing being created and it is fine
// without this: agents simply will not run for that org until it exists. Failing
// the whole onboarding because one application could not be registered would
// trade a working org for no org.
//
// LOUD because the consequence is invisible otherwise — an org silently missing
// its agent identity looks exactly like an org whose agents nobody has used yet,
// and the difference only surfaces months later as "why does the agent not work
// here". This is the one line that tells you.
func giveOrgAnAgent(ctx context.Context, c *iamClient, log func(string, ...any), org string) {
	if c == nil || org == "" {
		return
	}
	if err := c.ensureAgentApplication(ctx, org); err != nil && log != nil {
		log("org has no agent identity; agents will not run for it", "org", org, "err", err)
	}
}
