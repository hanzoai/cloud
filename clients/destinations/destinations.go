package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/analytics"
	"github.com/hanzoai/cloud/clients/kms"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// destinations.go mounts the /v1/destinations surface and owns the KMS custody + the
// in-process seam. It composes clients/analytics (installs the fan-out sink) and
// clients/integrations (a destination may reuse an OAuth connection's token).
//
// Surface (all org-scoped; /v1 only; mutations require org admin):
//
//	GET    /v1/destinations                 platforms + this org's connection status
//	GET    /v1/destinations/:platform       one platform status
//	POST   /v1/destinations/:platform       connect/update (ids + secret) -> Status
//	DELETE /v1/destinations/:platform       disconnect (forget secrets + row)
//	POST   /v1/destinations/:platform/test  send ONE synthetic event end-to-end
//
// TENANT ISOLATION mirrors clients/ads and clients/integrations: the org is
// principal.Org (a VALIDATED principal, never a client header); every store row is
// keyed (org,platform) and every KMS secret lives under a per-org path.

const (
	// kmsEnv is the stable KMS environment slug destinations secrets are sealed
	// under (destinations are per-org, not per-env — the integrations convention).
	kmsEnv = "default"
	// maxSecret bounds a destination API secret at intake. Real tokens are short;
	// anything over 8 KiB is hostile and refused before it reaches KMS.
	maxSecret = 8192
	// maxField bounds a non-secret config value (a measurement/pixel id is short).
	maxField = 512
	// maxFanout bounds concurrent fan-out work so a burst of ingest cannot spawn
	// unbounded HTTP to the platforms; excess batches are dropped (fail-soft).
	maxFanout = 32
	// sendTimeout bounds one destination Send in the fan-out.
	sendTimeout = 15 * time.Second
	// publicFanoutEnv turns the analytics fan-out OFF when falsey. Default ON: a
	// mounted destinations subsystem forwards a connected org's events.
	publicFanoutEnv = "CLOUD_DESTINATIONS_FANOUT"
)

// state is destinations' own data; shared deps live in the embedded cloud.Base.
type state struct {
	store *Store
	kms   *kms.Client // type-asserted from deps.KMS; nil ⇒ secret ops fail closed
	dests map[string]Destination
	sem   chan struct{} // fan-out concurrency bound
}

// mounted is the active service so Shutdown + the in-process seam reach it.
var mounted *cloud.Service[state]

// Mount wires /v1/destinations/* onto app. Complex flavour (a package global for the
// seam + Shutdown, and it installs the analytics fan-out sink), so it constructs the
// Service value directly.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("destinations.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("destinations.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("destinations.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("destinations.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "destinations.db"))
	if err != nil {
		return fmt.Errorf("destinations.Mount: open store: %w", err)
	}
	kc, _ := deps.KMS.(*kms.Client)
	b := cloud.NewBase(deps, "destinations")
	s := &cloud.Service[state]{Base: b, State: state{
		store: store,
		kms:   kc,
		dests: snapshot(),
		sem:   make(chan struct{}, maxFanout),
	}}
	mounted = s

	routes(app, s)

	// Install the fan-out sink onto the canonical event plane, unless disabled.
	if fanoutEnabled() {
		analytics.SetSink(func(org string, evs []analytics.SinkEvent) { consume(s, org, evs) })
		b.Log.Info("destinations fan-out sink installed", "platforms", len(s.State.dests))
	} else {
		b.Log.Info("destinations fan-out disabled", "flag", publicFanoutEnv)
	}
	b.Log.Info("destinations mounted", "platforms", len(s.State.dests), "kmsReady", kmsReady(s), "brand", deps.Brand)
	return nil
}

func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/destinations")
	g.Get("", cloud.Handle(s, list))
	g.Get("/:platform", cloud.Handle(s, get))
	g.Post("/:platform", cloud.Handle(s, connect))
	g.Delete("/:platform", cloud.Handle(s, disconnect))
	g.Post("/:platform/test", cloud.Handle(s, test))
}

