# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package standing

struct Upkeep {
    Structure    text        @0
    Jurisdiction text        @8
    Obligations  list<bytes> @16
    YearlyCents  i64         @24
    AtLeast      bool        @32
    Currency     text        @40
}

struct upkeepIn {
    Structure     text @0
    Jurisdiction  text @8
    AgentOfRecord bool @16
}

interface standing {
    # Reports what keeping this entity costs every year, itemised.
    # This is the figure that decides where to incorporate, and the one a formation
    # price cannot show: Delaware is cheaper to form than Wyoming for a corporation
    # and dearer to keep, so a founder shown only the formation fee is shown the half
    # that reverses. Each state line carries the authority that publishes it and the
    # date it was checked, and a franchise tax that scales is marked a minimum rather
    # than quoted as final.
    post_standing_upkeep(req: upkeepIn) returns (rep: Upkeep)
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   Upkeep.Obligations  standing.Obligation (list element)
