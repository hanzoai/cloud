// Package account mounts the signed-in caller's OWN account self-service surface
// natively in the unified cloud binary — the Go port of the console's two NON-proxy
// Next server routes (app/keys + app/onboard) plus the money/store data bridges the
// statically-exported console needs (task #41, "True 1-binary FE"). It replaces the
// retired /v1/console/* namespace: "console" is just the cloud FE name, so there is NO
// /v1/console API domain — every route lives on its REAL domain.
//
// WHY THESE ROUTES (and not the pure passthrough proxies). The console's PURE BFF
// reverse-proxies — app/cloud, app/ai — vanish in the one-binary model: the SPA calls
// the canonical /v1/* on its own origin and the already-mounted subsystems answer. The
// routes ported HERE do REAL server work a static SPA cannot: keys/onboard run
// privileged IAM logic as the confidential `hanzo-console` client; embed-status/topup
// do server-side verification; and the billing/commerce bridges inject the commerce
// SERVICE token and pin the caller's own subject SERVER-SIDE (a passthrough would leak
// cross-tenant ledgers). Each has no pure-proxy equivalent, so it must be ported.
//
// SURFACE — each route on its REAL domain (every one requires a VALIDATED principal — a
// gateway-minted, IAM-verified X-User-Id; a client-forged X-Org-Id on the bearer-less
// path is refused):
//
//	GET    /v1/keys                  — the caller's keys: { keys: [{ type, prefix, createdAt }] }; no secret.
//	POST   /v1/keys                  — create/rotate a key of { type: publishable | secret }; returns it ONCE.
//	DELETE /v1/keys                  — revoke the key of that type.
//	…      /v1/iam/keys              — DEPRECATED aliases of the three above (same handlers).
//	POST   /v1/iam/onboard           — create the caller's org (+ move them in on first run).
//	GET    /v1/csrf                  — mint the anti-CSRF token the SPA echoes on money writes (csrf.go).
//	GET    /v1/embed-status          — brand-app embed entitlement + reachability probe (embed.go).
//	POST   /v1/commerce/topup/wallet — HUSD on-chain verify → commerce credit (topup.go).
//	GET    /v1/billing/*             — per-tenant billing read, SCOPED to the validated caller (billing.go).
//	…      /v1/commerce/*            — per-tenant STORE CRUD, SCOPED to the validated caller's org (commerce.go).
//
// TWO SUBSYSTEM REGISTRATIONS FROM ONE PACKAGE. A route-ordering constraint forces the
// split (Fiber matches by registration order — the earliest-mounted route wins):
//   - `account` (order 48) mounts the SPECIFIC self-service routes. keys/onboard MUST
//     win over clients/iam's /v1/iam/* WILDCARD (order 50), and topup MUST win over the
//     commerce embed (order 100) + the /v1/commerce/* bridge — so they mount EARLY.
//   - `account-bridge` (order 122) mounts the CATCH-ALL data bridges. /v1/billing/* must
//     sit AFTER clients/billing's specific routes (order 121) and /v1/commerce/* after
//     the commerce embed (order 100) — so they mount LATE.
//
// Both share one state shape + the process-wide CSRF key (csrf.go), so a token minted at
// /v1/csrf verifies on the /v1/billing|commerce writes.
//
// TYPED OPS. Every ADDRESSABLE route here is a typed op (zip.Get/Post/Delete with
// real In/Out types) — eleven of them — so each is ONE registry entry the REST
// route, the OpenAPI operation's schema and prose, the MCP tool, the CLI command
// and every generated SDK method all derive from. Seven routes are deliberately
// NOT, and they are the same seven:
//
//   - The /v1/billing/* and /v1/commerce/* bridges are catch-alls: the path is a
//     wildcard remainder, the body is forwarded verbatim to another service and the
//     answer is that service's bytes and status. There is no In and no Out to name —
//     they are opaque by construction, not by omission. What they may reach is
//     nonetheless bounded, by an allowlist rather than by a type (billing.go).
//
// TENANCY. The caller is resolved from the VALIDATED identity headers ONLY
// (principal.Validated / c.Org() / c.User()), the same trust boundary every mutating
// subsystem uses. The IAM id targeted is DERIVED as `<owner>/<name>` from those
// validated claims — never taken from the request body/query — so a caller can only ever
// mint/revoke their OWN key and onboard THEMSELVES; there is no path to name a
// third-party subject. When the confidential client is unwired the surface is honestly
// "not configured" (501), never a fabricated key or org.
package account

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// adminOrg is THE SuperAdmin / IAM-system org that owns every customer org row —
// standardized as "admin" across the whole stack (IAM, commerce, ai, gateway all
// gate cross-tenant on owner=="admin"). A created customer org is owned by it.
const adminOrg = "admin"

// errNotConfigured is returned by the IAM client when the confidential
// `hanzo-console` credential is unset; handlers map it to a 501 (honest "not
// configured on this deployment", mirroring identity.ts's mintConfigured() gate).
var errNotConfigured = errors.New("iam confidential client not configured")

