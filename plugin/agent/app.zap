# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package agent

struct CodingStartIn {
    Repo           text @0
    Prompt         text @8
    Project        text @16
    Base           text @24
    After          text @32
    AgentRef       text @40
    TargetID       text @48
    TimeoutSeconds i64  @56
    Tool           text @64
    Desktop        bool @72
    ReplyChannel   text @80
    ReplyThread    text @88
}

struct CodingStarted {
    SessionID text @0
    Branch    text @8
    Repo      text @16
    Routed    bool @24
    TargetID  text @32
}

struct activityFeed {
    Activity list<bytes> @0
}

struct agentDetail {
    Instructions text        @0
    RecentRuns   list<bytes> @8
}

struct agentList {
    Agents list<bytes> @0
}

struct agentRef {
    Ref text @0
}

struct agentView {
    ID               text       @0
    Name             text       @8
    Model            text       @16
    Description      text       @24
    Tools            list<text> @32
    Status           text       @40
    ExecutionMode    text       @48
    Schedule         text       @56
    ComputeRef       text       @64
    ServiceAccountID text       @72
    Avatar           text       @80
    Emoji            text       @88
    Runs             i64        @96
    CreatedAt        text       @104
    UpdatedAt        text       @112
    CapMicroUSD      i64        @120
    MaxTaskMicroUSD  i64        @128
    Period           text       @136
    ConsumedMicroUSD i64        @144
}

struct buildList {
    Builds list<bytes> @0
}

struct buildRef {
    Org     text @0
    Project text @8
}

struct buildView {
    Org       text        @0
    Project   text        @8
    Session   text        @16
    Title     text        @24
    Agent     text        @32
    Status    text        @40
    Repo      text        @48
    Model     text        @56
    StartedAt text        @64
    EndedAt   text        @72
    Turns     list<bytes> @80
    Verify    text        @88
}

struct buildsQuery {
    Limit i64 @0
}

struct claimKeyOut {
    TargetID text @0
    ClaimKey text @8
}

struct controlDrain {
    Commands list<bytes> @0
    Cursor   i64         @8
}

struct controlDrainIn {
    ID    text @0
    After i64  @8
}

struct controlIn {
    ID      text  @0
    Message text  @8
    Payload bytes @16
}

struct controlResult {
    Command   text  @0
    Event     bytes @8
    Forwarded bool  @16
}

struct createAgentIn {
    Name             text       @0
    Model            text       @8
    Instructions     text       @16
    Description      text       @24
    Tools            list<text> @32
    ExecutionMode    text       @40
    Schedule         text       @48
    ComputeRef       text       @56
    ServiceAccountID text       @64
    Avatar           text       @72
    Emoji            text       @80
    CapMicroUSD      i64        @88
    MaxTaskMicroUSD  i64        @96
    Period           text       @104
}

struct eventIn {
    ID      text  @0
    Kind    text  @8
    Payload bytes @16
    Actor   text  @24
}

struct eventView {
    ID        text  @0
    SessionID text  @8
    Seq       i64   @16
    Kind      text  @24
    Actor     text  @32
    Payload   bytes @40
    CreatedAt text  @48
}

struct metricsQuery {
    Range text @0
}

struct metricsView {
    Range    text        @0
    Series   list<bytes> @8
    Resource bytes       @16
}

struct orgRunsQuery {
    Limit  i64  @0
    Status text @8
}

struct patchSessionIn {
    ID        text @0
    Status    text @8
    Title     text @16
    Target    text @24
    Terminal  text @32
    Project   text @40
    Published bool @48
    Cwd       text @56
}

struct patchTargetIn {
    ID       text  @0
    Label    text  @8
    Kind     text  @16
    Status   text  @24
    Capacity text  @32
    Host     text  @40
    Spec     bytes @48
    Metrics  bytes @56
}

struct registerReq {
    Agent           text @0
    Actor           text @8
    Title           text @16
    Status          text @24
    ParentSessionID text @32
    TaskWorkflowID  text @40
    TaskRunID       text @48
    Host            text @56
    Cwd             text @64
    Repo            text @72
    Target          text @80
    Terminal        text @88
    Provider        text @96
    Account         text @104
    Room            text @112
    Project         text @120
    Published       bool @128
    BudgetMicroUSD  i64  @136
}

