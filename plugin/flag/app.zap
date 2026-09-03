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

struct putDefIn {
    Key        text  @0
    Definition bytes @8
}

interface flag {
    # Removes one flag definition by key and records the
    # deletion in the change log. A key the caller's store does not hold is a 404.
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
    # Health reports that the flag engine is serving. It is not gated: liveness must
    # be probe-able without a token.
    get_flag_health() returns (rep: healthOut)
    # Evaluate runs the caller's flag definitions for one identity and returns the
    # flag verdict: which flags are on (or which variant), their payloads,
    # and whether any definition failed to compute. Evaluation is in-process over the
    # caller's own (org, project) definitions — no network hop, no shared KV — so a
    # tenant can only ever evaluate its own flags.
    post_flag(req: evaluateIn)
    # Evaluate runs the caller's flag definitions for one identity and returns the
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
    # appends to the change log under the caller's identity.
    put_flag_defs_by_key(req: putDefIn) returns (rep: DefRow)
}

# ---------------------------------------------------------------------
# 8 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   activityOut.Data  flags.ActivityRow (list element)
#   defsOut.Data  flags.DefRow (list element)
