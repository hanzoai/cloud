package s3

// multipart.go is the upload of a large file: S3 multipart, with every part
// presigned so the bytes go from the browser straight to the store, never
// through this binary.
//
//	POST   /v1/s3/buckets/:bucket/uploads                    {key, type}    start one
//	POST   /v1/s3/buckets/:bucket/uploads/:upload/parts      {key, parts}   presigned PUT URLs for some parts
//	GET    /v1/s3/buckets/:bucket/uploads/:upload?key=                       the parts already stored (resume)
//	POST   /v1/s3/buckets/:bucket/uploads/:upload/complete   {key}          assemble the object
//	DELETE /v1/s3/buckets/:bucket/uploads/:upload?key=                       abandon it
//
// A part is at most PartSize, which keeps one PUT under the edge's request-body
// ceiling however large the file; a file can be thousands of parts. An upload
// survives a dropped connection: its parts stay stored, the parts listing says
// which, and the client sends only the rest. Completing asks the STORE which
// parts it holds rather than trusting the client's ETags — the browser never has
// to read a response header to finish an upload.
//
// The bucket resolves to its physical name inside the caller's org, as every op
// here does, and an upload id is bound by the store to the bucket and key it was
// started on, so an id from another org's upload names nothing here.

import (
	"context"
	s3driver "github.com/hanzoai/cloud/internal/driver/s3"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/internal/fare"
	s3 "github.com/hanzos3/go"
	"github.com/zap-proto/zip"
)

// PartSize is the size of every part but the last: 16 MiB. The edge in front of
// the store refuses a request body over 100 MB, and a part the size of a few
// seconds of upload is what makes a resume cheap.
const PartSize = 16 << 20

// maxParts is the store's own bound on parts in one upload.
const maxParts = 10000

// partsPerMint bounds how many part URLs one call mints.
const partsPerMint = 64

// uploadStart is POST /v1/s3/buckets/{bucket}/uploads.
type uploadStart struct {
	// Bucket is the bucket to upload into, from the path.
	Bucket string `json:"bucket"`
	// Key is the object key the upload will become, relative to the bucket
	// root. It is path-cleaned, so a "../" cannot escape the bucket.
	Key string `json:"key" url:"-"`
	// Type is the object's media type, kept with it once it is assembled.
	Type string `json:"type,omitempty" url:"-"`
}

// uploadStarted is a multipart upload begun.
type uploadStarted struct {
	// Upload is the upload's id, which every later call names.
	Upload string `json:"upload"`
	// Key is the object key the upload is bound to.
	Key string `json:"key"`
	// PartSize is the size every part but the last must be, in bytes. The
	// number of parts is the file's size divided by it, rounded up.
	PartSize int64 `json:"partSize"`
}

// uploadParts is POST /v1/s3/buckets/{bucket}/uploads/{upload}/parts.
type uploadParts struct {
	// Bucket is the bucket, from the path.
	Bucket string `json:"bucket"`
	// Upload is the upload's id, from the path.
	Upload string `json:"upload"`
	// Key is the object key the upload was started on.
	Key string `json:"key" url:"-"`
	// Parts are the part numbers to mint URLs for, from 1. At most 64 a call.
	Parts []int `json:"parts" url:"-"`
}

// partURL is where one part goes.
type partURL struct {
	// Part is the part number.
	Part int `json:"part"`
	// URL is the presigned address to PUT the part's bytes to, directly.
	URL string `json:"url"`
}

// partURLs are the minted part addresses.
type partURLs struct {
	// URLs are one presigned PUT per part asked for.
	URLs []partURL `json:"urls"`
	// Expiry is how many seconds the URLs stay valid.
	Expiry int64 `json:"expiresIn"`
}

// uploadRef names one upload of one key.
type uploadRef struct {
	// Bucket is the bucket, from the path.
	Bucket string `json:"bucket"`
	// Upload is the upload's id, from the path.
	Upload string `json:"upload"`
	// Key is the object key the upload was started on. In the query for a read
	// or an abandon, in the body to complete.
	Key string `json:"key" url:"key"`
}

// storedPart is one part the store holds.
type storedPart struct {
	// Part is the part number.
	Part int `json:"part"`
	// Size is its length in bytes.
	Size int64 `json:"size"`
	// ETag is the store's tag for its bytes, quotes stripped.
	ETag string `json:"etag"`
}