struct reportOut {
    Delivered bool @0
}

struct reportRunIn {
    ID        text @0
    RunID     text @8
    OK        bool @16
    Changed   bool @17
    Branch    text @24
    CommitSha text @32
    Diffstat  text @40
    Error     text @48
}

struct routedRunOut {
    SessionID      text @0
    Repo           text @8
    Project        text @16
    Base           text @24
    Branch         text @32
    Prompt         text @40
    CloneURL       text @48
    TimeoutSeconds i64  @56
}

struct runList {
    Runs list<bytes> @0
}

struct runsQuery {
    Ref   text @0
    Limit i64  @8
}

struct sessionBudgetIn {
    ID             text @0
    BudgetMicroUSD i64  @8
}

struct sessionBudgetView {
    ID               text @0
    Status           text @8
    BudgetMicroUSD   i64  @16
    BudgetRemoved    bool @24
    ConsumedMicroUSD i64  @32
}

struct sessionDetail {
    Children     list<bytes> @0
    RecentEvents list<bytes> @8
}

struct sessionList {
    Sessions list<bytes> @0
}

struct sessionProgress {
    Pct       i64  @0
    Phase     text @8
    Activity  text @16
    At        text @24
    Estimated bool @32
}

struct sessionQuery {
    Root    text @0
    Parent  text @8
    Status  text @16
    Project text @24
    Room    text @32
    Limit   i64  @40
}

struct sessionRef {
    ID text @0
}

struct sessionView {
    ID              text  @0
    Org             text  @8
    Agent           text  @16
    Actor           text  @24
    Status          text  @32
    ParentSessionID text  @40
    RootSessionID   text  @48
    Title           text  @56
    TaskWorkflowID  text  @64
    TaskRunID       text  @72
    Host            text  @80
    Cwd             text  @88
    Repo            text  @96
    Terminal        text  @104
    Target          text  @112
    Provider        text  @120
    Account         text  @128
    Room            text  @136
    Project         text  @144
    Published       bool  @152
    Events          i64   @160
    Children        i64   @168
    StartedAt       text  @176
    EndedAt         text  @184
    CreatedAt       text  @192
    UpdatedAt       text  @200
    LastEvent       bytes @208
    Progress        bytes @216
}

struct spendQuery {
    Ref text @0
    By  text @8
}

struct targetDeleted {
    Deleted bool @0
    ID      text @8
}

struct targetList {
    Targets list<bytes> @0
}

struct targetRef {
    ID text @0
}

struct targetReq {
    Label    text  @0
    Kind     text  @8
    Status   text  @16
    Capacity text  @24
    Host     text  @32
    Spec     bytes @40
    Metrics  bytes @48
}

struct targetView {
    ID        text  @0
    Label     text  @8
    Kind      text  @16
    Status    text  @24
    Capacity  text  @32
    Host      text  @40
    Spec      bytes @48
    Metrics   bytes @56
    MetricsAt text  @64
    Sessions  i64   @72
    Running   i64   @80
    CreatedAt text  @88
    UpdatedAt text  @96
}

struct updateAgentIn {
    Ref              text       @0
    Model            text       @8
    Instructions     text       @16
    Description      text       @24
    Tools            list<text> @32
    ExecutionMode    text       @40
    Schedule         text       @48
    ComputeRef       text       @56
    ServiceAccountID text       @64
    Avatar           text       @72
    Emoji            text       @80
    CapMicroUSD      i64        @88
    MaxTaskMicroUSD  i64        @96
    Period           text       @104
}

