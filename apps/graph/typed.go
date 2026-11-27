package graph

// typed.go is the wire. Every operation here is TYPED — one method, one path,
// one input type, one output type — so the served document, every generated
// client, the agent tool list and the command group are all projections of these
// declarations and none of them is written by hand.
//
// The org is NEVER an input. It is resolved from the validated principal in
// tenantOf, and `by` is stamped from the same principal.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/claim"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

type ops struct{ s *cloud.Service[*state] }

// zipdoc lifts the doc comment off each typed op — and off each field of its In
// and Out types — into zipdoc_gen.go, which hands them to zip.Describe at init.
// Go drops comments at compile time, so this build-time pass is the ONLY way the
// prose in this file reaches the published document, the MCP tool list and the
// generated clients. Without it every field below publishes as a bare type, and
// `knowable`, `contested` and the direction of an edge are exactly the things a
// bare type does not say.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

func routes(app cloud.Router, s *cloud.Service[*state]) {
	o := ops{s: s}

	g := app.Group("/v1/graph")
	// An answer spends the org's balance on model calls, and a payer out of
	// funds is refused in the money wire's own bytes (cloud.Denied). Everything
	// else passes through untouched.
	g.Use(cloud.DenyEnvelope())
	// The collection route is the group's own address, declared as the empty leaf.
	g.Post("", o.assert,
		zip.WithOperationID("graphAssert"),
		zip.WithTags("graph"))
	g.Get("", o.read,
		zip.WithOperationID("graphRead"),
		zip.WithTags("graph"))
	g.Post("/resolve", o.resolve,
		zip.WithOperationID("graphResolve"),
		zip.WithTags("graph"))
	g.Post("/neighbors", o.neighbors,
		zip.WithOperationID("graphNeighbors"),
		zip.WithTags("graph"))
	g.Post("/path", o.path,
		zip.WithOperationID("graphPath"),
		zip.WithTags("graph"))
	g.Post("/diff", o.diff,
		zip.WithOperationID("graphDiff"),
		zip.WithTags("graph"))
	g.Get("/search", o.search,
		zip.WithOperationID("graphSearch"),
		zip.WithTags("graph"))
	g.Get("/vocabulary", o.vocabulary,
		zip.WithOperationID("graphVocabulary"),
		zip.WithTags("graph"))
	g.Post("/erase", o.erase,
		zip.WithOperationID("graphErase"),
		zip.WithTags("graph"))

	// The two that take a DOCUMENT rather than an assertion (semantic.go). They
	// are under this app's own name because the address is the owner's name
	// (HIP-0139 §3) and the pipeline owns no store: what it finds is only ever
	// holdable here, and ingest ends by calling the assert declared above rather
	// than writing beside it.
	g.Post("/extract", o.extract,
		zip.WithOperationID("graphExtract"),
		zip.WithTags("graph"))
	g.Post("/ingest", o.ingest,
		zip.WithOperationID("graphIngest"),
		zip.WithTags("graph"))

	// The two that read the graph WHOLE (community.go, answer.go). Both derive
	// from the assertions on every call and store nothing, so a partition or an
	// answer can never be staler than the rows it came from.
	g.Post("/communities", o.communities,
		zip.WithOperationID("graphCommunities"),
		zip.WithTags("graph"))
	g.Post("/answer", o.answer,
		zip.WithOperationID("graphAnswer"),
		zip.WithTags("graph"))

	// The one that applies the organization's RULES (derive.go). It too derives
	// on every call, cites what each conclusion rests on, and stores nothing
	// unless an admin asks it to file what it concluded.
	g.Post("/derive", o.derive,
		zip.WithOperationID("graphDerive"),
		zip.WithTags("graph"))

	// Entity state, personal knowledge graph ontology, Apple IntelligencePlatform
	// ingestion and conversational state updates (entity.go, ontology.go,
	// apple.go, convo.go).
	g.Get("/ontology", o.ontology,
		zip.WithOperationID("graphOntology"),
		zip.WithTags("graph"))
	g.Post("/entity", o.entity,
		zip.WithOperationID("graphEntity"),
		zip.WithTags("graph"))
	g.Post("/apple", o.apple,
		zip.WithOperationID("graphApple"),
		zip.WithTags("graph"))
	g.Post("/convo", o.convo,
		zip.WithOperationID("graphConvo"),
		zip.WithTags("graph"))

	// The GraphQL endpoint, at the address apps/explorer already established for one
	// (/v1/<product>/graphql), on zip's engine. It is UNTYPED by construction rather
	// than by omission: a typed op declares one In and one Out, and this route's
	// input is a query whose OUTPUT SHAPE the caller chooses. Its bodies are
	// declared instead, beside the routes, in the same init the rest of this
	// package uses (graphql.go).
	mountGraphQL(g, o)
}

// ── assert ───────────────────────────────────────────────────────────────────

// graphAssertIn is a batch of assertions.
type graphAssertIn struct {
	// Assertions is the batch. Each member is judged on its own: one refusal
	// does not discard the rest, because a caller redelivering five facts must
	// not lose four of them to one malformed fifth.
	Assertions []graphFact `json:"assertions" validate:"required"`
}

