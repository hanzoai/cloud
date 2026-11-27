// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

// sql.go decides where each org's Base keeps its two databases, and moves them.
//
// A Base is two databases, data (collections, records, auth, settings) and
// auxiliary (logs). With CLOUD_SQL_DSN set each is a schema on hanzo-sql, one per
// org per database, named and registered by locus like every other store
// (subsystems "base" and "base-aux"); without it each is the SQLite file in the
// org's directory, as it always was. Which one a Base is on is the registry's word
// and the file's presence (locus.WhereAt), never a flag: a Base with a file that
// has not been moved is its file, a Base with no file is born on the server, a
// Base whose registry row says moved is the schema and its file is a dead copy.
//
// THE MOVE is locus.MoveFile: the schema is made empty, Base's own migrations and
// the org's own collections make its tables, every table of the file is copied under
// the file's write lock, and each is read back and held to the file's row count and
// checksum before the registry says moved. Data moves first and auxiliary only if
// data did; a Base whose auxiliary move fails serves its logs from the file and the
// rest from the server, which Base allows (the two are independent databases).
//
// WHO WAITS. The move runs in the open of the org's Base, which the pool single-
// flights per org: requests for that org wait for it (a bounded wait, then 503) and
// no other org waits for anything.

import (
	"context"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	baseapp "github.com/hanzoai/base"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/goja"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
)

// The two subsystems a Base's databases are stored as, and the files they are on
// disk. The names of the files are Base's own.
const (
	dataSub = "base"
	auxSub  = "base-aux"

	dataFile = "data.db"
	auxFile  = "auxiliary.db"
)

// Pool sizing for a Base on hanzo-sql. Base asks the host twice for each database,
// one pool for readers and one for the writer, and every pool draws on the process's
// one connection budget, so these bound a Base's share rather than add to it.
const (
	placedDataConns = 8
	placedAuxConns  = 2
	placedIdleConns = 1
)

// dialer opens a pool of its own on a schema each time it is called.
type dialer = func() (*sql.DB, error)

// enc is the alphabet a tenant segment is written in (goja.TenantSegment).
var enc = base32.StdEncoding.WithPadding(base32.NoPadding)

// namespaceOf names the namespace an org's Base is stored under. The platform's own
// Base, which belongs to no tenant, is the system namespace.
func namespaceOf(org string) (namespace.Namespace, error) {
	if org == "" {
		return namespace.System(), nil
	}
	return principal.OrgNamespace(org, "")
}

// orgOf reads the tenant out of a Base directory's name. The name is the tenant
// segment, a bijection with the org, so this is a decode and then a check that
// re-encoding gives the name back, which is what makes a stray directory an error
// instead of some other tenant.
func orgOf(seg string) (string, error) {
	if seg == platformSeg {
		return "", nil
	}
	raw, err := enc.DecodeString(strings.ToUpper(seg))
	if err != nil {
		return "", fmt.Errorf("%q is not a Base directory", seg)
	}
	org := string(raw)
	if goja.TenantSegment(org) != seg {
		return "", fmt.Errorf("%q does not encode the org it decodes to", seg)
	}
	return org, nil
}

