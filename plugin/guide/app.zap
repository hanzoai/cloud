# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package guide

struct VentureRecord {
    Slug                  text        @0
    VentureOrg            text        @8
    StudioOrg             text        @16
    Title                 text        @24
    Thesis                text        @32
    Sponsor               text        @40
    Owner                 text        @48
    Stage                 text        @56
    Status                text        @64
    Imported              bool        @72
    PlaybookVersion       text        @80
    InitialBudgetMicroUSD i64         @88
    SpendMicroUSD         i64         @96
    PendingDecisions      i64         @104
    FailedRuns            i64         @112
    Evidence              list<text>  @120
    LatestEvidence        text        @128
    FormationID           text        @136
    Members               list<text>  @144
    Setup                 bytes       @152
    Progress              bytes       @160
    Templates             list<bytes> @168
    Resumed               bool        @176
    CreatedAt             i64         @184
    UpdatedAt             i64         @192
}

struct actionsView {
    Data list<bytes> @0
}

struct analyticsView {
    Funnel          bytes      @0
    Recommendations list<text> @8
}

struct blueprintVersionsView {
    Brand    text        @0
    Versions list<bytes> @8
}

struct chatRequest {
    Message text @0
}

struct chatResponse {
    Reply       text        @0
    Suggestions list<bytes> @8
    Funnel      bytes       @16
}

struct corpusView {
    Stage      text        @0
    Count      i64         @8
    Strategies list<bytes> @16
}

struct createVentureIn {
    Slug                  text       @0
    VentureOrg            text       @8
    Title                 text       @16
    Thesis                text       @24
    Sponsor               text       @32
    Owner                 text       @40
    InitialBudgetMicroUSD i64        @48
    Evidence              list<text> @56
    Playbook              text       @64
    Stage                 text       @72
    Imported              bool       @80
    TeamSeeded            bool       @81
    Members               list<text> @88
}

struct portfolioView {
    StudioOrg           text        @0
    TotalVentures       i64         @8
    ActiveVentures      i64         @16
    PausedVentures      i64         @24
    FormedVentures      i64         @32
    OffboardedVentures  i64         @40
    TotalBudgetMicroUSD i64         @48
    TotalSpendMicroUSD  i64         @56
    PendingDecisions    i64         @64
    FailedRuns          i64         @72
    Ventures            list<bytes> @80
}

struct stepRef {
    ID text @0
}

struct strategiesQuery {
    Category text @0
    Stage    text @8
    Workload text @16
}

struct suggestResponse {
    Next            text        @0
    Suggestions     list<bytes> @8
    Narrative       text        @16
    Funnel          bytes       @24
    Recommendations list<text>  @32
}

struct ventureLifecycleIn {
    Slug             text       @0
    Action           text       @8
    Stage            text       @16
    SpendMicroUSD    list<i64>  @24
    PendingDecisions list<i64>  @32
    FailedRuns       list<i64>  @40
    LatestEvidence   text       @48
    FormationID      text       @56
    TeamSeeded       list<bool> @64
    Member           text       @72
}

struct ventureList {
    Ventures list<bytes> @0
}

struct ventureRef {
    Slug text @0
}

