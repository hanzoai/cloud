package risk

// durable_test.go pins the property store.go's header claims: a record is not
// acknowledged until it is durable.
//
// The test drives the REAL durable plane — cloud.OrgStore over org.Durability
// over a conditional object store — with the object store standing in as an
// in-memory CAS register. Nothing here is a mock of our own code: the ship, the
// fence and the lease are the production ones, and the only substitution is the
// bucket.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/org"
	sqlitedrv "github.com/hanzoai/sqlite"
	"github.com/hanzoai/vfs/replica"
	"github.com/luxfi/aml/pkg/anomaly"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestADecisionIsShippedBeforeItIsAcknowledged.
//
// A decision is the record an adverse action is defended with. cloud deploys
// Recreate at one replica, so a decision that lives only on the pod's volume is
// one a rollout can lose AFTER the caller was told it was taken. The ship
// happens before the 200, or the 200 is a lie.
func TestADecisionIsShippedBeforeItIsAcknowledged(t *testing.T) {
	app, _, store := wireDurable(t)

	before := store.puts()
	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"payment","subject":{"kind":"transaction","id":"tx-1"},
		  "amount":{"nano":12000000000,"currency":"USD","direction":"in"}}`)
	if code != http.StatusOK {
		t.Fatalf("decide = %d %s", code, body)
	}
	if got := store.puts() - before; got == 0 {
		t.Fatal("the decision was acknowledged without a single write to the durable object: " +
			"a rollout loses it and the caller was told it was taken")
	}
}

// TestAWriteThatCannotBeShippedIsNotAcknowledged.
//
// The object store goes away mid-flight. Every record write must then FAIL —
// loudly, with a status a caller retries on — rather than answer 200 over a row
// that exists on one pod's disk and nowhere else. The retry is exact because the
// caller's idempotency key is claimed inside the same transaction.
func TestAWriteThatCannotBeShippedIsNotAcknowledged(t *testing.T) {
	app, _, store := wireDurable(t)

	// One good decision first, so the file, the lease and the object all exist:
	// what follows is a store that fails, not a store that was never reachable.
	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"payment","subject":{"kind":"transaction","id":"tx-1"}}`)
	if code != http.StatusOK {
		t.Fatalf("first decide = %d %s", code, body)
	}

	store.fail(errors.New("the object store is unreachable"))
	for _, tc := range []struct{ what, method, path, body string }{
		{"a decision", http.MethodPost, "/v1/risk/decide",
			`{"stage":"payment","subject":{"kind":"transaction","id":"tx-2"}}`},
		{"a policy version", http.MethodPut, "/v1/risk/policy",
			`{"stage":"payment","floor":"allow","reason":"the dispute rate doubled","bands":[{"at":0.9,"action":"block"}]}`},
		{"a rule", http.MethodPost, "/v1/risk/rules",
			`{"rule":{"id":"r-dur","name":"durable","stage":"payment","action":"review","weight":0.5,"enabled":true,
			   "all":[{"field":"subject.kind","op":"eq","value":"transaction"}]}}`},
	} {
		code, body := req(t, app, tc.method, tc.path, "acme", "u_acme", tc.body)
		if code < 500 {
			t.Errorf("%s answered %d %s while the durable object store was down — "+
				"the record exists on this pod only and the caller was told otherwise", tc.what, code, body)
		}
	}
}

// TestTheRecordPlaneIsOpenedThroughTheDurableStore is the guard that survives a
// revert. The bare encrypted open is the open and nothing more, so a package
// that calls it directly has no hydrate, no fence and no ship whatever its
// comments say. This package reaches a file through the durable org store and no
// other way.
func TestTheRecordPlaneIsOpenedThroughTheDurableStore(t *testing.T) {
	// Spelled in two halves so this file does not match its own guard.
	bareOpen := "cloud.Org" + "DB("

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no source read, so this guard proved nothing")
	}
	found := false
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(body), "cloud.NewOrgStore") {
			found = true
		}
		if strings.Contains(string(body), bareOpen) {
			t.Errorf("%s opens a tenant file with cloud.OrgDB — that open is local-only, so every record "+
				"in it is as durable as one pod's volume", f)
		}
	}
	if !found {
		t.Error("nothing in this package opens its files through cloud.NewOrgStore, so nothing is durable")
	}
}

