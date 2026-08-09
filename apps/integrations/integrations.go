// Package integrations is how your org connects third-party accounts like Slack, and
// revokes them.
//
// It is the generic, provider-agnostic OAuth connector plane for the unified Hanzo
// Cloud binary — the /v1/integrations surface (Slack today; GitHub scaffolded;
// Google / Salesforce plug into the SAME registry later) — and it hands the
// resulting per-org tokens to KMS custody.
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
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
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
	// MultiAccount marks a provider that can be connected once per PROVIDER-SIDE
	// account. A GitHub App is installed per account, so one org holding hanzoai,
	// hanzo-apps and hanzo-docs holds three connections; the account name is then
	// part of the key and each row carries its own installation. A provider with
	// one account per org leaves this false and keeps an empty owner, which is what
	// its callers look up.
	MultiAccount bool

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
	// AuthorizeReady reports whether the Authorize leg has the credentials it
	// needs. OPTIONAL: when nil the leg is standard OAuth2 and readiness is
	// ClientID being set. A provider whose authorize leg is NOT OAuth sets this
	// so the gate asks the provider instead of assuming — a GitHub App install
	// URL is built from the app slug and has no client id at all, so without this
	// its connect flow is refused for a credential it never uses.
	AuthorizeReady func() bool
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
// redirect_uri). mounted is the in-process seam other subsystems (the channel) reach
// through the package funcs at the bottom of this file.
type state struct {
	store      *Store
	kms        cloud.KMSClient // the store, reached in-process or over the internal plane; nil ⇒ secret ops fail closed
	consoleURL string          // where the callback 302s the user back to
	stateKey   []byte          // HMAC-SHA256 key for the CSRF/org-binding state
	providers  map[string]*Provider
	flight     *flight // keyed in-process mutex: refresh + device-poll serialization
}

var mounted *cloud.Service[state]

// ── HTTP response shapes (the published contract) ──────────────────────────────

type providerView struct {
	// ID is the provider's registry id and the :provider path segment ("slack").
	ID string `json:"id"`
	// Name is the provider's display name ("Slack").
	Name string `json:"name"`
	// Description is the one-line pitch the console card shows.
	Description string `json:"description"`
	// Category groups the card ("Communication", "Developer", "Marketing").
	Category string `json:"category"`
	// Available is whether THIS DEPLOYMENT has the provider's app credentials, so
	// connect can succeed. False renders the card without a working Connect button.
	Available bool `json:"available"`
	// Connected is whether this org has a live connection to the provider.
	Connected bool `json:"connected"`
	// Connection is the connected account's non-secret detail. Absent when the org
	// has no connection; tokens NEVER appear here (they live only in KMS).
	Connection *connectionView `json:"connection,omitempty"`
}

type connectionView struct {
	// Account is the human label of the connected third-party account (the Slack
	// team name, the GitHub org login). Provider-supplied and sanitized on ingest.
	Account string `json:"account"`
	// ExternalID is the provider's own id for the account (Slack team.id, GitHub
	// installation_id) — the value inbound webhooks are mapped back to this org by.
	ExternalID string `json:"externalId"`
	// Scopes are the permissions the provider granted. Never null; [] when none.
	Scopes []string `json:"scopes"`
	// ConnectedAt is when the connection was last (re)established, RFC 3339 UTC.
	ConnectedAt string `json:"connectedAt"`
}

// listOut is the provider catalog: every ORG-plane provider, sorted by id, each
// annotated with this org's connection. User-plane providers (/v1/connectors) are
// not in it.
type listOut struct {
	// Providers is the whole catalog. Never null; [] when nothing is registered.
	Providers []providerView `json:"providers"`
}

// connectOut is the answer to /connect, and it has TWO disjoint shapes because the
// route has two credential paths. The OAuth path returns AuthorizeURL alone; the
// apikey path returns the sealed connection's summary and no URL. Every field that
// is exclusive to one path is omitempty (or a pointer, where the empty value is
// itself meaningful), so each path puts EXACTLY its own keys on the wire.
type connectOut struct {
	// AuthorizeURL is the provider consent URL to send the user to. OAuth path only.
	AuthorizeURL string `json:"authorizeUrl,omitempty"`
	// Connected is true on the apikey path once the credential verified and sealed.
	Connected bool `json:"connected,omitempty"`
	// Provider is the connector's registry id. apikey path only.
	Provider string `json:"provider,omitempty"`
	// Account is the account label the provider reported for the credential.
	// apikey path only; a pointer because "" is a real answer the provider gave.
	Account *string `json:"account,omitempty"`
	// ExternalID is the provider's account id for the credential. apikey path only.
	ExternalID *string `json:"externalId,omitempty"`
	// Scopes are the permissions the credential carries. apikey path only; never
	// null on that path ([] when the provider reported none).
	Scopes *[]string `json:"scopes,omitempty"`
}

// disconnectOut is the answer to /disconnect. Idempotent: disconnecting a provider
// that was never connected still answers true.
type disconnectOut struct {
	// Disconnected is always true — the org's secrets and connection row are gone.
	Disconnected bool `json:"disconnected"`
}

// verifyOut reports a live re-check of a stored apikey credential. A verification
// FAILURE is a 200 carrying active:false plus a reason, not an HTTP error, so the
// console can render it; the credential itself is never echoed.
type verifyOut struct {
	// Provider is the connector's registry id.
	Provider string `json:"provider"`
	// Active is whether the stored credential verified live against the provider.
	Active bool `json:"active"`
	// Reason is why the check failed. Present only when active is false.
	Reason string `json:"reason,omitempty"`
	// Account is the account label the provider reported. Present only when active.
	Account *string `json:"account,omitempty"`
	// ExternalID is the provider's account id. Present only when active.
	ExternalID *string `json:"externalId,omitempty"`
	// Scopes are the permissions the credential carries. Present only when active.
	Scopes *[]string `json:"scopes,omitempty"`
}

// authorizeOut is the one-field answer of a connect leg that only has a URL to
// give back: the provider consent page the caller must send the user to.
type authorizeOut struct {
	// AuthorizeURL is the provider consent (or bot deep-link) URL.
	AuthorizeURL string `json:"authorizeUrl"`
}

// ── Mount / lifecycle ──────────────────────────────────────────────────────────

