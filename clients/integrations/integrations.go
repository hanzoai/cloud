// Package integrations is the generic, provider-agnostic OAuth connector plane
// for the unified Hanzo Cloud binary — the /v1/integrations surface that lets an
// org connect a third-party account (Slack today; GitHub scaffolded; Google /
// Salesforce plug into the SAME registry later) and hands the resulting per-org
// tokens to KMS custody.
//
// ONE framework, N providers. A provider self-registers (its file's init calls
// register) into a package registry declaring how to build its authorize URL,
// exchange the code, and revoke. The five HTTP handlers here are provider-blind:
// they resolve the provider by :provider, apply the SAME org gate, CSRF/state,
// KMS custody and console redirect for every one. Adding a provider is a new file,
// never a new route.
//
// Surface (subsystem name "integrations", prefix /v1/integrations):
//
//	GET    /v1/integrations                      list providers + this org's status  -> {providers:[...]}
//	GET    /v1/integrations/:provider            one provider (404 unknown id)        -> Provider
//	POST   /v1/integrations/:provider/connect    begin OAuth (org-authed)             -> {authorizeUrl} | 503 | 403
//	GET    /v1/integrations/:provider/callback   PUBLIC, state-authed                  -> 302 to console
//	POST   /v1/integrations/:provider/disconnect revoke + forget (org-authed)         -> {disconnected:true}
//
// TENANT ISOLATION. connect/list/get/disconnect derive the org from
// principal.Org (a VALIDATED principal — a client-forged X-Org-Id with no
// bearer is refused 403). The callback is Slack/GitHub-initiated and therefore
// UNAUTHENTICATED, so its org is taken ONLY from the HMAC-signed, single-use
// state — never a header (see state.go). Every org that reaches KMS or the store
// is additionally validOrg-checked so it can never smuggle path structure into a
// secret key.
//
// SECRET CUSTODY. Per-org customer tokens live ONLY in KMS (sealed, AES-256-GCM
// envelope), keyed /orgs/{org}/integrations/{provider}. The store holds only
// non-secret connection metadata (external id, account label, granted scopes).
// Provider APP creds (client id/secret) come from ENV, injected by the operator
// from KMS via KMSSecret — never plaintext in the store or a manifest. If KMS is
// not Ready the connect/callback flows fail closed (503 / failure redirect); a
// token is NEVER written in plaintext and NEVER put in SQLite.
package integrations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/kms"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

const (
	// consoleURLEnv overrides the console origin the callback 302-redirects the
	// user back to; defaultConsoleURL is the fallback.
	consoleURLEnv     = "CLOUD_CONSOLE_URL"
	defaultConsoleURL = "https://console.hanzo.ai"

	// kmsEnv is the KMS environment slug every integration secret is stored under.
	// Integrations are not environment-sharded (a connection is per-org, not
	// per-env), so one stable env keeps store/read/delete addressing identical.
	kmsEnv = "default"

	// maxCodeLen bounds the OAuth `code` the callback accepts. A real authorization
	// code is short (a few hundred bytes; well under 1 KiB); anything larger is
	// hostile, so it is rejected before it is handed to a provider Exchange. This is
	// the handler-level cap that also covers the in-process ZAP plane (which replays
	// /v1 handlers without fasthttp's request-URI read-buffer limit).
	maxCodeLen = 2048

	// maxMetaLen bounds a provider-supplied NON-secret metadata field (account
	// label, external id, bot user id, scope) at the ingest boundary. Display names
	// can be long unicode, so 256 is generous — it only stops a hostile/oversized
	// upstream field from bloating the per-org row.
	maxMetaLen = 256
)

// OAuthConfig is a provider's resolved APP credentials, read from ENV at request
// time by the provider's Creds func. ClientID/ClientSecret cover OAuth2; Extra
// carries provider-specific non-secret config (e.g. the GitHub App slug). It is
// NEVER persisted — it lives only for the duration of one authorize/exchange.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	Extra        map[string]string
}

// ExchangeResult is what a provider's Exchange returns after trading the OAuth
// code for tokens. Tokens is a map of KMS secret-name -> secret-value; each entry
// is sealed into the org's KMS namespace. ExternalID/AccountLabel/BotUserID/Scopes
// are NON-secret and land in the connection row.
type ExchangeResult struct {
	Tokens       map[string]string // secret name -> value (sealed into KMS)
	ExternalID   string            // provider account id (Slack team.id / GitHub installation_id)
	AccountLabel string            // human label (Slack team.name / GitHub org login)
	BotUserID    string            // Slack bot_user_id (non-secret)
	Scopes       []string          // granted scopes
	ExpiresAt    int64             // access-token expiry, unix seconds; 0 = non-expiring/unknown. Set by user-plane device/refresh providers; the org plane ignores it.
}

// apiKeyKind marks a Provider whose credential is supplied by the customer and
// verified live, rather than acquired via the 3-legged OAuth flow (the default
// for an empty Kind). See Provider.Kind.
const apiKeyKind = "apikey"

const (
	// userScope marks a Provider on the per-user /v1/connectors plane. User-scoped
	// providers are invisible on /v1/integrations; their rows are keyed
	// (org,user,provider,label) and their secrets live under userPath.
	userScope = "user"
	// accessSecret / refreshSecret are the canonical KMS secret names for OAuth
	// token pairs (shared by the google/gitlab org providers and every user-plane
	// provider). A Provider with Refresh != nil MUST custody its refresh token
	// under refreshSecret — sealTokens and the refresh engine key on the name.
	accessSecret  = "access_token"
	refreshSecret = "refresh_token"
	// defaultLabel names a user's first connection to a provider when the client
	// supplies no label.
	defaultLabel = "default"
	// refreshSkew: refresh when the access token expires within this window, so a
	// token handed to a caller is never seconds from death.
	refreshSkew = 60 * time.Second
	// maxConnectors bounds connectors per (org,user,provider): it caps the
	// storage/KMS amplification an authenticated user can create. Enforced at
	// intake (device start / credential), never at save — a completing device
	// flow or a reconnect to an existing label must not dead-end.
	maxConnectors = 10
)

// VerifyInput is what an apikey provider's Verify receives: the customer's
// credential (from the /connect body, originally read on STDIN by `hanzo
// connector add`) plus an OPTIONAL non-secret account hint the caller may supply
// when the provider's own verify response cannot disclose it (e.g. a Cloudflare
// least-privilege token that can list neither its own name nor its account).
type VerifyInput struct {
	Token     string
	AccountID string
}

// Bundle is an externally obtained OAuth token set (CLI local PKCE) submitted
// for adoption. Access/Refresh are secret; Account is a non-secret hint.
type Bundle struct{ Access, Refresh, Account string }

