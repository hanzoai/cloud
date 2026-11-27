# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package graph

struct graphAnswerIn {
    Question text @0
    AsOf     text @8
    AsKnown  text @16
}

struct graphAnswerOut {
    AsOf        text        @0
    AsKnown     text        @8
    Grounded    bool        @16
    Answer      text        @24
    Findings    list<bytes> @32
    Communities list<i64>   @40
    Assertions  list<text>  @48
    Asked       i64         @56
    Skipped     i64         @64
    Clipped     bool        @72
    Calls       i64         @80
}

struct graphAssertIn {
    Assertions list<bytes> @0
}

struct graphAssertOut {
    Recorded  i64        @0
    Duplicate i64        @8
    Refused   i64        @16
    Reasons   list<text> @24
}

struct graphCommunitiesIn {
    AsOf     text @0
    AsKnown  text @8
    Level    i64  @16
    Relation text @24
}

struct graphCommunitiesOut {
    AsOf        text        @0
    AsKnown     text        @8
    Level       i64         @16
    Levels      i64         @24
    Modularity  f64         @32
    Entities    i64         @40
    Edges       i64         @48
    Ceiling     i64         @56
    Communities list<bytes> @64
    Truncated   bool        @72
    Bound       i64         @80
}

struct graphConvoIn {
    Source text        @0
    At     text        @8
    Turns  list<bytes> @16
    DryRun bool        @24
}

struct graphConvoOut {
    Assertions list<bytes> @0
    Recorded   i64         @8
    Duplicate  i64         @16
    Refused    i64         @24
    Reasons    list<text>  @32
}

struct graphDeriveIn {
    Predicates list<text> @0
    AsOf       text       @8
    AsKnown    text       @16
    Without    list<text> @24
    File       bool       @32
}

struct graphDeriveOut {
    AsOf      text        @0
    AsKnown   text        @8
    Derived   list<bytes> @16
    Lemmas    list<bytes> @24
    Lost      list<bytes> @32
    Gained    list<bytes> @40
    Truncated bool        @48
    Cut       text        @56
    Bound     i64         @64
    Steps     i64         @72
    Filed     bytes       @80
}

struct graphDiffIn {
    Entity    text @0
    From      text @8
    To        text @16
    FromKnown text @24
    ToKnown   text @32
    Limit     i64  @40
}

struct graphDiffOut {
    From       text        @0
    To         text        @8
    FromKnown  text        @16
    ToKnown    text        @24
    Asserted   list<bytes> @32
    Superseded list<bytes> @40
    Retracted  list<bytes> @48
    Truncated  bool        @56
    Bound      i64         @64
}

struct graphEntityIn {
    Entity  text @0
    AsOf    text @8
    AsKnown text @16
}

struct graphEntityOut {
    Entity     text        @0
    AsOf       text        @8
    AsKnown    text        @16
    Known      bool        @24
    Type       text        @32
    Properties list<bytes> @40
    Outgoing   list<bytes> @48
    Incoming   list<bytes> @56
    Scorecard  list<bytes> @64
    Expired    list<bytes> @72
    Truncated  bool        @80
}

struct graphEraseIn {
    Entity text @0
    Reason text @8
}

struct graphEraseOut {
    Entity    text        @0
    IDs       list<text>  @8
    Digest    text        @16
    Stores    list<bytes> @24
    Complete  bool        @32
    Remaining i64         @40
    Sources   list<text>  @48
    At        text        @56
    By        text        @64
}

struct graphExtractOut {
    Triples list<bytes> @0
    Refused list<bytes> @8
}

struct graphNeighborsIn {
    Seeds     list<text> @0
    Relation  text       @8
    Direction text       @16
    Depth     i64        @24
    AsOf      text       @32
    AsKnown   text       @40
}

struct graphNeighborsOut {
    Entities  list<text> @0
    Depth     i64        @8
    Truncated bool       @16
    Bound     i64        @24
}

struct graphOntologyIn {
    Group text @0
    Q     text @8
}

struct graphOntologyOut {
    Classes      list<bytes> @0
    Predicates   list<bytes> @8
    Features     list<bytes> @16
    ScoreColumns list<text>  @24
}

