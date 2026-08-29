# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package provisioning

struct provisionRequest {
    Name     text @0
    Instance text @8
}

struct provisionResult {
    ID               text @0
    Kind             text @8
    Name             text @16
    Status           text @24
    Host             text @32
    Port             i64  @40
    Username         text @48
    Database         text @56
    ConnectionString text @64
    Password         text @72
}

struct provisionedResource {
    ID       text @0
    Name     text @8
    Kind     text @16
    Status   text @24
    Host     text @32
    Port     i64  @40
    Username text @48
    Database text @56
}

struct resourceRef {
    Name text @0
}

struct vectorCollectionList {
    Collections list<bytes> @0
}

struct vectorStats {
    TotalCollections  i64 @0
    TotalVectors      i64 @8
    TotalStorageBytes i64 @16
}

interface provisioning {
    # Deprovisions one Hanzo Datastore warehouse. It reverts any app
    # instance bound to it back to Base BEFORE tearing down the org's dedicated
    # instance, then deletes the sealed credential and removes the metadata row.
    # Answers 204 with no body; a second call is a 404.
    delete_provisioning_datastore_by_name(req: resourceRef)
    # DropDocDB deprovisions one Hanzo DocDB database. It reverts any app instance
    # bound to it back to Base BEFORE tearing down the org's dedicated FerretDB
    # instance, then deletes the sealed credential and removes the metadata row.
    # Answers 204 with no body; a second call is a 404.
    delete_provisioning_docdb_by_name(req: resourceRef)
    # DropKV deprovisions one Hanzo KV store. It reverts any app instance bound to
    # it back to Base BEFORE tearing down the org's dedicated Valkey instance, then
    # deletes the sealed credential and removes the metadata row. Answers 204 with
    # no body; a second call is a 404.
    delete_provisioning_kv_by_name(req: resourceRef)
    # Deletes one bucket from the shared object store and removes its
    # metadata row. Answers 204 with no body; a second call is a 404.
    delete_provisioning_s3_by_name(req: resourceRef)
    # Deletes one search index from the shared backend and removes its
    # metadata row. Answers 204 with no body; a second call is a 404.
    delete_provisioning_search_by_name(req: resourceRef)
    # DropSQL deprovisions one Hanzo SQL database. It reverts any app instance
    # bound to it back to Base BEFORE tearing down the org's dedicated Postgres
    # instance — never a live app pointed at a deleted backend — then deletes the
    # sealed credential and removes the metadata row. Answers 204 with no body; a
    # second call is a 404, not a second delete.
    delete_provisioning_sql_by_name(req: resourceRef)
    # Deletes one vector collection from the shared backend and removes
    # its metadata row. Answers 204 with no body; a second call is a 404.
    delete_provisioning_vector_by_name(req: resourceRef)
    # Lists every collection in the deployment's vector store
    # with its size and geometry, across all tenants.
    # Per-collection detail is best-effort — one collection that fails to describe
    # itself keeps its name and defaults (dimension 0, cosine) rather than blanking
    # the whole answer — and an unreachable Qdrant answers 200 with an EMPTY list, so
    # the panel shows an honest empty state instead of an error.
    get_admin_provisioning_vector_collections() returns (rep: vectorCollectionList)
    # Totals the collections, vectors and storage across the whole
    # vector store.
    # Every figure is summed from the same per-collection detail the collections
    # listing returns, so the two panels can never disagree. An unreachable Qdrant
    # answers 200 with all zeros rather than an error.
    get_admin_provisioning_vector_stats() returns (rep: vectorStats)
    # Lists the caller org's Hanzo Datastore warehouses. Each one is
    # a DEDICATED analytical instance the org alone runs, so the host is that
    # instance's own in-cluster Service and the port is its HTTP port, 8123.
    get_provisioning_datastore()
    # Returns one Hanzo Datastore warehouse's metadata. It carries the
    # warehouse's status, its instance address and the admin user the instance
    # booted with — never the password. A still-booting instance reads
    # "provisioning", reconciled from the operator's live view rather than the row.
    get_provisioning_datastore_by_name(req: resourceRef) returns (rep: provisionedResource)
    # ListDocDB lists the caller org's Hanzo DocDB document databases. Each one is
    # a DEDICATED FerretDB instance the org alone runs, speaking the MongoDB wire
    # protocol, so the host is that instance's own in-cluster Service and the port
    # is 27017.
    get_provisioning_docdb()
    # GetDocDB returns one Hanzo DocDB database's metadata. It carries the
    # database's status, its instance address and the SCRAM user the instance was
    # set up with — never the password. A still-booting instance reads
    # "provisioning", reconciled from the operator's live view.
    get_provisioning_docdb_by_name(req: resourceRef) returns (rep: provisionedResource)
    # ListKV lists the caller org's Hanzo KV stores. Each one is a DEDICATED Valkey
    # instance the org alone runs, so the host is that instance's own in-cluster
    # Service and the port is 6379.
    get_provisioning_kv()
    # GetKV returns one Hanzo KV store's metadata. It carries the store's status,
    # its instance address and the Valkey user it authenticates as ("default", the
    # only user a requirepass instance has) — never the password. A still-booting
    # instance reads "provisioning", reconciled from the operator's live view.
    get_provisioning_kv_by_name(req: resourceRef) returns (rep: provisionedResource)
    # Lists the caller org's object-storage buckets. A bucket lives in an
    # already-live shared object store and is reached through the public gateway.
    # The names here are the friendly ones the org provisioned; the physical bucket
    # is org-namespaced underneath, which is what keeps two tenants' buckets
    # distinct.
    get_provisioning_s3()
    # Returns one bucket's metadata. It carries the bucket's status and the
    # gateway address it is reached at, and no username: the object store
    # authenticates with a shared, out-of-band key rather than a per-bucket
    # credential.
    get_provisioning_s3_by_name(req: resourceRef) returns (rep: provisionedResource)
    # Lists the caller org's search indexes. An index is a logical
    # resource inside an already-live shared backend, so every one of them is
    # reached through the public gateway rather than at an instance of its own.
    get_provisioning_search()
    # Returns one search index's metadata. It carries the index's status
    # and the gateway address it is reached at, and no username: the backend
    # authenticates with a shared, out-of-band key rather than a per-index
    # credential.
    get_provisioning_search_by_name(req: resourceRef) returns (rep: provisionedResource)
    # ListSQL lists the caller org's Hanzo SQL databases. Each one is a DEDICATED
    # PostgreSQL instance the org alone runs, so the host is that instance's own
    # in-cluster Service and the port is 5432.
    get_provisioning_sql()
    # GetSQL returns one Hanzo SQL database's metadata. It carries the database's
    # status, its instance address and the admin user Postgres booted with — never
    # the password, which is returned once at create and otherwise lives only in
    # Hanzo KMS. A still-booting instance reads "provisioning", reconciled from the
    # operator's live view rather than from the row.
    get_provisioning_sql_by_name(req: resourceRef) returns (rep: provisionedResource)
    # Lists the caller org's vector collections. A collection is a
    # logical resource inside an already-live shared backend, so every one of them
    # is reached through the public gateway rather than at an instance of its own.
    get_provisioning_vector()
    # Returns one vector collection's metadata. It carries the
    # collection's status and the gateway address it is reached at, and no username:
    # the backend authenticates with a shared, out-of-band key rather than a
    # per-collection credential, so there is no per-resource user to report.
    get_provisioning_vector_by_name(req: resourceRef) returns (rep: provisionedResource)
    # Launches your org's OWN Hanzo Datastore instance and answers
    # with its `datastore://` connection string.
    # The instance is yours alone — a deployment in your own tenant namespace, so its
    # admin credential is naturally scoped to you and no other tenant shares the
    # process. Off-cluster this fails closed with 503 rather than handing back a
    # shared one.
    post_provisioning_datastore(req: provisionRequest) returns (rep: provisionResult)
    # CreateDocDB launches your org's OWN document-database instance and answers with
    # its `mongodb://` connection string. It speaks the MongoDB wire protocol, so
    # existing MongoDB drivers connect unchanged.
    # The instance is yours alone — a deployment in your own tenant namespace, so its
    # admin credential is naturally scoped to you and no other tenant shares the
    # process. Off-cluster this fails closed with 503 rather than handing back a
    # shared one.
    post_provisioning_docdb(req: provisionRequest) returns (rep: provisionResult)
    # CreateKV launches your org's OWN key-value instance and answers with its `kv://`
    # connection string.
    # The instance is yours alone — a deployment in your own tenant namespace, so its
    # admin credential is naturally scoped to you and no other tenant shares the
    # process. Off-cluster this fails closed with 503 rather than handing back a
    # shared one.
    post_provisioning_kv(req: provisionRequest) returns (rep: provisionResult)
    # Creates an S3-compatible bucket inside the already-running shared
    # object store and answers with the endpoint that reaches it.
    post_provisioning_s3(req: provisionRequest) returns (rep: provisionResult)
    # Creates a search index inside the already-running shared search
    # backend and answers with the endpoint that reaches it.
    post_provisioning_search(req: provisionRequest) returns (rep: provisionResult)
    # CreateSQL launches your org's OWN PostgreSQL instance and answers with its
    # `postgres://` connection string.
    # The instance is yours alone — a deployment in your own tenant namespace, so its
    # admin credential is naturally scoped to you and no other tenant shares the
    # process. Off-cluster, where there is no orchestrator to launch one, this fails
    # closed with 503 rather than handing back a shared one.
    post_provisioning_sql(req: provisionRequest) returns (rep: provisionResult)
    # Creates a vector collection inside the already-running shared
    # vector backend and answers with the endpoint that reaches it.
    post_provisioning_vector(req: provisionRequest) returns (rep: provisionResult)
}

# ---------------------------------------------------------------------
# 30 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   vectorCollectionList.Collections  provisioning.vectorCollection (list element)