// Shutdown closes the store and clears the sink. Idempotent.
func Shutdown() error {
	analytics.SetSink(nil)
	if mounted == nil || mounted.State.store == nil {
		mounted = nil
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

// fanoutEnabled reports whether the analytics fan-out is on (default ON).
func fanoutEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(publicFanoutEnv))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// ── shared helpers ───────────────────────────────────────────────────────────

func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

func platformParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("platform")) }

// validOrg mirrors clients/integrations: the org is folded into the KMS secret path
// and the store key, so it is validated strictly at every custody boundary.
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

// clip trims + bounds a non-secret text field.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxField {
		return s[:maxField]
	}
	return s
}

// toStr coerces a decoded JSON value to a trimmed string (numbers → their literal;
// non-scalars → ""). Lets the connect body carry an id as a string or a number.
func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

// camelOf converts a snake_case KMS secret name to the camelCase connect-body key
// (api_secret → apiSecret), so the body reads naturally while KMS keeps snake names.
func camelOf(s string) string {
	parts := strings.Split(s, "_")
	if len(parts) == 1 {
		return s
	}
	var b strings.Builder
	b.WriteString(parts[0])
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}

// ── KMS custody (per-org, sealed) ────────────────────────────────────────────

func kmsReady(s *cloud.Service[state]) bool { return s.State.kms != nil && s.State.kms.Ready() }

// kmsPath is the per-org, per-platform KMS namespace. org is validOrg-checked at
// every entry point; platform is a fixed registry slug.
func kmsPath(org, platform string) string { return "/orgs/" + org + "/destinations/" + platform }

func kmsGet(s *cloud.Service[state], path, name string) ([]byte, error) {
	return s.State.kms.Get(path, name, kmsEnv)
}

func kmsPut(s *cloud.Service[state], path, name string, value []byte) error {
	return s.State.kms.Put(path, name, kmsEnv, value)
}

func kmsDelete(s *cloud.Service[state], path, name string) error {
	err := s.State.kms.Delete(path, name, kmsEnv)
	if errors.Is(err, kms.ErrSecretNotFound) {
		return nil // idempotent
	}
	return err
}

// sealSecrets seals every provided secret at path. Logs the secret NAME on failure,
// never a value.
func sealSecrets(s *cloud.Service[state], path string, secrets map[string]string) error {
	for name, val := range secrets {
		if err := kmsPut(s, path, name, []byte(val)); err != nil {
			s.Log.Warn("destinations kms seal failed", "path", path, "secret", name, "err", err)
			return err
		}
	}
	return nil
}

// ── views ────────────────────────────────────────────────────────────────────

// Status is a destination's card for an org: its Spec (fields the console renders),
// this org's connection state, and whether a credential is resolvable (live).
type Status struct {
	Platform  string   `json:"platform"`
	Name      string   `json:"name"`
	Category  string   `json:"category"`
	Connected bool     `json:"connected"`
	Enabled   bool     `json:"enabled"`
	Live      bool     `json:"live"`
	Account   string   `json:"account,omitempty"`
	Config    Config   `json:"config,omitempty"`
	Fields    []Field  `json:"fields"`
	Secrets   []string `json:"secrets"`
}

// statusOf builds the card for a destination, folding in the org's live row (row may
// be nil when not connected). Live reflects whether a credential resolves NOW.
func statusOf(s *cloud.Service[state], ctx context.Context, org string, dest Destination, row *Row) Status {
	spec := dest.Spec()
	st := Status{
		Platform: dest.ID(), Name: dest.Name(), Category: dest.Category(),
		Fields: spec.Fields, Secrets: spec.Secrets,
	}
	if row != nil {
		st.Connected = true
		st.Enabled = row.Enabled
		st.Account = row.AccountLabel
		st.Config = row.Config
		_, err := resolveSecret(s, org, dest, row.Config)
		st.Live = err == nil
	}
	return st
}

// ── handlers ─────────────────────────────────────────────────────────────────

func list(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	rows, err := s.State.store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	byPlatform := make(map[string]Row, len(rows))
	for _, r := range rows {
		byPlatform[r.Platform] = r
	}
	out := make([]Status, 0, len(s.State.dests))
	for _, id := range sortedIDs(s.State.dests) {
		dest := s.State.dests[id]
		if r, ok := byPlatform[id]; ok {
			out = append(out, statusOf(s, c.Context(), org, dest, &r))
		} else {
			out = append(out, statusOf(s, c.Context(), org, dest, nil))
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"destinations": out})
}

