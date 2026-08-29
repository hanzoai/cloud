# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package lsp

struct Query {
    Repo      text @0
    Rev       text @8
    Path      text @16
    Line      i64  @24
    Character i64  @32
    Relation  text @40
}

# ---------------------------------------------------------------------
# 0 op(s) here. What follows is what this schema does not carry.
#
# blocked (5) — the op is absent; the field has no wire form:
#   post_lsp_complete  Answer.Diagnostics  []lsp.Diagnostic  (no wire form)
#   post_lsp_diagnostics  Answer  lsp.Answer  (reaches one)
#   post_lsp_hover  Answer  lsp.Answer  (reaches one)
#   post_lsp_locate  Answer  lsp.Answer  (reaches one)
#   post_lsp_symbols  Answer  lsp.Answer  (reaches one)
