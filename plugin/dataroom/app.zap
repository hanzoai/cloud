# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package dataroom

struct dataroomAddDocument {
    SizedIn    bytes @0
    DocumentId text  @8
    ID         text  @16
    OrderIndex text  @24
}

struct dataroomCreate {
    SizedIn     bytes @0
    Description text  @8
    Name        text  @16
}

struct dataroomDocumentOne {
    Document bytes @0
}

struct dataroomDocuments {
    Documents list<bytes> @0
}

struct dataroomLinkCreate {
    SizedIn        bytes      @0
    AllowDownload  text       @8
    AllowList      list<text> @16
    DataroomId     text       @24
    DenyList       list<text> @32
    DocumentId     text       @40
    EmailProtected text       @48
    ExpiresAt      text       @56
    Name           text       @64
    Password       text       @72
}

struct dataroomLinkOne {
    Link bytes @0
}

struct dataroomLinkStats {
    LinkId         text        @0
    Pages          list<bytes> @8
    TotalPageViews i64         @16
    TotalViews     i64         @24
}

struct dataroomLinks {
    Links list<bytes> @0
}

struct dataroomLiveness {
    Service text @0
    Status  text @8
}

struct dataroomMembership {
    DataroomDocumentId text @0
    DataroomId         text @8
    DocumentId         text @16
}

struct dataroomRef {
    ID text @0
}

struct dataroomRoomDetailOne {
    Dataroom bytes @0
}

struct dataroomRoomOne {
    Dataroom bytes @0
}

struct dataroomRooms {
    Datarooms list<bytes> @0
}

struct dataroomStats {
    DataroomId     text        @0
    Links          list<bytes> @8
    TotalPageViews i64         @16
    TotalViews     i64         @24
}

struct documentRef {
    ID text @0
}

struct linkRef {
    LinkID text @0
}

struct roomStatsRef {
    DataroomID text @0
}

struct slugRef {
    Slug text @0
}

struct trustAsk {
    SizedIn bytes @0
    Accept  bool  @8
    Email   text  @16
    Item    text  @24
    Party   text  @32
    Reason  text  @40
    Slug    text  @48
}

struct trustAsked {
    ID    text @0
    State text @8
}

struct trustDecision {
    SizedIn bytes @0
    Days    i64   @8
    ID      text  @16
    Note    text  @24
}

struct trustDesk {
    Grants    list<bytes> @0
    Items     list<bytes> @8
    Name      text        @16
    Nda       text        @24
    Published bool        @32
    Requests  list<bytes> @40
    Slug      text        @48
}

struct trustEdit {
    SizedIn   bytes @0
    Body      text  @8
    Document  text  @16
    Framework text  @24
    ID        text  @32
    Name      text  @40
    Retired   bool  @48
    Summary   text  @56
    Tier      text  @64
}

struct trustGranted {
    Delivery  text @0
    ExpiresAt i64  @8
    Link      text @16
    State     text @24
}

struct trustItemView {
    Attester  text @0
    Body      text @8
    CreatedAt i64  @16
    Document  text @24
    Framework text @32
    ID        text @40
    Kind      text @48
    Name      text @56
    Retired   bool @64
    Summary   text @72
    Tier      text @80
    UpdatedAt i64  @88
}

struct trustPage {
    Items list<bytes> @0
    Name  text        @8
    Nda   text        @16
    Slug  text        @24
}

struct trustPublish {
    SizedIn   bytes @0
    Attester  text  @8
    Body      text  @16
    Document  text  @24
    Framework text  @32
    Kind      text  @40
    Name      text  @48
    Summary   text  @56
    Tier      text  @64
}

struct trustRefused {
    State text @0
}

struct trustRosters {
    Centers list<bytes> @0
}

struct trustSettings {
    SizedIn bytes @0
    Name    text  @8
    Nda     text  @16
    Publish bool  @24
    Slug    text  @32
}

