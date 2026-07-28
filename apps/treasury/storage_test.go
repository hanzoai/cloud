package treasury

import "testing"

// TestStorageDriver_DefaultsToSQLite pins the standing rule: with no Formance URL
// wired (the production config), the treasury ledger of record runs on the
// per-tenant Hanzo Base (SQLite) files — the default driver is "sqlite".
func TestStorageDriver_DefaultsToSQLite(t *testing.T) {
	t.Setenv("FORMANCE_LEDGER_URL", "") // production: no Postgres/Formance wired
	if got := StorageDriver(); got != DriverSQLite {
		t.Fatalf("StorageDriver() = %q, want %q (Base per-tenant is the prod default)", got, DriverSQLite)
	}
}

// TestStorageDriver_PostgresOptInPreserved proves the Postgres option is intact:
// wiring FORMANCE_LEDGER_URL selects the Postgres (Formance) ledger of record. Kept
// as a supported OPT-IN — just never the live default.
func TestStorageDriver_PostgresOptInPreserved(t *testing.T) {
	t.Setenv("FORMANCE_LEDGER_URL", "http://ledger.formance.svc:8080")
	if got := StorageDriver(); got != DriverPostgres {
		t.Fatalf("StorageDriver() with FORMANCE_LEDGER_URL = %q, want %q (opt-in must still work)", got, DriverPostgres)
	}
}
