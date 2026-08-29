# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package flow

struct flowCreate {
    Name        text  @0
    Description text  @8
    Data        bytes @16
}

struct flowRef {
    Workflow text @0
}

struct flowRun {
    Workflow text  @0
    Input    text  @8
    Session  text  @16
    Tweaks   bytes @24
}

struct flowRuns {
    Workflow text @0
}

struct flowStatus {
    Reachable bool @0
    Version   text @8
}

struct flowUpdate {
    Workflow    text  @0
    Name        text  @8
    Description text  @16
    Data        bytes @24
    Locked      bool  @32
}

struct flowWorkflows {
    Page text @0
    Size text @8
}

interface flow {
    # Deletes one of the caller's workflows and its runs. Ownership
    # is verified first; a foreign id answers 404 and deletes nothing.
    delete_flow_workflows_by_workflow(req: flowRef)
    # Runs reads one workflow's recorded runs: every component build with its
    # result, keyed by component. Ownership is verified first — run records never
    # cross the org boundary.
    get_flow_runs(req: flowRuns)
    # Status reports whether the flow service is reachable and which version it
    # runs. It is the product's own /health and /v1/version composed — an honest
    # lens for "is the workflow plane up", never a fabricated ok.
    get_flow_status() returns (rep: flowStatus)
    # Workflows lists the caller's workflows, paged. The list is scoped
    # server-side to the org's project — the page can only ever hold the caller's
    # own workflows.
    get_flow_workflows(req: flowWorkflows)
    # Workflow reads one of the caller's workflows — the full record, graph
    # included. A workflow outside the caller's org answers 404, indistinguishable
    # from one that does not exist.
    get_flow_workflows_by_workflow(req: flowRef)
    # Patches one of the caller's workflows: name, description,
    # graph, or the locked flag — only the stated fields move. Ownership is
    # verified before the patch reaches the product.
    patch_flow_workflows_by_workflow(req: flowUpdate)
    # Run executes one of the caller's workflows synchronously: the graph runs in
    # the flow service and the response carries the run's session and outputs. A
    # graph whose components fail reports the product's own error. Runs are
    # bounded by the product's five-minute sync ceiling.
    post_flow_runs(req: flowRun)
    # Creates a workflow in the caller's org. The org's project id
    # is pinned server-side from the validated principal — there is no field by
    # which a caller could place a workflow in another org.
    post_flow_workflows(req: flowCreate)
}
