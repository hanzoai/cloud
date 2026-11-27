package postgres

// placed.go — a store a MODULE opens, handed to it as pools.
//
// Most stores are opened by this repository's own code, over one *sql.DB, through
// [Store]. A module that opens its own database (hanzoai/base is one: it asks for two
// pools of each of its two databases, in pgx's describe-exec mode, and keeps its own
// plain SQLite file when it has none) cannot take a handle. [Placed] is [Store] for it:
// it says where the store lives now, moves the module's old file to the server once when
// the policy says to, and answers a function that makes the module a pool of its own.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/namespace"
	"github.com/jackc/pgx/v5"
	luxlog "github.com/luxfi/log"
)

// Placed answers the pools of the store (ns, subsystem), or nil when the store stays on
// its file: no CLOUD_SQL_DSN, or a file that the move policy does not move, or one whose
// move failed (it is tried again later, as for [Store]).
//
// legacy is the plain SQLite file the module opened itself. When it exists and the
// registry does not say the store moved, it is moved: open is the module's own open over
// a [Host], run on the schema to make its tables, and then closed; prepare, when set,
// brings the file to the shape the module's current code writes first. The copy is
// [Move]'s, with its row counts and checksums read back and held. A store with no file
// is born on the server.
func Placed(ctx context.Context, log luxlog.Logger, ns namespace.Namespace, subsystem, dir, legacy string, prepare func() error, open func(Host) (io.Closer, error)) (Host, error) {
	dsn := DSN()
	if dsn == "" {
		return nil, nil
	}
	if log == nil {
		log = luxlog.Default()
	}
	schema, err := Schema(ns, subsystem)
	if err != nil {
		return nil, err
	}
	s, err := serverOf(dsn)
	if err != nil {
		return nil, err
	}
	state, _, err := s.state(ctx, schema)
	if err != nil {
		return nil, err
	}
	if state != stateMoved && legacy != "" && fileExists(legacy) {
		mode, err := MoveMode()
		if err != nil {
			return nil, err
		}
		if mode == Never {
			return nil, nil
		}
		if prepare != nil {
			if err := prepare(); err != nil {
				return nil, err
			}
		}
		_, err = move(ctx, log, ns, subsystem, dir, legacy, func(db *sql.DB) (io.Closer, error) {
			if !dialect.Of(db).Postgres() {
				return nopCloser{}, nil // the file was brought up to date by prepare
			}
			return open(s.host(schema))
		}, nil, nil)
		switch {
		case err == nil:
			log.Info("sql move: store moved to hanzo-sql", "subsystem", subsystem, "namespace", ns, "from", legacy)
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.Is(err, ErrMoving):
			return nil, err
		default:
			log.Warn("sql move: the store stays on its file", "subsystem", subsystem, "namespace", ns, "err", err)
			return nil, nil
		}
	}
	if err := s.ensure(schema, ns, subsystem); err != nil {
		return nil, err
	}
	return s.host(schema), nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// Scratch makes a schema nobody else uses and answers pools on it and the function that
// drops it. It is for the open that makes a store's tables while the module it belongs
// to needs a second database on the same engine and has none to spare (hanzoai/base
// runs one dialect over its two databases). Nothing registers it and nothing lives
// beyond drop.
func Scratch(ctx context.Context) (Host, func(), error) {
	dsn := DSN()
	if dsn == "" {
		return nil, nil, errors.New("postgres: " + Env + " is not set, so there is no schema to make")
	}
	s, err := serverOf(dsn)
	if err != nil {
		return nil, nil, err
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, nil, err
	}
	schema := "scratch_" + hex.EncodeToString(b[:])
	if _, err := s.admin.ExecContext(ctx, `CREATE SCHEMA `+quote(schema)); err != nil {
		return nil, nil, err
	}
	drop := func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
		defer cancel()
		_, _ = s.admin.ExecContext(dctx, `DROP SCHEMA IF EXISTS `+quote(schema)+` CASCADE`)
	}
	return s.host(schema), drop, nil
}

// host makes pools on schema, in describe-exec mode.
func (s *server) host(schema string) Host {
	return func() (*sql.DB, error) {
		cfg := s.cfg.Copy()
		cfg.RuntimeParams["search_path"] = schema
		cfg.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
		return pool(cfg, schema), nil
	}
}