// graphFact is one assertion, idempotent on its CONTENT: the same assertion
// delivered twice is one row, and anything differing in any field is a DIFFERENT
// assertion recorded beside the first. Nothing here ever overwrites anything,
// and the one delete is erasure (erase.go).
//
// A statement holds from At until the first of its own Until, a retraction of
// its pair — the same entity and relation with an empty value and names false —
// begun after it, or, for a property, a later value of the pair. An edge is not
// ended by another edge, so a pair holds as many edges as were asserted and not
// ended. A correction is the same statement filed again: the version known
// latest speaks for it.
type graphFact struct {
	// Entity is the thing being described, in the organization's own namespace.
	// It is not created: an entity exists because something was asserted about it.
	// It is a key, folded to Unicode NFC with control characters removed, so two
	// spellings of one name are one entity. An entity named `relation:<name>` is
	// that relation's declaration and one named `rule:<name>` is a rule
	// (graphDerive), and only an admin of the organization may assert anything
	// of either. Required, 512 bytes at most.
	Entity string `json:"entity" validate:"required"`
	// Relation is what is being asserted — `depends`, `owner`, `same`, `title`
	// — and a key, folded as Entity is. It is open until the organization
	// declares it: once `relation:<name>` has a `domain` or `range` in force,
	// the entity's `type` (and, for range, the value's) must match it. Required,
	// 128 bytes at most.
	Relation string `json:"relation" validate:"required"`
	// Value is what the relation points at. When Names is true it is another
	// entity's key, folded as Entity is, and the assertion is an EDGE; otherwise
	// it is a scalar, kept byte for byte, and the assertion is a property. 2048
	// bytes at most, or 512 when it names an entity.
	Value string `json:"value"`
	// Names says the value is an entity. A walk reads only the edges, so this is
	// a declaration and never a guess about the value's shape.
	Names bool `json:"names,omitempty"`
	// At is when the thing was so, RFC 3339. Required, and refused when it sits
	// more than five minutes ahead of the server clock — an assertion dated
	// further out would never mature and would skew every read until it did.
	At string `json:"at" validate:"required"`
	// Until is when the thing stopped being so, RFC 3339, and must follow At.
	// Absent is open: the statement holds until something ends it. A term of
	// office, an employment, a membership are At and Until on one statement.
	Until string `json:"until,omitempty"`
	// Seen is when this assertion became knowable, RFC 3339. Defaults to At and
	// may not precede it. It is provenance and it decides nothing: the instant
	// every read uses is derived as the later of Seen and the server's own clock.
	Seen string `json:"seen,omitempty"`
	// Source names who asserted. Required, because an assertion nobody is named
	// for cannot be weighed against one that is. Open text: this plane ranks no
	// source above another.
	Source string `json:"source" validate:"required"`
	// Evidence points at the record this claim came from, 512 bytes at most. An
	// assertion without one is admitted and carries no defence.
	Evidence string `json:"evidence"`
	// Confidence in [0,1]. A tie-breaker within the order, never a substitute
	// for it. Absent is 0, the weakest an assertion can be.
	Confidence float64 `json:"confidence,omitempty"`
}

// graphAssertOut reports what happened to each member. Recorded + Duplicate +
// Refused is exactly the number sent, so a caller can reconcile without guessing.
type graphAssertOut struct {
	// Recorded is how many members became new rows.
	Recorded int `json:"recorded"`
	// Duplicate is how many members this plane already held. A redelivery
	// collides on its content address and is counted here, not refused: it is
	// the success a retrying caller depends on.
	Duplicate int `json:"duplicate"`
	// Refused is how many members were turned away — a missing entity, a
	// timestamp that is not RFC 3339, a confidence outside [0,1], a relation
	// whose declared domain or range the entity's type does not meet, or a
	// declaration from a caller who is not an admin of the organization. The
	// rest of the batch was still recorded; a batch refused whole answers 400,
	// or 403 when a member of it was refused for want of authority.
	Refused int `json:"refused"`
	// Reasons names why each refused member was refused, in the order sent.
	Reasons []string `json:"reasons,omitempty"`
}

// assert records a batch of assertions and counts what became of each.
//
// Each member is judged alone: one refusal does not discard the rest, and a
// member this plane already holds is a duplicate, not a refusal. A batch with
// nothing admitted answers 400.
//
// Time: at and until are when the statement was so, which an as_of read is
// bounded by; the server stamps knowable, the later of seen and its own clock,
// which an as_known read is bounded by. Nothing is overwritten: a correction is
// the statement filed again, and the version known latest speaks for it.
//
// Example: {"assertions": [{"entity": "acme/svc/api", "relation": "owner", "value": "acme/team/core", "names": true, "at": "2026-09-01T00:00:00Z", "source": "wiki/api", "evidence": "wiki/api#1", "confidence": 0.9}]}
func (o ops) assert(ctx context.Context, in *graphAssertIn) (*graphAssertOut, error) {
	sc, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if len(in.Assertions) == 0 {
		return nil, zip.ErrBadRequest("no assertions")
	}
	out, forbidden, err := o.file(ctx, sc, st, in.Assertions, time.Now().UTC(), "graph.declare", sc.declared)
	if err != nil {
		return nil, err
	}
	// A batch nothing in which was admitted is a 400: a 200 that filed nothing
	// has told the caller nothing, and a single refused assertion is the common
	// case of it. It is a 403 when a member was refused for want of authority,
	// which no correction to the body cures.
	if out.Recorded+out.Duplicate == 0 {
		if forbidden {
			return nil, zip.ErrForbidden(strings.Join(out.Reasons, "; "))
		}
		return nil, zip.ErrBadRequest(strings.Join(out.Reasons, "; "))
	}
	return out, nil
}

