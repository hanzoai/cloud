package identity

import (
	"sync"
	"github.com/ethereum/go-ethereum/common"
)

// Simple in-memory store for the prototype, intended to be backed by cek/ SQLite
type Store struct {
	mu          sync.RWMutex
	agents      map[common.Address]*Agent
	delegations map[string]*Delegation
}

func NewStore() *Store {
	return &Store{
		agents:      make(map[common.Address]*Agent),
		delegations: make(map[string]*Delegation),
	}
}

func (s *Store) RegisterAgent(a *Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agents[a.Address] = a
}

func (s *Store) SaveDelegation(d *Delegation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delegations[d.ID] = d
}

func (s *Store) GetDelegation(id string) (*Delegation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.delegations[id]
	return d, ok
}
