package domain

import (
	"sort"
	"strings"
	"sync"
)

// MemStore is an in-memory Store: a concurrency-safe map keyed by (org, domain). It is
// the default ownership store — sufficient for a single-process deployment and every
// test. A multi-replica deployment swaps in a SQLite/Postgres-backed Store with the
// SAME interface (the clients/finance per-org ledger pattern); nothing else changes.
type MemStore struct {
	mu   sync.RWMutex
	rows map[string]Record // key = org + "\x00" + domain
}

// NewMemStore builds an empty in-memory store.
func NewMemStore() *MemStore { return &MemStore{rows: map[string]Record{}} }

func memKey(org, domainName string) string {
	return strings.ToLower(org) + "\x00" + strings.ToLower(domainName)
}

// Put upserts a record.
func (s *MemStore) Put(rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[memKey(rec.Org, rec.Domain)] = rec
	return nil
}

// Get returns the record for (org, domain) and whether it exists.
func (s *MemStore) Get(org, domainName string) (Record, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.rows[memKey(org, domainName)]
	return rec, ok, nil
}

// ListByOrg returns every domain the org holds, newest registration first.
func (s *MemStore) ListByOrg(org string) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix := strings.ToLower(org) + "\x00"
	out := make([]Record, 0)
	for k, rec := range s.rows {
		if strings.HasPrefix(k, prefix) {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RegisteredAt > out[j].RegisteredAt })
	return out, nil
}