interface dataroom {
    # Roster lists every published trust centre in the deployment with the size of its
    # queue — the platform's view of who is running one and who is leaving people
    # waiting.
    # It is the ONE cross-tenant read in this subsystem and it is refused to anyone who
    # is not a SuperAdmin: a member of the reserved admin org, the same predicate every
    # other subsystem asks. An org's own admin is a different, org-scoped fact and does
    # not pass here — reading it as platform authority is how one customer comes to see
    # every other customer's queue.
    # It counts and does not read: no item, request, address or grant of any org's
    # crosses into the answer.
    get_admin_dataroom_trust() returns (rep: trustRosters)
    # Rolls up every share link pointing at one data room:
    # session and page-view totals for the room, plus the per-page breakdown for each
    # link beneath it.
    # A room id outside the caller's own tenant store is not found. Only links that
    # NAME the room are counted — a link created over a single document contributes
    # nothing here, even when that document also sits in the room.
    get_dataroom_analytics_dataroom_by_dataroomid(req: roomStatsRef) returns (rep: dataroomStats)
    # Reports how one share link was actually read: total viewing
    # sessions, total page views, and per page the view count, the summed dwell
    # measure and its average.
    # The link is resolved in the caller's OWN tenant store, so another org's link id
    # is not found — knowing a link id is enough to OPEN the room it shares, and
    # never enough to read who has been reading it.
    get_dataroom_analytics_link_by_linkid(req: linkRef) returns (rep: dataroomLinkStats)
    # Returns every data room in the caller org's own store, newest
    # first, with its short public id, name, description and timestamps.
    # Documents are not included — a room's contents come from reading the single
    # room.
    get_dataroom_datarooms() returns (rep: dataroomRooms)
    # Reads one of the caller org's data rooms together with every
    # document in it, each carrying its membership id and order index.
    # The documents are sorted by that index with unordered ones last and creation
    # time breaking ties — the SAME order a link's visitor sees, so this is what the
    # room looks like from the outside. A room id outside the caller's own tenant
    # store is not found.
    get_dataroom_datarooms_by_id(req: dataroomRef) returns (rep: dataroomRoomDetailOne)
    # Returns every document in the caller org's own store, newest
    # first — name, opaque storage key, content type, page count, size and
    # timestamps.
    # Tenant isolation is the per-org store itself: there is one SQLite file per org
    # and the org is never a parameter, so no input the caller controls can address
    # another tenant's documents. Metadata only — the bytes come from the file route.
    get_dataroom_documents() returns (rep: dataroomDocuments)
    # Reads one of the caller org's documents — its name, opaque storage
    # key, content type, page count, size and timestamps.
    # The lookup runs in the caller's own tenant store, so an id belonging to another
    # org is not found exactly like one that never existed. Metadata only: the bytes
    # are a separate read.
    get_dataroom_documents_by_id(req: documentRef) returns (rep: dataroomDocumentOne)
    # Health reports that the data room subsystem is up.
    # It answers before the bundle loads, holds no state and touches no store, so it
    # stays true in exactly the situation an operator is probing for. It says nothing
    # about whether a room can be OPENED — that is what the room operations answer —
    # because a liveness probe that fails on a dependency takes a working process out
    # of rotation.
    get_dataroom_health() returns (rep: dataroomLiveness)
    # Returns every live share link in the caller org's own store,
    # newest first, with the controls a visitor will meet: whether an address is
    # required, whether a password is set, the allow and deny lists, whether download
    # is permitted, and when the link expires.
    # Archived links are omitted entirely. A link reports only THAT a password is
    # set — the stored form is a bcrypt hash and no route returns it.
    get_dataroom_links() returns (rep: dataroomLinks)
    # Answers the caller org's OWN trust centre: its settings, every item it
    # holds in both tiers, the requests waiting on it, and the grants it has made.
    # The org is the caller's, taken from the validated bearer and from nothing else,
    # so this op cannot be pointed at another tenant — there is no field for one. An
    # org that has never opened a centre reads back an empty one rather than an error,
    # because having no trust centre is an ordinary state and this is the read that
    # tells you so.
    get_dataroom_trust() returns (rep: trustDesk)
    # Answers an org's public trust centre: its name, the text a party must
    # accept to ask for a document, and every item it publishes.
    # An item is either available NOW — the things the org states itself, its policies,
    # its filled questionnaires, its subprocessor list, its knowledge base — or
    # available ON REQUEST, which is everything an independent auditor put their name
    # to. Both are listed by name and kind, so a reader can see WHAT exists before
    # asking for it; only the second withholds the content.
    # No principal is involved and none is accepted: the org is resolved from the
    # address, which answers only for a centre its owner has published. An address
    # nobody publishes at is not found, the same answer an unpublished one gets.
    get_dataroom_trust_center_by_slug(req: slugRef) returns (rep: trustPage)
    # Amend changes an item on the caller org's trust centre — replace its file with a
    # newer edition, move it between public and gated, rewrite what it says, or retire
    # it — and answers with the item as it now stands.
    # Retiring is the withdrawal: the item leaves the public centre immediately and can
    # no longer be granted, while grants already made over it stand, because a release
    # that happened is part of the record and un-happening it in the record would be a
    # lie. Restoring is the same call with retired false.
    # Moving an item an independent auditor signed to the public tier is refused, and
    # refused by the database rather than only here. Only an admin of the org may call
    # it, and the item is resolved in that org's own store, so another org's id is not
    # found.
    patch_dataroom_trust_artifacts_by_id(req: trustEdit) returns (rep: trustItemView)
    # Opens a new data room for the caller org and answers with it,
    # including the short public id it is addressed by.
    # `name` is required; without it the call is refused and the tenant store is
    # untouched, because a dispatch answering 4xx rolls its transaction back. A new
    # room holds no documents and is reachable by NOBODY until a share link is
    # created over it — opening a room and granting access are two separate acts, so
    # a room cannot leak by existing.
    post_dataroom_datarooms(req: dataroomCreate) returns (rep: dataroomRoomOne)
    # Puts an already-uploaded document into one of the caller
    # org's data rooms and answers with the new membership id.
    # It ATTACHES, it never uploads: the bytes must already be stored, so the usual
    # order is upload the document, then add it to the room. Both the room and the
    # document must exist in the caller's own store — either missing is not found —
    # and a document already in the room is refused as a conflict rather than
    # duplicated.
    post_dataroom_datarooms_by_id_documents(req: dataroomAddDocument) returns (rep: dataroomMembership)
    # Grants access: it mints a public share link over one data
    # room (`dataroomId`) or one document (`documentId`) — one of the two is
    # required — and answers with the link, whose `id` is the token a visitor opens
    # it with.
    # This is how a party is let in. The controls are declared HERE and enforced on
    # the viewer surface: `password` is hashed with bcrypt before storage and is
    # never readable back, `emailProtected` (on by default) makes a visitor state an
    # address, `allowList`/`denyList` narrow which addresses pass, `allowDownload`
    # (off by default) governs downloads, and `expiresAt` closes the link. The target
    # room or document must exist in the caller's own store or it is not found.
    # Creating a link also writes dataroom's ONE cross-tenant row: the link id to
    # owning org mapping an anonymous visitor is routed through. That write is part
    # of the operation — if it fails the call is 500 — so a link that no visitor
    # could open is never handed back as usable.
    # The address a visitor later states is recorded UNVERIFIED, so a link gated only
    # by email is openable by anyone the link reaches. Use a password for a link that
    # must not travel.
    post_dataroom_links(req: dataroomLinkCreate) returns (rep: dataroomLinkOne)
    # Publish puts an item on the caller org's trust centre and answers with it.
    # The item is GATED unless it says otherwise, so a kind nobody has thought of yet
    # arrives private and someone has to release it deliberately — that default is what
    # keeps an auditor's report from becoming readable because a field went unset. An
    # item whose attester is "auditor" cannot be public at all: the database refuses the
    # pair, so no path through this API can publish one.
    # A file is optional and is uploaded FIRST, through POST /v1/dataroom/documents,
    # then named here — the data room is the one place bytes enter, so a trust centre
    # document is an ordinary data-room document and inherits its storage, its grants
    # and its page-by-page access record. A gated item that has a file is added to the
    # org's release room, which is what lets a party be granted the whole gated tier in
    # one link.
    # Only an admin of the org may call it.
    post_dataroom_trust_artifacts(req: trustPublish) returns (rep: trustItemView)
    # Records a request to read what an independent auditor signed, and
    # answers with its id.
    # The org that owns the centre decides. Nothing is released here and no link is
    # minted: this writes the ask down, which is the whole promise the form makes.
    # The write is the answer — a request that could not be stored is an error, never
    # a receipt, so a form can never appear to have been sent and be gone.
    # `email` is required and is the ONLY address the eventual grant will admit, so an
    # address the asker cannot read is an ask that cannot be answered. Where the centre
    # states an NDA, `accept` must be true and the text in force is recorded verbatim
    # against the request.
    # Asking twice for the same thing from the same address is the SAME ask: the second
    # answers with the first's id rather than opening a second row, which is also what
    # keeps an anonymous endpoint from filling a tenant's store.
    post_dataroom_trust_center_by_slug_requests(req: trustAsk) returns (rep: trustAsked)
    # Grant answers a request by opening access: it mints a share link over what was
    # asked for, addressed to the address that asked and closing at expiry, records the
    # decision, and mails the asker.
    # The link is NEVER a public URL. It carries the asker's address on its allow list,
    # so forwarding it to somebody else does not open it, and it expires. What the
    # party then does with it — which document, which page, for how long — is recorded
    # by the data room's own view tracking, which is where the access record for this
    # release lives; there is no second log.
    # A request that was already answered is refused rather than answered twice, so a
    # second click cannot mint a second link. Only an admin of the org may call it, and
    # the request is resolved in that org's own store, so another org's request id is
    # not found — which is also what stops one org deciding another's queue.
    # Mail is best effort and the grant does not depend on it: a deployment that sends
    # no mail still records the grant and says so in `delivery`, so the approver knows
    # to pass the address on themselves.
    post_dataroom_trust_requests_by_id_grant(req: trustDecision) returns (rep: trustGranted)
    # Refuse answers a request by declining it, recording who declined and why.
    # Nothing is released and no link is minted. The refusal STAYS on the record beside
    # the ask — a request that was turned down is part of the access record exactly as
    # one that was granted is, and deleting it would leave a queue that only ever shows
    # the decisions somebody liked.
    # A request that was already answered is refused rather than answered twice. Only an
    # admin of the org may call it, and the request is resolved in that org's own store,
    # so another org's request id is not found.
    post_dataroom_trust_requests_by_id_refuse(req: trustDecision) returns (rep: trustRefused)
    # SetCenter opens, publishes or withdraws the caller org's trust centre and answers
    # with the centre as it now stands.
    # Publishing requires a name and an address, and the address must be free: another
    # org already answering there is a conflict, never a takeover. Withdrawing closes
    # the public endpoint only — items, grants and the access record are untouched, so
    # an org can go quiet and come back without losing anything.
    # Only an admin of the org may call it. The org is the caller's own, so there is no
    # field naming one and no way to point this at another tenant.
    put_dataroom_trust(req: trustSettings) returns (rep: trustDesk)
}