// errNotFound is a not-present sentinel (e.g. the user row IAM cannot return).
var errNotFound = errors.New("not found")

// state is account's own data; shared deps live in the embedded cloud.Base. Both
// subsystem registrations (account @48, account-bridge @122) build their own value;
// the CSRF key is the process-wide singleton (csrf.go) so a token minted by one
// verifies on the other.
type state struct {
	iam      *iamClient
	csrfKey  []byte       // keyed-BLAKE3 MAC key for the money-write CSRF token (csrf.go)
	writesRL *rateLimiter // per-IP abuse cap on the money-write routes (ratelimit.go)
}

// keysWriteRatePerMin caps money-write frequency per client IP (mint/rotate/revoke
// key, wallet top-up). Generous enough for real UI bursts, tight enough to blunt
// brute-force / enumeration when a caller reaches cloud directly (gateway bypassed).
const keysWriteRatePerMin = 30

// newService builds the shared subsystem value. Both subsystem Mounts construct one;
// the CSRF key is the process-wide singleton (csrf.go) so account (order 48) and
// account-bridge (order 122) verify each other's tokens.
func newService(deps cloud.Deps) *cloud.Service[state] {
	b := cloud.NewBase(deps, "account")
	st := state{iam: newIAMClient()}
	st.csrfKey = sharedCSRFKey(b.Log)
	st.writesRL = newRateLimiter(keysWriteRatePerMin)
	return &cloud.Service[state]{Base: b, State: st}
}

// Mount wires the SPECIFIC self-service routes (order 48) — the ones that must
// win over the IAM /v1/iam/* wildcard (50) and the commerce embed (100).
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("account.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("account.Mount: nil deps.Logger")
	}
	s := newService(deps)
	if err := routesAccount(s, app); err != nil {
		return err
	}
	s.Log.Info("account self-service surface mounted",
		"iam", s.State.iam.base, "configured", s.State.iam.configured(), "brand", s.Brand)
	return nil
}

