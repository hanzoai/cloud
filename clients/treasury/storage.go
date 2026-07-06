package treasury

import (
	"os"
	"strings"
)

// Storage-driver selection for the treasury ledger-of-record. Decomplected into ONE
// pure function so "the default is SQLite" is a single, testable fact rather than an
// implicit else-branch buried in Mount.
const (
	// DriverSQLite is the Hanzo default and what production runs: the per-tenant
	// Hanzo Base (SQLite) files this package's Manager opens under CLOUD_DATA_DIR.
	DriverSQLite = "sqlite"
	// DriverPostgres is the supported OPT-IN: the Formance Postgres ledger of record,
	// selected by wiring FORMANCE_LEDGER_URL. It is never the live default.
	DriverPostgres = "postgres"
)

// StorageDriver reports the storage driver the treasury ledger-of-record will use,
// decided by the environment ALONE:
//
//   - "sqlite" (default) — per-tenant Hanzo Base files. This is the standing rule:
//     every tenant's books run live on its own Base file.
//   - "postgres"         — set FORMANCE_LEDGER_URL to opt into the Formance Postgres
//     ledger of record for the house books.
//
// This is the ONE place the driver is decided; Mount branches on it and logs it.
func StorageDriver() string {
	if strings.TrimSpace(os.Getenv("FORMANCE_LEDGER_URL")) != "" {
		return DriverPostgres
	}
	return DriverSQLite
}
