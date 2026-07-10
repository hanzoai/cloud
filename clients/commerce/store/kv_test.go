// Package store — base-backed KV cache tests.
//
// These exercise KVStore against a real base app booted on a throwaway SQLite
// file (newTestStore from tenant_test.go). We do NOT fake base: TTL expiry,
// SetNX atomicity, and concurrent-writer safety all depend on base's
// serializable SQLite transactions, so faking would prove nothing.
package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestKVSetGet(t *testing.T) {
	kv := newTestStore(t).KV

	if err := kv.Set("k1", []byte("v1"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := kv.Get("k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v1" {
		t.Fatalf("Get = %q, want %q", got, "v1")
	}
}

func TestKVGetMissing(t *testing.T) {
	kv := newTestStore(t).KV
	_, err := kv.Get("nope")
	if !errors.Is(err, ErrKVNotFound) {
		t.Fatalf("Get missing = %v, want ErrKVNotFound", err)
	}
}

func TestKVSetOverwrite(t *testing.T) {
	kv := newTestStore(t).KV
	if err := kv.Set("k", []byte("a"), 0); err != nil {
		t.Fatalf("Set a: %v", err)
	}
	if err := kv.Set("k", []byte("b"), 0); err != nil {
		t.Fatalf("Set b: %v", err)
	}
	got, err := kv.Get("k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "b" {
		t.Fatalf("Get = %q, want overwrite %q", got, "b")
	}
}

// TestKVTTLExpiry is the canonical TTL test: Set with a 50ms TTL, sleep past
// it, and assert the entry reads back as ErrKVNotFound (lazy expiry).
func TestKVTTLExpiry(t *testing.T) {
	kv := newTestStore(t).KV
	if err := kv.Set("ttl", []byte("v"), 50*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Live immediately after Set.
	if _, err := kv.Get("ttl"); err != nil {
		t.Fatalf("Get before expiry: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	_, err := kv.Get("ttl")
	if !errors.Is(err, ErrKVNotFound) {
		t.Fatalf("Get after 100ms (50ms TTL) = %v, want ErrKVNotFound", err)
	}
}

func TestKVDelete(t *testing.T) {
	kv := newTestStore(t).KV
	if err := kv.Set("d", []byte("v"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := kv.Delete("d"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := kv.Get("d"); !errors.Is(err, ErrKVNotFound) {
		t.Fatalf("Get after delete = %v, want ErrKVNotFound", err)
	}
	// Deleting an absent key is a no-op, not an error.
	if err := kv.Delete("d"); err != nil {
		t.Fatalf("Delete absent: %v", err)
	}
}

func TestKVExists(t *testing.T) {
	kv := newTestStore(t).KV
	ok, err := kv.Exists("x")
	if err != nil || ok {
		t.Fatalf("Exists absent = (%v, %v), want (false, nil)", ok, err)
	}
	if err := kv.Set("x", []byte("v"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ok, err = kv.Exists("x")
	if err != nil || !ok {
		t.Fatalf("Exists present = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestKVSetNX(t *testing.T) {
	kv := newTestStore(t).KV
	ok, err := kv.SetNX("lock", []byte("a"), 0)
	if err != nil || !ok {
		t.Fatalf("first SetNX = (%v, %v), want (true, nil)", ok, err)
	}
	// Second SetNX on a live key must fail to acquire.
	ok, err = kv.SetNX("lock", []byte("b"), 0)
	if err != nil || ok {
		t.Fatalf("second SetNX = (%v, %v), want (false, nil)", ok, err)
	}
	// Original value untouched.
	got, _ := kv.Get("lock")
	if string(got) != "a" {
		t.Fatalf("value after failed SetNX = %q, want %q", got, "a")
	}
}

func TestKVSetNXOverwritesExpired(t *testing.T) {
	kv := newTestStore(t).KV
	if ok, err := kv.SetNX("e", []byte("old"), 30*time.Millisecond); err != nil || !ok {
		t.Fatalf("SetNX old = (%v, %v)", ok, err)
	}
	time.Sleep(60 * time.Millisecond)
	// Expired entry counts as absent: SetNX must succeed and overwrite.
	if ok, err := kv.SetNX("e", []byte("new"), 0); err != nil || !ok {
		t.Fatalf("SetNX after expiry = (%v, %v), want (true, nil)", ok, err)
	}
	got, _ := kv.Get("e")
	if string(got) != "new" {
		t.Fatalf("value = %q, want %q", got, "new")
	}
}

func TestKVCompareAndDelete(t *testing.T) {
	kv := newTestStore(t).KV
	if err := kv.Set("cad", []byte("token"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Wrong value: no delete.
	if ok, err := kv.CompareAndDelete("cad", []byte("wrong")); err != nil || ok {
		t.Fatalf("CAD wrong = (%v, %v), want (false, nil)", ok, err)
	}
	if _, err := kv.Get("cad"); err != nil {
		t.Fatalf("key gone after wrong-value CAD: %v", err)
	}
	// Right value: delete.
	if ok, err := kv.CompareAndDelete("cad", []byte("token")); err != nil || !ok {
		t.Fatalf("CAD right = (%v, %v), want (true, nil)", ok, err)
	}
	if _, err := kv.Get("cad"); !errors.Is(err, ErrKVNotFound) {
		t.Fatalf("key still present after CAD: %v", err)
	}
}

func TestKVCompareAndExtend(t *testing.T) {
	kv := newTestStore(t).KV
	if err := kv.Set("cae", []byte("token"), 40*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Wrong value: no extend.
	if ok, err := kv.CompareAndExtend("cae", []byte("wrong"), time.Hour); err != nil || ok {
		t.Fatalf("CAE wrong = (%v, %v), want (false, nil)", ok, err)
	}
	// Right value: extend well past the original TTL.
	if ok, err := kv.CompareAndExtend("cae", []byte("token"), time.Hour); err != nil || !ok {
		t.Fatalf("CAE right = (%v, %v), want (true, nil)", ok, err)
	}
	time.Sleep(80 * time.Millisecond) // past original 40ms TTL
	if _, err := kv.Get("cae"); err != nil {
		t.Fatalf("key expired despite extend: %v", err)
	}
}

// TestKVConcurrentWriters asserts that 100 goroutines hammering Set on the same
// and distinct keys never corrupt the store. base's serializable SQLite
// transactions serialize the read-modify-write upsert.
func TestKVConcurrentWriters(t *testing.T) {
	kv := newTestStore(t).KV
	const n = 100

	var wg sync.WaitGroup
	wg.Add(n)
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			// Half the goroutines fight over one hot key; half write unique
			// keys. Both paths must stay corruption-free.
			key := "shared"
			if i%2 == 0 {
				key = fmt.Sprintf("unique-%d", i)
			}
			if err := kv.Set(key, []byte(fmt.Sprintf("v%d", i)), 0); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent Set: %v", err)
	}

	// The hot key resolves to exactly one of the written values (no torn
	// write, no missing row).
	got, err := kv.Get("shared")
	if err != nil {
		t.Fatalf("Get shared after concurrent writes: %v", err)
	}
	if len(got) == 0 || got[0] != 'v' {
		t.Fatalf("shared = %q, want a v<N> value", got)
	}
	// Every unique key persisted.
	for i := 0; i < n; i += 2 {
		key := fmt.Sprintf("unique-%d", i)
		v, err := kv.Get(key)
		if err != nil {
			t.Fatalf("Get %s: %v", key, err)
		}
		if string(v) != fmt.Sprintf("v%d", i) {
			t.Fatalf("%s = %q, want v%d", key, v, i)
		}
	}
}

// TestKVConcurrentSetNX asserts SetNX is a true mutex: 100 goroutines race for
// the same key and exactly one wins.
func TestKVConcurrentSetNX(t *testing.T) {
	kv := newTestStore(t).KV
	const n = 100

	var wg sync.WaitGroup
	wg.Add(n)
	var won int64
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			ok, err := kv.SetNX("mutex", []byte(fmt.Sprintf("v%d", i)), time.Hour)
			if err != nil {
				return
			}
			if ok {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("SetNX winners = %d, want exactly 1", won)
	}
}
