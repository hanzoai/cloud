// Package migratetest is the shared regression harness for one production bug
// class: a store's migrate() creates an index over a column that a pre-existing
// (legacy) table lacks — because the column is ALTER-added later — so
// CREATE INDEX fails "no such column", migrate() fails, mount fails, and the pod
// crashloops on deploy.
//
// A Case seeds one store's REAL pre-migration schema into a fresh database, then
// runs that store's REAL open+migrate over it and asserts it succeeds and is
// idempotent — proving migration-correctness as a pure test over schema epochs
// instead of discovering it at production boot. Because each clients/* store's
// migrate() is unexported, the store's own package wires the Open closure; the
// assertion lives here, once, so every store shares ONE correctness check.
package migratetest
