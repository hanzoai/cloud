package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"

	model "github.com/hanzoai/iam/pkg/model"
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

// TestListProjects_NilIAMStore_ServesEmpty200 reproduces the live console-init 500
// and pins the fix. With the REAL iamProjects store and a co-resident IAM store that
// is NOT initialized in-process (clients/iam.DB() == nil — exactly the deployed
// condition before "iam" is enabled), List returns the iamStore 503 → the
// listProjects handler used to re-stamp it as a 500 that broke console.hanzo.ai
// dashboard init. The dashboard's first authenticated read must instead degrade to an
// empty project set (a new org genuinely has zero).
func TestListProjects_NilIAMStore_ServesEmpty200(t *testing.T) {
	app, s := mountSvcK8s(t, &k8sClient{initErr: "no cluster (test)", limits: testLimits()})
	s.State.projects = iamProjects{} // real store; the in-process IAM db is nil here

	code, body := do(t, app, http.MethodGet, "/v1/platform/projects", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("authed list over a nil IAM store: want 200, got %d (%s)", code, body)
	}
	var projects []projectView
	if err := json.Unmarshal(body, &projects); err != nil {
		t.Fatalf("list body must be a JSON array, got err=%v body=%s", err, body)
	}
	if len(projects) != 0 {
		t.Fatalf("nil IAM store: want empty project list, got %+v", projects)
	}
}

// errProjects is a ProjectStore whose List always fails — models any transient
// store outage sitting behind the dashboard's first read.
type errProjects struct{ err error }

func (e errProjects) List(context.Context, string) ([]*model.Project, error) { return nil, e.err }
func (errProjects) Get(context.Context, string, string) (*model.Project, error) {
	return nil, nil
}
func (errProjects) Create(context.Context, string, string, string, string) (*model.Project, error) {
	return nil, nil
}
func (errProjects) Delete(context.Context, string, string) (bool, error) { return false, nil }
func (errProjects) Exists(context.Context, string, string) (bool, error) { return false, nil }

// TestListProjects_StoreError_ServesEmpty200 pins the list-read policy: ANY store
// failure degrades to an empty set so console dashboard init never 500s. The real
// cause is logged for operators (never silently swallowed), but the user surface
// stays available.
func TestListProjects_StoreError_ServesEmpty200(t *testing.T) {
	app, s := mountSvcK8s(t, &k8sClient{initErr: "no cluster (test)", limits: testLimits()})
	s.State.projects = errProjects{err: errors.New("iam db offline")}

	code, body := do(t, app, http.MethodGet, "/v1/platform/projects", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("authed list over a failing store: want 200, got %d (%s)", code, body)
	}
	var projects []projectView
	if err := json.Unmarshal(body, &projects); err != nil {
		t.Fatalf("list body must be a JSON array, got err=%v body=%s", err, body)
	}
	if len(projects) != 0 {
		t.Fatalf("failing store: want empty project list, got %+v", projects)
	}
}
