// Package risk is HANZO RISK's model plane: the per-organisation feature surface
// and the per-organisation models trained on it.
//
// # What this app owns
//
// /v1/risk — score, learn, state, features, search. ONE face for deciding and
// learning, because they are one act: the model IS the decision, and a score is
// only meaningful against what that organisation's model has learned.
//
// It does NOT own /v1/ml. That prefix belongs to the model-SERVING plane
// (apps/ml): InferenceServices, /v1/ml/models, /v1/ml/models/{name}/predict.
// Serving a model somebody else trained and learning a model from an
// organisation's own behaviour are two different products, and putting them
// under one name would make /v1/ml/models mean two things at once.
//
// It is ONE app because the model is IN-PROCESS MUTABLE STATE. If one binary
// learned and another scored, the two would hold different mass counters and
// answer one question two ways — with no error, no log and nothing to alert on.
// So the owner of the state is the owner of every leaf that touches it.
//
// # The moat, stated plainly
//
// Every organisation's events already land in one columnar store through one
// door: product analytics, captured failures, and every priced inference. This
// app rolls that into a per-organisation feature surface and trains a model per
// organisation ON THAT ORGANISATION'S OWN DATA. Nobody who does not already
// operate the event surface can compute these features, and nobody who does not
// run the organisation's own inference can compute the spend ones.
//
// # The boundary, and why it is not merely a rule
//
// A feature read is unspellable without a tenant: [rows] takes a [tenant], which
// has no exported constructor and is minted in exactly one place from the
// validated principal. The tenant is the LEADING BOUND predicate of every
// statement. The key is `<brand>/<org>`, so two brands' identically named
// organisations are two tenants and not one. The model geometry is seeded from
// that key, so two organisations do not merely have different counters — they
// have different trees.
//
// Cross-organisation learning is AGGREGATE-ONLY and it is one table with no
// tenant column at all (baseline.go): three interpolated quantiles of one dimension over one
// day, published only when at least twenty-five organisations contributed. The
// leak is uncomputable rather than disallowed, and no model reads it — it is
// published for a human to compare against, never folded into a score.
//
// # What is inherited, and what had to be wired
//
// Nothing here is free. IAM auth is global (the identity middleware mints the org
// from a verified bearer); the tenant reaching a TYPED op is [cloud.Bridge],
// installed FIRST on each group; the gate and the meter are wired per op that
// costs compute; the scoped logger comes from [cloud.NewBase]; traces are
// ZAP-native already; health is a REAL probe this app serves itself.
package risk

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// state is this app's own data. The shared dependencies live in the embedded
// cloud.Base.
type state struct {
	// plane is the model plane: every resident model, the shared aggregate rings,
	// and the per-org shelves the learned state is durable on.
	plane *plane
	// gap is why the plane could not be built, when it could not. The app mounts
	// anyway and every route fails CLOSED with the real reason — a subsystem that
	// refuses to mount takes the whole binary's health surface with it.
	gap string
}

// Mount wires the model plane onto app.
//
// It is a direct construction rather than cloud.Mount because the plane owns
// background work and durable state: it must be reachable from the plugin's
// Shutdown so a rollout snapshots every resident model instead of silently
// returning every tenant to warming.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("risk.Mount: nil app")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "risk")}
	p, err := newPlane(s.Base)
	if err != nil {
		// Fail CLOSED and stay up: every route answers 503 with this reason, which
		// is a fact an operator can act on. A model plane that cannot be built and
		// pretends otherwise is the failure this whole app exists to avoid.
		s.State.gap = err.Error()
		s.Log.Error("model plane unavailable; every /v1/risk op will fail closed", "err", err)
	} else {
		s.State.plane = p
	}
	// THE LISTING IS RESOLVED AT MOUNT, so a misconfiguration is announced before a
	// decision is taken rather than discovered by the first payment that needed it.
	// It is otherwise resolved lazily, on whichever request first reaches the
	// geography half — which is exactly when nobody is reading.
	if gap := jurisdictions().Gap; gap != "" {
		s.Log.Error("the stated jurisdiction listing cannot assess any country; the compiled "+
			"default is in force and the freeze it arms is NOT the one you stated", "gap", gap)
	}
	mount(s, app)
	mounted = s
	// The internal scorer, published AFTER the state is built and the surface is
	// registered, so a peer that can reach the socket can reach a working model.
	// It is what arms every gate in every OTHER process — see risk_rpc.go.
	exposeDecide()
	// And the other half of the same seam. A plane that can be ASKED about a payment
	// and cannot be TOLD one settled leaves the pace and fan-out rules reading an
	// empty history for every self-serve organisation — see [planeObserve].
	exposeObserve()
	s.Log.Info("risk model plane mounted", "brand", deps.Brand, "env", deps.Env, "plane", s.State.plane != nil)
	return nil
}

