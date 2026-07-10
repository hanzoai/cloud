package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq" // PostgreSQL driver

	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// PostgresDBConfig holds configuration for a PostgreSQL database
type PostgresDBConfig struct {
	// DSN is the connection string
	// Format: postgres://user:pass@host:port/dbname?sslmode=disable
	DSN string

	// Config for connection pool
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration

	// QueryTimeout for queries
	QueryTimeout time.Duration

	// TenantID for multi-tenant isolation
	TenantID string

	// TenantType is "user" or "org"
	TenantType string

	// Schema for tenant isolation (optional)
	// If set, uses PostgreSQL schemas for multi-tenancy
	Schema string

	// EnableVectorSearch enables pgvector extension
	EnableVectorSearch bool

	// VectorDimensions for embeddings
	VectorDimensions int
}

// PostgresDB implements the DB interface using PostgreSQL
type PostgresDB struct {
	config *PostgresDBConfig
	db     *sql.DB
	mu     sync.RWMutex
	closed bool

	// Prepared statements for hot-path operations. Every one is tenant-scoped:
	// the shared Postgres store is multi-tenant, so a row is addressed by its
	// COMPOSITE key (id, kind, tenant_id) and every read/write filters by the
	// caller's tenant. There is deliberately NO tenant-less variant — an
	// unscoped get/delete would cross tenants (Red CRIT-2).
	stmtGet *sql.Stmt // Get: (id, kind, tenant_id)
	stmtPut *sql.Stmt // Upsert: ON CONFLICT (id, kind, tenant_id)
	stmtDel *sql.Stmt // Soft-delete: (id, kind, tenant_id)
}

// tenantFor resolves the tenant scope for a Postgres operation: the request's
// ctx namespace (per-org isolation — the org == tenant) when present, else the
// DB's configured TenantID (the systemDB default, "system"). This mirrors the
// SQLite store, which scopes every row by the ctx namespace — so the SHARED
// Postgres store can never let org B read, list, or overwrite org A's rows
// (Red CRIT-2). Empty is a valid tenant value (the global/default partition).
func (db *PostgresDB) tenantFor(ctx context.Context) string {
	if ns := nscontext.GetNamespace(ctx); ns != "" {
		return ns
	}
	return db.config.TenantID
}

// NewPostgresDB creates a new PostgreSQL database connection
func NewPostgresDB(cfg *PostgresDBConfig) (*PostgresDB, error) {
	if cfg == nil || cfg.DSN == "" {
		return nil, errors.New("db: PostgresDBConfig with DSN is required")
	}

	db, err := sql.Open("postgres", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("db: failed to open postgres connection: %w", err)
	}

	// Configure connection pool
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	} else {
		db.SetMaxOpenConns(4)
	}

	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	} else {
		db.SetMaxIdleConns(2)
	}

	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	} else {
		db.SetConnMaxLifetime(30 * time.Minute)
	}

	if cfg.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	} else {
		db.SetConnMaxIdleTime(5 * time.Minute)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: failed to ping postgres: %w", err)
	}

	pdb := &PostgresDB{
		config: cfg,
		db:     db,
	}

	// Initialize schema
	if err := pdb.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: failed to initialize schema: %w", err)
	}

	// Initialize pgvector if enabled
	if cfg.EnableVectorSearch {
		if err := pdb.initVectorSearch(); err != nil {
			// Non-fatal, just log
			fmt.Printf("Warning: pgvector not available: %v\n", err)
		}
	}

	// Prepare hot-path statements
	if err := pdb.prepareStatements(); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: failed to prepare statements: %w", err)
	}

	return pdb, nil
}

