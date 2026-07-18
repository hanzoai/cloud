package clients

import (
	"errors"
	"fmt"
	"testing"

	minio "github.com/hanzoai/s3-go"

	"github.com/hanzoai/cloud/clients/s3admin"
	"github.com/hanzoai/cloud/types"
)

// TestMapS3Err is the load-bearing translation Red flagged: a MISSING key
// (S3 NoSuchKey) must become types.ErrBlobNotFound (so files.go answers 404 /
// idempotent-204), nil stays nil, and EVERY other S3 error (NoSuchBucket, auth,
// throttle, connection-refused) passes through unchanged (so files.go fails CLOSED
// 502). This is table-tested directly — no live SeaweedFS required.
func TestMapS3Err(t *testing.T) {
	cases := []struct {
		name       string
		in         error
		wantNotFnd bool // expect types.ErrBlobNotFound
		wantNil    bool
	}{
		{"nil", nil, false, true},
		{"NoSuchKey", minio.ErrorResponse{Code: "NoSuchKey"}, true, false},
		{"NoSuchKey wrapped", fmt.Errorf("get: %w", minio.ErrorResponse{Code: "NoSuchKey"}), true, false},
		{"NoSuchBucket", minio.ErrorResponse{Code: "NoSuchBucket"}, false, false},
		{"AccessDenied", minio.ErrorResponse{Code: "AccessDenied"}, false, false},
		{"SlowDown throttle", minio.ErrorResponse{Code: "SlowDown"}, false, false},
		{"plain conn error", fmt.Errorf("dial tcp: connection refused"), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mapS3Err(c.in)
			switch {
			case c.wantNil:
				if got != nil {
					t.Fatalf("mapS3Err(nil) = %v, want nil", got)
				}
			case c.wantNotFnd:
				if !errors.Is(got, types.ErrBlobNotFound) {
					t.Fatalf("mapS3Err(%v) = %v, want ErrBlobNotFound", c.in, got)
				}
			default:
				if errors.Is(got, types.ErrBlobNotFound) {
					t.Fatalf("mapS3Err(%v) mapped a non-miss error to ErrBlobNotFound (would 404 an outage)", c.in)
				}
				if got == nil {
					t.Fatalf("mapS3Err(%v) = nil, want the error passed through", c.in)
				}
			}
		})
	}
}

// TestNewS3VFSRequiresCreds proves NewS3VFS fails (→ pickVFSClient degrades to the
// fail-closed DisabledVFS) when no S3 admin credentials are present — the R-7
// fail-closed posture is preserved. No network: admin.Client() errors on missing
// creds before any connection.
func TestNewS3VFSRequiresCreds(t *testing.T) {
	t.Setenv("S3_ADMIN_ACCESS_KEY", "")
	t.Setenv("S3_ADMIN_SECRET_KEY", "")
	if _, err := NewS3VFS(s3admin.New()); err == nil {
		t.Fatal("NewS3VFS with no creds must error (so pickVFSClient falls back to DisabledVFS)")
	}
}

// TestS3VFSIsVFSClient is the compile-time seam proof (also asserted by the
// package-level var in s3vfs.go) — the impl really satisfies the interface files.go
// consumes.
func TestS3VFSIsVFSClient(t *testing.T) {
	var _ types.VFSClient = (*s3vfs)(nil)
	if TeamBlobBucket != "team-blobs" {
		t.Fatalf("TeamBlobBucket = %q, want team-blobs", TeamBlobBucket)
	}
}
