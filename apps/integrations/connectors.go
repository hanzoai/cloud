// connectors.go is the per-USER connector plane — /v1/connectors, the sibling
// of the org-scoped /v1/integrations surface. Same package, same registry, same
// store file, same KMS client; only the custody key differs (org,user,provider,
// label → userPath).
//
// Surface (registered by connectorRoutes, called last from routes()):
//
//	GET    /v1/connectors                                  this user's connectors     -> {connectors:[...]}
//	GET    /v1/connectors/providers                        user-scoped provider cards -> {providers:[...]}
//	GET    /v1/connectors/:id/token                        custodied access token     -> {token,...}
//	POST   /v1/connectors/:provider/device                 begin device sign-in       -> {flow,userCode,...}
//	POST   /v1/connectors/:provider/device/:flow/poll      poll device sign-in        -> {status,...}
//	POST   /v1/connectors/:provider/credential             token / oauth-bundle intake-> {connected,connector}
//	POST   /v1/connectors/:id/refresh                      force a token rotation     -> {refreshed,connector}
//	DELETE /v1/connectors/:id                              forget + delete secrets    -> {disconnected:true}
//
// TENANTING. Every read/write is bound org=? AND user=? — the (org,user) pair
// from caller() IS the row key, so another user's connector id is simply "no
// row" → 404. No admin gate: a user owns their own connectors.
//
// CUSTODY. id = provider + ":" + label. Every intake path verifies the
// credential live BEFORE anything is stored (saveUser: sanitize → seal-before-
// row → upsert). No secret ever appears in a row, response, or log line except
// GET /:id/token's body — the ONE custody exit, readable only by the same
// validated (org,user). Device pending state lives in the cek-encrypted grants
// table (see the grants DDL comment in store.go), never in KMS.

package integrations

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// connectorRoutes registers the user-plane surface, every route a TYPED op (see
// ops.go — the registry is what OpenAPI, MCP and the CLI are projected from).
// Called last from routes(); the literal /providers registers before the wildcard
// GETs (static-before-wildcard discipline, same as routes()).
func connectorRoutes(app cloud.Router, zapp *zip.App, o ops) {
	// The ops below read the org (cloud.Bridge) and the user id (bridgeFacts) off
	// the request context, so the bridge has to run ahead of them.
	//
	// routes() already installs both through the scope, and the scope gates by
	// path across EVERY prefix the manifest declares for this subsystem —
	// /v1/connectors among them — so this subtree is covered by that one install.
	// The Group("/v1/connectors") that used to stand here was a second install on
	// a node with no routes beneath it (the ops register on zapp at absolute
	// paths), which is what zip refuses to compose.

	zip.Get(zapp, "/v1/connectors", o.connectors)
	zip.Get(zapp, "/v1/connectors/providers", o.connectorProviders)
	zip.Get(zapp, "/v1/connectors/:id/token", o.tokenConn)
	zip.Post(zapp, "/v1/connectors/:provider/device", o.startDevice)
	zip.Post(zapp, "/v1/connectors/:provider/device/:flow/poll", o.pollDevice)
	zip.Post(zapp, "/v1/connectors/:provider/credential", o.credential)
	zip.Post(zapp, "/v1/connectors/:id/refresh", o.refreshConn)
	zip.Delete(zapp, "/v1/connectors/:id", o.dropConn)
}

// ── inputs ─────────────────────────────────────────────────────────────────────

// connectorRef addresses one of the caller's own connectors by its id.
type connectorRef struct {
	// ID is the connector id, provider + ":" + label ("openai:default") — the
	// auth-profile-id shape. Another user's id is simply no row, so 404.
	ID string `json:"id"`
}

// split is the id's two halves, refused 400 when either is missing.
func (r connectorRef) split() (provider, label string, err error) {
	provider, label, ok := splitID(strings.TrimSpace(r.ID))
	if !ok {
		return "", "", zip.ErrBadRequest("malformed connector id")
	}
	return provider, label, nil
}