// sift is what of a batch may be filed, each member judged on its own: admitted
// (fromWire), then judged (judge). out counts and names the refusals; forbidden
// says one was for want of authority.
func (o ops) sift(ctx context.Context, sc scope, st *store, batch []graphFact, now time.Time) (kept []Fact, out *graphAssertOut, forbidden bool, err error) {
	why := make([]string, len(batch))
	ready := make([]Fact, 0, len(batch))
	at := make([]int, 0, len(batch))
	for i, a := range batch {
		f, err := fromWire(a, sc.by, now)
		if err != nil {
			why[i] = err.Error()
			continue
		}
		ready = append(ready, f)
		at = append(at, i)
	}
	// A member is judged only once it is well-formed, and on the admitted key,
	// so no spelling of `relation:` or `rule:` slips past the authority it needs.
	bad, forbidden, err := judge(ctx, st, ready)
	if err != nil {
		return nil, nil, false, err
	}
	kept = make([]Fact, 0, len(ready))
	for j, f := range ready {
		if bad[j] != "" {
			why[at[j]] = bad[j]
			continue
		}
		kept = append(kept, f)
	}
	out = &graphAssertOut{}
	for _, w := range why {
		if w != "" {
			out.Refused++
			out.Reasons = append(out.Reasons, w)
		}
	}
	return kept, out, forbidden, nil
}

// fromWire admits one wire assertion, stamped with the asserter.
func fromWire(a graphFact, by string, now time.Time) (Fact, error) {
	at, err := instant(a.At)
	if err != nil {
		return Fact{}, err
	}
	seen := at
	if a.Seen != "" {
		if seen, err = instant(a.Seen); err != nil {
			return Fact{}, err
		}
	}
	var until time.Time
	if a.Until != "" {
		if until, err = instant(a.Until); err != nil {
			return Fact{}, err
		}
	}
	return stamped(Fact{
		Entity: a.Entity, Relation: a.Relation, Value: a.Value, Names: a.Names,
		At: at, Until: until, Seen: seen, Source: a.Source, Evidence: a.Evidence,
		By: by, Confidence: a.Confidence,
	}, now)
}

// stamped admits an assertion and stamps what no caller may supply: the server
// clock, the derived knowable instant and the address.
func stamped(f Fact, now time.Time) (Fact, error) {
	f, err := admit(f, now)
	if err != nil {
		return Fact{}, err
	}
	f.Wrote = now
	f.Knowable = claim.Knowable(f.Seen, f.Wrote)
	f.ID = digest(f)
	return f, nil
}

// ── read ─────────────────────────────────────────────────────────────────────

type graphReadIn struct {
	// Entity narrows to what was asserted ABOUT one entity. Absent matches every
	// entity.
	Entity string `json:"entity,omitempty"`
	// Relation narrows to one relation. Absent matches every relation.
	Relation string `json:"relation,omitempty"`
	// Value narrows to assertions pointing AT one value, which is how the edges
	// into an entity are read. A property's scalar is matched byte for byte and
	// an edge's value by its key, folded as every key is.
	Value string `json:"value,omitempty"`
	// AsOf bounds the read to statements begun by an instant of the world, RFC
	// 3339. Absent reads every instant.
	AsOf string `json:"as_of,omitempty"`
	// AsKnown bounds the read to what this plane had heard by an instant, RFC
	// 3339. Absent reads everything it holds.
	AsKnown string `json:"as_known,omitempty"`
	// Limit caps how many assertions come back. Absent, zero, or anything above
	// the walk ceiling is the ceiling.
	Limit int `json:"limit,omitempty"`
}

type graphReadOut struct {
	// Assertions are the matching rows: a read's in the order they were written,
	// oldest first; a search's best match first. Every version is here: this
	// resolves nothing and withholds nothing, so a superseded claim and the one
	// that superseded it both appear.
	Assertions []wireFact `json:"assertions"`
	// Truncated says more assertions matched than came back: the limit, or the
	// ceiling, stopped the read. Narrow it, or page by as_known, to see the rest;
	// a search that stopped kept the best matches.
	Truncated bool `json:"truncated"`
}

// bounds reads a raw read's two optional bounds into a filter. Unlike a
// resolution, an absent bound here is no bound: a read of what was asserted
// withholds nothing it was not asked to.
func bounds(f *filter, asOf, asKnown string) (err error) {
	if asOf != "" {
		if f.Valid, err = instant(asOf); err != nil {
			return err
		}
	}
	if asKnown != "" {
		if f.Known, err = instant(asKnown); err != nil {
			return err
		}
	}
	return nil
}

