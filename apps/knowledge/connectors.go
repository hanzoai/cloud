// connectors.go is the per-org app-connector control plane: OAuth into Slack /
// GitHub / Google, store the token in KMS (never plaintext, never logged), and sync
// external documents INTO the same per-org knowledge store + vector index as manual
// pages. A connector is a pure PRODUCER of framework documents (kb.source) — it
// creates them through framework.Ingest, so the SAME after_save indexing hook that
// serves manual pages indexes them. One knowledge store, many sources, one index.
//
// SECURITY (the tenant + secret boundary):
//   - Every handler resolves its org from principal.Org (a validated principal),
//     NEVER a client field. A caller can only ever connect/sync/disconnect its OWN
//     org, and synced docs are written into that org's store only.
//   - The OAuth token is a RETRIEVABLE secret (it must be presented to the provider
//     API), so it lives in KMS at a deterministic per-org path — never in the
//     kb.connector document, never in a log line. The document holds only the KMS
//     PATH (kms_ref) and non-secret connection metadata.
//   - The OAuth `state` is an HMAC over (org|provider|nonce|expiry) keyed by a
//     server secret, so the callback re-derives the org from the STATE it signed —
//     not from a client header and not from the provider — which defeats the
//     login-CSRF / mix-up class (an attacker cannot bind their provider account to a
//     victim org, nor steer a victim's callback to a foreign org).

package knowledge

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// providers is the closed set of supported connector kinds. An unknown provider is
// rejected before any OAuth or store access (fail closed).
//
// github/slack/google are FIRST-PARTY Go connectors (native list+fetch in sync.go).
// notion is the first LONG-TAIL connector: its OAuth lifecycle is identical (same
// HMAC-org-bound state, same KMS token path), but its PULL runs the activepieces JS
// piece through the isolated auto engine runner (pieceSync in sync_piece.go) rather
// than native Go — proving the hybrid model. Every ~280 activepieces app plugs in the
// same way (add it here + one pieceConnectors entry); the ingestion path is unchanged
// (framework.Ingest), so a JS-sourced doc lands in the SAME per-org store + index as a
// Go-sourced one.
var providers = map[string]bool{"github": true, "slack": true, "google": true, "notion": true}

// oauthApp is a provider's OAuth application config, resolved from env at use. The
// client secret is read from env (KMS-injected on the deployment) and never logged.
// An unconfigured provider yields a clear "not configured" rather than a broken flow.
type oauthApp struct {
	clientID     string
	clientSecret string
	authURL      string
	tokenURL     string
	scope        string
}

// oauthConfig returns the provider's OAuth app config from env, or ok=false when
// the provider isn't provisioned for this deployment. Env keys are
// KB_<PROVIDER>_CLIENT_ID / _CLIENT_SECRET (KMS-injected). The authorize/token URLs
// and default scopes are the providers' standard endpoints.
func oauthConfig(provider string) (oauthApp, bool) {
	up := strings.ToUpper(provider)
	id := os.Getenv("KB_" + up + "_CLIENT_ID")
	secret := os.Getenv("KB_" + up + "_CLIENT_SECRET")
	if id == "" || secret == "" {
		return oauthApp{}, false
	}
	app := oauthApp{clientID: id, clientSecret: secret}
	switch provider {
	case "github":
		app.authURL = "https://github.com/login/oauth/authorize"
		app.tokenURL = "https://github.com/login/oauth/access_token"
		app.scope = environ.Or("KB_GITHUB_SCOPE", "repo read:org")
	case "slack":
		app.authURL = "https://slack.com/oauth/v2/authorize"
		app.tokenURL = "https://slack.com/api/oauth.v2.access"
		app.scope = environ.Or("KB_SLACK_SCOPE", "channels:history,channels:read,users:read")
	case "google":
		app.authURL = "https://accounts.google.com/o/oauth2/v2/auth"
		app.tokenURL = "https://oauth2.googleapis.com/token"
		app.scope = environ.Or("KB_GOOGLE_SCOPE", "https://www.googleapis.com/auth/drive.readonly")
	case "notion":
		// Notion is a long-tail connector: OAuth here, PULL via the activepieces JS
		// piece through the auto runner (see sync_piece.go). Notion's OAuth has no
		// space-separated scopes (capabilities are granted at integration setup), so
		// the scope stays empty.
		app.authURL = "https://api.notion.com/v1/oauth/authorize"
		app.tokenURL = "https://api.notion.com/v1/oauth/token"
		app.scope = environ.Or("KB_NOTION_SCOPE", "")
	}
	return app, true
}

