// Package settings is the per-org, per-product configuration plane for the unified
// Hanzo Cloud binary: the /v1/settings/:product surface behind every product's
// detail view in console.hanzo.ai (#59). It lets an org read and edit a product's
// configuration, backed by a durable per-tenant SQLite store with KMS custody for
// any secret-typed field.
//
// ONE settings engine, EVERY product. The console drives all products' Settings tab
// through this single surface (product id → :product). There is no per-product
// bespoke server code — a product is just an (org, product) key.
//
// Surface (all org-scoped; /v1 only):
//
//	GET /v1/settings/:product   org config (read, secrets masked)  -> settingsView
//	PUT /v1/settings/:product   org config (write)                 -> settingsView
//
// TENANT ISOLATION is enforced SERVER-SIDE on every request. The org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner (HIP-0026) — and is NEVER read from a query param, body, or client header.
// It is the mandatory predicate on every store statement. The client chooses a
// PRODUCT (validated against a slug shape); it never supplies the org.
//
// SECRET CUSTODY. A secret field's VALUE lives ONLY in KMS at
// orgs/{org}/settings/{product}/{key}; the store keeps only the non-secret JSON plus
// the list of secret key NAMES (so the read path knows which fields are set-but-
// masked). A plaintext secret can never reach SQLite — a secret write routes to KMS
// or fails closed (503).
//
// This surface was previously mounted by clients/observe alongside the o11y read
// paths; those reads now live in clients/o11y (the embedded runtime). Settings is
// NOT observability — it is console product-detail config — so it is its own plane
// here, its behavior preserved verbatim from the observe original.
package settings

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
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
	kms   cloud.KMSClient
	log   luxlog.Logger
}

var mounted *service

// Mount registers the settings surface on app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("settings.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("settings.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "settings")
	if deps.DataDir == "" {
		return fmt.Errorf("settings.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("settings.Mount: data dir: %w", err)
	}
	store, err := openSettingsStore(filepath.Join(deps.DataDir, "settings.db"))
	if err != nil {
		return fmt.Errorf("settings.Mount: open settings store: %w", err)
	}
	s := &service{store: store, kms: deps.KMS, log: log}
	mounted = s

	// The bridge FIRST: a typed op receives only its context and its decoded
	// input, so the request the org is derived from crosses here — and fiber runs
	// middleware in registration order, so one installed after the leaves it
	// serves would never run.
	app.Group("/v1/settings").Use(cloud.Bridge())
	routes(cloud.ZipApp(app), s)

	log.Info("settings surface mounted", "prefix", "/v1/settings", "brand", deps.Brand, "kms", deps.KMS != nil)
	return nil
}

// routes registers the settings surface as zip TYPED ops, so the declaration the
// router reads is the one the OpenAPI document, the MCP tool list and the CLI
// read too. The product rides the path; the org never does.
func routes(z *zip.App, s *service) {
	zip.Get(z, "/v1/settings/:product", s.settings)
	zip.Put(z, "/v1/settings/:product", s.saveSettings)
}

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

// ── tenant ───────────────────────────────────────────────────────────────────

// tenant resolves the org — the tenant-isolation KEY — for a VALIDATED principal
// only. Fails closed (caller answers 403) for an unvalidated or org-less request.
func (s *service) tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// requireProduct validates the :product path segment against productRE. It is
// the ONE boundary guard for the value that becomes a store key and a KMS ref
// segment, shared by both verbs.
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

type settingsView struct {
	// Product is the console catalog slug this configuration belongs to.
	Product string `json:"product"`
	// Config is the stored non-secret configuration, verbatim.
	// A product with no override yet reads an empty object, not a 404.
	Config json.RawMessage `json:"config"`
	// SecretKeys names the secret fields that are SET.
	// Only the names appear here; a secret's value lives in KMS and is never
	// returned by any read.
	SecretKeys []string `json:"secretKeys"`
	// UpdatedAt is RFC 3339, empty until the first write.
	UpdatedAt string `json:"updatedAt"`
	// CreatedAt is RFC 3339, empty until the first write.
	CreatedAt string `json:"createdAt"`
}

// settingsRef addresses one product's configuration. The org is NOT here and can
// never be: it is the validated principal's tenant, and an input field is
// caller-supplied.
type settingsRef struct {
	// Product is the console catalog slug from the :product path segment.
	// It must match ^[a-z0-9][a-z0-9._-]{0,62}$ — it becomes a store key and a
	// KMS ref segment.
	Product string `json:"product"`
}

