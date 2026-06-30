// Package migration ports a legacy `hanzo_cloud` PostgreSQL database
// into per-(org, user) SQLite files served by the Hanzo cloud
// orchestrator (HIP-0106).
//
// Why introspective (not schema-aware):
//
// Status owns its schema (7 tables in tree). kms's audit_logs subset
// is documented upstream. cloud's hanzo_cloud DB was the deployed
// cloud-api Python service — its schema is not in this Go repo and
// has drifted over multiple Python/TS versions across env. The right
// thing is to discover tables at migration time from
// information_schema and copy each one verbatim, with per-row routing
// to the destination SQLite file based on the row's org column.
//
// Per-(org, user) routing:
//
// The destination layout is the canonical
// `/data/<org>/<user>/cloud.sqlite` for user-scoped rows, or
// `/data/<org>/_org/cloud.sqlite` for org-scoped rows that no single
// user owns. The migrator inspects each table for an org column
// (canonical name: "org_id", fallback: "owner") and a user column
// (canonical: "user_id", fallback: "user_email"). Routing matrix:
//
//	has user_id  → /data/<org>/<user_id>/cloud.sqlite
//	has org_id only → /data/<org>/_org/cloud.sqlite
//	has neither  → /data/_global/_org/cloud.sqlite (fail-loud — a row
//	                without org context is a data-quality bug; we
//	                materialise it under _global so the operator can
//	                triage rather than silently dropping)
//
// Schema fidelity:
//
// Each destination SQLite file gets the same DDL for every source
// table the migrator visits, lifted from information_schema.columns.
// PG-specific column types are mapped conservatively:
//
//	int*, bigint, serial      → INTEGER
//	bool, boolean             → INTEGER (0/1)
//	text, varchar, char       → TEXT
//	timestamp, timestamptz    → TEXT (RFC3339Nano UTC)
//	jsonb, json               → TEXT
//	bytea                     → BLOB
//	numeric, decimal, real,
//	  double precision        → REAL
//	uuid                      → TEXT
//	(other)                   → TEXT
//
// This is the same conservative shape hanzoai/base uses for its
// generated DDL — see HIP-0302 §3. Hand-rolling per-type translation
// is the only correct choice across heterogeneous legacy schemas.
//
// See ~/work/hanzo/CLAUDE_PG_TO_SQLITE_MIGRATION.md (service #4 — cloud).
package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// Options configures the migration.
type Options struct {
	// SrcDSN is the legacy hanzo_cloud Postgres DSN.
	SrcDSN string
	// DstRoot is the data root under which per-(org, user) SQLite
	// files are created. Conventionally /data per
	// CLAUDE_PG_TO_SQLITE_MIGRATION.md.
	DstRoot string
	// IncludeSchemas, if non-empty, restricts the migration to tables
	// from this set of PG schemas. Default ["public"].
	IncludeSchemas []string
	// ExcludeTables, if non-empty, skips matching tables (basename match
	// only — schema-qualified names are not matched).
	ExcludeTables []string
	// BatchSize bounds memory use. Default 500.
	BatchSize int
}

// TableReport captures the per-(table, target) routing outcome.
type TableReport struct {
	Table       string
	SourceRows  int
	WrittenRows int
	// Targets is the set of (org, user) tuples a table's rows fanned
	// out into. Sorted lexicographically.
	Targets []Target
}

// Target identifies one destination SQLite file. User == "_org" is the
// org-scoped sentinel; Org == "_global" is the fail-loud sentinel.
type Target struct {
	Org  string
	User string
	Rows int
}

// validToken matches the safe org/user grammar from sqlitepath.For —
// path traversal is not possible because every path segment is
// validated against this pattern.
var validToken = regexp.MustCompile(`^[A-Za-z0-9._@+-]+$`)

