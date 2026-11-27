# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package ci

struct Executions {
    Executions list<bytes> @0
    Repos      i64         @8
    Orgs       list<text>  @16
    FetchedAt  bytes       @24
    Stale      bool        @32
    SourceErr  text        @40
}

interface ci {
    # Lists recent builds: the repo, the branch, the commit and how each run
    # ended, newest first. A run names a repo, a branch and an actor, so the list is
    # never wider than the caller — a SuperAdmin sees the fleet, an org member sees
    # only its own org.
    get_ci_runs() returns (rep: Executions)
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# dropped (1) — the value does not cross, and nothing fails:
#   Executions.FetchedAt  time.Time  (empty message)
#
# opaque (2) — crosses, arrives without its name:
#   Executions.Executions  ci.Execution (list element)
#   Executions.FetchedAt  time.Time
