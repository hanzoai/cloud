# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package guide

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

interface guide {
    # Returns the caller org's Business AI action ledger, most recent
    # first: every "do it for me" tool call, the arguments it ran with, its result and
    # whether it succeeded. It is the audit-visible record of what the agent did on
    # the org's behalf, and the backing state for the "acted" auto-detect signal.
    get_guide_actions() returns (rep: actionsView)
    # Analytics returns the caller org's funnel from the analytics lens plus the GTM
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
    # Strategies returns the ENABLED tactics corpus for the caller's org: the tactics
    # library narrowed by the explicit category/workload filters AND by the org's
    # OBSERVED growth stage and capability signals (a tactic's tags are
    # preconditions, so it surfaces only once the org can act on it). Passing stage
    # PREVIEWS the corpus at that stage instead of the observed one. The content is
    # shared platform data — no org's records — and the read is never a billable
    # effect.
    get_guide_strategies(req: strategiesQuery) returns (rep: corpusView)
    # Suggest returns the caller org's next-best quests: the available, non-terminal
    # steps of its journey ranked by how much downstream work each unblocks, each with
    # the grounded reason it is a good next move and whether the Business AI can run
    # it, plus the org's funnel and the GTM recommendations derived from it. A
    # best-effort AI narrative over exactly those quests and numbers is included when
    # an AI plane is wired. READ-ONLY: it advises and never runs a step — the only
    # executing path is POST /v1/guide/steps/{id}/do.
    get_guide_suggest() returns (rep: suggestResponse)
    # Chat answers a founder's question about their launch journey as the Business AI
    # coach: it grounds the reply in the org's REAL progress, its ranked available
    # quests and its analytics funnel, and returns those candidate quests alongside so
    # the caller can act on one. READ-ONLY — it advises and never runs a step, so it
    # cannot be talked into performing an action; the only executing path is POST
    # /v1/guide/steps/{id}/do. One AI completion per call, billed to the caller's own
    # payer.
    post_guide_chat(req: chatRequest) returns (rep: chatResponse)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# blocked (9) — the op is absent; the field has no wire form:
#   delete_guide_curriculum  curriculumView.Curriculum  guide.Curriculum  (reaches one)
#   get_guide  overviewView.Steps  []guide.stepView  (no wire form)
#   get_guide_blueprint  blueprintView.Blueprint  guide.Blueprint  (reaches one)
#   get_guide_curriculum  curriculumView  guide.curriculumView  (reaches one)
#   get_guide_profile  profileResponse.Signals  guide.SignalSet  (map)
#   post_guide_steps_by_id_done  overviewView  guide.overviewView  (reaches one)
#   post_guide_steps_by_id_reset  overviewView  guide.overviewView  (reaches one)
#   post_guide_steps_by_id_skip  overviewView  guide.overviewView  (reaches one)
#   post_guide_steps_by_id_start  overviewView  guide.overviewView  (reaches one)
#
# opaque (8) — crosses, arrives without its name:
#   actionsView.Data  guide.ActionRecord (list element)
#   analyticsView.Funnel  guide.Funnel
#   blueprintVersionsView.Versions  guide.VersionMeta (list element)
#   chatResponse.Funnel  guide.Funnel
#   chatResponse.Suggestions  guide.suggestion (list element)
#   corpusView.Strategies  guide.strategyView (list element)
#   suggestResponse.Funnel  guide.Funnel
#   suggestResponse.Suggestions  guide.suggestion (list element)