// mount registers the surface. It is separate from Mount because Mount BUILDS the
// state and the routes do not care where the state came from — which is what lets
// a test pin a plane (or a deliberately absent one) and exercise the WIRE.
func mount(s *cloud.Service[state], app cloud.Router) {
	// ONE `g := <router>.Group("/prefix")` per line: cmd/zipdoc resolves a group's
	// prefix by reading that exact assignment form and FAILS the generate rather
	// than filing prose under a path that does not exist.
	//
	// ONE GROUP, AND IT IS /v1/risk. This surface used to mount its leaves under
	// /v1/ml, which is a prefix a DIFFERENT live product already owns — the
	// Kubernetes model-SERVING plane at /v1/ml/models and /v1/ml/models/{name}/
	// predict (apps/ml), with customers on it. Two concepts under one name is the
	// one thing the naming rule exists to prevent: /v1/ml/models would have meant
	// "models you serve" and "models that learn" at once. Deciding and learning are
	// the same act here, so they are one face.
	g := app.Group("/v1/risk")
	// cloud.Bridge is not installed here. Whoever composes the program installs it
	// once at the root — after the identity check that mints the validated org and
	// before any subsystem registers a route (serve.go) — because that order is a
	// property of the whole program and no subsystem can assert it for itself.
	//
	// cloud.DenyEnvelope BEFORE the leaves, because fiber runs middleware in
	// registration order and one installed after its leaves never runs: every op
	// that costs compute gates on the caller's balance, and the envelope is what
	// makes the refusal the fleet's own nested {"error":{"code","message"}} 402
	// rather than a second vocabulary for a refusal the platform already has words
	// for.
	g.Use(cloud.DenyEnvelope())
	o := ops{s: s}

	zip.Post(g, "/score", o.score,
		zip.WithOperationID("riskScore"),
		zip.WithSummary("Score one event against your organisation's own model"),
		zip.WithTags("risk"))
	zip.Post(g, "/learn", o.learn,
		zip.WithOperationID("riskLearn"),
		zip.WithSummary("Teach your organisation's own model from its own events"),
		zip.WithTags("risk"))
	zip.Get(g, "/state", o.state,
		zip.WithOperationID("riskState"),
		zip.WithSummary("Report your organisation's model: what it learned, and what it realised"),
		zip.WithTags("risk"))
	// ONE ADDRESS FOR A MODEL VALUE, minted and put in force. POST mints a value from
	// the model in force; PUT puts a named value in force. They were
	// /v1/risk/state/snapshot and /v1/risk/state/restore — two addresses named after
	// the OPERATION rather than after the thing it operates on, which left a reader
	// asking what a snapshot is if it is not a value. Same collapse, same reason, as
	// GET and PUT on /v1/risk/policy.
	zip.Post(g, "/state/model", o.publish,
		zip.WithOperationID("riskPublishModel"),
		zip.WithSummary("Publish your organisation's model as a named, immutable value"),
		zip.WithStatus(http.StatusCreated),
		zip.WithTags("risk"))
	zip.Put(g, "/state/model", o.adopt,
		zip.WithOperationID("riskAdoptModel"),
		zip.WithSummary("Put one of your organisation's own published model values in force"),
		zip.WithTags("risk"))
	// ONE ADDRESS FOR THE DECISION REGIME, read and written. The write used to be
	// PUT /v1/risk/state/appetite — a second address for the same plane, named after
	// the mutable spot the regime happened to sit in, answering a fifteen-field
	// report of the whole model to a call that changes three numbers. Both verbs
	// answer riskPolicyOut now, so a caller has one shape to understand and the
	// write's answer is exactly what the read would say next.
	zip.Get(g, "/policy", o.policy,
		zip.WithOperationID("riskPolicy"),
		zip.WithSummary("Your organisation's decision-regime history, and which version is in force"),
		zip.WithTags("risk"))
	zip.Put(g, "/policy", o.appetite,
		zip.WithOperationID("riskSetPolicy"),
		zip.WithSummary("State the decision regime: the appetite, the sample, and whether the model is live"),
		zip.WithTags("risk"))
	zip.Get(g, "/features", o.features,
		zip.WithOperationID("riskFeatures"),
		zip.WithSummary("The feature catalogue: what the model reads, and what your surface carries"),
		zip.WithTags("risk"))
	zip.Post(g, "/search", o.search,
		zip.WithOperationID("riskSearch"),
		zip.WithSummary("Search exhaustively for the model shape that fits your own history"),
		zip.WithStatus(http.StatusAccepted),
		zip.WithTags("risk"))
	zip.Get(g, "/search/:id", o.result,
		zip.WithOperationID("riskSearchResult"),
		zip.WithSummary("Read back one exhaustive search"),
		zip.WithTags("risk"))

	// UNTYPED BY DESIGN, and the reason is THIS PACKAGE'S OWN INVARIANT rather than
	// anything about zip.
	//
	// The reason it used to give was the multi-status gap: a real probe answers 503
	// CARRYING the degraded report, and a typed op reached a non-2xx only by
	// returning an error whose envelope dropped exactly that detail. THAT HAS
	// EXPIRED — WithStatus is variadic and an answer states which declared status it
	// is (StatusCoder), which is how apps/deploy's probe became an op. The
	// conversion was written here, and this package's own gate refused it:
	// TestOps_EveryOpIsAdmittedAndPriced requires every op to pass through o.admit
	// (a per-tenant in-flight slot) and to be priced, because an op reaches a
	// per-tenant model, a per-tenant disk and a shared warehouse.
	//
	// A LIVENESS PROBE MUST DO NEITHER. It has to answer without a tenant — that is
	// what liveness means — so it can hold no tenant slot, and metering an
	// orchestrator's probe would bill a customer for being watched. Making it an op
	// would mean exempting it from that gate, which weakens a bound that exists to
	// stop unbounded per-tenant compute, for one route that gains a tool nobody
	// needs: an agent does not probe liveness, an orchestrator does, over HTTP.
	//
	// So the refusal stands on a better footing than before. Held to the closed list
	// in typed_wire_test.go.
	g.Get("/health", cloud.Handle(s, health))
}

