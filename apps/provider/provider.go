package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/hanzoai/cloud/sdk/text"

	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/piece"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

const (
	// consoleURLEnv overrides the console origin the callback 302-redirects the
	// user back to; defaultConsoleURL is the fallback when no brand console is
	// registered. Must point to the customer console (platform.hanzo.ai), never
	// the operator console (console.hanzo.ai / admin-console) whose admin org
	// rejects social federation ("federation is not permitted for this application").
	consoleURLEnv     = "CLOUD_CONSOLE_URL"
	defaultConsoleURL = "https://platform.hanzo.ai"

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
	Tokens     map[string]string // secret name -> value (sealed into KMS)
	ExternalID string            // provider account id (Slack team.id / GitHub installation_id)
	// Key is the connection's key when the account's own id is not a valid one: a
	// DID has colons, a Mastodon address an at-sign. Empty means the id is the key.
	Key          string
	AccountLabel string // human label (Slack team.name / GitHub org login)
	// Kind is the provider's own word for the account (GitHub: User or
	// Organization). It is shown to the person choosing, and never decides access.
	Kind      string
	BotUserID string // Slack bot_user_id (non-secret)
	// Installer is the provider-side user who COMPLETED the install — Slack's
	// authed_user.id. Non-secret, and load-bearing: whoever finished the OAuth
	// was already an admin of this org, so they need no second proof to talk to
	// the bot they just installed.
	Installer string
	Scopes    []string // granted scopes
	ExpiresAt int64    // access-token expiry, unix seconds; 0 = non-expiring/unknown. Kept on the connection, and an org connection whose provider has Refresh is refreshed against it (tokenFor).
}

// verifyRefused is a credential a network said no to, in words a person can act
// on. Its text is fixed by the connector and never carries the credential, so
// it is the one verify failure the connect reports instead of hiding.
type verifyRefused string

func (e verifyRefused) Error() string { return string(e) }

// apiKeyKind marks a Provider whose credential is supplied by the customer and
// verified live, rather than acquired via the 3-legged OAuth flow (the default
// for an empty Kind). See Provider.Kind.
const apiKeyKind = "apikey"

// mcpKind marks a Provider the caller reaches through the provider's OWN MCP
// endpoint rather than through our client for its REST API. The credential is
// still custodied here; what differs is who implements the tools — they do.
const mcpKind = "mcp"

const (
	// userScope marks a Provider on the per-user /v1/connection plane. User-scoped
	// providers are invisible on /v1/provider; their rows are keyed
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
	// Fields are the named inputs of a connector whose credential is more than
	// one value — a custom auth's fields, a username and password. Secret, like
	// Token: never logged, never echoed, sealed or discarded.
	Fields map[string]string
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
// repo list). It is a #51 client: DECLARED on Provider, nil for every provider
// today, and NOT wired to any route. When GitHub creds land, github.go sets this
// to the installation-token-minting + repo-sync implementation.
type SyncHook func(ctx context.Context, conn Connection) error

// WritebackHook pushes Hanzo state TO the provider. It is a #51 client: declared,
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
	RedirectPath string   // OAuth redirect path; MUST equal /v1/provider/{id}/callback
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
	// PerTarget seals a separate token for each account, keyed by the platform
	// id, instead of one token for the whole network. Publishing names the
	// account; it does not take whichever row sorts first.
	PerTarget bool
	// Expand turns one OAuth consent into the accounts to keep. Nil means the
	// login itself is the one account.
	Expand func(context.Context, OAuthConfig, string, identity) ([]socialGrant, error)

	AdminOnly bool
	// Verify validates an apikey credential against the provider and returns the
	// token(s) to seal + non-secret account metadata. It MUST fail closed (a
	// bad/inactive credential returns an error and NOTHING is stored) and its error
	// MUST NOT contain the credential value (it is logged). nil for oauth providers.
	// On the user plane it doubles as the token/apikey intake method.
	Verify func(ctx context.Context, in VerifyInput) (*ExchangeResult, error)

	// Scope selects the custody plane: "" = org-scoped /v1/provider (default);
	// userScope = per-user /v1/connection (rows keyed (org,user,provider,label),
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
	// Install completes an APP installation instead: the callback carries the
	// installing person's authorization code beside the installation the provider
	// just created, and Install returns that installation as the account to bind
	// only when the person's own authorization reaches it. The installation id is
	// a number in a URL and proves nothing by itself. nil for OAuth providers.
	Install func(ctx context.Context, creds OAuthConfig, code, installation string) (*ExchangeResult, error)
	// Installed answers the installations the person whose authorization code this is
	// already controls, for a callback that carries a code and no installation:
	// the provider's authorization page, where an APP provider's connect starts
	// (see finishInstalled). Which of them another org holds is bind's question. nil for
	// providers whose connect is not an APP install.
	Installed func(ctx context.Context, creds OAuthConfig, code string) ([]*ExchangeResult, error)
	// InstallPage is where finishInstalled sends the browser when it offers nothing,
	// and where the chooser's "install on another account" goes: the provider's
	// install page, carrying a fresh state. Set with Installed.
	InstallPage func(creds OAuthConfig, state string) (string, error)
	// Manage is the provider's own page for an account the App is on, where the
	// person changes what it reaches (GitHub: which repositories). The chooser
	// links to it beside each account it offers. nil hides the link.
	Manage func(o offered) string
	// Revoke best-effort invalidates a token at the provider on disconnect. nil
	// when the provider has no revoke endpoint.
	Revoke func(ctx context.Context, creds OAuthConfig, token string) error
	// Uninstall best-effort removes an APP installation at the provider once the
	// last org holding it has disconnected, so connecting that account again is a
	// fresh install the callback can bind. disconnect calls it only when no other
	// org still holds conn's account (Store.Holder): another org's integration is
	// never removed by this one's disconnect. nil for providers with nothing installed.
	Uninstall func(ctx context.Context, conn Connection) error

	// #51 clients — declared, nil today, not wired to a route (see SyncHook/WritebackHook).
	Sync      SyncHook
	Writeback WritebackHook

	// Actions and Triggers are the provider's CAPABILITY half — what it can be
	// asked to do, beside the credential half above. Populated only by
	// RegisterActions (action.go), never in a register() literal, so there is one
	// place to look for what a provider offers. Empty for a provider that is
	// reachable for consent and offers nothing to invoke.
	Actions  map[string]*Action
	Triggers map[string]*Trigger

	// Fields is the form a credential connector (Verify) asks for. Empty means
	// the one field every apikey connector takes: the key itself.
	Fields []Field
	// Piece is the catalog entry this connector is, or shares an id with: its
	// logo, and the actions and triggers the piece declares. nil for a
	// connector the catalog does not carry.
	Piece *piece.Piece
	// generated is true when the piece IS the connector (pieceProvider built it),
	// false for a native connector that only shares the piece's id — whose
	// sign-in, and the app it reads, are its own.
	generated bool
}

// Field is one input of a credential connector's form. The console renders
// the form from these, so a connector added to the catalog brings its own.
type Field struct {
	// Name is the key the value is submitted under.
	Name string `json:"name"`
	// Label is what the input is called on the form.
	Label string `json:"label"`
	// Secret masks the input; its value is sealed and never read back.
	Secret bool `json:"secret"`
	// Required is whether the form refuses to submit without it.
	Required bool `json:"required"`
	// Help is the connector's own instruction for finding the value.
	Help string `json:"help,omitempty"`
	// Type is "number", "boolean" or "text" (multi-line) when the value is
	// not a single line of text.
	Type string `json:"type,omitempty"`
	// Options are the values a choice field accepts.
	Options []FieldOption `json:"options,omitempty"`
	// Default is what an empty input stands for.
	Default string `json:"default,omitempty"`
}

// FieldOption is one choice of a Field.
type FieldOption struct {
	// Label is what the choice is called.
	Label string `json:"label"`
	// Value is what submitting it sends.
	Value string `json:"value"`
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
// redirect_uri). mounted is the in-process client other subsystems (the channel) reach
// through the package funcs at the bottom of this file.
type state struct {
	store      *Store
	kms        secret.Store // the store, reached in-process or over the internal plane; nil ⇒ secret ops fail closed
	consoleURL string       // where the callback 302s the user back to
	stateKey   []byte       // HMAC-SHA256 key for the CSRF/org-binding state
	providers  map[string]*Provider
	flight     *flight  // keyed in-process mutex: refresh + device-poll serialization
	forgeKey   forgeKey // the forge's webhook secret, held for a window (forge_webhook.go)
	// audit is the tamper-evident trail a minted GitHub run token is recorded in
	// (github_run.go); nil in a process that holds none, which refuses the mint.
	audit *audit.Recorder
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
	// Scopes are the permissions a connection will ask for. Never null.
	Scopes []string `json:"scopes"`
	// Available is whether THIS DEPLOYMENT has the provider's app credentials, so
	// connect can succeed. False renders the card without a working Connect button.
	Available bool `json:"available"`
	// Connected is whether this org has a live connection to the provider.
	Connected bool `json:"connected"`
	// Connection is the connected account's non-secret detail. Absent when the org
	// has no connection; tokens NEVER appear here (they live only in KMS).
	// When the org holds several accounts, this is the first and Accounts is
	// every one of them.
	Connection *connectionView `json:"connection,omitempty"`
	// Accounts is every account this org has connected for the provider, each
	// with its own platform id. Tokens never appear here.
	Accounts []connectionView `json:"accounts,omitempty"`
	// Modes are the ways this provider can be connected, derived from what it
	// actually declares — "managed", "oauth", "apikey", "device", "mcp". One
	// console page renders every card from this field rather than carrying a
	// table of its own, which is how a provider added here shows up there
	// without a second edit.
	Modes []string `json:"modes"`
	// Kind is how a reader connects it NOW: "oauth" (send the browser to the
	// provider), "credential" (fill in Fields), "device" (enter a code), or
	// "none" (nothing to connect — ready, or not connectable here; Note says).
	Kind string `json:"kind"`
	// Fields is the form a credential connector asks for. Absent otherwise.
	Fields []Field `json:"fields,omitempty"`
	// Logo is where the connector's logo is served. Absent when it has none.
	Logo string `json:"logo,omitempty"`
	// Actions are what the connector can be asked to do, by a flow step or an
	// agent (POST /v1/provider/{provider}/run when Runnable).
	Actions []opView `json:"actions,omitempty"`
	// Triggers are the events a flow can start on.
	Triggers []opView `json:"triggers,omitempty"`
	// Runnable is whether its actions run on this deployment.
	Runnable bool `json:"runnable"`
	// Checks is whether a pasted credential is checked with the provider before
	// it is stored. False means it is sealed on its shape and checked on use.
	Checks bool `json:"checks"`
	// App names the OAuth app registration its sign-in uses ("GOOGLE"), for a
	// connector whose sign-in awaits one.
	App string `json:"app,omitempty"`
	// Note says why the connector cannot be connected here, when it cannot.
	Note string `json:"note,omitempty"`
}

// opView names one action or trigger on a card.
type opView struct {
	// Name is the action's id — what a run or a flow step names.
	Name string `json:"name"`
	// DisplayName is what the card calls it.
	DisplayName string `json:"displayName"`
	// Description says what it does. Only on GET /v1/provider/{provider}.
	Description string `json:"description,omitempty"`
	// Props are its inputs — what POST /v1/provider/{provider}/run takes as
	// input. Only on GET /v1/provider/{provider}; the list names actions only.
	Props []PropSpec `json:"props,omitempty"`
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
	// ID is provider + ":" + label, the address every connection route takes.
	// Empty on the org plane, where a provider's connection is addressed by the
	// provider alone.
	ID string `json:"id,omitempty"`
	// Provider is the connected provider's registry id. Empty where the reader
	// already knows it, which is every org-plane card.
	Provider string `json:"provider,omitempty"`
	// Label is the caller's own name for this connection ("default", "work").
	Label string `json:"label,omitempty"`
	// ExpiresAt is when the access token expires, RFC 3339 UTC; empty for a
	// credential that does not expire. Reading the token rotates it inside the
	// window, so a reader never sees an expired one.
	ExpiresAt string `json:"expiresAt,omitempty"`
	// Kind is personal or page for a social account that has one. Empty for
	// every other connector.
	Kind string `json:"kind,omitempty"`
}

// listOut is the provider catalog: every ORG-plane provider, sorted by id, each
// annotated with this org's connection. User-plane providers (/v1/connection) are
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
	// Accounts are the account labels this disconnect removed. Omitted when none
	// was connected.
	Accounts []string `json:"accounts,omitempty"`
	// Uninstalled are the accounts whose provider installation was removed too,
	// because no other org held them. An account another org still holds keeps its
	// installation and is absent here.
	Uninstalled []string `json:"uninstalled,omitempty"`
}