interface agent {
    # Removes an agent and every run recorded against it. Answers 204.
    delete_agent_by_ref(req: agentRef)
    # Deregisters one machine. Only its owner, or an org admin, may
    # remove it; an unknown id, a cross-org id and a machine owned by someone else
    # all answer the same not-found, so a probe learns nothing about what exists.
    delete_agent_targets_by_id(req: targetRef) returns (rep: targetDeleted)
    # Returns every agent defined in the caller's org, each with the
    # number of runs recorded against it.
    get_agent() returns (rep: agentList)
    # Serves the org-wide recent-activity feed. Events are REAL: each
    # recorded run is an invoked (ok) or failed (error) event; each agent's own
    # create/update timestamps are created/updated events. Merged, newest first,
    # capped. Nothing is invented — an org with no agents and no runs gets [].
    get_agent_activity() returns (rep: activityFeed)
    # Returns the public index of every published build, most recently
    # updated first, so a gallery can link straight to the story behind each product.
    # PUBLIC, no tenancy: publishing is the author's act, and only published root
    # sessions appear here.
    get_agent_builds(req: buildsQuery) returns (rep: buildList)
    # Returns the readable build of one product: the agent session that
    # produced it, turn by turn — the prompts, the reasoning, the commits each turn
    # produced — plus the exact `git log` that re-derives every commit binding from
    # git itself, so nothing here has to be taken on trust.
    # PUBLIC, no tenancy: it answers only for a session its author explicitly
    # published, which is what makes it safe to be anonymous. An unpublished session
    # is invisible here no matter who asks; its owner reads it through the org-scoped
    # /v1/agent/sessions routes, which need a validated principal.
    get_agent_builds_by_org_by_project(req: buildRef) returns (rep: buildView)
    # Returns one agent with its system prompt and its 20 most recent runs.
    # The ref is the agent's public id or its org-unique name — a created agent is
    # immediately gettable by whatever create handed back.
    get_agent_by_ref(req: agentRef) returns (rep: agentDetail)
    # Returns one agent's execution history, newest first — each run's
    # input, its output or its error, and how long it took. Every row is a run that
    # actually happened.
    get_agent_by_ref_runs(req: runsQuery) returns (rep: runList)
    # Serves the invocations-over-time histogram for the org's Agents
    # dashboard. Every point is a REAL count of recorded runs in that time bucket —
    # one series line per agent that ran in the window. The Resource Usage rollup is
    # all-null because this store meters no CPU/memory/storage/cost; the console
    # renders those as "—" rather than a fabricated figure. No runs => empty series
    # (an honest "not connected / no activity yet"), never a synthesized trend.
    get_agent_metrics(req: metricsQuery) returns (rep: metricsView)
    # Returns the org's agent runs across EVERY agent, newest first —
    # what ran here, for whom, on which model, how long it took, and why it failed.
    # It is the feed the per-agent history could not be: an operator asking "what is
    # this tenant's agent plane doing" does not start out knowing an agent ref, and
    # answering by listing the agents and then paging each one's history is N+1 round
    # trips to reconstruct one ordering the database already has (RunsSince, ordered
    # by created_at over the org index).
    # The org is the CALLER's, resolved from identity by tenantStore — never a
    # parameter. There is deliberately no org field on orgRunsQuery to forge: run
    # history is the tenant's own record, and the only tenant this can answer for is
    # the one asking.
    get_agent_runs(req: orgRunsQuery) returns (rep: runList)
    # Returns the caller org's live sessions, newest first — each with
    # its event count, its direct-child count and a one-line preview of its latest
    # event. With no filter it returns ROOT sessions only, so a dashboard shows one
    # row per flow rather than one per subagent; ?root= or ?parent= descends.
    get_agent_sessions(req: sessionQuery) returns (rep: sessionList)
    # Returns one session with its direct child sessions and its 50 most
    # recent events, oldest of those first.
    get_agent_sessions_by_id(req: sessionRef) returns (rep: sessionDetail)
    # Returns the steering commands (pause/resume/stop/message)
    # recorded against the caller's own session that are newer than the cursor,
    # oldest first, with the cursor to poll from next. It is how a locally started
    # `hanzo code` session — which is not task-backed, so nothing forwards its
    # commands to an execution engine — consumes what the dashboard posted. Read-only
    # and bounded at 200 per poll, so a steady poll is cheap and an applied command is
    # never redelivered.
    get_agent_sessions_by_id_control(req: controlDrainIn) returns (rep: controlDrain)
    # Returns how far along one run is: the share of its goal that
    # is done, whether it is running, blocked or finished, and a line saying what it
    # is doing right now.
    # It is a MODEL ESTIMATE read off the run's own transcript, not a measurement —
    # `estimated` says so on every answer, and a run whose progress cannot be told
    # reports phase "unknown" with no percentage rather than a zero it does not
    # mean. A session that has already finished answers from its own status instead,
    # and is marked not estimated.
    # The list and detail reads carry the same value; this address is the one that
    # WAITS. Where the stored estimate has gone stale it is remade before answering,
    # so a human deciding whether to step into a run gets a current reading rather
    # than the last poll's — which costs one small completion, charged to the same
    # wallet the session already names, at most once every thirty seconds per run.
    get_agent_sessions_by_id_progress(req: sessionRef) returns (rep: sessionProgress)
    # Returns every machine registered to the caller's org, newest
    # first, each with its live session load.
    get_agent_targets() returns (rep: targetList)
    # Returns one registered machine, with its live session load.
    get_agent_targets_by_id(req: targetRef) returns (rep: targetView)
    # Changes an agent in place. Every field is optional; a field the
    # request omits keeps its stored value. The resulting mode+schedule are
    # re-validated together, so a partial update can never leave a long-running
    # agent without the cron the scheduler needs to fire it, and a transition INTO
    # long-running counts against the per-org cap on scheduled agents.
    patch_agent_by_ref(req: updateAgentIn) returns (rep: agentView)
    # Updates a session's surface-owned truth: its status, its title,
    # the run-target it is dispatched to, and the product it built plus whether that
    # build's story is public. A FINISHED session stays finished — reopening a
    # done/error run would fabricate liveness — and publishing is refused unless the
    # session names the project it built, because the public build route is keyed on
    # (org, project).
    patch_agent_sessions_by_id(req: patchSessionIn) returns (rep: sessionView)
    # Updates one machine in place. Every field is optional; a field the
    # request omits is left alone. A metrics patch IS a heartbeat — the server stamps
    # its own clock, so a client can neither forge nor backdate staleness.
    patch_agent_targets_by_id(req: patchTargetIn) returns (rep: targetView)
    # Defines an agent in the caller's org: a model, a system prompt
    # (instructions) and a set of tool names. The name must be unique in the org and
    # match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$. An omitted model takes the
    # deployment's configured default; a named one is checked against the gateway's
    # served catalog, so a model this deployment never serves is refused here rather
    # than failing at run time. A long-running agent must carry a 5-field cron
    # schedule (the scheduler would otherwise never fire it) and counts against a
    # per-org cap on scheduled agents.
    post_agent(req: createAgentIn) returns (rep: agentView)
    post_agent_coding(req: CodingStartIn) returns (rep: CodingStarted)
    # Opens a live agent session in the caller's org — the row every
    # surface (the CLI's outer agent, hanzo.bot, the console, chat) hangs its
    # activity off. A session with a parentSessionId becomes a subagent of that
    # session and inherits its root, so one flow is one tree; without one it is
    # itself a root. Registering with a terminal status records a session that has
    # already finished.
    post_agent_sessions(req: registerReq) returns (rep: sessionView)
    # Sets, raises, or removes a session's cap.
    # - a replacement must be strictly greater than what the session has consumed
    # - removal is one-way: a session whose cap was removed cannot take one again,
    # and a session created without one cannot be given one
    # - raising or removing the cap resumes work that paused at it
    post_agent_sessions_by_id_budget(req: sessionBudgetIn) returns (rep: sessionBudgetView)
    # Records one turn of a session's transcript and answers 201 with it.
    # A `progress` turn additionally MOVES THE SESSION'S PROGRESS, marked as the run's
    # own word rather than an estimate, and pushes the updated session onto the live
    # stream — so a board's bar follows the run without polling and without a second
    # write path. See progress.go.
    # THE TURN IS SCANNED BEFORE IT IS STORED. The same engine the code-security
    # surface runs reads the payload at this boundary, and a credential in it refuses
    # the append with 422 rather than redacting it — a redacted transcript is one
    # that still had the secret in it once, and this way the author learns which
    # value to rotate. The refusal carries every finding: the rule, the severity, the
    # line, a MASKED preview and the fingerprint. The secret is never in the answer.
    post_agent_sessions_by_id_events(req: eventIn) returns (rep: eventView)
    # Sends a steering message to a running session — the endpoint a
    # human or another agent interrupts through. It requires a `message` or a
    # `payload`; the other three commands do not.
    post_agent_sessions_by_id_message(req: controlIn) returns (rep: controlResult)
    # Asks a running session to pause. Recorded durably, and forwarded
    # to the durable-execution engine when the session is task-backed.
    post_agent_sessions_by_id_pause(req: controlIn) returns (rep: controlResult)
    # Asks a paused session to continue, on the same terms as a pause.
    post_agent_sessions_by_id_resume(req: controlIn) returns (rep: controlResult)
    # Ends a running session. `message` is recorded as the cancellation
    # reason, which is what a later reader of the transcript sees.
    # STOPPING IS NOT DELETING: the session, its transcript and anything it produced
    # stay readable. A session that has already finished is 409 rather than a second
    # stop.
    post_agent_sessions_by_id_stop(req: controlIn) returns (rep: controlResult)
    # Registers a machine as an agent target, or re-links one that is
    # already registered. Re-linking is idempotent and keyed on org+host+owner, so a
    # machine that reconnects refreshes its own row rather than piling up duplicates;
    # it answers 200, while a first registration answers 201.
    post_agent_targets(req: targetReq) returns (rep: targetView)
    # ClaimRoutedRun is the machine's long poll for work: it authenticates the
    # daemon, stamps the liveness the dispatch gate reads (the poll IS the proof a
    # runner is listening), and waits up to 25 seconds for the next run addressed to
    # THIS machine. It answers the run when one arrives and 204 with no body when the
    # window elapses, on which the daemon re-polls immediately.
    # TWO independent proofs are required and both fail closed to the same 403: the
    # caller must own this machine (or be an org admin) AND present its claim key in
    # X-Target-Key. A run offered to one machine is unreachable from another's claim.
    post_agent_targets_by_id_claim(req: targetRef) returns (rep: routedRunOut)
    # Mints (or rotates) the claim key a `hanzo code --serve`
    # daemon presents to claim work for this machine, and returns it ONCE: only its
    # SHA-256 hash is stored. Rotating supersedes any prior daemon, so only the
    # machine's owner — or an org admin — may call it; every other caller gets the
    # same not-found an unknown id gets, and learns nothing about what exists.
    post_agent_targets_by_id_key(req: targetRef) returns (rep: claimKeyOut)
    # Completes a claimed run: it delivers the terminal result to the
    # run's durable owner, which is what lets that workflow finish. Scoped to (org,
    # target, run) and claim-key authenticated, so a machine can only ever report a
    # run it legitimately holds. Idempotent — a report for an unknown or
    # already-finished run answers delivered:false rather than failing, because the
    # session's terminal state was already set by the machine's own stream.
    post_agent_targets_by_id_runs_by_runid_report(req: reportRunIn) returns (rep: reportOut)
}

