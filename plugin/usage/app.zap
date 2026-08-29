# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package usage

struct dashResp {
    Provider  text        @0
    Account   text        @8
    Range     text        @16
    From      text        @24
    To        text        @32
    Source    text        @40
    Scope     text        @48
    Available bool        @56
    Current   list<bytes> @64
    Windows   list<bytes> @72
}

struct reportReq {
    Samples           list<bytes> @0
    Provider          text        @8
    Account           text        @16
    Plan              text        @24
    Kind              text        @32
    Machine           text        @40
    Lane              text        @48
    Window            text        @56
    WindowMinutes     i32         @64
    WindowStart       text        @72
    ResetsAt          text        @80
    UsedPct           f64         @88
    Confidence        text        @96
    Synthetic         bool        @104
    Requests          i64         @112
    InputTokens       i64         @120
    OutputTokens      i64         @128
    TotalTokens       i64         @136
    CachedInputTokens i64         @144
    CostCents         i64         @152
    CostLimitCents    i64         @160
    Currency          text        @168
}

struct reportResp {
    Accepted i64  @0
    Stored   bool @8
}

struct usageAnalyticsAccess {
    Plan   text  @0
    Access bytes @8
}

struct usageAnalyticsQuery {
    End   text @0
    Plan  text @8
    Range text @16
    Start text @24
}

struct usageAnalyticsView {
    Scope         bytes @0
    Plan          text  @8
    Range         text  @16
    Start         text  @24
    End           text  @32
    RetentionDays i64   @40
    Export        bool  @48
    Providers     bytes @56
}

struct usagePlanQuery {
    Plan text @0
}

struct usageSamplesQuery {
    Account  text @0
    Provider text @8
    Range    text @16
    Window   text @24
}

struct usageSummary {
    Range    text  @0
    Start    text  @8
    End      text  @16
    Interval text  @24
    Scope    bytes @32
    Spend    bytes @40
    LLM      bytes @48
    Accounts bytes @56
    Sources  bytes @64
}

struct usageWindowQuery {
    Range text @0
    Start text @8
    End   text @16
}

interface usage {
    # Is the entitlement-GATED per-provider breakdown of the caller org's LLM
    # usage — the paid lens over the same warehouse ledger GET /v1/usage/summary reads
    # its totals from. Basic own-org usage stays ungated at /v1/usage/summary.
    # A plan that does not grant the analytics datastore is refused with 402, and an
    # unresolvable plan fails closed to the free floor, which does not grant it. The
    # window is clamped forward to the plan's retention entitlement, so a tenant can
    # never read older than its plan allows even with a custom start. The response is
    # marked no-store.
    # INTERIM (mirrors apps/world's limits echo): no org→plan resolver exists in cloud
    # yet — the subscription lookup is owned by the billing plane and the gateway
    # principal carries no plan claim — so the caller passes the plan and the gate
    # resolves THAT plan's access.
    get_usage_analytics(req: usageAnalyticsQuery) returns (rep: usageAnalyticsView)
    # Echoes a plan's resolved analytics entitlement so a dashboard can
    # configure itself against the LIVE catalog instead of hardcoding tier numbers. An
    # empty plan resolves the free floor, and a catalog resolution failure serves that
    # same floor rather than erroring — so this always answers 200. It is a read-only
    # contract echo and carries no tenant data.
    get_usage_analytics_access(req: usagePlanQuery) returns (rep: usageAnalyticsAccess)
    # Is the PER-PROVIDER view: one connected account's own consumption of its
    # own plan — "my plan is 47% through its 6h window, resets at 14:20".
    # `current` is the newest instance of each lane (the headline); `windows` is the
    # history behind it. Both come from ONE deduped read, so they can never disagree.
    # The rows are the caller's OWN linked accounts, scoped to the validated principal
    # and its subject — never another user's, and never another org's.
    get_usage_samples(req: usageSamplesQuery) returns (rep: dashResp)
    # Answers GET /v1/usage/summary: the caller's own usage footprint over one
    # window — the categorized spend roll-up from the commerce ledger, the org's LLM
    # usage totals from the warehouse, and the caller's OWN linked provider accounts
    # beside the org's Hanzo-routed usage.
    # Every source degrades INDEPENDENTLY to honest zeros and says so in `sources` and
    # in its own `available` flag, so a partial deploy reports "no data" rather than
    # fabricating spend. The account rows and the Hanzo rows are concatenated and never
    # summed: a plan's percent is not money.
    # The response is org-scoped from the validated principal and marked no-store — a
    # signed-out caller is refused.
    get_usage_summary(req: usageWindowQuery) returns (rep: usageSummary)
    # Ingests a batch of account-usage samples — what a developer's OWN AI
    # accounts have consumed of their OWN plans, metered from each provider's own
    # login — and appends them to the warehouse series. Answers 202.
    # Send either a `samples` array or one sample's fields at the top level. Every
    # sample needs a provider, a machine and a known window class; an unknown window or
    # kind is refused rather than silently rewritten, because a dash filled with a class
    # nobody reported is worse than an error. There is no timestamp field: the server
    # owns the observation clock, and a sample says which window it measured with
    # windowStart or resetsAt.
    # It is FAIL-SOFT on storage: a warehouse outage costs a poll of history
    # (stored:false), never a failed request. It records usage ONLY — the link registry
    # is refreshed separately via POST /v1/link, so there is one and only one way to
    # update an account row.
    post_usage(req: reportReq) returns (rep: reportResp)
}

# ---------------------------------------------------------------------
# 5 op(s) here. What follows is what this schema does not carry.
#
# opaque (11) — crosses, arrives without its name:
#   dashResp.Current  usage.usageWindowView (list element)
#   dashResp.Windows  usage.usageWindowView (list element)
#   reportReq.Samples  usage.sampleReq (list element)
#   usageAnalyticsAccess.Access  usage.usageAnalyticsGrant
#   usageAnalyticsView.Providers  usage.ProviderBreakdown
#   usageAnalyticsView.Scope  usage.usageScope
#   usageSummary.Accounts  usage.Accounts
#   usageSummary.LLM  usage.LLM
#   usageSummary.Scope  usage.usageScope
#   usageSummary.Sources  usage.Sources
#   usageSummary.Spend  usage.Spend
