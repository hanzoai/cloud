# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package code

struct AskAnswer {
    Question  text        @0
    Answer    text        @8
    Citations list<bytes> @16
    Degraded  bool        @24
}

struct ContextBundle {
    Query        text        @0
    Repo         text        @8
    BudgetTokens i64         @16
    UsedTokens   i64         @24
    Spans        list<bytes> @32
}

struct askIn {
    Q    text @0
    Repo text @8
}

struct askPostIn {
    Q         text @0
    RepoQuery text @8
    Query     text @16
    Repo      text @24
}

struct contextIn {
    Query        text @0
    BudgetTokens i64  @8
    Repo         text @16
}

struct fileContent {
    Content text @0
    Lang    text @8
    Path    text @16
    Repo    text @24
}

struct fileIn {
    Path text @0
    Repo text @8
}

struct indexIn {
    Repo  text        @0
    Files list<bytes> @8
    Prune bool        @16
}

struct indexResult {
    Repo     text @0
    Indexed  i64  @8
    Skipped  i64  @16
    Pruned   i64  @24
    Files    i64  @32
    Symbols  i64  @40
    Chunks   i64  @48
    Vectors  i64  @56
    Semantic bool @64
}

struct repoTree {
    Files list<bytes> @0
    Repo  text        @8
}

struct searchIn {
    Q     text @0
    Type  text @8
    Repo  text @16
    Limit i64  @24
}

struct searchResults {
    Degraded bool        @0
    Query    text        @8
    Results  list<bytes> @16
    Type     text        @24
}

struct treeIn {
    Repo text @0
}

interface code {
    # Answers a question about the caller org's code with a CITED answer:
    # retrieval packs grounding context, then the synthesizer writes the answer over
    # exactly those spans, which come back alongside it. It never answers without
    # grounding — with no matched code the answer is empty and says so, and with no
    # synthesizer available the citations still come back with "degraded": true so
    # the caller can reason over the spans itself.
    get_code_ask(req: askIn) returns (rep: AskAnswer)
    # Returns the INDEXED content of one file — read_file over the chunks the
    # search tiers hold, for pulling up code an agent just found. It is NOT
    # byte-verbatim: the git object plane is the source of record for exact bytes,
    # history and blame. A file absent from the index is a 404, so an agent can tell
    # "not indexed" from "empty file".
    get_code_file(req: fileIn) returns (rep: fileContent)
    # Finds code in the caller org's index across three orthogonal retrieval
    # tiers fused by reciprocal-rank fusion: lexical (FTS5 trigram over
    # code-tokenized text), symbolic (real definition and reference edges), and
    # semantic (embedding cosine over AST-boundary chunks). Pick one tier with
    # `type`, or leave it to run all three as hybrid, which is what a coding agent
    # usually wants. It is FAIL-HONEST: a retrieval outage answers 200 with an empty
    # result set and "degraded": true rather than a 5xx, so an agent degrades instead
    # of stalling. A malformed regex is a 400.
    get_code_search(req: searchIn) returns (rep: searchResults)
    # Returns one repository's file structure with a per-file symbol count —
    # get_repo_structure over the org's own index, with no git checkout involved. A
    # repository that has not been indexed answers an empty tree rather than an
    # error, so an agent can tell "nothing here" without handling a failure.
    get_code_tree(req: treeIn) returns (rep: repoTree)
    # Is askGet with the question in the request BODY, for a question too
    # long or too awkward to put in a URL. `query` and `repo` in the body take
    # precedence over `?q=` and `?repo=`; either source works alone.
    post_code_ask(req: askPostIn) returns (rep: AskAnswer)
    # Packs the most relevant code for a query into a token budget — THE
    # primitive for a coding agent that has to decide what to put in a prompt. It
    # retrieves seed spans, expands each with the definitions it calls and its key
    # callers, then greedily fills the budget, so the answer is a coherent slice of
    # the codebase rather than a list of disconnected matches. The top match is
    # always included, truncated if it alone overflows, so a matched query never
    # comes back empty. A retrieval outage answers 200 with an empty bundle rather
    # than a 5xx.
    post_code_context(req: contextIn) returns (rep: ContextBundle)
    # (re)indexes a repository for the caller's org, incrementally: files whose
    # content hash is unchanged are skipped, so re-sending a whole tree is cheap.
    # Each file is parsed for symbols, split at AST boundaries and — when the
    # semantic tier is available — embedded, which is what makes it searchable across
    # all three retrieval tiers. Pass `prune` to also DELETE indexed files absent
    # from the request, which turns the call into a full sync; without it the call is
    # an upsert. The index is written to the caller org's own physically separate
    # database.
    post_code_index(req: indexIn) returns (rep: indexResult)
}

# ---------------------------------------------------------------------
# 7 op(s) here. What follows is what this schema does not carry.
#
# opaque (5) — crosses, arrives without its name:
#   AskAnswer.Citations  code.Citation (list element)
#   ContextBundle.Spans  code.Span (list element)
#   indexIn.Files  code.fileInput (list element)
#   repoTree.Files  code.TreeEntry (list element)
#   searchResults.Results  code.Span (list element)
