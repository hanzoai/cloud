# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package cyber

struct addGraphNodeReq {
    ID       text @0
    Kind     text @8
    Label    text @16
    Detail   text @24
    Status   text @32
    Severity text @40
    ParentID text @48
}

struct approvalFilter {
    EngagementID text @0
    Status       text @8
}

struct approvalList {
    Data list<bytes> @0
}

struct approvalView {
    ID           text @0
    EngagementID text @8
    TaskID       text @16
    Action       text @24
    Target       text @32
    Tool         text @40
    RiskScore    i64  @48
    RiskLevel    text @56
    KaiRationale text @64
    Status       text @72
    DecidedBy    text @80
    DecidedAt    i64  @88
    CreatedAt    i64  @96
}

struct attackMatrixView {
    Tactics    list<text>  @0
    Techniques list<bytes> @8
}

struct coaView {
    Data list<bytes> @0
}

struct createEngagementReq {
    Name      text       @0
    Target    text       @8
    Scope     list<text> @16
    Mode      text       @24
    Synthetic bool       @32
}

struct createFindingReq {
    EngagementID   text @0
    TaskID         text @8
    Title          text @16
    Severity       text @24
    Category       text @32
    CWE            text @40
    MitreTechnique text @48
    MitreTactic    text @56
    Target         text @64
    EvidenceQuote  text @72
    TrafficID      text @80
    Remediation    text @88
}

struct createRuleReq {
    EngagementID text @0
    Name         text @8
    MatchHost    text @16
    MatchPath    text @24
    Action       text @32
}

struct createTaskReq {
    EngagementID text @0
    Title        text @8
    Goal         text @16
    Phase        text @24
}

struct dashboardView {
    Org              text @0
    Stage            text @8
    Engagements      i64  @16
    Tasks            i64  @24
    FindingsTotal    i64  @32
    FindingsVerified i64  @40
    Critical         i64  @48
    High             i64  @56
    Medium           i64  @64
    Low              i64  @72
    PendingApprovals i64  @80
    TrafficCount     i64  @88
    RulesCount       i64  @96
    VerificationRate i64  @104
}

struct decideApprovalReq {
    ID       text @0
    Decision text @8
}

struct deleteRes {
    Deleted bool @0
    ID      text @8
}

struct engagementFilter {
    EngagementID text @0
}

struct engagementList {
    Data list<bytes> @0
}

struct engagementView {
    ID        text       @0
    Name      text       @8
    Target    text       @16
    Scope     list<text> @24
    Mode      text       @32
    Status    text       @40
    Synthetic bool       @48
    CreatedAt i64        @56
    UpdatedAt i64        @64
}

struct findingFilter {
    EngagementID text @0
    Severity     text @8
    Status       text @16
}

struct findingList {
    Data list<bytes> @0
}

struct findingView {
    ID               text @0
    EngagementID     text @8
    TaskID           text @16
    Title            text @24
    Severity         text @32
    Category         text @40
    CWE              text @48
    MitreTechnique   text @56
    MitreTactic      text @64
    Status           text @72
    Target           text @80
    EvidenceQuote    text @88
    TrafficID        text @96
    Verified         bool @104
    VerificationNote text @112
    Remediation      text @120
    CreatedAt        i64  @128
    UpdatedAt        i64  @136
}

struct graphNodeView {
    ID        text @0
    TaskID    text @8
    Kind      text @16
    Label     text @24
    Detail    text @32
    Status    text @40
    Severity  text @48
    ParentID  text @56
    CreatedAt i64  @64
}

struct hardeningView {
    Score            i64         @0
    ScopeEnforced    bool        @8
    VerbatimVerified i64         @16
    OpenCritical     i64         @24
    Checks           list<bytes> @32
}

struct healthView {
    Status  text @0
    Flag    text @8
    Backend text @16
}

struct recordTrafficReq {
    EngagementID    text @0
    TaskID          text @8
    Method          text @16
    URL             text @24
    Host            text @32
    StatusCode      i64  @40
    RequestHeaders  text @48
    RequestBody     text @56
    ResponseHeaders text @64
    ResponseBody    text @72
    LatencyMS       i64  @80
}

struct ruleList {
    Data list<bytes> @0
}

struct ruleRef {
    ID text @0
}

struct ruleView {
    ID           text @0
    EngagementID text @8
    Name         text @16
    MatchHost    text @24
    MatchPath    text @32
    Action       text @40
    Enabled      bool @48
    CreatedAt    i64  @56
}

struct seedDemoRes {
    Engagement bytes @0
    Tasks      i64   @8
    Findings   i64   @16
    Traffic    i64   @24
    Approvals  i64   @32
    Rules      i64   @40
}

struct submitApprovalReq {
    EngagementID text @0
    TaskID       text @8
    Action       text @16
    Target       text @24
    Tool         text @32
}

struct taskGraphView {
    Task  bytes       @0
    Nodes list<bytes> @8
}

struct taskList {
    Data list<bytes> @0
}

struct taskRef {
    ID text @0
}

struct taskView {
    ID           text @0
    EngagementID text @8
    Title        text @16
    Goal         text @24
    Status       text @32
    Phase        text @40
    CreatedAt    i64  @48
    UpdatedAt    i64  @56
}

struct trafficFilter {
    EngagementID text @0
    Host         text @8
}

struct trafficList {
    Data list<bytes> @0
}