struct graphPathIn {
    From      text       @0
    To        text       @8
    Relations list<text> @16
    Direction text       @24
    AsOf      text       @32
    AsKnown   text       @40
}

struct graphPathOut {
    Found     bool        @0
    Hops      i64         @8
    Edges     list<bytes> @16
    Decay     f64         @24
    Weakest   bytes       @32
    AsOf      text        @40
    AsKnown   text        @48
    Truncated bool        @56
    Bound     i64         @64
}

struct graphReadIn {
    Entity   text @0
    Relation text @8
    Value    text @16
    AsOf     text @24
    AsKnown  text @32
    Limit    i64  @40
}

struct graphReadOut {
    Assertions list<bytes> @0
    Truncated  bool        @8
}

struct graphResolveIn {
    Entity   text @0
    Relation text @8
    AsOf     text @16
    AsKnown  text @24
}

struct graphResolveOut {
    Entity    text        @0
    Relation  text        @8
    AsOf      text        @16
    AsKnown   text        @24
    Truncated bool        @32
    Known     bool        @33
    Winner    bytes       @40
    Held      list<bytes> @48
    Conflicts list<bytes> @56
    Contested bool        @64
}

struct graphSearchIn {
    Q        text @0
    Relation text @8
    AsOf     text @16
    AsKnown  text @24
    Limit    i64  @32
}

struct graphSourceIn {
    Source  text @0
    Text    text @8
    At      text @16
    Subject text @24
}

struct graphVocabularyOut {
    Relations list<text>  @0
    Schema    list<bytes> @8
    Rule      list<text>  @16
    Bound     i64         @24
}

