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
    At    bytes      @48
    By    text       @56
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
    Org       text @40
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

struct importIn {
    Benchmark text        @0
    Model     text        @8
    Run       text        @16
    At        text        @24
    Data      list<bytes> @32
}

struct importOut {
    Imported  i64  @0
    Benchmark text @8
    Model     text @16
    Run       text @24
    Score     f64  @32
    Claim     f64  @40
    Source    text @48
}

struct leaderboard {
    Benchmark text        @0
    Direction text        @8
    Rows      list<bytes> @16
}

struct measurements {
    Org   text        @0
    By    text        @8
    Model text        @16
    Data  list<bytes> @24
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

struct presetList {
    Data list<bytes> @0
}

struct presetStored {
    ServedAs text  @0
    Preset   bytes @8
    Note     text  @16
}

struct promoteIn {
    Org       text @0
    Benchmark text @8
    Model     text @16
    Source    text @24
    Protocol  text @32
}

struct promoteOut {
    Promoted  i64         @0
    Unchanged i64         @8
    From      text        @16
    Rejected  list<text>  @24
    Data      list<bytes> @32
}

struct putClaimsIn {
    Data       list<bytes> @0
    Visibility text        @8
}

struct putClaimsOut {
    Recorded   i64        @0
    Rejected   list<text> @8
    Org        text       @16
    Visibility text       @24
}

struct suite {
    Benchmarks list<text> @0
    Model      text       @8
}

interface benchmark {
    # Is the canonical benchmarks this arena reads — the id, title, axis, item
    # count, upstream source and direction of each, with native marking the ones the
    # standardized harness runs today. The first 14 are the public set every major
    # provider reports; the next 4 are the tests coding agents are judged on —
    # DeepSWE, Terminal-Bench 4.0, SWE-Atlas QnA and SWE-Bench Pro v2 — published as
    # claims; the rest are the Decision Suite, scored by Kai's own harness and
    # published as claims citing the research run each number came from.
    # direction is "higher" or "lower": which way a score improves, and so which way
    # the leaderboard orders its rows. Every score is a percentage; a latency is a
    # fact about serving and lives in the research plane's runs, not here.
    # These ids are the vocabulary the rest of the surface takes: a run and a claim
    # name them, and the leaderboard and compare read them from ?benchmark=. The
    # catalog is deployment-wide and identical for every caller — there is no tenant
    # in it.
    get_benchmark_catalog() returns (rep: benchmarkCatalog)
    # Lists the effective claims the caller may read, each labelled with the
    # org that made it and the user who recorded it.
    # Anyone reads the public claims. A signed-in caller also reads its own org's
    # private ones. No caller reads another org's private claims — a SuperAdmin
    # acting in that org included, since acting in an org is not membership of it.
    # Rows under org "admin" are the platform's own curated readings: the compiled
    # seed and any correction a SuperAdmin wrote.
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
    # Published, Claims, Spread and Mean read the platform's own public claims (org
    # "admin") and nothing else, whoever asks. Other orgs' claims are at
    # /v1/benchmark/claims, labelled, and reach this board only when a SuperAdmin
    # publishes them: writing under org admin, or promoting an org's public claims
    # with POST /v1/benchmark/claims/promote, which keeps the org they came from in
    # the row's from.
    # Rows are best first. A benchmark whose score improves downward — a false-alarm
    # rate, a share of answers that moved — is ordered lowest first, and the answer
    # says which with direction.
    # n is coverage and is not decoration: two measured numbers taken over different
    # item counts are not comparable, so read the row's n before reading its accuracy.
    get_benchmark_leaderboard(req: benchmarkQuery) returns (rep: leaderboard)
    # Are the router blends available to compose from — a named set of model
    # arms, the rank they escalate through and the panel width that bounds fan-out —
    # each named for the model layer as enso-<name>.
    # The first row is the reference blend: a worked example written in models we
    # name, published as an example of the FORM. It is deliberately not the
    # composition of a Hanzo-served tier — the tier name exists to abstract that — so
    # fork it and swap arms by what the leaderboard measures on your own tasks rather
    # than reading it as a disclosure.
    # The rows after it are the caller's org's stored blends, by name, the latest
    # version of each. Anyone reads the reference; only a member of the org reads the
    # org's own, and a SuperAdmin acting in an org it does not belong to reads what a
    # stranger reads.
    get_benchmark_presets() returns (rep: presetList)
    # Stores the per-item rows of a published run, so the board's
    # measured column, history and compare read the items behind a number the
    # platform already publishes.
    # Only a SuperAdmin acting in org admin may import; anyone else is refused 403,
    # and the refusal is audited.
    # The rows are FOR A CLAIM: the platform must publish a claim for the model on
    # the benchmark whose source names the run (a research run URL whose id is run),
    # and the rows must score that claim to within 0.05 points, or the import is
    # refused 422 saying which. A run is imported once (409 after), every item id is
    # unique and carries an answer, and a lower-is-better benchmark is refused 422:
    # its score is a rate the board does not read as accuracy.
    # The trail takes the import's intent, naming the run and its counts, before the
    # first row lands.
    post_benchmark_attempts(req: importIn) returns (rep: importOut)
    # Records claims for the caller's org: one to correct a number, many to
    # import a leaderboard. Any signed-in caller may write; a caller with no verified
    # principal is refused 401.
    # The org and the author are the verified caller's. Nothing in the body names
    # either, and a body that tries is not read. Claims are private to that org unless
    # visibility is "public". Writing as org "admin" curates the leaderboard, so it
    # takes a SuperAdmin; anyone else acting there is refused 403, and the refusal is
    # audited.
    # Every row must carry a Source, because a claim without its citation is a
    # number nobody can check, and a benchmark id from /catalog, because an unknown
    # id would sit in the store invisible to every read. A row names its benchmark,
    # model, provider and protocol in at most 128 characters each, cites a source of
    # at most 2048 bytes, and scores a percentage from 0 to 100; a row outside that is
    # rejected by number.
    # A request carries at most 500 rows in at most 1 MiB (413 past either), and an
    # org writes at most 2000 rows per UTC day (429 past that, and nothing from the
    # request is written).
    # The trail takes the call's intent, naming every row, BEFORE the first row
    # lands; a trail that cannot take it answers 503 and nothing is written. A second
    # record then names which rows were stored and which failed.
    # Writes are append-only, so this never destroys the value it replaces. A
    # vendor restating a score leaves both rows on disk, which is how the restating
    # itself becomes visible.
    post_benchmark_claims(req: putClaimsIn) returns (rep: putClaimsOut)
    # Publishes an org's public claims onto the leaderboard: each one that
    # matches is copied, as it stands, into the platform's own claims, with the org it
    # came from kept in from.
    # Publication is a SuperAdmin act. The board is the platform's reading, so an
    # org's claim — public or not — never reaches it on its own; this is how one
    # does, on purpose and on the record. Only a SuperAdmin acting in org admin may
    # call it. Anyone else is refused 403, a SuperAdmin acting in another org
    # included, and the refusal is audited.
    # It reads only the org's PUBLIC claims, the same rows anyone reads at
    # /v1/benchmark/claims?org=; a private claim is not the platform's to publish. The
    # copy keeps the benchmark, provider, model, score, protocol and source, so the
    # published number is the filed number and the citation still names the run it
    # came from. A claim already published exactly as it stands is counted unchanged
    # and written again never, so promoting twice is free; one the org has restated
    # since is published at its new value.
    # The org's daily write bound applies to the platform's rows, and the trail
    # takes the call's intent, naming every row, before the first lands.
    post_benchmark_claims_promote(req: promoteIn) returns (rep: promoteOut)
    # Stores a router blend for the caller's org — its name, its arms, the
    # rank they escalate through and the panel fan-out width — and answers with the
    # stored blend and the enso-<name> it would be resolved under.
    # Storing is not serving: the model layer resolves no stored preset, and the
    # answer's note says so.
    # Any signed-in caller may compose; a caller with no verified principal is refused
    # 401. Owner, At and By are the server's, whatever the body says, so a blend is
    # never stored as another org's. Posting a name the org already holds replaces
    # that blend; an org holds at most 100 names and stores at most 500 versions per
    # UTC day (429 past that).
    # Defaults fill the shape rather than refusing it: an omitted rank becomes the arms
    # in declared order and a panel below 1 becomes 1. A blend with no name or no arms
    # is a 400. A name is the part after enso-: 1 to 64 lowercase letters, digits and
    # inner hyphens, never one of Hanzo's own tiers (auto, flash, pro, ultra, free,
    # embed) and never beginning with enso-, or it is a 422. So is a blend of more
    # than 32 arms, an arm named twice or past 128 characters, a rank naming an arm
    # the blend does not declare, a panel wider than the rank, or a note past 1024
    # characters.
    post_benchmark_presets(req: Preset) returns (rep: presetStored)
    # Answers what the arena already holds for a model on each benchmark asked
    # about, and spends nothing.
    # For a benchmark the store holds attempts for, the answer is the model's latest
    # run — its id, when it was recorded, its accuracy and how many items it covered
    # — served from the attempts already recorded. A measurement held is never run
    # again from here.
    # For a benchmark it holds none for, nothing is queued, because this arena has
    # no executor: the answer says so and names the harness that measures the
    # benchmark, or, where none here does, the import a published run's items take.
    # There is no flag to force a run, because nothing here would act on it.
    # Any signed-in caller may ask; a caller with no verified principal is refused
    # 401. The answer names the caller's verified org and user — nothing in the
    # body names either — and stores nothing.
    # Validation is up front and total — a request with no model is a 400, one with
    # no benchmarks is a 400, and any benchmark id outside the catalog is a 422
    # naming exactly which ids were unknown.
    post_benchmark_runs(req: suite) returns (rep: measurements)
}

# ---------------------------------------------------------------------
# 11 op(s) here. What follows is what this schema does not carry.
#
# dropped (1) — the value does not cross, and nothing fails:
#   Preset.At  time.Time  (empty message)
#
# opaque (11) — crosses, arrives without its name:
#   Preset.At  time.Time
#   benchmarkCatalog.Data  benchmark.Benchmark (list element)
#   claimsOut.Data  benchmark.ClaimRow (list element)
#   historyOut.Data  benchmark.ModelHistory (list element)
#   importIn.Data  benchmark.Item (list element)
#   leaderboard.Rows  benchmark.LeaderRow (list element)
#   measurements.Data  benchmark.measurement (list element)
#   presetList.Data  benchmark.Preset (list element)
#   presetStored.Preset  benchmark.Preset
#   promoteOut.Data  benchmark.ClaimRow (list element)
#   putClaimsIn.Data  benchmark.Claim (list element)
