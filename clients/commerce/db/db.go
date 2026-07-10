// Package db provides a multi-layer database abstraction supporting:
// - User-level SQLite with sqlite-vec for personal data and vector search
// - Organization-level SQLite for shared tenant data
// - Hanzo Datastore (DATASTORE_URL) for deep analytics and parallel queries
//
// Architecture:
//
//	┌─────────────────────────────────────────────────────────────┐
//	│                      Query Layer                            │
//	├─────────────────────────────────────────────────────────────┤
//	│  User SQLite    │   Org SQLite    │    Hanzo Datastore      │
//	│  (per-user)     │   (per-org)     │    (DATASTORE_URL)      │
//	│  + sqlite-vec   │   + sqlite-vec  │    (parallel queries)   │
//	│  Fast queries   │   Shared data   │    Deep analytics       │
//	└─────────────────────────────────────────────────────────────┘
package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNoSuchEntity is returned when an entity is not found
	ErrNoSuchEntity = errors.New("db: no such entity")

	// ErrInvalidKey is returned when a key is invalid
	ErrInvalidKey = errors.New("db: invalid key")

	// ErrInvalidEntityType is returned when an entity type is invalid
	ErrInvalidEntityType = errors.New("db: invalid entity type")

	// ErrConcurrentModification is returned when optimistic locking fails
	ErrConcurrentModification = errors.New("db: concurrent modification")

	// ErrDatabaseClosed is returned when operating on a closed database
	ErrDatabaseClosed = errors.New("db: database closed")
)

// Layer represents which database layer to use
type Layer int

const (
	// LayerUser uses the user-specific SQLite database
	LayerUser Layer = iota

	// LayerOrg uses the organization-level SQLite database
	LayerOrg

	// LayerDatastore uses the Hanzo Datastore (ClickHouse) for analytics
	LayerDatastore

	// LayerAll queries all layers (for cross-cutting queries)
	LayerAll
)

// Config holds database configuration options
type Config struct {
	// DataDir is the base directory for data storage
	DataDir string

	// UserDataDir is the directory for per-user SQLite databases
	// Defaults to DataDir/users
	UserDataDir string

	// OrgDataDir is the directory for per-org SQLite databases
	// Defaults to DataDir/orgs
	OrgDataDir string

	// DatastoreDSN is the connection string for Hanzo Datastore (DATASTORE_URL)
	DatastoreDSN string

	// EnableDatastore enables the Hanzo Datastore layer
	EnableDatastore bool

	// EnableVectorSearch enables sqlite-vec for vector embeddings
	EnableVectorSearch bool

	// VectorDimensions is the default dimension for vector embeddings
	VectorDimensions int

	// SQLite configuration
	SQLite SQLiteConfig

	// Datastore configuration (Hanzo Datastore)
	Datastore DatastoreConfig

	// IsDev enables development mode logging
	IsDev bool
}

// SQLiteConfig holds SQLite-specific configuration
type SQLiteConfig struct {
	// MaxOpenConns for concurrent reads
	MaxOpenConns int

	// MaxIdleConns for connection pooling
	MaxIdleConns int

	// BusyTimeout in milliseconds before giving up on locked DB
	BusyTimeout int

	// JournalMode (WAL recommended for concurrency)
	JournalMode string

	// Synchronous mode (NORMAL for balance of safety/speed)
	Synchronous string

	// CacheSize in KB (negative = KB, positive = pages)
	CacheSize int

	// QueryTimeout for SELECT queries
	QueryTimeout time.Duration
}