// prepareStatements creates prepared statements for frequently-executed queries.
// Every statement is tenant-scoped on the composite key (id, kind, tenant_id).
func (db *PostgresDB) prepareStatements() error {
	var err error

	db.stmtGet, err = db.db.Prepare(
		`SELECT data FROM _entities WHERE id = $1 AND kind = $2 AND tenant_id = $3 AND deleted = FALSE`)
	if err != nil {
		return fmt.Errorf("prepare stmtGet: %w", err)
	}

	db.stmtPut, err = db.db.Prepare(`
		INSERT INTO _entities (id, kind, tenant_id, parent_id, data, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (id, kind, tenant_id) DO UPDATE SET
			parent_id = EXCLUDED.parent_id,
			data = EXCLUDED.data,
			updated_at = NOW()
	`)
	if err != nil {
		return fmt.Errorf("prepare stmtPut: %w", err)
	}

	db.stmtDel, err = db.db.Prepare(
		`UPDATE _entities SET deleted = TRUE, updated_at = NOW() WHERE id = $1 AND kind = $2 AND tenant_id = $3`)
	if err != nil {
		return fmt.Errorf("prepare stmtDel: %w", err)
	}

	return nil
}

// initSchema creates the base tables
func (db *PostgresDB) initSchema() error {
	// Set search path if schema specified
	if db.config.Schema != "" {
		_, err := db.db.Exec(fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, db.config.Schema))
		if err != nil {
			return err
		}
		_, err = db.db.Exec(fmt.Sprintf(`SET search_path TO %s`, db.config.Schema))
		if err != nil {
			return err
		}
	}

	// Create metadata table
	_, err := db.db.Exec(`
		CREATE TABLE IF NOT EXISTS _metadata (
			key TEXT PRIMARY KEY,
			value TEXT,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)
	`)
	if err != nil {
		return err
	}

	// Create generic entity storage table with JSONB.
	//
	// The PRIMARY KEY is COMPOSITE (id, kind, tenant_id): the same entity id in
	// two different tenants is two distinct rows, so org B's write can never
	// overwrite org A's row, and the ON CONFLICT upsert only ever collides
	// within one tenant. tenant_id is NOT NULL DEFAULT '' so it can participate
	// in the key (a NULL column can't be a reliable PK member). This mirrors the
	// SQLite store's (id, kind, namespace) key — one canonical multi-tenant
	// shape across both backends (Red CRIT-2).
	_, err = db.db.Exec(`
		CREATE TABLE IF NOT EXISTS _entities (
			id TEXT NOT NULL,
			kind TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			parent_id TEXT,
			data JSONB NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW(),
			deleted BOOLEAN DEFAULT FALSE,
			PRIMARY KEY (id, kind, tenant_id)
		)
	`)
	if err != nil {
		return err
	}

	// Create indexes
	_, err = db.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_entities_kind ON _entities(kind);
		CREATE INDEX IF NOT EXISTS idx_entities_tenant ON _entities(tenant_id);
		CREATE INDEX IF NOT EXISTS idx_entities_parent ON _entities(parent_id);
		CREATE INDEX IF NOT EXISTS idx_entities_deleted ON _entities(deleted);
		CREATE INDEX IF NOT EXISTS idx_entities_data ON _entities USING GIN (data);
	`)
	if err != nil {
		return err
	}

	return nil
}

// initVectorSearch initializes pgvector extension
func (db *PostgresDB) initVectorSearch() error {
	// Enable pgvector extension
	_, err := db.db.Exec(`CREATE EXTENSION IF NOT EXISTS vector`)
	if err != nil {
		return fmt.Errorf("failed to create vector extension: %w", err)
	}

	dims := db.config.VectorDimensions
	if dims == 0 {
		dims = 1536 // Default to OpenAI ada-002
	}

	// Create vectors table
	_, err = db.db.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS _vectors (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			tenant_id TEXT,
			embedding vector(%d),
			metadata JSONB,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)
	`, dims))
	if err != nil {
		return fmt.Errorf("failed to create vectors table: %w", err)
	}

	// Create index for approximate nearest neighbor search
	_, err = db.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_vectors_embedding ON _vectors
		USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100)
	`)
	if err != nil {
		// IVFFlat requires data, try HNSW instead
		_, err = db.db.Exec(`
			CREATE INDEX IF NOT EXISTS idx_vectors_embedding ON _vectors
			USING hnsw (embedding vector_cosine_ops)
		`)
		if err != nil {
			// Non-fatal, just slower searches
			fmt.Printf("Warning: could not create vector index: %v\n", err)
		}
	}

	return nil
}

// TenantID returns the tenant identifier
func (db *PostgresDB) TenantID() string {
	return db.config.TenantID
}

// TenantType returns "user" or "org"
func (db *PostgresDB) TenantType() string {
	return db.config.TenantType
}

// Close closes the database connection and prepared statements
func (db *PostgresDB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed {
		return nil
	}
	db.closed = true

	// Close prepared statements
	for _, s := range []*sql.Stmt{
		db.stmtGet,
		db.stmtPut,
		db.stmtDel,
	} {
		if s != nil {
			s.Close()
		}
	}

	return db.db.Close()
}

// Get retrieves an entity by key, scoped to the ctx tenant. A row written by
// another tenant with the SAME id+kind is invisible here (Red CRIT-2).
func (db *PostgresDB) Get(ctx context.Context, key Key, dst interface{}) error {
	if key == nil {
		return ErrInvalidKey
	}

	row := db.stmtGet.QueryRowContext(ctx, key.Encode(), key.Kind(), db.tenantFor(ctx))

	var data []byte
	if err := row.Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchEntity
		}
		return err
	}

	return json.Unmarshal(data, dst)
}

// Put stores an entity
func (db *PostgresDB) Put(ctx context.Context, key Key, src interface{}) (Key, error) {
	if key == nil {
		return nil, ErrInvalidKey
	}

	data, err := json.Marshal(src)
	if err != nil {
		return nil, fmt.Errorf("db: failed to marshal entity: %w", err)
	}

	var parentID *string
	if p := key.Parent(); p != nil {
		id := p.Encode()
		parentID = &id
	}

	// tenant_id is NOT NULL and part of the composite PK — store the resolved
	// tenant string directly (never NULL). ON CONFLICT (id, kind, tenant_id)
	// then upserts only WITHIN this tenant.
	_, err = db.stmtPut.ExecContext(ctx, key.Encode(), key.Kind(), db.tenantFor(ctx), parentID, data)
	if err != nil {
		return nil, err
	}

	return key, nil
}

// Delete removes an entity (soft delete), scoped to the ctx tenant.
func (db *PostgresDB) Delete(ctx context.Context, key Key) error {
	if key == nil {
		return ErrInvalidKey
	}

	_, err := db.stmtDel.ExecContext(ctx, key.Encode(), key.Kind(), db.tenantFor(ctx))
	return err
}

// GetMulti retrieves multiple entities
func (db *PostgresDB) GetMulti(ctx context.Context, keys []Key, dst interface{}) error {
	if len(keys) == 0 {
		return nil
	}

	// Build query with ANY
	ids := make([]string, len(keys))
	for i, k := range keys {
		ids[i] = k.Encode()
	}

	// Always tenant-scoped — a multi-get must not reach across tenants.
	query := `SELECT id, data FROM _entities WHERE id = ANY($1) AND tenant_id = $2 AND deleted = FALSE`
	args := []interface{}{ids, db.tenantFor(ctx)}

	rows, err := db.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Build result map
	results := make(map[string][]byte)
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return err
		}
		results[id] = data
	}

	// Unmarshal into destination slice
	dstVal := reflect.ValueOf(dst)
	if dstVal.Kind() != reflect.Ptr || dstVal.Elem().Kind() != reflect.Slice {
		return errors.New("db: dst must be a pointer to a slice")
	}

	sliceVal := dstVal.Elem()
	elemType := sliceVal.Type().Elem()

	for _, k := range keys {
		data, ok := results[k.Encode()]
		if !ok {
			sliceVal = reflect.Append(sliceVal, reflect.Zero(elemType))
			continue
		}

		elem := reflect.New(elemType.Elem())
		if err := json.Unmarshal(data, elem.Interface()); err != nil {
			return err
		}
		sliceVal = reflect.Append(sliceVal, elem)
	}

	dstVal.Elem().Set(sliceVal)
	return nil
}

// PutMulti stores multiple entities
func (db *PostgresDB) PutMulti(ctx context.Context, keys []Key, src interface{}) ([]Key, error) {
	if len(keys) == 0 {
		return keys, nil
	}

	srcVal := reflect.ValueOf(src)
	if srcVal.Kind() != reflect.Slice {
		return nil, errors.New("db: src must be a slice")
	}

	if srcVal.Len() != len(keys) {
		return nil, errors.New("db: keys and src must have same length")
	}

	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO _entities (id, kind, tenant_id, parent_id, data, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (id, kind, tenant_id) DO UPDATE SET
			parent_id = EXCLUDED.parent_id,
			data = EXCLUDED.data,
			updated_at = NOW()
	`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	tenantID := db.tenantFor(ctx)

	for i, key := range keys {
		data, err := json.Marshal(srcVal.Index(i).Interface())
		if err != nil {
			return nil, err
		}

		var parentID *string
		if p := key.Parent(); p != nil {
			id := p.Encode()
			parentID = &id
		}

		_, err = stmt.ExecContext(ctx, key.Encode(), key.Kind(), tenantID, parentID, data)
		if err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return keys, nil
}

