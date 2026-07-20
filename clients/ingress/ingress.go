// Package ingress is cloud's embedded, runtime-configurable edge — the
// /v1/ingress subsystem. It makes the ONE hanzoai/cloud binary able to BE the
// fleet edge: terminate TLS, run ACME (Let's Encrypt), and reverse-proxy by Host
// to upstreams — all configured LIVE over an API, with NO static config file
// (routes.yaml) and NO restart to change a route.
//
// # Role model — one binary, role = runtime config
//
// The same artifact runs in either role; the emphasis is a single env flag:
//
//   - app role (default, CLOUD_INGRESS_EDGE_ENABLED unset): the /v1/ingress
//     CONTROL plane is mounted (so config can be authored/inspected) but the edge
//     DATA plane never binds a listener. cloud is a pure application.
//
//   - edge role (CLOUD_INGRESS_EDGE_ENABLED=true): additionally the edge data
//     plane binds :80 (ACME HTTP-01 + HTTP router) and :443 (SNI TLS termination
//
//   - router). This instance is now a fleet edge — it routes to other cloud
//     instances / services / itself.
//
// The two planes are orthogonal:
//
//   - CONTROL plane = this file's /v1/ingress/* zip handlers (routes, services,
//     middlewares, tls, status). SuperAdmin-gated; per-tenant persistence in
//     SQLite; every mutation hot-reloads the engine.
//   - DATA plane = edge.go's net/http listeners + engine.go's atomic host table.
//     Built on github.com/vulcand/oxy/v2 (Traefik's proxy lineage) and
//     golang.org/x/crypto/acme/autocert.
//
// This is NOT "TLS in every app binary": TLS/ACME live behind the edge role,
// which a deployment selects for the ONE instance that is the edge. Every other
// instance runs app role with the listeners off.
//
// # Replacing standalone hanzoai/ingress
//
// A single-tenant / serve deploy that today fronts cloud with a standalone
// hanzoai/ingress can instead run its cloud in edge role: point DNS at that
// instance, POST its own host→backend routes and a TLS route, and the standalone
// ingress pod is gone — one binary is app + edge. The shared multi-tenant fleet
// edge can migrate the same way (a cloud-in-edge-role instance whose route table
// is the union of every tenant's routes, host-unique) — that migration is a PLAN,
// delivered but NOT applied here; the live routes.yaml is untouched.
//
// # Composition with /v1/gateway
//
// Ingress and gateway are orthogonal edge subsystems: ingress owns routing + TLS;
// gateway owns auth + rate-limit. They compose (ingress in front, gateway as a
// backend/middleware layer) without colliding — different concerns, different
// surfaces.
package ingress

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// state is the mounted ingress subsystem's own data: the store (source of truth),
// the engine (compiled runtime table), and — in edge role — the edge (listeners).
// Shared deps live in the embedded cloud.Base.
type state struct {
	store   *Store
	engine  *Engine
	edge    *Edge // nil in app role
	edgeCfg edgeConfig
	role    string     // "edge" | "app" (derived, for /v1/ingress/status)
	mu      sync.Mutex // serializes reload compiles
}

var mounted *cloud.Service[state]

// Mount wires the /v1/ingress control plane onto app and, in edge role, starts the
// edge data plane.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("ingress.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("ingress.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("ingress.Mount: empty DataDir")
	}
	b := cloud.NewBase(deps, "ingress")
	log := b.Log

	dir := filepath.Join(deps.DataDir, "ingress")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ingress.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(dir, "ingress.db"))
	if err != nil {
		return fmt.Errorf("ingress.Mount: open store: %w", err)
	}

	edgeEnabled := boolEnv("CLOUD_INGRESS_EDGE_ENABLED")
	ecfg := edgeConfig{
		httpAddr:  getenv("CLOUD_INGRESS_HTTP_ADDR", ":80"),
		httpsAddr: getenv("CLOUD_INGRESS_HTTPS_ADDR", ":443"),
		cacheDir:  filepath.Join(dir, "acme"),
		email:     getenv("CLOUD_INGRESS_ACME_EMAIL", ""),
		staging:   boolEnv("CLOUD_INGRESS_ACME_STAGING"),
	}
	role := "app"
	if edgeEnabled {
		role = "edge"
	}

	s := &cloud.Service[state]{Base: b, State: state{store: store, engine: newEngine(log), edgeCfg: ecfg, role: role}}
	mounted = s
	mountRoutes(s, app)

	// Compile the persisted config into the engine before the edge serves it. A
	// reload failure is logged, not fatal — the edge serves an empty table until
	// the config is fixed, rather than refusing to boot.
	if err := reload(s, context.Background()); err != nil {
		log.Warn("ingress: initial reload failed", "err", err)
	}

	if edgeEnabled {
		if err := os.MkdirAll(ecfg.cacheDir, 0o700); err != nil {
			return fmt.Errorf("ingress.Mount: acme cache dir: %w", err)
		}
		s.State.edge = newEdge(s.State.engine, ecfg, log)
		s.State.edge.start()
		log.Info("ingress EDGE role active", "http", ecfg.httpAddr, "https", ecfg.httpsAddr, "acmeStaging", ecfg.staging)
	} else {
		log.Info("ingress control plane mounted (app role; edge listeners off)", "prefix", "/v1/ingress")
	}
	return nil
}