// MountBridge wires the CATCH-ALL data bridges (order 122) — the /v1/billing/* and
// /v1/commerce/* proxies that must sit AFTER clients/billing (121) + the commerce embed.
func MountBridge(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("account.MountBridge: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("account.MountBridge: nil deps.Logger")
	}
	s := newService(deps)
	routesBridge(s, app)
	s.Log.Info("account data bridges mounted", "prefixes", "/v1/billing/*,/v1/commerce/*", "brand", s.Brand)
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/account openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routesAccount wires the specific self-service routes (order 48).
func routesAccount(s *cloud.Service[state], app cloud.Router) error {
	// Bridge FIRST: a typed op receives only a context, so the request facts its
	// signature drops — here the VALIDATED principal every route resolves its caller
	// from — reach it by being parked there. fiber runs middleware in registration
	// order, so this must precede the leaves below. Serve installs one app-wide too
	// and nesting is harmless (the inner one is what the handler sees); this one is
	// what makes the subsystem self-sufficient when it is mounted on a bare app,
	// which is exactly what its own tests do.
	//
	// It goes through Use, not Group(prefix, mw): account's routes are spread across
	// six top-level nouns, so it owns no single prefix to hang a group on — and
	// Router.Use is the door that fans middleware out over the prefixes the
	// composition root declared for this subsystem, which is precisely that set.
	app.Use(cloud.Bridge())

	// The typed registrars take the App behind the Router: a typed op is a route
	// PLUS a registry entry, and the registry lives on the App (scope.go). A
	// subsystem that cannot reach it must fail its mount rather than serve routes no
	// projection knows about.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("account.Mount: router exposes no zip.App, so no typed op could be registered")
	}
	o := ops{s: s}

	// The five request pipelines, each declared once. `With` composes middleware
	// around a leaf at registration time (limit(csrf(handler))) and carries into the
	// typed registration, so a typed op is gated exactly as the untyped route beside
	// it — a decorator that dropped the gate there would register the op ungated.
	//
	// Each pipeline is the bare App decorated, never a Group, and every op below
	// spells its FULL path: cmd/zipdoc keys its extraction on the path LITERAL at
	// the call site while the registry keys on the group-joined one, so a
	// group-relative declaration lifts prose under a key no operation looks up —
	// the doc comments would be written, generated, and silently never rendered.
	limit, csrf := rateLimit(s.State.writesRL), requireCSRF(s)
	deprecated := deprecatedFor("/v1/keys")
	open := zapp                                     // reads: no gate
	write := zapp.With(limit, csrf)                  // money writes
	guard := zapp.With(csrf)                         // a write that is not rate-limited
	alias := zapp.With(deprecated)                   // deprecated read alias
	aliasWrite := zapp.With(deprecated, limit, csrf) // deprecated write alias

	// GET /v1/csrf issues the anti-CSRF token the embedded SPA echoes as X-CSRF-Token on
	// every money write (csrf.go). Safe (read-only), same-origin.
	zip.Get(open, "/v1/csrf", o.issueCSRFToken)
	// The caller's own API keys. ONE noun, the methods carry the operations, and the
	// key TYPE (publishable | secret) is a FIELD — the concept had four names
	// (/v1/iam/mint-user-keys, /v1/iam/revoke-user-keys, /v1/iam/keys,
	// /v1/ingest/keys) and the only honest one 404'd.
	//
	// It lives OUTSIDE /v1/iam on purpose, and that is not cosmetic: api.hanzo.ai
	// routes /v1/iam/* straight to the IAM service (ingress router
	// api-hanzo-ai-iam-api), so the handler below was UNREACHABLE at the only host
	// callers use — every request landed on IAM's Guard and 401'd. A key surface
	// spelled as if it belonged to IAM was answered by IAM. Naming it for the
	// resource instead of the subsystem that stores it is what makes it reachable.
	//
	// Reads are open; every state-changing WRITE is wrapped: requireCSRF blocks a
	// cross-site ambient-cookie forgery, and rateLimit caps per-IP frequency (cloud is
	// reachable off-gateway).
	zip.Get(open, "/v1/keys", o.getKey)
	zip.Post(write, "/v1/keys", o.mintKey)
	// DELETE addresses what it deletes with its URL, so its typed input binds from
	// `?type=` and the document declares that one parameter (zip's hasBody:
	// GET/HEAD/DELETE carry none). The class is ALSO still read out of a JSON body
	// when the query omits it — inside the handler, by revokeClass, because the
	// input cannot carry a half the method does not have. That read is what keeps
	// the wire whole: dropping it would silently revoke a body-selecting caller's
	// SECRET key in place of the publishable one they named.
	zip.Delete(write, "/v1/keys", o.revokeKey)
	// DEPRECATED alias of /v1/keys, kept because the go:embed console addresses it
	// directly (src/lib/api/keys.ts, IS_EMBED build) against cloud's own origin,
	// where it is not shadowed by the edge. The SAME handlers — an alias, never a
	// second implementation — plus a Deprecation header naming the replacement.
	// These SPECIFIC routes MUST register before clients/iam's /v1/iam/* wildcard
	// (order 50 > 48) so Fiber's first-match scan hits the native handler
	// (TestIAMKeysBeatsWildcard).
	zip.Get(alias, "/v1/iam/keys", o.getKey)
	zip.Post(aliasWrite, "/v1/iam/keys", o.mintKey)
	zip.Delete(aliasWrite, "/v1/iam/keys", o.revokeKey)
	zip.Post(guard, "/v1/iam/onboard", o.onboard)
	// Console module embed-entitlement + reachability probe (embed.go).
	zip.Get(open, "/v1/embed-status", o.embedStatus)
	// HUSD wallet top-up (on-chain verify → commerce credit). A SPECIFIC commerce route
	// that must beat the /v1/commerce/* bridge (122) AND the commerce embed (100) — so it
	// mounts here at 48, ahead of both.
	zip.Post(write, "/v1/commerce/topup/wallet", o.walletTopup)
	// The accepted rails are public on-chain data (chain, token, treasury), read by
	// the browser to render the send UI. A GET with no side effects and no secret,
	// so it needs neither CSRF nor the write limiter — but it MUST sit beside the
	// POST at this priority, or the /v1/commerce/* bridge swallows it.
	zip.Get(open, "/v1/commerce/topup/rails", o.topupRails)
	return nil
}

// routesBridge wires the per-tenant catch-all data bridges (order 122).
func routesBridge(s *cloud.Service[state], app cloud.Router) {
	// Per-tenant billing DATA bridge — the canonical /v1/billing/* the statically-exported
	// console calls, forwarded to commerce with the admin service token and SCOPED to the
	// validated caller's own subject (billing.go). Registered AFTER clients/billing's
	// specific routes (121 < 122) so those win and this catches the rest. GET+POST only.
	// The wildcard is what the ROUTER matches; it is NOT the forwardable set — billing.go's
	// billingForwardable allowlist decides that, per method, and 404s everything else
	// BEFORE the admin service token is attached. Widening this pattern grants nothing on
	// its own; adding a line to that table is the only way to expose an endpoint.
	csrf := requireCSRF(s)
	app.Get("/v1/billing/*", cloud.Handle(s, billingData))
	app.Post("/v1/billing/*", csrf(cloud.Handle(s, billingData)))
	// Per-tenant STORE DATA bridge — the canonical /v1/commerce/* the console calls,
	// forwarded to commerce's bare store surface /v1/<kind> with the admin service token
	// and SCOPED to the validated caller's own org (commerce.go). Registered AFTER the
	// commerce embed (100 < 122) so the embed wins when enabled. Full CRUD.
	app.Get("/v1/commerce/*", cloud.Handle(s, commerceData))
	app.Post("/v1/commerce/*", csrf(cloud.Handle(s, commerceData)))
	app.Put("/v1/commerce/*", csrf(cloud.Handle(s, commerceData)))
	app.Patch("/v1/commerce/*", csrf(cloud.Handle(s, commerceData)))
	app.Delete("/v1/commerce/*", csrf(cloud.Handle(s, commerceData)))
}

