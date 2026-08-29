# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package content

struct GenerateInput {
    DocType  text @0
    Title    text @8
    Brief    text @16
    Product  text @24
    Design   text @32
    Channels text @40
    Project  text @48
    Voice    text @56
    Tone     text @64
    Kind     text @72
    Source   text @80
    Model    text @88
}

struct GenerateResult {
    DocType text @0
    Name    text @8
    Status  text @16
}

struct PublishInput {
    DocType    text @0
    Name       text @8
    ScheduleAt text @16
}

struct boardPage {
    Data  list<bytes> @0
    Count i64         @8
}

struct boardQuery {
    Status  text @0
    Project text @8
    DocType text @16
    Limit   i64  @24
}

struct channelList {
    Data list<bytes> @0
}

struct transitionIn {
    DocType    text @0
    Name       text @8
    To         text @16
    ScheduleAt text @24
}

interface content {
    # Aggregates the caller org's marketing content across every publishable
    # content type into ONE queue board — the cross-type read the framework's
    # per-DocType list cannot give. It never fails on a partial outage: a content type
    # the org has not installed, or one whose search errors, is skipped and logged
    # rather than failing the whole board.
    get_content_board(req: boardQuery) returns (rep: boardPage)
    # Lists the distribution channels the caller's org has connected — the
    # social integrations a publish can target. A deployment with no distribution edge
    # wired answers 503 rather than an empty list that would read as "no channels".
    get_content_channels() returns (rep: channelList)
    # Draft a piece of marketing content and file it in the CMS as a draft.
    # Answers 201 with the created draft's identity — {doctype, name, status} — and the
    # document itself lands in the CMS through the SAME validate and lifecycle-hook
    # pipeline an ordinary create runs. This is a WRITE, not a preview: there is no
    # dry-run, and every call that succeeds leaves a document behind.
    # `doctype` picks which of two generation planes runs, and they are the only two.
    # Campaign and SocialPost are drafted as brand COPY on the platform AI plane (zen5 by
    # default, overridable per request with `model` or per deployment); Asset is a studio
    # image render the AI plane never sees. Everything else about the call is identical.
    # MONEY, metered in exactly one place per mode and never both. Copy rides the
    # platform's own inference meter — the org's balance is authorised before the model
    # call and debited at the exact token cost after — so content never re-bills it. A
    # studio render is invisible to that meter, so content is the sole meter for it: the
    # org is gated BEFORE the GPU compute and refused 402 when out of funds or over its
    # spend cap, and the debit is recorded only once the render actually returns, because
    # the billable event is the consumed compute and not the CMS row. `project` rides the
    # BODY rather than a server-minted identity claim, so it attributes spend but a
    # project-scoped cap stays soft on it — the org is the value that is enforced.
    # The org is the caller's own, resolved once from the validated principal and never
    # read from the body; a caller without one is refused 403. Status is not the
    # generator's to choose: a generated item is ALWAYS a draft, and the storage-boundary
    # hook enforces that a second time.
    # It fails closed rather than inventing anything. An unknown content type is 404 and a
    # deployment whose marketing module is not installed is 409 naming the install call.
    # An AI plane or studio that is unconfigured or unreachable, a graph the studio
    # rejects, and a render that does not return in time all degrade to 503 — never
    # fabricated copy, never a fake render. A `source_media` that fails the SSRF and
    # traversal validator is 400 raised before the billing gate and before the studio is
    # contacted, so a hostile source never costs the caller anything.
    post_content_generate(req: GenerateInput) returns (rep: GenerateResult)
}

# ---------------------------------------------------------------------
# 3 op(s) here. What follows is what this schema does not carry.
#
# blocked (3) — the op is absent; the field has no wire form:
#   get_content_lifecycle  stateGraph.Transitions  map[string][]string  (map)
#   post_content_by_doctype_by_name_transition  TransitionResult.Distribution  content.PublishResult  (reaches one)
#   post_content_publish  PublishResult.ExternalIDs  map[string]string  (map)
#
# opaque (2) — crosses, arrives without its name:
#   boardPage.Data  content.boardItem (list element)
#   channelList.Data  content.Channel (list element)
