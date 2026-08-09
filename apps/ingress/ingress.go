// Package ingress is your front door: automatic TLS certificates and hostname
// routing to any backend, changed live.
//
// It is cloud's embedded, runtime-configurable edge, controlled at /v1/ingress.
// It makes the ONE hanzoai/cloud binary able to BE the
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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/environ"
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
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("ingress.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("ingress.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("ingress.Mount: empty DataDir")
	}
	b := cloud.NewBase(deps, "ingress")
	log := b.Log

	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("ingress.Mount: open store: %w", err)
	}

	edgeEnabled := boolEnv("CLOUD_INGRESS_EDGE_ENABLED")
	ecfg := edgeConfig{
		httpAddr:  environ.Or("CLOUD_INGRESS_HTTP_ADDR", ":80"),
		httpsAddr: environ.Or("CLOUD_INGRESS_HTTPS_ADDR", ":443"),
		cacheDir:  filepath.Join(deps.DataDir, "ingress", "acme"),
		email:     environ.Or("CLOUD_INGRESS_ACME_EMAIL", ""),
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

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// mountRoutes registers the /v1/ingress control plane as TYPED ops. routes,
// services and middlewares share uniform CRUD (list/get/put/delete keyed by
// kind); create and update are ONE op per kind, because they are one behaviour —
// POST mints the id, PUT takes it from the URL — and zip binds the path over the
// body, which is exactly the precedence the untyped handler spelled out by hand.
func mountRoutes(s *cloud.Service[state], app cloud.Router) {
	g := app.Group("/v1/ingress")
	// The SuperAdmin gate below reads admin-ness off the REQUEST cloud.Bridge
	// parks on the context — a header principal.OrgFrom does not carry. Bridge is
	// the composer's install — once at the root of every program — so this
	// package does not install its own.
	//
	// TYPED ops declared on the GROUP: each op's path is the group's prefix
	// composed with its leaf — the identity every projection keys on — and
	// cmd/zipdoc resolves the prefix the same way as of zip v1.18.3, so the prose
	// below reaches the document, the MCP tool list, the CLI and the SDK.
	o := ops{s: s}
	zip.Get(g, "/status", o.status)
	zip.Get(g, "/tls", o.getTLS)
	zip.Put(g, "/tls", o.putTLS)

	zip.Get(g, "/routes", o.listRoutes)
	zip.Get(g, "/routes/:id", o.getRoute)
	zip.Post(g, "/routes", o.putRoute)
	zip.Put(g, "/routes/:id", o.putRoute)
	zip.Delete(g, "/routes/:id", o.deleteRoute)

	zip.Get(g, "/services", o.listServices)
	zip.Get(g, "/services/:id", o.getService)
	zip.Post(g, "/services", o.putService)
	zip.Put(g, "/services/:id", o.putService)
	zip.Delete(g, "/services/:id", o.deleteService)

	zip.Get(g, "/middlewares", o.listMiddlewares)
	zip.Get(g, "/middlewares/:id", o.getMiddleware)
	zip.Post(g, "/middlewares", o.putMiddleware)
	zip.Put(g, "/middlewares/:id", o.putMiddleware)
	zip.Delete(g, "/middlewares/:id", o.deleteMiddleware)
}

// ops binds the service to the typed control-plane ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — it has no parameter for the service
// — so the service arrives as a RECEIVER and every op is a method value
// (o.putRoute), which is also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// admin resolves the SuperAdmin tenant for an edge-config op. The edge is
// PLATFORM infrastructure (AC-6 least privilege): every /v1/ingress op requires
// SuperAdmin (owner=="admin" ⇒ c.IsAdmin(), the same predicate admin-guard and
// clients/admin enforce), and storage is scoped to that validated admin org. A
// non-admin — or a forged, unvalidated principal — is refused 403.
//
// It needs the REQUEST rather than only the tenant: admin-ness lives in a header
// (X-User-IsAdmin) that principal.OrgFrom does not carry. cloud.Bridge parks the
// request and cloud.Request takes it back off. FAIL CLOSED off the HTTP path —
// a CLI LocalInvoke has no request, so it is refused by this same line, with no
// second gate to keep in sync.
func admin(ctx context.Context) (string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", zip.ErrForbidden("ingress edge config requires SuperAdmin")
	}
	if !c.IsAdmin() {
		return "", zip.ErrForbidden("ingress edge config requires SuperAdmin")
	}
	org, ok := principal.Org(c)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return org, nil
}

