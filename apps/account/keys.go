package account

// keys.go is the caller's API keys: many named keys per person, each its own
// credential, listed, created, edited and revoked one at a time.
//
// THE CREDENTIAL IS IAM'S. Each key is one IAM key row, filed in the org it acts
// in under an id this surface picks, holding its holder, its label, the prefix of
// its credential and its policy. Cloud stores no key and no secret: it asks IAM
// to mint a row, reads the rows back, and edits them through IAM's own update.
//
// WHERE THE POLICY LIVES, and why there. A key's policy is on its IAM row, in the
// fields IAM already enforces or already hands back when a key is resolved:
//
//   - expiry is ExpireTime, and IAM refuses an expired key at resolution
//     (store.dead → key_expired);
//   - revocation is State "Revoked" with who and when beside it; the row stays,
//     so the key is still listed, and IAM refuses it (key_revoked);
//   - what the key may reach, its budget and its rate are cloud's grant
//     (cloud.Grant), which IAM stores in Scope beside the key's class and returns
//     with the principal. The boundary applies the reach, the scope rate limiter
//     the rate, and the AI usage policy the budget (apps/ai keyBudget).
//
// What a key has SPENT is counted where its model calls are recorded, in the ai
// process, and asked for over the plane (client.AISpend). When IAM last saw it
// used is IAM's own record (a sighting), read back with the row.
//
// So a key's policy reaches cloud's boundary in the same answer that says who the
// key is, with no second lookup and no second copy to drift.
//
// WHO SEES WHICH KEYS. A key belongs to the org it is filed in and names the person
// who created it. A member sees and manages their own; an admin of the org (or the
// platform SuperAdmin acting in it) sees and manages every key the org holds.

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	aipeer "github.com/hanzoai/cloud/client/ai"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// The key TYPES, as the product names them. A key's type says what the key may
// DO, so it is a field on the one resource, never a path segment:
//
//   - secret (sk-) authenticates its holder as the user. Session-equivalent — an
//     sk- resolves through IAM to a full user row — so it belongs on a server.
//   - publishable (pk-) identifies only the ORG, so it may be shipped in a browser
//     bundle. It covers analytics, product insights and error capture as ONE key.
const (
	keyTypeSecret      = "secret"
	keyTypePublishable = "publishable"
)

// The statuses a key is listed with.
const (
	statusActive   = "active"
	statusExpired  = "expired"
	statusRevoked  = "revoked"
	statusDisabled = "disabled"
)

// keyCeiling is how many live keys one org may hold, and rowCeiling how many rows
// in all, revoked and expired ones included (they stay listed). Abuse ceilings,
// not product limits: high enough that no real org meets them, low enough that a
// runaway create-and-revoke loop cannot fill IAM's key table.
const (
	keyCeiling = 1000
	rowCeiling = 10 * keyCeiling
)

// nameMax bounds a key's label.
const nameMax = 128

// apiKey is one API key as a caller may see it. NEVER secret material: the secret
// is returned once, by the POST that creates the key, and is unreadable afterwards.
// A publishable key carries its full value, because it is public by construction
// and useless to its holder if they cannot read it back.
type apiKey struct {
	// ID addresses this key in PATCH and DELETE /v1/account/keys/{id}.
	ID string `json:"id"`
	// Name is the key's label, chosen by the person who made it.
	Name string `json:"name"`
	// Type is the key class: secret (sk-) or publishable (pk-).
	Type string `json:"type"`
	// Prefix is the head of the credential this key's holder presents — the sk- of
	// a secret key — enough to tell which string a row is, never enough to use.
	// Absent on a key minted before prefixes were recorded.
	Prefix string `json:"prefix,omitempty"`
	// Key is the full credential. A publishable key always carries it; a secret
	// key carries it once, in the answer to the POST that created it.
	Key string `json:"key,omitempty"`
	// Status is active, expired, revoked, or disabled (switched off in IAM).
	Status string `json:"status"`
	// Creator is the person the key speaks for, `<org>/<user>`: who created it.
	Creator string `json:"creator,omitempty"`
	// Created is when the key was minted (RFC 3339).
	Created string `json:"created,omitempty"`
	// Expires is when the key stops working (RFC 3339). Absent means never.
	Expires string `json:"expires,omitempty"`
	// Revoker is who revoked the key, `<org>/<user>`, and Revoked when.
	Revoker string `json:"revoker,omitempty"`
	Revoked string `json:"revoked,omitempty"`
	// Limit is what this key may reach, as `kind:name` entries — `model:zen5`,
	// `project:acme`, `product:train` (read and write), `read:billing` (read only),
	// `read:*` (a read-only key). Absent means the key reaches whatever its holder
	// does.
	Limit []string `json:"limit,omitempty"`
	// Budget is what the key may spend on models, in cents. Absent means no cap.
	Budget *keyBudget `json:"budget,omitempty"`
	// Spend is what the key has spent on models, in cents rounded up. Absent when
	// it could not be read just now.
	Spend *keyBudget `json:"spend,omitempty"`
	// Rate is how many requests a minute the key may make. Absent means no limit
	// of its own.
	Rate int `json:"rate,omitempty"`
	// Used is when the key was last used (RFC 3339). Absent means never.
	Used string `json:"used,omitempty"`
}