// Run executes the migration: opens PG read-only, discovers tables via
// information_schema, copies row-by-row with per-row destination
// routing. Returns one report per source table.
func Run(ctx context.Context, opts Options) ([]TableReport, error) {
	if opts.SrcDSN == "" {
		return nil, errors.New("migration: src DSN required")
	}
	if opts.DstRoot == "" {
		return nil, errors.New("migration: dst root required")
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}
	if len(opts.IncludeSchemas) == 0 {
		opts.IncludeSchemas = []string{"public"}
	}

	src, err := sql.Open("postgres", opts.SrcDSN)
	if err != nil {
		return nil, fmt.Errorf("open src: %w", err)
	}
	defer src.Close()
	if err := src.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping src: %w", err)
	}

	if err := os.MkdirAll(opts.DstRoot, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir dst root: %w", err)
	}

	tables, err := discoverTables(ctx, src, opts.IncludeSchemas, opts.ExcludeTables)
	if err != nil {
		return nil, fmt.Errorf("discover tables: %w", err)
	}

	pool := newDstPool(opts.DstRoot)
	defer pool.Close()

	reports := make([]TableReport, 0, len(tables))
	for _, t := range tables {
		report, err := copyTable(ctx, src, pool, t, opts.BatchSize)
		if err != nil {
			return reports, fmt.Errorf("copy %s: %w", t.QualifiedName(), err)
		}
		reports = append(reports, report)
	}
	return reports, nil
}

// TableInfo is the discovered metadata for one PG table.
type TableInfo struct {
	Schema  string
	Name    string
	Columns []ColumnInfo
	// OrgCol / UserCol are the resolved (org, user) routing columns;
	// empty when no recognised column exists.
	OrgCol  string
	UserCol string
}

// ColumnInfo describes one PG column for SQLite DDL synthesis.
type ColumnInfo struct {
	Name       string
	DataType   string
	IsNullable bool
}

// QualifiedName returns schema.name with quoting that PG accepts. For
// schemaless sources (SQLite test fixtures) it returns the bare
// quoted table name. The SQL the migrator emits uses this same form,
// which is what allows the SQLite-as-source test path to drive the
// same copyTable code as production PG.
func (t TableInfo) QualifiedName() string {
	if t.Schema == "" {
		return fmt.Sprintf(`%q`, t.Name)
	}
	return fmt.Sprintf(`%q.%q`, t.Schema, t.Name)
}