// ── the shapes the ops take and give ─────────────────────────────────────────

// noInput is the In of an op that takes nothing off the wire — no body, no query
// parameter, no path segment. Its whole input is the caller's validated principal.
type noInput struct{}

// objRef addresses one stored object. The id is the path segment: the URL is the
// addressing authority, so it binds from there whatever a body says.
type objRef struct {
	// ID is the object to act on, from the path.
	ID string `json:"id"`
}

// A typed op's Go type name IS its schema name, and the fleet's schema namespace
// is FLAT — openapi.Weave refuses one name with two shapes across apps, because a
// generated SDK would bind whichever it read last. So the values below carry the
// product the namespace cannot: the obvious "serviceList" is already apps/admin's
// launch board, and the weave refused this package until these were qualified. The
// domain nouns (Route, Service, Middleware, Backend, TLSConfig) stay unqualified
// — they are ingress's published nouns and unique today, and the weave is the
// gate if that ever stops being true.

// ingressRoutes is every route the caller's org has configured.
type ingressRoutes struct {
	// Routes is the org's routes, ordered by id.
	Routes []Route `json:"routes"`
}

// ingressServices is every backend pool the caller's org has configured.
type ingressServices struct {
	// Services is the org's services, ordered by id.
	Services []Upstream `json:"services"`
}

// ingressMiddlewares is every edge transform the caller's org has configured.
type ingressMiddlewares struct {
	// Middlewares is the org's middlewares, ordered by id.
	Middlewares []Middleware `json:"middlewares"`
}

// ingressStatus is the edge's live posture: what this instance is configured to be,
// and what its compiled route table currently serves.
type ingressStatus struct {
	// Role is "edge" when CLOUD_INGRESS_EDGE_ENABLED is set, else "app".
	Role string `json:"role"`
	// EdgeEnabled is true when the edge listeners are actually bound.
	EdgeEnabled bool `json:"edgeEnabled"`
	// HTTPAddr is the address the ACME HTTP-01 + HTTP router listens on.
	HTTPAddr string `json:"httpAddr"`
	// HTTPSAddr is the address the SNI TLS terminator listens on.
	HTTPSAddr string `json:"httpsAddr"`
	// ACMEStaging is true when certificates are issued from Let's Encrypt staging.
	ACMEStaging bool `json:"acmeStaging"`
	// ACMECacheDir is where autocert persists accounts and certificates.
	ACMECacheDir string `json:"acmeCacheDir"`
	// LiveHosts is how many hosts the compiled table routes.
	LiveHosts int `json:"liveHosts"`
	// TLSHosts is how many hosts the ACME HostPolicy will issue a certificate
	// for. NOT a subset of LiveHosts: an extraHost owns no route, and a TLS route
	// naming a missing service is skipped while its host still wants a cert.
	TLSHosts int `json:"tlsHosts"`
	// Proxy names the reverse-proxy implementation behind every route.
	Proxy string `json:"proxy"`
}

// ingressTLS is the caller org's ACME intent plus the edge-wide TLS facts that
// intent lands in.
type ingressTLS struct {
	// Config is the caller org's stored ACME intent.
	Config TLSConfig `json:"config"`
	// Role is "edge" when this instance binds the listeners, else "app".
	Role string `json:"role"`
	// EdgeEnabled is true when the edge listeners are actually bound.
	EdgeEnabled bool `json:"edgeEnabled"`
	// ManagedHosts is every host the ACME HostPolicy will issue a certificate for
	// — the union across ALL orgs of TLS-marked routes and configured extraHosts,
	// because one process holds one certificate cache.
	ManagedHosts []string `json:"managedHosts"`
	// ACMEDirectory is the ACME endpoint in use: the staging URL, or
	// "letsencrypt-production".
	ACMEDirectory string `json:"acmeDirectory"`
	// ACMEEmail is the account email the PROCESS was started with
	// (CLOUD_INGRESS_ACME_EMAIL), not the stored config's.
	ACMEEmail string `json:"acmeEmail"`
	// Note states which fields hot-apply and which need an edge restart.
	Note string `json:"note"`
}

// ── object CRUD (one behaviour per verb, three kinds) ────────────────────────
//
// The four helpers below are the ONE implementation behind the twelve CRUD ops:
// a kind is a parameter, an object type is a type parameter, and each op is the
// name that binds both. They are functions rather than the closures they replace
// because a typed op must be a METHOD — a closure returned by a helper is a call
// expression with no doc comment for cmd/zipdoc to lift.

