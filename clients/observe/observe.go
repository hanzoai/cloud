// Package observe mounts the Hanzo Cloud console product-detail data plane: the
// REAL, per-org Settings / Status / Logs / Metrics behind every product's detail
// view in console.hanzo.ai (#59). It replaces the stubbed/placeholder tabs with
// live data from the platform o11y stack, org-scoped server-side.
//
// Surface (all org-scoped; /v1 only):
//
//	GET /v1/o11y/logs?product=&since=&limit=&follow=   live log stream    -> {lines:[…]}
//	GET /v1/o11y/metrics?product=&range=               per-org RED+usage  -> {series:{…}}
//	GET /v1/o11y/status?product=                       live health+latency-> {status}
//	GET /v1/settings/:product                          org config (read)  -> {settings}
//	PUT /v1/settings/:product                          org config (write) -> {settings}
//
// DATA SOURCES (all in-cluster, reached from the cloud pod):
//   - Logs    : ClickHouse signoz_logs.distributed_logs_v2 (app=<product>), read via
//               the SHARED ai/object datastore client (aiobject.DatastoreQuery — one
//               connection, KMS-injected DATASTORE_* creds). A non-admin org sees its
//               OWN request log stream, derived from org-tagged spans (see logs.go).
//   - Metrics : ClickHouse signoz_traces.distributed_signoz_index_v3, filtered by the
//               org-tagged span attribute `hanzo.org` (the TracingMiddleware already
//               stamps it) → real per-org rate/errors/latency; plus per-org LLM usage
//               from hanzo.cloud_usage. No VictoriaMetrics org gap — traces carry org.
//   - Status  : a live in-cluster health probe (latency) + VictoriaMetrics up{service}.
//   - Settings: SQLite (store.go), per (org,product); secret fields → KMS, never SQLite.
//
// TENANT ISOLATION is enforced SERVER-SIDE on every request. The org is
// principal.Tenant(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner (HIP-0026) — and is NEVER read from a query param, body, or client header.
// It is bound as a positional ClickHouse parameter (never interpolated) and is the
// mandatory predicate on every SQLite statement. There is no raw-query passthrough:
// the client chooses a PRODUCT (validated against a name shape) and a bounded range,
// never the query. Only the platform admin org (IAM_ADMIN_ORG) sees unattributed
// infra logs; every other org sees only rows carrying its own org.
//
// Order 44: binds /v1/o11y/{logs,metrics,status} + /v1/settings/* BEFORE the
// hanzoai/o11y reverse-proxy surface (order 70/71) so these scoped handlers WIN
// over the proxy wildcard — closing the "proxy forwards an unscoped SigNoz query"
// isolation hole for these three read paths. serve.go auto-registers
// GET /v1/observe/health.
package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
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

	// defaultLogLimit / maxLogLimit bound a logs response (OLAP-scan safety).
	defaultLogLimit = 200
	maxLogLimit     = 1000
)

// productRE constrains the product identifier: it is a console catalog slug that
// becomes a ClickHouse app/service filter value and a KMS ref segment, so this is
// the boundary guard. It matches the k8s label shape (lowercase DNS-ish).
var productRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// productAlias maps a console product slug to its k8s workload name when they
// differ. Absent an entry, the product id IS the app/service label (identity),
// so ANY product whose slug matches a real workload works with no per-product
// code — the map only records the exceptions. Unknown products resolve to an
// honest empty result, never another product's or org's data.
var productAlias = map[string]string{
	"cloud-api": "cloud",
	"api":       "cloud",
	"llm":       "gateway",
	"router":    "gateway",
	"analytics": "insights-capture",
	"observe":   "o11y",
	"o11y":      "o11y",
	"search":    "search",
}

// serviceFor resolves the k8s workload name (the signoz `app` / VM `service`
// label, and the health-probe host) for a validated product slug.
func serviceFor(product string) string {
	if a, ok := productAlias[product]; ok {
		return a
	}
	return product
}

// service is the composition root for the console product-detail data plane. It
// owns the settings store + config for the read sources; it holds no telemetry
// connection of its own (the shared ai/object datastore client owns that).
type service struct {
	store    *SettingsStore
	kms      cloud.KMSClient // nil ⇒ secret writes fail closed (never plaintext)
	log      luxlog.Logger
	adminOrg string // IAM_ADMIN_ORG — the ONE org that may see unattributed infra logs
	vmURL    string // VictoriaMetrics base URL (up{service} status signal)
	dnsSuffix string // in-cluster service DNS suffix for the health probe
}

var mounted *service

// Mount registers the console product-detail surface on app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("observe.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("observe.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "observe")
	if deps.DataDir == "" {
		return fmt.Errorf("observe.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("observe.Mount: data dir: %w", err)
	}
	store, err := openSettingsStore(filepath.Join(deps.DataDir, "observe.db"))
	if err != nil {
		return fmt.Errorf("observe.Mount: open settings store: %w", err)
	}
	s := &service{
		store:     store,
		kms:       deps.KMS,
		log:       log,
		adminOrg:  getenvDefault("IAM_ADMIN_ORG", "admin"),
		vmURL:     getenvDefault("CLOUD_VM_URL", "http://vmsingle-victoria-metrics-single-server.hanzo.svc.cluster.local:8428"),
		dnsSuffix: getenvDefault("CLOUD_SVC_DNS_SUFFIX", ".hanzo.svc.cluster.local"),
	}
	mounted = s

	// o11y reads (scoped) — registered BEFORE the hanzoai/o11y wildcard (order 70).
	app.Get("/v1/o11y/logs", s.getLogs)
	app.Get("/v1/o11y/metrics", s.getMetrics)
	app.Get("/v1/o11y/status", s.getStatus)

	// settings CRUD (scoped, persisted).
	app.Get("/v1/settings/:product", s.getSettings)
	app.Put("/v1/settings/:product", s.putSettings)

	log.Info("observe surface mounted", "brand", deps.Brand, "admin_org", s.adminOrg, "kms", deps.KMS != nil)
	return nil
}