// DeleteMulti removes multiple entities
func (db *PostgresDB) DeleteMulti(ctx context.Context, keys []Key) error {
	if len(keys) == 0 {
		return nil
	}

	ids := make([]string, len(keys))
	for i, k := range keys {
		ids[i] = k.Encode()
	}

	// Always tenant-scoped — a multi-delete must not reach across tenants.
	query := `UPDATE _entities SET deleted = TRUE, updated_at = NOW() WHERE id = ANY($1) AND tenant_id = $2`
	args := []interface{}{ids, db.tenantFor(ctx)}

	_, err := db.db.ExecContext(ctx, query, args...)
	return err
}

// Query returns a new query for the given kind
func (db *PostgresDB) Query(kind string) Query {
	return &postgresQuery{
		db:       db,
		kind:     kind,
		tenantID: db.config.TenantID,
	}
}

// VectorSearch performs similarity search using pgvector
func (db *PostgresDB) VectorSearch(ctx context.Context, opts *VectorSearchOptions) ([]VectorResult, error) {
	if opts == nil || len(opts.Vector) == 0 {
		return nil, errors.New("db: VectorSearchOptions with Vector is required")
	}

	// Convert vector to PostgreSQL array format
	vectorStr := fmt.Sprintf("[%s]", floatsToString(opts.Vector))

	limit := opts.Limit
	if limit == 0 {
		limit = 10
	}

	// Build query using cosine distance
	query := `
		SELECT id, 1 - (embedding <=> $1::vector) as score, metadata
		FROM _vectors
		WHERE 1=1
	`
	args := []interface{}{vectorStr}
	argNum := 2

	if opts.Kind != "" {
		query += fmt.Sprintf(" AND kind = $%d", argNum)
		args = append(args, opts.Kind)
		argNum++
	}

	// tenantFor(ctx) so a namespaced request scopes vectors to ITS OWN tenant
	// (Red LOW-2), falling back to the instance's configured tenant. Vector search
	// is unreachable today (no handler wires it), but fixing it now means a future
	// wiring can never enumerate another tenant's vectors.
	if tenant := db.tenantFor(ctx); tenant != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", argNum)
		args = append(args, tenant)
		argNum++
	}

	if opts.MinScore > 0 {
		query += fmt.Sprintf(" AND 1 - (embedding <=> $1::vector) >= $%d", argNum)
		args = append(args, opts.MinScore)
		argNum++
	}

	query += fmt.Sprintf(" ORDER BY embedding <=> $1::vector LIMIT %d", limit)

	rows, err := db.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []VectorResult
	for rows.Next() {
		var r VectorResult
		var metadataJSON []byte

		if err := rows.Scan(&r.ID, &r.Score, &metadataJSON); err != nil {
			return nil, err
		}

		if len(metadataJSON) > 0 {
			json.Unmarshal(metadataJSON, &r.Metadata)
		}

		results = append(results, r)
	}

	return results, rows.Err()
}

