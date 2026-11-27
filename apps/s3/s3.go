package s3

import (
	"context"
	"errors"
	s3driver "github.com/hanzoai/cloud/internal/driver/s3"
	"github.com/hanzoai/cloud/sdk/object"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/fare"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// presignTTL bounds every presigned upload/download URL. 5 minutes: short enough
// that a minted capability barely outlives a revoked session/role (RED MED —
// presigned URLs have no server-side revocation, so the TTL IS the revocation
// window), long enough for a browser to complete a normal PUT/GET. A caller who
// needs a fresh window simply re-mints (the console does so per action). NOTE:
// presign minting is not yet rate-limited — that is a platform-wide gap
// (zip/middleware/ratelimit exists but is unwired in serve.go); flagged to the
// platform, not fixable inside this subsystem.
const presignTTL = 5 * time.Minute

// maxListKeys caps one object-listing page so a bucket with millions of keys
// cannot exhaust memory or the response. Folder-style navigation only needs one
// level at a time, so this is generous.
const maxListKeys = 1000

// opFeeEnvPrefix is the operator knob for the per-operation object-storage fee:
// cloud.FeeCents(opFeeEnvPrefix, "op", 0), the CLOUD_S3_FEE_CENTS override, else
// free. Object storage has no live-size source in this data plane, so it is billed
// per-OPERATION (the S3 request-price model) via the ONE shared cloud.Meter
// (product "s3"); GB-month storage footprint reuses the SAME meter with a
// usage-derived amount once a live-size source exists — there is no second
// metering path.
//
// DEFAULTS TO FREE, not to cloud.DefaultResourceFeeCents. That $1.00 prices
// CREATING a resource; an operation here is a request — list the buckets, mint an
// upload URL — and a request is priced in fractions of a cent per thousand. A cent
// is the smallest price this knob can state, already thousands of times that, so
// the default is free (and therefore un-gated) and a price is a values change.
const opFeeEnvPrefix = "CLOUD_S3_FEE_CENTS"

// state is s3's own data; shared deps live in the embedded cloud.Base. It holds
// the shared S3 admin connection and nothing else: the per-org gate+meter is
// Base.Bill, whose commerce product label is the subsystem name, and that name
// is "s3" — the label the ledger carries for this plane. A not-Configured() admin
// means no credentials are present; the subsystem then mounts health/config only
// and every op fails closed 503. A nil Bill makes Authorize allow and Record a
// no-op.
type state struct {
	admin s3driver.Admin
}

// Mount wires /v1/s3/* onto app. The unconditional route set, each operation
// carrying its own preamble, makes this a direct construction (cloud.NewBase),
// not cloud.Use.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("s3: nil app")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "s3"), State: state{admin: s3driver.New()}}

	// Register the FULL surface unconditionally — even when S3 is unconfigured.
	// admit fails each operation closed with 503 (s.State.admin.Configured() is
	// false), so the s3 subsystem always OWNS its route space. If the routes were
	// mounted only when configured, an unconfigured deployment would leak
	// /v1/s3/buckets and /v1/s3/objects to provisioning's GET /v1/s3/:name (a 404
	// "resource not found") instead of the honest 503 — the file-manager surface
	// must fail closed under its own name, never fall through to a different
	// subsystem's handler.
	//
	// Routes go on the concrete app so zip's typed registrars and cmd/zipdoc can
	// both resolve the prefix; the scoped Router still owns any middleware, which
	// is where the ownership guard applies. Same shape apps/meet and apps/blueprint
	// use.
	o := ops{s: s}

	g := app.Group("/v1/s3")
	// Bridge is this subsystem's OWN, on its own group and ahead of every leaf: a
	// typed op is handed a context, and the request its gate reads is what this
	// parks there. Serve installs one app-wide, which is what carries the arms that
	// never touch a route — MCP, the call plane — and no package's test harness runs
	// Serve, so an app that does not install its own resolves no request on its own
	// routes and 403s in tests alone. It gates nothing, so /health, registered on
	// this group below and deliberately ungated, is unaffected.
	g.Use(cloud.Bridge())

	// The probe is NOT gated — liveness has to be probe-able without a token — and
	// it declares BOTH of its statuses, so the answer says which one it is.
	g.Get("/health", o.health, zip.WithStatus(http.StatusOK, http.StatusServiceUnavailable))

	// Everything else opens with admit and closes with settle, composed onto the
	// HANDLER. Nothing is installed at the prefix, so the probe registered above at
	// the same address stays ungated without a second scope to keep the two apart.
	//
	// THE LAST TWO SHADOW THE LISTING ABOVE THEM. fiber's `*` matches the EMPTY
	// remainder, so GET /v1/s3/buckets/photos/objects is answered by the download
	// rather than by listObjects and reads as 400 "object key is required" — with a
	// query string, with a trailing separator, every spelling. listObjects is
	// reachable only BY NAME, where dispatch is on the operation id and no route is
	// consulted.
	//
	// It is a property of the greedy capture and NOT of this order: the listing is
	// registered first and still loses, measured, on this exact route set. Nor is it
	// a cost of typing these two — the same requests answer the same way with them
	// raw. The narrowing fix is `+`, which is greedy but demands at least one
	// segment, and it is a WIRE change (that address would begin listing) plus a
	// change of fiber's key from "*1" to "+1", so it is a decision rather than a
	// tidy-up. Written down because the registration order reads as though the more
	// specific address wins, and it does not.
	g.Get("/buckets", fare.Paid(s, o.listBuckets))
	g.Post("/buckets", fare.Paid(s, o.createBucket), zip.WithStatus(http.StatusCreated))
	g.Delete("/buckets/:bucket", fare.Paid(s, o.deleteBucket))
	g.Get("/buckets/:bucket/objects", fare.Paid(s, o.listObjects))
	g.Post("/buckets/:bucket/objects", fare.Paid(s, o.presignUpload))
	// A large file goes up in parts (multipart.go): start, mint part URLs,
	// list what is stored to resume, complete, or abandon.
	g.Post("/buckets/:bucket/uploads", fare.Paid(s, o.startUpload))
	g.Post("/buckets/:bucket/uploads/:upload/parts", fare.Paid(s, o.mintParts))
	g.Get("/buckets/:bucket/uploads/:upload", fare.Paid(s, o.uploadParts))
	g.Post("/buckets/:bucket/uploads/:upload/complete", fare.Paid(s, o.completeUpload))
	g.Delete("/buckets/:bucket/uploads/:upload", fare.Paid(s, o.abandonUpload))
	// `+` and not `*`: a greedy `*` matches the EMPTY remainder and beats an exact
	// sibling whichever order they register in, so /objects — the collection —
	// arrived here as an object request with no key and was refused 400 before the
	// store was ever asked. listObjects was unreachable over HTTP by every
	// spelling. `+` requires at least one character after /objects/, which leaves
	// the bare collection to the route above it.
	//
	// The published document does not move for this: fiber keys the capture "+1"
	// where it keyed "*1", and the In binds that, but the segment's DOCUMENT name
	// is positional — {wildcard1} either way.
	g.Get("/buckets/:bucket/objects/+", fare.Paid(s, o.presignDownload))
	g.Delete("/buckets/:bucket/objects/+", fare.Paid(s, o.deleteObject))

	// The BYTES, at their own address. Raw rather than typed because a body is
	// not a struct: zip's typed pair carries a value in and a value out, and an
	// object's contents are neither. fare.Bytes is the same admission, the same
	// org, and the same debit the typed ops open with — see blob.go.
	g.Raw(http.MethodPut, blobPath, fare.Bytes(s, o.putBlob))
	g.Raw(http.MethodGet, blobPath, fare.Bytes(s, o.getBlob))

	cloud.Plane().Post("/s3/upload-form", o.uploadForm,
		zip.WithOperationID(client.S3UploadForm),
		zip.WithSummary("Sign a POST form for one object in a bucket of the caller's org"))

	if !s.State.admin.Configured() {
		s.Log.Warn("s3 subsystem mounted fail-closed: S3_ADMIN_ACCESS_KEY/SECRET_KEY not set (all ops 503 until provisioned)")
		return nil
	}
	s.Log.Info("s3 subsystem mounted",
		"prefix", "/v1/s3",
		"presign", s.State.admin.PresignConfigured(),
		"brand", cloud.Brand(),
		"env", cloud.Env(),
	)
	return nil
}