// Mount wires /v1/integrations/* onto app. Complex flavour: it publishes the
// package global `mounted` (the in-process token-custody seam) and pairs with a
// Shutdown, so it constructs the cloud.Service value directly (cloud.NewBase +
// &cloud.Service[state]{…}) rather than via cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("integrations.Mount: nil app")
	}
	// The typed-op registry lives on the concrete app (ops.go). A Router that is
	// neither an App nor a scope cannot reach it, and serving routes no projection
	// knows is worse than not serving them — fail the mount instead.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("integrations.Mount: router does not expose the typed-op registry")
	}
	if deps.Logger == nil {
		return fmt.Errorf("integrations.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("integrations.Mount: empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("integrations.Mount: open store: %w", err)
	}

	// deps.KMS is the cloud.KMSClient, and it is USED as that interface — no
	// assertion to the embedded client.
	//
	// The assertion was the bug. Exactly one process owns the secret store, so
	// every other app is handed a peer that reaches it over the internal plane;
	// integrations is one of those, so `deps.KMS.(*kms.Client)` yielded nil for
	// months and every credential op failed closed while the error blamed a master
	// key that was correctly configured. Speaking the interface is what makes this
	// work in the process it actually runs in, and the ref grammar (kmsRef) is what
	// lets the same (path, name, env) travel over it.
	kc := deps.KMS

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

	// Say out loud whether any provider account is held by more than one org.
	// Upsert refuses to create that, so a hit is a row written before the refusal
	// existed, and this log is the only place it surfaces — the rows themselves
	// read as ordinary connections.
	if dup, derr := store.Collisions(context.Background()); derr != nil {
		b.Log.Warn("could not check for shared provider accounts", "err", derr)
	} else {
		for _, d := range dup {
			b.Log.Error("provider account held by several orgs",
				"provider", d.Provider, "externalId", d.ExternalID, "orgs", strings.Join(d.Orgs, " "))
		}
	}

	// Publish the Slack egress on the internal plane so a peer plugin can reach
	// this process's bot-token store over the socket (slack_rpc.go).
	exposeSlack()
	exposeConnection()

	routes(app, zapp, s)

	serveIdentity()
	serveSend()
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
//
// TWO REGISTRARS, ONE ROUTER. zip.<Verb>(zapp, …) registers a TYPED op — a route
// PLUS the registry entry OpenAPI / MCP / the CLI are projected from (ops.go) —
// and takes the ABSOLUTE path, since the registry keys on it. app.<Verb>(…) stays
// for exactly three families a typed op cannot express, and nothing else. Each
// names a WIRE fact, checked against zip v1.18.3, not a preference:
//
//   - 8 REDIRECT legs (the link entries + the generic OAuth callback), which answer
//     302 to a browser. zip.WithStatus PANICS on a non-2xx (typed.go:104), so an op
//     cannot declare a 302 at all, and a typed dispatch ends in c.JSON(out).
//   - 5 HTML pages: the three link callbacks and telegram/link's sign-in widget
//     answer text/html (channelLinkedHTML / telegramWidgetHTML). Same c.JSON(out)
//     terminus — an op's Out is a JSON body, so a page cannot be one. The link legs
//     also SET __Host- cookies, and a typed op holds no response to set them on.
//   - 6 inbound WEBHOOKS, for two distinct reasons:
//     RAW BYTES (slack/events, slack/commands, connector/github/webhook,
//     discord/interactions) — auth is a signature over the exact received bytes
//     (Slack/GitHub HMAC-SHA256, Discord Ed25519), and a typed op is handed the
//     DECODED In, never what was signed. slack/commands is additionally
//     form-encoded, which zip's invoke would 400 as invalid JSON before the
//     signature check ran; slack/events answers the url_verification challenge as
//     text/plain.
//     200-ON-UNPARSEABLE (teams/events, telegram/webhook) — these two are NOT
//     raw-byte-signed (Teams verifies a Bot Framework JWT header, Telegram a shared
//     secret header), but both answer an EMPTY 200 to a body they cannot parse so
//     the platform does not retry-storm. zip's invoke unmarshals BEFORE the handler
//     (typed.go:227), turning that 200 into a 400 — and for telegram it also inverts
//     the auth order, leaking a parse result to an unauthenticated caller that today
//     gets 401 first.
//
// The 202 Accepted creators (/repos/import, /pages/builds) were a THIRD family
// until zip v1.18.2: zip wrote 200, or 204 for a nil Out, and cloud.Created only
// covered 201, so answering 202 meant staying raw — and staying raw meant no
// schema, no prose, no MCP tool, no CLI command and no SDK method for either
// route. zip.WithStatus(202) states the same fact in the one place every
// projection reads, so both are typed ops now and the document says 202 because
// the op does.
//
// The partition is COMPLETE at 22 typed / 19 raw: every route with a JSON
// request/response shape is a typed op, and each of the 19 above is refused by one
// of the three wire facts, not by an unfinished pass. Re-check before converting.
//
// vendorCall is the sentence every inbound webhook shares: who is actually
// calling. A reader who assumes these are tenant-facing API calls will look for a
// bearer that is never there and miss the verification that replaces it.
const vendorCall = "\n\nThe caller here is the PLATFORM, not a Hanzo tenant, so there is no bearer and " +
	"no principal. The signature check IS the authentication, and it fails closed. The " +
	"tenant is never read from the payload either: it is resolved from the verified " +
	"platform identifier through the connection map, so an event from a workspace nobody " +
	"connected does nothing. Refusals are written with their own status rather than being " +
	"flattened to a 500, so a rejected signature reads as 401 and a malformed body as 400."

// asyncTurn is the delivery contract the four chat channels share: ack fast, work
// later, never twice. Each of the three properties is one a platform integrator
// has to know to reason about retries.
const asyncTurn = "\n\nThe answer is acknowledged immediately and the work happens afterwards, because " +
	"every one of these platforms times out a slow webhook. Duplicate deliveries are " +
	"absorbed durably, so a platform retry of an event that already ran never runs it a " +
	"second time or bills for it twice. When the agent pool is full nothing at all is " +
	"recorded and the delivery is refused as retriable, so the message is re-delivered " +
	"later rather than being lost or half-processed."

// linkFlow is the shape all four account-link flows share. Each leg's own
// description says which leg it is; this says why the flow is built the way it is,
// which is the part a reader would otherwise get wrong.
const linkFlow = "\n\nThis is one leg of a three-leg flow, and the legs are not interchangeable: a " +
	"browser is expected to arrive here only from the leg before it. The link URL's state " +
	"proves the prompt was server-minted and carries the CHAT it started from — it is " +
	"provenance only, and it never decides which account gets linked. The account identity " +
	"always comes from the platform's own verified sign-in and a host-bound cookie, so " +
	"forwarding a link to someone else cannot bind their account, and a session lifted into " +
	"another browser is refused rather than completed. Each link is single-use, and a " +
	"deployment without linking configured answers 503."

// The prose for the nineteen untyped operations on this surface. The typed ops
// beside them carry theirs in a doc comment zipdoc lifts; these cannot, because
// each is refused typing by its own wire — a vendor-signed raw body, an HTML page,
// a 302 back to the console — so the prose is declared beside the route table.
//
// Every sentence below is about what the operation DOES and what it refuses. None
// of them names a secret, and none implies a token appears in a response: the link
// flows seal what they obtain into the org's KMS namespace, and the callback seals
// before it writes anything at all.
func init() {
	// ── install entry point ──────────────────────────────────────────────────
	openapi.Describe("/v1/integrations/slack/install", http.MethodGet,
		"Install the Hanzo app into a Slack workspace",
		"The address behind Slack's \"Add to Slack\" and Marketplace Install buttons. It answers a "+
			"302 to Slack's own consent screen and does nothing else — it is a redirector by "+
			"design.\n\n"+
			"It exists because Slack refuses a slack.com URL in that field and requires one of ours "+
			"that redirects there, which makes the field an ATTRIBUTION hook: routing the click "+
			"through our own address is what lets an install be counted, and always answering the "+
			"redirect is what keeps the counter from becoming a detour that never reaches consent. "+
			"The destination is the same consent URL every time, built from the same scopes the "+
			"console's Connect button asks for, so a workspace is asked to grant one thing however "+
			"the install began.\n\n"+
			"It is PUBLIC and carries no principal, because whoever clicks Install in Slack's "+
			"directory has no Hanzo session yet. It binds no org either, and that is deliberate "+
			"rather than missing: the org is resolved at the shared provider callback, from the "+
			"signed state a console connect minted or from the workspace's existing connection. "+
			"Minting an org for an anonymous click is the one thing that would break tenant "+
			"isolation, so an install begun here finishes under exactly the rules every other "+
			"install obeys.\n\n"+
			"Where the app is not configured it answers 503, rather than a consent URL carrying "+
			"an empty client_id that Slack would render as its own dead-end error page.")

	// ── inbound platform webhooks ────────────────────────────────────────────
	openapi.Describe("/v1/integrations/slack/events", http.MethodPost,
		"Slack Events API webhook",
		"The address a Slack app posts workspace events to. It answers Slack's "+
			"url_verification handshake with the challenge, and routes an @mention or a "+
			"direct message to an agent turn that replies in the same thread. A prompt "+
			"beginning with `code:` is routed to the coding flow instead, which runs under "+
			"its own pool.\n\n"+
			"The raw body and its timestamp are verified against the app's signing secret "+
			"before anything is read from them. Hanzo's own bot messages are dropped, so a "+
			"reply cannot trigger another reply."+vendorCall+asyncTurn)
	openapi.Describe("/v1/integrations/slack/commands", http.MethodPost,
		"Slack slash command webhook",
		"The address Slack posts a slash command to, form-encoded. It acknowledges inside "+
			"Slack's three-second budget and posts the answer afterwards to the command's "+
			"own response URL, which is why the immediate reply is empty.\n\n"+
			"The body is verified against the same app signing secret as the events "+
			"webhook, and a repeat of the same command invocation is absorbed rather than "+
			"answered twice."+vendorCall+asyncTurn)
	openapi.Describe("/v1/integrations/discord/interactions", http.MethodPost,
		"Discord interactions endpoint",
		"The Interactions Endpoint URL for the Discord app. It answers Discord's PING with "+
			"a PONG, and handles the `/hanzo` slash command by acknowledging with a deferred "+
			"ephemeral reply and editing that reply with the answer once the agent has run. "+
			"Any other interaction is acknowledged and ignored.\n\n"+
			"Requests are verified by ED25519 SIGNATURE over the timestamp and body against "+
			"the app's public key — not by HMAC, unlike the Slack webhooks. Interactions "+
			"work over plain HTTP, so no gateway connection and no message-content intent is "+
			"involved.\n\n"+
			"Discord does not retry, so this is the one channel where being at capacity is "+
			"shown to the user as an ephemeral ask-to-run-it-again rather than answered as a "+
			"retriable failure — nothing is recorded either way, so the next attempt is "+
			"clean."+vendorCall)
	openapi.Describe("/v1/integrations/teams/events", http.MethodPost,
		"Microsoft Teams Bot Framework webhook",
		"The messaging endpoint for the Teams bot. A message activity is routed to an agent "+
			"turn and answered proactively through the Bot Connection; anything that is not a "+
			"message with text is acknowledged and ignored.\n\n"+
			"Authentication is the Bot Framework's RS256 JWT, verified against its published "+
			"keys and bound BOTH to this deployment's app id and to the activity's own "+
			"service URL. The service-URL binding is the part that matters: without it a "+
			"token valid for one activity could point the outbound reply somewhere else."+
			vendorCall+asyncTurn)
	openapi.Describe("/v1/integrations/telegram/webhook", http.MethodPost,
		"Telegram Bot API webhook",
		"The update webhook for the Telegram bot. It does two jobs: `/start <code>` or "+
			"`/connect <code>` binds the chat it was sent from to an org, idempotently; "+
			"anything else is treated as a possible agent trigger.\n\n"+
			"What counts as a trigger differs by chat type, and it is easy to get wrong: in "+
			"a private chat every message is a trigger, while in a group the message must "+
			"mention the bot or use the `/hanzo` command. Non-triggers and non-message "+
			"updates are acknowledged and dropped.\n\n"+
			"Authentication is the secret token Telegram echoes on every update, compared in "+
			"constant time. A message in a chat that has never been bound is dropped, which "+
			"is why the bind command exists."+vendorCall+asyncTurn)
	openapi.Describe("/v1/connector/github/webhook", http.MethodPost,
		"GitHub App webhook",
		"The address the GitHub App delivers events to. A push is handed to the repository "+
			"sync engine, and an issue or issue-comment event is mirrored into the native "+
			"tracker — idempotently, so the same issue re-syncs to one row however many "+
			"times it is edited, closed or reopened.\n\n"+
			"It answers a benign 200 for everything it does not act on — the ping, other "+
			"event types, an unknown installation — deliberately, so GitHub does not enter a "+
			"retry storm over events that were never going to do anything. Only a bad "+
			"signature and a genuine sync failure are non-200, and an oversized payload is "+
			"refused outright.\n\n"+
			"Two sync rules are worth stating because neither is guessable. EVERY ref syncs, "+
			"tags as well as branches, because releases are cut by tag and filtering them "+
			"would stop publishing with nothing reporting a failure. And a delete is NEVER "+
			"propagated: the native side is canonical, so an inbound delete never removes a "+
			"native ref.\n\n"+
			"The payload is verified by HMAC against the webhook secret before it is parsed."+
			vendorCall)

	// ── account-link flows (three legs each) ─────────────────────────────────
	openapi.Describe("/v1/integrations/slack/link", http.MethodGet,
		"Begin linking a Hanzo account from Slack",
		"The entry point behind the connect prompt Hanzo posts in Slack. It starts a link "+
			"session in the browser and redirects to Slack's own sign-in, which is what "+
			"proves which Slack user is asking."+linkFlow)
	openapi.Describe("/v1/integrations/slack/link/slack", http.MethodGet,
		"Slack sign-in return leg",
		"Where Slack returns the user after they sign in. It establishes the verified Slack "+
			"workspace and user, confirms that workspace is connected to an org, and hands "+
			"the browser on to the Hanzo sign-in that completes the link.\n\n"+
			"The verified pair is carried onward in a host-bound cookie rather than in the "+
			"URL, so the identity being linked cannot be edited in transit."+linkFlow)
	openapi.Describe("/v1/integrations/slack/link/callback", http.MethodGet,
		"Complete the Slack account link",
		"The final leg: the user has proved both who they are in Slack and who they are in "+
			"Hanzo, and this binds the two. It answers a short confirmation page telling "+
			"them to return to Slack.\n\n"+
			"The Hanzo credential obtained here is sealed into the connected workspace's own "+
			"KMS namespace; it is never written to a database column and never logged. A "+
			"deployment whose secret store is unavailable refuses the link rather than "+
			"completing it without custody of the credential."+linkFlow)
	openapi.Describe("/v1/integrations/discord/link", http.MethodGet,
		"Begin linking a Hanzo account from Discord",
		"The entry point behind the connect prompt Hanzo shows in a Discord server. It "+
			"starts a link session and redirects to Discord's OAuth `identify` consent — the "+
			"narrowest scope that establishes which Discord user is asking, and nothing "+
			"more."+linkFlow)
	openapi.Describe("/v1/integrations/discord/link/discord", http.MethodGet,
		"Discord sign-in return leg",
		"Where Discord returns the user after the identify consent. It resolves the verified "+
			"Discord user, confirms the server is connected to an org, and hands the browser "+
			"to the Hanzo sign-in that completes the link."+linkFlow)
	openapi.Describe("/v1/integrations/discord/link/callback", http.MethodGet,
		"Complete the Discord account link",
		"The final leg: it binds the verified Discord user to the Hanzo account that just "+
			"signed in, and answers a short confirmation page telling them to return to "+
			"Discord. The Hanzo credential is sealed into the connected org's KMS namespace "+
			"rather than stored beside the link."+linkFlow)
	openapi.Describe("/v1/integrations/teams/link", http.MethodGet,
		"Begin linking a Hanzo account from Teams",
		"The entry point behind the connect prompt Hanzo shows in Teams. It starts a link "+
			"session and redirects to Microsoft sign-in addressed to the CHAT'S OWN tenant, "+
			"not the common endpoint, so only a member of that tenant can complete it."+
			linkFlow)
	openapi.Describe("/v1/integrations/teams/link/aad", http.MethodGet,
		"Microsoft sign-in return leg",
		"Where Microsoft returns the user after sign-in. It resolves the verified directory "+
			"identity and then re-checks the tenant: the signed-in user's tenant must equal "+
			"the tenant of the chat the link started from, so a valid Microsoft sign-in from "+
			"a different organization is refused here rather than accepted.\n\n"+
			"This is the leg Teams has and the other platforms do not, which is why the "+
			"Teams flow has an extra address."+linkFlow)
	openapi.Describe("/v1/integrations/teams/link/callback", http.MethodGet,
		"Complete the Teams account link",
		"The final leg: it binds the verified directory identity to the Hanzo account that "+
			"just signed in, and answers a short confirmation page telling them to return to "+
			"Teams. The Hanzo credential is sealed into the connected org's KMS namespace."+
			linkFlow)
	openapi.Describe("/v1/integrations/telegram/link", http.MethodGet,
		"Begin linking a Hanzo account from Telegram",
		"The entry point behind the connect prompt Hanzo sends in Telegram. Unlike the other "+
			"platforms it answers an HTML PAGE rather than a redirect: Telegram has no OAuth "+
			"flow, so the page hosts Telegram's Login Widget, and the browser is sent onward "+
			"only after the user signs in through it.\n\n"+
			"The widget only appears on the domain registered for the bot, so a deployment "+
			"whose bot domain is unset renders a page with nothing on it."+linkFlow)
	openapi.Describe("/v1/integrations/telegram/link/auth", http.MethodGet,
		"Telegram Login Widget return leg",
		"Where Telegram's Login Widget sends the user with its signed authentication data. "+
			"That data is verified against the bot token — this is the identity source, and "+
			"it is the widget's signature rather than a code exchange — and the chat is "+
			"confirmed to be bound to an org before the browser is handed to the Hanzo "+
			"sign-in.\n\n"+
			"Widget data is only accepted while it is fresh, so a captured sign-in blob "+
			"cannot be replayed later even though its signature stays valid."+linkFlow)
	openapi.Describe("/v1/integrations/telegram/link/callback", http.MethodGet,
		"Complete the Telegram account link",
		"The final leg: it binds the verified Telegram user to the Hanzo account that just "+
			"signed in, and answers a short confirmation page telling them to return to "+
			"Telegram. The Hanzo credential is sealed into the connected org's KMS "+
			"namespace."+linkFlow)

	// ── the generic OAuth return ─────────────────────────────────────────────
	openapi.Describe("/v1/integrations/:provider/callback", http.MethodGet,
		"OAuth return for any connector",
		"The single address every connector's OAuth flow returns to. It exchanges the "+
			"authorization the provider granted, records the connection, and ALWAYS "+
			"redirects the browser back to the console — on success and on every labeled "+
			"failure alike, so a user never lands on a raw JSON dead end.\n\n"+
			"It is public and carries no principal, so the org is taken ONLY from the signed "+
			"state minted when the flow began; no header is trusted here. That state is "+
			"single-use and is burned BEFORE the exchange, so one authorization is one "+
			"attempt and a replayed return fails instead of exchanging twice.\n\n"+
			"Tokens are sealed into the org's KMS namespace BEFORE the connection row is "+
			"written, so a failure of the secret store leaves no half-connected integration "+
			"advertising a credential that was never stored. Token values never appear in "+
			"the redirect, in a log line or in an error.\n\n"+
			"One generalization is worth knowing: a GitHub App installation returns an "+
			"installation identifier instead of an OAuth code, and it is accepted in the "+
			"code's place so the App model needs no second address.")
}

// The two are interleaved in the ORIGINAL order because fiber resolves by
// registration order, and that order is load-bearing here (literals before the
// /:provider wildcards).
func routes(app cloud.Router, zapp *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}
	// channelFacts first: every typed op below reads the caller's identity facts off
	// the request context, and a Use only runs ahead of routes registered after it.
	//
	// The ORG is NOT parked here, and this subsystem does not install cloud.Bridge.
	// Whoever composes the program installs it once at the root — after the identity
	// check that mints the validated org and before any subsystem registers a route
	// (serve.go) — because that order is a property of the whole program and no
	// subsystem can assert it for itself. A subsystem holding its own copy only ever
	// covered for a composer that had none, and the cover is what broke this app:
	// the copy needs a node to hang on, that node has no routes beneath it because
	// these routes are registered on the root, and zip refuses to compose a program
	// whose middleware could never run. The app then exits rather than listening.
	//
	// channelFacts stays because it is integrations' own. scope.Use installs it at
	// the root and runs it only for the paths manifest/apps.go says this subsystem
	// answers, so dropping the group costs no confinement.
	app.Use(zip.H(channelFacts))

	zip.Get(zapp, "/v1/integrations", o.list)
	// Slack adapter (slack_events.go / slack_link.go), every route raw on purpose.
	// The two webhooks speak Slack's own protocol: the HMAC covers the RAW bytes,
	// which an op handed the decoded In could not re-verify. The three link legs
	// drive a browser — a 302 to the Slack / hanzo.id sign-in, then a short HTML
	// confirmation page — and never answer JSON.
	// The Marketplace / "Add to Slack" entry point. A literal, so it is matched
	// before the /:provider wildcards below, and Terminal like its siblings: it
	// carries no principal by design and must not be gated into a 403, because the
	// person clicking Install in Slack's directory has no Hanzo session yet.
	app.Get("/v1/integrations/slack/install", cloud.Terminal(cloud.Handle(s, slackInstall)))
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
	// It stays raw because it speaks GitHub's webhook protocol: the HMAC covers
	// the raw body, which an op handed the decoded In could not re-verify.
	app.Post("/v1/connector/github/webhook", cloud.Terminal(cloud.Handle(s, githubWebhook)))
	zip.Get(zapp, "/v1/integrations/github/installations", o.githubInstallations)
	// Bind installations the App already holds to the org the caller acts in.
	zip.Post(zapp, "/v1/integrations/github/claim", o.githubClaim)
	zip.Get(zapp, "/v1/integrations/github/repos", o.githubRepos)
	// FIND ONE, AND TAKE A COPY. Search reads GitHub's public index — how you
	// find a repository to fork, never a way to see inside one. Fork resolves
	// against the installation's GRANTED set like every other write here.
	zip.Post(zapp, "/v1/integrations/github/search", o.githubSearch)
	zip.Post(zapp, "/v1/integrations/github/fork", o.githubFork)
	// 202: the import runs in a bounded background worker, so the op DECLARES the
	// status it has always answered rather than setting it per request.
	zip.Post(zapp, "/v1/integrations/github/repos/import", o.githubImport, zip.WithStatus(http.StatusAccepted))
	// Seed the native tracker with the org's EXISTING GitHub issues (the webhook
	// keeps them live thereafter). Org-authed via the principal; bounded + idempotent.
	zip.Post(zapp, "/v1/integrations/github/issues/backfill", o.githubIssuesBackfill)
	// GitHub Pages management (github_pages.go), one repo as a resource. Registered
	// AFTER the literal /repos/import so registration-order matching keeps the literal
	// unshadowed; the :repo routes all carry a /pages suffix, so /repos/import (no
	// suffix) never matches them. Org-authed via the principal; the repo is resolved
	// against the installation's granted set (owner is server-derived).
	zip.Get(zapp, "/v1/integrations/github/repos/:repo/pages", o.githubPagesGet)
	zip.Post(zapp, "/v1/integrations/github/repos/:repo/pages", o.githubPagesEnable)
	zip.Put(zapp, "/v1/integrations/github/repos/:repo/pages", o.githubPagesUpdate)
	zip.Delete(zapp, "/v1/integrations/github/repos/:repo/pages", o.githubPagesDisable)
	// 202: the build is queued at GitHub, not completed here.
	zip.Post(zapp, "/v1/integrations/github/repos/:repo/pages/builds", o.githubPagesBuild, zip.WithStatus(http.StatusAccepted))
	// ChatBridge adapters (channel.go + discord/teams/telegram). Same discipline as
	// the slack channel: the literal paths register BEFORE the /:provider wildcards so
	// they win under registration-order matching. All PUBLIC at the JWT layer — auth
	// is done INSIDE each handler: Discord Ed25519 interaction verify, Teams Bot
	// Framework JWT, Telegram secret-token; the link legs use signed __Host- cookies +
	// state. Telegram's /connect is org-authed via the principal (like the framework
	// connect). They must NOT sit behind any principal/tenant gate.
	// Every route here is raw on purpose: each event door speaks its platform's
	// own webhook protocol over the raw request (Discord's Ed25519 signs the raw
	// bytes; Teams and Telegram answer in their platform's envelope), and every
	// link leg drives a browser with a 302 or an HTML page, never JSON.
	app.Post("/v1/integrations/discord/interactions", cloud.Terminal(cloud.Handle(s, discordInteractions)))
	app.Get("/v1/integrations/discord/link", cloud.Handle(s, discordLink))
	app.Get("/v1/integrations/discord/link/discord", cloud.Handle(s, discordLinkDiscord))
	app.Get("/v1/integrations/discord/link/callback", cloud.Handle(s, discordLinkCallback))
	app.Post("/v1/integrations/teams/events", cloud.Terminal(cloud.Handle(s, teamsEvents)))
	app.Get("/v1/integrations/teams/link", cloud.Handle(s, teamsLink))
	app.Get("/v1/integrations/teams/link/aad", cloud.Handle(s, teamsLinkAAD))
	app.Get("/v1/integrations/teams/link/callback", cloud.Handle(s, teamsLinkCallback))
	zip.Post(zapp, "/v1/integrations/telegram/connect", o.telegramConnect)
	app.Post("/v1/integrations/telegram/webhook", cloud.Terminal(cloud.Handle(s, telegramWebhook)))
	app.Get("/v1/integrations/telegram/link", cloud.Handle(s, telegramLink))
	app.Get("/v1/integrations/telegram/link/auth", cloud.Handle(s, telegramLinkAuth))
	app.Get("/v1/integrations/telegram/link/callback", cloud.Handle(s, telegramLinkCallback))
	zip.Get(zapp, "/v1/integrations/:provider", o.get)
	zip.Post(zapp, "/v1/integrations/:provider/connect", o.connect)
	// PUBLIC, state-authed, RAW (302). RedirectPath == this path for every provider
	// (asserted in Mount), so this single generic route serves every provider's
	// OAuth callback.
	app.Get("/v1/integrations/:provider/callback", cloud.Handle(s, callback))
	zip.Post(zapp, "/v1/integrations/:provider/disconnect", o.disconnect)
	// apikey connectors: re-verify a stored credential live (`hanzo connector verify`).
	zip.Post(zapp, "/v1/integrations/:provider/verify", o.verifyConn)
	// Per-USER connector plane (/v1/connectors — connectors.go). Own prefix, so no
	// shadowing interplay with the /:provider wildcards above.
	connectorRoutes(app, zapp, o)
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

// list returns every registered integration provider together with THIS org's
// connection status for it — the catalog the console's Integrations page renders.
// Org-authed: a caller with no validated principal is 403, because the status is
// per-org and there is no org-less answer. User-plane providers (the /v1/connectors
// surface) are omitted; the two planes are disjoint.
//
// Response: {"providers":[{"id":"slack","name":"Slack","description":"Connect your workspace.","category":"Communication","available":true,"connected":true,"connection":{"account":"Acme","externalId":"T0231","scopes":["chat:write"],"connectedAt":"2026-07-01T10:00:00Z"}}]}
func (o ops) list(ctx context.Context, _ *noArgs) (*listOut, error) {
	org, err := authed(ctx, principalRequired)
	if err != nil {
		return nil, err
	}
	ids := sortedProviderIDs(o.s)
	out := make([]providerView, 0, len(ids))
	for _, id := range ids {
		p := o.s.State.providers[id]
		if p.Scope == userScope {
			continue // user-plane providers are invisible on the org surface
		}
		v, verr := providerViewFor(o.s, ctx, org, p)
		if verr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", verr)
		}
		out = append(out, v)
	}
	return &listOut{Providers: out}, nil
}

