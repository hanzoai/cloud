package graph

// entity.go resolves the whole state of one entity at a point in time: its
// type, its in-force scalar properties (with any competing accounts that lost
// the order), its in-force outgoing and incoming edges, its kinship scorecard
// and the expired or superseded claims it used to hold.

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// graphEntityIn asks for the complete resolved state of one entity.
type graphEntityIn struct {
	// Entity is the entity key to resolve. Required.
	Entity string `json:"entity" validate:"required"`
	// AsOf is the instant of the world to answer about, RFC 3339: what held
	// then. Absent is now.
	AsOf string `json:"as_of,omitempty"`
	// AsKnown is how much this plane had heard, RFC 3339: the answer as it would
	// have been given then. Absent is now.
	AsKnown string `json:"as_known,omitempty"`
}

// graphEntityProperty is one scalar relation holding on an entity at a point.
type graphEntityProperty struct {
	// Relation is the scalar relation's key.
	Relation string `json:"relation"`
	// Value is the winning scalar value holding at the point.
	Value string `json:"value"`
	// Winner is the assertion that speaks for this relation at the point.
	Winner wireFact `json:"winner"`
	// Conflicts is every weaker account of the winner's moment that names a
	// different value, strongest first.
	Conflicts []wireFact `json:"conflicts,omitempty"`
	// Contested is true exactly when Conflicts is not empty.
	Contested bool `json:"contested"`
}

// graphKinshipScore is one column of a person's kinship or closeness scorecard.
type graphKinshipScore struct {
	// Relationship is the scorecard column, such as mother, father, partner,
	// family or familyAndFriends.
	Relationship string `json:"relationship"`
	// Score is the classifier probability in [0,1].
	Score float64 `json:"score"`
	// Assertion is the underlying scorecard assertion holding at the point.
	Assertion wireFact `json:"assertion"`
}

// graphEntityOut is everything holding for, pointing at, or formerly asserted
// about one entity at a point.
type graphEntityOut struct {
	// Entity is the folded entity key the question named.
	Entity string `json:"entity"`
	// AsOf is the valid instant this state was resolved at, RFC 3339.
	AsOf string `json:"as_of"`
	// AsKnown is the knowledge instant this state was resolved at, RFC 3339.
	AsKnown string `json:"as_known"`
	// Known is true when at least one property, outgoing edge, incoming edge or
	// expired statement was found for the entity.
	Known bool `json:"known"`
	// Type is the winning `type` of the entity at the point. Absent when no
	// type holds.
	Type string `json:"type,omitempty"`
	// Properties is every in-force scalar property of the entity at the point,
	// ordered by relation key.
	Properties []graphEntityProperty `json:"properties"`
	// Outgoing is every in-force edge asserted about the entity at the point,
	// ordered by relation key and then winner order.
	Outgoing []wireFact `json:"outgoing"`
	// Incoming is every in-force edge pointing at the entity at the point,
	// ordered by source entity and relation key.
	Incoming []wireFact `json:"incoming"`
	// Scorecard is the kinship and closeness scores holding on the entity,
	// highest score first.
	Scorecard []graphKinshipScore `json:"scorecard,omitempty"`
	// Expired is every statement about the entity that was known by AsKnown and
	// begun by AsOf but no longer holds at AsOf because its until passed, a
	// retraction ended it, or a later statement superseded it.
	Expired []wireFact `json:"expired"`
	// Truncated says the read of outgoing or incoming assertions hit the walk
	// ceiling.
	Truncated bool `json:"truncated,omitempty"`
}