// configure answers how the Base of org, whose directory is dir, is opened: the
// embedded files with no DSN, else each database where it lives now, moving a file
// first when the policy (CLOUD_SQL_MOVE) says to. It is the ONE place this package
// builds a baseapp.Config, so no Base opens without the question being asked.
//
// An error means the Base must not open: a Base opened on its file while its rows
// are being moved, or while hanzo-sql cannot be asked where they are, would be a
// second copy of the org's data.
func configure(ctx context.Context, log luxlog.Logger, org, dir string) (baseapp.Config, error) {
	cfg := baseapp.Config{DefaultDataDir: dir, HideStartBanner: true}

	// Uploaded files are in the object store wherever it is configured, whether or
	// not the rows are on hanzo-sql: a file is not a thing a pod keeps.
	files, backups, err := filesFor(ctx, org, dir)
	if err != nil {
		return cfg, err
	}
	cfg.Files, cfg.BackupFiles = files, backups

	if locus.DSN() == "" {
		return cfg, nil
	}
	ns, err := namespaceOf(org)
	if err != nil {
		return cfg, err
	}
	mode, err := locus.MoveMode()
	if err != nil {
		return cfg, err
	}
	if mode != locus.Never {
		if err := move(ctx, log, ns, dir); err != nil {
			return cfg, err
		}
	}

	data, err := locus.WhereAt(ns, dataSub, filepath.Join(dir, dataFile))
	if err != nil {
		return cfg, err
	}
	aux, err := locus.WhereAt(ns, auxSub, filepath.Join(dir, auxFile))
	if err != nil {
		return cfg, err
	}
	if data != aux {
		return cfg, fmt.Errorf("the Base's data is %s and its auxiliary database is %s, and a Base has one dialect: set %s to move the rest, or revert the move", where(data), where(aux), locus.MoveEnv)
	}
	if data == locus.OnServer {
		cfg.DataDB = func() (*sql.DB, error) { return locus.Described(ns, dataSub) }
		cfg.DataMaxOpenConns, cfg.DataMaxIdleConns = placedDataConns, placedIdleConns
	}
	if aux == locus.OnServer {
		cfg.AuxDB = func() (*sql.DB, error) { return locus.Described(ns, auxSub) }
		cfg.AuxMaxOpenConns, cfg.AuxMaxIdleConns = placedAuxConns, placedIdleConns
	}
	return cfg, nil
}

func where(p locus.Place) string {
	if p == locus.OnServer {
		return "on hanzo-sql"
	}
	return "a file"
}

// move moves the Base in dir from its files to hanzo-sql, as far as it can. A move
// that fails leaves the Base on its files and is not an error here (the registry
// says why and the next open tries again after locus's backoff); a move in
// progress elsewhere, or a cancelled context, is.
func move(ctx context.Context, log luxlog.Logger, ns namespace.Namespace, dir string) error {
	dataPath, auxPath := filepath.Join(dir, dataFile), filepath.Join(dir, auxFile)

	var (
		settled bool
		schema  []byte // the collections of the file, as the schema must make them
	)
	settle := func() error {
		if settled {
			return nil
		}
		raw, err := collectionsOf(dir)
		if err != nil {
			return err
		}
		schema, settled = raw, true
		return nil
	}

	for _, d := range []struct {
		sub, path string
		make      func(dial dialer) error
	}{
		{dataSub, dataPath, func(dial dialer) error { return makeData(dial, schema) }},
		{auxSub, auxPath, makeAux},
	} {
		place, err := locus.WhereAt(ns, d.sub, d.path)
		if err != nil {
			return err
		}
		if place == locus.OnServer {
			continue
		}
		rep, err := locus.MoveFile(ctx, log, ns, d.sub, locus.Files{Path: d.path, Settle: settle, Make: d.make})
		switch {
		case err == nil && rep.Already:
		case err == nil:
			log.Info("base: moved to hanzo-sql", "namespace", ns, "database", d.sub,
				"tables", len(rep.Tables), "rows", rep.Rows, "bytes", rep.Bytes, "took", rep.Took.Round(time.Millisecond))
		case errors.Is(err, locus.ErrMoving), ctx.Err() != nil:
			return err
		default:
			log.Warn("base: stays on its file", "namespace", ns, "database", d.sub, "err", err)
			if d.sub == auxSub {
				// A Base has one dialect, so it is wholly its files or wholly the
				// server. Data moved and nothing has written to it yet: put it back.
				if rerr := locus.Revert(ctx, ns, dataSub); rerr != nil {
					return fmt.Errorf("the auxiliary database did not move (%v) and the data could not be put back: %w", err, rerr)
				}
			}
			return nil
		}
	}
	return nil
}

// collectionsOf opens the Base in dir as it is today, which brings its files to the
// shape this binary writes, and answers its collections as Base exports them. It
// closes the Base before it answers: the mover holds the file's write lock next, and
// a Base left open would write behind it.
func collectionsOf(dir string) ([]byte, error) {
	app := baseapp.NewWithConfig(baseapp.Config{DefaultDataDir: dir, HideStartBanner: true})
	if err := open(app); err != nil {
		return nil, err
	}
	defer func() { _ = app.ResetBootstrapState() }()
	cols, err := app.FindAllCollections()
	if err != nil {
		return nil, fmt.Errorf("read the collections: %w", err)
	}
	raw, err := json.Marshal(cols)
	if err != nil {
		return nil, fmt.Errorf("encode the collections: %w", err)
	}
	return raw, nil
}