// Shutdown drains the edge listeners and closes the store. Idempotent.
func Shutdown(ctx context.Context) error {
	if mounted == nil {
		return nil
	}
	var err error
	if mounted.State.edge != nil {
		err = mounted.State.edge.stop(ctx)
	}
	if mounted.State.store != nil {
		if e := mounted.State.store.Close(); e != nil && err == nil {
			err = e
		}
	}
	mounted = nil
	return err
}

// mountRoutes registers the /v1/ingress control-plane surface. routes/services/
// middlewares share uniform CRUD (list/get/delete keyed by kind); create+update
// share one handler per kind (POST and PUT both land there).
func mountRoutes(s *cloud.Service[state], app *zip.App) {
	g := app.Group("/v1/ingress")
	g.Get("/status", cloud.Handle(s, status))
	g.Get("/tls", cloud.Handle(s, getTLS))
	g.Put("/tls", cloud.Handle(s, putTLS))

	g.Get("/routes", list(s, KindRoute))
	g.Get("/routes/:id", getObj(s, KindRoute))
	g.Post("/routes", cloud.Handle(s, putRoute))
	g.Put("/routes/:id", cloud.Handle(s, putRoute))
	g.Delete("/routes/:id", del(s, KindRoute))

	g.Get("/services", list(s, KindService))
	g.Get("/services/:id", getObj(s, KindService))
	g.Post("/services", cloud.Handle(s, putService))
	g.Put("/services/:id", cloud.Handle(s, putService))
	g.Delete("/services/:id", del(s, KindService))

	g.Get("/middlewares", list(s, KindMiddleware))
	g.Get("/middlewares/:id", getObj(s, KindMiddleware))
	g.Post("/middlewares", cloud.Handle(s, putMiddleware))
	g.Put("/middlewares/:id", cloud.Handle(s, putMiddleware))
	g.Delete("/middlewares/:id", del(s, KindMiddleware))
}

// admin resolves the SuperAdmin tenant for an edge-config request. The edge is
// PLATFORM infrastructure (AC-6 least privilege): every /v1/ingress op requires
// SuperAdmin (owner=="admin" ⇒ c.IsAdmin(), the same predicate admin-guard and
// clients/admin enforce), and storage is scoped to that validated admin org. A
// non-admin — or a forged, unvalidated principal — is refused 403.
func admin(s *cloud.Service[state], c *zip.Ctx) (string, error) {
	if !c.IsAdmin() {
		return "", zip.ErrForbidden("ingress edge config requires SuperAdmin")
	}
	org, ok := principal.Org(c)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return org, nil
}

// ── object CRUD ───────────────────────────────────────────────────────────────

func list(s *cloud.Service[state], kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, err := admin(s, c)
		if err != nil {
			return err
		}
		objs, err := s.State.store.List(c.Context(), org, kind)
		if err != nil {
			return zip.ErrInternal("list " + kind + ": " + err.Error())
		}
		items := make([]json.RawMessage, 0, len(objs))
		for _, o := range objs {
			items = append(items, json.RawMessage(o.Doc))
		}
		return c.JSON(http.StatusOK, map[string]any{kind + "s": items})
	}
}

func getObj(s *cloud.Service[state], kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, err := admin(s, c)
		if err != nil {
			return err
		}
		doc, found, err := s.State.store.Get(c.Context(), org, kind, c.Param("id"))
		if err != nil {
			return zip.ErrInternal("get " + kind + ": " + err.Error())
		}
		if !found {
			return zip.ErrNotFound(kind + " not found")
		}
		return c.JSON(http.StatusOK, json.RawMessage(doc))
	}
}

