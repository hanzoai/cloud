# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package allowance

struct Allowance {
    Plan   text @0
    Limit  i64  @8
    Used   i64  @16
    Spent  bool @24
    Window text @32
    Resets i64  @40
}

interface allowance {
    # Answers what the CALLER has left of their plan's free-call allowance this period,
    # and the instant the count starts again.
    # This is the number a product shows beside the composer — "17 of 20 left today" —
    # and the moment to offer a plan is when it reaches zero. It READS: asking does not
    # spend, so a page that polls it costs the caller nothing.
    # The subject is the caller's own, resolved from the verified credential, and can
    # never be named in the request — so this is a mirror, not a lookup of someone else.
    # An unauthenticated caller is refused: there is no allowance without someone to
    # hold it.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_allowance() returns (rep: Allowance)
}
