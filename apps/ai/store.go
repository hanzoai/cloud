package ai

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cek"
	"github.com/hanzoai/dbx"
	"github.com/hanzoai/namespace"

	aiobject "github.com/hanzoai/ai/object"
)

// ai's own store (providers, routes, listings, routing versions, media jobs, the
// auto-routing ledgers: aiobject.SyncSchema) is a database this host opens and hands
// over (aiobject.UseDB), so it is a schema of hanzo-sql like every other store and not
// a file the module opens from `driverName` and `dataSourceName`. Those are no longer
// read: the seam decides where the store lives.
//
// The subsystem is "ai" in the system namespace, app "ai" (manifest.SQLStores).
const storeSubsystem = "ai"

// aiStore is the database ai is handed; the host owns the close.
type aiStore struct {
	db  *dbx.DB
	sql *sql.DB
}

func (s *aiStore) Close() error { return s.sql.Close() }

var (
	storeMu sync.Mutex
	opened  *aiStore
)

// openStore opens ai's store under dir where it lives now: the schema on hanzo-sql,
// or the encrypted file. A file ai kept before the host owned it is carried into the
// seam first, so a move finds it.
func openStore(ctx context.Context, dir string) (*aiStore, error) {
	if err := carry(dir); err != nil {
		return nil, err
	}
	return locus.Use(ctx, nil, namespace.System(), storeSubsystem, dir, func(sdb *sql.DB) (*aiStore, error) {
		pg := dialect.Of(sdb).Postgres()
		driver := "sqlite"
		if pg {
			driver = "pgx"
		}
		db := dbx.NewFromDB(sdb, driver)
		// This is the store's own open: the tables are made on the file and on the schema
		// a move fills, so the move finds the same ones on both sides. A table that fails
		// on the server fails the open (and so the move); on the file it is logged by the
		// module as it always was, because the file is what served before and one model
		// that never synced there must not take the whole API down.
		if err := aiobject.SyncSchema(db); err != nil && pg {
			return nil, fmt.Errorf("ai: store schema: %w", err)
		}
		return &aiStore{db: db, sql: sdb}, nil
	})
}

// legacyFile is where ai kept its store before the host owned it: the file
// `dataSourceName` names (file:/var/lib/cloud/cloud.db in production), else
// cloud.db under the data directory.
func legacyFile(dir string) string {
	if dsn, ok := strings.CutPrefix(os.Getenv("dataSourceName"), "file:"); ok {
		if path, _, _ := strings.Cut(dsn, "?"); filepath.IsAbs(path) {
			return path
		}
	}
	return filepath.Join(dir, "cloud.db")
}

// carry makes ai's legacy file the encrypted file the seam opens, once: a consistent
// copy (VACUUM INTO folds the WAL), encrypted under the store's key, put where the
// seam keeps the store. The legacy file is left exactly as it was, for a rollback and
// for an operator to delete after the move; a marker beside it says it was carried, so
// a store that has since moved and whose file was removed is not carried again.
func carry(dir string) error {
	if dir == "" {
		return nil
	}
	src := legacyFile(dir)
	mark := src + ".carried"
	if fileExists(mark) || !fileExists(src) {
		return nil
	}
	ns := namespace.System()
	dst, err := namespace.Path(dir, ns, storeSubsystem)
	if err != nil {
		return err
	}
	if fileExists(dst) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".carry"
	_ = os.Remove(tmp)
	legacy, err := sql.Open("sqlite", src)
	if err != nil {
		return fmt.Errorf("ai: open the legacy store %s: %w", src, err)
	}
	defer legacy.Close()
	if _, err := legacy.Exec("VACUUM INTO '" + strings.ReplaceAll(tmp, "'", "''") + "'"); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("ai: copy the legacy store %s: %w", src, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := cek.Convert(ns, storeSubsystem, dst); err != nil {
		// Nothing half-made stays where the seam would open it as the store.
		_ = os.Remove(dst)
		return fmt.Errorf("ai: encrypt the carried store: %w", err)
	}
	return os.WriteFile(mark, nil, 0o600)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}

// hand opens ai's store and gives it to the module. Called before the module boots.
func hand(ctx context.Context) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	if opened != nil {
		return nil
	}
	s, err := openStore(ctx, cloud.DataDir())
	if err != nil {
		return fmt.Errorf("ai: open the store: %w", err)
	}
	aiobject.UseDB(s.db)
	opened = s
	return nil
}

// release takes the store back from the module and closes it.
func release() error {
	storeMu.Lock()
	defer storeMu.Unlock()
	if opened == nil {
		return nil
	}
	aiobject.UseDB(nil)
	err := opened.Close()
	opened = nil
	return err
}