// callbackURL is the deployment's own OAuth redirect URI for a provider. It is
// derived from the deployment Domain (Deps.Domain) — a fixed, server-known origin —
// so it is never client-influenced. Providers must have this registered.
func callbackURL(s *cloud.Service[state], provider string) string {
	base := environ.Or("KB_OAUTH_REDIRECT_BASE", "https://"+s.Domain)
	return strings.TrimRight(base, "/") + "/v1/knowledge/connectors/" + provider + "/callback"
}

// kmsRef is the deterministic KMS path holding an org's provider token. It embeds the
// org so one org's token is never at another's path. The org is run through
// namespace.Sanitize — the codebase's ONE org-slug normalizer (shared with
// S3/KMS/projects) — so the KMS path is INJECTIVE in the owner: distinct owners
// that would fold onto one path ("a b" vs "a_b", or a '/'-bearing org that would
// break the path structure) get a hash-suffixed slug and stay distinct (RED LOW-1).
// The value is written/read through the KMS client and never appears in the document
// or logs.
func kmsRef(org, provider string) string {
	return "kb/connectors/" + namespace.Sanitize(org) + "/" + provider + "/oauth-token"
}

// ---- OAuth state (org-bound, signed, expiring) ----

// stateSecret is the HMAC key for OAuth state, from env (KMS-injected). Absent, the
// connector flow is disabled (fail closed) rather than issuing forgeable state.
func stateSecret() []byte { return []byte(os.Getenv("KB_OAUTH_STATE_SECRET")) }

// signState mints an opaque state binding (org, provider) with a short expiry,
// authenticated by HMAC-SHA256. The callback verifies the MAC and expiry and
// recovers the org from the SIGNED payload — so the org a callback acts on is the
// one THIS server bound at connect time, not anything the client or provider sends.
func signState(org, provider string) (string, error) {
	secret := stateSecret()
	if len(secret) == 0 {
		return "", fmt.Errorf("KB_OAUTH_STATE_SECRET not set")
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	exp := time.Now().Add(10 * time.Minute).Unix()
	payload := fmt.Sprintf("%s|%s|%s|%d", org, provider, hex.EncodeToString(nonce), exp)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// verifyState checks the MAC + expiry and returns the (org, provider) the state was
// signed for. A tampered, expired, or foreign-provider state is rejected — the
// callback then refuses, so an attacker cannot forge a state that binds their token
// to a victim org.
func verifyState(state, provider string) (org string, err error) {
	secret := stateSecret()
	if len(secret) == 0 {
		return "", fmt.Errorf("KB_OAUTH_STATE_SECRET not set")
	}
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("malformed state")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("malformed state payload")
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("malformed state sig")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	wantSig := mac.Sum(nil)
	if subtle.ConstantTimeCompare(gotSig, wantSig) != 1 {
		return "", fmt.Errorf("state signature mismatch")
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 4 {
		return "", fmt.Errorf("malformed state fields")
	}
	if fields[1] != provider {
		return "", fmt.Errorf("state provider mismatch")
	}
	exp, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", fmt.Errorf("state expired")
	}
	if fields[0] == "" {
		return "", fmt.Errorf("state missing org")
	}
	return fields[0], nil
}

// ---- handlers ----

// providerIn addresses ONE connector by its provider. A GET and a DELETE take
// their input from the URL and carry no request body, so this is the whole input.
type providerIn struct {
	// Provider is the connector to act on: github, slack, google or notion.
	Provider string `json:"provider"`
}

// kbAuthorizeOut is the URL the console opens to start an OAuth connection.
type kbAuthorizeOut struct {
	// AuthorizeURL is the provider's authorize endpoint with an org-bound signed state.
	AuthorizeURL string `json:"authorizeUrl"`
}

// StartConnectorOAuth returns the provider authorize URL the console opens to
// connect this org's account. There is no server-side redirect — the console
// stays in control of the navigation. The URL carries a state this server SIGNED
// over the caller's validated org, so the connection the callback completes can
// only ever land in that org.
func (o ops) connectStart(ctx context.Context, in *providerIn) (*kbAuthorizeOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	provider := in.Provider
	if !providers[provider] {
		return nil, zip.ErrNotFound("unknown connector")
	}
	app, ok := oauthConfig(provider)
	if !ok {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s connector is not configured", provider)
	}
	state, err := signState(org, provider)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "connector state unavailable")
	}
	q := url.Values{}
	q.Set("client_id", app.clientID)
	q.Set("redirect_uri", callbackURL(o.s, provider))
	q.Set("scope", app.scope)
	q.Set("state", state)
	q.Set("response_type", "code")
	if provider == "google" {
		q.Set("access_type", "offline")
		q.Set("prompt", "consent")
	}
	return &kbAuthorizeOut{AuthorizeURL: app.authURL + "?" + q.Encode()}, nil
}