// PutVector stores a vector embedding
func (db *PostgresDB) PutVector(ctx context.Context, kind string, id string, vector []float32, metadata map[string]interface{}) error {
	vectorStr := fmt.Sprintf("[%s]", floatsToString(vector))

	var metadataJSON []byte
	var err error
	if metadata != nil {
		metadataJSON, err = json.Marshal(metadata)
		if err != nil {
			return err
		}
	}

	// Tag the stored vector with the REQUEST tenant (Red LOW-2), not the static
	// config tenant, so a shared PostgresDB scoping by ctx stores it under the
	// right tenant.
	var tenantID *string
	if tenant := db.tenantFor(ctx); tenant != "" {
		tenantID = &tenant
	}

	_, err = db.db.ExecContext(ctx, `
		INSERT INTO _vectors (id, kind, tenant_id, embedding, metadata, created_at)
		VALUES ($1, $2, $3, $4::vector, $5, NOW())
		ON CONFLICT (id) DO UPDATE SET
			embedding = EXCLUDED.embedding,
			metadata = EXCLUDED.metadata
	`, id, kind, tenantID, vectorStr, metadataJSON)

	return err
}

// NewKey creates a new key
func (db *PostgresDB) NewKey(kind string, stringID string, intID int64, parent Key) Key {
	return &postgresKey{
		kind:      kind,
		stringID:  stringID,
		intID:     intID,
		parent:    parent,
		namespace: db.config.TenantID,
	}
}

