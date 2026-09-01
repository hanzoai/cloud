# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package knowledge

struct callbackIn {
    Provider text @0
    Code     text @8
    State    text @16
    Error    text @24
}

struct catalogOut {
    Connectors list<bytes> @0
}

struct connectionOut {
    Provider text @0
    Status   text @8
    Account  text @16
}

struct graphIn {
    Project text @0
}

struct graphOut {
    Nodes    list<bytes> @0
    Edges    list<bytes> @8
    Degraded bool        @16
}

struct kbAuthorizeOut {
    AuthorizeURL text @0
}

struct kbConnectorsOut {
    Connectors list<bytes> @0
}

struct kbSyncOut {
    Provider text @0
    Ingested i64  @8
}

struct providerIn {
    Provider text @0
}

struct reindexOut {
    Vectors i64 @0
    Lexical i64 @8
    Removed i64 @16
    Failed  i64 @24
}

struct searchIn {
    Query    text       @0
    Limit    i64        @8
    Project  text       @16
    DocTypes list<text> @24
}

struct searchOut {
    Hits     list<bytes> @0
    Degraded bool        @8
}

interface knowledge {
    # Revokes a connection: it tombstones the stored credential
    # so a later sync cannot reuse it, purges this provider's points from the org's
    # vector namespace, and marks the connector disconnected. The documents already
    # ingested stay in the org's store — they are the org's own data — but stop being
    # retrievable by search; a caller deletes them through the document surface.
    delete_knowledge_connectors_by_provider(req: providerIn) returns (rep: connectionOut)
    # Returns every supported knowledge connector with THIS org's
    # connection state and the REAL number of documents each has ingested into the
    # org's store. A provider that is configured for the deployment but not yet
    # connected appears as disconnected, so the console can offer a Connect button.
    # No secret is ever returned.
    get_knowledge_connectors() returns (rep: kbConnectorsOut)
    # CompleteConnectorOAuth finishes an OAuth connection: it exchanges the
    # provider's code for a token, seals that token in KMS, and records the
    # connection. THE ORG COMES FROM THE SIGNED STATE, not from a header and not from
    # the provider, so an attacker cannot bind their own account to someone else's
    # org — a tampered, expired or foreign-provider state is refused outright. The
    # token itself is never returned, never written into the document, and never
    # logged; the document holds only its KMS path.
    get_knowledge_connectors_by_provider_callback(req: callbackIn) returns (rep: connectionOut)
    # StartConnectorOAuth returns the provider authorize URL the console opens to
    # connect this org's account. There is no server-side redirect — the console
    # stays in control of the navigation. The URL carries a state this server SIGNED
    # over the caller's validated org, so the connection the callback completes can
    # only ever land in that org.
    get_knowledge_connectors_by_provider_connect(req: providerIn) returns (rep: kbAuthorizeOut)
    # Returns the ONE catalog of everything a caller can
    # connect: every first-party connector and every long-tail one, in a single list
    # sorted by provider. `configured` reports whether this deployment holds OAuth
    # credentials for a source, so the console can show Connect rather than a dead
    # button, and `kind` is a badge only — the connect and sync lifecycle is
    # identical for both. The catalog itself is org-independent; a validated
    # principal is still required. It is metadata only: no secret is ever returned.
    get_knowledge_connectors_catalog() returns (rep: catalogOut)
    # Returns the caller org's knowledge as a node/edge graph
    # shaped for a force-directed renderer: pages, memories and synced sources as
    # nodes; the page parent tree, the wikilinks between pages, and each source's
    # connector provenance as edges. Wikilink targets are resolved HERE by title or
    # slug, so a rename never needs an edge rewrite and a link that matches no page
    # renders as its own "unresolved" node instead of vanishing. ?project= narrows
    # it. A store outage degrades to an honest empty graph, never a 5xx.
    get_knowledge_graph(req: graphIn) returns (rep: graphOut)
    # Pulls the provider's documents for the caller's org and files
    # them as knowledge sources, which the store's own hook then indexes — so a
    # synced document is retrievable exactly like a hand-written page. The org is the
    # validated tenant and the credential is read from KMS, so an org can only ever
    # sync its own connection. A provider failure is reported honestly (502) and
    # recorded on the connector rather than silently swallowed.
    post_knowledge_connectors_by_provider_sync(req: providerIn) returns (rep: kbSyncOut)
    # Rebuilds the caller org's retrieval from its documents: the vector
    # collection is dropped and created again at the configured embedding size and
    # every page, memory and source is embedded into it; the lexical index is
    # reconciled to the same set. It is what an operator runs after the embedding
    # model or its dimension changes, and what puts an org's retrieval right after
    # a vector outage. It requires ORG ADMIN and runs inline: an org's knowledge is
    # a few thousand documents, and the answer is the count.
    # The request has no body. Response: {"vectors": 412, "lexical": 412, "removed": 3, "failed": 0}
    post_knowledge_reindex() returns (rep: reindexOut)
    # Runs a semantic search over the caller org's own knowledge —
    # its wiki pages, its agent memories and everything its connectors have synced —
    # and returns the matching passages. This is the RAG entry point: an agent asks
    # "what does this org know about X" and the org's OWN vector namespace answers.
    # The org comes from the validated principal, and both the collection and the
    # payload filter are pinned to it, so cross-tenant retrieval is impossible. An
    # unreachable index returns an honest empty result set with degraded=true, never
    # a 5xx.
    post_knowledge_search(req: searchIn) returns (rep: searchOut)
}

# ---------------------------------------------------------------------
# 9 op(s) here. What follows is what this schema does not carry.
#
# opaque (5) — crosses, arrives without its name:
#   catalogOut.Connectors  knowledge.catalogEntry (list element)
#   graphOut.Edges  knowledge.graphEdge (list element)
#   graphOut.Nodes  knowledge.graphNode (list element)
#   kbConnectorsOut.Connectors  knowledge.connectorView (list element)
#   searchOut.Hits  knowledge.hit (list element)
