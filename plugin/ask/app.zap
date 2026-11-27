# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package ask

struct Report {
    Answer    text        @0
    Sources   list<bytes> @8
    FollowUps list<text>  @16
    Mode      text        @24
    Model     text        @32
}

struct webQuestion {
    Q          text       @0
    Mode       text       @8
    Sources    list<text> @16
    Language   text       @24
    MaxSources i64        @32
}

interface ask {
    # Researches a question on the live web and answers it with its
    # sources cited.
    # This is the DEEP one. It plans the question into topics, runs several web
    # searches, FETCHES AND READS the pages it finds, ranks them, and writes a
    # grounded answer with inline markdown citations. Use it for anything that needs
    # evidence, comparison or current fact — "what changed in X", "compare A and B",
    # "is this claim true". For a plain list of links, use search_web instead; for
    # one page you already have the URL of, use read_page.
    # `mode` buys depth: `search` is a single fast pass, `news` biases to recency,
    # `research` plans and iterates, `deep` surveys widest. `sources` narrows the
    # evidence to `web`, `news`, `academic`, `github`, `reddit` or `x` — each becomes
    # a site-scoped search, which is how this reaches X/Twitter posts.
    # EVERY CITATION IS A PAGE THIS CALL FETCHED. That is a property of the text and
    # not an instruction to the model: each source is fenced with a per-request nonce
    # so a crawled page cannot print itself a source number, and every markdown link
    # in the answer is checked against the gathered set before it is returned. So a
    # link in `answer` always appears in `sources`, and a page that was not read
    # cannot be cited.
    # It is BOUNDED and it degrades rather than failing: a mode's rounds, wall clock
    # and token ceiling all cap it, and a search that finds little or a page that
    # will not load yields a thinner answer, never an error. A validated principal is
    # required, and the answer is billed once to that principal's org.
    research_web(req: webQuestion) returns (rep: Report)
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   Report.Sources  answer.Source (list element)
