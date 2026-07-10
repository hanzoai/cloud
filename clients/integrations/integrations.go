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
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/kms"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
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

	// Configured reports whether the provider's APP creds are present in ENV.
	// When false: available=false in the card, and connect/callback fail closed
	// with an honest 503 / failure redirect (never a dead-end, never a fake OK).
	Configured func() bool
	// Creds resolves the APP creds from ENV. Called only when Configured is true.
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

// svc is the mounted subsystem. mounted is the in-process seam other subsystems
// (the bridge) reach through the package funcs at the bottom of this file.
type svc struct {
	store      *Store
	kms        *kms.Client // type-asserted from deps.KMS; nil ⇒ secret ops fail closed
	domain     string      // deps.Domain, e.g. api.hanzo.ai — builds the redirect_uri
	consoleURL string      // where the callback 302s the user back to
	stateKey   []byte      // HMAC-SHA256 key for the CSRF/org-binding state
	providers  map[string]*Provider
	log        luxlog.Logger
}

var mounted *svc

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

// Mount wires /v1/integrations/* onto app.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("integrations.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("integrations.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "integrations")
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
	// Fail LOUD at boot if a provider's RedirectPath is not the path the generic
	// dispatcher actually serves — otherwise its OAuth callback would 404 silently
	// and the connect flow would dead-end. One route, one truth.
	for id, p := range providers {
		if want := callbackPath(id); p.RedirectPath != want {
			_ = store.Close()
			return fmt.Errorf("integrations.Mount: provider %q RedirectPath %q must equal %q", id, p.RedirectPath, want)
		}
	}

	s := &svc{
		store:      store,
		kms:        kc,
		domain:     strings.TrimSpace(deps.Domain),
		consoleURL: consoleURL(),
		stateKey:   resolveStateKey(os.Getenv(stateKeyEnv), log),
		providers:  providers,
		log:        log,
	}
	mounted = s

	app.Get("/v1/integrations", s.list)
	// Slack agent bridge (clients/integrations/slack_events.go + slack_link.go).
	// These LITERAL paths are registered BEFORE the /:provider wildcards so they
	// win under Fiber's registration-order matching and a later `:provider`-shaped
	// change can never shadow them (same discipline as clients/agents' static-
	// before-:ref rule). They are PUBLIC at the JWT layer — reached like
	// /:provider/callback (IdentityMiddleware only POPULATES a principal, never
	// rejects; DefaultPrice returns 0 so BillingGate passes through) — because
	// their auth is done INSIDE the handler: HMAC-SHA256 over the raw body for the
	// events/commands webhooks, and signed __Host- cookie + state for the link legs.
	// They must NOT be placed behind any principal/tenant gate.
	app.Post("/v1/integrations/slack/events", s.slackEvents)
	app.Post("/v1/integrations/slack/commands", s.slackCommands)
	app.Get("/v1/integrations/slack/link", s.slackLink)
	app.Get("/v1/integrations/slack/link/slack", s.slackLinkSlack)
	app.Get("/v1/integrations/slack/link/callback", s.slackLinkCallback)
	app.Get("/v1/integrations/:provider", s.get)
	app.Post("/v1/integrations/:provider/connect", s.connect)
	// PUBLIC, state-authed. RedirectPath == this path for every provider (asserted
	// above), so this single generic route serves every provider's OAuth callback.
	app.Get("/v1/integrations/:provider/callback", s.callback)
	app.Post("/v1/integrations/:provider/disconnect", s.disconnect)

	log.Info(
		"integrations mounted",
		"providers", len(s.providers),
		"kmsReady", s.kmsReady(),
		"domain", s.domain,
		"console", s.consoleURL,
		"brand", deps.Brand,
	)
	return nil
}

func init() {
	// Order 137: after security (136), before functions/AI (150). Registered WITH
	// a shutdown so the per-org store is closed on graceful stop.
	cloud.RegisterWithShutdown("integrations", 137, cloud.Typed(Mount), Shutdown)
}

// Shutdown closes the store. Idempotent — safe when nothing is mounted.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	var err error
	if mounted.store != nil {
		err = mounted.store.Close()
	}
	mounted = nil
	return err
}

// snapshotRegistry copies the global registry into a per-mount map so a mounted
// svc reads a stable set (and a test can construct one deterministically) without
// aliasing package state.
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
func (s *svc) list(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	ids := s.sortedProviderIDs()
	out := make([]providerView, 0, len(ids))
	for _, id := range ids {
		v, err := s.providerView(c.Context(), org, s.providers[id])
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
		}
		out = append(out, v)
	}
	return c.JSON(http.StatusOK, map[string]any{"providers": out})
}

