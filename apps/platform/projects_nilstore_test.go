package platform

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"

	iamserver "github.com/hanzoai/iam/server"
)

// realDB opens a throwaway embedded IAM store so the guard tests can exercise the
// non-nil-db path (fn actually runs) — the SAME store type production sources from
// clients/iam.DB().
func realDB(t *testing.T) orm.DB {
	t.Helper()
	db, err := iamserver.OpenSQLite(filepath.Join(t.TempDir(), "iam.db"))
	if err != nil {
		t.Fatalf("open iam sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// When the co-resident IAM store is not initialized, clients/iam.DB() is nil. The
// iamStore guard must convert that into a clean 503 WITHOUT invoking fn — never a 500
// "runtime error: invalid memory address" that the live /v1/platform surface returned.
// Regression gate for the platform panic.
func TestIAMStore_NilDBBecomes503(t *testing.T) {
	called := false
	_, err := iamStore(nil, func(orm.DB) (int, error) { called = true; return 0, nil })
	if called {
		t.Fatal("iamStore must not invoke fn when the IAM store db is nil")
	}
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable {
		t.Fatalf("iamStore(nil) error = %v, want a 503 zip.HTTPError", err)
	}
}

// A nil-deref inside the guarded call (defense in depth) also becomes a clean 503.
func TestIAMStore_PanicBecomes503(t *testing.T) {
	_, err := iamStore(realDB(t), func(orm.DB) (int, error) {
		var engine *struct{ n int }
		return engine.n, nil // nil deref
	})
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable {
		t.Fatalf("iamStore panic error = %v, want a 503 zip.HTTPError", err)
	}
}

// The guard is transparent when the call succeeds — no false 503.
func TestIAMStore_PassthroughOnSuccess(t *testing.T) {
	got, err := iamStore(realDB(t), func(orm.DB) (string, error) { return "ok", nil })
	if err != nil || got != "ok" {
		t.Fatalf("iamStore passthrough = (%q,%v), want (ok,nil)", got, err)
	}
}

// A real (non-panic) error from the store passes through unchanged — the guard
// never masks a genuine error as a 503.
func TestIAMStore_RealErrorPassthrough(t *testing.T) {
	sentinel := errors.New("db offline")
	_, err := iamStore(realDB(t), func(orm.DB) (int, error) { return 0, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("iamStore masked a real error: got %v, want %v", err, sentinel)
	}
}
