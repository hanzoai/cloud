# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package link

struct AccountsUsage {
    Scope    text        @0
    Source   text        @8
    Total    bytes       @16
    Accounts list<bytes> @24
}

struct RoutePlan {
    Candidates  list<bytes> @0
    Primary     bytes       @8
    GeneratedAt text        @16
}

struct boardResp {
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

struct dashIn {
    Provider text @0
    Account  text @8
    Window   text @16
    Range    text @24
}

struct deviceView {
    Machine        text        @0
    Host           text        @8
    OS             text        @16
    LastSeen       text        @24
    Accounts       list<bytes> @32
    ActiveSessions i64         @40
}

struct enrollReq {
    Machine  text  @0
    Host     text  @8
    OS       text  @16
    Provider text  @24
    Account  text  @32
    Plan     text  @40
    Kind     text  @48
    Usage    bytes @56
}

struct ingestReq {
    Samples list<bytes> @0
}

struct ingestResp {
    Accepted i64         @0
    Stored   bool        @8
    Links    list<bytes> @16
}

struct linkList {
    Devices list<bytes> @0
    Links   list<bytes> @8
}

struct linkRef {
    ID text @0
}

struct linkView {
    ID        text  @0
    User      text  @8
    Machine   text  @16
    Host      text  @24
    OS        text  @32
    Provider  text  @40
    Account   text  @48
    Plan      text  @56
    Kind      text  @64
    Billing   text  @72
    Status    text  @80
    LastSeen  text  @88
    Usage     bytes @96
    CreatedAt text  @104
    UpdatedAt text  @112
}

struct machineRef {
    Machine text @0
}

struct revokeResp {
    Revoked         i64         @0
    SessionsStopped i64         @8
    Links           list<bytes> @16
}

struct summaryIn {
    Range text @0
}

struct summaryResp {
    Range   text        @0
    From    text        @8
    To      text        @16
    Rows    list<bytes> @24
    Account bytes       @32
    Hanzo   bytes       @40
}

interface link {
    # Logs out one account and stops the sessions it was running.
    # It revokes a single linked account and stops the agent sessions that ran under
    # it, answering with the revoked row and how many sessions stopped. The link is
    # RETAINED with a revoked status rather than deleted, so its usage history and
    # the audit trail survive the log-out — which also means a revoked account still
    # appears in the list, and is excluded from the route plan rather than absent
    # from it. The session stop is narrowed to the revoking user's own sessions on
    # that device, provider and account, and a stop that fails does not fail the
    # revoke: the revoked row is the durable truth. An id that does not exist, or
    # belongs to another user or org, is the same 404.
    delete_link_by_id(req: linkRef) returns (rep: revokeResp)
    # Lists your linked accounts and the devices they sit on.
    # It answers the caller's own links plus a devices projection of the same rows
    # folded per machine — the cross-machine "AI Providers / Accounts" view. A
    # device is a projection, not a stored entity: its labels come from its
    # most-recently-seen account, so there is no device to create and none to
    # garbage-collect. Revoked links are INCLUDED rather than dropped, because a
    # logged-out account keeps its usage history and audit trail. Scoped to the
    # caller: a validated principal and a non-empty org, else 403.
    get_link() returns (rep: linkList)
    # Reads one linked account.
    # It answers a single link — its device, provider, account, plan, how it bills,
    # its status and its latest usage snapshot. An id that does not exist, or
    # belongs to another user or org, is the same 404: the scope is a bound
    # predicate on the read, so a wrong id and a foreign id are indistinguishable
    # and neither confirms the other's existence. The static paths on this
    # collection — route, usage, devices — register before this one and win
    # first-match, so a link whose id collided with one of those words could not be
    # addressed here.
    get_link_by_id(req: linkRef) returns (rep: linkView)
    # Shows one machine: its accounts, usage and live sessions.
    # It answers one device — its host and OS labels, every account the caller has
    # signed in on that machine with its latest usage, and how many agent sessions
    # the caller currently has running on it. The device labels come from the
    # most-recently-seen account, since a device is a projection of its links rather
    # than a row of its own. A machine with none of the caller's accounts is 404,
    # which is also the answer when the machine belongs to someone else — the scope
    # makes the two indistinguishable, deliberately. The session count reports 0
    # where the agent plane is not mounted rather than failing the read.
    get_link_devices_by_machine(req: machineRef) returns (rep: deviceView)
    # Gets the failover order across your linked accounts.
    # It answers an ordered redundancy plan over the caller's LINKED (not revoked)
    # accounts: each candidate with its remaining rate-limit headroom, whether it is
    # routable right now, how it BILLS (plan or commerce), and a reason when it is
    # not — plus the primary to try first. It is what lets a router fail over from
    # one subscription to another and fall back to the metered API as the
    # always-available backstop, knowing the cost consequence before it dials.
    # It is POLICY, not execution: the plan is computed purely from the usage
    # snapshots already in the registry, never by probing a provider, so it is a
    # total function of the links and costs nothing to ask for. Actually dialing,
    # detecting a live 429 and advancing to the next candidate belongs to the
    # caller. A link with no snapshot counts as full headroom.
    get_link_route() returns (rep: RoutePlan)
    # Shows one provider account's own usage dashboard.
    # It answers the time series for a SINGLE provider account — the windows in
    # range plus the currently-open ones — as that provider's own meter reported it:
    # "my plan is 47% through its 6h window, resets at 14:20". current is the newest
    # instance of each lane (the headline); windows is the history behind it, both
    # computed from ONE deduped read. provider is required; an unknown window class
    # or range is 400, never a quiet fallback to a different one. When no series is
    # available the response is a 200 with available:false and empty lists — an
    # honest "we have no data", which is a different claim from zero usage.
    get_link_usage(req: dashIn) returns (rep: boardResp)
    # Breaks down what the gateway routed through each of your accounts.
    # It answers one row per linked account the GATEWAY actually routed through,
    # plus their total — requests, prompt and completion tokens, and cost. This is
    # the routed ledger, the read twin of the counter the router writes, and it is
    # distinct from both of its neighbours: not the device collector's plan
    # snapshots, and not the org money ledger. The source and scope fields on the
    # response say so on every payload. The same shape answers in the billing
    # namespace, from one shaping function, so the two mounts cannot drift.
    get_link_usage_accounts() returns (rep: AccountsUsage)
    # Shows plan consumption and Hanzo spend side by side.
    # It answers the global usage board over one window: the caller's own linked
    # accounts, metered from each provider's own login, alongside their org's
    # Hanzo-routed inference. These come from different ledgers and mean different
    # things, so every row is LABELLED by source, by scope and by availability, and
    # THE TWO ARE NEVER SUMMED — a plan's percentage is not money, and a provider's
    # own spend is not a Hanzo charge. The rows sit side by side and say what they
    # are.
    # One resolver fixes the window for both halves, so the two sets always cover
    # the same period. range is one of 1h, 24h, 7d or 30d and defaults to 24h;
    # anything else is 400 rather than a silent substitution. A ledger that cannot
    # answer reports available:false instead of a zero that would read as "no usage".
    get_link_usage_summary(req: summaryIn) returns (rep: summaryResp)
    # Registers a signed-in AI provider account on a machine.
    # It records that a developer has signed into one provider account on one
    # machine — a Claude Max or ChatGPT Plus subscription, a Hanzo key, a raw
    # provider key — and answers 201 with the stored link. Re-reporting the same
    # (machine, provider, account) UPDATES that link rather than creating a second,
    # so a collector may call this on every heartbeat. machine and provider are
    # required (400 otherwise), as is a valid kind, and every field is
    # length-bounded. Scoped to the caller: a validated principal and a non-empty
    # org, else 403, so a caller writes only their OWN accounts within their own org.
    post_link(req: enrollReq) returns (rep: linkView)
    # Logs out every account on one machine and stops its sessions.
    # It revokes every one of the caller's accounts on one machine and stops the
    # agent sessions they were running, answering with how many of each. This is the
    # "I lost that laptop" button. Revoked links are RETAINED, not deleted, so usage
    # history and the audit trail survive a log-out — the rows come back in the
    # response with their new status. The session stop reaches only the REVOKING
    # user's own sessions, so a shared machine name can never be used to stop a
    # co-tenant's work, and a stop that fails does not fail the revoke: the revoked
    # row is the durable truth and the count then honestly reports fewer. A machine
    # with nothing left to revoke is 404.
    post_link_devices_by_machine_revoke(req: machineRef) returns (rep: revokeResp)
    # Reports usage samples from the device collector.
    # It ingests a batch of usage samples and answers with how many were accepted,
    # whether history was durably stored, and the links they refreshed. A report
    # also REFRESHES one link per distinct (machine, provider, account) it names, so
    # a running collector keeps the accounts overview current without a separate
    # registration call.
    # A caller can only ever report for THEMSELVES: org and subject come from the
    # validated bearer, never from the body, so no sample can be attributed to
    # another user or tenant. History is FAIL-SOFT and stored says which happened —
    # a warehouse outage still accepts the report and refreshes the links rather
    # than failing the device, and answers 202 either way. Send either one sample
    # inline or up to 256 in samples; an empty batch or an over-long one is 400, as
    # is a provider, window class or kind outside the closed vocabulary — an
    # unrecognized window is refused rather than rewritten, because a silently
    # reclassified sample would fill a dashboard with a class nobody reported.
    post_link_usage(req: ingestReq) returns (rep: ingestResp)
}

# ---------------------------------------------------------------------
# 11 op(s) here. What follows is what this schema does not carry.
#
# dropped (1) — the value does not cross, and nothing fails:
#   ingestReq.readingReq  link.readingReq  (promoted, not carried)
#
# opaque (15) — crosses, arrives without its name:
#   AccountsUsage.Accounts  link.RoutedUsage (list element)
#   AccountsUsage.Total  link.AccountsTotal
#   RoutePlan.Candidates  link.RouteCandidate (list element)
#   RoutePlan.Primary  link.RouteCandidate
#   boardResp.Current  link.readingView (list element)
#   boardResp.Windows  link.readingView (list element)
#   deviceView.Accounts  link.linkView (list element)
#   ingestReq.Samples  link.readingReq (list element)
#   ingestResp.Links  link.linkView (list element)
#   linkList.Devices  link.deviceView (list element)
#   linkList.Links  link.linkView (list element)
#   revokeResp.Links  link.linkView (list element)
#   summaryResp.Account  link.sourceState
#   summaryResp.Hanzo  link.sourceState
#   summaryResp.Rows  link.totalView (list element)
