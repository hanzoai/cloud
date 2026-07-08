package replication

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/luxfi/age"
	zapdb "github.com/luxfi/zapdb"
)

// memStore is an in-process stand-in for the S3/vfs object sink.
type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) Put(_ context.Context, key string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.m[key] = b
	s.mu.Unlock()
	return nil
}

func (s *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	b, ok := s.m[key]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("memstore: no object %q", key)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func openDB(t *testing.T) *zapdb.DB {
	t.Helper()
	db, err := zapdb.Open(zapdb.DefaultOptions(t.TempDir()).WithLogger(nil))
	if err != nil {
		t.Fatalf("open zapdb: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func put(t *testing.T, db *zapdb.DB, k, v string) {
	t.Helper()
	if err := db.Update(func(txn *zapdb.Txn) error { return txn.Set([]byte(k), []byte(v)) }); err != nil {
		t.Fatalf("set %s: %v", k, err)
	}
}

func get(t *testing.T, db *zapdb.DB, k string) string {
	t.Helper()
	var out string
	err := db.View(func(txn *zapdb.Txn) error {
		item, err := txn.Get([]byte(k))
		if err != nil {
			return err
		}
		v, err := item.ValueCopy(nil)
		out = string(v)
		return err
	})
	if err != nil {
		t.Fatalf("get %s: %v", k, err)
	}
	return out
}

// TestInProcessBackupRoundTripWhileWriterLive is the C1 refutation. Red proved a
// SECOND process cannot open the writer's live dir to back it up ("Log truncate
// required"). This drives the CORRECT path: the writer's OWN live *zapdb.DB
// streams incremental, age-encrypted db.Backup blocks to the object store WHILE
// it stays open and keeps writing, and a reader Restores them into its OWN,
// separate dir — never opening the writer's dir, never sharing a dir.
func TestInProcessBackupRoundTripWhileWriterLive(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("age keygen: %v", err)
	}
	store := newMemStore()

	// Writer: live handle, stays open for the whole test.
	writer := openDB(t)
	for i := 0; i < 10; i++ {
		put(t, writer, fmt.Sprintf("k%02d", i), fmt.Sprintf("v%02d", i))
	}

	p := NewProducer(writer, store, "kms", []age.Recipient{id.Recipient()})
	if _, err := p.BackupOnce(context.Background()); err != nil {
		t.Fatalf("backup block 1: %v", err)
	}

	// More writes AFTER the first block — the writer keeps writing (the point):
	// the incremental block captures only versions above the prior high-water.
	for i := 10; i < 20; i++ {
		put(t, writer, fmt.Sprintf("k%02d", i), fmt.Sprintf("v%02d", i))
	}
	if _, err := p.BackupOnce(context.Background()); err != nil {
		t.Fatalf("backup block 2 (incremental): %v", err)
	}

	// Every uploaded block is age ciphertext, never plaintext KV.
	for key, blob := range store.m {
		if strings.HasSuffix(key, ".age") && !bytes.HasPrefix(blob, []byte("age-encryption.org/v1")) {
			t.Fatalf("block %q is not age ciphertext (plaintext leak)", key)
		}
	}

	// Reader: a SEPARATE db in its OWN dir, restored from the stream while the
	// writer is STILL OPEN. No second-process open of the writer's dir anywhere.
	reader := openDB(t)
	if err := Restore(context.Background(), store, "kms", []age.Identity{id}, reader); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for i := 0; i < 20; i++ {
		k := fmt.Sprintf("k%02d", i)
		want := fmt.Sprintf("v%02d", i)
		if got := get(t, reader, k); got != want {
			t.Fatalf("reader %s = %q, want %q", k, got, want)
		}
	}
}

// TestBackupFailsClosedWithoutRecipient proves the producer refuses to emit any
// block when no age recipient is configured — never plaintext KV to the store.
func TestBackupFailsClosedWithoutRecipient(t *testing.T) {
	p := NewProducer(openDB(t), newMemStore(), "kms", nil)
	if _, err := p.BackupOnce(context.Background()); err == nil {
		t.Fatal("expected ErrEncryptionRequired, got nil (plaintext would leak)")
	}
}

// TestRestoreRejectsWrongIdentity proves a block encrypted to one recipient is
// undecryptable — and thus unrestorable — by any other identity.
func TestRestoreRejectsWrongIdentity(t *testing.T) {
	producerID, _ := age.GenerateX25519Identity()
	attackerID, _ := age.GenerateX25519Identity()
	store := newMemStore()

	writer := openDB(t)
	put(t, writer, "k", "v")
	p := NewProducer(writer, store, "kms", []age.Recipient{producerID.Recipient()})
	if _, err := p.BackupOnce(context.Background()); err != nil {
		t.Fatalf("backup: %v", err)
	}

	if err := Restore(context.Background(), store, "kms", []age.Identity{attackerID}, openDB(t)); err == nil {
		t.Fatal("restore with the wrong identity succeeded — ciphertext is not bound to its recipient")
	}
}

// TestRestoreRequiresManifestAndIdentity proves restore fails closed with no
// identity and with no manifest (an unhydrated store) rather than silently
// serving an empty reader.
func TestRestoreRequiresManifestAndIdentity(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	if err := Restore(context.Background(), newMemStore(), "kms", nil, openDB(t)); err == nil {
		t.Error("restore with no identity should fail closed")
	}
	if err := Restore(context.Background(), newMemStore(), "kms", []age.Identity{id}, openDB(t)); err == nil {
		t.Error("restore with no manifest (unhydrated store) should fail, not serve empty")
	}
}

// TestRepeatedBackupRestoreIsExact proves the guaranteed invariant across
// repeated increments — including a no-op backup with no new writes and an
// overwrite: restoring the whole chain yields EXACTLY the latest committed value
// for every key. (A no-op tick may still emit a redundant boundary block; that
// is harmless to correctness — the manifest-growth optimization is a separate
// concern, see the Producer doc — this asserts what must never break.)
func TestRepeatedBackupRestoreIsExact(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	store := newMemStore()
	writer := openDB(t)
	p := NewProducer(writer, store, "kms", []age.Recipient{id.Recipient()})

	put(t, writer, "a", "1")
	mustBackup(t, p)
	mustBackup(t, p) // no new writes — must not corrupt the chain
	put(t, writer, "b", "2")
	put(t, writer, "a", "1b") // overwrite: newer version must win on restore
	mustBackup(t, p)

	reader := openDB(t)
	if err := Restore(context.Background(), store, "kms", []age.Identity{id}, reader); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for k, want := range map[string]string{"a": "1b", "b": "2"} {
		if got := get(t, reader, k); got != want {
			t.Fatalf("reader %s = %q, want %q", k, got, want)
		}
	}
}

func mustBackup(t *testing.T, p *Producer) {
	t.Helper()
	if _, err := p.BackupOnce(context.Background()); err != nil {
		t.Fatalf("backup: %v", err)
	}
}