struct trafficView {
    ID              text @0
    EngagementID    text @8
    TaskID          text @16
    Method          text @24
    URL             text @32
    Host            text @40
    StatusCode      i64  @48
    RequestHeaders  text @56
    RequestBody     text @64
    ResponseHeaders text @72
    ResponseBody    text @80
    LatencyMS       i64  @88
    CreatedAt       i64  @96
}

struct updateFindingReq {
    ID            text @0
    Status        text @8
    EvidenceQuote text @16
    TrafficID     text @24
    Remediation   text @32
}

struct verifyFindingReq {
    ID            text @0
    TrafficID     text @8
    EvidenceQuote text @16
}

interface cyber {
    # Removes an org-scoped intercept rule.
    delete_cyber_intercept_rules_by_id(req: ruleRef) returns (rep: deleteRes)
    # Returns Kai risk-scored action approvals for the caller's org.
    get_cyber_approvals(req: approvalFilter) returns (rep: approvalList)
    # Projects the org's findings onto MITRE ATT&CK tactics and techniques.
    get_cyber_attack(req: engagementFilter) returns (rep: attackMatrixView)
    # Computes prioritized remediation Courses of Action from active findings.
    get_cyber_coa(req: engagementFilter) returns (rep: coaView)
    # Aggregates engagements, findings, verbatim proof coverage, Kai approvals, and traffic counts for the authenticated org.
    get_cyber_dashboard(req: engagementFilter) returns (rep: dashboardView)
    # Returns every authorized cyber engagement belonging to the caller's org.
    get_cyber_engagements() returns (rep: engagementList)
    # Returns org-scoped findings optionally filtered by engagementId, severity, and status.
    get_cyber_findings(req: findingFilter) returns (rep: findingList)
    # Evaluates enclave scope enforcement, verbatim proof coverage, and unresolved critical risk.
    get_cyber_hardening(req: engagementFilter) returns (rep: hardeningView)
    # Returns liveness status for the native Hanzo Cyber cloud subsystem.
    get_cyber_health() returns (rep: healthView)
    # Returns scope and intercept governance rules for the caller's org.
    get_cyber_intercept_rules(req: engagementFilter) returns (rep: ruleList)
    # Returns cyber tasks belonging to the caller's org, optionally filtered by engagementId.
    get_cyber_tasks(req: engagementFilter) returns (rep: taskList)
    # Returns a task together with its ordered GOAL -> INTENT -> FINDING -> HINT reasoning graph.
    get_cyber_tasks_by_id_graph(req: taskRef) returns (rep: taskGraphView)
    # Returns captured HTTP exchanges belonging to the caller's org.
    get_cyber_traffic(req: trafficFilter) returns (rep: trafficList)
    # Updates a finding's status, bound traffic, evidence quote, or remediation text.
    patch_cyber_findings_by_id(req: updateFindingReq) returns (rep: findingView)
    # Evaluates a proposed cyber action with Kai deterministic risk scoring and queues it for human approval.
    post_cyber_approvals(req: submitApprovalReq) returns (rep: approvalView)
    # Records a human Approve, Deny, or Always-Allow decision on a queued Kai approval.
    post_cyber_approvals_by_id_decide(req: decideApprovalReq) returns (rep: approvalView)
    # Creates a new org-scoped cyber engagement workspace with explicit target scope.
    post_cyber_engagements(req: createEngagementReq) returns (rep: engagementView)
    # Idempotently seeds a synthetic MDTF Tactical Enclave (Lab) engagement for internal testing and defense demos.
    post_cyber_engagements_seed_demo() returns (rep: seedDemoRes)
    # Records a security finding inside an authorized engagement.
    post_cyber_findings(req: createFindingReq) returns (rep: findingView)
    # Performs deterministic verbatim substring verification of a finding's evidenceQuote against its bound captured HTTP traffic record.
    post_cyber_findings_by_id_verify(req: verifyFindingReq) returns (rep: findingView)
    # Adds a scope or intercept governance rule for the caller's org.
    post_cyber_intercept_rules(req: createRuleReq) returns (rep: ruleView)
    # Creates a cyber task and initializes its root GOAL node in the reasoning graph.
    post_cyber_tasks(req: createTaskReq) returns (rep: taskView)
    # Appends an intent, finding, hint, or action node to a task's reasoning graph.
    post_cyber_tasks_by_id_nodes(req: addGraphNodeReq) returns (rep: graphNodeView)
    # Stores a captured HTTP exchange for inspection and verbatim finding proof binding.
    post_cyber_traffic(req: recordTrafficReq) returns (rep: trafficView)
}

# ---------------------------------------------------------------------
# 24 op(s) here. What follows is what this schema does not carry.
#
# opaque (12) — crosses, arrives without its name:
#   approvalList.Data  cyber.approvalView (list element)
#   attackMatrixView.Techniques  cyber.attackTechniqueView (list element)
#   coaView.Data  cyber.coaItemView (list element)
#   engagementList.Data  cyber.engagementView (list element)
#   findingList.Data  cyber.findingView (list element)
#   hardeningView.Checks  cyber.hardeningCheckView (list element)
#   ruleList.Data  cyber.ruleView (list element)
#   seedDemoRes.Engagement  cyber.engagementView
#   taskGraphView.Nodes  cyber.graphNodeView (list element)
#   taskGraphView.Task  cyber.taskView
#   taskList.Data  cyber.taskView (list element)
#   trafficList.Data  cyber.trafficView (list element)
#
# renamed (1) — spelled differently here than on every other surface:
#   post_cyber_engagements_seed-demo  ->  post_cyber_engagements_seed_demo
