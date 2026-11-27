# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package search

struct Fusion {
    Status   text        @0
    Mode     text        @8
    Hits     list<bytes> @16
    Backends list<bytes> @24
    TookMS   i64         @32
}

struct Request {
    Query    text       @0
    Mode     text       @8
    Project  text       @16
    DocTypes list<text> @24
    Index    text       @32
    Limit    i64        @40
    Offset   i64        @48
}

struct keyedIn {
    Authorization text @0
}

struct searchIndexList {
    Indexes list<bytes> @0
}

struct searchStats {
    TotalDocuments i64         @0
    TotalSearches  i64         @8
    TotalSessions  i64         @16
    SearchesPerDay list<bytes> @24
}

interface search {
    # Lists the search indexes with their document counts and timestamps.
    # It reads the in-cluster Meilisearch service and reshapes its /stats and
    # /indexes replies into the rows the console's Search panel renders. The read is
    # degrade-friendly by design: an unreachable Meilisearch answers 200 with an
    # EMPTY list, so the panel shows an honest empty state instead of an error.
    # createdAt falls back to now and lastIndexedAt to null when the index list is
    # unavailable.
    get_admin_search_indexes(req: keyedIn) returns (rep: searchIndexList)
    # Totals the documents across every search index.
    # totalDocuments is summed from Meilisearch's own per-index counts. The other
    # three fields are structurally zero rather than estimated: Meilisearch keeps no
    # query-history counters, so searches, sessions and the per-day series are not
    # derivable from the index and this surface reports the honest zero instead of a
    # fabricated number. An unreachable Meilisearch answers 200 with all zeros.
    get_admin_search_stats(req: keyedIn) returns (rep: searchStats)
    # Is the typed op behind POST /v1/search. It does exactly two things the
    # in-process entry point must not do: resolve the tenant from the validated
    # principal, and refuse when there is none. Everything else is ForOrg.
    search(req: Request) returns (rep: Fusion)
}

# ---------------------------------------------------------------------
# 3 op(s) here. What follows is what this schema does not carry.
#
# opaque (4) — crosses, arrives without its name:
#   Fusion.Backends  search.BackendStatus (list element)
#   Fusion.Hits  search.Hit (list element)
#   searchIndexList.Indexes  search.searchIndex (list element)
#   searchStats.SearchesPerDay  search.dayCount (list element)
