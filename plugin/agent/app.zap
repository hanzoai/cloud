# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package agent

struct BriefRecord {
    ID                text        @0
    Venture           text        @8
    Title             text        @16
    Question          text        @24
    LeadAgentID       text        @32
    LeadAgent         text        @40
    LeadRunID         text        @48
    Status            text        @56
    Synthesis         text        @64
    Citations         list<text>  @72
    UnsupportedClaims list<text>  @80
    OpenQuestions     list<text>  @88
    Attribution       list<bytes> @96
    Tasks             list<bytes> @104
    TotalMicroUSD     i64         @112
    SkippedCompleted  i64         @120
    RerunsPerformed   i64         @128
    CreatedAt         i64         @136
    UpdatedAt         i64         @144
}

struct CodingAck {
    OK     bool @0
    Queued bool @1
}

struct CodingArtifacts {
    Session   text        @0
    Saved     text        @8
    Artifacts list<bytes> @16
}

struct CodingBlob {
    Path      text @0
    Size      i64  @8
    Encoding  text @16
    Content   text @24
    Binary    bool @32
    Truncated bool @33
}

struct CodingChanges {
    Repo        text        @0
    Base        text        @8
    Head        text        @16
    Commits     list<bytes> @24
    Files       list<bytes> @32
    Pull        bytes       @40
    MoreCommits bool        @48
    MoreFiles   bool        @49
    Steers      list<bytes> @56
}

struct CodingExtendIn {
    Session   text @0
    Seconds   i64  @8
    RequestID text @16
}

struct CodingExtended {
    Until   text @0
    Seconds i64  @8
}

struct CodingMergeIn {
    Session   text @0
    RequestID text @8
}

struct CodingMerged {
    Number i64  @0
    URL    text @8
    State  text @16
    Base   text @24
}

struct CodingPathIn {
    Session text @0
    Path    text @8
}

struct CodingPullIn {
    Session   text       @0
    Title     text       @8
    Body      text       @16
    Base      text       @24
    Draft     bool       @32
    Assignees list<text> @40
    Reviewers list<text> @48
    RequestID text       @56
}

struct CodingPulled {
    Number  i64  @0
    URL     text @8
    Base    text @16
    Warning text @24
}

struct CodingRunIn {
    Session text @0
}

struct CodingStartIn {
    Repo                  text       @0
    Prompt                text       @8
    Project               text       @16
    Base                  text       @24
    After                 text       @32
    Conversation          text       @40
    Branch                text       @48
    Pull                  bool       @56
    Draft                 bool       @57
    Assignees             list<text> @64
    Reviewers             list<text> @72
    AgentRef              text       @80
    TargetID              text       @88
    Dir                   text       @96
    TimeoutSeconds        i64        @104
    Tool                  text       @112
    Desktop               bool       @120
    Reply                 bytes      @128
    Task                  text       @136
    Mode                  text       @144
    Model                 text       @152
    Effort                text       @160
    Issue                 text       @168
    RequestID             text       @176
    Instructions          text       @184
    DeveloperInstructions text       @192
    ReadOnly              bool       @200
}

struct CodingStarted {
    SessionID text @0
    Branch    text @8
    Repo      text @16
    Routed    bool @24
    TargetID  text @32
    Pull      text @40
    Carried   i64  @48
    Left      i64  @56
    Dropped   i64  @64
    Note      text @72
}

struct CodingTree {
    Ref     text        @0
    Entries list<bytes> @8
}

struct RoleProposal {
    ID                text       @0
    Venture           text       @8
    Name              text       @16
    Title             text       @24
    Function          text       @32
    ReportingLine     text       @40
    Rationale         text       @48
    Examples          list<text> @56
    EstimatedMicroUSD i64        @64
    CapMicroUSD       i64        @72
    MaxTaskMicroUSD   i64        @80
    Period            text       @88
    Tools             list<text> @96
    Instructions      text       @104
    Status            text       @112
    AgentID           text       @120
    DecidedBy         text       @128
    DecisionReason    text       @136
    CreatedAt         i64        @144
    DecidedAt         i64        @152
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
    AvatarSource     text       @96
    AvatarLicence    text       @104
    Runs             i64        @112
    CreatedAt        text       @120
    UpdatedAt        text       @128
    CapMicroUSD      i64        @136
    MaxTaskMicroUSD  i64        @144
    Period           text       @152
    ConsumedMicroUSD i64        @160
    Parent           text       @168
    Lineage          list<text> @176
    IsPrimary        bool       @184
    GroupID          text       @192
    Layer            i64        @200
}

struct answerIn {
    ID       text @0
    Question text @8
    Text     text @16
    Chosen   bool @24
}

