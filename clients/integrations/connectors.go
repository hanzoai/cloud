// connectors.go holds the per-USER admission handlers of the unified
// /v1/connectors plane — device sign-in, credential intake, token custody exit,
// refresh, drop. They are ROUTED by connectorRoutes in extension.go (the ONE
// registration for every kind/scope); this file is the user-plane (scope=user)
// half of the custody logic, sibling to the org-plane (scope=org) handlers in
// integrations.go. Same registry, same store file, same KMS client; only the
// custody key differs (org,user,provider,label → userPath).
//
// TENANTING. Every read/write is bound org=? AND user=? — the (org,user) pair
// from caller() IS the row key, so another user's connector id is simply "no
// row" → 404. No admin gate: a user owns their own connectors.
//
// CUSTODY. id = provider + ":" + label. Every intake path verifies the
// credential live BEFORE anything is stored (saveUser: sanitize → seal-before-
// row → upsert). No secret ever appears in a row, response, or log line except
// GET /:id/token's body — the ONE custody exit, readable only by the same
// validated (org,user), and gated on enablement. Device pending state lives in
// the cek-encrypted grants table (see the grants DDL comment in store.go).
package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/kms"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// ── identity + id helpers ──────────────────────────────────────────────────────

// caller resolves the validated (org,user) pair every handler is scoped by.
// The user is CLONED: c.User() is a zero-copy view into the reused fasthttp
// request buffer, and both values key rows/KMS paths that outlive the request —
// the same reason principal.Org clones. err is a ready-made *zip.HTTPError:
// missing identity is exactly 403 (principal.Org fails at Validated before any
// user validation runs).
func caller(c *zip.Ctx) (org, user string, err error) {
	org, ok := principal.Org(c)
	if !ok {
		return "", "", zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return "", "", zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	user = strings.Clone(strings.TrimSpace(c.User()))
	if !validUser(user) {
		return "", "", zip.ErrBadRequest("invalid user id")
	}
	return org, user, nil
}

// connID is the client-facing connector id: provider + ":" + label
// ("openai:default" — the auth-profile-id shape). ':' is a legal URL pchar;
// validLabel forbids it and provider slugs never contain it, so the id splits
// unambiguously both ways.
func connID(provider, label string) string { return provider + ":" + label }

func splitID(id string) (provider, label string, ok bool) {
	provider, label, ok = strings.Cut(id, ":")
	if provider == "" || label == "" {
		return "", "", false
	}
	return provider, label, true
}

// labelIn normalizes the client-chosen label: empty means defaultLabel, so a
// user's first connection needs no naming decision.
func labelIn(raw string) (string, error) {
	l := strings.TrimSpace(raw)
	if l == "" {
		return defaultLabel, nil
	}
	if !validLabel(l) {
		return "", zip.ErrBadRequest("label must be 1-64 of [A-Za-z0-9._-]")
	}
	return l, nil
}

// room enforces maxConnectors per (org,user,provider) at INTAKE time (device
// start / credential), BEFORE any provider call: reconnecting an existing label
// is always allowed, so a completing device flow or a re-auth can never
// dead-end; only a NEW label past the cap is refused.
func room(ctx context.Context, s *cloud.Service[state], org, user, provider, label string) error {
	_, found, err := s.State.store.GetConnector(ctx, org, user, provider, label)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if found {
		return nil
	}
	n, err := s.State.store.CountConnectors(ctx, org, user, provider)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if n >= maxConnectors {
		return zip.ErrBadRequest("too many connectors for provider")
	}
	return nil
}

// httpErr unwraps a ready-made *zip.HTTPError (saveUser/userPath custody
// errors: 400/503/500) so engine failures propagate with their true status
// instead of being flattened into the caller's 502 transport error.
func httpErr(err error) (*zip.HTTPError, bool) {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he, true
	}
	return nil, false
}

func kmsUnavailable() error {
	return zip.Errorf(http.StatusServiceUnavailable, "%s", kms.ErrMasterKeyMissing.Error())
}

// ── views ──────────────────────────────────────────────────────────────────────

