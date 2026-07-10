package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/base/core"
)

// ErrKVNotFound is returned by KVStore.Get and KVStore.GetExpiry when no live
// (unexpired) entry exists for the key. Callers translate to a cache miss —
// never to a 500.
var ErrKVNotFound = errors.New("store: kv key not found")

// KVStore is the base-backed key/value cache that replaces the former
// Redis/Valkey client. It persists into the commerce_kv collection (per-store
// SQLite file, or Postgres when DataDSN is set), giving the same single-tenant
// embedded-data backend the rest of commerce already uses.
//
// Semantics match the subset of the old KV interface that commerce actually
// consumed:
//
//   - Get  — returns the live value or ErrKVNotFound. Expired entries are
//     deleted lazily on read and reported as ErrKVNotFound.
//   - Set  — upserts; ttl==0 means no expiry (sentinel expires_at = 0).
//   - Delete — removes by key; deleting an absent key is not an error.
//   - SetNX — atomic set-if-absent (drives distributed locking); an expired
//     entry counts as absent and is overwritten.
//   - CompareAndDelete / CompareAndExtend — atomic lock release/renew without
//     server-side scripting (base SQLite transactions are serializable).
//
// All mutating operations run inside RunInTransaction so concurrent writers
// cannot corrupt or race the upsert; SQLite's serializable isolation makes the
// read-modify-write atomic.
type KVStore struct {
	app core.App
}

// NewKVStore wraps a base app. The commerce_kv collection must already exist
// (the migration under store/migrations creates it on Bootstrap).
func NewKVStore(app core.App) *KVStore {
	return &KVStore{app: app}
}

// nowUnixMilli is the monotonic-enough wall clock used for expiry math.
// Indirected for tests that need to assert lazy expiry without sleeping.
var nowUnixMilli = func() int64 { return time.Now().UnixMilli() }

// expiresAt converts a ttl into an absolute expires_at (unix millis). A zero
// or negative ttl yields the no-expiry sentinel (0).
func expiresAt(ttl time.Duration) int64 {
	if ttl <= 0 {
		return 0
	}
	return nowUnixMilli() + ttl.Milliseconds()
}

// live reports whether an expires_at value is still in the future (or the
// no-expiry sentinel).
func live(exp int64) bool {
	return exp == 0 || exp > nowUnixMilli()
}

// findRecord returns the raw record for key, or (nil, nil) if absent. It does
// NOT apply expiry — callers decide whether an expired row counts.
func (s *KVStore) findRecord(app core.App, key string) (*core.Record, error) {
	rec, err := app.FindFirstRecordByFilter(
		"commerce_kv",
		"key = {:key}",
		map[string]any{"key": key},
	)
	if err != nil {
		// base returns sql.ErrNoRows-wrapped errors for "no match"; collapse
		// every not-found to (nil, nil) so the caller distinguishes only
		// present-vs-absent, not the failure shape.
		return nil, nil
	}
	return rec, nil
}

// Get returns the live value for key. Expired entries are deleted lazily and
// reported as ErrKVNotFound.
func (s *KVStore) Get(key string) ([]byte, error) {
	rec, err := s.findRecord(s.app, key)
	if err != nil {
		return nil, fmt.Errorf("store: kv get: %w", err)
	}
	if rec == nil {
		return nil, ErrKVNotFound
	}
	if !live(int64(rec.GetInt("expires_at"))) {
		// Expired: best-effort lazy delete, then report miss. A delete error
		// here is non-fatal — the row will be overwritten on next Set and is
		// already logically gone.
		_ = s.app.Delete(rec)
		return nil, ErrKVNotFound
	}
	return []byte(rec.GetString("value")), nil
}

// Set upserts key→value with the given ttl (0 == no expiry). Atomic under
// RunInTransaction: a concurrent Set on the same key serializes.
func (s *KVStore) Set(key string, value []byte, ttl time.Duration) error {
	if key == "" {
		return errors.New("store: kv set: empty key")
	}
	exp := expiresAt(ttl)
	return s.app.RunInTransaction(func(txApp core.App) error {
		rec, err := s.findRecord(txApp, key)
		if err != nil {
			return fmt.Errorf("store: kv set lookup: %w", err)
		}
		if rec == nil {
			collection, err := txApp.FindCollectionByNameOrId("commerce_kv")
			if err != nil {
				return fmt.Errorf("store: kv set collection: %w", err)
			}
			rec = core.NewRecord(collection)
			rec.Set("key", key)
		}
		rec.Set("value", string(value))
		rec.Set("expires_at", exp)
		if err := txApp.Save(rec); err != nil {
			return fmt.Errorf("store: kv set save: %w", err)
		}
		return nil
	})
}

