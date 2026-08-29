package s3

// typed.go is this surface's typed half — one registry entry per operation,
// which is what the OpenAPI operation's schema, the MCP tool, the CLI command and
// every generated SDK method are all projected from. All eight ops are here, and
// untypedByDesign (typed_wire_test.go) is empty.
//
// IT REACHED EIGHT BY RE-READING FOUR RECORDED REFUSALS rather than inheriting
// them. Each named a capability zip did not have, and each of the four has it now:
//
//   - THE MONEY WIRE. The refusal said a balance denial must be written IN BAND
//     because a typed op's only refusal is a returned error, which zip renders
//     flat. cloud.Denied carries the fleet's NESTED {"error":{"code","message"}}
//     off a returned error and serve.go installs DenyEnvelope app-wide, so the
//     capability was already there. It never even applied: the balance is asked
//     in paid, which runs before the op is entered, so the denial does not pass
//     through a typed handler at all.
//   - TWO STATUSES, ONE OBJECT. zip v1.31.0 made WithStatus variadic and added
//     StatusCoder, so an op declares the set and the ANSWER says which one it is.
//     health is that: one shape, 200 or 503, both declared.
//   - THE ADDRESS. zip's Template spells a greedy segment {wildcardN}, the name
//     cloud's router reading already gave it, so openapi.Fold accepts an op there
//     instead of refusing the whole document.
//   - THE ARGUMENT. A greedy segment is now a DECLARED path parameter — zip reads
//     every matched segment, not the ":name" ones alone. Without that the address
//     templated {wildcard1} and declared nothing under it, and openapi.Fields,
//     which builds the fleet's GraphQL field out of those parameters, produced a
//     field with no argument for the object: the download answered 200 with a URL
//     signed for an object named "{wildcard1}" and the delete answered 204 having
//     removed nothing the caller named.

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/fare"
	"github.com/hanzoai/cloud/apps/s3admin"
	s3 "github.com/hanzos3/go"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and off every In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops is the typed-op receiver. A TypedHandler takes no service parameter, and a
// bound METHOD is the only form cmd/zipdoc can lift prose from — a closure
// returned by a factory is a call expression with no doc comment to read.
type ops struct{ s *cloud.Service[state] }

// client is the admin S3 client, or the honest 503 every op answers without one.
func (o ops) client() (*s3.Client, error) {
	cli, err := o.s.State.admin.Client()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	return cli, nil
}

// ── health ──────────────────────────────────────────────────────────────────

// s3Health is the probe's ONE shape, answered under both of its statuses.
//
// QUALIFIED, because a typed op's Go type name IS its schema name across the whole
// fleet and openapi.Compose refuses one name meaning two things. "healthReport" is
// already apps/event's, with a different shape — and it was free until this file
// typed the probe, which is what entering the namespace costs. The unpublished
// name is the one that yields.
type s3Health struct {
	// Service names the subsystem this probe is for. Always "s3".
	Service string `json:"service"`
	// Status is "ok" when the store is reachable in principle, "degraded" when it
	// is not. It is the field to read; the HTTP status carries the same fact for a
	// caller that only looks at the code.
	Status string `json:"status"`
	// Ready is whether this deployment can serve object operations at all: true
	// only when admin credentials are configured.
	Ready bool `json:"ready"`
	// Presign is whether presigned upload and download URLs can be minted, which
	// needs a PUBLIC endpoint on top of the credentials. False does not make the
	// surface degraded — listing and creating still work.
	//
	// NOT omitempty. On the ready path the untyped probe wrote this key
	// unconditionally, and omitting a false one would turn what a
	// healthy-but-presignless deployment reports from "presign: false" into
	// silence — two different facts. The degraded body now carries it too, which is
	// the one delta of typing this: the probe answers ONE shape under both of its
	// statuses, which is what makes a single declared Out honest.
	Presign bool `json:"presign"`
	// Error is why the probe is degraded, in plain words. Absent when it is not.
	Error string `json:"error,omitempty"`
}