// listOf reads every object of kind for the caller's org and decodes each stored
// doc into T. A doc that will not decode is an internal error, never a silently
// dropped row: the store only ever holds what putOf marshalled into it, so an
// undecodable doc means corruption the caller must hear about.
func listOf[T any](ctx context.Context, s *cloud.Service[state], kind string) ([]T, error) {
	org, err := admin(ctx)
	if err != nil {
		return nil, err
	}
	objs, err := s.State.store.List(ctx, org, kind)
	if err != nil {
		return nil, zip.ErrInternal("list " + kind + ": " + err.Error())
	}
	items := make([]T, 0, len(objs))
	for _, o := range objs {
		var v T
		if err := json.Unmarshal([]byte(o.Doc), &v); err != nil {
			return nil, zip.ErrInternal("decode " + kind + " " + o.ID + ": " + err.Error())
		}
		items = append(items, v)
	}
	return items, nil
}

// getOf reads one (org, kind, id) object and decodes it into T. An id this org
// does not hold — including one another org does — is 404.
func getOf[T any](ctx context.Context, s *cloud.Service[state], kind, id string) (*T, error) {
	org, err := admin(ctx)
	if err != nil {
		return nil, err
	}
	doc, found, err := s.State.store.Get(ctx, org, kind, id)
	if err != nil {
		return nil, zip.ErrInternal("get " + kind + ": " + err.Error())
	}
	if !found {
		return nil, zip.ErrNotFound(kind + " not found")
	}
	var v T
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		return nil, zip.ErrInternal("decode " + kind + ": " + err.Error())
	}
	return &v, nil
}

// putOf validates, persists and hot-applies one object. id comes from the URL
// when the route has one and from the body otherwise; an object that names
// neither gets a fresh one. host is the route's globally-unique DNS claim ("" for
// the kinds that make none).
func putOf[T interface{ validate() error }](ctx context.Context, s *cloud.Service[state], kind string, v T, id *string, host string) error {
	org, err := admin(ctx)
	if err != nil {
		return err
	}
	if *id == "" {
		*id = genID()
	}
	if err := v.validate(); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	doc, _ := json.Marshal(v)
	if err := s.State.store.Put(ctx, org, kind, *id, string(doc), host, now()); err != nil {
		if errors.Is(err, ErrHostTaken) {
			return zip.ErrConflict("host already claimed: " + host)
		}
		return zip.ErrInternal("persist " + kind + ": " + err.Error())
	}
	if err := reload(s, ctx); err != nil {
		return zip.ErrInternal("reload: " + err.Error())
	}
	return nil
}

// deleteOf removes one (org, kind, id) object and hot-applies the shrunken
// table. A nil result is the 204 every delete has always answered.
func deleteOf(ctx context.Context, s *cloud.Service[state], kind, id string) (*struct{}, error) {
	org, err := admin(ctx)
	if err != nil {
		return nil, err
	}
	ok, err := s.State.store.Delete(ctx, org, kind, id)
	if err != nil {
		return nil, zip.ErrInternal("delete " + kind + ": " + err.Error())
	}
	if !ok {
		return nil, zip.ErrNotFound(kind + " not found")
	}
	if err := reload(s, ctx); err != nil {
		return nil, zip.ErrInternal("reload: " + err.Error())
	}
	return nil, nil
}

// ── routes ───────────────────────────────────────────────────────────────────

// ListRoutes returns every routing rule the caller's org has configured, ordered
// by id. A route maps an exact Host (and optional path prefix) to a service.
func (o ops) listRoutes(ctx context.Context, _ *noInput) (*ingressRoutes, error) {
	items, err := listOf[Route](ctx, o.s, KindRoute)
	if err != nil {
		return nil, err
	}
	return &ingressRoutes{Routes: items}, nil
}

// GetRoute returns one of the caller org's routing rules by id.
//
// Example: {"id": "a1b2c3d4e5f60718"}
func (o ops) getRoute(ctx context.Context, in *objRef) (*Route, error) {
	return getOf[Route](ctx, o.s, KindRoute, in.ID)
}

