package graph

// schema.go is the ontology, and it is assertions like everything else: there is
// no schema table, so a declaration has a source, evidence, an asserter and a
// knowable instant, and "what was the schema last Tuesday" is an as-of read.
//
// THREE RELATIONS AND ONE PREFIX are the whole of it:
//
//	(acme/person/z, "type", "person")               the entity's type
//	("relation:works_at", "domain", "person")       what works_at may be said OF
//	("relation:works_at", "range",  "company")      what works_at may point AT
//
// Each is resolved by the same order every other relation is, at the instant the
// checked assertion becomes knowable, so a re-declaration binds from the moment
// it is knowable and never reaches back over what was already admitted. One type
// is in force per entity and one per declaration, because resolution names one
// winner; a relation that must admit two types admits the type they share.
// Declaring the empty string reopens a relation.
//
// A relation nobody has declared is OPEN, which is how this plane behaved before
// any of this existed. The three relations are never checked themselves: they
// are the schema, and a schema checked against itself has no fixed point. Nor is
// a retraction (void): it states no value, so there is nothing to type, and a
// claim under a declared relation must stay withdrawable.
//
// A DECLARATION BINDS EVERY MEMBER, so only an admin of the organization may
// file one (admin): anything asserted of a `relation:` key, or of a `rule:` key
// (derive.go), is refused from anyone else, and each one filed is on the audit
// trail before it commits (file). An entity's `type` is an ordinary fact about
// an ordinary entity and stays open to every member.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/claim"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

const (
	typeKey     = "type"
	domainKey   = "domain"
	rangeKey    = "range"
	relationKey = "relation:"
)

// declares reports whether a relation is one of the schema's own.
func declares(relation string) bool {
	return relation == typeKey || relation == domainKey || relation == rangeKey || relation == cardinalityKey
}

// schematic reports whether an entity is a declaration — a relation's, a
// `relation:` key, which binds every assertion of that relation, or a rule, a
// `rule:` key, which binds every derivation.
func schematic(entity string) bool {
	return strings.HasPrefix(entity, relationKey) || strings.HasPrefix(entity, rulePrefix)
}

// cardinalityKey declares how many values a relation holds at once: (`relation:
// <name>`, `cardinality`, `one` | `many`). A term of office is many — a
// position has a holder per term — and an owner is one.
const cardinalityKey = "cardinality"

// cardinality is what the organization has declared of its relations' arity:
// relation → holds one value at a time. A relation it has not declared holds one
// when it is a property and many when it is an edge — an attribute has a value,
// a node has neighbours.
type cardinality map[string]bool

// one reports whether the pair these facts are about holds one value at a time.
func (c cardinality) one(facts []Fact) bool {
	if len(facts) == 0 {
		return true
	}
	if one, ok := c[facts[0].Relation]; ok {
		return one
	}
	for _, f := range facts {
		if !void(f) {
			return !f.Names
		}
	}
	return true
}

// arity reads the declared cardinality of relations as known at an instant: one
// indexed read per distinct relation, and nothing for a relation never declared.
func (s *store) arity(ctx context.Context, relations []string, known time.Time) (cardinality, error) {
	out := cardinality{}
	for _, r := range slices.Compact(slices.Sorted(slices.Values(relations))) {
		facts, err := s.read(ctx, filter{Entity: relationKey + r, Relation: cardinalityKey, Known: known, Newest: true})
		if err != nil {
			return nil, err
		}
		held, _ := Resolve(facts, when{valid: forever, known: known}, true)
		if len(held) == 1 {
			switch held[0].Value {
			case "one":
				out[r] = true
			case "many":
				out[r] = false
			}
		}
	}
	return out, nil
}

// relationsOf is the distinct relations facts speak of.
func relationsOf(facts []Fact) []string {
	out := make([]string, 0, len(facts))
	for _, f := range facts {
		out = append(out, f.Relation)
	}
	return slices.Compact(slices.Sorted(slices.Values(out)))
}

// pair is one (entity, relation): the unit resolution answers about.
type pair struct{ entity, relation string }

