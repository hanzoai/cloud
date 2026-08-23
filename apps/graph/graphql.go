// graphql.go is the graph's second door, and the only surface here that a
// caller can TRAVERSE in one request.
//
// The REST ops are each one question: read the assertions, walk the edges from a
// seed set, resolve what is in force. Composing them — "the entities this one
// points at, and for each of those what its owner resolves to" — costs a request
// per hop, and the caller has to hold the intermediate keys. That is the shape
// GraphQL exists for, and it is worth a door HERE and nowhere else in the fleet:
// this plane is the one whose data is actually a graph. Projecting the other
// 1,500 operations as GraphQL fields would publish a REST catalogue in another
// syntax — flat, edgeless, and worse than the REST it wrapped.
//
// IT ADDS NO WAY TO ASK ANYTHING. Every resolver below calls the SAME ops method
// the REST route calls, so the tenancy, the as-of bound, the traversal bounds and
// the conflict rule are the ones already written and tested; a second
// implementation of any of them is how two doors come to disagree about what an
// organization knows. A field that cannot be answered by an existing op is not
// added here — it is added as an op, and this door follows.
package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud/openapi"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/zap-proto/zip"
)

// sdl is the schema. It names four types because the store models four things:
// an assertion, the entity it is about, what resolution makes of a contested
// relation, and the vocabulary in use.
//
// Entity has no stored existence — "a node is NOT a row" (fact.go) — so it
// carries only its key and the questions askable about it. Asking for an entity
// nothing has been asserted about is not an error; it answers with empty lists,
// which is what "nothing has been said" looks like.
const sdl = `
schema { query: Query }

# The graph of one organization, as assertions.
type Query {
    # An entity by key. Never null: an entity is what has been said about it, so
    # one nobody has spoken of answers with empty lists rather than absent.
    entity(key: String!): Entity!

    # Assertions, filtered. The same read the REST GET serves.
    assertions(entity: String, relation: String, value: String, asOf: String, limit: Int): [Assertion!]!

    # Assertions found by their TEXT rather than by a key: words, matched as
    # prefixes, all of them required. It resolves nothing — what matches is what
    # was asserted, so ask resolve about what you find.
    search(q: String!, relation: String, asOf: String, limit: Int): [Assertion!]!

    # The relations in use and the rule that settles a conflict.
    vocabulary: Vocabulary!
}

# A thing in the organization's namespace, addressed by key.
type Entity {
    key: String!

    # What has been asserted of it, newest-knowable first.
    assertions(relation: String, asOf: String, limit: Int): [Assertion!]!

    # The entities its EDGES point at — assertions whose value names another
    # entity. This is the traversal, bounded by depth exactly as the REST walk is.
    edges(relation: String, direction: String, depth: Int, asOf: String): [Entity!]!

    # What is in force for one relation as of an instant, and what disagreed.
    resolve(relation: String!, asOf: String): Resolution!
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
    seen: String!
    knowable: String!
    source: String!
    evidence: String!
    by: String!
    confidence: Float!
}

# What resolution made of a relation, including what it ruled against.
type Resolution {
    entity: String!
    relation: String!
    asOf: String!
    known: Boolean!
    contested: Boolean!
    winner: Assertion
    conflicts: [Assertion!]!
}

type Vocabulary {
    relations: [String!]!
    rule: [String!]!
    bound: Int!
}
`

// query is the schema's root resolver. It holds the ops rather than the store,
// which is what keeps this door from reaching past the checks the ops make.
type query struct{ o ops }

func (q *query) Entity(args struct{ Key string }) *entity {
	return &entity{o: q.o, key: args.Key}
}

func (q *query) Assertions(ctx context.Context, args struct {
	Entity, Relation, Value, AsOf *string
	Limit                         *int32
}) ([]*assertion, error) {
	in := &graphReadIn{
		Entity:   str(args.Entity),
		Relation: str(args.Relation),
		Value:    str(args.Value),
		AsOf:     str(args.AsOf),
		Limit:    num(args.Limit),
	}
	out, err := q.o.read(ctx, in)
	if err != nil {
		return nil, err
	}
	return wrap(out.Assertions), nil
}

