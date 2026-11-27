// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hanzoai/namespace"
	"github.com/hanzoai/o11y/pkg/factory"
	o11yrt "github.com/hanzoai/o11y/pkg/o11y"
	"github.com/hanzoai/o11y/pkg/sqlstore/postgressqlstore"
	"github.com/hanzoai/sqlite"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/hanzoai/cloud/sdk/locus"
)

// legacyStore is the file the control plane lived in before it ran on hanzo-sql. It is
// plain sqlite, written by hanzoai/o11y's own provider, not a locus store.
const legacyStore = "o11y.db"

// openControlStore gives the o11y runtime its control-plane store on hanzo-sql: the
// dashboards, alerts, channels, keys and users the runtime keeps, as one schema
// (subsystem "o11y") drawing on the process's connection budget. It answers false when
// the deployment keeps no stores on hanzo-sql, and the runtime opens its sqlite file.
//
// A file left by an earlier run is carried once: both sides are brought to the current
// version, the target is emptied, every shared table is copied in one transaction and
// counted against the source, and only then is the file set aside as o11y.db.carried.
// A carry that fails leaves the file where it was and the runtime unstarted, so the
// next start tries again over an empty schema, never over half a copy.
func openControlStore(ctx context.Context, dataDir string) (bool, error) {
	if locus.DSN() == "" {
		return false, nil
	}
	st, err := locus.Use(ctx, nil, namespace.System(), "o11y", dataDir, func(db *sql.DB) (*controlStore, error) {
		if err := carryLegacy(ctx, db, filepath.Join(dataDir, legacyStore)); err != nil {
			return nil, err
		}
		return &controlStore{db}, nil
	})
	if err != nil {
		return false, fmt.Errorf("open the o11y store: %w", err)
	}
	postgressqlstore.UsePool(st.DB)
	return true, nil
}

// controlStore is the schema's pool, held for the life of the process.
type controlStore struct{ *sql.DB }

func carryLegacy(ctx context.Context, to *sql.DB, path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	settings := providerSettings()

	from, err := sql.Open("sqlite", sqlite.PragmaDSN(path, []sqlite.Pragma{
		{Name: "busy_timeout", Value: "10000"},
		{Name: "journal_mode", Value: "wal"},
		{Name: "foreign_keys", Value: "1"},
	}))
	if err != nil {
		return err
	}
	closed := false
	closeSource := func() {
		if !closed {
			closed = true
			_ = from.Close()
		}
	}
	defer closeSource()

	if err := o11yrt.MigrateSQL(ctx, settings, from, false); err != nil {
		return fmt.Errorf("bring the o11y file to the current version: %w", err)
	}
	if err := o11yrt.MigrateSQL(ctx, settings, to, true); err != nil {
		return fmt.Errorf("migrate the o11y schema: %w", err)
	}

	began := time.Now()
	carried, err := o11yrt.CarrySQLite(ctx, from, to)
	if err != nil {
		return fmt.Errorf("carry the o11y file to hanzo-sql: %w", err)
	}
	var rows int64
	for _, n := range carried {
		rows += n
	}
	slog.Info("o11y: carried the control plane to hanzo-sql", "tables", len(carried), "rows", rows, "took", time.Since(began).String())

	// The source is closed (and its write-ahead log folded in) before it is set aside.
	closeSource()
	set := path + ".carried"
	if err := os.Rename(path, set); err != nil {
		return fmt.Errorf("set the o11y file aside: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Rename(path+suffix, set+suffix); err != nil && !os.IsNotExist(err) {
			slog.Warn("o11y: could not set aside a sqlite sidecar", "file", path+suffix, "err", strconv.Quote(err.Error()))
		}
	}
	return nil
}

// providerSettings are the settings the migrations log and measure through; the runtime
// proper builds its own.
func providerSettings() factory.ProviderSettings {
	return factory.ProviderSettings{
		Logger:               slog.Default(),
		MeterProvider:        noop.NewMeterProvider(),
		TracerProvider:       tracenoop.NewTracerProvider(),
		PrometheusRegisterer: prometheus.NewRegistry(),
	}
}
