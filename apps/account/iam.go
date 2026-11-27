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
// A READ of an org's API keys is the exception, and it goes as the caller: IAM
// confines this client's own reads to the org it serves, so whose keys a customer
// sees is decided by IAM from the customer's token (orgKeys).
//
// CREDENTIALS come from server-only env (IAM_MINT_CLIENT_ID / IAM_MINT_CLIENT_SECRET,
// sourced from KMS by the deployment), never a NEXT_PUBLIC value and never the
// browser. When they are unset the subsystem is honestly "not configured" (501),
// exactly as identity.ts's mintConfigured() gate behaved — no fabricated key/org.

package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/authz"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/iam"
)

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
	base := iam.Base()
	return &iamClient{
		base:         base,
		clientID:     env.Or("IAM_MINT_CLIENT_ID", ""),
		clientSecret: env.Or("IAM_MINT_CLIENT_SECRET", ""),
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

// iamEnvelope carries what one IAM call answered. Only Data is read; the wire
// shape it arrived in is iam.Answer's business, not this client's.
type iamEnvelope struct {
	Data json.RawMessage `json:"data"`
}

// do performs one IAM request as the confidential client.
func (c *iamClient) do(ctx context.Context, method, path string, q url.Values, body []byte) (iamEnvelope, error) {
	if !c.configured() {
		return iamEnvelope{}, errNotConfigured
	}
	return c.send(ctx, c.basicAuth(), method, path, q, body)
}

// send performs one IAM request carrying auth as its Authorization and hands the
// body to iam.Answer, which owns reading IAM's two wire shapes. body is an
// optional JSON payload (nil for GET/param-only POST). The response body is
// size-bounded and never logged (it may carry a freshly-minted key).
func (c *iamClient) send(ctx context.Context, auth, method, path string, q url.Values, body []byte) (iamEnvelope, error) {
	if err := supported(ctx, method); err != nil {
		return iamEnvelope{}, err
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
	req.Header.Set("Authorization", auth)
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
		if resp.StatusCode < http.StatusInternalServerError {
			return iamEnvelope{}, refusal{err, resp.StatusCode}
		}
		return iamEnvelope{}, err
	}
	return iamEnvelope{Data: data}, nil
}

// refusal is an answer below 500 that declined the request: IAM, or its Guard in
// front of it, turned the request down. A 5xx, which a proxy between cloud and IAM
// can also send, and a request that got no answer at all are not wrapped: whether
// those wrote anything is unknown. The message and errors.Is are unchanged.
// status is IAM's answer: a 4xx, or a 2xx carrying an error envelope.
type refusal struct {
	error
	status int
}

func (r refusal) Unwrap() error { return r.error }

// ── API keys ─────────────────────────────────────────────────────────────────

// iamScopePublish is IAM's storage value for a PUBLISHABLE key
// (schema.KeyScopePublish). It is the one string the mint, the resolver and the
// ingest endpoint already agree on; cloud reads it to tell a key's type apart.
const iamScopePublish = "publish"

// keyRevoked is the State IAM gives a revoked key (schema.KeyStateRevoked). The
// row stays, so the key is still listed; the resolvers refuse it.
const keyRevoked = "Revoked"

// userKey is an IAM key row as cloud reads it. NO confidential half: IAM masks it
// on every read (schema.Key.Mask), so there is nothing here to leak.
type userKey struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	AccessKey   string `json:"accessKey"`
	Scope       string `json:"scope"`
	User        string `json:"user"`
	State       string `json:"state"`
	ExpireTime  string `json:"expireTime"`
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
	Prefix      string `json:"prefix"`
	Revoker     string `json:"revoker"`
	RevokeTime  string `json:"revokeTime"`
	UsedTime    string `json:"usedTime"`

	// row is the object IAM answered, whole. IAM's PUT replaces every editable
	// field, so an edit writes this back with its changes made, and a field this
	// client does not model survives the round trip untouched.
	row map[string]any
}

