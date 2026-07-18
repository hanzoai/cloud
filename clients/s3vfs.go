package clients

// s3vfs is the REAL in-process VFSClient (deps.VFS) backing the team files plane
// on the shared SeaweedFS S3 gateway — the canonical, key-based, already-running
// object store. CTO decision (.97): back deps.VFS on S3, NOT the content-addressed
// hanzoai/vfs (its single-block PutBlock->BlockID API does not fit the key-based
// VFSClient seam; an adapter+crypto is needless complexity for small avatars).
//
// It reuses the ONE shared S3 construction (s3admin.Admin — the SAME endpoint +
// S3_ADMIN_* credentials clients/s3 and clients/projects use; no second client
// setup, DRY). Everything lands in ONE bucket keyed by the caller-opaque key the
// consumer supplies; tenant isolation is UPSTREAM in that key
// (clients/team/files.go builds team/blobs/<verified-org>/<ws>/<blobId> via
// seg()). S3 object keys are flat opaque strings (no "/" directory traversal), and
// every key component is seg()-sanitized with a server-verified org, so a caller
// can only ever address keys under its own team/blobs/<its-org>/ prefix — the key
// cannot escape the bucket or another tenant's prefix.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	minio "github.com/hanzoai/s3-go"

	"github.com/hanzoai/cloud/clients/s3admin"
	"github.com/hanzoai/cloud/types"
)

// TeamBlobBucket is the single bucket every team blob lands in. Per-tenant
// isolation is the org-scoped KEY PREFIX (team/blobs/<org>/…) the consumer builds,
// exactly like clients/s3's per-org physical naming — not a bucket-per-tenant.
const TeamBlobBucket = "team-blobs"

// s3vfs implements types.VFSClient over a minio client bound to the SeaweedFS S3
// admin endpoint.
type s3vfs struct {
	client  *minio.Client
	bucket  string
	region  string
	ensured int32 // atomic: 1 once the bucket is confirmed present (create-if-absent)
}

// Compile-time proof s3vfs satisfies the seam.
var _ types.VFSClient = (*s3vfs)(nil)

// NewS3VFS builds the S3-backed VFSClient from the shared admin config. The minio
// client is offline-constructed (no network here); the bucket is created if absent
// best-effort now AND lazily on the first op (so a boot-time S3 blip self-heals
// rather than permanently disabling files). Returns an error only if the client
// cannot be constructed (missing creds / invalid endpoint) — the caller then falls
// back to DisabledVFS (fail closed).
func NewS3VFS(admin s3admin.Admin) (types.VFSClient, error) {
	cl, err := admin.Client()
	if err != nil {
		return nil, fmt.Errorf("s3vfs: build client: %w", err)
	}
	v := &s3vfs{client: cl, bucket: TeamBlobBucket, region: admin.Region()}
	_ = v.ensure(context.Background()) // best-effort create-if-absent at construction
	return v, nil
}

// ensure creates the bucket if absent, exactly once (idempotent, retried on each
// op until it succeeds). A concurrent creator (another replica) is tolerated: a
// MakeBucket race is resolved by re-checking existence.
func (v *s3vfs) ensure(ctx context.Context) error {
	if atomic.LoadInt32(&v.ensured) == 1 {
		return nil
	}
	exists, err := v.client.BucketExists(ctx, v.bucket)
	if err != nil {
		return err
	}
	if !exists {
		if err := v.client.MakeBucket(ctx, v.bucket, minio.MakeBucketOptions{Region: v.region}); err != nil {
			// Another replica may have created it between the check and the make.
			if ex2, e2 := v.client.BucketExists(ctx, v.bucket); e2 != nil || !ex2 {
				return err
			}
		}
	}
	atomic.StoreInt32(&v.ensured, 1)
	return nil
}

// Put stores payload at key (PutObject). ContentType is fixed to
// application/octet-stream — the served type is re-derived from the bytes in the
// files download handler (allow-list), so the stored type is irrelevant.
func (v *s3vfs) Put(ctx context.Context, key string, payload []byte) error {
	if err := v.ensure(ctx); err != nil {
		return err
	}
	_, err := v.client.PutObject(ctx, v.bucket, key, bytes.NewReader(payload), int64(len(payload)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return err
}

// Get returns the object bytes at key. A MISSING key maps to types.ErrBlobNotFound
// (so the consumer answers 404 / idempotent-204); any OTHER S3 error is returned
// verbatim (so the consumer fails closed 502). minio's GetObject is lazy — the
// NoSuchKey surfaces on read — so mapS3Err runs over the read error too.
func (v *s3vfs) Get(ctx context.Context, key string) ([]byte, error) {
	if err := v.ensure(ctx); err != nil {
		return nil, err
	}
	obj, err := v.client.GetObject(ctx, v.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, mapS3Err(err)
	}
	defer func() { _ = obj.Close() }()
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, mapS3Err(err)
	}
	return data, nil
}

// Delete removes the object at key (RemoveObject). S3 DELETE is idempotent (a
// missing key is a no-op success); should a backend surface NoSuchKey, it maps to
// types.ErrBlobNotFound (which the consumer treats as an idempotent 204). Any other
// error is returned verbatim (consumer fails closed 502).
func (v *s3vfs) Delete(ctx context.Context, key string) error {
	if err := v.ensure(ctx); err != nil {
		return err
	}
	return mapS3Err(v.client.RemoveObject(ctx, v.bucket, key, minio.RemoveObjectOptions{}))
}

// mapS3Err is the load-bearing translation: a NoSuchKey S3 error becomes the
// types.ErrBlobNotFound sentinel (a genuine miss on a WORKING backend → 404 /
// idempotent-204), while nil stays nil and every OTHER error (connection refused,
// NoSuchBucket, auth, throttle, …) passes through unchanged so the consumer fails
// CLOSED with 502. This keeps clients/team/files.go's honest
// 404-vs-idempotent-204-vs-502 split intact — a missing blob never masquerades as
// an outage and an outage never masquerades as 404.
func mapS3Err(err error) error {
	if err == nil {
		return nil
	}
	// errors.As finds a minio.ErrorResponse whether it is returned directly (the
	// common case) or wrapped (%w) — more robust than a bare type assertion.
	var er minio.ErrorResponse
	if errors.As(err, &er) && er.Code == "NoSuchKey" {
		return types.ErrBlobNotFound
	}
	return err
}
