// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// The commerce module's own datastore is a seam store: every org's commerce.db, the
// system one included (the org records org.Resolve reads, the limit rows), is a schema
// on hanzo-sql when CLOUD_SQL_DSN is set, its file otherwise, and a file moves to the
// server on its first open. These tests hold that to the module as it is mounted here.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/cek"
	commercedb "github.com/hanzoai/commerce/db"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	"github.com/hanzoai/commerce/util/nscontext"
	"github.com/hanzoai/namespace"
	sqlitedrv "github.com/hanzoai/sqlite"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
)

// systemStore is the namespace of the org commerce keeps its global kinds in.
func systemStore(t *testing.T) namespace.Namespace {
	t.Helper()
	ns, err := namespace.OrgProject("system", "")
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

// commerceDir is commerce's data directory: where its files have always been.
func commerceDir() string { return filepath.Join(cloud.DataDir(), "commerce") }

func fileOf(t *testing.T, ns namespace.Namespace) string {
	t.Helper()
	path, err := namespace.Path(commerceDir(), ns, tenantSubsystem)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// resolved is the org the way commerce resolves it on every request, with the cache out
// of the way so the store is read.
func resolved(t *testing.T, name string) string {
	t.Helper()
	commerceorg.Invalidate(name)
	o, err := commerceorg.Resolve(context.Background(), name)
	if err != nil {
		t.Fatalf("resolve %s: %v", name, err)
	}
	return o.Id()
}

// limitOf is the sandbox limit an org stands at, and the level it came from.
func limitOf(t *testing.T, org string) (int64, string) {
	t.Helper()
	s, err := planeLimits(caller(org, false), &client.LimitsIn{})
	if err != nil {
		t.Fatalf("limits of %s: %v", org, err)
	}
	return sandboxes(t, s)
}

// Commerce's stores are where the seam keeps them: the org records and the limit rows
// are in the system store, which is a file on SQLite and a schema (and no file) on
// hanzo-sql; the org resolves to one record however often it is asked, and a limit set
// is the limit read.
func TestCommerceKeepsItsStoresWhereTheSeamDoes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bootRecord(t)
		const name = "seam-acme"
		id := resolved(t, name)
		if id == "" || resolved(t, name) != id {
			t.Fatalf("org %s resolved to %q and then to another record", name, id)
		}
		if _, err := planeLimitSet(staff(), &client.LimitSet{Key: client.LimitSandboxRunning, Scope: "org:" + name, Value: 321, Reason: "seam"}); err != nil {
			t.Fatalf("set: %v", err)
		}
		if n, src := limitOf(t, name); n != 321 || src != client.LimitOrg {
			t.Fatalf("the limit reads %d from %s, want 321 from the org's own row", n, src)
		}

		// A file is whole on disk once its store is closed.
		stopEmbedded(t)
		system := systemStore(t)
		switch {
		case locus.DSN() == "":
			if !exists(fileOf(t, system)) {
				t.Fatalf("commerce's system store is not %s", fileOf(t, system))
			}
		default:
			if exists(fileOf(t, system)) {
				t.Fatalf("commerce wrote a file, %s, beside the server", fileOf(t, system))
			}
			if on, err := locus.Exists(system, tenantSubsystem); err != nil || !on {
				t.Fatalf("commerce's system store is not on hanzo-sql (%v, %v)", on, err)
			}
		}
	})
}

