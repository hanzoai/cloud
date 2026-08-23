package risk

// family.go — A MODEL FAMILY IS A VALUE, AND THE GEOMETRY IS THE FAMILY'S OWN.
//
// # The place that was here before
//
// This plane ran ONE kind of model and said so in a comment: "WHERE THE TRAINING
// HAPPENS: in this process, on half-space trees". Everything downstream of that
// sentence was true of half-space trees and of nothing else. A shape was four
// numbers only that family has; three separate functions rebuilt an
// [anomaly.Config] by hand to install one; the search grid enumerated tree counts;
// and the residency held the whole braid — geometry, appetite, seed and a tenant
// bound — in a single Config field that three different owners wrote into. A second
// family could not be ADDED to that. It could only be forked.
//
// So the family becomes a value, and every mass, verdict and content address is
// meaningful against a family-and-geometry rather than against a geometry that had
// only one family it could belong to.
//
// # The family is a FUNCTION of the geometry, never a field beside it
//
// The obvious shape for this is a family NAME next to a bag of parameters, and it is
// the wrong one: `{"family":"transformer","trees":40}` is then a value that has to be
// REJECTED, at runtime, in every place that reads one, forever — and the day a reader
// forgets, a transformer runs on tree counts. Here the family is derived from the
// geometry's own TYPE ([geometry.family]), so there is no value of [shape] whose
// family and parameters disagree. Half-space parameters attached to another family
// are not refused, they are UNSPELLABLE, and this package therefore contains no
// check for it — the check is the type.
//
// The set of families is CLOSED for the same reason: [geometry]'s methods are
// unexported, so the implementations in this package are the families this plane can
// run, and a family cannot be introduced from outside it.
//
// # The family is in the content address, and that is what makes it load-bearing
//
// A published value's name already covers everything that makes two models answer
// the same event differently (address.go). The family is the first of those terms:
// two families' masses are not merely fitted differently, they are different KINDS
// of number. So [address] is domain-separated by family, and a value fitted under
// one family can never wear a name minted under another.
//
// The refusal is then free at the entry point as well. [plane.install] already decides
// between restoring in place and REPLANTING by asking whether the value's space is
// the one already running; with the family inside the space, a cross-family adoption
// takes the replant branch and is refused BEFORE a single mass is read — by the
// family gate stated there, and again by the engine's own digest check if it ever
// got past it. [TestFamily_AValueIsNotAdoptableIntoAnotherFamily] is that claim,
// made falsifiable.
//
// # What a family owes, and what it does NOT
//
// [detector] is the whole of it, and it was DISCOVERED from what this plane already
// asks of a model rather than declared in advance. Six methods, no tenant parameter
// on any of them: a detector IS one organisation's model, so there is no way to ask
// one about another organisation. The isolation this app is built on stops being a
// predicate every call site has to remember and becomes a fact about the type.
//
// A family owes nothing else. It does not own its durability (the shelf does), its
// policy (policy.go does), its aggregates (ring.go does), its address (address.go
// does) or its retention. Those are this plane's, and they are the same for every
// family — which is the point of the client being here and not around the whole plane.

import (
	"encoding/json"
	"fmt"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"
	"github.com/zap-proto/zip"
)

// family names a KIND of model: its own geometry, its own arithmetic, and its own
// idea of what a mass is.
//
// It is a value and not a build flag, because it travels: it is in the name of every
// published model value, in the space every verdict cites, and in the refusal that
// keeps one family's masses out of another's model.
type family string

// halfSpace is the family this plane was born running: an ensemble of half-space
// trees over the feature inventory, whose masses are counters and whose learning is
// therefore an increment with no training pass, no retained sample and no job. See
// learn.go's header for why that is the cheap half of the moat.
const halfSpace family = "halfspace"

// qualify names a model space: this family, and the family's own digest over its
// geometry and the inventory it reads.
//
// Two families' digests are two families' arithmetic, so an unqualified digest could
// only be compared WITHIN a family — and every comparison in this plane would have
// had to carry the family beside it and remember to check it. Qualified, the family
// travels inside the one string that already had to be equal, and "same space" means
// same family and same geometry for free.
//
// Same reasoning, same shape, as [qualify] over a brand and an organisation: a name
// that is only unique inside a namespace carries the namespace.
func (f family) qualify(digest string) string { return string(f) + ":" + digest }