// get returns one provider (404 for an unknown id) with this org's status.
func (s *svc) get(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	p, ok := s.providers[providerParam(c)]
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}
	v, err := s.providerView(c.Context(), org, p)
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
func (s *svc) connect(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required to connect an integration")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	p, ok := s.providers[providerParam(c)]
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}
	if !p.Configured() {
		return zip.Errorf(http.StatusServiceUnavailable, "%s integration is not configured on this deployment", p.ID)
	}
	if !s.kmsReady() {
		return zip.Errorf(http.StatusServiceUnavailable, "%s", kms.ErrMasterKeyMissing.Error())
	}

	// Opportunistic GC of expired nonces (best-effort; never fails the request).
	if _, err := s.store.GCNonces(c.Context(), staleNonceCutoff()); err != nil {
		s.log.Warn("nonce gc", "err", err)
	}

	nonce, err := genToken()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	if err := s.store.PutNonce(c.Context(), nonce, org, p.ID); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "nonce: %v", err)
	}
	state, err := s.sign(org, p.ID, nonce)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "state: %v", err)
	}
	authorizeURL, err := p.Authorize(p.Creds(), s.redirectURI(p), state)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "authorize: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"authorizeUrl": authorizeURL})
}

// callback is the PUBLIC, state-authed OAuth return. It ALWAYS 302s the user back
// to the console (success or a labeled failure) — never a raw JSON dead-end. The
// org comes ONLY from the signed, single-use state; no header is trusted here.
func (s *svc) callback(c *zip.Ctx) error {
	pid := providerParam(c)
	p, ok := s.providers[pid]
	if !ok {
		return s.failRedirect(c, pid, "unknown provider")
	}

	payload, err := s.verify(c.Query("state"), p.ID)
	if err != nil {
		return s.failRedirect(c, p.ID, "invalid state")
	}
	// Consume the single-use nonce, bound to (org,provider). Burned BEFORE the
	// exchange so one state = one attempt: a replay (or a slow-flow retry) finds
	// zero rows and fails here, never double-exchanging.
	consumed, err := s.store.ConsumeNonce(c.Context(), payload.Nonce, payload.Org, p.ID)
	if err != nil {
		return s.failRedirect(c, p.ID, "state error")
	}
	if !consumed {
		return s.failRedirect(c, p.ID, "state already used or expired")
	}
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		return s.failRedirect(c, p.ID, "authorization denied")
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		return s.failRedirect(c, p.ID, "missing authorization code")
	}
	if len(code) > maxCodeLen {
		return s.failRedirect(c, p.ID, "authorization code too large")
	}
	if !p.Configured() {
		return s.failRedirect(c, p.ID, "provider not configured")
	}
	if !s.kmsReady() {
		return s.failRedirect(c, p.ID, "secret store unavailable")
	}

	res, err := p.Exchange(c.Context(), p.Creds(), s.redirectURI(p), code)
	if err != nil || res == nil {
		s.log.Warn("oauth exchange failed", "provider", p.ID, "org", payload.Org, "err", err)
		return s.failRedirect(c, p.ID, "token exchange failed")
	}
	// Harden the provider-supplied NON-secret metadata at the framework ingest
	// boundary — ONE place, every provider — before it is logged, stored, or
	// reflected in the redirect. Strips control chars (kills log-line/separator
	// injection via e.g. a crafted Slack workspace name) and bounds length. Secret
	// token VALUES are never passed through here; they go straight to the KMS seal.
	res.ExternalID = sanitizeMeta(res.ExternalID)
	res.AccountLabel = sanitizeMeta(res.AccountLabel)
	res.BotUserID = sanitizeMeta(res.BotUserID)
	res.Scopes = sanitizeScopes(res.Scopes)
	// Seal every returned token into the org's KMS namespace BEFORE writing the
	// connection row, so a KMS failure leaves NO half-connected state advertising a
	// token that was never stored.
	for name, value := range res.Tokens {
		if err := s.kmsPut(payload.Org, p.ID, name, []byte(value)); err != nil {
			s.log.Warn("kms seal failed", "provider", p.ID, "org", payload.Org, "secret", name, "err", err)
			return s.failRedirect(c, p.ID, "secret custody failed")
		}
	}
	conn := Connection{
		Org:          payload.Org,
		Provider:     p.ID,
		ExternalID:   res.ExternalID,
		AccountLabel: res.AccountLabel,
		BotUserID:    res.BotUserID,
		Scopes:       res.Scopes,
	}
	if err := s.store.Upsert(c.Context(), conn); err != nil {
		s.log.Warn("connection upsert failed", "provider", p.ID, "org", payload.Org, "err", err)
		return s.failRedirect(c, p.ID, "persist failed")
	}
	s.log.Info("integration connected", "provider", p.ID, "org", payload.Org, "account", res.AccountLabel, "externalId", res.ExternalID)
	return s.successRedirect(c, p.ID, res.AccountLabel)
}

