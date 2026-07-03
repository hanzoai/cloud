// Package tasksvc mounts the Hanzo Tasks HTTP + UI surface natively onto the
// unified cloud binary per HIP-0106 — the follow-up named in cloud's durable.go
// ("consolidating that surface into cloud"). Tasks is the durable
// workflow/activity engine (event-sourced, exactly-once, crash-recovering)
// previously fronted by a standalone tasksd + cluster Service.
//
// ONE ENGINE. cloud already embeds the single in-process tasks engine in
// durable.go (wireDurableIngest → cloud.EmbeddedTasks), shared by ai's durable
// ingest. This subsystem does NOT Embed a second engine; it mounts that ONE
// engine's HTTP handlers on the shared zip mux, so the Tasks product (console/
// studio) reads the SAME durable state as ai ingest — one engine, one binary,
// one way. The engine is created after MountAll, so the surface resolves it
// LAZILY per request (503 until it is live).
//
// Surface (all under /v1/tasks/*, plus the embedded React UI at /_/tasks/*):
//
//	/v1/tasks/health                    generic liveness (cloud's per-subsystem contract)
//	/v1/tasks/settings                  capability flags (open bootstrap)
//	/v1/tasks/cluster[/health]          cluster status (open probes)
//	/v1/tasks/namespaces|nexus|...      engine JSON API (identity-gated)
//	/v1/tasks/mcp                       MCP tool surface (identity-gated)
//	/v1/tasks/events                    SSE realtime stream (identity-gated)
//	/_/tasks/*                          embedded React UI
//
// Identity: cloud's gateway validates the IAM JWT and mints X-Org-Id / X-User-Id
// (HIP-0026); clients/principal treats a credential-minted X-User-Id as the ONE
// trust signal. gate refuses data requests lacking it (403, never the unscoped
// store) and threads the validated org into the engine via
// tasks/pkg/auth.WithIdentity, so per-(org,ns) shard scoping applies.
package tasksvc

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/hanzoai/cloud"
	tasksauth "github.com/hanzoai/tasks/pkg/auth"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	tasksui "github.com/hanzoai/tasks/ui"
	"github.com/zap-proto/zip"
)

// Mount adapts the shared engine's HTTP surface + the static UI onto app. It
// creates NO engine — the ONE engine lives in cloud.EmbeddedTasks (durable.go).
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("tasksvc.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("tasksvc.Mount: nil deps.Logger")
	}

	h := zip.AdaptNetHTTP(&surface{})
	app.All("/v1/tasks", h)
	app.All("/v1/tasks/*", h)

	// The UI is a static asset bundle — engine-independent, mount directly.
	ui := zip.AdaptNetHTTP(http.StripPrefix("/_/tasks", tasksui.Handler()))
	app.All("/_/tasks", ui)
	app.All("/_/tasks/*", ui)

	deps.Logger.New("subsystem", "tasks").Info("tasks HTTP+UI surface mounted (shared in-process engine)", "brand", deps.Brand)
	return nil
}

// surface serves the Tasks HTTP API off cloud.EmbeddedTasks. The engine is
// created after MountAll, so it is resolved lazily on the first request (by which
// point Serve has run wireDurableIngest); the per-engine route mux is then cached
// once. A nil engine (embed failed / not yet wired) fails soft with 503.
type surface struct {
	once sync.Once
	mux  http.Handler
}

func (s *surface) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	srv := cloud.EmbeddedTasks()
	if srv == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"tasks engine not ready","code":503}`))
		return
	}
	s.once.Do(func() { s.mux = httpMux(srv) })
	s.mux.ServeHTTP(w, r)
}

// httpMux mirrors tasksd's buildHTTP route composition, swapping tasksd's
// JWT-validating identity middleware for gate — cloud's gateway already validated
// the JWT, so gate trusts its minted X-User-Id instead of re-verifying. net/http
// ServeMux longest-prefix matching makes the specific routes win over the
// /v1/tasks/ catch-all, exactly as in standalone tasksd:
//
//   - /v1/tasks/settings, /v1/tasks/cluster[/health] are OPEN (capability flags
//     and probes carry no per-org data), matching tasksd registering them outside
//     its identity wrap. (/v1/tasks/health is the generic per-subsystem liveness
//     route cloud registers before MountAll, which wins ahead of this mux.)
//   - the data surface (the /v1/tasks/ catch-all, /v1/tasks/mcp, /v1/tasks/events)
//     is gated: a request without a validated principal is refused, exactly as the
//     rest of the cloud data plane (clients/principal.Tenant) and tasksd's
//     RequireIdentity(require=true) refuse it.
func httpMux(srv *tasks.Embedded) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/tasks/settings", srv.HTTPHandler())
	mux.Handle("/v1/tasks/cluster", srv.ClusterHandler())
	mux.Handle("/v1/tasks/cluster/health", srv.ClusterHandler())
	mux.Handle("/v1/tasks/mcp", gate(srv.MCPHandler()))
	mux.Handle("/v1/tasks/events", gate(srv.EventsHandler()))
	mux.Handle("/v1/tasks/", gate(srv.HTTPHandler()))
	return mux
}

// gate enforces cloud's data-plane trust boundary on the Tasks surface and injects
// the validated tenant into the handler context. The gateway (HIP-0026) mints
// X-User-Id ONLY from a verified credential and X-Org-Id from the validated owner
// claim; the fiber adaptor forwards those request headers verbatim, so a non-empty
// X-User-Id is the SAME trust signal clients/principal.Validated uses. Absent it,
// the request is the anonymous-forge path and is refused (403) — never served
// another tenant's data nor the unscoped store. Present, the org is threaded into
// the engine via tasks/pkg/auth.WithIdentity so per-(org,ns) shard scoping applies.
func gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get(tasksauth.HeaderUserID)
		if user == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"identity required","code":403}`))
			return
		}
		ctx := tasksauth.WithIdentity(r.Context(), r.Header.Get(tasksauth.HeaderOrgID), user, r.Header.Get(tasksauth.HeaderUserEmail))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func init() {
	// Order 147: before hanzoai/ai (150) so /v1/tasks/* wins over ai's /v1/*
	// catch-all. No ShutdownFunc — the shared engine's lifecycle is owned by
	// durable.go/Serve, not this consuming surface.
	cloud.Register("tasks", 147, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("tasksvc.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}
