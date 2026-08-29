# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package esign

struct esignCompletion {
    DocumentStatus text @0
    RecipientID    text @8
    Sealed         bool @16
}

struct esignCompletionIn {
    SizedIn bytes @0
}

struct esignDocument {
    CompletedAt  i64         @0
    CreatedAt    i64         @8
    ExternalID   text        @16
    Fields       list<bytes> @24
    ID           text        @32
    Message      text        @40
    Recipients   list<bytes> @48
    SigningOrder text        @56
    Source       text        @64
    Status       text        @72
    Subject      text        @80
    Title        text        @88
    UpdatedAt    i64         @96
}

struct esignDocuments {
    Documents list<bytes> @0
}

struct esignFieldIn {
    SizedIn     bytes @0
    ID          text  @8
    RecipientID text  @16
    Type        text  @24
    Page        text  @32
    PositionX   text  @40
    PositionY   text  @48
    Width       text  @56
    Height      text  @64
    FieldMeta   bytes @72
}

struct esignHealth {
    Service text @0
    Status  text @8
}

struct esignInsertion {
    FieldID  text @0
    Inserted bool @8
}

struct esignInvite {
    Email text @0
    ID    text @8
    Name  text @16
    Role  text @24
    Token text @32
}

struct esignLinks {
    ID         text        @0
    Recipients list<bytes> @8
    Status     text        @16
}

struct esignPDF {
    Filename  text @0
    ID        text @8
    PdfBase64 text @16
    Sealed    bool @24
    Status    text @32
}

struct esignPlacement {
    ID          text @0
    Page        f64  @8
    RecipientID text @16
    Type        text @24
}

struct esignRecipientIn {
    SizedIn      bytes @0
    ID           text  @8
    Email        text  @16
    Name         text  @24
    Role         text  @32
    SigningOrder text  @40
}

struct esignRef {
    ID text @0
}

struct esignRejectIn {
    SizedIn bytes @0
    Reason  text  @8
}

struct esignRejection {
    RecipientID text @0
    Status      text @8
}

struct esignSendIn {
    SizedIn bytes @0
    ID      text  @8
}

struct esignSession {
    Document  bytes       @0
    Fields    list<bytes> @8
    PdfBase64 text        @16
    Recipient bytes       @24
}

struct esignTokenRef {
    Org   text @0
    Token text @8
}

struct esignTrail {
    DocumentID text        @0
    Entries    list<bytes> @8
}

struct esignUploadIn {
    SizedIn      bytes @0
    Title        text  @8
    PdfBase64    text  @16
    ExternalID   text  @24
    Subject      text  @32
    Message      text  @40
    SigningOrder text  @48
}

struct esignValueIn {
    SizedIn  bytes @0
    FieldID  text  @8
    Value    text  @16
    IsBase64 text  @24
}

