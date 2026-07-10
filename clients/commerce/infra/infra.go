// Package infra provides unified infrastructure clients for Commerce.
//
// This package integrates various backend services:
//   - Vector: Embeddings and semantic search (Qdrant)
//   - KV: Key-value cache and sessions (hanzo/base, per-org/user SQLite)
//   - Storage: Object storage for assets (S3_URL, S3-compatible)
//   - Search: Full-text search (Meilisearch)
//   - PubSub: Event streaming (NATS)
//   - Tasks: Workflow orchestration (Temporal)
//
// Architecture:
//
//	+------------------------------------------------------------------+
//	|                     Infrastructure Manager                        |
//	+------------------------------------------------------------------+
//	|  Vector  |   KV     |  Storage  |  Search  |  PubSub  |  Tasks   |
//	+------------------------------------------------------------------+
package infra

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrNotConfigured is returned when a service is not configured
	ErrNotConfigured = errors.New("infra: service not configured")

	// ErrNotConnected is returned when a service is not connected
	ErrNotConnected = errors.New("infra: service not connected")

	// ErrConnectionFailed is returned when a connection attempt fails
	ErrConnectionFailed = errors.New("infra: connection failed")

	// ErrTimeout is returned when an operation times out
	ErrTimeout = errors.New("infra: operation timeout")

	// ErrClosed is returned when operating on closed infrastructure
	ErrClosed = errors.New("infra: infrastructure closed")
)

// Config holds configuration for all infrastructure services
type Config struct {
	// Vector (Qdrant) configuration
	Vector VectorConfig

	// KV (hanzo/base, per-org/user SQLite) configuration
	KV KVConfig

	// Storage (MinIO) configuration
	Storage StorageConfig

	// Search (Meilisearch) configuration
	Search SearchConfig

	// PubSub (NATS) configuration
	PubSub PubSubConfig

	// Tasks (Temporal) configuration
	Tasks TasksConfig

	// Global settings
	ConnectTimeout time.Duration
	RetryAttempts  int
	RetryDelay     time.Duration
}

// DefaultConfig returns a default configuration for local development
func DefaultConfig() *Config {
	return &Config{
		Vector: VectorConfig{
			Enabled: false,
			Host:    "localhost",
			Port:    6333,
		},
		KV: KVConfig{
			Enabled: false,
		},
		Storage: StorageConfig{
			Enabled:   false,
			Endpoint:  "localhost:9000",
			AccessKey: "minioadmin",
			SecretKey: "minioadmin",
			UseSSL:    false,
			Bucket:    "commerce",
		},
		Search: SearchConfig{
			Enabled: false,
			Host:    "http://localhost:7700",
			APIKey:  "",
		},
		PubSub: PubSubConfig{
			Enabled: false,
			URL:     "nats://localhost:4222",
		},
		Tasks: TasksConfig{
			Enabled:   false,
			HostPort:  "localhost:7233",
			Namespace: "commerce",
		},
		ConnectTimeout: 10 * time.Second,
		RetryAttempts:  3,
		RetryDelay:     time.Second,
	}
}

// Manager manages all infrastructure connections
type Manager struct {
	config *Config
	mu     sync.RWMutex

	// Service clients
	vector  *VectorClient
	kv      *KVClient
	storage *StorageClient
	search  *SearchClient
	pubsub  *PubSubClient
	tasks   *TasksClient

	// State
	closed bool
}

// New creates a new infrastructure manager
func New(cfg *Config) *Manager {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	return &Manager{
		config: cfg,
	}
}

// Connect establishes connections to all enabled services
func (m *Manager) Connect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return ErrClosed
	}

	var errs []error

	// Connect to Vector (Qdrant)
	if m.config.Vector.Enabled {
		client, err := NewVectorClient(ctx, &m.config.Vector)
		if err != nil {
			errs = append(errs, fmt.Errorf("vector: %w", err))
		} else {
			m.vector = client
		}
	}

	// Connect to KV (hanzo/base). When the caller has already attached a
	// shared-store KV client via SetKV, reuse it — opening a second base app
	// on the same SQLite files would be a footgun. Otherwise open a dedicated
	// store from the KV config.
	if m.config.KV.Enabled && m.kv == nil {
		client, err := NewKVClient(ctx, &m.config.KV)
		if err != nil {
			errs = append(errs, fmt.Errorf("kv: %w", err))
		} else {
			m.kv = client
		}
	}

	// Connect to Storage (MinIO)
	if m.config.Storage.Enabled {
		client, err := NewStorageClient(ctx, &m.config.Storage)
		if err != nil {
			errs = append(errs, fmt.Errorf("storage: %w", err))
		} else {
			m.storage = client
		}
	}

	// Connect to Search (Meilisearch)
	if m.config.Search.Enabled {
		client, err := NewSearchClient(ctx, &m.config.Search)
		if err != nil {
			errs = append(errs, fmt.Errorf("search: %w", err))
		} else {
			m.search = client
		}
	}

	// Connect to PubSub (NATS)
	if m.config.PubSub.Enabled {
		client, err := NewPubSubClient(ctx, &m.config.PubSub)
		if err != nil {
			errs = append(errs, fmt.Errorf("pubsub: %w", err))
		} else {
			m.pubsub = client
		}
	}

	// Connect to Tasks (Temporal)
	if m.config.Tasks.Enabled {
		client, err := NewTasksClient(ctx, &m.config.Tasks)
		if err != nil {
			errs = append(errs, fmt.Errorf("tasks: %w", err))
		} else {
			m.tasks = client
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("infra connect errors: %v", errs)
	}

	return nil
}