// ceiling is the row count a read with this limit stops at.
func ceiling(limit int) int {
	if limit <= 0 || limit > walkBound {
		return walkBound
	}
	return limit
}

// read lists the assertions recorded, every version, oldest first.
//
// It resolves nothing: a superseded claim and the one that superseded it both
// appear.
//
// Time: as_of keeps statements begun by that instant of the world and as_known
// keeps what this plane had heard by then; either absent is no bound.
//
// Example: {"entity": "acme/svc/api", "as_known": "2026-09-01T00:00:00Z"}
func (o ops) read(ctx context.Context, in *graphReadIn) (*graphReadOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	f := filter{Entity: key(in.Entity), Relation: key(in.Relation), Value: in.Value, Limit: in.Limit}
	if err := bounds(&f, in.AsOf, in.AsKnown); err != nil {
		return nil, err
	}
	facts, err := st.read(ctx, f)
	if err != nil {
		return nil, err
	}
	return &graphReadOut{Assertions: toWire(facts), Truncated: len(facts) == ceiling(in.Limit)}, nil
}

// ── search ───────────────────────────────────────────────────────────────────

type graphSearchIn struct {
	// Q is what to look for: words, matched as prefixes, all of them required.
	// Punctuation is text here rather than syntax, so an entity key searches as
	// itself.
	Q string `json:"q" validate:"required"`
	// Relation narrows to one relation. Absent matches every relation.
	Relation string `json:"relation,omitempty"`
	// AsOf bounds the search to statements begun by an instant of the world,
	// RFC 3339. Absent searches every instant.
	AsOf string `json:"as_of,omitempty"`
	// AsKnown bounds the search to what this plane had heard by an instant, RFC
	// 3339. Absent searches everything it holds.
	AsKnown string `json:"as_known,omitempty"`
	// Limit caps how many assertions come back. Absent, zero, or anything above
	// the walk ceiling is the ceiling.
	Limit int `json:"limit,omitempty"`
}

// search finds assertions by their words, where read finds them by their keys,
// best match first.
//
// Every word must match, as a prefix. The answer is ordered by bm25, which
// weighs a word by how rare it is and a row by how much of it the words are, so
// a limit keeps the best matches rather than the oldest. It resolves nothing, so
// a match may be a claim later corrected; ask resolve about what it finds.
//
// Time: as_of and as_known bound it exactly as they bound read; either absent is
// no bound.
//
// Example: {"q": "billing owner"}
func (o ops) search(ctx context.Context, in *graphSearchIn) (*graphReadOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	q := terms(in.Q)
	if len(q) == 0 {
		return nil, zip.ErrBadRequest("q is required: a search with no word in it is a read")
	}
	f := filter{Relation: key(in.Relation), Limit: in.Limit, Match: q}
	if err := bounds(&f, in.AsOf, in.AsKnown); err != nil {
		return nil, err
	}
	facts, err := st.read(ctx, f)
	if err != nil {
		return nil, err
	}
	return &graphReadOut{Assertions: toWire(facts), Truncated: len(facts) == ceiling(in.Limit)}, nil
}

// ── resolve ──────────────────────────────────────────────────────────────────

type graphResolveIn struct {
	// Entity is the thing to answer about. Required.
	Entity string `json:"entity" validate:"required"`
	// Relation is the one relation to settle. Required: this answers a single
	// (entity, relation) pair, never a whole entity at once.
	Relation string `json:"relation" validate:"required"`
	// AsOf is the instant of the world to answer about, RFC 3339: what held
	// then. Absent is now.
	AsOf string `json:"as_of,omitempty"`
	// AsKnown is how much this plane had heard, RFC 3339: the answer as it would
	// have been given then, which is what makes a past answer reproducible.
	// Absent is now.
	AsKnown string `json:"as_known,omitempty"`
}

// graphResolveOut is everything holding at the point, winner first. A contested
// relation is visible as contested rather than resolved into silence.
type graphResolveOut struct {
	// Entity is the entity the question named, echoed so a stored answer still
	// says what it is about.
	Entity string `json:"entity"`
	// Relation is the relation the question named, echoed for the same reason.
	Relation string `json:"relation"`
	// AsOf is the valid instant this answer was taken at, RFC 3339: the one
	// asked for, or the server's clock when none was.
	AsOf string `json:"as_of"`
	// AsKnown is the knowledge instant it was taken at, RFC 3339, the same way.
	AsKnown string `json:"as_known"`
	// Truncated says this pair holds more statements than one read returns, so
	// the answer was decided from the ceiling-full begun most recently. It is
	// reported because a provenance plane that trims silently is a plane that
	// answers confidently and wrongly; narrow the question with as_of to see
	// what it dropped.
	Truncated bool `json:"truncated,omitempty"`
	// Known is false when nothing held at the point. That is an answer, not an
	// error.
	Known bool `json:"known"`
	// Winner is the statement holding at the point that began most recently,
	// then the strongest under the order `rule` names. Absent exactly when Known
	// is false.
	Winner *wireFact `json:"winner,omitempty"`
	// Held is every statement holding at the point, Winner first, one per
	// value: one for a relation that holds one value at a time, and every one
	// the pair holds for a relation that holds many — the five holders of a
	// position over five terms are five.
	Held []wireFact `json:"held,omitempty"`
	// Conflicts is, for a relation holding one value, every weaker account of
	// the winner's moment that names a different value, strongest first. They
	// lost the order and stay visible, so a disagreement is never resolved into
	// silence.
	Conflicts []wireFact `json:"conflicts,omitempty"`
	// Contested is true exactly when Conflicts is not empty.
	Contested bool `json:"contested"`
}