// StatusCode makes the ANSWER say which of the declared statuses it is: a
// degraded probe is 503 so an orchestrator reading only the code is told the
// truth, and the body carries the same fact for one that reads further.
func (h *s3Health) StatusCode() int {
	if h.Status != "ok" {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

// Health reports whether this deployment can serve object storage.
//
// It is a REAL probe rather than a constant: 200 when admin credentials are
// present, so the store is reachable in principle, and 503 with the reason when
// they are not. It is deliberately NOT gated — liveness has to be probe-able
// without a token — so it is the one operation here that names no bucket and
// bills nothing.
func (o ops) health(_ context.Context, _ *noInput) (*s3Health, error) {
	r := &s3Health{Service: "s3", Status: "ok"}
	if !o.s.State.admin.Configured() {
		r.Status, r.Ready = "degraded", false
		r.Error = "S3_ADMIN credentials not configured"
		return r, nil
	}
	r.Ready = true
	r.Presign = o.s.State.admin.PresignConfigured()
	return r, nil
}

// noInput is the In of an op that takes nothing off the wire. Its whole input is
// the caller's validated principal, or in health's case nothing at all.
type noInput struct{}

// ── buckets ─────────────────────────────────────────────────────────────────

// bucketList is the caller's own buckets. Never another tenant's: a bucket is
// physically named under an org prefix, and one that does not carry the caller's
// is not merely filtered out of this list — it is invisible to every op here.
type bucketList struct {
	// Buckets are the caller org's buckets, friendly names, oldest first as the
	// store returns them.
	Buckets []bucketItem `json:"buckets"`
	// Total is how many buckets this org has. It equals len(buckets): the listing
	// is not paged, because an org's bucket count is small by construction.
	Total int `json:"total"`
}

// ListBuckets lists the caller org's own buckets.
//
// Only the caller's: every bucket is physically named under a per-org prefix and
// the listing strips that prefix, so a tenant sees friendly names and another
// tenant's buckets are not in the answer at all. Another org's bucket is not
// refused but INVISIBLE, so this cannot be used to learn that a name is taken
// elsewhere.
//
// Billed per call: the balance is checked BEFORE anything is touched, so an
// unfunded org is refused with nothing done, and the debit lands only once the
// work has succeeded.
func (o ops) listBuckets(ctx context.Context, _ *noInput) (*bucketList, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	all, err := cli.ListBuckets(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "list buckets: %v", err)
	}
	out := make([]bucketItem, 0, len(all))
	for _, b := range all {
		name, ok := friendlyBucket(org, b.Name)
		if !ok {
			continue // another tenant's bucket — invisible
		}
		out = append(out, bucketItem{Name: name, CreatedAt: b.CreationDate.Unix()})
	}
	return &bucketList{Buckets: out, Total: len(out)}, nil
}

// bucketIn names a bucket to create.
type bucketIn struct {
	// Name is the bucket's friendly name, matching
	// ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$. It is validated AS GIVEN and never
	// lower-cased for you: a client that creates "Photos" and then lists "photos"
	// would be reading a bucket it did not make, so mixed case is a clean 400.
	Name string `json:"name" url:"-"`
}

// CreateBucket makes a new bucket for the caller's org and answers 201 with it.
//
// The physical name is derived from the caller's validated org, so a tenant can
// only ever create inside its own namespace and no request field can redirect
// that. A name already taken in the org is 409.
//
// Billed per call: the balance is checked BEFORE anything is touched, so an
// unfunded org is refused with nothing created, and the debit lands only once the
// bucket exists.
func (o ops) createBucket(ctx context.Context, in *bucketIn) (*bucketItem, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if !bucketNameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	physical := physicalBucket(org, name)
	exists, err := cli.BucketExists(ctx, physical)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "bucket check: %v", err)
	}
	if exists {
		return nil, zip.ErrConflict("bucket already exists")
	}
	if err := cli.MakeBucket(ctx, physical, s3.MakeBucketOptions{Region: o.s.State.admin.Region()}); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "create bucket: %v", err)
	}
	return &bucketItem{Name: name, CreatedAt: time.Now().Unix()}, nil
}

// bucketRef addresses one bucket. The name is the path segment: the URL is the
// addressing authority.
type bucketRef struct {
	// Bucket is the bucket's friendly name, from the path.
	Bucket string `json:"bucket"`
}

