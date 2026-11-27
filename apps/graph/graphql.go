// graphql.go is the graph's second endpoint, and the only surface here that a
// caller can TRAVERSE in one request.
//
// The REST ops are each one question: read the assertions, walk the edges from a
// seed set, resolve what is in force. Composing them — "the entities this one
// points at, and for each of those what its owner resolves to" — costs a request
// per hop, and the caller has to hold the intermediate keys. That is the shape
// GraphQL exists for, and it is worth an endpoint HERE and nowhere else in the fleet:
// this plane is the one whose data is actually a graph. Projecting the other
// 1,500 operations as GraphQL fields would publish a REST catalogue in another
// syntax — flat, edgeless, and worse than the REST it wrapped.
//
// IT ADDS NO WAY TO ASK ANYTHING. Every field below calls the SAME ops method
// the REST route calls, so the tenancy, the as-of bound, the traversal bounds and
// the conflict rule are the ones already written and tested; a second
// implementation of any of them is how two endpoints come to disagree about what an
// organization knows. A field that cannot be answered by an existing op is not
// added here — it is added as an op, and this endpoint follows.
//
// AND IT IS NOT A GRAPHQL ENGINE. zip's is the one (zip.Group.MountGraphFor): it
// parses, gathers fragments and variables, bounds the request and writes the
// answer. What is here is the schema; a table of fields for the two types that
// take arguments, Query and Entity, each field an op call asked only when the
// request selects it; and Go types for the four that are values. Nesting is
// traversal because an Entity's `edges` answer more Entities, each asked in turn,
// and what that traversal may cost is bounded below (hopsMost, entityMost).

package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// graphQLPath is where the endpoint answers: GET renders the schema, POST runs a
// query against it.
const graphQLPath = "/v1/graph/graphql"

// sdl is the schema. It names five types because the store models five things:
// an assertion, the entity it is about, what resolution makes of a contested
// relation, the vocabulary in use, and what the organization declared of it.
//
// Entity has no stored existence — "a node is NOT a row" (fact.go) — so it
// carries only its key and the questions askable about it. Asking for an entity
// nothing has been asserted about is not an error; it answers with empty lists,
// which is what "nothing has been said" looks like.
//
// Every field here is a row in one of the tables below, and graphql_test.go holds
// the two to each other in both directions.
const sdl = `
schema { query: Query }

# The graph of one organization, as assertions.
type Query {
    # An entity by key. Never null: an entity is what has been said about it, so
    # one nobody has spoken of answers with empty lists rather than absent.
    entity(key: String!): Entity!

    # Assertions, filtered. The same read the REST GET serves.
    # asOf bounds by when the world was so, asKnown by what had been heard then.
    assertions(entity: String, relation: String, value: String, asOf: String, asKnown: String, limit: Int): [Assertion!]!

    # Assertions found by their TEXT rather than by a key: words, matched as
    # prefixes, all of them required, best match first. It resolves nothing —
    # what matches is what was asserted, so ask resolve about what you find.
    search(q: String!, relation: String, asOf: String, asKnown: String, limit: Int): [Assertion!]!

    # The relations in use and the rule that settles a conflict.
    vocabulary: Vocabulary!
}

# A thing in the organization's namespace, addressed by key.
type Entity {
    key: String!

    # What has been asserted of it, in the order it was written.
    assertions(relation: String, asOf: String, asKnown: String, limit: Int): [Assertion!]!

    # The entities its EDGES point at — assertions whose value names another
    # entity. This is the traversal, bounded by depth exactly as the REST walk is.
    edges(relation: String, direction: String, depth: Int, asOf: String, asKnown: String): [Entity!]!

    # What holds for one relation at an instant of the world, as known at
    # another: every statement in force, and whether a property is contested.
    resolve(relation: String!, asOf: String, asKnown: String): Resolution!
}

# One assertion: somebody, at some moment, from some evidence, said this.
type Assertion {
    id: String!
    entity: String!
    relation: String!
    value: String!
    # True when value names another entity — the assertion is an edge.
    names: Boolean!
    at: String!
    # When the thing stopped being so; null while it is open.
    until: String
    seen: String!
    knowable: String!
    source: String!
    evidence: String!
    by: String!
    confidence: Float!
}

# What resolution made of a relation: everything holding, winner first.
type Resolution {
    entity: String!
    relation: String!
    asOf: String!
    asKnown: String!
    known: Boolean!
    contested: Boolean!
    winner: Assertion
    held: [Assertion!]!
    # For a relation holding one value, the weaker accounts of the winner's
    # moment that name another.
    conflicts: [Assertion!]!
}

type Vocabulary {
    relations: [String!]!
    # What the organization has declared a domain or range for. Absent is open.
    schema: [Declaration!]!
    rule: [String!]!
    bound: Int!
}

# One relation's declared schema: the type its entity and its value must have.
type Declaration {
    relation: String!
    domain: String!
    range: String!
}
`