// get returns ONE provider with this org's connection status — the same view list
// carries, for a single id. An unknown id is 404, and so is a user-plane provider:
// the org surface never resolves one.
//
// Example: {"provider":"slack"}
// Response: {"id":"slack","name":"Slack","description":"Connect your workspace.","category":"Communication","available":true,"connected":false}
func (o ops) get(ctx context.Context, in *providerRef) (*providerView, error) {
	org, err := authed(ctx, principalRequired)
	if err != nil {
		return nil, err
	}
	p, ok := orgProvider(o.s, in.id())
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	v, err := providerViewFor(o.s, ctx, org, p)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	return &v, nil
}

// connect acquires the org's credential for one provider. It has TWO paths and the
// REQUEST picks which: a "token" key in the body seals that credential directly
// (verify-before-store), and its absence begins the 3-legged OAuth flow — minting a
// single-use nonce plus an HMAC-signed state that binds this org to this provider,
// and answering with the provider's authorize URL for the caller to redirect to.
//
// Fail-closed order, unchanged: no principal → 403; unknown provider → 404; an
// AdminOnly connector without the caller's own-org admin bit → 403; not configured
// → 503; KMS not ready → 503 (the flow WILL need to seal a token, so refuse now
// rather than dead-end at the callback).
//
// Example: {"provider":"cloudflare","token":"cf-scoped-api-token","accountId":"a1b2c3"}
// Response: {"connected":true,"provider":"cloudflare","account":"Acme","externalId":"a1b2c3","scopes":[]}
func (o ops) connect(ctx context.Context, in *connectIn) (*connectOut, error) {
	org, err := authed(ctx, "a validated principal is required to connect an integration")
	if err != nil {
		return nil, err
	}
	s := o.s
	p, ok := orgProvider(s, in.ref().id())
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	// Mutating a connector is an org-admin action for providers that declare it
	// (parity with the platform deploy-provider adminProcedure). The predicate is
	// the caller's OWN-org isAdmin bit (principal.IsOrgAdmin) — NOT SuperAdmin.
	if p.AdminOnly && !orgAdmin(ctx) {
		return nil, zip.ErrForbidden("connecting the " + p.ID + " connector requires org admin")
	}
	if !p.Configured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s integration is not configured on this deployment", p.ID)
	}
	// The credential store is required only by a provider that CUSTODIES something.
	// A provider declaring no secrets seals nothing (sealTokens over an empty map
	// is a no-op) and reads nothing back, so gating it on the store refused a
	// connection that never needed one — GitHub declares `Secrets: nil` because
	// installation tokens are minted on demand, and its connect was blocked for
	// months by a store it does not touch.
	if len(p.Secrets) > 0 && !kmsReady(s) {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", errCredentialStore)
	}
	// Pick the credential-acquisition path by REQUEST. A provider may offer an
	// apikey path (Verify) and/or an OAuth path (Authorize). A credential in the
	// /connect body seals via apikey (verify-before-store; NOTHING persisted on a
	// bad credential); its absence starts the OAuth flow below. A provider with only
	// one path always takes it — an apikey-only provider with no token still returns
	// connectByCredential's helpful "token required" 400.
	if p.Verify != nil && (p.Authorize == nil || in.Token != nil) {
		return connectByCredential(s, ctx, org, p, in)
	}
	if p.Authorize == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s: no connect method configured", p.ID)
	}
	// The authorize leg needs Hanzo's registered app creds. A dual-path provider
	// keeps Configured()==true for its always-available apikey path, so gate on the
	// authorize leg's OWN creds here — an honest 503 that points the caller at the
	// token path rather than a dead consent URL with an empty client_id.
	if !authorizeReady(p) {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s is not configured for the install flow on this deployment; connect with a scoped API token instead", p.ID)
	}

	// Opportunistic GC of expired nonces (best-effort; never fails the request).
	if _, gerr := s.State.store.GCNonces(ctx, staleNonceCutoff()); gerr != nil {
		s.Log.Warn("nonce gc", "err", gerr)
	}

	nonce, err := genToken()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	if err := s.State.store.PutNonce(ctx, nonce, org, p.ID); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "nonce: %v", err)
	}
	signed, err := sign(s, org, p.ID, nonce)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "state: %v", err)
	}
	authorizeURL, err := p.Authorize(p.Creds(), redirectURI(s, p), signed)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "authorize: %v", err)
	}
	return &connectOut{AuthorizeURL: authorizeURL}, nil
}

