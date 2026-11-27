// Package sidecar holds the test that a store written under a random key kept
// beside it (a .dek sidecar, the scheme before derived keys) survives the move to
// a disk that does not have that sidecar. It is its own binary because it installs
// the fixed master its fixture was sealed under, which no other test may share.
package sidecar

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/cek"
	sqlitedrv "github.com/hanzoai/sqlite"
	"github.com/hanzoai/vfs/replica"
)

// memory is an object store in memory: compare-and-set, and a listing.
type memory struct {
	mu      sync.Mutex
	objects map[string][]byte
	seq     int
	version map[string]string
}

func (m *memory) Get(_ context.Context, key string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return nil, "", replica.ErrNotFound
	}
	return b, m.version[key], nil
}

func (m *memory) PutIfVersion(_ context.Context, key string, data []byte, expect string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.version[key]; (expect == "" && ok) || (expect != "" && cur != expect) {
		return "", fmt.Errorf("put %s: %w", key, replica.ErrConflict)
	}
	m.seq++
	v := fmt.Sprintf("v%d", m.seq)
	m.objects[key] = append([]byte(nil), data...)
	m.version[key] = v
	return v, nil
}

func (m *memory) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

func openSecret(db *sql.DB) (*sql.DB, error) { return db, nil }

func secret(t *testing.T, db *sql.DB) string {
	t.Helper()
	var v string
	if err := db.QueryRow(`SELECT v FROM secret WHERE k = 'UNIVERSE_PIN_TOKEN'`).Scan(&v); err != nil {
		t.Fatalf("read: %v", err)
	}
	return v
}

// testdata/widget.db was sealed under a random DEK wrapped into its sidecar
// (testdata/widget.key), under the master 0x01..0x20 (written by cek's own fixture
// builder, writeWrappedDEKStore). Opened by an OrgStore it is moved to its
// derived key, the move is shipped, and a pod on an empty disk reads its row.
func TestASidecarStoreSurvivesAnEmptyDisk(t *testing.T) {
	master := make([]byte, cek.KeyLen)
	for i := range master {
		master[i] = byte(i + 1)
	}
	if err := cek.SetMaster(master); err != nil {
		t.Fatal(err)
	}

	// The sidecar is kept as widget.key: the repository ignores *.dek, which is
	// what a real one is called, so none is ever committed by accident.
	old := t.TempDir()
	if err := os.MkdirAll(filepath.Join(old, "orgs", "acme"), 0o700); err != nil {
		t.Fatal(err)
	}
	for from, to := range map[string]string{"widget.db": "widget.db", "widget.key": "widget.db.dek"} {
		b, err := os.ReadFile(filepath.Join("testdata", from))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(old, "orgs", "acme", to), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	store := &memory{objects: map[string][]byte{}, version: map[string]string{}}
	members := tenure.NewMembership("pod-0", tenure.StaticSource(tenure.Member{ID: "pod-0", Addr: "pod-0"}), time.Minute)
	if err := members.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	dur := tenure.NewDurability(store, members, nil, tenure.WithSeal(sqlitedrv.Checkpoint), tenure.WithLister(store))
	acme := principal.MustOrgNamespace("acme", "")

	first := cloud.NewOrgStore(cloud.Base{DataDir: old, Durable: dur}, "widget", openSecret)
	db, err := first.For(acme)
	if err != nil {
		t.Fatalf("open the sidecar store: %v", err)
	}
	if v := secret(t, db); v != "written-before-derived-keys" {
		t.Fatalf("read %q", v)
	}
	if _, err := os.Stat(filepath.Join(old, "orgs", "acme", "widget.db.dek")); !os.IsNotExist(err) {
		t.Fatalf("the sidecar is still beside the store after it opened: %v", err)
	}
	if acked, err := first.Sync(acme); err != nil || !acked {
		t.Fatalf("ship: acked=%v err=%v", acked, err)
	}
	if err := first.CloseAll(); err != nil {
		t.Fatal(err)
	}

	second := cloud.NewOrgStore(cloud.Base{DataDir: t.TempDir(), Durable: dur}, "widget", openSecret)
	t.Cleanup(func() { _ = second.CloseAll() })
	db2, err := second.For(acme)
	if err != nil {
		t.Fatalf("open on an empty disk: %v", err)
	}
	if v := secret(t, db2); v != "written-before-derived-keys" {
		t.Fatalf("an empty disk read %q from the shipped store", v)
	}
}

// A store nobody opens moves too: building the store set moves every sidecar store
// of its subsystem on the disk, before any of them is asked for.
func TestAStoreNobodyOpensMovesWhenTheSetIsBuilt(t *testing.T) {
	master := make([]byte, cek.KeyLen)
	for i := range master {
		master[i] = byte(i + 1)
	}
	if err := cek.SetMaster(master); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "orgs", "acme"), 0o700); err != nil {
		t.Fatal(err)
	}
	for from, to := range map[string]string{"widget.db": "widget.db", "widget.key": "widget.db.dek"} {
		b, err := os.ReadFile(filepath.Join("testdata", from))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "orgs", "acme", to), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	st := cloud.NewOrgStore(cloud.Base{DataDir: dir}, "widget", openSecret)
	t.Cleanup(func() { _ = st.CloseAll() })
	if _, err := os.Stat(filepath.Join(dir, "orgs", "acme", "widget.db.dek")); !os.IsNotExist(err) {
		t.Fatalf("building the store set left the sidecar: %v", err)
	}
	db, err := cek.OpenAt(principal.MustOrgNamespace("acme", ""), "widget", filepath.Join(dir, "orgs", "acme", "widget.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v := secret(t, db); v != "written-before-derived-keys" {
		t.Fatalf("read %q under the derived key", v)
	}
}