// keyBudget is an amount of model spend in cents, per calendar month (UTC) and in
// all. As a budget a zero is no cap; as spend it is nothing spent.
type keyBudget struct {
	// Month is per calendar month, UTC.
	Month int64 `json:"month"`
	// Total is over the key's whole life.
	Total int64 `json:"total"`
}

// budgetMax bounds a key's budget: $10,000,000, in cents.
const budgetMax = 1_000_000_000

// rateMax bounds a key's rate: requests a minute.
const rateMax = 1_000_000

// apiKeyList is the API keys the caller may see. Named for what they ARE rather
// than `keyList`, which the fleet's flat schema namespace already spends on git's
// SSH deploy keys (openapi/compose.go refuses one name for two shapes).
type apiKeyList struct {
	// Keys is every key the caller may see, newest first: their own, or every key
	// of the org when the caller administers it. Revoked keys stay listed.
	Keys []apiKey `json:"keys"`
}

// keyIn is a key to create.
type keyIn struct {
	// Name labels the key. Omitted names it "key N".
	Name string `json:"name"`
	// Type is the key class: "secret" (sk-, session-equivalent, belongs on a
	// server) or "publishable" (pk-, org-identifying, safe in a browser bundle).
	// Omitted means secret.
	Type string `json:"type"`
	// Limit narrows what the key may reach, as `kind:name` entries: `model:zen5`,
	// `project:acme`, `product:commerce`, or `model:*` for a whole kind. It only
	// ever NARROWS — a key never reaches further than the person who made it.
	// Omitted makes an unrestricted key.
	//
	// Example: {"name": "ci", "limit": ["model:zen5", "project:acme"]}
	Limit []string `json:"limit,omitempty" url:"-"`
	// Expires is when the key stops working, RFC 3339. Omitted means never.
	Expires string `json:"expires,omitempty" url:"-"`
	// Budget caps what the key may spend on models, in cents, per calendar month
	// and in all; a request past either is refused 402. Omitted means no cap.
	Budget *keyBudget `json:"budget,omitempty" url:"-"`
	// Rate caps the key's requests a minute; past it a request is refused 429.
	// Omitted means no limit of its own.
	Rate int `json:"rate,omitempty" url:"-"`
}

// keyEdit is a change to one key's policy. Every field is optional and an absent
// one is kept; the credential itself is never changed.
type keyEdit struct {
	// ID is the key to edit, from the path.
	ID string `json:"id"`
	// Name relabels the key.
	Name *string `json:"name,omitempty"`
	// Limit replaces what the key may reach. An empty list makes it unrestricted.
	Limit []string `json:"limit,omitempty"`
	// Expires replaces when the key stops working, RFC 3339. An empty string means
	// never.
	Expires *string `json:"expires,omitempty"`
	// Budget replaces the key's spend caps, in cents; a zero drops that cap.
	Budget *keyBudget `json:"budget,omitempty"`
	// Rate replaces the key's requests a minute; zero drops the limit.
	Rate *int `json:"rate,omitempty"`
}

// keyRef names one key by its id.
type keyRef struct {
	// ID is the key to act on, from the path.
	ID string `json:"id"`
}

// keyClass normalizes a requested key type: empty means secret. An unrecognized
// value is refused rather than defaulted — a caller asking for a browser-safe key
// must never be handed a session-equivalent secret by accident.
func keyClass(t string) (string, bool) {
	switch strings.TrimSpace(t) {
	case "", keyTypeSecret:
		return keyTypeSecret, true
	case keyTypePublishable:
		return keyTypePublishable, true
	}
	return "", false
}

