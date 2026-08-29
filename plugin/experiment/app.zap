# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package experiment

struct Analysis {
    Trial        text        @0
    Metric       text        @8
    Alpha        f64         @16
    Outcomes     list<bytes> @24
    Winner       text        @32
    ExposedTotal i64         @40
}

struct Trial {
    Project       text        @0
    ID            text        @8
    Name          text        @16
    SubjectKind   text        @24
    FlagKey       text        @32
    ExposureEvent text        @40
    MetricEvent   text        @48
    Arms          list<bytes> @56
    Status        text        @64
    Winner        text        @72
    CreatedBy     text        @80
    CreatedAt     text        @88
    DecidedBy     text        @96
    DecidedAt     text        @104
}

struct analyzeQuery {
    ID    text @0
    Start text @8
    End   text @16
    Days  i64  @24
    Alpha f64  @32
}

struct assignQuery {
    ID      text @0
    Subject text @8
    Props   text @16
}

struct assignment {
    Trial   text  @0
    Subject text  @8
    Arm     text  @16
    On      bool  @24
    Payload bytes @32
}

struct createBody {
    ID            text        @0
    Name          text        @8
    SubjectKind   text        @16
    FlagKey       text        @24
    ExposureEvent text        @32
    MetricEvent   text        @40
    Arms          list<bytes> @48
}

struct decideBody {
    ID     text @0
    Winner text @8
}

struct experimentList {
    Data  list<bytes> @0
    Total i64         @8
}

struct experimentRef {
    ID text @0
}

struct health {
    OK        bool @0
    Subsystem text @8
}

