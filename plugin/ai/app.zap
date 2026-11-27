# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package ai

struct CorrectIn {
    Decision text @0
    Question text @8
    Answer   text @16
    Note     text @24
    Cohort   text @32
}

struct Correction {
    ID       text       @0
    Decision text       @8
    Question text       @16
    Type     text       @24
    Labels   list<text> @32
    Answer   text       @40
    Base     text       @48
    Note     text       @56
    Cohort   text       @64
    State    text       @72
    By       text       @80
    At       i64        @88
}

struct Corrections {
    Data      list<bytes> @0
    Questions list<bytes> @8
}

struct DecisionIn {
    Decision text @0
}

struct UncorrectIn {
    Decision text @0
    ID       text @8
}

struct aiChat {
    Answer  text  @0
    Model   text  @8
    Receipt bytes @16
}

struct aiChatIn {
    Model     text @0
    Prompt    text @8
    System    text @16
    MaxTokens i64  @24
}

struct aiDecision {
    Decision bytes @0
    Receipt  bytes @8
}

struct aiFrameIn {
    Model          text        @0
    Prompt         text        @8
    Instruction    text        @16
    Size           text        @24
    Layout         text        @32
    Theme          text        @40
    Eyebrow        text        @48
    Headline       text        @56
    Accent         text        @64
    Subhead        text        @72
    Pills          list<text>  @80
    Terminal       bytes       @88
    TerminalBox    bytes       @96
    CopyBox        bytes       @104
    BackgroundB64  text        @112
    EditBackground bool        @120
    Layers         list<bytes> @128
    LayerPatches   list<bytes> @136
}

struct aiFrameOut {
    Created i64         @0
    Model   text        @8
    Mode    text        @16
    Data    list<bytes> @24
    Receipt bytes       @32
}

struct aiMCPQuery {
    Names bool @0
}

struct aiMCPSurface {
    Tools i64         @0
    Apps  list<bytes> @8
    Names list<text>  @16
}

struct aiModelsQuery {
    Q          text @0
    Class      text @8
    Family     text @16
    Capability text @24
    Free       bool @32
    ZDR        bool @33
    Limit      i64  @40
}

struct aiRate {
    Org     text @0
    Rpm     i64  @8
    Default i64  @16
    Own     i64  @24
}

struct aiRateIn {
    Rpm i64 @0
}

