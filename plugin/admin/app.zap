# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package admin

struct AccessOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct BackfillIn {
    Org text @0
}

struct BackfillOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct CordonIn {
    ID     text @0
    Cordon bool @8
    Drain  bool @9
}

struct CustomerDetailOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct CustomersOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
    Total  i64         @24
}

struct DropletIn {
    ID   text @0
    Size text @8
    Disk bool @16
}

struct FinanceOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct GrantIn {
    Org         text @0
    User        text @8
    AmountCents i64  @16
    Currency    text @24
    Reason      text @32
    Source      text @40
}

struct GrantOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct GrantsIn {
    Org    text @0
    Result text @8
    Limit  text @16
}

struct GrantsOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
    Total  i64         @24
}

struct InvoicesIn {
    Status text @0
    Org    text @8
    Limit  text @16
}

struct InvoicesOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
    Total  i64         @24
}

struct LoadBalancerIn {
    ID text @0
}

struct MetricsIn {
    Window text @0
    Limit  text @8
}

struct MetricsOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct MoneyOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct OrgIn {
    Org text @0
}

struct ProvidersCreditOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
}

struct ReadIn {
    Refresh text @0
}

struct ReadOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct RecordsIn {
    Org        text @0
    Sub        text @8
    Action     text @16
    Resource   text @24
    ResourceID text @32
    Result     text @40
    Since      text @48
    Until      text @56
    PageSize   text @64
    Page       text @72
}

struct RevenueOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct ScaleIn {
    ID    text @0
    Pool  text @8
    Count i64  @16
}

struct ServiceInput {
    Service      text       @0
    DisplayName  text       @8
    Description  text       @16
    Hosts        list<text> @24
    WaitlistMode bool       @32
}

struct SubscriptionsIn {
    Status text @0
    Org    text @8
    Limit  text @16
}

struct SubscriptionsOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
    Total  i64         @24
}

struct SubsystemsIn {
    Range text @0
}

struct SubsystemsOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct UsageFundingIn {
    From text @0
    To   text @8
}

struct UsageFundingOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
}

struct VerifyOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct VolumeIn {
    ID       text @0
    Snapshot text @8
    Name     text @16
    SizeGiB  i64  @24
}

struct VolumeSnapshotOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct aimetricsOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct basesOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
    Total  i64         @24
}

struct capIn {
    Org text @0
    ID  text @8
}

struct computeIn {
    Kind  text @0
    Org   text @8
    Range text @16
}

struct computeOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
    Total  i64         @24
}

struct flagsOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct iamPageIn {
    Owner    text @0
    Page     text @8
    PageSize text @16
}

struct meOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct o11yOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct orgsIn {
    Page     text @0
    PageSize text @8
}

struct orgsOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
    Total  i64         @24
}

struct overviewOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct productsIn {
    Kind text @0
    Tier text @8
    Env  text @16
}

struct productsOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
    Total  i64         @24
}

struct promoIn {
    PercentOff i64        @0
    Start      text       @8
    End        text       @16
    Plans      list<text> @24
    Active     bool       @32
}

struct rangeIn {
    Range text @0
}

struct serviceModeIn {
    Service      text @0
    WaitlistMode bool @8
}

struct serviceOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct servicesOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct setFlagIn {
    Key     text  @0
    Active  bool  @8
    Filters bytes @16
}

struct syncOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct usageIn {
    Org text @0
}

struct usageOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct usersIn {
    Org      text @0
    Query    text @8
    Page     text @16
    PageSize text @24
}

struct usersOut {
    Status text        @0
    Msg    text        @8
    Data   list<bytes> @16
    Total  i64         @24
}

struct volumesOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct waitlistBoostRequest {
    Waitlist text @0
    Email    text @8
    RefCode  text @16
    Points   i64  @24
    Reason   text @32
}

struct waitlistIn {
    Waitlist text @0
    Page     text @8
    PageSize text @16
}

