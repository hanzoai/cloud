// connectors.go is the per-org app-connector control plane: OAuth into Slack /
// GitHub / Google, store the token in KMS (never plaintext, never logged), and sync
// external documents INTO the same per-org knowledge store + vector index as manual
// pages. A connector is a pure PRODUCER of framework documents (kb-source) — it
// creates them through framework.Ingest, so the SAME after_save indexing hook that
// serves manual pages indexes them. One knowledge store, many sources, one index.
//
// SECURITY (the tenant + secret boundary):
//   - Every handler resolves its org from principal.Org (a validated principal),
//     NEVER a client field. A caller can only ever connect/sync/disconnect its OWN
//     org, and synced docs are written into that org's store only.
//   - The OAuth token is a RETRIEVABLE secret (it must be presented to the provider
//     API), so it lives in KMS at a deterministic per-org path — never in the
//     kb-connector document, never in a log line. The document holds only the KMS
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
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/apps/provisioning"
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
		app.scope = getenv("KB_GITHUB_SCOPE", "repo read:org")
	case "slack":
		app.authURL = "https://slack.com/oauth/v2/authorize"
		app.tokenURL = "https://slack.com/api/oauth.v2.access"
		app.scope = getenv("KB_SLACK_SCOPE", "channels:history,channels:read,users:read")
	case "google":
		app.authURL = "https://accounts.google.com/o/oauth2/v2/auth"
		app.tokenURL = "https://oauth2.googleapis.com/token"
		app.scope = getenv("KB_GOOGLE_SCOPE", "https://www.googleapis.com/auth/drive.readonly")
	case "notion":
		// Notion is a long-tail connector: OAuth here, PULL via the activepieces JS
		// piece through the auto runner (see sync_piece.go). Notion's OAuth has no
		// space-separated scopes (capabilities are granted at integration setup), so
		// the scope stays empty.
		app.authURL = "https://api.notion.com/v1/oauth/authorize"
		app.tokenURL = "https://api.notion.com/v1/oauth/token"
		app.scope = getenv("KB_NOTION_SCOPE", "")
	}
	return app, true
}

// callbackURL is the deployment's own OAuth redirect URI for a provider. It is
// derived from the deployment Domain (Deps.Domain) — a fixed, server-known origin —
// so it is never client-influenced. Providers must have this registered.
func callbackURL(s *cloud.Service[state], provider string) string {
	base := getenv("KB_OAUTH_REDIRECT_BASE", "https://"+s.Domain)
	return strings.TrimRight(base, "/") + "/v1/kb/connectors/" + provider + "/callback"
}