// callbackIn is what the PROVIDER sends back: the path names which provider, and
// the query carries its grant. None of it is trusted as an identity — the org
// comes from the state this server signed.
type callbackIn struct {
	// Provider is the connector completing its flow, from the path.
	Provider string `json:"provider"`
	// Code is the provider's authorization code, exchanged for a token.
	Code string `json:"code"`
	// State is the org-bound value this server signed at connect time.
	State string `json:"state"`
	// Error is the provider's denial reason when the user refused consent.
	Error string `json:"error"`
}

// connectionOut is a connector's state after a lifecycle call.
type connectionOut struct {
	// Provider is the connector this answer is about.
	Provider string `json:"provider"`
	// Status is the connection state: connected or disconnected.
	Status string `json:"status"`
	// Account names the connected external account, when the provider reports one.
	Account string `json:"account,omitempty"`
}

// CompleteConnectorOAuth finishes an OAuth connection: it exchanges the
// provider's code for a token, seals that token in KMS, and records the
// connection. THE ORG COMES FROM THE SIGNED STATE, not from a header and not from
// the provider, so an attacker cannot bind their own account to someone else's
// org — a tampered, expired or foreign-provider state is refused outright. The
// token itself is never returned, never written into the document, and never
// logged; the document holds only its KMS path.
func (o ops) connectCallback(ctx context.Context, in *callbackIn) (*connectionOut, error) {
	provider := in.Provider
	if !providers[provider] {
		return nil, zip.ErrNotFound("unknown connector")
	}
	if in.Error != "" {
		return nil, zip.ErrBadRequest("authorization denied: " + in.Error)
	}
	if in.Code == "" || in.State == "" {
		return nil, zip.ErrBadRequest("missing code or state")
	}
	org, err := verifyState(in.State, provider)
	if err != nil {
		// A bad state is an attack or a stale link — refuse, do not guess an org.
		return nil, zip.ErrForbidden("invalid oauth state")
	}
	app, ok := oauthConfig(provider)
	if !ok {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s connector is not configured", provider)
	}

	token, account, err := exchangeCode(ctx, provider, app, in.Code, callbackURL(o.s, provider))
	if err != nil {
		o.s.Log.Warn("kb oauth exchange failed", "provider", provider, "org", org, "err", err)
		return nil, zip.ErrBadRequest("token exchange failed")
	}

	// Store the token in KMS — the ONE place a retrievable secret lives. Never the doc.
	ref := kmsRef(org, provider)
	if o.s.KMS == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "secret store unavailable")
	}
	if err := o.s.KMS.PutSecret(ctx, ref, []byte(token)); err != nil {
		o.s.Log.Warn("kb kms put failed", "provider", provider, "org", org, "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "could not persist connector credential")
	}

	if err := upsertConnector(o.s, ctx, org, provider, map[string]any{
		"provider": provider,
		"status":   "connected",
		"account":  account,
		"scope":    app.scope,
		"kms_ref":  ref,
		"error":    "",
	}); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "could not record connection")
	}
	return &connectionOut{Provider: provider, Status: "connected", Account: account}, nil
}