// connectIn is the /connect request: which connector, and optionally the customer
// credential that selects the apikey path over OAuth.
type connectIn struct {
	// Provider is the connector's registry id, from the :provider path segment.
	Provider string `json:"provider"`
	// Token is the customer's provider credential. Its PRESENCE — not its value —
	// is what selects the apikey seal over the OAuth flow for a provider that
	// offers both: {"token":"…"}, even empty, is an apikey attempt (→ verify, which
	// answers the "token required" 400 on an empty value), while a body with no
	// token key (the console Connect button, `hanzo connector add` with no --token)
	// starts OAuth. Read on STDIN by the CLI, never argv; never logged or echoed.
	Token *string `json:"token"`
	// AccountID is the provider account the credential should be scoped to, for the
	// providers whose Verify needs one (Cloudflare). Ignored by the OAuth path.
	AccountID string `json:"accountId"`
}

// ref is the connector this request addresses, normalized like every other
// :provider op.
func (in connectIn) ref() providerRef { return providerRef{Provider: in.Provider} }

// maxCredentialLen bounds the credential an apikey /connect accepts. Real provider
// API tokens are short (a Cloudflare token is ~40 chars); anything over 8 KiB is
// hostile and rejected before it reaches Verify or KMS.
const maxCredentialLen = 8192

