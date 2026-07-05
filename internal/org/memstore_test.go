// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// memStore is an in-memory Store test double (a vfs stand-in), used by cipher_test.
// The Replicator/owner-election logic itself is tested in hanzoai/vfs/replica; this
// only exercises cloud's own cipher over the shared Store interface.

import (
	"context"
	"sync"

	"github.com/hanzoai/vfs/replica"
)

type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string][]byte{}} }

func (s *memStore) Put(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := make([]byte, len(data))
	copy(b, data)
	s.data[key] = b
	return nil
}

func (s *memStore) Get(_ context.Context, key string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key], replica.Version(s.data[key]), nil
}

func (s *memStore) Version(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return replica.Version(s.data[key]), nil
}

// memDB is an in-memory DB test double (Snapshot/Restore over a byte slice).
type memDB struct {
	mu  sync.Mutex
	buf []byte
}

func (d *memDB) set(b []byte) { d.mu.Lock(); d.buf = b; d.mu.Unlock() }
func (d *memDB) get() []byte  { d.mu.Lock(); defer d.mu.Unlock(); return d.buf }

func (d *memDB) Snapshot(context.Context) ([]byte, error)  { return d.get(), nil }
func (d *memDB) Restore(_ context.Context, b []byte) error { d.set(b); return nil }

// errString is a trivial error used by membership_test's failing-source fixture.
type errString string

func (e errString) Error() string { return string(e) }