type connView struct {
	ID          string   `json:"id"`
	Provider    string   `json:"provider"`
	Label       string   `json:"label"`
	Account     string   `json:"account"`
	ExternalID  string   `json:"externalId"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   string   `json:"expiresAt"`
	ConnectedAt string   `json:"connectedAt"`
}

// connViewFor is the ONE view builder for a connected connector — credential,
// pollDevice(done), and refreshConn all answer this same wire shape.
func connViewFor(conn Connector) connView {
	return connView{
		ID:          connID(conn.Provider, conn.Label),
		Provider:    conn.Provider,
		Label:       conn.Label,
		Account:     conn.AccountLabel,
		ExternalID:  conn.ExternalID,
		Scopes:      nonNil(conn.Scopes),
		ExpiresAt:   rfc3339(conn.ExpiresAt),
		ConnectedAt: rfc3339(conn.ConnectedAt),
	}
}

// ── handlers ───────────────────────────────────────────────────────────────────
//
// The user list + provider cards folded into the unified listExtensions
// (extension.go): a user connector is an Extension{scope:user} with its labelled
// instances; discovery + status is ONE list across both planes.

// startDevice begins a device sign-in. KMS readiness is checked NOW rather than
// dead-ending the user at poll-done (connect() parity); the cap is checked
// before the provider is called. The provider device code is persisted only in
// the cek-encrypted grants table and NEVER returned.
func startDevice(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, err := caller(c)
	if err != nil {
		return err
	}
	p, ok := userProvider(s, providerParam(c))
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}
	if p.Device == nil {
		return zip.ErrBadRequest("device sign-in not supported")
	}
	if !kmsReady(s) {
		return kmsUnavailable()
	}
	var body struct {
		Label string `json:"label"`
	}
	if len(c.Body()) > 0 {
		if err := json.Unmarshal(c.Body(), &body); err != nil {
			return zip.ErrBadRequest("invalid request body")
		}
	}
	label, err := labelIn(body.Label)
	if err != nil {
		return err
	}
	if err := room(c.Context(), s, org, user, p.ID, label); err != nil {
		return err
	}
	g, ds, err := begin(c.Context(), s, org, user, label, p)
	if err != nil {
		s.Log.Warn("device start failed", "provider", p.ID, "org", org, "user", user, "err", err)
		return zip.Errorf(http.StatusBadGateway, "device start failed")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"flow":      g.ID,
		"userCode":  ds.UserCode,
		"verifyUrl": ds.VerifyURL,
		"interval":  g.Interval,
		"expiresAt": rfc3339(g.ExpiresAt),
	})
}

// pollDevice advances a device sign-in. Terminal outcomes are DATA, not errors
// (verifyConn {active:false} discipline) — the status set is closed:
// pending|connected|denied|expired. pollSlow collapses to "pending" on the
// wire; the raised cadence rides interval.
func pollDevice(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, err := caller(c)
	if err != nil {
		return err
	}
	p, ok := userProvider(s, providerParam(c))
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}
	flow := strings.TrimSpace(c.Param("flow"))
	g, found, err := s.State.store.GetGrant(c.Context(), flow, org, user)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	// Read-time TTL + (id,org,user) tenant scope live in GetGrant, so an expired
	// or foreign grant is indistinguishable from an unknown one.
	if !found || g.Provider != p.ID {
		return zip.ErrNotFound("unknown or expired flow")
	}
	if !kmsReady(s) {
		return kmsUnavailable()
	}
	dp, conn, err := poll(c.Context(), s, p, g)
	if err != nil {
		if he, ok := httpErr(err); ok {
			return he
		}
		s.Log.Warn("device poll failed", "provider", p.ID, "org", org, "user", user, "err", err)
		return zip.Errorf(http.StatusBadGateway, "device poll failed")
	}
	switch dp.Status {
	case pollPending:
		// A server-throttled poll answers pending without an upstream call and may
		// carry no interval — g.Interval keeps the client on the stored cadence.
		return c.JSON(http.StatusOK, map[string]any{"status": "pending", "interval": max(dp.Interval, g.Interval)})
	case pollSlow:
		return c.JSON(http.StatusOK, map[string]any{"status": "pending", "interval": dp.Interval})
	case pollDone:
		return c.JSON(http.StatusOK, map[string]any{"status": "connected", "connector": connViewFor(conn)})
	case pollDenied:
		return c.JSON(http.StatusOK, map[string]any{"status": "denied"})
	case pollExpired:
		return c.JSON(http.StatusOK, map[string]any{"status": "expired"})
	}
	s.Log.Warn("device poll unknown status", "provider", p.ID, "status", dp.Status)
	return zip.Errorf(http.StatusBadGateway, "device poll failed")
}

// credential is the direct intake path: a customer-held token/setup-token
// (Verify) or an externally obtained OAuth bundle from the CLI's local PKCE
// (Adopt). ALWAYS verify-before-store: a bad credential is refused and NOTHING
// is persisted (connectByCredential's fail-closed order).
func credential(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, err := caller(c)
	if err != nil {
		return err
	}
	p, ok := userProvider(s, providerParam(c))
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}
	if !kmsReady(s) {
		return kmsUnavailable()
	}
	var body struct {
		Label     string `json:"label"`
		Token     string `json:"token"`
		AccountID string `json:"accountId"`
		OAuth     *struct {
			Access  string `json:"access"`
			Refresh string `json:"refresh"`
			Account string `json:"account"`
		} `json:"oauth"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	label, err := labelIn(body.Label)
	if err != nil {
		return err
	}
	if err := room(c.Context(), s, org, user, p.ID, label); err != nil {
		return err
	}
	var res *ExchangeResult
	var verr error
	if body.OAuth != nil {
		if p.Adopt == nil {
			return zip.ErrBadRequest("oauth intake not supported")
		}
		if len(body.OAuth.Access) > maxCredentialLen || len(body.OAuth.Refresh) > maxCredentialLen {
			return zip.ErrBadRequest("credential too large")
		}
		res, verr = p.Adopt(c.Context(), Bundle{
			Access:  body.OAuth.Access,
			Refresh: body.OAuth.Refresh,
			Account: sanitizeMeta(strings.TrimSpace(body.OAuth.Account)),
		})
	} else {
		if p.Verify == nil {
			return zip.ErrBadRequest("credential intake not supported")
		}
		token := strings.TrimSpace(body.Token)
		if token == "" {
			return zip.ErrBadRequest("a credential token is required")
		}
		if len(token) > maxCredentialLen {
			return zip.ErrBadRequest("credential too large")
		}
		res, verr = p.Verify(c.Context(), VerifyInput{Token: token, AccountID: sanitizeMeta(strings.TrimSpace(body.AccountID))})
	}
	if verr != nil || res == nil {
		// Provider errors are token-free by contract; the credential value is never
		// logged and NOTHING is stored.
		s.Log.Warn("connector verify failed", "provider", p.ID, "org", org, "user", user, "err", verr)
		return zip.ErrBadRequest("credential verification failed")
	}
	conn, err := saveUser(c.Context(), s, org, user, label, p, res)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"connected": true, "connector": connViewFor(conn)})
}