// disconnectIn is the /disconnect request: which connector, and optionally which
// one of its accounts.
type disconnectIn struct {
	// Provider is the connector's registry id, from the :provider path segment.
	Provider string `json:"provider"`
	// Account names ONE account to disconnect — the label the connection carries
	// (a GitHub login, for the App) or its platform id (a Page, a channel, a
	// board). Empty disconnects every account the org holds for the provider. An
	// account the org does not hold disconnects nothing.
	Account string `json:"account" url:"-"`
}

// ref is the connector this request addresses, normalized like every other
// :provider op.
func (in disconnectIn) ref() providerRef { return providerRef{Provider: in.Provider} }

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

// Use wires the org plane (/v1/provider) and the user plane (/v1/connection)
// onto app. Complex flavour: it publishes the package global `mounted` (the
// in-process token-custody client) and pairs with a Shutdown, so it constructs
// the cloud.Service value directly (cloud.NewBase + &cloud.Service[state]{…})
// rather than via cloud.Use.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("provider: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("provider.Use:  empty DataDir")
	}
	store, err := openStore(cloud.DataDir(), deps.MasterKey, deps.KMS)
	if err != nil {
		return fmt.Errorf("provider.Use:  open store: %w", err)
	}

	// deps.KMS is the cloud.Store, and it is USED as that interface — no
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
	// OAuth/config surface (nothing on /v1/connection calls them) and at least one
	// user intake method; an org provider must declare its config surface, and its
	// RedirectPath must be the path the generic callback dispatcher actually
	// serves — otherwise its OAuth callback would 404 silently. One route, one truth.
	for id, p := range providers {
		if p.Scope == userScope {
			if p.Authorize != nil || p.Exchange != nil || p.Revoke != nil ||
				p.RedirectPath != "" || p.Configured != nil || p.Creds != nil {
				_ = store.Close()
				return fmt.Errorf("provider.Use:  user-scoped provider %q must not declare org OAuth/config fields", id)
			}
			if p.Device == nil && p.Adopt == nil && p.Verify == nil {
				_ = store.Close()
				return fmt.Errorf("provider.Use:  user-scoped provider %q needs at least one of Device/Adopt/Verify", id)
			}
			continue
		}
		// AN ACTION-ONLY PROVIDER SPENDS NO CREDENTIAL, and is the third shape.
		// `core` and `waitlist` reach the registry through RegisterActions alone
		// (action.go), so they have no consent flow to declare and nothing to
		// custody — demanding Configured and Creds of them asks for the ENV app
		// creds of an app that does not exist.
		//
		// The test is that it declares NO credential surface AT ALL and does
		// declare something to do. A provider that half-declares one — Authorize
		// without Creds — is not action-only and still fails below, which is the
		// case this check exists for.
		if (len(p.Actions) > 0 || len(p.Triggers) > 0) &&
			p.Kind == "" && p.RedirectPath == "" && len(p.Secrets) == 0 &&
			p.Configured == nil && p.Creds == nil && p.Authorize == nil &&
			p.Exchange == nil && p.Revoke == nil && p.Verify == nil &&
			p.Device == nil && p.Adopt == nil && p.Refresh == nil {
			continue
		}
		if p.Configured == nil || p.Creds == nil {
			_ = store.Close()
			return fmt.Errorf("provider.Use:  org provider %q must declare Configured and Creds", id)
		}
		if p.Kind == apiKeyKind || p.Kind == noneKind {
			// apikey providers have no OAuth callback, and a provider with
			// nothing to connect has no flow at all; RedirectPath is unused.
			continue
		}
		if want := callbackPath(id); p.RedirectPath != want {
			_ = store.Close()
			return fmt.Errorf("provider.Use:  provider %q RedirectPath %q must equal %q", id, p.RedirectPath, want)
		}
	}

	b := cloud.NewBase(deps, "integrations")
	s := &cloud.Service[state]{
		Base: b,
		State: state{
			store:      store,
			kms:        kc,
			consoleURL: consoleURL(),
			stateKey:   resolveStateKey(env.Or(stateKeyEnv, ""), b.Log),
			providers:  providers,
			flight:     &flight{m: map[string]*hold{}},
			audit:      deps.Audit,
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
	exposePost()
	exposeConnection()
	exposeGrant()
	exposeAccount()
	exposeFigures()
	exposeRunners()
	exposeRun()
	exposeRepo()
	exposeRef()
	exposeIssue()
	exposeChange()
	exposeAct()
	exposeGoogleSend()

	routes(app, s)

	serveIdentity()
	serveSend()
	serveHistory()
	b.Log.Info(
		"integrations mounted",
		"providers", len(s.State.providers),
		"kmsReady", kmsReady(s),
		"domain", s.Domain,
		"console", s.State.consoleURL,
		"brand", cloud.Brand(),
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
// teams events, telegram webhook, github webhook) is
// cloud.Terminal-wrapped so its bad-signature 401 / malformed-body 400 is written
// in-band and survives the commerce /v1 ErrorHandlerJSON (co-mounted ahead of us),
// which would otherwise flatten a propagated 4xx to 500. Uniform reject codes.
//
// TWO REGISTRARS, ONE GROUP. g.<Verb>(…) registers a TYPED op: a route plus the
// registry entry OpenAPI / MCP / the CLI are projected from (ops.go). g.Raw(…)
// stays for exactly three families a typed op cannot express, and nothing else.
// Both take a leaf relative to the group, which composes it under the group's
// prefix. Each raw family names a WIRE fact, checked against zip v1.18.3, not a
// preference:
//
//   - 8 REDIRECT legs (the link entries + the generic OAuth callback), which answer
//     302 to a browser. zip.WithStatus PANICS on a non-2xx (typed.go:104), so an op
//     cannot declare a 302 at all, and a typed dispatch ends in c.JSON(out).
//   - 5 HTML pages: the three link callbacks and telegram/link's sign-in widget
//     answer text/html (channelLinkedHTML / telegramWidgetHTML). Same c.JSON(out)
//     terminus — an op's Out is a JSON body, so a page cannot be one. The link legs
//     also SET __Host- cookies, and a typed op holds no response to set them on.
//   - 7 inbound WEBHOOKS, for three distinct reasons:
//     RAW BYTES (slack/events, slack/commands, github/webhook,
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
//     A SUBSET OF A WIRE (openrouter/webhook) — the body is OpenTelemetry's OTLP/JSON
//     and openrouter.go reads the handful of fields a usage row needs. An op's In IS
//     the published request schema, so typing it would publish that handful as the
//     whole of a wire OpenTelemetry defines.
//
// The 202 Accepted creators (/repos/import, /pages/builds) were a THIRD family
// until zip v1.18.2: zip wrote 200, or 204 for a nil Out, and cloud.Created only
// covered 201, so answering 202 meant staying raw — and staying raw meant no
// schema, no prose, no MCP tool, no CLI command and no SDK method for either
// route. zip.WithStatus(202) states the same fact in the one place every
// projection reads, so both are typed ops now and the document says 202 because
// the op does.
//
// The partition is COMPLETE at 26 typed / 21 raw: every route with a JSON
// request/response shape is a typed op, and each of the 21 above is refused by one
// of those wire facts, not by an unfinished pass. Re-check before converting.
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

// The prose for the twenty-one untyped operations on this surface. The typed ops
// beside them carry theirs in a doc comment zipdoc lifts; these cannot, because
// each is refused typing by its own wire — a vendor-signed raw body, a body this
// package decodes only a subset of, an HTML page, a 302 back to the console — so
// the prose is declared beside the route table.
//
// Every sentence below is about what the operation DOES and what it refuses. None
// of them names a secret, and none implies a token appears in a response: the link
// flows seal what they obtain into the org's KMS namespace, and the callback seals
// before it writes anything at all.
func init() {
	// ── install entry point ──────────────────────────────────────────────────
	// UNTYPED BY DESIGN: the response IS the file, so there is no JSON body to
	// declare and the Content-Type comes from the bytes. A route the wire keeps
	// untyped states itself here instead of through a lifted doc comment.
	spec.Describe("/v1/provider/slack/file", http.MethodGet,
		"Read one Slack file's bytes.",
		"Streams the bytes of a file attached to a message in the workspace this "+
			"organization connected. `id` is the Slack file id a message's `files` "+
			"carries; the response is the file itself, under the Content-Type Slack "+
			"recorded for it, never JSON.\n\n"+

			"THE WORKSPACE IS THE CALLER'S. The org comes from the validated identity "+
			"and never from the request, and it selects the token the read spends, so a "+
			"caller cannot name a workspace they have not connected. A file that "+
			"workspace cannot see answers 404 from Slack's own answer rather than from a "+
			"guess made here, and a file past the size bound is refused by name instead "+
			"of arriving truncated.")
	spec.Describe("/v1/provider/slack/install", http.MethodGet,
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
	spec.Describe("/v1/provider/slack/events", http.MethodPost,
		"Slack Events API webhook",
		"The address a Slack app posts workspace events to. It answers Slack's "+
			"url_verification handshake with the challenge, and routes an @mention or a "+
			"direct message to an agent turn that replies in the same thread. The turn holds "+
			"the product's own tools, so a request to change code starts a sandbox run "+
			"because the model chose to — there is no prefix and no second path.\n\n"+
			"The raw body and its timestamp are verified against the app's signing secret "+
			"before anything is read from them. Hanzo's own bot messages are dropped, so a "+
			"reply cannot trigger another reply."+vendorCall+asyncTurn)
	spec.Describe("/v1/provider/slack/commands", http.MethodPost,
		"Slack slash command webhook",
		"The address Slack posts a slash command to, form-encoded. It acknowledges inside "+
			"Slack's three-second budget and posts the answer afterwards to the command's "+
			"own response URL, which is why the immediate reply is empty.\n\n"+
			"The body is verified against the same app signing secret as the events "+
			"webhook, and a repeat of the same command invocation is absorbed rather than "+
			"answered twice."+vendorCall+asyncTurn)
	spec.Describe("/v1/provider/discord/interactions", http.MethodPost,
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
	spec.Describe("/v1/provider/teams/events", http.MethodPost,
		"Microsoft Teams Bot Framework webhook",
		"The messaging endpoint for the Teams bot. A message activity is routed to an agent "+
			"turn and answered proactively through the Bot Connection; anything that is not a "+
			"message with text is acknowledged and ignored.\n\n"+
			"Authentication is the Bot Framework's RS256 JWT, verified against its published "+
			"keys and bound BOTH to this deployment's app id and to the activity's own "+
			"service URL. The service-URL binding is the part that matters: without it a "+
			"token valid for one activity could point the outbound reply somewhere else."+
			vendorCall+asyncTurn)
	spec.Describe("/v1/provider/whatsapp/webhook", http.MethodGet,
		"WhatsApp Cloud API subscription challenge",
		"Meta calls this once when the webhook is subscribed, carrying the verify token "+
			"this deployment was configured with and a challenge to echo. The token is "+
			"compared in constant time before the echo — answering the challenge without "+
			"checking it would let anyone point their own app at this address and have it "+
			"confirm the subscription.")
	spec.Describe("/v1/provider/whatsapp/webhook", http.MethodPost,
		"WhatsApp Cloud API webhook",
		"One delivery from Meta. Authenticity is the X-Hub-Signature-256 HMAC over the "+
			"raw body, and it is the whole of it: a message accepted here creates the reply "+
			"route that authorises this org to answer, so an unsigned delivery would let "+
			"anyone hand an org a conversation to answer under its own number.\n\n"+
			"Meta batches (entry × changes × messages) and sends status callbacks — "+
			"sent/delivered/read — through this same address with no message at all. Those "+
			"are acknowledged and dropped rather than refused, because a non-2xx is retried "+
			"with backoff and eventually disables the subscription: the only refusals here "+
			"are an unconfigured endpoint and a bad signature, which are ours to fix and not "+
			"Meta's to retry."+asyncTurn)
	spec.Describe("/v1/provider/telegram/webhook", http.MethodPost,
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
	spec.Describe("/v1/provider/linear/webhook", http.MethodPost,
		"Linear webhook",
		"The address Linear delivers Issue and Comment events to. An issue event is "+
			"mirrored into the native todo — idempotently by identifier, so ENG-123 is one "+
			"row however many times it is edited, moved or closed — and every issue and "+
			"comment event is handed to the automations engine as a verified trigger, "+
			"which is how an org runs an agent when an issue is assigned to it or a comment "+
			"mentions it. A remove is never propagated: the native side is canonical.\n\n"+
			"It answers a benign 200 for what it does not act on — an unknown organization, "+
			"other event types — so Linear does not retry-storm. A bad signature and a "+
			"delivery older than a minute are 401; only a sink failure is 502.\n\n"+
			"The delivery names its Linear organization; that organization's own webhook "+
			"secret — sealed at /v1/provider/linear/claim — verifies the HMAC over the raw body, "+
			"so the tenant is the organization the signature proves, never a header."+
			vendorCall)
	spec.Describe("/v1/provider/github/webhook", http.MethodPost,
		"GitHub App webhook",
		"The address the GitHub App delivers events to. A push is handed to the repository "+
			"sync engine, and an issue or issue-comment event is mirrored into the native "+
			"todo — idempotently, so the same issue re-syncs to one row however many "+
			"times it is edited, closed or reopened. A repository event that puts a repo "+
			"INTO the granted set (created, transferred, unarchived) raises one todo "+
			"offering to import it; accepting means POSTing that repo to /v1/provider/github/import. It never imports on its own.\n\n"+
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
	// The one endpoint here whose BODIES are declared as well as described. Both are
	// free-form objects and their shape is in the prose, because neither is this
	// package's to publish as a schema: the request is OpenTelemetry's OTLP/JSON, of
	// which openrouter.go decodes the subset a usage row is built from, and a partial
	// view published as "the" schema is a document that lies.
	spec.Register(openrouterPath, http.MethodPost, map[string]any{}, map[string]any{})
	spec.Describe(openrouterPath, http.MethodPost,
		"Receive OpenRouter Broadcast traces as usage rows",
		"OpenRouter's spend is invisible to every Hanzo money lens because those lenses read "+
			"hanzo.cloud_usage and OpenRouter meters keys of its own. Point a Broadcast destination "+
			"(Settings ▸ Observability ▸ Webhook) at this endpoint and each generation span becomes ONE "+
			"row in that same ledger with provider `openrouter`, so one query answers what we spend "+
			"everywhere. Enable the Cost and Identity field categories: cost is the money and identity "+
			"carries `openrouter.api_key_name`, which is what says WHICH key spent it — it lands in "+
			"`account` as openrouter/<key name>.\n\n"+
			"AUTHENTICATION IS A HANZO KEY. Broadcast signs nothing; its only authentication is the "+
			"destination's Headers map, so send a key as `Authorization: Bearer pk-…` and it is "+
			"admitted exactly as /v1/event admits a beacon's: a project key resolves through the "+
			"project that minted it, an IAM-issued key through IAM. That key names the org every row "+
			"is filed under; it can write and cannot read. No key, or a key that names no org, is 401 "+
			"and nothing is stored.\n\n"+
			"The body is OTLP/JSON — `{resourceSpans:[{scopeSpans:[{spans:[…]}]}]}` — exactly as "+
			"OpenTelemetry defines it; the model, tokens and cost are read from each span's `gen_ai.*` "+
			"attributes and the key name from `openrouter.api_key_name`. The answer is "+
			"`{stored, dropped}`: how many generations became rows, and how many spans named no model. "+
			"Those are OpenRouter's trace and span parents — they carry no cost to meter. An empty "+
			"payload stores nothing and answers 200, which is what makes Test Connection pass. A "+
			"warehouse that cannot take the rows answers 503 so the delivery shows red and can be "+
			"replayed: a row is keyed by its span id, so a redelivery collapses rather than "+
			"double-counting.")

	// ── account-link flows (three legs each) ─────────────────────────────────
	spec.Describe("/v1/provider/slack/link", http.MethodGet,
		"Begin linking a Hanzo account from Slack",
		"The entry point behind the connect prompt Hanzo posts in Slack. It starts a link "+
			"session in the browser and redirects to Slack's own sign-in, which is what "+
			"proves which Slack user is asking."+linkFlow)
	spec.Describe("/v1/provider/slack/link/slack", http.MethodGet,
		"Slack sign-in return leg",
		"Where Slack returns the user after they sign in. It establishes the verified Slack "+
			"workspace and user, confirms that workspace is connected to an org, and hands "+
			"the browser on to the Hanzo sign-in that completes the link.\n\n"+
			"The verified pair is carried onward in a host-bound cookie rather than in the "+
			"URL, so the identity being linked cannot be edited in transit."+linkFlow)
	spec.Describe("/v1/provider/slack/link/callback", http.MethodGet,
		"Complete the Slack account link",
		"The final leg: the user has proved both who they are in Slack and who they are in "+
			"Hanzo, and this binds the two. It answers a short confirmation page telling "+
			"them to return to Slack.\n\n"+
			"The Hanzo credential obtained here is sealed into the connected workspace's own "+
			"KMS namespace; it is never written to a database column and never logged. A "+
			"deployment whose secret store is unavailable refuses the link rather than "+
			"completing it without custody of the credential."+linkFlow)
	spec.Describe("/v1/provider/discord/link", http.MethodGet,
		"Begin linking a Hanzo account from Discord",
		"The entry point behind the connect prompt Hanzo shows in a Discord server. It "+
			"starts a link session and redirects to Discord's OAuth `identify` consent — the "+
			"narrowest scope that establishes which Discord user is asking, and nothing "+
			"more."+linkFlow)
	spec.Describe("/v1/provider/discord/link/discord", http.MethodGet,
		"Discord sign-in return leg",
		"Where Discord returns the user after the identify consent. It resolves the verified "+
			"Discord user, confirms the server is connected to an org, and hands the browser "+
			"to the Hanzo sign-in that completes the link."+linkFlow)
	spec.Describe("/v1/provider/discord/link/callback", http.MethodGet,
		"Complete the Discord account link",
		"The final leg: it binds the verified Discord user to the Hanzo account that just "+
			"signed in, and answers a short confirmation page telling them to return to "+
			"Discord. The Hanzo credential is sealed into the connected org's KMS namespace "+
			"rather than stored beside the link."+linkFlow)
	spec.Describe("/v1/provider/teams/link", http.MethodGet,
		"Begin linking a Hanzo account from Teams",
		"The entry point behind the connect prompt Hanzo shows in Teams. It starts a link "+
			"session and redirects to Microsoft sign-in addressed to the CHAT'S OWN tenant, "+
			"not the common endpoint, so only a member of that tenant can complete it."+
			linkFlow)
	spec.Describe("/v1/provider/teams/link/aad", http.MethodGet,
		"Microsoft sign-in return leg",
		"Where Microsoft returns the user after sign-in. It resolves the verified directory "+
			"identity and then re-checks the tenant: the signed-in user's tenant must equal "+
			"the tenant of the chat the link started from, so a valid Microsoft sign-in from "+
			"a different organization is refused here rather than accepted.\n\n"+
			"This is the leg Teams has and the other platforms do not, which is why the "+
			"Teams flow has an extra address."+linkFlow)
	spec.Describe("/v1/provider/teams/link/callback", http.MethodGet,
		"Complete the Teams account link",
		"The final leg: it binds the verified directory identity to the Hanzo account that "+
			"just signed in, and answers a short confirmation page telling them to return to "+
			"Teams. The Hanzo credential is sealed into the connected org's KMS namespace."+
			linkFlow)
	spec.Describe("/v1/provider/telegram/link", http.MethodGet,
		"Begin linking a Hanzo account from Telegram",
		"The entry point behind the connect prompt Hanzo sends in Telegram. Unlike the other "+
			"platforms it answers an HTML PAGE rather than a redirect: Telegram has no OAuth "+
			"flow, so the page hosts Telegram's Login Widget, and the browser is sent onward "+
			"only after the user signs in through it.\n\n"+
			"The widget only appears on the domain registered for the bot, so a deployment "+
			"whose bot domain is unset renders a page with nothing on it."+linkFlow)
	spec.Describe("/v1/provider/telegram/link/auth", http.MethodGet,
		"Telegram Login Widget return leg",
		"Where Telegram's Login Widget sends the user with its signed authentication data. "+
			"That data is verified against the bot token — this is the identity source, and "+
			"it is the widget's signature rather than a code exchange — and the chat is "+
			"confirmed to be bound to an org before the browser is handed to the Hanzo "+
			"sign-in.\n\n"+
			"Widget data is only accepted while it is fresh, so a captured sign-in blob "+
			"cannot be replayed later even though its signature stays valid."+linkFlow)
	spec.Describe("/v1/provider/telegram/link/callback", http.MethodGet,
		"Complete the Telegram account link",
		"The final leg: it binds the verified Telegram user to the Hanzo account that just "+
			"signed in, and answers a short confirmation page telling them to return to "+
			"Telegram. The Hanzo credential is sealed into the connected org's KMS "+
			"namespace."+linkFlow)

	// ── the generic OAuth return ─────────────────────────────────────────────
	spec.Describe("/v1/provider/:provider/callback", http.MethodGet,
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
			"code's place so the App model needs no second address.\n\n"+
			"A GitHub connect that returns from GitHub's authorization page, rather than its "+
			"install page, answers a short page instead of a redirect when the person already "+
			"controls installations no organization holds: they choose which one to connect, "+
			"or go on to install the app on another account. Nothing is bound until they choose.")
	spec.Describe("/v1/provider/:provider/choose", http.MethodPost,
		"Answer the connect's account chooser",
		"The form the chooser page posts: `state` (the token the page was given), and either "+
			"`account` (one of the accounts it offered) or `install` (go on to the provider's "+
			"install page). It binds the chosen account to the organization that started the "+
			"connect and redirects back to the page that asked, as the callback does.\n\n"+
			"It is public and carries no principal. Its authority is the token alone: signed like "+
			"the callback's state, carrying the offered accounts under its signature, and "+
			"single-use, so a page answers once and only with an account it offered.")
}

// The two are interleaved in the ORIGINAL order because fiber resolves by
// registration order, and that order is load-bearing here (literals before the
// /:provider wildcards).
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	g := app.Group("/v1/provider")
	// channelFacts first: every typed op below reads the caller's identity facts off
	// the request context, and a Use only runs ahead of routes registered after it.
	//
	// The ORG is NOT parked here, and this subsystem does not install cloud.Bridge.
	// Whoever composes the program installs it once at the root — after the identity
	// check that mints the validated org and before any subsystem registers a route
	// (serve.go) — because that order is a property of the whole program and no
	// subsystem can assert it for itself.
	//
	// channelFacts is integrations' own, so it hangs on the group it covers: this
	// one for the org plane, and the user plane's own in connectionRoutes.
	g.Use(zip.H(channelFacts))

	g.Get("", o.list)
	// Slack adapter (slack_events.go / slack_link.go), every route raw on purpose.
	// The two webhooks speak Slack's own protocol: the HMAC covers the RAW bytes,
	// which an op handed the decoded In could not re-verify. The three link legs
	// drive a browser — a 302 to the Slack / hanzo.id sign-in, then a short HTML
	// confirmation page — and never answer JSON.
	// The Marketplace / "Add to Slack" entry point. A literal, so it is matched
	// before the /:provider wildcards below, and Terminal like its siblings: it
	// carries no principal by design and must not be gated into a 403, because the
	// person clicking Install in Slack's directory has no Hanzo session yet.
	g.Raw(http.MethodGet, "/slack/install", cloud.Terminal(cloud.Handle(s, slackInstall)))
	g.Raw(http.MethodPost, "/slack/events", cloud.Terminal(cloud.Handle(s, slackEvents)))
	g.Raw(http.MethodPost, "/slack/commands", cloud.Terminal(cloud.Handle(s, slackCommands)))
	g.Raw(http.MethodGet, "/slack/link", cloud.Handle(s, slackLink))
	g.Raw(http.MethodGet, "/slack/link/slack", cloud.Handle(s, slackLinkSlack))
	g.Raw(http.MethodGet, "/slack/link/callback", cloud.Handle(s, slackLinkCallback))
	// Walk into every public channel in the workspace (slack_join.go). Typed and
	// org-authed, unlike the webhooks above: it is the org acting on its own
	// workspace, not Slack calling us. A literal, so it is matched before the
	// /:provider wildcards.
	g.Post("/slack/join", o.slackJoin)
	g.Get("/slack/channels", o.slackChannels)
	g.Get("/slack/messages", o.slackMessages)
	g.Put("/slack/messages", o.slackUpdateMessage)
	g.Delete("/slack/messages", o.slackDeleteMessage)
	g.Post("/slack/reactions", o.slackReact)
	g.Post("/slack/search", o.slackSearch)
	g.Post("/slack/messages", o.slackSendMessage)
	// The CONTENT of a file on a message (slack_file.go). Untyped, unlike its
	// siblings above, because the response is the bytes: there is no In/Out pair
	// for a stream, and a base64 envelope would make every reader decode a file to
	// look at it. Org-authed like the rest — the caller's own workspace, its own
	// bot token — and a literal, so it is matched before the /:provider wildcards.
	g.Raw(http.MethodGet, "/slack/file", cloud.Handle(s, slackFileRead))
	// GitHub App sync (github_app.go / github_webhook.go). The App POSTs push events
	// here, at the address every other vendor's inbound endpoint already uses:
	// /v1/provider/<vendor>/<the vendor's own noun>, GitHub's being "webhook".
	// It answered at /v1/connector/github/webhook until this change — a namespace one
	// endpoint in the estate used and nothing else, so the fleet carried a second word for
	// "an integration". It is PUBLIC at the JWT layer, HMAC-verified inside, and hands
	// the push to the universal sync engine (cloud.Sync). cloud.Terminal writes the
	// handler's reject status in-band so the commerce /v1 ErrorHandlerJSON (co-mounted
	// ahead of us) cannot flatten a bad-sig 401 / malformed-body 400 to 500. It stays
	// raw because it speaks GitHub's webhook protocol: the HMAC covers the raw body,
	// which an op handed the decoded In could not re-verify.
	g.Raw(http.MethodPost, "/github/webhook", cloud.Terminal(cloud.Handle(s, githubWebhook)))
	// The forge delivers a queued workflow job here (forge_webhook.go). Raw and
	// Terminal for the same reason as GitHub's: the HMAC covers the raw body.
	g.Raw(http.MethodPost, forgeWebhookLeaf, cloud.Terminal(cloud.Handle(s, forgeWebhook)))
	// Linear delivers Issue and Comment events here (linear_webhook.go). Raw and
	// Terminal for the same reason as GitHub's: the HMAC covers the raw body.
	g.Raw(http.MethodPost, "/linear/webhook", cloud.Terminal(cloud.Handle(s, linearWebhook)))
	// OpenRouter's Broadcast destination (openrouter.go). Raw and Terminal like its
	// siblings, and public at the JWT layer for the same reason: the credential is a
	// key in the destination's Headers map, admitted INSIDE the handler through the
	// admission /v1/event uses. A literal, so it is matched before the /:provider
	// wildcards below.
	g.Raw(http.MethodPost, openrouterLeaf, cloud.Terminal(cloud.Handle(s, openrouterWebhook)))
	// A person's own GitHub (github_user.go): connect, the callback GitHub returns
	// to, the completion the signed-in console makes, disconnect and status. The
	// callback is raw because it answers a browser with a redirect, and public
	// because GitHub's return carries no principal; the state is its authority.
	g.Get("/github/user", o.githubUserStatus)
	g.Post("/github/user/connect", o.githubUserConnect)
	g.Raw(http.MethodGet, "/github/user/callback", cloud.Handle(s, githubUserCallback))
	g.Post("/github/user/complete", o.githubUserComplete)
	g.Post("/github/user/disconnect", o.githubUserDisconnect)
	// A person's own Google (google_user.go), in the same five shapes: the grant
	// sends mail as them from their Gmail (google_send.go), and nothing else.
	g.Get("/google/user", o.googleUserStatus)
	g.Post("/google/user/connect", o.googleUserConnect)
	g.Raw(http.MethodGet, "/google/user/callback", cloud.Handle(s, googleUserCallback))
	g.Post("/google/user/complete", o.googleUserComplete)
	g.Post("/google/user/disconnect", o.googleUserDisconnect)
	g.Get("/github/installations", o.githubInstallations)
	// Bind installations the App already holds to the org the caller acts in.
	g.Post("/github/claim", o.githubClaim)
	g.Get("/github/repos", o.githubRepos)
	g.Post("/github/repos", o.githubCreateRepo, zip.WithStatus(http.StatusCreated))
	// FIND ONE, AND TAKE A COPY. Search reads GitHub's public index — how you
	// find a repository to fork, never a way to see inside one. Fork resolves
	// against the installation's GRANTED set like every other write here.
	g.Post("/github/search", o.githubSearch)
	g.Post("/github/fork", o.githubFork)
	// 202: the import runs in a bounded background worker, so the op DECLARES the
	// status it has always answered rather than setting it per request.
	g.Post("/github/repos/import", o.githubImport, zip.WithStatus(http.StatusAccepted))
	// Seed the native todo with the org's EXISTING GitHub issues (the webhook
	// keeps them live thereafter). Org-authed via the principal; bounded + idempotent.
	g.Post("/github/issues/backfill", o.githubIssuesBackfill)
	// Linear (linear.go): bind the caller's Linear organization + webhook secret,
	// comment on an issue with the caller's own key, seed the native todo. All three
	// spend the person's connector, so the caller is (org,user).
	g.Post("/linear/claim", o.linearClaim)
	g.Post("/linear/comments", o.linearComment, zip.WithStatus(http.StatusCreated))
	g.Post("/linear/issues/backfill", o.linearIssuesBackfill)
	// GitHub Pages management (github_pages.go), one repo as a resource. Registered
	// AFTER the literal /repos/import so registration-order matching keeps the literal
	// unshadowed; the :repo routes all carry a /pages suffix, so /repos/import (no
	// suffix) never matches them. Org-authed via the principal; the repo is resolved
	// against the installation's granted set (owner is server-derived).
	// One repository's branches, from the caller's own index (github_branches.go).
	// Three segments after /repos, so it never meets the one-segment :repo
	// routes below, and its literal tail tells it from .../pages/builds.
	g.Get("/github/repos/:owner/:repo/branches", o.githubBranches)
	g.Get("/github/repos/:repo/pages", o.githubPagesGet)
	g.Post("/github/repos/:repo/pages", o.githubPagesEnable)
	g.Put("/github/repos/:repo/pages", o.githubPagesUpdate)
	g.Delete("/github/repos/:repo/pages", o.githubPagesDisable)
	// 202: the build is queued at GitHub, not completed here.
	g.Post("/github/repos/:repo/pages/builds", o.githubPagesBuild, zip.WithStatus(http.StatusAccepted))
	// ChatBridge adapters (channel.go + discord/teams/telegram). Same discipline as
	// the slack channel: the literal paths register BEFORE the /:provider wildcards so
	// they win under registration-order matching. All PUBLIC at the JWT layer — auth
	// is done INSIDE each handler: Discord Ed25519 interaction verify, Teams Bot
	// Framework JWT, Telegram secret-token; the link legs use signed __Host- cookies +
	// state. Telegram's /connect is org-authed via the principal (like the framework
	// connect). They must NOT sit behind any principal/tenant gate.
	// Every route here is raw on purpose: each event endpoint speaks its platform's
	// own webhook protocol over the raw request (Discord's Ed25519 signs the raw
	// bytes; Teams and Telegram answer in their platform's envelope), and every
	// link leg drives a browser with a 302 or an HTML page, never JSON.
	g.Raw(http.MethodPost, "/discord/interactions", cloud.Terminal(cloud.Handle(s, discordInteractions)))
	g.Raw(http.MethodGet, "/discord/link", cloud.Handle(s, discordLink))
	g.Raw(http.MethodGet, "/discord/link/discord", cloud.Handle(s, discordLinkDiscord))
	g.Raw(http.MethodGet, "/discord/link/callback", cloud.Handle(s, discordLinkCallback))
	g.Raw(http.MethodPost, "/teams/events", cloud.Terminal(cloud.Handle(s, teamsEvents)))
	g.Raw(http.MethodGet, "/teams/link", cloud.Handle(s, teamsLink))
	g.Raw(http.MethodGet, "/teams/link/aad", cloud.Handle(s, teamsLinkAAD))
	g.Raw(http.MethodGet, "/teams/link/callback", cloud.Handle(s, teamsLinkCallback))
	g.Post("/telegram/connect", o.telegramConnect)
	g.Raw(http.MethodPost, "/telegram/webhook", cloud.Terminal(cloud.Handle(s, telegramWebhook)))
	// GET answers Meta's subscription challenge; POST takes the deliveries. Both
	// live at one address because that is the address Meta is configured with.
	g.Raw(http.MethodGet, "/whatsapp/webhook", cloud.Handle(s, whatsappVerify))
	g.Raw(http.MethodPost, "/whatsapp/webhook", cloud.Terminal(cloud.Handle(s, whatsappWebhook)))
	// Groups this org's number owns. Typed and org-admin, unlike the webhook
	// above: it is the org acting on its own number, not Meta calling us.
	// Literal, so it is matched before the /:provider wildcards.
	g.Post("/whatsapp/groups", o.whatsappCreateGroup, zip.WithStatus(http.StatusCreated))
	g.Get("/whatsapp/groups", o.whatsappGroupLink)
	g.Raw(http.MethodGet, "/telegram/link", cloud.Handle(s, telegramLink))
	g.Raw(http.MethodGet, "/telegram/link/auth", cloud.Handle(s, telegramLinkAuth))
	g.Raw(http.MethodGet, "/telegram/link/callback", cloud.Handle(s, telegramLinkCallback))
	// What the GitLab connection reaches (gitlab_projects.go). Literal, so it is
	// matched before the /:provider wildcards below. The token stays in KMS: a
	// caller asks this package to read GitLab, the way the GitHub routes above
	// already work, rather than asking for the credential.
	g.Get("/gitlab/projects", o.gitlabProjects)
	// The per-user connector plane (connection.go) answers under the app's other
	// declared prefix, so it takes the grant and opens its own group.
	connectionRoutes(app, o)
	g.Get("/:provider", o.get)
	g.Post("/:provider/connect", o.connect)
	// PUBLIC, RAW (bytes): the connector's logo, from the embedded catalog.
	g.Raw(http.MethodGet, "/:provider/logo", cloud.Handle(s, logo))
	// Run one connector action as the caller's org (piece.go).
	g.Post("/:provider/run", o.run)
	// PUBLIC, state-authed, RAW (302). RedirectPath == this path for every provider
	// (asserted in Use), so this single generic route serves every provider's
	// OAuth callback.
	// X's developer app also registers /v1/provider/twitter/callback. The consent
	// URL this process sends is /v1/provider/x/callback. This alias completes a
	// browser that was sent to the other spelling, against the same provider.
	g.Raw(http.MethodGet, "/twitter/callback", cloud.Handle(s, twitterCallback))
	g.Raw(http.MethodGet, "/:provider/callback", cloud.Handle(s, callback))
	// PUBLIC, token-authed, RAW (a form post answered with a 302): the chooser
	// page the callback draws posts its answer here (choose.go).
	g.Raw(http.MethodPost, chooseLeaf, cloud.Handle(s, choose))
	g.Post("/:provider/disconnect", o.disconnect)
	// apikey connectors: re-verify a stored credential live (`hanzo connector verify`).
	g.Post("/:provider/verify", o.verifyConn)
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
	withPieces()
	out := make(map[string]*Provider, len(registry))
	maps.Copy(out, registry)
	return out
}

// ── handlers ───────────────────────────────────────────────────────────────────

// list returns every registered integration provider together with THIS org's
// connection status for it — the catalog the console's Integrations page renders.
// Org-authed: a caller with no validated principal is 403, because the status is
// per-org and there is no org-less answer. User-plane providers (the /v1/connection
// surface) are omitted; the two planes are disjoint.
//
// Response: {"providers":[{"id":"slack","name":"Slack","description":"Connect your workspace.","category":"Communication","available":true,"connected":true,"connection":{"account":"Acme","externalId":"T0231","scopes":["chat:write"],"connectedAt":"2026-07-01T10:00:00Z"}}]}
func (o ops) list(ctx context.Context, _ *cloud.Unit) (*listOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	// ONE read for the whole catalog: the org's connections, grouped by
	// provider. A read per provider was a query per card, and the catalog is
	// hundreds of cards.
	conns, err := o.s.State.store.List(ctx, org, "")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	held := map[string]Connection{}
	for _, c := range conns {
		if _, seen := held[c.Provider]; !seen {
			held[c.Provider] = c
		}
	}
	ids := sortedProviderIDs(o.s)
	out := make([]providerView, 0, len(ids))
	for _, id := range ids {
		p := o.s.State.providers[id]
		if p.Scope == userScope {
			continue // user-plane providers are invisible on the org surface
		}
		c, ok := held[id]
		out = append(out, orgView(p, c, ok))
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
	org, err := principal.Acting(ctx)
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
	// One connector is read to use it, so its card says what each action
	// takes: an agent reads this and then runs the action.
	v.Actions, v.Triggers = opDetail(p, false), opDetail(p, true)
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
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	who := ""
	if self, serr := iam.SelfFrom(ctx); serr == nil {
		who = self.ID
	}
	return connectFor(ctx, o.s, org, who, orgAdmin(ctx), in)
}

func connectFor(ctx context.Context, s *cloud.Service[state], org, who string, isAdmin bool, in *connectIn) (*connectOut, error) {
	p, ok := orgProvider(s, in.ref().id())
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	// Mutating a connector is an org-admin action for providers that declare it
	// (parity with the platform deploy-provider adminProcedure). The predicate is
	// the caller's OWN-org isAdmin bit (principal.IsOrgAdmin) — NOT SuperAdmin.
	if p.AdminOnly && !isAdmin {
		return nil, zip.ErrForbidden("connecting the " + p.ID + " connector requires org admin")
	}
	if !p.Configured() && credsFor(s, org, p).ClientID == "" {
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
	if p.Kind == noneKind {
		return nil, zip.Errorf(http.StatusConflict, "%s has nothing to connect", p.Name)
	}
	if p.Verify != nil && (p.Authorize == nil || in.Token != nil || in.Fields != nil) {
		return connectByCredential(s, ctx, org, p, in)
	}
	if p.Authorize == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s: no connect method configured", p.ID)
	}
	// The authorize leg needs Hanzo's registered app creds. A dual-path provider
	// keeps Configured()==true for its always-available apikey path, so gate on the
	// authorize leg's OWN creds here — an honest 503 that points the caller at the
	// token path rather than a dead consent URL with an empty client_id.
	creds := credsFor(s, org, p)
	if creds.ClientID == "" && !authorizeReady(p) {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s is not configured for the install flow on this deployment; connect with a scoped API token instead", p.ID)
	}
	if creds.ClientID == "" && p.Creds != nil {
		creds = p.Creds()
	}

	// Opportunistic GC of expired nonces (best-effort; never fails the request).
	if _, gerr := s.State.store.GCNonces(ctx, staleNonceCutoff()); gerr != nil {
		s.Log.Warn("nonce gc", "err", gerr)
	}

	nonce, err := genToken()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	if err := s.State.store.PutNonce(ctx, nonce, org, p.ID, checkedBack(s, in.Return)); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "nonce: %v", err)
	}
	signed, err := sign(s, org, who, p.ID, nonce)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "state: %v", err)
	}
	authorizeURL, err := p.Authorize(creds, redirectURI(s, p), signed)
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
	// Fields are the named inputs of a credential connector whose form is more
	// than one key (the connector's published fields). Their presence selects
	// the credential path exactly as Token's does. Never logged or echoed.
	Fields map[string]string `json:"fields"`
	// Return is the page the OAuth callback sends the person back to, with
	// connected=<provider>&account=<label> or error=<provider>&reason=<why> added
	// to its query. It must name one of this deployment's return pages (on
	// api.hanzo.ai: https://hanzo.ai/?at=-/settings/integrations,
	// https://console.hanzo.ai/connectors, https://platform.hanzo.ai/connectors);
	// anything else, or nothing, returns to the console's /connectors. Ignored by
	// the credential path.
	Return string `json:"return" url:"-"`
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
	if token == "" && len(in.Fields) == 0 {
		return nil, zip.ErrBadRequest("a credential token is required (pipe it on stdin: `hanzo connector add --provider " + p.ID + " --token -`)")
	}
	if len(token) > maxCredentialLen || len(in.Fields) > 64 {
		return nil, zip.ErrBadRequest("credential too large")
	}
	res, err := p.Verify(ctx, VerifyInput{Token: token, AccountID: sanitizeMeta(strings.TrimSpace(in.AccountID)), Fields: in.Fields})
	if err != nil || res == nil {
		// Verify FAILED → refuse; store NOTHING. err is provider-authored and must
		// not carry the credential value (only its status/reason).
		s.Log.Warn("connector verify failed", "provider", p.ID, "org", org, "err", err)
		var said verifyRefused
		if errors.As(err, &said) {
			return nil, zip.ErrBadRequest("credential verification failed: " + string(said))
		}
		return nil, zip.ErrBadRequest("credential verification failed")
	}
	// Harden provider-supplied NON-secret metadata (strip control chars, bound
	// length) — never the secret token, which goes straight to the KMS seal.
	sanitizeResult(res)
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
		Installer:    res.Installer,
		Scopes:       res.Scopes,
		ExpiresAt:    res.ExpiresAt,
	}
	if err := bind(ctx, s, p, conn, res.Tokens); err != nil {
		s.Log.Warn("connector bind failed", "provider", p.ID, "org", org, "err", err)
		switch {
		case errors.Is(err, errBound):
			return nil, zip.Errorf(http.StatusConflict, "%s", heldElsewhere)
		case errors.Is(err, errCustody):
			return nil, zip.Errorf(http.StatusServiceUnavailable, "secret custody failed")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "persist failed")
	}
	s.Log.Info("connector connected", "provider", p.ID, "org", org, "account", res.AccountLabel, "externalId", res.ExternalID)
	rememberSocial(ctx, conn)
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
	org, err := principal.Acting(ctx)
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
// (success or a labeled failure) — never a raw JSON dead-end — to the page the
// connect named, once the state proves which flow this is, and to the console
// before that or when it named none. The org comes ONLY from the signed,
// single-use state; no header is trusted here.
func callback(s *cloud.Service[state], c *zip.Ctx) error {
	return callbackFor(s, c, providerParam(c))
}

// Completes an X consent that arrived on the twitter spelling of the callback.
// The consent URL this process sends is /v1/provider/x/callback. The X app is
// also registered at this address, so a browser sent there still finishes the
// same flow. State is signed for provider x, and the answer is a redirect.
func twitterCallback(s *cloud.Service[state], c *zip.Ctx) error {
	return callbackFor(s, c, xProvider)
}

func callbackFor(s *cloud.Service[state], c *zip.Ctx, pid string) error {
	p, ok := orgProvider(s, pid)
	if !ok {
		return failRedirect(s, c, "", pid, "unknown provider", nil)
	}

	payload, err := verify(s, c.Query("state"), p.ID)
	if err != nil {
		return failRedirect(s, c, "", p.ID, "invalid state", err)
	}
	// A chooser's token is signed with the same key but is no callback state, and
	// it is refused before its nonce is spent, so the choice it offers still stands.
	if payload.chooser() {
		return failRedirect(s, c, "", p.ID, "invalid state", errors.New("a chooser's token came back as a callback state"))
	}
	// Consume the single-use nonce, bound to (org,provider). Burned BEFORE the
	// exchange so one state = one attempt: a replay (or a slow-flow retry) finds
	// zero rows and fails here, never double-exchanging.
	back, consumed, err := s.State.store.ConsumeNonce(c.Context(), payload.Nonce, payload.Org, p.ID)
	if err != nil {
		return failRedirect(s, c, "", p.ID, "state error", err)
	}
	if !consumed {
		return failRedirect(s, c, "", p.ID, "state already used or expired",
			fmt.Errorf("org %s: nonce not present (replayed, or GC'd after %s)", payload.Org, stateTTL))
	}
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		// The provider's own refusal text, bounded: it is remote input on a public
		// route, so it is logged at a fixed width and never reflected to the browser.
		return failRedirect(s, c, back, p.ID, "authorization denied",
			fmt.Errorf("org %s: provider reported %q", payload.Org, truncate(e, 200)))
	}
	// A provider with neither Exchange nor Install does not complete its binding
	// here, and the callback must say so rather than invent one. An App install
	// hands back an installation_id the state cannot cover — the install does not
	// exist when the state is minted — so the id alone is nothing but a number the
	// caller typed. The App JWT reads every tenant's installation, so an id that
	// resolves proves only that the App is installed somewhere, never that it is
	// installed for THIS org. What proves it is the installer's own authorization,
	// which Install checks the id against.
	if p.Exchange == nil && p.Install == nil {
		return failRedirect(s, c, back, p.ID, "this install must be bound by an operator",
			fmt.Errorf("org %s: %s returns no grant this org can prove (installation %q)",
				payload.Org, p.ID, truncate(strings.TrimSpace(c.Query("installation_id")), 32)))
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		return failRedirect(s, c, back, p.ID, "missing authorization code",
			fmt.Errorf("org %s: no code present", payload.Org))
	}
	if len(code) > maxCodeLen {
		return failRedirect(s, c, back, p.ID, "authorization code too large",
			fmt.Errorf("org %s: %d bytes exceeds %d", payload.Org, len(code), maxCodeLen))
	}
	creds := credsFor(s, payload.Org, p)
	if !p.Configured() && creds.ClientID == "" {
		return failRedirect(s, c, back, p.ID, "provider not configured", nil)
	}
	if creds.ClientID == "" && p.Creds != nil {
		creds = p.Creds()
	}
	if len(p.Secrets) > 0 && !kmsReady(s) {
		return failRedirect(s, c, back, p.ID, "secret store unavailable", nil)
	}

	installation := strings.TrimSpace(c.Query("installation_id"))
	if p.Installed != nil && installation == "" {
		return finishInstalled(s, c, p, payload, back, creds, code)
	}
	var res *ExchangeResult
	if p.Install != nil {
		res, err = p.Install(c.Context(), creds, code, installation)
		if err != nil || res == nil {
			return failRedirect(s, c, back, p.ID, "the installation could not be verified", fmt.Errorf("org %s: %w", payload.Org, err))
		}
	} else {
		res, err = p.Exchange(c.Context(), creds, redirectURI(s, p), code)
		if err != nil || res == nil {
			return failRedirect(s, c, back, p.ID, "token exchange failed", fmt.Errorf("org %s: %w", payload.Org, err))
		}
	}
	// Harden the provider-supplied NON-secret metadata at the framework ingest
	// boundary — ONE place, every provider — before it is logged, stored, or
	// reflected in the redirect. Secret token VALUES are never passed through
	// here; they go straight to the KMS seal.
	sanitizeResult(res)
	if p.Expand != nil {
		return finishExpanded(s, c, p, payload, back, creds, res)
	}
	conn := Connection{
		Org:      payload.Org,
		Provider: p.ID,
		Label:    connOwner(p, res), // part of the key — see the connect path

		ExternalID:   res.ExternalID,
		AccountLabel: res.AccountLabel,
		BotUserID:    res.BotUserID,
		Installer:    res.Installer,
		Scopes:       res.Scopes,
		ExpiresAt:    res.ExpiresAt,
	}
	if conn.Installer == "" {
		conn.Installer = payload.User
	}
	if err := bind(c.Context(), s, p, conn, res.Tokens); err != nil {
		cause := fmt.Errorf("org %s: %w", payload.Org, err)
		switch {
		case errors.Is(err, errBound):
			return failRedirect(s, c, back, p.ID, heldElsewhere, cause)
		case errors.Is(err, errCustody):
			return failRedirect(s, c, back, p.ID, "secret custody failed", cause)
		}
		return failRedirect(s, c, back, p.ID, "persist failed", cause)
	}
	s.Log.Info("integration connected", "provider", p.ID, "org", payload.Org, "account", res.AccountLabel, "externalId", res.ExternalID)
	rememberSocial(c.Context(), conn)
	return successRedirect(s, c, back, p.ID, res.AccountLabel)
}

// finishInstalled finishes an APP provider's connect that came back from the provider's
// authorization page rather than its install page: a code and no installation.
// The installations the person already controls (p.Installed) that no org holds
// are OFFERED, not bound: the person chooses one on the chooser page (choose.go),
// or goes on to the install page to put the App on another account or change
// which repositories it reaches. Binding every one of them at once was what made
// a reconnect after a Disconnect come back as the old account, with no way to pick
// another. An account the App is already on still connects without the
// provider's Configure page, which returns nothing. Another org's account is never
// offered, but it is SHOWN, unnamed, as held elsewhere: the install page would
// offer it only Configure, which returns nothing, and a person whose own account a
// workspace they have left still holds was stranded there. One this org holds is
// no answer to a connect that may be asking for another. With nothing to offer or
// show, the same browser goes on to the install page under a fresh state, and
// that install's return binds as ever.
func finishInstalled(s *cloud.Service[state], c *zip.Ctx, p *Provider, payload statePayload, back string, creds OAuthConfig, code string) error {
	ctx := c.Context()
	found, err := p.Installed(ctx, creds, code)
	if err != nil {
		return failRedirect(s, c, back, p.ID, "the authorization could not be verified", fmt.Errorf("org %s: %w", payload.Org, err))
	}
	held := map[string]bool{}
	for _, conn := range Connections(payload.Org, p.ID) {
		held[conn.ExternalID] = true
	}
	var open, elsewhere []offered
	for _, res := range found {
		sanitizeResult(res)
		if res.ExternalID == "" || held[res.ExternalID] {
			continue
		}
		other, err := s.State.store.Holder(ctx, p.ID, res.ExternalID, payload.Org)
		if err != nil {
			return failRedirect(s, c, back, p.ID, "state error", fmt.Errorf("org %s: %w", payload.Org, err))
		}
		o := offered{ID: res.ExternalID, Login: res.AccountLabel, Kind: res.Kind}
		if other != "" {
			elsewhere = append(elsewhere, o)
			continue
		}
		open = append(open, o)
	}
	if len(open) > 0 || len(elsewhere) > 0 {
		return offer(s, c, p, payload, back, open, elsewhere)
	}
	return installNext(s, c, p, payload, back, creds)
}

// installNext sends the same browser on to the provider's install page under a
// fresh state, so the install's return binds what it made for this org.
func installNext(s *cloud.Service[state], c *zip.Ctx, p *Provider, payload statePayload, back string, creds OAuthConfig) error {
	ctx := c.Context()
	nonce, err := genToken()
	if err != nil {
		return failRedirect(s, c, back, p.ID, "state error", err)
	}
	if err := s.State.store.PutNonce(ctx, nonce, payload.Org, p.ID, back); err != nil {
		return failRedirect(s, c, back, p.ID, "state error", err)
	}
	signed, err := sign(s, payload.Org, payload.User, p.ID, nonce)
	if err != nil {
		return failRedirect(s, c, back, p.ID, "state error", err)
	}
	page, err := p.InstallPage(creds, signed)
	if err != nil {
		return failRedirect(s, c, back, p.ID, "provider not configured", err)
	}
	return redirect(c, page)
}

// finishExpanded seals every account one consent produced. Each grant is a
// finished row with its own key, so an in-progress login is never stored under
// a key a saved account already uses.
func finishExpanded(s *cloud.Service[state], c *zip.Ctx, p *Provider, payload statePayload, back string, creds OAuthConfig, res *ExchangeResult) error {
	who := identity{ExternalID: res.ExternalID, Label: res.AccountLabel}
	grants, err := p.Expand(c.Context(), creds, res.Tokens[accessSecret], who)
	if err != nil || len(grants) == 0 {
		return failRedirect(s, c, back, p.ID, "no account to connect", fmt.Errorf("org %s: %w", payload.Org, err))
	}
	var first string
	for _, g := range grants {
		key := grantKey(g)
		if key == "" || g.ExternalID == "" {
			continue
		}
		token := g.Token
		if token == "" {
			token = res.Tokens[accessSecret]
		}
		tokens := map[string]string{accessSecret: token}
		expires := res.ExpiresAt
		// A company page that posts with the member token keeps that token's
		// refresh. A token of its own, a Page's or a long-lived one, is its own
		// credential: it brings the refresh it renews with, if any, and the
		// expiry it was given, which is none for a Page token.
		if token == res.Tokens[accessSecret] {
			if refresh := res.Tokens[refreshSecret]; refresh != "" {
				tokens[refreshSecret] = refresh
			}
		} else {
			expires = g.ExpiresAt
		}
		if g.Refresh != "" {
			tokens[refreshSecret] = g.Refresh
			expires = g.ExpiresAt
		}
		conn := Connection{
			Org: payload.Org, Provider: p.ID, Label: key,
			ExternalID: g.ExternalID, AccountLabel: g.AccountLabel,
			BotUserID: g.Kind, Installer: payload.User,
			Scopes: res.Scopes, ExpiresAt: expires,
		}
		if conn.Installer == "" {
			conn.Installer = g.Author
		}
		if err := bind(c.Context(), s, p, conn, tokens); err != nil {
			cause := fmt.Errorf("org %s: %w", payload.Org, err)
			switch {
			case errors.Is(err, errBound):
				return failRedirect(s, c, back, p.ID, heldElsewhere, cause)
			case errors.Is(err, errCustody):
				return failRedirect(s, c, back, p.ID, "secret custody failed", cause)
			}
			return failRedirect(s, c, back, p.ID, "persist failed", cause)
		}
		rememberSocial(c.Context(), conn)
		if first == "" {
			first = g.AccountLabel
		}
	}
	if first == "" {
		return failRedirect(s, c, back, p.ID, "no account to connect", fmt.Errorf("org %s: no grant had a key", payload.Org))
	}
	s.Log.Info("integration connected", "provider", p.ID, "org", payload.Org, "account", first)
	return successRedirect(s, c, back, p.ID, first)
}

// heldElsewhere is what a person is told when the account they installed on is
// already another org's. It names no org: which tenant holds an account is that
// tenant's business.
const heldElsewhere = "this account is connected to another organization"

// errCustody reports a grant that could not be sealed.
var errCustody = errors.New("secret custody failed")

// bind makes a verified grant this org's connection — the one path the OAuth
// callback and the credential connect share.
//
// An account another org holds is refused BEFORE anything is sealed. Sealing
// first and letting the row write refuse afterwards left the other org's token
// in this org's slot, over the token of the account this org really holds — and
// every later read through this org's row spoke with the other org's credential.
// A per-target provider seals under the account key, so two accounts of one
// network are two slots.
//
// The check, the seal and the row are held under one per-account lock, so two
// orgs completing an install on the same account at once cannot both pass the
// check. The store refuses the second row on its own; the lock is what keeps the
// loser's seal from landing at all. Process-local is enough for the same reason
// flight is: this store is one writer's SQLite.
//
// Tokens are still sealed before the row is written, so a custody failure leaves
// no row advertising a token that was never stored.
func bind(ctx context.Context, s *cloud.Service[state], p *Provider, conn Connection, tokens map[string]string) error {
	unlock := s.State.flight.lock("bind\x00" + p.ID + "\x00" + conn.ExternalID)
	defer unlock()
	other, err := s.State.store.Holder(ctx, p.ID, conn.ExternalID, conn.Org)
	if err != nil {
		return err
	}
	if other != "" {
		return fmt.Errorf("%w: %s %s belongs to %s", errBound, p.ID, conn.ExternalID, other)
	}
	if err := sealTokens(s, custodyPath(p, conn.Org, conn.Label), tokens); err != nil {
		return fmt.Errorf("%w: %v", errCustody, err)
	}
	if err := s.State.store.Upsert(ctx, conn); err != nil {
		return err
	}
	// A GitHub account just bound is in none of the org's repository listings
	// (github_index.go): they were filled before it, and its installation's
	// webhook clears only listings that already name it.
	if p.ID == "github" {
		repoIndex.forgetOrg(conn.Org)
	}
	return nil
}

// release forgets a provider account the provider says is gone — an uninstall,
// reported signed and naming the account rather than an org. Every org holding
// it loses it: its rows go, so the account resolves to nobody and is free to be
// installed again, and its custodied secrets go, because they are dead. Returns
// the orgs that held it.
func release(ctx context.Context, s *cloud.Service[state], p *Provider, externalID string) ([]string, error) {
	orgs, err := s.State.store.Release(ctx, p.ID, externalID)
	if err != nil {
		return nil, err
	}
	for _, org := range orgs {
		dropSecrets(s, org, p)
	}
	return orgs, nil
}

// dropSecrets deletes every secret an org custodies for a provider. Best-effort
// and idempotent: a secret already gone is the outcome wanted.
func dropSecrets(s *cloud.Service[state], org string, p *Provider) {
	dropSecretsAt(s, p, kmsPath(org, p.ID))
}

func dropSecretsAt(s *cloud.Service[state], p *Provider, path string) {
	if s.State.kms == nil || path == "" {
		return
	}
	for _, name := range p.Secrets {
		if err := kmsDelete(s, path, name); err != nil {
			s.Log.Warn("kms delete failed (continuing)", "provider", p.ID, "path", path, "secret", name, "err", err)
		}
	}
}

// disconnect revokes (best-effort) and forgets an org's connection: it deletes
// every custodied KMS secret and the connection row. Idempotent — disconnecting a
// provider that was never connected still returns {disconnected:true}. Symmetric
// with connect: an AdminOnly connector needs the caller's own-org admin bit.
//
// An `account` disconnects that one account and leaves the org's others; without
// it every account goes. A provider with an App installation (GitHub) also removes
// the installation at the provider for each account no other org still holds, so
// connecting that account again is a fresh install rather than GitHub's Configure
// page, which returns nothing the connect can bind.
//
// Example: {"provider":"github","account":"acme"}
// Response: {"disconnected":true,"accounts":["acme"],"uninstalled":["acme"]}
func (o ops) disconnect(ctx context.Context, in *disconnectIn) (*disconnectOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	p, ok := orgProvider(s, in.ref().id())
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	// Symmetric with connect: disconnecting an admin-only connector requires the
	// caller be an admin of its OWN org (principal.IsOrgAdmin — NOT SuperAdmin).
	if p.AdminOnly && !orgAdmin(ctx) {
		return nil, zip.ErrForbidden("disconnecting the " + p.ID + " connector requires org admin")
	}

	conns, err := s.State.store.ListFor(ctx, org, p.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	account := strings.TrimSpace(in.Account)
	held := len(conns)
	if account != "" {
		conns = slices.DeleteFunc(conns, func(c Connection) bool {
			return !strings.EqualFold(c.Label, account) && c.ExternalID != account
		})
	}
	found := len(conns) > 0
	// The accounts of one consent share a token, so revoking it for one would end
	// the others. It is revoked when the last account goes.
	last := len(conns) == held

	// Best-effort provider-side revoke of each account's own token. Never fails
	// the disconnect: local forgetting is what ends the connection.
	if found && last && p.Revoke != nil && len(p.Secrets) > 0 && kmsReady(s) {
		creds := credsFor(s, org, p)
		for _, conn := range conns {
			tok, gerr := kmsGet(s, custodyPath(p, org, conn.Label), p.Secrets[0])
			if gerr != nil {
				continue
			}
			if rerr := p.Revoke(ctx, creds, string(tok)); rerr != nil {
				s.Log.Warn("provider revoke failed (continuing)", "provider", p.ID, "org", org, "err", rerr)
			}
		}
	}
	for _, conn := range conns {
		dropSecretsAt(s, p, custodyPath(p, org, conn.Label))
	}
	out := &disconnectOut{Disconnected: true}
	if account == "" {
		dropSecrets(s, org, p)
		if _, derr := s.State.store.Disconnect(ctx, org, "", p.ID); derr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", derr)
		}
	} else {
		for _, conn := range conns {
			if _, derr := s.State.store.Delete(ctx, org, conn.User, p.ID, conn.Label); derr != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", derr)
			}
		}
	}
	for _, conn := range conns {
		out.Accounts = append(out.Accounts, conn.Label)
		forgetSocial(ctx, conn)
		if uninstall(ctx, s, p, org, conn) {
			out.Uninstalled = append(out.Uninstalled, conn.Label)
		}
	}
	return out, nil
}

// uninstall removes conn's installation at the provider when the org's row for
// it is gone and no other org holds the same account, and reports whether it did.
// Best-effort, like Revoke: the local row going is what ends the connection, and
// an installation left behind is the one the provider's own uninstall reaches.
func uninstall(ctx context.Context, s *cloud.Service[state], p *Provider, org string, conn Connection) bool {
	if p.Uninstall == nil || strings.TrimSpace(conn.ExternalID) == "" {
		return false
	}
	other, err := s.State.store.Holder(ctx, p.ID, conn.ExternalID, org)
	if err != nil {
		s.Log.Warn("uninstall skipped: holder lookup failed", "provider", p.ID, "org", org, "account", conn.Label, "err", err)
		return false
	}
	if other != "" {
		return false // another org still uses this installation
	}
	if err := p.Uninstall(ctx, conn); err != nil {
		s.Log.Warn("provider uninstall failed (continuing)", "provider", p.ID, "org", org, "account", conn.Label, "err", err)
		return false
	}
	return true
}

// ── view + redirect builders ───────────────────────────────────────────────────

// providerViewFor renders a provider's card for an org, folding in the org's live
// connection status. (Named …For, not providerView, because Go forbids a func and
// the providerView TYPE sharing an identifier — same deviation as ConnectionFor.)
// providerModes is how a provider can be connected, read off what it declares
// rather than from a parallel table. Order is stable so a card's badges do not
// reshuffle between reads.
//
// "managed" is the one worth naming carefully: it means THIS DEPLOYMENT holds the
// provider's own app credentials, so a reader connects by clicking once. A
// provider we ship an app for — Slack, GitHub — is managed; one whose app
// credentials nobody has configured is not, and its card says so instead of
// offering a Connect button that dead-ends.
func providerModes(p *Provider) []string {
	out := make([]string, 0, 4)
	// MANAGED MEANS WE HOLD THE PROVIDER'S OWN APP CREDENTIAL, so a reader
	// connects by pressing once. It therefore requires a CONSENT FLOW: an
	// apikey provider has no app credential to hold — the customer's key IS the
	// credential — so it is never managed however its Configured answers.
	//
	// Reading Creds+Configured alone said otherwise. cloudflare, whatsapp, chrome
	// and warpcast are apikey providers carrying `Configured: func() bool { return
	// true }` and, for three of them, a Creds stub that returns an empty config —
	// so all four were published as managed, and a console rendering from this
	// field would offer a one-press Connect for something that cannot work without
	// the customer's own token.
	//
	// IT ASKS THE CREDS, NOT Configured(), because those two are the same question
	// only for a single-path provider: every ordinary Configured is literally
	// `Creds().ClientID != ""`. Cloudflare is DUAL-PATH — a scoped API token or a
	// real consent flow — and its Configured is deliberately true for the apikey
	// leg, which is always available. Its OAuth leg gates on Creds().ClientID
	// inside connect, so that is the question here too: a deployment with no
	// Cloudflare OAuth app offers a token path and not a one-press one, and saying
	// otherwise is the same lie the other three told.
	if p.Authorize != nil && p.Creds != nil && p.Creds().ClientID != "" {
		out = append(out, "managed")
	}
	// Consent, whichever plane asks for it: Authorize is the org's three-legged
	// flow, Adopt the user plane's already-obtained bundle.
	if p.Authorize != nil || p.Adopt != nil {
		out = append(out, "oauth")
	}
	// A credential the CUSTOMER supplies is one the provider verifies. Reading
	// Verify rather than Kind is why the 42 providers registered with a keyVerify
	// and no Kind — HubSpot, Klaviyo, Mailchimp and the rest of the saas and
	// messaging tables — say what they are instead of rendering a card with no
	// badge and no way to connect.
	if p.Verify != nil {
		out = append(out, "apikey")
	}
	if p.Device != nil {
		out = append(out, "device")
	}
	if p.Kind == mcpKind {
		out = append(out, "mcp")
	}
	// Nothing to connect: a piece that needs no credential, or one whose
	// credential this deployment does not take. The card's note says which.
	if p.Kind == noneKind {
		out = append(out, noneKind)
	}
	return out
}

func providerViewFor(s *cloud.Service[state], ctx context.Context, org string, p *Provider) (providerView, error) {
	// A provider is connected when ANY of its accounts is. The first is the
	// representative the card renders; ListFor is ordered by owner so it is stable.
	conns, err := s.State.store.ListFor(ctx, org, p.ID)
	if err != nil {
		return providerView{}, err
	}
	if len(conns) == 0 {
		v := orgView(p, Connection{}, false)
		return markOrgApp(s, org, p, v), nil
	}
	v := orgView(p, conns[0], true)
	v.Accounts = make([]connectionView, 0, len(conns))
	for _, c := range conns {
		v.Accounts = append(v.Accounts, connectionCard(c))
	}
	return markOrgApp(s, org, p, v), nil
}

func connectionCard(c Connection) connectionView {
	return connectionView{
		Account: c.AccountLabel, ExternalID: c.ExternalID, Label: c.Label,
		Scopes: nonNil(c.Scopes), ConnectedAt: text.Unix(c.ConnectedAt),
		Kind: socialKind(c.BotUserID),
	}
}

// socialKind is personal or page. Anything else stored in the same column — a
// Slack bot user id — stays off the card.
func socialKind(v string) string {
	if v == "personal" || v == "page" {
		return v
	}
	return ""
}

// markOrgApp lights the card when this org sealed its own app, even though the
// deployment environment has none.
func markOrgApp(s *cloud.Service[state], org string, p *Provider, v providerView) providerView {
	if v.Available || p.Authorize == nil {
		return v
	}
	if credsFor(s, org, p).ClientID != "" {
		v.Available = true
		if v.Kind == "" || v.Kind == "credential" {
			v.Kind = "oauth"
		}
	}
	return v
}

// orgView is one org-plane card: the connector, and this org's connection to
// it when it has one.
func orgView(p *Provider, conn Connection, connected bool) providerView {
	v := catalogView(p)
	if connected {
		v.Connected = true
		card := connectionCard(conn)
		v.Connection = &card
	}
	return v
}

// catalogView is what a connector IS, on either plane — everything on its
// card but this org's connection. One derivation, so a connector cannot
// describe itself one way on one plane and another on the other.
func catalogView(p *Provider) providerView {
	v := providerView{
		ID: p.ID, Name: p.Name, Description: p.Description, Category: p.Category,
		Scopes:   nonNil(p.Scopes),
		Modes:    providerModes(p),
		Logo:     logoPath(p),
		Actions:  opNames(p, false),
		Triggers: opNames(p, true),
		Runnable: len(p.Actions) > 0,
		Note:     pieceNote(p),
	}
	if v.Description == "" && p.Piece != nil {
		v.Description = p.Piece.Description
	}
	v.Kind, v.Available = connectMode(p)
	if p.Verify != nil {
		v.Fields = p.Fields
		if len(v.Fields) == 0 {
			v.Fields = []Field{{Name: "token", Label: "API key", Secret: true, Required: true}}
		}
		v.Checks = p.Piece == nil || pieceChecks(p.Piece)
	}
	if v.Kind == "oauth" && !v.Available && p.generated {
		if sp, ok := pieceSpec(p.Piece, firstOAuth(p.Piece)); ok {
			v.App = strings.TrimSuffix(sp.clientIDEnv, "_CLIENT_ID")
		}
	}
	return v
}

// connectMode is how a reader connects p now, and whether it can. A connector
// with a ready OAuth leg is one press; one whose OAuth awaits an app
// registration but takes a pasted credential offers the credential instead,
// so an org is never stuck behind a registration it cannot make.
func connectMode(p *Provider) (string, bool) {
	switch {
	case p.Kind == noneKind:
		return noneKind, p.Configured != nil && p.Configured()
	case p.Scope == userScope && p.Device != nil:
		return "device", true
	case p.Authorize != nil && p.Creds != nil && authorizeReady(p):
		return "oauth", true
	case p.Verify != nil:
		return "credential", p.Scope == userScope || (p.Configured != nil && p.Configured())
	case p.Adopt != nil:
		return "oauth", false
	}
	return "oauth", false
}

// pieceChecks reports whether a piece checks a pasted credential itself.
func pieceChecks(pc *piece.Piece) bool {
	if !pc.Runnable {
		return false
	}
	for _, a := range pc.Auth {
		if a.Validate && a.Type != piece.OAuth2 {
			return true
		}
	}
	return false
}

func firstOAuth(pc *piece.Piece) *piece.Auth {
	for i := range pc.Auth {
		if pc.Auth[i].Type == piece.OAuth2 {
			return &pc.Auth[i]
		}
	}
	return nil
}

func sortedProviderIDs(s *cloud.Service[state]) []string {
	return slices.Sorted(maps.Keys(s.State.providers))
}

// successRedirect 302s to back (or the console's /connectors) with
// connected={provider}&account=<label>.
func successRedirect(s *cloud.Service[state], c *zip.Ctx, back, provider, account string) error {
	return redirect(c, returnURL(s.State.consoleURL, back, "connected", provider, "account", account))
}

// failRedirect 302s to back (or the console's /connectors) with
// error={provider}&reason=<short msg>, and is the ONE place a failed OAuth return
// is recorded. back is "" until the state has proven which flow this is. reason is
// the opaque public label the browser sees; cause (nil when reason is the whole
// truth) is the precise internal detail, which goes ONLY to the log. Logging here
// rather than at each call site is what makes "every failure is recorded" a
// property of the funnel instead of a habit — a silent callback is
// indistinguishable from one that never arrived, and that ambiguity is expensive
// to debug.
func failRedirect(s *cloud.Service[state], c *zip.Ctx, back, provider, reason string, cause error) error {
	s.Log.Warn("oauth callback rejected", "provider", provider, "reason", reason, "cause", cause)
	return redirect(c, returnURL(s.State.consoleURL, back, "error", provider, "reason", reason))
}

// checkedBack is backTo with the refusal logged: a connect that named an address
// off the list still runs, and returns to the console.
func checkedBack(s *cloud.Service[state], raw string) string {
	back := backTo(raw)
	if raw != "" && back == "" {
		s.Log.Warn("oauth return refused; the callback returns to the console", "return", truncate(raw, 200))
	}
	return back
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

// connOwner is the account a connection is keyed by. Only a MultiAccount provider
// gets one; everything else keeps the empty owner its callers resolve.
func connOwner(p *Provider, res *ExchangeResult) string {
	if p == nil || !p.MultiAccount || res == nil {
		return ""
	}
	if p.PerTarget {
		if res.Key != "" {
			return targetLabel(res.Key)
		}
		return targetLabel(res.ExternalID)
	}
	return res.AccountLabel
}

// custodyPath is where one connection's tokens are sealed. A single-account
// provider keeps the network path it has always used. A per-target provider
// adds the account key, so a second account cannot overwrite the first token.
func custodyPath(p *Provider, org, label string) string {
	if p != nil && p.PerTarget && label != "" {
		if path := kms.OrgPath(org, "integrations", p.ID, label); path != "" {
			return path
		}
	}
	id := ""
	if p != nil {
		id = p.ID
	}
	return kmsPath(org, id)
}

// kmsPath is the per-org, per-provider KMS namespace: /orgs/{slug}/integrations/
// {provider}, and "" for an org that names no store. The org segment is derived by
// kms.OrgPath, so it cannot smuggle path structure whatever it is called; provider
// is a fixed registry slug.
func kmsPath(org, provider string) string {
	return kms.OrgPath(org, "integrations", provider)
}

// credsFor is the app a connect or a refresh uses. An organization's own sealed
// app wins over the deployment environment. A network with neither has an empty
// client id and the consent URL is refused.
func credsFor(s *cloud.Service[state], org string, p *Provider) OAuthConfig {
	if s != nil && p != nil {
		if c, ok := orgApp(s.State.kms, org, p.ID); ok {
			return c
		}
	}
	if p != nil && p.Creds != nil {
		return p.Creds()
	}
	return OAuthConfig{}
}

// orgApp reads the id and secret sealed for one org. An id alone is a hit: the
// secret may have been cleared, and falling through to the deployment secret
// would send this org's id with someone else's secret.
func orgApp(k secret.Store, org, provider string) (OAuthConfig, bool) {
	if k == nil || org == "" || provider == "" {
		return OAuthConfig{}, false
	}
	path := kms.OrgPath(org, "social", "apps", provider)
	if path == "" {
		return OAuthConfig{}, false
	}
	id, err := k.GetSecret(context.Background(), kmsRef(path, "client_id"))
	if err != nil && !errors.Is(err, secret.ErrNone) {
		return OAuthConfig{}, false
	}
	sealed, err := k.GetSecret(context.Background(), kmsRef(path, "client_secret"))
	if err != nil && !errors.Is(err, secret.ErrNone) {
		return OAuthConfig{}, false
	}
	if len(id) == 0 && len(sealed) == 0 {
		return OAuthConfig{}, false
	}
	return OAuthConfig{ClientID: string(id), ClientSecret: string(sealed)}, true
}

// userPath is the per-user connector namespace:
// /orgs/{slug}/users/{user}/connectors/{provider}/{label}. Segments are
// pre-validated (validUser/registry slug/validLabel) but the COMBINED path can
// exceed the KMS 253-byte cap (a 128-char user + 64-char label overflows), so the
// full path is checked with kms.ValidSubpath — failure is a ready-made 400
// *zip.HTTPError (client input, never a 503).
func userPath(org, user, provider, label string) (string, error) {
	p := kms.OrgPath(org, "users", user, "connectors", provider, label)
	if p == "" || !kms.ValidSubpath(p) {
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

// kmsGet reads one sealed secret. A secret the store does not hold is an error
// that is cloud.ErrNoSecret, the KMSClient contract on the embedded store and on
// the plane peer alike, so every caller tells "never written" from "could not be
// read" with errors.Is(err, cloud.ErrNoSecret) and never by the error's words.
func kmsGet(s *cloud.Service[state], path, name string) ([]byte, error) {
	return s.State.kms.GetSecret(context.Background(), kmsRef(path, name))
}

func kmsDelete(s *cloud.Service[state], path, name string) error {
	err := s.State.kms.DeleteSecret(context.Background(), kmsRef(path, name))
	if errors.Is(err, secret.ErrNone) {
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

// ── in-process client (mirror agents `var mounted *cloud.Service`) ───────────────
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
	if kms.OrgPath(org) == "" {
		return false
	}
	if _, ok := s.State.providers[provider]; !ok {
		return false
	}
	conns, err := s.State.store.ListFor(ctx, org, provider)
	return err == nil && len(conns) > 0
}

func tokenFor(s *cloud.Service[state], ctx context.Context, org, provider, name string) ([]byte, error) {
	if kms.OrgPath(org) == "" {
		return nil, fmt.Errorf("integrations: org names no store")
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
	p := s.State.providers[provider]
	conn := conns[0]
	if p.PerTarget {
		var perr error
		conn, perr = pickConn(conns, "")
		if perr != nil {
			return nil, perr
		}
	}
	path := custodyPath(p, org, conn.Label)
	if name == accessSecret && p.Refresh != nil && stale(conn) {
		return freshOrg(ctx, s, p, conn)
	}
	return kmsGet(s, path, name)
}

// tokenForTarget is tokenFor for one named account. An empty account is the
// network's only connection. Several connections and no name is a refusal:
// posting must not guess the first row.
func tokenForTarget(s *cloud.Service[state], ctx context.Context, org, provider, account, name string) ([]byte, error) {
	if kms.OrgPath(org) == "" {
		return nil, fmt.Errorf("integrations: org names no store")
	}
	p, ok := s.State.providers[provider]
	if !ok {
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
	conn, err := pickConn(conns, account)
	if err != nil {
		return nil, err
	}
	path := custodyPath(p, org, conn.Label)
	if name == accessSecret && p.Refresh != nil && stale(conn) {
		return freshOrg(ctx, s, p, conn)
	}
	return kmsGet(s, path, name)
}

// pickConn chooses the connection a post or a read named. One connection is
// that connection. A name matches the platform id or the connection key.
func pickConn(conns []Connection, account string) (Connection, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		switch len(conns) {
		case 0:
			return Connection{}, fmt.Errorf("integrations: not connected")
		case 1:
			return conns[0], nil
		default:
			return Connection{}, fmt.Errorf("integrations: name the account; %d are connected", len(conns))
		}
	}
	for _, c := range conns {
		if c.ExternalID == account || c.Label == account {
			return c, nil
		}
	}
	return Connection{}, fmt.Errorf("integrations: account %s is not connected", account)
}

// stale reports whether a connection's access token expires within refreshSkew.
func stale(c Connection) bool {
	return c.ExpiresAt != 0 && time.Until(time.Unix(c.ExpiresAt, 0)) <= refreshSkew
}

// freshOrg trades an org connection's refresh token for a new access token, seals
// the pair again and moves the connection's expiry on, and answers the new token.
// Single-flight per connection, re-reading the row under the lock and adopting a
// rotation another flight finished: a provider that rotates refresh tokens kills
// the credential if the old one is spent twice (refresh.go fresh, the user plane's).
func freshOrg(ctx context.Context, s *cloud.Service[state], p *Provider, conn Connection) ([]byte, error) {
	path := custodyPath(p, conn.Org, conn.Label)
	unlock := s.State.flight.lock("refresh\x00" + path)
	defer unlock()
	cur, found, err := s.State.store.Get(ctx, conn.Org, "", conn.Provider, conn.Label)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("integrations: %s not connected for org", p.ID)
	}
	if !stale(cur) {
		return kmsGet(s, path, accessSecret)
	}
	ref, err := kmsGet(s, path, refreshSecret)
	if err != nil || len(ref) == 0 {
		// Nothing to trade: the stored token is all there is, and the provider
		// says whether it still works.
		return kmsGet(s, path, accessSecret)
	}
	res, err := p.Refresh(ctx, string(ref))
	if err != nil || res == nil || res.Tokens[accessSecret] == "" {
		s.Log.Warn("token refresh failed", "provider", p.ID, "org", cur.Org, "err", err)
		return nil, refreshFailure{cause: err}
	}
	if err := sealTokens(s, path, res.Tokens); err != nil {
		return nil, err
	}
	cur.ExpiresAt = res.ExpiresAt
	if err := s.State.store.Upsert(ctx, cur); err != nil {
		return nil, err
	}
	return []byte(res.Tokens[accessSecret]), nil
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
// NOTE (single contract deviation): the contract names this client `Connection`,
// but Go forbids a func and a type sharing an identifier and `Connection` is the
// domain-noun TYPE (used by SyncHook/WritebackHook, the store, and this return
// value). The accessor is therefore `ConnectionFor` — the idiomatic Go name for
// "the Connection for (org,provider)". The channel calls provider.ConnectionFor.
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
// surface (/v1/provider) never resolves a user-scoped provider and vice
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
func callbackPath(provider string) string { return "/v1/provider/" + provider + "/callback" }

// consoleURL resolves the console origin the callback redirects back to.
// Prefers explicit CLOUD_CONSOLE_URL, then the active brand's customer console
// (e.g. https://platform.hanzo.ai for hanzo, https://console.lux.cloud for lux),
// and falls back to defaultConsoleURL.
func consoleURL() string {
	if v := env.Or(consoleURLEnv, ""); v != "" {
		return strings.TrimRight(v, "/")
	}
	if b := cloud.Brand(); brand.Registered(b) {
		if host := strings.TrimSpace(brand.For(b).Console); host != "" {
			if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
				return strings.TrimRight(host, "/")
			}
			return "https://" + strings.TrimRight(host, "/")
		}
	}
	return defaultConsoleURL
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
	res.Kind = sanitizeMeta(res.Kind)
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

// truncate bounds a value to n bytes and says so, because a silently clipped
// string reads as the whole of it.
func truncate(s string, n int) string {
	if len(s) > n {
		// shorten.To, not s[:n]: a byte slice can cut a multi-byte rune in half and
		// emit invalid UTF-8 into a Slack message. The fleet has one way to bound a
		// length and this is it.
		return text.Cut(s, n) + "…"
	}
	return s
}
