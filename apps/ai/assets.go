// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package ai

// assets.go — where a file a model made is kept: the object store.
//
// ai copies each finished video clip here (hanzoai/ai controllers/video_vendor.go)
// and hands the caller a signed address into it. The store is the one admin
// object store every writer in this binary uses (sdk/object): local disk by
// default, Hanzo S3 where the deployment configures it, and the address is signed
// for a host a browser can follow.

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/object"
)

// mediaBucket is the bucket a model's files are kept in, one prefix an org.
const mediaBucket = "hanzo-media"

// mediaStore is the object store as ai's asset store (aiobject.Assets).
type mediaStore struct {
	store  object.Store
	bucket string
	made   object.Made
}

func newMediaStore() *mediaStore {
	return &mediaStore{store: object.Default(), bucket: env.Or("MEDIA_BUCKET", mediaBucket)}
}

// Put stores size bytes of r under key, making the bucket the first time it is
// written.
func (m *mediaStore) Put(ctx context.Context, key, contentType string, r io.Reader, size int64) error {
	if err := m.made.Ensure(ctx, m.store, m.bucket); err != nil {
		return fmt.Errorf("media bucket %s: %w", m.bucket, err)
	}
	_, err := m.store.Put(ctx, m.bucket, key, r, size, object.Put{ContentType: contentType})
	return err
}

// URL signs an address that serves key for ttl, on a host a browser can follow.
func (m *mediaStore) URL(ctx context.Context, key string, ttl time.Duration) (string, time.Time, error) {
	l, err := m.store.Share(ctx, object.Share{Bucket: m.bucket, Key: key, TTL: ttl})
	if err != nil {
		return "", time.Time{}, err
	}
	return l.URL, time.Now().Add(ttl), nil
}
