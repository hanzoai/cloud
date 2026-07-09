// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// condstore.go is the concrete atomic-CAS object store the fence needs:
// minio-go's native If-Match / If-None-Match conditional PUT against the same
// S3-compatible SeaweedFS gateway clients/s3 already speaks to. It satisfies
// github.com/hanzoai/vfs/replica.ConditionalStore, the seam FencedStore (per-org
// DB ship) and CASFencer (per-org lease) compose over.
//
// WHY minio-go directly and not vfs's block layer: the vfs content-addressable
// backend exposes NO conditional-write primitive — "overwriting is allowed" is its
// contract — so the fence, which is a CONDITIONAL write, cannot ride that path. It
// addresses a dedicated object per shard directly against the gateway, exactly as
// clients/s3 does. No new dependency: github.com/minio/minio-go/v7 is already
// vendored. Build one from s3admin's admin client:
//
//	a := s3admin.New()
//	client, err := a.Client()          // admin-credentialed minio client
//	cs := &MinioConditionalStore{Client: client, Bucket: orgBucket}
//	dbFence := replica.NewFencedStore(cs)      // per-org DB fenced ship
//	fencer  := NewCASFencer(cs, membership)    // per-org monotone lease round

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hanzoai/vfs/replica"
	minio "github.com/minio/minio-go/v7"
)

// MinioConditionalStore is a replica.ConditionalStore over minio-go's native
// optimistic-locking extension (SetMatchETag / SetMatchETagExcept). The server —
// not this process — evaluates the precondition atomically, which is the property
// the fence's correctness depends on.
type MinioConditionalStore struct {
	Client *minio.Client
	Bucket string
}

// NewMinioConditionalStore builds a ConditionalStore over an already-constructed
// minio client and bucket. The caller owns the client lifecycle (credentials, TLS,
// endpoint) — this type adds only the conditional-write semantics.
func NewMinioConditionalStore(client *minio.Client, bucket string) *MinioConditionalStore {
	return &MinioConditionalStore{Client: client, Bucket: bucket}
}

// Get returns the object bytes and its ETag from ONE GET response (obj.Stat reads
// the header of the same request io.ReadAll drains), so the version is guaranteed
// consistent with the bytes. Absent key maps to replica.ErrNotFound.
func (s *MinioConditionalStore) Get(ctx context.Context, key string) ([]byte, string, error) {
	obj, err := s.Client.GetObject(ctx, s.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("condstore: get %s/%s: %w", s.Bucket, key, err)
	}
	defer obj.Close()
	info, err := obj.Stat()
	if err != nil {
		if isNoSuchKey(err) {
			return nil, "", replica.ErrNotFound
		}
		return nil, "", fmt.Errorf("condstore: stat %s/%s: %w", s.Bucket, key, err)
	}
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, "", fmt.Errorf("condstore: read %s/%s: %w", s.Bucket, key, err)
	}
	return data, info.ETag, nil
}

// PutIfVersion writes data at key iff the store's current ETag still equals
// expectVersion (SetMatchETag), or iff the object does not exist when
// expectVersion is "" (SetMatchETagExcept "*"). A failed precondition is mapped to
// replica.ErrConflict.
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
			return "", fmt.Errorf("condstore: %w: %v", replica.ErrConflict, err)
		}
		return "", fmt.Errorf("condstore: put %s/%s: %w", s.Bucket, key, err)
	}
	return info.ETag, nil
}

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

var _ replica.ConditionalStore = (*MinioConditionalStore)(nil)
