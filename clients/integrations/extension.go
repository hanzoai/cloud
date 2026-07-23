// extension.go is the ONE connect+configure plane — /v1/connectors — over which
// every third-party connection is listed, connected, configured, enabled,
// disabled, and forgotten, regardless of KIND or SCOPE. It is the decomplect: a
// per-user API key, a per-org OAuth app, a BYO model provider, a cloud account, a
// plugin, and a skill are the SAME shape — an Extension — distinguished by VALUES
// (kind, scope), not by separate URLs.
//
//	Extension = { id, kind, scope, capabilities[], available, methods[],
//	              enabled, config, instances[] }
//
// kind  ∈ { key, oauth, model, cloud, plugin, skill } — the archetype (kindOf).
// scope ∈ { org, user }                               — the custody plane (scopeOf).
// instances = the connected credentials (never a secret VALUE) — 0/1 for org,
//
//	0..maxConnectors for user. `credential?` in the model == len>0.
//
// capabilities = what the extension ENABLES downstream (model-routing, cf-resource-
//
//	management, cluster-fold, chat-bridge, git-sync…). Those stay as
//	their OWN surfaces (/v1/cloudflare, /v1/clusters, model routing);
//	they CONSUME a connector by id, they are not connectors.
//
// Surface (connectorRoutes — the ONE registration, called from routes()):
//
//	GET    /v1/connectors            [?kind=&scope=]  list Extensions      -> {extensions:[…]}
//	GET    /v1/connectors/:id                         one Extension        -> Extension
//	POST   /v1/connectors/:id/connect                 connect (single-shot)-> {connected|authorizeUrl…}
//	GET    /v1/connectors/:id/callback                OAuth return (org)   -> 302 console
//	POST   /v1/connectors/:id/verify                  re-verify stored key -> {active,…}
//	POST   /v1/connectors/:id/device                  device begin (user)  -> {flow,userCode,…}
//	POST   /v1/connectors/:id/device/:flow/poll       device poll  (user)  -> {status,…}
//	GET    /v1/connectors/:id/token                   custodied token(user)-> {token,…}
//	POST   /v1/connectors/:id/refresh                 rotate token (user)  -> {refreshed,…}
//	POST   /v1/connectors/:id/enable                  enablement on        -> {id,enabled:true}
//	POST   /v1/connectors/:id/disable                 enablement off       -> {id,enabled:false}
//	PATCH  /v1/connectors/:id/config                  set opaque config    -> {id,config}
//	DELETE /v1/connectors/:id                         forget + drop secrets-> {disconnected|forgotten}
//
// TENANTING. org is required on every verb (principal.Org → 403 if absent); the
// user plane additionally binds c.User(). Reads/writes are bound (org[,user]) so
// another tenant's id is "no row". The two custody planes stay disjoint by
// provider scope; the id resolves the plane, never the caller.
package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// ── kind / scope values (the archetype + plane, as VALUES not URLs) ─────────────

const (
	// kind archetypes. key/oauth are live on this plane today; model/cloud/plugin/
	// skill are the reserved values the mapped-in follow-ons (ai-connections,
	// venue, plugins, skills) will carry when they fold onto this surface.
	kindKey    = "key"
	kindOAuth  = "oauth"
	kindModel  = "model"
	kindCloud  = "cloud"
	kindPlugin = "plugin"
	kindSkill  = "skill"

	// orgScope is the wire value for the org custody plane. Internally an org
	// provider carries Scope == "" (the default); scopeOf normalizes "" → "org" so
	// the wire and the ?scope= filter speak ONE vocabulary {org,user}.
	orgScope = "org"
)

// kindOf is the ONE archetype classifier. An explicit Provider.Kind wins (cloudflare
// declares "key"; the follow-on model/cloud providers will declare theirs); absent
// that it is structural — a pure customer-credential provider (only Verify) is a
// key, everything else (Authorize / Adopt / Device) is an oauth-family flow.
func kindOf(p *Provider) string {
	if p.Kind != "" {
		return p.Kind
	}
	if p.Verify != nil && p.Authorize == nil && p.Adopt == nil && p.Device == nil {
		return kindKey
	}
	return kindOAuth
}

// scopeOf normalizes the internal Scope ("" org default / "user") to the wire
// value {org,user}.
func scopeOf(p *Provider) string {
	if p.Scope == userScope {
		return userScope
	}
	return orgScope
}