// refreshConn forces a token rotation for a connected connector.
func refreshConn(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, err := caller(c)
	if err != nil {
		return err
	}
	provider, label, ok := splitID(strings.TrimSpace(c.Param("id")))
	if !ok {
		return zip.ErrBadRequest("malformed connector id")
	}
	p, ok := userProvider(s, provider)
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}
	conn, found, err := s.State.store.GetConnector(c.Context(), org, user, p.ID, label)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if !found {
		return zip.ErrNotFound("connector not connected")
	}
	if p.Refresh == nil {
		return zip.ErrBadRequest("refresh not supported")
	}
	if !kmsReady(s) {
		return kmsUnavailable()
	}
	conn, _, err = fresh(c.Context(), s, p, conn, true)
	if err != nil {
		if he, ok := httpErr(err); ok {
			return he
		}
		s.Log.Warn("token refresh failed", "provider", p.ID, "org", org, "user", user, "label", label, "err", err)
		return zip.Errorf(http.StatusBadGateway, "token refresh failed")
	}
	return c.JSON(http.StatusOK, map[string]any{"refreshed": true, "connector": connViewFor(conn)})
}

// tokenConn hands the custodied access token to its owner — the ONE place
// custody exits. The (org,user)-keyed row IS the same-user gate: another user's
// id is simply "no row" → 404. fresh() auto-rotates within the refreshSkew
// window; static providers degenerate to a plain kmsGet of Secrets[0]. Refresh
// tokens are NEVER returned — custody keeps the sink. The token is never logged.
func tokenConn(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, err := caller(c)
	if err != nil {
		return err
	}
	provider, label, ok := splitID(strings.TrimSpace(c.Param("id")))
	if !ok {
		return zip.ErrBadRequest("malformed connector id")
	}
	p, ok := userProvider(s, provider)
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}
	conn, found, err := s.State.store.GetConnector(c.Context(), org, user, p.ID, label)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if !found {
		return zip.ErrNotFound("connector not connected")
	}
	// Enablement gate (fail-closed): a disabled connector yields NO token. The
	// (org,user,provider,label) row IS the same-user scope; a store error denies.
	if err := gateEnabled(c.Context(), s, userScope, org, user, p.ID, label); err != nil {
		return err
	}
	if !kmsReady(s) {
		return kmsUnavailable()
	}
	conn, tok, err := fresh(c.Context(), s, p, conn, false)
	if err != nil {
		if he, ok := httpErr(err); ok {
			return he
		}
		s.Log.Warn("token unavailable", "provider", p.ID, "org", org, "user", user, "label", label, "err", err)
		return zip.Errorf(http.StatusBadGateway, "token unavailable")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"token":     string(tok),
		"provider":  p.ID,
		"label":     label,
		"expiresAt": rfc3339(conn.ExpiresAt),
	})
}

// dropConn forgets a connector: every custodied secret, then the row.
// Idempotent — dropping a never-connected id still answers {disconnected:true}
// (disconnect() parity). No provider Revoke: none of the user-plane providers
// exposes a revoke endpoint.
func dropConn(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, err := caller(c)
	if err != nil {
		return err
	}
	provider, label, ok := splitID(strings.TrimSpace(c.Param("id")))
	if !ok {
		return zip.ErrBadRequest("malformed connector id")
	}
	p, ok := userProvider(s, provider)
	if !ok {
		return zip.ErrNotFound("unknown provider")
	}
	path, err := userPath(org, user, p.ID, label)
	if err != nil {
		return err
	}
	if s.State.kms != nil {
		for _, name := range p.Secrets {
			if derr := kmsDelete(s, path, name); derr != nil {
				s.Log.Warn("kms delete failed (continuing)", "provider", p.ID, "org", org, "user", user, "label", label, "secret", name, "err", derr)
			}
		}
	}
	if _, derr := s.State.store.DeleteConnector(c.Context(), org, user, p.ID, label); derr != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", derr)
	}
	return c.JSON(http.StatusOK, map[string]any{"disconnected": true})
}