// kmsRef is the deterministic KMS path holding an org's provider token. It embeds the
// org so one org's token is never at another's path. The org is run through
// provisioning.SanitizeOrg — the codebase's ONE org-slug normalizer (shared with
// S3/KMS/projects) — so the KMS path is INJECTIVE in the owner: distinct owners
// that would fold onto one path ("a b" vs "a_b", or a '/'-bearing org that would
// break the path structure) get a hash-suffixed slug and stay distinct (RED LOW-1).
// The value is written/read through the KMS client and never appears in the document
// or logs.
func kmsRef(org, provider string) string {
	return "kb/connectors/" + provisioning.SanitizeOrg(org) + "/" + provider + "/oauth-token"
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

// connectStartView carries the provider authorize URL for the console to open —
// no server-side redirect, so the BFF stays in control. The state inside it is
// SIGNED and binds the flow to the caller's org, so only THIS org's connection
// can result and the callback recovers the org without trusting a header.
type connectStartView struct {
	// AuthorizeURL is the provider's consent URL, already carrying the signed state.
	AuthorizeURL string `json:"authorizeUrl"`
}

// connectStart returns the provider authorize URL the console sends the user to.
// The state it carries is signed for the CALLER's org, so the token the callback
// stores can only ever land in that org. Nothing is stored yet.
//
// Example: {"provider": "slack"}
// Response: {"authorizeUrl": "https://slack.com/oauth/v2/authorize?client_id=..."}
func (o ops) connectStart(ctx context.Context, in *providerRef) (*connectStartView, error) {
	s := o.s
	org, err := tenant(ctx)
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
	q.Set("redirect_uri", callbackURL(s, provider))
	q.Set("scope", app.scope)
	q.Set("state", state)
	q.Set("response_type", "code")
	if provider == "google" {
		q.Set("access_type", "offline")
		q.Set("prompt", "consent")
	}
	return &connectStartView{AuthorizeURL: app.authURL + "?" + q.Encode()}, nil
}

// callbackQuery is what the provider redirect carries back.
type callbackQuery struct {
	// Provider is the connector id from the path.
	Provider string `json:"provider"`
	// Code is the provider's authorization code, exchanged for a token.
	Code string `json:"code"`
	// State is the value connect signed; the org is recovered from it, never a header.
	State string `json:"state"`
	// Error is set by the provider when the user denied consent.
	Error string `json:"error"`
}

// connectionView reports a connector's state after a connect or disconnect.
type connectionView struct {
	// Provider is the connector id.
	Provider string `json:"provider"`
	// Status is "connected" or "disconnected".
	Status string `json:"status"`
	// Account is the connected account label; present only after a connect.
	Account string `json:"account,omitempty"`
}

// connectCallback completes the OAuth exchange and records the connection.
// The org is recovered from the SIGNED state, not a header, so an attacker cannot
// land their token in a victim org; the token itself goes to KMS, never the
// document or the logs.
//
// Example: {"provider": "slack", "code": "abc123", "state": "signed-state"}
// Response: {"provider": "slack", "status": "connected", "account": "acme"}
func (o ops) connectCallback(ctx context.Context, in *callbackQuery) (*connectionView, error) {
	s := o.s
	provider := in.Provider
	if !providers[provider] {
		return nil, zip.ErrNotFound("unknown connector")
	}
	if errParam := in.Error; errParam != "" {
		return nil, zip.ErrBadRequest("authorization denied: " + errParam)
	}
	code := in.Code
	state := in.State
	if code == "" || state == "" {
		return nil, zip.ErrBadRequest("missing code or state")
	}
	org, err := verifyState(state, provider)
	if err != nil {
		// A bad state is an attack or a stale link — refuse, do not guess an org.
		return nil, zip.ErrForbidden("invalid oauth state")
	}
	app, ok := oauthConfig(provider)
	if !ok {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s connector is not configured", provider)
	}

	token, account, err := exchangeCode(ctx, provider, app, code, callbackURL(s, provider))
	if err != nil {
		s.Log.Warn("kb oauth exchange failed", "provider", provider, "org", org, "err", err)
		return nil, zip.ErrBadRequest("token exchange failed")
	}

	// Store the token in KMS — the ONE place a retrievable secret lives. Never the doc.
	ref := kmsRef(org, provider)
	if s.KMS == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "secret store unavailable")
	}
	if err := s.KMS.PutSecret(ctx, ref, []byte(token)); err != nil {
		s.Log.Warn("kb kms put failed", "provider", provider, "org", org, "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "could not persist connector credential")
	}

	if err := upsertConnector(s, ctx, org, provider, map[string]any{
		"provider": provider,
		"status":   "connected",
		"account":  account,
		"scope":    app.scope,
		"kms_ref":  ref,
		"error":    "",
	}); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "could not record connection")
	}
	return &connectionView{Provider: provider, Status: "connected", Account: account}, nil
}

// connectorView is one provider's state in the caller's org. account, lastSync and
// error are POINTERS because they are carried only when a connector document
// exists: absent means "never connected", an empty string means "connected, with
// nothing recorded there yet". No secret is ever in this shape.
type connectorView struct {
	// Provider is the connector id.
	Provider string `json:"provider"`
	// Configured reports whether this deployment holds OAuth credentials for it.
	Configured bool `json:"configured"`
	// Status is disconnected | connected | syncing | error.
	Status string `json:"status"`
	// DocCount is a live count of this provider's ingested documents in the org.
	DocCount int `json:"docCount"`
	// Kind is "native" (a Go connector) or "piece" (an activepieces connector).
	Kind string `json:"kind"`
	// Account is the connected account label; absent until first connected.
	Account *string `json:"account,omitempty"`
	// LastSync is when the last sync finished; absent until first connected.
	LastSync *string `json:"lastSync,omitempty"`
	// Error is the last sync failure; absent until first connected, empty when clean.
	Error *string `json:"error,omitempty"`
}

// connectorList is the caller org's connector inventory.
type connectorList struct {
	// Connectors is one entry per known provider, connected or not.
	Connectors []connectorView `json:"connectors"`
}

