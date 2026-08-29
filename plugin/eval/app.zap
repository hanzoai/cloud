# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package eval

struct Board {
    Scope   bytes       @0
    Range   bytes       @8
    Totals  bytes       @16
    Series  list<bytes> @24
    ByModel list<bytes> @32
    Other   bytes       @40
    Latency bytes       @48
}

struct boardQuery {
    Range    text @0
    Interval text @8
}

struct datasetRef {
    Name text @0
}

struct evaluatorList {
    Data list<bytes> @0
}

struct evaluatorReq {
    Name      text @0
    Model     text @8
    Criteria  text @16
    ScoreName text @24
}

struct evaluatorView {
    Name      text @0
    Model     text @8
    Criteria  text @16
    ScoreName text @24
    CreatedAt text @32
    UpdatedAt text @40
}

struct itemPage {
    Dataset text @0
}

struct page {
    Limit i64 @0
}

struct runFilter {
    Dataset text @0
}

struct runRequest {
    Dataset       text  @0
    Model         text  @8
    RunName       text  @16
    Limit         i64   @24
    Judge         bytes @32
    Authorization text  @40
}

struct runSummary {
    Dataset    text        @0
    Model      text        @8
    JudgeModel text        @16
    RunName    text        @24
    Items      i64         @32
    Scored     i64         @40
    AvgScore   f64         @48
    Results    list<bytes> @56
}

struct runs {
    Data list<bytes> @0
}

struct scoreConfigList {
    Data list<bytes> @0
}

struct scoreConfigReq {
    Name       text       @0
    DataType   text       @8
    MinValue   f64        @16
    MaxValue   f64        @24
    Categories list<text> @32
}

struct scoreConfigView {
    Name       text       @0
    DataType   text       @8
    MinValue   f64        @16
    MaxValue   f64        @24
    Categories list<text> @32
    CreatedAt  text       @40
    UpdatedAt  text       @48
}

struct scoreFilter {
    Name    text @0
    RunName text @8
    TraceID text @16
}

struct scoreList {
    Data list<bytes> @0
}

struct scoreReq {
    Name        text @0
    TraceID     text @8
    RunName     text @16
    Dataset     text @24
    ItemID      text @32
    DataType    text @40
    Value       f64  @48
    StringValue text @56
    Comment     text @64
}

struct scoreView {
    ID          text @0
    Name        text @8
    TraceID     text @16
    RunName     text @24
    DataType    text @32
    Value       f64  @40
    StringValue text @48
    Comment     text @56
    Timestamp   text @64
}

struct traceFilter {
    SessionID text @0
    RunName   text @8
    Dataset   text @16
}

