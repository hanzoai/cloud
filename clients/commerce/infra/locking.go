// Package infra provides infrastructure clients.
//
// This file implements distributed locking backed by the base KV store.
// Locks use atomic set-if-absent (SetNX) with a TTL for acquisition and
// compare-and-delete / compare-and-extend for safe release and renewal —
// all served by base's serializable SQLite transactions, no server-side
// scripting required.
package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/util/rand"
)

var (
	// ErrLockNotAcquired is returned when the lock is already held.
	ErrLockNotAcquired = errors.New("lock: not acquired")

	// ErrLockNotHeld is returned when releasing or extending a lock
	// that is no longer held by this instance.
	ErrLockNotHeld = errors.New("lock: not held")
)

// Lock represents a distributed lock backed by the base KV store.
type Lock struct {
	kv    *KVClient
	key   string
	value string
	ttl   time.Duration
}

// Acquire attempts to acquire a distributed lock with the given key.
// The key is prefixed via the KV client's key prefix. Returns a Lock
// that must be released when done.
func (m *Manager) Acquire(ctx context.Context, key string, ttl time.Duration) (*Lock, error) {
	if m.kv == nil || !m.config.KV.Enabled {
		return nil, fmt.Errorf("lock: KV not enabled")
	}

	// Logical key — the KVClient applies its KeyPrefix once, so the on-disk lock
	// layout is identical whether the KV is Base- or external-backed.
	lockKey := "lock:" + key
	lockValue := rand.ShortId()

	ok, err := m.kv.SetNX(lockKey, []byte(lockValue), ttl)
	if err != nil {
		return nil, fmt.Errorf("lock: kv error: %w", err)
	}
	if !ok {
		return nil, ErrLockNotAcquired
	}

	return &Lock{
		kv:    m.kv,
		key:   lockKey,
		value: lockValue,
		ttl:   ttl,
	}, nil
}

// Release releases the distributed lock.
// Only releases if the lock is still held by this instance (compare-and-delete).
func (l *Lock) Release(ctx context.Context) error {
	ok, err := l.kv.CompareAndDelete(l.key, []byte(l.value))
	if err != nil {
		return fmt.Errorf("lock: release error: %w", err)
	}
	if !ok {
		return ErrLockNotHeld
	}
	return nil
}

// Extend extends the TTL of the lock.
// Only extends if the lock is still held by this instance.
func (l *Lock) Extend(ctx context.Context, ttl time.Duration) error {
	ok, err := l.kv.CompareAndExtend(l.key, []byte(l.value), ttl)
	if err != nil {
		return fmt.Errorf("lock: extend error: %w", err)
	}
	if !ok {
		return ErrLockNotHeld
	}
	l.ttl = ttl
	return nil
}
