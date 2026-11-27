# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package label

struct riskCoverageIn {
    From    text @0
    To      text @8
    Horizon i64  @16
}

struct riskDisposeIn {
    Before text @0
}

struct riskDisposeOut {
    Before    text @0
    Disposed  i64  @8
    Remaining i64  @16
    Held      i64  @24
    Restored  i64  @32
    Total     i64  @40
    Oldest    text @48
}

struct riskHoldIn {
    IDs  list<text> @0
    Hold bool       @8
}

struct riskHoldOut {
    Hold    bool @0
    Changed i64  @8
    Missing i64  @16
    Held    i64  @24
}

struct riskLabelCoverage {
    From         text        @0
    To           text        @8
    Horizon      i64         @16
    Facts        i64         @24
    Events       i64         @32
    Matured      i64         @40
    Unmatured    i64         @48
    Judged       i64         @56
    Unlabelled   i64         @64
    Contested    i64         @72
    Productive   i64         @80
    Unproductive i64         @88
    Sources      list<bytes> @96
    Explore      f64         @104
    Pending      i64         @112
}

struct riskLabelIn {
    Labels list<bytes> @0
}

struct riskLabelOut {
    Recorded  i64         @0
    Duplicate i64         @8
    Refused   i64         @16
    Results   list<bytes> @24
    Mirror    text        @32
    Pending   i64         @40
}

struct riskLabelVocabulary {
    Kinds        list<text> @0
    Dispositions list<text> @8
    Precedence   list<text> @16
    Rule         list<text> @24
    Retention    i64        @32
}

struct riskLabelsIn {
    Kind    text @0
    Subject text @8
    Source  text @16
    From    text @24
    To      text @32
    Limit   i64  @40
}

struct riskLabelsOut {
    Labels list<bytes> @0
    Count  i64         @8
}

struct riskResolveIn {
    Subjects list<bytes> @0
    Horizon  i64         @8
    Now      text        @16
}

struct riskResolveOut {
    Now        text        @0
    Horizon    i64         @8
    Labels     list<bytes> @16
    Unmatured  i64         @24
    Unlabelled i64         @32
}

interface label {
    # Applies this tenant's retention, and only this tenant's.
    # It is bounded three ways, each a compliance property rather than a
    # convenience. It refuses a boundary younger than the platform floor, because a
    # label can be the input to an adverse action and five years is what the
    # retention ledger holds such a record for. It never touches a record under
    # litigation hold. And it disposes of whole records rather than redacting
    # fields.
    # It removes the derived columnar copy BEFORE the record, and refuses the whole
    # disposal if the warehouse cannot be reached. The other order would leave rows
    # in the warehouse that nothing can identify any more, which is a disposal that
    # did not happen and says it did.
    riskDisposeLabels(req: riskDisposeIn) returns (rep: riskDisposeOut)
    # Places or releases a litigation hold on named records.
    # A hold is a fact about the RECORD, not about the world: it says retention may
    # not dispose of this row, and it asserts nothing about what happened. So it is
    # not a field on an assertion and it is not folded into the content digest —
    # carried there it was silently a no-op on any record that already existed, since
    # re-filing the same assertion with a hold flag produced the same digest, the
    # insert was ignored, and the caller was answered `duplicate` while the hold it
    # asked for was never placed. This op is the one way a hold moves, in either
    # direction, and the move is written to the audit log.
    # Every named id is this tenant's or is nothing. The statement runs against the
    # tenant's own file, which holds no other tenant's rows and has no column that
    # could name one.
    riskHoldLabels(req: riskHoldIn) returns (rep: riskHoldOut)
    # Records a batch of ground truth against the entities it judges.
    # Each assertion carries TWO times — when the judged event happened, and when
    # the assertion became knowable — and both are required. The second is what
    # keeps a chargeback that landed in June out of a model that had to decide in
    # February.
    # It is idempotent on the CONTENT of an assertion, so a webhook that redelivers
    # is safe. It never overwrites: a source that corrects itself later files a NEW
    # assertion, which wins from the moment it became knowable and leaves every
    # earlier observation instant seeing exactly what it saw.
    # The asserter is stamped from the validated credential and is not a body field.
    riskLabel(req: riskLabelIn) returns (rep: riskLabelOut)
    # Reports how much of a window has matured and how much of that is
    # judged, per source.
    # It is the gate on training. A supervised fit over a window whose judged count
    # is near zero produces a number, and the number is meaningless; this op is what
    # lets that be stated before the fit rather than discovered after it.
    # It reads the RECORD plane and folds every assertion at that event's OWN as-of,
    # so the counts obey exactly the leakage rule a materialisation would. It counts
    # only what was ASSERTED: what share of the whole event STREAM carries a label is
    # a question about the feature plane's denominator and is not answerable here.
    riskLabelCoverage(req: riskCoverageIn) returns (rep: riskLabelCoverage)
    # Publishes the closed vocabularies and the precedence rule that
    # resolves a conflict between two sources.
    # A precedence rule nobody can read is a rule nobody can audit or dispute, and
    # the whole defensibility of a contested label rests on being able to say why
    # one assertion beat another. The order returned here is derived from the same
    # declaration the resolver reads — it is not a description of it.
    riskLabelVocabulary() returns (rep: riskLabelVocabulary)
    # Reads the assertions this tenant has recorded, newest event first.
    # It reads the RECORD — the tenant's own store — and not the columnar copy, so
    # what it returns is what would be produced in an audit. Narrow it by entity, by
    # asserter, or by event window.
    riskLabels(req: riskLabelsIn) returns (rep: riskLabelsOut)
    # Answers, for each named event, which assertion was in force AS OF that
    # event's own horizon — and what disagreed with it.
    # This is the join surface: the dataset materialiser calls it to attach ground
    # truth to training rows, and the evaluator calls it to score a past decision
    # against what was knowable when the decision had to be made. One mechanism for
    # both, so a model can never be trained under one leakage rule and scored under
    # another.
    # Three answers are distinct and all three are honest: a resolved label, an
    # event that has not matured, and a matured event nobody has judged. The last is
    # never reported as unproductive.
    riskResolveLabels(req: riskResolveIn) returns (rep: riskResolveOut)
}

# ---------------------------------------------------------------------
# 7 op(s) here. What follows is what this schema does not carry.
#
# opaque (6) — crosses, arrives without its name:
#   riskLabelCoverage.Sources  label.riskSourceCoverage (list element)
#   riskLabelIn.Labels  label.riskLabelFact (list element)
#   riskLabelOut.Results  label.riskLabelResult (list element)
#   riskLabelsOut.Labels  label.riskLabelRecord (list element)
#   riskResolveIn.Subjects  label.riskLabelEvent (list element)
#   riskResolveOut.Labels  label.riskResolved (list element)
