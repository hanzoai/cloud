package objects

import (
	"context"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"io"
	"strings"
	"time"
)

// Blobs is where pack bytes live: a flat keyspace of immutable objects the store reads by
// range. It is a bucket of the object store in production (Bucket) and a map in a test (blobtest).
type Blobs interface {
	// Put stores size bytes read from r at key, replacing what was there.
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	// Range reads n bytes of key from offset off. A range that runs past the end of the
	// object answers what is there; a key that is not there is ErrNoBlob.
	Range(ctx context.Context, key string, off, n int64) (io.ReadCloser, error)
	// Delete removes key; a key that is not there is not an error.
	Delete(ctx context.Context, key string) error
	// List calls fn for each key under prefix, in key order.
	List(ctx context.Context, prefix string, fn func(Blob) error) error
}

// Blob is one listed object.
type Blob struct {
	Key      string
	Size     int64
	Modified time.Time
}

// ErrNoBlob is a key the blob store does not hold.
var ErrNoBlob = errors.New("objects: no such blob")

// ErrNoBlobs is a store opened with nowhere to put pack bytes. It is refused at the first
// use rather than falling back to a file: a replica keeps nothing on its own disk.
var ErrNoBlobs = errors.New("objects: no object storage is configured")

// Bucket is Blobs over a bucket of the process's object store (sdk/object). The
// bucket is made on first use.
func Bucket(store object.Store, bucket string) Blobs {
	return &bucketBlobs{store: store, bucket: bucket}
}

type bucketBlobs struct {
	store  object.Store
	bucket string
	made   object.Made
}

func (b *bucketBlobs) ready(ctx context.Context) error {
	if err := b.made.Ensure(ctx, b.store, b.bucket); err != nil {
		if errors.Is(err, object.ErrNotConfigured) || errors.Is(err, object.ErrNoDriver) {
			return fmt.Errorf("%w: %v", ErrNoBlobs, err)
		}
		return fmt.Errorf("objects: bucket %s: %w", b.bucket, err)
	}
	return nil
}

func (b *bucketBlobs) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	if err := b.ready(ctx); err != nil {
		return err
	}
	_, err := b.store.Put(ctx, b.bucket, key, r, size, object.Put{
		ContentType: "application/octet-stream",
		// 64 MiB parts: a 1 GB pack is sixteen requests, and a part is the unit a
		// failed upload is repeated at.
		PartSize: 64 << 20,
	})
	if err != nil {
		return fmt.Errorf("objects: put %s: %w", key, err)
	}
	return nil
}

func (b *bucketBlobs) Range(ctx context.Context, key string, off, n int64) (io.ReadCloser, error) {
	if err := b.ready(ctx); err != nil {
		return nil, err
	}
	if n <= 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	body, err := b.store.Open(ctx, b.bucket, key, object.Get{Ranged: true, First: off, Last: off + n - 1})
	if err != nil {
		if !object.Absent(err) {
			// A range that starts at or past the end is an empty answer, not a fault.
			if st, e := b.store.Stat(ctx, b.bucket, key); e == nil && off >= st.Size {
				return io.NopCloser(strings.NewReader("")), nil
			}
		}
		return nil, mapErr(key, err)
	}
	return body, nil
}

func mapErr(key string, err error) error {
	if object.Absent(err) {
		return fmt.Errorf("%w: %s", ErrNoBlob, key)
	}
	return fmt.Errorf("objects: read %s: %w", key, err)
}

func (b *bucketBlobs) Delete(ctx context.Context, key string) error {
	if err := b.ready(ctx); err != nil {
		return err
	}
	if err := b.store.Remove(ctx, b.bucket, key); err != nil && !object.NoKey(err) {
		return fmt.Errorf("objects: delete %s: %w", key, err)
	}
	return nil
}

func (b *bucketBlobs) List(ctx context.Context, prefix string, fn func(Blob) error) error {
	if err := b.ready(ctx); err != nil {
		return err
	}
	for o, err := range b.store.List(ctx, b.bucket, object.List{Prefix: prefix}) {
		if err != nil {
			return fmt.Errorf("objects: list %s: %w", prefix, err)
		}
		if err := fn(Blob{Key: o.Key, Size: o.Size, Modified: o.Modified}); err != nil {
			return err
		}
	}
	return nil
}

// absent is the Blobs of a store opened with none.
type absent struct{}

func (absent) Put(context.Context, string, io.Reader, int64) error { return ErrNoBlobs }
func (absent) Range(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return nil, ErrNoBlobs
}
func (absent) Delete(context.Context, string) error                 { return ErrNoBlobs }
func (absent) List(context.Context, string, func(Blob) error) error { return ErrNoBlobs }