// entity resolves the complete state of one entity at a point: its type, its
// holding properties and edges in both directions, its kinship scorecard and
// the expired statements it no longer holds.
//
// Each (entity, relation) pair is settled by the same Resolve rule as
// graphResolve. Statements begun by the point that lost to a retraction, an
// until or a newer value are returned in expired rather than discarded.
//
// Time: as_of is the instant of the world asked about and as_known how much this
// plane had heard by then; either absent is now.
//
// Example: {"entity": "acme/person/mara", "as_of": "2026-09-01T00:00:00Z"}
func (o ops) entity(ctx context.Context, in *graphEntityIn) (*graphEntityOut, error) {
	_, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	ent := key(in.Entity)
	if ent == "" {
		return nil, zip.ErrBadRequest("entity is required")
	}
	w, err := moment(in.AsOf, in.AsKnown)
	if err != nil {
		return nil, err
	}

	out := &graphEntityOut{
		Entity:     ent,
		AsOf:       w.valid.Format(time.RFC3339),
		AsKnown:    w.known.Format(time.RFC3339),
		Properties: []graphEntityProperty{},
		Outgoing:   []wireFact{},
		Incoming:   []wireFact{},
		Expired:    []wireFact{},
	}

	// 1. All assertions about this entity begun by w.valid and known by w.known.
	about, err := st.read(ctx, filter{Entity: ent, Valid: w.valid, Known: w.known, Newest: true})
	if err != nil {
		return nil, err
	}
	if len(about) == walkBound {
		out.Truncated = true
	}

	byRel := make(map[string][]Fact)
	var rels []string
	for _, f := range about {
		if _, seen := byRel[f.Relation]; !seen {
			rels = append(rels, f.Relation)
		}
		byRel[f.Relation] = append(byRel[f.Relation], f)
	}
	slices.Sort(rels)

	card, err := st.arity(ctx, rels, w.known)
	if err != nil {
		return nil, err
	}

	heldIDs := make(map[string]bool)
	var expired []Fact
	for _, rel := range rels {
		group := byRel[rel]
		one := card.one(group)
		held, conflicts := Resolve(group, w, one)
		for _, h := range held {
			heldIDs[h.ID] = true
		}
		for _, c := range conflicts {
			heldIDs[c.ID] = true
		}
		for _, f := range group {
			if !void(f) && !heldIDs[f.ID] {
				expired = append(expired, f)
			}
		}
		if len(held) == 0 {
			continue
		}

		// Separate edges from scalar properties and kinship scorecards.
		var edgeHeld []Fact
		var propHeld []Fact
		for _, h := range held {
			if h.Names {
				edgeHeld = append(edgeHeld, h)
			} else {
				propHeld = append(propHeld, h)
			}
		}
		if len(edgeHeld) > 0 {
			out.Outgoing = append(out.Outgoing, toWire(edgeHeld)...)
		}
		if len(propHeld) > 0 {
			winnerWire := toWire([]Fact{propHeld[0]})[0]
			if rel == "type" && out.Type == "" {
				out.Type = propHeld[0].Value
			}
			if after, ok := strings.CutPrefix(rel, "kinship:"); ok {
				score, err := strconv.ParseFloat(propHeld[0].Value, 64)
				if err != nil {
					score = propHeld[0].Confidence
				}
				out.Scorecard = append(out.Scorecard, graphKinshipScore{
					Relationship: after,
					Score:        score,
					Assertion:    winnerWire,
				})
			}
			out.Properties = append(out.Properties, graphEntityProperty{
				Relation:  rel,
				Value:     propHeld[0].Value,
				Winner:    winnerWire,
				Conflicts: toWire(conflicts),
				Contested: len(conflicts) > 0,
			})
		}
	}

	slices.SortFunc(out.Scorecard, func(a, b graphKinshipScore) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.Relationship, b.Relationship)
	})

	// Order expired statements newest At first so recent withdrawals lead.
	slices.SortFunc(expired, func(a, b Fact) int {
		if c := b.At.Compare(a.At); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	out.Expired = toWire(expired)

	// 2. Incoming edges: assertions pointing at this entity (Value == ent, Names == true).
	pointing, err := st.read(ctx, filter{Value: ent, Valid: w.valid, Known: w.known, Newest: true})
	if err != nil {
		return nil, err
	}
	if len(pointing) == walkBound {
		out.Truncated = true
	}

	type pair struct{ entity, relation string }
	inPairs := make(map[pair]bool)
	var pairList []pair
	for _, f := range pointing {
		if !f.Names {
			continue
		}
		p := pair{f.Entity, f.Relation}
		if !inPairs[p] {
			inPairs[p] = true
			pairList = append(pairList, p)
		}
	}
	slices.SortFunc(pairList, func(a, b pair) int {
		if c := cmp.Compare(a.entity, b.entity); c != 0 {
			return c
		}
		return cmp.Compare(a.relation, b.relation)
	})

	for _, p := range pairList {
		pairFacts, err := st.read(ctx, filter{Entity: p.entity, Relation: p.relation, Valid: w.valid, Known: w.known, Newest: true})
		if err != nil {
			return nil, err
		}
		pCard, err := st.arity(ctx, []string{p.relation}, w.known)
		if err != nil {
			return nil, err
		}
		held, _ := Resolve(pairFacts, w, pCard.one(pairFacts))
		for _, h := range held {
			if h.Names && h.Value == ent {
				out.Incoming = append(out.Incoming, toWire([]Fact{h})[0])
			}
		}
	}

	out.Known = len(out.Properties) > 0 || len(out.Outgoing) > 0 || len(out.Incoming) > 0 || len(out.Expired) > 0
	return out, nil
}