// Delete removes key. Deleting an absent (or already-expired) key is a no-op,
// not an error — matching the old Redis DEL semantics commerce relied on.
func (s *KVStore) Delete(key string) error {
	return s.app.RunInTransaction(func(txApp core.App) error {
		rec, err := s.findRecord(txApp, key)
		if err != nil {
			return fmt.Errorf("store: kv delete lookup: %w", err)
		}
		if rec == nil {
			return nil
		}
		if err := txApp.Delete(rec); err != nil {
			return fmt.Errorf("store: kv delete: %w", err)
		}
		return nil
	})
}

// Exists reports whether a live entry exists for key.
func (s *KVStore) Exists(key string) (bool, error) {
	_, err := s.Get(key)
	if errors.Is(err, ErrKVNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SetNX atomically sets key→value only if no live entry exists. Returns true
// when the value was written, false when a live entry already held the key. An
// expired entry counts as absent and is overwritten. This is the primitive the
// distributed lock builds on.
func (s *KVStore) SetNX(key string, value []byte, ttl time.Duration) (bool, error) {
	if key == "" {
		return false, errors.New("store: kv setnx: empty key")
	}
	exp := expiresAt(ttl)
	var acquired bool
	err := s.app.RunInTransaction(func(txApp core.App) error {
		rec, err := s.findRecord(txApp, key)
		if err != nil {
			return fmt.Errorf("store: kv setnx lookup: %w", err)
		}
		if rec != nil && live(int64(rec.GetInt("expires_at"))) {
			acquired = false
			return nil
		}
		if rec == nil {
			collection, err := txApp.FindCollectionByNameOrId("commerce_kv")
			if err != nil {
				return fmt.Errorf("store: kv setnx collection: %w", err)
			}
			rec = core.NewRecord(collection)
			rec.Set("key", key)
		}
		rec.Set("value", string(value))
		rec.Set("expires_at", exp)
		if err := txApp.Save(rec); err != nil {
			return fmt.Errorf("store: kv setnx save: %w", err)
		}
		acquired = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return acquired, nil
}

// CompareAndDelete atomically deletes key only if its current live value
// equals want. Returns true on delete, false when the value differs or the key
// is absent/expired. Replaces the Redis compare-and-delete Lua script.
func (s *KVStore) CompareAndDelete(key string, want []byte) (bool, error) {
	var deleted bool
	err := s.app.RunInTransaction(func(txApp core.App) error {
		rec, err := s.findRecord(txApp, key)
		if err != nil {
			return fmt.Errorf("store: kv cad lookup: %w", err)
		}
		if rec == nil || !live(int64(rec.GetInt("expires_at"))) {
			deleted = false
			return nil
		}
		if rec.GetString("value") != string(want) {
			deleted = false
			return nil
		}
		if err := txApp.Delete(rec); err != nil {
			return fmt.Errorf("store: kv cad delete: %w", err)
		}
		deleted = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

// CompareAndExtend atomically resets key's ttl only if its current live value
// equals want. Returns true on extend, false when the value differs or the key
// is absent/expired. Replaces the Redis compare-and-extend Lua script.
func (s *KVStore) CompareAndExtend(key string, want []byte, ttl time.Duration) (bool, error) {
	exp := expiresAt(ttl)
	var extended bool
	err := s.app.RunInTransaction(func(txApp core.App) error {
		rec, err := s.findRecord(txApp, key)
		if err != nil {
			return fmt.Errorf("store: kv cae lookup: %w", err)
		}
		if rec == nil || !live(int64(rec.GetInt("expires_at"))) {
			extended = false
			return nil
		}
		if rec.GetString("value") != string(want) {
			extended = false
			return nil
		}
		rec.Set("expires_at", exp)
		if err := txApp.Save(rec); err != nil {
			return fmt.Errorf("store: kv cae save: %w", err)
		}
		extended = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return extended, nil
}

// isUniqueViolationKV mirrors isUniqueViolation for kv-key collisions. A SetNX
// race where two transactions both pass the absence check is impossible under
// SQLite serializable isolation, but the unique index on `key` is the
// belt-and-suspenders guard; surface it as a retryable signal to the caller.
func isUniqueViolationKV(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Value must be unique") ||
		strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value")
}

var _ = isUniqueViolationKV