// publishable answers what a key IS, and the SCOPE is what says so. IAM mints both
// classes with a pk- AccessKey and puts a secret key's sk- in AccessSecret, which
// every read masks, so the AccessKey prefix reads the same on every row. An sk-
// sitting in AccessKey is still refused: that disagreement would print a
// confidential value into a listing.
func publishable(r userKey) bool {
	return cloud.ParseGrant(r.Scope).Publishable() &&
		!strings.HasPrefix(r.AccessKey, prefixForType(keyTypeSecret))
}

// statusOf is where a key stands at now.
func statusOf(k userKey, now time.Time) string {
	switch k.State {
	case "", "Active", "test":
	case keyRevoked:
		return statusRevoked
	default:
		return statusDisabled
	}
	if k.ExpireTime != "" {
		if t, err := time.Parse(time.RFC3339, k.ExpireTime); err != nil || !now.Before(t) {
			return statusExpired
		}
	}
	return statusActive
}

// qualified spells a key's holder as "<org>/<user>": a bare name is a user of the
// key's own org, as IAM reads it.
func qualified(owner, user string) string {
	if user == "" || strings.Contains(user, "/") {
		return user
	}
	return owner + "/" + user
}

// view is how a key row is shown.
func view(k userKey, now time.Time) apiKey {
	out := apiKey{
		ID:      k.Name,
		Name:    k.DisplayName,
		Type:    keyTypeSecret,
		Prefix:  k.Prefix,
		Status:  statusOf(k, now),
		Creator: qualified(k.Owner, k.User),
		Created: cmp.Or(k.CreatedTime, k.UpdatedTime),
		Expires: k.ExpireTime,
		Used:    k.UsedTime,
	}
	g := cloud.ParseGrant(k.Scope)
	out.Limit = g.Reach()
	if month, total := g.Budget(); month > 0 || total > 0 {
		out.Budget = &keyBudget{Month: month, Total: total}
	}
	out.Rate = g.Rate()
	if out.Status == statusRevoked {
		out.Revoker, out.Revoked = k.Revoker, k.RevokeTime
	}
	if publishable(k) {
		// A publishable key is public by construction: hand back the whole value.
		out.Type, out.Key = keyTypePublishable, k.AccessKey
		out.Prefix = cmp.Or(out.Prefix, prefixOf(k.AccessKey))
	}
	return out
}

// prefixOf is the head of a public key, for a publishable row minted before IAM
// recorded prefixes.
func prefixOf(key string) string {
	if len(key) > 12 {
		return key[:12]
	}
	return key
}

// scopeOf renders a key's class and grant as the one Scope field IAM stores.
func scopeOf(typ string, g cloud.Grant) string {
	if typ == keyTypePublishable {
		return append(cloud.Grant{cloud.GrantClassPublish}, g...).String()
	}
	return g.String()
}

// policy reads a requested budget and rate.
func policy(b *keyBudget, rate int) (month, total int64, r int, err error) {
	if b != nil {
		month, total = b.Month, b.Total
	}
	if month < 0 || total < 0 || month > budgetMax || total > budgetMax {
		return 0, 0, 0, zip.ErrBadRequest(fmt.Sprintf("a budget is between 0 and %d cents", budgetMax))
	}
	if rate < 0 || rate > rateMax {
		return 0, 0, 0, zip.ErrBadRequest(fmt.Sprintf("a rate is between 0 and %d requests a minute", rateMax))
	}
	return month, total, rate, nil
}

// expiry reads a requested expiry: RFC 3339 and in the future, rendered in UTC.
// Empty means never.
func expiry(raw string, now time.Time) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", zip.ErrBadRequest("expires must be an RFC 3339 time, like 2026-12-31T00:00:00Z")
	}
	if !now.Before(t) {
		return "", zip.ErrBadRequest("expires must be in the future")
	}
	return t.UTC().Format(time.RFC3339), nil
}

// label reads a requested key name.
func label(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if len(name) > nameMax {
		return "", zip.ErrBadRequest(fmt.Sprintf("a key's name is at most %d characters", nameMax))
	}
	return name, nil
}

