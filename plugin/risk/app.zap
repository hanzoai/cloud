# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package risk

struct riskAdoptIn {
    Address text @0
}

struct riskAppetiteIn {
    Review f64  @0
    Sample f64  @8
    Live   bool @16
}

struct riskCatalog {
    Tenant  text        @0
    Model   list<bytes> @8
    Surface list<bytes> @16
    Network list<bytes> @24
    Gap     text        @32
}

struct riskCatalogIn {
    Days i64 @0
}

struct riskLearnIn {
    Events list<bytes> @0
}

struct riskLearnOut {
    Learned i64 @0
}

struct riskPolicyOut {
    Version  i64         @0
    History  list<bytes> @8
    Disposed i64         @16
    Retained i64         @24
    Changes  i64         @32
    Window   text        @40
}

struct riskPublishOut {
    Tenant text  @0
    Value  bytes @8
    Minted bool  @16
}

struct riskRunRef {
    ID text @0
}

struct riskScoreIn {
    Event bytes @0
}

struct riskScoreOut {
    Scored  bool        @0
    Refusal text        @8
    Score   f64         @16
    Cut     f64         @24
    Alert   bool        @32
    Shadow  bool        @33
    Shape   text        @40
    Policy  i64         @48
    Causes  list<bytes> @56
    Values  list<bytes> @64
}

struct riskSearchIn {
    Days i64 @0
}

struct riskSearchReport {
    ID      text        @0
    Done    bool        @8
    Started text        @16
    Ended   text        @24
    Events  i64         @32
    Trials  list<bytes> @40
    Winner  bytes       @48
    Fitted  bytes       @56
    Refusal text        @64
    Gap     text        @72
}

struct riskSearchRun {
    ID         text @0
    Events     i64  @8
    Candidates i64  @16
}

