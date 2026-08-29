# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package audit

struct trailPage {
    Data   list<bytes> @0
    Msg    text        @8
    Status text        @16
    Total  i64         @24
}

struct trailQuery {
    Sub        text @0
    Action     text @8
    Resource   text @16
    ResourceID text @24
    Result     text @32
    Since      text @40
    Until      text @48
    PageSize   text @56
    Page       text @64
}

interface audit {
    # List reads the caller's OWN org audit trail, newest first, with the total the
    # filter matched so a console can page it.
    # Every filter is optional and applies WITHIN the caller's org — the org itself is
    # the validated principal's and can never be widened by a request. Fails closed:
    # an absent principal is a true "not signed in" (401), and a deployment with no
    # local tamper-evident store answers an honest 501 rather than silently serving
    # somebody else's trail.
    get_audit(req: trailQuery) returns (rep: trailPage)
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   trailPage.Data  audit.Wire (list element)