// open bootstraps a Base and brings its schema up to this binary's.
func open(app *baseapp.Base) error {
	if err := app.Bootstrap(); err != nil {
		return err
	}
	if err := app.RunAllMigrations(); err != nil {
		_ = app.ResetBootstrapState()
		return err
	}
	return nil
}

// makeData makes the data schema's tables: Base's own, by its migrations, and the
// org's collections, by importing them (saving a collection is what creates the table
// its records go in, in the server's own spelling, which is why this is not a copy of
// the file's DDL). The rows of every table, the collections' own included, are the
// mover's to copy afterwards.
//
// A Base has ONE dialect, taken from its data database, so its auxiliary database
// cannot be a SQLite file beside a data database on the server: both are asked of
// the same dial here. Base's own suite runs both in one schema. The tables of the
// other database that this makes in the schema stay empty (a move copies only the
// tables the file has) and nothing reads them.
func makeData(dial dialer, collections []byte) error {
	scratch, err := os.MkdirTemp("", "base-make-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	app := baseapp.NewWithConfig(baseapp.Config{DefaultDataDir: scratch, HideStartBanner: true, DataDB: dial, AuxDB: dial})
	if err := open(app); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer func() { _ = app.ResetBootstrapState() }()
	if err := app.ImportCollectionsByMarshaledJSON(collections, false); err != nil {
		return fmt.Errorf("create the collections: %w", err)
	}
	return nil
}

// makeAux makes the auxiliary schema's tables (and the empty data tables beside
// them, for the reason makeData gives).
func makeAux(dial dialer) error {
	scratch, err := os.MkdirTemp("", "base-make-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	app := baseapp.NewWithConfig(baseapp.Config{DefaultDataDir: scratch, HideStartBanner: true, DataDB: dial, AuxDB: dial})
	if err := open(app); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return app.ResetBootstrapState()
}

// sweep moves every Base that is still a file, smallest first, one at a time across
// the deployment (locus.Slot), until none is left or ctx ends. It is the "sweep"
// policy's background half; "lazy" moves a Base when it is first opened.
//
// A Base is moved without being kept open, so a sweep over thousands of orgs holds
// one connection and one Base at a time. A Base a request opened first was moved by
// that request, and the sweep finds it moved.
func (p *pool) sweep(ctx context.Context) {
	mode, err := locus.MoveMode()
	if err != nil || mode != locus.Sweep || locus.DSN() == "" {
		return
	}
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return
	}
	type pending struct {
		org  string
		size int64
	}
	var todo []pending
	for _, e := range entries {
		if !e.IsDir() || e.Name() == platformSeg {
			continue
		}
		org, err := orgOf(e.Name())
		if err != nil {
			continue
		}
		fi, err := os.Stat(filepath.Join(p.dir, e.Name(), dataFile))
		if err != nil {
			continue
		}
		todo = append(todo, pending{org, fi.Size()})
	}
	sort.SliceStable(todo, func(i, j int) bool { return todo[i].size < todo[j].size })

	for _, t := range todo {
		if ctx.Err() != nil {
			return
		}
		release, err := locus.Slot(ctx)
		if err != nil {
			return
		}
		err = p.settle(ctx, t.org)
		release()
		if err != nil && !errors.Is(err, locus.ErrMoving) {
			p.log.Warn("base: sweep", "err", err)
		}
	}
	p.log.Info("base: sweep finished", "bases", len(todo))
}

// settle moves one org's Base if it is a file, and opens nothing.
func (p *pool) settle(ctx context.Context, org string) error {
	ns, err := namespaceOf(org)
	if err != nil {
		return err
	}
	// Only through an open that is single-flighted with the requests: the entry in
	// flight is how a request for this org waits for the sweep, and the reverse.
	seg := goja.TenantSegment(org)
	o, mine := p.begin(seg)
	if !mine {
		return locus.ErrMoving
	}
	err = move(ctx, p.log, ns, filepath.Join(p.dir, seg))
	p.end(seg, o, nil, err)
	return err
}
