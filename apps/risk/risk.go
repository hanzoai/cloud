// Package risk is Hanzo Risk: the scoring and decision plane for any entity a
// tenant has — an account, a transaction, a session, an agent, a merchant, a
// payout — and the shared model plane both it and the compliance face read.
//
// THREE VERBS, ONE CORE: decide, record, learn.
//
// ONE FACE, AND IT IS /v1/risk. POST /v1/risk/decide answers allow / challenge /
// review / restrict / block WITH A REASON; the planes around it (decisions,
// rules, lists, suppressions, controls, dictionary, activity) are how a tenant
// governs it; and score, train, search, state, features, snapshot and restore
// are how it learns. Deciding and learning are the same state, so they are the
// same face.
//
// THE THREE FACES DO NOT OVERLAP. /v1/aml is compliance — cases, sanctions,
// retention. /v1/ml is SERVING — apps/ml deploys InferenceServices there and
// /v1/ml/models means "models you serve", which is a different concept from
// "models that learn"; this app claims nothing under it. /v1/risk is this one.
//
// Fraud is a USE of /v1/risk, not a sibling of it, and neither is abuse, bots,
// account takeover, spam or pay-as-you-go abuse. There is no /v1/fraud.
//
// WHY IT IS IN CLOUD AND NOT A SERVICE OF ITS OWN. Being in cloud is what earns
// the five things a standalone engine would each have to grow: IAM-validated
// identity, the tenant gate, usage metering, billing and structured logs. None
// of them is free — every one is wired explicitly below, and the table in
// Mount's comment says where.
//
// WHAT IS LINKED FROM THE ENGINE, AND WHAT IS DELIBERATELY NOT. github.com/luxfi/aml
// is a MODULE DEPENDENCY, exactly as luxfi/kms and hanzoai/o11y are; no source is
// vendored. Only its transitively base-free packages are linked — types,
// velocity, anomaly, replay — because pkg/engine, pkg/measure and pkg/history all
// reach github.com/hanzoai/base/core and github.com/hanzoai/tasks through
// pkg/history's Base-backed store, and dragging an application framework and a
// workflow engine into a payment-authorization path is not a trade worth making.
// See rule.go for the measurement and the upstream fix.
package risk

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/openapi"
	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/zap-proto/zip"
)

// state is everything the surface reads. Every per-tenant plane it can reach —
// the model's counters, the velocity rings, the SQLite file and the governance
// cache — lives inside ONE resident per tenant (resident.go) and is SINGLE-WRITER
// by nature, which is why this app must run under the shard router that pins an
// org to one pod, and why Shutdown must snapshot.
//
// NOTHING PER-TENANT IS HELD HERE. A field on this struct is a field every tenant
// shares, and a shared store with a global cap is how one org silently evicts
// another's fraud controls. The residency is the only tenant-indexed thing in the
// app and it holds cells, never rows.
type state struct {
	// brand is the deployment's brand, the half of the tenant key that never
	// comes from a header.
	brand string
	// dataDir is where the per-tenant files live.
	dataDir string

	// res is the bounded set of live tenants: each one's own aggregates, own
	// model, own file, own cached rules.
	res *residency
	// runs is the process's bounded search queue.
	runs *runner
	// reg answers whether an agent reference is one the asking org registered.
	// An interface so the wire is one implementation and a test is another, with
	// no flag in production choosing between them.
	reg registry

	// digest names the model SHAPE in force. It is a pure function of the
	// configuration, identical for every tenant, so it is computed once here
	// rather than read off whichever tenant's store came to hand.
	digest string

	// bill is the shared per-org gate and meter, on the "risk" product.
	bill *cloud.ResourceMeter

	// warehouse records whether the feature tables were created. A false value
	// is an honest gap on the dictionary and the backfill, never a zero.
	mu        sync.Mutex
	warehouse bool
}