// deviceStartIn begins a device sign-in for one provider.
type deviceStartIn struct {
	// Provider is the user-scoped provider's registry id, from the path.
	Provider string `json:"provider"`
	// Label names this connection so one user can hold several per provider
	// ("work", "personal"). Empty means "default". 1-64 of [A-Za-z0-9._-].
	Label string `json:"label"`
}

// ref is the provider this request addresses, normalized like every other
// :provider op.
func (in deviceStartIn) ref() providerRef { return providerRef{Provider: in.Provider} }

// devicePollIn advances one device sign-in.
type devicePollIn struct {
	// Provider is the user-scoped provider's registry id, from the path.
	Provider string `json:"provider"`
	// Flow is the id deviceStartOut returned. Expired or another user's flow is
	// indistinguishable from an unknown one: 404.
	Flow string `json:"flow"`
}

// ref is the provider this request addresses, normalized like every other
// :provider op.
func (in devicePollIn) ref() providerRef { return providerRef{Provider: in.Provider} }

// credentialIn is the direct intake body: EITHER a customer-held token (verified
// via the provider's Verify) OR an externally obtained OAuth bundle (adopted via
// Adopt). Presence of oauth selects the second.
type credentialIn struct {
	// Provider is the user-scoped provider's registry id, from the path.
	Provider string `json:"provider"`
	// Label names this connection; empty means "default".
	Label string `json:"label"`
	// Token is the customer-held credential for the Verify path. Read on STDIN by
	// the CLI, never argv; never logged, echoed, or stored outside KMS.
	Token string `json:"token"`
	// AccountID scopes the credential where the provider's Verify needs one.
	AccountID string `json:"accountId"`
	// OAuth is a bundle the CLI already obtained through its own local PKCE flow.
	// Present ⇒ the Adopt path; absent ⇒ the Token path.
	OAuth *oauthBundleIn `json:"oauth"`
}

// ref is the provider this request addresses, normalized like every other
// :provider op.
func (in credentialIn) ref() providerRef { return providerRef{Provider: in.Provider} }

// oauthBundleIn is an externally obtained OAuth grant handed over for custody.
type oauthBundleIn struct {
	// Access is the access token.
	Access string `json:"access"`
	// Refresh is the refresh token. It is sealed and NEVER handed back out.
	Refresh string `json:"refresh"`
	// Account is the account label the flow reported; sanitized on ingest.
	Account string `json:"account"`
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
	return zip.Errorf(http.StatusServiceUnavailable, "%s", errCredentialStore)
}

// ── views ──────────────────────────────────────────────────────────────────────

type connView struct {
	// ID is provider + ":" + label — what every other connector route addresses.
	ID string `json:"id"`
	// Provider is the user-scoped provider's registry id.
	Provider string `json:"provider"`
	// Label is the caller's name for this connection ("default", "work").
	Label string `json:"label"`
	// Account is the provider's label for the connected account.
	Account string `json:"account"`
	// ExternalID is the provider's own id for that account.
	ExternalID string `json:"externalId"`
	// Scopes are the permissions the credential carries. Never null; [] when none.
	Scopes []string `json:"scopes"`
	// ExpiresAt is when the access token expires, RFC 3339 UTC; empty for a
	// non-expiring credential. Reading the token auto-rotates inside the window.
	ExpiresAt string `json:"expiresAt"`
	// ConnectedAt is when the connector was last (re)established, RFC 3339 UTC.
	ConnectedAt string `json:"connectedAt"`
}

// connectorsOut is this user's connectors, across every provider.
type connectorsOut struct {
	// Connectors is the caller's own set. Never null; [] when they have none.
	Connectors []connView `json:"connectors"`
}

// connectorProvidersOut is the user-plane provider catalog.
type connectorProvidersOut struct {
	// Providers is every USER-scoped provider, sorted by id. Never null.
	Providers []connectorProviderView `json:"providers"`
}