// DeviceStart is the non-secret-facing half of a started device authorization.
// Code is the provider device handle (secret-adjacent; persisted only in the
// cek-encrypted grants table, never a response). Interval is the raw wire
// value; begin() is the sole normalizer.
type DeviceStart struct {
	Code, UserCode, VerifyURL string
	Interval                  int64 // seconds between polls, raw from the provider
	ExpiresAt                 int64 // unix seconds
}

// Device poll outcomes — closed set.
const (
	pollPending = "pending"
	pollSlow    = "slow"
	pollDone    = "done"
	pollExpired = "expired"
	pollDenied  = "denied"
)

// DevicePoll is one poll outcome. Interval is set for pollSlow (the new poll
// cadence) and on server-throttled pending answers (current cadence, no
// upstream call). Result is set for pollDone and MUST be live-proven by the
// provider (a real token exchange or verify call) — saveUser trusts it.
// Errors returned by Device funcs never carry token or device-code material.
type DevicePoll struct {
	Status   string
	Interval int64           // seconds; pollSlow and throttled pending
	Result   *ExchangeResult // pollDone only
}

// Device is a provider's RFC-8628-style device authorization capability.
type Device struct {
	Start func(ctx context.Context) (*DeviceStart, error)
	Poll  func(ctx context.Context, g Grant) (*DevicePoll, error)
}

// SyncHook pulls provider-side state INTO Hanzo (e.g. a GitHub App installation's
// repo list). It is a #51 seam: DECLARED on Provider, nil for every provider
// today, and NOT wired to any route. When GitHub creds land, github.go sets this
// to the installation-token-minting + repo-sync implementation.
type SyncHook func(ctx context.Context, conn Connection) error

// WritebackHook pushes Hanzo state TO the provider. It is a #51 seam: declared,
// nil today, not wired.
type WritebackHook func(ctx context.Context, conn Connection, payload []byte) error

// Provider is one connectable third-party. Everything provider-specific is a
// field here so the handlers stay provider-blind. The func fields read ENV at
// call time (not at init), so an operator can inject creds without a rebuild.
type Provider struct {
	ID           string   // stable slug, the :provider path segment ("slack","github")
	Name         string   // display name
	Description  string   // one-line card copy
	Category     string   // grouping ("Communication","Developer",...)
	Scopes       []string // requested scopes (display + authorize URL)
	RedirectPath string   // OAuth redirect path; MUST equal /v1/integrations/{id}/callback
	Secrets      []string // KMS secret names this provider custodies (deleted on disconnect)

	// Kind selects credential acquisition. Empty/"oauth" (default) uses the
	// 3-legged Authorize/Exchange flow. "apikey" (apiKeyKind) takes a
	// customer-held credential submitted to /connect (from `hanzo connector add`,
	// read on STDIN — never argv/URL), VERIFIES it live, and seals it to KMS; such
	// providers use Verify, not Authorize/Exchange, and have no OAuth callback.
	Kind string
	// AdminOnly gates /connect and /disconnect on the caller being an admin of its
	// OWN org (principal.IsOrgAdmin — NOT SuperAdmin), parity with the platform
	// deploy-provider adminProcedure. OAuth social/chat providers leave it false.
	AdminOnly bool
	// Verify validates an apikey credential against the provider and returns the
	// token(s) to seal + non-secret account metadata. It MUST fail closed (a
	// bad/inactive credential returns an error and NOTHING is stored) and its error
	// MUST NOT contain the credential value (it is logged). nil for oauth providers.
	// On the user plane it doubles as the token/apikey intake method.
	Verify func(ctx context.Context, in VerifyInput) (*ExchangeResult, error)

	// Scope selects the custody plane: "" = org-scoped /v1/integrations (default);
	// userScope = per-user /v1/connectors (rows keyed (org,user,provider,label),
	// KMS under /orgs/{org}/users/{user}/connectors/...). The planes are disjoint:
	// a user-scoped provider 404s on the org surface and vice versa; Mount asserts
	// scope coherence at boot.
	Scope string
	// Device: device-code sign-in (user scope). nil = unsupported.
	Device *Device
	// Adopt verifies an externally obtained OAuth bundle (CLI local PKCE) before
	// custody. Implementations MUST live-verify (e.g. one refresh) and return the
	// rotated material — custody owns the canonical refresh token afterwards.
	// nil = unsupported.
	Adopt func(ctx context.Context, b Bundle) (*ExchangeResult, error)
	// Refresh trades a refresh token for rotated material. The result MUST carry
	// Secrets[0] and refreshSecret entries and an ExpiresAt. nil = static credential.
	Refresh func(ctx context.Context, refresh string) (*ExchangeResult, error)

	// Configured reports whether the provider's APP creds are present in ENV.
	// When false: available=false in the card, and connect/callback fail closed
	// with an honest 503 / failure redirect (never a dead-end, never a fake OK).
	// Org plane only — nil for Scope == userScope (nothing on the user plane
	// calls it; list() skips user providers before providerViewFor).
	Configured func() bool
	// Creds resolves the APP creds from ENV. Called only when Configured is true.
	// Org plane only — nil for Scope == userScope.
	Creds func() OAuthConfig
	// Authorize builds the provider's consent URL for (creds, redirectURI, state).
	Authorize func(creds OAuthConfig, redirectURI, state string) (string, error)
	// Exchange trades the OAuth code for tokens + account metadata.
	Exchange func(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error)
	// Revoke best-effort invalidates a token at the provider on disconnect. nil
	// when the provider has no revoke endpoint.
	Revoke func(ctx context.Context, creds OAuthConfig, token string) error

	// #51 seams — declared, nil today, not wired to a route (see SyncHook/WritebackHook).
	Sync      SyncHook
	Writeback WritebackHook
}

// registry is populated by each provider file's register() from its init(). Go
// initializes this map before any init() runs, so every provider is present by
// the time Mount snapshots it.
var registry = map[string]*Provider{}

// register adds a provider to the package registry. Called once per provider from
// its file's init(). A duplicate id is a programming error and panics at init —
// two providers cannot own the same :provider slug.
func register(p *Provider) {
	if p == nil || p.ID == "" {
		panic("integrations: register nil/empty provider")
	}
	if _, dup := registry[p.ID]; dup {
		panic("integrations: duplicate provider id " + p.ID)
	}
	registry[p.ID] = p
}

// state is integrations' own data; shared deps live in the embedded cloud.Base —
// logger (s.Log), deployment domain (s.Domain, e.g. api.hanzo.ai — builds the
// redirect_uri). mounted is the in-process seam other subsystems (the bridge) reach
// through the package funcs at the bottom of this file.
type state struct {
	store      *Store
	kms        *kms.Client // concrete client type-asserted from deps.KMS; nil ⇒ secret ops fail closed
	consoleURL string      // where the callback 302s the user back to
	stateKey   []byte      // HMAC-SHA256 key for the CSRF/org-binding state
	providers  map[string]*Provider
	flight     *flight // keyed in-process mutex: refresh + device-poll serialization
}

var mounted *cloud.Service[state]