// geometry is ONE family's own parameters, and the closed set of types implementing
// it IS the set of families this plane can run.
//
// The methods are the two things a geometry is FOR: saying which family it belongs to
// and building the detector it describes. It deliberately owes nothing about masses,
// durability or policy — a geometry is a description of a space, and everything else
// in this package is about what happens in one.
type geometry interface {
	// family is which family these parameters belong to. It is derived and never
	// stored, which is the whole reason a shape cannot state a family its parameters
	// do not have.
	family() family
	// build makes the detector these parameters describe: one organisation's model,
	// deciding under pol, over that organisation's own aggregates, in the partition
	// seed names.
	//
	// The four arguments are four facts with four owners and are deliberately not one
	// config: the geometry is the state's, the regime is the policy record's
	// (policy.go), the seed is the learned state's, and the aggregates are the
	// organisation's own (ring.go). Braided into one struct they were written by three
	// different callers, each of which had to remember not to disturb the other two's
	// fields.
	build(t tenant, pol regime, seed uint64, vel *rings) (detector, error)
}

// halfspace is [halfSpace]'s geometry: the four parameters the engine's own digest
// covers, which is exactly the set that decides whether two of this family's models
// mean anything against each other.
//
// It is COMPLETE by construction — see [halfspace.complete]. A parameter left at zero
// used to mean "the deployment's own", resolved by the engine after construction and
// read back out of the built store, which is why the residency had to record a config
// it did not choose. Resolved at the entry point instead, there is one shape, it is
// the one that runs, and nothing has to be read back to find out what it was.
type halfspace struct {
	// Trees is how many half-space trees partition the space.
	Trees int `json:"trees"`
	// Depth is how deep each one cuts, so one tree holds 2^(Depth+1)-1 regions.
	Depth int `json:"depth"`
	// Window is how many events one reference window holds before it folds.
	Window int `json:"window"`
	// Blend is how much of the open window folds into the reference on a fold.
	Blend float64 `json:"blend"`
}

func (halfspace) family() family { return halfSpace }

// The deployment's own half-space geometry, stated here rather than inherited.
//
// These are the engine's own defaults and they are RESTATED on purpose: a shape that
// arrives at zero used to be completed by the engine at construction, so the shape a
// residency ran was decided by a library and discovered afterwards. Stated, the
// deployment owns its shape, [defaultShape] is a value a reader can see, and a change
// in the engine's defaults is a change this plane makes deliberately instead of one it
// inherits at the next dependency bump.
//
// 25 trees of 2^9-1 regions is the 466 KiB measured in address.go's header.
const (
	defaultTrees  = 25
	defaultDepth  = 8
	defaultWindow = 256
	defaultBlend  = 0.25
)

// complete resolves every parameter left at zero to the deployment's own, so a built
// geometry never holds a zero standing in for a number somebody else will choose.
//
// It is EXACTLY the engine's own rule, applied at the entry point instead of after
// construction: the engine fills a zero with the same value, so this changes no shape
// and no digest — it only moves WHEN the shape is known from "after the store was
// built" to "before".
func (g halfspace) complete() halfspace {
	if g.Trees <= 0 {
		g.Trees = defaultTrees
	}
	if g.Depth <= 0 {
		g.Depth = defaultDepth
	}
	if g.Window <= 0 {
		g.Window = defaultWindow
	}
	if g.Blend <= 0 || g.Blend > 1 {
		g.Blend = defaultBlend
	}
	return g
}

// build plants this family's detector: one anomaly store holding exactly one
// organisation's model.
//
// MaxOrgs is 1 and that is the isolation argument rather than a tuning choice — the
// store IS the tenant boundary here, so a store that could hold two organisations
// would be a place two organisations' masses could meet. [detector] has no tenant
// parameter for the same reason, one level up.
func (g halfspace) build(t tenant, pol regime, seed uint64, vel *rings) (detector, error) {
	g = g.complete()
	cfg := pol.applyTo(anomaly.Config{
		Trees: g.Trees, Depth: g.Depth, Window: g.Window, Blend: g.Blend,
		Seed:    seed,
		MaxOrgs: 1,
	})
	mod, err := anomaly.New(cfg, vel.vel)
	if err != nil {
		return nil, err
	}
	return &forest{key: t, mod: mod}, nil
}

