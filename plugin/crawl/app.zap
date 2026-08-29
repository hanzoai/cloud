# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package crawl

struct crawlRequest {
    URL text @0
}

struct crawlResult {
    Success bool  @0
    Data    bytes @8
    Error   text  @16
}

interface crawl {
    # Reads one URL and answers with the page as markdown.
    # It fetches a single URL from inside the cluster and answers with the address it
    # actually landed on, the document's title, its content rendered to MARKDOWN, and
    # whatever the page said about itself. One URL per call: batching would make the
    # answer a partial-failure envelope every caller then has to unpack.
    # A PAGE THAT COULD NOT BE FETCHED IS A NORMAL ANSWER, not a fault. An
    # unreachable host, a refused address and a content type that is not a document
    # all answer 200 with `success:false` and the reason in `error`, because the
    # caller sent a well-formed ask and gets a well-formed answer. Non-2xx is reserved
    # for a caller problem — 400 with the same body when there is no url, 401 for a
    # bad key, 503 when the surface is unconfigured — so error handling can trust the
    # status. Check `success` before reading `data`.
    # Admission is either a validated principal or the shared service key, presented
    # as X-API-Key or a Bearer; neither is refused, and an unset key fails closed
    # rather than opening the fetcher to the private network. Pages are archived under
    # the scope of the VERIFIED principal and NEVER a scope named in the body, so a
    # URL already read under that scope is answered from the archive without touching
    # the network; a service caller has no org and its pages land in the shared
    # corpus.
    # The URL is caller-supplied and dialled from INSIDE the cluster, which makes this
    # a request-forgery primitive by construction. Only http and https are accepted,
    # and every address actually dialled must be public unicast — loopback,
    # link-local, private and multicast are refused. The check lives in the DIALER
    # rather than on the hostname, because resolving a name to validate it and then
    # letting the transport resolve it again is a gap DNS rebinding walks straight
    # through; redirects re-enter the same dialer.
    read_page(req: crawlRequest) returns (rep: crawlResult)
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   crawlResult.Data  crawl.crawlDocument
