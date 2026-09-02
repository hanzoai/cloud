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
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/claim"
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
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		s.Log.Error("graph: the router exposes no op registry; the assertion surface would serve routes no projection knows")
		return
	}
	o := ops{s: s}

	// Declared on the App with WHOLE paths: the collection route IS the prefix,
	// which a group cannot spell.
	zip.Post(zapp, "/v1/graph", o.assert,
		zip.WithOperationID("graphAssert"),
		zip.WithSummary("Assert what is true of an entity"),
		zip.WithTags("graph"))
	zip.Get(zapp, "/v1/graph", o.read,
		zip.WithOperationID("graphRead"),
		zip.WithSummary("Read the assertions this organization has recorded"),
		zip.WithTags("graph"))
	zip.Post(zapp, "/v1/graph/resolve", o.resolve,
		zip.WithOperationID("graphResolve"),
		zip.WithSummary("What is in force about an entity as of an instant, and what disagreed"),
		zip.WithTags("graph"))
	zip.Post(zapp, "/v1/graph/neighbors", o.neighbors,
		zip.WithOperationID("graphNeighbors"),
		zip.WithSummary("Walk the edges from a seed set, bounded"),
		zip.WithTags("graph"))
	zip.Get(zapp, "/v1/graph/search", o.search,
		zip.WithOperationID("graphSearch"),
		zip.WithSummary("Find assertions by their text rather than by an entity key"),
		zip.WithTags("graph"))
	zip.Get(zapp, "/v1/graph/vocabulary", o.vocabulary,
		zip.WithOperationID("graphVocabulary"),
		zip.WithSummary("The relations in use, and the rule that resolves a conflict"),
		zip.WithTags("graph"))

	// The GraphQL endpoint, at the address apps/explorer already established for one
	// (/v1/<product>/graphql). It is UNTYPED by construction rather than by
	// omission: a typed op declares one In and one Out, and this route's input is
	// a query whose OUTPUT SHAPE the caller chooses. Its bodies are declared
	// instead, beside the routes, in the same init the rest of this package uses.
	//
	// A schema its resolvers do not satisfy is a mount failure, not a 500 on the
	// first request — the parse happens once, here.
	sc, err := schemaOf(o)
	if err != nil {
		// Same shape as openapi.Register's duplicate: a fact about the code, so
		// it stops the binary rather than waiting for a caller to discover it.
		panic("graph: graphql schema does not match its resolvers: " + err.Error())
	}
	zapp.Post("/v1/graph/graphql", serveGraphQL(sc))
}

// ── assert ───────────────────────────────────────────────────────────────────

// graphAssertIn is a batch of assertions.
type graphAssertIn struct {
	// Assertions is the batch. Each member is judged on its own: one refusal
	// does not discard the rest, because a caller redelivering five facts must
	// not lose four of them to one malformed fifth.
	Assertions []graphFact `json:"assertions"`
}

// graphFact is one assertion, idempotent on its CONTENT: the same assertion
// delivered twice is one row, and anything differing in any field is a DIFFERENT
// assertion recorded beside the first. Nothing here ever overwrites anything,
// and there is no delete — a retraction is an assertion.
type graphFact struct {
	// Entity is the thing being described, in the organization's own namespace.
	// It is not created: an entity exists because something was asserted about it.
	// Required, 512 bytes at most.
	Entity string `json:"entity"`
	// Relation is what is being asserted — `depends`, `owner`, `same`, `title`.
	// It is open: this plane holds no vocabulary of its own. Required, 128 bytes
	// at most.
	Relation string `json:"relation"`
	// Value is what the relation points at. When Names is true it is another
	// entity's key and the assertion is an EDGE; otherwise it is a scalar and
	// the assertion is a property. 2048 bytes at most, or 512 when it names an
	// entity.
	Value string `json:"value"`
	// Names says the value is an entity. A walk reads only the edges, so this is
	// a declaration and never a guess about the value's shape.
	Names bool `json:"names,omitempty"`
	// At is when the thing was so, RFC 3339. Required, and refused when it sits
	// more than five minutes ahead of the server clock — an assertion dated
	// further out would never mature and would skew every read until it did.
	At string `json:"at"`
	// Seen is when this assertion became knowable, RFC 3339. Defaults to At and
	// may not precede it. It is provenance and it decides nothing: the instant
	// every read uses is derived as the later of Seen and the server's own clock.
	Seen string `json:"seen,omitempty"`
	// Source names who asserted. Required, because an assertion nobody is named
	// for cannot be weighed against one that is. Open text: this plane ranks no
	// source above another.
	Source string `json:"source"`
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
	// Refused is how many members were turned away on arrival, before the store
	// was touched — a missing entity, a timestamp that is not RFC 3339, a
	// confidence outside [0,1]. The rest of the batch was still recorded.
	Refused int `json:"refused"`
	// Reasons names why each refused member was refused, in the order sent.
	Reasons []string `json:"reasons,omitempty"`
}

