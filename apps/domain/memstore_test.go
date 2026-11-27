package domain

import (
	"sort"
	"strings"
	"sync"
)

// MemStore is an in-memory Store: a concurrency-safe map keyed by (org, domain). It is
// the Service's test double, so a policy test runs with no database; the deployment's
// store is Holdings (store.go), which the store's own tests hold to the same rules.
type MemStore struct {
	mu   sync.RWMutex
	rows map[string]Holding // key = org + "\x00" + domain
}

// NewMemStore builds an empty in-memory store.
func NewMemStore() *MemStore { return &MemStore{rows: map[string]Holding{}} }

// memKey keys a holding on its org EXACTLY and its domain case-folded: a domain
// name is case-insensitive (RFC 4343) and an org name is not, so `Acme` and
// `acme` are two orgs and hold nothing of each other's.
func memKey(org, domainName string) string {
	return org + "\x00" + strings.ToLower(domainName)
}

// Put upserts a record.
func (s *MemStore) Put(rec Holding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[memKey(rec.Org, rec.Domain)] = rec
	return nil
}

// Get returns the record for (org, domain) and whether it exists.
func (s *MemStore) Get(org, domainName string) (Holding, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.rows[memKey(org, domainName)]
	return rec, ok, nil
}

// ListByOrg returns every domain the org holds, newest registration first.
func (s *MemStore) ListByOrg(org string) ([]Holding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix := org + "\x00"
	out := make([]Holding, 0)
	for k, rec := range s.rows {
		if strings.HasPrefix(k, prefix) {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RegisteredAt > out[j].RegisteredAt })
	return out, nil
}
