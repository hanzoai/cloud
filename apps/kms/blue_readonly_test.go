package kms

import (
	"testing"

	luxlog "github.com/luxfi/log"
)

// TestReaderReadOnlyRoundTrip proves the reader HA path: a per-org file written by
// the writer (keyed) can be reopened in READ-ONLY mode and its secrets read back,
// while mutations fail closed. Per-org SQLite is WAL-shareable (no exclusive
// opener lock), so a reader can serve KMS reads off the shared/restored files
// locally — it no longer needs to reverse-proxy KMS to the writer.
func TestReaderReadOnlyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	log := luxlog.NewNoOpLogger()
	key := b64key(t, 0x5A)

	// Writer: create the encrypted store and write a secret.
	w, err := New(Config{DataDir: dir, MasterKeyB64: key}, log)
	if err != nil {
		t.Fatalf("writer New: %v", err)
	}
	if err := w.Put("/orgs/acme", "DB_URL", "default", []byte("postgres://secret")); err != nil {
		t.Fatalf("writer Put: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer Close: %v", err)
	}

	// Reader: reopen the SAME per-org store read-only, same key.
	r, err := New(Config{DataDir: dir, MasterKeyB64: key, ReadOnly: true}, log)
	if err != nil {
		t.Fatalf("reader New (read-only): %v", err)
	}
	defer r.Close()
	if !r.Ready() {
		t.Fatal("reader should be Ready (has key + store)")
	}
	got, err := r.Get("/orgs/acme", "DB_URL", "default")
	if err != nil {
		t.Fatalf("reader Get: %v", err)
	}
	if string(got) != "postgres://secret" {
		t.Fatalf("reader Get = %q, want %q", got, "postgres://secret")
	}

	// A read-only reader must NOT be able to write: writes fail closed rather than
	// mutating a replica that is not the authoritative writer.
	if err := r.Put("/orgs/acme", "DB_URL", "default", []byte("tampered")); err == nil {
		t.Fatal("reader Put must fail on a read-only store (would fork writer state)")
	}
}

// TestReaderWithoutRestoredStoreFailsClosed: a reader with no hydrated store must
// refuse rather than open an empty store and serve zero secrets as if healthy.
func TestReaderWithoutRestoredStoreFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(Config{DataDir: dir, MasterKeyB64: b64key(t, 0x5A), ReadOnly: true}, luxlog.NewNoOpLogger()); err == nil {
		t.Fatal("reader with no restored store must fail closed")
	}
}

// TestReaderWithoutKeyFailsClosed: a reader cannot decrypt the store at rest
// without the master key; it must fail closed, not open a broken handle.
func TestReaderWithoutKeyFailsClosed(t *testing.T) {
	dir := t.TempDir()
	log := luxlog.NewNoOpLogger()
	// Seed a per-org store so a restored store exists on disk.
	w, err := New(Config{DataDir: dir, MasterKeyB64: b64key(t, 0x5A)}, log)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = w.Put("/o", "K", "default", []byte("v"))
	w.Close()

	if _, err := New(Config{DataDir: dir, MasterKeyB64: "", ReadOnly: true}, log); err == nil {
		t.Fatal("reader with no key must fail closed")
	}
}
