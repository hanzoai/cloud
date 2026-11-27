package postgres

// copy.go — the rows of one store, from its SQLite file into the tables the store's own
// migration made on hanzo-sql.
//
// THE TARGET IS THE AUTHORITY on what a store has. Its tables, columns and their types
// are the DDL the store runs on PostgreSQL (Identity(), Bytewise(), JSON(), Words,
// Grams), which nothing could derive from the SQLite catalog; the file is read for what
// the target names, column by column, by NAME. A column the file has and the target has
// not is dropped and reported; a column the target has and the file has not takes its
// default. A table the file holds rows in and the target does not have is NOT dropped:
// that is a store whose migration left something out, and the move refuses it.
//
// THE CHECKSUM IS COMPUTED THE SAME WAY ON BOTH SIDES: every row is reduced to a
// type-tagged, length-prefixed encoding of its converted values, hashed with SHA-256,
// and the hashes summed (256 bits, order-independent, so a scan in another order is no
// difference and a duplicate row does not cancel). The file's sum is taken from the very
// values sent to the server; the server's from reading them back, after commit.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// kind is what a column holds, as PostgreSQL declares it. SQLite is dynamically typed,
// so the type a value must take is the target's word and not the file's.
type kind int

const (
	kInt kind = iota
	kFloat
	kText
	kBlob
	kJSON
	kBool
	kTime
	kNumeric
)

func (k kind) String() string {
	return [...]string{"integer", "float", "text", "blob", "json", "boolean", "timestamp", "numeric"}[k]
}

// kindOf maps a PostgreSQL data_type to a kind. A type the stores do not use is not
// copied on a guess.
func kindOf(dataType string) (kind, bool) {
	switch dataType {
	case "bigint", "integer", "smallint":
		return kInt, true
	case "double precision", "real":
		return kFloat, true
	case "numeric":
		return kNumeric, true
	case "text", "character varying", "character":
		return kText, true
	case "bytea":
		return kBlob, true
	case "json", "jsonb":
		return kJSON, true
	case "boolean":
		return kBool, true
	case "timestamp with time zone", "timestamp without time zone":
		return kTime, true
	}
	return 0, false
}

// timeLayouts are the spellings a SQLite file keeps a moment in: what its drivers write
// for a time.Time, and what CURRENT_TIMESTAMP is.
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02",
}

// moment reads a time from a file's text. A time with no zone is UTC, as CURRENT_TIMESTAMP
// is. PostgreSQL holds a microsecond, so that is how finely a moment is kept.
func moment(text string) (time.Time, error) {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, " m=+"); i > 0 { // a time.Time printed with its monotonic reading
		text = text[:i]
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, text); err == nil {
			return t.UTC().Truncate(time.Microsecond), nil
		}
	}
	return time.Time{}, errors.New("a time written in a form no column of the file's drivers writes")
}

// convert takes a value as the SQLite driver scanned it to the Go value PostgreSQL's
// column takes, or refuses. It never repairs: a value the target cannot hold is a store
// that stays on its file with the reason, not one that loses a byte quietly.
func (k kind) convert(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch k {
	case kInt:
		switch x := v.(type) {
		case int64:
			return x, nil
		case bool:
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		case float64:
			if x == math.Trunc(x) && x >= -(1<<63) && x < 1<<63 {
				return int64(x), nil
			}
		}
	case kNumeric:
		// A numeric column of a Base holds a number field: a float64 the engine
		// wrote, which SQLite keeps as an integer when it has no fraction. The
		// server takes the float and holds it exactly as written (shortest form).
		f, ok := asFloat(v)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("a %T where the column is numeric", v)
		}
		return f, nil
	case kFloat:
		switch x := v.(type) {
		case float64:
			return x, nil
		case int64:
			return float64(x), nil
		}
	case kText:
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case []byte:
			s = string(x)
		default:
			return nil, fmt.Errorf("a %T where the column is text", v)
		}
		if !utf8.ValidString(s) {
			return nil, errors.New("text that is not valid UTF-8, which PostgreSQL's text refuses")
		}
		if strings.IndexByte(s, 0) >= 0 {
			return nil, errors.New("text with a NUL, which PostgreSQL's text refuses")
		}
		return s, nil
	case kBlob:
		switch x := v.(type) {
		case []byte:
			return x, nil
		case string:
			return []byte(x), nil
		}
	case kJSON:
		var b []byte
		switch x := v.(type) {
		case string:
			b = []byte(x)
		case []byte:
			b = x
		default:
			return nil, fmt.Errorf("a %T where the column is json", v)
		}
		if !utf8.Valid(b) || !json.Valid(b) {
			return nil, errors.New("a document that is not valid JSON")
		}
		return string(b), nil
	case kBool:
		switch x := v.(type) {
		case bool:
			return x, nil
		case int64:
			if x == 0 || x == 1 {
				return x == 1, nil
			}
		}
	case kTime:
		switch x := v.(type) {
		case time.Time:
			return x.UTC().Truncate(time.Microsecond), nil
		case string:
			return moment(x)
		case []byte:
			return moment(string(x))
		}
	}
	return nil, fmt.Errorf("a %T where the column is %s", v, k)
}

