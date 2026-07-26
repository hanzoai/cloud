package prefs

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/cek"
)

// testStore opens a real store on a temp file. It injects a fixed test master
// key so the suite runs on an ENCRYPTION-CAPABLE build too: cek fails closed
// without one, and skipping there would leave the isolation invariant below
// untested on exactly the build configuration production ships — a green suite
// that proves nothing. The key is a throwaway constant, never a real secret.
func testStore(t *testing.T) *Store {
	t.Helper()
	cek.SetMasterKey(bytes.Repeat([]byte{0x2a}, 32))
	s, err := openStore(filepath.Join(t.TempDir(), "prefs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// A user who has never saved anything reads as "not found", which the handler
// turns into an empty document rather than a 404 — a preferences read always
// succeeds, or the user menu cannot render.
func TestStore_MissingIsNotFound(t *testing.T) {
	s := testStore(t)
	if _, err := s.Get(context.Background(), "hanzo/z"); err != errNotFound {
		t.Fatalf("want errNotFound for an unwritten subject, got %v", err)
	}
}

// The whole point of merging INSIDE the transaction: two surfaces saving
// DIFFERENT keys both survive. Done as read-modify-write across two calls, the
// second writer would overwrite the first's key with its own stale snapshot.
func TestStore_MergeIsAdditiveAcrossWrites(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const subject = "hanzo/z"

	if _, err := s.Merge(ctx, subject, map[string]any{"theme": "dark"}, 100); err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if _, err := s.Merge(ctx, subject, map[string]any{"density": "compact"}, 200); err != nil {
		t.Fatalf("second merge: %v", err)
	}

	got, err := s.Get(ctx, subject)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(got.Doc), &doc); err != nil {
		t.Fatalf("stored doc is not JSON: %v", err)
	}
	if doc["theme"] != "dark" {
		t.Fatalf("the first surface's key was lost: %s", got.Doc)
	}
	if doc["density"] != "compact" {
		t.Fatalf("the second surface's key was lost: %s", got.Doc)
	}
	if got.UpdatedAt != 200 {
		t.Fatalf("updatedAt not advanced: %d", got.UpdatedAt)
	}
}

// Two subjects are two documents. This is the isolation invariant the whole
// plane rests on: `hanzo/z` and `admin/z` are different people who happen to
// share a name, and neither may read or clobber the other.
func TestStore_SubjectsAreIsolated(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if _, err := s.Merge(ctx, "hanzo/z", map[string]any{"theme": "dark"}, 1); err != nil {
		t.Fatalf("merge hanzo/z: %v", err)
	}
	if _, err := s.Merge(ctx, "admin/z", map[string]any{"theme": "light"}, 1); err != nil {
		t.Fatalf("merge admin/z: %v", err)
	}

	a, err := s.Get(ctx, "hanzo/z")
	if err != nil {
		t.Fatalf("get hanzo/z: %v", err)
	}
	b, err := s.Get(ctx, "admin/z")
	if err != nil {
		t.Fatalf("get admin/z: %v", err)
	}
	if a.Doc == b.Doc {
		t.Fatalf("two subjects share one document: %s", a.Doc)
	}
	if !contains(a.Doc, "dark") || !contains(b.Doc, "light") {
		t.Fatalf("subject documents crossed over: %s / %s", a.Doc, b.Doc)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
