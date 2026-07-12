package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	datastore "github.com/hanzo-ds/go"
	"github.com/hanzo-ds/go/lib/driver"
)

// NewDatastore creates a new Hanzo Datastore connection
// This connects to datastore via hanzo/datastore-go for deep analytics
func NewDatastore(cfg *Config) (Datastore, error) {
	if cfg.DatastoreDSN == "" {
		return nil, errors.New("db: DatastoreDSN is required for Hanzo Datastore")
	}

	// Parse DSN to get options
	opts, err := datastore.ParseDSN(cfg.DatastoreDSN)
	if err != nil {
		return nil, fmt.Errorf("db: failed to parse datastore DSN: %w", err)
	}

	// Apply configuration overrides
	if cfg.Datastore.MaxOpenConns > 0 {
		opts.MaxOpenConns = cfg.Datastore.MaxOpenConns
	}
	if cfg.Datastore.MaxIdleConns > 0 {
		opts.MaxIdleConns = cfg.Datastore.MaxIdleConns
	}
	if cfg.Datastore.ConnMaxLifetime > 0 {
		opts.ConnMaxLifetime = cfg.Datastore.ConnMaxLifetime
	}

	// Set compression based on config
	if cfg.Datastore.Compression != "" {
		switch cfg.Datastore.Compression {
		case "lz4":
			opts.Compression = &datastore.Compression{Method: datastore.CompressionLZ4}
		case "zstd":
			opts.Compression = &datastore.Compression{Method: datastore.CompressionZSTD}
		case "none":
			opts.Compression = &datastore.Compression{Method: datastore.CompressionNone}
		}
	}

	// Enable debug logging in dev mode
	if cfg.IsDev {
		opts.Debug = true
	}

	// Open connection
	conn, err := datastore.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("db: failed to open datastore connection: %w", err)
	}

	// Verify connection with a ping
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("db: failed to ping datastore: %w", err)
	}

	return &datastoreConn{
		dsn:    cfg.DatastoreDSN,
		config: cfg.Datastore,
		conn:   conn,
	}, nil
}

// datastoreConn implements Datastore using datastore via hanzo/datastore-go
type datastoreConn struct {
	dsn    string
	config DatastoreConfig
	conn   driver.Conn
}

// Query executes a datastore query
func (c *datastoreConn) Query(ctx context.Context, query string, args ...interface{}) (DatastoreRows, error) {
	// Apply query timeout if configured
	if c.config.QueryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.config.QueryTimeout)
		defer cancel()
	}

	// Convert []interface{} to []any for datastore-go
	anyArgs := make([]any, len(args))
	for i, arg := range args {
		anyArgs[i] = arg
	}

	rows, err := c.conn.Query(ctx, query, anyArgs...)
	if err != nil {
		return nil, fmt.Errorf("datastore query failed: %w", err)
	}

	return &datastoreRows{rows: rows}, nil
}

// Select scans results into a destination slice
func (c *datastoreConn) Select(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	// Apply query timeout if configured
	if c.config.QueryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.config.QueryTimeout)
		defer cancel()
	}

	// Convert []interface{} to []any for datastore-go
	anyArgs := make([]any, len(args))
	for i, arg := range args {
		anyArgs[i] = arg
	}

	if err := c.conn.Select(ctx, dest, query, anyArgs...); err != nil {
		return fmt.Errorf("datastore select failed: %w", err)
	}

	return nil
}

// Exec executes a non-query statement
func (c *datastoreConn) Exec(ctx context.Context, query string, args ...interface{}) error {
	// Apply query timeout if configured
	if c.config.QueryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.config.QueryTimeout)
		defer cancel()
	}

	// Convert []interface{} to []any for datastore-go
	anyArgs := make([]any, len(args))
	for i, arg := range args {
		anyArgs[i] = arg
	}

	if err := c.conn.Exec(ctx, query, anyArgs...); err != nil {
		return fmt.Errorf("datastore exec failed: %w", err)
	}

	return nil
}