// Mount wires /v1/risk onto app.
//
// EVERY INHERITED CAPABILITY IS WIRED HERE, EXPLICITLY. Being embedded in cloud
// makes each one AVAILABLE; none of them is automatic:
//
//	IAM auth        SanitizeIdentity mints X-Org-Id from the verified bearer.
//	                Global (serve.go) — nothing to do here, and that is the point:
//	                this app never validates a token and never can.
//	tenant gate     cloud.Bridge() on EACH group, FIRST, before any leaf. A typed
//	                op receives only a context; Bridge is what parks the validated
//	                org and validated-ness in it. Installed after the leaves it
//	                would never run — fiber orders middleware by registration.
//	org sub-scope   principal.ValidatedProject, read in tenantOf.
//	meter + gate    cloud.NewResourceMeter(deps, "risk"); rm.Gate before priced
//	                work, rm.Meter after it. Wired per op, in typed.go.
//	logs            cloud.NewBase(deps, "risk") gives the scoped luxlog.
//	traces          global and already ZAP-native (OTLZ). This package imports no
//	                otlp transport, deliberately.
//	health          OwnsHealth on the plugin plus the real probe below.
//
// The route registration follows apps/ml's own form, including the one shape
// cmd/zipdoc can read: ONE `g := <router>.Group("/prefix")` per line.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("risk.Mount: nil app")
	}
	s, err := build(deps)
	if err != nil {
		return err
	}

	// The warehouse is not on any request path this app serves, so a warehouse
	// that is down at boot must not stop the decision plane from mounting. The
	// tables are ensured once, in the background, and the honest gap is recorded.
	go warehouse(s)
	// Memory comes back from a SILENT tenant on a timer, never under another
	// tenant's pressure — that ordering is what makes it reclaim and not
	// eviction. See residency.reclaimLocked.
	go reclaim(s)

	mount(s, app)
	s.Log.Info("risk surface mounted",
		"brand", deps.Brand, "env", deps.Env,
		"billing", s.State.bill.Enabled(), "model", s.State.digest,
		"text_max", textMax, "tenant_bytes", velBytes(), "tenant_keys", maxKeys(),
		"cell_bytes", cellBytes, "node_bytes", memBytes(), "reclaim_idle", idleReclaim().String())

	// Shutdown is registered by the plugin (plugin/risk/main.go) and calls back
	// here; holding the service in a package var would be a second owner of the
	// state, so the closure captures it instead.
	shutdown = func(context.Context) error { return teardown(s) }
	return nil
}

// warehouse creates the two feature planes and then keeps the NETWORK BASELINE
// current. It is the only background work this app does and it never touches a
// request path.
//
// The baseline recompute is here, on a timer, rather than on any route — which
// is what makes the cross-org surface unreachable by a caller. Nobody can time
// it, steer it, or observe its cost, and the statement it runs is a package
// constant with no placeholder, so nothing a caller sends reaches it. What it
// writes carries no tenant, no subject and no pseudonym: quantiles over a
// k-anonymous set of contributing orgs, and nothing else.
func warehouse(s *stateService) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := ensureTables(ctx); err != nil {
		cancel()
		s.Log.Warn("risk: feature tables unavailable; the dictionary, the peer comparison and the search sandbox will report an honest gap", "err", err)
		return
	}
	cancel()
	s.State.mu.Lock()
	s.State.warehouse = true
	s.State.mu.Unlock()

	for {
		bg, stop := context.WithTimeout(context.Background(), 10*time.Minute)
		if err := publishBaseline(bg); err != nil {
			s.Log.Warn("risk: the network baseline was not recomputed; peer comparison holds its last published day", "err", err)
		}
		stop()
		time.Sleep(baselineEvery)
	}
}

// baselineEvery is how often the network quantiles are recomputed. Six hours:
// the statement covers whole days, so anything faster republishes the same
// numbers, and anything slower lets a day go unpublished after a restart.
const baselineEvery = 6 * time.Hour

// reclaim returns memory from tenants that have gone SILENT.
//
// It runs on a timer rather than on admission pressure, and that is the whole
// design: a reclaim triggered by a NEW tenant's arrival would make one tenant's
// traffic the reason another tenant's aggregates went away, which is the
// cross-tenant eviction this app exists without. Here the only input is the
// reclaimed tenant's own silence.
func reclaim(s *stateService) {
	for {
		time.Sleep(sweepEvery)
		s.State.res.sweep(s.Log)
	}
}

// sweepEvery is how often idleness is checked. A fraction of idleReclaim, so a
// tenant is reclaimed near its own threshold rather than up to a threshold later.
const sweepEvery = 5 * time.Minute

// capacityAlarm is how long a refusal keeps the probe degraded. Longer than the
// sweep, so an operator sees the alarm across at least one chance for the node to
// free itself; short enough that a resolved event stops paging.
const capacityAlarm = 3 * sweepEvery