func get(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	dest, ok := s.State.dests[platformParam(c)]
	if !ok {
		return zip.ErrNotFound("unknown destination")
	}
	row, found, err := s.State.store.Get(c.Context(), org, dest.ID())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if found {
		return c.JSON(http.StatusOK, statusOf(s, c.Context(), org, dest, &row))
	}
	return c.JSON(http.StatusOK, statusOf(s, c.Context(), org, dest, nil))
}

// connect provisions (or updates) a destination: non-secret ids into the store, API
// secret(s) sealed to KMS (fail-closed). Connecting an ad destination is an org-admin
// action (parity with the integrations AdminOnly discipline). The secret never
// appears in the response, the store, or a log line.
func connect(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	dest, ok := s.State.dests[platformParam(c)]
	if !ok {
		return zip.ErrNotFound("unknown destination")
	}
	if !principal.IsOrgAdmin(c) {
		return zip.ErrForbidden("connecting a destination requires org admin")
	}
	if !kmsReady(s) {
		return zip.Errorf(http.StatusServiceUnavailable, "%s", kms.ErrMasterKeyMissing.Error())
	}
	var body map[string]any
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	spec := dest.Spec()
	cfg := Config{}
	for _, f := range spec.Fields {
		v := clip(toStr(body[f.Key]))
		if v == "" {
			if f.Required {
				return zip.ErrBadRequest(dest.ID() + ": " + f.Key + " is required")
			}
			continue
		}
		cfg[f.Key] = v
	}
	secrets := map[string]string{}
	for _, name := range spec.Secrets {
		v := strings.TrimSpace(firstNonEmpty(toStr(body[camelOf(name)]), toStr(body[name])))
		if v == "" {
			continue
		}
		if len(v) > maxSecret {
			return zip.ErrBadRequest("credential too large")
		}
		secrets[name] = v
	}
	if len(secrets) > 0 {
		if err := sealSecrets(s, kmsPath(org, dest.ID()), secrets); err != nil {
			return zip.Errorf(http.StatusServiceUnavailable, "secret custody failed")
		}
	}
	enabled := true
	if v, ok := body["enabled"].(bool); ok {
		enabled = v
	}
	row := Row{Org: org, Platform: dest.ID(), Enabled: enabled, Config: cfg, AccountLabel: clip(toStr(body["account"]))}
	if err := s.State.store.Upsert(c.Context(), row); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	s.Log.Info("destination connected", "platform", dest.ID(), "org", org, "enabled", enabled)
	return c.JSON(http.StatusOK, statusOf(s, c.Context(), org, dest, &row))
}

// disconnect forgets a destination: every custodied KMS secret, then the row.
// Idempotent. Org-admin.
func disconnect(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	dest, ok := s.State.dests[platformParam(c)]
	if !ok {
		return zip.ErrNotFound("unknown destination")
	}
	if !principal.IsOrgAdmin(c) {
		return zip.ErrForbidden("disconnecting a destination requires org admin")
	}
	if s.State.kms != nil {
		for _, name := range dest.Spec().Secrets {
			if err := kmsDelete(s, kmsPath(org, dest.ID()), name); err != nil {
				s.Log.Warn("destinations kms delete failed (continuing)", "platform", dest.ID(), "org", org, "secret", name, "err", err)
			}
		}
	}
	if _, err := s.State.store.Delete(c.Context(), org, dest.ID()); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"disconnected": true})
}

