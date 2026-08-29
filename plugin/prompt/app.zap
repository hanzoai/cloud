# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package prompt

struct catalogList {
    Data list<bytes> @0
}

struct metricList {
    Data list<bytes> @0
}

struct promptDetail {
    Name      text        @0
    Type      text        @8
    Prompt    text        @16
    Version   i64         @24
    Labels    list<text>  @32
    Tags      list<text>  @40
    Versions  list<bytes> @48
    CreatedAt text        @56
    UpdatedAt text        @64
}

struct promptList {
    Data list<bytes> @0
}

struct promptRef {
    Name text @0
}

struct promptReq {
    Name   text       @0
    Type   text       @8
    Prompt text       @16
    Labels list<text> @24
    Tags   list<text> @32
}

interface prompt {
    # Delete removes one of the caller org's prompts and every version of it, answering
    # 204. It is scoped to the caller's org, so a name another tenant owns is the same
    # 404 an unknown name gives. There is no undo: the version history goes with it.
    delete_prompt_by_name(req: promptRef)
    # List returns the caller org's prompt library as one row per prompt: its name,
    # type, every version number it has, its taxonomy and when it last changed. The
    # template bodies are deliberately absent — fetch one prompt to read its text.
    get_prompt() returns (rep: promptList)
    # Get returns one of the caller org's prompts: its CURRENT template text plus the
    # metadata of every version it has had. The history carries version numbers, types
    # and timestamps only — not each version's body — so a long history cannot inflate
    # this response. A name the caller's org does not own is 404, whoever owns it.
    get_prompt_by_name(req: promptRef) returns (rep: promptDetail)
    # Catalog returns the read-only starter prompt library shipped with the binary —
    # reference content every tenant sees the same, NOT the caller's own prompts and
    # never mixed into them. An org's library stays honestly empty until someone
    # explicitly imports a starter, which is an ordinary POST /v1/prompt. Entries that
    # would fail the create guards are dropped, so everything offered here can actually
    # be imported.
    get_prompt_catalog() returns (rep: catalogList)
    # Metrics returns real per-prompt statistics for the caller's org: how many versions
    # each prompt has, which one is current, and when it was created and last changed.
    # Every number is counted from the store — nothing here is estimated or fabricated.
    get_prompt_metrics() returns (rep: metricList)
    # Create records a prompt for the caller's org and answers 201 with it. A name the
    # org already uses is NOT an error and NOT an overwrite: it appends a new version,
    # so the library keeps real, inspectable history and the response carries the whole
    # version list. The name is also the URL segment the prompt is fetched by, which is
    # why its shape is constrained and a handful of names are reserved.
    post_prompt(req: promptReq) returns (rep: promptDetail)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# opaque (4) — crosses, arrives without its name:
#   catalogList.Data  prompt.CatalogEntry (list element)
#   metricList.Data  prompt.metricRow (list element)
#   promptDetail.Versions  prompt.versionView (list element)
#   promptList.Data  prompt.promptMeta (list element)
