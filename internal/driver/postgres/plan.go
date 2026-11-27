package postgres

// plan.go — what a move copies: the tables the store's migration made on hanzo-sql, read
// against the tables the file has.

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// fileTable is one table of the SQLite file.
type fileTable struct {
	virtual bool
	ddl     string
	cols    map[string]bool
}

// fileTables reads the file's catalog: every table, with its columns that hold stored
// values. fc is the connection that holds the file's write lock.
func fileTables(ctx context.Context, fc Querier) (map[string]*fileTable, error) {
	rows, err := fc.QueryContext(ctx, `SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return nil, fmt.Errorf("read the file's tables: %w", err)
	}
	out := map[string]*fileTable{}
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[name] = &fileTable{virtual: strings.HasPrefix(strings.ToUpper(strings.TrimSpace(ddl)), "CREATE VIRTUAL TABLE"), ddl: ddl, cols: map[string]bool{}}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	for name, t := range out {
		cr, err := fc.QueryContext(ctx, `SELECT name, hidden FROM pragma_table_xinfo($1)`, name)
		if err != nil {
			return nil, fmt.Errorf("read the columns of %s: %w", name, err)
		}
		for cr.Next() {
			var col string
			var hidden int
			if err := cr.Scan(&col, &hidden); err != nil {
				_ = cr.Close()
				return nil, err
			}
			if hidden == 0 { // 1 is a virtual table's own, 2 and 3 are computed and never stored
				t.cols[col] = true
			}
		}
		if err := cr.Err(); err != nil {
			_ = cr.Close()
			return nil, err
		}
		_ = cr.Close()
	}
	return out, nil
}

// shadowSuffixes are the tables FTS5 keeps beside a virtual table of its own.
var shadowSuffixes = []string{"_data", "_idx", "_content", "_docsize", "_config"}

// ours reports whether the file's table is the store's own data: not SQLite's
// bookkeeping, not a virtual table, not a shadow of one.
func ours(name string, tables map[string]*fileTable) bool {
	// _replicate_seq and _replicate_lock are hanzoai/replicate's bookkeeping beside a
	// file it streams (one row that forces a WAL frame, and a lock): about the file,
	// not in it, and meaningless once the store is a schema on hanzo-sql.
	if strings.HasPrefix(name, "sqlite_") || name == "_replicate_seq" || name == "_replicate_lock" || tables[name].virtual {
		return false
	}
	for v, t := range tables {
		if !t.virtual {
			continue
		}
		for _, suffix := range shadowSuffixes {
			if name == v+suffix {
				return false
			}
		}
	}
	return true
}

// targetColumns reads what the store's migration made on the server: for each base
// table its columns that a copy may write, its identity columns, and the tables its
// foreign keys name, in the order the columns were declared.
func targetColumns(ctx context.Context, db *sql.DB, schema string) (map[string]*table, map[string][]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.table_name, c.column_name, c.data_type, c.is_generated, c.is_identity, COALESCE(c.identity_generation, '')
		FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = $1 AND t.table_type = 'BASE TABLE'
		ORDER BY c.table_name, c.ordinal_position`, schema)
	if err != nil {
		return nil, nil, fmt.Errorf("read the store's tables on hanzo-sql: %w", err)
	}
	defer func() { _ = rows.Close() }()
	tables := map[string]*table{}
	for rows.Next() {
		var tbl, col, typ, gen, ident, how string
		if err := rows.Scan(&tbl, &col, &typ, &gen, &ident, &how); err != nil {
			return nil, nil, err
		}
		t := tables[tbl]
		if t == nil {
			t = &table{name: tbl, from: tbl}
			tables[tbl] = t
		}
		if gen == "ALWAYS" {
			continue // computed by the server (a tsvector over the row's text, say)
		}
		if how == "ALWAYS" {
			return nil, nil, fmt.Errorf("%s.%s is GENERATED ALWAYS AS IDENTITY, which refuses the ids a copy must keep", tbl, col)
		}
		k, ok := kindOf(typ)
		if !ok {
			return nil, nil, fmt.Errorf("%s.%s is %s, a type a move does not copy", tbl, col, typ)
		}
		t.cols = append(t.cols, column{name: col, kind: k})
		if ident == "YES" {
			t.identity = append(t.identity, col)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	_ = rows.Close() // a pool of few connections: one query open at a time
	fk, err := db.QueryContext(ctx, `SELECT c.relname, r.relname
		FROM pg_constraint k
		JOIN pg_class c ON c.oid = k.conrelid
		JOIN pg_class r ON r.oid = k.confrelid
		JOIN pg_namespace n ON n.oid = k.connamespace
		WHERE k.contype = 'f' AND n.nspname = $1 AND c.oid <> r.oid`, schema)
	if err != nil {
		return nil, nil, fmt.Errorf("read the store's foreign keys: %w", err)
	}
	defer func() { _ = fk.Close() }()
	needs := map[string][]string{}
	for fk.Next() {
		var child, parent string
		if err := fk.Scan(&child, &parent); err != nil {
			return nil, nil, err
		}
		needs[child] = append(needs[child], parent)
	}
	return tables, needs, fk.Err()
}

// plan is the tables to copy, parents before the tables that reference them, and what
// the file has that the server's tables do not.
type plan struct {
	tables  []*table
	dropped []string
}

// makePlan reads the file against the target. It refuses a file that holds rows in a
// table of its own that the target has no counterpart for.
func makePlan(ctx context.Context, fc Querier, target map[string]*table, needs map[string][]string, files map[string]*fileTable) (*plan, error) {
	p := &plan{}
	names := make([]string, 0, len(target))
	for name := range target {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t := target[name]
		f := files[name]
		if f == nil || len(t.cols) == 0 {
			continue // a table the file has never had: it starts empty
		}
		switch {
		case f.virtual && isTrigram(f, t):
			// The substring index is a virtual FTS5 table keyed by rowid on SQLite and
			// an ordinary (id, body) table on PostgreSQL.
			t.from, t.read, t.lower = name, []string{"rowid", quote("body")}, "body"
		case f.virtual:
			return nil, fmt.Errorf("%s is a virtual table on the file and an ordinary one on hanzo-sql, and a move knows how to carry only the trigram index between them", name)
		default:
			for _, c := range t.cols {
				if f.cols[c.name] {
					t.read = append(t.read, quote(c.name))
				}
			}
			// Only the columns the file has are copied; the rest take their defaults.
			kept := t.cols[:0:0]
			for _, c := range t.cols {
				if f.cols[c.name] {
					kept = append(kept, c)
				}
			}
			for col := range f.cols {
				if !slices.ContainsFunc(t.cols, func(c column) bool { return c.name == col }) {
					p.dropped = append(p.dropped, name+"."+col)
				}
			}
			t.cols = kept
		}
		if len(t.cols) == 0 {
			continue
		}
		p.tables = append(p.tables, t)
	}
	for name, f := range files {
		if _, ok := target[name]; ok || !ours(name, files) {
			continue
		}
		if len(f.cols) == 0 {
			continue
		}
		var has int
		err := fc.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+quote(name)+`)`).Scan(&has)
		if err != nil {
			return nil, fmt.Errorf("look in %s: %w", name, err)
		}
		if has != 0 {
			return nil, fmt.Errorf("the file holds rows in %s, which the store's tables on hanzo-sql do not have: moving it would drop them", name)
		}
		p.dropped = append(p.dropped, name+" (empty)")
	}
	sort.Strings(p.dropped)
	order, err := inOrder(p.tables, needs)
	if err != nil {
		return nil, err
	}
	p.tables = order
	return p, nil
}

// isTrigram reports whether the file's virtual table is the substring index and the
// target's is the table that takes its place.
func isTrigram(f *fileTable, t *table) bool {
	if !strings.Contains(strings.ToLower(f.ddl), "trigram") || len(t.cols) != 2 {
		return false
	}
	return t.cols[0].name == "id" && t.cols[0].kind == kInt && t.cols[1].name == "body" && t.cols[1].kind == kText
}

// inOrder sorts tables so that every table follows the tables its foreign keys name.
// Ties keep the (alphabetical) order the tables came in, so a move is the same every
// time. A cycle is an error: PostgreSQL would refuse the rows either way.
func inOrder(tables []*table, needs map[string][]string) ([]*table, error) {
	by := map[string]*table{}
	for _, t := range tables {
		by[t.name] = t
	}
	var out []*table
	done := map[string]bool{}
	visiting := map[string]bool{}
	var visit func(t *table) error
	visit = func(t *table) error {
		if done[t.name] {
			return nil
		}
		if visiting[t.name] {
			return fmt.Errorf("the foreign keys of %s form a cycle", t.name)
		}
		visiting[t.name] = true
		parents := slices.Clone(needs[t.name])
		sort.Strings(parents)
		for _, parent := range parents {
			if p := by[parent]; p != nil {
				if err := visit(p); err != nil {
					return err
				}
			}
		}
		visiting[t.name] = false
		done[t.name] = true
		out = append(out, t)
		return nil
	}
	for _, t := range tables {
		if err := visit(t); err != nil {
			return nil, err
		}
	}
	return out, nil
}