// TestCommitRefusesToShipAWriteThatFailed pins the order inside commit: the ship
// is the acknowledgement of a write that HAPPENED, so a failed write must not
// produce one.
func TestCommitRefusesToShipAWriteThatFailed(t *testing.T) {
	_, s := wireApp(t)
	boom := errors.New("the write failed")
	err := s.State.shelf.commit(Tenant("hanzo/acme"), func(*sql.DB) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("commit returned %v, want the write's own error", err)
	}
}

// ── the durable harness ─────────────────────────────────────────────────────

// wireDurable mounts the surface over a REAL durable org store whose object
// store is an in-memory CAS register.
func wireDurable(t *testing.T) (*zip.App, *stateService, *bucket) {
	t.Helper()
	dir := t.TempDir()
	log := luxlog.New("risktest")
	b := cloud.NewBase(cloud.Deps{Logger: log, DataDir: dir, Brand: "hanzo"}, "risk")
	store := newBucket()
	// The SAME checkpoint the composition root wires (build.go durableCheckpoint):
	// the ship reads the real on-disk path, and on an encrypting backend that path
	// is only fresh after the WAL is folded and re-encrypted. Without it this
	// harness would be testing a ship of a file that does not exist.
	b.Durable = org.NewDurability(store, soleWriter{}, nil, org.WithCheckpoint(checkpoint))

	shape, err := forest(anomaly.Config{}, aggregates())
	if err != nil {
		t.Fatalf("forest: %v", err)
	}
	s := &cloud.Service[state]{Base: b, State: state{
		brand:    "hanzo",
		dataDir:  dir,
		shelf:    newShelf(b),
		inflight: newInflight("a measurement"),
		running:  newInflight("an exhaustive search"),
		digest:   shape.Digest(),
		bill:     cloud.NewResourceMeter(cloud.Deps{Logger: log}, "risk"),
	}}
	app := zip.New(zip.Config{Logger: log, DisableStartupMessage: true})
	mount(s, app)
	t.Cleanup(func() { s.State.shelf.close() })
	return app, s, store
}

// checkpoint folds the WAL into the real path and re-encrypts it, which is what
// makes the bytes a ship reads the bytes a caller was told were written.
func checkpoint(ctx context.Context, db *sql.DB) error {
	// The fold runs on its OWN connection and RELEASES it before the re-encrypt:
	// the store holds one connection, and an envelope re-encrypt that asks for a
	// second while the first is still open waits for itself. (Production splits
	// these two for the same reason — build.go walCheckpointTruncate.)
	if err := fold(ctx, db); err != nil {
		return err
	}
	return sqlitedrv.Checkpoint(db)
}

func fold(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var busy, frames, done int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &done); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("checkpoint busy=%d", busy)
	}
	return nil
}

// soleWriter is the membership: one pod, which is therefore every org's elected
// writer. The election itself is tested in internal/org; here it only has to
// resolve to "this process owns the lease" so a ship can be acknowledged.
type soleWriter struct{}

func (soleWriter) Self() string          { return "pod-a" }
func (soleWriter) Members() []org.Member { return []org.Member{{ID: "pod-a"}} }

// bucket is an in-memory conditional store: the CAS register the fence and the
// ship both run over. It counts writes and can be made to fail.
type bucket struct {
	mu     sync.Mutex
	slots  map[string]*slot
	writes int
	err    error
}

type slot struct {
	data []byte
	ver  int
}

func newBucket() *bucket { return &bucket{slots: map[string]*slot{}} }

func (b *bucket) puts() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writes
}

func (b *bucket) fail(err error) {
	b.mu.Lock()
	b.err = err
	b.mu.Unlock()
}

func (b *bucket) Get(_ context.Context, key string) ([]byte, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return nil, "", b.err
	}
	s, ok := b.slots[key]
	if !ok {
		return nil, "", replica.ErrNotFound
	}
	return append([]byte(nil), s.data...), strconv.Itoa(s.ver), nil
}

func (b *bucket) PutIfVersion(_ context.Context, key string, data []byte, expect string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return "", b.err
	}
	s, ok := b.slots[key]
	cur := ""
	if ok {
		cur = strconv.Itoa(s.ver)
	}
	if cur != expect {
		return "", fmt.Errorf("bucket: %w: have %q want %q", replica.ErrConflict, cur, expect)
	}
	if !ok {
		s = &slot{}
		b.slots[key] = s
	}
	s.ver++
	s.data = append([]byte(nil), data...)
	if !strings.Contains(key, "lease") { // a lease renewal is not a record ship
		b.writes++
	}
	return strconv.Itoa(s.ver), nil
}
