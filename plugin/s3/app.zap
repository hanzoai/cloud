# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package s3

struct bucketIn {
    Name text @0
}

struct bucketItem {
    Name      text @0
    CreatedAt i64  @8
}

struct bucketList {
    Buckets list<bytes> @0
    Total   i64         @8
}

struct bucketRef {
    Bucket text @0
}

struct listIn {
    Bucket    text @0
    Prefix    text @8
    Recursive text @16
}

struct objectList {
    Bucket  text        @0
    Prefix  text        @8
    Objects list<bytes> @16
    Total   i64         @24
}

struct objectRef {
    Bucket text @0
    Key    text @8
}

struct partURLs {
    URLs   list<bytes> @0
    Expiry i64         @8
}

struct presignResponse {
    URL    text @0
    Method text @8
    Key    text @16
    Expiry i64  @24
}

struct s3Health {
    Service text @0
    Status  text @8
    Ready   bool @16
    Presign bool @17
    Error   text @24
}

struct storedParts {
    Upload text        @0
    Key    text        @8
    Parts  list<bytes> @16
}

struct uploadDone {
    Key   text @0
    ETag  text @8
    Size  i64  @16
    Parts i64  @24
}

struct uploadGone {
    Upload text @0
}

struct uploadIn {
    Bucket text @0
    Key    text @8
}

struct uploadParts {
    Bucket text      @0
    Upload text      @8
    Key    text      @16
    Parts  list<i64> @24
}

struct uploadRef {
    Bucket text @0
    Upload text @8
    Key    text @16
}

struct uploadStart {
    Bucket text @0
    Key    text @8
    Type   text @16
}

struct uploadStarted {
    Upload   text @0
    Key      text @8
    PartSize i64  @16
}