// ── HTTP response shapes (the published contract) ──────────────────────────────

type providerView struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Category    string          `json:"category"`
	Available   bool            `json:"available"` // creds configured
	Connected   bool            `json:"connected"` // this org has a connection
	Connection  *connectionView `json:"connection,omitempty"`
}

type connectionView struct {
	Account     string   `json:"account"`
	ExternalID  string   `json:"externalId"`
	Scopes      []string `json:"scopes"`
	ConnectedAt string   `json:"connectedAt"`
}

// ── Mount / lifecycle ──────────────────────────────────────────────────────────

// Mount wires /v1/integrations/* onto app. Complex flavour: it publishes the
// package global `mounted` (the in-process token-custody seam) and pairs with a
// Shutdown, so it constructs the cloud.Service value directly (cloud.NewBase +
// &cloud.Service[state]{…}) rather than via cloud.Mount.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("integrations.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("integrations.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("integrations.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("integrations.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "integrations.db"))
	if err != nil {
		return fmt.Errorf("integrations.Mount: open store: %w", err)
	}

	// deps.KMS is the in-process cloud.KMSClient. The interface exposes only
	// GetSecret/PutSecret/Sign; per-secret Delete + Ready live on the concrete
	// client, so we type-assert to it exactly as clients/kms does. A non-KMS
	// impl (RPC/disabled) leaves this nil and every secret op fails closed.
	kc, _ := deps.KMS.(*kms.Client)

	providers := snapshotRegistry()
	// Fail LOUD at boot on an incoherent provider — the plane split is structural,
	// not discipline-based. A user-scoped provider must declare NO org-plane
	// OAuth/config surface (nothing on /v1/connectors calls them) and at least one
	// user intake method; an org provider must declare its config surface, and its
	// RedirectPath must be the path the generic callback dispatcher actually
	// serves — otherwise its OAuth callback would 404 silently. One route, one truth.
	for id, p := range providers {
		if p.Scope == userScope {
			if p.Authorize != nil || p.Exchange != nil || p.Revoke != nil ||
				p.RedirectPath != "" || p.Configured != nil || p.Creds != nil {
				_ = store.Close()
				return fmt.Errorf("integrations.Mount: user-scoped provider %q must not declare org OAuth/config fields", id)
			}
			if p.Device == nil && p.Adopt == nil && p.Verify == nil {
				_ = store.Close()
				return fmt.Errorf("integrations.Mount: user-scoped provider %q needs at least one of Device/Adopt/Verify", id)
			}
			continue
		}
		if p.Configured == nil || p.Creds == nil {
			_ = store.Close()
			return fmt.Errorf("integrations.Mount: org provider %q must declare Configured and Creds", id)
		}
		if p.Kind == apiKeyKind {
			// apikey providers have no OAuth callback; RedirectPath is unused.
			continue
		}
		if want := callbackPath(id); p.RedirectPath != want {
			_ = store.Close()
			return fmt.Errorf("integrations.Mount: provider %q RedirectPath %q must equal %q", id, p.RedirectPath, want)
		}
	}

	b := cloud.NewBase(deps, "integrations")
	s := &cloud.Service[state]{
		Base: b,
		State: state{
			store:      store,
			kms:        kc,
			consoleURL: consoleURL(),
			stateKey:   resolveStateKey(os.Getenv(stateKeyEnv), b.Log),
			providers:  providers,
			flight:     &flight{m: map[string]*hold{}},
		},
	}
	mounted = s

	routes(app, s)

	b.Log.Info(
		"integrations mounted",
		"providers", len(s.State.providers),
		"kmsReady", kmsReady(s),
		"domain", s.Domain,
		"console", s.State.consoleURL,
		"brand", deps.Brand,
	)
	return nil
}