// connectByCredential completes an apikey connector. The caller submits the
// provider credential in the request body (from `hanzo connector add`, read on
// STDIN — never argv/URL); the provider VERIFIES it live; and ONLY on success is
// the token sealed into the org's KMS namespace with non-secret metadata written
// to the connection row. FAIL-CLOSED: a bad/inactive credential is refused and
// NOTHING is stored (no KMS write, no row). The credential value never appears in
// a log line, the response, or the store — only in the KMS seal input.
func connectByCredential(s *cloud.Service[state], ctx context.Context, org string, p *Provider, in *connectIn) (*connectOut, error) {
	if p.Verify == nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%s: apikey provider without Verify", p.ID)
	}
	var token string
	if in.Token != nil {
		token = strings.TrimSpace(*in.Token)
	}
	if token == "" {
		return nil, zip.ErrBadRequest("a credential token is required (pipe it on stdin: `hanzo connector add --provider " + p.ID + " --token -`)")
	}
	if len(token) > maxCredentialLen {
		return nil, zip.ErrBadRequest("credential too large")
	}
	res, err := p.Verify(ctx, VerifyInput{Token: token, AccountID: sanitizeMeta(strings.TrimSpace(in.AccountID))})
	if err != nil || res == nil {
		// Verify FAILED → refuse; store NOTHING. err is provider-authored and must
		// not carry the credential value (only its status/reason).
		s.Log.Warn("connector verify failed", "provider", p.ID, "org", org, "err", err)
		return nil, zip.ErrBadRequest("credential verification failed")
	}
	// Harden provider-supplied NON-secret metadata (strip control chars, bound
	// length) — never the secret token, which goes straight to the KMS seal.
	sanitizeResult(res)
	// Seal every verified secret into the org's KMS namespace BEFORE the row, so a
	// KMS failure leaves NO half-connected row advertising a token that was never
	// stored (same ordering discipline as the OAuth callback).
	if err := sealTokens(s, kmsPath(org, p.ID), res.Tokens); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "secret custody failed")
	}
	conn := Connection{
		Org:      org,
		Provider: p.ID,
		// The provider-side account this connection is FOR, and part of the key when
		// the provider can hold several. Without it a second account collides with
		// the first and silently replaces it — the failure the key exists to
		// prevent. AccountLabel is the same value, which is why the migration could
		// recover it for rows written before the key was widened.
		Label:        connOwner(p, res),
		ExternalID:   res.ExternalID,
		AccountLabel: res.AccountLabel,
		BotUserID:    res.BotUserID,
		Scopes:       res.Scopes,
	}
	if err := s.State.store.Upsert(ctx, conn); err != nil {
		s.Log.Warn("connection upsert failed", "provider", p.ID, "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "persist failed")
	}
	s.Log.Info("connector connected", "provider", p.ID, "org", org, "account", res.AccountLabel, "externalId", res.ExternalID)
	scopes := nonNil(res.Scopes)
	return &connectOut{
		Connected:  true,
		Provider:   p.ID,
		Account:    &res.AccountLabel,
		ExternalID: &res.ExternalID,
		Scopes:     &scopes,
	}, nil
}