// config is the BRAIDED form, and this is the only place it is still spelled: the
// resume row (`model.config`) has carried an [anomaly.Config] since before the
// geometry and the regime had separate owners, and reading a row written then is what
// keeps a rollout from returning every organisation to the default shape.
//
// It carries no seed: the engine withholds it from that Config's JSON, and it is
// carried by the snapshot beside it — where it belongs, because the partition is
// learned state and not policy.
func (g halfspace) config(pol regime) anomaly.Config {
	g = g.complete()
	return pol.applyTo(anomaly.Config{Trees: g.Trees, Depth: g.Depth, Window: g.Window, Blend: g.Blend})
}

// legacy is the resume row's own form of a shape and the regime beside it: the
// [anomaly.Config] that column has carried since before the geometry and the regime
// had separate owners.
//
// It is the ONE place the braid is still spelled, and it is spelled at the DISK
// boundary rather than in memory — which is what makes reading a row written before
// the family was a value a decode rather than a migration.
//
// A family whose geometry that column cannot carry is REFUSED rather than written as
// something it is not: a resume row that describes another family's model is how a
// rollout comes back running the wrong one, silently, which is the failure this whole
// app exists to avoid. That refusal is the resume row's own follow-on — a column that
// holds a family-tagged shape — and it is a REFUSAL and not a TODO, so the day a
// second family lands the write fails loudly instead of lying.
func legacy(g shape, pol regime) (anomaly.Config, error) {
	switch h := g.geometry.(type) {
	case halfspace:
		return h.config(pol), nil
	case nil:
		return anomaly.Config{}, fmt.Errorf("risk: a resume row must record the space its masses describe")
	default:
		return anomaly.Config{}, fmt.Errorf("risk: the resume row cannot carry a %q geometry", h.family())
	}
}

// ── the shape: a family and that family's own geometry, as one value ─────────

// shape is the model SPACE as a value: one family's geometry, and therefore the
// family.
//
// ONE SPELLING, FOUR USERS: the residency's own record of the space it runs
// ([resident.geom]), a published value's record of the space its masses describe
// ([model]), the search's candidate ([topology]), and the resume row.
//
// It is a wrapper around one interface field rather than the interface itself because
// a shape has to survive a round trip through JSON — it is stored inside every
// published value — and an interface cannot decode itself. The tag is written and read
// HERE, in one place, so no reader of a stored value has to know how a family is
// spelled on disk.
type shape struct {
	geometry
}

// stated is whether this shape names a space at all. A zero shape records no space,
// and that is a state to REFUSE a replant onto rather than to default: "the
// deployment's own shape" is a guess, and these are mass counters.
func (s shape) stated() bool { return s.geometry != nil }

// family is TOTAL where the promoted one is not: a zero shape has no geometry, so
// asking the embedded interface would panic, and the one place a panic is least
// welcome is the path that installs a stored value. An unstated shape names no
// family and says so.
func (s shape) family() family {
	if !s.stated() {
		return ""
	}
	return s.geometry.family()
}

// same is whether two shapes name the SAME space: the same family, and that family's
// same parameters.
//
// It is a method and not `==` because a shape holds an interface, and `==` on an
// interface holding an uncomparable value panics at run time. Every geometry in this
// package is comparable ([TestFamily_EveryGeometryIsComparable] holds that closed),
// so this is that comparison made total instead of trusted.
func (s shape) same(o shape) bool {
	if !s.stated() || !o.stated() {
		return false
	}
	if s.family() != o.family() {
		return false
	}
	return s.geometry == o.geometry
}

// defaultShape is the space a residency is planted in before it knows better: this
// deployment's own half-space geometry.
func defaultShape() shape { return shape{halfspace{}.complete()} }

