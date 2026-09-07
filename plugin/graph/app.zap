# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package graph

struct graphAssertIn {
    Assertions list<bytes> @0
}

struct graphAssertOut {
    Recorded  i64        @0
    Duplicate i64        @8
    Refused   i64        @16
    Reasons   list<text> @24
}

struct graphExtractOut {
    Triples list<bytes> @0
}

struct graphNeighborsIn {
    Seeds     list<text> @0
    Relation  text       @8
    Direction text       @16
    Depth     i64        @24
    AsOf      text       @32
}

struct graphNeighborsOut {
    Entities  list<text> @0
    Depth     i64        @8
    Truncated bool       @16
    Bound     i64        @24
}

struct graphReadIn {
    Entity   text @0
    Relation text @8
    Value    text @16
    AsOf     text @24
    Limit    i64  @32
}

struct graphReadOut {
    Assertions list<bytes> @0
}

struct graphResolveIn {
    Entity   text @0
    Relation text @8
    AsOf     text @16
}

struct graphResolveOut {
    Entity    text        @0
    Relation  text        @8
    AsOf      text        @16
    Truncated bool        @24
    Known     bool        @25
    Winner    bytes       @32
    Conflicts list<bytes> @40
    Contested bool        @48
}

struct graphSearchIn {
    Q        text @0
    Relation text @8
    AsOf     text @16
    Limit    i64  @24
}

struct graphSourceIn {
    Source  text @0
    Text    text @8
    At      text @16
    Subject text @24
}

struct graphVocabularyOut {
    Relations list<text> @0
    Rule      list<text> @8
    Bound     i64        @16
}

interface graph {
    graphAssert(req: graphAssertIn) returns (rep: graphAssertOut)
    # Reads a source and returns the relations it states, recording
    # nothing. It is how a caller sees what a document would file before the plane —
    # which has no update and no delete — has anything filed into it.
    # A relation is stated as `relation:: value` on its own line; prose states none. A
    # value written `[[key]]` names another entity, which makes the assertion an edge.
    # The subject is the nearest heading above the line, or the request's `subject`
    # until a heading names one.
    graphExtract(req: graphSourceIn) returns (rep: graphExtractOut)
    # Reads a source and records what it states into the calling
    # organization's graph — the same store, the same admission and the same content
    # address as POST /v1/graph, because this operation ends by calling that one.
    # Every assertion carries the source it came from and, as its evidence, the
    # section that stated it: `<source>#<section>`. Delivering the same source at the
    # same `at` twice therefore records one set of rows and reports the rest as
    # duplicates, which is the property a retrying importer depends on.
    # A source that states no relation is refused rather than recorded as an empty
    # success: a caller that wrote its document in prose has been told nothing by a
    # 200 that filed nothing.
    graphIngest(req: graphSourceIn) returns (rep: graphAssertOut)
    graphNeighbors(req: graphNeighborsIn) returns (rep: graphNeighborsOut)
    graphRead(req: graphReadIn) returns (rep: graphReadOut)
    graphResolve(req: graphResolveIn) returns (rep: graphResolveOut)
    # Finds assertions by their text where read finds them by their keys.
    # It is the READ with one more term, not a second way to leave the store: same
    # order, same ceiling, same tenancy, and searching composes with narrowing by
    # relation and by instant because all of them are terms of one filter.
    # It resolves nothing. What matches is what was asserted, including claims that
    # were later corrected — which is the honest answer to "where is this mentioned"
    # and the reason the caller then asks resolve about what it found.
    graphSearch(req: graphSearchIn) returns (rep: graphReadOut)
    graphVocabulary() returns (rep: graphVocabularyOut)
}

# ---------------------------------------------------------------------
# 8 op(s) here. What follows is what this schema does not carry.
#
# opaque (5) — crosses, arrives without its name:
#   graphAssertIn.Assertions  graph.graphFact (list element)
#   graphExtractOut.Triples  graph.graphTriple (list element)
#   graphReadOut.Assertions  graph.wireFact (list element)
#   graphResolveOut.Conflicts  graph.wireFact (list element)
#   graphResolveOut.Winner  graph.wireFact