// What one request may cost, in the three ways a traversal can grow. `edges` is
// the schema's only recursion — every other field reads a value already in hand —
// so a request's cost is how deep its `edges` nest and how many entities they
// answer, and both are bounded here, at the field that spends them:
//
//   - hopsMost: `edges` may nest three deep along any path. That is checked on the
//     whole selection before any op runs, because zip hands the root field all of
//     it; past three a request's size is the graph's degree to the fourth power,
//     which no caller means and any caller can type.
//   - entityMost: one root field's `edges` answer at most 1000 entities between
//     them. Each is a node a further `edges` may walk from, so this bounds the
//     walks too; past it the field that would spend more is refused, and no
//     further walk starts. One walk is bounded inside neighbors (walkBound), as
//     over REST.
//
// zip's own budget (fieldLimit resolutions per request) stands behind both.
const (
	hopsMost   = 3
	entityMost = 1000
)

// mountGraphQL serves the schema and its answers at graphQLPath, on the
// /v1/graph group so the group's middleware stands in front of it as it does of
// every op here.
func mountGraphQL(g *zip.Group, o ops) {
	g.MountGraphFor(strings.TrimPrefix(graphQLPath, "/v1/graph"),
		func(context.Context) (string, error) { return sdl, nil },
		o.graphql)
}

// graphql answers one root field. The context carries the validated principal
// the ops read their tenant from, so every field below is scoped exactly as the
// REST route is.
func (o ops) graphql(ctx context.Context, kind string, s zip.Selection) (any, error) {
	if kind != "query" {
		return nil, zip.ErrBadRequest("no mutations are offered by the schema")
	}
	if n := hops(s); n > hopsMost {
		return nil, zip.ErrBadRequest(fmt.Sprintf(
			"%q nests edges %d deep; a request may nest them %d deep", s.Key(), n, hopsMost))
	}
	return query.answer(ctx, "Query", o, s)
}

// hops is how deep `edges` nest under a selection, along its deepest path.
func hops(s zip.Selection) int {
	most := 0
	for _, f := range s.Fields {
		if n := hops(f); n > most {
			most = n
		}
	}
	if s.Name == "edges" {
		most++
	}
	return most
}

// table is one type's fields: each one the schema publishes on it, and how it
// answers. A field a request names that the table lacks is refused by name. Only
// the two types with arguments or traversal need one — Query and Entity; the
// rest are values (assertion, resolution, vocabulary, declaration), projected by
// zip against their Go types.
type table[T any] map[string]func(ctx context.Context, v T, s zip.Selection) (any, error)

func (t table[T]) answer(ctx context.Context, typ string, v T, s zip.Selection) (any, error) {
	f, ok := t[s.Name]
	if !ok {
		return nil, zip.ErrBadRequest(fmt.Sprintf("cannot query field %q on type %q", s.Name, typ))
	}
	return f(ctx, v, s)
}

