package postgres

import (
	"context"
	"database/sql"
	"github.com/hanzoai/cloud/manifest"
	"io"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// The SDK keeps every store on its own SQLite and learns of a server only through an
// Engine. Linking this package is what turns hanzo-sql on: the host and every plugin
// binary import it for this registration, and CLOUD_SQL_DSN then says where it is.
func init() { locus.Register(engine{}) }

// engine is the SDK's locus.Engine over the functions of this package.
type engine struct{}

func (engine) Ported(subsystem string) bool { _, ok := manifest.SQLStores[subsystem]; return ok }

func (engine) Where(ns namespace.Namespace, subsystem, dir string) (locus.Place, error) {
	return Where(ns, subsystem, dir)
}

func (engine) WhereAt(ns namespace.Namespace, subsystem, path string) (locus.Place, error) {
	return WhereAt(ns, subsystem, path)
}

func (engine) Open(ns namespace.Namespace, subsystem string) (*sql.DB, error) {
	return onServer(DSN(), ns, subsystem)
}

func (engine) Guard(ns namespace.Namespace, subsystem string) error { return Guard(ns, subsystem) }

func (engine) Settle(ctx context.Context, log luxlog.Logger, ns namespace.Namespace, subsystem, dir string, open func(*sql.DB) (io.Closer, error)) error {
	return moveIfFile(ctx, log, ns, subsystem, dir, open)
}

func (engine) Move(ctx context.Context, log luxlog.Logger, ns namespace.Namespace, subsystem, dir string, open func(*sql.DB) (io.Closer, error)) (locus.Report, error) {
	return Move(ctx, log, ns, subsystem, dir, open)
}

func (engine) MoveOnline(ctx context.Context, log luxlog.Logger, ns namespace.Namespace, subsystem, dir string, open func(*sql.DB) (io.Closer, error), on locus.Online) (locus.Report, error) {
	return MoveOnline(ctx, log, ns, subsystem, dir, open, on)
}

func (engine) MoveFile(ctx context.Context, log luxlog.Logger, ns namespace.Namespace, subsystem string, f locus.Files) (locus.Report, error) {
	return MoveFile(ctx, log, ns, subsystem, f)
}

func (engine) Revert(ctx context.Context, ns namespace.Namespace, subsystem string) error {
	return Revert(ctx, ns, subsystem)
}

func (engine) Stores(subsystem string) ([]string, error) { return Stores(subsystem) }

func (engine) Subsystems(ns namespace.Namespace) ([]string, error) { return Subsystems(ns) }

func (engine) Exists(ns namespace.Namespace, subsystem string) (bool, error) {
	return Exists(ns, subsystem)
}

func (engine) Lease(ns namespace.Namespace, subsystem, holder string, ttl time.Duration) (bool, time.Duration, error) {
	return Lease(ns, subsystem, holder, ttl)
}

func (engine) Unlease(holder string) error { return Unlease(holder) }

func (engine) Slot(ctx context.Context) (func(), error) { return Slot(ctx) }

func (engine) Locks() (*sql.DB, error) { return Locks() }

func (engine) Described(ns namespace.Namespace, subsystem string) (*sql.DB, error) {
	return Described(ns, subsystem)
}

func (engine) Placed(ctx context.Context, log luxlog.Logger, ns namespace.Namespace, subsystem, dir, legacy string, prepare func() error, open func(locus.Host) (io.Closer, error)) (locus.Host, error) {
	return Placed(ctx, log, ns, subsystem, dir, legacy, prepare, open)
}

func (engine) Scratch(ctx context.Context) (locus.Host, func(), error) { return Scratch(ctx) }