// build constructs the state. Separate from Mount because Mount also starts the
// background work and installs the routes, and a test wants the state without
// either — the same split apps/ml makes for the same reason.
func build(deps cloud.Deps) (*stateService, error) {
	if deps.Logger == nil {
		return nil, fmt.Errorf("risk.Mount: nil deps.Logger")
	}
	if deps.Brand == "" {
		// The brand is half the tenant key. A deployment that did not state one
		// cannot mint a key, and every request would 403 — better to say so at
		// boot than once per request.
		return nil, fmt.Errorf("risk.Mount: no brand, so no tenant key can be minted")
	}

	// The model SHAPE, from a store that will never hold a tenant. The digest is
	// a pure function of the configuration — the inventory in order and the
	// geometry parameters — so it can be settled once at boot, and reading it
	// off a throwaway is better than keeping a spare store around that something
	// could later be tempted to score with.
	shape, err := forest(anomaly.Config{}, aggregates())
	if err != nil {
		return nil, fmt.Errorf("risk.Mount: %w", err)
	}

	base := cloud.NewBase(deps, "risk")
	return &cloud.Service[state]{
		Base: base,
		State: state{
			brand:   deps.Brand,
			dataDir: deps.DataDir,
			res:     newResidency(deps.DataDir),
			runs:    newRunner(base.Log),
			reg:     peerRegistry{},
			digest:  shape.Digest(),
			bill:    cloud.NewResourceMeter(deps, "risk"),
		},
	}, nil
}

// stateService is the concrete service this package builds. An alias, so the
// helpers below read as functions of the service and no second type exists.
type stateService = cloud.Service[state]

// shutdown is set by Mount and read by the plugin. A nil value means the app
// never mounted, and tearing down what never came up is a no-op rather than a
// panic.
var shutdown func(context.Context) error

// Shutdown snapshots every resident tenant's model and closes the record planes.
//
// THIS IS NOT HOUSEKEEPING. cloud deploys with strategy Recreate at one replica:
// every rollout is a hard all-endpoint window AND drops every in-memory model.
// A model that comes back with nothing learned declines to score for the whole
// warm period, and a control that is off for however long that takes is a
// control that was off — reported, if anyone reads Refusal, and silent if not.
func Shutdown(ctx context.Context) error {
	if shutdown == nil {
		return nil
	}
	return shutdown(ctx)
}

func teardown(s *stateService) error {
	// Stop the search queue FIRST: a worker holds a tenant's file handle, and
	// closing under it would turn a rollout into a write error on a durable
	// report. Every in-flight run is cancelled and its row says so.
	s.State.runs.stop()
	kept, failed := s.State.res.close(s.Log)
	s.Log.Info("risk surface down", "models_kept", kept, "models_lost", failed)
	if failed > 0 {
		return fmt.Errorf("risk: %d tenant model(s) could not be snapshotted", failed)
	}
	return nil
}

// tenantState resolves the caller's scope and its resident — the tenant's own
// aggregates, model, file and cached governance, armed and restored if this
// process does not already hold them.
//
// Every typed op starts here, so there is ONE place the tenant is established,
// ONE place the bound is applied, and ONE place the learned state is reloaded.
// IT ALSO RESOLVES THE TENANT'S FILE, and that is what keeps a closed handle
// off the query path. residency.close retires EVERY cell the moment teardown
// runs — no idle requirement, and cloud deploys Recreate at ONE replica — so a
// request already holding a cell can find its handle gone, and a nil *sql.DB
// locks a nil mutex on its first use. Handing the handle out HERE, resolved and
// checked once, is what makes that unrepresentable downstream: an op cannot
// obtain a file without also obtaining the error that says it has none.
func tenantState(ctx context.Context, s *stateService) (scope, *resident, *sql.DB, error) {
	sc, err := tenantOf(ctx, s.State.brand)
	if err != nil {
		return scope{}, nil, nil, err
	}
	// The mint's own shape check, asserted at the boundary rather than assumed.
	// A regression here is the whole product: the key is the store index, the
	// history org column and the model's tree seed at once.
	if !qualified(s.State.brand, sc.tenant) {
		return scope{}, nil, nil, zip.ErrForbidden("the tenant key is not qualified")
	}
	r, err := s.State.res.of(sc.tenant, s.Log)
	if err != nil {
		return scope{}, nil, nil, err
	}
	db, err := r.file()
	if err != nil {
		return scope{}, nil, nil, err
	}
	return sc, r, db, nil
}

// governState is tenantState plus the ONE predicate that separates USING this
// plane from GOVERNING it: the caller must be an admin of its own org.
//
// EVERY WRITE BEHIND IT CAN TURN A CONTROL OFF. Shadow mode makes every rule
// observe and nothing act; retiring a rule deletes a detection; a blanket
// suppression mutes one; an allow-list entry is a bypass; appetite decides how
// much of the stream the model may even look at. A leaked low-privilege customer
// key that can reach any of those turns the customer's fraud plane off, and the
// customer finds out from a chargeback.
//
// It is org-scoped and org-scoped only. There is no cross-tenant surface in this
// app, so there is nothing for platform authority to reach and no reason to ask
// for it — conflating the two scopes is a privilege escalation, not a
// convenience.
//
// A governance write is also EMITTED with its actor, so the change is readable
// after the fact by whoever has to explain why a control was off.
func governState(ctx context.Context, s *stateService) (scope, *resident, *sql.DB, error) {
	sc, res, db, err := tenantState(ctx, s)
	if err != nil {
		return scope{}, nil, nil, err
	}
	if !sc.admin {
		return scope{}, nil, nil, zip.ErrForbidden("governing this tenant's risk controls requires an admin of this org; scoring and reading do not")
	}
	return sc, res, db, nil
}