// test sends ONE synthetic event through the connected destination end-to-end and
// reports the platform's response as DATA (a send failure is {ok:false,error:…}, not
// an HTTP error, so the console renders it). Org-admin.
func test(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	dest, ok := s.State.dests[platformParam(c)]
	if !ok {
		return zip.ErrNotFound("unknown destination")
	}
	if !principal.IsOrgAdmin(c) {
		return zip.ErrForbidden("testing a destination requires org admin")
	}
	row, found, err := s.State.store.Get(c.Context(), org, dest.ID())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if !found {
		return zip.ErrNotFound("destination not connected")
	}
	secret, err := resolveSecret(s, org, dest, row.Config)
	if err != nil {
		return zip.ErrBadRequest("no credential is configured for this destination")
	}
	cv := syntheticConversion(org, s.Brand)
	res, serr := dest.Send(c.Context(), row.Config, secret, []Conversion{cv})
	if serr != nil {
		return c.JSON(http.StatusOK, map[string]any{"ok": false, "error": serr.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true, "sent": res.Sent, "message": res.Message})
}

// syntheticConversion builds the one test event: a pageview attributed to a stable
// per-org test visitor. It carries no PII — only the external test id.
func syntheticConversion(org, brand string) Conversion {
	host := strings.TrimSpace(brand)
	if host == "" {
		host = "hanzo"
	}
	return Conversion{
		Standard: EventPageView,
		Name:     "$pageview",
		EventID:  "hanzo_test_" + org + "_" + fmt.Sprint(time.Now().Unix()),
		Time:     time.Now(),
		URL:      "https://" + host + ".ai/",
		User:     UserData{ExternalID: "hanzo-test-" + org},
	}
}

// ── in-process seam (the guide MCP tool + siblings) ──────────────────────────

// Connect provisions (or updates) a destination's NON-SECRET config for org — the
// seam the guide's destinations_connect MCP tool drives. It NEVER accepts a secret
// (secrets flow only via the authenticated HTTP connect body → KMS), so the tool args
// and the guide action ledger never carry one. It requires the destination's REQUIRED
// non-secret fields (the guide cannot fabricate a measurement/pixel id), returning an
// honest error otherwise, and reports whether the destination is now live. Fails
// closed when unmounted / invalid org / unknown platform.
func Connect(ctx context.Context, org, platform string, in map[string]any) (Status, error) {
	if mounted == nil {
		return Status{}, fmt.Errorf("destinations: not mounted")
	}
	s := mounted
	if !validOrg(org) {
		return Status{}, fmt.Errorf("destinations: invalid org")
	}
	dest, ok := s.State.dests[strings.TrimSpace(platform)]
	if !ok {
		return Status{}, fmt.Errorf("destinations: unknown platform %q", platform)
	}
	spec := dest.Spec()
	existing, found, err := s.State.store.Get(ctx, org, dest.ID())
	if err != nil {
		return Status{}, err
	}
	cfg := Config{}
	if found {
		for k, v := range existing.Config {
			cfg[k] = v
		}
	}
	for _, f := range spec.Fields {
		if v := clip(toStr(in[f.Key])); v != "" {
			cfg[f.Key] = v
		}
	}
	for _, f := range spec.Fields {
		if f.Required && cfg[f.Key] == "" {
			return Status{}, fmt.Errorf("%s requires %s", dest.ID(), f.Key)
		}
	}
	row := Row{Org: org, Platform: dest.ID(), Enabled: true, Config: cfg}
	if found {
		row.AccountLabel = existing.AccountLabel
	}
	if err := s.State.store.Upsert(ctx, row); err != nil {
		return Status{}, err
	}
	return statusOf(s, ctx, org, dest, &row), nil
}

// List returns the org's destination status for every registered platform — the seam
// a sibling (the guide) reads to report what is connected. Fails closed when
// unmounted.
func List(ctx context.Context, org string) ([]Status, error) {
	if mounted == nil {
		return nil, fmt.Errorf("destinations: not mounted")
	}
	s := mounted
	if !validOrg(org) {
		return nil, fmt.Errorf("destinations: invalid org")
	}
	rows, err := s.State.store.List(ctx, org)
	if err != nil {
		return nil, err
	}
	byPlatform := make(map[string]Row, len(rows))
	for _, r := range rows {
		byPlatform[r.Platform] = r
	}
	out := make([]Status, 0, len(s.State.dests))
	for _, id := range sortedIDs(s.State.dests) {
		dest := s.State.dests[id]
		if r, ok := byPlatform[id]; ok {
			out = append(out, statusOf(s, ctx, org, dest, &r))
		} else {
			out = append(out, statusOf(s, ctx, org, dest, nil))
		}
	}
	return out, nil
}