interface eval {
    # Removes the named dataset of the caller's org AND all of its
    # examples, in one transaction.
    # This is not a detach: the examples are gone with the set, so a dataset cannot
    # be resurrected by re-creating the name. A name this org does not have is 404 —
    # never a silent success — and a name belonging to another tenant is the same
    # 404, because the delete is predicated on the validated org. Requires a
    # validated principal; 403 without one. Runs and scores already recorded against
    # the dataset are telemetry events and are NOT deleted with it.
    delete_eval_datasets_by_name(req: datasetRef)
    # Is the judges your org has defined, each with its judge model,
    # criteria and the score name it writes under.
    # Requires a validated principal; 403 without one, and the listing is filtered on
    # the validated org.
    get_eval_evaluators(req: page) returns (rep: evaluatorList)
    # Is your org's AI overview board over a window: totals
    # (generations, prompt and completion tokens, cost in cents, errors, success
    # rate, distinct models and users), a gap-filled time series, a per-model
    # breakdown with the long tail folded into "other", and latency percentiles read
    # from the GenAI spans.
    # The window the answer was actually computed over is echoed back, so a client
    # never has to infer it. A platform admin sees the board across ALL orgs;
    # everyone else sees their own.
    # The board is HONEST-EMPTY where it cannot be computed: with no datastore wired,
    # or under a named project scope the usage ledger does not yet carry, it answers a
    # valid board with zero totals and a flat series rather than a fabricated number
    # or a 500. Requires a validated principal; 403 without one.
    get_eval_metrics(req: boardQuery) returns (rep: Board)
    # Is the score shapes your org has declared — each name's data
    # type, its numeric bounds and its allowed categories.
    # Requires a validated principal; 403 without one, and the listing is filtered on
    # the validated org.
    get_eval_rubrics(req: page) returns (rep: scoreConfigList)
    # Is your past runs and how they scored — the dataset and model, the
    # judge model, how many examples were attempted and how many scored, the average
    # score, and when it happened.
    # Requires a validated principal; 403 without one, and rows are filtered on the
    # validated org. These records come from the metastore rather than the datastore,
    # so they are readable on a deployment with no telemetry wired — but a run's
    # traces and scores are not.
    get_eval_runs(req: runFilter) returns (rep: runs)
    # Is the score events your org has recorded, narrowed by any of name,
    # runName and traceId.
    # The org is bound as an authoritative predicate on the query, never taken from a
    # header, so a filter can narrow the caller's own scores but can never widen past
    # them. Requires a validated principal; 403 without one. Scores live in the
    # datastore, so a deployment with none wired answers 503 rather than an empty
    # page that would read as "no scores".
    get_eval_scores(req: scoreFilter) returns (rep: scoreList)
    # Saves a reusable judge for the caller's org — the judge model
    # and the written criteria it grades against — and answers 201 with it.
    # Like a dataset, the NAME is the key: re-posting a name edits that judge rather
    # than adding a second one. Requires a validated principal; 403 without one.
    post_eval_evaluators(req: evaluatorReq) returns (rep: evaluatorView)
    # Defines the shape of one score name for the caller's org and
    # answers 201 with it.
    # This is the integrity contract, not documentation: once a rubric exists for a
    # name, every score recorded under that name is checked against it and the
    # rubric's data type is AUTHORITATIVE — a caller cannot claim a different one.
    # Out-of-range values, unlisted labels and non-finite numbers are refused at
    # write time.
    # A CATEGORICAL rubric with no categories is 400, as is a non-finite bound or a
    # minValue above maxValue. Requires a validated principal; 403 without one.
    post_eval_rubrics(req: scoreConfigReq) returns (rep: scoreConfigView)
    # Runs a real evaluation and answers the summary when it is finished —
    # this is synchronous work, not a job id.
    # For each ACTIVE example in the dataset it calls the model under test, records a
    # trace, calls the LLM-as-judge, and records the judge's score with its
    # reasoning. The answer carries the per-item results (item id, trace id, score,
    # output or error) alongside items, scored and avgScore.
    # The dataset must belong to the caller's org (404 otherwise) and must have at
    # least one ACTIVE example (422 otherwise).
    # It runs as YOU: the caller's own Authorization bearer drives the model gateway,
    # so a request without one is 401 rather than a run made anonymously or under a
    # service identity. Only a non-reversible hash of that credential is recorded on
    # the traces.
    # Bounded and honest about it: an org may have at most 4 runs in flight and the
    # fifth is 429 rather than queued, and the whole run is capped at 10 minutes —
    # examples past the deadline come back with an error instead of a score, and
    # scored counts only real successes. A run where NOTHING scored answers 502, not
    # a 200 that looks like an evaluation. A run must be able to persist what it
    # produces, so a deployment with no datastore wired is 503 up front. Requires a
    # validated principal; 403 without one.
    post_eval_runs(req: runRequest) returns (rep: runSummary)
    # Files one score event for the caller's org and answers 201 with it.
    # This is how human review and out-of-band graders land beside the automatic
    # ones: name the score, give it a value (or a stringValue for a categorical
    # label), and attach it to a trace, a run, a dataset example, or any combination.
    # Scores are validated fail-closed. A value must be FINITE — NaN and Inf are 400
    # — and if the org has declared a rubric for this name, that rubric decides the
    # type and the value must satisfy it: inside the numeric bounds, or one of the
    # allowed categories. A caller cannot override the declared type by sending a
    # different dataType.
    # A score is TELEMETRY, not metadata, so it needs the datastore: a deployment
    # with none wired answers 503 rather than accepting a score it cannot persist.
    # Requires a validated principal; 403 without one, and the org is stamped from
    # the validated claim rather than read off the body.
    post_eval_scores(req: scoreReq) returns (rep: scoreView)
}

# ---------------------------------------------------------------------
# 10 op(s) here. What follows is what this schema does not carry.
#
# dropped (4) — the value does not cross, and nothing fails:
#   itemPage.page  eval.page  (promoted, not carried)
#   runFilter.page  eval.page  (promoted, not carried)
#   scoreFilter.page  eval.page  (promoted, not carried)
#   traceFilter.page  eval.page  (promoted, not carried)
#
# blocked (10) — the op is absent; the field has no wire form:
#   get_eval_datasets  datasetList.Data  []eval.datasetView  (no wire form)
#   get_eval_datasets_by_name  datasetView.Metadata  map[string]interface {}  (map)
#   get_eval_datasets_by_name_items  itemList.Data  []eval.itemView  (no wire form)
#   get_eval_traces  traceList.Data  []eval.traceView  (no wire form)
#   post_eval_datasets  datasetReq.Metadata  map[string]interface {}  (map)
#   post_eval_datasets  datasetView  eval.datasetView  (reaches one)
#   post_eval_datasets_by_name_items  itemReq.Metadata  map[string]interface {}  (map)
#   post_eval_datasets_by_name_items  itemView.Expected  interface {}  (any)
#   post_eval_datasets_by_name_items  itemView.Input  interface {}  (any)
#   post_eval_datasets_by_name_items  itemView.Metadata  map[string]interface {}  (map)
#
# opaque (13) — crosses, arrives without its name:
#   Board.ByModel  eval.ModelStat (list element)
#   Board.Latency  eval.LatencyStat
#   Board.Other  eval.ModelStat
#   Board.Range  eval.BoardRange
#   Board.Scope  eval.BoardScope
#   Board.Series  eval.BoardPoint (list element)
#   Board.Totals  eval.BoardTotals
#   evaluatorList.Data  eval.evaluatorView (list element)
#   runRequest.Judge  eval.judgeSpec
#   runSummary.Results  eval.itemResult (list element)
#   runs.Data  eval.runRecord (list element)
#   scoreConfigList.Data  eval.scoreConfigView (list element)
#   scoreList.Data  eval.scoreView (list element)