// storedParts is what an upload holds so far.
type storedParts struct {
	// Upload is the upload's id.
	Upload string `json:"upload"`
	// Key is the object key it is bound to.
	Key string `json:"key"`
	// Parts are the parts stored, in order. A client resuming sends the others.
	Parts []storedPart `json:"parts"`
}

// uploadDone is an upload assembled into its object.
type uploadDone struct {
	// Key is the object's key.
	Key string `json:"key"`
	// ETag is the assembled object's tag, quotes stripped.
	ETag string `json:"etag"`
	// Size is the object's length in bytes: the sum of its parts.
	Size int64 `json:"size"`
	// Parts is how many parts it was assembled from.
	Parts int `json:"parts"`
}

// uploadGone is an upload abandoned.
type uploadGone struct {
	// Upload is the id of the upload abandoned; its stored parts are deleted.
	Upload string `json:"upload"`
}

// target resolves what every multipart op opens with: the org, the bucket and
// the key, refusing before the store is touched.
func (o ops) target(ctx context.Context, bucket, key string) (string, string, string, error) {
	org, err := fare.Org(ctx)
	if err != nil {
		return "", "", "", err
	}
	bname, ok := friendlyParam(bucket)
	if !ok {
		return "", "", "", zip.ErrBadRequest("invalid bucket name")
	}
	k, ok := cleanKey(key)
	if !ok {
		return "", "", "", zip.ErrBadRequest("key is required and must be a clean object path")
	}
	return org, bname, k, nil
}

func (o ops) core() (s3.Core, error) {
	cli, err := o.client()
	if err != nil {
		return s3.Core{}, err
	}
	return s3.Core{Client: cli}, nil
}

func uploadID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" || len(id) > 1024 || strings.ContainsAny(id, "/?#&\x00") {
		return "", zip.ErrBadRequest("invalid upload id")
	}
	return id, nil
}

// StartUpload begins a multipart upload of a large file into one of the caller's
// org buckets. Then mint presigned URLs for its parts with POST
// .../uploads/{upload}/parts, PUT each part's bytes to its URL — every part but
// the last exactly partSize bytes — and assemble the object with POST
// .../uploads/{upload}/complete. A dropped connection loses nothing:
// GET .../uploads/{upload} lists the parts already stored, so only the rest are
// sent again. Use this for anything over a few megabytes; a small file takes the
// single presigned PUT of POST /v1/s3/buckets/{bucket}/objects.
//
// Example: {"key": "chat/backup.tar", "type": "application/x-tar"}
func (o ops) startUpload(ctx context.Context, in *uploadStart) (*uploadStarted, error) {
	org, bname, key, err := o.target(ctx, in.Bucket, in.Key)
	if err != nil {
		return nil, err
	}
	if !o.s.State.admin.PresignConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "multipart upload needs a public object store to sign against; use POST /v1/s3/buckets/{bucket}/objects")
	}
	c, err := o.core()
	if err != nil {
		return nil, err
	}
	typ := strings.TrimSpace(in.Type)
	if typ == "" {
		typ = "application/octet-stream"
	}
	id, err := c.NewMultipartUpload(ctx, physicalBucket(org, bname), key, s3.PutObjectOptions{ContentType: typ})
	if err != nil {
		if s3driver.NoSuchBucket(err) {
			return nil, zip.ErrNotFound("bucket not found")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "start upload: %v", err)
	}
	return &uploadStarted{Upload: id, Key: key, PartSize: PartSize}, nil
}

// MintParts answers presigned PUT URLs for some parts of a multipart upload, at
// most 64 a call, each valid for minutes: mint them as the upload reaches them.
// PUT a part's bytes to its URL as they are — no headers are signed — and the
// store answers the part's ETag, which completing does not need.
func (o ops) mintParts(ctx context.Context, in *uploadParts) (*partURLs, error) {
	org, bname, key, err := o.target(ctx, in.Bucket, in.Key)
	if err != nil {
		return nil, err
	}
	id, err := uploadID(in.Upload)
	if err != nil {
		return nil, err
	}
	if len(in.Parts) == 0 || len(in.Parts) > partsPerMint {
		return nil, zip.ErrBadRequest("parts names 1 to 64 part numbers")
	}
	pub, err := o.s.State.admin.PublicClient()
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage unavailable")
	}
	out := &partURLs{URLs: make([]partURL, 0, len(in.Parts)), Expiry: int64(presignTTL.Seconds())}
	for _, n := range in.Parts {
		if n < 1 || n > maxParts {
			return nil, zip.ErrBadRequest("a part number is 1 to 10000")
		}
		u, err := pub.Presign(ctx, http.MethodPut, physicalBucket(org, bname), key, presignTTL,
			url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": {id}})
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "presign part: %v", err)
		}
		out.URLs = append(out.URLs, partURL{Part: n, URL: u.String()})
	}
	return out, nil
}

