// Package sqlcheck says whether the text of a SQL statement is in the dialect both
// engines read alike.
//
// The same rules are asked two ways. The source gate (sqlgate_test.go) reads the
// string literals of every ported package and asks them of the text a statement is
// assembled into. The harness (internal/sqltest) asks them of every statement a
// test actually executes, on the connection that executes it, which reads what no
// reading of the source can: a statement composed across a closure, a method value,
// a helper's return, a struct field or another package's constant. Both are needed.
// The source gate is on every build and sees statements no test reaches; the
// runtime one sees statements however they were put together.
//
// The rules exist because some mistakes run green on SQLite and are wrong:
//
//   - SQLite numbers $n parameters by first appearance, not by n. `b = $2 AND a = $1`
//     binds the first argument to b, silently. PostgreSQL binds by number. A statement
//     numbers 1 to n, in the order the numbers are written.
//   - A `?` or `?N` placeholder is SQLite's, and PostgreSQL does not read it at all.
//   - INSERT OR <x> INTO, UPDATE OR <x> and REPLACE INTO are SQLite's; PostgreSQL has
//     ON CONFLICT.
package sqlcheck
