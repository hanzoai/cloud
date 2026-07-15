package platform

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"
)

// When the co-resident IAM object store is not initialized, iamobj.GetProjects
// nil-derefs (ormer.Engine == nil). The iamStore guard must convert that panic
// into a clean 503 — never a 500 "runtime error: invalid memory address" that
// the live /v1/platform surface returned. Regression gate for the platform panic.
func TestIAMStore_NilStorePanicBecomes503(t *testing.T) {
	// Simulate a store call that nil-derefs (exactly what GetProjects does with a
	// nil ormer.Engine): dereference a nil pointer inside the guarded call.
	var engine *struct{ n int }
	_, err := iamStore(func() (int, error) { return engine.n, nil }) // nil deref
	if err == nil {
		t.Fatal("iamStore swallowed a nil-store panic — must surface a 503 error")
	}
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable {
		t.Fatalf("iamStore error = %v, want a 503 zip.HTTPError", err)
	}
}

// The guard is transparent when the call succeeds — no false 503.
func TestIAMStore_PassthroughOnSuccess(t *testing.T) {
	got, err := iamStore(func() (string, error) { return "ok", nil })
	if err != nil || got != "ok" {
		t.Fatalf("iamStore passthrough = (%q,%v), want (ok,nil)", got, err)
	}
}

// A real (non-panic) error from the store passes through unchanged — the guard
// never masks a genuine error as a 503.
func TestIAMStore_RealErrorPassthrough(t *testing.T) {
	sentinel := errors.New("db offline")
	_, err := iamStore(func() (int, error) { return 0, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("iamStore masked a real error: got %v, want %v", err, sentinel)
	}
}

var _ = context.Background