// resolve answers what holds for one entity and relation at a point, winner
// first, with every weaker account that disagreed.
//
// The winner is the holding statement begun most recently, then the strongest
// under the rule vocabulary names. A relation that holds many values answers
// each; a disagreement is reported as contested, never resolved into silence.
//
// Time: as_of is the instant of the world asked about and as_known how much this
// plane had heard; either absent is now.
//
// Example: {"entity": "acme/svc/api", "relation": "owner", "as_of": "2026-09-01T00:00:00Z"}
func (o ops) resolve(ctx context.Context, in *graphResolveIn) (*graphResolveOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	// The tags hold this at the wire; GraphQL's resolve reaches here without it,
	// and an empty relation would read every relation of the entity.
	entity, relation := key(in.Entity), key(in.Relation)
	if entity == "" || relation == "" {
		return nil, zip.ErrBadRequest("entity and relation are both required")
	}
	w, err := moment(in.AsOf, in.AsKnown)
	if err != nil {
		return nil, err
	}
	// Bounded by the point AND ordered by the statements begun most recently,
	// both of which decide the answer rather than merely trimming it. Without
	// the bounds, rows begun or heard of later spend the ceiling on what this
	// resolution must ignore; without the order, the ceiling drops the rows
	// that hold. The pair is what makes a truncated read still resolve right.
	out := &graphResolveOut{Entity: entity, Relation: relation,
		AsOf: w.valid.Format(time.RFC3339), AsKnown: w.known.Format(time.RFC3339)}
	facts, err := st.read(ctx, filter{Entity: entity, Relation: relation, Valid: w.valid, Known: w.known, Newest: true})
	if err != nil {
		return nil, err
	}
	out.Truncated = len(facts) == walkBound
	card, err := st.arity(ctx, []string{relation}, w.known)
	if err != nil {
		return nil, err
	}
	held, conflicts := Resolve(facts, w, card.one(facts))
	if len(held) == 0 {
		return out, nil
	}
	all := toWire(held)
	out.Known, out.Winner, out.Held = true, &all[0], all
	out.Conflicts, out.Contested = toWire(conflicts), len(conflicts) > 0
	return out, nil
}

// moment reads a question's point in the plane's two times: as_of, when the
// world was so, and as_known, how much this plane had heard by then. Either
// absent is now.
func moment(asOf, asKnown string) (when, error) {
	now := time.Now().UTC()
	w := when{valid: now, known: now}
	var err error
	if asOf != "" {
		if w.valid, err = instant(asOf); err != nil {
			return w, err
		}
	}
	if asKnown != "" {
		if w.known, err = instant(asKnown); err != nil {
			return w, err
		}
	}
	return w, nil
}

// ── neighbors ────────────────────────────────────────────────────────────────

type graphNeighborsIn struct {
	// Seeds is where the walk starts. At least one.
	Seeds []string `json:"seeds" validate:"required"`
	// Relation narrows the walk to one edge relation. Absent follows all. Only
	// edges IN FORCE are ever followed — the assertion that wins its (entity,
	// relation) at the instant — so a superseded or retracted edge is not a hop,
	// and neither is a property.
	Relation string `json:"relation,omitempty"`
	// Direction is out, in or both. Out follows an edge from its entity to its
	// value — what the node points at; in follows it the other way — what points
	// at the node; both is the union of the two, not a third rule. Absent is out.
	Direction string `json:"direction,omitempty"`
	// Depth is how many hops. Absent is one.
	Depth int `json:"depth,omitempty"`
	// AsOf walks the graph as it stood at an instant of the world, RFC 3339:
	// the edges that held then. Absent is now.
	AsOf string `json:"as_of,omitempty"`
	// AsKnown is how much this plane had heard, RFC 3339: the answer as it would
	// have been given then, which is what makes a past answer reproducible.
	// Absent is now.
	AsKnown string `json:"as_known,omitempty"`
}

type graphNeighborsOut struct {
	// Entities is everything reached, the seeds included, ordered by the fewest
	// hops that reach each one and then by key.
	Entities []string `json:"entities"`
	// Depth is the deepest hop count actually reached. It is at most the depth
	// asked for, and smaller when the walk ran out of edges first.
	Depth int `json:"depth"`
	// Truncated says the bound stopped the walk. The bound is part of the answer
	// rather than a silent short read.
	Truncated bool `json:"truncated"`
	// Bound is the ceiling this walk was held to, the same for every caller, so
	// Truncated can be read against a number rather than guessed at.
	Bound int `json:"bound"`
}