// DeleteBucket removes an EMPTY bucket and answers 204.
//
// A non-empty bucket is 409 rather than a cascade: deleting a tenant's objects
// behind a single bucket call is not a thing this surface will do silently. A
// bucket the caller's org does not own is the same 404 an unknown name gives.
//
// Billed per call: the balance is checked BEFORE anything is touched, so an
// unfunded org is refused with nothing deleted, and the debit lands only once the
// bucket is gone.
func (o ops) deleteBucket(ctx context.Context, in *bucketRef) (*struct{}, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	name, ok := friendlyParam(in.Bucket)
	if !ok {
		return nil, zip.ErrBadRequest("invalid bucket name")
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	if err := cli.RemoveBucket(ctx, physicalBucket(org, name)); err != nil {
		if s3admin.NoSuchBucket(err) {
			return nil, zip.ErrNotFound("bucket not found")
		}
		if s3admin.BucketNotEmpty(err) {
			return nil, zip.ErrConflict("bucket is not empty")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "delete bucket: %v", err)
	}
	return nil, nil
}

// ── objects ─────────────────────────────────────────────────────────────────

// listIn addresses one folder level of a bucket.
type listIn struct {
	// Bucket is the bucket to list, from the path.
	Bucket string `json:"bucket"`
	// Prefix scopes the listing to a sub-"folder". Keys come back RELATIVE to it,
	// so a UI renders a breadcrumb without trimming anything itself.
	Prefix string `json:"-" url:"prefix"`
	// Recursive lists every key flat under the prefix instead of one folder level.
	// It is compared to the literal "true": the folder view is the default and
	// only recursion is opt-in, so any other value lists one level.
	Recursive string `json:"-" url:"recursive"`
}

// objectList is one folder level of a bucket.
type objectList struct {
	// Bucket is the bucket that was listed, friendly name.
	Bucket string `json:"bucket"`
	// Prefix is the sub-folder the listing was scoped to, cleaned. Empty for the
	// bucket root.
	Prefix string `json:"prefix"`
	// Objects are the entries at this level, keys RELATIVE to Prefix.
	Objects []objectItem `json:"objects"`
	// Total is how many entries came back. The listing is BOUNDED, so a bucket
	// with more keys than the cap answers the cap and this says so — it is not a
	// count of what the bucket holds.
	Total int `json:"total"`
}

// ListObjects lists one folder level of a bucket.
//
// Folder-style by default: sub-prefixes come back as directory entries, which is
// the file-manager view. `?recursive=true` lists every key flat under the prefix
// instead. Keys are RELATIVE to `?prefix=`, and the listing is bounded so a huge
// bucket cannot exhaust memory — Total is what came back, not what the bucket
// holds.
//
// Billed per call: the balance is checked BEFORE anything is touched, so an
// unfunded org is refused with nothing read, and the debit lands only once the
// listing has succeeded.
func (o ops) listObjects(ctx context.Context, in *listIn) (*objectList, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	bname, ok := friendlyParam(in.Bucket)
	if !ok {
		return nil, zip.ErrBadRequest("invalid bucket name")
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	prefix := cleanPrefix(in.Prefix)
	out := make([]objectItem, 0, 64)
	opts := s3.ListObjectsOptions{Prefix: prefix, Recursive: in.Recursive == "true", MaxKeys: maxListKeys}
	for obj := range cli.ListObjects(ctx, physicalBucket(org, bname), opts) {
		if obj.Err != nil {
			if s3admin.NoSuchBucket(obj.Err) {
				return nil, zip.ErrNotFound("bucket not found")
			}
			return nil, zip.Errorf(http.StatusBadGateway, "list objects: %v", obj.Err)
		}
		rel := strings.TrimPrefix(obj.Key, prefix)
		if rel == "" {
			continue // the prefix "folder" placeholder itself
		}
		out = append(out, objectItem{
			Key:          rel,
			IsDir:        strings.HasSuffix(obj.Key, "/"),
			Size:         obj.Size,
			LastModified: modTime(obj.LastModified),
			ETag:         strings.Trim(obj.ETag, `"`),
		})
		if len(out) >= maxListKeys {
			break
		}
	}
	return &objectList{Bucket: bname, Prefix: prefix, Objects: out, Total: len(out)}, nil
}

// uploadIn names the object to mint an upload URL for.
type uploadIn struct {
	// Bucket is the bucket to upload into, from the path.
	Bucket string `json:"bucket" url:"-"`
	// Key is the object key relative to the bucket root. It is path-cleaned, so a
	// "../" cannot escape the bucket, and an empty or unclean key is 400.
	Key string `json:"key" url:"-"`
}

// PresignUpload mints a presigned PUT URL the caller uploads to DIRECTLY.
//
// The bytes never pass through this binary and the admin credential never leaves
// the server: the URL is signed against the PUBLIC host, scoped to exactly this
// bucket and key, and expires. A deployment with no public endpoint configured
// cannot mint one and answers 503 rather than a URL that will not work.
//
// Billed per call — for MINTING the URL, which is the work this operation does;
// the upload that follows it goes straight to the store and is not seen here. The
// balance is checked BEFORE anything is touched, so an unfunded org is refused
// with no URL issued.
func (o ops) presignUpload(ctx context.Context, in *uploadIn) (*presignResponse, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	bname, ok := friendlyParam(in.Bucket)
	if !ok {
		return nil, zip.ErrBadRequest("invalid bucket name")
	}
	key, ok := cleanKey(in.Key)
	if !ok {
		return nil, zip.ErrBadRequest("key is required and must be a clean object path")
	}
	if !o.s.State.admin.PresignConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "presigned upload is not available (no public endpoint configured)")
	}
	pub, err := o.s.State.admin.PublicClient()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	u, err := pub.PresignedPutObject(ctx, physicalBucket(org, bname), key, presignTTL)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "presign upload: %v", err)
	}
	return &presignResponse{
		URL: u.String(), Method: http.MethodPut, Key: key, Expiry: int64(presignTTL.Seconds()),
	}, nil
}

