// Package tasks is Hanzo Tasks: durable workflows that survive a crash, with
// every run visible and replayable.
//
// Tasks is the durable workflow/activity engine (event-sourced, exactly-once,
// crash-recovering) previously fronted by a standalone tasksd + cluster
// Service; this mounts its HTTP + UI surface natively onto the unified cloud
// binary per HIP-0106 — the follow-up named in cloud's durable.go
// ("consolidating that surface into cloud").
//
// ONE ENGINE. cloud already embeds the single in-process tasks engine in
// durable.go (wireDurableIngest → cloud.EmbeddedTasks), shared by ai's durable
// ingest. This subsystem does NOT Embed a second engine; it mounts that ONE
// engine's HTTP handlers on the shared zip mux, so the Tasks product (console/
// studio) reads the SAME durable state as ai ingest — one engine, one binary,
// one way. The engine is created after MountAll, so the surface resolves it
// LAZILY per request (503 until it is live).
//
// Surface (all under /v1/tasks/*; the studio is its own image on tasks.hanzo.ai):
//
//	/v1/tasks/health                    generic liveness (cloud's per-subsystem contract)
//	/v1/tasks/settings                  capability flags (open bootstrap)
//	/v1/tasks/cluster[/health]          cluster status (open probes)
//	/v1/tasks/namespaces|nexus|...      engine JSON API (identity-gated)
//	/v1/tasks/mcp                       MCP tool surface (identity-gated)
//	/v1/tasks/events                    SSE realtime stream (identity-gated)
//
// Identity: cloud's gateway validates the IAM JWT and mints X-Org-Id / X-User-Id
// (HIP-0026). gate resolves those two through apps/principal — the ONE place the
// cloud data plane turns a request into an org — and refuses (403, never the
// unscoped store) anything that decision refuses, then threads the validated org
// into the engine via tasks/pkg/auth.WithIdentity, so per-(org,ns) shard scoping
// applies.
package tasks

import (
	"fmt"
	"net/http"
	"sync"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/cron"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	tasksauth "github.com/hanzoai/tasks/pkg/auth"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	"github.com/zap-proto/zip"
)

// bare is the whole answer at /v1/tasks, on every method: the engine's ServeMux
// derives a subtree redirect from its /v1/tasks/ pattern before any handler runs,
// so the address is a signpost rather than a resource. TestBareNounIsARedirect
// measures it.
const bare = "Answers 307 with Location /v1/tasks/ — this address serves nothing itself. " +
	"The redirect is a routing fact derived from the engine's subtree, decided before any " +
	"handler runs, so it is the same on every method.\n\n"

// engine is the sentence the five operations at /v1/tasks/* share: what the one
// wildcard actually fronts, and the gate in front of it.
const engine = "\n\nThis single address fronts the whole durable-workflow engine — namespaces, " +
	"workflows, schedules, batches, deployments, nexus, task queues, workers, activities and " +
	"the rest — matched by path segment inside the engine's own router, which is why the " +
	"document publishes one wildcard rather than sixty-four operations.\n\n" +
	"Most of it requires a validated principal and is refused 403 otherwise; the settings and " +
	"cluster probes are open, because capability flags and cluster health carry no tenant " +
	"data. A principal that is validated but carries NO org is refused too — that request " +
	"would otherwise read the shared unscoped store instead of anyone's shard, so it fails " +
	"closed. Admitted, the org, project and user are threaded into the engine, and every read " +
	"and write lands in that tenant's own shard.\n\n" +
	"Two things about the answers differ from the rest of this API and will bite a generic " +
	"client: errors here carry `code` as a NUMBER rather than the usual `status`, and the " +
	"address serves several content types — JSON, plain-text refusals, and an event stream at " +
	"the events path. Until the engine is wired the whole surface answers 503."

