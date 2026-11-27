package library

// objects.go — where an item's bytes live: the organization's own bucket
// (`o<orghash>-library`, derived by `object.BucketName(org, "library")`).
//
// Each organization's Library items are stored in its own bucket under:
//
//	<orghash>/<ownerhash>/<item>/<name>     the bytes
//	<orghash>/<ownerhash>/<item>/still      the preview, when the writer sent one
//
// Hashes rather than names, so a key carries no identifier a log would leak, and
// fixed width, so no org's prefix can be another's prefix.

import (
	"bytes"
	"context"
	"github.com/hanzoai/cloud/sdk/object"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/env"
)

const (
	// signed is how long a URL the library hands out works.
	signed = time.Hour
	// maxInline is the largest item sent in one call; larger ones reserve.
	maxInline = 10 << 20
	// maxReserved is the largest item a reservation admits.
	maxReserved = 2 << 30
	// maxStill is the largest preview.
	maxStill = 2 << 20
)

// objects is the bucket. A var so a test stands a fake in; production never
// reassigns it.
type objects interface {
	put(ctx context.Context, key, mime string, data []byte) error
	// sign is a GET of key, rendered inline as a type a browser cannot run, or
	// as an attachment named name.
	sign(ctx context.Context, key, mime, name string, attach bool) (string, error)
	// upload is a PUT of key, of type mime.
	upload(ctx context.Context, key, mime string) (string, []client.Field, error)
	// size is the stored object's length.
	size(ctx context.Context, key string) (int64, error)
	remove(ctx context.Context, keys ...string) error
}

const legacyBucket = "hanzo-library"

var bucket objects = &store3{store: object.Default(), name: env.Or("LIBRARY_BUCKET", "")}

// store3 is the organization's library bucket (`o<orghash>-library`) in the
// process's object store.
type store3 struct {
	store object.Store
	name  string
	made  object.Made
}

// bucketFor resolves the physical bucket for key. When LIBRARY_BUCKET is not
// explicitly set, the leading 16-hex org hash on key selects the organization's
// own bucket `o<orghash>-library` (matching object.BucketName(org, "library")).
func (b *store3) bucketFor(key string) string {
	if b.name != "" {
		return b.name
	}
	if prefix, _, ok := strings.Cut(key, "/"); ok && len(prefix) == 16 && isHex(prefix) {
		return "o" + prefix + "-library"
	}
	return legacyBucket
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

func (b *store3) put(ctx context.Context, key, mime string, data []byte) error {
	target := b.bucketFor(key)
	if err := b.made.Ensure(ctx, b.store, target); err != nil {
		return err
	}
	_, err := b.store.Put(ctx, target, key, bytes.NewReader(data), int64(len(data)), object.Put{ContentType: mime})
	return err
}

func (b *store3) sign(ctx context.Context, key, kind, name string, attach bool) (string, error) {
	target := b.bucketFor(key)
	if err := b.made.Ensure(ctx, b.store, target); err != nil {
		return "", err
	}
	q := url.Values{}
	if attach {
		q.Set("response-content-disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		q.Set("response-content-type", "application/octet-stream")
	} else {
		q.Set("response-content-type", inline(kind))
		q.Set("response-content-disposition", "inline")
	}
	l, err := b.store.Share(ctx, object.Share{Bucket: target, Key: key, TTL: signed, Query: q})
	if err != nil {
		return "", err
	}
	return l.URL, nil
}

func (b *store3) upload(ctx context.Context, key, kind string) (string, []client.Field, error) {
	target := b.bucketFor(key)
	if err := b.made.Ensure(ctx, b.store, target); err != nil {
		return "", nil, err
	}
	// The type is signed, so the PUT must carry it. The size is not — a signed
	// length is a header many clients cannot set — and commit holds the stored
	// object to the size reserved instead.
	h := http.Header{}
	h.Set("Content-Type", kind)
	l, err := b.store.Share(ctx, object.Share{Method: http.MethodPut, Bucket: target, Key: key, TTL: signed, Header: h})
	if err != nil {
		return "", nil, err
	}
	return l.URL, []client.Field{{Name: "Content-Type", Value: kind}}, nil
}

func (b *store3) size(ctx context.Context, key string) (int64, error) {
	target := b.bucketFor(key)
	if err := b.made.Ensure(ctx, b.store, target); err != nil {
		return 0, err
	}
	st, err := b.store.Stat(ctx, target, key)
	if err != nil && b.name == "" && target != legacyBucket && object.Absent(err) {
		st, err = b.store.Stat(ctx, legacyBucket, key)
	}
	if err != nil {
		return 0, err
	}
	return st.Size, nil
}

func (b *store3) remove(ctx context.Context, keys ...string) error {
	for _, k := range keys {
		if k == "" {
			continue
		}
		target := b.bucketFor(k)
		if err := b.store.Remove(ctx, target, k); err != nil && !object.Absent(err) {
			return err
		}
		if b.name == "" && target != legacyBucket {
			_ = b.store.Remove(ctx, legacyBucket, k)
		}
	}
	return nil
}

// inline is the type an item is served as when opened in place. A type a
// browser renders without running anything is served as itself; markup and
// script are served as text, so an item opened from its signed URL can show its
// source and never execute it.
func inline(kind string) string {
	base, _, _ := strings.Cut(kind, ";")
	base = strings.ToLower(strings.TrimSpace(base))
	switch {
	case strings.HasPrefix(base, "image/") && base != "image/svg+xml",
		strings.HasPrefix(base, "video/"), strings.HasPrefix(base, "audio/"),
		strings.HasPrefix(base, "font/"), strings.HasPrefix(base, "model/"),
		base == "application/pdf", base == "application/json":
		return base
	}
	return "text/plain; charset=utf-8"
}

// objectKey is where an item's bytes are kept.
func objectKey(org, owner, id, name string) string {
	return object.OrgHash(org) + "/" + object.OrgHash(owner) + "/" + id + "/" + fileName(name)
}

// stillKey is where an item's preview is kept.
func stillKey(org, owner, id string) string {
	return object.OrgHash(org) + "/" + object.OrgHash(owner) + "/" + id + "/still"
}

// fileName is name as an object key's last segment: letters, digits, dot,
// dash and underscore, at most 100 bytes, never empty or a dot path.
func fileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 100 {
			break
		}
	}
	s := strings.Trim(b.String(), ".")
	if s == "" {
		return "file"
	}
	return s
}