// deviceStartOut is a device sign-in in progress: what to show the user, and how
// to poll. The provider's device_code is NOT here — it stays in the encrypted
// grants table.
type deviceStartOut struct {
	// Flow is the id to poll with.
	Flow string `json:"flow"`
	// UserCode is the short code the user types at VerifyURL.
	UserCode string `json:"userCode"`
	// VerifyURL is the page the user opens to enter UserCode.
	VerifyURL string `json:"verifyUrl"`
	// Interval is the seconds to wait between polls.
	Interval int64 `json:"interval"`
	// ExpiresAt is when the flow dies, RFC 3339 UTC.
	ExpiresAt string `json:"expiresAt"`
}

// devicePollOut is one poll's answer. Terminal outcomes are DATA, not errors, and
// the status set is CLOSED: pending | connected | denied | expired.
type devicePollOut struct {
	// Status is the flow's state. "pending" means poll again after Interval.
	Status string `json:"status"`
	// Interval is the seconds to wait before the next poll. Present only while
	// pending, and it may rise when the provider asks the client to slow down.
	Interval *int64 `json:"interval,omitempty"`
	// Connector is the connected connector. Present only on "connected".
	Connector *connView `json:"connector,omitempty"`
}

// credentialOut acknowledges a verified, sealed credential.
type credentialOut struct {
	// Connected is always true — a failed verification is a 400 and stores nothing.
	Connected bool `json:"connected"`
	// Connector is the connector as it now stands.
	Connector connView `json:"connector"`
}

// refreshOut acknowledges a forced token rotation.
type refreshOut struct {
	// Refreshed is always true — a failed rotation is an HTTP error.
	Refreshed bool `json:"refreshed"`
	// Connector is the connector with its new expiry.
	Connector connView `json:"connector"`
}

// connectorTokenOut is the ONE place custody exits: the live access token, handed
// to the user who owns it. The refresh token is never included.
type connectorTokenOut struct {
	// Token is the access token, rotated first if it was within the refresh window.
	Token string `json:"token"`
	// Provider is the connector's provider id.
	Provider string `json:"provider"`
	// Label is the connector's label.
	Label string `json:"label"`
	// ExpiresAt is when this token expires, RFC 3339 UTC; empty if non-expiring.
	ExpiresAt string `json:"expiresAt"`
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

type connectorProviderView struct {
	// ID is the provider's registry id and the :provider path segment.
	ID string `json:"id"`
	// Name is the provider's display name.
	Name string `json:"name"`
	// Description is the one-line pitch the console card shows.
	Description string `json:"description"`
	// Category groups the card.
	Category string `json:"category"`
	// Scopes are the permissions a connection will ask for. Never null.
	Scopes []string `json:"scopes"`
	// Methods are the intake paths this provider supports, derived from its
	// capabilities: "device", "oauth" (adopt an externally obtained bundle) and
	// "token" (a customer-held credential). At least one, always.
	Methods []string `json:"methods"`
}

// ── handlers ───────────────────────────────────────────────────────────────────

// connectors lists the caller's OWN connectors across every provider — the set
// `hanzo connector ls` prints. Rows are keyed (org,user), so this can never
// surface another user's connector, and no secret is in the view.
//
// Response: {"connectors":[{"id":"openai:default","provider":"openai","label":"default","account":"me@acme.com","externalId":"u-42","scopes":["api"],"expiresAt":"2026-07-01T11:00:00Z","connectedAt":"2026-07-01T10:00:00Z"}]}
func (o ops) connectors(ctx context.Context, _ *noArgs) (*connectorsOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	list, err := o.s.State.store.ListConnectors(ctx, org, user)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]connView, 0, len(list))
	for _, conn := range list {
		out = append(out, connViewFor(conn))
	}
	return &connectorsOut{Connectors: out}, nil
}