// conform says, for each fact, why the organization's declared schema refuses
// it, or "" when it conforms.
//
// The cost is one indexed read per DISTINCT pair the batch needs — two per
// relation used, one per entity whose type is asked — and never a scan. The
// schema the batch itself states counts, so a document that types an entity and
// relates it in one breath conforms in one breath.
func (s *store) conform(ctx context.Context, facts []Fact) ([]string, error) {
	stated := map[pair][]Fact{}
	var latest time.Time
	for _, f := range facts {
		if declares(f.Relation) {
			k := pair{f.Entity, f.Relation}
			stated[k] = append(stated[k], f)
		}
		if f.Knowable.After(latest) {
			latest = f.Knowable
		}
	}
	// A type and a declaration are read at forever, as known when the checked
	// fact was filed: they are about the organization's words, so a fact about
	// 1995 filed today is checked against today's vocabulary, not against what
	// the world was typed as in 1995 — when nothing may have been.
	held := map[pair][]Fact{}
	force := func(entity, relation string, at time.Time) (string, error) {
		k := pair{entity, relation}
		rows, ok := held[k]
		if !ok {
			var err error
			rows, err = s.read(ctx, filter{Entity: entity, Relation: relation, Known: latest, Newest: true})
			if err != nil {
				return "", err
			}
			rows = append(rows, stated[k]...)
			held[k] = rows
		}
		held, _ := Resolve(rows, when{valid: forever, known: at}, true)
		if len(held) == 0 {
			return "", nil
		}
		return held[0].Value, nil
	}

	why := make([]string, len(facts))
	for i, f := range facts {
		if declares(f.Relation) || void(f) {
			continue
		}
		decl := relationKey + f.Relation
		want, err := force(decl, domainKey, f.Knowable)
		if err != nil {
			return nil, err
		}
		if want != "" {
			got, err := force(f.Entity, typeKey, f.Knowable)
			if err != nil {
				return nil, err
			}
			if got != want {
				why[i] = refusal(f, domainKey, want, f.Entity, got)
				continue
			}
		}
		if want, err = force(decl, rangeKey, f.Knowable); err != nil {
			return nil, err
		}
		if want == "" {
			continue
		}
		if !f.Names {
			why[i] = fmt.Sprintf("%s %s %q: %s's range is %s, so its value names an entity, and this one is a scalar",
				f.Entity, f.Relation, f.Value, f.Relation, want)
			continue
		}
		got, err := force(f.Value, typeKey, f.Knowable)
		if err != nil {
			return nil, err
		}
		if got != want {
			why[i] = refusal(f, rangeKey, want, f.Value, got)
		}
	}
	// A rule is a declaration too, admitted only while the rules stay
	// evaluable (derive.go).
	return why, s.lawful(ctx, facts, why)
}

// judge says, for each fact, why this caller may not file it, or "" when it
// may: a declaration from anyone but an admin of the organization, then the
// declared schema over the rest. A declaration the caller may not file is not
// schema, so conform never counts it. forbidden reports a refusal for want of
// authority. Assert files by this and extract reports by it, so what a source
// is told it states is what filing it does.
func judge(ctx context.Context, st *store, facts []Fact) (why []string, forbidden bool, err error) {
	why = make([]string, len(facts))
	declarer := admin(ctx)
	ready := make([]Fact, 0, len(facts))
	at := make([]int, 0, len(facts))
	for i, f := range facts {
		if schematic(f.Entity) && !declarer {
			why[i] = fmt.Sprintf("%s is a declaration, which binds what every member may assert: "+
				"filing one requires an admin of the organization it acts in", f.Entity)
			forbidden = true
			continue
		}
		ready = append(ready, f)
		at = append(at, i)
	}
	bad, err := st.conform(ctx, ready)
	if err != nil {
		return nil, false, err
	}
	for j, w := range bad {
		why[at[j]] = w
	}
	return why, forbidden, nil
}

func refusal(f Fact, side, want, entity, got string) string {
	is := "has no type"
	if got != "" {
		is = "is " + got
	}
	return fmt.Sprintf("%s %s %s: %s's %s is %s, and %s %s",
		f.Entity, f.Relation, f.Value, f.Relation, side, want, entity, is)
}