struct answerView {
    Question text @0
    Text     text @8
}

struct applyRosterIn {
    Venture          text        @0
    TemplateVersion  text        @8
    SeedDefaultSeven bool        @16
    Roles            list<bytes> @24
}

struct applyRosterOut {
    Venture         text        @0
    TemplateVersion text        @8
    Created         i64         @16
    Updated         i64         @24
    Agents          list<bytes> @32
}

struct bootstrapPrimaryIn {
    Name  text @0
    Model text @8
}

struct bootstrapPrimaryOut {
    Agent   bytes @0
    Created bool  @8
}

struct briefList {
    Briefs list<bytes> @0
}

struct briefRef {
    ID text @0
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

struct codingInstruction {
    Session   text @0
    Text      text @8
    RequestID text @16
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
    Run       text  @24
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
    AvatarSource     text       @88
    AvatarLicence    text       @96
    CapMicroUSD      i64        @104
    MaxTaskMicroUSD  i64        @112
    Period           text       @120
    Parent           text       @128
    IsPrimary        bool       @136
    GroupID          text       @144
    Layer            i64        @152
}

struct createGroupIn {
    Name          text       @0
    Description   text       @8
    Layer         i64        @16
    ParentGroupID text       @24
    Members       list<text> @32
    Policy        bytes      @40
}

struct decideProposalIn {
    ID       text @0
    Decision text @8
    Reason   text @16
}

struct eventIn {
    ID      text  @0
    Kind    text  @8
    Payload bytes @16
    Actor   text  @24
}

struct eventList {
    Events list<bytes> @0
    Next   i64         @8
}

struct eventPage {
    ID    text @0
    After i64  @8
    Limit i64  @16
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

struct folderIn {
    ID     text @0
    Folder text @8
}

struct folderList {
    Folders list<bytes> @0
}

struct folderRevoked {
    Revoked bool @0
    Folder  text @8
}

struct folderView {
    Folder    text @0
    GrantedBy text @8
    GrantedAt text @16
}

struct groupList {
    Groups list<bytes> @0
}

struct groupRef {
    Ref text @0
}

struct groupView {
    ID            text       @0
    Name          text       @8
    Description   text       @16
    Layer         i64        @24
    ParentGroupID text       @32
    Members       list<text> @40
    Policy        bytes      @48
    CreatedAt     text       @56
    UpdatedAt     text       @64
}

struct listProposalsQuery {
    Venture text @0
    Status  text @8
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
    ID        text       @0
    Status    list<text> @8
    Title     list<text> @16
    Target    list<text> @24
    Terminal  list<text> @32
    Project   list<text> @40
    Published list<bool> @48
    Error     list<text> @56
    Cwd       list<text> @64
}

struct patchTargetIn {
    ID       text       @0
    Label    list<text> @8
    Kind     list<text> @16
    Status   list<text> @24
    Capacity list<text> @32
    Host     list<text> @40
    Spec     bytes      @48
    Metrics  bytes      @56
}

struct proposalList {
    Proposals list<bytes> @0
}

struct registerReq {
    Agent           text @0
    Title           text @8
    Status          text @16
    ParentSessionID text @24
    TaskWorkflowID  text @32
    TaskRunID       text @40
    Host            text @48
    Cwd             text @56
    Repo            text @64
    Target          text @72
    Terminal        text @80
    Provider        text @88
    Account         text @96
    Channel         text @104
    Project         text @112
    Published       bool @120
    BudgetMicroUSD  i64  @128
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

struct reviewProposalsIn {
    Venture          text        @0
    FailureThreshold i64         @8
    ObservedGaps     list<bytes> @16
}

struct reviewProposalsOut {
    Created   i64         @0
    Proposals list<bytes> @8
}

struct rosterTemplatesOut {
    Version   text        @0
    Functions list<bytes> @8
    Templates list<bytes> @16
}

struct routedRunOut {
    SessionID      text @0
    Repo           text @8
    Project        text @16
    Base           text @24
    Branch         text @32
    Prompt         text @40
    CloneURL       text @48
    DeployKey      text @56
    KnownHosts     text @64
    TimeoutSeconds i64  @72
    Model          text @80
    Effort         text @88
    Instructions   text @96
    Dir            text @104
    ReadOnly       bool @112
}

struct runGroupIn {
    Ref   text @0
    Input text @8
}

struct runGroupOut {
    ID         text        @0
    GroupID    text        @8
    Group      text        @16
    Layer      i64         @24
    Policy     text        @32
    Status     text        @40
    Output     text        @48
    Error      text        @56
    DurationMs i64         @64
    Votes      list<bytes> @72
    Quasar     bytes       @80
}

struct runList {
    Runs list<bytes> @0
}

struct runsQuery {
    Ref   text @0
    Limit i64  @8
}

struct sessionBudgetIn {
    ID             text      @0
    BudgetMicroUSD list<i64> @8
}

struct sessionBudgetView {
    ID               text @0
    Status           text @8
    BudgetMicroUSD   i64  @16
    BudgetRemoved    bool @24
    ConsumedMicroUSD i64  @32
}

struct sessionDeleted {
    Deleted bool @0
    ID      text @8
}

struct sessionDetail {
    Children     list<bytes> @0
    RecentEvents list<bytes> @8
    Next         i64         @16
}

struct sessionDetailIn {
    ID    text @0
    After text @8
    Limit i64  @16
}

struct sessionList {
    Sessions list<bytes> @0
    Next     text        @8
}

struct sessionProgress {
    Pct       list<i64> @0
    Phase     text      @8
    Activity  text      @16
    At        text      @24
    Estimated bool      @32
}

struct sessionQuery {
    Root    text @0
    Parent  text @8
    Status  text @16
    Project text @24
    Channel text @32
    Kind    text @40
    Limit   i64  @48
    After   text @56
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
    Channel         text  @136
    Project         text  @144
    Published       bool  @152
    Kind            text  @160
    Base            text  @168
    Branch          text  @176
    Environment     text  @184
    PR              text  @192
    Mode            text  @200
    Sandbox         text  @208
    Events          i64   @216
    Children        i64   @224
    StartedAt       text  @232
    EndedAt         text  @240
    CreatedAt       text  @248
    UpdatedAt       text  @256
    LastEvent       bytes @264
    Progress        bytes @272
    Error           text  @280
    Park            bytes @288
}

struct spendQuery {
    Ref text @0
    By  text @8
}

struct submitBriefIn {
    Venture     text       @0
    Title       text       @8
    Question    text       @16
    Lead        text       @24
    Specialists list<text> @32
    FailOnce    list<text> @40
}

struct talkIn {
    Session               text @0
    Input                 text @8
    Model                 text @16
    Effort                text @24
    Repo                  text @32
    Base                  text @40
    Target                text @48
    Mode                  text @56
    Issue                 i64  @64
    Instructions          text @72
    DeveloperInstructions text @80
}

struct talkOut {
    SessionID text @0
    RunID     text @8
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
    ID        text       @0
    Label     text       @8
    Kind      text       @16
    Status    text       @24
    Capacity  text       @32
    Host      text       @40
    Spec      bytes      @48
    Metrics   bytes      @56
    MetricsAt text       @64
    Sessions  i64        @72
    Running   i64        @80
    CreatedAt text       @88
    UpdatedAt text       @96
    Serving   bool       @104
    Folders   list<text> @112
}

struct updateAgentIn {
    Ref              text       @0
    Model            list<text> @8
    Instructions     list<text> @16
    Description      list<text> @24
    Tools            list<text> @32
    ExecutionMode    list<text> @40
    Schedule         list<text> @48
    ComputeRef       list<text> @56
    ServiceAccountID list<text> @64
    Avatar           list<text> @72
    Emoji            list<text> @80
    AvatarSource     list<text> @88
    AvatarLicence    list<text> @96
    CapMicroUSD      list<i64>  @104
    MaxTaskMicroUSD  list<i64>  @112
    Period           list<text> @120
    IsPrimary        list<bool> @128
    GroupID          list<text> @136
    Layer            list<i64>  @144
}

struct updateGroupIn {
    Ref           text       @0
    Description   list<text> @8
    Layer         list<i64>  @16
    ParentGroupID list<text> @24
    Members       list<text> @32
    Policy        bytes      @40
}

interface agent {
    # Removes an agent and every run recorded against it. Answers 204.
    delete_agent_by_ref(req: agentRef)
    # Deletes a Bot Group and unassigns its member bots without
    # deleting the bots themselves. Answers 204.
    delete_agent_groups_by_ref(req: groupRef)
    # Stops a session if it is still live, settles any unbilled
    # runtime, and removes the session, its subagent subtree, and their recorded
    # events from the caller org's registry.
    # Only the session's owner — or, in a customer org for non-private sessions, an
    # org admin or SuperAdmin — may delete it; another member's session, a
    # cross-tenant id, and an unknown id all answer 404.
    delete_agent_sessions_by_id(req: sessionRef) returns (rep: sessionDeleted)
    # Deregisters one machine. Only its owner, or an org admin, may
    # remove it; an unknown id, a cross-org id and a machine owned by someone else
    # all answer the same not-found, so a probe learns nothing about what exists.
    delete_agent_targets_by_id(req: targetRef) returns (rep: targetDeleted)
    # Ends a folder's grant on this machine. Only the machine's owner
    # may revoke, from the machine or the web. The machine reads its folders on
    # every heartbeat and drops this one, and a queued run in it is refused when
    # the machine claims it. A folder with no active grant is 404.
    delete_agent_targets_by_id_folders(req: folderIn) returns (rep: folderRevoked)
    # Returns every agent defined in the caller's org, each with the
    # number of runs recorded against it. A retired built-in is left out.
    get_agent() returns (rep: agentList)
    # Serves the recent-activity feed. Events are REAL: each of the
    # caller's own recorded runs is an invoked (ok) or failed (error) event; each of
    # the org's agents' create/update timestamps are created/updated events. Merged, newest first,
    # capped. Nothing is invented — an org with no agents and no runs gets [].
    get_agent_activity() returns (rep: activityFeed)
    # Returns every durable research brief recorded in the caller's
    # organization, newest first.
    get_agent_briefs() returns (rep: briefList)
    # Returns one durable research brief and all of its specialist child
    # tasks, citations, unsupported claims, open questions, and attribution records.
    get_agent_briefs_by_id(req: briefRef) returns (rep: BriefRecord)
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
    # Returns one agent with its system prompt and the caller's own 20 most
    # recent runs of it: a run's input and output are its person's.
    # The ref is the agent's public id or its org-unique name — a created agent is
    # immediately gettable by whatever create handed back.
    get_agent_by_ref(req: agentRef) returns (rep: agentDetail)
    # Returns the caller's own runs of one agent, newest first — each
    # run's input, its output or its error, and how long it took. A run is the
    # person's who asked for it, so another member's runs are not listed, nor an
    # org admin's view of them; a run no person asked for (a schedule) is listed to a
    # SuperAdmin alone. Every row is a run that actually happened.
    get_agent_by_ref_runs(req: runsQuery) returns (rep: runList)
    # Lists what a coding run left, kept after its sandbox is gone: every file it
    # added or changed and its whole change as `changes.patch`, stored beside the run;
    # the ports it served, each a preview while its sandbox is kept; its pull request
    # and where its work was published. A run that is still working, or saved
    # nothing, answers an empty list.
    get_agent_coding_by_session_artifacts(req: CodingRunIn) returns (rep: CodingArtifacts)
    # Returns one file of a coding run's repository, read where the tree is read,
    # in the shape GET /v1/git/repos/{name}/blob answers: text verbatim, anything
    # else base64, and a file past the 1 MiB view cap marked truncated with no
    # content.
    get_agent_coding_by_session_blob(req: CodingPathIn) returns (rep: CodingBlob)
    # Returns what a coding run changed, read from the forge it pushed its branch
    # to: the commits on its branch that the base does not have, newest first; the
    # net change of the branch against its base, one entry per file with that file's
    # patch; and its pull request with the reviews it has had, or null while it has
    # none. `steers` lists the instructions said to the run while no pod held it, each
    # queued until the run took it and delivered after.
    # A run whose branch is not on the forge yet — still working, or finished with
    # nothing to change — answers with no commits, no files and no pull request.
    # Every read is made as the caller, so a repository they cannot open on the
    # forge is not found here either, whoever can see the run.
    # One answer is bounded, and says where it was cut rather than failing: the
    # newest 250 commits (`moreCommits` when there are more), the change up to 8 MiB
    # of diff or 3000 files (`moreFiles`, the last file marked truncated), and the
    # first 50 reviews (`moreReviews`), each body up to 16 KiB and 256 KiB across
    # them (`truncated` on a cut one). A caller has at most two of these reads in
    # flight and is answered 429 past that; two asking for the same change at once
    # share one read.
    get_agent_coding_by_session_changes(req: CodingRunIn) returns (rep: CodingChanges)
    # Lists one directory of a coding run's repository, one level down with
    # directories first: at the run's own branch once the forge holds it, and at the
    # branch it started from until then — `ref` says which. Walk down a level at a
    # time; an empty path is the root.
    get_agent_coding_by_session_tree(req: CodingPathIn) returns (rep: CodingTree)
    # Returns every Bot Group defined in the caller's org, ordered
    # from Layer 1 downward.
    get_agent_groups() returns (rep: groupList)
    # Returns one Bot Group by its public grp_... id or org-unique
    # name.
    get_agent_groups_by_ref(req: groupRef) returns (rep: groupView)
    # Serves the invocations-over-time histogram for the Agents
    # dashboard, over the caller's own runs. Every point is a REAL count of recorded
    # runs in that time bucket —
    # one series line per agent that ran in the window. The Resource Usage rollup is
    # all-null because this store meters no CPU/memory/storage/cost; the console
    # renders those as "—" rather than a fabricated figure. No runs => empty series
    # (an honest "not connected / no activity yet"), never a synthesized trend.
    get_agent_metrics(req: metricsQuery) returns (rep: metricsView)
    # Returns the organization's nightly role candidate proposals,
    # optionally filtered by venture slug or decision status.
    get_agent_proposals(req: listProposalsQuery) returns (rep: proposalList)
    # Returns the seven canonical studio functions and the shared,
    # versioned role templates that ventures reuse.
    get_agent_roster_templates() returns (rep: rosterTemplatesOut)
    # Returns the caller's own agent runs across EVERY agent of the org,
    # newest first — what ran, on which model, how long it took, and why it failed.
    # A run is the person's who asked for it: another member's never appears, and a
    # run no person asked for (a schedule) appears to a SuperAdmin alone.
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
    # Returns one session with its direct child sessions and a page of
    # its events, oldest of those first (50 from the tail by default, up to `limit`
    # up to 1000, or forward from `after` with a `next` cursor). The children are
    # those the caller may see, by the rule the session itself is read by: a
    # member's own, and every one for an admin of the org. In a held org, where
    # self-serve signups land beside the platform's staff, a session is its
    # person's alone.
    get_agent_sessions_by_id(req: sessionDetailIn) returns (rep: sessionDetail)
    # Returns the steering commands (pause/resume/stop/message)
    # recorded against the caller's own session that are newer than the cursor,
    # oldest first, with the cursor to poll from next. It is how a locally started
    # `hanzo code` session — which is not task-backed, so nothing forwards its
    # commands to an execution engine — consumes what the dashboard posted. Read-only
    # and bounded at 200 per poll, so a steady poll is cheap and an applied command is
    # never redelivered.
    get_agent_sessions_by_id_control(req: controlDrainIn) returns (rep: controlDrain)
    # Reads a session's log in order, a page at a time: everything after the seq a
    # reader last saw, so a poll resumes exactly where it stopped. It is how to see
    # what a run did and why it ended — each model turn, tool call, status and log
    # line, as the run wrote them. A run whose process went away is ended first,
    # with its reason as the last event.
    get_agent_sessions_by_id_events(req: eventPage) returns (rep: eventList)
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
    # first, each with its live session load. In a held org, where self-serve
    # signups land beside the platform's staff, it is only the caller's own.
    get_agent_targets() returns (rep: targetList)
    # Returns one registered machine, with its live session load.
    get_agent_targets_by_id(req: targetRef) returns (rep: targetView)
    # Returns the folders of this machine the web may start runs in:
    # the picker's choices. Anyone who can see the machine may read them.
    get_agent_targets_by_id_folders(req: targetRef) returns (rep: folderList)
    # Changes an agent in place. Every field is optional; a field the
    # request omits keeps its stored value. The resulting mode+schedule are
    # re-validated together, so a partial update can never leave a long-running
    # agent without the cron the scheduler needs to fire it, and a transition INTO
    # long-running counts against the per-org cap on scheduled agents.
    patch_agent_by_ref(req: updateAgentIn) returns (rep: agentView)
    # Updates a Bot Group's members, hierarchy layer, parent group,
    # or coordination policy in place.
    patch_agent_groups_by_ref(req: updateGroupIn) returns (rep: groupView)
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
    # per-org cap on scheduled agents. An agent spawned by another names it as its
    # parent; the parent must be an agent of the caller's org, and the new agent
    # carries its whole lineage.
    post_agent(req: createAgentIn) returns (rep: agentView)
    # Ensures the caller's org has its single Primary Bot
    # (default name "hanzo", Layer 0, full cloud & computer-use tools) and returns
    # it. Calling bootstrap repeatedly is idempotent: once an org has a Primary Bot,
    # that bot is returned unchanged without creating a second primary.
    post_agent_bootstrap(req: bootstrapPrimaryIn) returns (rep: bootstrapPrimaryOut)
    # Assigns a research brief to a venture lead, dispatches durable
    # child tasks to the designated specialist agents, attributes each specialist's
    # cited findings and unsupported-claim flags, and runs the lead synthesis once
    # all specialist tasks complete. If any specialist fails, completed specialist
    # work is preserved on the brief (`partial_failure`) so `POST /v1/agent/briefs/:id/rerun`
    # re-executes only the failed specialist task.
    post_agent_briefs(req: submitBriefIn) returns (rep: BriefRecord)
    # RerunBrief re-executes only the failed or pending specialist child tasks on a
    # brief without re-running or duplicating specialists that already succeeded,
    # then runs the lead synthesis once all specialist tasks are complete.
    post_agent_briefs_by_id_rerun(req: briefRef) returns (rep: BriefRecord)
    # Runs a coding task on a repository: clones it into a sandbox, lets a model read
    # and edit the code, run the tests, and push the work to a branch. Say the thing
    # you want done — "fix the failing auth test in hanzoai/cloud" — and the run
    # infers the repo, the branch and the plan. No prefix, no ceremony.
    # Name no repo and the run works in an empty workspace of its own: nothing is
    # cloned and nothing is pushed, and what it makes is kept in its sandbox and as
    # its artifacts. Name a repository the caller can read and not push to — a public
    # one — and it is cloned read-only, the same way.
    # It answers 202 with the run's handle the moment the run is ADMITTED — not when
    # it finishes. A coding run takes minutes; holding a request open for one would
    # tie a connection to a model loop and give the caller nothing it cannot get
    # better from the session stream.
    # The handle is a session id, and that is deliberate: the session is already the
    # run's durable record and its live stream (/v1/agent/sessions/stream?root=<id>), so
    # this op does not grow a progress endpoint, a status endpoint or a cancel
    # endpoint of its own. One way to watch a run, whoever started it.
    # It is also how work CONTINUES. Pass an earlier run's session as `after` and
    # this one starts from where that one stopped, so "now add tests for it" builds
    # on the branch already pushed instead of a fresh clone. The follow-up has its
    # own session and writes the same branch, so one piece of work is one branch and
    # one pull request however many instructions it takes.
    # Pass `branch` to name the branch the run writes (`agent/<name>`), and `pull`
    # as false to push it without opening a pull request.
    # Pass `requestId`, or the Idempotency-Key header, to make a retry the same
    # request: one run, whichever pod takes the retry and whatever restarts between,
    # and the retry answers the first's handle. The same id with another request is
    # 422, and an id is at most 256 bytes.
    post_agent_coding(req: CodingStartIn) returns (rep: CodingStarted)
    # Gives your running coding run more time. A run is held to the time it was asked
    # for (`timeoutSeconds`) and, when it is up, keeps its work on its branch and
    # ends; this moves that time later while the run works, so a task expected to
    # take five minutes that is nearly done is given two more rather than restarted.
    # `seconds` is added to the time the run has left, as far as the longest a run
    # may be given; the answer says when the run is up now, so asking for more than
    # that reads what you got.
    # The run must be yours and still working: a run that is paused or already out
    # of time is a conflict (start a new one with `after` set to its session), and
    # one that is not running is not found. A run sent to a machine of your own
    # keeps that machine's time and cannot be extended.
    # `requestId`, or the Idempotency-Key header, makes a retry the same request: the
    # time is added once, and the retry answers what the first answered. The same id
    # with another request is 422.
    post_agent_coding_by_session_extend(req: CodingExtendIn) returns (rep: CodingExtended)
    # Merges a coding run's pull request into the branch it proposes into, on the
    # forge the run pushed to, and answers the pull request after.
    # The caller must be able to read the run: its person, or an admin of its org.
    # What lands is the run's branch at the commit the forge holds when this asks —
    # a push after that is refused, not merged unseen — and nothing is forced: a
    # pull request that conflicts, was closed, or that a rule on its base keeps
    # from merging is 409 with the forge's reason. One already merged answers as
    # it is. `requestId`, or the Idempotency-Key header, makes a retry the same
    # request, answered as the first was; the same id with another request is 422.
    post_agent_coding_by_session_merge(req: CodingMergeIn) returns (rep: CodingMerged)
    # Opens the pull request for the branch a coding run pushed, as the run's person
    # and with no agent run, and answers its number and address.
    # A run pushes its branch and stops; the pull request is the step after, taken
    # when the work is ready. A run that already has an open pull request, or a
    # branch that has one on its host, answers with that one, so asking twice is
    # safe. Title, body, base and draft are optional: the title and body come from
    # the run's task, the base is the branch the run started from, and draft opens it
    # as one a reviewer cannot merge yet. The run's branch must be on its host: a run
    # that pushed nothing, or has not pushed yet, is 409. Who may is who may read the
    # run, and the pull request is read back at GET /v1/agent/coding/{session}/changes.
    # `requestId`, or the Idempotency-Key header, makes a retry the same request,
    # answered as the first was; the same id with another request is 422.
    post_agent_coding_by_session_pull(req: CodingPullIn) returns (rep: CodingPulled)
    # Redirect your running or paused coding session. On an active dev turn the
    # instruction is queued into the running turn in place; otherwise the current
    # run keeps its work before starting a follow-up. Ownership comes from the
    # authenticated credential, never the request body.
    # A run that no pod holds at that moment, between the halves of a rolling restart
    # or while it is resumed, keeps the instruction in its inbox and takes it, once and
    # in order, when its owner is up; GET /v1/agent/coding/{session}/changes lists these
    # as `steers`, queued or delivered, and the answer says `queued` while the run has
    # not taken the instruction. A run that has ended refuses it with 409. `requestId`,
    # or the Idempotency-Key header, makes a retry the same instruction: said once,
    # whichever pod takes the retry and whatever restarts between. The same id with
    # another instruction is 422.
    post_agent_coding_by_session_steer(req: codingInstruction) returns (rep: CodingAck)
    # Defines a multi-bot coordination group at a hierarchy layer
    # under Primary Bot "hanzo" (or nested under a parent group) with a leader, Lux
    # Quasar consensus, or voting policy.
    post_agent_groups(req: createGroupIn) returns (rep: groupView)
    # Executes a prompt across a Bot Group using its configured
    # coordination policy (leader delegation, Lux Quasar consensus with SHA3-384
    # cSHAKE256 finality certificates, majority, weighted, unanimous, kai, or
    # custom) and returns the finalized response along with every member's vote.
    post_agent_groups_by_ref_run(req: runGroupIn) returns (rep: runGroupOut)
    # Approves or rejects a proposed role candidate. Approving a
    # proposal creates the specialist agent under its reporting line once (and is
    # idempotent on repeated approval); rejecting records the rejection without
    # creating an agent.
    post_agent_proposals_by_id_decide(req: decideProposalIn) returns (rep: RoleProposal)
    # Runs the scheduled review of repeated failures, missing
    # expertise, and human review effort across the organization's runs, briefs,
    # and observed coverage gaps, producing role candidate proposals with concrete
    # examples, a micro-USD cost estimate, and a reporting line.
    post_agent_proposals_review(req: reviewProposalsIn) returns (rep: reviewProposalsOut)
    # Turns an approved organization chart into agent definitions in the
    # caller's organization, resolving manager reporting lines (`parent` and
    # `lineage`) and shared role templates with explicit versions. Applying the same
    # roster again (or resuming an interrupted launch) updates existing roles in
    # place without duplicating agents or budgets.
    post_agent_roster(req: applyRosterIn) returns (rep: applyRosterOut)
    # Opens a live agent session in the caller's org — the row every
    # surface (the CLI's outer agent, hanzo.bot, the console, chat) hangs its
    # activity off. A session with a parentSessionId becomes a subagent of that
    # session and inherits its root, so one flow is one tree; without one it is
    # itself a root. Registering with a terminal status records a session that has
    # already finished.
    post_agent_sessions(req: registerReq) returns (rep: sessionView)
    # Answers a question a session's run asked its person — a coding
    # run's harness, or a turn of the app's conversation asking whether a coding run
    # may start — from the page the session is read on. The run waiting on it goes on
    # with the answer. Only the person the question is for may answer, once: a
    # question already answered or closed answers 409, and one this session did not
    # ask answers 404.
    post_agent_sessions_by_id_answer(req: answerIn) returns (rep: answerView)
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
    # A chat turn's run READS it: the message is handed to the model as the person's
    # next words at its next step, and a message that arrives while the model is
    # writing its answer gets a step of its own before the answer stands. A turn that
    # has already answered takes no more and answers 409 — what is said after an
    # answer is a new turn.
    post_agent_sessions_by_id_message(req: controlIn) returns (rep: controlResult)
    # Asks a running session to pause. Recorded durably, and forwarded
    # to the durable-execution engine when the session is task-backed.
    # A chat turn's run is paused where it runs: it stops, the session stays live as
    # `paused`, and its sandbox is PARKED — the pod stops and every file stays, with
    # nothing billed for compute — until a resume picks it up.
    post_agent_sessions_by_id_pause(req: controlIn) returns (rep: controlResult)
    # Asks a paused session to continue, on the same terms as a pause.
    # A chat turn that was paused or STOPPED runs again: the same session, in the same
    # sandbox with every file it had written, told `message` next ("Continue where you
    # left off." when there is none). Only the person whose turn it was may resume
    # it, since the agent runs as them.
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
    # Lets runs started from the web work in one folder of this
    # machine and every folder under it. Only the machine's owner may grant, from
    # the machine itself (the desktop or `hanzo link grant`); another member gets
    # 403. Granting a folder already granted answers that grant with 200; a new one
    # answers 201.
    post_agent_targets_by_id_folders(req: folderIn) returns (rep: folderView)
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
    # Says one thing in the app's conversation and starts the turn that
    # answers it. It answers at once with the conversation and the turn's run; the
    # turn itself is read from the session (GET /v1/agent/sessions/{id}, and its
    # ?root= stream): the person's words, what the agent is doing, the coding runs it
    # starts, a question it asks, and its answer.
    # With no `session` it starts a conversation. With one it continues that
    # conversation in the same session, under a new run, once its last turn has
    # answered: 409 while a turn is still answering — what is said then is that
    # turn's next input (POST /v1/agent/sessions/{id}/message). A conversation is its
    # person's alone: anyone else's answers as absent, and a SuperAdmin acting in an
    # org that is not their own holds none there (their sandbox and builds would be
    # paid by that org).
    # Nothing is leased to answer: a turn that only talks uses no sandbox. Code is
    # worked on by a coding run — `dev`, with its own tools — which leases the
    # conversation's sandbox on the first build; a later one works on the
    # conversation's repository and carries on its last run, in that run's sandbox
    # and on its branch.
    post_agent_turns(req: talkIn) returns (rep: talkOut)
}

# ---------------------------------------------------------------------
# 63 op(s) here. What follows is what this schema does not carry.
#
# dropped (2) — the value does not cross, and nothing fails:
#   agentDetail.agentView  agents.agentView  (promoted, not carried)
#   sessionDetail.sessionView  agents.sessionView  (promoted, not carried)
#
# blocked (2) — the op is absent; the field has no wire form:
#   get_agent_by_ref_spend  spendView.ByComponent  map[string]int64  (map)
#   get_agent_sessions_by_id_tree  treeNode.Children  []agents.treeNode  (no wire form)
#
# opaque (49) — crosses, arrives without its name:
#   BriefRecord.Attribution  agents.BriefAttribution (list element)
#   BriefRecord.Tasks  agents.BriefTask (list element)
#   CodingArtifacts.Artifacts  client.Artifact (list element)
#   CodingChanges.Commits  client.CodingCommit (list element)
#   CodingChanges.Files  client.CodingFile (list element)
#   CodingChanges.Pull  client.CodingPull
#   CodingChanges.Steers  client.CodingSteered (list element)
#   CodingStartIn.Reply  client.Reply
#   CodingTree.Entries  client.CodingEntry (list element)
#   activityFeed.Activity  agents.activityView (list element)
#   agentDetail.RecentRuns  agents.agentRunView (list element)
#   agentList.Agents  agents.agentView (list element)
#   applyRosterIn.Roles  agents.RoleSpec (list element)
#   applyRosterOut.Agents  agents.agentView (list element)
#   bootstrapPrimaryOut.Agent  agents.agentView
#   briefList.Briefs  agents.BriefRecord (list element)
#   buildList.Builds  agents.buildSummary (list element)
#   buildView.Turns  agents.buildTurn (list element)
#   controlDrain.Commands  agents.controlCommandView (list element)
#   controlResult.Event  agents.eventView
#   createGroupIn.Policy  agents.groupPolicy
#   eventList.Events  agents.eventView (list element)
#   folderList.Folders  agents.folderView (list element)
#   groupList.Groups  agents.groupView (list element)
#   groupView.Policy  agents.groupPolicy
#   metricsView.Resource  agents.resourceUsage
#   metricsView.Series  agents.seriesLine (list element)
#   patchTargetIn.Metrics  agents.Metrics
#   patchTargetIn.Spec  agents.Spec
#   proposalList.Proposals  agents.RoleProposal (list element)
#   reviewProposalsIn.ObservedGaps  agents.RoleGapInput (list element)
#   reviewProposalsOut.Proposals  agents.RoleProposal (list element)
#   rosterTemplatesOut.Functions  agents.StudioFunction (list element)
#   rosterTemplatesOut.Templates  agents.RoleTemplate (list element)
#   runGroupOut.Quasar  agents.quasarCertView
#   runGroupOut.Votes  agents.groupVoteView (list element)
#   runList.Runs  agents.agentRunView (list element)
#   sessionDetail.Children  agents.sessionView (list element)
#   sessionDetail.RecentEvents  agents.eventView (list element)
#   sessionList.Sessions  agents.sessionView (list element)
#   sessionView.LastEvent  agents.lastEventView
#   sessionView.Park  agents.sessionPark
#   sessionView.Progress  agents.sessionProgress
#   targetList.Targets  agents.targetView (list element)
#   targetReq.Metrics  agents.Metrics
#   targetReq.Spec  agents.Spec
#   targetView.Metrics  agents.Metrics
#   targetView.Spec  agents.Spec
#   updateGroupIn.Policy  agents.groupPolicy