// routes registers the integrations surface on app. The LITERAL slack bridge paths
// are registered BEFORE the /:provider wildcards so they win under the router's
// registration-order matching and a later `:provider`-shaped change can never
// shadow them (same discipline as clients/agents' static-before-:ref rule). They
// are PUBLIC at the JWT layer — reached like /:provider/callback (IdentityMiddleware
// only POPULATES a principal, never rejects; DefaultPrice returns 0 so BillingGate
// passes through) — because their auth is done INSIDE the handler: HMAC-SHA256 over
// the raw body for the events/commands webhooks, and signed __Host- cookie + state
// for the link legs. They must NOT be placed behind any principal/tenant gate.
//
// Every verify-inside inbound webhook (slack events/commands, discord interactions,
// teams events, telegram webhook — like /v1/connector/github/webhook) is
// cloud.Terminal-wrapped so its bad-signature 401 / malformed-body 400 is written
// in-band and survives the commerce /v1 ErrorHandlerJSON (co-mounted ahead of us),
// which would otherwise flatten a propagated 4xx to 500. Uniform reject codes.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/integrations", cloud.Handle(s, list))
	app.Post("/v1/integrations/slack/events", cloud.Terminal(cloud.Handle(s, slackEvents)))
	app.Post("/v1/integrations/slack/commands", cloud.Terminal(cloud.Handle(s, slackCommands)))
	app.Get("/v1/integrations/slack/link", cloud.Handle(s, slackLink))
	app.Get("/v1/integrations/slack/link/slack", cloud.Handle(s, slackLinkSlack))
	app.Get("/v1/integrations/slack/link/callback", cloud.Handle(s, slackLinkCallback))
	// GitHub App sync (github_app.go / github_webhook.go). The App POSTs push events
	// to /v1/connector/github/webhook — the EXTERNAL-platform namespace
	// /v1/connector/<provider>/webhook (github now; gitlab/others are sibling literal
	// routes later, each with its own signature scheme + handler). It is PUBLIC at the
	// JWT layer, HMAC-verified inside, and hands the push to the universal sync engine
	// (cloud.Sync). cloud.Terminal writes the handler's reject status in-band so the
	// commerce /v1 ErrorHandlerJSON (co-mounted ahead of us) cannot flatten a bad-sig
	// 401 / malformed-body 400 to 500. repos/import register BEFORE the /:provider
	// wildcards (registration-order matching) and are org-authed via the principal.
	app.Post("/v1/connector/github/webhook", cloud.Terminal(cloud.Handle(s, githubWebhook)))
	app.Get("/v1/integrations/github/repos", cloud.Handle(s, githubRepos))
	app.Post("/v1/integrations/github/repos/import", cloud.Handle(s, githubImport))
	// GitHub Pages management (github_pages.go), one repo as a resource. Registered
	// AFTER the literal /repos/import so registration-order matching keeps the literal
	// unshadowed; the :repo routes all carry a /pages suffix, so /repos/import (no
	// suffix) never matches them. Org-authed via the principal; the repo is resolved
	// against the installation's granted set (owner is server-derived).
	app.Get("/v1/integrations/github/repos/:repo/pages", cloud.Handle(s, githubPagesGet))
	app.Post("/v1/integrations/github/repos/:repo/pages", cloud.Handle(s, githubPagesEnable))
	app.Put("/v1/integrations/github/repos/:repo/pages", cloud.Handle(s, githubPagesUpdate))
	app.Delete("/v1/integrations/github/repos/:repo/pages", cloud.Handle(s, githubPagesDisable))
	app.Post("/v1/integrations/github/repos/:repo/pages/builds", cloud.Handle(s, githubPagesBuild))
	// ChatBridge adapters (bridge.go + discord/teams/telegram). Same discipline as
	// the slack bridge: the literal paths register BEFORE the /:provider wildcards so
	// they win under registration-order matching. All PUBLIC at the JWT layer — auth
	// is done INSIDE each handler: Discord Ed25519 interaction verify, Teams Bot
	// Framework JWT, Telegram secret-token; the link legs use signed __Host- cookies +
	// state. Telegram's /connect is org-authed via the principal (like the framework
	// connect). They must NOT sit behind any principal/tenant gate.
	app.Post("/v1/integrations/discord/interactions", cloud.Terminal(cloud.Handle(s, discordInteractions)))
	app.Get("/v1/integrations/discord/link", cloud.Handle(s, discordLink))
	app.Get("/v1/integrations/discord/link/discord", cloud.Handle(s, discordLinkDiscord))
	app.Get("/v1/integrations/discord/link/callback", cloud.Handle(s, discordLinkCallback))
	app.Post("/v1/integrations/teams/events", cloud.Terminal(cloud.Handle(s, teamsEvents)))
	app.Get("/v1/integrations/teams/link", cloud.Handle(s, teamsLink))
	app.Get("/v1/integrations/teams/link/aad", cloud.Handle(s, teamsLinkAAD))
	app.Get("/v1/integrations/teams/link/callback", cloud.Handle(s, teamsLinkCallback))
	app.Post("/v1/integrations/telegram/connect", cloud.Handle(s, telegramConnect))
	app.Post("/v1/integrations/telegram/webhook", cloud.Terminal(cloud.Handle(s, telegramWebhook)))
	app.Get("/v1/integrations/telegram/link", cloud.Handle(s, telegramLink))
	app.Get("/v1/integrations/telegram/link/auth", cloud.Handle(s, telegramLinkAuth))
	app.Get("/v1/integrations/telegram/link/callback", cloud.Handle(s, telegramLinkCallback))
	app.Get("/v1/integrations/:provider", cloud.Handle(s, get))
	app.Post("/v1/integrations/:provider/connect", cloud.Handle(s, connect))
	// PUBLIC, state-authed. RedirectPath == this path for every provider (asserted
	// in Mount), so this single generic route serves every provider's OAuth callback.
	app.Get("/v1/integrations/:provider/callback", cloud.Handle(s, callback))
	app.Post("/v1/integrations/:provider/disconnect", cloud.Handle(s, disconnect))
	// apikey connectors: re-verify a stored credential live (`hanzo connector verify`).
	app.Post("/v1/integrations/:provider/verify", cloud.Handle(s, verifyConn))
	// Per-USER connector plane (/v1/connectors — connectors.go). Own prefix, so no
	// shadowing interplay with the /:provider wildcards above.
	connectorRoutes(app, s)
}

// Shutdown closes the store. Idempotent — safe when nothing is mounted.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	var err error
	if mounted.State.store != nil {
		err = mounted.State.store.Close()
	}
	mounted = nil
	return err
}

// snapshotRegistry copies the global registry into a per-mount map so a mounted
// service reads a stable set (and a test can construct one deterministically)
// without aliasing package state.
func snapshotRegistry() map[string]*Provider {
	out := make(map[string]*Provider, len(registry))
	for id, p := range registry {
		out[id] = p
	}
	return out
}

// ── handlers ───────────────────────────────────────────────────────────────────

// list returns every registered provider with this org's connection status.
// Org-authed: a caller with no validated principal is 403 (the status is per-org,
// so an org is required).
func list(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	ids := sortedProviderIDs(s)
	out := make([]providerView, 0, len(ids))
	for _, id := range ids {
		p := s.State.providers[id]
		if p.Scope == userScope {
			continue // user-plane providers are invisible on the org surface
		}
		v, err := providerViewFor(s, c.Context(), org, p)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
		}
		out = append(out, v)
	}
	return c.JSON(http.StatusOK, map[string]any{"providers": out})
}

// get returns one provider (404 for an unknown id) with this org's status.
func get(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	p, ok := orgProvider(s, providerParam(c))
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}
	v, err := providerViewFor(s, c.Context(), org, p)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	return c.JSON(http.StatusOK, v)
}

// connect begins an OAuth flow: it mints a single-use nonce + HMAC-signed state
// binding this org to this provider, and returns the provider's authorize URL.
// Fail-closed order: no principal → 403; unknown provider → 404; not configured
// → 503; KMS not ready → 503 (the flow WILL need to seal a token, so refuse now
// rather than dead-end at the callback).
func connect(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required to connect an integration")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	p, ok := orgProvider(s, providerParam(c))
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}
	// Mutating a connector is an org-admin action for providers that declare it
	// (parity with the platform deploy-provider adminProcedure). The predicate is
	// the caller's OWN-org isAdmin bit (principal.IsOrgAdmin) — NOT SuperAdmin.
	if p.AdminOnly && !principal.IsOrgAdmin(c) {
		return zip.ErrForbidden("connecting the " + p.ID + " connector requires org admin")
	}
	if !p.Configured() {
		return zip.Errorf(http.StatusServiceUnavailable, "%s integration is not configured on this deployment", p.ID)
	}
	if !kmsReady(s) {
		return zip.Errorf(http.StatusServiceUnavailable, "%s", kms.ErrMasterKeyMissing.Error())
	}
	// Pick the credential-acquisition path by REQUEST. A provider may offer an
	// apikey path (Verify) and/or an OAuth path (Authorize). A credential in the
	// /connect body seals via apikey (verify-before-store; NOTHING persisted on a
	// bad credential); its absence starts the OAuth flow below. A provider with only
	// one path always takes it — an apikey-only provider with no token still returns
	// connectByCredential's helpful "token required" 400.
	if p.Verify != nil && (p.Authorize == nil || bodyHasCredential(c)) {
		return connectByCredential(s, c, org, p)
	}
	if p.Authorize == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "%s: no connect method configured", p.ID)
	}
	// The OAuth leg needs Hanzo's registered app creds. A dual-path provider keeps
	// Configured()==true for its always-available apikey path, so gate OAuth on its
	// OWN creds here — an honest 503 that points the caller at the token path rather
	// than a dead consent URL with an empty client_id.
	if p.Creds().ClientID == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "%s OAuth is not configured on this deployment; connect with a scoped API token instead", p.ID)
	}

	// Opportunistic GC of expired nonces (best-effort; never fails the request).
	if _, err := s.State.store.GCNonces(c.Context(), staleNonceCutoff()); err != nil {
		s.Log.Warn("nonce gc", "err", err)
	}

	nonce, err := genToken()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	if err := s.State.store.PutNonce(c.Context(), nonce, org, p.ID); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "nonce: %v", err)
	}
	state, err := sign(s, org, p.ID, nonce)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "state: %v", err)
	}
	authorizeURL, err := p.Authorize(p.Creds(), redirectURI(s, p), state)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "authorize: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"authorizeUrl": authorizeURL})
}