// ops binds the service to the typed account ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.mintKey), which is also
// the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noInput is the In of an op that takes nothing off the wire: it is addressed
// entirely by the caller's own validated principal.
type noInput struct{}

// requestCaller is resolveCaller for a typed op. Account's entire surface is the
// signed-in caller's OWN account, and resolving them needs more of the validated
// principal than the tenant: the user id (X-User-Id), the IAM username
// (X-User-Name) and validated-ness itself, none of which principal.OrgFrom
// carries. So this package reaches for the REQUEST, in this ONE function, and
// every op asks it rather than reading headers of its own.
//
// It fails closed off the HTTP path (the CLI's LocalInvoke, where there is no
// request): no request, no attested caller, no account — the same refusal an
// anonymous HTTP caller gets, with no second gate to keep in sync.
//
// The *zip.Ctx comes back with the caller because two ops need the request for
// more than identity: issueCSRFToken pins Cache-Control on its response, and
// embedStatus reads the SuperAdmin claim (X-User-IsAdmin) that lives in a header.
func requestCaller(ctx context.Context, requireOwner bool) (caller, *zip.Ctx, bool) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return caller{}, nil, false
	}
	cr, ok := resolveCaller(c, requireOwner)
	if !ok {
		return caller{}, nil, false
	}
	return cr, c, true
}

// ── caller resolution (the tenancy boundary) ─────────────────────────────────

// caller is the signed-in user resolved from the VALIDATED identity headers. id is
// the `<owner>/<name>` composite IAM's privileged ops parse (GetOwnerAndNameFromId
// requires it — a bare token count of 1 throws "wrong token count"); owner is the
// org (X-Org-Id).
type caller struct {
	id       string // <owner>/<name> (or the bare user id when owner-less)
	owner    string // validated org (may be "" for a zero-org, first-run user)
	name     string // == X-User-Id: the stable user id (a UUID on the direct path)
	username string // IAM username (X-User-Name); the `name` half IAM's user-key ops parse
}

// keyID is the `<owner>/<username>` composite IAM's user-key ops (mint/get/revoke
// user AccessKey) parse via GetOwnerAndNameFromId. It uses the IAM USERNAME, not
// name (== X-User-Id): on the in-binary direct-Bearer path X-User-Id is the UUID
// subject and `<owner>/<uuid>` fails IAM's user lookup ("password or code is
// incorrect"). On the gateway path username==name so keyID()==id — no change.
// Owner-less (first-run) callers can't own a key, so this is only reached with a
// validated owner; it falls back to id defensively.
func (cr caller) keyID() string {
	if cr.owner != "" && cr.username != "" {
		return cr.owner + "/" + cr.username
	}
	return cr.id
}

// resolveCaller derives the caller from the validated identity, or (zero,false)
// when there is no validated principal. requireOwner=true refuses a user with no
// org yet (used by the key ops, which must act scoped); onboarding passes
// requireOwner=false so a first-run zero-org user can create their first org. The id
// is ALWAYS derived from the validated claims, never a request value.
func resolveCaller(c *zip.Ctx, requireOwner bool) (caller, bool) {
	if !principal.Validated(c) {
		return caller{}, false // no gateway-minted, IAM-verified principal — refuse
	}
	name := strings.TrimSpace(c.User())
	if name == "" {
		return caller{}, false
	}
	owner := strings.TrimSpace(c.Org())
	if requireOwner && owner == "" {
		return caller{}, false
	}
	// IAM parses `<owner>/<name>`; prefer it, fall back to the bare id for an
	// owner-less (first-run) user. Same id semantics as identity.ts.
	id := name
	if owner != "" {
		id = owner + "/" + name
	}
	// username is the IAM USERNAME, kept DISTINCT from name (== X-User-Id) so the
	// billing/topup subjects (which key on name) are byte-identical to today — this
	// value narrows the blast radius to the IAM user-key ops alone (keyID()). It
	// prefers X-User-Name (stamped from the validated `name` claim by
	// SanitizeIdentity), because on the in-binary direct-Bearer path X-User-Id is the
	// UUID subject and <owner>/<uuid> fails IAM's mint-user-keys/get-user lookup.
	// Falls back to name for the gateway path (which mints X-User-Id==username). Both
	// inputs are gateway/SanitizeIdentity-minted from a verified principal.
	username := strings.TrimSpace(c.Header("X-User-Name"))
	if username == "" {
		username = name
	}
	return caller{id: id, owner: owner, name: name, username: username}, true
}

// ── keys (/v1/keys — the caller's own API keys) ───────────────────────────────