// DatastoreConfig holds Hanzo Datastore (ClickHouse) configuration
type DatastoreConfig struct {
	// MaxOpenConns for parallel queries
	MaxOpenConns int

	// MaxIdleConns for connection pooling
	MaxIdleConns int

	// ConnMaxLifetime for connection recycling
	ConnMaxLifetime time.Duration

	// Compression method (lz4, zstd, etc.)
	Compression string

	// QueryTimeout for datastore queries
	QueryTimeout time.Duration
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		DataDir:            "./data",
		EnableDatastore:    false,
		EnableVectorSearch: true,
		VectorDimensions:   1536, // OpenAI ada-002 dimensions
		SQLite: SQLiteConfig{
			MaxOpenConns: 120,
			MaxIdleConns: 15,
			BusyTimeout:  10000, // 10 seconds
			JournalMode:  "WAL",
			Synchronous:  "NORMAL",
			CacheSize:    -16000, // 16MB
			QueryTimeout: 30 * time.Second,
		},
		Datastore: DatastoreConfig{
			MaxOpenConns:    25,
			MaxIdleConns:    5,
			ConnMaxLifetime: time.Hour,
			Compression:     "lz4",
			QueryTimeout:    60 * time.Second,
		},
		IsDev: false,
	}
}

// Manager is the main entry point for database operations.
// It manages multiple database layers and provides unified access.
type Manager struct {
	config *Config
	mu     sync.RWMutex

	// User databases (userID -> DB)
	userDBs map[string]*SQLiteDB

	// Organization databases (orgID -> DB)
	orgDBs map[string]*SQLiteDB

	// Hanzo Datastore (shared)
	datastoreDB Datastore

	// masterKey is the 32-byte KMS-sourced at-rest encryption master key
	// (COMMERCE_KMS_MASTER_KEY). Nil => per-tenant SQLite files are unencrypted
	// (dev/CI). Resolved once in NewManager and passed to every tenant store.
	masterKey []byte

	// Closed flag
	closed bool
}

// NewManager creates a new database manager
func NewManager(cfg *Config) (*Manager, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	if cfg.UserDataDir == "" {
		cfg.UserDataDir = cfg.DataDir + "/users"
	}
	if cfg.OrgDataDir == "" {
		cfg.OrgDataDir = cfg.DataDir + "/orgs"
	}

	// Decide at-rest encryption posture ONCE, from COMMERCE_KMS_MASTER_KEY. On a
	// non-encrypting (pure-Go) build this hard-errors when the key is set rather
	// than silently persisting plaintext money data.
	masterKey, err := resolveMasterKey()
	if err != nil {
		return nil, err
	}

	m := &Manager{
		config:    cfg,
		userDBs:   make(map[string]*SQLiteDB),
		orgDBs:    make(map[string]*SQLiteDB),
		masterKey: masterKey,
	}

	// Initialize Hanzo Datastore if enabled
	if cfg.EnableDatastore && cfg.DatastoreDSN != "" {
		datastore, err := NewDatastore(cfg)
		if err != nil {
			return nil, err
		}
		m.datastoreDB = datastore
	}

	return m, nil
}

// isSafeTenantID reports whether id is safe to use as a single filesystem path
// segment for a per-tenant SQLite store. The tenant id is the org (or user)
// namespace, which derives from a request-supplied — gateway/JWT-verified —
// owner/subject value, so it must never be able to escape the data dir (e.g.
// "..", "a/b", "/etc/passwd"). Reject anything that is not a clean single
// segment; callers fail closed on the returned error.
func isSafeTenantID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return false
	}
	// Leading-dot names (dotfiles / hidden dirs) are rejected — an org "."-prefixed
	// name shouldn't be able to create a hidden per-tenant dir (Red LOW-1). This is
	// pure path-safety, so it also applies to the app's own Org() calls (which
	// never use dot-names). The RESERVED-namespace policy (system/admin/default) is
	// intentionally NOT here: this function guards BOTH untrusted tenant requests
	// AND the app's legitimate Manager.Org("system") systemDB-fallback + test
	// harness, so rejecting "system" here would break boot. That policy lives at
	// the tenant boundary — datastore.NewNamespaced.
	if strings.HasPrefix(id, ".") {
		return false
	}
	return true
}