func (o ops) assert(ctx context.Context, in *graphAssertIn) (*graphAssertOut, error) {
	sc, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if len(in.Assertions) == 0 {
		return nil, zip.ErrBadRequest("no assertions")
	}
	now := time.Now().UTC()
	out := &graphAssertOut{}
	ready := make([]Fact, 0, len(in.Assertions))
	for _, a := range in.Assertions {
		f, err := fromWire(a, sc.by, now)
		if err != nil {
			out.Refused++
			out.Reasons = append(out.Reasons, err.Error())
			continue
		}
		ready = append(ready, f)
	}
	n, err := st.record(ctx, ready)
	if err != nil {
		return nil, err
	}
	out.Recorded = n
	out.Duplicate = len(ready) - n
	return out, nil
}

// fromWire admits one wire assertion and stamps what the caller may not supply:
// the asserter, the server clock, the derived knowable instant and the address.
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
	f := Fact{
		Entity: a.Entity, Relation: a.Relation, Value: a.Value, Names: a.Names,
		At: at, Seen: seen, Source: a.Source, Evidence: a.Evidence,
		By: by, Confidence: a.Confidence,
	}
	if f, err = admit(f, now); err != nil {
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
	// into an entity are read.
	Value string `json:"value,omitempty"`
	// AsOf bounds the read to what was knowable at an instant, RFC 3339. Absent
	// reads everything this plane holds.
	AsOf string `json:"as_of,omitempty"`
	// Limit caps how many assertions come back. Absent, zero, or anything above
	// the walk ceiling is the ceiling.
	Limit int `json:"limit,omitempty"`
}

type graphReadOut struct {
	// Assertions are the matching rows in the order they were written, oldest
	// first. Every version is here: this read resolves nothing and withholds
	// nothing, so a superseded claim and the one that superseded it both appear.
	Assertions []wireFact `json:"assertions"`
}

func (o ops) read(ctx context.Context, in *graphReadIn) (*graphReadOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	f := filter{Entity: in.Entity, Relation: in.Relation, Value: in.Value, Limit: in.Limit}
	if in.AsOf != "" {
		if f.AsOf, err = instant(in.AsOf); err != nil {
			return nil, err
		}
	}
	facts, err := st.read(ctx, f)
	if err != nil {
		return nil, err
	}
	return &graphReadOut{Assertions: toWire(facts)}, nil
}

// ── search ───────────────────────────────────────────────────────────────────

type graphSearchIn struct {
	// Q is what to look for: words, matched as prefixes, all of them required.
	// Punctuation is text here rather than syntax, so an entity key searches as
	// itself.
	Q string `json:"q"`
	// Relation narrows to one relation. Absent matches every relation.
	Relation string `json:"relation,omitempty"`
	// AsOf bounds the search to what was knowable at an instant, RFC 3339. Absent
	// searches everything this plane holds.
	AsOf string `json:"as_of,omitempty"`
	// Limit caps how many assertions come back. Absent, zero, or anything above
	// the walk ceiling is the ceiling.
	Limit int `json:"limit,omitempty"`
}

// search finds assertions by their text where read finds them by their keys.
//
// It is the READ with one more term, not a second way to leave the store: same
// order, same ceiling, same tenancy, and searching composes with narrowing by
// relation and by instant because all of them are terms of one filter.
//
// It resolves nothing. What matches is what was asserted, including claims that
// were later corrected — which is the honest answer to "where is this mentioned"
// and the reason the caller then asks resolve about what it found.
func (o ops) search(ctx context.Context, in *graphSearchIn) (*graphReadOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	q := match(in.Q)
	if q == "" {
		return nil, zip.ErrBadRequest("q is required: a search with no word in it is a read")
	}
	f := filter{Relation: in.Relation, Limit: in.Limit, Match: q}
	if in.AsOf != "" {
		if f.AsOf, err = instant(in.AsOf); err != nil {
			return nil, err
		}
	}
	facts, err := st.read(ctx, f)
	if err != nil {
		return nil, err
	}
	return &graphReadOut{Assertions: toWire(facts)}, nil
}

// ── resolve ──────────────────────────────────────────────────────────────────

type graphResolveIn struct {
	// Entity is the thing to answer about. Required.
	Entity string `json:"entity"`
	// Relation is the one relation to settle. Required: this answers a single
	// (entity, relation) pair, never a whole entity at once.
	Relation string `json:"relation"`
	// AsOf is the instant to answer at, RFC 3339. Absent means now.
	AsOf string `json:"as_of,omitempty"`
}