// The key TYPES, as the product names them. A key's type says what the key may
// DO, so it is a field on the one resource, never a path segment and never a
// separate endpoint:
//
//   - secret (sk-) authenticates its holder as the user. Session-equivalent — an
//     sk- resolves through IAM to a full user row — so it belongs on a server.
//   - publishable (pk-) identifies only the ORG, so it may be shipped in a browser
//     bundle. It covers analytics, product insights and error capture as ONE key,
//     which is why "there is no way to mint one" meant every surface configured its
//     own thing and error reporting kept a separate DSN.
const (
	keyTypeSecret      = "secret"
	keyTypePublishable = "publishable"
)

// apiKey is one API key as a caller may see it: what it is, enough of it to
// recognize, and when it last changed. NEVER secret material — the secret is
// returned once, by the POST that mints it, and is unreadable afterwards.
//
// A publishable key is the exception that proves the rule: `key` carries its FULL
// value, because a publishable key is public by construction and useless to its
// holder if they cannot read it back.
type apiKey struct {
	// Type is the key class: secret (sk-) or publishable (pk-).
	Type string `json:"type"`
	// Prefix is the recognizable, non-secret head of the key — enough to tell two
	// keys apart, never enough to use one.
	Prefix string `json:"prefix,omitempty"`
	// Key is the FULL value, and is present for a publishable key only: it is
	// public by construction and useless to its holder if it cannot be read back.
	Key string `json:"key,omitempty"`
	// CreatedAt is when the key last changed, as IAM records it.
	CreatedAt string `json:"createdAt,omitempty"`
}

// apiKeyList is the caller's own API keys. Named for what they ARE rather than
// the shorter `keyList`, which the fleet's flat schema namespace already spends on
// git's SSH deploy keys — one name for two shapes would bind every generated SDK to
// whichever it read last (openapi/weave.go refuses it).
type apiKeyList struct {
	// Keys is every key the caller holds, at most one per type.
	Keys []apiKey `json:"keys"`
}

// keyTypeIn names which key class an op acts on.
type keyTypeIn struct {
	// Type is the key class to act on: "secret" (sk-, session-equivalent, belongs
	// on a server) or "publishable" (pk-, org-identifying, safe in a browser
	// bundle). Omitted means secret, which is what every existing caller means.
	Type string `json:"type"`
}

// mintedKey is the one-time reveal of a freshly minted key.
type mintedKey struct {
	// Type is the class of key that was minted.
	Type string `json:"type"`
	// Key is the credential, returned ONCE — a secret key is unreadable afterwards.
	Key string `json:"key"`
	// AccessKey is the same value under its predecessor name, carried so callers
	// written against the older field keep working. One value, two names.
	AccessKey string `json:"accessKey"`
}

// keyClass normalizes a requested key type: empty means secret, which is what
// every existing caller means. An unrecognized value is refused rather than
// defaulted — a caller asking for a browser-safe key must never be handed a
// session-equivalent secret by accident.
func keyClass(t string) (string, bool) {
	switch strings.TrimSpace(t) {
	case "", keyTypeSecret:
		return keyTypeSecret, true
	case keyTypePublishable:
		return keyTypePublishable, true
	}
	return "", false
}

// revokeClass resolves which key class a revoke acts on, in the ORDER this route
// has always used: the DECLARED `?type=` (which the typed input carries, because a
// DELETE addresses what it deletes with its URL), and only when the caller sent
// none, the `{"type":…}` request BODY.
//
// The body half cannot live on the input — zip's hasBody says a DELETE carries no
// body, so no generated client would ever send one and the document must not claim
// otherwise — so it is read here, off the request. It is a COMPATIBILITY read for
// callers written against the older shape, not a second way to call this route:
// without it a body-selected revoke would resolve to the empty string, default to
// secret, and destroy the caller's session-equivalent credential in place of the
// publishable one they named.
func revokeClass(in *keyTypeIn, c *zip.Ctx) (string, bool) {
	t := in.Type
	if strings.TrimSpace(t) == "" {
		var body struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(c.Body(), &body)
		t = body.Type
	}
	return keyClass(t)
}

// GetKey returns the caller's own API keys — every type they hold, read
// AUTHORITATIVELY from IAM rather than from the session claim, which lags a key
// minted moments ago. No secret material comes back: a secret key is represented
// by its prefix, and only a publishable key (public by construction) carries its
// full value.
//
// A transient IAM read failure reports an empty set rather than a 5xx, so the
// page shows the honest empty state and never a fabricated key.
func (o ops) getKey(ctx context.Context, _ *noInput) (*apiKeyList, error) {
	cr, c, ok := requestCaller(ctx, true)
	if !ok {
		return nil, zip.ErrForbidden("sign in to manage API keys")
	}
	if !o.s.State.iam.configured() {
		return nil, notConfigured("API key management")
	}
	rows, err := o.s.State.iam.userKeys(c.Context(), cr.owner, cr.username)
	if err != nil {
		// Fail-soft on a transient IAM read: report an empty set rather than 5xx, so the
		// page shows the honest empty state (never a fabricated key). The mint path
		// still 502s loudly on a real failure — reads degrade, writes do not.
		o.s.Log.Warn("get keys: iam read failed (reporting none)", "err", err)
		return &apiKeyList{Keys: []apiKey{}}, nil
	}
	out := apiKeyList{Keys: make([]apiKey, 0, len(rows))}
	for _, r := range rows {
		rec := apiKey{Type: keyTypeSecret, CreatedAt: r.UpdatedTime}
		if r.Scope == iamScopePublish {
			// Publishable: hand back the whole value. It is the one a browser bundle
			// carries, and there is no second chance to read it.
			rec.Type, rec.Key, rec.Prefix = keyTypePublishable, r.AccessKey, prefixOf(r.AccessKey)
		} else {
			// Secret: the prefix only. The AccessKey half identifies the row; the
			// confidential sk- is masked by IAM and never leaves it.
			rec.Prefix = prefixOf(r.AccessKey)
		}
		out.Keys = append(out.Keys, rec)
	}
	return &out, nil
}

