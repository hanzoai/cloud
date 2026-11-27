# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package legal

struct documentFilter {
    Limit i64 @0
}

struct documentPage {
    Data       list<bytes> @0
    Disclaimer text        @8
}

struct documentRef {
    ID text @0
}

struct documentReply {
    Document   bytes @0
    Disclaimer text  @8
}

struct filingPage {
    Data       list<bytes> @0
    Disclaimer text        @8
}

struct filingReply {
    Filing     bytes @0
    Disclaimer text  @8
}

struct filingRequest {
    DocumentIDs  list<text> @0
    Jurisdiction text       @8
}

struct legalHealth {
    Status    text @0
    Templates i64  @8
}

struct signReply {
    Document bytes @0
    EsignRef text  @8
    Provider text  @16
}

struct signRequest {
    ID      text        @0
    Signers list<bytes> @8
}

struct templateCatalog {
    Data       list<bytes> @0
    Disclaimer text        @8
}

struct templateOverride {
    ID            text        @0
    Category      text        @8
    Title         text        @16
    Body          text        @24
    CounselReview bool        @32
    Fields        list<bytes> @40
}

struct templateRef {
    ID text @0
}

struct templateReply {
    Template   bytes @0
    Disclaimer text  @8
}

interface legal {
    # Returns the org's generated documents, newest first, WITHOUT
    # their rendered content — fetch one document to read its body.
    # The response is marked no-store: these records name the counterparties an org is
    # contracting with, and must not sit in a shared cache.
    get_legal_documents(req: documentFilter) returns (rep: documentPage)
    # Returns one of the org's documents WITH its rendered body. 404
    # when the org has no document with that id — a document is never readable across
    # orgs.
    # The response is marked no-store: the body is contract text, sealed at rest and
    # returned only to the owning org, and must not sit in a shared cache.
    get_legal_documents_by_id(req: documentRef) returns (rep: documentReply)
    # Returns the org's filing records, newest first — which documents
    # were filed where, through which provider, and what the filing's honest status is.
    get_legal_filings(req: documentFilter) returns (rep: filingPage)
    # Reports that the legal subsystem is serving and how many built-in
    # templates its catalog carries. It reads no tenant, so a liveness prober that
    # sends no principal is answered rather than refused.
    get_legal_health() returns (rep: legalHealth)
    # Returns the org's effective template catalog: every built-in
    # template, with any the org has overridden replaced by its own latest version.
    # The listing carries each template's metadata and its declared MERGE FIELDS — the
    # keys a document generation must supply — but never the template bodies; fetch one
    # template to get its body. Templates in the formation and equity categories are
    # marked counselReview: every document rendered from them carries a counsel notice,
    # and that posture cannot be dropped by an override.
    get_legal_templates() returns (rep: templateCatalog)
    # Returns one template resolved for the caller's org — the org's
    # own override if it has saved one, else the built-in — with its full text/template
    # body and its declared merge fields. 404 when neither exists.
    get_legal_templates_by_id(req: templateRef) returns (rep: templateReply)
    # Opens an e-signature request over one document and moves it
    # to out_for_signature, returning the provider's reference for the request.
    # The provider is whatever this deployment has wired. The honest default is
    # "manual": the request is recorded and the org fulfils it out of band — nothing
    # here fabricates a signature, and the stub never reports itself complete.
    post_legal_documents_by_id_sign(req: signRequest) returns (rep: signReply)
    # Records a filing of one or more of the org's documents with a
    # state or agency, and returns the tracking record.
    # It is a TRACKING record, not an autonomous filing. With no filing partner wired
    # the honest status is "manual" and the note says so: the documents were generated
    # for signature, and the org files them through its registered agent. Nothing here
    # invents a filing id it does not have.
    # Every document id must belong to the caller's org; one that does not is a 404
    # naming it, so a filing can never reach across tenants.
    post_legal_filings(req: filingRequest) returns (rep: filingReply)
    # Saves the org's own version of a template — a custom
    # NDA, a house MSA — and returns it with its new version number. It takes effect
    # for that org only; other orgs keep the built-in.
    # Two boundaries cannot be crossed here. Overriding a built-in INHERITS its
    # category and its counsel-review posture, which can be raised but never dropped;
    # and a formation or equity template is counsel-review whatever the caller sends,
    # so no org can generate a securities-class document without the notice.
    # The body is validated on save, not at generation: a template that references an
    # UNDECLARED merge field is refused with 400 rather than stored and rendered blank
    # into a contract months later.
    put_legal_templates_by_id(req: templateOverride) returns (rep: templateReply)
}

# ---------------------------------------------------------------------
# 9 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   post_legal_documents  generateRequest.Data  map[string]string  (map)
#
# opaque (9) — crosses, arrives without its name:
#   documentPage.Data  legal.documentSummary (list element)
#   documentReply.Document  legal.documentView
#   filingPage.Data  legal.legalFiling (list element)
#   filingReply.Filing  legal.legalFiling
#   signReply.Document  legal.documentSummary
#   signRequest.Signers  legal.legalSigner (list element)
#   templateCatalog.Data  legal.templateView (list element)
#   templateOverride.Fields  legal.Field (list element)
#   templateReply.Template  legal.legalTemplate