var query = table[ops]{
	"entity": func(_ context.Context, o ops, s zip.Selection) (any, error) {
		var in struct {
			Key string `json:"key" validate:"required"`
		}
		if err := s.Bind(&in); err != nil {
			return nil, err
		}
		// Answered under the key the ops fold every lookup to, so the entity a
		// caller selected is echoed as the one each of its fields read. The
		// tree below it shares one count of the entities its edges answer.
		return &entity{o: o, key: key(in.Key), spent: new(atomic.Int64)}, nil
	},
	"assertions": func(ctx context.Context, o ops, s zip.Selection) (any, error) {
		var in struct {
			Entity   string `json:"entity"`
			Relation string `json:"relation"`
			Value    string `json:"value"`
			Limit    int    `json:"limit"`
			AsOf     string `json:"asOf"`
			AsKnown  string `json:"asKnown"`
		}
		if err := s.Bind(&in); err != nil {
			return nil, err
		}
		out, err := o.read(ctx, &graphReadIn{
			Entity: in.Entity, Relation: in.Relation, Value: in.Value,
			AsOf: in.AsOf, AsKnown: in.AsKnown, Limit: in.Limit,
		})
		if err != nil {
			return nil, err
		}
		return wrap(out.Assertions), nil
	},
	"search": func(ctx context.Context, o ops, s zip.Selection) (any, error) {
		var in struct {
			Q        string `json:"q" validate:"required"`
			Relation string `json:"relation"`
			Limit    int    `json:"limit"`
			AsOf     string `json:"asOf"`
			AsKnown  string `json:"asKnown"`
		}
		if err := s.Bind(&in); err != nil {
			return nil, err
		}
		out, err := o.search(ctx, &graphSearchIn{
			Q: in.Q, Relation: in.Relation, AsOf: in.AsOf, AsKnown: in.AsKnown, Limit: in.Limit,
		})
		if err != nil {
			return nil, err
		}
		return wrap(out.Assertions), nil
	},
	"vocabulary": func(ctx context.Context, o ops, s zip.Selection) (any, error) {
		if err := s.Bind(&struct{}{}); err != nil {
			return nil, err
		}
		out, err := o.vocabulary(ctx, &cloud.Unit{})
		if err != nil {
			return nil, err
		}
		decls := make([]declaration, 0, len(out.Schema))
		for _, d := range out.Schema {
			decls = append(decls, declaration{Relation: d.Relation, Domain: d.Domain, Range: d.Range})
		}
		return &vocabulary{Relations: nonNil(out.Relations), Schema: decls, Rule: nonNil(out.Rule), Bound: out.Bound}, nil
	},
}

// entity is a KEY plus the questions askable about it. It holds no loaded state:
// each field is its own op call, so asking for assertions and edges in one query
// is two reads with the same tenancy rather than one read reinterpreted twice.
// spent is the count of entities its root field's edges have answered.
type entity struct {
	o     ops
	key   string
	spent *atomic.Int64
}

func (e *entity) Type() string { return "Entity" }

func (e *entity) Field(ctx context.Context, s zip.Selection) (any, error) {
	return entities.answer(ctx, "Entity", e, s)
}