interface experiment {
    # Is every experiment in the caller's org, with its variants, status and
    # decision, ordered by project then id.
    # Scoped to the org resolved from the validated principal — a distinct org is a
    # distinct physical store, so no query here can reach another tenant's rows — and
    # further narrowed to the caller's project scope when the credential carries one.
    # A principal with NO project scope sees the org's experiments across all of its
    # projects, which is the answer a reader most often expects to be filtered and is
    # not.
    # Requires a validated principal; refuses without one rather than answering an
    # empty list.
    get_experiment() returns (rep: experimentList)
    # Is one experiment's definition and lifecycle: variants, weights, control
    # arm, status and winner.
    # It reads the registry row only — the definition and the decision, never live
    # measurements. Assignment lives in the flags plane and outcomes in analytics;
    # this is the value that names both.
    # Scoped to the caller's org and project from the validated principal, so another
    # tenant's experiment of the same id is simply not found. An id that is not a
    # legal slug is answered the same way, without a store read — the shape check and
    # the existence check are one answer, so neither leaks the other.
    get_experiment_by_id(req: experimentRef) returns (rep: Trial)
    # Is the variant one subject is bucketed into, and the payload that
    # variant carries.
    # The bucketing is a deterministic hash of the subject, so the same subject gets
    # the same arm on every call for as long as the flag definition is unchanged —
    # and this is a pure READ: it records nothing. In particular it does NOT record an
    # exposure. The caller's SDK must emit the experiment's exposure event itself, or
    # the analysis has an empty denominator and every arm measures zero.
    # An empty variant with on false is not an error — it means the flag returned
    # nothing for this subject, so the subject is not enrolled. A flags engine that is
    # unavailable refuses rather than defaulting to an arm. Requires a validated
    # principal, and the experiment must exist in the caller's org and project.
    get_experiment_by_id_assign(req: assignQuery) returns (rep: assignment)
    # Is whether the experiments subsystem is mounted and serving in this
    # process.
    # It answers unconditionally. It proves exactly one thing — that this binary
    # registered the experiments routes and is dispatching them — and deliberately no
    # more: it reads no principal, opens no per-org registry, and touches neither the
    # flags engine nor the analytics plane, so a 200 here says nothing about whether a
    # given tenant's store will open or whether an analysis can run. It is the only
    # route on this surface that needs no org.
    # The static path is registered ahead of the /:id read, so it always wins the
    # first-match scan. "health" is a legal experiment id, which means an experiment
    # created under that id can never be fetched by id — pick another.
    get_experiment_health() returns (rep: health)
    # Registers a controlled experiment AND puts its assignment flag live, in
    # that order, so the arms start bucketing subjects the moment this returns 201 —
    # the flag is created active at 100% rollout, with each variant weighted as
    # declared. There is no separate start call; creating IS starting.
    # A variant carries an opaque payload this primitive never interprets: a feature
    # config, an ad-creative id, a subject line, a model id.
    # Requires a validated principal, and refuses without one. The org and project are
    # taken from that principal and the creator is stamped from the credential — none
    # of the three is a body field, so an experiment cannot be filed against another
    # tenant. An id already used in this project is a conflict, never a silent
    # overwrite: re-creating would stomp the assignment flag of a run in progress.
    # It fails closed on the flag write. An experiment whose assignment flag does not
    # exist would assign nobody, so if that write fails nothing is registered.
    post_experiment(req: createBody) returns (rep: Trial)
    # Is per-variant conversion, lift and statistical significance against
    # the control arm.
    # It reads per-subject outcomes from the analytics plane over a window, folds them
    # into per-variant samples, and returns each arm's exposed count, conversions,
    # rate, lift versus control, two-proportion z, two-tailed p-value and whether it
    # clears alpha. Arms with no data still appear with zero exposed, so the read is
    # complete over the experiment's declared arms; the control arm sorts first. The
    # pooled-variance estimator is used and the p-value is exact; a degenerate
    # comparison (an empty arm, no variance) answers z 0 and p 1 — not significant,
    # never an error.
    # Only EXPOSED subjects are counted, and each is joined to its arm by re-evaluating
    # the assignment flag AT ANALYSIS TIME — not from what was in force during the
    # window. That is the one rule to get right: analyzing an experiment after its
    # winner has been promoted re-buckets every subject into the promoted arm,
    # collapsing the control to zero exposed and making the result meaningless. Read
    # the analysis before deciding. A subject the flag cannot place is dropped rather
    # than allowed to poison the fold.
    # The winner in the response is ADVISORY — the significant, control-beating arm
    # with the highest rate, or empty when inconclusive. It promotes nothing; the
    # decision is a separate, explicit act.
    # Every plane read is scoped to the caller's org. Per-variant samples are also
    # written to the research evidence plane as immutable ab rows, best-effort: the
    # analysis is still returned if that write fails, because the samples are
    # recomputable, and the failure is logged rather than swallowed.
    post_experiment_by_id_analyze(req: analyzeQuery) returns (rep: Analysis)
    # Promotes one variant to the whole rollout and records who decided.
    # It rewrites the assignment flag so the named winner serves 100% of the rollout
    # and every other arm 0%, preserving the flag's targeting groups and payloads,
    # then stamps the experiment decided with the winner, the deciding credential and
    # the time. This is a production behaviour change that takes effect immediately
    # for every subject the flag evaluates.
    # It requires an ORG ADMIN of the caller's own org — a stricter gate than the rest
    # of this surface, matching the flags write plane, because promoting is a flag
    # write. The admin check runs AFTER the experiment is found, so a caller from
    # another tenant is answered not-found rather than forbidden and learns nothing
    # about what exists.
    # An experiment whose assignment flag has gone missing is a conflict rather than a
    # silent no-op — there is nothing to promote.
    # Deciding is NOT terminal. A second call re-promotes a different variant and
    # re-stamps the row; the status stays decided and the previous winner is
    # overwritten with no record that it was ever chosen. Nothing here reverts the
    # flag to its original weights either, so an experiment cannot be un-decided
    # through this route — restoring a split means writing the flag definition back
    # through the flags plane.
    post_experiment_by_id_decide(req: decideBody) returns (rep: Trial)
}

# ---------------------------------------------------------------------
# 7 op(s) here. What follows is what this schema does not carry.
#
# opaque (4) — crosses, arrives without its name:
#   Analysis.Outcomes  experiment.Outcome (list element)
#   Trial.Arms  experiment.Arm (list element)
#   createBody.Arms  experiment.Arm (list element)
#   experimentList.Data  experiment.Trial (list element)
