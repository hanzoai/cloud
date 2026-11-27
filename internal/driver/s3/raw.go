// Copyright (c) 2026 Hanzo AI Inc.

package s3

// The S3 error codes a caller of the RAW client (Admin.Client) maps to a clean status
// instead of a generic 502. Callers of sdk/object use its predicates instead; these
// are for the two apps that keep the raw client (apps/s3, which is the S3 surface,
// and apps/train, which needs S3's sealing and checksums).

import (
	"errors"

	hs3 "github.com/hanzos3/go"
)

// NoSuchBucket reports whether err is the store saying the bucket is not there.
// A caller renders it as 404 under whatever the bucket means to it — a bucket to
// apps/s3, a space to apps/space.
func NoSuchBucket(err error) bool { return code(err) == "NoSuchBucket" }

// NoSuchKey reports whether err is the store saying the object is not there —
// on a read, or on the HEAD a stat makes.
func NoSuchKey(err error) bool { return code(err) == "NoSuchKey" }

// NoSuchUpload reports whether err is the store saying a multipart upload id
// names no upload of that bucket and key — finished, abandoned, or never begun.
func NoSuchUpload(err error) bool { return code(err) == "NoSuchUpload" }

// BucketNotEmpty reports whether err is the store refusing to delete a bucket
// that still holds objects. A caller renders it as 409: deleting an org's objects
// behind a single call is not a thing either surface does silently.
func BucketNotEmpty(err error) bool { return code(err) == "BucketNotEmpty" }

// code is the store's own error code, or "" for an error that is not one.
func code(err error) string {
	if resp, ok := errors.AsType[hs3.ErrorResponse](err); ok {
		return resp.Code
	}
	return ""
}
