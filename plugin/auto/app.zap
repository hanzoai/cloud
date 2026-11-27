# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package auto

struct Automation {
    Event        bytes      @0
    Target       list<text> @8
    ID           text       @16
    Name         text       @24
    Instructions text       @32
    Project      list<text> @40
    Model        list<text> @48
    Schedule     bytes      @56
    Permissions  text       @64
    Notify       bool       @72
    PostTo       list<text> @80
    Enabled      bool       @88
    Draft        bool       @89
    Source       text       @96
    Next         list<text> @104
    Last         bytes      @112
    Created      text       @120
    Updated      text       @128
    Dir          list<text> @136
}

struct AutomationRun {
    ID         text        @0
    Status     text        @8
    At         text        @16
    Finished   list<text>  @24
    Summary    text        @32
    Transcript list<text>  @40
    Draft      text        @48
    Posts      list<bytes> @56
    Outputs    list<text>  @64
}

struct Catalog {
    ConnectorCount i64         @0
    RunnableCount  i64         @8
    Connectors     list<bytes> @16
}

struct Flow {
    ID                 text  @0
    Org                text  @8
    ExternalID         text  @16
    FolderID           text  @24
    Status             text  @32
    PublishedVersionID text  @40
    Metadata           bytes @48
    Created            i64   @56
    Updated            i64   @64
}

struct FlowRun {
    ID            text @0
    Org           text @8
    FlowID        text @16
    FlowVersionID text @24
    WorkflowID    text @32
    Status        text @40
    StartTime     i64  @48
    FinishTime    i64  @56
    Created       i64  @64
    Updated       i64  @72
    Session       text @80
    Summary       text @88
    Person        text @96
    Email         text @104
    Actor         text @112
    Draft         text @120
    Posts         text @128
    Outputs       text @136
}

struct Job {
    ID         text       @0
    Product    text       @8
    Kind       text       @16
    Title      text       @24
    State      text       @32
    Status     text       @40
    Progress   list<f64>  @48
    Automation list<text> @56
    Session    list<text> @64
    Ref        text       @72
    Origin     text       @80
    Summary    text       @88
    Error      text       @96
    Outputs    list<text> @104
    Cost       list<i64>  @112
    Started    text       @120
    Finished   list<text> @128
    Updated    text       @136
}

struct automationIn {
    Event        bytes      @0
    Target       list<text> @8
    Name         text       @16
    Instructions text       @24
    Project      list<text> @32
    Model        list<text> @40
    Schedule     bytes      @48
    Permissions  text       @56
    Notify       bool       @64
    PostTo       list<text> @72
    Enabled      list<bool> @80
    Source       text       @88
    Dir          list<text> @96
}

struct automationPage {
    Data list<bytes> @0
}

struct automationPatch {
    Event        bytes      @0
    Target       list<text> @8
    ID           text       @16
    Name         list<text> @24
    Instructions list<text> @32
    Project      list<text> @40
    Model        list<text> @48
    Schedule     bytes      @56
    Permissions  list<text> @64
    Notify       list<bool> @72
    PostTo       list<text> @80
    Enabled      list<bool> @88
    Dir          list<text> @96
}

struct automationQuery {
    Q      text @0
    Sort   text @8
    Source text @16
}

struct automationRef {
    ID text @0
}

struct automationRunPage {
    Data list<bytes> @0
}

struct automationRunsQuery {
    ID    text @0
    Limit i64  @8
}

struct flowPage {
    Data list<bytes> @0
}

struct flowRef {
    ID text @0
}

struct jobPage {
    Data list<bytes> @0
}

struct jobQuery {
    Product text @0
    State   text @8
    Limit   i64  @16
}

struct jobRef {
    ID text @0
}

struct listQuery {
    Limit i64 @0
}

struct patchFlowIn {
    ID                 text       @0
    FolderID           list<text> @8
    ExternalID         list<text> @16
    PublishedVersionID list<text> @24
    Metadata           bytes      @32
}

struct reviewIn {
    ID   text @0
    Run  text @8
    Post bool @16
}

struct runPage {
    Data list<bytes> @0
}

struct runQuery {
    FlowID text @0
    Limit  i64  @8
}

struct runRef {
    ID text @0
}

struct runStarted {
    Run bytes @0
}

