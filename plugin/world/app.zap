# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package world

struct limitsQuery {
    Plan text @0
}

struct limitsView {
    Plan   text  @0
    Unit   text  @8
    Limits bytes @16
}

struct newsResponse {
    Items list<bytes> @0
}

struct pipelineReq {
    Feeds   list<text> @0
    Filters bytes      @8
}

struct pipelineView {
    Org       text       @0
    Project   text       @8
    Feeds     list<text> @16
    Filters   bytes      @24
    Default   bool       @32
    CreatedAt text       @40
    UpdatedAt text       @48
}

struct worldIndex {
    Product text        @0
    Summary text        @8
    Wires   list<bytes> @16
}

interface world {
    # Answers GET /v1/world — the product's public endpoint, naming every wire
    # this surface answers on.
    # It exists because two of those wires are INVISIBLE to the generated document.
    # /v1/world/mcp and /v1/world/zap are carved off the cloud catch-all by the
    # ingress and answered by world-gw, so the cloud router never serves them — and
    # openapi.Describe renders prose only for a route the router actually serves,
    # which is the very property that keeps the document from being able to claim an
    # operation nothing answers. Both addresses are real and public, so without this
    # op the only way to learn they exist is to read the ingress config. This is
    # where that fact lives, in the product's own surface.
    # Public on purpose: discovery precedes credentials. It reports addresses and
    # protocols only — never feed data, and never the caller's plan, which
    # GET /v1/world/limits owns — so there is nothing here to leak.
    get_world() returns (rep: worldIndex)
    # Echoes a World plan's rate limits, alert quota and model-API grant, read
    # straight from the live @hanzo/plans catalog, so agents and dashboards configure
    # themselves against the catalog instead of hardcoding tier numbers.
    # An empty or unknown plan resolves world-free, and a catalog failure serves that
    # same free floor rather than erroring — so this always answers 200, and it can only
    # ever under-grant. It reports the contract; it does not enforce it.
    get_world_limits(req: limitsQuery) returns (rep: limitsView)
    # Returns the caller's merged world-news feed: every source their project's
    # pipeline names — GDELT once per keyword, plus each allowlisted RSS or Atom feed —
    # fetched concurrently, narrowed by the pipeline's keyword/region/source filters,
    # deduplicated by link and sorted freshest first, capped at 50 items.
    # A project with no stored pipeline gets a sensible default set of world feeds
    # rather than an empty answer. A source that fails or times out is SKIPPED: the feed
    # degrades to honest partial results and never 5xxs because one outlet was down.
    # Reading also publishes the result to the /v1/world/stream subscribers of the same
    # (org, project), so a dashboard's own refresh updates every open tab.
    get_world_news() returns (rep: newsResponse)
    # Returns the caller project's news pipeline: which feeds it reads and how
    # the merged result is filtered. A project that has never written one is answered
    # with the built-in world feeds and `default: true`, so a fresh project sees the
    # same feed /v1/world/news would actually serve rather than an empty configuration.
    get_world_pipeline() returns (rep: pipelineView)
    # Replaces the caller project's news pipeline and returns what was
    # stored. It is a WHOLE replacement, not a patch: a field the request leaves out is
    # stored empty, so sending only feeds clears the filters.
    # Every feed URL is validated HERE, at the write boundary — http(s) only, and the
    # host must be on the server's allowlist — so a stored pipeline can never name a
    # host the fetcher would later refuse, and the allowlist is one decision in one
    # place rather than a check at each fetch.
    put_world_pipeline(req: pipelineReq) returns (rep: pipelineView)
}

# ---------------------------------------------------------------------
# 5 op(s) here. What follows is what this schema does not carry.
#
# opaque (5) — crosses, arrives without its name:
#   limitsView.Limits  world.limitsBlock
#   newsResponse.Items  world.NewsItem (list element)
#   pipelineReq.Filters  world.Filters
#   pipelineView.Filters  world.Filters
#   worldIndex.Wires  world.worldWire (list element)
