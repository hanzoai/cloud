package clients

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	s3 "github.com/hanzoai/s3-go"

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
		{"NoSuchKey", s3.ErrorResponse{Code: "NoSuchKey"}, true, false},
		{"NoSuchKey wrapped", fmt.Errorf("get: %w", s3.ErrorResponse{Code: "NoSuchKey"}), true, false},
		{"NoSuchBucket", s3.ErrorResponse{Code: "NoSuchBucket"}, false, false},
		{"AccessDenied", s3.ErrorResponse{Code: "AccessDenied"}, false, false},
		{"SlowDown throttle", s3.ErrorResponse{Code: "SlowDown"}, false, false},
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

// Construction must not wait on the object store. NewS3VFS runs on the BOOT path,
// before the process listens, and its bucket-ensure is a discarded-error
// optimisation — so a store that accepts the connection and then never answers
// must cost a bounded pause, not the binary.
//
// The listener here is the exact failure that took production down: it completes
// the TCP handshake and then reads forever without writing a byte, so every
// connect succeeds and every request hangs. Unbounded, this test does not fail —
// it never finishes, which is precisely what the pods did (no log line, liveness
// killed them, and a rollback did not help because the hang predates both
// versions' changes).
func TestNewS3VFSDoesNotBlockBootOnAHangingStore(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Accept and go silent. Never read to completion, never respond.
			defer c.Close()
		}
	}()

	t.Setenv("S3_ADMIN_ACCESS_KEY", "ak")
	t.Setenv("S3_ADMIN_SECRET_KEY", "sk")
	t.Setenv("S3_ADMIN_ENDPOINT", ln.Addr().String())
	t.Setenv("S3_SECURE", "false")

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		// A client is still returned: deps.VFS must never be nil (R-7), and the
		// first real op retries the ensure.
		v, err := NewS3VFS(s3admin.New())
		if err == nil && v == nil {
			t.Error("NewS3VFS returned (nil, nil) — deps.VFS must never be nil")
		}
		done <- time.Since(start)
	}()

	// Generous versus the 5s bound, decisive versus unbounded.
	select {
	case took := <-done:
		if took > 30*time.Second {
			t.Fatalf("construction took %v — the ensure is not bounded", took)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("NewS3VFS never returned against a hanging store — this is the boot hang that took prod down")
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