// settings returns one product's configuration for the caller's org.
//
// Secret fields are reported by NAME only, in secretKeys; their values live in
// KMS and are never returned.
// A product the org has never configured reads an empty config rather than a
// 404, because the console's Settings tab has to render either way.
//
// Example: {"product": "gateway"}
// Response: {"product": "gateway", "config": {"region": "sfo3"}, "secretKeys": ["api-token"], "updatedAt": "2026-07-29T00:00:00Z", "createdAt": "2026-07-01T00:00:00Z"}
func (s *service) settings(ctx context.Context, in *settingsRef) (*settingsView, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("X-Org-Id required")
	}
	org, ok := s.tenant(c)
	if !ok {
		return nil, zip.ErrForbidden("X-Org-Id required")
	}
	product, err := requireProduct(in.Product)
	if err != nil {
		return nil, err
	}
	st, err := s.store.Get(ctx, org, product)
	if err == errNotFound {
		// No override yet — an honest empty config (the console merges its own
		// display defaults). Not a 404: the tab always renders.
		return &settingsView{
			Product: product, Config: json.RawMessage(`{}`), SecretKeys: []string{},
		}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get settings: %v", err)
	}
	v := toSettingsView(st)
	return &v, nil
}

type settingsReq struct {
	// Product is the console catalog slug from the :product path segment.
	// It must match ^[a-z0-9][a-z0-9._-]{0,62}$ — it becomes a store key and a
	// KMS ref segment.
	Product string `json:"product"`
	// Config is the non-secret configuration, stored verbatim and capped at 64KiB.
	// Omit it to store an empty object; it REPLACES what was there.
	Config map[string]any `json:"config"`
	// Secrets carries secret fields by name.
	// Each VALUE is routed to KMS and never reaches SQLite; a key omitted here
	// keeps its stored value, and sending back the mask the read returned means
	// "unchanged".
	Secrets map[string]string `json:"secrets"`
}

// saveSettings writes one product's configuration for the caller's org.
//
// Non-secret config is stored verbatim within a 64KiB cap.
// Every provided secret VALUE is routed to KMS at orgs/{org}/settings/{product}/{key}
// and never touches SQLite, so with no KMS configured the whole write fails
// closed rather than persisting a secret in plaintext.
// A secret omitted from the body, or echoed back as the mask the read returned,
// keeps its stored value.
//
// Example: {"product": "gateway", "config": {"region": "sfo3"}, "secrets": {"api-token": "hk-live-..."}}
func (s *service) saveSettings(ctx context.Context, in *settingsReq) (*settingsView, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("X-Org-Id required")
	}
	org, ok := s.tenant(c)
	if !ok {
		return nil, zip.ErrForbidden("X-Org-Id required")
	}
	product, err := requireProduct(in.Product)
	if err != nil {
		return nil, err
	}

	// Non-secret config: validate + serialize within the cap.
	cfgJSON := []byte("{}")
	if in.Config != nil {
		b, mErr := json.Marshal(in.Config)
		if mErr != nil {
			return nil, zip.ErrBadRequest("config must be JSON-serializable")
		}
		if len(b) > maxConfig {
			return nil, zip.ErrBadRequest("config too large (max 64KiB)")
		}
		cfgJSON = b
	}

	// Existing secret-key set (so a PUT that omits a secret keeps it).
	var secretKeys []string
	if prev, gErr := s.store.Get(ctx, org, product); gErr == nil {
		secretKeys = prev.SecretKeys
	} else if gErr != errNotFound {
		return nil, zip.Errorf(http.StatusInternalServerError, "load settings: %v", gErr)
	}

	// Route each provided secret to KMS. A secret VALUE never touches SQLite; if KMS
	// is unavailable, the whole write fails closed rather than dropping or (worse)
	// persisting the secret in plaintext.
	if len(in.Secrets) > 0 {
		if s.kms == nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "settings: KMS not configured; refusing to store secrets")
		}
		for key, val := range in.Secrets {
			if !productRE.MatchString(key) {
				return nil, zip.ErrBadRequest("secret key must match ^[a-z0-9][a-z0-9._-]{0,62}$")
			}
			if val == "" || val == secretMask {
				// Empty / mask sentinel = "unchanged" — never overwrite a stored secret
				// with a blank or the mask the read path returned.
				continue
			}
			if len(val) > maxSecretValue {
				return nil, zip.ErrBadRequest("secret value too large (max 8KiB)")
			}
			ref := secretRef(org, product, key)
			if err := s.kms.PutSecret(ctx, ref, []byte(val)); err != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "kms put secret: %v", err)
			}
			secretKeys = addStr(secretKeys, key)
		}
		if len(secretKeys) > maxSecretKeys {
			return nil, zip.ErrBadRequest("too many secret fields (max 64)")
		}
	}

	now := time.Now().Unix()
	st, err := s.store.Put(ctx, Settings{
		Org: org, Product: product, Config: string(cfgJSON), SecretKeys: secretKeys, UpdatedAt: now,
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist settings: %v", err)
	}
	v := toSettingsView(st)
	return &v, nil
}

func toSettingsView(st Settings) settingsView {
	cfg := json.RawMessage(st.Config)
	if len(cfg) == 0 {
		cfg = json.RawMessage(`{}`)
	}
	keys := st.SecretKeys
	if keys == nil {
		keys = []string{}
	}
	return settingsView{
		Product:    st.Product,
		Config:     cfg,
		SecretKeys: keys,
		UpdatedAt:  rfc3339(st.UpdatedAt),
		CreatedAt:  rfc3339(st.CreatedAt),
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
	for _, v := range xs {
		if v == x {
			return xs
		}
	}
	return append(xs, x)
}

func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}