struct starterPage {
    Data list<bytes> @0
}

struct versionQuery {
    ID    text @0
    Limit i64  @8
}

struct whenIn {
    Text text @0
    TZ   text @8
}

struct whenOut {
    Schedule bytes      @0
    Says     text       @8
    Next     list<text> @16
}

interface auto {
    # Deletes an automation, its schedule and its run history, and
    # stops a Dev run it has going. It answers no content.
    delete_auto_automations_by_id(req: automationRef)
    # Deletes one automation, its versions and its run history. It answers
    # no content, and a flow of another org answers not-found.
    delete_auto_flows_by_id(req: flowRef)
    # Returns your automations — another member's are never listed.
    # `q` keeps those whose name or instructions contain it, ignoring case; `source`
    # keeps those made in one product; `sort` is `updated` (the default, newest
    # first), `name`, or `next` (soonest first, unscheduled last).
    get_auto_automations(req: automationQuery) returns (rep: automationPage)
    # Returns one automation. A flow of the org that is not an
    # automation, and anything of another org, answers not-found.
    get_auto_automations_by_id(req: automationRef) returns (rep: Automation)
    # Returns one automation's runs, newest first, to the person
    # it runs as. Each links the
    # Dev run it started as `transcript`. The optional `limit` bounds the page.
    get_auto_automations_by_id_runs(req: automationRunsQuery) returns (rep: automationRunPage)
    # Returns the caller org's automations, most-recently-updated first. The
    # optional `limit` query bounds the page.
    get_auto_flows(req: listQuery) returns (rep: flowPage)
    # Lists everything of yours that runs without you watching, across
    # every product: each run of your automations and workflows, and every Studio
    # render, long video or other background run a product recorded for you. Each
    # is in one of five states — scheduled, running, succeeded, failed, cancelled —
    # with what it made (Library item ids) and what it cost when that is known.
    # Another member's jobs are never listed.
    get_auto_jobs(req: jobQuery) returns (rep: jobPage)
    # Reads one of your jobs. A job that is not yours answers 404, as one that
    # does not exist.
    get_auto_jobs_by_id(req: jobRef) returns (rep: Job)
    # Returns the connector catalogue. Each entry is an external service a
    # flow step can invoke, carrying its auth descriptor and the input properties of its
    # actions and triggers. The catalogue is the same for every tenant, so the gate is a
    # validated principal rather than a per-org view.
    get_auto_provider() returns (rep: Catalog)
    # Returns your run history, newest first: the runs of the flows you
    # hold. The optional `flowId` query narrows it to one of them and `limit` bounds
    # the page.
    get_auto_runs(req: runQuery) returns (rep: runPage)
    # Returns one run. A run that has not reached a terminal status is refreshed
    # from the durable engine first — scoped to the org's own namespace — so the caller
    # sees live progress rather than the last status that happened to be persisted.
    get_auto_runs_by_id(req: runRef) returns (rep: FlowRun)
    # Returns the starter automations: a name, what it does, the
    # instructions it runs and the schedule it suggests, in the caller's own zone.
    get_auto_templates() returns (rep: starterPage)
    # Changes an automation, for the person it runs as: any field
    # it was created with, and
    # `enabled`, which arms or disarms its schedule. A field the request omits is
    # left alone; `project` or `model` sent as null (or "") clears it. Any change
    # but `enabled` makes the caller the person it runs as from then on. A draft is
    # finished by saving its `instructions`: it becomes an automation in place, on
    # unless `enabled` says otherwise.
    patch_auto_automations_by_id(req: automationPatch) returns (rep: Automation)
    # Updates one automation's metadata in place. Every field is optional; a
    # field the request omits is left alone. Publishing a version pins which one runs,
    # and is refused unless that version belongs to this flow.
    patch_auto_flows_by_id(req: patchFlowIn) returns (rep: Flow)
    # Creates an automation and arms its schedule. It runs as the
    # caller: their Dev run, in this org, each time it fires. `name` and
    # `instructions` are required; no `schedule` runs it only on demand, no
    # `permissions` is ask: it proposes what it would do and changes nothing, and
    # `enabled: false` creates it off, its schedule unarmed.
    post_auto_automations(req: automationIn) returns (rep: Automation)
    # Starts one run now, whether or not its schedule is armed. Only
    # the person it runs as may start one; who did is kept. An
    # automation runs one at a time: while a run is going, this records a skipped
    # start and answers it. Every run first asks IAM whether the person it runs as
    # is still a member of the org; one who is not makes the run refused and turns
    # the automation off.
    post_auto_automations_by_id_run(req: automationRef) returns (rep: runStarted)
    # Answers a run that holds a post for review, for the person the
    # automation runs as: `post` true posts it to the
    # automation's accounts and answers the run while it posts; false discards it.
    # A run not in review answers 409; one held longer than a day is not posted.
    post_auto_automations_by_id_runs_by_run_review(req: reviewIn) returns (rep: AutomationRun)
    # Reads a schedule said in words — "every 5 minutes", "weekdays at 8:30",
    # "every monday at 9am", "tomorrow at 7pm", "in 20 minutes", or a five-field
    # cron expression — in your time zone, and answers the schedule an automation
    # takes, said back in plain words, with when it would first run. Nothing is
    # created: send the schedule to POST /v1/auto/automations. A phrase that no rule
    # reads is read by a model; a phrase that names no schedule answers 422.
    post_auto_cron(req: whenIn) returns (rep: whenOut)
    # Disarms a flow's trigger and marks it DISABLED. Its schedule and its
    # event subscriptions are dropped, so a disabled flow is never a live target; runs
    # already in flight are unaffected, and it can still be started on demand.
    post_auto_flows_by_id_disable(req: flowRef) returns (rep: Flow)
    # Arms a flow's trigger and marks it ENABLED. A POLLING trigger gets a
    # cron schedule on the durable engine; a WEBHOOK trigger gets a subscription in the
    # routing index, so an inbound event starts it; a MANUAL trigger arms nothing and
    # still runs on demand.
    post_auto_flows_by_id_enable(req: flowRef) returns (rep: Flow)
    # Starts one durable run of a flow now. It runs the flow's published
    # version if one is pinned, else its latest, and answers the run record it created.
    # The run is bounded by the org's per-minute run-start budget and its in-flight
    # concurrency ceiling; over either, or with the engine not ready, no run is started
    # and no run id is burned.
    post_auto_flows_by_id_run(req: flowRef) returns (rep: FlowRun)
}