// verifyConn re-checks a CONNECTED apikey connector's stored credential against the
// provider, live (`hanzo connector verify`). Org-scoped (any member may check
// status); the credential is read from KMS, verified, and NEVER returned or logged.
// A verification failure is reported as {active:false}, not an error — the console/
// CLI renders it. Only apikey providers support verify (OAuth tokens are checked at
// use, not re-verified here).
//
// Example: {"provider":"cloudflare"}
// Response: {"provider":"cloudflare","active":true,"account":"Acme","externalId":"a1b2c3","scopes":["zone:read"]}
func (o ops) verifyConn(ctx context.Context, in *providerRef) (*verifyOut, error) {
	org, err := authed(ctx, principalRequired)
	if err != nil {
		return nil, err
	}
	s := o.s
	p, ok := orgProvider(s, in.id())
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	if p.Kind != apiKeyKind || p.Verify == nil || len(p.Secrets) == 0 {
		return nil, zip.ErrBadRequest("verify is only supported for credential connectors")
	}
	conns, err := s.State.store.ListFor(ctx, org, p.ID)
	found := len(conns) > 0
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("connector not connected")
	}
	if !kmsReady(s) {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", errCredentialStore)
	}
	tok, err := kmsGet(s, kmsPath(org, p.ID), p.Secrets[0])
	if err != nil || len(tok) == 0 {
		return nil, zip.ErrBadRequest("stored credential unavailable")
	}
	res, verr := p.Verify(ctx, VerifyInput{Token: string(tok)})
	if verr != nil || res == nil {
		return &verifyOut{Provider: p.ID, Reason: "verification failed"}, nil
	}
	account, externalID := sanitizeMeta(res.AccountLabel), sanitizeMeta(res.ExternalID)
	scopes := nonNil(sanitizeScopes(res.Scopes))
	return &verifyOut{
		Provider:   p.ID,
		Active:     true,
		Account:    &account,
		ExternalID: &externalID,
		Scopes:     &scopes,
	}, nil
}