// PrepareBatch prepares a batch insert
func (c *datastoreConn) PrepareBatch(ctx context.Context, query string) (DatastoreBatch, error) {
	batch, err := c.conn.PrepareBatch(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("datastore prepare batch failed: %w", err)
	}

	return &datastoreBatch{batch: batch}, nil
}

// AsyncInsert performs an async insert (fire-and-forget style)
func (c *datastoreConn) AsyncInsert(ctx context.Context, query string, wait bool, args ...interface{}) error {
	// Convert []interface{} to []any for datastore-go
	anyArgs := make([]any, len(args))
	for i, arg := range args {
		anyArgs[i] = arg
	}

	if err := c.conn.AsyncInsert(ctx, query, wait, anyArgs...); err != nil {
		return fmt.Errorf("datastore async insert failed: %w", err)
	}

	return nil
}

// Close closes the datastore connection
func (c *datastoreConn) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// datastoreRows wraps driver.Rows to implement DatastoreRows
type datastoreRows struct {
	rows driver.Rows
}

func (r *datastoreRows) Next() bool {
	return r.rows.Next()
}

func (r *datastoreRows) Scan(dest ...interface{}) error {
	// Convert []interface{} to []any for datastore-go
	anyDest := make([]any, len(dest))
	for i, d := range dest {
		anyDest[i] = d
	}
	return r.rows.Scan(anyDest...)
}

func (r *datastoreRows) ScanStruct(dest interface{}) error {
	return r.rows.ScanStruct(dest)
}

func (r *datastoreRows) Columns() []string {
	return r.rows.Columns()
}

func (r *datastoreRows) Close() error {
	return r.rows.Close()
}

func (r *datastoreRows) Err() error {
	return r.rows.Err()
}

// datastoreBatch wraps driver.Batch to implement DatastoreBatch
type datastoreBatch struct {
	batch driver.Batch
}

func (b *datastoreBatch) Append(v ...interface{}) error {
	// Convert []interface{} to []any for datastore-go
	anyV := make([]any, len(v))
	for i, val := range v {
		anyV[i] = val
	}
	return b.batch.Append(anyV...)
}

func (b *datastoreBatch) AppendStruct(v interface{}) error {
	return b.batch.AppendStruct(v)
}

func (b *datastoreBatch) Flush() error {
	return b.batch.Flush()
}

func (b *datastoreBatch) Send() error {
	return b.batch.Send()
}

func (b *datastoreBatch) Abort() error {
	return b.batch.Abort()
}

func (b *datastoreBatch) Rows() int {
	return b.batch.Rows()
}

func (b *datastoreBatch) Close() error {
	return b.batch.Close()
}

// NoOpDatastore is a no-op implementation when datastore is disabled
type NoOpDatastore struct{}

func (n *NoOpDatastore) Query(ctx context.Context, query string, args ...interface{}) (DatastoreRows, error) {
	return &noOpRows{}, nil
}

func (n *NoOpDatastore) Select(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return nil
}

func (n *NoOpDatastore) Exec(ctx context.Context, query string, args ...interface{}) error {
	return nil
}

func (n *NoOpDatastore) PrepareBatch(ctx context.Context, query string) (DatastoreBatch, error) {
	return &noOpBatch{}, nil
}

func (n *NoOpDatastore) AsyncInsert(ctx context.Context, query string, wait bool, args ...interface{}) error {
	return nil
}

func (n *NoOpDatastore) Close() error {
	return nil
}

// noOpRows implements DatastoreRows
type noOpRows struct{}

func (r *noOpRows) Next() bool                        { return false }
func (r *noOpRows) Scan(dest ...interface{}) error    { return nil }
func (r *noOpRows) ScanStruct(dest interface{}) error { return nil }
func (r *noOpRows) Columns() []string                 { return nil }
func (r *noOpRows) Close() error                      { return nil }
func (r *noOpRows) Err() error                        { return nil }

// noOpBatch implements DatastoreBatch
type noOpBatch struct{}

