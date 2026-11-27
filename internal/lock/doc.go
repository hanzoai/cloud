// Package lock is a mutex per key: at most one holder of a key at a time, and
// nothing kept for a key nobody holds.
//
// It is for a check and an act that must not interleave with another check and
// act on the same thing — a payment settled while the same job is refunded —
// where the store's own compare-and-set covers one statement and not the crossing
// to another process between two of them.
//
// Keyed is the lock in one process, and a store with one writer (SQLite under
// the driver's single-connection cap) is in one process, so it is the whole of it
// there. Across is the same lock for every replica that reaches one database, and
// Keyed.Across is the two together: the one a caller whose store may be either takes.
package lock