// orgKeys lists the key rows filed in owner that the caller may see, newest first,
// AUTHORITATIVELY from IAM.
//
// It reads AS THE CALLER, with the token they presented (bearer), so IAM decides
// which rows from who they are: every key of an org they administer, the keys they
// hold in an org they belong to, and a refusal anywhere else. The confidential
// client's own credential reads only the org it serves, so it cannot answer for a
// customer of any other org, and letting it read further would make one client
// secret a reader of every tenant's keys.
//
// A caller who presented an API key holds no token IAM accepts (bearer is empty), so
// their read is the client's and IAM bounds it to the org that client serves.
//
// It reads the KEY ROWS, which is where a minted key lives. Reading the USER row
// instead was the "key never listed" bug: the mint wrote a key row while the read
// looked at User.AccessKey, so GET reported "no key" right after a POST.
func (c *iamClient) orgKeys(ctx context.Context, owner, bearer string) ([]userKey, error) {
	if strings.TrimSpace(owner) == "" {
		return nil, nil // an owner-less (first-run) user holds no keys yet
	}
	q := url.Values{"owner": {owner}}
	var env iamEnvelope
	var err error
	if bearer != "" {
		env, err = c.send(ctx, "Bearer "+bearer, http.MethodGet, "/v1/iam/keys", q, nil)
	} else {
		env, err = c.do(ctx, http.MethodGet, "/v1/iam/keys", q, nil)
	}
	if err != nil {
		return nil, err
	}
	var out struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return nil, fmt.Errorf("iam keys: decode: %w", err)
	}
	keys := make([]userKey, len(out.Keys))
	for i, raw := range out.Keys {
		if err := json.Unmarshal(raw, &keys[i]); err != nil {
			return nil, fmt.Errorf("iam keys: decode: %w", err)
		}
		if err := json.Unmarshal(raw, &keys[i].row); err != nil {
			return nil, fmt.Errorf("iam keys: decode: %w", err)
		}
	}
	return keys, nil
}

// keyBelongsTo reports whether an IAM key row is the holder's. holder is
// "<home>/<username>". IAM files a row's User as a bare username or as
// "<owner>/<name>"; within the key's own org a bare name means that org's user, so
// both spellings are accepted for a holder at home. A member's row always names
// their home, and nothing else is accepted.
func keyBelongsTo(k userKey, owner, holder string) bool {
	home, user, ok := strings.Cut(holder, "/")
	if !ok || user == "" {
		return false
	}
	if home != owner {
		return k.User == holder
	}
	return k.User == user || k.User == holder
}

// putKey writes a listed key's row, with its changes made, as the confidential
// client, and answers the row as IAM stored it. The credential is not reissued.
//
// The client writes because the write is a credential act on the holder's behalf
// (key-mint), which IAM admits in any tenant; the caller's own listing already
// settled which row they may change.
func (c *iamClient) putKey(ctx context.Context, owner, name string, row map[string]any) (userKey, error) {
	payload, err := json.Marshal(row)
	if err != nil {
		return userKey{}, err
	}
	env, err := c.do(ctx, http.MethodPut, keyPath(owner, name), nil, payload)
	if err != nil {
		return userKey{}, err
	}
	var k userKey
	if err := json.Unmarshal(env.Data, &k); err != nil {
		return userKey{}, fmt.Errorf("iam key: decode: %w", err)
	}
	return k, nil
}

