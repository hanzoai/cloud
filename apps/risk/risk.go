// Package risk is Hanzo Risk: the scoring and decision plane for any entity a
// tenant has — an account, a transaction, a session, an agent, a merchant, a
// payout — and the shared model plane both it and the compliance face read.
//
// THREE VERBS, ONE CORE: decide, record, learn.
//
//	/v1/risk   decide + record. One hot op (POST /v1/risk/decide) answers
//	           allow / challenge / review / restrict / block WITH A REASON, and
//	           the planes around it (decisions, rules, lists, suppressions,
//	           controls, dictionary, activity) are how a tenant governs it.
//	/v1/ml     learn. train, exhaustive-search, score, plus the state a reviewer
//	           reads and the snapshot an auditor pins.
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

// state is everything the surface reads. The per-tenant halves — the model's
// counters, the velocity rings and the SQLite file — are SINGLE-WRITER by
// nature, which is why this app must run under the shard router that pins an org
// to one pod, and why Shutdown must snapshot.
//
// THE IN-MEMORY PLANES ARE NOT HERE, and that is the point. One velocity store
// and one forest on this struct meant every tenant shared them under a GLOBAL
// cap, so one org's volume evicted another org's counters and another org's
// learned model. They live on the tenant's own cell now (store.go), each under
// that tenant's own bound (bound.go).
type state struct {
	// brand is the deployment's brand, the half of the tenant key that never
	// comes from a header.
	brand string
	// dataDir is where the per-tenant files live.
	dataDir string

	// shelf is the bounded registry of live tenants: one cell each, holding that
	// tenant's file, its aggregates and its model.
	shelf *shelf
	// inflight is the per-tenant bound on measurement: one at a time, per tenant,
	// so a caller that loops the measurement surface degrades only itself.
	inflight *inflight
	// running is the per-tenant bound on the exhaustive search: one at a time,
	// per tenant, for the same reason and with the same shape.
	running *inflight

	// digest is the model SHAPE — the feature inventory in order and the
	// detector's geometry parameters. It is a pure function of the configuration,
	// identical for every tenant, so it is settled once at boot rather than read
	// off whichever tenant's forest is at hand.
	digest string

	// bill is the shared per-org gate and meter, on the "risk" product.
	bill *cloud.ResourceMeter

	// warehouse records whether the feature tables were created. A false value
	// is an honest gap on the dictionary and the backfill, never a zero.
	mu        sync.Mutex
	warehouse bool
}

// Mount wires /v1/risk and the native /v1/ml leaves onto app.
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
// The route registration follows apps/ml exactly, including the one form
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
	// Memory comes back from a tenant's OWN silence on a timer, so a quiet tenant
	// releases its rings without a busy one having to need them first.
	go reclaim(s)

	mount(s, app)
	s.Log.Info("risk surface mounted",
		"brand", deps.Brand, "env", deps.Env,
		"billing", s.State.bill.Enabled(), "model", s.State.digest,
		"tenants", tenantMax(), "keys_per_tenant", maxKeys())

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

// reclaim retires tenants that have gone silent for longer than their rings hold
// anything. It is the only thing in this process that releases a tenant's
// memory, and it is driven by that tenant's own idleness — never by another
// tenant's arrival, which is what makes it reclaim rather than eviction.
func reclaim(s *stateService) {
	for {
		time.Sleep(sweepEvery)
		s.State.shelf.sweep()
	}
}