func del(s *cloud.Service[state], kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, err := admin(s, c)
		if err != nil {
			return err
		}
		ok, err := s.State.store.Delete(c.Context(), org, kind, c.Param("id"))
		if err != nil {
			return zip.ErrInternal("delete " + kind + ": " + err.Error())
		}
		if !ok {
			return zip.ErrNotFound(kind + " not found")
		}
		if err := reload(s, c.Context()); err != nil {
			return zip.ErrInternal("reload: " + err.Error())
		}
		return c.NoContent(http.StatusNoContent)
	}
}

func putRoute(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := admin(s, c)
	if err != nil {
		return err
	}
	var r Route
	if err := c.Bind(&r); err != nil {
		return zip.ErrBadRequest("invalid route: " + err.Error())
	}
	if id := c.Param("id"); id != "" {
		r.ID = id
	}
	if r.ID == "" {
		r.ID = genID()
	}
	if err := r.validate(); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	doc, _ := json.Marshal(r)
	if err := s.State.store.Put(c.Context(), org, KindRoute, r.ID, string(doc), r.Host, now()); err != nil {
		if errors.Is(err, ErrHostTaken) {
			return zip.ErrConflict("host already claimed: " + r.Host)
		}
		return zip.ErrInternal("persist route: " + err.Error())
	}
	if err := reload(s, c.Context()); err != nil {
		return zip.ErrInternal("reload: " + err.Error())
	}
	return c.JSON(http.StatusOK, r)
}

func putService(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := admin(s, c)
	if err != nil {
		return err
	}
	var svcObj Service
	if err := c.Bind(&svcObj); err != nil {
		return zip.ErrBadRequest("invalid service: " + err.Error())
	}
	if id := c.Param("id"); id != "" {
		svcObj.ID = id
	}
	if svcObj.ID == "" {
		svcObj.ID = genID()
	}
	if err := svcObj.validate(); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	doc, _ := json.Marshal(svcObj)
	if err := s.State.store.Put(c.Context(), org, KindService, svcObj.ID, string(doc), "", now()); err != nil {
		return zip.ErrInternal("persist service: " + err.Error())
	}
	if err := reload(s, c.Context()); err != nil {
		return zip.ErrInternal("reload: " + err.Error())
	}
	return c.JSON(http.StatusOK, svcObj)
}

func putMiddleware(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := admin(s, c)
	if err != nil {
		return err
	}
	var m Middleware
	if err := c.Bind(&m); err != nil {
		return zip.ErrBadRequest("invalid middleware: " + err.Error())
	}
	if id := c.Param("id"); id != "" {
		m.ID = id
	}
	if m.ID == "" {
		m.ID = genID()
	}
	if err := m.validate(); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	doc, _ := json.Marshal(m)
	if err := s.State.store.Put(c.Context(), org, KindMiddleware, m.ID, string(doc), "", now()); err != nil {
		return zip.ErrInternal("persist middleware: " + err.Error())
	}
	if err := reload(s, c.Context()); err != nil {
		return zip.ErrInternal("reload: " + err.Error())
	}
	return c.JSON(http.StatusOK, m)
}

// ── TLS / ACME config ─────────────────────────────────────────────────────────

func getTLS(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := admin(s, c)
	if err != nil {
		return err
	}
	t, err := orgTLS(s, c.Context(), org)
	if err != nil {
		return zip.ErrInternal("load tls: " + err.Error())
	}
	hosts, err := tlsHostSet(s, c.Context())
	if err != nil {
		return zip.ErrInternal("tls hosts: " + err.Error())
	}
	directory := "letsencrypt-production"
	if s.State.edgeCfg.staging {
		directory = leStagingDirectory
	}
	return c.JSON(http.StatusOK, map[string]any{
		"config":        t,
		"role":          s.State.role,
		"edgeEnabled":   s.State.edge != nil,
		"managedHosts":  sortedKeys(hosts),
		"acmeDirectory": directory,
		"acmeEmail":     s.State.edgeCfg.email,
		"note":          "acmeEmail/staging apply on edge (re)start; extraHosts + route.tls hot-apply on reload",
	})
}

func putTLS(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := admin(s, c)
	if err != nil {
		return err
	}
	var t TLSConfig
	if err := c.Bind(&t); err != nil {
		return zip.ErrBadRequest("invalid tls config: " + err.Error())
	}
	if err := t.normalize(); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	doc, _ := json.Marshal(t)
	if err := s.State.store.PutTLS(c.Context(), org, string(doc), now()); err != nil {
		return zip.ErrInternal("persist tls: " + err.Error())
	}
	if err := reload(s, c.Context()); err != nil {
		return zip.ErrInternal("reload: " + err.Error())
	}
	return c.JSON(http.StatusOK, t)
}