// User returns the database for a specific user
func (m *Manager) User(userID string) (DB, error) {
	if !isSafeTenantID(userID) {
		return nil, fmt.Errorf("db: unsafe user id %q", userID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, ErrDatabaseClosed
	}

	if db, ok := m.userDBs[userID]; ok {
		return db, nil
	}

	// Create new user database
	db, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:               m.config.UserDataDir + "/" + userID + "/data.db",
		Config:             m.config.SQLite,
		EnableVectorSearch: m.config.EnableVectorSearch,
		VectorDimensions:   m.config.VectorDimensions,
		TenantID:           userID,
		TenantType:         "user",
		MasterKey:          m.masterKey,
	})
	if err != nil {
		return nil, err
	}

	m.userDBs[userID] = db
	return db, nil
}

// Org returns the database for a specific organization
func (m *Manager) Org(orgID string) (DB, error) {
	if !isSafeTenantID(orgID) {
		return nil, fmt.Errorf("db: unsafe org id %q", orgID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, ErrDatabaseClosed
	}

	if db, ok := m.orgDBs[orgID]; ok {
		return db, nil
	}

	// Create new org database
	db, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:               m.config.OrgDataDir + "/" + orgID + "/data.db",
		Config:             m.config.SQLite,
		EnableVectorSearch: m.config.EnableVectorSearch,
		VectorDimensions:   m.config.VectorDimensions,
		TenantID:           orgID,
		TenantType:         "org",
		MasterKey:          m.masterKey,
	})
	if err != nil {
		return nil, err
	}

	m.orgDBs[orgID] = db
	return db, nil
}

// Datastore returns the Hanzo Datastore for deep analytics queries
func (m *Manager) Datastore() Datastore {
	return m.datastoreDB
}

// Encrypted reports whether per-tenant SQLite files are SQLCipher-encrypted at
// rest (i.e. COMMERCE_KMS_MASTER_KEY was supplied and this build can encrypt).
func (m *Manager) Encrypted() bool {
	return m.masterKey != nil
}

// Close closes all database connections
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}
	m.closed = true

	var lastErr error

	// Close user databases
	for _, db := range m.userDBs {
		if err := db.Close(); err != nil {
			lastErr = err
		}
	}

	// Close org databases
	for _, db := range m.orgDBs {
		if err := db.Close(); err != nil {
			lastErr = err
		}
	}

	// Close Hanzo Datastore
	if m.datastoreDB != nil {
		if err := m.datastoreDB.Close(); err != nil {
			lastErr = err
		}
	}

	return lastErr
}

// DB is the main database interface for user/org SQLite databases
type DB interface {
	// Core operations
	Get(ctx context.Context, key Key, dst interface{}) error
	Put(ctx context.Context, key Key, src interface{}) (Key, error)
	Delete(ctx context.Context, key Key) error

	// Batch operations
	GetMulti(ctx context.Context, keys []Key, dst interface{}) error
	PutMulti(ctx context.Context, keys []Key, src interface{}) ([]Key, error)
	DeleteMulti(ctx context.Context, keys []Key) error

	// Query
	Query(kind string) Query

	// Vector search (sqlite-vec)
	VectorSearch(ctx context.Context, opts *VectorSearchOptions) ([]VectorResult, error)
	PutVector(ctx context.Context, kind string, id string, vector []float32, metadata map[string]interface{}) error

	// Key management
	NewKey(kind string, stringID string, intID int64, parent Key) Key
	NewIncompleteKey(kind string, parent Key) Key
	AllocateIDs(kind string, parent Key, n int) ([]Key, error)

	// Transactions
	RunInTransaction(ctx context.Context, fn func(tx Transaction) error, opts *TransactionOptions) error

	// Lifecycle
	Close() error

	// Tenant info
	TenantID() string
	TenantType() string
}

// Datastore is the interface for Hanzo Datastore (ClickHouse) analytics queries
type Datastore interface {
	// Query executes datastore queries
	Query(ctx context.Context, query string, args ...interface{}) (DatastoreRows, error)

	// Select scans results into a destination slice
	Select(ctx context.Context, dest interface{}, query string, args ...interface{}) error

	// Exec executes a non-query statement
	Exec(ctx context.Context, query string, args ...interface{}) error

	// Batch insert for high-throughput data ingestion
	PrepareBatch(ctx context.Context, query string) (DatastoreBatch, error)

	// AsyncInsert for fire-and-forget event logging
	AsyncInsert(ctx context.Context, query string, wait bool, args ...interface{}) error

	// Close closes the datastore connection
	Close() error
}