// objectRef addresses ONE object: a bucket by friendly name and a key that is the
// whole trailing path.
//
// Key rides the route's greedy capture, which fiber names "*1" — the first
// wildcard, numbered over wildcards rather than segments, so the named :bucket
// ahead of it does not shift the number. The two tags answer two questions and
// both are needed. `url:` says where the value comes from over HTTP, and it must
// be fiber's key or the segment binds nothing. `json:` keeps the field's ordinary
// name for a caller that addresses the operation BY NAME — MCP, the call plane
// and the CLI hand their arguments across as one JSON object with no path to read,
// so `json:"-"` would leave them no way to say which object they mean.
//
// The URL WINS over both other sources: zip binds body, then query, then path, so
// a query or body naming a different bucket or key is overwritten by the address
// that was matched. The operation therefore acts on what the URL named, which is
// also what admit checked and what the ledger is debited for.
type objectRef struct {
	// Bucket is the bucket's friendly name, from the path.
	Bucket string `json:"bucket"`
	// Key is the object's key within the bucket — everything after that bucket's
	// /objects/. It MAY contain "/", because a key is a path and this segment is
	// captured whole: "2019/summer/a.jpg" is one key, not three. It is
	// path-cleaned before use, so "../" reaches nothing outside the bucket, and a
	// key that is empty, absolute or a bare folder marker is refused 400.
	Key string `json:"key" url:"+1"` // "+1" is fiber's key for the route's `+` capture; see s3.go.
}

// PresignDownload mints a presigned GET URL the caller downloads from DIRECTLY.
//
// The bytes never pass through this binary and the admin credential never leaves
// the server: the URL is signed against the PUBLIC host, scoped to exactly this
// bucket and key, and expires. It carries a content disposition of attachment
// naming the object's file name, so a browser following it saves the object rather
// than rendering it in place. A deployment with no public endpoint configured
// cannot mint one and answers 503 rather than a URL that will not work.
//
// Billed per call — for MINTING the URL, which is the work this operation does;
// the download that follows it comes straight from the store and is not seen here.
// The balance is checked BEFORE anything is touched, so an unfunded org is refused
// with no URL issued.
func (o ops) presignDownload(ctx context.Context, in *objectRef) (*presignResponse, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	bname, ok := friendlyParam(in.Bucket)
	if !ok {
		return nil, zip.ErrBadRequest("invalid bucket name")
	}
	raw, ok := remainder(in.Key)
	if !ok {
		return nil, zip.ErrBadRequest("object key is not a decodable path")
	}
	key, ok := cleanKey(raw)
	if !ok {
		return nil, zip.ErrBadRequest("object key is required and must be a clean path")
	}
	if !o.s.State.admin.PresignConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "presigned download is not available (no public endpoint configured)")
	}
	pub, err := o.s.State.admin.PublicClient()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	params := url.Values{}
	params.Set("response-content-disposition", "attachment; filename=\""+path.Base(key)+"\"")
	u, err := pub.PresignedGetObject(ctx, physicalBucket(org, bname), key, presignTTL, params)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "presign download: %v", err)
	}
	return &presignResponse{
		URL: u.String(), Method: http.MethodGet, Key: key, Expiry: int64(presignTTL.Seconds()),
	}, nil
}

// DeleteObject removes one object and answers 204.
//
// It removes ONE object and never a prefix: a key that looks like a folder deletes
// the placeholder at that key, not the objects beneath it. The key is path-cleaned
// first, so the delete cannot reach outside the bucket it names, and a bucket the
// caller's org does not own is the same 404 an unknown name gives.
//
// Billed per call: the balance is checked BEFORE anything is touched, so an
// unfunded org is refused with nothing deleted, and the debit lands only once the
// object is gone.
func (o ops) deleteObject(ctx context.Context, in *objectRef) (*struct{}, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return nil, err
	}
	bname, ok := friendlyParam(in.Bucket)
	if !ok {
		return nil, zip.ErrBadRequest("invalid bucket name")
	}
	raw, ok := remainder(in.Key)
	if !ok {
		return nil, zip.ErrBadRequest("object key is not a decodable path")
	}
	key, ok := cleanKey(raw)
	if !ok {
		return nil, zip.ErrBadRequest("object key is required and must be a clean path")
	}
	cli, err := o.client()
	if err != nil {
		return nil, err
	}
	if err := cli.RemoveObject(ctx, physicalBucket(org, bname), key, s3.RemoveObjectOptions{}); err != nil {
		if s3admin.NoSuchBucket(err) {
			return nil, zip.ErrNotFound("bucket not found")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "delete object: %v", err)
	}
	return nil, nil
}
