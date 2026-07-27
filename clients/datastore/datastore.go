// Package datastore holds cloud's one connection to the analytics warehouse —
// the shared columnar store behind the usage, observability and billing
// ledgers. Every read and write of a warehouse table in this binary goes
// through here.
//
// One store, not one per tenant. Rows carry the tenant as a column and as the
// leading sort key, so a single-org read is a bound predicate over a shared
// table (WHERE organization = ?) and a fleet-wide aggregate is the same table
// with that predicate left off. That is the opposite of the relational plane in
// orgdb.go, where a tenant IS a SQLite file and a cross-tenant read is
// structurally impossible. The two planes share no connection and are
// configured independently; keeping them apart is what lets the leaderboard and
// the admin fleet views span every org while an entity read cannot leave one.
//
// The connection opens from the environment on first use — DATASTORE_ADDR and
// friends, read by datastore.Env. Open never blocks and never fails, so a
// warehouse that is down at boot leaves the ledgers unavailable rather than
// taking down the process that owns them; callers gate on Ready and report an
// honest gap instead of fabricating zeroes. Because the connection is
// established once and never replaced, there is no wiring order to get wrong
// and no window in which a caller sees a half-built client.
package datastore

import (
	"context"
	"sync"

	"github.com/hanzoai/orm/datastore"
)

var (
	once sync.Once
	conn *datastore.Conn
)

// open returns the process's warehouse connection, establishing it on first
// call. Every exported function funnels through it, so "the connection" has one
// definition and one construction site.
func open() *datastore.Conn {
	once.Do(func() { conn = datastore.Open(datastore.Env()) })
	return conn
}

// Ready reports whether the warehouse is reachable. Read paths gate on it to
// return an honest gap rather than zeroes, and write paths to skip an insert
// that would be lost. It is assignable as a bare func() bool.
func Ready() bool { return open().Ready() }

// Query runs a read and materialises the result, decoding each column into its
// native scan type keyed by column name. Placeholders bind positionally from
// args and are never interpolated into the statement, so a tenant predicate
// holds whatever the tenant value contains.
func Query(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	return open().Query(ctx, query, args...)
}

// Exec runs a statement — engine DDL, or an insert. Placeholders bind
// positionally from args.
func Exec(ctx context.Context, stmt string, args ...any) error {
	return open().Exec(ctx, stmt, args...)
}

// Wait blocks until the warehouse is reachable, the context ends, or the client
// is closed. It returns datastore.ErrUnavailable for a client that can never
// become ready, so a readiness probe fails fast on a misconfiguration instead
// of timing out.
func Wait(ctx context.Context) error { return open().Wait(ctx) }

// Close releases the connection. It is idempotent, and Exec and Query return
// datastore.ErrUnavailable afterwards.
func Close() error { return open().Close() }
