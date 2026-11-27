package cloud

// orgdb_fresh_test.go — A POD ON AN EMPTY DISK SEES EVERY STORE THE DEPLOYMENT HAS.
//
// On the durable plane a pod's disk is a cache: a new pod (an emptyDir) holds no
// store until it opens one. Has, Stored and the sweeps used to read only the
// disk, so such a pod answered "nothing here" for an org whose data sat in the
// object store, and swept nothing until each org's next request opened it.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
	sqlitedrv "github.com/hanzoai/sqlite"
)

// List makes the in-memory store a Lister, as the S3 store is.
func (s *recordingStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

func TestAnEmptyDiskSeesTheObjectStore(t *testing.T) {
	cond := &recordingStore{objects: map[string]versioned{}}
	members := tenure.NewMembership("pod-0", tenure.StaticSource(tenure.Member{ID: "pod-0", Addr: "pod-0"}), time.Minute)
	if err := members.Start(context.Background()); err != nil {
		t.Fatalf("membership: %v", err)
	}
	dur := tenure.NewDurability(cond, members, nil, tenure.WithSeal(sqlitedrv.Checkpoint), tenure.WithReader(OrgReader), tenure.WithLister(cond))
	acme := principal.MustOrgNamespace("acme", "")

	// The pod before: writes acme's row, ships it, and is gone.
	before := NewOrgStore(Base{DataDir: t.TempDir(), Durable: dur}, "widget", openRows)
	db, err := before.For(acme)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO rows(v) VALUES(7)`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if acked, err := before.Sync(acme); err != nil || !acked {
		t.Fatalf("ship: acked=%v err=%v", acked, err)
	}
	if err := before.CloseAll(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The pod after, on an empty disk.
	dir := t.TempDir()
	after := NewOrgStore(Base{DataDir: dir, Durable: dur}, "widget", openRows)
	t.Cleanup(func() { _ = after.CloseAll() })

	if !after.Has(acme) {
		t.Fatal("Has said acme has no store; its store is in the object store")
	}
	stranger := principal.MustOrgNamespace("nobody", "")
	if after.Has(stranger) {
		t.Fatal("Has said a name with no store has one")
	}
	if _, err := os.Stat(filepath.Join(dir, "orgs", "nobody")); !os.IsNotExist(err) {
		t.Fatalf("asking about a stranger's name left something on disk: %v", err)
	}
	if !after.Stored() {
		t.Fatal("Stored said the deployment has no store")
	}

	var swept []string
	var rows int
	if err := after.Each(func(ns namespace.Namespace, db *sql.DB, err error) {
		if err != nil {
			t.Fatalf("sweep open %s: %v", ns, err)
		}
		swept = append(swept, ns.ID())
		if err := db.QueryRow(`SELECT count(*) FROM rows WHERE v = 7`).Scan(&rows); err != nil {
			t.Fatalf("read: %v", err)
		}
	}); err != nil {
		t.Fatalf("Each: %v", err)
	}
	if !slices.Equal(swept, []string{"acme"}) || rows != 1 {
		t.Fatalf("the first sweep on an empty disk visited %v and read %d rows; want [acme] and its row", swept, rows)
	}
}

// Without a lister the disk is all a store knows: Has answers from it alone.
func TestNoListerAsksOnlyTheDisk(t *testing.T) {
	cond := &recordingStore{objects: map[string]versioned{"orgs/acme/widget.db": {data: []byte("x"), version: "v1"}}}
	members := tenure.NewMembership("pod-0", tenure.StaticSource(tenure.Member{ID: "pod-0", Addr: "pod-0"}), time.Minute)
	if err := members.Start(context.Background()); err != nil {
		t.Fatalf("membership: %v", err)
	}
	dur := tenure.NewDurability(cond, members, nil)
	st := NewOrgStore(Base{DataDir: t.TempDir(), Durable: dur}, "widget", openRows)
	t.Cleanup(func() { _ = st.CloseAll() })
	if st.Has(principal.MustOrgNamespace("acme", "")) {
		t.Fatal("Has answered from an object store it cannot list")
	}
}
