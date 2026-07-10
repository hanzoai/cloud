package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode"

	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// sqliteQuery implements the Query interface for SQLite
type sqliteQuery struct {
	db   *SQLiteDB
	tx   *sql.Tx // Optional transaction context
	kind string
	ns   string // namespace for scoping queries

	// Query state
	filters     []queryFilter
	orders      []queryOrder
	projections []string
	ancestor    Key
	limit       int
	offset      int
	distinct    bool
	keysOnly    bool
	startCursor *sqliteCursor
	endCursor   *sqliteCursor
}

type queryFilter struct {
	field string
	op    string
	value any
}

type queryOrder struct {
	field string
	desc  bool
}

// getNamespace extracts the namespace from context.
func getNamespace(ctx context.Context) string {
	return nscontext.GetNamespace(ctx)
}

// callPostLoad calls Load(nil) on the entity if it implements a Load method.
// This is needed because unmarshalForDB populates raw storage fields (e.g. State_,
// Metadata_) but does not call the model's Load() deserialization hook which
// deserializes those raw fields into their typed counterparts (State, Metadata maps).
func callPostLoad(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	if v.Kind() != reflect.Ptr {
		if v.CanAddr() {
			v = v.Addr()
		} else {
			return
		}
	}
	m := v.MethodByName("Load")
	if !m.IsValid() {
		return
	}
	mt := m.Type()
	if mt.NumIn() != 1 || mt.NumOut() != 1 {
		return
	}
	// Call Load with a nil/zero value for the parameter type.
	// Commerce's PropertyLoadSaver.Load([]Property) handles nil gracefully.
	nilArg := reflect.Zero(mt.In(0))
	m.Call([]reflect.Value{nilArg})
}

// Filter adds a filter condition (format: "field=", "field>", etc.)
func (q *sqliteQuery) Filter(filterStr string, value any) Query {
	// Parse filter string like "Field=" or "Field>="
	field, op := parseFilterString(filterStr)
	return q.FilterField(field, op, value)
}

// FilterField adds a filter with explicit operator
func (q *sqliteQuery) FilterField(fieldPath string, op string, value any) Query {
	newQ := q.clone()
	newQ.filters = append(newQ.filters, queryFilter{
		field: fieldPath,
		op:    normalizeOp(op),
		value: value,
	})
	return newQ
}

// Order adds ascending order
func (q *sqliteQuery) Order(fieldPath string) Query {
	newQ := q.clone()
	// Handle "-field" syntax for descending
	if strings.HasPrefix(fieldPath, "-") {
		newQ.orders = append(newQ.orders, queryOrder{
			field: strings.TrimPrefix(fieldPath, "-"),
			desc:  true,
		})
	} else {
		newQ.orders = append(newQ.orders, queryOrder{
			field: fieldPath,
			desc:  false,
		})
	}
	return newQ
}

// OrderDesc adds descending order
func (q *sqliteQuery) OrderDesc(fieldPath string) Query {
	newQ := q.clone()
	newQ.orders = append(newQ.orders, queryOrder{
		field: fieldPath,
		desc:  true,
	})
	return newQ
}

// Limit sets the maximum number of results
func (q *sqliteQuery) Limit(limit int) Query {
	newQ := q.clone()
	newQ.limit = limit
	return newQ
}

// Offset sets the number of results to skip
func (q *sqliteQuery) Offset(offset int) Query {
	newQ := q.clone()
	newQ.offset = offset
	return newQ
}

// Project specifies which fields to return
func (q *sqliteQuery) Project(fieldNames ...string) Query {
	newQ := q.clone()
	newQ.projections = append(newQ.projections, fieldNames...)
	return newQ
}

// Distinct returns only distinct values
func (q *sqliteQuery) Distinct() Query {
	newQ := q.clone()
	newQ.distinct = true
	return newQ
}

// Ancestor filters by ancestor key
func (q *sqliteQuery) Ancestor(ancestor Key) Query {
	newQ := q.clone()
	newQ.ancestor = ancestor
	return newQ
}