// asFloat reads a number as a float64 from any form a driver hands it.
func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		if x > 1<<53 || x < -(1<<53) {
			return 0, false // would not round-trip through a float
		}
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	case []byte:
		f, err := strconv.ParseFloat(string(x), 64)
		return f, err == nil
	}
	return 0, false
}

// canonJSON is a document reduced to one spelling of itself, so the file's text and the
// text jsonb gives back, which it has normalised in key order, spacing and number
// form, compare equal when they say the same thing.
func canonJSON(doc []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("data after the document")
	}
	var b bytes.Buffer
	writeCanon(&b, v)
	return b.Bytes(), nil
}

func writeCanon(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case json.Number:
		// By value, so 1.0, 1 and 1e0 are one number, as jsonb has them. A number too
		// long to be worth the arithmetic keeps its spelling.
		s := x.String()
		if len(s) <= 40 {
			if r, ok := new(big.Rat).SetString(s); ok {
				s = r.RatString()
			}
		}
		b.WriteByte('n')
		b.WriteString(s)
	case string:
		b.WriteString(strconv.Quote(x))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanon(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(k))
			b.WriteByte(':')
			writeCanon(b, x[k])
		}
		b.WriteByte('}')
	}
}

// total is the checksum of a table: how many rows, and the sum of their hashes.
type total struct {
	rows int64
	sum  [32]byte
}

// plus is the checksum of two disjoint sets of rows taken together.
func (t total) plus(o total) total {
	var carry uint16
	for i := 31; i >= 0; i-- {
		s := uint16(t.sum[i]) + uint16(o.sum[i]) + carry
		t.sum[i] = byte(s)
		carry = s >> 8
	}
	t.rows += o.rows
	return t
}

// add folds one row in. vals are the converted values of the row (the file's side) or
// the values scanned from PostgreSQL (the server's), in the same column order.
func (t *total) add(kinds []kind, vals []any) error {
	h := sha256.New()
	for i, v := range vals {
		if err := encode(h, kinds[i], v); err != nil {
			return err
		}
	}
	var row [32]byte
	h.Sum(row[:0])
	var carry uint16
	for i := 31; i >= 0; i-- {
		s := uint16(t.sum[i]) + uint16(row[i]) + carry
		t.sum[i] = byte(s)
		carry = s >> 8
	}
	t.rows++
	return nil
}

func lp(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	h.Write(n[:])
	h.Write(b)
}

