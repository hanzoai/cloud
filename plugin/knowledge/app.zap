# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package knowledge

struct File {
    ID       text @0
    Name     text @8
    Type     text @16
    Size     i64  @24
    Bucket   text @32
    Key      text @40
    Project  text @48
    Parent   text @56
    Status   text @64
    Stage    text @72
    Error    text @80
    Chars    i64  @88
    Clipped  bool @96
    Sections i64  @104
    Passages i64  @112
    Embedded i64  @120
    Done     i64  @128
    Total    i64  @136
    Note     text @144
    Created  i64  @152
    Updated  i64  @160
}

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

struct fileGraph {
    File     bytes       @0
    Links    list<bytes> @8
    Entities list<bytes> @16
}

struct fileIn {
    Bucket  text @0
    Key     text @8
    Project text @16
}

struct fileRef {
    ID text @0
}

struct fileSearchIn {
    Query   text       @0
    Files   list<text> @8
    Project text       @16
    Limit   i64        @24
}

struct fileSearchOut {
    Passages list<bytes> @0
    Degraded bool        @8
}

struct filesIn {
    Bucket text @0
    Limit  text @8
}

struct filesOut {
    Files list<bytes> @0
}

struct forgotten {
    ID text @0
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

struct retrieveIn {
    Query   text       @0
    Files   list<text> @8
    Project text       @16
    Limit   i64        @24
}

struct retrieveOut {
    Sections list<bytes> @0
    Passages list<bytes> @8
    Picked   text        @16
    Degraded bool        @24
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

struct sectionOut {
    File     bytes       @0
    Section  bytes       @8
    Summary  text        @16
    Text     text        @24
    Children list<bytes> @32
    Links    list<bytes> @40
    Entities list<bytes> @48
}

struct sectionRef {
    ID      text @0
    Section text @8
}

struct tocOut {
    File     bytes       @0
    Sections list<bytes> @8
}

interface knowledge {
    # Revokes a connection: it tombstones the stored credential
    # so a later sync cannot reuse it, removes this provider's passages from the
    # org's store, and marks the connector disconnected. The documents already
    # ingested stay in the org's store — they are the org's own data — but stop being
    # retrievable by search; a caller deletes them through the document surface.
    delete_knowledge_connectors_by_provider(req: providerIn) returns (rep: connectionOut)
    # Removes one of the caller's org files from the index — its table of
    # contents, passages, full-text rows, mentions and links — and its record. The
    # file is gone from every read when this answers: its listing, its contents,
    # search and retrieval. What the index held of it is removed after, a batch at
    # a time, so forgetting a file of any size answers at once. The object itself
    # stays in its bucket; delete it there. Drive calls this when it deletes a file.
    # Only the person who registered the file or an admin of the org may forget it;
    # anyone else in the org, who can still read it, is refused 403. A file
    # registered before uploaders were recorded is forgotten by an admin alone.
    delete_knowledge_files_by_id(req: fileRef) returns (rep: forgotten)
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
    # Answers the caller's org files, most recently changed first, each
    # with its status and stage — optionally only those in one bucket, which is how
    # Drive shows a folder's files with their index state.
    get_knowledge_files(req: filesIn) returns (rep: filesOut)
    # Answers one of the caller's org files: what it is, where its bytes
    # are, and how far its ingest has got. 404 for an id the org holds no file for.
    get_knowledge_files_by_id(req: fileRef) returns (rep: File)
    # Answers one of the caller's org files' place in the org's graph:
    # the files it links to and is linked from (a hyperlink, a citation by name),
    # the files that name the same entities, and the entities it names. It is how
    # an agent finds the other documents a question about this one needs.
    get_knowledge_files_by_id_graph(req: fileRef) returns (rep: fileGraph)
    # Answers one section of one of the caller's org files: its own
    # text, its subsections, the sections it links to or shares entities with —
    # in this file or another — and the entities it names. Open the document
    # itself with section 0.
    get_knowledge_files_by_id_sections_by_section(req: sectionRef) returns (rep: sectionOut)
    # Answers one of the caller's org files' table of contents: every
    # section in document order with its depth, its parent and a one-line summary.
    # It is the map a reader — a person in Drive, or an agent deciding where to
    # look — reads before opening a section with GET
    # /v1/knowledge/files/{id}/sections/{section}.
    get_knowledge_files_by_id_toc(req: fileRef) returns (rep: tocOut)
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
    # Makes an object in one of the caller's org buckets a workspace
    # file. Upload the bytes first — POST /v1/s3/buckets/{bucket}/objects mints a
    # presigned PUT, POST /v1/s3/buckets/{bucket}/uploads starts a multipart upload
    # for a large one — then register the key here. The object's name, type and
    # size are read from the store. The file is recorded as queued, a file_stored
    # event is stated on the org's event plane, and a durable ingest is queued that
    # extracts its text and structure, summarizes its table of contents, cuts and
    # embeds its passages and links it into the org's graph. An archive (.zip) is
    # unpacked and every file inside becomes a workspace file of its own. Poll GET
    # /v1/knowledge/files/{id} for status. Registering an object again answers the
    # same file, and re-indexes it only when the object changed or its last ingest
    # failed.
    # Registering a changed object again changes its file, which only the person
    # who registered it or an admin of the org may do: anyone else is refused 403
    # and the file keeps its version. Registering an unchanged object is a read.
    post_knowledge_files(req: fileIn) returns (rep: File)
    # Grounds an answer in the caller's org files, table of contents
    # first: candidate documents are found by searching their passages; a model
    # reads their tables of contents — titles and one-line summaries — and picks
    # the sections the answer is in; hybrid search drills into the passages of
    # those sections; and the graph expands to the sections they link to or name
    # the same entities as, in any file of the workspace. Every passage cites its
    # file › section › paragraph, so an answer can say exactly where it came from.
    # This is what a chat runs before it answers about an attached file, and what
    # an agent runs before it answers about a workspace.
    post_knowledge_files_retrieve(req: retrieveIn) returns (rep: retrieveOut)
    # Answers the passages of the caller's org files that match a query,
    # each citing its file › section › paragraph: the search behind Drive's box,
    # and the first step an agent takes across a workspace. Name file ids to search
    # only those files; name none to search them all. A semantic leg compares the
    # query with every embedded passage and a full-text leg finds the passages
    # holding its words; the two are fused, so a passage both found comes first.
    post_knowledge_files_search(req: fileSearchIn) returns (rep: fileSearchOut)
    # Rebuilds the caller org's retrieval from its documents: every passage
    # is removed, and every page, memory and source is cut into passages and
    # embedded again with the configured model; the lexical index is reconciled to
    # the same documents. It is what an operator runs after the embedding model
    # changes — passages of another model are never compared with the query, so
    # until then they are unread — and what puts an org's retrieval right after an
    # outage of the ai plane. It requires ORG ADMIN and runs inline: an org's
    # knowledge is a few thousand documents, and the answer is the count.
    # The request has no body. Response: {"vectors": 412, "lexical": 412, "removed": 3, "failed": 0}
    post_knowledge_reindex() returns (rep: reindexOut)
    # Runs a hybrid search over the caller org's own knowledge —
    # its wiki pages, its agent memories and everything its connectors have synced.
    # This is the RAG entry point: an agent asks "what does this org know about X"
    # and the org's OWN knowledge answers. Two legs run and are fused by reciprocal
    # rank: a semantic leg that embeds the query and compares it with every passage
    # of every document in reach (a document is cut into passages of about 2000
    # bytes, so a fact deep in a long page is found), and a keyword leg over the
    # org's knowledge index. The org comes from the validated principal and its
    # passages live in its own database, so cross-tenant retrieval is impossible. A
    # failed leg answers 200 with what the other leg found and degraded=true, never
    # a 5xx.
    post_knowledge_search(req: searchIn) returns (rep: searchOut)
}

# ---------------------------------------------------------------------
# 18 op(s) here. What follows is what this schema does not carry.
#
# opaque (19) — crosses, arrives without its name:
#   catalogOut.Connectors  knowledge.catalogEntry (list element)
#   fileGraph.Entities  knowledge.fileEntity (list element)
#   fileGraph.File  knowledge.File
#   fileGraph.Links  knowledge.fileLink (list element)
#   fileSearchOut.Passages  knowledge.citedPassage (list element)
#   filesOut.Files  knowledge.File (list element)
#   graphOut.Edges  knowledge.graphEdge (list element)
#   graphOut.Nodes  knowledge.graphNode (list element)
#   kbConnectorsOut.Connectors  knowledge.connectorView (list element)
#   retrieveOut.Passages  knowledge.citedPassage (list element)
#   retrieveOut.Sections  knowledge.pickedSection (list element)
#   searchOut.Hits  knowledge.hit (list element)
#   sectionOut.Children  knowledge.tocEntry (list element)
#   sectionOut.Entities  knowledge.fileEntity (list element)
#   sectionOut.File  knowledge.fileBrief
#   sectionOut.Links  knowledge.sectionLink (list element)
#   sectionOut.Section  knowledge.sectionBrief
#   tocOut.File  knowledge.File
#   tocOut.Sections  knowledge.tocEntry (list element)
