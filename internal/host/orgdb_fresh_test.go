package host

// orgdb_fresh_test.go — A POD ON AN EMPTY DISK SEES EVERY STORE THE DEPLOYMENT HAS.
//
// On the durable plane a pod's disk is a cache: a new pod (an emptyDir) holds no
// store until it opens one. Has, Stored and the sweeps used to read only the
// disk, so such a pod answered "nothing here" for an org whose data sat in the
// object store, and swept nothing until each org's next request opened it.

import (
	"context"
	"slices"
	"strings"
)

// List makes the in-memory store a Lister, as the S3 store is.
func (s *recordingStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys, nil
}