// neighbors walks the in-force edges from a set of seeds and lists every
// entity reached, bounded.
//
// Only the edge that holds at the point is a hop: a superseded or retracted
// edge is not, and neither is a property.
//
// Time: as_of walks the graph as it stood at that instant and as_known as this
// plane knew it then; either absent is now.
//
// Example: {"seeds": ["acme/svc/api"], "relation": "depends", "depth": 2}
func (o ops) neighbors(ctx context.Context, in *graphNeighborsIn) (*graphNeighborsOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	dir := in.Direction
	if dir == "" {
		dir = "out"
	}
	w, err := moment(in.AsOf, in.AsKnown)
	if err != nil {
		return nil, err
	}
	var rels []string
	if in.Relation != "" {
		rels = []string{key(in.Relation)}
	}
	t, err := st.walk(ctx, folded(in.Seeds), rels, dir, in.Depth, w, "")
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	return &graphNeighborsOut{Entities: t.nodes, Depth: t.deepest, Truncated: t.truncated, Bound: walkBound}, nil
}

// ── path ─────────────────────────────────────────────────────────────────────

type graphPathIn struct {
	// From is the entity the path starts at. Required.
	From string `json:"from" validate:"required"`
	// To is the entity the path ends at. Required.
	To string `json:"to" validate:"required"`
	// Relations is the edge relations a path may cross. Absent crosses every
	// relation. A causal chain is this list naming the relations that mean
	// cause — `caused_by` and `influenced` under the decision convention. At
	// most 256.
	Relations []string `json:"relations,omitempty"`
	// Direction is out, in or both. Out crosses each edge from its entity to
	// its value; in crosses it backwards; both crosses it either way. Absent is
	// out.
	Direction string `json:"direction,omitempty"`
	// AsOf finds the path through the graph as it stood at an instant of the
	// world, RFC 3339: an edge that did not hold then is not crossed. Absent is
	// now.
	AsOf string `json:"as_of,omitempty"`
	// AsKnown is how much this plane had heard, RFC 3339: the answer as it would
	// have been given then, which is what makes a past answer reproducible.
	// Absent is now.
	AsKnown string `json:"as_known,omitempty"`
}

type graphPathOut struct {
	// Found is false when no path of in-force edges joins the two within the
	// bound. That is an answer, not an error.
	Found bool `json:"found"`
	// Hops is how many edges the path crosses: zero when From is To, and zero
	// when nothing was found.
	Hops int `json:"hops"`
	// Edges is the path from From to To, one assertion per hop, each with its
	// source, evidence and filer. An edge is listed as it was asserted, so
	// under in or both it may point against the direction of travel.
	Edges []wireFact `json:"edges"`
	// Decay is the product of the edges' confidences: how much certainty
	// survives the whole chain. Absent unless every edge states a confidence
	// above zero — an unstated confidence reads as zero, and a product of
	// numbers nobody gave measures nothing.
	Decay float64 `json:"decay,omitempty"`
	// Weakest is the edge with the lowest confidence, the first of them on a
	// tie: the link the chain is only as strong as. Absent exactly when Decay
	// is.
	Weakest *wireFact `json:"weakest,omitempty"`
	// AsOf is the instant the path was taken at, RFC 3339: the one asked for,
	// or the server's clock when none was.
	AsOf string `json:"as_of"`
	// AsKnown is the knowledge instant it was taken at, RFC 3339, the same way.
	AsKnown string `json:"as_known"`
	// Truncated says the bound stopped the search before a path was found, so
	// Found false means none within the bound rather than none at all.
	Truncated bool `json:"truncated"`
	// Bound is the ceiling the search was held to, the same one a walk is.
	Bound int `json:"bound"`
}

// path finds the shortest chain of in-force edges from one entity to another.
//
// It is the walk behind neighbors stopped at a goal: the same bound and the same
// rule for which edge is in force. A causal chain is this op with relations
// naming the relations that mean cause, such as caused_by and influenced.
//
// Time: as_of and as_known place the graph the path is found in; either absent
// is now.
//
// Example: {"from": "acme/svc/api", "to": "acme/team/core", "direction": "both"}
func (o ops) path(ctx context.Context, in *graphPathIn) (*graphPathOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	from, to := key(in.From), key(in.To)
	if from == "" || to == "" {
		return nil, zip.ErrBadRequest("from and to are both required")
	}
	dir := in.Direction
	if dir == "" {
		dir = "out"
	}
	w, err := moment(in.AsOf, in.AsKnown)
	if err != nil {
		return nil, err
	}
	edges, found, cut, err := st.path(ctx, from, to, folded(in.Relations), dir, w)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	out := &graphPathOut{
		Found: found, Hops: len(edges), Edges: toWire(edges),
		AsOf: w.valid.Format(time.RFC3339), AsKnown: w.known.Format(time.RFC3339), Truncated: cut, Bound: walkBound,
	}
	if product, weakest, ok := decay(edges); ok {
		out.Decay, out.Weakest = product, &out.Edges[weakest]
	}
	return out, nil
}

// ── diff ─────────────────────────────────────────────────────────────────────

