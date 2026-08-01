// Package risk is HANZO RISK's model plane: the per-organisation feature surface
// and the per-organisation models trained on it.
//
// # What this app owns
//
// The NATIVE leaves of /v1/ml — score, learn, state, features, search. Model
// serving and Kubeflow training keep their own app at /v1/ml/models and
// /v1/train/*; longest-prefix match separates the two and no route moves.
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
// tenant column at all (baseline.go): four quantiles of one dimension over one
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
	if deps.Logger == nil {
		return fmt.Errorf("risk.Mount: nil deps.Logger")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "risk")}
	p, err := newPlane(s.Base)
	if err != nil {
		// Fail CLOSED and stay up: every route answers 503 with this reason, which
		// is a fact an operator can act on. A model plane that cannot be built and
		// pretends otherwise is the failure this whole app exists to avoid.
		s.State.gap = err.Error()
		s.Log.Error("model plane unavailable; /v1/ml native leaves will fail closed", "err", err)
	} else {
		s.State.plane = p
	}
	mount(s, app)
	mounted = s.State.plane
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
	gml := app.Group("/v1/ml")
	grisk := app.Group("/v1/risk")
	// cloud.Bridge FIRST on each group. A typed op receives only a context, so the
	// validated identity it reads has to be parked there; fiber runs middleware in
	// registration order, so one installed after its leaves never runs.
	gml.Use(cloud.Bridge())
	grisk.Use(cloud.Bridge())
	o := ops{s: s}

	zip.Post(gml, "/score", o.score,
		zip.WithOperationID("mlScore"),
		zip.WithSummary("Score one event against your organisation's own model"),
		zip.WithTags("ml"))
	zip.Post(gml, "/learn", o.learn,
		zip.WithOperationID("mlLearn"),
		zip.WithSummary("Teach your organisation's own model from its own events"),
		zip.WithTags("ml"))
	zip.Get(gml, "/state", o.state,
		zip.WithOperationID("mlState"),
		zip.WithSummary("Report your organisation's model: what it learned, and what it realised"),
		zip.WithTags("ml"))
	zip.Put(gml, "/state/appetite", o.appetite,
		zip.WithOperationID("mlSetAppetite"),
		zip.WithSummary("Restate the risk appetite, and whether the model is live"),
		zip.WithTags("ml"))
	zip.Post(gml, "/state/snapshot", o.snapshot,
		zip.WithOperationID("mlSnapshot"),
		zip.WithSummary("Pin your organisation's learned state so a decision can be reproduced"),
		zip.WithStatus(http.StatusCreated),
		zip.WithTags("ml"))
	zip.Post(gml, "/state/restore", o.restore,
		zip.WithOperationID("mlRestore"),
		zip.WithSummary("Install previously pinned state into your organisation's model"),
		zip.WithTags("ml"))
	zip.Get(gml, "/features", o.features,
		zip.WithOperationID("mlFeatures"),
		zip.WithSummary("The feature catalogue: what the model reads, and what your surface carries"),
		zip.WithTags("ml"))
	zip.Post(gml, "/search", o.search,
		zip.WithOperationID("mlSearch"),
		zip.WithSummary("Search exhaustively for the model shape that fits your own history"),
		zip.WithStatus(http.StatusAccepted),
		zip.WithTags("ml"))
	zip.Get(gml, "/search/:id", o.result,
		zip.WithOperationID("mlSearchResult"),
		zip.WithSummary("Read back one exhaustive search"),
		zip.WithTags("ml"))

	// UNTYPED BY DESIGN — a REAL probe answers 503 CARRYING THE DEGRADED REPORT as
	// its body, which is the whole point of a probe. A typed op reaches a non-2xx
	// only by returning an error, and zip renders that as its own envelope,
	// dropping exactly the detail the probe exists to deliver. Held to the closed
	// list in typed_wire_test.go.
	grisk.Get("/health", cloud.Handle(s, health))
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
	res["resident"] = s.State.plane.residents()
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
func Shutdown(context.Context) error {
	if mounted == nil {
		return nil
	}
	err := mounted.close()
	mounted = nil
	return err
}

// mounted is the plane this binary mounted, so the composition root's Shutdown
// can reach it. Mount and Shutdown are two independent functions the plugin wires
// separately, so the value they share is held here rather than smuggled through a
// closure — one binary, one mount, one plane. Same shape as apps/dataroom.
var mounted *plane

// residents is how many tenants' models are held right now. It names a count and
// never a tenant, so the probe reveals nothing about who is using the plane.
func (p *plane) residents() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.res)
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
			"It answers about the process, not about a tenant: it takes no organisation and names "+
			"none.")
}
