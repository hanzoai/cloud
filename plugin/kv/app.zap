# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package kv

struct bucketRecord {
    Bucket  text @0
    History i64  @8
    TTL     i64  @16
    Values  u64  @24
}

struct bucketRef {
    Bucket text @0
}

struct bucketWrite {
    Bucket   text @0
    History  i64  @8
    TTL      i64  @16
    MaxValue i64  @24
}

struct keyRef {
    Bucket text @0
    Key    text @8
}

struct kvAck {
    Revision u64 @0
}

struct kvEntry {
    Key       text @0
    Value     text @8
    Revision  u64  @16
    Created   text @24
    Operation text @32
}

struct kvPage {
    Data list<bytes> @0
}

struct kvWrite {
    Bucket text @0
    Key    text @8
    Value  text @16
}

interface kv {
    # Removes one bucket of the caller's org — every key and every
    # revision with it — and answers 204 with no body. 404 when the org has no
    # bucket of that name.
    delete_kv_by_bucket(req: bucketRef)
    # Delete removes one key — a delete marker in the key's history, so watchers
    # see it and Get answers 404 — and answers 204 with no body. 404 when the
    # bucket does not exist.
    delete_kv_by_bucket_by_key(req: keyRef)
    # Get returns one key's current value and revision. 404 when the bucket does
    # not exist, the key was never written, or its latest revision is a delete.
    get_kv_by_bucket_by_key(req: keyRef) returns (rep: kvEntry)
    # History returns one key's retained revisions, oldest first — every put and
    # every delete marker up to the bucket's History depth. 404 when the bucket
    # does not exist or the key was never written.
    get_kv_by_bucket_by_key_history(req: keyRef) returns (rep: kvPage)
    # Creates a KV bucket and returns it. A bucket is keyed state on
    # the same durable plane as the streams: each key holds up to History
    # revisions, entries can expire by TTL, and watchers on the NATS port see every
    # write. 409 when the org already has a bucket of that name.
    post_kv_by_bucket(req: bucketWrite) returns (rep: bucketRecord)
    # Put sets one key to one value and returns the revision the write created.
    # Writes are versioned: each put is a new revision and the bucket retains up to
    # its History of them per key.
    put_kv_by_bucket_by_key(req: kvWrite) returns (rep: kvAck)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   kvPage.Data  kv.kvEntry (list element)