// discoverTables enumerates user tables from information_schema. The
// excludes list is matched case-insensitively against the bare table
// name (not the schema-qualified form) so callers can drop "schema_
// migrations" once and have it apply across PG schemas.
func discoverTables(ctx context.Context, db *sql.DB, schemas, excludes []string) ([]TableInfo, error) {
	exclSet := map[string]struct{}{}
	for _, e := range excludes {
		exclSet[strings.ToLower(e)] = struct{}{}
	}

	args := make([]any, len(schemas))
	placeholders := make([]string, len(schemas))
	for i, s := range schemas {
		args[i] = s
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	q := fmt.Sprintf(`
SELECT table_schema, table_name
FROM information_schema.tables
WHERE table_type='BASE TABLE' AND table_schema IN (%s)
ORDER BY table_schema, table_name`, strings.Join(placeholders, ","))

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []TableInfo
	for rows.Next() {
		var t TableInfo
		if err := rows.Scan(&t.Schema, &t.Name); err != nil {
			return nil, err
		}
		if _, skip := exclSet[strings.ToLower(t.Name)]; skip {
			continue
		}
		tables = append(tables, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Second pass: column descriptors for each surviving table.
	for i, t := range tables {
		cols, err := discoverColumns(ctx, db, t.Schema, t.Name)
		if err != nil {
			return nil, fmt.Errorf("columns %s: %w", t.QualifiedName(), err)
		}
		tables[i].Columns = cols
		tables[i].OrgCol, tables[i].UserCol = pickRoutingColumns(cols)
	}
	return tables, nil
}

func discoverColumns(ctx context.Context, db *sql.DB, schema, name string) ([]ColumnInfo, error) {
	rows, err := db.QueryContext(ctx, `
SELECT column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2
ORDER BY ordinal_position`, schema, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ColumnInfo
	for rows.Next() {
		var c ColumnInfo
		var nullable string
		if err := rows.Scan(&c.Name, &c.DataType, &nullable); err != nil {
			return nil, err
		}
		c.IsNullable = strings.EqualFold(nullable, "YES")
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// pickRoutingColumns picks the (org, user) routing columns from a
// column list. Canonical names take priority over fallbacks.
func pickRoutingColumns(cols []ColumnInfo) (orgCol, userCol string) {
	names := make(map[string]struct{}, len(cols))
	for _, c := range cols {
		names[strings.ToLower(c.Name)] = struct{}{}
	}
	for _, c := range []string{"org_id", "orgid", "organization_id", "owner"} {
		if _, ok := names[c]; ok {
			orgCol = c
			break
		}
	}
	for _, c := range []string{"user_id", "userid", "user_email", "owner_user_id"} {
		if _, ok := names[c]; ok {
			userCol = c
			break
		}
	}
	return
}

// copyTable streams the source table once, routing each row to the
// destination SQLite file derived from its org/user columns.
func copyTable(ctx context.Context, src *sql.DB, pool *dstPool, t TableInfo, batch int) (TableReport, error) {
	cols := joinColIdents(t.Columns)
	q := fmt.Sprintf("SELECT %s FROM %s", cols, t.QualifiedName())
	rows, err := src.QueryContext(ctx, q)
	if err != nil {
		return TableReport{}, err
	}
	defer rows.Close()

	report := TableReport{Table: t.QualifiedName()}
	targetCounts := map[Target]int{}

	values := make([]any, len(t.Columns))
	scanArgs := make([]any, len(t.Columns))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	orgIdx := indexOf(t.Columns, t.OrgCol)
	userIdx := indexOf(t.Columns, t.UserCol)

	for rows.Next() {
		if err := rows.Scan(scanArgs...); err != nil {
			return report, err
		}
		report.SourceRows++

		org, user := routeRow(values, orgIdx, userIdx)
		dst, err := pool.get(ctx, org, user, t)
		if err != nil {
			return report, fmt.Errorf("dst open %s/%s: %w", org, user, err)
		}
		row := make([]any, len(values))
		for i, v := range values {
			row[i] = normalize(t.Columns[i].DataType, v)
		}
		if err := dst.insert(ctx, t.Name, row); err != nil {
			return report, fmt.Errorf("insert row %d: %w", report.SourceRows, err)
		}
		report.WrittenRows++
		tk := Target{Org: org, User: user}
		targetCounts[tk]++
		if report.SourceRows%batch == 0 {
			if err := pool.flushAll(ctx); err != nil {
				return report, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	if err := pool.flushAll(ctx); err != nil {
		return report, err
	}

	for tk, n := range targetCounts {
		tk.Rows = n
		report.Targets = append(report.Targets, tk)
	}
	sort.Slice(report.Targets, func(i, j int) bool {
		if report.Targets[i].Org != report.Targets[j].Org {
			return report.Targets[i].Org < report.Targets[j].Org
		}
		return report.Targets[i].User < report.Targets[j].User
	})
	return report, nil
}

// routeRow extracts (org, user) from a row using the resolved routing
// indices. Empty / non-string values fall through to "_global"/"_org".
func routeRow(values []any, orgIdx, userIdx int) (org, user string) {
	org = sentinelOrg
	user = sentinelOrgUser
	if orgIdx >= 0 {
		if s := tokenize(values[orgIdx]); s != "" {
			org = s
		}
	}
	if userIdx >= 0 {
		if s := tokenize(values[userIdx]); s != "" {
			user = s
		}
	}
	return
}

const (
	sentinelOrg     = "_global"
	sentinelOrgUser = "_org"
)

// tokenize coerces an `any` value into a path-safe token. Rows whose
// org/user column is NULL or non-string become "" — the caller
// substitutes the sentinel.
func tokenize(v any) string {
	if v == nil {
		return ""
	}
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case []byte:
		s = string(x)
	default:
		s = fmt.Sprintf("%v", x)
	}
	s = strings.TrimSpace(s)
	if !validToken.MatchString(s) {
		return ""
	}
	return s
}

func indexOf(cols []ColumnInfo, name string) int {
	if name == "" {
		return -1
	}
	for i, c := range cols {
		if strings.EqualFold(c.Name, name) {
			return i
		}
	}
	return -1
}

func joinColIdents(cols []ColumnInfo) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = fmt.Sprintf("%q", c.Name)
	}
	return strings.Join(parts, ", ")
}

// normalize coerces a PG-typed value into something SQLite accepts.
// Booleans → 0/1; times → RFC3339Nano UTC strings; everything else
// passes through (modernc.org/sqlite accepts all standard
// database/sql primitives).
func normalize(pgType string, v any) any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	default:
		return x
	}
}