// health is a REAL probe: it reports whether the model plane exists, whether the
// per-org shelves can be written, and whether the event surface behind the
// feature plane is reachable. 200 only when the model plane can actually work.
//
// The warehouse being DOWN is reported and is NOT a failure: scoring reads
// in-memory rings and never the warehouse, so a warm that cannot run degrades the
// moat and does not stop a decision. Saying so is the difference between a probe
// and status theatre.
func health(s *cloud.Service[state], c *zip.Ctx) error {
	res := map[string]any{"service": "risk", "status": "ok"}
	if s.State.plane == nil {
		res["status"], res["plane"], res["error"] = "degraded", false, s.State.gap
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	res["plane"] = true
	if err := dataDirWritable(s.DataDir); err != nil {
		res["status"], res["shelf"], res["error"] = "degraded", false, err.Error()
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	res["shelf"] = true
	res["surface"] = storeReady()
	// COUNTS, and none of them names a tenant, so the probe stays a fact about the
	// process.
	//
	// `evicted` is here because the resident bound is one whose pressure a tenant
	// cannot see for itself: eviction is lossless, so the only sign it is happening
	// at all is this number climbing. `strained` is here because the OTHER bound —
	// a tenant's own aggregates — degrades that tenant silently by construction:
	// at it, its least-recently-active subject is forgotten and reads as inactive.
	// A control that switches itself off must be visible from outside.
	held, built, evicted, strained := s.State.plane.residents()
	res["resident"], res["built"], res["evicted"], res["strained"] = held, built, evicted, strained
	// THE LISTING, DATED. The geography half of the rule can only decide against a
	// listing whose currency can be assessed, and a listing that has quietly gone
	// stale — or one an operator stated that cannot decide at all — degrades the
	// rule with nothing anywhere saying so. A date and an age are what let that be
	// read from outside, which is the same argument `evicted` and `strained` above
	// are here for.
	//
	// It is NOT a degradation. A stale listing still decides, the default is always
	// dated, and a probe that failed on a listing's age would take the model plane
	// down over a reference table. Reported, never fatal.
	j := jurisdictions()
	res["listed"] = j.AsOf.UTC().Format(time.RFC3339)
	res["listed_days"] = int(j.Age(time.Now().UTC()).Hours() / 24)
	res["listing"] = "default"
	if j.Operator {
		res["listing"] = "operator"
	}
	if j.Gap != "" {
		res["listing_gap"] = j.Gap
	}
	return c.JSON(http.StatusOK, res)
}

// Shutdown snapshots every resident model and closes the shelves.
//
// This is not housekeeping. The binary deploys one replica at a time with the old
// pod stopped before the new one starts, so without this every rollout drops
// every warming model and the threshold it had computed — and a warming model
// refuses to score, which reads as "clean" to anything that does not check the
// refusal. A control that is off for the length of a warm period is a control
// that was off.
// The context is the shutdown WINDOW and it is honoured, not decorative: the
// composition root builds it with the deployment's own budget in it, so the plane
// has no business inventing a bound of its own — or, as it did, waiting with none.
func Shutdown(ctx context.Context) error {
	s := mounted
	mounted = nil
	if s == nil || s.State.plane == nil {
		return nil
	}
	return s.State.plane.close(ctx)
}

// mounted is the service this binary mounted, so the two entry points that are
// not Mount can reach what Mount built: the composition root's Shutdown, which
// snapshots every resident model, and the internal scorer (risk_rpc.go), which
// needs the plane AND the brand its tenant key is qualified by. Mount, Shutdown
// and the plane op are wired separately by the plugin, so the value they share is
// held here rather than smuggled through a closure — one binary, one mount, one
// service. Same shape as apps/dataroom.
//
// It is the SERVICE and not the plane because two globals for one mount is two
// facts that can disagree about whether this process holds a model.
var mounted *cloud.Service[state]

// residents is how many tenants' models are held right now, how many residencies
// have been BUILT, how many have been evicted to hold that bound, and how many of
// those held are at their OWN aggregate bound and therefore forgetting their own
// subjects. All four name a COUNT and never a tenant, so the probe reveals
// nothing about who is using the plane.
func (p *plane) residents() (held int, built, evicted int64, strained int) {
	p.mu.Lock()
	all := make([]*resident, 0, len(p.res))
	for _, r := range p.res {
		all = append(all, r)
	}
	built, evicted = p.built, p.evicted
	p.mu.Unlock()
	for _, r := range all {
		if r.vel.strain().Saturated {
			strained++
		}
	}
	return len(all), built, evicted, strained
}

// The prose for the one route that is untyped by design. zipdoc lifts prose from
// a typed handler's doc comment and this is not one, so without a Describe it
// would publish an operationId and nothing else.
func init() {
	openapi.Describe("/v1/risk/health", http.MethodGet,
		"Whether the risk model plane can actually work right now",
		"Reports whether the per-organisation model plane is genuinely usable: that the plane "+
			"was built, that the per-organisation stores can be written, and whether the event "+
			"surface the feature plane is rolled up from is reachable. It is a REAL probe, not "+
			"status theatre.\n\n"+
			"200 only when the plane can work. Otherwise 503 CARRYING THE REPORT — which part "+
			"failed and the real error — and that body is why this is not a typed op: a typed op "+
			"reaches a non-2xx by returning an error, and the envelope that produces would drop "+
			"exactly the detail the probe exists to deliver.\n\n"+
			"An unreachable event surface is REPORTED and is not a failure. Scoring reads "+
			"in-memory aggregates and never the warehouse, so a warm that cannot run degrades how "+
			"much history a model has seen and does not stop it deciding.\n\n"+
			"It also reports how many organisations' models are resident, how many have been "+
			"evicted to hold that bound, and how many of the resident ones are at their own "+
			"aggregate bound. Eviction is lossless — learned state is written to that "+
			"organisation's own store first and its aggregates rebuild from its own record — so a "+
			"climbing count is a capacity signal, not a loss. A STRAINED model is different: it "+
			"has started forgetting its own least-recently-active subjects, and each forgotten "+
			"subject reads as inactive until it is active again. That is a control degrading, and "+
			"it is reported here because it is otherwise silent.\n\n"+
			"It answers about the process, not about a tenant: it takes no organisation and names "+
			"none.")
}