// maxCredentialLen bounds the credential an apikey /connect accepts. Real provider
// API tokens are short (a Cloudflare token is ~40 chars); anything over 8 KiB is
// hostile and rejected before it reaches Verify or KMS.
const maxCredentialLen = 8192

// bodyHasCredential reports whether the /connect body carries an apikey credential
// INTENT — the signal that selects the apikey seal over the OAuth flow for a
// provider that offers both. The signal is PRESENCE of the "token" key, not its
// value: {"token":"…"} (even empty/whitespace) is an apikey attempt (→ verify,
// which answers the "token required" 400 on an empty value); a body with no token
// key (the console "Connect" button, `hanzo connector add` with no --token) starts
// OAuth. A malformed body reads as "no credential" (→ OAuth). Never logs the token.
func bodyHasCredential(c *zip.Ctx) bool {
	var b struct {
		Token *string `json:"token"`
	}
	_ = json.Unmarshal(c.Body(), &b)
	return b.Token != nil
}

// connectByCredential completes an apikey connector. The caller submits the
// provider credential in the request body (from `hanzo connector add`, read on
// STDIN — never argv/URL); the provider VERIFIES it live; and ONLY on success is
// the token sealed into the org's KMS namespace with non-secret metadata written
// to the connection row. FAIL-CLOSED: a bad/inactive credential is refused and
// NOTHING is stored (no KMS write, no row). The credential value never appears in
// a log line, the response, or the store — only in the KMS seal input.
func connectByCredential(s *cloud.Service[state], c *zip.Ctx, org string, p *Provider) error {
	if p.Verify == nil {
		return zip.Errorf(http.StatusInternalServerError, "%s: apikey provider without Verify", p.ID)
	}
	var body struct {
		Token     string `json:"token"`
		AccountID string `json:"accountId"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		return zip.ErrBadRequest("a credential token is required (pipe it on stdin: `hanzo connector add --provider " + p.ID + " --token -`)")
	}
	if len(token) > maxCredentialLen {
		return zip.ErrBadRequest("credential too large")
	}
	res, err := p.Verify(c.Context(), VerifyInput{Token: token, AccountID: sanitizeMeta(strings.TrimSpace(body.AccountID))})
	if err != nil || res == nil {
		// Verify FAILED → refuse; store NOTHING. err is provider-authored and must
		// not carry the credential value (only its status/reason).
		s.Log.Warn("connector verify failed", "provider", p.ID, "org", org, "err", err)
		return zip.ErrBadRequest("credential verification failed")
	}
	// Harden provider-supplied NON-secret metadata (strip control chars, bound
	// length) — never the secret token, which goes straight to the KMS seal.
	sanitizeResult(res)
	// Seal every verified secret into the org's KMS namespace BEFORE the row, so a
	// KMS failure leaves NO half-connected row advertising a token that was never
	// stored (same ordering discipline as the OAuth callback).
	if err := sealTokens(s, kmsPath(org, p.ID), res.Tokens); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "secret custody failed")
	}
	conn := Connection{
		Org:          org,
		Provider:     p.ID,
		ExternalID:   res.ExternalID,
		AccountLabel: res.AccountLabel,
		BotUserID:    res.BotUserID,
		Scopes:       res.Scopes,
	}
	if err := s.State.store.Upsert(c.Context(), conn); err != nil {
		s.Log.Warn("connection upsert failed", "provider", p.ID, "org", org, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "persist failed")
	}
	s.Log.Info("connector connected", "provider", p.ID, "org", org, "account", res.AccountLabel, "externalId", res.ExternalID)
	return c.JSON(http.StatusOK, map[string]any{
		"connected":  true,
		"provider":   p.ID,
		"account":    res.AccountLabel,
		"externalId": res.ExternalID,
		"scopes":     nonNil(res.Scopes),
	})
}

// verifyConn re-checks a CONNECTED apikey connector's stored credential against the
// provider, live (`hanzo connector verify`). Org-scoped (any member may check
// status); the credential is read from KMS, verified, and NEVER returned or logged.
// A verification failure is reported as {active:false}, not an error — the console/
// CLI renders it. Only apikey providers support verify (OAuth tokens are checked at
// use, not re-verified here).
func verifyConn(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	p, ok := orgProvider(s, providerParam(c))
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}
	if p.Kind != apiKeyKind || p.Verify == nil || len(p.Secrets) == 0 {
		return zip.ErrBadRequest("verify is only supported for credential connectors")
	}
	_, found, err := s.State.store.Get(c.Context(), org, p.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if !found {
		return zip.ErrNotFound("connector not connected")
	}
	if !kmsReady(s) {
		return zip.Errorf(http.StatusServiceUnavailable, "%s", kms.ErrMasterKeyMissing.Error())
	}
	tok, err := kmsGet(s, kmsPath(org, p.ID), p.Secrets[0])
	if err != nil || len(tok) == 0 {
		return zip.ErrBadRequest("stored credential unavailable")
	}
	res, verr := p.Verify(c.Context(), VerifyInput{Token: string(tok)})
	if verr != nil || res == nil {
		return c.JSON(http.StatusOK, map[string]any{"provider": p.ID, "active": false, "reason": "verification failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"provider":   p.ID,
		"active":     true,
		"account":    sanitizeMeta(res.AccountLabel),
		"externalId": sanitizeMeta(res.ExternalID),
		"scopes":     nonNil(sanitizeScopes(res.Scopes)),
	})
}

// callback is the PUBLIC, state-authed OAuth return. It ALWAYS 302s the user back
// to the console (success or a labeled failure) — never a raw JSON dead-end. The
// org comes ONLY from the signed, single-use state; no header is trusted here.
func callback(s *cloud.Service[state], c *zip.Ctx) error {
	pid := providerParam(c)
	p, ok := orgProvider(s, pid)
	if !ok {
		return failRedirect(s, c, pid, "unknown provider")
	}

	payload, err := verify(s, c.Query("state"), p.ID)
	if err != nil {
		return failRedirect(s, c, p.ID, "invalid state")
	}
	// Consume the single-use nonce, bound to (org,provider). Burned BEFORE the
	// exchange so one state = one attempt: a replay (or a slow-flow retry) finds
	// zero rows and fails here, never double-exchanging.
	consumed, err := s.State.store.ConsumeNonce(c.Context(), payload.Nonce, payload.Org, p.ID)
	if err != nil {
		return failRedirect(s, c, p.ID, "state error")
	}
	if !consumed {
		return failRedirect(s, c, p.ID, "state already used or expired")
	}
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		return failRedirect(s, c, p.ID, "authorization denied")
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		// A GitHub App install returns installation_id (+ setup_action), not an OAuth
		// code, unless "request user authorization during installation" is enabled.
		// Surface it as the identifier the provider's Exchange trades — the ONE
		// generalization the App model needs (OAuth providers always have `code`).
		code = strings.TrimSpace(c.Query("installation_id"))
	}
	if code == "" {
		return failRedirect(s, c, p.ID, "missing authorization code")
	}
	if len(code) > maxCodeLen {
		return failRedirect(s, c, p.ID, "authorization code too large")
	}
	if !p.Configured() {
		return failRedirect(s, c, p.ID, "provider not configured")
	}
	if !kmsReady(s) {
		return failRedirect(s, c, p.ID, "secret store unavailable")
	}

	res, err := p.Exchange(c.Context(), p.Creds(), redirectURI(s, p), code)
	if err != nil || res == nil {
		s.Log.Warn("oauth exchange failed", "provider", p.ID, "org", payload.Org, "err", err)
		return failRedirect(s, c, p.ID, "token exchange failed")
	}
	// Harden the provider-supplied NON-secret metadata at the framework ingest
	// boundary — ONE place, every provider — before it is logged, stored, or
	// reflected in the redirect. Secret token VALUES are never passed through
	// here; they go straight to the KMS seal.
	sanitizeResult(res)
	// Seal every returned token into the org's KMS namespace BEFORE writing the
	// connection row, so a KMS failure leaves NO half-connected state advertising a
	// token that was never stored.
	if err := sealTokens(s, kmsPath(payload.Org, p.ID), res.Tokens); err != nil {
		return failRedirect(s, c, p.ID, "secret custody failed")
	}
	conn := Connection{
		Org:          payload.Org,
		Provider:     p.ID,
		ExternalID:   res.ExternalID,
		AccountLabel: res.AccountLabel,
		BotUserID:    res.BotUserID,
		Scopes:       res.Scopes,
	}
	if err := s.State.store.Upsert(c.Context(), conn); err != nil {
		s.Log.Warn("connection upsert failed", "provider", p.ID, "org", payload.Org, "err", err)
		return failRedirect(s, c, p.ID, "persist failed")
	}
	s.Log.Info("integration connected", "provider", p.ID, "org", payload.Org, "account", res.AccountLabel, "externalId", res.ExternalID)
	return successRedirect(s, c, p.ID, res.AccountLabel)
}

// disconnect revokes (best-effort) and forgets an org's connection: it deletes
// every custodied KMS secret and the connection row. Idempotent — disconnecting a
// provider that was never connected still returns {disconnected:true}.
func disconnect(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required to disconnect an integration")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	p, ok := orgProvider(s, providerParam(c))
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}
	// Symmetric with connect: disconnecting an admin-only connector requires the
	// caller be an admin of its OWN org (principal.IsOrgAdmin — NOT SuperAdmin).
	if p.AdminOnly && !principal.IsOrgAdmin(c) {
		return zip.ErrForbidden("disconnecting the " + p.ID + " connector requires org admin")
	}

	_, found, err := s.State.store.Get(c.Context(), org, p.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}

	// Best-effort provider-side revoke using the primary custodied secret. Never
	// fails the disconnect: local forgetting is authoritative for the tenant.
	if found && p.Revoke != nil && len(p.Secrets) > 0 && kmsReady(s) && p.Configured() {
		if tok, gerr := kmsGet(s, kmsPath(org, p.ID), p.Secrets[0]); gerr == nil {
			if rerr := p.Revoke(c.Context(), p.Creds(), string(tok)); rerr != nil {
				s.Log.Warn("provider revoke failed (continuing)", "provider", p.ID, "org", org, "err", rerr)
			}
		}
	}
	// Delete every custodied secret from KMS (ignore not-found — idempotent).
	if s.State.kms != nil {
		for _, name := range p.Secrets {
			if derr := kmsDelete(s, kmsPath(org, p.ID), name); derr != nil {
				s.Log.Warn("kms delete failed (continuing)", "provider", p.ID, "org", org, "secret", name, "err", derr)
			}
		}
	}
	if _, derr := s.State.store.Delete(c.Context(), org, p.ID); derr != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", derr)
	}
	return c.JSON(http.StatusOK, map[string]any{"disconnected": true})
}

// ── view + redirect builders ───────────────────────────────────────────────────

// providerViewFor renders a provider's card for an org, folding in the org's live
// connection status. (Named …For, not providerView, because Go forbids a func and
// the providerView TYPE sharing an identifier — same deviation as ConnectionFor.)
func providerViewFor(s *cloud.Service[state], ctx context.Context, org string, p *Provider) (providerView, error) {
	v := providerView{
		ID: p.ID, Name: p.Name, Description: p.Description, Category: p.Category,
		Available: p.Configured(),
	}
	conn, found, err := s.State.store.Get(ctx, org, p.ID)
	if err != nil {
		return providerView{}, err
	}
	if found {
		v.Connected = true
		v.Connection = &connectionView{
			Account:     conn.AccountLabel,
			ExternalID:  conn.ExternalID,
			Scopes:      nonNil(conn.Scopes),
			ConnectedAt: rfc3339(conn.ConnectedAt),
		}
	}
	return v, nil
}

func sortedProviderIDs(s *cloud.Service[state]) []string {
	ids := make([]string, 0, len(s.State.providers))
	for id := range s.State.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// successRedirect 302s to {console}/integrations?connected={provider}&account=<label>.
func successRedirect(s *cloud.Service[state], c *zip.Ctx, provider, account string) error {
	return redirect(c, consoleRedirectURL(s.State.consoleURL, "connected", provider, "account", account))
}

// failRedirect 302s to {console}/integrations?error={provider}&reason=<short msg>.
func failRedirect(s *cloud.Service[state], c *zip.Ctx, provider, reason string) error {
	return redirect(c, consoleRedirectURL(s.State.consoleURL, "error", provider, "reason", reason))
}

// consoleRedirectURL builds the ONE console-return URL shape both redirects use:
// {base}/integrations?{statusKey}={provider}&{detailKey}={detail}. base is the
// env-fixed console origin; every DYNAMIC segment (provider, and the possibly
// provider-supplied detail such as a Slack workspace name) is query-escaped, so
// it can never break out of the query into the path/host/scheme — nor inject a
// CR/LF into the Location header. The redirect host is always the console origin.
func consoleRedirectURL(base, statusKey, provider, detailKey, detail string) string {
	return base + "/integrations?" + statusKey + "=" + url.QueryEscape(provider) +
		"&" + detailKey + "=" + url.QueryEscape(detail)
}

// redirect writes a 302 with the Location header. stdlib-clean: no dependency on
// Fiber's redirect builder (which carries flash-cookie state we do not want).
func redirect(c *zip.Ctx, loc string) error {
	c.SetHeader("Location", loc)
	return c.NoContent(http.StatusFound)
}

// redirectURI is the operator-facing OAuth redirect the provider posts back to.
// Built from the deployment domain + the provider's RedirectPath (asserted equal
// to the generic callback path at Mount).
func redirectURI(s *cloud.Service[state], p *Provider) string {
	return "https://" + s.Domain + p.RedirectPath
}

// ── KMS custody (per-org, sealed) ──────────────────────────────────────────────

func kmsReady(s *cloud.Service[state]) bool { return s.State.kms != nil && s.State.kms.Ready() }

// kmsPath is the per-org, per-provider KMS namespace: /orgs/{org}/integrations/{provider}.
// org is validOrg-checked at every entry point, so it can never smuggle path
// structure; provider is a fixed registry slug.
func kmsPath(org, provider string) string {
	return "/orgs/" + org + "/integrations/" + provider
}

// userPath is the per-user connector namespace:
// /orgs/{org}/users/{user}/connectors/{provider}/{label}. Segments are
// pre-validated (validOrg/validUser/registry slug/validLabel) but the COMBINED
// path can exceed the KMS 253-byte cap (a 128-char user + 64-char label
// overflows), so the full path is checked with kms.ValidSubpath — failure is a
// ready-made 400 *zip.HTTPError (client input, never a 503).
func userPath(org, user, provider, label string) (string, error) {
	p := "/orgs/" + org + "/users/" + user + "/connectors/" + provider + "/" + label
	if !kms.ValidSubpath(p) {
		return "", zip.ErrBadRequest("user and label combine into a custody path that is too long")
	}
	return p, nil
}

// The wrappers are path-first: org callers pass kmsPath(org, provider), user
// callers a validated userPath — ONE seal/open/delete implementation, two planes.
func kmsPut(s *cloud.Service[state], path, name string, value []byte) error {
	return s.State.kms.Put(path, name, kmsEnv, value)
}

func kmsGet(s *cloud.Service[state], path, name string) ([]byte, error) {
	return s.State.kms.Get(path, name, kmsEnv)
}

func kmsDelete(s *cloud.Service[state], path, name string) error {
	err := s.State.kms.Delete(path, name, kmsEnv)
	if errors.Is(err, kms.ErrSecretNotFound) {
		return nil // idempotent — deleting an absent secret is not an error
	}
	return err
}

// sealTokens seals every verified secret at path BEFORE any row is written
// (seal-before-row: a KMS failure must leave no half-connected row).
// Deterministic order, refreshSecret FIRST then sorted rest: the refresh token
// is the recovery root — on rotation the provider already invalidated the old
// one, so a partial failure must never leave a new access token beside a dead
// refresh token. Logs path + secret NAME on failure, never a value.
func sealTokens(s *cloud.Service[state], path string, tokens map[string]string) error {
	names := make([]string, 0, len(tokens))
	for name := range tokens {
		if name != refreshSecret {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if _, ok := tokens[refreshSecret]; ok {
		names = append([]string{refreshSecret}, names...)
	}
	for _, name := range names {
		if err := kmsPut(s, path, name, []byte(tokens[name])); err != nil {
			s.Log.Warn("kms seal failed", "path", path, "secret", name, "err", err)
			return err
		}
	}
	return nil
}

// saveUser admits a VERIFIED result into per-user custody — the SINGLE
// user-plane admission point (credential intake, device-flow completion, and
// token refresh all land here): sanitizeResult, then seal-before-row at
// userPath (deterministic, refresh-first), then UpsertConnector. Errors are
// ready-made *zip.HTTPError values (400 invalid path / 503 "secret custody
// failed" / 500 "persist failed") so callers propagate them untouched.
func saveUser(ctx context.Context, s *cloud.Service[state], org, user, label string, p *Provider, res *ExchangeResult) (Connector, error) {
	sanitizeResult(res)
	path, err := userPath(org, user, p.ID, label)
	if err != nil {
		return Connector{}, err
	}
	if err := sealTokens(s, path, res.Tokens); err != nil {
		return Connector{}, zip.Errorf(http.StatusServiceUnavailable, "secret custody failed")
	}
	conn := Connector{
		Org:          org,
		User:         user,
		Provider:     p.ID,
		Label:        label,
		ExternalID:   res.ExternalID,
		AccountLabel: res.AccountLabel,
		Scopes:       res.Scopes,
		ExpiresAt:    res.ExpiresAt,
	}
	if err := s.State.store.UpsertConnector(ctx, conn); err != nil {
		s.Log.Warn("connector upsert failed", "provider", p.ID, "org", org, "user", user, "label", label, "err", err)
		return Connector{}, zip.Errorf(http.StatusInternalServerError, "persist failed")
	}
	saved, found, err := s.State.store.GetConnector(ctx, org, user, p.ID, label)
	if err != nil || !found {
		s.Log.Warn("connector readback failed", "provider", p.ID, "org", org, "user", user, "label", label, "err", err)
		return Connector{}, zip.Errorf(http.StatusInternalServerError, "persist failed")
	}
	s.Log.Info("connector connected", "provider", p.ID, "org", org, "user", user, "label", label, "account", res.AccountLabel)
	return saved, nil
}

// ── in-process seam (mirror agents `var mounted *cloud.Service`) ───────────────
//
// Token custody lives ONLY here; the bridge (af3999a) never touches KMS directly.
// Every func is nil-safe against an unmounted subsystem.

// TokenFor returns a custodied secret for a CONNECTED (org,provider). It fails
// closed: unmounted, invalid org, unknown provider, not-connected, or KMS-down
// each return an error and NEVER a value.
func TokenFor(ctx context.Context, org, provider, name string) ([]byte, error) {
	if mounted == nil {
		return nil, fmt.Errorf("integrations: not mounted")
	}
	return tokenFor(mounted, ctx, org, provider, name)
}

// Connected reports whether org has CONNECTED provider — a BOOLEAN presence check
// for the observe/growth plane. It reads ONLY the existence of the org's connection
// row (store.Get found), scoped to the org; it NEVER touches KMS and NEVER returns
// the token. Nil-safe and fail-closed: an unmounted subsystem, an invalid org, an
// unknown provider, or a store error all yield false — never a spurious true and
// never a secret.
func Connected(ctx context.Context, org, provider string) bool {
	s := mounted
	if s == nil || s.State.store == nil {
		return false
	}
	if !validOrg(org) {
		return false
	}
	if _, ok := s.State.providers[provider]; !ok {
		return false
	}
	_, found, err := s.State.store.Get(ctx, org, provider)
	return err == nil && found
}

func tokenFor(s *cloud.Service[state], ctx context.Context, org, provider, name string) ([]byte, error) {
	if !validOrg(org) {
		return nil, fmt.Errorf("integrations: invalid org")
	}
	if _, ok := s.State.providers[provider]; !ok {
		return nil, fmt.Errorf("integrations: unknown provider %q", provider)
	}
	_, found, err := s.State.store.Get(ctx, org, provider)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("integrations: %s not connected for org", provider)
	}
	if !kmsReady(s) {
		return nil, kms.ErrMasterKeyMissing
	}
	return kmsGet(s, kmsPath(org, provider), name)
}

// OrgForExternalID resolves a provider account id (Slack team_id / GitHub
// installation_id) back to the org that connected it. Used by inbound provider
// events (which carry the external id, not an org) to find the tenant.
func OrgForExternalID(provider, externalID string) (string, bool) {
	if mounted == nil {
		return "", false
	}
	org, ok, err := mounted.State.store.ResolveOrgByExternalID(context.Background(), provider, externalID)
	if err != nil || !ok {
		return "", false
	}
	return org, true
}

// ConnectionFor returns an org's non-secret connection metadata for a provider.
//
// NOTE (single contract deviation): the contract names this seam `Connection`,
// but Go forbids a func and a type sharing an identifier and `Connection` is the
// domain-noun TYPE (used by SyncHook/WritebackHook, the store, and this return
// value). The accessor is therefore `ConnectionFor` — the idiomatic Go name for
// "the Connection for (org,provider)". The bridge calls integrations.ConnectionFor.
func ConnectionFor(org, provider string) (Connection, bool) {
	if mounted == nil {
		return Connection{}, false
	}
	conn, ok, err := mounted.State.store.Get(context.Background(), org, provider)
	if err != nil || !ok {
		return Connection{}, false
	}
	return conn, true
}

// ── helpers ────────────────────────────────────────────────────────────────────

func providerParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("provider")) }

// orgProvider / userProvider keep the two custody planes disjoint: the org
// surface (/v1/integrations) never resolves a user-scoped provider and vice
// versa — the other plane's ids are simply 404s.
func orgProvider(s *cloud.Service[state], id string) (*Provider, bool) {
	p, ok := s.State.providers[id]
	if !ok || p.Scope == userScope {
		return nil, false
	}
	return p, true
}

func userProvider(s *cloud.Service[state], id string) (*Provider, bool) {
	p, ok := s.State.providers[id]
	if !ok || p.Scope != userScope {
		return nil, false
	}
	return p, true
}

// callbackPath is the ONE generic OAuth callback route for a provider — the path
// the dispatcher serves AND the value every provider's RedirectPath must equal.
func callbackPath(provider string) string { return "/v1/integrations/" + provider + "/callback" }

// consoleURL resolves the console origin the callback redirects back to.
func consoleURL() string {
	if v := strings.TrimSpace(os.Getenv(consoleURLEnv)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultConsoleURL
}

// validOrg accepts a DNS-1123-ish label. The org is folded into the KMS secret
// path and the store key, so it is validated strictly at every boundary that
// reaches custody. Identical rule to clients/kms's tenant boundary (kept local
// because that copy is unexported; both mirror the SAME platform org-slug shape).
func validOrg(org string) bool {
	if org == "" || len(org) > 63 {
		return false
	}
	for _, r := range org {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// validUser mirrors the kms.ValidSubpath per-segment rule: the user id is a
// platform identity fact keyed verbatim (clients/link parity) — gateway
// usernames, emails ("z@zoo.ngo"), and bearer UUIDs all pass. Bounded at
// principal.MaxOrgLen (the same IAM identity cap).
func validUser(u string) bool {
	if u == "" || len(u) > principal.MaxOrgLen || u == "." || u == ".." {
		return false
	}
	for _, r := range u {
		if r == '/' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// validLabel accepts a client-chosen connector label: [A-Za-z0-9._-], 1..64
// bytes, not "."/"..". The charset forbids ':' so provider+":"+label is an
// unambiguous connector id, and '/' so a label can never add path structure.
func validLabel(l string) bool {
	if l == "" || len(l) > 64 || l == "." || l == ".." {
		return false
	}
	for _, r := range l {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// genToken returns a 128-bit hex random token (nonce / id).
func genToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// sanitizeMeta hardens a provider-supplied, NON-secret metadata string (account
// label, external id, bot user id, scope) at the framework ingest boundary: it
// STRIPS ASCII C0 control characters and DEL — so a crafted value (a Slack
// workspace name carrying an embedded CR/LF or NUL) cannot inject a log line,
// smuggle a separator, or store an unprintable byte — and BOUNDS the length.
// Printable Unicode (emoji, non-ASCII display names) is preserved. A byte-boundary
// truncation is repaired with ToValidUTF8 so the stored value is always valid UTF-8.
func sanitizeMeta(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > maxMetaLen {
		s = strings.ToValidUTF8(s[:maxMetaLen], "")
	}
	return s
}

// sanitizeScopes applies sanitizeMeta to each granted scope, dropping any that
// sanitize to empty. One rule for every provider-supplied display string.
func sanitizeScopes(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x = sanitizeMeta(strings.TrimSpace(x)); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// sanitizeResult hardens every provider-supplied NON-secret metadata field
// (sanitizeMeta/sanitizeScopes) in place — both planes, one rule. Token VALUES
// are untouched: they go straight to the KMS seal.
func sanitizeResult(res *ExchangeResult) {
	res.ExternalID = sanitizeMeta(res.ExternalID)
	res.AccountLabel = sanitizeMeta(res.AccountLabel)
	res.BotUserID = sanitizeMeta(res.BotUserID)
	res.Scopes = sanitizeScopes(res.Scopes)
}

// ── keyed single-flight (refresh + device-poll serialization) ──────────────────

// hold is one flight entry: a mutex plus a waiter refcount so the map entry is
// removed when the last holder releases (bounded, no leak).
type hold struct {
	mu sync.Mutex
	n  int
}

// flight is a keyed in-process mutex: at most one holder per key. Keys:
// "refresh\x00"+userPath and "poll\x00"+grantID. Deliberately NOT
// x/sync/singleflight: waiters must re-read state under their own ctx after
// acquiring (adopt-after-lock), and force/non-force refresh callers cannot
// share one result. Process-local is correct because integrations state is
// per-pod SQLite (MaxOpenConns(1)); revisit if storage ever goes shared.
type flight struct {
	mu sync.Mutex
	m  map[string]*hold
}

// lock blocks until the caller holds key, returning the paired unlock.
func (f *flight) lock(key string) (unlock func()) {
	f.mu.Lock()
	h := f.m[key]
	if h == nil {
		h = &hold{}
		f.m[key] = h
	}
	h.n++
	f.mu.Unlock()
	h.mu.Lock()
	return func() {
		h.mu.Unlock()
		f.mu.Lock()
		h.n--
		if h.n == 0 {
			delete(f.m, key)
		}
		f.mu.Unlock()
	}
}