// health is the app's real, fail-closed probe.
//
// UNTYPED BY DESIGN. A real probe answers 503 CARRYING THE DEGRADED REPORT AS
// ITS BODY, and a typed op reaches a non-2xx only by returning an error, which
// zip renders as its own envelope — dropping exactly the report the probe
// exists to deliver. Same reason apps/ml keeps its two health routes raw.
func health(s *stateService) func(*zip.Ctx) error {
	return func(c *zip.Ctx) error {
		s.State.mu.Lock()
		warehouse := s.State.warehouse
		s.State.mu.Unlock()
		tenants, refused, reclaimed, refusedAt := s.State.res.count()

		// THE SATURATION VIEW, and every number on it is one an operator acts
		// on. A bound that binds silently is the defect this app was held for,
		// so each way this node can run short says so here: `bytes` against
		// `bytes_max` is the headroom, `reclaimed` is how many cells the node
		// has taken back under pressure, `refused` is how many tenants it turned
		// away, and `strained` is how many resident tenants are reading partial
		// rings right now. A control that switches off quietly is worse than no
		// control.
		report := map[string]any{
			"status":    "ok",
			"model":     s.State.digest,
			"tenants":   tenants,
			"bytes":     s.State.res.bytes(),
			"bytes_max": memBytes(),
			"refused":   refused,
			"reclaimed": reclaimed,
			"strained":  s.State.res.strained(),
			"warehouse": warehouse || datastore.Ready(),
			"billing":   s.State.bill.Enabled(),
		}
		// The DECISION plane is what this app promises, and it does not need the
		// warehouse: rings are in memory, rules and lists are the tenant's own
		// file, the model is in process. The probe is degraded only when
		// something the hot path actually needs is missing.
		if s.State.res == nil || s.State.runs == nil || s.State.dataDir == "" {
			report["status"] = "degraded"
			report["error"] = "the decision plane is not constructed"
			return c.JSON(http.StatusServiceUnavailable, report)
		}
		// A node that has REFUSED a tenant is out of room, and the tenants it
		// turned away are unprotected. That is a capacity event an operator must
		// be paged for, not a counter someone finds later — so the probe goes
		// degraded while it stands, and the reason names itself.
		//
		// WHILE IT STANDS, not forever. The counter is monotone, so latching on
		// it would leave the pod unready for the rest of its life over one
		// refusal — turning a capacity event for ONE tenant into an outage for
		// every tenant the node is serving perfectly well, which is a worse
		// version of the same defect. The alarm clears once the node has gone
		// capacityAlarm without turning anyone away; the count stays on the
		// report either way, so nothing is hidden.
		if !refusedAt.IsZero() && time.Since(refusedAt) < capacityAlarm {
			report["status"] = "degraded"
			report["error"] = "this node has no memory left for another tenant and has refused admissions; it will not take a live tenant's state to make room"
			return c.JSON(http.StatusServiceUnavailable, report)
		}
		return c.JSON(http.StatusOK, report)
	}
}

// The prose for the ONE route above that is untyped by design. zipdoc lifts
// prose from a typed handler's doc comment, and this is not one — so without a
// Describe the probe would publish an operationId and nothing else. Declared
// beside the wire fact it belongs to.
func init() {
	openapi.Describe("/v1/risk/health", http.MethodGet,
		"Report whether the decision plane can decide",
		"Answers 200 with a report of what is up, or 503 CARRYING THE SAME REPORT as its "+
			"body — which is the whole point of a real probe, and the reason this one route is "+
			"not a typed op: a typed op reaches a non-2xx only by returning an error, and that "+
			"renders as an envelope with the report dropped.\n\n"+
			"The report names the model digest in force, how many tenants this process holds "+
			"resident, whether the feature warehouse is reachable, and whether metering is "+
			"configured. Only the first is load-bearing for a decision: the rings are in "+
			"memory, the rules and lists are the tenant's own file, and the model is in "+
			"process, so a decision does NOT need the warehouse and the probe stays green "+
			"without it. A warehouse that is down costs the field dictionary and the search "+
			"sandbox, and is reported as exactly that rather than as a failure of the "+
			"authorization path.")
}
