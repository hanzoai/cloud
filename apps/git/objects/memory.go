package objects

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Memory is a Blobs that keeps its objects in a map.
type Memory struct {
	mu   sync.Mutex
	objs map[string]item
	// Now stamps writes; a test sets it to age an object past a grace period.
	Now func() time.Time
	// Gets and Puts count the requests, which is how a test shows what a read cost.
	Gets, Puts atomic.Int64
	// Fail makes every Put store its bytes and then report an error: the crash between an
	// upload and a commit is an upload that landed and a caller that never heard.
	Fail atomic.Bool
}

type item struct {
	b   []byte
	mod time.Time
}

// NewMemory is an empty Memory.
func NewMemory() *Memory { return &Memory{objs: map[string]item{}, Now: time.Now} }

var _ Blobs = (*Memory)(nil)

// Put stores the bytes; with Fail set it stores them and then reports the failure, as a
// network that drops the answer does.
func (m *Memory) Put(_ context.Context, key string, r io.Reader, size int64) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if size >= 0 && int64(len(b)) != size {
		return fmt.Errorf("blobtest: %s: wrote %d of %d bytes", key, len(b), size)
	}
	m.Puts.Add(1)
	m.mu.Lock()
	m.objs[key] = item{b: b, mod: m.Now()}
	m.mu.Unlock()
	if m.Fail.Load() {
		return errors.New("objects: injected upload failure")
	}
	return nil
}

func (m *Memory) Range(_ context.Context, key string, off, n int64) (io.ReadCloser, error) {
	m.Gets.Add(1)
	m.mu.Lock()
	it, ok := m.objs[key]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoBlob, key)
	}
	off = min(off, int64(len(it.b)))
	end := min(off+n, int64(len(it.b)))
	return io.NopCloser(bytes.NewReader(it.b[off:end])), nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.objs, key)
	m.mu.Unlock()
	return nil
}

func (m *Memory) List(_ context.Context, prefix string, fn func(Blob) error) error {
	m.mu.Lock()
	var out []Blob
	for k, it := range m.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, Blob{Key: k, Size: int64(len(it.b)), Modified: it.mod})
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	for _, b := range out {
		if err := fn(b); err != nil {
			return err
		}
	}
	return nil
}

// Keys is every key, sorted.
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ks []string
	for k := range m.objs {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Bytes is the stored size of every object.
func (m *Memory) Bytes() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, it := range m.objs {
		n += int64(len(it.b))
	}
	return n
}

// Get is the whole object at key, nil when it is not there.
func (m *Memory) Get(key string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objs[key].b
}