// listed reads every part an upload holds, in order.
func (o ops) listed(ctx context.Context, bucket, key, id string) ([]s3.ObjectPart, error) {
	c, err := o.core()
	if err != nil {
		return nil, err
	}
	var parts []s3.ObjectPart
	marker := 0
	for {
		res, err := c.ListObjectParts(ctx, bucket, key, id, marker, 1000)
		if err != nil {
			if s3driver.NoSuchUpload(err) {
				return nil, zip.ErrNotFound("no such upload")
			}
			return nil, zip.Errorf(http.StatusBadGateway, "list parts: %v", err)
		}
		parts = append(parts, res.ObjectParts...)
		if !res.IsTruncated || res.NextPartNumberMarker <= marker {
			break
		}
		marker = res.NextPartNumberMarker
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	return parts, nil
}

// UploadParts answers the parts a multipart upload already holds, in order: what
// a client resuming after a dropped connection reads to send only the rest.
func (o ops) uploadParts(ctx context.Context, in *uploadRef) (*storedParts, error) {
	org, bname, key, err := o.target(ctx, in.Bucket, in.Key)
	if err != nil {
		return nil, err
	}
	id, err := uploadID(in.Upload)
	if err != nil {
		return nil, err
	}
	parts, err := o.listed(ctx, physicalBucket(org, bname), key, id)
	if err != nil {
		return nil, err
	}
	out := &storedParts{Upload: id, Key: key, Parts: make([]storedPart, 0, len(parts))}
	for _, p := range parts {
		out.Parts = append(out.Parts, storedPart{Part: p.PartNumber, Size: p.Size, ETag: strings.Trim(p.ETag, `"`)})
	}
	return out, nil
}

// CompleteUpload assembles a multipart upload into its object from every part the
// store holds, in order. The parts must run 1, 2, 3 … with no gap; a gap is 409,
// naming the first part missing, and the upload stays open to send it.
func (o ops) completeUpload(ctx context.Context, in *uploadRef) (*uploadDone, error) {
	org, bname, key, err := o.target(ctx, in.Bucket, in.Key)
	if err != nil {
		return nil, err
	}
	id, err := uploadID(in.Upload)
	if err != nil {
		return nil, err
	}
	bucket := physicalBucket(org, bname)
	parts, err := o.listed(ctx, bucket, key, id)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, zip.Errorf(http.StatusConflict, "the upload holds no parts")
	}
	done := make([]s3.CompletePart, len(parts))
	var size int64
	for i, p := range parts {
		if p.PartNumber != i+1 {
			return nil, zip.Errorf(http.StatusConflict, "part %d is missing", i+1)
		}
		done[i] = s3.CompletePart{PartNumber: p.PartNumber, ETag: p.ETag}
		size += p.Size
	}
	c, err := o.core()
	if err != nil {
		return nil, err
	}
	if err := o.withinQuota(ctx, c.Client, org, bucket, size); err != nil {
		_ = c.AbortMultipartUpload(ctx, bucket, key, id)
		return nil, err
	}
	info, err := c.CompleteMultipartUpload(ctx, bucket, key, id, done, s3.PutObjectOptions{})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "complete upload: %v", err)
	}
	return &uploadDone{Key: key, ETag: strings.Trim(info.ETag, `"`), Size: size, Parts: len(parts)}, nil
}

// AbandonUpload aborts a multipart upload and deletes the parts it stored. The
// object it would have become is never created.
func (o ops) abandonUpload(ctx context.Context, in *uploadRef) (*uploadGone, error) {
	org, bname, key, err := o.target(ctx, in.Bucket, in.Key)
	if err != nil {
		return nil, err
	}
	id, err := uploadID(in.Upload)
	if err != nil {
		return nil, err
	}
	c, err := o.core()
	if err != nil {
		return nil, err
	}
	if err := c.AbortMultipartUpload(ctx, physicalBucket(org, bname), key, id); err != nil {
		if s3driver.NoSuchUpload(err) {
			return nil, zip.ErrNotFound("no such upload")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "abandon upload: %v", err)
	}
	return &uploadGone{Upload: id}, nil
}