// disconnect revokes (best-effort) and forgets an org's connection: it deletes
// every custodied KMS secret and the connection row. Idempotent — disconnecting a
// provider that was never connected still returns {disconnected:true}.
func (s *svc) disconnect(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required to disconnect an integration")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	p, ok := s.providers[providerParam(c)]
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}

	_, found, err := s.store.Get(c.Context(), org, p.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}

	// Best-effort provider-side revoke using the primary custodied secret. Never
	// fails the disconnect: local forgetting is authoritative for the tenant.
	if found && p.Revoke != nil && len(p.Secrets) > 0 && s.kmsReady() && p.Configured() {
		if tok, gerr := s.kmsGet(org, p.ID, p.Secrets[0]); gerr == nil {
			if rerr := p.Revoke(c.Context(), p.Creds(), string(tok)); rerr != nil {
				s.log.Warn("provider revoke failed (continuing)", "provider", p.ID, "org", org, "err", rerr)
			}
		}
	}
	// Delete every custodied secret from KMS (ignore not-found — idempotent).
	if s.kms != nil {
		for _, name := range p.Secrets {
			if derr := s.kmsDelete(org, p.ID, name); derr != nil {
				s.log.Warn("kms delete failed (continuing)", "provider", p.ID, "org", org, "secret", name, "err", derr)
			}
		}
	}
	if _, derr := s.store.Delete(c.Context(), org, p.ID); derr != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", derr)
	}
	return c.JSON(http.StatusOK, map[string]any{"disconnected": true})
}

// ── view + redirect builders ───────────────────────────────────────────────────

// providerView renders a provider's card for an org, folding in the org's live
// connection status.
func (s *svc) providerView(ctx context.Context, org string, p *Provider) (providerView, error) {
	v := providerView{
		ID: p.ID, Name: p.Name, Description: p.Description, Category: p.Category,
		Available: p.Configured(),
	}
	conn, found, err := s.store.Get(ctx, org, p.ID)
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

func (s *svc) sortedProviderIDs() []string {
	ids := make([]string, 0, len(s.providers))
	for id := range s.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// successRedirect 302s to {console}/integrations?connected={provider}&account=<label>.
func (s *svc) successRedirect(c *zip.Ctx, provider, account string) error {
	return redirect(c, consoleRedirectURL(s.consoleURL, "connected", provider, "account", account))
}

// failRedirect 302s to {console}/integrations?error={provider}&reason=<short msg>.
func (s *svc) failRedirect(c *zip.Ctx, provider, reason string) error {
	return redirect(c, consoleRedirectURL(s.consoleURL, "error", provider, "reason", reason))
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
func (s *svc) redirectURI(p *Provider) string {
	return "https://" + s.domain + p.RedirectPath
}

// ── KMS custody (per-org, sealed) ──────────────────────────────────────────────

func (s *svc) kmsReady() bool { return s.kms != nil && s.kms.Ready() }

// kmsPath is the per-org, per-provider KMS namespace: /orgs/{org}/integrations/{provider}.
// org is validOrg-checked at every entry point, so it can never smuggle path
// structure; provider is a fixed registry slug.
func kmsPath(org, provider string) string {
	return "/orgs/" + org + "/integrations/" + provider
}

func (s *svc) kmsPut(org, provider, name string, value []byte) error {
	return s.kms.Put(kmsPath(org, provider), name, kmsEnv, value)
}

func (s *svc) kmsGet(org, provider, name string) ([]byte, error) {
	return s.kms.Get(kmsPath(org, provider), name, kmsEnv)
}

func (s *svc) kmsDelete(org, provider, name string) error {
	err := s.kms.Delete(kmsPath(org, provider), name, kmsEnv)
	if errors.Is(err, kms.ErrSecretNotFound) {
		return nil // idempotent — deleting an absent secret is not an error
	}
	return err
}

// ── in-process seam (mirror agents `var mounted *svc`) ─────────────────────────
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
	return mounted.tokenFor(ctx, org, provider, name)
}

func (s *svc) tokenFor(ctx context.Context, org, provider, name string) ([]byte, error) {
	if !validOrg(org) {
		return nil, fmt.Errorf("integrations: invalid org")
	}
	if _, ok := s.providers[provider]; !ok {
		return nil, fmt.Errorf("integrations: unknown provider %q", provider)
	}
	_, found, err := s.store.Get(ctx, org, provider)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("integrations: %s not connected for org", provider)
	}
	if !s.kmsReady() {
		return nil, kms.ErrMasterKeyMissing
	}
	return s.kmsGet(org, provider, name)
}

// OrgForExternalID resolves a provider account id (Slack team_id / GitHub
// installation_id) back to the org that connected it. Used by inbound provider
// events (which carry the external id, not an org) to find the tenant.
func OrgForExternalID(provider, externalID string) (string, bool) {
	if mounted == nil {
		return "", false
	}
	org, ok, err := mounted.store.ResolveOrgByExternalID(context.Background(), provider, externalID)
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
	conn, ok, err := mounted.store.Get(context.Background(), org, provider)
	if err != nil || !ok {
		return Connection{}, false
	}
	return conn, true
}

// ── helpers ────────────────────────────────────────────────────────────────────

func providerParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("provider")) }

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
