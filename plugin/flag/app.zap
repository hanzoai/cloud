# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package flag

struct DefRow {
    Key        text  @0
    Definition bytes @8
    Version    i64   @16
    UpdatedAt  text  @24
    UpdatedBy  text  @32
}

struct activityIn {
    Limit i64 @0
}

struct activityOut {
    Data list<bytes> @0
}

struct defsOut {
    Data list<bytes> @0
}

struct deletedOut {
    Deleted text @0
}

struct evaluateIn {
    DistinctID       text  @0
    PersonProperties bytes @8
    Groups           bytes @16
}

struct healthOut {
    OK     bool @0
    Engine text @8
}

struct keyIn {
    Key text @0
}

struct optinIn {
    On bool @0
}

struct optinOut {
    On      bool       @0
    Blocked bool       @1
    Public  list<text> @8
}

struct orgOptinIn {
    Blocked bool @0
}

struct orgOptinOut {
    Blocked bool @0
}

struct putDefIn {
    Key        text  @0
    Definition bytes @8
}

interface flag {
    # Removes one flag definition by key and records the
    # deletion in the change log. A key the caller's store does not hold is a 404.
    # A staged capability's key is a SuperAdmin's to remove, as it is to set, and
    # every other key an owner's or admin's of the org.
    delete_flag_defs_by_key(req: keyIn) returns (rep: deletedOut)
    # Returns the caller's flag change log newest-first: every
    # create, update and delete, with the actor and the time.
    get_flag_activity(req: activityIn) returns (rep: activityOut)
    # Returns every flag definition in the caller's (org,
    # project) store, by key, with its version and who last changed it.
    get_flag_defs() returns (rep: defsOut)
    # Returns one flag definition by key, or 404 when the caller's
    # store has none under that key.
    get_flag_defs_by_key(req: keyIn) returns (rep: DefRow)
    # Reports that the flag engine is serving. It is not gated: liveness must
    # be probe-able without a token.
    get_flag_health() returns (rep: healthOut)
    # Answers the signed-in caller's opt-in to public betas: whether they
    # opted in, whether the org they act in blocked it, and which betas are public. A
    # beta not listed is private, granted person by person.
    get_flag_optin() returns (rep: optinOut)
    # Runs the caller's flag definitions for one identity and returns the
    # flag verdict: which flags are on (or which variant), their payloads,
    # and whether any definition failed to compute. Evaluation is in-process over the
    # caller's own (org, project) definitions — no network hop, no shared KV — so a
    # tenant can only ever evaluate its own flags.
    post_flag(req: evaluateIn)
    # Runs the caller's flag definitions for one identity and returns the
    # flag verdict: which flags are on (or which variant), their payloads,
    # and whether any definition failed to compute. Evaluation is in-process over the
    # caller's own (org, project) definitions — no network hop, no shared KV — so a
    # tenant can only ever evaluate its own flags.
    post_flag_decide(req: evaluateIn)
    # Creates or replaces the flag definition at the path's key and
    # returns the stored row. The BODY IS THE DEFINITION DOCUMENT — the flag-definition
    # JSON object the evaluator consumes — and it is stored verbatim except that its
    # "key" is forced to the key in the URL, so a document can never be filed under a
    # name other than the one it was addressed by. Every write bumps the version and
    # appends to the change log under the caller's identity. A key that lets an org
    # into a capability that is not ga — research, machines — is a SuperAdmin's to
    # set, never an org admin's, and its write is on the audit trail before it lands;
    # every other key is an owner's or admin's of the org, never a member's.
    put_flag_defs_by_key(req: putDefIn) returns (rep: DefRow)
    # Sets the signed-in caller's OWN opt-in to public betas: on, every
    # public beta and each one promoted later is on for them; off, none is. Self only:
    # the row written is the caller's validated subject, whatever the request says.
    # Opting in while the org they act in blocked it is refused; opting out never is.
    put_flag_optin(req: optinIn) returns (rep: optinOut)
    # Blocks or unblocks the opt-in to public betas for every member of
    # the org the caller acts in. An owner or admin of that org, or a SuperAdmin, may;
    # a member may not. It only withholds: blocked, no member gets a public beta by
    # opting in, and their own choices are kept for when it is lifted; it never turns
    # a beta on. A shared sign-up org has no admin to speak for its members and is
    # refused. The change is on the audit trail before it lands.
    put_flag_optin_org(req: orgOptinIn) returns (rep: orgOptinOut)
}

# ---------------------------------------------------------------------
# 11 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   get_flag_platform  platformOut.Flags  map[string]bool  (map)
#
# opaque (2) — crosses, arrives without its name:
#   activityOut.Data  flags.ActivityRow (list element)
#   defsOut.Data  flags.DefRow (list element)