// prefixOf is the recognizable, non-secret head of a key — enough for a holder to
// tell two keys apart, never enough to use one.
func prefixOf(key string) string {
	if len(key) > 11 {
		return key[:11]
	}
	return key
}

// MintKey creates — or rotates — the caller's API key of the requested type and
// returns it ONCE. A real IAM failure surfaces as 502, never a fabricated key.
//
// Rotating is what creating means here: a user holds one key per type, so the
// endpoint is idempotent by (caller, type) and the superseded credential stops
// working. Two live secrets for one user would make "revoke my key" a lie.
//
// Example: {"type": "publishable"}
func (o ops) mintKey(ctx context.Context, in *keyTypeIn) (*mintedKey, error) {
	cr, c, ok := requestCaller(ctx, true)
	if !ok {
		return nil, zip.ErrForbidden("sign in to manage API keys")
	}
	if !o.s.State.iam.configured() {
		return nil, notConfigured("API key management")
	}
	typ, ok := keyClass(in.Type)
	if !ok {
		return nil, zip.ErrBadRequest("type must be " + keyTypeSecret + " or " + keyTypePublishable)
	}
	key, err := o.s.State.iam.mintUserKey(c.Context(), cr.keyID(), typ)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "could not mint an API key: %v", err)
	}
	// `key` is the canonical field and `accessKey` its predecessor, carried so the
	// live console keeps working across the deploy; both are the same one value.
	return &mintedKey{Type: typ, Key: key, AccessKey: key}, nil
}

// revokedKey is the answer to a revoke: which class stopped working.
type revokedKey struct {
	// OK is true when the key was revoked. A failure is an error status, never a
	// false here.
	OK bool `json:"ok"`
	// Type is the key class that was revoked, resolved — so a caller that named
	// nothing can see it revoked the secret key.
	Type string `json:"type"`
}

// RevokeKey revokes the caller's own API key of the requested class. The class is
// the same field mint takes — `?type=publishable`, defaulting to secret — so
// revoking the key that ships in a browser bundle does not sign its holder out of
// their own API: the other key keeps working.
//
// Revoking is how a key is replaced when it does not need replacing; minting the
// same class again rotates it in one step. IAM drops the credential immediately,
// but the gateway caches keys for a few minutes, so a request that beat the cache
// expiry may still be served.
//
// For callers written against the older shape, the class is also accepted in a JSON
// request body, read only when `?type=` is absent.
//
// Example: {"type": "publishable"}
func (o ops) revokeKey(ctx context.Context, in *keyTypeIn) (*revokedKey, error) {
	cr, c, ok := requestCaller(ctx, true)
	if !ok {
		return nil, zip.ErrForbidden("sign in to manage API keys")
	}
	if !o.s.State.iam.configured() {
		return nil, notConfigured("API key management")
	}
	typ, ok := revokeClass(in, c)
	if !ok {
		return nil, zip.ErrBadRequest("type must be " + keyTypeSecret + " or " + keyTypePublishable)
	}
	if err := o.s.State.iam.revokeUserKey(c.Context(), cr.keyID(), typ); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "could not revoke the API key: %v", err)
	}
	return &revokedKey{OK: true, Type: typ}, nil
}

// deprecatedFor gates a route served at a superseded path: it answers exactly as
// the canonical path does — the SAME handler, so there is one implementation — and
// says so on the wire (RFC 8594 Deprecation + a Link naming the successor), which is
// how a caller finds out without reading a changelog.
//
// It is a zip.Middleware, which is what lets ONE definition serve every method of
// the alias: `With` carries it into the typed registration, so the announcement is
// a property of the GROUP the ops sit on rather than a wrapper somebody has to
// remember around each handler.
func deprecatedFor(canonical string) zip.Middleware {
	return func(next zip.Handler) zip.Handler {
		return func(c *zip.Ctx) error {
			c.SetHeader("Deprecation", "true")
			c.SetHeader("Link", "<"+canonical+`>; rel="successor-version"`)
			return next(c)
		}
	}
}

// ── onboard (create the caller's org) ────────────────────────────────────────