interface esign {
    # Returns your org's documents, newest first.
    # Each carries its status, recipients and field layout. The listing is capped at
    # 200 and there is no paging, so treat it as the recent window rather than a
    # complete export. It reads the caller's own tenant store, so no other org's
    # documents can appear in it.
    get_esign_documents() returns (rep: esignDocuments)
    # Returns one document with its recipients and field layout.
    # It answers the document, its recipients with each one's read and signing status,
    # and every field with its type, page and position — the view a sender's UI
    # renders, and where the field ids come from. The id is resolved in the caller's
    # OWN tenant store, so another org's document id is a 404 rather than a refusal
    # that would confirm it exists.
    get_esign_documents_by_id(req: esignRef) returns (rep: esignDocument)
    # Returns the document's full audit trail, oldest first.
    # It answers every recorded event for the document in order — created, recipient
    # added, field created, sent, opened, each field inserted, each recipient
    # completed or rejected, and completion — with the actor and timestamp on each.
    # This is the evidence record behind a signature, so it is append-only and nothing
    # in the surface edits it.
    # The id is resolved in the caller's OWN tenant store, so another org's document
    # id is a 404.
    get_esign_documents_by_id_audit(req: esignRef) returns (rep: esignTrail)
    # Returns the document — the sealed PDF once it is complete.
    # It answers the document's current PDF as base64 with a sealed flag and a
    # filename. Before completion that is the original upload; once every signer has
    # finished it is the SEALED artifact, with the field values rendered onto the page
    # and a real x509 PKCS#7 digital signature applied. There is one pdfBase64 field
    # either way, so sealed is what tells you which you are holding.
    # The id is resolved in the caller's OWN tenant store, so another org's document
    # id is a 404.
    get_esign_documents_by_id_download(req: esignRef) returns (rep: esignPDF)
    # Reports whether the e-signature surface is mounted.
    # It answers ok whenever the subsystem is mounted, takes no tenant and needs no
    # principal. It is deliberately shallow: it is registered before the document host
    # is built, so it still answers on a deployment that came up WITHOUT object
    # storage and therefore serves nothing else. Read it as reachability, never as a
    # promise that documents can be stored.
    get_esign_health() returns (rep: esignHealth)
    # Opens a document you were asked to sign, using your signing link.
    # It answers the document, the recipient the link identifies, the fields THAT
    # recipient must fill, and the PDF to display. The first open also marks the
    # recipient as having opened it and records that on the audit trail, so this read
    # has a side effect by design.
    # This surface takes NO account: the signing token is the entire credential, and
    # it names the recipient, so a signer sees only their own fields and never the
    # other recipients' tokens. The token resolves to its owning tenant FIRST, before
    # any per-tenant store is opened, and the org segment is only checked against
    # that answer. An unknown or wrong-org token is one and the same 404, never a
    # hint that some other document exists.
    get_esign_o_by_org_sign_by_token(req: esignTokenRef) returns (rep: esignSession)
    # Uploads a PDF and opens a draft ready for recipients and fields.
    # It answers 201 with the document in DRAFT — the state where recipients and
    # fields may still be added, and the only state they may. The bytes go to object
    # storage rather than into the tenant database, and the original is kept under its
    # own key so it survives sealing untouched: a completed document can always be
    # compared against what was uploaded. Creation is recorded on the audit trail.
    # This is the sender's surface: a validated principal is required, and the document
    # lands in that principal's OWN org. Isolation is physical rather than a filter —
    # each tenant has its own store — so another org's document id is simply not
    # there. A body over 32 MiB is refused with 413.
    post_esign_documents(req: esignUploadIn) returns (rep: esignDocument)
    # Places a field on the page for one recipient to fill.
    # It adds a signature, date, name, email or text box at a page and position for
    # ONE named recipient, and answers 201 with its id. The recipient must belong to
    # this document; one from elsewhere is refused.
    # Fields are what make a recipient signable: a document cannot be sent while any
    # signing recipient has none. Only while DRAFT — adding a field to a sent document
    # is a 409 — and an unknown document is a 404. The addition is recorded on the
    # audit trail.
    post_esign_documents_by_id_fields(req: esignFieldIn) returns (rep: esignPlacement)
    # Adds someone to a draft and mints their signing token.
    # It answers 201 with the recipient's id and their signing TOKEN — the
    # crypto-random capability that is the only credential the signer's surface
    # accepts — so this response is where the signing link is built from. A CC
    # recipient is recorded as already complete, because they are never asked to
    # sign.
    # Only while DRAFT: adding a recipient to a document already sent is a 409,
    # because the field layout and the turn order were fixed when it went out. An
    # unknown document is a 404. The addition is recorded on the audit trail.
    post_esign_documents_by_id_recipients(req: esignRecipientIn) returns (rep: esignInvite)
    # Sends the document out and answers each signer's link.
    # It moves the document from DRAFT to PENDING and answers the signing tokens — one
    # per signing recipient, with the path to hand them — which is how the links reach
    # the people who must sign. Nothing is emailed by this call; delivering the links
    # is the caller's.
    # It refuses to send an unsignable document: no recipients at all is a 400, and so
    # is any signing recipient with no fields to fill, named in the error. Re-sending
    # an already-pending document is allowed and re-issues the same links rather than
    # restarting anything; a completed document is a 409, and an unknown one a 404.
    # The send is recorded on the audit trail.
    post_esign_documents_by_id_send(req: esignSendIn) returns (rep: esignLinks)
    # Finishes your signing — and seals the document if you were the
    # last.
    # It marks this recipient as done and answers whether the DOCUMENT sealed with it.
    # When every signing recipient has completed, sealing happens right here in the
    # same call: the collected values are rendered onto the PDF, a real x509 PKCS#7
    # signature is applied, the sealed bytes are stored beside the untouched original,
    # and the document moves to COMPLETED. Until then the answer is the recipient's
    # own completion with the document still pending.
    # It refuses to complete a half-filled signature: a recipient with any unfilled
    # field is a 400 naming how many remain. A document not out for signature is a
    # 409, as is a recipient who has already completed, and under SEQUENTIAL order a
    # signer out of turn is a 403. The token is the whole credential — no account, and
    # a token that does not resolve under the org segment is a 404. Sealing and
    # completion are one transaction, so a failure anywhere leaves the document
    # exactly as it was.
    post_esign_o_by_org_sign_by_token_complete(req: esignCompletionIn) returns (rep: esignCompletion)
    # Fills in one of your fields.
    # It records a value for one field and marks it inserted. A signature field takes
    # a value with isBase64 true for drawn image bytes, or false for a typed
    # signature; a date, name or email field falls back to today, the recipient's name
    # or their email when the value is omitted; any other type requires one.
    # Nothing is sealed here — filling every field still leaves the document pending
    # until the completion call. The token is the whole credential and it bounds what
    # can be written: a field belonging to another recipient is refused with 401 even
    # under a valid token, an unknown field is a 404, and a field already filled is a
    # 409. A document not out for signature is a 409, as is a recipient who has
    # already completed or rejected. Under SEQUENTIAL order a signer whose turn has
    # not come is refused 403 until every earlier signer has signed. Each insertion is
    # recorded on the audit trail.
    post_esign_o_by_org_sign_by_token_fields_by_fieldid(req: esignValueIn) returns (rep: esignInsertion)
    # Declines to sign, with an optional reason.
    # It records this recipient's refusal and moves the WHOLE DOCUMENT to REJECTED —
    # one declining signer ends it for everyone, and there is no route back: the
    # document cannot then be signed or completed. An optional reason is stored and
    # written onto the audit trail with the rejection, which is what the sender sees.
    # A document not out for signature is a 409, and so is a recipient who has already
    # signed or already rejected — a refusal cannot be taken back or repeated. The
    # token is the whole credential; one that does not resolve under the org segment
    # is a 404.
    post_esign_o_by_org_sign_by_token_reject(req: esignRejectIn) returns (rep: esignRejection)
}