// Ready and Fee are the two facts [fare.Paid] asks this surface about itself.
// A not-Configured() admin means no credentials are present, so the subsystem
// mounts health/config only and every operation fails closed 503; a nil Bill makes
// the money leg a no-op.

// Ready is nil when the object store is configured, and the honest refusal
// otherwise. It is what makes the whole route set answer 503 under its own name
// rather than falling through to a different subsystem's 404.
func (st state) Ready() error {
	if !st.admin.Configured() {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage is not configured")
	}
	return nil
}

// Fee is what one object-storage operation costs, from operator config. Read per
// call rather than once at mount, so the knob takes effect without a restart.
func (st state) Fee() (string, int64) { return "op", cloud.FeeCents(opFeeEnvPrefix, "op", 0) }

// ── bucket name mapping (tenant ↔ physical) ─────────────────────────────────

// physicalBucket maps a caller's FRIENDLY bucket name to its real S3 bucket name,
// namespaced to the caller's org. This is the SAME derivation
// provisioning.BucketName uses (bucketName(physicalName(org,name)) — org-hash
// prefixed AND '_'→'-' folded to a DNS-safe S3 name), so a bucket provisioned via
// POST /v1/s3 is browsable here and a bucket created here is a valid S3 name.
// Enroll, when set, gives an org whatever standing it needs in the store before
// its first bucket exists.
//
// A hook rather than a call, because what it does belongs to the process that
// RUNS the store (plugin/s3, which holds the store's IAM) and this package is
// only the route surface in front of it. Unset — a deployment whose store lives
// somewhere else — a bucket is created exactly as before.
var Enroll func(ctx context.Context, org string) error