interface s3 {
    # Removes an EMPTY bucket and answers 204.
    # It requires an admin of the org, or a SuperAdmin, as creating one does; anyone
    # else is refused 403 with nothing deleted.
    # A non-empty bucket is 409 rather than a cascade: deleting a tenant's objects
    # behind a single bucket call is not a thing this surface will do silently. A
    # bucket the caller's org does not own is the same 404 an unknown name gives.
    # Billed per call: the balance is checked BEFORE anything is touched, so an
    # unfunded org is refused with nothing deleted, and the debit lands only once the
    # bucket is gone.
    delete_s3_buckets_by_bucket(req: bucketRef)
    # Removes one object and answers 204.
    # It removes ONE object and never a prefix: a key that looks like a folder deletes
    # the placeholder at that key, not the objects beneath it. The key is path-cleaned
    # first, so the delete cannot reach outside the bucket it names, and a bucket the
    # caller's org does not own is the same 404 an unknown name gives.
    # Billed per call: the balance is checked BEFORE anything is touched, so an
    # unfunded org is refused with nothing deleted, and the debit lands only once the
    # object is gone.
    delete_s3_buckets_by_bucket_objects_by_wildcard1(req: objectRef)
    # Aborts a multipart upload and deletes the parts it stored. The
    # object it would have become is never created.
    delete_s3_buckets_by_bucket_uploads_by_upload(req: uploadRef) returns (rep: uploadGone)
    # Lists the caller org's own buckets.
    # Only the caller's: every bucket is physically named under a per-org prefix and
    # the listing strips that prefix, so a tenant sees friendly names and another
    # tenant's buckets are not in the answer at all. Another org's bucket is not
    # refused but INVISIBLE, so this cannot be used to learn that a name is taken
    # elsewhere.
    # Billed per call: the balance is checked BEFORE anything is touched, so an
    # unfunded org is refused with nothing done, and the debit lands only once the
    # work has succeeded.
    get_s3_buckets() returns (rep: bucketList)
    # Lists one folder level of a bucket.
    # Folder-style by default: sub-prefixes come back as directory entries, which is
    # the file-manager view. `?recursive=true` lists every key flat under the prefix
    # instead. Keys are RELATIVE to `?prefix=`, and the listing is bounded so a huge
    # bucket cannot exhaust memory — Total is what came back, not what the bucket
    # holds.
    # Billed per call: the balance is checked BEFORE anything is touched, so an
    # unfunded org is refused with nothing read, and the debit lands only once the
    # listing has succeeded.
    get_s3_buckets_by_bucket_objects(req: listIn) returns (rep: objectList)
    # Mints a presigned GET URL the caller downloads from DIRECTLY.
    # The bytes never pass through this binary and the admin credential never leaves
    # the server: the URL is signed against the PUBLIC host, scoped to exactly this
    # bucket and key, and expires. It carries a content disposition of attachment
    # naming the object's file name, so a browser following it saves the object rather
    # than rendering it in place. A deployment with no public endpoint configured
    # cannot mint one and answers 503 rather than a URL that will not work.
    # Billed per call — for MINTING the URL, which is the work this operation does;
    # the download that follows it comes straight from the store and is not seen here.
    # The balance is checked BEFORE anything is touched, so an unfunded org is refused
    # with no URL issued.
    get_s3_buckets_by_bucket_objects_by_wildcard1(req: objectRef) returns (rep: presignResponse)
    # Answers the parts a multipart upload already holds, in order: what
    # a client resuming after a dropped connection reads to send only the rest.
    get_s3_buckets_by_bucket_uploads_by_upload(req: uploadRef) returns (rep: storedParts)
    # Reports whether this deployment can serve object storage.
    # It is a REAL probe rather than a constant: 200 when admin credentials are
    # present, so the store is reachable in principle, and 503 with the reason when
    # they are not. It is deliberately NOT gated — liveness has to be probe-able
    # without a token — so it is the one operation here that names no bucket and
    # bills nothing.
    get_s3_health() returns (rep: s3Health)
    # Makes a new bucket for the caller's org and answers 201 with it.
    # It requires an admin of the org, a SuperAdmin, or the org's own application
    # (CI makes its cache bucket on first use): every member of the org uses
    # its buckets, and only an admin makes or removes one. Anyone else is refused 403
    # with nothing created.
    # The physical name is derived from the caller's validated org, so a tenant can
    # only ever create inside its own namespace and no request field can redirect
    # that. A name already taken in the org is 409.
    # Billed per call: the balance is checked BEFORE anything is touched, so an
    # unfunded org is refused with nothing created, and the debit lands only once the
    # bucket exists.
    post_s3_buckets(req: bucketIn) returns (rep: bucketItem)
    # Mints a presigned PUT URL the caller uploads to DIRECTLY.
    # The bytes never pass through this binary and the admin credential never leaves
    # the server: the URL is signed against the PUBLIC host, scoped to exactly this
    # bucket and key, and expires. A deployment with no public endpoint configured
    # cannot mint one and answers 503 rather than a URL that will not work.
    # Billed per call — for MINTING the URL, which is the work this operation does;
    # the upload that follows it goes straight to the store and is not seen here. The
    # balance is checked BEFORE anything is touched, so an unfunded org is refused
    # with no URL issued.
    post_s3_buckets_by_bucket_objects(req: uploadIn) returns (rep: presignResponse)
    # Begins a multipart upload of a large file into one of the caller's
    # org buckets. Then mint presigned URLs for its parts with POST
    # .../uploads/{upload}/parts, PUT each part's bytes to its URL — every part but
    # the last exactly partSize bytes — and assemble the object with POST
    # .../uploads/{upload}/complete. A dropped connection loses nothing:
    # GET .../uploads/{upload} lists the parts already stored, so only the rest are
    # sent again. Use this for anything over a few megabytes; a small file takes the
    # single presigned PUT of POST /v1/s3/buckets/{bucket}/objects.
    post_s3_buckets_by_bucket_uploads(req: uploadStart) returns (rep: uploadStarted)
    # Assembles a multipart upload into its object from every part the
    # store holds, in order. The parts must run 1, 2, 3 … with no gap; a gap is 409,
    # naming the first part missing, and the upload stays open to send it.
    post_s3_buckets_by_bucket_uploads_by_upload_complete(req: uploadRef) returns (rep: uploadDone)
    # Answers presigned PUT URLs for some parts of a multipart upload, at
    # most 64 a call, each valid for minutes: mint them as the upload reaches them.
    # PUT a part's bytes to its URL as they are — no headers are signed — and the
    # store answers the part's ETag, which completing does not need.
    post_s3_buckets_by_bucket_uploads_by_upload_parts(req: uploadParts) returns (rep: partURLs)
}

# ---------------------------------------------------------------------
# 13 op(s) here. What follows is what this schema does not carry.
#
# opaque (4) — crosses, arrives without its name:
#   bucketList.Buckets  s3.bucketItem (list element)
#   objectList.Objects  s3.objectItem (list element)
#   partURLs.URLs  s3.partURL (list element)
#   storedParts.Parts  s3.storedPart (list element)
