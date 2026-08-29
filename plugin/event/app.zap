# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package event

struct Overview {
    Range    text  @0
    Start    text  @8
    End      text  @16
    Interval text  @24
    Scope    bytes @32
    LLM      bytes @40
    Web      bytes @48
    Commerce bytes @56
}

struct Timeseries {
    Range    text        @0
    Start    text        @8
    End      text        @16
    Interval text        @24
    Scope    bytes       @32
    Series   list<bytes> @40
    Source   text        @48
}

struct Top {
    Range     text  @0
    Start     text  @8
    End       text  @16
    Scope     bytes @24
    Models    bytes @32
    Products  bytes @40
    Pages     bytes @48
    Referrers bytes @56
    Sources   bytes @64
}

struct errorList {
    Data list<bytes> @0
}

struct eventList {
    Data list<bytes> @0
}

struct healthReport {
    Service   text  @0
    Status    text  @8
    Datastore bool  @16
    Warehouse text  @24
    Plane     bytes @32
    Reason    text  @40
    Lenses    bytes @48
    Lost      bytes @56
}

struct insightsStatus {
    OK      bool @0
    Engine  text @8
    Surface text @16
}

struct limitQuery {
    Limit i64 @0
}

struct topQuery {
    Range text @0
    Start text @8
    End   text @16
    Limit i64  @24
}

struct windowQuery {
    Range text @0
    Start text @8
    End   text @16
}

interface event {
    # Errors returns the caller org's most recently captured errors, newest first. The
    # error-tracking read view over event.error — the plane table the write core's error
    # facts land in (errors are DELIBERATELY not on event.event) — each with its captured
    # exception surfaced from the attributes map as a first-class field.
    # The org is the validated principal's — never a parameter — and this read requires a
    # real bearer, NEVER the write-only publishable key: pk- can attribute a write and can
    # read nothing. 403 without a validated bearer, 503 when the warehouse is unreachable.
    get_event_errors(req: limitQuery) returns (rep: errorList)
    # Health reports whether the event plane can take a write and the warehouse can
    # answer a read.
    # It reports the analytics subsystem's own liveness in BOTH directions: plane is
    # the event plane it WRITES (the bus and the JetStream stream every accepted
    # event is published to, both named in the report), and datastore is the
    # warehouse it READS, with each read lens's table reported as it is provisioned
    # (the LLM usage ledger and the product-event table).
    # EITHER ONE DOWN IS A 503, and the report says WHICH — they are probed
    # independently and never collapse into a single bit. This endpoint used to
    # report the read half only, and answered 200/ok while every POST /v1/event
    # failed on a stream that could not bind: a total ingest outage behind a green
    # probe. A readiness gate here now gates on the write path too.
    # plane.ready IS A REAL PROBE and walks the ingest path itself — the same
    # connection and the same stream a publish uses — so it cannot answer ready while
    # a publish would 503. plane.reason carries the plane's own error text when it is
    # false.
    # datastore IS NOT PROBED WITH A QUERY. It is the state of the process's own
    # shared client — established, and not since closed — so a warehouse accepting
    # connections and failing reads still reports true. Degraded CARRIES the report
    # (status, the failing half, reason) as its body rather than an error envelope,
    # so a gate reads the cause off the same object it got at 200.
    # A MISSING LENS TABLE IS NOT A FAILURE and never moves the status: a lens
    # reported available:false answers honest-empty rather than erroring, so a fresh
    # deployment whose collector has not emitted yet is legitimately 200 with the
    # product-event lens unavailable. The lens block is reported whenever the
    # warehouse is REACHABLE — including on a report degraded by the plane, where the
    # tables genuinely were probed — and is absent only when the warehouse is not,
    # having nothing to say about tables it could not reach.
    # Unauthenticated on purpose — liveness has to be probe-able — and it reads NO
    # tenant data: table existence and stream presence only, never a row and never an
    # event.
    get_event_health() returns (rep: healthReport)
    # Returns the caller org's most recent product events, newest first.
    # The console's raw-event view over event.event — the same table the capture endpoints
    # fill — one row per stored event, with the row's attributes returned as the
    # properties object.
    # The org is the validated principal's — never a parameter — and a read requires a
    # real bearer, never the write-only publishable key. 403 without a validated bearer,
    # 503 when the warehouse is unreachable.
    get_event_insights_events(req: limitQuery) returns (rep: eventList)
    # Reports that the unified insights surface is serving. It reads no
    # tenant data and consults no dependency, so it answers 200 unconditionally and needs
    # no principal — liveness must be probe-able. The warehouse-connectivity probe is a
    # different question and lives at GET /v1/event/health.
    get_event_insights_health() returns (rep: insightsStatus)
    # Overview returns the caller org's analytics KPIs for one time window. Three lenses
    # over one warehouse: llm is the live per-org LLM usage ledger (requests, tokens,
    # spend, models, providers, errors) and is always real; web (pageviews, visitors,
    # sessions) and commerce (orders, revenue, AOV) read the product-event table and
    # report available=false rather than fabricating zeros when it holds nothing yet.
    # The org is the validated principal's — never a parameter — so a caller can only
    # ever read its own tenant. 403 without a validated bearer, 400 on an unknown range,
    # 503 when the warehouse is unreachable.
    get_event_overview(req: windowQuery) returns (rep: Overview)
    # Timeseries returns the caller org's LLM usage over time as an evenly-spaced series.
    # One point per hour or per day — the bucket the window implies, 24h giving hours and
    # 7d/30d giving days — carrying requests, total tokens and spend in cents. Empty
    # buckets are filled with zeros so a client charts a continuous line.
    # The org is the validated principal's — never a parameter. 403 without a validated
    # bearer, 400 on an unknown range, 503 when the warehouse is unreachable.
    get_event_timeseries(req: windowQuery) returns (rep: Timeseries)
    # Top returns the caller org's ranked lenses for one window, five of them at once.
    # models ranks LLM models by spend and is always real; products ranks commerce orders
    # by revenue; topPages ranks requested paths, topReferrers the external referrer
    # domains ("(direct)" for a missing or same-origin one) and topSources the utm_source
    # campaigns ("(none)" when absent), each by pageviews. Every lens carries each row's
    # share of the in-window total, so a top-N honestly shows the long tail.
    # The four event lenses report available=false rather than fabricating zeros when the
    # product-event table holds nothing yet. The org is the validated principal's — never
    # a parameter. 403 without a validated bearer, 400 on an unknown range, 503 when the
    # warehouse is unreachable.
    get_event_top(req: topQuery) returns (rep: Top)
}

# ---------------------------------------------------------------------
# 7 op(s) here. What follows is what this schema does not carry.
#
# opaque (17) — crosses, arrives without its name:
#   Overview.Commerce  event.CommerceOverview
#   Overview.LLM  event.LLMOverview
#   Overview.Scope  event.Scope
#   Overview.Web  event.WebOverview
#   Timeseries.Scope  event.Scope
#   Timeseries.Series  event.UsagePoint (list element)
#   Top.Models  event.TopModels
#   Top.Pages  event.Breakdown
#   Top.Products  event.TopProducts
#   Top.Referrers  event.Breakdown
#   Top.Scope  event.Scope
#   Top.Sources  event.Breakdown
#   errorList.Data  event.capturedError (list element)
#   eventList.Data  event.productEvent (list element)
#   healthReport.Lenses  event.healthLenses
#   healthReport.Lost  event.loss
#   healthReport.Plane  event.healthPlane