// graphResolveOut carries the dissenters beside the winner. A contested relation
// is visible as contested rather than resolved into silence.
type graphResolveOut struct {
	// Entity is the entity the question named, echoed so a stored answer still
	// says what it is about.
	Entity string `json:"entity"`
	// Relation is the relation the question named, echoed for the same reason.
	Relation string `json:"relation"`
	// AsOf is the instant this answer was taken at, RFC 3339: the one asked for,
	// or the server's clock when none was.
	AsOf string `json:"as_of"`
	// Truncated says this pair holds more assertions than one read returns, so
	// the winner was decided from the most recent ceiling-full of them. It is
	// reported because a provenance plane that trims silently is a plane that
	// answers confidently and wrongly; narrow the question with as_of to see
	// what it dropped.
	Truncated bool `json:"truncated,omitempty"`
	// Known is false when this plane held nothing knowable at AsOf. That is an
	// answer, not an error.
	Known bool `json:"known"`
	// Winner is the assertion in force — the strongest of those knowable at AsOf
	// under the order `rule` names. Absent exactly when Known is false.
	Winner *wireFact `json:"winner,omitempty"`
	// Conflicts is every OTHER assertion knowable at AsOf, strongest first. They
	// are not all disagreements: one that repeats the winner's value ranks below
	// it and is listed here too.
	Conflicts []wireFact `json:"conflicts,omitempty"`
	// Contested is true when at least one conflict claims a value different from
	// the winner's. Any number of conflicts that all agree leaves it false.
	Contested bool `json:"contested"`
}

func (o ops) resolve(ctx context.Context, in *graphResolveIn) (*graphResolveOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if in.Entity == "" || in.Relation == "" {
		return nil, zip.ErrBadRequest("entity and relation are both required")
	}
	asOf := time.Now().UTC()
	if in.AsOf != "" {
		if asOf, err = instant(in.AsOf); err != nil {
			return nil, err
		}
	}
	// Bounded by the instant AND ordered newest-first, both of which decide the
	// answer rather than merely trimming it. Without the instant, assertions
	// knowable after `asOf` spend the ceiling on rows this resolution must
	// ignore; without the ordering, the ceiling drops the newest rows, which are
	// exactly the ones that win. The pair is what makes a truncated read still
	// resolve to the right winner.
	out := &graphResolveOut{Entity: in.Entity, Relation: in.Relation, AsOf: asOf.Format(time.RFC3339)}
	facts, err := st.read(ctx, filter{Entity: in.Entity, Relation: in.Relation, AsOf: asOf, Newest: true})
	if err != nil {
		return nil, err
	}
	out.Truncated = len(facts) == walkBound
	win, conflicts, contested, ok := Resolve(facts, asOf)
	if !ok {
		return out, nil
	}
	w := toWire([]Fact{win})[0]
	out.Known, out.Winner, out.Conflicts, out.Contested = true, &w, toWire(conflicts), contested
	return out, nil
}

// ── neighbors ────────────────────────────────────────────────────────────────

type graphNeighborsIn struct {
	// Seeds is where the walk starts. At least one.
	Seeds []string `json:"seeds"`
	// Relation narrows the walk to one edge relation. Absent follows all. Only
	// edges are ever followed: an assertion whose value is a scalar is a property
	// and is never a hop.
	Relation string `json:"relation,omitempty"`
	// Direction is out, in or both. Out follows an edge from its entity to its
	// value — what the node points at; in follows it the other way — what points
	// at the node; both is the union of the two, not a third rule. Absent is out.
	Direction string `json:"direction,omitempty"`
	// Depth is how many hops. Absent is one.
	Depth int `json:"depth,omitempty"`
	// AsOf walks the graph as it stood at an instant, RFC 3339. Absent walks it
	// as it stands now.
	AsOf string `json:"as_of,omitempty"`
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

func (o ops) neighbors(ctx context.Context, in *graphNeighborsIn) (*graphNeighborsOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	dir := in.Direction
	if dir == "" {
		dir = "out"
	}
	asOf := time.Now().UTC()
	if in.AsOf != "" {
		if asOf, err = instant(in.AsOf); err != nil {
			return nil, err
		}
	}
	nodes, depth, truncated, err := st.walk(ctx, in.Seeds, in.Relation, dir, in.Depth, asOf)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	return &graphNeighborsOut{Entities: nodes, Depth: depth, Truncated: truncated, Bound: walkBound}, nil
}

// ── vocabulary ───────────────────────────────────────────────────────────────

type graphVocabularyOut struct {
	// Relations is what this organization has actually asserted, which is the
	// only vocabulary there is: this plane declares none of its own.
	Relations []string `json:"relations"`
	// Rule names the terms of the precedence order, in the order they apply. A
	// reader who is told a winner without the rule cannot check it.
	Rule []string `json:"rule"`
	// Bound is the ceiling on one walk.
	Bound int `json:"bound"`
}

func (o ops) vocabulary(ctx context.Context, _ *cloud.Unit) (*graphVocabularyOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	rels, err := st.relations(ctx)
	if err != nil {
		return nil, err
	}
	// The terms this plane's order ACTUALLY applies. Rank is absent on purpose:
	// this plane adjudicates between no two sources, so it asserts no weight.
	return &graphVocabularyOut{
		Relations: rels,
		Rule:      []string{"knowable", "confidence", "digest"},
		Bound:     walkBound,
	}, nil
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
			At: f.At.Format(time.RFC3339), Seen: f.Seen.Format(time.RFC3339),
			Knowable: f.Knowable.Format(time.RFC3339),
			Source:   f.Source, Evidence: f.Evidence, By: f.By, Confidence: f.Confidence,
		})
	}
	return out
}

func instant(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, zip.ErrBadRequest(fmt.Sprintf("timestamp %q is not RFC 3339", s))
	}
	return t.UTC(), nil
}