# ---------------------------------------------------------------------
# 20 op(s) here. What follows is what this schema does not carry.
#
# dropped (8) — the value does not cross, and nothing fails:
#   dataroomAddDocument.SizedIn  goja.SizedIn  (empty message)
#   dataroomCreate.SizedIn  goja.SizedIn  (empty message)
#   dataroomLinkCreate.SizedIn  goja.SizedIn  (empty message)
#   trustAsk.SizedIn  goja.SizedIn  (empty message)
#   trustDecision.SizedIn  goja.SizedIn  (empty message)
#   trustEdit.SizedIn  goja.SizedIn  (empty message)
#   trustPublish.SizedIn  goja.SizedIn  (empty message)
#   trustSettings.SizedIn  goja.SizedIn  (empty message)
#
# opaque (22) — crosses, arrives without its name:
#   dataroomAddDocument.SizedIn  goja.SizedIn
#   dataroomCreate.SizedIn  goja.SizedIn
#   dataroomDocumentOne.Document  dataroom.dataroomDocument
#   dataroomDocuments.Documents  dataroom.dataroomDocument (list element)
#   dataroomLinkCreate.SizedIn  goja.SizedIn
#   dataroomLinkOne.Link  dataroom.dataroomLink
#   dataroomLinkStats.Pages  dataroom.dataroomPageStat (list element)
#   dataroomLinks.Links  dataroom.dataroomLink (list element)
#   dataroomRoomDetailOne.Dataroom  dataroom.dataroomRoomDetail
#   dataroomRoomOne.Dataroom  dataroom.dataroomRoom
#   dataroomRooms.Datarooms  dataroom.dataroomRoom (list element)
#   dataroomStats.Links  dataroom.dataroomLinkStats (list element)
#   trustAsk.SizedIn  goja.SizedIn
#   trustDecision.SizedIn  goja.SizedIn
#   trustDesk.Grants  dataroom.trustGrantView (list element)
#   trustDesk.Items  dataroom.trustItemView (list element)
#   trustDesk.Requests  dataroom.trustAskView (list element)
#   trustEdit.SizedIn  goja.SizedIn
#   trustPage.Items  dataroom.trustItem (list element)
#   trustPublish.SizedIn  goja.SizedIn
#   trustRosters.Centers  dataroom.trustRoster (list element)
#   trustSettings.SizedIn  goja.SizedIn
