package postgres

import (
	"database/sql"
	"time"
)

// BackOff sets how long a failed move keeps a store a file, and returns what restores
// it, for the tests of the package that move a store again.
func BackOff(d time.Duration) (restore func()) {
	old := moveBackoff
	moveBackoff = d
	return func() { moveBackoff = old }
}

// Admin is the one connection pool the server at dsn keeps for provisioning, leases
// and registry reads, limited to one connection so that every ask runs on the same
// backend and a test can read that backend's own counters.
func Admin(dsn string) (*sql.DB, error) {
	s, err := serverOf(dsn)
	if err != nil {
		return nil, err
	}
	s.admin.SetMaxOpenConns(1)
	return s.admin, nil
}