type graphDiffIn struct {
	// Entity narrows the diff to what was asserted about one entity. Absent
	// diffs the whole graph, bounded by Limit.
	Entity string `json:"entity,omitempty"`
	// From is the earlier instant of the world, RFC 3339. What held here is
	// the baseline, so a statement beginning exactly at From is part of it and
	// not a change. Absent is To: the diff is then of what was heard, not of
	// what happened.
	From string `json:"from,omitempty"`
	// To is the later instant of the world, RFC 3339, and may not precede From.
	// A statement beginning or ending exactly at To is in the diff. Absent is
	// now.
	To string `json:"to,omitempty"`
	// FromKnown is how much this plane had heard at the baseline, RFC 3339.
	// Absent is ToKnown: the diff is then of the world, as known once.
	FromKnown string `json:"from_known,omitempty"`
	// ToKnown is how much it had heard at the later point, RFC 3339, and may
	// not precede FromKnown. Absent is now.
	ToKnown string `json:"to_known,omitempty"`
	// Limit caps how many (entity, relation) pairs are examined, in key order.
	// Absent, zero, or anything above the walk ceiling is the ceiling.
	Limit int `json:"limit,omitempty"`
}

// graphChange is one pair's in-force assertion at each end of a diff.
type graphChange struct {
	// Was is the assertion in force at From.
	Was wireFact `json:"was"`
	// Now is the assertion in force at To: the claim that replaced Was, or,
	// for a retraction, the retraction itself — which is who withdrew it and on
	// what evidence.
	Now wireFact `json:"now"`
}

type graphDiffOut struct {
	// From is the earlier instant, RFC 3339, echoed.
	From string `json:"from"`
	// To is the later instant, RFC 3339: the one asked for, or the server's
	// clock when none was.
	To string `json:"to"`
	// FromKnown is the knowledge instant of the baseline, RFC 3339.
	FromKnown string `json:"from_known"`
	// ToKnown is the knowledge instant of the later point, RFC 3339.
	ToKnown string `json:"to_known"`
	// Asserted is each statement holding at To that did not at From.
	Asserted []wireFact `json:"asserted"`
	// Superseded is each property whose value at From was replaced by a
	// different one at To, once per pair however many came in between.
	Superseded []graphChange `json:"superseded"`
	// Retracted is each statement that held at From and no longer does at To,
	// with what ended it as Now: the retraction, the later value, or the
	// statement itself when its own until did.
	Retracted []graphChange `json:"retracted"`
	// Truncated says more pairs gathered assertions in the interval than Bound,
	// or that one pair holds more than a single read returns. Narrow by entity
	// or by a shorter interval to see the rest.
	Truncated bool `json:"truncated"`
	// Bound is how many pairs this diff examined at most.
	Bound int `json:"bound"`
}

// diff reports what came into force, was superseded and was retracted between
// two points.
//
// Each point is resolved as resolve would, never read off the rows in between.
// A version that loses the order is invisible here as it is there, and one that
// repeats what held is agreement, not change.
//
// Time: a point is an as_of and an as_known instant, from and from_known against
// to and to_known. Hold the known instants equal for what happened in the world;
// hold the valid instants equal for what the record learned. to and to_known
// default to now, from to to and from_known to to_known, and the two points must
// differ.
//
// Example: {"entity": "acme/svc/api", "from": "2026-08-01T00:00:00Z", "to": "2026-09-01T00:00:00Z"}
func (o ops) diff(ctx context.Context, in *graphDiffIn) (*graphDiffOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	b, err := moment(in.To, in.ToKnown)
	if err != nil {
		return nil, err
	}
	a := b
	if in.From != "" {
		if a.valid, err = instant(in.From); err != nil {
			return nil, err
		}
	}
	if in.FromKnown != "" {
		if a.known, err = instant(in.FromKnown); err != nil {
			return nil, err
		}
	}
	if b.valid.Before(a.valid) {
		return nil, zip.ErrBadRequest("to precedes from")
	}
	if b.known.Before(a.known) {
		return nil, zip.ErrBadRequest("to_known precedes from_known")
	}
	if a == b {
		return nil, zip.ErrBadRequest("a diff is between two points: from or from_known is required")
	}
	limit := in.Limit
	if limit <= 0 || limit > walkBound {
		limit = walkBound
	}
	deltas, cut, err := st.diff(ctx, key(in.Entity), a, b, limit)
	if err != nil {
		return nil, err
	}
	out := &graphDiffOut{
		From: a.valid.Format(time.RFC3339), To: b.valid.Format(time.RFC3339),
		FromKnown: a.known.Format(time.RFC3339), ToKnown: b.known.Format(time.RFC3339),
		Asserted: []wireFact{}, Superseded: []graphChange{}, Retracted: []graphChange{},
		Truncated: cut, Bound: limit,
	}
	for _, d := range deltas {
		now := toWire([]Fact{d.now})[0]
		switch d.kind {
		case "asserted":
			out.Asserted = append(out.Asserted, now)
		case "superseded":
			out.Superseded = append(out.Superseded, graphChange{Was: toWire([]Fact{d.was})[0], Now: now})
		case "retracted":
			out.Retracted = append(out.Retracted, graphChange{Was: toWire([]Fact{d.was})[0], Now: now})
		}
	}
	return out, nil
}

// ── vocabulary ───────────────────────────────────────────────────────────────

