# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package websearch

struct searchEngineKey {
    Name text @0
    Key  text @8
}

struct searchEngineKeyed {
    Name  text @0
    Keyed bool @8
}

struct searchEngineRef {
    Name text @0
}

struct searchEngines {
    Saved   bool        @0
    Engines list<bytes> @8
}

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
    # Removes an engine's API key from KMS.
    # Searches stop using the key at once. Removing a key that is not held answers
    # the same as removing one that is. 409 when the saved setting would be left with
    # no engine that can answer — enable bing or ddg first.
    # SuperAdmin only. 404 for an engine that does not exist, 400 for one that takes
    # no key, 503 when the settings store or KMS cannot answer.
    delete_search_engine_key(req: searchEngineRef) returns (rep: searchEngineKeyed)
    # Lists every engine web search can run and how the platform runs it.
    # For each engine: whether it is enabled and its place in the run order, whether
    # it takes an API key and whether it needs one, whether the platform holds that
    # key (`keyed` — the key itself is never returned), whether it answers from this
    # cluster as things stand (`available`), the option values it is asked with and
    # the options it supports with their bounds. Enabled engines come first, in the
    # order they run.
    # `saved` is false when no setting is stored, and the engines shown enabled are
    # then the default: Mojeek alone when its key is held, otherwise bing, ddg and
    # Mojeek — Mojeek's keyless page does not answer from this cluster on its own.
    # SuperAdmin only. 503 when the settings store or KMS cannot answer: a board
    # that guessed would show an operator a configuration nobody saved.
    list_search_engines() returns (rep: searchEngines)
    # Searches the live web and answers with ranked results.
    # This is the fleet's path to what is happening RIGHT NOW — today's weather, an
    # outage, a release that postdates any model's training. `q` is the query and
    # `language` narrows it to a locale. The answer is `{query, number_of_results,
    # results:[{url, title, content, engine}]}`, where `content` is the ENGINE's
    # snippet and not the page: read a page with POST /v1/crawl.
    # It is served in-process by a Go meta-search over the engines the platform
    # enables (GET /v1/websearch/engines): Mojeek's API by default when the platform
    # holds its key, otherwise the keyless engines that answer from this cluster. The
    # enabled engines run concurrently and their hits are merged, deduplicated by
    # normalised URL (host and path, trailing slash and fragment dropped, query kept,
    # so distinct queries stay distinct results) and capped at 30. Hits that several
    # engines agree on rank first; ties keep a stable order.
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
    # Seals an engine's API key in KMS, replacing any key held.
    # The key is the platform's: it lives in KMS under the platform org admin at
    # /websearch/<ENGINE>_KEY (env prod), every search reads it from there at the
    # moment it runs, and no
    # operation returns, logs or echoes it — the answer says only that a key is now
    # held. Only an engine that takes a key accepts one (mojeek, brave).
    # SuperAdmin only. 404 for an engine that does not exist, 400 for one that takes
    # no key or a key that is empty, longer than 4096 bytes or holds whitespace or
    # control characters, 503 when KMS cannot take it.
    set_search_engine_key(req: searchEngineKey) returns (rep: searchEngineKeyed)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   set_search_engines  searchEnginesSet.Options  map[string]websearch.searchEngineOptions  (map)
#
# opaque (3) — crosses, arrives without its name:
#   searchEngines.Engines  websearch.searchEngine (list element)
#   webSearchResults.Engines  websearch.webEngine (list element)
#   webSearchResults.Results  websearch.webResult (list element)