// NewIncompleteKey creates a key that will be assigned an ID on Put
func (db *PostgresDB) NewIncompleteKey(kind string, parent Key) Key {
	return &postgresKey{
		kind:       kind,
		parent:     parent,
		namespace:  db.config.TenantID,
		incomplete: true,
	}
}

// AllocateIDs pre-allocates entity IDs
func (db *PostgresDB) AllocateIDs(kind string, parent Key, n int) ([]Key, error) {
	keys := make([]Key, n)
	for i := 0; i < n; i++ {
		keys[i] = &postgresKey{
			kind:      kind,
			stringID:  generateID(),
			parent:    parent,
			namespace: db.config.TenantID,
		}
	}
	return keys, nil
}

// RunInTransaction executes a function within a transaction
func (db *PostgresDB) RunInTransaction(ctx context.Context, fn func(tx Transaction) error, opts *TransactionOptions) error {
	sqlTx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer sqlTx.Rollback()

	tx := &postgresTransaction{
		db:     db,
		tx:     sqlTx,
		tenant: db.tenantFor(ctx),
	}

	if err := fn(tx); err != nil {
		return err
	}

	return sqlTx.Commit()
}

// postgresKey implements the Key interface
type postgresKey struct {
	kind       string
	stringID   string
	intID      int64
	parent     Key
	namespace  string
	incomplete bool
}

func (k *postgresKey) Kind() string      { return k.kind }
func (k *postgresKey) StringID() string  { return k.stringID }
func (k *postgresKey) IntID() int64      { return k.intID }
func (k *postgresKey) Parent() Key       { return k.parent }
func (k *postgresKey) Namespace() string { return k.namespace }
func (k *postgresKey) Incomplete() bool  { return k.incomplete }

func (k *postgresKey) Encode() string {
	if k.stringID != "" {
		return k.stringID
	}
	if k.intID != 0 {
		return fmt.Sprintf("%d", k.intID)
	}
	if k.incomplete {
		k.stringID = generateID()
		k.incomplete = false
	}
	return k.stringID
}

func (k *postgresKey) Equal(other Key) bool {
	if other == nil {
		return false
	}
	return k.Kind() == other.Kind() && k.Encode() == other.Encode()
}

// postgresTransaction implements Transaction. tenant is resolved ONCE from the
// RunInTransaction ctx (the caller org) and scopes every op in the tx, so a
// transaction can never read or write across tenants (Red CRIT-2).
type postgresTransaction struct {
	db     *PostgresDB
	tx     *sql.Tx
	tenant string
}

func (t *postgresTransaction) Get(key Key, dst interface{}) error {
	query := `SELECT data FROM _entities WHERE id = $1 AND kind = $2 AND tenant_id = $3 AND deleted = FALSE`
	args := []interface{}{key.Encode(), key.Kind(), t.tenant}

	row := t.tx.QueryRow(query, args...)

	var data []byte
	if err := row.Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchEntity
		}
		return err
	}

	return json.Unmarshal(data, dst)
}

