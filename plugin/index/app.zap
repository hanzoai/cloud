# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package index

struct indexAt {
    UID text @0
}

struct indexDoc {
    ID  text @0
    UID text @8
}

struct indexDocuments {
    Limit   i64         @0
    Offset  i64         @8
    Results list<bytes> @16
    Total   i64         @24
}

struct indexEnqueued {
    EnqueuedAt text @0
    IndexUID   text @8
    Status     text @16
    TaskUID    i64  @24
    Type       text @32
}

struct indexFilter {
    FilterableAttributes list<text> @0
    UID                  text       @8
}

struct indexHealth {
    Status text @0
}

struct indexHits {
    EstimatedTotalHits i64         @0
    Hits               list<bytes> @8
    Limit              i64         @16
    Offset             i64         @24
    ProcessingTimeMs   i64         @32
    Query              text        @40
}

struct indexList {
    Limit   i64         @0
    Offset  i64         @8
    Results list<bytes> @16
    Total   i64         @24
}

struct indexNew {
    PrimaryKey text @0
    UID        text @8
}

struct indexPage {
    Limit  text @0
    Offset text @8
    UID    text @16
}

struct indexQuery {
    Filter bytes @0
    Limit  i64   @8
    Offset i64   @16
    Q      text  @24
    UID    text  @32
}

struct indexSettings {
    FilterableAttributes list<text> @0
}

struct indexTask {
    EnqueuedAt text @0
    FinishedAt text @8
    StartedAt  text @16
    Status     text @24
    Type       text @32
    UID        i64  @40
}

struct indexTaskAt {
    UID i64 @0
}

struct indexVersion {
    CommitDate text @0
    CommitSha  text @8
    PkgVersion text @16
}

struct indexView {
    CreatedAt  text @0
    PrimaryKey text @8
    UID        text @16
    UpdatedAt  text @24
}

