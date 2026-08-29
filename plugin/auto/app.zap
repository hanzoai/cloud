# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package auto

struct Catalog {
    ConnectorCount i64         @0
    Connectors     list<bytes> @8
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
}

struct flowPage {
    Data list<bytes> @0
}

struct flowRef {
    ID text @0
}

struct listQuery {
    Limit i64 @0
}

struct patchFlowIn {
    ID                 text  @0
    FolderID           text  @8
    ExternalID         text  @16
    PublishedVersionID text  @24
    Metadata           bytes @32
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

struct versionQuery {
    ID    text @0
    Limit i64  @8
}

interface auto {
    # Deletes one automation, its versions and its run history. It answers
    # no content, and a flow of another org answers not-found.
    delete_auto_flows_by_id(req: flowRef)
    # Connectors returns the connector catalogue. Each entry is an external service a
    # flow step can invoke, carrying its auth descriptor and the input properties of its
    # actions and triggers. The catalogue is the same for every tenant, so the gate is a
    # validated principal rather than a per-org view.
    get_auto_connectors() returns (rep: Catalog)
    # Returns the caller org's automations, most-recently-updated first. The
    # optional `limit` query bounds the page.
    get_auto_flows(req: listQuery) returns (rep: flowPage)
    # Returns the caller org's run history, newest first. The optional
    # `flowId` query narrows it to one flow and `limit` bounds the page.
    get_auto_runs(req: runQuery) returns (rep: runPage)
    # Returns one run. A run that has not reached a terminal status is refreshed
    # from the durable engine first — scoped to the org's own namespace — so the caller
    # sees live progress rather than the last status that happened to be persisted.
    get_auto_runs_by_id(req: runRef) returns (rep: FlowRun)
    # Updates one automation's metadata in place. Every field is optional; a
    # field the request omits is left alone. Publishing a version pins which one runs,
    # and is refused unless that version belongs to this flow.
    patch_auto_flows_by_id(req: patchFlowIn) returns (rep: Flow)
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
# 9 op(s) here. What follows is what this schema does not carry.
#
# blocked (9) — the op is absent; the field has no wire form:
#   get_auto_flows_by_id  populatedFlow.Version  auto.FlowVersion  (reaches one)
#   get_auto_flows_by_id_versions  versionPage.Data  []auto.FlowVersion  (no wire form)
#   post_auto_connectors_by_id_run  runIn.Auth  interface {}  (any)
#   post_auto_connectors_by_id_run  runIn.Props  map[string]interface {}  (map)
#   post_auto_connectors_by_id_run  runResp.Output  interface {}  (any)
#   post_auto_flows  createFlowReq.Trigger  auto.FlowTrigger  (reaches one)
#   post_auto_flows  populatedFlow  auto.populatedFlow  (reaches one)
#   post_auto_flows_by_id_versions  FlowVersion.Trigger  auto.FlowTrigger  (reaches one)
#   post_auto_flows_by_id_versions  createVersionIn.Trigger  auto.FlowTrigger  (reaches one)
#
# opaque (3) — crosses, arrives without its name:
#   Catalog.Connectors  auto.ConnectorMetadata (list element)
#   flowPage.Data  auto.Flow (list element)
#   runPage.Data  auto.FlowRun (list element)