// connectorProviders lists the user-scoped provider cards — the catalog of what a
// user can connect, and how. Methods derive from capabilities (Device/Adopt/Verify
// — Mount asserts at least one), never from a parallel kind enum.
//
// Response: {"providers":[{"id":"openai","name":"OpenAI","description":"Use your own OpenAI account.","category":"AI","scopes":["api"],"methods":["device","token"]}]}
func (o ops) connectorProviders(ctx context.Context, _ *noArgs) (*connectorProvidersOut, error) {
	if _, _, err := caller(ctx); err != nil {
		return nil, err
	}
	ids := sortedProviderIDs(o.s)
	out := make([]connectorProviderView, 0, len(ids))
	for _, id := range ids {
		p := o.s.State.providers[id]
		if p.Scope != userScope {
			continue
		}
		methods := make([]string, 0, 3)
		if p.Device != nil {
			methods = append(methods, "device")
		}
		if p.Adopt != nil {
			methods = append(methods, "oauth")
		}
		if p.Verify != nil {
			methods = append(methods, "token")
		}
		out = append(out, connectorProviderView{
			ID: p.ID, Name: p.Name, Description: p.Description, Category: p.Category,
			Scopes: nonNil(p.Scopes), Methods: methods,
		})
	}
	return &connectorProvidersOut{Providers: out}, nil
}

// startDevice begins a device sign-in and returns the code to show the user plus
// how to poll for completion. KMS readiness is checked NOW rather than dead-ending
// the user at poll-done (connect() parity), and the per-provider connector cap is
// checked before the provider is called. The provider's device code is persisted
// only in the encrypted grants table and is NEVER returned.
//
// Example: {"provider":"openai","label":"work"}
// Response: {"flow":"g_7f2c","userCode":"WDJB-MJHT","verifyUrl":"https://example.com/device","interval":5,"expiresAt":"2026-07-01T10:15:00Z"}
func (o ops) startDevice(ctx context.Context, in *deviceStartIn) (*deviceStartOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	p, ok := userProvider(s, in.ref().id())
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	if p.Device == nil {
		return nil, zip.ErrBadRequest("device sign-in not supported")
	}
	if !kmsReady(s) {
		return nil, kmsUnavailable()
	}
	label, err := labelIn(in.Label)
	if err != nil {
		return nil, err
	}
	if err := room(ctx, s, org, user, p.ID, label); err != nil {
		return nil, err
	}
	g, ds, err := begin(ctx, s, org, user, label, p)
	if err != nil {
		s.Log.Warn("device start failed", "provider", p.ID, "org", org, "user", user, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "device start failed")
	}
	return &deviceStartOut{
		Flow:      g.ID,
		UserCode:  ds.UserCode,
		VerifyURL: ds.VerifyURL,
		Interval:  g.Interval,
		ExpiresAt: rfc3339(g.ExpiresAt),
	}, nil
}

// pollDevice advances a device sign-in. Terminal outcomes are DATA, not errors
// (verifyConn {active:false} discipline) — the status set is closed:
// pending|connected|denied|expired. pollSlow collapses to "pending" on the
// wire; the raised cadence rides interval.
//
// Example: {"provider":"openai","flow":"g_7f2c"}
// Response: {"status":"connected","connector":{"id":"openai:work","provider":"openai","label":"work","account":"me@acme.com","externalId":"u-42","scopes":["api"],"expiresAt":"2026-07-01T11:00:00Z","connectedAt":"2026-07-01T10:05:00Z"}}
func (o ops) pollDevice(ctx context.Context, in *devicePollIn) (*devicePollOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	p, ok := userProvider(s, in.ref().id())
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	g, found, err := s.State.store.GetGrant(ctx, strings.TrimSpace(in.Flow), org, user)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	// Read-time TTL + (id,org,user) tenant scope live in GetGrant, so an expired
	// or foreign grant is indistinguishable from an unknown one.
	if !found || g.Provider != p.ID {
		return nil, zip.ErrNotFound("unknown or expired flow")
	}
	if !kmsReady(s) {
		return nil, kmsUnavailable()
	}
	dp, conn, err := poll(ctx, s, p, g)
	if err != nil {
		if he, ok := httpErr(err); ok {
			return nil, he
		}
		s.Log.Warn("device poll failed", "provider", p.ID, "org", org, "user", user, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "device poll failed")
	}
	switch dp.Status {
	case pollPending:
		// A server-throttled poll answers pending without an upstream call and may
		// carry no interval — g.Interval keeps the client on the stored cadence.
		interval := max(dp.Interval, g.Interval)
		return &devicePollOut{Status: "pending", Interval: &interval}, nil
	case pollSlow:
		return &devicePollOut{Status: "pending", Interval: &dp.Interval}, nil
	case pollDone:
		view := connViewFor(conn)
		return &devicePollOut{Status: "connected", Connector: &view}, nil
	case pollDenied:
		return &devicePollOut{Status: "denied"}, nil
	case pollExpired:
		return &devicePollOut{Status: "expired"}, nil
	}
	s.Log.Warn("device poll unknown status", "provider", p.ID, "status", dp.Status)
	return nil, zip.Errorf(http.StatusBadGateway, "device poll failed")
}