// declarations is the schema in force now: every relation this organization has
// declared a domain or a range for. A relation reopened with the empty string is
// left out, because it constrains nothing.
func (s *store) declarations(ctx context.Context, now time.Time) ([]graphDeclared, error) {
	// The prefix is compared as text and not with LIKE, which folds ASCII case on
	// SQLite: `RELATION:x` declares nothing — conform reads `relation:x` and
	// nothing else — and anyone may file it, so listing it here would show a schema
	// nobody enforces and no admin wrote.
	rows, err := s.q.QueryContext(ctx, `SELECT DISTINCT entity FROM assertion
		WHERE relation IN ($1, $2, $3) AND substr(entity, 1, length($4)) = $4 ORDER BY entity LIMIT $5`,
		domainKey, rangeKey, cardinalityKey, relationKey, walkBound)
	if err != nil {
		return nil, err
	}
	var decls []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			_ = rows.Close()
			return nil, err
		}
		decls = append(decls, e)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []graphDeclared
	for _, e := range decls {
		d := graphDeclared{Relation: e[len(relationKey):]}
		for key, into := range map[string]*string{domainKey: &d.Domain, rangeKey: &d.Range, cardinalityKey: &d.Cardinality} {
			facts, err := s.read(ctx, filter{Entity: e, Relation: key, Known: now, Newest: true})
			if err != nil {
				return nil, err
			}
			if held, _ := Resolve(facts, when{valid: forever, known: now}, true); len(held) == 1 {
				*into = held[0].Value
			}
		}
		if d.Domain != "" || d.Range != "" || d.Cardinality != "" {
			out = append(out, d)
		}
	}
	return out, nil
}

// file is the one write a batch makes. Each member is judged on its own (sift),
// and what is kept is filed in the same transaction the verdicts were reached in
// (write). Before it commits, the audit trail records what account says of the
// rows the write made new: the IDs it names, whose digest is the record's
// resource, and what the record says of them. An account that names nothing
// records nothing and needs no trail. A trail that cannot take the record rolls
// the whole write back, and a deployment with no trail files nothing that needs
// one, as it makes no erasure. forbidden says a member was refused for want of
// authority.
func (o ops) file(ctx context.Context, sc scope, st *store, batch []graphFact, now time.Time, action string,
	account func(fresh []Fact) (ids []string, after map[string]any)) (out *graphAssertOut, forbidden bool, err error) {
	rec := o.s.State.audit
	c, _ := cloud.Request(ctx)
	var kept []Fact
	// asked says the trail was asked for a record; entry is the record, set once
	// the trail holds it.
	asked := false
	var entry *audit.Record
	n, err := st.write(ctx, func(st *store) ([]Fact, error) {
		var err error
		kept, out, forbidden, err = o.sift(ctx, sc, st, batch, now)
		return kept, err
	}, func(fresh []Fact) error {
		ids, after := account(fresh)
		if len(ids) == 0 {
			return nil
		}
		if rec == nil {
			return zip.Errorf(http.StatusServiceUnavailable,
				"no durable audit store is configured on this deployment; %s is recorded before it is filed", action)
		}
		asked = true
		slices.Sort(ids)
		r, err := act(c, sc.org, action, claim.Digest(ids...), now, after)
		if err != nil {
			return err
		}
		if _, err := rec.Append(ctx, r); err != nil {
			return err
		}
		entry = &r
		return nil
	})
	switch {
	case err != nil && asked:
		o.s.Log.Error("graph: a witnessed write failed; nothing was filed", "graph", sc.ns.String(), "action", action, "err", err)
		if entry != nil {
			// The trail took the write and the file did not.
			o.s.State.unmade(ctx, *entry, sc.ns.String())
		}
		return nil, false, zip.Errorf(http.StatusServiceUnavailable,
			"%s could not be recorded and filed, so nothing in the batch was filed; retry", action)
	case err != nil:
		return nil, false, err
	}
	out.Recorded, out.Duplicate = n, len(kept)-n
	return out, forbidden, nil
}

// declared is the trail's account of an assertion batch: the declarations —
// anything asserted of a `relation:` or `rule:` key — it made new, as
// graph.declare. A batch that only redelivers declarations already held records
// nothing, because it changed nothing.
func (sc scope) declared(fresh []Fact) ([]string, map[string]any) {
	var ids []string
	var said []map[string]string
	for _, f := range fresh {
		if schematic(f.Entity) {
			ids = append(ids, f.ID)
			said = append(said, map[string]string{"id": f.ID, "entity": f.Entity, "relation": f.Relation, "value": f.Value})
		}
	}
	return ids, map[string]any{"graph": sc.ns.String(), "declared": said}
}