interface admin {
    # Is the fleet AI board: LLM generations over gen_ai spans (count, cost,
    # avg/p95 latency, per-model), per-model usage from the live cloud_usage ledger, and
    # the eval plane (traces, scores, score names, runs, and the average-score trend).
    # Every signal degrades INDEPENDENTLY — a table that is absent or errors contributes its
    # zero value and the read still succeeds. Generation latency is a SEPARATE query from
    # generations and cost on purpose: a duration/attribute mismatch there must not zero
    # the two numbers that did read.
    adminAIMetrics(req: rangeIn) returns (rep: aimetricsOut)
    # Walks EVERY hash chain this deployment keeps and reports each one: which
    # chains were checked, how many records each holds, the head hash to pin externally
    # against tail-truncation, and — when a chain is broken — the seq of the first bad
    # record and why.
    # The trail is a FAMILY of chains, one per process, so the answer is a set and not a
    # boolean: `intact`, `broken` and `unread` count the three verdicts and sum to the
    # number of chains. A chain that could not be READ is reported `unread` and is never
    # a pass — an unreadable chain and a verified one must not render the same, which is
    # the whole reason this is not one flag.
    # An unconfigured store is an honest failure here rather than a fabricated pass.
    adminAuditVerify() returns (rep: VerifyOut)
    # Lists the tenant Base instances in the caller's window — a SuperAdmin sees every
    # tenant's, anyone else only their own subtree's.
    # The scope is enforced TWICE: the upstream is asked for the caller's org, AND every row
    # it returns is re-checked against the resolved scope. An upstream that ignored the
    # filter therefore degrades to empty, never to a cross-tenant leak.
    # The Base engine is being embedded into cloud; until it lands this proxies
    # BASE_ADMIN_URL and, when that is unset, answers 200 with an empty list and msg saying
    # so — the honest not-yet state, never fabricated instances.
    adminBases() returns (rep: basesOut)
    # Rolls the fleet's compute usage up to one row per (org, app, project, kind):
    # how many distinct machines ran in the window, how many are still active, what they
    # billed, and when each group last emitted an event. The console folds these into its
    # org → app → project tree.
    # A machine counts as ACTIVE when its LATEST lifecycle event is not a terminal one
    # (stop/destroy/terminate/delete/off/shutdown/expire and their past tenses) — the same
    # fold the console applies, done in the warehouse so the count is over every machine and
    # not just the page.
    # Honest-empty when the warehouse is not connected or hanzo.compute_usage is not
    # provisioned yet: an empty list, never a fabricated fleet.
    adminCompute(req: computeIn) returns (rep: computeOut)
    # Answers GET /v1/admin/customers/:org.
    adminCustomer(req: OrgIn) returns (rep: CustomerDetailOut)
    # Lists every customer org at a glance, sorted by slug: owner email, plan,
    # suspend status, member count, balance, month-to-date spend and MRR.
    # Each row costs one IAM read plus the org's money reads, fanned out under a fixed
    # concurrency ceiling so a large fleet cannot stampede the upstreams. Every read is
    # best-effort per row: an upstream miss degrades THAT field to its honest zero rather
    # than failing the fleet.
    adminCustomers() returns (rep: CustomersOut)
    # Answers GET /v1/admin/finance. It reads the multi-vendor COGS from commerce
    # /v1/costs, the DO promo-credit/burn-down treasury view, and the fleet commerce revenue,
    # then hands them to ComputeFinance. SuperAdmin only.
    adminFinance() returns (rep: FinanceOut)
    # Carries ONE org's current commerce prepaid balance into the native finance
    # wallet — the one-time cutover between the two ledgers.
    # It is IDEMPOTENT: the deposit uses the fixed ref "backfill:<org>", so re-running it
    # credits the wallet at most once. Safe to retry.
    # The pre-migration balance is read from the CO-RESIDENT commerce ledger, not over HTTP:
    # the admin HTTP client dials an unroutable in-process address and would read $0, and a
    # phantom zero would silently carry nothing while reporting success. When commerce is
    # not co-resident this fails rather than migrating nothing.
    adminFinanceBackfill(req: BackfillIn) returns (rep: BackfillOut)
    # Reads the platform control-plane board: every runtime launch/release
    # switch (waitlist, public signup, subsystem activation, gateway limits, network ids)
    # with its LIVE value and where that value came from — a stored definition or the
    # compiled-in default.
    adminFlags() returns (rep: flagsOut)
    # Issues a staff credit grant to the org named in the path — a comp, refund
    # or promo — through the ONE credit-write path core.ApplyGrant, which validates the
    # amount against the per-grant cap, checks the org exists, moves the money and records
    # the tamper-evident audit row.
    # The credit lands on the account account.Payer resolves, NOT necessarily the org: name
    # a member of a pooled org and the pool is credited. The receipt echoes the subject so
    # the caller can see which.
    adminGrantCredit(req: GrantIn) returns (rep: GrantOut)
    # Reads the credit-grant ledger across ALL orgs, newest first — who granted what
    # to whom, when, and from which money bucket.
    # It is a PROJECTION of the tamper-evident audit trail, not a second store: every grant
    # is written there as action "admin.customer.credit", so this view cannot drift from
    # what actually happened, and FAILED grants appear too.
    # A deployment with no local audit store has no history to project, and says so with an
    # empty list and a msg rather than an error.
    adminGrants(req: GrantsIn) returns (rep: GrantsOut)
    # Serves the whole DigitalOcean infrastructure board: droplets, volumes, DOKS
    # clusters and load balancers, each cross-referenced against every cluster's live
    # Kubernetes state so the board can say what is safe to destroy and what is not.
    # It is cached for up to a minute because one read is a fan-out over the DO API plus a
    # full pod/PV listing per cluster. Staleness is never load-bearing: every MUTATION
    # re-scans from scratch and ignores this cache.
    # Only an unusable DO account is a hard failure. A partial read still produces a board,
    # with the failing source named in sources[] — except for clusters and volumes, which
    # the safety verdict depends on; without those the analysis degrades rather than
    # classifying anything it cannot prove.
    adminInfra(req: ReadIn) returns (rep: ReadOut)
    # Answers GET /v1/admin/invoices.
    # GET /v1/admin/invoices?org=&status=&limit=
    adminInvoices(req: InvoicesIn) returns (rep: InvoicesOut)
    # Issues a credit grant to any org from the operator Grants view, with the
    # target named in the body. It funnels through the SAME core.ApplyGrant that
    # POST /v1/admin/customers/:org/credit uses, so there is exactly ONE credit-write path
    # and one audit trail behind both.
    adminIssueGrant(req: GrantIn) returns (rep: GrantOut)
    # Answers with the validated operator identity — who the console is signed in as,
    # which tier they are, and how wide their tenant window is. The fields come from the
    # sanitized identity headers the gate just read, so they are authoritative and never
    # client-forgeable; nothing is looked up.
    adminMe() returns (rep: meOut)
    # Answers GET /v1/admin/metrics by aggregating commerce.events directly
    # (fleet-wide, no per-org fan-out). SuperAdmin only.
    # GET /v1/admin/metrics?window=30d&limit=20
    adminMetrics(req: MetricsIn) returns (rep: MetricsOut)
    # moneyBoardHandler answers GET /v1/admin/money.
    adminMoney() returns (rep: MoneyOut)
    # Is the fleet-wide observability board: LLM usage (requests, tokens, cost,
    # errors, top orgs, top models), trace RED metrics (count, p50/p95/p99 latency in ms,
    # error rate, top services), fleet log volume, and the O11yAI generation rollup — all
    # aggregated across EVERY tenant, with no org filter applied.
    # Every signal degrades INDEPENDENTLY. A table that is absent or errors contributes its
    # zero value and the read still succeeds, so the board renders exactly what the
    # warehouse holds rather than failing whole because one of four sources is missing.
    # Same when the warehouse is not connected at all: the zero board, never a fabricated
    # fleet.
    adminO11y(req: rangeIn) returns (rep: o11yOut)
    # Lists the tenant directory one row per org, sorted by slug: member count and the
    # org's month-to-date spend and credit balance, read live from IAM and commerce.
    # The rows are the caller's tenant window, not the fleet: a SuperAdmin gets every org, a
    # white-label admin only their own subtree. A per-org read that fails degrades THAT row
    # to an honest zero — this panel carries no sources[] channel to report freshness on, so
    # the alternative would be a fleet total that silently reads healthy.
    adminOrgs(req: orgsIn) returns (rep: orgsOut)
    # Is the Platform Overview tiles: how many orgs and users are in the caller's
    # tenant window, the fleet workload counts, and month-to-date spend and credits.
    # It ALWAYS answers 200 — a tile board that fails as a whole because one upstream is
    # down is useless. Instead every upstream reports itself in sources[]: ok, degraded, or
    # not-configured. A commerce read that failed for ANY org marks that source degraded,
    # because the spend/credits totals are then an undercount and must not read healthy.
    # The AI tiles — 30-day spend and tokens — come from the AI ledger (ledger.go), the
    # plane that owns "what was served". They used to come from the money plane with the
    # token counter hardcoded to zero, so the board read $0.00 and 0 tokens over a month in
    # which the fleet served fifteen thousand requests. Credits still come from commerce,
    # which owns the wallet.
    adminOverview() returns (rep: overviewOut)
    # Lists the fleet workload registry: every operator App CR across the platform
    # namespaces with its declared vs running image tag, reconciled health/phase and drift
    # verdict. Optionally narrowed by kind, tier or env, each an exact match.
    # The rows are the SAME observation /v1/platform/fleet renders — read through the in-process
    # platform client, not a second k8s client — so the two boards can never disagree about what
    # the fleet is. A PaaS plane that is not co-resident yields an honestly empty registry,
    # never a fabricated row.
    adminProducts(req: productsIn) returns (rep: productsOut)
    # Serves GET /v1/admin/providers/credit — the per-provider upstream
    # credit ledger. SuperAdmin-guarded (see Routes).
    adminProvidersCredit() returns (rep: ProvidersCreditOut)
    # Restores access for every member of the org, undoing a suspend. It
    # reports the same per-user breakdown.
    adminReactivateCustomer(req: OrgIn) returns (rep: AccessOut)
    # Is the fleet money board: total prepaid balances held, total realized spend,
    # MRR, ARPU, a per-customer table sorted highest-revenue first, and a real 30-day spend
    # trend from the usage ledger.
    # ORTHOGONAL to /v1/admin/finance, which is the COGS/margin view of what WE pay vendors.
    # This is the customer side: what each customer holds, spends and subscribes to.
    # arpu divides realized spend by PAYING customers, not by all of them — a fleet of free
    # signups must not deflate the number. A customer counts as paying when it has spend or
    # MRR.
    # An org whose money did not read degrades to honest zeros and marks the commerce source
    # degraded in sources[], so a partial fleet read is visible instead of quietly low.
    adminRevenue() returns (rep: RevenueOut)
    # Reads the launch board: every hosted service in the registry with its LIVE
    # waitlist mode, evaluated through the flag engine. This is the "remove the waitlist one
    # service at a time" view.
    adminServices() returns (rep: servicesOut)
    # Stores or overwrites ONE platform switch's definition and answers with the
    # whole board as it now stands. The flip is hot: this pod applies it immediately and
    # peers converge within one evaluation TTL (15s by default), with no redeploy.
    # The body reaches the flag engine BYTE-FOR-BYTE — it is the engine's definition
    # format, not this layer's, so a field the engine understands and admin does not must
    # still arrive intact. setFlagIn names the two fields that matter for documentation; it
    # is not a filter.
    # The write is recorded in the store's activity log against the caller's email.
    adminSetFlag(req: setFlagIn) returns (rep: flagsOut)
    # Flips ONE service's waitlist switch — the launch lever. Hot: it takes
    # effect on this pod immediately and on peers within one evaluation TTL, with no
    # redeploy. An unknown service is a 404, not a silent create; onboarding goes through
    # upsertService.
    adminSetServiceMode(req: serviceModeIn) returns (rep: serviceOut)
    # Takes a point-in-time snapshot of one volume — the undo a delete relies
    # on, available on its own so an operator can take one before any risky change.
    # It re-scans the board first (never the cache) so the volume it snapshots is one that
    # exists right now, and audits the outcome either way.
    adminSnapshotVolume(req: VolumeIn) returns (rep: VolumeSnapshotOut)
    # Answers GET /v1/admin/subscriptions.
    # GET /v1/admin/subscriptions?org=&status=&limit=
    adminSubscriptions(req: SubscriptionsIn) returns (rep: SubscriptionsOut)
    # subsystems answers GET /v1/admin/subsystems. ?range=24h|7d|30d bounds the telemetry
    # window (default 30d) — the same enum, and the same helpers, as the o11y board.
    adminSubsystems(req: SubsystemsIn) returns (rep: SubsystemsOut)
    # Cuts off every member of the org: IAM refuses a forbidden user at
    # login AND at token issuance, so a suspended customer can neither sign in nor mint a
    # fresh token. Fully reversible with ReactivateCustomer.
    # The result names every user updated and every user that was NOT — a partial failure
    # leaves the org in a mixed state and says so instead of reporting a clean success.
    adminSuspendCustomer(req: OrgIn) returns (rep: AccessOut)
    # Answers the operator's "Sync now" button. There is nothing to kick: admin
    # aggregates LIVE on every read, so the button is just a re-read. It acknowledges
    # honestly with started:true rather than pretending a batch job was queued.
    adminSync() returns (rep: syncOut)
    # Onboards a hosted service, or edits one, so a new host comes under the
    # launch gate WITHOUT a redeploy. Re-registering an existing service PRESERVES its live
    # switch — editing the hosts of a service that is already open must not silently close
    # it again.
    adminUpsertService(req: ServiceInput) returns (rep: serviceOut)
    # Returns the trailing 30 days of AI usage: one org's when org names one, else the
    # whole fleet's — the spend, the tokens and the requests, the daily curve behind them,
    # and the split by model.
    # It reads the AI ledger (ledger.go), which is the plane that owns this question. It used
    # to ask the commerce billing API instead, once per org, and answer with a hardcoded
    # empty series, zero tokens and zero requests, on the reasoning that a trend and a split
    # were "not derivable from the commerce billing API". They are not — but the question was
    # never commerce's. hanzo.cloud_usage carries a row per served request, so all three fall
    # out of the same window the totals do.
    adminUsage(req: usageIn) returns (rep: usageOut)
    # Splits our upstream AI usage by how it was FUNDED: one row per (provider,
    # model) over the window, tagged credit (provider grant still remaining), paid (grant
    # exhausted) or paid_only (no grant at all).
    # The class is resolved at the PROVIDER level from the credit ledger, not per call — the
    # per-call split, and the `byo` class, arrive when the metering write stamps a funding
    # column on cloud_usage and this can GROUP BY it directly. Until then a provider with
    # remaining grant reports all of its usage as credit, which is right in aggregate and
    # approximate at the boundary where a grant runs out mid-window.
    # An unparseable window falls back to the last 30 days rather than refusing: this is a
    # dashboard read, and a typo in a date must not blank the board.
    adminUsageFunding(req: UsageFundingIn) returns (rep: UsageFundingOut)
    # Lists the user directory across the caller's tenant window, one page at a time.
    # total is IAM's REAL total, so the console can page through it.
    # A SuperAdmin may aim the read at one tenant with org; a white-label admin cannot — for
    # them the owner is hard-pinned to their own org and org is ignored, which is what keeps
    # the directory from becoming a cross-tenant read.
    adminUsers(req: usersIn) returns (rep: usersOut)
    # Returns the realtime block-storage board: the DigitalOcean volume fleet
    # (count, capacity, monthly list cost, per-volume region and attachment) plus the
    # analytics datastore's OWN fill, read from its system.disks.
    # A volume's usedGiB and pct are null, always: DO exposes capacity and attachment but no
    # fill, so the console renders "—" rather than a number nobody measured. The datastore
    # card is the one real fill here, and it is the number to scale on.
    # The two sources degrade independently — a DO outage still returns the datastore fill,
    # and a disconnected datastore still returns the DO fleet. What a DO outage must NOT do
    # is pass for an account with no volumes, so the fleet it could not read is marked
    # incomplete rather than reported as a count of zero at a cost of zero.
    adminVolumes() returns (rep: volumesOut)
}