interface guide {
    # Returns the caller org's Business AI action ledger, most recent
    # first: every "do it for me" tool call, the arguments it ran with, its result and
    # whether it succeeded. It is the audit-visible record of what the agent did on
    # the org's behalf, and the backing state for the "acted" auto-detect signal.
    get_guide_actions() returns (rep: actionsView)
    # Returns the caller org's funnel from the analytics lens plus the GTM
    # recommendations derived from it. It is the Business AI's data-grounded read —
    # what the funnel is doing, and the next-best action to move its weakest stage. An
    # unreachable or silent warehouse answers available=false, never a fabricated
    # number.
    get_guide_analytics() returns (rep: analyticsView)
    # Returns the brand blueprint's version history — every
    # stored version's number and edit time, newest first — which is the
    # point-in-time-recovery and audit trail behind the authoring plane. Metadata
    # only: the documents are not returned. SuperAdmin only, like the rest of this
    # plane. The history is listable even when the current stored document no longer
    # parses, so a schema-drifted row can still be diagnosed.
    get_guide_blueprint_versions() returns (rep: blueprintVersionsView)
    # Returns the studio's portfolio operations rollup: stage, status,
    # checklist progress, decisions waiting on people, latest evidence, failed runs,
    # and spend for every venture operated by the studio organization.
    get_guide_portfolio() returns (rep: portfolioView)
    # Returns the ENABLED tactics corpus for the caller's org: the tactics
    # library narrowed by the explicit category/workload filters AND by the org's
    # OBSERVED growth stage and capability signals (a tactic's tags are
    # preconditions, so it surfaces only once the org can act on it). Passing stage
    # PREVIEWS the corpus at that stage instead of the observed one. The content is
    # shared platform data — no org's records — and the read is never a billable
    # effect.
    get_guide_strategies(req: strategiesQuery) returns (rep: corpusView)
    # Returns the caller org's next-best quests: the available, non-terminal
    # steps of its journey ranked by how much downstream work each unblocks, each with
    # the grounded reason it is a good next move and whether the Business AI can run
    # it, plus the org's funnel and the GTM recommendations derived from it. A
    # best-effort AI narrative over exactly those quests and numbers is included when
    # an AI plane is wired. READ-ONLY: it advises and never runs a step — the only
    # executing path is POST /v1/guide/steps/{id}/do.
    get_guide_suggest() returns (rep: suggestResponse)
    # Returns every venture in the caller studio organization's
    # portfolio that the caller is authorized to access, with its live checklist
    # progress and shared studio templates.
    get_guide_ventures() returns (rep: ventureList)
    # Returns one venture from the caller studio's portfolio by slug,
    # including its setup checkpoints, live checklist progress, and shared templates.
    get_guide_ventures_by_slug(req: ventureRef) returns (rep: VentureRecord)
    # Answers a founder's question about their launch journey as the Business AI
    # coach: it grounds the reply in the org's REAL progress, its ranked available
    # quests and its analytics funnel, and returns those candidate quests alongside so
    # the caller can act on one. READ-ONLY — it advises and never runs a step, so it
    # cannot be talked into performing an action; the only executing path is POST
    # /v1/guide/steps/{id}/do. One AI completion per call, billed to the caller's own
    # payer.
    post_guide_chat(req: chatRequest) returns (rep: chatResponse)
    # Creates or imports a venture under the caller studio's portfolio,
    # captures its thesis, sponsor, owner, initial budget and seed evidence, and
    # enrolls its isolated venture organization in a versioned Guide playbook
    # (defaulting to venture). Repeated or interrupted calls with the same slug
    # resume idempotently without duplicating the venture, its team checkpoint, or
    # its initial budget.
    post_guide_ventures(req: createVentureIn) returns (rep: VentureRecord)
}

# ---------------------------------------------------------------------
# 10 op(s) here. What follows is what this schema does not carry.
#
# blocked (10) — the op is absent; the field has no wire form:
#   delete_guide_curriculum  curriculumView.Curriculum  guide.Curriculum  (reaches one)
#   get_guide  overviewView.Steps  []guide.stepView  (no wire form)
#   get_guide_blueprint  blueprintView.Blueprint  guide.Blueprint  (reaches one)
#   get_guide_curriculum  curriculumView  guide.curriculumView  (reaches one)
#   get_guide_profile  profileResponse.Signals  guide.SignalSet  (map)
#   post_guide_steps_by_id_done  overviewView  guide.overviewView  (reaches one)
#   post_guide_steps_by_id_reset  overviewView  guide.overviewView  (reaches one)
#   post_guide_steps_by_id_skip  overviewView  guide.overviewView  (reaches one)
#   post_guide_steps_by_id_start  overviewView  guide.overviewView  (reaches one)
#   post_guide_ventures_by_slug_lifecycle  ventureLifecycleOut.Export  guide.ventureExportBundle  (reaches one)
#
# opaque (13) — crosses, arrives without its name:
#   VentureRecord.Progress  guide.progressView
#   VentureRecord.Setup  guide.VentureSetupState
#   VentureRecord.Templates  guide.Page (list element)
#   actionsView.Data  guide.ActionRecord (list element)
#   analyticsView.Funnel  guide.Funnel
#   blueprintVersionsView.Versions  guide.VersionMeta (list element)
#   chatResponse.Funnel  guide.Funnel
#   chatResponse.Suggestions  guide.suggestion (list element)
#   corpusView.Strategies  guide.strategyView (list element)
#   portfolioView.Ventures  guide.VentureRecord (list element)
#   suggestResponse.Funnel  guide.Funnel
#   suggestResponse.Suggestions  guide.suggestion (list element)
#   ventureList.Ventures  guide.VentureRecord (list element)