func (q *query) Search(ctx context.Context, args struct {
	Q              string
	Relation, AsOf *string
	Limit          *int32
}) ([]*assertion, error) {
	out, err := q.o.search(ctx, &graphSearchIn{
		Q:        args.Q,
		Relation: str(args.Relation),
		AsOf:     str(args.AsOf),
		Limit:    num(args.Limit),
	})
	if err != nil {
		return nil, err
	}
	return wrap(out.Assertions), nil
}

func (q *query) Vocabulary(ctx context.Context) (*vocabulary, error) {
	out, err := q.o.vocabulary(ctx, &struct{}{})
	if err != nil {
		return nil, err
	}
	return &vocabulary{out}, nil
}

// entity is a KEY plus the questions askable about it. It holds no loaded state:
// each field is its own op call, so asking for assertions and edges in one query
// is two reads with the same tenancy rather than one read reinterpreted twice.
type entity struct {
	o   ops
	key string
}

func (e *entity) Key() string { return e.key }

func (e *entity) Assertions(ctx context.Context, args struct {
	Relation, AsOf *string
	Limit          *int32
}) ([]*assertion, error) {
	out, err := e.o.read(ctx, &graphReadIn{
		Entity:   e.key,
		Relation: str(args.Relation),
		AsOf:     str(args.AsOf),
		Limit:    num(args.Limit),
	})
	if err != nil {
		return nil, err
	}
	return wrap(out.Assertions), nil
}

// Edges is the walk, and it is the reason this door exists. It answers ENTITIES
// rather than assertions because that is what a caller composes on: the next
// selection set asks its own questions of each one, which over REST is a request
// per hop plus the keys held in between.
func (e *entity) Edges(ctx context.Context, args struct {
	Relation, Direction, AsOf *string
	Depth                     *int32
}) ([]*entity, error) {
	out, err := e.o.neighbors(ctx, &graphNeighborsIn{
		Seeds:     []string{e.key},
		Relation:  str(args.Relation),
		Direction: str(args.Direction),
		Depth:     num(args.Depth),
		AsOf:      str(args.AsOf),
	})
	if err != nil {
		return nil, err
	}
	out2 := make([]*entity, 0, len(out.Entities))
	for _, k := range out.Entities {
		out2 = append(out2, &entity{o: e.o, key: k})
	}
	return out2, nil
}

func (e *entity) Resolve(ctx context.Context, args struct {
	Relation string
	AsOf     *string
}) (*resolution, error) {
	out, err := e.o.resolve(ctx, &graphResolveIn{
		Entity:   e.key,
		Relation: args.Relation,
		AsOf:     str(args.AsOf),
	})
	if err != nil {
		return nil, err
	}
	return &resolution{out}, nil
}

type assertion struct{ f wireFact }

func (a *assertion) ID() string          { return a.f.ID }
func (a *assertion) Entity() string      { return a.f.Entity }
func (a *assertion) Relation() string    { return a.f.Relation }
func (a *assertion) Value() string       { return a.f.Value }
func (a *assertion) Names() bool         { return a.f.Names }
func (a *assertion) At() string          { return a.f.At }
func (a *assertion) Seen() string        { return a.f.Seen }
func (a *assertion) Knowable() string    { return a.f.Knowable }
func (a *assertion) Source() string      { return a.f.Source }
func (a *assertion) Evidence() string    { return a.f.Evidence }
func (a *assertion) By() string          { return a.f.By }
func (a *assertion) Confidence() float64 { return a.f.Confidence }

type resolution struct{ r *graphResolveOut }

func (r *resolution) Entity() string   { return r.r.Entity }
func (r *resolution) Relation() string { return r.r.Relation }
func (r *resolution) AsOf() string     { return r.r.AsOf }
func (r *resolution) Known() bool      { return r.r.Known }
func (r *resolution) Contested() bool  { return r.r.Contested }
func (r *resolution) Winner() *assertion {
	if r.r.Winner == nil {
		return nil
	}
	return &assertion{*r.r.Winner}
}
func (r *resolution) Conflicts() []*assertion { return wrap(r.r.Conflicts) }