func init() {
	cloud.Register("observe", 44, cloud.Typed(Mount))
}

// Shutdown releases the settings store. Idempotent.
func Shutdown() error {
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
// It is the SAME gate every other data-plane subsystem uses (principal.Tenant).
func (s *service) tenant(c *zip.Ctx) (string, bool) { return principal.Tenant(c) }

// isAdmin reports whether org is the platform operator org (IAM_ADMIN_ORG). Only
// the admin org may read the unattributed shared-service infra log stream; every
// other org is confined to rows carrying its own org. adminOrg is server config,
// never client input, so it cannot be forged.
func (s *service) isAdmin(org string) bool {
	return s.adminOrg != "" && org == s.adminOrg
}

// requireProduct reads + validates the ?product query param against productRE.
func requireProductQuery(c *zip.Ctx) (string, error) {
	p := strings.TrimSpace(c.Query("product"))
	if p == "" {
		return "", zip.ErrBadRequest("product query param is required")
	}
	if !productRE.MatchString(p) {
		return "", zip.ErrBadRequest("product must match ^[a-z0-9][a-z0-9._-]{0,62}$")
	}
	return p, nil
}

func requireProductParam(c *zip.Ctx) (string, error) {
	p := strings.TrimSpace(c.Param("product"))
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
	Product    string          `json:"product"`
	Config     json.RawMessage `json:"config"`
	SecretKeys []string        `json:"secretKeys"` // names of set secret fields (values masked, never returned)
	UpdatedAt  string          `json:"updatedAt"`
	CreatedAt  string          `json:"createdAt"`
}

func (s *service) getSettings(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	product, err := requireProductParam(c)
	if err != nil {
		return err
	}
	st, err := s.store.Get(c.Context(), org, product)
	if err == errNotFound {
		// No override yet — an honest empty config (the console merges its own
		// display defaults). Not a 404: the tab always renders.
		return c.JSON(http.StatusOK, settingsView{
			Product: product, Config: json.RawMessage(`{}`), SecretKeys: []string{},
		})
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get settings: %v", err)
	}
	return c.JSON(http.StatusOK, toSettingsView(st))
}

type settingsReq struct {
	Config  map[string]any    `json:"config"`  // non-secret config; stored verbatim (bounded)
	Secrets map[string]string `json:"secrets"` // secret fields; VALUES routed to KMS, never SQLite
}

func (s *service) putSettings(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	product, err := requireProductParam(c)
	if err != nil {
		return err
	}
	var body settingsReq
	if err := c.Bind(&body); err != nil {
		return err
	}

	// Non-secret config: validate + serialize within the cap.
	cfgJSON := []byte("{}")
	if body.Config != nil {
		b, mErr := json.Marshal(body.Config)
		if mErr != nil {
			return zip.ErrBadRequest("config must be JSON-serializable")
		}
		if len(b) > maxConfig {
			return zip.ErrBadRequest("config too large (max 64KiB)")
		}
		cfgJSON = b
	}

	// Existing secret-key set (so a PUT that omits a secret keeps it).
	var secretKeys []string
	if prev, gErr := s.store.Get(c.Context(), org, product); gErr == nil {
		secretKeys = prev.SecretKeys
	} else if gErr != errNotFound {
		return zip.Errorf(http.StatusInternalServerError, "load settings: %v", gErr)
	}

	// Route each provided secret to KMS. A secret VALUE never touches SQLite; if
	// KMS is unavailable, the whole write fails closed rather than dropping or
	// (worse) persisting the secret in plaintext.
	if len(body.Secrets) > 0 {
		if s.kms == nil {
			return zip.Errorf(http.StatusServiceUnavailable, "settings: KMS not configured; refusing to store secrets")
		}
		for key, val := range body.Secrets {
			if !productRE.MatchString(key) {
				return zip.ErrBadRequest("secret key must match ^[a-z0-9][a-z0-9._-]{0,62}$")
			}
			if val == "" || val == secretMask {
				// Empty / mask sentinel = "unchanged" — never overwrite a stored
				// secret with a blank or the mask the read path returned.
				continue
			}
			if len(val) > maxSecretValue {
				return zip.ErrBadRequest("secret value too large (max 8KiB)")
			}
			ref := secretRef(org, product, key)
			if err := s.kms.PutSecret(c.Context(), ref, []byte(val)); err != nil {
				return zip.Errorf(http.StatusInternalServerError, "kms put secret: %v", err)
			}
			secretKeys = addStr(secretKeys, key)
		}
		if len(secretKeys) > maxSecretKeys {
			return zip.ErrBadRequest("too many secret fields (max 64)")
		}
	}

	now := time.Now().Unix()
	st, err := s.store.Put(c.Context(), Settings{
		Org: org, Product: product, Config: string(cfgJSON), SecretKeys: secretKeys, UpdatedAt: now,
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist settings: %v", err)
	}
	return c.JSON(http.StatusOK, toSettingsView(st))
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

// secretMask is what the read path returns in place of a secret value; a PUT that
// echoes it back means "unchanged", so the real value is never round-tripped.
const secretMask = "••••••••"

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

// logLimit reads a bounded ?limit for a logs/metrics response.
func logLimit(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
		return defaultLogLimit
	}
	if n > maxLogLimit {
		return maxLogLimit
	}
	return n
}

func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func getenv(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func getenvDefault(key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// ctxWithTimeout bounds an outbound o11y read so a slow datastore/VM can never
// pin the request goroutine past the budget.
func ctxWithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
