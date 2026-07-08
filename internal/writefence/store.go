package writefence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	minio "github.com/minio/minio-go/v7"
)

// MinioConditionalStore is a real ConditionalStore backed by minio-go's
// native optimistic-locking extension (PutObjectOptions.SetMatchETag /
// SetMatchETagExcept — an If-Match / If-None-Match conditional PUT against
// any S3-compatible endpoint). No new dependency: github.com/minio/minio-go/v7
// is already vendored at v7.0.100 (clients/s3 uses the same client against
// the SeaweedFS S3 gateway) — nothing here bumps go.mod.
//
// WIRING NOTE — this is a real, working atomic-CAS store, but it is NOT (yet)
// how internal/org's HIP-0107 per-org replication path talks to storage:
// that path goes through github.com/hanzoai/vfs's content-addressable block
// layer (see internal/org/vfsstore.go), whose Store/Backend interfaces
// expose NO conditional-write primitive at all — "Overwriting is allowed" is
// the verbatim upstream doc comment on backend.Backend.Put. Wiring this
// fence under that specific path would require hanzoai/vfs (an external
// module this repo does not own) to grow a conditional-Put capability at the
// backend layer — out of scope for a non-forking, surgical patch here.
//
// Until that lands upstream, MinioConditionalStore is the fence's real
// backing store for any (plugin, shard) mirror addressed directly against an
// S3-compatible endpoint (e.g. the same SeaweedFS gateway clients/s3 already
// speaks to via minio-go) rather than through vfs's block/backend
// abstraction. The recommended near-term wiring for a shard that needs the
// fence TODAY: point its mirror at a dedicated bucket/prefix through this
// store, independent of the vfs-block path other per-org data still uses.
type MinioConditionalStore struct {
	Client *minio.Client
	Bucket string
}

// NewMinioConditionalStore builds a ConditionalStore over an already
// constructed minio client and bucket. The caller owns the client's
// lifecycle (credentials, TLS, endpoint resolution) — this type adds only
// the conditional-write semantics on top.
func NewMinioConditionalStore(client *minio.Client, bucket string) *MinioConditionalStore {
	return &MinioConditionalStore{Client: client, Bucket: bucket}
}

// Get implements ConditionalStore.
func (s *MinioConditionalStore) Get(ctx context.Context, key string) ([]byte, string, error) {
	info, err := s.Client.StatObject(ctx, s.Bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNoSuchKey(err) {
			return nil, "", ErrNotFound
		}
		return nil, "", fmt.Errorf("writefence: stat %s/%s: %w", s.Bucket, key, err)
	}
	obj, err := s.Client.GetObject(ctx, s.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("writefence: get %s/%s: %w", s.Bucket, key, err)
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, "", fmt.Errorf("writefence: read %s/%s: %w", s.Bucket, key, err)
	}
	return data, info.ETag, nil
}

// PutIfVersion implements ConditionalStore using MinIO's If-Match (advance)
// / If-None-Match: * (create-only) conditional PUT. The server — not this
// process — evaluates the precondition atomically.
func (s *MinioConditionalStore) PutIfVersion(ctx context.Context, key string, data []byte, expectVersion string) (string, error) {
	opts := minio.PutObjectOptions{ContentType: "application/octet-stream"}
	if expectVersion == "" {
		opts.SetMatchETagExcept("*") // object must not exist yet.
	} else {
		opts.SetMatchETag(expectVersion)
	}
	info, err := s.Client.PutObject(ctx, s.Bucket, key, bytes.NewReader(data), int64(len(data)), opts)
	if err != nil {
		if isPreconditionFailed(err) {
			return "", fmt.Errorf("writefence: %w: %v", ErrConflict, err)
		}
		return "", fmt.Errorf("writefence: put %s/%s: %w", s.Bucket, key, err)
	}
	return info.ETag, nil
}

// isNoSuchKey / isPreconditionFailed classify the minio S3 error codes this
// store maps to ErrNotFound / ErrConflict — same errors.As(minio.ErrorResponse)
// pattern clients/s3/s3.go already uses for its own error classification.
func isNoSuchKey(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "NoSuchKey"
	}
	return false
}

func isPreconditionFailed(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "PreconditionFailed" || resp.StatusCode == 412
	}
	return false
}