func physicalBucket(org, friendly string) string { return object.BucketName(org, friendly) }

// orgPrefix is the S3-bucket-name prefix that ALL of a caller's buckets share
// ("o"<orgHash>-). List filters to it and strips it to recover friendly names.
// Derived through provisioning.BucketPrefix so it matches the real bucket names
// exactly (including the '_'→'-' fold of the org-hash separator).
func orgPrefix(org string) string { return object.BucketPrefix(org) }

// friendlyBucket recovers the friendly name from a physical bucket owned by org,
// or ("",false) when the bucket is NOT in the caller's namespace (so listing
// skips other tenants' buckets — an existence-oracle guard). The recovered name
// is RE-VALIDATED against the friendly-name shape (RED LOW defense-in-depth): a
// bucket that carries the org prefix but a non-conforming name — one that could
// only exist via an out-of-band route (a manual `mc mb`, a future admin tool),
// never through createBucket — is treated as not-owned rather than echoed raw to
// the UI. So listBuckets always returns names a subsequent request can address.
func friendlyBucket(org, physical string) (string, bool) {
	pfx := orgPrefix(org)
	if !strings.HasPrefix(physical, pfx) {
		return "", false
	}
	name := strings.TrimPrefix(physical, pfx)
	if !object.Friendly(name) {
		return "", false
	}
	return name, true
}

// ── buckets ─────────────────────────────────────────────────────────────────

type bucketItem struct {
	Name      string `json:"name"`      // friendly name
	CreatedAt int64  `json:"createdAt"` // unix seconds
}

// ── objects ─────────────────────────────────────────────────────────────────