# ---------------------------------------------------------------------
# 21 op(s) here. What follows is what this schema does not carry.
#
# blocked (9) — the op is absent; the field has no wire form:
#   get_auto_flows_by_id  populatedFlow.Version  auto.FlowVersion  (reaches one)
#   get_auto_flows_by_id_versions  versionPage.Data  []auto.FlowVersion  (no wire form)
#   post_auto_flows  createFlowReq.Trigger  auto.FlowTrigger  (reaches one)
#   post_auto_flows  populatedFlow  auto.populatedFlow  (reaches one)
#   post_auto_flows_by_id_versions  FlowVersion.Trigger  auto.FlowTrigger  (reaches one)
#   post_auto_flows_by_id_versions  createVersionIn.Trigger  auto.FlowTrigger  (reaches one)
#   post_auto_provider_by_id_run  runIn.Auth  interface {}  (any)
#   post_auto_provider_by_id_run  runIn.Props  map[string]interface {}  (map)
#   post_auto_provider_by_id_run  runResp.Output  interface {}  (any)
#
# opaque (17) — crosses, arrives without its name:
#   Automation.Event  auto.OutcomeTrigger
#   Automation.Last  auto.LastRun
#   Automation.Schedule  auto.Schedule
#   AutomationRun.Posts  auto.PostResult (list element)
#   Catalog.Connectors  auto.ConnectorMetadata (list element)
#   automationIn.Event  auto.OutcomeTrigger
#   automationIn.Schedule  auto.Schedule
#   automationPage.Data  auto.Automation (list element)
#   automationPatch.Event  auto.OutcomeTrigger
#   automationPatch.Schedule  auto.Schedule
#   automationRunPage.Data  auto.AutomationRun (list element)
#   flowPage.Data  auto.Flow (list element)
#   jobPage.Data  client.Job (list element)
#   runPage.Data  auto.FlowRun (list element)
#   runStarted.Run  auto.runHandle
#   starterPage.Data  auto.Starter (list element)
#   whenOut.Schedule  auto.Schedule