type vocabulary struct{ v *graphVocabularyOut }

func (v *vocabulary) Relations() []string { return v.v.Relations }
func (v *vocabulary) Rule() []string      { return v.v.Rule }
func (v *vocabulary) Bound() int32        { return int32(v.v.Bound) }

func wrap(fs []wireFact) []*assertion {
	out := make([]*assertion, 0, len(fs))
	for _, f := range fs {
		out = append(out, &assertion{f})
	}
	return out
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func num(p *int32) int {
	if p == nil {
		return 0
	}
	return int(*p)
}

// schemaOf parses the SDL against the resolvers ONCE, at mount. A schema whose
// resolvers do not satisfy it is a programming error rather than a request-time
// failure, so it is found when the process starts and not when a caller asks.
func schemaOf(o ops) (*graphql.Schema, error) {
	return graphql.ParseSchema(sdl, &query{o: o},
		graphql.UseFieldResolvers(),
		// A query that fans out over a large neighbourhood must not become an
		// unbounded number of op calls. The walk is bounded inside neighbors;
		// this bounds the SHAPE of the request that reaches it.
		graphql.MaxDepth(12),
	)
}

// serveGraphQL is the door. It answers 200 with a GraphQL error list for a query
// that cannot run, which is the wire every GraphQL client parses — a transport
// error would be read as the server being down rather than the query being
// wrong.
func serveGraphQL(sc *graphql.Schema) func(*zip.Ctx) error {
	return func(c *zip.Ctx) error {
		var req graphQLIn
		if err := json.Unmarshal(c.Body(), &req); err != nil {
			return c.JSON(400, map[string]any{
				"errors": []map[string]string{{"message": fmt.Sprintf("request is not JSON: %v", err)}},
			})
		}
		if req.Query == "" {
			return c.JSON(400, map[string]any{
				"errors": []map[string]string{{"message": "no query"}},
			})
		}
		// c.Context() carries the validated principal the ops read their tenant
		// from, so every resolver below is scoped exactly as the REST route is.
		// Variables arrives as raw JSON because a map[string]any publishes
		// `additionalProperties: {"type":"object"}`, which is FALSE — a variable
		// is routinely a string or a number. Decoded here, declared as open.
		var vars map[string]any
		if len(req.Variables) > 0 {
			if err := json.Unmarshal(req.Variables, &vars); err != nil {
				return c.JSON(400, map[string]any{
					"errors": []map[string]string{{"message": "variables is not a JSON object"}},
				})
			}
		}
		return c.JSON(200, sc.Exec(c.Context(), req.Query, req.OperationName, vars))
	}
}

// graphQLIn is the door's request, and it is DECLARED rather than typed: a typed
// op names one Out, and this route's output shape is whatever the query selected.
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
	// Path is the field the failure happened at, when it happened at one.
	Path []string `json:"path,omitempty"`
}

func init() {
	openapi.Register("/v1/graph/graphql", http.MethodPost, graphQLIn{}, graphQLOut{})
	openapi.Describe("/v1/graph/graphql", http.MethodPost,
		"Ask the graph in one request, traversing.",
		"Runs a GraphQL query against this organization's assertions.\n\n"+
			"It is the one door here a caller can TRAVERSE: the REST ops each answer a "+
			"single question, so composing them — the entities this one points at, and "+
			"what each of those resolves to — costs a request per hop with the "+
			"intermediate keys held by the caller. Here that is one query and the "+
			"nesting is the answer's shape.\n\n"+
			"It adds no way to ask anything new. Every field runs the SAME operation "+
			"the matching REST route runs, so the tenancy, the as-of bound, the "+
			"traversal bounds and the conflict rule are the ones already in force; the "+
			"schema is served by introspection.\n\n"+
			"A query that cannot run answers 200 with an `errors` list, which is the "+
			"wire every GraphQL client parses.")
}