// encode writes a value so two equal rows hash alike and a NULL is never an empty
// string or a zero.
func encode(h hash.Hash, k kind, v any) error {
	if v == nil {
		h.Write([]byte{0})
		return nil
	}
	if k == kNumeric {
		// The file's side is a float64 and the server's is the text of a numeric:
		// both are the same number when they parse to the same float64.
		f, ok := asFloat(v)
		if !ok {
			return fmt.Errorf("a %T cannot be checksummed as a number", v)
		}
		v = f
	}
	var n [8]byte
	switch x := v.(type) {
	case int64:
		binary.BigEndian.PutUint64(n[:], uint64(x))
		h.Write([]byte{1})
		h.Write(n[:])
	case float64:
		binary.BigEndian.PutUint64(n[:], math.Float64bits(x))
		h.Write([]byte{2})
		h.Write(n[:])
	case bool:
		h.Write([]byte{3})
		if x {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
	case time.Time:
		binary.BigEndian.PutUint64(n[:], uint64(x.UTC().UnixMicro()))
		h.Write([]byte{7})
		h.Write(n[:])
	case []byte:
		if k == kJSON {
			return encodeJSON(h, x)
		}
		h.Write([]byte{4})
		lp(h, x)
	case string:
		if k == kJSON {
			return encodeJSON(h, []byte(x))
		}
		h.Write([]byte{5})
		lp(h, []byte(x))
	default:
		return fmt.Errorf("a %T cannot be checksummed", v)
	}
	return nil
}

func encodeJSON(h hash.Hash, doc []byte) error {
	c, err := canonJSON(doc)
	if err != nil {
		return err
	}
	h.Write([]byte{6})
	lp(h, c)
	return nil
}

// size is what a value weighs on the wire, for the rate a move reports.
func size(v any) int64 {
	switch x := v.(type) {
	case string:
		return int64(len(x))
	case []byte:
		return int64(len(x))
	case nil:
		return 0
	}
	return 8
}

// column is one column the target has.
type column struct {
	name string
	kind kind
}

// table is one table to copy: the target's columns the file also has, in the target's
// order, and the target's identity columns.
type table struct {
	name     string
	cols     []column
	identity []string
	// read is the SELECT list on the file, one expression per column.
	read []string
	// from is the file's table: its own name, except for the trigram index, whose
	// FTS5 table is virtual and keyed by rowid.
	from string
	// key names the integer column an online move walks the table by, or is empty.
	key string
	// lower lowercases a text column as it is copied: the trigram index on PostgreSQL
	// holds lowered text.
	lower string
}

func (t *table) kinds() []kind {
	out := make([]kind, len(t.cols))
	for i, c := range t.cols {
		out[i] = c.kind
	}
	return out
}

func (t *table) names() []string {
	out := make([]string, len(t.cols))
	for i, c := range t.cols {
		out[i] = c.name
	}
	return out
}

func quote(ident string) string { return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"` }

func quoted(idents []string) string {
	q := make([]string, len(idents))
	for i, s := range idents {
		q[i] = quote(s)
	}
	return strings.Join(q, ", ")
}

// source streams a table of the file as the rows PostgreSQL's COPY takes, and sums
// them as it goes. It is a pgx.CopyFromSource.
type source struct {
	rows  *sql.Rows
	t     *table
	kinds []kind
	raw   []any
	ptrs  []any
	cur   []any
	sum   total
	bytes int64
	keyAt int   // the index of the key column, or -1
	last  int64 // the key of the last row read, when the table is walked by one
	err   error
}

func newSource(rows *sql.Rows, t *table) *source {
	n := len(t.cols)
	s := &source{rows: rows, t: t, kinds: t.kinds(), raw: make([]any, n), ptrs: make([]any, n), cur: make([]any, n), keyAt: -1}
	for i, c := range t.cols {
		if t.key != "" && c.name == t.key {
			s.keyAt = i
		}
	}
	for i := range s.raw {
		s.ptrs[i] = &s.raw[i]
	}
	return s
}

func (s *source) Next() bool {
	if s.err != nil || !s.rows.Next() {
		if s.err == nil {
			s.err = s.rows.Err()
		}
		return false
	}
	if err := s.rows.Scan(s.ptrs...); err != nil {
		s.err = err
		return false
	}
	for i, v := range s.raw {
		c, err := s.kinds[i].convert(v)
		if err != nil {
			s.err = fmt.Errorf("%s.%s: %w", s.t.name, s.t.cols[i].name, err)
			return false
		}
		if s.t.lower == s.t.cols[i].name && c != nil {
			c = strings.ToLower(c.(string))
		}
		s.cur[i] = c
		s.bytes += size(c)
	}
	if s.keyAt >= 0 {
		s.last, _ = s.cur[s.keyAt].(int64)
	}
	if err := s.sum.add(s.kinds, s.cur); err != nil {
		s.err = fmt.Errorf("%s: %w", s.t.name, err)
		return false
	}
	return true
}

func (s *source) Values() ([]any, error) { return s.cur, nil }
func (s *source) Err() error             { return s.err }

// copyTable writes the rows of t from the file to the table on the server and answers
// the file's side of the checksum. It runs on the connection pg, which is the server
// session that holds the store's move lock.
func copyTable(ctx context.Context, fc Querier, pg *pgx.Conn, schema string, t *table) (total, int64, error) {
	sum, bytes, _, err := copyRows(ctx, fc, pg, schema, t, "")
	return sum, bytes, err
}

// copyRows is copyTable for the rows a tail selects: the text after the table's name
// in the SELECT (a WHERE, an ORDER BY, a LIMIT), with its arguments. It also answers
// the key of the last row it read when the table is walked by one.
func copyRows(ctx context.Context, fc Querier, pg *pgx.Conn, schema string, t *table, tail string, args ...any) (total, int64, int64, error) {
	rows, err := fc.QueryContext(ctx, `SELECT `+strings.Join(t.read, ", ")+` FROM `+quote(t.from)+` `+tail, args...)
	if err != nil {
		return total{}, 0, 0, fmt.Errorf("read %s: %w", t.from, err)
	}
	defer func() { _ = rows.Close() }()
	src := newSource(rows, t)
	n, err := pg.CopyFrom(ctx, pgx.Identifier{schema, t.name}, t.names(), src)
	if src.err != nil { // the better word: the value that could not be converted
		err = src.err
	}
	if err != nil {
		return total{}, 0, 0, fmt.Errorf("copy %s: %w", t.name, err)
	}
	if n != src.sum.rows {
		return total{}, 0, 0, fmt.Errorf("copy %s: the file gave %d rows and the server took %d", t.name, src.sum.rows, n)
	}
	return src.sum, src.bytes, src.last, nil
}

// readBack sums the table as the server holds it.
func readBack(ctx context.Context, db *sql.DB, t *table) (total, error) {
	return readBackWhere(ctx, db, t, "")
}

// readBackWhere sums the rows of the table on the server that a tail selects.
func readBackWhere(ctx context.Context, db *sql.DB, t *table, tail string, args ...any) (total, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+quoted(t.names())+` FROM `+quote(t.name)+` `+tail, args...)
	if err != nil {
		return total{}, fmt.Errorf("read back %s: %w", t.name, err)
	}
	defer func() { _ = rows.Close() }()
	kinds := t.kinds()
	vals := make([]any, len(kinds))
	ptrs := make([]any, len(kinds))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	var sum total
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return total{}, fmt.Errorf("read back %s: %w", t.name, err)
		}
		if err := sum.add(kinds, vals); err != nil {
			return total{}, fmt.Errorf("read back %s: %w", t.name, err)
		}
	}
	return sum, rows.Err()
}