func (t *postgresTransaction) Put(key Key, src interface{}) (Key, error) {
	data, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}

	var parentID *string
	if p := key.Parent(); p != nil {
		id := p.Encode()
		parentID = &id
	}

	_, err = t.tx.Exec(`
		INSERT INTO _entities (id, kind, tenant_id, parent_id, data, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (id, kind, tenant_id) DO UPDATE SET
			parent_id = EXCLUDED.parent_id,
			data = EXCLUDED.data,
			updated_at = NOW()
	`, key.Encode(), key.Kind(), t.tenant, parentID, data)

	return key, err
}

func (t *postgresTransaction) Delete(key Key) error {
	query := `UPDATE _entities SET deleted = TRUE, updated_at = NOW() WHERE id = $1 AND kind = $2 AND tenant_id = $3`
	args := []interface{}{key.Encode(), key.Kind(), t.tenant}

	_, err := t.tx.Exec(query, args...)
	return err
}

func (t *postgresTransaction) Query(kind string) Query {
	return &postgresQuery{
		db:       t.db,
		kind:     kind,
		tenantID: t.tenant,
		tx:       t.tx,
	}
}

// postgresQuery implements Query for PostgreSQL
type postgresQuery struct {
	db       *PostgresDB
	tx       *sql.Tx
	kind     string
	tenantID string

	filters     []queryFilter
	orders      []queryOrder
	projections []string
	ancestor    Key
	limit       int
	offset      int
	distinct    bool
}

func (q *postgresQuery) Filter(filterStr string, value interface{}) Query {
	field, op := parseFilterString(filterStr)
	return q.FilterField(field, op, value)
}

func (q *postgresQuery) FilterField(fieldPath string, op string, value interface{}) Query {
	newQ := q.clone()
	newQ.filters = append(newQ.filters, queryFilter{
		field: fieldPath,
		op:    normalizeOp(op),
		value: value,
	})
	return newQ
}

func (q *postgresQuery) Order(fieldPath string) Query {
	newQ := q.clone()
	if strings.HasPrefix(fieldPath, "-") {
		newQ.orders = append(newQ.orders, queryOrder{
			field: strings.TrimPrefix(fieldPath, "-"),
			desc:  true,
		})
	} else {
		newQ.orders = append(newQ.orders, queryOrder{field: fieldPath})
	}
	return newQ
}

func (q *postgresQuery) OrderDesc(fieldPath string) Query {
	newQ := q.clone()
	newQ.orders = append(newQ.orders, queryOrder{field: fieldPath, desc: true})
	return newQ
}

func (q *postgresQuery) Limit(limit int) Query {
	newQ := q.clone()
	newQ.limit = limit
	return newQ
}

func (q *postgresQuery) Offset(offset int) Query {
	newQ := q.clone()
	newQ.offset = offset
	return newQ
}

func (q *postgresQuery) Project(fieldNames ...string) Query {
	newQ := q.clone()
	newQ.projections = append(newQ.projections, fieldNames...)
	return newQ
}

func (q *postgresQuery) Distinct() Query {
	newQ := q.clone()
	newQ.distinct = true
	return newQ
}

func (q *postgresQuery) Ancestor(ancestor Key) Query {
	newQ := q.clone()
	newQ.ancestor = ancestor
	return newQ
}

func (q *postgresQuery) Start(cursor Cursor) Query {
	// PostgreSQL uses OFFSET for pagination
	return q
}

func (q *postgresQuery) End(cursor Cursor) Query {
	return q
}

