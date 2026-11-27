package train

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	s3driver "github.com/hanzoai/cloud/internal/driver/s3"
	"github.com/hanzoai/cloud/sdk/object"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/env"
	s3 "github.com/hanzos3/go"
	"github.com/hanzos3/go/pkg/encrypt"
)

const (
	// grantTTL is how long an upload grant is accepted: the largest artifact,
	// maxArtifact, in partBytes parts at about 80 Mbit/s. Every part's address is
	// signed when the grant is, so a shorter life fails a large upload part way.
	grantTTL    = 2 * time.Hour
	downloadTTL = 10 * time.Minute
	maxArtifact = 64 << 30
	// pendingCeiling bounds the bytes an org may hold granted and not confirmed:
	// two of the largest artifact. Such bytes are charged nothing until they are
	// confirmed, and the sweep deletes them once their grants lapse.
	pendingCeiling = 2 * maxArtifact
	// partBytes bounds one PUT: the edge in front of the object store refuses larger bodies.
	partBytes = 64 << 20
)

// Content types a grant binds: an output is a tar of its files (HIP-1333 §6), a
// dataset JSON Lines rows.
const (
	tarType   = "application/x-tar"
	jsonlType = "application/jsonl"
)

// upload is a grant to write one object: a presigned PUT that S3 accepts only
// with these headers, or, for an object over one part, a presigned PUT per part.
type upload struct {
	// Method is PUT.
	Method string `json:"method"`
	// URL is the presigned address of a single PUT; empty when Parts is given.
	URL string `json:"url,omitempty"`
	// Headers must be sent verbatim with the single PUT; the signature covers them.
	Headers map[string]string `json:"headers,omitempty"`
	// Parts are the object's consecutive byte ranges, each PUT to its own address.
	Parts []part `json:"parts,omitempty"`
	// Expires is when the grant stops being accepted, unix seconds.
	Expires int64 `json:"expires"`
	// id is the multipart upload the parts belong to.
	id string
}

// part is one byte range of a multipart upload.
type part struct {
	// Number orders the part from 1.
	Number int `json:"number"`
	// Offset is the part's first byte in the object.
	Offset int64 `json:"offset"`
	// Size is the part's byte count.
	Size int64 `json:"size"`
	// URL is the presigned address to PUT the range to.
	URL string `json:"url"`
	// Headers must be sent verbatim; the signature covers them.
	Headers map[string]string `json:"headers"`
}

var (
	errMissing  = errors.New("train: the object is not stored")
	errMismatch = errors.New("train: the stored object is not those bytes")
	errPlain    = errors.New("train: the stored object is not encrypted under the org's key")
)

// objects is where artifact bytes live: one object per org and sha256, each
// encrypted under its org's key (seal, the key's id).
type objects interface {
	// grant presigns the upload of exactly these bytes, as mime, sealed.
	grant(ctx context.Context, key, sum string, size int64, mime, seal string) (*upload, error)
	// verify completes an upload (id, when it came in parts) and reads it back:
	// those bytes, sealed under seal, or the object is deleted.
	verify(ctx context.Context, key, sum string, size int64, id, seal string) error
	// link presigns a download.
	link(ctx context.Context, key string) (string, error)
	// remove deletes an object and abandons the uploads still open for it.
	remove(ctx context.Context, key string, ids []string) error
	// abort abandons uploads still open for an object, and keeps the object.
	abort(ctx context.Context, key string, ids []string) error
	// serve copies an org's published capability to the serving store, the models
	// bucket's capabilities/<org>/<sum>, where the decision service fetches it.
	serve(ctx context.Context, key, org, sum string) error
	// write writes data at key, as mime, sealed under seal: bytes the cloud holds
	// itself (a decision's capture, a capability's tar), never an upload.
	write(ctx context.Context, key string, data []byte, mime, seal string) error
	// read reads the bytes at key; errMissing when there are none.
	read(ctx context.Context, key string) ([]byte, error)
}

// models is the platform's models bucket: the decision service reads Kai's
// weights and each published capability from it over the S3 API, under a key
// that reads its kai/ and capabilities/ prefixes and nothing else.
var models = object.BucketName("hanzo", "models")

// sse is the server-side encryption under an org's key, and the header a
// grant's signature binds it with.
func sse(seal string) (encrypt.ServerSide, error) {
	if seal == "" {
		return nil, errors.New("train: no key to seal the object under")
	}
	return encrypt.NewSSEKMS(seal, nil)
}

// bucket is Hanzo S3 through the deployment's admin credential.
//
// Train is the one app that keeps the S3 driver's raw API (internal/driver/s3 Admin)
// instead of sdk/object. Its objects are sealed under the org's key by the store
// itself (SSE-KMS headers a grant's signature binds), its uploads carry the
// x-amz-checksum-sha256 the store verifies, and its parts are presigned with those
// headers. Those are properties of the S3 protocol that local disk has no analogue
// for, so train refuses where no S3 is configured, as it did before.
type bucket struct {
	admin s3driver.Admin
	name  string
	once  sync.Once
	err   error
}

