package kmstest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// Mem holds secrets by ref.
type Mem struct {
	mu     sync.Mutex
	values map[string][]byte
	reads  int
}

// New is an empty KMS.
func New() *Mem { return &Mem{values: map[string][]byte{}} }

// admit refuses a tenant path asked for by a call acting for another org.
func (m *Mem) admit(ctx context.Context, ref string) error {
	org := zip.CallerOf(ctx).Org
	if rest, ok := strings.CutPrefix(ref, "orgs/"); ok {
		slug, _, _ := strings.Cut(rest, "/")
		if slug != namespace.Sanitize(org) {
			return fmt.Errorf("kmstest: %s belongs to another org than %q", ref, org)
		}
	}
	return nil
}

// GetSecret reads one secret.
func (m *Mem) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	if err := m.admit(ctx, ref); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	v, ok := m.values[ref]
	if !ok {
		return nil, secret.ErrNone
	}
	return append([]byte(nil), v...), nil
}

// PutSecret writes one secret.
func (m *Mem) PutSecret(ctx context.Context, ref string, value []byte) error {
	if err := m.admit(ctx, ref); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[ref] = append([]byte(nil), value...)
	return nil
}

// DeleteSecret forgets one secret.
func (m *Mem) DeleteSecret(ctx context.Context, ref string) error {
	if err := m.admit(ctx, ref); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.values[ref]; !ok {
		return secret.ErrNone
	}
	delete(m.values, ref)
	return nil
}

// Sign is not a thing this KMS does.
func (m *Mem) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, fmt.Errorf("kmstest: no signing")
}

// Held is every secret, by ref, as a test reads what KMS keeps.
func (m *Mem) Held() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.values))
	for k, v := range m.values {
		out[k] = string(v)
	}
	return out
}

// Refs is the refs KMS holds, sorted.
func (m *Mem) Refs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.values))
	for k := range m.values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Reads is how many times a secret was asked for.
func (m *Mem) Reads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reads
}