// connectorView is one connector row. account, lastSync and error are POINTERS
// because their PRESENCE is the wire: a provider this org has never connected
// carries none of them at all, and a connected one carries them even when empty.
// A plain string with omitempty would drop an empty-but-present field and change
// the shape the console reads.
type connectorView struct {
	// Provider is the connector's id.
	Provider string `json:"provider"`
	// Configured is true when this deployment holds OAuth credentials for the provider.
	Configured bool `json:"configured"`
	// Status is connected, disconnected, syncing or error.
	Status string `json:"status"`
	// DocCount is the live count of this provider's documents in the org's store.
	DocCount int `json:"docCount"`
	// Kind is "native" for a first-party Go connector, "piece" for a long-tail one.
	Kind string `json:"kind"`
	// Account names the connected external account. Absent until the org connects.
	Account *string `json:"account,omitempty"`
	// LastSync is when the last pull finished. Absent until the org connects.
	LastSync *string `json:"lastSync,omitempty"`
	// Error is the last sync failure, if any. Absent until the org connects.
	Error *string `json:"error,omitempty"`
}

// kbConnectorsOut is the org's connector list.
type kbConnectorsOut struct {
	// Connectors is every supported provider with this org's connection state.
	Connectors []connectorView `json:"connectors"`
}

// ListConnectors returns every supported knowledge connector with THIS org's
// connection state and the REAL number of documents each has ingested into the
// org's store. A provider that is configured for the deployment but not yet
// connected appears as disconnected, so the console can offer a Connect button.
// No secret is ever returned.
func (o ops) listConnectors(ctx context.Context, _ *cloud.Unit) (*kbConnectorsOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]connectorView, 0, len(providers))
	for provider := range providers {
		_, configured := oauthConfig(provider)
		entry := connectorView{
			Provider:   provider,
			Configured: configured,
			Status:     "disconnected",
			DocCount:   0,
			Kind:       kindOf(provider), // "native" | "piece" — one list, badged
		}
		if name, _ := framework.FindByField(ctx, org, DTConnector, "provider", provider); name != "" {
			if docs, err := framework.Search(ctx, org, DTConnector, map[string]string{"name": name}, 1); err == nil && len(docs) == 1 {
				d := docs[0].Data
				entry.Status = str(d["status"])
				entry.Account = ptr(str(d["account"]))
				entry.LastSync = ptr(str(d["last_sync"]))
				entry.Error = ptr(str(d["error"]))
			}
		}
		// Real count of this provider's ingested docs in this org.
		if docs, err := framework.Search(ctx, org, DTSource, map[string]string{"provider": provider}, maxSyncDocs); err == nil {
			entry.DocCount = len(docs)
		}
		out = append(out, entry)
	}
	return &kbConnectorsOut{Connectors: out}, nil
}

// ptr keeps a field's PRESENCE distinct from its emptiness on the wire.
func ptr(s string) *string { return &s }

// kbSyncOut reports what one pull ingested.
type kbSyncOut struct {
	// Provider is the connector that was pulled.
	Provider string `json:"provider"`
	// Ingested is how many documents landed in the org's knowledge store.
	Ingested int `json:"ingested"`
}