// shapeOf reads a shape off a resume row's config.
//
// A config that states NO geometry answers an unstated shape rather than the
// deployment's own, and that distinction is the one this function exists to keep: a
// row written before this plane recorded shapes describes masses whose space nobody
// recorded, and [plane.install] refuses to replant onto a guess.
func shapeOf(c anomaly.Config) shape {
	if c.Trees <= 0 && c.Depth <= 0 && c.Window <= 0 && c.Blend <= 0 {
		return shape{}
	}
	return shape{halfspace{Trees: c.Trees, Depth: c.Depth, Window: c.Window, Blend: c.Blend}.complete()}
}

// wire is the shape a shape takes on disk and on the wire: the family, then that
// family's own parameters, flat.
//
// FLAT AND TAGGED, so a value published before the family was one decodes as
// half-space without a migration: the parameters are where they always were, and the
// only family that ever wrote a shape without naming one is the family those
// parameters belong to.
type wire struct {
	Family family `json:"family,omitempty"`
	// The families' own parameters, inline. A family adds its own fields here and
	// [shape.UnmarshalJSON] gains one case; nothing else in this package moves.
	halfspace
}

// MarshalJSON writes the family beside its parameters.
func (s shape) MarshalJSON() ([]byte, error) {
	if !s.stated() {
		return []byte(`{}`), nil
	}
	switch g := s.geometry.(type) {
	case halfspace:
		return json.Marshal(wire{Family: halfSpace, halfspace: g})
	default:
		// Unreachable while [geometry] is closed, and stated rather than silently
		// encoded as an empty shape: a value whose space could not be written down is a
		// value nobody could adopt, and finding that out at adoption would be finding
		// it out too late.
		return nil, fmt.Errorf("risk: no wire form for the %q model family", g.family())
	}
}

// UnmarshalJSON reads the family FIRST and lets it decide what the parameters mean.
//
// An UNKNOWN family is refused rather than defaulted. A stored value naming a family
// this binary does not run is a value from a plane that does run it — a rollback, or a
// replica mid-rollout — and reading its parameters as half-space would restore another
// family's numbers into these trees, which is exactly the corruption the digest gate
// exists to catch one step later. Refusing here says so with the family's own name.
func (s *shape) UnmarshalJSON(b []byte) error {
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	switch w.Family {
	case halfSpace:
		*s = shape{w.halfspace.complete()}
	case "":
		// No family recorded. Either the whole shape is absent — which is honest, and
		// install refuses to replant onto it — or it predates the family being a value,
		// in which case the parameters name their own family.
		if w.halfspace == (halfspace{}) {
			*s = shape{}
			return nil
		}
		*s = shape{w.halfspace.complete()}
	default:
		return fmt.Errorf("risk: this model value was fitted in the %q family, which this plane does not run", w.Family)
	}
	return nil
}

// ── the detector: what this plane asks of a model, and nothing more ──────────

