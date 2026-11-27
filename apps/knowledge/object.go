package knowledge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"io"
	"net/http"

	"github.com/zap-proto/zip"
)

// object.go is how knowledge reads an org's object: its record for a register,
// its bytes for an ingest. The org's bucket is resolved to its physical name
// inside that org (object.BucketName, the derivation apps/s3 and the control
// plane use), so the bucket a caller names can only ever be one of their own.

// stored is what the store says about one object.
type stored struct {
	size int64
	typ  string
	etag string
}

// objectStore is the org's object store as knowledge reads it. A variable so a
// test reads from memory; production reads the process's object store (sdk/object), as apps/space and
// apps/projects do.
type objectStore interface {
	stat(ctx context.Context, org, bucket, key string) (stored, error)
	// read answers the whole object, refusing one larger than max.
	read(ctx context.Context, org, bucket, key string, max int64) ([]byte, error)
	// span answers at most n bytes of the object from offset at: none past its end.
	span(ctx context.Context, org, bucket, key string, at, n int64) ([]byte, error)
	// write stores data as the object at key: how an archive's files land.
	write(ctx context.Context, org, bucket, key, typ string, data []byte) error
}

var objects objectStore = storeObjects{object.Default()}

type storeObjects struct{ store object.Store }

// refused renders a store failure as the status the caller sees: an object or
// bucket the org does not hold is 404, a deployment with no store to open is 503,
// anything else 502.
func refused(err error) error {
	switch {
	case missing(err):
		return zip.ErrNotFound("no such object in that bucket")
	case errors.Is(err, object.ErrNotConfigured), errors.Is(err, object.ErrNoDriver):
		return zip.Errorf(http.StatusServiceUnavailable, "object storage is not configured")
	}
	return zip.Errorf(http.StatusBadGateway, "object storage unavailable")
}

// stat answers 404 for an object, or a bucket, the org does not hold.
func (a storeObjects) stat(ctx context.Context, org, bucket, key string) (stored, error) {
	info, err := a.store.Stat(ctx, object.BucketName(org, bucket), key)
	if err != nil {
		return stored{}, refused(err)
	}
	return stored{size: info.Size, typ: info.ContentType, etag: info.ETag}, nil
}

// read answers the object's bytes, refusing one larger than max rather than
// holding it.
func (a storeObjects) read(ctx context.Context, org, bucket, key string, max int64) ([]byte, error) {
	body, err := a.store.Open(ctx, object.BucketName(org, bucket), key, object.Get{})
	if err != nil {
		if missing(err) {
			return nil, errGone
		}
		return nil, fmt.Errorf("read object: %w", err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, fmt.Errorf("read object: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("object is larger than %d MB", max>>20)
	}
	return data, nil
}

// span answers n bytes from offset at, the read a text file is indexed by: one
// segment at a time, however long the file.
func (a storeObjects) span(ctx context.Context, org, bucket, key string, at, n int64) ([]byte, error) {
	body, err := a.store.Open(ctx, object.BucketName(org, bucket), key, object.Get{Ranged: true, First: at, Last: at + n - 1})
	if err != nil {
		if missing(err) {
			return nil, errGone
		}
		return nil, fmt.Errorf("read object: %w", err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, n))
	if err != nil {
		return nil, fmt.Errorf("read object: %w", err)
	}
	return data, nil
}

// write puts data at key in the org's bucket.
func (a storeObjects) write(ctx context.Context, org, bucket, key, typ string, data []byte) error {
	if typ == "" {
		typ = "application/octet-stream"
	}
	_, err := a.store.Put(ctx, object.BucketName(org, bucket), key, bytes.NewReader(data), int64(len(data)), object.Put{ContentType: typ})
	if err != nil {
		return fmt.Errorf("write object: %w", err)
	}
	return nil
}

// missing reports the store saying the object or its bucket is not there.
func missing(err error) bool { return object.Absent(err) }