// sweepEvery is how often the reclaim runs. It is far shorter than the idleness
// it looks for, so the granularity of "when memory comes back" is minutes rather
// than a multiple of the threshold.
const sweepEvery = 10 * time.Minute

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

	// The model SHAPE, off a forest that will never hold a tenant. The digest is
	// a pure function of the configuration — the inventory in order and the
	// geometry parameters — so it can be settled once at boot, and reading it off
	// a throwaway is better than keeping a spare store around that something
	// could later be tempted to score with.
	//
	// SHADOW IS THE DEFAULT AT THE ENGINE TOO (forest forces it), and the
	// per-tenant switch in the record plane is what turns a tenant live. Two gates
	// in series rather than one: a deployment-wide flag flipped by mistake still
	// cannot make a tenant act.
	shape, err := forest(anomaly.Config{}, aggregates())
	if err != nil {
		return nil, fmt.Errorf("risk.Mount: %w", err)
	}

	base := cloud.NewBase(deps, "risk")
	return &cloud.Service[state]{
		Base: base,
		State: state{
			brand:    deps.Brand,
			dataDir:  deps.DataDir,
			shelf:    newShelf(base),
			inflight: newInflight("a measurement"),
			running:  newInflight("an exhaustive search"),
			digest:   shape.Digest(),
			bill:     cloud.NewResourceMeter(deps, "risk"),
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
	kept, failed := s.State.shelf.close()
	s.Log.Info("risk surface down", "models_kept", kept, "models_lost", failed)
	if failed > 0 {
		return fmt.Errorf("risk: %d tenant model(s) could not be snapshotted", failed)
	}
	return nil
}

// tenantCell resolves the caller's scope and its live state: its own file, its
// own aggregates and its own model, restored from its own snapshot the first
// time this process arms it. Every typed op starts here, so the tenant is
// established in one place, admitted in one place and armed in one place.
func tenantCell(ctx context.Context, s *stateService) (scope, *cell, error) {
	sc, err := tenantOf(ctx, s.State.brand)
	if err != nil {
		return scope{}, nil, err
	}
	// The mint's own shape check, asserted at the boundary rather than assumed.
	// A regression here is the whole product: the key is the store index, the
	// history org column and the model's tree seed at once.
	if !qualified(s.State.brand, sc.tenant) {
		return scope{}, nil, zip.ErrForbidden("the tenant key is not qualified")
	}
	c, err := s.State.shelf.of(sc.tenant)
	if err != nil {
		return scope{}, nil, err
	}
	return sc, c, nil
}

// tenantState is the file-only door for the ops that touch the record plane and
// not the two in-memory planes.
func tenantState(ctx context.Context, s *stateService) (scope, *sql.DB, error) {
	sc, c, err := tenantCell(ctx, s)
	if err != nil {
		return scope{}, nil, err
	}
	return sc, c.db, nil
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
		tenants, refused, refusedAt := s.State.shelf.count()

		report := map[string]any{
			"status":   "ok",
			"model":    s.State.digest,
			"tenants":  tenants,
			"capacity": tenantMax(),
			// strained COUNTS the tenants whose own aggregates are at their own
			// cardinality bound, so a count they read may under-state their own
			// traffic. It is on the probe because a partial ring reads exactly like
			// a quiet one, and nobody goes looking for a control that went quiet —
			// and it is a NUMBER, because this route is unauthenticated by design
			// and a tenant roster served to an anonymous GET is a customer list.
			// The tenant learns about its OWN ring on its own scoped surface,
			// GET /v1/ml/state.
			"strained":  s.State.shelf.strained(),
			"warehouse": warehouse || datastore.Ready(),
			"billing":   s.State.bill.Enabled(),
		}
		// The DECISION plane is what this app promises, and it does not need the
		// warehouse: rings are in memory, rules and lists are the tenant's own
		// file, the model is in process. The probe is degraded only when
		// something the hot path actually needs is missing.
		if s.State.shelf == nil || s.State.dataDir == "" {
			report["status"] = "degraded"
			report["error"] = "the decision plane is not constructed"
			return c.JSON(http.StatusServiceUnavailable, report)
		}
		// A REFUSED ADMISSION IS A PAGE, not a log line. This pod turned a tenant
		// away rather than evict an incumbent, which is the right answer and also
		// an outage for whoever was turned away — so it degrades the probe until
		// an operator shards or raises the ceiling.
		if refused > 0 && time.Since(refusedAt) < capacityAlarm {
			report["status"] = "degraded"
			report["refused"] = refused
			report["refused_at"] = refusedAt.UTC().Format(time.RFC3339)
			report["error"] = "this node is at its tenant ceiling and is refusing new tenants; shard, or raise RISK_TENANTS to what the pod's memory allows"
			return c.JSON(http.StatusServiceUnavailable, report)
		}
		return c.JSON(http.StatusOK, report)
	}
}

// capacityAlarm is how long a refusal keeps the probe degraded. Three sweeps:
// long enough that a refusal cannot be missed between two scrapes, short enough
// that a pod which has since reclaimed room reports itself healthy again.
const capacityAlarm = 3 * sweepEvery

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
