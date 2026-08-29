# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package functions

struct definition {
    Name        text       @0
    Environment text       @8
    Runtime     text       @16
    Namespace   text       @24
    Image       text       @32
    Code        text       @40
    Handler     text       @48
    TimeoutSec  i64        @56
    MemoryLimit text       @64
    EnvNames    list<text> @72
    Target      text       @80
}

struct fnList {
    Functions list<bytes> @0
}

struct fnRef {
    Name text @0
}

struct functionDetail {
    Triggers          list<bytes> @0
    RecentInvocations list<bytes> @8
    Secrets           list<text>  @16
}

struct functionView {
    Name           text @0
    Namespace      text @8
    Environment    text @16
    Status         text @24
    Image          text @32
    Endpoint       text @40
    EnvCount       i64  @48
    TimeoutSec     i64  @56
    MemoryLimit    text @64
    Invocations7d  i64  @72
    SuccessRate    f64  @80
    AvgDurationMs  f64  @88
    Errors7d       i64  @96
    Target         text @104
    CreatedAt      text @112
    LastDeployedAt text @120
}

struct invocationList {
    Invocations list<bytes> @0
}

struct invocationPage {
    Name  text @0
    Limit i64  @8
}

struct invocationView {
    ID         text @0
    Code       i64  @8
    Status     text @16
    Method     text @24
    Time       text @32
    DurationMs i64  @40
}

struct invokeReq {
    Name  text @0
    Input text @8
}

struct logLines {
    Logs text @0
}

struct metricsQuery {
    Range text @0
}

struct secretList {
    Secrets list<bytes> @0
}

struct triggerList {
    Triggers list<bytes> @0
}

struct usage {
    Series    list<bytes> @0
    Status    bytes       @8
    CostCents i64         @16
}

interface functions {
    # Removes one of the caller org's functions and answers 204.
    # A name this org does not hold is 404 — never a silent success — and a name
    # belonging to another tenant is the same 404, because the delete is predicated on
    # the validated org.
    delete_functions_by_name(req: fnRef)
    # Is every serverless function the caller's org has published, each with its
    # real 7-day rollup.
    # A row carries the function's runtime, resource limits, deployment target and its
    # invoke endpoint, plus envCount — how many secrets it mounts. The rollup fields
    # are ABSENT rather than zero when the function has not run in the window, so a
    # console renders "—" instead of a fabricated 0.
    # Requires a validated principal; the listing is scoped to its org.
    get_functions() returns (rep: fnList)
    # Is one function with everything a detail page needs in one round-trip: its
    # definition, its 7-day rollup, its trigger, its twenty most recent invocations
    # and the NAMES of the secrets it mounts.
    # Secret values are never read or returned. A name the caller's org does not hold
    # is 404, which is also what another tenant's function looks like from here.
    get_functions_by_name(req: fnRef) returns (rep: functionDetail)
    # Is one function's past runs, newest first — each with its status,
    # HTTP code, method, time and duration.
    # These are real recorded rows, not a projection: an invocation appears here only
    # once it actually ran. Requires a validated principal; the read is scoped to its
    # org.
    get_functions_by_name_invocations(req: invocationPage) returns (rep: invocationList)
    # Is the output of a function's most recent run — its error text when that
    # run failed, else what it printed.
    # It is the LAST run only, and it is empty when the function has never run. There
    # is no log retention behind this beyond the recorded invocation itself.
    get_functions_by_name_logs(req: fnRef) returns (rep: logLines)
    # Is what is live right now — each function's current record IS its
    # live deployment, so this is the deployment inventory.
    # There is no deployment history behind it: a function has one record, and
    # publishing replaces it. The 7-day rollup is deliberately absent here, because
    # this read is about what is deployed rather than about how it has performed.
    get_functions_deployments() returns (rep: fnList)
    # Is the org's serverless dashboard over a window: a per-function
    # invocation costLine and how those invocations ended.
    # Every point is a REAL count of rows that fell in that bucket — nothing is
    # interpolated or invented, so an empty window draws a flat line rather than a
    # fabricated one.
    # costCents is null and stays null: there is no per-invocation cost source to read,
    # and reporting a number computed some other way would be a guess presented as a
    # measurement. Requires a validated principal; the read is scoped to its org.
    get_functions_metrics(req: metricsQuery) returns (rep: usage)
    # Is the NAMES of the secrets the caller org's functions mount.
    # Values are NEVER read or returned — this surface knows which names a function
    # asks for and nothing about what is behind them, which is what makes it safe to
    # list at all. One row per distinct (namespace, name).
    get_functions_secrets() returns (rep: secretList)
    # Is what calls the caller org's functions — one row per function.
    # Every function has exactly one trigger today, its HTTP invoke endpoint, so this
    # is the function list read as "how is each of these reached".
    get_functions_triggers() returns (rep: triggerList)
    # Publishes a serverless function under the caller's org and answers 201
    # with it.
    # The name is the key and is claimed once; the names that would shadow a
    # collection route are reserved. runtime and environment are the same field —
    # either spelling is accepted — and default to node.
    # Bounds are clamped rather than refused where a clamp is honest: a timeout above
    # the 900-second ceiling becomes the ceiling instead of silently reverting to the
    # 30-second default, and an omitted memory limit becomes 256Mi. target=fleet runs
    # on the org's own GPU fleet and supports runtime=python only.
    # Requires a validated principal; the function is owned by that principal's org.
    post_functions(req: definition) returns (rep: functionView)
    # Runs a function and records a REAL invocation.
    # The answer is the invocation record whatever happened to it: 200 when the org's
    # code ran clean, 502 when it ran and failed, 503 when this deployment has no
    # sandbox to run code in. The record IS the evidence, so it rides the failure
    # rather than being replaced by an error envelope.
    # Billing is two-part and both parts are prepaid-then-metered on the one shared
    # meter: a flat per-invocation request fee, gated BEFORE any sandbox compute runs
    # so an unfunded org gets 402 and nothing executes, and a usage-native
    # GB-seconds compute debit taken after the run. Either is independently free when
    # its fee is zero, so an operator can bill by request alone, by compute alone, or
    # by both — and a zero request fee removes the balance gate with it.
    # A TRANSPORT failure is not charged: the sandbox being unreachable ran no
    # billable compute. Code that ran and exited non-zero IS charged — that is a
    # successful invocation of a failing program, not a billing failure.
    # When the sandbox is not configured on this deployment, a non-fleet function
    # fails closed before anything is recorded — no execution and no fabricated
    # output. Scoped to the caller's org; requires a validated principal.
    post_functions_by_name_invoke(req: invokeReq) returns (rep: invocationView)
}

# ---------------------------------------------------------------------
# 11 op(s) here. What follows is what this schema does not carry.
#
# dropped (1) — the value does not cross, and nothing fails:
#   functionDetail.functionView  functions.functionView  (promoted, not carried)
#
# opaque (8) — crosses, arrives without its name:
#   fnList.Functions  functions.functionView (list element)
#   functionDetail.RecentInvocations  functions.invocationView (list element)
#   functionDetail.Triggers  functions.triggerView (list element)
#   invocationList.Invocations  functions.invocationView (list element)
#   secretList.Secrets  functions.secretView (list element)
#   triggerList.Triggers  functions.triggerView (list element)
#   usage.Series  functions.costLine (list element)
#   usage.Status  functions.statusBreakdown