# ---------------------------------------------------------------------
# 13 op(s) here. What follows is what this schema does not carry.
#
# dropped (10) — the value does not cross, and nothing fails:
#   esignCompletionIn.SizedIn  goja.SizedIn  (empty message)
#   esignCompletionIn.esignTokenRef  esign.esignTokenRef  (promoted, not carried)
#   esignFieldIn.SizedIn  goja.SizedIn  (empty message)
#   esignRecipientIn.SizedIn  goja.SizedIn  (empty message)
#   esignRejectIn.SizedIn  goja.SizedIn  (empty message)
#   esignRejectIn.esignTokenRef  esign.esignTokenRef  (promoted, not carried)
#   esignSendIn.SizedIn  goja.SizedIn  (empty message)
#   esignUploadIn.SizedIn  goja.SizedIn  (empty message)
#   esignValueIn.SizedIn  goja.SizedIn  (empty message)
#   esignValueIn.esignTokenRef  esign.esignTokenRef  (promoted, not carried)
#
# opaque (15) — crosses, arrives without its name:
#   esignCompletionIn.SizedIn  goja.SizedIn
#   esignDocument.Fields  esign.esignField (list element)
#   esignDocument.Recipients  esign.esignRecipient (list element)
#   esignDocuments.Documents  esign.esignDocument (list element)
#   esignFieldIn.SizedIn  goja.SizedIn
#   esignLinks.Recipients  esign.esignLink (list element)
#   esignRecipientIn.SizedIn  goja.SizedIn
#   esignRejectIn.SizedIn  goja.SizedIn
#   esignSendIn.SizedIn  goja.SizedIn
#   esignSession.Document  esign.esignState
#   esignSession.Fields  esign.esignField (list element)
#   esignSession.Recipient  esign.esignSigner
#   esignTrail.Entries  esign.esignEvent (list element)
#   esignUploadIn.SizedIn  goja.SizedIn
#   esignValueIn.SizedIn  goja.SizedIn