// credential is the direct intake path: a customer-held token/setup-token
// (Verify) or an externally obtained OAuth bundle from the CLI's local PKCE
// (Adopt). ALWAYS verify-before-store: a bad credential is refused and NOTHING
// is persisted (connectByCredential's fail-closed order).
//
// Example: {"provider":"openai","label":"work","token":"sk-user-owned-key"}
// Response: {"connected":true,"connector":{"id":"openai:work","provider":"openai","label":"work","account":"me@acme.com","externalId":"u-42","scopes":["api"],"expiresAt":"","connectedAt":"2026-07-01T10:00:00Z"}}
func (o ops) credential(ctx context.Context, in *credentialIn) (*credentialOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	p, ok := userProvider(s, in.ref().id())
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	if !kmsReady(s) {
		return nil, kmsUnavailable()
	}
	label, err := labelIn(in.Label)
	if err != nil {
		return nil, err
	}
	if err := room(ctx, s, org, user, p.ID, label); err != nil {
		return nil, err
	}
	var res *ExchangeResult
	var verr error
	if in.OAuth != nil {
		if p.Adopt == nil {
			return nil, zip.ErrBadRequest("oauth intake not supported")
		}
		if len(in.OAuth.Access) > maxCredentialLen || len(in.OAuth.Refresh) > maxCredentialLen {
			return nil, zip.ErrBadRequest("credential too large")
		}
		res, verr = p.Adopt(ctx, Bundle{
			Access:  in.OAuth.Access,
			Refresh: in.OAuth.Refresh,
			Account: sanitizeMeta(strings.TrimSpace(in.OAuth.Account)),
		})
	} else {
		if p.Verify == nil {
			return nil, zip.ErrBadRequest("credential intake not supported")
		}
		token := strings.TrimSpace(in.Token)
		if token == "" {
			return nil, zip.ErrBadRequest("a credential token is required")
		}
		if len(token) > maxCredentialLen {
			return nil, zip.ErrBadRequest("credential too large")
		}
		res, verr = p.Verify(ctx, VerifyInput{Token: token, AccountID: sanitizeMeta(strings.TrimSpace(in.AccountID))})
	}
	if verr != nil || res == nil {
		// Provider errors are token-free by contract; the credential value is never
		// logged and NOTHING is stored.
		s.Log.Warn("connector verify failed", "provider", p.ID, "org", org, "user", user, "err", verr)
		return nil, zip.ErrBadRequest("credential verification failed")
	}
	conn, err := saveUser(ctx, s, org, user, label, p, res)
	if err != nil {
		return nil, err
	}
	return &credentialOut{Connected: true, Connector: connViewFor(conn)}, nil
}

