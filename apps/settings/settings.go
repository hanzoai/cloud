package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/hanzoai/cloud/sdk/text"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	// maxConfig bounds a stored non-secret config document.
	maxConfig = 64 * 1024
	// maxSecretValue bounds a single secret value routed to KMS.
	maxSecretValue = 8 * 1024
	// maxSecretKeys bounds how many secret fields one (org,product) may hold.
	maxSecretKeys = 64
)

// productRE constrains the product identifier: it is a console catalog slug that
// becomes a store key segment and a KMS ref segment, so this is the boundary guard.
// It matches the k8s label shape (lowercase DNS-ish).
var productRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// secretMask is what the read path returns in place of a secret value; a PUT that
// echoes it back means "unchanged", so the real value is never round-tripped.
const secretMask = "••••••••"

// service is the composition root for the settings surface. It owns the settings
// store; KMS is nil ⇒ secret writes fail closed (never plaintext).
type service struct {
	store *SettingsStore
	kms   secret.Store
	log   luxlog.Logger
	// trail records every write to the platform's own configuration (fleet_rpc.go).
	trail *audit.Recorder
}

var mounted *service

// Mount registers the settings surface on app per HIP-0106.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("settings: nil app")
	}
	log := luxlog.Default().New("subsystem", "settings")
	if cloud.DataDir() == "" {
		return fmt.Errorf("settings.Use:  empty DataDir")
	}
	store, err := openSettingsStore(cloud.DataDir())
	if err != nil {
		return fmt.Errorf("settings.Use:  open settings store: %w", err)
	}
	s := &service{store: store, kms: deps.KMS, log: log, trail: deps.Audit}
	mounted = s

	routes(app, s)
	exposeFleet(s)

	log.Info("settings surface mounted", "prefix", "/v1/settings", "brand", cloud.Brand(), "kms", deps.KMS != nil)
	return nil
}

// routes is the ONE place the surface is wired, so a test drives the same router
// the binary serves rather than a reconstruction of it.
func routes(app cloud.Router, s *service) {
	// cloud.Bridge parks the validated org on the context a typed op receives; it
	// is the composer's install — once at the root of every program — so this
	// package does not install its own.
	o := settingsOps{s: s}
	g := app.Group("/v1/settings")
	g.Get("/:product", o.getSettings)
	g.Put("/:product", o.putSettings)
}

// settingsOps is the receiver the settings ops hang off. A method value is the only
// bound form cmd/zipdoc can lift prose from, so ops are methods and not closures.
type settingsOps struct{ s *service }

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Shutdown releases the settings store. Idempotent.
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

// ZDREnabled reports whether org has Zero Data Retention (ZDR) or strict
// non-data-shared model filtering enabled in its privacy or platform-console
// configuration. Safe to call when settings is unmounted (returns false).
func ZDREnabled(ctx context.Context, org string) bool {
	zdr, _ := Privacy(ctx, org)
	return zdr
}

// Privacy is org's data-handling policy as its privacy, platform-console and models
// settings state it, each product read once: zdr when any of them turns on Zero Data
// Retention (zdrEnabled, enforceZdrModelsOnly, a retentionDays or
// chatProjectRetentionDays of "0"), and residency, the region its data must stay in
// (dataResidency, lower case; the first product that states one), "" for none. The
// model gate reads it before a request reaches a route that keeps data or runs
// somewhere (apps/ai/capability). Safe to call when settings is unmounted (nothing).
func Privacy(ctx context.Context, org string) (zdr bool, residency string) {
	if mounted == nil || mounted.store == nil || strings.TrimSpace(org) == "" {
		return false, ""
	}
	for _, product := range []string{"privacy", "platform-console", "models"} {
		if zdr && residency != "" {
			break
		}
		st, err := mounted.store.Get(ctx, org, product)
		if err != nil || len(st.Config) == 0 {
			continue
		}
		var cfg map[string]any
		if json.Unmarshal([]byte(st.Config), &cfg) != nil {
			continue
		}
		if b, ok := cfg["zdrEnabled"].(bool); ok && b {
			zdr = true
		}
		if b, ok := cfg["enforceZdrModelsOnly"].(bool); ok && b {
			zdr = true
		}
		if s, ok := cfg["retentionDays"].(string); ok && strings.TrimSpace(s) == "0" {
			zdr = true
		}
		if s, ok := cfg["chatProjectRetentionDays"].(string); ok && strings.TrimSpace(s) == "0" {
			zdr = true
		}
		if s, ok := cfg["dataResidency"].(string); ok && residency == "" {
			residency = strings.ToLower(strings.TrimSpace(s))
		}
	}
	return zdr, residency
}