// The prose for the twenty operations these four mounts publish. Not one of them
// is a typed op, and each is refused for a fact typed_wire_test.go MEASURES
// rather than for want of an edit — see Mount's note. That leaves openapi.Describe
// as the seam: it declares the prose beside the wire fact, keyed on (method,
// path), rendering only while the router serves the route. It does not make these
// typed, and the place they become typed is still hanzoai/tasks; it does mean the
// document, the generated SDKs and the spec-derived CLI stop offering twenty calls
// they cannot explain.
func init() {
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		openapi.Describe("/v1/tasks", m,
			"Redirect to the tasks API root",
			bare+"A 307 preserves both the method and the body, so a client that follows "+
				"redirects re-sends the request unchanged to /v1/tasks/ and nothing is lost. A "+
				"client that does NOT follow redirects sees only the 307 and performs no work — "+
				"address /v1/tasks/ directly and the hop disappears.")
	}

	openapi.Describe("/v1/tasks/*", http.MethodGet,
		"Read workflow state from the durable engine",
		"Reads from the durable engine: list namespaces, workflows, schedules, batches, "+
			"deployments, task queues, workers and search attributes, fetch one workflow with "+
			"its history, or subscribe to the realtime event stream. The cluster and settings "+
			"probes are on this method too."+engine)
	openapi.Describe("/v1/tasks/*", http.MethodPost,
		"Start workflows and act on running ones",
		"Everything that changes the engine's state: register a namespace, start a workflow "+
			"or signal-with-start one, and signal, query, cancel, terminate or reset a workflow "+
			"that is already running. The MCP tool surface is on this method as well, and is "+
			"the one part of it that refuses a non-POST with a plain-text 405.\n\n"+
			"The engine is event-sourced and exactly-once, so an action is durable once it is "+
			"accepted and survives a process crash — a started workflow resumes rather than "+
			"restarts."+engine)
	openapi.Describe("/v1/tasks/*", http.MethodDelete,
		"Delete an engine resource",
		"Removes a resource the engine owns — a namespace and the like — inside the caller's "+
			"own tenant shard.\n\n"+
			"It is the narrowest of the three working methods: most of the engine's surface is "+
			"read on GET and acted on with POST, so a delete that finds no route for its path "+
			"answers the same plain-text 404 any unrouted path does."+engine)
	openapi.Describe("/v1/tasks/*", http.MethodPut,
		"Not served by the engine",
		"Published because this address accepts every method, but the engine routes no PUT: "+
			"the answer is a plain-text 404, not a 405, and no state changes.\n\n"+
			"Nothing here is updated by replacement. The engine is event-sourced — a workflow "+
			"is changed by signalling, cancelling, terminating or resetting it, all of which "+
			"are POST — so a client reaching for PUT wants POST."+engine)
	openapi.Describe("/v1/tasks/*", http.MethodPatch,
		"Not served by the engine",
		"Published because this address accepts every method, but the engine routes no "+
			"PATCH: the answer is a plain-text 404, not a 405, and no state changes.\n\n"+
			"There is no partial update on this surface. State advances by appending events, so "+
			"the operations that change a running workflow — signal, cancel, terminate, reset — "+
			"are all POST."+engine)

	// The methods left over on the ENGINE's addresses. Bound with All(), so they
	// publish every method this generator knows and the ones above are only the
	// ones that DO something. DescribeRest covers the remainder from the
	// generator's own set, so a method added there is covered the day it appears
	// rather than published bare — which is what a hand-copied list here had
	// already produced for OPTIONS and TRACE. The UI addresses are the helper's.
	for _, p := range []string{"/v1/tasks", "/v1/tasks/*"} {
		openapi.DescribeRest(p,
			"Not routed by the durable engine",
			"Published because this address accepts every method, but the engine routes nothing "+
				"here: the request arrives as an unrouted path and no workflow is read or started.")
	}

}

