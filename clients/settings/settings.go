// Package settings is the per-org, per-product configuration plane for the
// unified Hanzo Cloud binary — the /v1/settings/:product surface that lets an org
// read and edit a product's configuration, backed by a durable per-tenant store
// with KMS custody for any secret-typed field.
//
// ONE settings engine, EVERY product. The console drives all ~130 products'
// Settings tab through this single surface (product id → :product). The config
// SCHEMA is a small, fixed, honest default set (fields that are genuinely stored,
// org-scoped, and readable by a product's own backend via the in-process seam) —
// merged with the org's persisted overrides. There is no per-product bespoke
// server code; a product is just a (org, product) key.
//
// TENANT ISOLATION (what Red will attack). The org is ALWAYS principal.Tenant —
// the validated IAM owner claim (HIP-0026), never a client-supplied header, query
// param, or body field. Every store query binds `WHERE org=? AND product=?`;
// every KMS secret is keyed /orgs/{org}/settings/{product}/{key}. An org can
// therefore read or write ONLY its own product config. The product path segment is
// strictly slug-validated at the boundary so it can never smuggle path structure
// into the KMS key or the store key.
//
// SECRET CUSTODY. A secret-typed field's VALUE lives ONLY in KMS (sealed,
// AES-256-GCM envelope); the store keeps only a non-secret marker that the secret
// is set. A GET never returns a secret value (masked). If KMS is not Ready, a
// write that includes a secret field fails closed (503) — a secret is NEVER
// written to SQLite in plaintext.
//
// Surface (subsystem "settings", prefix /v1/settings; /v1 only):
//
//	GET /v1/settings/:product   the merged config doc (secrets masked)  -> settingsView
//	PUT /v1/settings/:product   persist config overrides                -> settingsView
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/kms"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	// kmsEnv is the KMS environment slug settings secrets are stored under. Settings
	// are per-(org,product), not environment-sharded, so one stable env keeps
	// store/read/delete addressing identical.
	kmsEnv = "default"

	// maxValueLen bounds a single non-secret config value at the ingest boundary.
	maxValueLen = 4096
	// maxSecretLen bounds a secret value handed to KMS.
	maxSecretLen = 8192
)

// fieldType enumerates the config field kinds. `secret` routes the value to KMS;
// the rest are non-secret values persisted in the store.
type fieldType string

const (
	typeString fieldType = "string"
	typeBool   fieldType = "bool"
	typeEmail  fieldType = "email"
	typeURL    fieldType = "url"
	typeSecret fieldType = "secret"
)

// fieldDef is a declared config field in the default schema.
type fieldDef struct {
	Key     string
	Label   string
	Type    fieldType
	Default string
}

// defaultSchema is the fixed, universal config schema every product exposes. Kept
// small and honest — each field is genuinely stored, org-scoped, and available to
// a product's backend via Get(); nothing here fabricates product behavior. A
// product-specific default set can be layered later without changing the surface.
var defaultSchema = []fieldDef{
	{Key: "displayName", Label: "Display name", Type: typeString},
	{Key: "environment", Label: "Environment", Type: typeString, Default: "production"},
	{Key: "notificationsEmail", Label: "Notifications email", Type: typeEmail},
	{Key: "webhookUrl", Label: "Event webhook URL", Type: typeURL},
	{Key: "webhookSecret", Label: "Webhook signing secret", Type: typeSecret},
	{Key: "alertsEnabled", Label: "Enable alerts", Type: typeBool, Default: "false"},
}

// schemaIndex is defaultSchema keyed by field key (the allowlist for PUT).
var schemaIndex = func() map[string]fieldDef {
	m := make(map[string]fieldDef, len(defaultSchema))
	for _, f := range defaultSchema {
		m[f.Key] = f
	}
	return m
}()

// persistedDoc is the JSON shape stored in the SQLite `doc` column: non-secret
// values, plus a marker set of secret keys that have a value in KMS. Secret values
// are NEVER in here.
type persistedDoc struct {
	Values     map[string]string `json:"values"`
	SecretsSet map[string]bool   `json:"secretsSet"`
}

// ── HTTP shapes (the console contract) ────────────────────────────────────────

type fieldView struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Type   string `json:"type"`
	Value  string `json:"value"` // non-secret value; "" for secret (masked)
	Secret bool   `json:"secret"`
	Set    bool   `json:"set,omitempty"` // secret only: whether a value is stored
}

type settingsView struct {
	Product   string      `json:"product"`
	Fields    []fieldView `json:"fields"`
	UpdatedAt string      `json:"updatedAt,omitempty"`
}

