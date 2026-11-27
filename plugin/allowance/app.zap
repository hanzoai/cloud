# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package allowance

struct Allowance {
    Plan   text  @0
    Limit  i64   @8
    Used   i64   @16
    Spent  bool  @24
    Window text  @32
    Resets i64   @40
    Start  i64   @48
    Pooled bool  @56
    Pool   bytes @64
}

interface allowance {
    # Answers what the CALLER has left of their plan's free-call allowance in the window
    # that holds them, and the instant that window reopens.
    # This is the number a product shows beside the composer — "2 of 3 left" —
    # and the moment to offer a plan is when it reaches zero. It READS: asking does not
    # spend, so a page that polls it costs the caller nothing. A Free person's window is
    # a session that opens at their first call after the last one ended and runs five
    # hours; while none runs, resets is 0 and the next call opens one.
    # A bounded caller is POOLED: the Free plan is limited usage from one pool every
    # free user shares — the platform's vendor accounts for free models — so the answer
    # says so (pooled) and carries that pool's standing: available, busy or exhausted,
    # and when it refills. The pool is read from the process that spends it and is
    # absent when that process does not answer, never guessed. A paid plan is not
    # pooled and carries no pool: its usage is metered in money.
    # The subject is the caller's own, resolved from the verified credential, and can
    # never be named in the request — so this is a mirror, not a lookup of someone else.
    # An unauthenticated caller is refused: there is no allowance without someone to
    # hold it.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_allowance() returns (rep: Allowance)
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   Allowance.Pool  client.Pool