type graphVocabularyOut struct {
	// Relations is what this organization has actually asserted. This plane
	// declares none of its own.
	Relations []string `json:"relations"`
	// Schema is what the organization has declared, in force now: each relation
	// with a domain or range, asserted as (`relation:<name>`, `domain` |
	// `range`, `<type>`) and checked against the entities' (`<entity>`, `type`,
	// `<type>`). A relation absent here is open.
	//
	// A decision is an entity of type `decision` by convention, never enforced:
	// `scenario`, `reasoning`, `outcome` and `confidence` are its properties;
	// `decided_by`, `caused_by`, `influenced` and `precedent` are its edges.
	// Declare their domain and range to make that convention checked; find a
	// precedent with search, then neighbors over `precedent`.
	Schema []graphDeclared `json:"schema"`
	// Rule names the terms of the precedence order, in the order they apply. A
	// reader who is told a winner without the rule cannot check it.
	Rule []string `json:"rule"`
	// Bound is the ceiling on one walk.
	Bound int `json:"bound"`
}

// vocabulary lists the relations in use, the schema declared for them and the
// rule that settles a conflict.
//
// Time: it takes no as_of or as_known; the schema is the one in force now.
func (o ops) vocabulary(ctx context.Context, _ *cloud.Unit) (*graphVocabularyOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	rels, err := st.relations(ctx)
	if err != nil {
		return nil, err
	}
	decls, err := st.declarations(ctx, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	// The terms this plane's order ACTUALLY applies. Rank is absent on purpose:
	// this plane adjudicates between no two sources, so it asserts no weight.
	return &graphVocabularyOut{
		Relations: rels,
		Schema:    decls,
		Rule:      []string{"at", "knowable", "confidence", "digest"},
		Bound:     walkBound,
	}, nil
}

// graphDeclared is one relation's declared schema.
type graphDeclared struct {
	// Relation is the relation declared, without its `relation:` prefix.
	Relation string `json:"relation"`
	// Domain is the type the entity asserting it must have. Absent is any.
	Domain string `json:"domain,omitempty"`
	// Range is the type the entity it points at must have; a relation with a
	// range is an edge. Absent is any.
	Range string `json:"range,omitempty"`
	// Cardinality is how many values the relation holds at once: one, or many.
	// Absent is the default — one for a property, many for an edge.
	Cardinality string `json:"cardinality,omitempty"`
}

// ── wire ─────────────────────────────────────────────────────────────────────

// wireFact is one assertion as a caller reads it. Seq and the hold are the
// store's own business and are not on the wire.
type wireFact struct {
	// ID is the assertion's content address, minted by the server from what was
	// asserted. Two callers who assert the identical thing land on one ID and one
	// row; changing any asserted field makes a different ID and a second row.
	ID string `json:"id"`
	// Entity is the thing described, in the organization's own namespace.
	Entity string `json:"entity"`
	// Relation is what was asserted of it.
	Relation string `json:"relation"`
	// Value is what the relation points at: another entity's key when Names is
	// true, otherwise a scalar.
	Value string `json:"value"`
	// Names true means the assertion is an edge and Value is an entity. A walk
	// reads only these.
	Names bool `json:"names,omitempty"`
	// At is when the thing was so, RFC 3339, as the asserter gave it.
	At string `json:"at"`
	// Until is when the thing stopped being so, RFC 3339, as the asserter gave
	// it. Absent is open.
	Until string `json:"until,omitempty"`
	// Seen is when the asserter says it became knowable, RFC 3339. Provenance
	// only — Knowable is what an as-of read is bounded by.
	Seen string `json:"seen"`
	// Knowable is the first instant this plane could have answered with the
	// assertion, RFC 3339: the later of Seen and the server's clock at the write.
	// Derived and never supplied, which is what stops history filed today from
	// being backdated into a past read.
	Knowable string `json:"knowable"`
	// Source names who asserted, as the caller gave it. This plane ranks no
	// source above another, so it never outweighs a later Knowable.
	Source string `json:"source"`
	// Evidence points at the record the claim came from. Absent when the asserter
	// gave none.
	Evidence string `json:"evidence,omitempty"`
	// By is the identity that filed it — `owner` or `owner/user` — stamped from
	// the validated principal at the write, never from the body.
	By string `json:"by"`
	// Confidence in [0,1] as the asserter gave it; absent is 0. It breaks a tie
	// between two assertions equally knowable and decides nothing else.
	Confidence float64 `json:"confidence,omitempty"`
}

func toWire(facts []Fact) []wireFact {
	out := make([]wireFact, 0, len(facts))
	for _, f := range facts {
		out = append(out, wireFact{
			ID: f.ID, Entity: f.Entity, Relation: f.Relation, Value: f.Value, Names: f.Names,
			At: f.At.Format(time.RFC3339), Until: stamp(f.Until), Seen: f.Seen.Format(time.RFC3339),
			Knowable: f.Knowable.Format(time.RFC3339),
			Source:   f.Source, Evidence: f.Evidence, By: f.By, Confidence: f.Confidence,
		})
	}
	return out
}

// stamp is an instant as the wire writes it, and nothing for an open end.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func instant(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, zip.ErrBadRequest(fmt.Sprintf("timestamp %q is not RFC 3339", s))
	}
	return t.UTC(), nil
}