type putRequest struct {
	Fields []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"fields"`
}

// ── service / lifecycle ───────────────────────────────────────────────────────

type svc struct {
	store *Store
	kms   *kms.Client // type-asserted from deps.KMS; nil ⇒ secret ops fail closed
	log   luxlog.Logger
}

var mounted *svc

// Mount wires /v1/settings/* onto app.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("settings.Mount: nil zip.App")
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
	store, err := openStore(filepath.Join(deps.DataDir, "settings.db"))
	if err != nil {
		return fmt.Errorf("settings.Mount: open store: %w", err)
	}
	kc, _ := deps.KMS.(*kms.Client) // non-KMS impl leaves this nil → secret ops fail closed

	s := &svc{store: store, kms: kc, log: log}
	mounted = s

	app.Get("/v1/settings/:product", s.get)
	app.Put("/v1/settings/:product", s.put)

	log.Info("settings surface mounted", "prefix", "/v1/settings", "kmsReady", s.kmsReady())
	return nil
}

func init() {
	// Order 138: after integrations (137), before the AI /v1/* catch-all (150).
	cloud.RegisterWithShutdown("settings", 138, cloud.Typed(Mount), Shutdown)
}

// Shutdown closes the store. Idempotent.
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

// ── handlers ──────────────────────────────────────────────────────────────────

// get returns the merged config doc for (org, product): server defaults overlaid
// with the org's persisted overrides, secret values masked.
func (s *svc) get(c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	product, err := requireProduct(c)
	if err != nil {
		return err
	}
	rec, found, err := s.store.Get(c.Context(), org, product)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "load settings: %v", err)
	}
	doc := decodeDoc(rec.Doc)
	view := s.render(product, doc)
	if found {
		view.UpdatedAt = rfc3339(rec.UpdatedAt)
	}
	return c.JSON(http.StatusOK, view)
}

// put persists config overrides for (org, product). Non-secret values go to the
// store; a secret value goes to KMS (fail closed when KMS is not ready). Unknown
// field keys are rejected — the surface is the declared schema, not an arbitrary
// blob. Returns the freshly-merged view (secrets masked).
func (s *svc) put(c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	product, err := requireProduct(c)
	if err != nil {
		return err
	}
	var body putRequest
	if err := c.Bind(&body); err != nil {
		return err
	}

	rec, _, err := s.store.Get(c.Context(), org, product)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "load settings: %v", err)
	}
	doc := decodeDoc(rec.Doc)

	for _, f := range body.Fields {
		def, known := schemaIndex[f.Key]
		if !known {
			return zip.ErrBadRequest("unknown setting: " + safeEcho(f.Key))
		}
		if def.Type == typeSecret {
			if err := s.applySecret(org, product, def.Key, f.Value, doc); err != nil {
				return err
			}
			continue
		}
		if len(f.Value) > maxValueLen {
			return zip.ErrBadRequest(def.Key + " value too large")
		}
		if err := validateValue(def.Type, f.Value); err != nil {
			return err
		}
		doc.Values[def.Key] = f.Value
	}

	blob, err := json.Marshal(doc)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "encode settings: %v", err)
	}
	now := time.Now().Unix()
	if err := s.store.Put(c.Context(), org, product, string(blob), now); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist settings: %v", err)
	}
	view := s.render(product, doc)
	view.UpdatedAt = rfc3339(now)
	return c.JSON(http.StatusOK, view)
}

// applySecret writes a secret field's value to KMS and marks it set in the doc. An
// empty value LEAVES the existing secret untouched (a masked GET returns "" so a
// blind PUT-back must not clear it); a non-empty value seals to KMS. KMS not ready
// ⇒ fail closed (503) — a secret is never persisted in the store.
func (s *svc) applySecret(org, product, key, value string, doc *persistedDoc) error {
	if value == "" {
		return nil // no change — never overwrite/clear on an empty (masked) submit
	}
	if len(value) > maxSecretLen {
		return zip.ErrBadRequest(key + " secret too large")
	}
	if !s.kmsReady() {
		return zip.Errorf(http.StatusServiceUnavailable, "%s", kms.ErrMasterKeyMissing.Error())
	}
	if err := s.kms.Put(kmsPath(org, product), key, kmsEnv, []byte(value)); err != nil {
		s.log.Warn("settings kms seal failed", "org", org, "product", product, "key", key, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "secret custody failed")
	}
	doc.SecretsSet[key] = true
	return nil
}

// render overlays a persisted doc onto the default schema and masks secrets.
func (s *svc) render(product string, doc *persistedDoc) settingsView {
	out := settingsView{Product: product, Fields: make([]fieldView, 0, len(defaultSchema))}
	for _, def := range defaultSchema {
		fv := fieldView{
			Key:    def.Key,
			Label:  def.Label,
			Type:   string(def.Type),
			Secret: def.Type == typeSecret,
		}
		if def.Type == typeSecret {
			fv.Set = doc.SecretsSet[def.Key] // value is NEVER returned
		} else if v, ok := doc.Values[def.Key]; ok {
			fv.Value = v
		} else {
			fv.Value = def.Default
		}
		out.Fields = append(out.Fields, fv)
	}
	return out
}

func (s *svc) kmsReady() bool { return s.kms != nil && s.kms.Ready() }

// ── in-process seam ───────────────────────────────────────────────────────────

// Config returns an org's persisted NON-secret settings values for a product (the
// merged view of defaults + overrides), for a product's own backend to consume.
// Fails closed (nil) when unmounted. Secret values are NOT returned here — a
// consumer fetches those from KMS by the documented path.
func Config(ctx context.Context, org, product string) (map[string]string, bool) {
	if mounted == nil || !validOrg(org) || !validProductSlug(product) {
		return nil, false
	}
	rec, _, err := mounted.store.Get(ctx, org, product)
	if err != nil {
		return nil, false
	}
	doc := decodeDoc(rec.Doc)
	out := make(map[string]string, len(defaultSchema))
	for _, def := range defaultSchema {
		if def.Type == typeSecret {
			continue
		}
		if v, ok := doc.Values[def.Key]; ok {
			out[def.Key] = v
		} else if def.Default != "" {
			out[def.Key] = def.Default
		}
	}
	return out, true
}

// ── helpers ───────────────────────────────────────────────────────────────────

// requireProduct validates the :product path segment. It is folded into the KMS
// key and the store key, so it is strictly slug-validated at the boundary.
func requireProduct(c *zip.Ctx) (string, error) {
	p := strings.TrimSpace(c.Param("product"))
	if !validProductSlug(p) {
		return "", zip.ErrBadRequest("product must be a slug (lowercase alnum + hyphen, ≤63)")
	}
	return p, nil
}

// kmsPath is the per-org, per-product KMS namespace. org + product are validated
// at every entry point, so they can never smuggle path structure.
func kmsPath(org, product string) string {
	return "/orgs/" + org + "/settings/" + product
}

// decodeDoc parses the persisted doc, always returning non-nil maps.
func decodeDoc(s string) *persistedDoc {
	doc := &persistedDoc{Values: map[string]string{}, SecretsSet: map[string]bool{}}
	if strings.TrimSpace(s) == "" {
		return doc
	}
	var d persistedDoc
	if err := json.Unmarshal([]byte(s), &d); err == nil {
		if d.Values != nil {
			doc.Values = d.Values
		}
		if d.SecretsSet != nil {
			doc.SecretsSet = d.SecretsSet
		}
	}
	return doc
}

// validateValue enforces a light, honest shape on a non-secret value (empty is
// always allowed = clear). URLs must be http(s); emails must contain an @; bools
// must be true/false. Not a full validator — a boundary sanity gate.
func validateValue(t fieldType, v string) error {
	if v == "" {
		return nil
	}
	switch t {
	case typeURL:
		if !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") {
			return zip.ErrBadRequest("URL must start with http:// or https://")
		}
	case typeEmail:
		if !strings.Contains(v, "@") || strings.ContainsAny(v, " \t\r\n") {
			return zip.ErrBadRequest("invalid email")
		}
	case typeBool:
		if v != "true" && v != "false" {
			return zip.ErrBadRequest("bool must be true or false")
		}
	}
	return nil
}

// validProductSlug accepts a DNS-1123-ish slug (lowercase alnum + internal
// hyphen, ≤63). The product is folded into the KMS + store key, so it is validated
// strictly at every boundary that reaches custody.
func validProductSlug(p string) bool {
	if p == "" || len(p) > 63 {
		return false
	}
	for i, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(p)-1:
		default:
			return false
		}
	}
	return true
}

// validOrg accepts a DNS-1123-ish org label (≤63). Mirrors the KMS/integrations
// tenant boundary; the org is folded into the KMS secret path + the store key.
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

// safeEcho strips control chars from a value echoed back in an error (kills
// log/response injection via a crafted key).
func safeEcho(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// rfc3339 renders a unix timestamp as RFC3339 UTC (empty for 0).
func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}