// methodsOf lists how a caller may connect this provider — the connect verbs it
// answers, derived from capabilities (never a parallel enum).
func methodsOf(p *Provider) []string {
	m := make([]string, 0, 3)
	if p.Scope == userScope {
		if p.Device != nil {
			m = append(m, "device")
		}
		if p.Adopt != nil {
			m = append(m, "oauth")
		}
		if p.Verify != nil {
			m = append(m, "token")
		}
		return m
	}
	if p.Authorize != nil {
		m = append(m, "oauth")
	}
	if p.Verify != nil {
		m = append(m, "token")
	}
	return m
}

// availableOf reports whether the provider is connectable on this deployment: user
// providers always are; an org provider needs its app creds configured in ENV.
func availableOf(p *Provider) bool {
	if p.Scope == userScope {
		return true
	}
	return p.Configured != nil && p.Configured()
}

// ── wire shape ──────────────────────────────────────────────────────────────────

// Extension is the unified wire view — a provider archetype folded with the
// caller's connection + config + enablement state in the relevant plane.
type Extension struct {
	ID           string         `json:"id"`
	Kind         string         `json:"kind"`  // key|oauth|model|cloud|plugin|skill
	Scope        string         `json:"scope"` // org|user
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Category     string         `json:"category"`
	Capabilities []string       `json:"capabilities"` // what it ENABLES (consumed by capability surfaces)
	Available    bool           `json:"available"`    // connectable now (org: creds present)
	Methods      []string       `json:"methods"`      // oauth|token|device
	Connected    bool           `json:"connected"`    // len(instances) > 0
	Enabled      bool           `json:"enabled"`      // provider-level enablement (default true)
	Config       map[string]any `json:"config,omitempty"`
	Instances    []Instance     `json:"instances"` // connected credentials (never the secret value)
}