func status(s *cloud.Service[state], c *zip.Ctx) error {
	if _, err := admin(s, c); err != nil {
		return err
	}
	hosts, tlsHosts := s.State.engine.counts()
	return c.JSON(http.StatusOK, map[string]any{
		"role":         s.State.role,
		"edgeEnabled":  s.State.edge != nil,
		"httpAddr":     s.State.edgeCfg.httpAddr,
		"httpsAddr":    s.State.edgeCfg.httpsAddr,
		"acmeStaging":  s.State.edgeCfg.staging,
		"acmeCacheDir": s.State.edgeCfg.cacheDir,
		"liveHosts":    hosts,
		"tlsHosts":     tlsHosts,
		"proxy":        "github.com/vulcand/oxy/v2 (weighted round-robin, Traefik lineage)",
	})
}

// ── reload (hot-apply) ────────────────────────────────────────────────────────

// reload compiles the ENTIRE persisted config (union across orgs) into the engine
// and atomically swaps it in. Called after every mutation and once at Mount. The
// compile is cheap and lock-free on the read path; mu only serializes concurrent
// compiles so the last writer wins deterministically.
func reload(s *cloud.Service[state], ctx context.Context) error {
	s.State.mu.Lock()
	defer s.State.mu.Unlock()

	routes, err := loadObjects[Route](ctx, s.State.store, KindRoute)
	if err != nil {
		return err
	}
	svcObjs, err := loadObjects[Service](ctx, s.State.store, KindService)
	if err != nil {
		return err
	}
	mwObjs, err := loadObjects[Middleware](ctx, s.State.store, KindMiddleware)
	if err != nil {
		return err
	}
	services := make(map[string]Service, len(svcObjs))
	for _, o := range svcObjs {
		services[o.ID] = o
	}
	mws := make(map[string]Middleware, len(mwObjs))
	for _, o := range mwObjs {
		mws[o.ID] = o
	}
	tlsHosts, err := tlsHostSet(s, ctx)
	if err != nil {
		return err
	}
	live, skipped := s.State.engine.apply(routes, services, mws, tlsHosts)
	s.Log.Info("ingress reloaded", "liveRoutes", live, "skipped", skipped, "tlsHosts", len(tlsHosts))
	return nil
}

// tlsHostSet is the union of every route marked TLS and every org's tls.extraHosts
// — the hosts the ACME HostPolicy will issue certs for.
func tlsHostSet(s *cloud.Service[state], ctx context.Context) (map[string]struct{}, error) {
	set := map[string]struct{}{}
	routes, err := loadObjects[Route](ctx, s.State.store, KindRoute)
	if err != nil {
		return nil, err
	}
	for _, r := range routes {
		if r.TLS {
			set[normalizeHost(r.Host)] = struct{}{}
		}
	}
	docs, err := s.State.store.AllTLS(ctx)
	if err != nil {
		return nil, err
	}
	for _, doc := range docs {
		var t TLSConfig
		if json.Unmarshal([]byte(doc), &t) == nil {
			for _, h := range t.ExtraHosts {
				if nh := normalizeHost(h); validHost(nh) {
					set[nh] = struct{}{}
				}
			}
		}
	}
	return set, nil
}

func orgTLS(s *cloud.Service[state], ctx context.Context, org string) (TLSConfig, error) {
	var t TLSConfig
	doc, found, err := s.State.store.GetTLS(ctx, org)
	if err != nil {
		return t, err
	}
	if found {
		_ = json.Unmarshal([]byte(doc), &t)
	}
	return t, nil
}

// loadObjects reads every object of kind (across orgs) and unmarshals it. A doc
// that fails to unmarshal is skipped (logged by the caller via reload counts),
// never aborting the whole compile.
func loadObjects[T any](ctx context.Context, store *Store, kind string) ([]T, error) {
	objs, err := store.All(ctx, kind)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(objs))
	for _, o := range objs {
		var v T
		if json.Unmarshal([]byte(o.Doc), &v) == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// ── small helpers ─────────────────────────────────────────────────────────────

func now() int64 { return time.Now().Unix() }

func genID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func getenv(key, dflt string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return dflt
}

func boolEnv(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "true" || v == "1"
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// sortStrings is a tiny insertion sort — the host set is small and this avoids a
// sort import solely for a status field.
func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