// A deployment's commerce files are moved to the server as they are. The stores are
// written on files, as a deployment before the move wrote them, and commerce stopped;
// started again with the server named and the move on, every org's store is moved on its
// first open and reads as it did: the org records resolve to the same records, the limit
// is the limit, a merchant model of an org is the model. The file is left where it was.
func TestACommerceFileMovesToTheServerAsItIs(t *testing.T) {
	dsn := sqltest.Server(t)
	t.Setenv(locus.Env, "") // the first life is on files

	bootRecord(t)
	const name = "moved-acme"
	id := resolved(t, name)
	if _, err := planeLimitSet(staff(), &client.LimitSet{Key: client.LimitSandboxRunning, Scope: "org:" + name, Value: 654, Reason: "before the move"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	ctx := nscontext.WithNamespace(context.Background(), name)
	orgs := currentEmbedded().App().DB
	merchant, err := orgs.Org(name)
	if err != nil {
		t.Fatalf("the org's merchant store: %v", err)
	}
	type shelf struct {
		Label string `json:"label"`
		Count int    `json:"count"`
	}
	if _, err := merchant.Put(ctx, merchant.NewKey("Shelf", "one", 0, nil), &shelf{Label: "before the move", Count: 7}); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Commerce stops, and its files are whole on disk.
	stopEmbedded(t)
	for _, ns := range []namespace.Namespace{systemStore(t), mustOrg(t, name)} {
		if !exists(fileOf(t, ns)) {
			t.Fatalf("commerce wrote no file for %s", ns)
		}
	}

	// The second life names the server.
	t.Setenv(locus.Env, dsn)
	t.Setenv(locus.MoveEnv, string(locus.Lazy))
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	reboot(t)

	if got := resolved(t, name); got != id {
		t.Fatalf("org %s resolves to %q after the move, was %q", name, got, id)
	}
	if n, src := limitOf(t, name); n != 654 || src != client.LimitOrg {
		t.Fatalf("the limit reads %d from %s after the move, want 654 from the org's own row", n, src)
	}
	moved, err := currentEmbedded().App().DB.Org(name)
	if err != nil {
		t.Fatalf("the org's merchant store after the move: %v", err)
	}
	var got shelf
	if err := moved.Get(ctx, moved.NewKey("Shelf", "one", 0, nil), &got); err != nil || got.Label != "before the move" || got.Count != 7 {
		t.Fatalf("the merchant model after the move is %+v (%v)", got, err)
	}
	for _, ns := range []namespace.Namespace{systemStore(t), mustOrg(t, name)} {
		place, err := locus.Where(ns, tenantSubsystem, commerceDir())
		if err != nil || place != locus.OnServer {
			t.Fatalf("%s is %v (%v) after the move, want the server", ns, place, err)
		}
		if !exists(fileOf(t, ns)) {
			t.Fatalf("the move removed the file of %s: it is left where it was", ns)
		}
	}
}

// stopEmbedded stops the commerce the test booted, which closes its stores.
func stopEmbedded(t *testing.T) {
	t.Helper()
	stop, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := currentEmbedded().Stop(stop); err != nil {
		t.Fatalf("stop: %v", err)
	}
	PublishEmbedded(nil)
}

// reboot starts commerce again over the data directory the test already has, as a
// process restarting does.
func reboot(t *testing.T) {
	t.Helper()
	if err := Use(zip.New(zip.Config{Logger: luxlog.New("reboot")}), cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if currentEmbedded() == nil {
		t.Fatal("the commerce embed did not boot again")
	}
}

func mustOrg(t *testing.T, name string) namespace.Namespace {
	t.Helper()
	ns, err := namespace.OrgProject(name, "")
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

// A deployment's commerce files were keyed by the module itself, from the master the host
// handed it. The seam opens them under the data plane's key, so the two must be the one key:
// a file the module wrote is read through the seam as it is, and moves to the server whole.
// It needs the live codec (make test-codec), as the files it stands for were written under it.
func TestAFileTheModuleKeyedIsReadThroughTheSeamAndMoves(t *testing.T) {
	if !sqlitedrv.CodecLinked() {
		t.Skip("needs the SQLCipher codec (CGO_ENABLED=1 -tags libsqlite3, linked against libsqlcipher)")
	}
	dsn := sqltest.Server(t)
	t.Setenv(locus.Env, "") // the file is written and read first
	master := bytes.Repeat([]byte{0x42}, cek.KeyLen)
	if err := cek.SetMaster(master); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = cek.SetDevMaster() })
	dir := t.TempDir()
	ctx := nscontext.WithNamespace(context.Background(), "acme")
	type shelf struct{ Label string }

	old, err := commercedb.NewManager(&commercedb.Config{DataDir: dir, MasterKey: master})
	if err != nil {
		t.Fatal(err)
	}
	store, err := old.Org("acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, store.NewKey("Shelf", "one", 0, nil), &shelf{Label: "keyed by the module"}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	file := fileOfIn(t, dir, mustOrg(t, "acme"))
	if head, err := os.ReadFile(file); err != nil || bytes.HasPrefix(head, []byte("SQLite format 3")) {
		t.Fatalf("the module's file is not encrypted (%v)", err)
	}

	read := func(t *testing.T, where string) {
		t.Helper()
		m, err := commercedb.NewManager(&commercedb.Config{DataDir: dir, Seam: seam(dir, luxlog.New("seam"))})
		if err != nil {
			t.Fatalf("%s: %v", where, err)
		}
		defer func() { _ = m.Close() }()
		s, err := m.Org("acme")
		if err != nil {
			t.Fatalf("%s: %v", where, err)
		}
		var got shelf
		if err := s.Get(ctx, s.NewKey("Shelf", "one", 0, nil), &got); err != nil || got.Label != "keyed by the module" {
			t.Fatalf("%s: the row reads %+v (%v)", where, got, err)
		}
	}
	read(t, "on the file")

	t.Setenv(locus.Env, dsn)
	t.Setenv(locus.MoveEnv, string(locus.Lazy))
	read(t, "after the move")
	if place, err := locus.Where(mustOrg(t, "acme"), tenantSubsystem, dir); err != nil || place != locus.OnServer {
		t.Fatalf("the module's file is %v (%v), want the server", place, err)
	}
}

func fileOfIn(t *testing.T, dir string, ns namespace.Namespace) string {
	t.Helper()
	path, err := namespace.Path(dir, ns, tenantSubsystem)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