var entities = table[*entity]{
	"key": func(_ context.Context, e *entity, s zip.Selection) (any, error) {
		if err := s.Bind(&struct{}{}); err != nil {
			return nil, err
		}
		return e.key, nil
	},
	"assertions": func(ctx context.Context, e *entity, s zip.Selection) (any, error) {
		var in struct {
			Relation string `json:"relation"`
			Limit    int    `json:"limit"`
			AsOf     string `json:"asOf"`
			AsKnown  string `json:"asKnown"`
		}
		if err := s.Bind(&in); err != nil {
			return nil, err
		}
		out, err := e.o.read(ctx, &graphReadIn{
			Entity: e.key, Relation: in.Relation, AsOf: in.AsOf, AsKnown: in.AsKnown, Limit: in.Limit,
		})
		if err != nil {
			return nil, err
		}
		return wrap(out.Assertions), nil
	},
	// edges is the walk, and it is the reason this endpoint exists. It answers
	// ENTITIES rather than assertions because that is what a caller composes on:
	// the next selection asks its own questions of each one, which over REST is a
	// request per hop plus the keys held in between.
	"edges": func(ctx context.Context, e *entity, s zip.Selection) (any, error) {
		var in struct {
			Relation  string `json:"relation"`
			Direction string `json:"direction"`
			Depth     int    `json:"depth"`
			AsOf      string `json:"asOf"`
			AsKnown   string `json:"asKnown"`
		}
		if err := s.Bind(&in); err != nil {
			return nil, err
		}
		if e.spent.Load() >= entityMost {
			return nil, past()
		}
		out, err := e.o.neighbors(ctx, &graphNeighborsIn{
			Seeds: []string{e.key}, Relation: in.Relation, Direction: in.Direction, Depth: in.Depth,
			AsOf: in.AsOf, AsKnown: in.AsKnown,
		})
		if err != nil {
			return nil, err
		}
		if e.spent.Add(int64(len(out.Entities))) > entityMost {
			return nil, past()
		}
		next := make([]*entity, 0, len(out.Entities))
		for _, k := range out.Entities {
			next = append(next, &entity{o: e.o, key: k, spent: e.spent})
		}
		return next, nil
	},
	"resolve": func(ctx context.Context, e *entity, s zip.Selection) (any, error) {
		var in struct {
			Relation string `json:"relation" validate:"required"`
			AsOf     string `json:"asOf"`
			AsKnown  string `json:"asKnown"`
		}
		if err := s.Bind(&in); err != nil {
			return nil, err
		}
		out, err := e.o.resolve(ctx, &graphResolveIn{
			Entity: e.key, Relation: in.Relation, AsOf: in.AsOf, AsKnown: in.AsKnown,
		})
		if err != nil {
			return nil, err
		}
		r := &resolution{
			Entity: out.Entity, Relation: out.Relation, AsOf: out.AsOf, AsKnown: out.AsKnown,
			Known: out.Known, Contested: out.Contested,
			Held: wrap(out.Held), Conflicts: wrap(out.Conflicts),
		}
		if out.Winner != nil {
			w := fact(*out.Winner)
			r.Winner = &w
		}
		return r, nil
	},
}

// past is the refusal of a request whose edges have answered entityMost
// entities: a 400 the caller reads, saying how to ask for less.
func past() error {
	return zip.ErrBadRequest(fmt.Sprintf(
		"the edges under this entity reach more than %d entities; narrow them by relation, "+
			"direction or depth, or walk the rest with POST /v1/graph/neighbors", entityMost))
}

// The four VALUE types. Each is one answer already in hand, so zip projects it
// against these Go types: the JSON names are the schema's, a field the type does
// not have is named as an error, and __typename is the type's name. Being values
// rather than zip.Objects, reading them costs the request's budget nothing.

// assertion is one assertion: somebody, at some moment, from some evidence, said this.
type assertion struct {
	ID         string  `json:"id"`
	Entity     string  `json:"entity"`
	Relation   string  `json:"relation"`
	Value      string  `json:"value"`
	Names      bool    `json:"names"`
	At         string  `json:"at"`
	Until      *string `json:"until"`
	Seen       string  `json:"seen"`
	Knowable   string  `json:"knowable"`
	Source     string  `json:"source"`
	Evidence   string  `json:"evidence"`
	By         string  `json:"by"`
	Confidence float64 `json:"confidence"`
}

// resolution is what resolution made of a relation: everything holding, winner first.
type resolution struct {
	Entity    string      `json:"entity"`
	Relation  string      `json:"relation"`
	AsOf      string      `json:"asOf"`
	AsKnown   string      `json:"asKnown"`
	Known     bool        `json:"known"`
	Contested bool        `json:"contested"`
	Winner    *assertion  `json:"winner"`
	Held      []assertion `json:"held"`
	Conflicts []assertion `json:"conflicts"`
}

// vocabulary is the relations in use and the rule that settles a conflict.
type vocabulary struct {
	Relations []string      `json:"relations"`
	Schema    []declaration `json:"schema"`
	Rule      []string      `json:"rule"`
	Bound     int           `json:"bound"`
}

// declaration is one relation's declared schema.
type declaration struct {
	Relation string `json:"relation"`
	Domain   string `json:"domain"`
	Range    string `json:"range"`
}

