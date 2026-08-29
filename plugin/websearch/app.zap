# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package websearch

struct webSearchQuery {
    Q        text @0
    Language text @8
}

struct webSearchResults {
    Query           text        @0
    NumberOfResults i64         @8
    Results         list<bytes> @16
    Engines         list<bytes> @24
}

interface websearch {
    # Searches the live web and answers with ranked results.
    # This is the fleet's path to what is happening RIGHT NOW — today's weather, an
    # outage, a release that postdates any model's training. `q` is the query and
    # `language` narrows it to a locale. The answer is `{query, number_of_results,
    # results:[{url, title, content, engine}]}`, where `content` is the ENGINE's
    # snippet and not the page: read a page with POST /v1/crawl.
    # It is served in-process by a Go meta-search over keyless public engines — never
    # a third-party search API and never a search key. The enabled engines run
    # concurrently and their hits are merged, deduplicated by normalised URL (host
    # and path, trailing slash and fragment dropped, query kept, so distinct queries
    # stay distinct results) and capped at 30. Ranking is deterministic rather than
    # scored: the first configured engine's hits lead.
    # It fails SOFT on the engines. One that errors, times out or is served a
    # bot-challenge page contributes zero results and never fails the call, so an
    # empty `results` is a real answer — nothing was found — and not an outage. The
    # array is always present, never null.
    # Two refusals in the order they have to be asked, both in the PREAMBLE. A typed
    # op is also an MCP tool, a call-plane operation, a graph field and a CLI
    # command, and every one of those invokes it with no route and therefore no
    # middleware — so what admits a caller here is asked where every caller reaches
    # it rather than in a middleware only one of them passes through.
    # A VALIDATED PRINCIPAL IS REQUIRED, and there is no tenant beyond that: the
    # results are public web pages, identical for every caller, so nothing here is
    # scoped and nothing here can leak across orgs.
    # THEN THE ANTI-FORGERY TOKEN, immediately before the money, because that is
    # what it is about. This search is the SAME bought meta-search the compat
    # endpoint runs — the engines cost, and account.Shared/meter.go bills the
    # caller's ledger for the answer — so a page the caller never visited must not
    # be able to spend for them by sending their browser here with a cookie they
    # already hold. Nothing leaks; the answer is unreadable cross-origin. What
    # moves is money.
    # It is account's control, the one every operation in this estate asks, and it
    # is a no-op the moment a caller PRESENTS a credential (Bearer, gateway, API
    # key) — which is every service and console caller here — so it costs a CLI, an
    # agent and an API client nothing. Only the ambient-cookie path is asked for the
    # echoed token. The raw /v1/websearch/search route asks the same control on its
    # group (see Mount), so the two addresses of one search are admitted alike.
    search_web(req: webSearchQuery) returns (rep: webSearchResults)
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   webSearchResults.Engines  websearch.webEngine (list element)
#   webSearchResults.Results  websearch.webResult (list element)
