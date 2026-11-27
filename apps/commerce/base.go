package commerce

// base.go — commerce's own Base, placed off the volume.
//
// Besides its per-org datastore (seam, use.go) the commerce module keeps one Base: its
// cache collection (commerce_kv) and its settings, in two databases that hanzoai/base
// opens itself, <dir>/base/data.db and <dir>/base/auxiliary.db, and a filesystem for
// uploaded files. A deployment that keeps no state on a disk gives the module all three:
// the databases are two schemas of hanzo-sql (subsystems baseData and baseAux, moved once
// from the old plain files under CLOUD_SQL_MOVE, with every row counted and checksummed)
// and the files are a bucket of the object store.
//
// With no CLOUD_SQL_DSN, or a move the policy does not make or that failed, the module
// keeps the files it always had.

import (
	"context"
	"errors"
	"github.com/hanzoai/cloud/sdk/object"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/base/tools/filesystem"
	cstore "github.com/hanzoai/commerce/store"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
)

// The two databases of the module's Base, as stores of the data plane: the names are
// also the registry's, and commerce is the app that owns their connections
// (manifest.SQLStores).
const (
	baseData = "commerce-base-data"
	baseAux  = "commerce-base-aux"
)

// baseBucket is where the Base's uploaded files live.
var baseBucket = func() string { return env.Or("COMMERCE_BASE_BUCKET", "hanzo-commerce") }

// placeBase places the module's Base. dir is the module's data directory; its Base
// files are under dir/base, where the module opened them before. A zero result is the
// files.
func placeBase(ctx context.Context, lg luxlog.Logger, dir string) (cstore.Host, error) {
	if locus.DSN() == "" {
		return cstore.Host{}, nil
	}
	if lg == nil {
		lg = luxlog.Default()
	}
	legacy := filepath.Join(dir, "base")
	ns := namespace.System()

	// The old files, brought to the shape the module's current code writes, once, before
	// either is read.
	var once sync.Once
	var prepared error
	prepare := func() error {
		once.Do(func() {
			s, err := cstore.New(cstore.Config{DataDir: legacy})
			if err != nil {
				prepared = err
				return
			}
			prepared = s.Close(nil)
		})
		return prepared
	}

	// hanzoai/base runs one dialect over its two databases, so making the tables of one
	// needs the other on the same engine: a scratch schema stands in for it.
	tables := func(data bool) func(locus.Host) (io.Closer, error) {
		return func(host locus.Host) (io.Closer, error) {
			other, drop, err := locus.Scratch(ctx)
			if err != nil {
				return nil, err
			}
			h := cstore.Host{Data: other, Aux: host}
			if data {
				h = cstore.Host{Data: host, Aux: other}
			}
			c, err := makeTables(h)
			if err != nil {
				drop()
				return nil, err
			}
			return closer(func() error { err := c.Close(); drop(); return err }), nil
		}
	}

	var h cstore.Host
	var err error
	if h.Data, err = locus.Placed(ctx, lg, ns, baseData, dir, filepath.Join(legacy, "data.db"), prepare, tables(true)); err != nil {
		return cstore.Host{}, err
	}
	if h.Aux, err = locus.Placed(ctx, lg, ns, baseAux, dir, filepath.Join(legacy, "auxiliary.db"), prepare, tables(false)); err != nil {
		return cstore.Host{}, err
	}
	if h.Data == nil && h.Aux == nil {
		return cstore.Host{}, nil
	}
	// One engine for both: a database that stayed on its file beside one that moved is
	// a Base the module cannot run. The data is the store that matters; the aux holds
	// the Base's request log, which is born on the server when its own move failed.
	if h.Data == nil {
		return cstore.Host{}, errors.New("commerce base: its auxiliary database is on hanzo-sql and its data is still a file")
	}
	if h.Aux == nil {
		lg.Warn("commerce base: the auxiliary database did not move; it starts empty on hanzo-sql")
		if h.Aux, err = locus.Placed(ctx, lg, ns, baseAux, dir, "", nil, tables(false)); err != nil {
			return cstore.Host{}, err
		}
	}
	h.Files = baseFiles()
	return h, nil
}

// makeTables opens the module's Base on the given pools, which brings the tables of the
// database being moved into being, in a directory that is thrown away.
func makeTables(h cstore.Host) (io.Closer, error) {
	scratch, err := os.MkdirTemp("", "commerce-base-")
	if err != nil {
		return nil, err
	}
	s, err := cstore.New(cstore.Config{DataDir: scratch, Host: h})
	if err != nil {
		_ = os.RemoveAll(scratch)
		return nil, err
	}
	return closer(func() error {
		err := s.Close(nil)
		_ = os.RemoveAll(scratch)
		return err
	}), nil
}

type closer func() error

func (c closer) Close() error { return c() }

// baseFiles is the filesystem of the Base's uploaded files: the deployment's object
// store, in baseBucket, reached with the credential the store hands out (the Base
// writes with an S3 client of its own). With none (local disk) it is nil and the Base
// keeps them in its own directory.
func baseFiles() func() (*filesystem.System, error) {
	st, ok := object.Delegate()
	if !ok {
		return nil
	}
	return func() (*filesystem.System, error) {
		return filesystem.NewS3(baseBucket(), st.Region, st.URL, st.AccessKey, st.Secret, st.PathStyle)
	}
}