// issue names a new key row: the caller's own stem, so a row in IAM says whose it
// is, and eight random hex digits, so each create is a new row. A member's stem
// carries their home, because two members from different orgs may share a
// username; "~" is in no username and no org slug, so it cannot be read two ways.
func (cr caller) issue(typ string) (string, error) {
	user := cr.username
	if user == "" {
		_, user, _ = strings.Cut(cr.keyID(), "/")
	}
	if cr.member() {
		user = cr.home + "~" + cr.username
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return user + "-" + typ + "-" + hex.EncodeToString(b[:]), nil
}

// keyTeam is the caller and the keys of the org they act in that they may see.
type keyTeam struct {
	cr    caller
	c     *zip.Ctx
	admin bool
	all   []userKey
}

// sees reports whether the caller may see and manage k: their own key, or any key
// of an org they administer.
func (t keyTeam) sees(k userKey) bool {
	return t.admin || t.mine(k)
}

// mine reports whether k is the caller's own key.
func (t keyTeam) mine(k userKey) bool { return keyBelongsTo(k, t.cr.owner, t.cr.holder()) }

// keyTeamOf resolves the caller for a key op and reads the org's key rows that IAM
// shows them, as them (orgKeys).
func (o ops) keyTeamOf(ctx context.Context, does act) (keyTeam, error) {
	cr, c, err := o.requestCaller(ctx, does, scoped, "manage API keys")
	if err != nil {
		return keyTeam{}, err
	}
	if !o.s.State.iam.configured() {
		return keyTeam{}, notConfigured("API key management")
	}
	all, err := o.s.State.iam.orgKeys(c.Context(), cr.owner, cloud.CallerBearer(c))
	if err != nil {
		// A degraded IAM is reported, never served as an empty list: "you have no
		// keys" is a claim, and a page that showed it would invite a duplicate.
		return keyTeam{}, iamFailed(err, "could not read API keys")
	}
	return keyTeam{cr: cr, c: c, admin: cloud.Admin.Admits(cloud.AuthorityOf(c)), all: all}, nil
}

// iamFailed is a key op's answer when IAM could not do what it asked: what could
// not be done, and why. Never 502: the edge in front of api.hanzo.ai replaces an
// origin 502 with a page of its own that carries no CORS headers, so a browser
// reads it as "Failed to fetch" and the person never learns why (#755).
//
// IAM's refusal of the act (400, 403, 404, 409, 422, 429) keeps its status, so a
// page can say what to do; a 401 is IAM refusing a sign-in cloud already accepted,
// so it is a 403, never read as signed out. Anything else (IAM unreachable or slow,
// a 5xx, an error envelope, an answer cloud cannot use) is 503: the act was not
// done, and asking again later may do it.
func iamFailed(err error, what string) error {
	var r refusal
	if errors.As(err, &r) {
		switch r.status {
		case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
			http.StatusUnprocessableEntity, http.StatusTooManyRequests:
			return zip.Errorf(r.status, "%s: %v", what, err)
		case http.StatusUnauthorized:
			return zip.Errorf(http.StatusForbidden, "%s: %v", what, err)
		}
	}
	return zip.Errorf(http.StatusServiceUnavailable, "%s: %v", what, err)
}

// widens refuses a create or an edit made with a limited key — one with a reach,
// a budget or a rate. Either could hand the caller a key reaching or spending
// further than the one they arrived on, and a grant only ever narrows; revoking
// narrows, so it is not asked.
func (t keyTeam) widens() error {
	for _, e := range cloud.GrantOf(t.c) {
		if e != cloud.GrantClassPublish {
			return zip.ErrForbidden("a limited API key cannot create or edit keys; sign in, or use an unrestricted key")
		}
	}
	return nil
}

// find is the key id names, when the caller may see it. A key that exists and is
// not theirs answers exactly as one that does not exist, so an id cannot be
// probed for another person's key.
func (t keyTeam) find(id string) (userKey, error) {
	for _, k := range t.all {
		if k.Name == id && t.sees(k) {
			return k, nil
		}
	}
	return userKey{}, zip.ErrNotFound("no API key " + id)
}

// ListKeys returns the API keys the caller may see, newest first: their own, or
// every key the org holds when the caller administers it. Each is its own
// credential with its own name, prefix, status, expiry and limit. Revoked keys
// stay listed. No secret material comes back; a publishable key, which is public
// by construction, carries its full value.
func (o ops) listKeys(ctx context.Context, _ *cloud.Unit) (*apiKeyList, error) {
	t, err := o.keyTeamOf(ctx, reads)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := apiKeyList{Keys: []apiKey{}}
	for _, k := range t.all {
		if t.sees(k) {
			out.Keys = append(out.Keys, view(k, now))
		}
	}
	spent(t.c.Context(), t.cr.owner, out.Keys)
	return &out, nil
}

// spent fills in what each listed key has spent, asked of the ai process that
// counts it. A read that fails leaves Spend absent, which the listing says means
// "could not be read just now", never "nothing spent".
func spent(ctx context.Context, org string, keys []apiKey) {
	if len(keys) == 0 {
		return
	}
	ids := make([]string, len(keys))
	for i, k := range keys {
		ids[i] = k.ID
	}
	got, err := aipeer.AISpend(cloud.For(ctx, org), &client.KeysIn{Keys: ids})
	if errors.Is(err, plane.ErrNoPeer) {
		return // no model API in this deployment, so no key has spent anything on one
	}
	if err != nil || got == nil {
		slog.Warn("api keys: spend unread", "org", org, "err", err)
		return
	}
	by := make(map[string]client.KeySpend, len(got.Keys))
	for _, s := range got.Keys {
		by[s.Key] = s
	}
	for i := range keys {
		if s, ok := by[keys[i].ID]; ok {
			keys[i].Spend = &keyBudget{Month: s.Month, Total: s.Total}
		}
	}
}

// CreateKey creates a NEW API key and returns its secret ONCE. It never revokes
// or changes any other key: every key the caller already holds keeps working.
// The key is filed in the org the caller acts in and names them as its creator.
//
// An org holds at most 1,000 live keys; past that the create is refused until one
// is revoked.
//
// Example: {"name": "production", "type": "secret", "budget": {"month": 5000, "total": 0}, "rate": 600}
func (o ops) createKey(ctx context.Context, in *keyIn) (*apiKey, error) {
	t, err := o.keyTeamOf(ctx, rotates)
	if err != nil {
		return nil, err
	}
	if err := t.widens(); err != nil {
		return nil, err
	}
	typ, ok := keyClass(in.Type)
	if !ok {
		return nil, zip.ErrBadRequest("type must be " + keyTypeSecret + " or " + keyTypePublishable)
	}
	// The limit is VALIDATED here, not stored as typed. A kind cloud does not ask
	// about is a limit nothing enforces — it would read as a narrowing and be one
	// nowhere.
	reach, err := cloud.ParseLimit(in.Limit)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	month, total, rate, err := policy(in.Budget, in.Rate)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	expires, err := expiry(in.Expires, now)
	if err != nil {
		return nil, err
	}
	name, err := label(in.Name)
	if err != nil {
		return nil, err
	}
	live, mine := 0, 0
	for _, k := range t.all {
		if statusOf(k, now) == statusActive {
			live++
		}
		if keyBelongsTo(k, t.cr.owner, t.cr.holder()) {
			mine++
		}
	}
	if live >= keyCeiling {
		return nil, zip.Errorf(http.StatusConflict,
			"this organization holds %d live API keys, the most it may; revoke one to create another", keyCeiling)
	}
	if len(t.all) >= rowCeiling {
		return nil, zip.Errorf(http.StatusConflict,
			"this organization has made %d API keys, the most it may hold, revoked ones included", rowCeiling)
	}
	if name == "" {
		name = fmt.Sprintf("key %d", mine+1)
	}
	id, err := t.cr.issue(typ)
	if err != nil {
		return nil, err
	}
	row, secret, err := o.s.State.iam.createKey(t.c.Context(), newKey{
		Owner: t.cr.owner, Name: id, Holder: t.cr.holder(), Label: name,
		Type: typ, Scope: scopeOf(typ, reach.Policy(month, total, rate)), Expires: expires,
	})
	if err != nil {
		return nil, iamFailed(err, "could not create an API key")
	}
	out := view(row, now)
	out.Key = secret
	out.Prefix = cmp.Or(out.Prefix, prefixOf(secret))
	return &out, nil
}

// EditKey changes one key's name, permissions, budget, rate or expiry. The secret
// is not reissued: the key in your deployment keeps working, under its new
// policy. Only the person the key belongs to may edit it, and a revoked key
// cannot be edited.
//
// Example: {"name": "staging", "limit": ["read:*"], "budget": {"month": 5000, "total": 0}, "rate": 60}
func (o ops) editKey(ctx context.Context, in *keyEdit) (*apiKey, error) {
	t, err := o.keyTeamOf(ctx, rotates)
	if err != nil {
		return nil, err
	}
	if err := t.widens(); err != nil {
		return nil, err
	}
	k, err := t.find(strings.TrimSpace(in.ID))
	if err != nil {
		return nil, err
	}
	// A key's policy is its holder's to set. An org admin may revoke a member's key
	// but not edit it: an edit can widen as easily as narrow, and a limit an admin
	// set on someone else's key could be lifted by its holder the next minute.
	if !t.mine(k) {
		return nil, zip.ErrForbidden("only the person a key belongs to may edit it; an org admin may revoke it")
	}
	now := time.Now()
	if statusOf(k, now) == statusRevoked {
		return nil, zip.Errorf(http.StatusConflict, "API key %s is revoked; create a new key instead", k.Name)
	}
	row := maps.Clone(k.row)
	if in.Name != nil {
		name, err := label(*in.Name)
		if err != nil {
			return nil, err
		}
		row["displayName"] = name
	}
	if in.Limit != nil || in.Budget != nil || in.Rate != nil {
		held := cloud.ParseGrant(k.Scope)
		reach := held.Reach()
		if in.Limit != nil {
			if reach, err = cloud.ParseLimit(in.Limit); err != nil {
				return nil, zip.ErrBadRequest(err.Error())
			}
		}
		month, total := held.Budget()
		rate := held.Rate()
		if in.Budget != nil {
			month, total = in.Budget.Month, in.Budget.Total
		}
		if in.Rate != nil {
			rate = *in.Rate
		}
		b := keyBudget{Month: month, Total: total}
		if month, total, rate, err = policy(&b, rate); err != nil {
			return nil, err
		}
		typ := keyTypeSecret
		if publishable(k) {
			typ = keyTypePublishable
		}
		row["scope"] = scopeOf(typ, reach.Policy(month, total, rate))
	}
	if in.Expires != nil {
		expires, err := expiry(*in.Expires, now)
		if err != nil {
			return nil, err
		}
		row["expireTime"] = expires
	}
	got, err := o.s.State.iam.putKey(t.c.Context(), k.Owner, k.Name, row)
	if err != nil {
		return nil, iamFailed(err, "could not edit API key "+k.Name)
	}
	out := view(got, now)
	return &out, nil
}

// RevokeKey revokes exactly one API key, by id. Every other key keeps working.
// The key stays listed as revoked, with who revoked it and when, and can never be
// used again. IAM refuses it at once; cloud caches a resolved key for up to 60
// seconds, so a request inside that window may still be served.
//
// A member revokes their own keys; an org admin revokes any of the org's, and
// revoking someone else's is recorded on the audit trail.
func (o ops) revokeKey(ctx context.Context, in *keyRef) (*apiKey, error) {
	t, err := o.keyTeamOf(ctx, rotates)
	if err != nil {
		return nil, err
	}
	k, err := t.find(strings.TrimSpace(in.ID))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if statusOf(k, now) == statusRevoked {
		out := view(k, now)
		return &out, nil
	}
	row := maps.Clone(k.row)
	row["state"], row["revoker"] = keyRevoked, t.cr.holder()
	if !t.mine(k) {
		// Revoking someone else's key is an act of authority over their credential:
		// it is on the audit trail before it happens, or it does not happen.
		if err := o.audited(t, k); err != nil {
			return nil, err
		}
	}
	got, err := o.s.State.iam.putKey(t.c.Context(), k.Owner, k.Name, row)
	if err != nil {
		return nil, iamFailed(err, "could not revoke API key "+k.Name)
	}
	out := view(got, now)
	return &out, nil
}

// audited records an admin revoking key k, which belongs to someone else.
func (o ops) audited(t keyTeam, k userKey) error {
	trail := o.s.State.trail
	if trail == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "revoking another person's key could not be audited")
	}
	a := cloud.AuthorityOf(t.c)
	_, err := trail.Append(t.c.Context(), audit.Record{
		Actor:     audit.Actor{Org: t.cr.owner, Sub: t.cr.holder()},
		Action:    "account.keys.revoke",
		Resource:  audit.Resource{Type: "api_key", ID: k.Owner + "/" + k.Name},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: a.Super},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		RequestID: t.c.Header("X-Request-Id"),
		Method:    http.MethodDelete,
		Path:      prefix + "/keys/" + k.Name,
	})
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "revoking another person's key could not be audited")
	}
	return nil
}
