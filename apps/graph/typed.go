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
	zip.Get(zapp, "/v1/graph/vocabulary", o.vocabulary,
		zip.WithOperationID("graphVocabulary"),
		zip.WithSummary("The relations in use, and the rule that resolves a conflict"),
		zip.WithTags("graph"))
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
	Entity string `json:"entity"`
	// Relation is what is being asserted — `depends`, `owner`, `same`, `title`.
	// It is open: this plane holds no vocabulary of its own.
	Relation string `json:"relation"`
	// Value is what the relation points at. When Names is true it is another
	// entity's key and the assertion is an EDGE; otherwise it is a scalar and
	// the assertion is a property.
	Value string `json:"value"`
	// Names says the value is an entity. A walk reads only the edges, so this is
	// a declaration and never a guess about the value's shape.
	Names bool `json:"names,omitempty"`
	// At is when the thing was so, RFC 3339.
	At string `json:"at"`
	// Seen is when this assertion became knowable, RFC 3339. Defaults to At.
	// It is provenance and it decides nothing: the instant every read uses is
	// derived as the later of Seen and the server's own clock.
	Seen string `json:"seen,omitempty"`
	// Source names who asserted. Required, because an assertion nobody is named
	// for cannot be weighed against one that is.
	Source string `json:"source"`
	// Evidence points at the record this claim came from. Required.
	Evidence string `json:"evidence"`
	// Confidence in [0,1]. A tie-breaker within the order, never a substitute
	// for it.
	Confidence float64 `json:"confidence,omitempty"`
}

// graphAssertOut reports what happened to each member. Recorded + Duplicate +
// Refused is exactly the number sent, so a caller can reconcile without guessing.
type graphAssertOut struct {
	Recorded  int `json:"recorded"`
	Duplicate int `json:"duplicate"`
	Refused   int `json:"refused"`
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
	Entity   string `json:"entity,omitempty"`
	Relation string `json:"relation,omitempty"`
	Value    string `json:"value,omitempty"`
	// AsOf bounds the read to what was knowable at an instant, RFC 3339. Absent
	// reads everything this plane holds.
	AsOf  string `json:"as_of,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type graphReadOut struct {
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

// ── resolve ──────────────────────────────────────────────────────────────────

type graphResolveIn struct {
	Entity   string `json:"entity"`
	Relation string `json:"relation"`
	// AsOf is the instant to answer at, RFC 3339. Absent means now.
	AsOf string `json:"as_of,omitempty"`
}

// graphResolveOut carries the dissenters beside the winner. A contested relation
// is visible as contested rather than resolved into silence.
type graphResolveOut struct {
	Entity   string `json:"entity"`
	Relation string `json:"relation"`
	AsOf     string `json:"as_of"`
	// Known is false when this plane held nothing knowable at AsOf. That is an
	// answer, not an error.
	Known     bool       `json:"known"`
	Winner    *wireFact  `json:"winner,omitempty"`
	Conflicts []wireFact `json:"conflicts,omitempty"`
	Contested bool       `json:"contested"`
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
	facts, err := st.read(ctx, filter{Entity: in.Entity, Relation: in.Relation})
	if err != nil {
		return nil, err
	}
	out := &graphResolveOut{Entity: in.Entity, Relation: in.Relation, AsOf: asOf.Format(time.RFC3339)}
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
	// Relation narrows the walk to one edge relation. Absent follows all.
	Relation string `json:"relation,omitempty"`
	// Direction is out, in or both. Absent is out.
	Direction string `json:"direction,omitempty"`
	// Depth is how many hops. Absent is one.
	Depth int `json:"depth,omitempty"`
	// AsOf walks the graph as it stood at an instant, RFC 3339.
	AsOf string `json:"as_of,omitempty"`
}

type graphNeighborsOut struct {
	Entities []string `json:"entities"`
	Depth    int      `json:"depth"`
	// Truncated says the bound stopped the walk. The bound is part of the answer
	// rather than a silent short read.
	Truncated bool `json:"truncated"`
	Bound     int  `json:"bound"`
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

func (o ops) vocabulary(ctx context.Context, _ *struct{}) (*graphVocabularyOut, error) {
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
	ID         string  `json:"id"`
	Entity     string  `json:"entity"`
	Relation   string  `json:"relation"`
	Value      string  `json:"value"`
	Names      bool    `json:"names,omitempty"`
	At         string  `json:"at"`
	Seen       string  `json:"seen"`
	Knowable   string  `json:"knowable"`
	Source     string  `json:"source"`
	Evidence   string  `json:"evidence,omitempty"`
	By         string  `json:"by"`
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