// Vector returns the Qdrant vector client
func (m *Manager) Vector() (*VectorClient, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, ErrClosed
	}
	if !m.config.Vector.Enabled {
		return nil, ErrNotConfigured
	}
	if m.vector == nil {
		return nil, ErrNotConnected
	}
	return m.vector, nil
}

// SetKV attaches a KV client backed by a caller-owned base store. It must be
// called before Connect so Connect reuses it instead of opening a second base
// app on the same SQLite files. The Manager does NOT take ownership: Close
// leaves a SetKV-attached client's store alone (NewKVClientFromStore wires
// this so Close is a no-op there).
func (m *Manager) SetKV(client *KVClient) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kv = client
}

// KV returns the base-backed KV client
func (m *Manager) KV() (*KVClient, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, ErrClosed
	}
	if !m.config.KV.Enabled {
		return nil, ErrNotConfigured
	}
	if m.kv == nil {
		return nil, ErrNotConnected
	}
	return m.kv, nil
}

// Storage returns the MinIO storage client
func (m *Manager) Storage() (*StorageClient, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, ErrClosed
	}
	if !m.config.Storage.Enabled {
		return nil, ErrNotConfigured
	}
	if m.storage == nil {
		return nil, ErrNotConnected
	}
	return m.storage, nil
}

// Search returns the Meilisearch client
func (m *Manager) Search() (*SearchClient, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, ErrClosed
	}
	if !m.config.Search.Enabled {
		return nil, ErrNotConfigured
	}
	if m.search == nil {
		return nil, ErrNotConnected
	}
	return m.search, nil
}

// PubSub returns the NATS pubsub client
func (m *Manager) PubSub() (*PubSubClient, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, ErrClosed
	}
	if !m.config.PubSub.Enabled {
		return nil, ErrNotConfigured
	}
	if m.pubsub == nil {
		return nil, ErrNotConnected
	}
	return m.pubsub, nil
}

// Tasks returns the Temporal tasks client
func (m *Manager) Tasks() (*TasksClient, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, ErrClosed
	}
	if !m.config.Tasks.Enabled {
		return nil, ErrNotConfigured
	}
	if m.tasks == nil {
		return nil, ErrNotConnected
	}
	return m.tasks, nil
}

// Health checks the health of all connected services
func (m *Manager) Health(ctx context.Context) map[string]HealthStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := make(map[string]HealthStatus)

	if m.vector != nil {
		status["vector"] = m.vector.Health(ctx)
	}
	if m.kv != nil {
		status["kv"] = m.kv.Health(ctx)
	}
	if m.storage != nil {
		status["storage"] = m.storage.Health(ctx)
	}
	if m.search != nil {
		status["search"] = m.search.Health(ctx)
	}
	if m.pubsub != nil {
		status["pubsub"] = m.pubsub.Health(ctx)
	}
	if m.tasks != nil {
		status["tasks"] = m.tasks.Health(ctx)
	}

	return status
}

// Close closes all infrastructure connections
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}
	m.closed = true

	var errs []error

	if m.vector != nil {
		if err := m.vector.Close(); err != nil {
			errs = append(errs, fmt.Errorf("vector: %w", err))
		}
	}
	if m.kv != nil {
		if err := m.kv.Close(); err != nil {
			errs = append(errs, fmt.Errorf("kv: %w", err))
		}
	}
	if m.storage != nil {
		// MinIO client doesn't need explicit close
	}
	if m.search != nil {
		// Meilisearch client doesn't need explicit close
	}
	if m.pubsub != nil {
		if err := m.pubsub.Close(); err != nil {
			errs = append(errs, fmt.Errorf("pubsub: %w", err))
		}
	}
	if m.tasks != nil {
		m.tasks.Close()
	}

	if len(errs) > 0 {
		return fmt.Errorf("infra close errors: %v", errs)
	}

	return nil
}

// HealthStatus represents the health of a service
type HealthStatus struct {
	Healthy bool          `json:"healthy"`
	Latency time.Duration `json:"latency"`
	Error   string        `json:"error,omitempty"`
}