interface graph {
    # Answers a question from the whole graph and cites the assertions it
    # rests on.
    # It asks a model what each community's in-force facts say about the question,
    # then asks once more for one answer from those findings. Every cited ID is
    # checked to be a row of this graph, a failed model call fails the answer, and
    # the calls are billed to the calling organization.
    # Time: as_of and as_known place the graph that is read; either absent is now.
    graphAnswer(req: graphAnswerIn) returns (rep: graphAnswerOut)
    # Records a batch of assertions and counts what became of each.
    # Each member is judged alone: one refusal does not discard the rest, and a
    # member this plane already holds is a duplicate, not a refusal. A batch with
    # nothing admitted answers 400.
    # Time: at and until are when the statement was so, which an as_of read is
    # bounded by; the server stamps knowable, the later of seen and its own clock,
    # which an as_known read is bounded by. Nothing is overwritten: a correction is
    # the statement filed again, and the version known latest speaks for it.
    graphAssert(req: graphAssertIn) returns (rep: graphAssertOut)
    # Partitions the edge graph into sets of entities more densely
    # connected to each other than to the rest.
    # The graph is the one a walk reads, direction dropped, and the partition is
    # deterministic: the same graph at the same point numbers the same communities.
    # Time: as_of and as_known place the graph that is partitioned; either absent is
    # now.
    graphCommunities(req: graphCommunitiesIn) returns (rep: graphCommunitiesOut)
    # Extracts entity state and relationship assertions from conversation
    # turns and updates the graph.
    # First-person statements bind to the turn's speaker, second-person statements
    # to its partner, and named subjects to their entity keys. Questions are
    # skipped. Single-valued state updates (such as moving cities or changing
    # roles) supersede prior values at the turn's timestamp, and explicit
    # departures (such as leaving an employer) file a retraction ending the prior
    # claim. Set dry_run to inspect what the turns state without writing.
    # Time: each turn's at (or the batch's at when omitted) is the valid instant of
    # the assertions extracted from it, which an as_of read is bounded by; each
    # becomes knowable at the write, which an as_known read is bounded by.
    graphConvo(req: graphConvoIn) returns (rep: graphConvoOut)
    # Concludes what the organization's rules derive from the graph, with a
    # proof for every conclusion: the rule, and each support as the assertion in
    # force it rests on or as the derived atom it is.
    # The rules are assertions — (`rule:<name>`, `rule`, `<text>`), Datalog over
    # the organization's relations, with recursion, stratified negation and the
    # builtins = and != — and the graph is read the way resolve reads it, so a
    # retraction, an until and a declared cardinality all apply. Nothing derived is
    # stored unless an admin asks to file it. Without names assertions to derive
    # without, and the answer then says what would be lost, and what gained, if
    # they had never been filed.
    # Time: as_of and as_known place the graph and the rules it is read with, as
    # they place a resolve; either absent is now. Filing takes neither: a filed
    # conclusion holds from the instant it was derived.
    graphDerive(req: graphDeriveIn) returns (rep: graphDeriveOut)
    # Reports what came into force, was superseded and was retracted between
    # two points.
    # Each point is resolved as resolve would, never read off the rows in between.
    # A version that loses the order is invisible here as it is there, and one that
    # repeats what held is agreement, not change.
    # Time: a point is an as_of and an as_known instant, from and from_known against
    # to and to_known. Hold the known instants equal for what happened in the world;
    # hold the valid instants equal for what the record learned. to and to_known
    # default to now, from to to and from_known to to_known, and the two points must
    # differ.
    graphDiff(req: graphDiffIn) returns (rep: graphDiffOut)
    # Resolves the complete state of one entity at a point: its type, its
    # holding properties and edges in both directions, its kinship scorecard and
    # the expired statements it no longer holds.
    # Each (entity, relation) pair is settled by the same Resolve rule as
    # graphResolve. Statements begun by the point that lost to a retraction, an
    # until or a newer value are returned in expired rather than discarded.
    # Time: as_of is the instant of the world asked about and as_known how much this
    # plane had heard by then; either absent is now.
    graphEntity(req: graphEntityIn) returns (rep: graphEntityOut)
    # Removes every assertion that names an entity and returns a receipt.
    # It requires an admin of the organization it acts in; platform sudo is not
    # enough, so erasure is never cross-tenant. Nothing is removed while a matching
    # assertion is under litigation hold (409) or where the erasure cannot be
    # recorded (503). The audit trail keeps who, when, why, how many and the
    # receipt's digest, never the entity, and holds it before the erasure commits.
    # Time: erasure is the one act outside the two times. The rows go at every
    # as_of and as_known, so no past read recovers them.
    graphErase(req: graphEraseIn) returns (rep: graphEraseOut)
    # Reads the relations a source states and returns them, recording
    # nothing.
    # A line `relation:: value` states one; a value written `[[key]]` names an
    # entity and makes the relation an edge. The subject is the nearest heading
    # above the line, or the request's subject until a heading names one.
    # What filing would refuse is returned apart, in refused: a declaration from a
    # caller who is not an admin of the organization, and what the declared schema
    # refuses, checked against the store and against the types and declarations the
    # document itself states that the caller may file.
    # Time: it records nothing and takes no as_of or as_known; the schema is checked
    # as of now.
    graphExtract(req: graphSourceIn) returns (rep: graphExtractOut)
    # Reads a source and records what it states, through the same
    # admission as assert.
    # Each assertion's evidence is the section that stated it, `<source>#<section>`,
    # so the same source at the same at records one set of rows however often it is
    # delivered. What extract reports refused is not recorded; it is counted as
    # refused, with its reason, in the order the document states it. A source
    # stating no relation is refused whole.
    # Time: at is the valid time of every assertion recorded, which an as_of read is
    # bounded by; each becomes knowable at the write, so a read with an earlier
    # as_known does not see it.
    graphIngest(req: graphSourceIn) returns (rep: graphAssertOut)
    # Walks the in-force edges from a set of seeds and lists every
    # entity reached, bounded.
    # Only the edge that holds at the point is a hop: a superseded or retracted
    # edge is not, and neither is a property.
    # Time: as_of walks the graph as it stood at that instant and as_known as this
    # plane knew it then; either absent is now.
    graphNeighbors(req: graphNeighborsIn) returns (rep: graphNeighborsOut)
    # Lists the entity classes, predicates, classifier features and
    # kinship scorecard columns of the personal knowledge graph ontology.
    # Every code graph.db and entityTagging.db write on macOS and iOS is here with
    # its plain-language reading and the canonical relation key graphApple maps it
    # to. Filter by group or by substring q.
    # Time: it takes no as_of or as_known; the shipped catalog is static.
    graphOntology(req: graphOntologyIn) returns (rep: graphOntologyOut)
    # Finds the shortest chain of in-force edges from one entity to another.
    # It is the walk behind neighbors stopped at a goal: the same bound and the same
    # rule for which edge is in force. A causal chain is this op with relations
    # naming the relations that mean cause, such as caused_by and influenced.
    # Time: as_of and as_known place the graph the path is found in; either absent
    # is now.
    graphPath(req: graphPathIn) returns (rep: graphPathOut)
    # Lists the assertions recorded, every version, oldest first.
    # It resolves nothing: a superseded claim and the one that superseded it both
    # appear.
    # Time: as_of keeps statements begun by that instant of the world and as_known
    # keeps what this plane had heard by then; either absent is no bound.
    graphRead(req: graphReadIn) returns (rep: graphReadOut)
    # Answers what holds for one entity and relation at a point, winner
    # first, with every weaker account that disagreed.
    # The winner is the holding statement begun most recently, then the strongest
    # under the rule vocabulary names. A relation that holds many values answers
    # each; a disagreement is reported as contested, never resolved into silence.
    # Time: as_of is the instant of the world asked about and as_known how much this
    # plane had heard; either absent is now.
    graphResolve(req: graphResolveIn) returns (rep: graphResolveOut)
    # Finds assertions by their words, where read finds them by their keys,
    # best match first.
    # Every word must match, as a prefix. The answer is ordered by bm25, which
    # weighs a word by how rare it is and a row by how much of it the words are, so
    # a limit keeps the best matches rather than the oldest. It resolves nothing, so
    # a match may be a claim later corrected; ask resolve about what it finds.
    # Time: as_of and as_known bound it exactly as they bound read; either absent is
    # no bound.
    graphSearch(req: graphSearchIn) returns (rep: graphReadOut)
    # Lists the relations in use, the schema declared for them and the
    # rule that settles a conflict.
    # Time: it takes no as_of or as_known; the schema is the one in force now.
    graphVocabulary() returns (rep: graphVocabularyOut)
}