interface risk {
    # Features is the feature catalogue in its two honest lenses.
    # The MODEL lens is the governed inventory: one entry per dimension of the model
    # space, each carrying the typology it serves, the supervisor's own words for the
    # indicator, and the published standard those words come from — so a coverage
    # claim is checkable rather than asserted. It is the same for every organisation.
    # The SURFACE lens is what THIS organisation's own event surface actually carries,
    # measured over the window: how many of its buckets carry each dimension at all,
    # and what the dimension reads where it is present. A dimension present in no
    # bucket is BLIND, and saying so is the difference between no risk and no data.
    riskFeatures(req: riskCatalogIn) returns (rep: riskCatalog)
    # Learn records a batch of events into the caller organisation's own aggregates
    # and lets its model learn from them. It answers how many it learned from.
    # IT DOES NOT SCORE, AND THAT IS THE POINT. An observation is a value you record;
    # learning is a transformation over observations; a verdict is a query against the
    # result. This op is the first two. [ops.score] is the third, it is pure, and it
    # is the ONE entry point to a verdict. They were one call, which meant you could
    # not record without training and could not train without being answered — and the
    # model ran twice over every event to produce a verdict the response carried and
    # no caller read.
    # TO OBSERVE AND JUDGE, COMPOSE THE TWO, and mind the order. Score FIRST, then
    # learn: the score is then the model's opinion of an event it has not yet learned
    # from, which is the question worth asking. The other order answers for a model
    # that has already absorbed the event it is judging.
    # This is the training path, and there is no job behind it: the model IS a set of
    # mass counters over half-space trees, so learning is an increment and the model
    # is current the instant the last event lands. Nothing from any other
    # organisation is in it, and nothing from this organisation leaves it.
    # A RETRY IS INERT. The record deduplicates on the event id you send, and an event
    # already in it moves nothing, costs nothing and is not counted — so a client that
    # timed out can send the same batch again and its model holds what it holds.
    # Without an id of your own there is nothing to converge on: two identical bodies
    # are two events.
    riskLearn(req: riskLearnIn) returns (rep: riskLearnOut)
    # Policy reports the caller organisation's own decision-regime history: every
    # distinct regime it has adopted, which version is in force, and what retention
    # has taken.
    # WHY IT EXISTS. Every score cites the version it was decided under
    # ([riskScoreOut.Policy]), and the threshold that score was measured against is
    # derived from the appetite that version states. Restate the appetite and, without
    # this record, every earlier decision becomes unreconstructible — the cut it was
    # judged by no longer exists anywhere. An adverse decision that cannot be
    # explained against the policy in force when it was taken cannot be defended.
    # It covers ONE organisation. The history is on that organisation's own shelf, so
    # another's versions are not filtered out of the answer — they are not in the file
    # the answer is read from.
    riskPolicy() returns (rep: riskPolicyOut)
    # Publishes your organisation's model as a NAMED VALUE, so a decision
    # taken today can be reconstructed tomorrow and a change made today can be undone.
    # It answers with a NAME and not with the state. The masses stay on your
    # organisation's own encrypted store and are referred to by an address computed
    # from their own content: the shape, the geometry seed, the position in the window,
    # the threshold, the masses themselves as IEEE-754 bits, and the fold watermark
    # behind them. That is what makes the value nameable without making the caller its
    # custodian.
    # IT IS IDEMPOTENT ON THE VALUE. A model that has not changed publishes to the name
    # it already has and mints nothing, reporting minted=false — so publishing at every
    # boundary that matters is free. Ten values are retained per organisation, bounded
    # in BYTES rather than in rows, and the oldest is disposed of past that.
    # A model that has learned nothing is refused: planted is not learned, and a value
    # that reproduces nothing is not a value.
    # It is POST and PUT on one address because they are one plane's two verbs over one
    # kind of thing: POST mints a value from the model in force, PUT puts a value in
    # force. They were /v1/risk/state/snapshot and /v1/risk/state/restore — two addresses
    # named after the operation rather than after the thing, which is how a reader ends
    # up asking what the difference between a snapshot and a value is.
    riskPublishModel() returns (rep: riskPublishOut)
    # Score judges one event against the caller organisation's OWN model and learns
    # nothing from it. It is how a candidate is tried against real behaviour before
    # anything depends on the answer, and it is the model's analogue of testing a
    # rule.
    # Because it records nothing, the aggregates it reads do not include the event:
    # the numbers are the organisation's history as it stands. A model still warming
    # declines with a reason rather than answering zero, because silence must never
    # read as a clean result.
    riskScore(req: riskScoreIn) returns (rep: riskScoreOut)
    # Search runs an exhaustive search for the model shape that best fits the caller
    # organisation's own history, and answers 202 with the run to read back.
    # Every candidate is replayed over that organisation's OWN feature surface in its
    # own sandbox — its own aggregates, its own model, neither of them the live one —
    # so a run cannot move a live threshold and cannot see another organisation's
    # data. The result is the learning curve for each shape and the one that fit
    # best, ranked on how closely it honoured the stated appetite, whether it warmed
    # at all, whether it saturated, and how much of the coordinate space it left
    # blind.
    # An empty history is REFUSED rather than reported as zero alerts, because "no
    # alerts" is exactly what a quiet model looks like.
    riskSearch(req: riskSearchIn) returns (rep: riskSearchRun)
    # Reads back one search run: every shape tried over this
    # organisation's own history, best first, and the one that fit.
    # A run another organisation started is simply not there — the same 404 an
    # unknown id gives, so the read is not a probe oracle.
    riskSearchResult(req: riskRunRef) returns (rep: riskSearchReport)
    # States the decision regime the caller organisation's model decides
    # under: how much of its own stream may be sent for examination, how much of the
    # rest is sampled to measure what was missed, and whether the model may change an
    # outcome at all.
    # The appetite is the decision a model is not permitted to make for itself: its
    # output is a probability, so how likely it is to MISS something is a matter of
    # policy that has to be stated, measured and reviewed rather than absorbed into a
    # constant. The alert threshold is derived from it as a quantile of the scores
    # actually observed, which is what keeps its meaning as the distribution drifts.
    # It is DURABLE BEFORE IT IS IN FORCE. The regime is recorded as a new version on
    # the organisation's own shelf before anything in memory moves, so a policy that
    # cannot be written down is refused rather than answered from state the next
    # rollout would silently undo.
    # ARMING IS AN ADMIN ACT AND TUNING IS NOT. Setting `live` requires an admin of
    # this organisation; stating the appetite and the sample is self-service for any
    # member. Taking the model live decides whether it may change an OUTCOME at all —
    # a payment frozen, a grant refused — for every customer this organisation has,
    # and that is a decision an organisation takes rather than one of its members.
    # A RESTATEMENT OF THE REGIME IN FORCE MINTS NOTHING and answers the version
    # already in force. Compare the version you receive with the version you had:
    # unchanged means the numbers were the same, which is why there is no flag for it.
    # Learned state survives the change. The model's identity covers its SHAPE — the
    # inventory and the geometry — and not its appetite, so restating policy unlearns
    # nothing. It also does not REPORT the learned state: what the model is is read
    # from the model.
    riskSetPolicy(req: riskAppetiteIn) returns (rep: riskPolicyOut)
}

# ---------------------------------------------------------------------
# 8 op(s) here. What follows is what this schema does not carry.
#
# blocked (3) — the op is absent; the field has no wire form:
#   riskAdoptModel  riskModelState.Blind  map[string]int64  (map)
#   riskAdoptModel  riskModelState.Refused  map[string]int64  (map)
#   riskState  riskModelState  risk.riskModelState  (reaches one)
#
# opaque (12) — crosses, arrives without its name:
#   riskCatalog.Model  risk.riskModelFeature (list element)
#   riskCatalog.Network  risk.riskBand (list element)
#   riskCatalog.Surface  risk.riskOrgFeature (list element)
#   riskLearnIn.Events  risk.riskEvent (list element)
#   riskPolicyOut.History  risk.riskPolicyVersion (list element)
#   riskPublishOut.Value  risk.riskModelValue
#   riskScoreIn.Event  risk.riskEvent
#   riskScoreOut.Causes  risk.riskCause (list element)
#   riskScoreOut.Values  risk.riskValue (list element)
#   riskSearchReport.Fitted  risk.riskModelValue
#   riskSearchReport.Trials  risk.riskTrial (list element)
#   riskSearchReport.Winner  risk.riskTrial