func newBucket() *bucket {
	return &bucket{admin: s3driver.New(), name: env.Or("TRAIN_BUCKET", "hanzo-train")}
}

func (b *bucket) ready(ctx context.Context) error {
	b.once.Do(func() {
		cli, err := b.admin.Client()
		if err != nil {
			b.err = err
			return
		}
		ok, err := cli.BucketExists(ctx, b.name)
		if err == nil && !ok {
			err = cli.MakeBucket(ctx, b.name, s3.MakeBucketOptions{Region: b.admin.Region()})
		}
		b.err = err
	})
	return b.err
}

// checksum is the x-amz-checksum-sha256 value of a hex digest.
func checksum(sum string) (string, error) {
	raw, err := hex.DecodeString(sum)
	if err != nil || len(raw) != sha256.Size {
		return "", fmt.Errorf("train: %q is not a sha256", sum)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func (b *bucket) grant(ctx context.Context, key, sum string, size int64, mime, seal string) (*upload, error) {
	if !b.admin.PresignConfigured() {
		return nil, fmt.Errorf("train: object store not configured for upload grants")
	}
	if err := b.ready(ctx); err != nil {
		return nil, err
	}
	cs, err := checksum(sum)
	if err != nil {
		return nil, err
	}
	enc, err := sse(seal)
	if err != nil {
		return nil, err
	}
	cli, err := b.admin.PublicClient()
	if err != nil {
		return nil, err
	}
	if size > partBytes {
		return b.parts(ctx, cli, key, size, mime, enc)
	}
	n := strconv.FormatInt(size, 10)
	h := http.Header{}
	h.Set("x-amz-checksum-sha256", cs)
	h.Set("Content-Type", mime)
	h.Set("Content-Length", n)
	enc.Marshal(h)
	u, err := cli.PresignHeader(ctx, http.MethodPut, b.name, key, grantTTL, nil, h)
	if err != nil {
		return nil, err
	}
	sent := map[string]string{}
	for k, v := range h {
		sent[strings.ToLower(k)] = v[0]
	}
	return &upload{
		Method:  http.MethodPut,
		URL:     u.String(),
		Headers: sent,
		Expires: time.Now().Add(grantTTL).Unix(),
	}, nil
}

// parts opens a multipart upload, sealed under the org's key from its start, and
// presigns a PUT for each partBytes range.
func (b *bucket) parts(ctx context.Context, public *s3.Client, key string, size int64, mime string, enc encrypt.ServerSide) (*upload, error) {
	cli, err := b.admin.Client()
	if err != nil {
		return nil, err
	}
	id, err := s3.Core{Client: cli}.NewMultipartUpload(ctx, b.name, key, s3.PutObjectOptions{ContentType: mime, ServerSideEncryption: enc})
	if err != nil {
		return nil, err
	}
	up := &upload{Method: http.MethodPut, Expires: time.Now().Add(grantTTL).Unix(), id: id}
	for i, off := 1, int64(0); off < size; i, off = i+1, off+partBytes {
		n := strconv.FormatInt(min(partBytes, size-off), 10)
		h := http.Header{}
		h.Set("Content-Length", n)
		q := url.Values{"partNumber": {strconv.Itoa(i)}, "uploadId": {id}}
		u, err := public.PresignHeader(ctx, http.MethodPut, b.name, key, grantTTL, q, h)
		if err != nil {
			return nil, err
		}
		up.Parts = append(up.Parts, part{Number: i, Offset: off, Size: min(partBytes, size-off), URL: u.String(), Headers: map[string]string{"content-length": n}})
	}
	return up, nil
}

// complete joins a multipart upload's parts in order into its object.
func (b *bucket) complete(ctx context.Context, cli *s3.Client, key, id string) error {
	core := s3.Core{Client: cli}
	var done []s3.CompletePart
	for marker := 0; ; {
		r, err := core.ListObjectParts(ctx, b.name, key, id, marker, 1000)
		if err != nil {
			if s3.ToErrorResponse(err).Code == "NoSuchUpload" {
				return nil
			}
			return err
		}
		for _, p := range r.ObjectParts {
			done = append(done, s3.CompletePart{PartNumber: p.PartNumber, ETag: p.ETag})
		}
		if !r.IsTruncated {
			break
		}
		marker = r.NextPartNumberMarker
	}
	if len(done) == 0 {
		return errMissing
	}
	_, err := core.CompleteMultipartUpload(ctx, b.name, key, id, done, s3.PutObjectOptions{})
	return err
}

func (b *bucket) verify(ctx context.Context, key, sum string, size int64, id, seal string) error {
	cli, err := b.admin.Client()
	if err != nil {
		return err
	}
	if id != "" {
		if err := b.complete(ctx, cli, key, id); err != nil {
			return err
		}
	}
	info, err := cli.StatObject(ctx, b.name, key, s3.StatObjectOptions{Checksum: true})
	if err != nil {
		if s3.ToErrorResponse(err).Code == "NoSuchKey" {
			return errMissing
		}
		return err
	}
	if !sealed(info.Metadata, seal) {
		if err := cli.RemoveObject(ctx, b.name, key, s3.RemoveObjectOptions{}); err != nil {
			return err
		}
		return errPlain
	}
	if info.Size != size {
		return b.drop(ctx, cli, key)
	}
	want, err := checksum(sum)
	if err != nil {
		return err
	}
	if info.ChecksumSHA256 != "" && !strings.Contains(info.ChecksumSHA256, "-") {
		if info.ChecksumSHA256 != want {
			return b.drop(ctx, cli, key)
		}
		return nil
	}
	obj, err := cli.GetObject(ctx, b.name, key, s3.GetObjectOptions{})
	if err != nil {
		return err
	}
	defer obj.Close()
	h := sha256.New()
	if _, err := io.Copy(h, obj); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != sum {
		return b.drop(ctx, cli, key)
	}
	return nil
}

// sealed reports whether an object's headers say it is encrypted under seal.
func sealed(h http.Header, seal string) bool {
	return seal != "" && h.Get(encrypt.SseGenericHeader) == "aws:kms" && h.Get(encrypt.SseKmsKeyID) == seal
}

// remove deletes an object, and aborts the multipart uploads still open for it so
// their parts go with it.
func (b *bucket) remove(ctx context.Context, key string, ids []string) error {
	if err := b.abort(ctx, key, ids); err != nil {
		return err
	}
	cli, err := b.admin.Client()
	if err != nil {
		return err
	}
	if err := cli.RemoveObject(ctx, b.name, key, s3.RemoveObjectOptions{}); err != nil && s3.ToErrorResponse(err).Code != "NoSuchKey" {
		return err
	}
	return nil
}

// abort abandons the multipart uploads still open for an object, so their parts
// go, and leaves the object as it is.
func (b *bucket) abort(ctx context.Context, key string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	cli, err := b.admin.Client()
	if err != nil {
		return err
	}
	core := s3.Core{Client: cli}
	for _, id := range ids {
		if err := core.AbortMultipartUpload(ctx, b.name, key, id); err != nil && s3.ToErrorResponse(err).Code != "NoSuchUpload" {
			return err
		}
	}
	return nil
}

// drop deletes an object that is not the bytes it was named for, and answers the
// mismatch.
func (b *bucket) drop(ctx context.Context, cli *s3.Client, key string) error {
	if err := cli.RemoveObject(ctx, b.name, key, s3.RemoveObjectOptions{}); err != nil {
		return err
	}
	return errMismatch
}

func (b *bucket) serve(ctx context.Context, key, org, sum string) error {
	cli, err := b.admin.Client()
	if err != nil {
		return err
	}
	ok, err := cli.BucketExists(ctx, models)
	if err == nil && !ok {
		err = cli.MakeBucket(ctx, models, s3.MakeBucketOptions{Region: b.admin.Region()})
	}
	if err != nil {
		return err
	}
	obj, err := cli.GetObject(ctx, b.name, key, s3.GetObjectOptions{})
	if err != nil {
		return err
	}
	defer obj.Close()
	info, err := obj.Stat()
	if err != nil {
		return err
	}
	_, err = cli.PutObject(ctx, models, "capabilities/"+org+"/"+sum, obj, info.Size, s3.PutObjectOptions{ContentType: tarType})
	return err
}

func (b *bucket) write(ctx context.Context, key string, data []byte, mime, seal string) error {
	if err := b.ready(ctx); err != nil {
		return err
	}
	enc, err := sse(seal)
	if err != nil {
		return err
	}
	cli, err := b.admin.Client()
	if err != nil {
		return err
	}
	_, err = cli.PutObject(ctx, b.name, key, bytes.NewReader(data), int64(len(data)),
		s3.PutObjectOptions{ContentType: mime, ServerSideEncryption: enc})
	return err
}

func (b *bucket) read(ctx context.Context, key string) ([]byte, error) {
	cli, err := b.admin.Client()
	if err != nil {
		return nil, err
	}
	obj, err := cli.GetObject(ctx, b.name, key, s3.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil && s3.ToErrorResponse(err).Code == "NoSuchKey" {
		return nil, errMissing
	}
	return data, err
}

func (b *bucket) link(ctx context.Context, key string) (string, error) {
	if !b.admin.PresignConfigured() {
		return "", fmt.Errorf("train: object store not configured for downloads")
	}
	cli, err := b.admin.PublicClient()
	if err != nil {
		return "", err
	}
	u, err := cli.PresignedGetObject(ctx, b.name, key, downloadTTL, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