// Instance is one connected credential under an Extension: org has ≤1 (label ""),
// user has ≤ maxConnectors (labelled). The secret VALUE is never here.
type Instance struct {
	Label       string   `json:"label"`
	Account     string   `json:"account"`
	ExternalID  string   `json:"externalId"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   string   `json:"expiresAt,omitempty"`
	ConnectedAt string   `json:"connectedAt"`
	Enabled     bool     `json:"enabled"`
}

// ── list / get ──────────────────────────────────────────────────────────────────

// cfgKey indexes a loaded ExtConfig by (scope,provider,label).
func cfgKey(scope, provider, label string) string {
	return scope + "\x00" + provider + "\x00" + label
}

// enablementFrom reads the enablement bit for (scope,provider,label) from a
// pre-loaded config index; a missing row is enabled-by-default.
func enablementFrom(idx map[string]ExtConfig, scope, provider, label string) bool {
	if cfg, ok := idx[cfgKey(scope, provider, label)]; ok {
		return cfg.Enabled
	}
	return true
}

// listExtensions is the ONE registry+status list across BOTH planes, filtered by
// the ?kind= and ?scope= VALUES. org is required; the user plane folds in this
// user's instances (a caller with no user still sees every provider as a card).
func listExtensions(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	user := strings.Clone(strings.TrimSpace(c.User()))
	if user != "" && !validUser(user) {
		user = "" // tolerate a malformed user id: list cards without user instances
	}
	kindF := strings.TrimSpace(c.Query("kind"))
	scopeF := strings.TrimSpace(c.Query("scope"))

	orgConns, err := s.State.store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	orgByProvider := make(map[string]Connection, len(orgConns))
	for _, cn := range orgConns {
		orgByProvider[cn.Provider] = cn
	}
	var userConns []Connector
	if user != "" {
		userConns, err = s.State.store.ListConnectors(c.Context(), org, user)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
		}
	}
	userByProvider := make(map[string][]Connector)
	for _, cn := range userConns {
		userByProvider[cn.Provider] = append(userByProvider[cn.Provider], cn)
	}
	cfgs, err := s.State.store.ListExtConfig(c.Context(), org, user)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	cfgIndex := make(map[string]ExtConfig, len(cfgs))
	for _, cfg := range cfgs {
		cfgIndex[cfgKey(cfg.Scope, cfg.Provider, cfg.Label)] = cfg
	}

	out := make([]Extension, 0, len(s.State.providers))
	for _, id := range sortedProviderIDs(s) {
		p := s.State.providers[id]
		sc := scopeOf(p)
		if scopeF != "" && sc != scopeF {
			continue
		}
		if kindF != "" && kindOf(p) != kindF {
			continue
		}
		orgConn, hasOrg := orgByProvider[id]
		out = append(out, buildExtension(p, orgConn, hasOrg, userByProvider[id], cfgIndex))
	}
	return c.JSON(http.StatusOK, map[string]any{"extensions": out})
}

// getExtension returns one Extension (404 for an unknown id). The label part of a
// user id is ignored here — the Extension carries every instance for the provider.
func getExtension(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	user := strings.Clone(strings.TrimSpace(c.User()))
	if user != "" && !validUser(user) {
		user = ""
	}
	p, ok := resolveProvider(s, providerOf(idParam(c)))
	if !ok {
		return zip.ErrNotFound("unknown connector")
	}
	cfgs, err := s.State.store.ListExtConfig(c.Context(), org, user)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	cfgIndex := make(map[string]ExtConfig, len(cfgs))
	for _, cfg := range cfgs {
		cfgIndex[cfgKey(cfg.Scope, cfg.Provider, cfg.Label)] = cfg
	}
	var (
		orgConn   Connection
		hasOrg    bool
		userConns []Connector
	)
	if scopeOf(p) == orgScope {
		orgConn, hasOrg, err = s.State.store.Get(c.Context(), org, p.ID)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
		}
	} else if user != "" {
		all, err := s.State.store.ListConnectors(c.Context(), org, user)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
		}
		for _, cn := range all {
			if cn.Provider == p.ID {
				userConns = append(userConns, cn)
			}
		}
	}
	return c.JSON(http.StatusOK, buildExtension(p, orgConn, hasOrg, userConns, cfgIndex))
}

// buildExtension folds a provider archetype with the caller's connection + config
// + enablement into the ONE wire shape. Pure over its inputs (no store/HTTP), so
// the fold is unit-testable without a live tenant.
func buildExtension(p *Provider, orgConn Connection, hasOrg bool, userConns []Connector, cfgIndex map[string]ExtConfig) Extension {
	sc := scopeOf(p)
	ext := Extension{
		ID: p.ID, Kind: kindOf(p), Scope: sc,
		Name: p.Name, Description: p.Description, Category: p.Category,
		Capabilities: nonNil(p.Capabilities),
		Available:    availableOf(p),
		Methods:      methodsOf(p),
		Enabled:      enablementFrom(cfgIndex, sc, p.ID, ""),
		Instances:    []Instance{},
	}
	if cfg, ok := cfgIndex[cfgKey(sc, p.ID, "")]; ok && cfg.Config != "" {
		ext.Config = decodeConfig(cfg.Config)
	}
	switch sc {
	case orgScope:
		if hasOrg {
			ext.Instances = append(ext.Instances, Instance{
				Label:       "",
				Account:     orgConn.AccountLabel,
				ExternalID:  orgConn.ExternalID,
				Scopes:      nonNil(orgConn.Scopes),
				ConnectedAt: rfc3339(orgConn.ConnectedAt),
				Enabled:     ext.Enabled,
			})
		}
	case userScope:
		// Deterministic instance order (newest-first is the store default; sort by
		// label for a stable wire regardless of load path).
		sort.Slice(userConns, func(i, j int) bool { return userConns[i].Label < userConns[j].Label })
		for _, cn := range userConns {
			ext.Instances = append(ext.Instances, Instance{
				Label:       cn.Label,
				Account:     cn.AccountLabel,
				ExternalID:  cn.ExternalID,
				Scopes:      nonNil(cn.Scopes),
				ExpiresAt:   rfc3339(cn.ExpiresAt),
				ConnectedAt: rfc3339(cn.ConnectedAt),
				Enabled:     enablementFrom(cfgIndex, userScope, p.ID, cn.Label),
			})
		}
	}
	ext.Connected = len(ext.Instances) > 0
	return ext
}

// decodeConfig parses a stored config blob into a map for the wire. A malformed
// blob (should never happen — we only store what configureExt compacted) yields
// nil rather than an error, so a bad row can never break a list.
func decodeConfig(s string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

// ── connect / forget (dispatch by the resolved provider's scope) ────────────────

// connectExt is the single-shot connect verb for EVERY plane. It resolves the
// provider by id and delegates to the plane's admission path: org → connect
// (OAuth begin or apikey verify-before-seal), user → credential (token verify or
// OAuth-bundle adopt). The multi-step device flow has its own verb (startDevice).
func connectExt(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := resolveProvider(s, providerOf(idParam(c)))
	if !ok {
		return zip.ErrNotFound("unknown connector")
	}
	if p.Scope == userScope {
		return credential(s, c)
	}
	return connect(s, c)
}

// forgetExt forgets a connector on EITHER plane: it clears the config row
// (best-effort, so a reconnect starts enabled-by-default) then delegates to the
// plane's secret+row deletion (org → disconnect, user → dropConn), each of which
// is idempotent and writes the terminal response.
func forgetExt(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := resolveProvider(s, providerOf(idParam(c)))
	if !ok {
		return zip.ErrNotFound("unknown connector")
	}
	if org, ok := principal.Org(c); ok && validOrg(org) {
		sc := scopeOf(p)
		user, label := "", ""
		if sc == userScope {
			user = strings.Clone(strings.TrimSpace(c.User()))
			_, label, _ = strings.Cut(idParam(c), ":")
			if label = strings.TrimSpace(label); label == "" {
				label = defaultLabel
			}
		}
		_, _ = s.State.store.DeleteExtConfig(c.Context(), sc, org, user, p.ID, label)
	}
	if p.Scope == userScope {
		return dropConn(s, c)
	}
	return disconnect(s, c)
}

// verifyExt re-verifies a connected credential against the provider (org plane).
func verifyExt(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := resolveProvider(s, providerOf(idParam(c)))
	if !ok {
		return zip.ErrNotFound("unknown connector")
	}
	if p.Scope == userScope {
		return zip.ErrBadRequest("verify is not supported on the user plane; re-add the credential instead")
	}
	return verifyConn(s, c)
}

// ── enablement + config (the NEW axis — orthogonal to custody) ──────────────────

// extIdentity resolves the full extension identity for a mutation verb plus its
// provider, applying the org-admin gate for AdminOnly org providers. It is the ONE
// place enablement/config verbs derive (scope,org,user,provider,label).
func extIdentity(s *cloud.Service[state], c *zip.Ctx) (scope, org, user, provider, label string, p *Provider, err error) {
	org, ok := principal.Org(c)
	if !ok {
		return "", "", "", "", "", nil, zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return "", "", "", "", "", nil, zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	pid, lbl, _ := strings.Cut(idParam(c), ":")
	pid = strings.TrimSpace(pid)
	p, ok = resolveProvider(s, pid)
	if !ok {
		return "", "", "", "", "", nil, zip.ErrNotFound("unknown connector")
	}
	scope = scopeOf(p)
	if scope == userScope {
		user = strings.Clone(strings.TrimSpace(c.User()))
		if !validUser(user) {
			return "", "", "", "", "", nil, zip.ErrBadRequest("invalid user id")
		}
		label = strings.TrimSpace(lbl)
		if label == "" {
			label = defaultLabel
		} else if !validLabel(label) {
			return "", "", "", "", "", nil, zip.ErrBadRequest("label must be 1-64 of [A-Za-z0-9._-]")
		}
		return scope, org, user, p.ID, label, p, nil
	}
	// org plane: mutating an admin-only connector requires OWN-org admin.
	if p.AdminOnly && !principal.IsOrgAdmin(c) {
		return "", "", "", "", "", nil, zip.ErrForbidden("configuring the " + p.ID + " connector requires org admin")
	}
	return scope, org, "", p.ID, "", p, nil
}

func enableExt(s *cloud.Service[state], c *zip.Ctx) error  { return setEnablement(s, c, true) }
func disableExt(s *cloud.Service[state], c *zip.Ctx) error { return setEnablement(s, c, false) }

func setEnablement(s *cloud.Service[state], c *zip.Ctx, enabled bool) error {
	scope, org, user, provider, label, _, err := extIdentity(s, c)
	if err != nil {
		return err
	}
	if err := s.State.store.SetExtEnabled(c.Context(), scope, org, user, provider, label, enabled); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "config: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"id": connectorID(provider, label, scope), "enabled": enabled})
}

// maxConfigLen bounds an extension's config blob. Config is small tuning (routing
// prefs, plugin knobs); anything over 16 KiB is hostile and refused before store.
const maxConfigLen = 16 * 1024

// configureExt sets the opaque per-extension config object. The body IS the config
// object (not wrapped) — re-marshalled compact so the stored row is always valid
// JSON, bounded so a hostile blob can't bloat the store.
//
// NON-SECRET by contract: config is echoed by getExtension, readable by any org
// member (config is NOT admin-gated on a non-AdminOnly provider), so a credential
// must NEVER be placed here — credentials go to KMS via connect, custody stays in
// KMS. This axis is tuning only (routing prefs, region, plugin knobs).
func configureExt(s *cloud.Service[state], c *zip.Ctx) error {
	scope, org, user, provider, label, _, err := extIdentity(s, c)
	if err != nil {
		return err
	}
	if len(c.Body()) > maxConfigLen {
		return zip.ErrBadRequest("config too large")
	}
	var cfg map[string]any
	if len(c.Body()) > 0 {
		if err := json.Unmarshal(c.Body(), &cfg); err != nil {
			return zip.ErrBadRequest("config must be a JSON object")
		}
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	blob, err := json.Marshal(cfg)
	if err != nil {
		return zip.ErrBadRequest("config must be a JSON object")
	}
	if err := s.State.store.SetExtConfig(c.Context(), scope, org, user, provider, label, string(blob)); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "config: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"id": connectorID(provider, label, scope), "config": cfg})
}

// ── enablement gate (the load-bearing consumer of the enablement axis) ──────────

// gateEnabled fails CLOSED: a disabled extension, or a store error while checking,
// denies the token custody exit. A missing config row is enabled-by-default, so an
// already-connected connector is unaffected until explicitly disabled.
func gateEnabled(ctx context.Context, s *cloud.Service[state], scope, org, user, provider, label string) error {
	cfg, found, err := s.State.store.GetExtConfig(ctx, scope, org, user, provider, label)
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "enablement check failed")
	}
	if found && !cfg.Enabled {
		return zip.ErrForbidden("connector is disabled")
	}
	return nil
}

// ── helpers ─────────────────────────────────────────────────────────────────────

// resolveProvider resolves a provider by slug across BOTH planes — the unified
// surface reads the plane FROM the provider (scopeOf), it does not pre-select one.
func resolveProvider(s *cloud.Service[state], provider string) (*Provider, bool) {
	p, ok := s.State.providers[provider]
	return p, ok
}

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

// providerOf returns the provider slug from a connector id (the part before ':').
func providerOf(id string) string {
	p, _, _ := strings.Cut(strings.TrimSpace(id), ":")
	return strings.TrimSpace(p)
}

// connectorID is the client-facing id echoed by the config verbs: the bare
// provider on the org plane, provider:label on the user plane.
func connectorID(provider, label, scope string) string {
	if scope == userScope && label != "" {
		return provider + ":" + label
	}
	return provider
}

// connectorRoutes registers the ONE unified connect+configure plane. Static and
// shallow routes register before deeper wildcards so registration-order matching
// never shadows a sub-verb with the bare /:id.
func connectorRoutes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/connectors", cloud.Handle(s, listExtensions))
	// connect / lifecycle sub-verbs (deeper than /:id — registered first).
	app.Post("/v1/connectors/:id/connect", cloud.Handle(s, connectExt))
	app.Get("/v1/connectors/:id/callback", cloud.Handle(s, callback))
	app.Post("/v1/connectors/:id/verify", cloud.Handle(s, verifyExt))
	app.Post("/v1/connectors/:id/device", cloud.Handle(s, startDevice))
	app.Post("/v1/connectors/:id/device/:flow/poll", cloud.Handle(s, pollDevice))
	app.Get("/v1/connectors/:id/token", cloud.Handle(s, tokenConn))
	app.Post("/v1/connectors/:id/refresh", cloud.Handle(s, refreshConn))
	app.Post("/v1/connectors/:id/enable", cloud.Handle(s, enableExt))
	app.Post("/v1/connectors/:id/disable", cloud.Handle(s, disableExt))
	app.Patch("/v1/connectors/:id/config", cloud.Handle(s, configureExt))
	app.Delete("/v1/connectors/:id", cloud.Handle(s, forgetExt))
	// bare /:id last so the sub-verbs above win.
	app.Get("/v1/connectors/:id", cloud.Handle(s, getExtension))
}
