package cloud

// orgdb_reader_test.go — the ship's second handle, and what happens when there is none.
//
// The corruption fix rests on one handle: a ship opens a read-only connection of its own
// and holds a read transaction on it while it copies the org file, so no checkpoint can
// move that file under the copy. Whether the handle can be opened at all is a property of
// the linked engine, read at runtime — so a link that changed, or a probe that could not
// run, silently takes the property away. These assert that it cannot go quietly.

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/internal/codec"
	"github.com/hanzoai/namespace"
	sqlitedrv "github.com/hanzoai/sqlite"
)

// The reader answers a handle or it says why not — never neither.
//
// (nil, nil) is the answer this used to give wherever the codec was not linked, and the
// ship read it as "no reader available": it copied the file with writers free to move it
// and acknowledged the write. The property was then a function of a cached probe that is
// also false when a temp directory cannot be made, with no way to tell from the outside.
func TestOrgReaderAnswersAHandleOrWhyNot(t *testing.T) {
	ns := MustOrgNamespace("acme", "")
	db, err := orgReader(ns, "widget", filepath.Join(t.TempDir(), "widget.db"))
	if db != nil {
		defer db.Close()
	}
	if db == nil && err == nil {
		t.Fatal("the reader answered no handle and no reason; a ship reads that as leave to copy the file unpinned")
	}
	if !sqlitedrv.CodecLinked() && !errors.Is(err, errEnvelope) {
		t.Fatalf("this build links no codec, so the reader owes the envelope as its reason: %v", err)
	}
}

// On the engine the image ships, the reader really does open a second handle. The test
// above holds on every build; this one is what says the shipped build takes the good
// branch, and under the codec lane it cannot stand down.
func TestOrgReaderOpensASecondHandleOnTheShippedEngine(t *testing.T) {
	codec.Require(t, "a ship pins its file with a second handle, which only a database the codec keeps live on disk can give")
	ns := MustOrgNamespace("acme", "")
	dir := t.TempDir()
	store, err := openOrgDB(ns, "widget", dir)
	if err != nil {
		t.Fatalf("open org db: %v", err)
	}
	defer store.Close()
	path, err := namespace.Path(dir, ns, "widget")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	db, err := orgReader(ns, "widget", path)
	if err != nil {
		t.Fatalf("the shipped engine gave no second handle: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatalf("the second handle does not answer: %v", err)
	}
}

// Held is the boot question, and it is a function of two facts rather than of the
// process it is asked in — so both answers are asserted here instead of one of them
// being whatever this build happens to be.
func TestHeldRefusesADeployedProcessThatCannotHoldItsFiles(t *testing.T) {
	for _, c := range []struct {
		deployed, linked, refuse bool
	}{
		{deployed: true, linked: true, refuse: false},   // production
		{deployed: true, linked: false, refuse: true},   // the link regression
		{deployed: false, linked: false, refuse: false}, // a laptop on the envelope
		{deployed: false, linked: true, refuse: false},  // a laptop on the shipped engine
	} {
		err := held(c.deployed, c.linked)
		if got := err != nil; got != c.refuse {
			t.Fatalf("held(deployed=%v, linked=%v) refused=%v, want %v (%v)", c.deployed, c.linked, got, c.refuse, err)
		}
		if c.refuse && !errors.Is(err, errEnvelope) {
			t.Fatalf("the refusal does not name why the file cannot be held: %v", err)
		}
	}
}
