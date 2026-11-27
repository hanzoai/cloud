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

// Connect starts the connection now rather than at the first call that needs
// it. It never blocks. A subsystem calls it at mount so its first request finds
// a dial already under way instead of the one that request itself began.
func Connect() { open() }

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