// Start sets the cursor to start from
func (q *sqliteQuery) Start(cursor Cursor) Query {
	newQ := q.clone()
	if c, ok := cursor.(*sqliteCursor); ok {
		newQ.startCursor = c
	}
	return newQ
}

// End sets the cursor to end at
func (q *sqliteQuery) End(cursor Cursor) Query {
	newQ := q.clone()
	if c, ok := cursor.(*sqliteCursor); ok {
		newQ.endCursor = c
	}
	return newQ
}

// GetAll retrieves all matching entities
func (q *sqliteQuery) GetAll(ctx context.Context, dst any) ([]Key, error) {
	q.ns = getNamespace(ctx)

	if dst == nil {
		return q.Keys(ctx)
	}

	query, args := q.buildSQL()

	var rows *sql.Rows
	var err error

	if q.tx != nil {
		rows, err = q.tx.QueryContext(ctx, query, args...)
	} else {
		rows, err = q.db.readDB.QueryContext(ctx, query, args...)
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
		if err := unmarshalForDB(data, elem.Interface()); err != nil {
			return nil, err
		}
		callPostLoad(elem)

		if isPointer {
			sliceVal = reflect.Append(sliceVal, elem)
		} else {
			sliceVal = reflect.Append(sliceVal, elem.Elem())
		}

		if intID, err2 := strconv.ParseInt(id, 10, 64); err2 == nil && intID > 0 {
			keys = append(keys, &sqliteKey{kind: q.kind, intID: intID, namespace: q.ns})
		} else {
			keys = append(keys, &sqliteKey{kind: q.kind, stringID: id, namespace: q.ns})
		}
	}

	dstVal.Elem().Set(sliceVal)
	return keys, rows.Err()
}

// First retrieves the first matching entity
func (q *sqliteQuery) First(ctx context.Context, dst any) (Key, error) {
	q.ns = getNamespace(ctx)

	limitedQ := q.Limit(1).(*sqliteQuery)
	query, args := limitedQ.buildSQL()

	var row *sql.Row
	if q.tx != nil {
		row = q.tx.QueryRowContext(ctx, query, args...)
	} else {
		row = q.db.readDB.QueryRowContext(ctx, query, args...)
	}

	var id string
	var data []byte

	if err := row.Scan(&id, &data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoSuchEntity
		}
		return nil, err
	}

	if err := unmarshalForDB(data, dst); err != nil {
		return nil, err
	}
	callPostLoad(reflect.ValueOf(dst))

	if intID, err2 := strconv.ParseInt(id, 10, 64); err2 == nil && intID > 0 {
		return &sqliteKey{kind: q.kind, intID: intID, namespace: q.ns}, nil
	}
	return &sqliteKey{kind: q.kind, stringID: id, namespace: q.ns}, nil
}

// Count returns the number of matching entities
func (q *sqliteQuery) Count(ctx context.Context) (int, error) {
	q.ns = getNamespace(ctx)

	where, args := q.buildWhere()

	query := fmt.Sprintf(`SELECT COUNT(*) FROM _entities WHERE kind = ? AND namespace = ? AND deleted = 0%s`, where)
	args = append([]any{q.kind, q.ns}, args...)

	var row *sql.Row
	if q.tx != nil {
		row = q.tx.QueryRowContext(ctx, query, args...)
	} else {
		row = q.db.readDB.QueryRowContext(ctx, query, args...)
	}

	var count int
	err := row.Scan(&count)
	return count, err
}

