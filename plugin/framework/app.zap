# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package framework

struct DocType {
    Name          text        @0
    Module        text        @8
    IsSingle      bool        @16
    IsSubmittable bool        @17
    Autoname      text        @24
    TitleField    text        @32
    Fields        list<bytes> @40
    Perms         list<bytes> @48
    CreatedAt     i64         @56
    UpdatedAt     i64         @64
}

struct Install {
    Module   text       @0
    Created  list<text> @8
    Existing list<text> @16
}

struct ModuleState {
    Module    text       @0
    DocTypes  list<text> @8
    Installed list<text> @16
}

struct docRef {
    DocType text @0
    Name    text @8
}

struct docTypeList {
    Data list<bytes> @0
}

struct docTypeRef {
    Name text @0
}

struct listDocumentsIn {
    DocType text @0
    Filters text @8
    Fields  text @16
    OrderBy text @24
    Limit   text @32
}

struct moduleList {
    Data list<bytes> @0
}

struct moduleRef {
    Module text @0
}

struct summaryView {
    DocTypes  i64 @0
    Documents i64 @8
}

interface framework {
    # Removes one document, after its on_trash hooks agree. A
    # SUBMITTED document cannot be deleted — cancel it first. Answers 204.
    delete_framework_by_doctype_by_name(req: docRef)
    # Removes a DocType and every document stored under it. The
    # definition and its data go together — a document with no schema can be neither
    # validated nor read back — so there is no undo. Manager-only. Answers 204.
    delete_framework_doctypes_by_name(req: docTypeRef)
    # Returns one document by name, with Password fields redacted.
    get_framework_by_doctype_by_name(req: docRef)
    # Returns every DocType defined in the caller's org. Another
    # tenant's definitions are never included: the org is part of the store key.
    get_framework_doctypes() returns (rep: docTypeList)
    # Returns one DocType definition — its fields, naming rule,
    # permissions and lifecycle flags. Scoped to the caller's org, so another
    # tenant's DocType of the same name is simply not found.
    get_framework_doctypes_by_name(req: docTypeRef) returns (rep: DocType)
    # Returns every app lane compiled into this deployment and the
    # DocTypes each one installs. It describes the BINARY, not the org: what a given
    # org has actually installed is the per-module state below.
    get_framework_modules() returns (rep: moduleList)
    # Returns one app lane's install state for the caller's org: the
    # DocTypes the lane declares, and which of them already exist in the org. That
    # is the honest "set up" versus "installed" answer a console renders.
    get_framework_modules_by_module(req: moduleRef) returns (rep: ModuleState)
    # Reports how much of the DocType surface the caller's org uses: how
    # many DocTypes it has defined, and how many documents exist across them.
    get_framework_summary() returns (rep: summaryView)
    # Moves a submitted document to cancelled (docstatus 1 → 2) after
    # its on_cancel hooks agree. Cancelling is terminal — a cancelled document
    # cannot be re-submitted — but it CAN then be deleted.
    post_framework_by_doctype_by_name_cancel(req: docRef)
    # Moves a draft to submitted (docstatus 0 → 1) after its
    # on_submit hooks agree. A submitted document is IMMUTABLE: further writes and
    # deletes are refused until it is cancelled. Only a submittable DocType has this
    # lifecycle; any other docstatus is an illegal transition.
    post_framework_by_doctype_by_name_submit(req: docRef)
    # Defines a DocType in the caller's org: the metadata that gives a
    # document surface its fields, its naming rule, whether it has a submit/cancel
    # lifecycle, and which role may do what to it. Manager-only — on a fresh org the
    # first caller to administer it is seeded as its System Manager, after which
    # only a System Manager (or a platform admin) may define. Answers 201.
    post_framework_doctypes(req: DocType) returns (rep: DocType)
    # Creates an app lane's DocTypes in the caller's org. Idempotent
    # and create-if-absent: a DocType the org already has is reported as existing
    # and never replaced, so re-installing cannot clobber a definition the org has
    # since edited. Manager-only.
    post_framework_modules_by_module_install(req: moduleRef) returns (rep: Install)
    # Replaces a DocType definition wholesale (PUT semantics): the
    # stored definition becomes the body. The name in the URL is authoritative over
    # the body's, and documents already stored under the DocType are left intact.
    # Manager-only.
    put_framework_doctypes_by_name(req: DocType) returns (rep: DocType)
}

# ---------------------------------------------------------------------
# 13 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   get_framework_by_doctype  documentList.Data  []framework.docView  (no wire form)
#
# opaque (4) — crosses, arrives without its name:
#   DocType.Fields  doctype.DocField (list element)
#   DocType.Perms  doctype.DocPerm (list element)
#   docTypeList.Data  doctype.DocType (list element)
#   moduleList.Data  framework.module (list element)