# ---------------------------------------------------------------------
# 17 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   graphApple  graphAppleIn.Scorecards  []graph.graphAppleScorecard  (no wire form)
#
# opaque (31) — crosses, arrives without its name:
#   graphAnswerOut.Findings  graph.graphFinding (list element)
#   graphAssertIn.Assertions  graph.graphFact (list element)
#   graphCommunitiesOut.Communities  graph.graphCommunity (list element)
#   graphConvoIn.Turns  graph.graphTurn (list element)
#   graphConvoOut.Assertions  graph.graphFact (list element)
#   graphDeriveOut.Derived  graph.graphConclusion (list element)
#   graphDeriveOut.Filed  graph.graphAssertOut
#   graphDeriveOut.Gained  graph.graphAtom (list element)
#   graphDeriveOut.Lemmas  graph.graphConclusion (list element)
#   graphDeriveOut.Lost  graph.graphAtom (list element)
#   graphDiffOut.Asserted  graph.wireFact (list element)
#   graphDiffOut.Retracted  graph.graphChange (list element)
#   graphDiffOut.Superseded  graph.graphChange (list element)
#   graphEntityOut.Expired  graph.wireFact (list element)
#   graphEntityOut.Incoming  graph.wireFact (list element)
#   graphEntityOut.Outgoing  graph.wireFact (list element)
#   graphEntityOut.Properties  graph.graphEntityProperty (list element)
#   graphEntityOut.Scorecard  graph.graphKinshipScore (list element)
#   graphEraseOut.Stores  graph.graphStore (list element)
#   graphExtractOut.Refused  graph.graphTriple (list element)
#   graphExtractOut.Triples  graph.graphTriple (list element)
#   graphOntologyOut.Classes  graph.graphOntologyClass (list element)
#   graphOntologyOut.Features  graph.graphOntologyFeature (list element)
#   graphOntologyOut.Predicates  graph.graphOntologyPredicate (list element)
#   graphPathOut.Edges  graph.wireFact (list element)
#   graphPathOut.Weakest  graph.wireFact
#   graphReadOut.Assertions  graph.wireFact (list element)
#   graphResolveOut.Conflicts  graph.wireFact (list element)
#   graphResolveOut.Held  graph.wireFact (list element)
#   graphResolveOut.Winner  graph.wireFact
#   graphVocabularyOut.Schema  graph.graphDeclared (list element)