// Keys returns only the keys of matching entities
func (q *sqliteQuery) Keys(ctx context.Context) ([]Key, error) {
	q.ns = getNamespace(ctx)

	where, args := q.buildWhere()

	query := fmt.Sprintf(`SELECT id FROM _entities WHERE kind = ? AND namespace = ? AND deleted = 0%s`, where)
	args = append([]any{q.kind, q.ns}, args...)

	query += q.buildOrderBy()
	query += q.buildLimitOffset()

	var rows *sql.Rows
	var err error

	if q.tx != nil {
		rows, err = q.tx.QueryContext(ctx, query, args...)
	} else {
		rows, err = q.db.readDB.QueryContext(ctx, query, args...)
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
		if intID, err2 := strconv.ParseInt(id, 10, 64); err2 == nil && intID > 0 {
			keys = append(keys, &sqliteKey{kind: q.kind, intID: intID, namespace: q.ns})
		} else {
			keys = append(keys, &sqliteKey{kind: q.kind, stringID: id, namespace: q.ns})
		}
	}

	return keys, rows.Err()
}

// Run returns an iterator over the results
func (q *sqliteQuery) Run(ctx context.Context) Iterator {
	q.ns = getNamespace(ctx)

	query, args := q.buildSQL()

	var rows *sql.Rows
	var err error

	if q.tx != nil {
		rows, err = q.tx.QueryContext(ctx, query, args...)
	} else {
		rows, err = q.db.readDB.QueryContext(ctx, query, args...)
	}

	return &sqliteIterator{
		rows:      rows,
		err:       err,
		kind:      q.kind,
		namespace: q.ns,
		offset:    0,
	}
}

// buildSQL constructs the SQL query
func (q *sqliteQuery) buildSQL() (string, []any) {
	where, args := q.buildWhere()

	selectClause := "id, data"
	if q.distinct {
		selectClause = "DISTINCT " + selectClause
	}

	query := fmt.Sprintf(`SELECT %s FROM _entities WHERE kind = ? AND namespace = ? AND deleted = 0%s`, selectClause, where)
	args = append([]any{q.kind, q.ns}, args...)

	query += q.buildOrderBy()
	query += q.buildLimitOffset()

	return query, args
}

// buildWhere constructs the WHERE clause
func (q *sqliteQuery) buildWhere() (string, []any) {
	var conditions []string
	var args []any

	// Ancestor filter
	if q.ancestor != nil {
		conditions = append(conditions, "parent_id = ?")
		args = append(args, q.ancestor.Encode())
	}

	// Field filters - use JSON extraction
	for _, f := range q.filters {
		// Convert Go struct field name (PascalCase) to JSON field name (camelCase).
		// Go's json.Marshal uses the json tag name, which is typically camelCase.
		fieldName := toJSONFieldName(f.field)
		jsonPath := fmt.Sprintf("json_extract(data, '$.%s')", fieldName)

		// Handle boolean false: omitempty fields may be absent (NULL in JSON),
		// so treat NULL as equivalent to false/0 for equality checks.
		if f.op == "=" {
			switch v := f.value.(type) {
			case bool:
				if !v {
					conditions = append(conditions, fmt.Sprintf("COALESCE(%s, 0) = ?", jsonPath))
					args = append(args, 0)
					continue
				}
			case int:
				if v == 0 {
					conditions = append(conditions, fmt.Sprintf("COALESCE(%s, 0) = ?", jsonPath))
					args = append(args, 0)
					continue
				}
			}
		}

		conditions = append(conditions, fmt.Sprintf("%s %s ?", jsonPath, f.op))
		args = append(args, f.value)
	}

	// Cursor filters
	if q.startCursor != nil {
		conditions = append(conditions, "id >= ?")
		args = append(args, q.startCursor.id)
	}
	if q.endCursor != nil {
		conditions = append(conditions, "id < ?")
		args = append(args, q.endCursor.id)
	}

	if len(conditions) == 0 {
		return "", args
	}

	return " AND " + strings.Join(conditions, " AND "), args
}

// buildOrderBy constructs the ORDER BY clause
func (q *sqliteQuery) buildOrderBy() string {
	if len(q.orders) == 0 {
		return " ORDER BY rowid ASC"
	}

	var parts []string
	for _, o := range q.orders {
		// Use JSON extraction for ordering (convert PascalCase to camelCase)
		jsonPath := fmt.Sprintf("json_extract(data, '$.%s')", toJSONFieldName(o.field))
		if o.desc {
			parts = append(parts, jsonPath+" DESC")
		} else {
			parts = append(parts, jsonPath+" ASC")
		}
	}

	return " ORDER BY " + strings.Join(parts, ", ")
}