interface index {
    # Deletes an index and everything in it.
    # Drops the index and every document in it from the caller's own org, and
    # answers the dialect's EnqueuedTask. This is the only way to retire an index;
    # without it a mistaken uid is permanent. Deleting an index that is not there
    # succeeds, so a cleanup pass is safe to re-run.
    # The 202 and its `enqueued` task are DIALECT COMPATIBILITY, not a promise of
    # later work: the documents are already gone when this answers.
    delete_index_indexes_by_uid(req: indexAt) returns (rep: indexEnqueued)
    # Deletes one document by its primary key.
    # Removes the document from the caller's own org and answers the dialect's
    # EnqueuedTask. Deleting a key that is not there succeeds, so a client
    # reconciling its own corpus can delete without checking first.
    # The 202 and its `enqueued` task are DIALECT COMPATIBILITY, not a promise of
    # later work: the document is already gone when this answers.
    delete_index_indexes_by_uid_documents_by_id(req: indexDoc) returns (rep: indexEnqueued)
    # Reports whether the search plane can serve.
    # Answers the dialect's `{"status":"available"}` when the index store is
    # readable. It FAILS CLOSED — an unreadable store answers 503 with
    # `{"status":"unavailable"}` rather than an empty result set, because a
    # Meilisearch client probes this before it will use a server at all and a
    # cheerful 200 over a broken volume turns "search is down" into "nothing
    # matched". It requires no principal and reads no tenant data.
    get_index_health() returns (rep: indexHealth)
    # Lists the indexes your org holds.
    # Answers every index in the caller's own org with its primary key and
    # timestamps. Without it an index whose uid a caller has forgotten is
    # unreachable — there is no other way to enumerate what an org holds. The page
    # is the whole set: an org's index count is small by construction, so `limit`
    # and `total` both report it.
    # The tenant is the org minted from the VALIDATED bearer's owner claim, never a
    # client-supplied header, and two orgs may both hold an index named "messages"
    # without either seeing the other. Without a validated principal the answer is
    # 403 carrying the dialect's `invalid_api_key` body.
    get_index_indexes() returns (rep: indexList)
    # Reads one index's definition.
    # Answers the index's uid, primary key and timestamps. An index this org does
    # not hold answers 404 carrying the dialect's `index_not_found` — the code a
    # Meilisearch client reads as permission to create it, which is why this is a
    # refusal rather than an empty object.
    # The uid is scoped to the caller's own org, so another tenant's index is
    # indistinguishable from one that never existed: this surface is not an
    # existence oracle.
    get_index_indexes_by_uid(req: indexAt) returns (rep: indexView)
    # Pages through the documents in an index.
    # Answers the org's stored documents in insertion order, whole, with the page's
    # bounds and the index's total. It is the enumeration surface — search ranks by
    # relevance and cannot walk a corpus — so a caller reconciling what it has
    # written reads it here.
    # An index this org does not hold answers 404 carrying the dialect's
    # `index_not_found`.
    get_index_indexes_by_uid_documents(req: indexPage) returns (rep: indexDocuments)
    # Reads one document by its primary key.
    # Answers the stored document exactly as it was written — this surface keeps
    # documents whole rather than projecting them, so what comes back is what went
    # in. A primary key this index does not hold answers 404 carrying the dialect's
    # `document_not_found`; an index this org does not hold answers
    # `index_not_found`, and the two are different facts a client acts on
    # differently.
    get_index_indexes_by_uid_documents_by_id(req: indexDoc)
    # Reads an index's filterable attributes.
    # Answers the settings subset this surface implements: the attributes a search
    # `filter` may constrain. An index this org does not hold answers 404 carrying
    # the dialect's `index_not_found`.
    get_index_indexes_by_uid_settings(req: indexAt) returns (rep: indexSettings)
    # Checks a write task, which has already finished.
    # Always reports `succeeded`. Writes here are applied to SQLite before their
    # EnqueuedTask is returned, so a client polling waitForTask resolves on its
    # first call rather than waiting for a queue that was never there. The three
    # timestamps are the same instant for the same reason.
    # It requires a validated principal but reads no tenant data: the task id it
    # echoes was minted by this process and names nothing about any org.
    get_index_tasks_by_uid(req: indexTaskAt) returns (rep: indexTask)
    # Identifies the search implementation answering.
    # Reports the dialect's version shape with `commitSha` naming this
    # implementation rather than a Meilisearch build, so a client that logs the
    # version records which server answered instead of implying a release of
    # software this is not. It requires no principal and reads no tenant data.
    get_index_version() returns (rep: indexVersion)
    # Sets which attributes an index can be filtered on.
    # Replaces the whole filterable set. An attribute not listed here cannot be
    # used in a search `filter`, so this is what makes a per-user or per-tag
    # narrowing possible at all.
    # It CREATES the index when it is missing rather than answering 404, because a
    # Meilisearch client configures settings on an index it has just asked for and
    # a refusal there leaves the client with no index at all.
    # The 202 and its `enqueued` task are DIALECT COMPATIBILITY, not a promise of
    # later work: the setting is already applied when this answers.
    patch_index_indexes_by_uid_settings(req: indexFilter) returns (rep: indexEnqueued)
    # Creates an index.
    # Registers a named index in the caller's own org and answers the dialect's
    # EnqueuedTask. It is idempotent: creating an index that already exists returns
    # the same receipt and changes nothing, which is what lets a client create on
    # startup without checking first.
    # `primaryKey` is optional — the first write establishes one when it is omitted.
    # An index is a ROW here rather than a table, so an unusual uid is stored
    # verbatim instead of being sanitised into a schema name.
    # The 202 and its `enqueued` task are DIALECT COMPATIBILITY, not a promise of
    # later work: the write is already applied when this answers. A client that
    # polls waitForTask resolves immediately.
    post_index_indexes(req: indexNew) returns (rep: indexEnqueued)
    # Searches an index, forgiving typos.
    # Ranks the org's documents in one index against `q` and answers the matching
    # documents whole, most relevant first. A prefix matches, so a partial word
    # finds the documents containing it, and `filter` narrows the result to
    # documents whose filterable attributes match — which is how a caller scopes
    # results to one end user within its own org.
    # `estimatedTotalHits` is the dialect's name for the count; every hit is
    # materialised here, so for this page it is exact. An index this org does not
    # hold answers 404 carrying the dialect's `index_not_found`.
    post_index_indexes_by_uid_search(req: indexQuery) returns (rep: indexHits)
}

# ---------------------------------------------------------------------
# 13 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   get_index_stats  indexStats.Indexes  map[string]index.indexCount  (map)
#
# opaque (1) — crosses, arrives without its name:
#   indexList.Results  index.indexView (list element)