// SyncConnector pulls the provider's documents for the caller's org and files
// them as knowledge sources, which the store's own hook then indexes — so a
// synced document is retrievable exactly like a hand-written page. The org is the
// validated tenant and the credential is read from KMS, so an org can only ever
// sync its own connection. A provider failure is reported honestly (502) and
// recorded on the connector rather than silently swallowed.
func (o ops) syncConnector(ctx context.Context, in *providerIn) (*kbSyncOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	provider := in.Provider
	if !providers[provider] {
		return nil, zip.ErrNotFound("unknown connector")
	}
	if !framework.Installed(ctx, org, DTSource) {
		return nil, zip.ErrBadRequest("install the kb module first (POST /v1/framework/modules/kb/install)")
	}
	if o.s.KMS == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "secret store unavailable")
	}
	tokenBytes, err := o.s.KMS.GetSecret(ctx, kmsRef(org, provider))
	if err != nil || len(tokenBytes) == 0 {
		return nil, zip.ErrBadRequest("connector not connected")
	}

	_ = upsertConnector(o.s, ctx, org, provider, map[string]any{"provider": provider, "status": "syncing"})

	n, cursor, syncErr := runSync(o.s, ctx, org, provider, string(tokenBytes))

	fields := map[string]any{
		"provider":  provider,
		"status":    "connected",
		"last_sync": time.Now().UTC().Format("2006-01-02 15:04:05"),
		"cursor":    cursor,
		"doc_count": n,
		"error":     "",
	}
	if syncErr != nil {
		fields["status"] = "error"
		fields["error"] = truncate([]byte(syncErr.Error()), 200)
	}
	_ = upsertConnector(o.s, ctx, org, provider, fields)

	if syncErr != nil {
		o.s.Log.Warn("kb sync failed", "provider", provider, "org", org, "err", syncErr)
		return nil, zip.Errorf(http.StatusBadGateway, "sync failed: %s", syncErr.Error())
	}
	return &kbSyncOut{Provider: provider, Ingested: n}, nil
}

// DisconnectConnector revokes a connection: it tombstones the stored credential
// so a later sync cannot reuse it, purges this provider's points from the org's
// vector namespace, and marks the connector disconnected. The documents already
// ingested stay in the org's store — they are the org's own data — but stop being
// retrievable by search; a caller deletes them through the document surface.
func (o ops) disconnectConnector(ctx context.Context, in *providerIn) (*connectionOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	provider := in.Provider
	if !providers[provider] {
		return nil, zip.ErrNotFound("unknown connector")
	}
	if o.s.KMS != nil {
		// Tombstone the credential so a later sync cannot reuse it.
		_ = o.s.KMS.PutSecret(ctx, kmsRef(org, provider), []byte{})
	}
	if err := index().deindexProvider(ctx, org, provider); err != nil {
		o.s.Log.Warn("kb deindex provider failed", "provider", provider, "org", org, "err", err)
	}
	_ = upsertConnector(o.s, ctx, org, provider, map[string]any{
		"provider": provider, "status": "disconnected", "kms_ref": "", "cursor": "",
	})
	return &connectionOut{Provider: provider, Status: "disconnected"}, nil
}

// upsertConnector creates or updates the org's kb.connector document for a provider
// (one per provider per org, named by provider). It merges the given fields onto the
// existing document so a partial status update (e.g. "syncing") doesn't drop the
// account/kms_ref. Uses the framework in-process API so the write is validated and
// org-scoped exactly like an HTTP write.
func upsertConnector(s *cloud.Service[state], ctx context.Context, org, provider string, fields map[string]any) error {
	name, err := framework.FindByField(ctx, org, DTConnector, "provider", provider)
	if err != nil {
		return err
	}
	if name == "" {
		_, err := framework.Ingest(ctx, org, DTConnector, fields, "")
		return err
	}
	// Merge onto existing data so we never blank fields the caller didn't set.
	docs, err := framework.Search(ctx, org, DTConnector, map[string]string{"name": name}, 1)
	if err != nil {
		return err
	}
	merged := map[string]any{}
	if len(docs) == 1 {
		maps.Copy(merged, docs[0].Data)
	}
	maps.Copy(merged, fields)
	return framework.UpdateData(ctx, org, DTConnector, name, merged)
}