# ---------------------------------------------------------------------
# 37 op(s) here. What follows is what this schema does not carry.
#
# blocked (19) — the op is absent; the field has no wire form:
#   adminAnalytics  analyticsOut.Data  admin.analyticsData  (reaches one)
#   adminApplications  iamRowsOut.Data  interface {}  (any)
#   adminAudit  RecordsOut.Data  interface {}  (any)
#   adminCaps  rawOut.Data  interface {}  (any)
#   adminCordonNode  MutationOut.Data  interface {}  (any)
#   adminCreateCap  rawOut  admin.rawOut  (reaches one)
#   adminDeleteCap  rawOut  admin.rawOut  (reaches one)
#   adminDeleteDroplet  MutationOut  infra.MutationOut  (reaches one)
#   adminDeleteLoadBalancer  MutationOut  infra.MutationOut  (reaches one)
#   adminDeleteVolume  MutationOut  infra.MutationOut  (reaches one)
#   adminPromo  rawOut  admin.rawOut  (reaches one)
#   adminResizeDroplet  MutationOut  infra.MutationOut  (reaches one)
#   adminResizeVolume  MutationOut  infra.MutationOut  (reaches one)
#   adminRoles  iamRowsOut  admin.iamRowsOut  (reaches one)
#   adminScaleNodePool  MutationOut  infra.MutationOut  (reaches one)
#   adminSetPromo  rawOut  admin.rawOut  (reaches one)
#   adminUpdateCap  rawOut  admin.rawOut  (reaches one)
#   adminWaitlist  rawOut  admin.rawOut  (reaches one)
#   adminWaitlistBoost  rawOut  admin.rawOut  (reaches one)
#
# opaque (33) — crosses, arrives without its name:
#   AccessOut.Data  customer.AccessChange
#   BackfillOut.Data  finance.Backfilled
#   CustomerDetailOut.Data  customer.CustomerDetailData
#   CustomersOut.Data  customer.CustomerRow (list element)
#   FinanceOut.Data  finance.FinanceData
#   GrantOut.Data  core.GrantResult
#   GrantsOut.Data  customer.GrantRow (list element)
#   InvoicesOut.Data  invoices.InvoiceRow (list element)
#   MetricsOut.Data  metrics.MetricsData
#   MoneyOut.Data  admin.moneyBoard
#   ProvidersCreditOut.Data  finance.ProviderCredit (list element)
#   ReadOut.Data  infra.Snapshot
#   RevenueOut.Data  revenue.RevenueData
#   SubscriptionsOut.Data  subscriptions.SubscriptionRow (list element)
#   SubsystemsOut.Data  admin.subsystemBoard
#   UsageFundingOut.Data  finance.UsageFundingRow (list element)
#   VerifyOut.Data  audit.Trail
#   VolumeSnapshotOut.Data  digitalocean.Snapshot
#   aimetricsOut.Data  admin.aiMetrics
#   basesOut.Data  admin.baseInstance (list element)
#   computeOut.Data  admin.computeLeaf (list element)
#   flagsOut.Data  plane.FlagBoard
#   meOut.Data  admin.adminMe
#   o11yOut.Data  admin.o11yGlobal
#   orgsOut.Data  admin.orgRow (list element)
#   overviewOut.Data  admin.overviewData
#   productsOut.Data  admin.productRow (list element)
#   serviceOut.Data  admin.serviceOne
#   servicesOut.Data  admin.serviceList
#   syncOut.Data  admin.syncStarted
#   usageOut.Data  admin.usageData
#   usersOut.Data  admin.operatorUser (list element)
#   volumesOut.Data  admin.storageSnapshot