# ---------------------------------------------------------------------
# 32 op(s) here. What follows is what this schema does not carry.
#
# dropped (2) — the value does not cross, and nothing fails:
#   agentDetail.agentView  agents.agentView  (promoted, not carried)
#   sessionDetail.sessionView  agents.sessionView  (promoted, not carried)
#
# blocked (2) — the op is absent; the field has no wire form:
#   get_agent_by_ref_spend  spendView.ByComponent  map[string]int64  (map)
#   get_agent_sessions_by_id_tree  treeNode.Children  []agents.treeNode  (no wire form)
#
# opaque (22) — crosses, arrives without its name:
#   activityFeed.Activity  agents.activityView (list element)
#   agentDetail.RecentRuns  agents.agentRunView (list element)
#   agentList.Agents  agents.agentView (list element)
#   buildList.Builds  agents.buildSummary (list element)
#   buildView.Turns  agents.buildTurn (list element)
#   controlDrain.Commands  agents.controlCommandView (list element)
#   controlResult.Event  agents.eventView
#   metricsView.Resource  agents.resourceUsage
#   metricsView.Series  agents.seriesLine (list element)
#   patchTargetIn.Metrics  agents.Metrics
#   patchTargetIn.Spec  agents.Spec
#   runList.Runs  agents.agentRunView (list element)
#   sessionDetail.Children  agents.sessionView (list element)
#   sessionDetail.RecentEvents  agents.eventView (list element)
#   sessionList.Sessions  agents.sessionView (list element)
#   sessionView.LastEvent  agents.lastEventView
#   sessionView.Progress  agents.sessionProgress
#   targetList.Targets  agents.targetView (list element)
#   targetReq.Metrics  agents.Metrics
#   targetReq.Spec  agents.Spec
#   targetView.Metrics  agents.Metrics
#   targetView.Spec  agents.Spec