func (q *postgresQuery) GetAll(ctx context.Context, dst interface{}) ([]Key, error) {
	query, args := q.buildSQL(q.db.tenantFor(ctx))

	var rows *sql.Rows
	var err error

	if q.tx != nil {
		rows, err = q.tx.QueryContext(ctx, query, args...)
	} else {
		rows, err = q.db.db.QueryContext(ctx, query, args...)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	dstVal := reflect.ValueOf(dst)
	if dstVal.Kind() != reflect.Ptr || dstVal.Elem().Kind() != reflect.Slice {
		return nil, errors.New("db: dst must be a pointer to a slice")
	}

	sliceVal := dstVal.Elem()
	elemType := sliceVal.Type().Elem()
	isPointer := elemType.Kind() == reflect.Ptr
	if isPointer {
		elemType = elemType.Elem()
	}

	var keys []Key
	for rows.Next() {
		var id string
		var data []byte

		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}

		elem := reflect.New(elemType)
		if err := json.Unmarshal(data, elem.Interface()); err != nil {
			return nil, err
		}

		if isPointer {
			sliceVal = reflect.Append(sliceVal, elem)
		} else {
			sliceVal = reflect.Append(sliceVal, elem.Elem())
		}

		keys = append(keys, &postgresKey{
			kind:      q.kind,
			stringID:  id,
			namespace: q.tenantID,
		})
	}

	dstVal.Elem().Set(sliceVal)
	return keys, rows.Err()
}

func (q *postgresQuery) First(ctx context.Context, dst interface{}) (Key, error) {
	limitedQ := q.Limit(1).(*postgresQuery)
	query, args := limitedQ.buildSQL(q.db.tenantFor(ctx))

	var row *sql.Row
	if q.tx != nil {
		row = q.tx.QueryRowContext(ctx, query, args...)
	} else {
		row = q.db.db.QueryRowContext(ctx, query, args...)
	}

	var id string
	var data []byte

	if err := row.Scan(&id, &data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoSuchEntity
		}
		return nil, err
	}

	if err := json.Unmarshal(data, dst); err != nil {
		return nil, err
	}

	return &postgresKey{
		kind:      q.kind,
		stringID:  id,
		namespace: q.tenantID,
	}, nil
}

func (q *postgresQuery) Count(ctx context.Context) (int, error) {
	where, args := q.buildWhere(q.db.tenantFor(ctx))

	query := fmt.Sprintf(`SELECT COUNT(*) FROM _entities WHERE kind = $1 AND deleted = FALSE%s`, where)
	args = append([]interface{}{q.kind}, args...)

	var row *sql.Row
	if q.tx != nil {
		row = q.tx.QueryRowContext(ctx, query, args...)
	} else {
		row = q.db.db.QueryRowContext(ctx, query, args...)
	}

	var count int
	err := row.Scan(&count)
	return count, err
}

func (q *postgresQuery) Keys(ctx context.Context) ([]Key, error) {
	where, args := q.buildWhere(q.db.tenantFor(ctx))

	query := fmt.Sprintf(`SELECT id FROM _entities WHERE kind = $1 AND deleted = FALSE%s`, where)
	args = append([]interface{}{q.kind}, args...)

	query += q.buildOrderBy()
	query += q.buildLimitOffset()

	var rows *sql.Rows
	var err error

	if q.tx != nil {
		rows, err = q.tx.QueryContext(ctx, query, args...)
	} else {
		rows, err = q.db.db.QueryContext(ctx, query, args...)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []Key
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		keys = append(keys, &postgresKey{
			kind:      q.kind,
			stringID:  id,
			namespace: q.tenantID,
		})
	}

	return keys, rows.Err()
}

func (q *postgresQuery) Run(ctx context.Context) Iterator {
	query, args := q.buildSQL(q.db.tenantFor(ctx))

	var rows *sql.Rows
	var err error

	if q.tx != nil {
		rows, err = q.tx.QueryContext(ctx, query, args...)
	} else {
		rows, err = q.db.db.QueryContext(ctx, query, args...)
	}

	return &postgresIterator{
		rows:      rows,
		err:       err,
		kind:      q.kind,
		namespace: q.tenantID,
	}
}

func (q *postgresQuery) buildSQL(tenant string) (string, []interface{}) {
	where, args := q.buildWhere(tenant)

	selectClause := "id, data"
	if q.distinct {
		selectClause = "DISTINCT " + selectClause
	}

	query := fmt.Sprintf(`SELECT %s FROM _entities WHERE kind = $1 AND deleted = FALSE%s`, selectClause, where)
	args = append([]interface{}{q.kind}, args...)

	query += q.buildOrderBy()
	query += q.buildLimitOffset()

	return query, args
}