func keyPath(owner, name string) string {
	return "/v1/iam/keys/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// newKey is what a create asks IAM to file: the row's (owner, name), its holder,
// the person's label, the class-and-reach scope, and when it expires.
type newKey struct {
	Owner   string
	Name    string
	Holder  string
	Label   string
	Type    string
	Scope   string
	Expires string
}

// createKey mints a NEW key row and returns the row and the credential, which is
// shown ONCE to the caller and never again. It never touches another row: a name
// already taken is IAM's 409, answered as an error, so creating can never
// invalidate a key that is in use. IAM binds the key to its holder, so a caller
// mints only their OWN; in an org they joined by membership IAM admits the row
// only for a member.
//
// A secret key returns its confidential sk- half; a publishable key returns its
// pk- (and IAM stores no secret for it at all), which is what a browser carries.
func (c *iamClient) createKey(ctx context.Context, in newKey) (userKey, string, error) {
	if home, user, ok := strings.Cut(in.Holder, "/"); !ok || home == "" || user == "" || in.Owner == "" || in.Name == "" {
		return userKey{}, "", fmt.Errorf("iam mint: %q in %q is not an owner/user id", in.Holder, in.Owner)
	}
	payload, err := json.Marshal(map[string]string{
		"owner": in.Owner, "name": in.Name, "user": in.Holder,
		"displayName": in.Label, "scope": in.Scope, "state": "Active",
		"expireTime": in.Expires,
	})
	if err != nil {
		return userKey{}, "", err
	}
	env, err := c.do(ctx, http.MethodPost, "/v1/iam/keys", nil, payload)
	if err != nil {
		return userKey{}, "", err
	}
	var out struct {
		userKey
		AccessSecret string `json:"accessSecret"`
	}
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return userKey{}, "", fmt.Errorf("iam create key: decode: %w", err)
	}

	// WHICH HALF IS THE CREDENTIAL DEPENDS ON THE TYPE, and reading the wrong
	// field is how a caller asking for a browser key gets a session-equivalent
	// secret. IAM mints a pk- accessKey on EVERY row and adds an sk- accessSecret
	// only for a confidential one; the secret is returned exactly once, here.
	got := out.AccessKey
	if in.Type == keyTypePublishable {
		// A publish-scoped row is minted with NO secret at all. One that comes back
		// carrying a confidential half means IAM did not honour the scope, and the
		// row in its table is a session-equivalent credential under a name its
		// holder believes is publishable. Refuse loudly rather than hand back the pk-.
		if out.AccessSecret != "" {
			return userKey{}, "", fmt.Errorf("iam minted a secret half for a %s key; it may not honour the publish scope yet", in.Type)
		}
	} else {
		got = out.AccessSecret
	}
	if got == "" {
		return userKey{}, "", fmt.Errorf("iam did not return a %s key", in.Type)
	}

	// The PREFIX must match the type that was asked for. A key's prefix is what every
	// downstream reader dispatches on — a pk- resolves to an org, an sk- resolves to
	// the USER — so a mismatch is a session-equivalent secret handed to a caller who
	// asked for something to put in a browser bundle.
	if want := prefixForType(in.Type); !strings.HasPrefix(got, want) {
		return userKey{}, "", fmt.Errorf("iam returned a key that is not %s (expected the %s prefix)", in.Type, want)
	}
	return out.userKey, got, nil
}

// prefixForType is the one place the wire type and the credential prefix are tied
// together: publishable keys are pk-, secret keys are sk-.
func prefixForType(typ string) string {
	if typ == keyTypePublishable {
		return "pk-"
	}
	return "sk-"
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
	// A 404 is IAM answering that there is no such org. Anything else — a
	// refusal, a transport failure — propagates, because reading it as "absent"
	// tests every slug as available.
	env, err := c.do(ctx, http.MethodGet, item("organizations", adminOrg, slug), nil, nil)
	if err != nil {
		if errors.Is(err, iam.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil, nil
	}
	var o iamOrg
	if err := json.Unmarshal(env.Data, &o); err != nil {
		return nil, fmt.Errorf("iam organization: decode: %w", err)
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
	_, err = c.do(ctx, http.MethodPost, "/v1/iam/organizations", nil, body)
	return err
}

// deleteOrganization removes a customer org by slug. An org that is already gone
// counts as removed.
func (c *iamClient) deleteOrganization(ctx context.Context, slug string) error {
	_, err := c.do(ctx, http.MethodDelete, item("organizations", adminOrg, slug), nil, nil)
	if err == nil || errors.Is(err, iam.ErrNotFound) {
		return nil
	}
	return err
}

// ensureMembership records that user ("<home>/<username>") may act in org with
// role, through IAM's membership relation: the rows the `orgs` claim of the
// user's next token is built from. IAM treats a repeat as a no-op and never lowers
// an existing role.
func (c *iamClient) ensureMembership(ctx context.Context, user, org, role string) error {
	body, err := json.Marshal(map[string]string{"user": user, "org": org, "role": role})
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, "/v1/iam/memberships", nil, body)
	return err
}

// revokeMembership removes user's membership of org. Removing one that does not
// exist succeeds.
func (c *iamClient) revokeMembership(ctx context.Context, user, org string) error {
	body, err := json.Marshal(map[string]string{"user": user, "org": org})
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, "/v1/iam/delete-membership", nil, body)
	return err
}

