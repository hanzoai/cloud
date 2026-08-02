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
	"github.com/luxfi/aml/pkg/velocity"
	"github.com/zap-proto/zip"
)

// state is everything the surface reads. Three of the four members are
// SINGLE-WRITER by nature — the model's counters, the velocity rings and the
// per-tenant SQLite file — which is why this app must run under the shard
// router that pins an org to one pod, and why Shutdown must snapshot.
type state struct {
	// brand is the deployment's brand, the half of the tenant key that never
	// comes from a header.
	brand string
	// dataDir is where the per-tenant files live.
	dataDir string

	// vel holds the in-memory sliding aggregates every decision reads. Constant
	// time, fixed memory per key, bounded cardinality with LRU eviction.
	vel *velocity.Store
	// shape is the geometry this process ships: what a tenant runs before it has
	// promoted a version of its own, and what a fit inherits when the caller
	// names no shape. It is anomaly's own default, read back once at build so
	// there is one definition of it and it is the engine's.
	shape candidate
	// digest fingerprints that geometry together with the feature inventory in
	// force. It is what a version is pinned against and what an auditor compares:
	// learned state estimated under one inventory cannot be restored into
	// another, and the digest is what Restore checks.
	digest string
	// stable holds one store per (tenant, version). There is no store in this
	// package that is not one tenant's — a store shared across tenants evicts
	// across them, and a shared store keyed on the GEOMETRY also makes two
	// versions of one shape into one model. See lifecycle.go.
	stable *stable
	// bench is the bounded, cancellable estimation queue. An estimation replays
	// thousands of rows through a fresh forest on the pod that is also serving
	// authorisations, so it is never run in a request.
	bench *bench
	// shelf holds the per-tenant record planes.
	shelf *shelf

	// bill is the shared per-org gate and meter, on the "risk" product.
	bill *cloud.ResourceMeter

	// warehouse records whether the feature tables were created. A false value
	// is an honest gap on the dictionary and the backfill, never a zero.
	warehouse bool

	// mu guards the fields above that are written after mount.
	mu sync.Mutex
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

	// The second loop: re-estimate a tenant's model when its schedule says so,
	// and record the champion's drift when it degrades. It walks only the tenants
	// this process holds open, so it costs nothing on a quiet deployment. See
	// lifecycle.go.
	go lifecycle(s)

	mount(s, app)
	s.Log.Info("risk surface mounted",
		"brand", deps.Brand, "env", deps.Env,
		"billing", s.State.bill.Enabled(), "model", s.State.digest)

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

	vel := velocity.New(velocity.Config{})
	// The shipped geometry and the digest that names it, read back from a store
	// built with anomaly's own defaults and then let go. Derived rather than
	// restated: the default has ONE definition and it is the engine's, and the
	// digest a tenant's own store will report is a pure function of that
	// geometry plus the inventory — which risk_test asserts, because a shipped
	// snapshot that will not restore is a control that comes back warming.
	//
	// SHADOW IS THE DEFAULT AT THE ENGINE TOO, and the per-tenant switch in the
	// record plane is what turns a tenant live. Two gates in series rather than
	// one: a deployment-wide flag flipped by mistake still cannot make a tenant
	// act.
	tpl, err := anomaly.New(anomaly.Config{Shadow: true}, vel)
	if err != nil {
		return nil, fmt.Errorf("risk.Mount: %w", err)
	}
	shape, digest := shapeOf(tpl.Config()), tpl.Digest()

	return &cloud.Service[state]{
		Base: cloud.NewBase(deps, "risk"),
		State: state{
			brand:   deps.Brand,
			dataDir: deps.DataDir,
			vel:     vel,
			shape:   shape,
			digest:  digest,
			stable:  newStable(shape, vel),
			bench:   newBench(),
			shelf:   newShelf(deps.DataDir),
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
	var kept, failed int
	for _, t := range s.State.shelf.tenants() {
		db, err := s.State.shelf.open(t)
		if err != nil {
			failed++
			s.Log.Error("risk: a tenant's record plane could not be opened to keep its state", "tenant", t.String(), "err", err)
			continue
		}
		// Every resident geometry, not only the shipped one: a tenant running a
		// promoted fit keeps its state under that fit, and a champion that comes
		// back with nothing learned declines to score for its whole warm period.
		if err := keepAll(s, t, db); err != nil {
			failed++
			s.Log.Error("risk: a tenant's learned state was not kept", "tenant", t.String(), "err", err)
			continue
		}
		kept++
	}
	s.State.shelf.close()
	s.Log.Info("risk surface down", "models_kept", kept, "models_lost", failed)
	if failed > 0 {
		return fmt.Errorf("risk: %d tenant model(s) could not be snapshotted", failed)
	}
	return nil
}

// tenantState resolves the caller's scope, opens its record plane, and restores
// its model the first time this process sees it. Every typed op starts here, so
// there is one place the tenant is established and one place the restore
// happens.
func tenantState(ctx context.Context, s *stateService) (scope, *sql.DB, error) {
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
	db, err := s.State.shelf.open(sc.tenant)
	if err != nil {
		return scope{}, nil, err
	}
	// The shipped model AND every promoted version's state, in one place. cloud
	// deploys Recreate at one replica, so this is the whole answer to a rollout:
	// whatever this tenant was running comes back, or it comes back warming and
	// declines to score.
	//
	// EVERY REQUEST, not only the first. A once-per-process restore is correct
	// until something drops the state, and something does — the engine evicts the
	// least recently used tenant when a store fills, and a tenant restored once
	// then never reloads. It costs two map reads when there is nothing to do. See
	// lifecycle.go's hydrate.
	hydrate(s, sc.tenant, db)
	return sc, db, nil
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

		report := map[string]any{
			"status":    "ok",
			"model":     s.State.digest,
			"tenants":   len(s.State.shelf.tenants()),
			"warehouse": warehouse || datastore.Ready(),
			"billing":   s.State.bill.Enabled(),
		}
		// The DECISION plane is what this app promises, and it does not need the
		// warehouse: rings are in memory, rules and lists are the tenant's own
		// file, the model is in process. The probe is degraded only when
		// something the hot path actually needs is missing.
		if s.State.stable == nil || s.State.vel == nil || s.State.dataDir == "" {
			report["status"] = "degraded"
			report["error"] = "the decision plane is not constructed"
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
