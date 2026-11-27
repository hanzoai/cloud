package host

// orgdb_evict_test.go — THE OPEN SET IS BOUNDED, AND AN EVICTION IS A DRAIN.
//
// OrgStore held every entity it was ever asked for, for the life of the process:
// no eviction, no ceiling, and CloseAll — the only thing that closed anything —
// terminal and reached only at shutdown. So a host's resident cost tracked how
// many tenants had EVER touched it rather than how many were active, which is the
// opposite of the property the durable plane exists to buy. An org's state is
// re-derivable from the object store; that is what makes a pod disposable, and
// keeping every org open forever spends it.
//
// Bounding it is the easy half. The half that has to be right is what an eviction
// DOES: it takes the same path a graceful drain takes — the Durable's own Close,
// which ships the final state fenced at the lease round and releases ownership —
// because a handle closed out from under the fence strands the org on a replica
// that is no longer serving it.

import (
	"context"
	"fmt"
	"sync"

	"github.com/hanzoai/vfs/replica"
)

// recordingStore is a ConditionalStore in memory that COUNTS writes, which is how
// the eviction's ship is observed. It implements the CAS contract faithfully —
// version mismatch is a conflict — because the fence is what makes a racing
// re-open safe and a store that accepted every put would hide a broken one.
type recordingStore struct {
	mu      sync.Mutex
	objects map[string]versioned
	puts    int
	seq     int
}

type versioned struct {
	data    []byte
	version string
}

func (s *recordingStore) Get(_ context.Context, key string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if !ok {
		return nil, "", fmt.Errorf("recordingStore get %q: %w", key, replica.ErrNotFound)
	}
	return o.data, o.version, nil
}

func (s *recordingStore) PutIfVersion(_ context.Context, key string, data []byte, expect string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, exists := s.objects[key]
	switch {
	case expect == "" && exists:
		return "", fmt.Errorf("recordingStore put %q: %w", key, replica.ErrConflict)
	case expect != "" && (!exists || cur.version != expect):
		return "", fmt.Errorf("recordingStore put %q: %w", key, replica.ErrConflict)
	}
	s.seq++
	s.puts++
	v := fmt.Sprintf("v%d", s.seq)
	s.objects[key] = versioned{data: append([]byte(nil), data...), version: v}
	return v, nil
}

func (s *recordingStore) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}