// PutRoute creates or replaces one routing rule and hot-applies the new table —
// there is no config file and no restart. POST mints an id when the body omits
// one; PUT takes the id from the URL, which wins over any id in the body. A
// route's host is a GLOBALLY unique DNS claim: a host another org's route already
// holds is refused 409, so no tenant can hijack another's hostname.
//
// Example: {"id": "web", "host": "app.example.com", "service": "app-pool", "tls": true}
func (o ops) putRoute(ctx context.Context, in *Route) (*Route, error) {
	r := *in
	if err := putOf(ctx, o.s, KindRoute, &r, &r.ID, normalizeHost(r.Host)); err != nil {
		return nil, err
	}
	return &r, nil
}

// DeleteRoute removes one of the caller org's routing rules and hot-applies the
// shrunken table, freeing its host for another claim. Answers 204; an id this org
// does not hold is 404.
//
// Example: {"id": "web"}
func (o ops) deleteRoute(ctx context.Context, in *objRef) (*struct{}, error) {
	return deleteOf(ctx, o.s, KindRoute, in.ID)
}

// ── services ─────────────────────────────────────────────────────────────────

// ListServices returns every backend pool the caller's org has configured,
// ordered by id. A service is the weighted round-robin target a route dispatches
// to.
func (o ops) listServices(ctx context.Context, _ *noInput) (*ingressServices, error) {
	items, err := listOf[Upstream](ctx, o.s, KindService)
	if err != nil {
		return nil, err
	}
	return &ingressServices{Services: items}, nil
}

// GetService returns one of the caller org's backend pools by id.
//
// Example: {"id": "app-pool"}
func (o ops) getService(ctx context.Context, in *objRef) (*Upstream, error) {
	return getOf[Upstream](ctx, o.s, KindService, in.ID)
}

// PutService creates or replaces one backend pool and hot-applies it. POST mints
// an id when the body omits one; PUT takes the id from the URL, which wins over
// any id in the body. A pool needs at least one backend and every backend URL
// must be http(s)://host[:port].
//
// Example: {"id": "app-pool", "backends": [{"url": "http://10.0.0.7:8000", "weight": 1}]}
func (o ops) putService(ctx context.Context, in *Upstream) (*Upstream, error) {
	svcObj := *in
	// A service claims no host: the globally-unique DNS index is the route's.
	if err := putOf(ctx, o.s, KindService, &svcObj, &svcObj.ID, ""); err != nil {
		return nil, err
	}
	return &svcObj, nil
}

// DeleteService removes one of the caller org's backend pools and hot-applies the
// change. Routes still pointing at it stop being served (they compile as skipped)
// until they name a pool that exists. Answers 204; an id this org does not hold
// is 404.
//
// Example: {"id": "app-pool"}
func (o ops) deleteService(ctx context.Context, in *objRef) (*struct{}, error) {
	return deleteOf(ctx, o.s, KindService, in.ID)
}

// ── middlewares ──────────────────────────────────────────────────────────────

// ListMiddlewares returns every edge transform the caller's org has configured,
// ordered by id. A route names the ones it wants, in order.
func (o ops) listMiddlewares(ctx context.Context, _ *noInput) (*ingressMiddlewares, error) {
	items, err := listOf[Middleware](ctx, o.s, KindMiddleware)
	if err != nil {
		return nil, err
	}
	return &ingressMiddlewares{Middlewares: items}, nil
}

// GetMiddleware returns one of the caller org's edge transforms by id.
//
// Example: {"id": "strip-api"}
func (o ops) getMiddleware(ctx context.Context, in *objRef) (*Middleware, error) {
	return getOf[Middleware](ctx, o.s, KindMiddleware, in.ID)
}

// PutMiddleware creates or replaces one edge transform and hot-applies it. POST
// mints an id when the body omits one; PUT takes the id from the URL, which wins
// over any id in the body. type must be one of redirectScheme, stripPrefix,
// addPrefix or headers, and stripPrefix/addPrefix each require their config key.
//
// Example: {"id": "strip-api", "type": "stripPrefix", "config": {"prefixes": "/api"}}
func (o ops) putMiddleware(ctx context.Context, in *Middleware) (*Middleware, error) {
	m := *in
	// A middleware claims no host: the globally-unique DNS index is the route's.
	if err := putOf(ctx, o.s, KindMiddleware, &m, &m.ID, ""); err != nil {
		return nil, err
	}
	return &m, nil
}