// foundOrg makes member ("<home>/<username>") the owner of org, which was just
// created for them, and removes org when that cannot be recorded. It returns the
// membership error.
//
// The removal must not strand a membership row: IAM does not check that a row's
// org exists, so a row that outlives its org hands that name to the row's holder
// the day anyone creates an org called the same thing. So:
//
//   - A refusal wrote nothing (IAM's membership write refuses before its one store
//     call, and a failed call writes no row), so the org is deleted at once.
//   - Any other failure may have landed, so the row is revoked first and the org
//     deleted only after the revoke succeeds.
//
// If the revoke or the delete fails, the org stays, owned by member or by nobody,
// and the log names it.
func foundOrg(ctx context.Context, c *iamClient, log func(string, ...any), member, org string) error {
	err := c.ensureMembership(ctx, member, org, string(authz.Owner))
	if err == nil {
		return nil
	}
	var undo error
	var r refusal
	if !errors.As(err, &r) {
		undo = c.revokeMembership(ctx, member, org)
	}
	if undo == nil {
		undo = c.deleteOrganization(ctx, org)
	}
	if undo != nil && log != nil {
		log("organization kept after its owner could not be recorded", "org", org, "err", undo)
	}
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
// a token for a run is a separate act, at a separate endpoint, reviewed separately.
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
	if got, gerr := c.do(ctx, http.MethodGet, item("applications", org, name), nil, nil); gerr == nil && len(got.Data) > 2 {
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

// item is the address of one IAM record: /v1/iam/<entity>/{owner}/{name}. IAM
// names a record by its natural key in the path and serves its read, replace and
// delete there, and nowhere else.
func item(entity, owner, name string) string {
	return "/v1/iam/" + entity + "/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// splitID splits the `<owner>/<name>` composite the callers carry into the two
// fields IAM's user ops actually want. A bare name (a first-run, org-less user)
// yields an empty owner, which addresses no row.
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
	env, err := c.do(ctx, http.MethodGet, "/v1/iam/users", url.Values{"owner": {owner}}, nil)
	if err != nil {
		return "", err
	}
	// The roster arrives named for its entity — {"users":[…],"total":N} — rather
	// than as a bare array, so the rows are read out from under that key.
	var page struct {
		Users []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"users"`
	}
	if err := json.Unmarshal(env.Data, &page); err != nil {
		return "", err
	}
	rows := page.Users
	for _, r := range rows {
		if r.ID == id {
			return r.Name, nil
		}
	}
	return "", errNotFound
}

// getUser reads the full row of owner/name. name is the USERNAME — the row's own
// `name` field, "z" — and a caller holding only the UUID `sub` (the direct-Bearer
// path) is resolved to it through the org's roster and read once more. A caller
// with no owner names no row.
func (c *iamClient) getUser(ctx context.Context, owner, name string) (json.RawMessage, error) {
	if owner == "" {
		return nil, errNotFound
	}
	env, err := c.do(ctx, http.MethodGet, item("users", owner, name), nil, nil)
	if errors.Is(err, iam.ErrNotFound) {
		if n, rerr := c.nameOf(ctx, owner, name); rerr == nil && n != "" && n != name {
			env, err = c.do(ctx, http.MethodGet, item("users", owner, n), nil, nil)
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

// putUser writes a whole user row back — the second half of every read-change-write
// on this client (the avatar, the appearance preference, the move into a new org).
//
// The row travels NESTED under `user` and is written at the address its own owner
// and name spell, so the row read is the row written and the two cannot name
// different people. It lives here so the shape is stated ONCE — three callers
// spelling the same envelope is three places for it to be spelled differently.
func (c *iamClient) putUser(ctx context.Context, row map[string]any) error {
	owner, _ := row["owner"].(string)
	name, _ := row["name"].(string)
	if owner == "" || name == "" {
		return errors.New("iam user row names no owner and name")
	}
	body, err := json.Marshal(map[string]any{"user": row})
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPut, item("users", owner, name), nil, body)
	return err
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
	return c.putUser(ctx, row)
}

// MOVING A FOUNDER INTO THEIR NEW ORG IS `provision`, AND ONLY provision.
//
// There used to be a second way here — read the row, set owner to the new slug,
// write it back — and the user update cannot express it: it resolves WHICH row to
// write from that row's own owner and name, so a body naming the new org is a
// lookup of a user who does not exist there yet, and the write is refused. The
// org would be created and its founder left outside it.
//
// So the pair is gone and the atomic op is the whole path. It was already the
// preferred one; what is removed is the fallback that ran when no service token
// was configured, and a deployment in that state now hears so at the first step
// instead of after the org exists.

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