// callback is the PUBLIC, state-authed OAuth return. It ALWAYS 302s the user back
// to the console (success or a labeled failure) — never a raw JSON dead-end. The
// org comes ONLY from the signed, single-use state; no header is trusted here.
func callback(s *cloud.Service[state], c *zip.Ctx) error {
	pid := providerParam(c)
	p, ok := orgProvider(s, pid)
	if !ok {
		return failRedirect(s, c, pid, "unknown provider", nil)
	}

	payload, err := verify(s, c.Query("state"), p.ID)
	if err != nil {
		return failRedirect(s, c, p.ID, "invalid state", err)
	}
	// Consume the single-use nonce, bound to (org,provider). Burned BEFORE the
	// exchange so one state = one attempt: a replay (or a slow-flow retry) finds
	// zero rows and fails here, never double-exchanging.
	consumed, err := s.State.store.ConsumeNonce(c.Context(), payload.Nonce, payload.Org, p.ID)
	if err != nil {
		return failRedirect(s, c, p.ID, "state error", err)
	}
	if !consumed {
		return failRedirect(s, c, p.ID, "state already used or expired",
			fmt.Errorf("org %s: nonce not present (replayed, or GC'd after %s)", payload.Org, stateTTL))
	}
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		// The provider's own refusal text, bounded: it is remote input on a public
		// route, so it is logged at a fixed width and never reflected to the browser.
		return failRedirect(s, c, p.ID, "authorization denied",
			fmt.Errorf("org %s: provider reported %q", payload.Org, truncate(e, 200)))
	}
	// A provider with no Exchange does not complete its binding here, and the
	// callback must say so rather than invent one. GitHub is the case: an App
	// install hands back an installation_id, and the state cannot cover it — the
	// install does not exist when the state is minted — so the id arrives as
	// nothing but a number the caller typed. The App JWT reads every tenant's
	// installation, so an id that resolves proves only that the App is installed
	// somewhere, never that it is installed for THIS org. Trading it for a
	// connection let any org name any other tenant's install and mint live tokens
	// against it. The consent is real, but the org it belongs to is not in the
	// callback; githubClaim binds it, under platform sudo, for that reason.
	if p.Exchange == nil {
		return failRedirect(s, c, p.ID, "this install must be bound by an operator",
			fmt.Errorf("org %s: %s returns no grant this org can prove (installation %q)",
				payload.Org, p.ID, truncate(strings.TrimSpace(c.Query("installation_id")), 32)))
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		return failRedirect(s, c, p.ID, "missing authorization code",
			fmt.Errorf("org %s: no code present", payload.Org))
	}
	if len(code) > maxCodeLen {
		return failRedirect(s, c, p.ID, "authorization code too large",
			fmt.Errorf("org %s: %d bytes exceeds %d", payload.Org, len(code), maxCodeLen))
	}
	if !p.Configured() {
		return failRedirect(s, c, p.ID, "provider not configured", nil)
	}
	if !kmsReady(s) {
		return failRedirect(s, c, p.ID, "secret store unavailable", nil)
	}

	res, err := p.Exchange(c.Context(), p.Creds(), redirectURI(s, p), code)
	if err != nil || res == nil {
		return failRedirect(s, c, p.ID, "token exchange failed", fmt.Errorf("org %s: %w", payload.Org, err))
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
		return failRedirect(s, c, p.ID, "secret custody failed", fmt.Errorf("org %s: %w", payload.Org, err))
	}
	conn := Connection{
		Org:      payload.Org,
		Provider: p.ID,
		Label:    connOwner(p, res), // part of the key — see the connect path

		ExternalID:   res.ExternalID,
		AccountLabel: res.AccountLabel,
		BotUserID:    res.BotUserID,
		Scopes:       res.Scopes,
	}
	if err := s.State.store.Upsert(c.Context(), conn); err != nil {
		return failRedirect(s, c, p.ID, "persist failed", fmt.Errorf("org %s: %w", payload.Org, err))
	}
	s.Log.Info("integration connected", "provider", p.ID, "org", payload.Org, "account", res.AccountLabel, "externalId", res.ExternalID)
	return successRedirect(s, c, p.ID, res.AccountLabel)
}