// listConnectors lists every connector with the caller org's real ingested-doc count.
// A provider that is configured but not yet connected appears as "disconnected", so
// the console can offer a Connect button. No secret is ever returned.
//
// Response: {"connectors": [{"provider": "slack", "configured": true, "status": "connected", "docCount": 42, "kind": "native", "account": "acme", "lastSync": "2026-07-29 10:00:00", "error": ""}]}
func (o ops) listConnectors(ctx context.Context, _ *struct{}) (*connectorList, error) {
	org, err := tenant(ctx)
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
				status, account, lastSync, docErr := str(d["status"]), str(d["account"]), str(d["last_sync"]), str(d["error"])
				entry.Status = status
				entry.Account, entry.LastSync, entry.Error = &account, &lastSync, &docErr
			}
		}
		// Real count of this provider's ingested docs in this org.
		if docs, err := framework.Search(ctx, org, DTSource, map[string]string{"provider": provider}, maxSyncDocs); err == nil {
			entry.DocCount = len(docs)
		}
		out = append(out, entry)
	}
	return &connectorList{Connectors: out}, nil
}

// syncView reports how many documents one sync ingested. The pulled documents are
// filed as kb-source pages, which the after_save hook indexes.
type syncView struct {
	// Provider is the connector that was synced.
	Provider string `json:"provider"`
	// Ingested is how many documents this sync filed into the org's knowledge.
	Ingested int `json:"ingested"`
}

// syncConnector pulls a provider's documents into the caller org's knowledge.
// The token is read from KMS and the documents land as kb-source pages the index
// picks up. A provider-side failure answers 502 and the connector is left in
// status "error" with the reason recorded.
//
// Example: {"provider": "slack"}
// Response: {"provider": "slack", "ingested": 42}
func (o ops) syncConnector(ctx context.Context, in *providerRef) (*syncView, error) {
	s := o.s
	org, err := tenant(ctx)
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
	if s.KMS == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "secret store unavailable")
	}
	tokenBytes, err := s.KMS.GetSecret(ctx, kmsRef(org, provider))
	if err != nil || len(tokenBytes) == 0 {
		return nil, zip.ErrBadRequest("connector not connected")
	}

	_ = upsertConnector(s, ctx, org, provider, map[string]any{"provider": provider, "status": "syncing"})

	n, cursor, syncErr := runSync(s, ctx, org, provider, string(tokenBytes))

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
	_ = upsertConnector(s, ctx, org, provider, fields)

	if syncErr != nil {
		s.Log.Warn("kb sync failed", "provider", provider, "org", org, "err", syncErr)
		return nil, zip.Errorf(http.StatusBadGateway, "sync failed: %s", syncErr.Error())
	}
	return &syncView{Provider: provider, Ingested: n}, nil
}

// disconnectConnector revokes a connection and de-indexes what it ingested.
// The KMS credential is tombstoned (overwritten with empty — the KMS client has
// no delete) so a later sync cannot reuse it, this provider's points are removed
// from the org's vector namespace, and the connector document is marked
// disconnected. The ingested kb-source documents stay in the store — they are the
// org's own data, deletable through the framework CRUD surface — but are no
// longer retrievable by search. Everything is org-scoped.
//
// Example: {"provider": "slack"}
// Response: {"provider": "slack", "status": "disconnected"}
func (o ops) disconnectConnector(ctx context.Context, in *providerRef) (*connectionView, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	provider := in.Provider
	if !providers[provider] {
		return nil, zip.ErrNotFound("unknown connector")
	}
	if s.KMS != nil {
		// Tombstone the credential so a later sync cannot reuse it.
		_ = s.KMS.PutSecret(ctx, kmsRef(org, provider), []byte{})
	}
	if err := index().deindexProvider(ctx, org, provider); err != nil {
		s.Log.Warn("kb deindex provider failed", "provider", provider, "org", org, "err", err)
	}
	_ = upsertConnector(s, ctx, org, provider, map[string]any{
		"provider": provider, "status": "disconnected", "kms_ref": "", "cursor": "",
	})
	return &connectionView{Provider: provider, Status: "disconnected"}, nil
}

// upsertConnector creates or updates the org's kb-connector document for a provider
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
		for k, v := range docs[0].Data {
			merged[k] = v
		}
	}
	for k, v := range fields {
		merged[k] = v
	}
	return framework.UpdateData(ctx, org, DTConnector, name, merged)
}