// detector is one organisation's fitted model, whatever family it belongs to.
//
// # It was discovered, not declared
//
// Every method here is a call this plane already makes. The half-space family reached
// it through *[anomaly.Store]; the set below is what those call sites actually use,
// with the two that were only ever used to work around a construction detail removed:
//
//	observe   learning, at learn.go's batch loop and the search's replay. The engine's
//	          Assess answers a rule hit and whether it fired; BOTH returns are
//	          discarded at all three call sites, because learning is a transformation
//	          and a verdict is a query. So this answers nothing.
//	score     the one entry point to a verdict ([plane.score]). Pure: it moves no
//	          counter and touches no aggregate.
//	digest    the identity of the space, over this family's geometry and the inventory
//	          it reads. Qualified by the family ([family.qualify]) wherever it is
//	          recorded or compared.
//	snapshot  the learned state as a value, or false when nothing has been learned.
//	restore   that value, put back. It is the family's own proof that the masses
//	          describe this space: the half-space family checks the digest, the tree
//	          count, the region count and the mass invariant, and refuses.
//	state     the governance report — what it learned, the threshold in force, the
//	          appetite realised beside the one stated, every refusal by reason, and
//	          every coordinate that took its neutral value.
//
// NO METHOD TAKES A TENANT. A detector IS one organisation's model, so there is no
// way to ask one about another organisation — the isolation this app is built on is a
// fact about the type here rather than a predicate every call site has to remember.
// That deleted the org argument from seven call sites that all passed the same one.
//
// # What this client does NOT hide, stated so it is not discovered later
//
// [anomaly.Snapshot], [anomaly.Assessment] and [anomaly.State] are the plane's shared
// VOCABULARY — the value, the verdict and the report — and a second family speaks
// them. Two of the three are family-neutral. Snapshot is not: `Ref` and `Cur` are
// [][]float64, which is a tree count by a region count, so a family whose state is not
// a rectangle of masses either fits itself into that or the Snapshot widens. That is
// the one place a remote family presses on this client, and it presses on the address
// too, which hashes those arrays. Naming it here is cheaper than finding it in a
// migration.
type detector interface {
	// observe learns from one event and answers nothing.
	observe(tx types.Transaction)
	// score judges one event WITHOUT learning from it.
	score(tx types.Transaction) anomaly.Assessment
	// digest identifies the space this detector's arithmetic runs in, unqualified.
	digest() string
	// snapshot is the learned state as a value, and false when there is none.
	snapshot() (anomaly.Snapshot, bool)
	// restore puts one value back, refusing state that does not describe this space.
	restore(snap anomaly.Snapshot) error
	// state is the governance report, and its Digest is the QUALIFIED name of this
	// space ([family.qualify]) rather than the family's bare digest.
	//
	// That is the one place this client RESHAPES what a family hands back, and it is
	// deliberate. The report is surfaced to a caller who is told to compare its `shape`
	// with the `shape` on a published value and with the `shape` a verdict cites — and
	// those three were derived in three different places from three different sources.
	// They agreed only because one family's digest was all any of them could be.
	// Qualified here, once per family, there is one spelling of "which space" on the
	// whole wire.
	state() anomaly.State
}

// forest is [halfSpace]'s detector: an ensemble of half-space trees over ONE
// organisation's feature surface, whose nodes hold masses.
//
// It is a wrapper over *[anomaly.Store] and the wrapper is thin ON PURPOSE. The store
// holds one model per tenant and this one holds exactly one ([halfspace.build] sets
// MaxOrgs to 1), so every method the plane used to call with the organisation's own
// key is called here with the key the forest was built for — which is what removes
// the parameter from the interface without removing the isolation it carried.
type forest struct {
	key tenant
	mod *anomaly.Store
}

// The engine's Assess answers a rule hit and whether it fired; the plane discards
// both at every call site, so they are dropped here rather than carried to be
// dropped again. Learning is a transformation over observations; a verdict is a query
// against the result ([plane.score]).
func (f *forest) observe(tx types.Transaction) {
	f.mod.Assess(tx, types.Entity{OrgID: string(f.key)})
}

func (f *forest) score(tx types.Transaction) anomaly.Assessment {
	return f.mod.Inspect(tx, types.Entity{OrgID: string(f.key)})
}

func (f *forest) digest() string { return f.mod.Digest() }

func (f *forest) snapshot() (anomaly.Snapshot, bool) { return f.mod.Snapshot(string(f.key)) }

// restore is the family's own proof, and the refusals it produces are the engine's:
// the shape must match the running inventory, the trees and regions must be the ones
// this space has, and the masses must satisfy the invariant this algorithm cannot
// avoid producing. A caller-facing refusal is what those are turned into at
// [plane.install]; here they are returned as they are.
func (f *forest) restore(snap anomaly.Snapshot) error { return f.mod.Restore(snap) }

// The report's Digest leaves here QUALIFIED, per the [detector] contract: the engine
// answers its own digest and the plane's whole wire names a space by family and digest
// together.
func (f *forest) state() anomaly.State {
	st := f.mod.State(string(f.key))
	st.Digest = halfSpace.qualify(st.Digest)
	return st
}

// errFamily is the refusal when a value's family is not the one an organisation's
// model runs. It names BOTH, because "this does not fit" is indistinguishable from a
// fault and the two families are the whole of the answer.
func errFamily(value, running family) error {
	return zip.Errorf(400,
		"this model value was fitted in the %q family and this organisation's model runs the %q family, so its masses do not describe this model at all",
		value, running)
}