// DatastoreRows represents datastore query results
type DatastoreRows interface {
	Next() bool
	Scan(dest ...interface{}) error
	ScanStruct(dest interface{}) error
	Columns() []string
	Close() error
	Err() error
}

// DatastoreBatch for bulk inserts into Hanzo Datastore
type DatastoreBatch interface {
	Append(v ...interface{}) error
	AppendStruct(v interface{}) error
	Flush() error
	Send() error
	Abort() error
	Rows() int
	Close() error
}

// VectorSearchOptions configures vector similarity search
type VectorSearchOptions struct {
	// Kind is the entity type to search
	Kind string

	// Vector is the query vector
	Vector []float32

	// Limit is the maximum number of results
	Limit int

	// MinScore filters results below this similarity score
	MinScore float32

	// Filters are additional SQL conditions
	Filters map[string]interface{}
}

// VectorResult represents a vector search result
type VectorResult struct {
	// ID is the entity identifier
	ID string

	// Score is the similarity score (0-1, higher is more similar)
	Score float32

	// Metadata is additional data stored with the vector
	Metadata map[string]interface{}
}

// Transaction represents a database transaction
type Transaction interface {
	Get(key Key, dst interface{}) error
	Put(key Key, src interface{}) (Key, error)
	Delete(key Key) error
	Query(kind string) Query
}

// TransactionOptions configures transaction behavior
type TransactionOptions struct {
	// ReadOnly indicates this is a read-only transaction
	ReadOnly bool

	// MaxAttempts for retries on conflict
	MaxAttempts int

	// Isolation level (for SQL databases)
	Isolation IsolationLevel
}

// IsolationLevel represents transaction isolation levels
type IsolationLevel int

const (
	IsolationDefault IsolationLevel = iota
	IsolationReadUncommitted
	IsolationReadCommitted
	IsolationRepeatableRead
	IsolationSerializable
)

// Key represents a unique identifier for an entity
type Key interface {
	// Kind returns the entity kind/table name
	Kind() string

	// StringID returns the string identifier (if any)
	StringID() string

	// IntID returns the integer identifier (if any)
	IntID() int64

	// Parent returns the parent key (for hierarchical keys)
	Parent() Key

	// Namespace returns the namespace/tenant
	Namespace() string

	// Incomplete returns true if this key needs an ID assigned
	Incomplete() bool

	// Encode returns an encoded string representation
	Encode() string

	// Equal checks if two keys are the same
	Equal(other Key) bool
}

// Query provides a fluent interface for querying entities
type Query interface {
	// Filtering
	Filter(filterStr string, value interface{}) Query
	FilterField(fieldPath string, op string, value interface{}) Query

	// Ordering
	Order(fieldPath string) Query
	OrderDesc(fieldPath string) Query

	// Pagination
	Limit(limit int) Query
	Offset(offset int) Query

	// Projection
	Project(fieldNames ...string) Query
	Distinct() Query

	// Ancestor queries (for hierarchical data)
	Ancestor(ancestor Key) Query

	// Execution
	GetAll(ctx context.Context, dst interface{}) ([]Key, error)
	First(ctx context.Context, dst interface{}) (Key, error)
	Count(ctx context.Context) (int, error)
	Keys(ctx context.Context) ([]Key, error)
	Run(ctx context.Context) Iterator

	// Cursors for pagination
	Start(cursor Cursor) Query
	End(cursor Cursor) Query
}

// Iterator allows iterating over query results
type Iterator interface {
	Next(dst interface{}) (Key, error)
	Cursor() (Cursor, error)
}

// Cursor represents a position in a result set
type Cursor interface {
	String() string
}

// Entity is the interface that all model entities should implement
type Entity interface {
	// Kind returns the entity kind/table name
	Kind() string
}

// Syncable entities can be synced to Hanzo Datastore
type Syncable interface {
	Entity

	// SyncToDatastore returns true if this entity should be synced
	SyncToDatastore() bool
}