interface ai {
    # Asks one model one prompt and answers the reply, which model served it and who
    # paid. The call is the caller's own POST /v1/chat/completions, run as the caller,
    # so it is gated, billed and refused exactly as that route is; a refusal names its
    # code and what lifts it, never an amount. An empty model asks the deployment's
    # default Hanzo model.
    aiChat(req: aiChatIn) returns (rep: aiChat)
    # Alias for POST /v1/frame/generations: generates a layered Hyperframe graphic
    # composite (strictly text-free diffusion background plus pixel-perfect
    # @hanzo/font typography and HTML/UI layers). Prefer POST /v1/frame/generations
    # to create composites and POST /v1/frame/edits for ~15ms zero-diffusion layer
    # edits. For layered HyperFrames motion/video compositions (MP4/WebM), use
    # POST /v1/studio/runs with render.composition, video.edit, or video.generate.
    # prompt is required: a request without one is a 400 and no model is asked.
    aiFrame(req: aiFrameIn) returns (rep: aiFrameOut)
    # Edits an existing Hyperframe composite or its layer stack (POST /v1/frame/edits):
    # pass background_b64 and layers (or headline, layout, theme, terminal, instruction,
    # or layer_patches) to redesign text, reposition panels, swap themes (light, glass,
    # dark), or add custom card/HTML layers in ~15ms with zero diffusion cost; or set
    # edit_background=true (with qwen-image-edit or iro) to redo the visual background
    # while keeping all typography and UI layers intact. background_b64 or layers is
    # required, plus something to change: a request without them is a 400 and no model
    # is asked.
    aiFrameEdit(req: aiFrameIn) returns (rep: aiFrameOut)
    # Generates a Hyperframe graphic composite (POST /v1/frame/generations): strips
    # all copy and text triggers from the diffusion prompt so the image model (iro,
    # qwen-image, z-image-turbo, flux-3) renders a strictly text-free background,
    # auto-detects the dark hardware/terminal surface bounding box, and layers
    # pixel-perfect @hanzo/font (Zen + Zen Mono) typography and interactive HTML/UI
    # elements on top. Returns the composited PNG (b64_json), the clean diffusion
    # background (background_b64), self-contained Hyperframe HTML (html), and the
    # editable layers stack (layers). prompt is required: a request without one is a
    # 400 and no model is asked.
    aiFrameGenerate(req: aiFrameIn) returns (rep: aiFrameOut)
    # Reports what THIS PROCESS's MCP server carries: how many tools its own
    # registry projects, optionally their names, and which subsystems this process
    # composed. It is the answer to "is this MCP server up and does it have anything
    # behind it" — a question a status code cannot answer, since an empty server and
    # a full one are both 200. What the FLEET's server carries is the fleet server's
    # own answer: POST /v1/mcp, tools/list, which asks every subsystem and names the
    # ones that did not reply.
    aiMCPTools(req: aiMCPQuery) returns (rep: aiMCPSurface)
    # Reads the per-minute limit as it governs the organization the caller acts in: the
    # model requests one person may make in a minute on every surface — the API, chat,
    # coding runs, bots and MCP — the platform's default, which is the most the
    # organization may set, and the organization's own setting. Any member may read it.
    aiRateLimit() returns (rep: aiRate)
    # Sets the model requests one person may make in a minute acting in the
    # organization the caller acts in, on every surface: 1 up to the platform's
    # default, never more, or 0 to hold its people to the default. An owner or admin of
    # the organization, or a SuperAdmin, may; a member may not, and a shared sign-up
    # org, which has no admin to speak for its people, is refused. A change a page made
    # with the caller's cookie, not the caller (cloud.CSRF), is refused (403). Live at
    # once for every model request. On the audit trail before the write (ai.limits.rate.attempt)
    # and with its outcome (ai.limits.rate), the setting before and after; a trail that
    # does not take the attempt refuses the change (503), and nothing moves.
    aiSetRateLimit(req: aiRateIn) returns (rep: aiRate)
    # Files a correction of one question of one of the org's Kai
    # decisions: the right answer, which must be one of that question's options and
    # not what Kai answered, and why. A decision Kai captured can teach a capability
    # of its question (/v1/capabilities); one made with capture off is kept with
    # state missing and cannot. The org's first correction turns capture on, so its
    # next decisions can teach. Refused: 409 not_wrong when the answer is Kai's own,
    # 409 corrected when the question already has a correction, 404 when the
    # decision captured no such question.
    decisionsCorrect(req: CorrectIn) returns (rep: Correction)
    # Answers one of the org's decisions' corrections, each
    # captured (it can teach) or missing (the decision was made with capture off).
    decisionsCorrections(req: DecisionIn) returns (rep: Corrections)
    # Deletes one correction of one of the org's decisions.
    # A capability already taught from it keeps its versions.
    decisionsUncorrect(req: UncorrectIn)
}

# ---------------------------------------------------------------------
# 10 op(s) here. What follows is what this schema does not carry.
#
# blocked (5) — the op is absent; the field has no wire form:
#   aiDecide  aiDecideIn.Questions  map[string]jsontext.Value  (map)
#   aiLimits  aiLimits.Classes  map[string]*ai.aiClass  (map)
#   aiLimits  aiLimits.Entitlements  map[string]*ai.aiEntitlement  (map)
#   aiLimits  aiLimits.Pays  map[string]string  (map)
#   aiModels  aiModels.Models  []ai.aiModel  (no wire form)
#
# opaque (12) — crosses, arrives without its name:
#   Corrections.Data  client.Correction (list element)
#   Corrections.Questions  client.Captured (list element)
#   aiChat.Receipt  ai.aiReceipt
#   aiDecision.Receipt  ai.aiReceipt
#   aiFrameIn.CopyBox  ai.aiFrameBox
#   aiFrameIn.LayerPatches  ai.aiFrameLayer (list element)
#   aiFrameIn.Layers  ai.aiFrameLayer (list element)
#   aiFrameIn.Terminal  ai.aiFrameTerminal
#   aiFrameIn.TerminalBox  ai.aiFrameBox
#   aiFrameOut.Data  ai.aiFrameItem (list element)
#   aiFrameOut.Receipt  ai.aiReceipt
#   aiMCPSurface.Apps  ai.aiMCPApp (list element)
