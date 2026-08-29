# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package benchmark

struct Preset {
    Name  text       @0
    Owner text       @8
    Arms  list<text> @16
    Rank  list<text> @24
    Panel i64        @32
    Note  text       @40
}

struct admission {
    Status     text       @0
    Model      text       @8
    Endpoint   text       @16
    Benchmarks list<text> @24
    Note       text       @32
}

struct benchmarkCatalog {
    Data  list<bytes> @0
    Total i64         @8
}

struct benchmarkQuery {
    Benchmark text @0
}

struct claimsIn {
    Benchmark text @0
    Model     text @8
    Provider  text @16
    Source    text @24
    Protocol  text @32
}

struct claimsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct compareQuery {
    Benchmark text @0
    A         text @8
    B         text @16
}

struct historyIn {
    Benchmark text @0
    Model     text @8
}

struct historyOut {
    Benchmark text        @0
    Data      list<bytes> @8
    Total     i64         @16
}

struct leaderboard {
    Benchmark text        @0
    Rows      list<bytes> @8
}

struct pairing {
    Benchmark    text @0
    A            text @8
    B            text @16
    NCommon      i64  @24
    ACorrect     i64  @32
    BCorrect     i64  @40
    RescueAOverB i64  @48
    RescueBOverA i64  @56
    NetAMinusB   i64  @64
    McnemarP     f64  @72
}

struct presetAccepted {
    Status   text  @0
    ServedAs text  @8
    Preset   bytes @16
    Note     text  @24
}

struct presetList {
    Data list<bytes> @0
}

struct putClaimsIn {
    Data list<bytes> @0
    By   text        @8
}

struct putClaimsOut {
    Recorded i64        @0
    Rejected list<text> @8
}

struct suite {
    Benchmarks list<text> @0
    Model      text       @8
    Endpoint   text       @16
    Attempts   i64        @24
}

interface benchmark {
    # Is the canonical public benchmarks this arena runs — the id, title, axis,
    # item count and upstream source of each, with native marking the ones the
    # standardized harness runs today; the rest are registered and adapter-pending.
    # These ids are the vocabulary the rest of the surface takes: a run names them, and
    # the leaderboard and compare read them from ?benchmark=. The catalog is
    # deployment-wide and identical for every caller — there is no tenant in it.
    get_benchmark_catalog() returns (rep: benchmarkCatalog)
    # Lists the effective published claims: what the leaderboard will use for
    # each (benchmark, model) after the seed, the import and any stored correction
    # are layered. It answers the operator's question — what does this arena
    # currently believe someone else reported, and did we ship that or fix it.
    # Effective values only. The history of a key lives in the append-only file and
    # is not what this op is for; a list that returned every superseded row would
    # make the common question the hard one.
    get_benchmark_claims(req: claimsIn) returns (rep: claimsOut)
    # Is the ONLY valid arm-vs-arm test: it pairs the two models on the items
    # BOTH completed, and answers rescue and damage counts with an exact-McNemar p.
    # Pairing is what prevents the subset artifact — comparing one model's easy subset
    # against another's full run — so n_common, not either arm's own coverage, is the
    # number to read this by.
    # Both a and b are required. The benchmark defaults to gpqa_diamond.
    get_benchmark_compare(req: compareQuery) returns (rep: pairing)
    # Returns each model's measured score per run over time, oldest first,
    # with the change between runs.
    # This is the counterweight to a leaderboard: the board shows the latest run
    # because that is what "how good is it" means, and a single latest number cannot
    # distinguish a model that has always been strong from one that just improved,
    # or from one that regressed after a provider changed something. Both matter for
    # routing, and only one of them is visible on a board.
    # Runs with no id — attempts recorded before runs existed — group under the
    # empty run, which is honestly what they are: one undated measurement.
    get_benchmark_history(req: historyIn) returns (rep: historyOut)
    # Answers one row per model for the benchmark named — what our own
    # harness measured, beside what the vendor claims, and the gap between them.
    # The gap is the point of the arena; provider-reported claims have run materially
    # hot against one standardized harness.
    # The two planes are NEVER blended, and that is the rule to read the rows by: a
    # model we have measured but no vendor has claimed for shows published null, a
    # model with only a claim shows measured null, and gap exists only where both do.
    # n is coverage and is not decoration: two measured numbers taken over different
    # item counts are not comparable, so read the row's n before reading its accuracy.
    get_benchmark_leaderboard(req: benchmarkQuery) returns (rep: leaderboard)
    # Are the router blends available to compose from — a named set of model
    # arms, the rank they escalate through and the panel width that bounds fan-out —
    # each served by the model layer as enso-<name>.
    # Today it answers exactly one row, the reference blend: a worked example written
    # in models we name, published as an example of the FORM. It is deliberately not
    # the composition of a Hanzo-served tier — the tier name exists to abstract that —
    # so fork it and swap arms by what the leaderboard measures on your own tasks
    # rather than reading it as a disclosure.
    get_benchmark_presets() returns (rep: presetList)
    # Records published claims: one to correct a number, many to import a
    # leaderboard. Every row must carry a Source, because a claim without its
    # citation is a number nobody can check — and an unattributed number in the
    # published plane is indistinguishable from a measurement, which is the one
    # confusion this whole surface is built to prevent.
    # Writes are append-only, so this never destroys the value it replaces. A
    # vendor restating a score leaves both rows on disk, which is how the restating
    # itself becomes visible.
    post_benchmark_claims(req: putClaimsIn) returns (rep: putClaimsOut)
    # Validates a router blend — its name, its arms, the rank they escalate
    # through and the panel fan-out width — and answers 202 with the preset and the
    # enso-<name> it would be served as.
    # It VALIDATES AND ECHOES: the definition is not persisted yet, so a preset
    # accepted here is not one the model layer will resolve. Treat the response as a
    # check on the blend, not a promise to serve it.
    # Defaults fill the shape rather than refusing it: an omitted rank becomes the arms
    # in declared order and a panel below 1 becomes 1. The one real invariant is that
    # rank may only name arms the blend declares — the same rule the model catalog
    # enforces — and a rank naming anything else is a 422 listing exactly which entries
    # were undeclared. A blend with no name or no arms is a 400.
    post_benchmark_presets(req: Preset) returns (rep: presetAccepted)
    # Admits and queues a benchmark run against a model or your own endpoint, and
    # answers 202 with the receipt.
    # It is an ADMISSION, not a result: the work is done by the harness afterwards and
    # the numbers appear on the leaderboard as it completes them.
    # Cost is bounded by the store rather than by a quota: attempts are append-only and
    # keyed by (benchmark, item, model), so an (item, model) pair already attempted is
    # skipped instead of re-spent, and re-queuing the same run is close to free.
    # Validation is up front and total — a request with neither model nor endpoint is a
    # 400, one with no benchmarks is a 400, and any benchmark id outside the catalog is
    # a 422 naming exactly which ids were unknown, so a typo never silently queues a
    # partial run.
    post_benchmark_runs(req: suite) returns (rep: admission)
}

# ---------------------------------------------------------------------
# 9 op(s) here. What follows is what this schema does not carry.
#
# opaque (7) — crosses, arrives without its name:
#   benchmarkCatalog.Data  benchmark.Benchmark (list element)
#   claimsOut.Data  benchmark.ClaimRow (list element)
#   historyOut.Data  benchmark.ModelHistory (list element)
#   leaderboard.Rows  benchmark.LeaderRow (list element)
#   presetAccepted.Preset  benchmark.Preset
#   presetList.Data  benchmark.Preset (list element)
#   putClaimsIn.Data  benchmark.publishedClaim (list element)