// disconnect revokes (best-effort) and forgets an org's connection: it deletes
// every custodied KMS secret and the connection row. Idempotent — disconnecting a
// provider that was never connected still returns {disconnected:true}. Symmetric
// with connect: an AdminOnly connector needs the caller's own-org admin bit.
//
// Example: {"provider":"slack"}
// Response: {"disconnected":true}
func (o ops) disconnect(ctx context.Context, in *providerRef) (*disconnectOut, error) {
	org, err := authed(ctx, "a validated principal is required to disconnect an integration")
	if err != nil {
		return nil, err
	}
	s := o.s
	p, ok := orgProvider(s, in.id())
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	// Symmetric with connect: disconnecting an admin-only connector requires the
	// caller be an admin of its OWN org (principal.IsOrgAdmin — NOT SuperAdmin).
	if p.AdminOnly && !orgAdmin(ctx) {
		return nil, zip.ErrForbidden("disconnecting the " + p.ID + " connector requires org admin")
	}

	conns, err := s.State.store.ListFor(ctx, org, p.ID)
	found := len(conns) > 0
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}

	// Best-effort provider-side revoke using the primary custodied secret. Never
	// fails the disconnect: local forgetting is authoritative for the tenant.
	if found && p.Revoke != nil && len(p.Secrets) > 0 && kmsReady(s) && p.Configured() {
		if tok, gerr := kmsGet(s, kmsPath(org, p.ID), p.Secrets[0]); gerr == nil {
			if rerr := p.Revoke(ctx, p.Creds(), string(tok)); rerr != nil {
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
	if _, derr := s.State.store.Disconnect(ctx, org, "", p.ID); derr != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", derr)
	}
	return &disconnectOut{Disconnected: true}, nil
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
	// A provider is connected when ANY of its accounts is. The first is the
	// representative the card renders; ListFor is ordered by owner so it is stable.
	conns, err := s.State.store.ListFor(ctx, org, p.ID)
	if err != nil {
		return providerView{}, err
	}
	found := len(conns) > 0
	var conn Connection
	if found {
		conn = conns[0]
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

// failRedirect 302s to {console}/integrations?error={provider}&reason=<short msg>,
// and is the ONE place a failed OAuth return is recorded. reason is the opaque
// public label the browser sees; cause (nil when reason is the whole truth) is the
// precise internal detail, which goes ONLY to the log. Logging here rather than at
// each call site is what makes "every failure is recorded" a property of the funnel
// instead of a habit — a silent callback is indistinguishable from one that never
// arrived, and that ambiguity is expensive to debug.
func failRedirect(s *cloud.Service[state], c *zip.Ctx, provider, reason string, cause error) error {
	s.Log.Warn("oauth callback rejected", "provider", provider, "reason", reason, "cause", cause)
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

// errCredentialStore is what a caller is told when credentials cannot be sealed.
// It names the SYMPTOM, not a cause: this check is false when the client is the
// wrong type, when it was never injected, AND when the master key is unset, and
// the handler cannot tell which. It used to report ErrMasterKeyMissing, which
// sent every investigation to an environment variable that was correctly set.
const errCredentialStore = "the credential store is unavailable on this deployment"

// kmsReady reports whether a credential store is reachable at all. It cannot ask
// the REMOTE store whether its master key is configured without a round trip on
// every check, and it does not need to: a store that is wired but unhealthy
// surfaces that on the operation, which is where the caller can act on it. What
// this gate is for is the case with no store at all.
func kmsReady(s *cloud.Service[state]) bool { return s.State.kms != nil }

// kmsPath is the per-org, per-provider KMS namespace: /orgs/{org}/integrations/{provider}.
// org is validOrg-checked at every entry point, so it can never smuggle path
// structure; provider is a fixed registry slug.
// connOwner is the account a connection is keyed by. Only a MultiAccount provider
// gets one; everything else keeps the empty owner its callers resolve.
func connOwner(p *Provider, res *ExchangeResult) string {
	if p == nil || !p.MultiAccount || res == nil {
		return ""
	}
	return res.AccountLabel
}

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
// kmsRef addresses a secret the way the KMSClient interface does — "path/name@env",
// the grammar parseRef reads back into the same (path, name, env) the embedded
// client holds. Composing the ref is what lets these helpers speak the INTERFACE
// rather than the concrete client, which is the whole reason they work here: this
// process is not the store's owner, so deps.KMS is the peer over the internal
// plane, and a type assertion to the embedded client can never succeed.
func kmsRef(path, name string) string { return path + "/" + name + "@" + kmsEnv }

func kmsPut(s *cloud.Service[state], path, name string, value []byte) error {
	return s.State.kms.PutSecret(context.Background(), kmsRef(path, name), value)
}

// kmsGet reads one sealed secret, and RESTORES kms.ErrSecretNotFound when the
// store answered "not found" across a boundary that flattened the sentinel.
//
// A plugin is a PROCESS. An error crossing that wire is re-created from its
// STRING, so errors.Is(err, kms.ErrSecretNotFound) is false on the far side even
// though the store said exactly that. Every caller that distinguishes "absent"
// from "broken" therefore silently took the broken branch.
//
// Measured in production: @hanzo answered every Slack DM with "Sorry — I
// couldn't reach your Hanzo account just now" because getUserLink read an
// unlinked user's secret, got `kms kms_get: kms.get: store: secret not found`
// as an opaque error, and returned it as a FAILURE instead of the "not linked
// yet" it is. The link prompt that teaches a user how to connect was
// unreachable, so the feature could never be used at all.
//
// Repaired HERE rather than at each call site: this is the one door onto the
// store, so the sentinel is whole for everyone above it and no future caller has
// to know the wire eats error identity.
func kmsGet(s *cloud.Service[state], path, name string) ([]byte, error) {
	raw, err := s.State.kms.GetSecret(context.Background(), kmsRef(path, name))
	if err != nil && !errors.Is(err, kms.ErrSecretNotFound) && isNotFoundText(err) {
		return nil, fmt.Errorf("%s: %w", err.Error(), kms.ErrSecretNotFound)
	}
	return raw, err
}

// isNotFoundText recognises a store's not-found answer that arrived as prose.
//
// Matching on text is what the flattened wire leaves available, so it is kept
// deliberately narrow — the store's own phrase — rather than any message
// containing "not found", which would swallow a genuine failure that merely
// mentions a missing thing and turn a broken store into a silent "unlinked".
func isNotFoundText(err error) bool {
	return strings.Contains(err.Error(), "secret not found")
}

func kmsDelete(s *cloud.Service[state], path, name string) error {
	err := s.State.kms.DeleteSecret(context.Background(), kmsRef(path, name))
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
func saveUser(ctx context.Context, s *cloud.Service[state], org, user, label string, p *Provider, res *ExchangeResult) (Connection, error) {
	sanitizeResult(res)
	path, err := userPath(org, user, p.ID, label)
	if err != nil {
		return Connection{}, err
	}
	if err := sealTokens(s, path, res.Tokens); err != nil {
		return Connection{}, zip.Errorf(http.StatusServiceUnavailable, "secret custody failed")
	}
	conn := Connection{
		Org:          org,
		User:         user,
		Provider:     p.ID,
		Label:        label,
		ExternalID:   res.ExternalID,
		AccountLabel: res.AccountLabel,
		Scopes:       res.Scopes,
		ExpiresAt:    res.ExpiresAt,
	}
	if err := s.State.store.Upsert(ctx, conn); err != nil {
		s.Log.Warn("connector upsert failed", "provider", p.ID, "org", org, "user", user, "label", label, "err", err)
		return Connection{}, zip.Errorf(http.StatusInternalServerError, "persist failed")
	}
	saved, found, err := s.State.store.Get(ctx, org, user, p.ID, label)
	if err != nil || !found {
		s.Log.Warn("connector readback failed", "provider", p.ID, "org", org, "user", user, "label", label, "err", err)
		return Connection{}, zip.Errorf(http.StatusInternalServerError, "persist failed")
	}
	s.Log.Info("connector connected", "provider", p.ID, "org", org, "user", user, "label", label, "account", res.AccountLabel)
	return saved, nil
}

// ── in-process seam (mirror agents `var mounted *cloud.Service`) ───────────────
//
// Token custody lives ONLY here; the channel (af3999a) never touches KMS directly.
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
	conns, err := s.State.store.ListFor(ctx, org, provider)
	return err == nil && len(conns) > 0
}

func tokenFor(s *cloud.Service[state], ctx context.Context, org, provider, name string) ([]byte, error) {
	if !validOrg(org) {
		return nil, fmt.Errorf("integrations: invalid org")
	}
	if _, ok := s.State.providers[provider]; !ok {
		return nil, fmt.Errorf("integrations: unknown provider %q", provider)
	}
	conns, err := s.State.store.ListFor(ctx, org, provider)
	if err != nil {
		return nil, err
	}
	if len(conns) == 0 {
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
// authorizeReady reports whether a provider's authorize leg can actually build a
// URL. Standard OAuth2 needs a client id; a provider that says otherwise via
// AuthorizeReady is asked instead, so an install flow is never refused for a
// credential its protocol does not have.
func authorizeReady(p *Provider) bool {
	if p.AuthorizeReady != nil {
		return p.AuthorizeReady()
	}
	return p.Creds().ClientID != ""
}

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
// "the Connection for (org,provider)". The channel calls integrations.ConnectionFor.
func ConnectionFor(org, provider, owner string) (Connection, bool) {
	if mounted == nil {
		return Connection{}, false
	}
	conn, ok, err := mounted.State.store.Get(context.Background(), org, "", provider, owner)
	if err != nil || !ok {
		return Connection{}, false
	}
	return conn, true
}

// Connections returns every account an org has connected for a provider, one per
// owner. A caller that must reach ALL of an org's accounts iterates this rather
// than naming one; a caller that already knows which account it means uses
// ConnectionFor.
func Connections(org, provider string) []Connection {
	if mounted == nil {
		return nil
	}
	conns, err := mounted.State.store.ListFor(context.Background(), org, provider)
	if err != nil {
		return nil
	}
	return conns
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