// refreshConn forces a token rotation for a connected connector, ahead of the
// automatic rotation a token read would do inside the expiry window. Only
// providers that declare a Refresh support it.
//
// Example: {"id":"openai:work"}
// Response: {"refreshed":true,"connector":{"id":"openai:work","provider":"openai","label":"work","account":"me@acme.com","externalId":"u-42","scopes":["api"],"expiresAt":"2026-07-01T12:00:00Z","connectedAt":"2026-07-01T10:00:00Z"}}
func (o ops) refreshConn(ctx context.Context, in *connectorRef) (*refreshOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	provider, label, err := in.split()
	if err != nil {
		return nil, err
	}
	s := o.s
	p, ok := userProvider(s, provider)
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	conn, found, err := s.State.store.GetConnector(ctx, org, user, p.ID, label)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("connector not connected")
	}
	if p.Refresh == nil {
		return nil, zip.ErrBadRequest("refresh not supported")
	}
	if !kmsReady(s) {
		return nil, kmsUnavailable()
	}
	conn, _, err = fresh(ctx, s, p, conn, true)
	if err != nil {
		if he, ok := httpErr(err); ok {
			return nil, he
		}
		s.Log.Warn("token refresh failed", "provider", p.ID, "org", org, "user", user, "label", label, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "token refresh failed")
	}
	return &refreshOut{Refreshed: true, Connector: connViewFor(conn)}, nil
}

// tokenConn hands the custodied access token to its owner — the ONE place
// custody exits. The (org,user)-keyed row IS the same-user gate: another user's
// id is simply "no row" → 404. fresh() auto-rotates within the refreshSkew
// window; static providers degenerate to a plain kmsGet of Secrets[0]. Refresh
// tokens are NEVER returned — custody keeps the sink. The token is never logged.
//
// Example: {"id":"openai:work"}
// Response: {"token":"<the access token>","provider":"openai","label":"work","expiresAt":"2026-07-01T11:00:00Z"}
func (o ops) tokenConn(ctx context.Context, in *connectorRef) (*connectorTokenOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	provider, label, err := in.split()
	if err != nil {
		return nil, err
	}
	s := o.s
	p, ok := userProvider(s, provider)
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	conn, found, err := s.State.store.GetConnector(ctx, org, user, p.ID, label)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("connector not connected")
	}
	if !kmsReady(s) {
		return nil, kmsUnavailable()
	}
	conn, tok, err := fresh(ctx, s, p, conn, false)
	if err != nil {
		if he, ok := httpErr(err); ok {
			return nil, he
		}
		s.Log.Warn("token unavailable", "provider", p.ID, "org", org, "user", user, "label", label, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "token unavailable")
	}
	return &connectorTokenOut{
		Token:     string(tok),
		Provider:  p.ID,
		Label:     label,
		ExpiresAt: rfc3339(conn.ExpiresAt),
	}, nil
}

// dropConn forgets a connector: every custodied secret, then the row.
// Idempotent — dropping a never-connected id still answers {disconnected:true}
// (disconnect() parity). No provider Revoke: none of the user-plane providers
// exposes a revoke endpoint.
//
// Example: {"id":"openai:work"}
// Response: {"disconnected":true}
func (o ops) dropConn(ctx context.Context, in *connectorRef) (*disconnectOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	provider, label, err := in.split()
	if err != nil {
		return nil, err
	}
	s := o.s
	p, ok := userProvider(s, provider)
	if !ok {
		return nil, zip.ErrNotFound("unknown provider")
	}
	path, err := userPath(org, user, p.ID, label)
	if err != nil {
		return nil, err
	}
	if s.State.kms != nil {
		for _, name := range p.Secrets {
			if derr := kmsDelete(s, path, name); derr != nil {
				s.Log.Warn("kms delete failed (continuing)", "provider", p.ID, "org", org, "user", user, "label", label, "secret", name, "err", derr)
			}
		}
	}
	if _, derr := s.State.store.DeleteConnector(ctx, org, user, p.ID, label); derr != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", derr)
	}
	return &disconnectOut{Disconnected: true}, nil
}