// Mount adapts the shared engine's HTTP surface + the static UI onto app. It
// creates NO engine — the ONE engine lives in cloud.EmbeddedTasks (durable.go).
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("tasks.Mount: nil app")
	}

	// NOT TYPED OPS, and the reason is the wire rather than the want of an edit.
	// A typed op (zip.Get[In, Out]) is the ONE registry entry every projection
	// reads, so what stays out of it publishes no schema, no prose, no MCP tool,
	// no CLI command and no SDK method. The four mounts below are 28 published
	// operations (plugin/tasks/openapi.json) with nothing said about any of them.
	// Each address is refused for a fact typed_wire_test.go MEASURES, so a refusal
	// here cannot outlive its reason:
	//
	// /v1/tasks answers 307 to /v1/tasks/ on every method — the engine's own
	// ServeMux decides it from its subtree pattern, before a handler runs. A typed
	// op answers 200, 204 or a 2xx it DECLARED; 307 and Location are not in its
	// vocabulary. TestBareNounIsARedirect.
	//
	// /v1/tasks/* is ONE route over 64 engine operations this router never sees.
	// They are matched by path SEGMENT inside hanzoai/tasks' own ServeMux
	// (pkg/tasks/embed.go, HTTPHandler) rather than by patterns, so there is no
	// route here to type; their inputs are anonymous structs local to that
	// module's handlers, so there is no named type to type it with; and the engine
	// hands cloud its surface only as http.Handler (HTTPHandler / ClusterHandler /
	// MCPHandler / EventsHandler), its programmatic seam — View plus the three
	// *ForOrg helpers — reaching 13 of the 64, so there is no value to answer
	// with either. The one route also carries four content types at once (the JSON
	// API, two text/plain refusals, an event STREAM), 12 of its verbs run on a
	// malformed body a typed op would 400, and its errors carry `code` as a number
	// where zip's carry `status`. TestOneWildcardCarriesFourContentTypes,
	// TestCancelIgnoresAMalformedBody, TestEngineErrorEnvelopeIsNotZips.
	//
	// The place these operations CAN become typed is hanzoai/tasks, which OWNS the
	// surface. Typing them here would put a second copy of that module's route
	// table in cloud, free to drift from the one that answers the requests — and
	// re-shaping a relayed answer to fit a local struct is the wire break this
	// migration exists to avoid.
	h := zip.AdaptNetHTTP(&surface{})
	app.All("/v1/tasks", h)
	app.All("/v1/tasks/*", h)

	// THE STUDIO IS NOT HERE. It is its own image (ghcr.io/hanzoai/admin-tasks, built
	// from hanzoai/admin apps/tasks at base '/') on its own host, tasks.hanzo.ai,
	// like todo and meet before it. It used to be //go:embed'd here and served at
	// /tasks/*, which put every UI change behind a full Go release and put a
	// browser app on the API origin.
	//
	// ONE ORIGIN SURVIVES THE MOVE, and that is why the studio could go. It reads
	// this surface with same-origin credentials and carries no bearer, so a bundle
	// on one host and an API on another would send no credential at all. The edge
	// splits tasks.hanzo.ai instead: the bundle from its own pods, /v1/tasks to
	// this binary (universe infra/k8s/ingress/routes.yaml), which is the same
	// split console.hanzo.ai runs. The browser sees one origin either way, so
	// nothing about the requests that arrive here changes.

	luxlog.Default().New("subsystem", "tasks").Info("tasks HTTP+UI surface mounted (shared in-process engine)", "brand", deps.Brand)

	// Platform cron is a FACET of tasks, not its own subsystem: it mounts NO routes,
	// only registers durable schedules on the SAME shared engine (cloud.EmbeddedTasks)
	// this surface fronts. Folded in here as a terminal sub-mount (was a separate Wire
	// entry) so there is ONE tasks subsystem. cron.Mount just launches a background
	// starter that waits for the engine wired after MountAll — no ordering dependency.
	if err := cron.Mount(app, deps); err != nil {
		return err
	}

	// The engine this process owns, readable by the processes that do not own one.
	// See activities_rpc.go: the BYO fleet is written through the surface above and
	// rendered by visor, which embeds a different engine entirely.
	exposeActivities()
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
//     rest of the cloud data plane (clients/principal.Org) and tasksd's
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
// X-User-Id ONLY from a verified credential, X-Org-Id from the validated owner
// claim, and X-Project-Id from the validated project claim; the fiber adaptor
// forwards those request headers verbatim, so the pair is the SAME two facts
// clients/principal decides on. A request that fails that decision is the
// anonymous-forge path and is refused (403) — never served another tenant's data
// nor the unscoped store. Admitted, the full org/project/user identity is threaded
// into the engine via tasks/pkg/auth.WithIdentity so per-(org,ns) shard scoping
// (and the project↔namespace convention) applies.
//
// The decision is principal.OrgOf and NOT a local copy of it. principal is the
// ONE place the cloud data plane turns a request into an org, and OrgOf is that
// decision over the only two facts it turns on — the validated user claim and the
// org claim — taken as plain strings precisely so a reader that holds headers
// rather than a *zip.Ctx (this one; the internal plane's capability envelope) asks
// the same function. A local `user != ""` check is not the same rule: it admits a
// validated principal carrying NO org, which the engine reads as the ZERO
// Principal — the shared unscoped store (hanzoai/tasks store/principal.go:31,
// <root>/_/_/_), not that caller's shard. cloud's identity boundary produces that
// exact request on purpose: SanitizeIdentity mints X-User-Id from the claims but
// leaves X-Org-Id unset whenever homeOrg() is empty (auth_identity.go:289 — a
// token with no `orgs` claim), so it fails closed everywhere principal is asked
// and, until this call, open here. TestOrglessValidatedPrincipalIsRefused.
func gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get(tasksauth.HeaderUserID)
		org, ok := principal.OrgOf(user, r.Header.Get(tasksauth.HeaderOrgID))
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"identity required","code":403}`))
			return
		}
		ctx := tasksauth.WithIdentity(r.Context(),
			org,
			r.Header.Get(tasksauth.HeaderProjectID),
			user,
			r.Header.Get(tasksauth.HeaderUserEmail))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