type onboardReq struct {
	// Name is the organization's display name. Ignored when personal is true, which
	// derives the name from the caller's own username instead.
	Name string `json:"name"`
	// Personal asks for the caller's own workspace: the name is derived from their
	// username and the slug auto-suffixes to stay unique. Meaningless — and refused
	// — for a caller who already has an organization.
	Personal bool `json:"personal"`
}

type onboardResp struct {
	// Org is the created organization's slug, which is what X-Org-Id carries.
	Org string `json:"org"`
	// DisplayName is the organization's human name.
	DisplayName string `json:"displayName"`
	// Additional is true when the caller already had an organization and this one
	// was created WITHOUT moving them into it — they reach it via the org switcher.
	Additional bool `json:"additional"`
}

// Onboard creates the caller's organization. Two flows, keyed on whether the caller
// already has a home org (mirrors app/onboard/route.ts):
//
//   - FIRST-RUN (no owner): create + MOVE the user in as admin, so their next JWT
//     carries the new owner and the cloud scopes everything to it.
//   - ADDITIONAL (owner set): create the org but do NOT move the user — a move
//     changes their IAM owner (stripping a SuperAdmin's status + orphaning their
//     current org). They reach the new org via the OrgSwitcher, which re-scopes
//     X-Org-Id without touching IAM membership. A personal-org request from someone
//     who already has an org is meaningless → 409.
//
// Example: {"name": "Acme"}
func (o ops) onboard(ctx context.Context, in *onboardReq) (*onboardResp, error) {
	cr, c, ok := requestCaller(ctx, false) // first-run onboarding allows a zero-org user
	if !ok {
		return nil, zip.ErrForbidden("sign in to create an organization")
	}
	s := o.s
	if !s.State.iam.configured() {
		return nil, notConfigured("organization creation")
	}
	body := *in
	rctx := c.Context()

	additional := cr.owner != ""
	if additional && body.Personal {
		return nil, zip.ErrConflict("you already have an organization; name the new one explicitly")
	}

	baseSlug, displayName, herr := resolveOnboardName(s, body, cr)
	if herr != nil {
		return nil, herr
	}

	// Resolve a unique slug. Personal orgs auto-suffix to stay unique; an explicit
	// name that's taken is an honest conflict the user resolves by renaming.
	slug, herr := uniqueSlug(s, rctx, baseSlug, body.Personal)
	if herr != nil {
		return nil, herr
	}

	// ADDITIONAL org (caller already has a home): create it WITHOUT moving them —
	// they reach it via the OrgSwitcher (a move would strip their SuperAdmin / orphan
	// their current org).
	if additional {
		org := buildOrg(s, rctx, slug, displayName, body.Personal, cr.owner)
		if err := s.State.iam.createOrganization(rctx, org); err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "could not create the organization: %v", err)
		}
		return &onboardResp{Org: slug, DisplayName: displayName, Additional: true}, nil
	}

	// FIRST-RUN: drive the ONE atomic IAM provision (org + admin move + hashed
	// org-scoped credential), replacing the create-org + move-user pair — a mid-flight
	// retry now converges on the founder's own org instead of orphaning it. The org
	// starts at a zero balance (usage is pre-paid). Prefer it whenever the
	// service-token path is wired; fall back to the legacy pair only when it is not,
	// so a partial deploy still onboards.
	if s.State.iam.provisionReady() {
		resp, err := onboardFirstRun(rctx, s.State.iam, cr.id, slug, displayName, body.Personal)
		if err != nil {
			return nil, err
		}
		return &resp, nil
	}

	// Legacy fallback (service token unset): create then move — the non-atomic pair.
	org := buildOrg(s, rctx, slug, displayName, body.Personal, cr.owner)
	if err := s.State.iam.createOrganization(rctx, org); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "could not create the organization: %v", err)
	}
	if err := s.State.iam.moveUserToOrg(rctx, cr.id, slug); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "org created but could not assign you to it: %v", err)
	}
	return &onboardResp{Org: slug, DisplayName: displayName, Additional: false}, nil
}

// onboardFirstRun drives the ONE atomic IAM provision for a zero-org caller (create
// org + move them in as admin + mint the hashed org-scoped credential), replacing
// the create-org + move-user pair so a mid-flight retry converges on the founder's
// own org instead of orphaning it. The org starts at a ZERO balance — usage is
// pre-paid, so there is no signup grant. Split out so the provisioning glue is
// unit-tested against mock IAM without the CSRF/routing/principal shell.
func onboardFirstRun(ctx context.Context, iam *iamClient, callerID, slug, displayName string, personal bool) (onboardResp, error) {
	row, err := iam.getUserRow(ctx, callerID)
	if err != nil {
		return onboardResp{}, zip.Errorf(http.StatusBadGateway, "could not resolve the user: %v", err)
	}
	res, err := iam.provision(ctx, row.Owner, row.Name, slug, personal)
	if err != nil {
		return onboardResp{}, zip.Errorf(http.StatusBadGateway, "could not provision the organization: %v", err)
	}
	return onboardResp{Org: res.Org, DisplayName: displayName, Additional: false}, nil
}

