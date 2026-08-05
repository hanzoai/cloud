package org

import (
	"context"
	"fmt"
	"github.com/hanzoai/vfs/replica"
)

// vfsClient is the subset of hanzoai/vfs (deps.VFS / types.VFSClient) the store
// adapter needs. Declared LOCALLY so package org has ZERO external dependencies
// and its tests run pure — types.VFSClient satisfies this structurally, so
// `org.NewVFSStore(deps.VFS)` compiles at the call site with no import here.
type vfsClient interface {
	Put(ctx context.Context, key string, payload []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// vfsStore adapts hanzoai/vfs to the Store surface. This is the ONLY object-store
// binding — SeaweedFS via vfs, no third-party S3 SDK. Versioning is
// content-addressable (SHA-256 of the payload), matching vfs's model.
type vfsStore struct{ vfs vfsClient }

// NewVFSStore builds a Store over the native vfs client (pass deps.VFS).
func NewVFSStore(vfs vfsClient) Store { return &vfsStore{vfs: vfs} }

func (s *vfsStore) Put(ctx context.Context, key string, data []byte) error {
	if s.vfs == nil {
		return fmt.Errorf("org: vfs client not configured")
	}
	return s.vfs.Put(ctx, key, data)
}

func (s *vfsStore) Get(ctx context.Context, key string) ([]byte, string, error) {
	if s.vfs == nil {
		return nil, "", fmt.Errorf("org: vfs client not configured")
	}
	data, err := s.vfs.Get(ctx, key)
	if err != nil {
		return nil, "", err
	}
	return data, replica.Version(data), nil
}

func (s *vfsStore) Version(ctx context.Context, key string) (string, error) {
	if s.vfs == nil {
		return "", fmt.Errorf("org: vfs client not configured")
	}
	data, err := s.vfs.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return replica.Version(data), nil
}