func (b *noOpBatch) Append(v ...interface{}) error    { return nil }
func (b *noOpBatch) AppendStruct(v interface{}) error { return nil }
func (b *noOpBatch) Flush() error                     { return nil }
func (b *noOpBatch) Send() error                      { return nil }
func (b *noOpBatch) Abort() error                     { return nil }
func (b *noOpBatch) Rows() int                        { return 0 }
func (b *noOpBatch) Close() error                     { return nil }

// SyncConfig configures how data is synced to Hanzo Datastore
type SyncConfig struct {
	// Enabled turns on datastore sync
	Enabled bool

	// BatchSize is the number of records to batch before sending
	BatchSize int

	// FlushInterval is how often to flush partial batches
	FlushInterval time.Duration

	// Kinds specifies which entity kinds to sync (empty = all)
	Kinds []string

	// AsyncInsert uses datastore async insert for fire-and-forget
	AsyncInsert bool
}

// DefaultSyncConfig returns default sync configuration
func DefaultSyncConfig() *SyncConfig {
	return &SyncConfig{
		Enabled:       false,
		BatchSize:     1000,
		FlushInterval: 10 * time.Second,
		AsyncInsert:   true,
	}
}

// Syncer handles syncing data from SQLite to Hanzo Datastore
type Syncer struct {
	config    *SyncConfig
	datastore Datastore
	pending   []syncRecord
	lastFlush time.Time
}

type syncRecord struct {
	kind      string
	id        string
	data      []byte
	timestamp time.Time
}

// NewSyncer creates a new datastore syncer
func NewSyncer(config *SyncConfig, datastore Datastore) *Syncer {
	return &Syncer{
		config:    config,
		datastore: datastore,
		pending:   make([]syncRecord, 0, config.BatchSize),
		lastFlush: time.Now(),
	}
}

// Sync queues a record for syncing to Hanzo Datastore
func (s *Syncer) Sync(kind, id string, data []byte) error {
	if !s.config.Enabled || s.datastore == nil {
		return nil
	}

	// Check if kind should be synced
	if len(s.config.Kinds) > 0 {
		found := false
		for _, k := range s.config.Kinds {
			if k == kind {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}

	s.pending = append(s.pending, syncRecord{
		kind:      kind,
		id:        id,
		data:      data,
		timestamp: time.Now(),
	})

	// Flush if batch is full
	if len(s.pending) >= s.config.BatchSize {
		return s.Flush(context.Background())
	}

	// Flush if interval elapsed
	if time.Since(s.lastFlush) > s.config.FlushInterval {
		return s.Flush(context.Background())
	}

	return nil
}

// Flush sends pending records to Hanzo Datastore
func (s *Syncer) Flush(ctx context.Context) error {
	if len(s.pending) == 0 {
		return nil
	}

	// Group by kind
	byKind := make(map[string][]syncRecord)
	for _, r := range s.pending {
		byKind[r.kind] = append(byKind[r.kind], r)
	}

	var lastErr error
	for kind, records := range byKind {
		if err := s.flushKind(ctx, kind, records); err != nil {
			lastErr = err
		}
	}

	s.pending = s.pending[:0]
	s.lastFlush = time.Now()

	return lastErr
}

func (s *Syncer) flushKind(ctx context.Context, kind string, records []syncRecord) error {
	if s.config.AsyncInsert {
		// Use async insert for fire-and-forget
		for _, r := range records {
			query := fmt.Sprintf(`INSERT INTO %s_events (id, data, timestamp) VALUES (?, ?, ?)`, kind)
			if err := s.datastore.AsyncInsert(ctx, query, false, r.id, r.data, r.timestamp); err != nil {
				return err
			}
		}
		return nil
	}

	// Use batch insert for guaranteed delivery
	query := fmt.Sprintf(`INSERT INTO %s_events (id, data, timestamp) VALUES`, kind)
	batch, err := s.datastore.PrepareBatch(ctx, query)
	if err != nil {
		return err
	}
	defer batch.Close()

	for _, r := range records {
		if err := batch.Append(r.id, r.data, r.timestamp); err != nil {
			batch.Abort()
			return err
		}
	}

	return batch.Send()
}

// Close flushes any pending records and closes the syncer
func (s *Syncer) Close() error {
	return s.Flush(context.Background())
}