// buildLimitOffset constructs the LIMIT/OFFSET clause
func (q *sqliteQuery) buildLimitOffset() string {
	var result string
	if q.limit > 0 {
		result += fmt.Sprintf(" LIMIT %d", q.limit)
	}
	if q.offset > 0 {
		result += fmt.Sprintf(" OFFSET %d", q.offset)
	}
	return result
}

// clone creates a copy of the query
func (q *sqliteQuery) clone() *sqliteQuery {
	newQ := *q
	newQ.filters = append([]queryFilter{}, q.filters...)
	newQ.orders = append([]queryOrder{}, q.orders...)
	newQ.projections = append([]string{}, q.projections...)
	return &newQ
}

// toJSONFieldName converts a Go struct field name (PascalCase) to its JSON
// equivalent (camelCase) by lowercasing the first letter of each path segment.
// This is needed because Go's json.Marshal uses the json struct tag (camelCase)
// when serializing, but callers use Go field names (PascalCase) in Filter/Order
// calls. Handles nested paths like "Account.TransactionHash".
func toJSONFieldName(field string) string {
	if field == "" {
		return field
	}
	// Handle nested paths (e.g., "Account.TransactionHash" -> "account.transactionHash")
	if strings.Contains(field, ".") {
		parts := strings.Split(field, ".")
		for i, p := range parts {
			parts[i] = lowercaseFirst(p)
		}
		return strings.Join(parts, ".")
	}
	return lowercaseFirst(field)
}

func lowercaseFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

// parseFilterString parses "Field=" or "Field =" into field and operator
func parseFilterString(s string) (field, op string) {
	// Find the operator at the end
	operators := []string{">=", "<=", "!=", "=", ">", "<"}
	for _, opStr := range operators {
		if strings.HasSuffix(s, opStr) {
			return strings.TrimSpace(strings.TrimSuffix(s, opStr)), opStr
		}
	}
	// Default to equality
	return strings.TrimSpace(s), "="
}

// normalizeOp converts operators to SQL
func normalizeOp(op string) string {
	switch op {
	case "=", "==":
		return "="
	case "!=", "<>":
		return "!="
	case ">", ">=", "<", "<=":
		return op
	default:
		return "="
	}
}

// sqliteIterator implements Iterator
type sqliteIterator struct {
	rows      *sql.Rows
	err       error
	kind      string
	namespace string
	offset    int
}

func (it *sqliteIterator) Next(dst any) (Key, error) {
	if it.err != nil {
		return nil, it.err
	}

	if it.rows == nil {
		return nil, errors.New("db: iterator exhausted")
	}

	if !it.rows.Next() {
		if err := it.rows.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("db: no more results")
	}

	var id string
	var data []byte

	if err := it.rows.Scan(&id, &data); err != nil {
		return nil, err
	}

	if err := unmarshalForDB(data, dst); err != nil {
		return nil, err
	}
	callPostLoad(reflect.ValueOf(dst))

	it.offset++

	if intID, err2 := strconv.ParseInt(id, 10, 64); err2 == nil && intID > 0 {
		return &sqliteKey{kind: it.kind, intID: intID, namespace: it.namespace}, nil
	}
	return &sqliteKey{kind: it.kind, stringID: id, namespace: it.namespace}, nil
}

func (it *sqliteIterator) Cursor() (Cursor, error) {
	if it.rows == nil {
		return nil, errors.New("db: iterator not initialized")
	}
	return &sqliteCursor{
		id:     fmt.Sprintf("%d", it.offset),
		offset: it.offset,
	}, nil
}

// sqliteCursor implements Cursor
type sqliteCursor struct {
	id     string
	offset int
}

func (c *sqliteCursor) String() string {
	return c.id
}

// DecodeCursor parses a cursor string
func DecodeCursor(s string) (Cursor, error) {
	return &sqliteCursor{id: s}, nil
}