// fact is one stored assertion as the schema spells it; `until` is null while
// the thing is still so.
func fact(f wireFact) assertion {
	a := assertion{
		ID: f.ID, Entity: f.Entity, Relation: f.Relation, Value: f.Value, Names: f.Names, At: f.At,
		Seen: f.Seen, Knowable: f.Knowable, Source: f.Source, Evidence: f.Evidence, By: f.By,
		Confidence: f.Confidence,
	}
	if f.Until != "" {
		until := f.Until
		a.Until = &until
	}
	return a
}

// wrap answers a list of assertions, empty rather than null when there are none:
// the schema says every list here is non-null.
func wrap(fs []wireFact) []assertion {
	out := make([]assertion, 0, len(fs))
	for _, f := range fs {
		out = append(out, fact(f))
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// graphQLIn is the endpoint's request, and it is DECLARED rather than typed: a typed op names one Out, and this route's output shape is whatever the query selected.
type graphQLIn struct {
	// Query is the GraphQL document to run against the schema above.
	Query string `json:"query"`
	// Variables binds the document's declared variables. Raw JSON because the
	// values are the caller's own types — a string, a number, a list — and a
	// map[string]any would publish a schema saying each one is an object.
	Variables json.RawMessage `json:"variables,omitempty"`
	// OperationName selects one operation when the document carries several.
	OperationName string `json:"operationName,omitempty"`
}

// graphQLOut is the GraphQL envelope. `data` is deliberately open: its shape IS
// the query's selection set, so any schema naming its fields would describe one
// caller's question and mis-describe every other.
type graphQLOut struct {
	// Data is the answer, shaped by the selection set the caller sent.
	Data json.RawMessage `json:"data,omitempty"`
	// Errors is what could not be answered. A GraphQL client reads this out of a
	// 200 — a query that names an unknown field is a bad QUERY, not a broken
	// server, and answering a transport error would say the wrong thing.
	Errors []graphQLError `json:"errors,omitempty"`
}

// graphQLError is one entry of that list.
type graphQLError struct {
	// Message says what could not be answered.
	Message string `json:"message"`
	// Path is the field the failure happened at, when it happened at one: field
	// names, and the index of a list member.
	Path []any `json:"path,omitempty"`
}

func init() {
	spec.Register(graphQLPath, http.MethodPost, graphQLIn{}, graphQLOut{})
	spec.Describe(graphQLPath, http.MethodPost,
		"Ask the graph in one request, traversing.",
		"Runs a GraphQL query against this organization's assertions.\n\n"+
			"It is the one endpoint here a caller can TRAVERSE: the REST ops each answer a "+
			"single question, so composing them — the entities this one points at, and "+
			"what each of those resolves to — costs a request per hop with the "+
			"intermediate keys held by the caller. Here that is one query and the "+
			"nesting is the answer's shape.\n\n"+
			"It adds no way to ask anything new. Every field runs the SAME operation "+
			"the matching REST route runs, so the tenancy, the as-of bound, the "+
			"traversal bounds and the conflict rule are the ones already in force; the "+
			"schema is served by GET at this same address, and introspection is not.\n\n"+
			"A query that ran answers 200 with each failure in an `errors` list beside "+
			"the data, which is the wire every GraphQL client parses; one that could "+
			"not begin — it does not parse, or names a fragment it does not define — "+
			"answers 400 with the reason and no data. `edges` nest at most three deep, refused "+
			"before any operation runs, and one root field's edges answer at most 1000 entities.")
	spec.Register(graphQLPath, http.MethodGet, nil, spec.Bytes{Type: "text/plain; charset=utf-8"})
	spec.Describe(graphQLPath, http.MethodGet,
		"Read the graph's GraphQL schema.",
		"Returns the schema POST at this address runs queries against, as GraphQL SDL: "+
			"the five types the store models and the fields askable of each. It is what a "+
			"code generator or an editor reads, and it is the same for every organization; "+
			"what a query answers is scoped to the caller's.")
}