// buildWhere scopes the query to `tenant` — the caller org (per-org isolation),
// resolved from the request ctx by the executor. The tenant filter is ALWAYS
// applied (even for the empty/default tenant, which matches tenant_id = ''), so
// a list/count/keys scan can never enumerate another tenant's rows (Red CRIT-2).
func (q *postgresQuery) buildWhere(tenant string) (string, []interface{}) {
	var conditions []string
	var args []interface{}
	argNum := 2 // $1 is kind

	// Tenant filter — always present.
	conditions = append(conditions, fmt.Sprintf("tenant_id = $%d", argNum))
	args = append(args, tenant)
	argNum++

	// Ancestor filter
	if q.ancestor != nil {
		conditions = append(conditions, fmt.Sprintf("parent_id = $%d", argNum))
		args = append(args, q.ancestor.Encode())
		argNum++
	}

	// Field filters using JSONB operators
	// Go Filter() calls use PascalCase field names (e.g., "DestinationKind"),
	// but JSON tags use camelCase (e.g., "destinationKind"). Convert first char to lower.
	for _, f := range q.filters {
		jsonField := f.field
		if len(jsonField) > 0 {
			jsonField = strings.ToLower(jsonField[:1]) + jsonField[1:]
		}
		jsonPath := fmt.Sprintf("data->>'%s'", jsonField)
		conditions = append(conditions, fmt.Sprintf("%s %s $%d", jsonPath, f.op, argNum))
		args = append(args, f.value)
		argNum++
	}

	if len(conditions) == 0 {
		return "", args
	}

	return " AND " + strings.Join(conditions, " AND "), args
}

func (q *postgresQuery) buildOrderBy() string {
	if len(q.orders) == 0 {
		return ""
	}

	var parts []string
	for _, o := range q.orders {
		orderField := o.field
		if len(orderField) > 0 {
			orderField = strings.ToLower(orderField[:1]) + orderField[1:]
		}
		jsonPath := fmt.Sprintf("data->>'%s'", orderField)
		if o.desc {
			parts = append(parts, jsonPath+" DESC")
		} else {
			parts = append(parts, jsonPath+" ASC")
		}
	}

	return " ORDER BY " + strings.Join(parts, ", ")
}

func (q *postgresQuery) buildLimitOffset() string {
	var result string
	if q.limit > 0 {
		result += fmt.Sprintf(" LIMIT %d", q.limit)
	}
	if q.offset > 0 {
		result += fmt.Sprintf(" OFFSET %d", q.offset)
	}
	return result
}

func (q *postgresQuery) clone() *postgresQuery {
	newQ := *q
	newQ.filters = append([]queryFilter{}, q.filters...)
	newQ.orders = append([]queryOrder{}, q.orders...)
	newQ.projections = append([]string{}, q.projections...)
	return &newQ
}

// postgresIterator implements Iterator
type postgresIterator struct {
	rows      *sql.Rows
	err       error
	kind      string
	namespace string
	offset    int
}

func (it *postgresIterator) Next(dst interface{}) (Key, error) {
	if it.err != nil {
		return nil, it.err
	}

	if it.rows == nil || !it.rows.Next() {
		if it.rows != nil {
			if err := it.rows.Err(); err != nil {
				return nil, err
			}
		}
		return nil, errors.New("db: no more results")
	}

	var id string
	var data []byte

	if err := it.rows.Scan(&id, &data); err != nil {
		return nil, err
	}

	if err := json.Unmarshal(data, dst); err != nil {
		return nil, err
	}

	it.offset++

	return &postgresKey{
		kind:      it.kind,
		stringID:  id,
		namespace: it.namespace,
	}, nil
}

func (it *postgresIterator) Cursor() (Cursor, error) {
	return &sqliteCursor{
		id:     fmt.Sprintf("%d", it.offset),
		offset: it.offset,
	}, nil
}

// floatsToString converts a float slice to comma-separated string
func floatsToString(v []float32) string {
	strs := make([]string, len(v))
	for i, f := range v {
		strs[i] = fmt.Sprintf("%f", f)
	}
	return strings.Join(strs, ",")
}
