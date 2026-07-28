// Command migrate-pg-to-sqlite copies a legacy `hanzo_cloud`
// PostgreSQL database into per-(org, user) SQLite files served by the
// Hanzo cloud orchestrator (HIP-0106).
//
// The migrator is introspective: it reads information_schema for the
// table set and columns, then routes each row to its destination file
// based on org_id / user_id columns (or canonical fallbacks). Rows
// with neither column land under /<root>/_global/_org/cloud.sqlite so
// the operator can triage rather than silently dropping them.
//
// Usage:
//
//	migrate-pg-to-sqlite \
//	    --src 'postgres://cloud:pass@postgres.hanzo.svc:5432/hanzo_cloud?sslmode=disable' \
//	    --dst /data
//
// Per ~/work/hanzo/CLAUDE_PG_TO_SQLITE_MIGRATION.md (service #4 — cloud).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/hanzoai/cloud/migration"
)

func main() {
	src := flag.String("src", "", "PostgreSQL DSN (postgres://user:pass@host:5432/dbname)")
	dst := flag.String("dst", "", "Destination data root (default /data)")
	schemas := flag.String("schemas", "public", "Comma-separated PG schemas to include")
	excludes := flag.String("exclude", "schema_migrations,knex_migrations", "Comma-separated table basenames to skip")
	batch := flag.Int("batch", 500, "Insert batch size")
	flag.Parse()
	if *src == "" || *dst == "" {
		fmt.Fprintln(os.Stderr, "usage: migrate-pg-to-sqlite --src <pg-dsn> --dst <data-root>")
		os.Exit(2)
	}
	reports, err := migration.Run(context.Background(), migration.Options{
		SrcDSN:         *src,
		DstRoot:        *dst,
		IncludeSchemas: split(*schemas),
		ExcludeTables:  split(*excludes),
		BatchSize:      *batch,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate-pg-to-sqlite:", err)
		os.Exit(1)
	}
	fmt.Println("table\tsource\twritten\ttargets")
	for _, r := range reports {
		fmt.Printf("%s\t%d\t%d\t%d\n", r.Table, r.SourceRows, r.WrittenRows, len(r.Targets))
	}
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
