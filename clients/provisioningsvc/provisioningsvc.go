// Package provisioningsvc is the Hanzo Cloud provisioning control plane. It
// turns "create a database" into a real logical resource inside an
// already-live, shared product backend, per the unified /v1 binary (HIP-0106).
//
// One HTTP surface, seven kinds, one Provisioner each:
//
//	databases -> Postgres   sql.hanzo.svc:5432    CREATE DATABASE + ROLE
//	vector    -> Qdrant     vector.hanzo.svc:6333 PUT /collections/{name}
//	datastore -> ClickHouse datastore.hanzo.svc:8123 CREATE DATABASE + USER
//	kv        -> Redis      kv.hanzo.svc:6379     ACL SETUSER (keyspace scope)
//	search    -> Meilisearch search.hanzo.svc:7700 POST /indexes
//	storage   -> S3/MinIO   s3.hanzo.svc:9000     MakeBucket
//	docdb     -> MongoDB    docdb.hanzo.svc:27017 createCollection + createUser
//
// Tenancy: every request is scoped to the gateway-minted org (X-Org-Id /
// c.Org()). Empty org is rejected 403 unless the caller is an admin. The
// physical resource on the shared backend is namespaced org_<org>_<name> so two
// tenants never collide.
//
// Secrets: generated per-resource passwords are sealed in Hanzo KMS
// (client-side encrypted) and only a secret_ref is persisted. When KMS is not
// configured the service degrades safely — it returns the password once in the
// create response and stores NOTHING in plaintext. See kms.go.
package provisioningsvc

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
)

// kinds is the closed set of resource kinds this control plane provisions.
var kinds = []string{"databases", "vector", "datastore", "kv", "search", "storage", "docdb"}

// secretfulKinds are the kinds whose backend wires a real per-resource
// credential (so the generated password is meaningful and gets sealed in KMS /
// returned once). The others (vector, search, storage) authenticate with a
// shared, out-of-band key, so no per-resource password is produced.
var secretfulKinds = map[string]bool{
	"databases": true,
	"kv":        true,
	"datastore": true,
	"docdb":     true,
}

// nameRE constrains the user-supplied resource name to a DNS/identifier-safe
// slug. Validated at the boundary; the physical name and every SQL identifier
// derive from it, so this is the injection guard.
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

type svc struct {
	store *Store
	sec   *secrets
	reg   map[string]Provisioner
	log   luxlog.Logger
}