// ── tenant ───────────────────────────────────────────────────────────────────

// tenant resolves the org — the tenant-isolation KEY — for a VALIDATED principal
// only, from the context cloud.Bridge parked it on. Fails closed (caller answers
// 403) for an unvalidated or org-less request. It is NEVER an In field: an In field
// is caller-supplied, so a tenant key read from one is a cross-tenant read the
// caller asserted for itself.
func (s *service) tenant(ctx context.Context) (string, bool) { return principal.OrgFrom(ctx) }

// requireProduct validates the :product path segment against productRE. The value
// arrives on the In, bound from the PATH last (zip binds body, then query, then
// path), so the URL names the product whatever a body claims.
func requireProduct(product string) (string, error) {
	p := strings.TrimSpace(product)
	if p == "" {
		return "", zip.ErrBadRequest("product is required")
	}
	if !productRE.MatchString(p) {
		return "", zip.ErrBadRequest("product must match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	return p, nil
}

// ── settings CRUD ────────────────────────────────────────────────────────────

// View is one (org, product) configuration as the console renders it.
type View struct {
	// Product is the catalog slug this configuration belongs to.
	Product string `json:"product"`
	// Config is the product's non-secret configuration, an opaque JSON object the
	// server stores and returns verbatim. `{}` when nothing has been saved.
	Config json.RawMessage `json:"config"`
	// SecretKeys names the secret fields that ARE set. Their VALUES live only in KMS
	// and are never returned here — the console renders a mask.
	SecretKeys []string `json:"secretKeys"`
	// UpdatedAt is when this configuration was last written, RFC 3339 UTC. Empty
	// when nothing has been saved.
	UpdatedAt string `json:"updatedAt"`
	// CreatedAt is when this configuration was first written, RFC 3339 UTC.
	CreatedAt string `json:"createdAt"`
}

// productIn addresses one product's configuration. The product comes from the PATH.
type productIn struct {
	// Product is the catalog slug, from the path. Must match ^[a-z0-9][a-z0-9._-]{0,62}$.
	Product string `json:"product"`
}

// GetSettings reads the caller org's configuration for one product, with every
// secret field MASKED — only the names of the set secrets come back, never their
// values, which live in KMS. A product the org has never configured is not a 404:
// it answers 200 with an empty config object, so the console's Settings tab always
// renders and merges its own display defaults on top.
func (o settingsOps) getSettings(ctx context.Context, in *productIn) (*View, error) {
	s := o.s
	org, ok := s.tenant(ctx)
	if !ok {
		return nil, principal.RefusedFrom(ctx)
	}
	product, err := requireProduct(in.Product)
	if err != nil {
		return nil, err
	}
	v, err := s.read(ctx, org, product)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// read is the org-EXPLICIT read core both the self-service op (org from the
// validated principal) and the cross-tenant admin path (org from the SuperAdmin's
// URL) share, so the masking and the empty-is-not-404 rule are decided in ONE place
// whatever supplied the org. product is already validated by the caller.
func (s *service) read(ctx context.Context, org, product string) (View, error) {
	st, err := s.store.Get(ctx, org, product)
	if err == errNotFound {
		// No override yet — an honest empty config (the console merges its own
		// display defaults). Not a 404: the tab always renders.
		return View{Product: product, Config: json.RawMessage(`{}`), SecretKeys: []string{}}, nil
	}
	if err != nil {
		return View{}, zip.Errorf(http.StatusInternalServerError, "get settings: %v", err)
	}
	return toView(st), nil
}

// settingsReq is the write body plus the product the URL named.
type settingsReq struct {
	// Product is the catalog slug, from the PATH. zip binds the path last, so the
	// URL names the product being written whatever a body field claims.
	Product string `json:"product"`
	// Config is the product's non-secret configuration, stored verbatim. Bounded at
	// 64 KiB once serialized. Omit it to store an empty object.
	Config map[string]any `json:"config"`
	// Secrets are the secret fields, by name. Each VALUE is sealed into KMS and
	// never reaches this deployment's database; a value that is empty or equal to
	// the mask the read path returns means "unchanged" and is skipped, so a console
	// round-trip cannot blank a stored secret. A key must match
	// ^[a-z0-9][a-z0-9._-]{0,62}$, a value is bounded at 8 KiB, and an org may hold
	// at most 64 secret fields per product.
	Secrets map[string]string `json:"secrets"`
}

// PutSettings writes the caller org's configuration for one product and answers the
// stored result, secrets masked. Secret VALUES are sealed into KMS under
// orgs/{org}/settings/{product}/{key} and never touch this deployment's database;
// with no KMS configured a write that carries any secret is refused whole (503)
// rather than dropping it or persisting it in the clear. A secret the body omits
// keeps its stored value, so a partial write never silently clears one.
func (o settingsOps) putSettings(ctx context.Context, in *settingsReq) (*View, error) {
	s := o.s
	org, ok := s.tenant(ctx)
	if !ok {
		return nil, principal.RefusedFrom(ctx)
	}
	product, err := requireProduct(in.Product)
	if err != nil {
		return nil, err
	}
	v, err := s.write(ctx, org, product, in.Config, in.Secrets)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// write is the org-EXPLICIT write core both the self-service op (org from the
// validated principal) and the cross-tenant admin path (org from the SuperAdmin's
// URL) share. Every custody rule lives here and only here — the config cap, the
// KMS-or-fail-closed secret routing to orgs/{org}/settings/{product}/{key}, the
// omit-keeps-a-stored-secret rule — so the two entry points cannot drift. product is
// already validated by the caller.
func (s *service) write(ctx context.Context, org, product string, config map[string]any, secrets map[string]string) (View, error) {
	// Non-secret config: validate + serialize within the cap.
	cfgJSON := []byte("{}")
	if config != nil {
		b, mErr := json.Marshal(config)
		if mErr != nil {
			return View{}, zip.ErrBadRequest("config must be JSON-serializable")
		}
		if len(b) > maxConfig {
			return View{}, zip.ErrBadRequest("config too large (max 64KiB)")
		}
		cfgJSON = b
	}

	// Existing secret-key set (so a PUT that omits a secret keeps it).
	var secretKeys []string
	if prev, gErr := s.store.Get(ctx, org, product); gErr == nil {
		secretKeys = prev.SecretKeys
	} else if gErr != errNotFound {
		return View{}, zip.Errorf(http.StatusInternalServerError, "load settings: %v", gErr)
	}

	// Route each provided secret to KMS. A secret VALUE never touches SQLite; if KMS
	// is unavailable, the whole write fails closed rather than dropping or (worse)
	// persisting the secret in plaintext.
	if len(secrets) > 0 {
		if s.kms == nil {
			return View{}, zip.Errorf(http.StatusServiceUnavailable, "settings: KMS not configured; refusing to store secrets")
		}
		for key, val := range secrets {
			if !productRE.MatchString(key) {
				return View{}, zip.ErrBadRequest("secret key must match ^[a-z0-9][a-z0-9._-]{0,62}$")
			}
			if val == "" || val == secretMask {
				// Empty / mask sentinel = "unchanged" — never overwrite a stored secret
				// with a blank or the mask the read path returned.
				continue
			}
			if len(val) > maxSecretValue {
				return View{}, zip.ErrBadRequest("secret value too large (max 8KiB)")
			}
			ref := secretRef(org, product, key)
			if err := s.kms.PutSecret(ctx, ref, []byte(val)); err != nil {
				return View{}, zip.Errorf(http.StatusInternalServerError, "kms put secret: %v", err)
			}
			secretKeys = addStr(secretKeys, key)
		}
		if len(secretKeys) > maxSecretKeys {
			return View{}, zip.ErrBadRequest("too many secret fields (max 64)")
		}
	}

	now := time.Now().Unix()
	st, err := s.store.Put(ctx, Settings{
		Org: org, Product: product, Config: string(cfgJSON), SecretKeys: secretKeys, UpdatedAt: now,
	})
	if err != nil {
		return View{}, zip.Errorf(http.StatusInternalServerError, "persist settings: %v", err)
	}
	return toView(st), nil
}

func toView(st Settings) View {
	cfg := json.RawMessage(st.Config)
	if len(cfg) == 0 {
		cfg = json.RawMessage(`{}`)
	}
	keys := st.SecretKeys
	if keys == nil {
		keys = []string{}
	}
	return View{
		Product:    st.Product,
		Config:     cfg,
		SecretKeys: keys,
		UpdatedAt:  text.Unix(st.UpdatedAt),
		CreatedAt:  text.Unix(st.CreatedAt),
	}
}

// secretRef is the KMS ref for a settings secret. Org + product are already
// validated (owner claim / productRE); key is validated at the call site.
func secretRef(org, product, key string) string {
	return "orgs/" + org + "/settings/" + product + "/" + key
}

// ── shared helpers ───────────────────────────────────────────────────────────

func encodeStrList(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeStrList(s string) []string {
	if s == "" {
		return nil
	}
	var xs []string
	if err := json.Unmarshal([]byte(s), &xs); err != nil {
		return nil
	}
	return xs
}

func addStr(xs []string, x string) []string {
	if slices.Contains(xs, x) {
		return xs
	}
	return append(xs, x)
}