// DeleteMiddleware removes one of the caller org's edge transforms and hot-applies
// the change. Routes still naming it stop being served (they compile as skipped)
// until they name a transform that exists. Answers 204; an id this org does not
// hold is 404.
//
// Example: {"id": "strip-api"}
func (o ops) deleteMiddleware(ctx context.Context, in *objRef) (*struct{}, error) {
	return deleteOf(ctx, o.s, KindMiddleware, in.ID)
}

// ── TLS / ACME config ─────────────────────────────────────────────────────────

// GetTLS returns the caller org's ACME intent together with the edge-wide TLS
// facts it lands in: which role this instance runs in, whether its listeners are
// bound, every host the ACME HostPolicy will issue a certificate for (the union
// across ALL orgs of TLS-marked routes and configured extraHosts, because one
// process holds one certificate cache), and the ACME directory and account email
// the process was started with.
func (o ops) getTLS(ctx context.Context, _ *noInput) (*ingressTLS, error) {
	org, err := admin(ctx)
	if err != nil {
		return nil, err
	}
	t, err := orgTLS(o.s, ctx, org)
	if err != nil {
		return nil, zip.ErrInternal("load tls: " + err.Error())
	}
	hosts, err := tlsHostSet(o.s, ctx)
	if err != nil {
		return nil, zip.ErrInternal("tls hosts: " + err.Error())
	}
	directory := "letsencrypt-production"
	if o.s.State.edgeCfg.staging {
		directory = leStagingDirectory
	}
	return &ingressTLS{
		Config:        t,
		Role:          o.s.State.role,
		EdgeEnabled:   o.s.State.edge != nil,
		ManagedHosts:  sortedKeys(hosts),
		ACMEDirectory: directory,
		ACMEEmail:     o.s.State.edgeCfg.email,
		Note:          tlsNote,
	}, nil
}

// PutTLS replaces the caller org's ACME intent and hot-applies what can be
// hot-applied. extraHosts are normalized and validated, then feed the ACME
// HostPolicy on the reload this op performs, alongside the per-route tls flags.
// acmeEmail and staging bind an ACME account for the lifetime of an edge process,
// so they only take effect when the edge (re)starts — the returned note says so.
//
// Example: {"extraHosts": ["www.example.com"]}
func (o ops) putTLS(ctx context.Context, in *TLSConfig) (*TLSConfig, error) {
	org, err := admin(ctx)
	if err != nil {
		return nil, err
	}
	t := *in
	if err := t.normalize(); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	doc, _ := json.Marshal(t)
	if err := o.s.State.store.PutTLS(ctx, org, string(doc), now()); err != nil {
		return nil, zip.ErrInternal("persist tls: " + err.Error())
	}
	if err := reload(o.s, ctx); err != nil {
		return nil, zip.ErrInternal("reload: " + err.Error())
	}
	return &t, nil
}

// ── status ────────────────────────────────────────────────────────────────────

// Status reports the ingress edge's live posture: the role this instance runs in
// (app or edge), whether its listeners are bound and on which addresses, the ACME
// posture (staging flag and certificate cache directory), how many hosts the
// compiled route table currently serves, and how many the ACME HostPolicy will
// issue a certificate for.
func (o ops) status(ctx context.Context, _ *noInput) (*ingressStatus, error) {
	if _, err := admin(ctx); err != nil {
		return nil, err
	}
	hosts, tlsHosts := o.s.State.engine.counts()
	return &ingressStatus{
		Role:         o.s.State.role,
		EdgeEnabled:  o.s.State.edge != nil,
		HTTPAddr:     o.s.State.edgeCfg.httpAddr,
		HTTPSAddr:    o.s.State.edgeCfg.httpsAddr,
		ACMEStaging:  o.s.State.edgeCfg.staging,
		ACMECacheDir: o.s.State.edgeCfg.cacheDir,
		LiveHosts:    hosts,
		TLSHosts:     tlsHosts,
		Proxy:        proxyImpl,
	}, nil
}

// The two constant strings the status/tls views report verbatim.
const (
	proxyImpl = "github.com/vulcand/oxy/v2 (weighted round-robin, Traefik lineage)"
	tlsNote   = "acmeEmail/staging apply on edge (re)start; extraHosts + route.tls hot-apply on reload"
)

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
	svcObjs, err := loadObjects[Upstream](ctx, s.State.store, KindService)
	if err != nil {
		return err
	}
	mwObjs, err := loadObjects[Middleware](ctx, s.State.store, KindMiddleware)
	if err != nil {
		return err
	}
	services := make(map[string]Upstream, len(svcObjs))
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
