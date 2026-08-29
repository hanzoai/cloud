# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package catalog

struct browseQuery {
    Q         text @0
    Org       text @8
    Kind      text @16
    Origin    text @24
    Archetype text @32
    Language  text @40
    Template  text @48
    Forkable  text @56
    Limit     text @64
    Offset    text @72
}

# ---------------------------------------------------------------------
# 0 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   get_catalog  catalogPage.Facets  map[string]catalog.counts  (map)
