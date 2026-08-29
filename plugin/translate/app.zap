# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package translate

struct MemoryEntry {
    Source    text @0
    Target    text @8
    Tier      text @16
    Glossary  text @24
    Text      text @32
    State     text @40
    Actor     text @48
    UpdatedAt i64  @56
}

struct MemoryPage {
    Data list<bytes> @0
}

struct MemoryQuery {
    Target text @0
    State  text @8
    Limit  i64  @16
}

interface translate {
    # List returns the org's own translation-memory entries, newest first, optionally
    # narrowed to one target language and/or one position on the review ladder. It is
    # the review lane's read: what a human reviewer works through.
    # The org is ALWAYS the validated principal's org, never a request field, so one
    # tenant can never read another's memory — the entries hold customer source text.
    get_translate_memory(req: MemoryQuery) returns (rep: MemoryPage)
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   put_translate_memory  ReviewRequest.Glossary  map[string]string  (map)
#
# opaque (1) — crosses, arrives without its name:
#   MemoryPage.Data  translate.MemoryEntry (list element)