// resolveOnboardName derives the base slug + display name from the request, or a
// mapped HTTP error. Personal orgs derive from the username; a named org validates
// through the shared policy (onboarding.go).
func resolveOnboardName(s *cloud.Service[state], body onboardReq, cr caller) (baseSlug, displayName string, err error) {
	if body.Personal {
		baseSlug = personalOrgSlug(cr.name)
		if len(baseSlug) < minOrgSlug || isReservedOrg(baseSlug) {
			baseSlug = "org-" + firstNonEmpty(slugifyOrg(cr.name), "workspace")
		}
		return baseSlug, humanize(cr.name), nil
	}
	v := validateOrgName(body.Name)
	if !v.ok {
		return "", "", zip.ErrBadRequest(v.error)
	}
	return v.slug, strings.TrimSpace(body.Name), nil
}

// uniqueSlug returns a free slug at/after base. A named org that's taken is a 409;
// a personal org auto-suffixes (base, base-2, …) up to a small bound.
func uniqueSlug(s *cloud.Service[state], ctx context.Context, base string, personal bool) (string, error) {
	existing, err := s.State.iam.getOrganization(ctx, base)
	if err != nil {
		return "", zip.Errorf(http.StatusBadGateway, "could not check organization availability: %v", err)
	}
	if existing == nil {
		return base, nil
	}
	if !personal {
		return "", zip.Errorf(http.StatusConflict, "“%s” is taken; choose a different name", base)
	}
	free, err := freeSlug(s, ctx, base)
	if err != nil {
		return "", err
	}
	if free == "" {
		return "", zip.Errorf(http.StatusConflict, "could not find an available name")
	}
	return free, nil
}

// freeSlug finds the first free slug at/after base (base, base-2, … base-20), or ""
// if all are taken. Mirrors identity.ts's freeSlug bound of 20.
func freeSlug(s *cloud.Service[state], ctx context.Context, base string) (string, error) {
	for i := 2; i <= 20; i++ {
		trimmed := base
		if len(trimmed) > maxOrgSlug-3 {
			trimmed = trimmed[:maxOrgSlug-3]
		}
		candidate := strings.Trim(fmt.Sprintf("%s-%d", strings.TrimRight(trimmed, "-"), i), "-")
		if len(candidate) < minOrgSlug || isReservedOrg(candidate) {
			continue
		}
		existing, err := s.State.iam.getOrganization(ctx, candidate)
		if err != nil {
			return "", zip.Errorf(http.StatusBadGateway, "could not check organization availability: %v", err)
		}
		if existing == nil {
			return candidate, nil
		}
	}
	return "", nil
}

// buildOrg assembles the new customer org owned by the `admin` org, cloning
// password/locale settings from the caller's current org (best-effort; a nil source
// just yields a minimal org IAM completes with its defaults) and clearing all
// instance-specific material. Mirrors identity.ts's createOrganization body.
func buildOrg(s *cloud.Service[state], ctx context.Context, slug, displayName string, personal bool, sourceOwner string) iamOrg {
	org := iamOrg{Owner: adminOrg, Name: slug, DisplayName: displayName, IsPersonal: personal}
	if sourceOwner == "" {
		return org
	}
	src, err := s.State.iam.getOrganization(ctx, sourceOwner)
	if err != nil || src == nil {
		return org // clone is best-effort; IAM applies its org defaults otherwise
	}
	org.PasswordType = src.PasswordType
	org.PasswordSalt = src.PasswordSalt
	org.PasswordObfuscatorType = src.PasswordObfuscatorType
	org.PasswordObfuscatorKey = src.PasswordObfuscatorKey
	org.PasswordOptions = src.PasswordOptions
	org.CountryCodes = src.CountryCodes
	org.Languages = src.Languages
	org.DefaultAvatar = src.DefaultAvatar
	return org
}

// ── shared helpers ────────────────────────────────────────────────────────────

// notConfigured is the honest 501 for a surface whose confidential client is
// unwired — the deployment simply lacks the `hanzo-console` credential.
func notConfigured(surface string) error {
	return zip.Errorf(http.StatusNotImplemented, "%s is not configured on this deployment (IAM client unset)", surface)
}

// humanize title-cases the base of a username for a personal org's display name
// (dave.smith@x.com → "Dave Smith"). Mirrors identity/onboard humanize().
func humanize(username string) string {
	base := username
	// Split on '@' anywhere (mirrors identity.ts humanize's `includes('@')`), so a
	// bare "@" collapses to "" → "Personal". (personalOrgSlug intentionally uses
	// `> 0` instead, matching its own TS source.)
	if at := strings.IndexByte(base, '@'); at >= 0 {
		base = base[:at]
	}
	base = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '.' || r == '_' || r == '-' {
			return ' '
		}
		return r
	}, base))
	if base == "" {
		return "Personal"
	}
	parts := strings.Fields(base)
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func getenv(key, dflt string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return dflt
}

func basicToken(id, secret string) string {
	return base64.StdEncoding.EncodeToString([]byte(id + ":" + secret))
}