type objectItem struct {
	Key          string `json:"key"`          // key RELATIVE to the requested prefix
	IsDir        bool   `json:"isDir"`        // true for a folder (common prefix)
	Size         int64  `json:"size"`         // bytes (0 for a folder)
	LastModified int64  `json:"lastModified"` // unix seconds (0 for a folder)
	// ETag is the store's entity tag for the bytes currently at this key, with the
	// quotes the store wraps it in stripped. It is an opaque VERSION and not a
	// checksum to verify against: a single-part upload's tag happens to be the MD5
	// of the content and a multipart upload's is not, and nothing here says which
	// this was. Compare two reads of one key to learn whether the object changed;
	// absent for a folder entry, and for an object the store reports none for.
	ETag string `json:"etag,omitempty"`
}

type presignResponse struct {
	URL    string `json:"url"`    // presigned URL the browser follows directly
	Method string `json:"method"` // "PUT" (upload) or "GET" (download)
	// Key is the object key the URL was signed for, relative to the bucket root
	// and path-cleaned — so it is what the store will actually read or write, which
	// is not always the string the caller sent. The signature covers this one bucket
	// and this one key: a URL minted here reaches nothing else.
	Key    string `json:"key"`
	Expiry int64  `json:"expiresIn"` // seconds until the URL expires
}

// ── validation + helpers ────────────────────────────────────────────────────

// friendlyParam validates a bucket path param against the friendly-name shape.
// A malformed param is rejected 400 before any physical name is derived. The
// name is validated AS-IS (not lowercased): the friendly name in a URL must
// match exactly what listBuckets returned, and object.Friendly already requires
// lowercase — silently coercing "Photos"→"photos" would let a client address a
// bucket by a name the listing never showed, so an out-of-shape param is a 400.
func friendlyParam(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if !object.Friendly(name) {
		return "", false
	}
	return name, true
}

// remainder is the trailing path of an /objects/+ address as an object key: the
// greedy capture DECODED, then with a leading separator taken off, so a doubled
// separator in the URL (…/objects//a.txt) addresses the same key one separator
// does. The trim runs before cleanKey and NOT inside it, because a leading
// separator reaching cleanKey by any other route — an upload naming "/a.txt" in
// its body — is a caller writing an absolute key, which is a 400.
//
// It is applied to the BOUND FIELD rather than read off the request, so a caller
// that addresses the operation by name is normalized by the same rule as one that
// addresses it by URL.
//
// IT DOES NOT DECODE, BECAUSE THE ROUTER ALREADY DID. Measured on the router this
// app mounts on: "a%25b.txt" arrives as "a%b.txt", "a%20b.txt" as "a b.txt" and
// "a%2Fb.txt" as "a/b.txt". So every client that percent-encodes a path parameter
// (Go url.PathEscape, Python quote(safe=""), JS encodeURIComponent all render
// "2019/summer/a.jpg" as "2019%2Fsummer%2Fa.jpg") is already decoded once by the
// time the field is bound, and the same object still has one key.
//
// This function used to decode a second time, from a measurement that has since
// stopped being true, and a second decode is not a no-op on the two spellings that
// matter. A literal percent — "a%25b.txt", bound as "a%b.txt" — failed to unescape
// and answered 400, so no key containing a percent was addressable at all. And
// "a%2520b.txt", the encoding of the key "a%20b.txt", bound as "a%20b.txt" and
// decoded again to "a b.txt": a different object, silently.
//
// ok stays in the signature because the caller reads it, and is now always true:
// there is nothing left here that can fail. A malformed escape never reaches this
// point — the router refuses it while parsing the path.
func remainder(raw string) (string, bool) {
	return strings.TrimPrefix(strings.TrimSpace(raw), "/"), true
}

// cleanKey normalizes an object key and refuses any traversal or unsafe byte
// (object.Key, the one rule every reader of a tenant's keys applies).
func cleanKey(raw string) (string, bool) { return object.Key(raw) }

// cleanPrefix normalizes a listing prefix. A prefix MAY be empty (bucket root)
// and MAY end with "/" (a folder). It must not be absolute or contain traversal.
// An unsafe prefix is coerced to "" (root) rather than erroring — a listing of a
// bad prefix simply lists the root, never another location.
func cleanPrefix(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return ""
	}
	return p
}

// modTime returns unix seconds, or 0 for the zero time (a folder placeholder).
func modTime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