type createResp struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Status           string `json:"status"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Username         string `json:"username,omitempty"`
	Database         string `json:"database"`
	ConnectionString string `json:"connectionString"`
	Password         string `json:"password,omitempty"`
}

type getResp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Database string `json:"database"`
}

type listItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	CreatedAt int64  `json:"createdAt"`
}

// Mount wires the provisioning surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("provisioningsvc.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("provisioningsvc.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "provisioning")

	if deps.DataDir == "" {
		return fmt.Errorf("provisioningsvc.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("provisioningsvc.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "provisioning.db"))
	if err != nil {
		return fmt.Errorf("provisioningsvc.Mount: open store: %w", err)
	}

	s := &svc{
		store: store,
		sec:   openSecrets(deps.Brand, log),
		reg:   newRegistry(),
		log:   log,
	}

	for _, kind := range kinds {
		k := kind
		app.Post("/v1/"+k, s.create(k))
		app.Get("/v1/"+k, s.list(k))
		app.Get("/v1/"+k+"/:name", s.get(k))
		app.Delete("/v1/"+k+"/:name", s.drop(k))
	}

	log.Info("provisioning mounted",
		"kinds", len(kinds),
		"kms", s.sec.Enabled(),
		"brand", deps.Brand,
	)
	return nil
}

func init() {
	cloud.Register("provisioning", 120, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("provisioningsvc.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// create provisions a new logical resource of kind for the caller's org.
func (s *svc) create(kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		prov := s.reg[kind]
		if prov == nil {
			return zip.Errorf(http.StatusNotImplemented, "kind %q not supported", kind)
		}

		var body struct {
			Name string `json:"name"`
		}
		if err := c.Bind(&body); err != nil {
			return err
		}
		name := strings.ToLower(strings.TrimSpace(body.Name))
		if !nameRE.MatchString(name) {
			return zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
		}

		ctx := c.Context()

		// Fast duplicate check (the UNIQUE index is the authoritative guard).
		if _, err := s.store.Get(ctx, org, kind, name); err == nil {
			return zip.ErrConflict("resource already exists")
		} else if !errors.Is(err, errNotFound) {
			return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
		}

		physical := physicalName(org, name)
		user := physical
		pw, err := genToken(24)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
		}

		cs, host, port, db, err := prov.Create(ctx, physical, user, pw)
		if err != nil {
			if errors.Is(err, errAlreadyExists) {
				return zip.ErrConflict("resource already exists")
			}
			s.log.Error("provision failed", "kind", kind, "org", org, "name", name, "err", err)
			return zip.Errorf(http.StatusBadGateway, "provision %s failed: %v", kind, err)
		}

		// Secret handling. Only secretful kinds carry a real per-resource
		// password. Seal it in KMS when configured; otherwise return once and
		// store nothing (never plaintext).
		secretRef := fmt.Sprintf("org/%s/%s/%s", org, kind, name)
		storedRef, returnPw, username := "", "", ""
		if secretfulKinds[kind] {
			returnPw, username = pw, user
			if s.sec.Enabled() {
				if err := s.sec.Put(secretRef, []byte(pw)); err != nil {
					_ = prov.Drop(ctx, physical, user)
					s.log.Error("kms put failed; rolled back backend", "kind", kind, "err", err)
					return zip.Errorf(http.StatusInternalServerError, "store secret failed")
				}
				storedRef = secretRef
			} else {
				s.log.Warn("KMS degraded: password returned once, not persisted", "kind", kind, "org", org, "name", name)
			}
		}

		id, err := genID()
		if err != nil {
			_ = prov.Drop(ctx, physical, user)
			if storedRef != "" {
				_ = s.sec.Delete(storedRef)
			}
			return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
		}

		r := Resource{
			ID: id, Org: org, Kind: kind, Name: name,
			PhysicalName: physical, SecretRef: storedRef,
			Host: host, Port: port, Username: username, DBName: db,
			Status: "ready", CreatedAt: time.Now().Unix(),
		}
		if err := s.store.Insert(ctx, r); err != nil {
			// Lost a concurrent race or DB error — undo the backend + secret.
			_ = prov.Drop(ctx, physical, user)
			if storedRef != "" {
				_ = s.sec.Delete(storedRef)
			}
			if errors.Is(err, errConflict) {
				return zip.ErrConflict("resource already exists")
			}
			return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}

		return c.JSON(http.StatusCreated, createResp{
			ID: id, Kind: kind, Name: name, Status: "ready",
			Host: host, Port: port, Username: username, Database: db,
			ConnectionString: cs, Password: returnPw,
		})
	}
}

// list returns every resource of kind for the caller's org. Never a password.
func (s *svc) list(kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		rows, err := s.store.List(c.Context(), org, kind)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
		}
		out := make([]listItem, 0, len(rows))
		for _, r := range rows {
			out = append(out, listItem{
				ID: r.ID, Name: r.Name, Kind: r.Kind, Status: r.Status,
				Host: r.Host, Port: r.Port, CreatedAt: r.CreatedAt,
			})
		}
		return c.JSON(http.StatusOK, out)
	}
}

// get returns one resource's metadata. Never a password.
func (s *svc) get(kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		name := strings.ToLower(strings.TrimSpace(c.Param("name")))
		r, err := s.store.Get(c.Context(), org, kind, name)
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("resource not found")
		}
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
		}
		return c.JSON(http.StatusOK, getResp{
			ID: r.ID, Name: r.Name, Kind: r.Kind, Status: r.Status,
			Host: r.Host, Port: r.Port, Username: r.Username, Database: r.DBName,
		})
	}
}

// drop deprovisions the backend resource, deletes the sealed secret, and
// removes the metadata row.
func (s *svc) drop(kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		name := strings.ToLower(strings.TrimSpace(c.Param("name")))
		ctx := c.Context()

		r, err := s.store.Get(ctx, org, kind, name)
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("resource not found")
		}
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
		}

		if prov := s.reg[kind]; prov != nil {
			if err := prov.Drop(ctx, r.PhysicalName, r.Username); err != nil {
				s.log.Error("deprovision failed", "kind", kind, "org", org, "name", name, "err", err)
				return zip.Errorf(http.StatusBadGateway, "deprovision %s failed: %v", kind, err)
			}
		}
		if r.SecretRef != "" {
			if err := s.sec.Delete(r.SecretRef); err != nil {
				s.log.Warn("kms delete failed (continuing)", "ref", r.SecretRef, "err", err)
			}
		}
		if _, err := s.store.Delete(ctx, org, kind, name); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
		}
		return c.NoContent(http.StatusNoContent)
	}
}

// ----- tenancy + naming -----------------------------------------------------

// tenant resolves the org for a request. Empty org is allowed only for admins,
// who are bucketed under the literal "admin" org.
func tenant(c *zip.Ctx) (string, bool) {
	org := sanitizeOrg(c.Org())
	if org != "" {
		return org, true
	}
	if c.IsAdmin() {
		return "admin", true
	}
	return "", false
}

// sanitizeOrg reduces a gateway org id to a lowercase [a-z0-9-] slug, capped at
// 32 chars. Defense in depth: org comes from the JWT via the gateway, but it
// still flows into physical identifiers.
func sanitizeOrg(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 32 {
		out = strings.Trim(out[:32], "-")
	}
	return out
}

// physicalName namespaces a resource on the shared backend: org_<org>_<name>,
// with hyphens folded to underscores so it is a valid SQL/Mongo/CH identifier.
func physicalName(org, name string) string {
	return "org_" + underscore(org) + "_" + underscore(name)
}

func underscore(s string) string { return strings.ReplaceAll(s, "-", "_") }

func genID() (string, error) {
	tok, err := genToken(12)
	if err != nil {
		return "", err
	}
	return "rs_" + tok, nil
}
