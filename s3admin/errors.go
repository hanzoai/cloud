// Copyright (c) 2026 Hanzo AI Inc.

package s3admin

// The two store error codes a caller maps to a clean status instead of a generic
// 502, read here because the S3 error vocabulary is this package's — the same
// argument that put the physical naming here. Two subsystems address one backend,
// and a second classifier is how one of them starts answering 502 for the absence
// the other answers 404 for.

import (
	"errors"

	s3 "github.com/hanzos3/go"
)

// NoSuchBucket reports whether err is the store saying the bucket is not there.
// A caller renders it as 404 under whatever the bucket means to it — a bucket to
// apps/s3, a space to apps/space.
func NoSuchBucket(err error) bool { return code(err) == "NoSuchBucket" }

// BucketNotEmpty reports whether err is the store refusing to delete a bucket
// that still holds objects. A caller renders it as 409: deleting an org's objects
// behind a single call is not a thing either surface does silently.
func BucketNotEmpty(err error) bool { return code(err) == "BucketNotEmpty" }

// code is the store's own error code, or "" for an error that is not one.
func code(err error) string {
	if resp, ok := errors.AsType[s3.ErrorResponse](err); ok {
		return resp.Code
	}
	return ""
}
