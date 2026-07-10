// The link index is the ONE cross-tenant piece of dataroom: a public share link
// is opened anonymously by a visitor who supplies only the link id, so the leaf
// must map that id back to the owning org BEFORE it can select the tenant's
// SQLite store. That mapping is pure routing/infra (Go's job), kept out of the
// per-tenant domain bundle. It lives in a single small SQLite file under the data
// root and is written when a link is created, read on every viewer request.
package dataroom

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/hanzoai/sqlite"
)

type linkIndex struct{ db *sql.DB }

func openLinkIndex(dataDir string) (*linkIndex, error) {
	// Its OWN dir, distinct from gojabase's per-tenant tree ({dataDir}/dataroom/):
	// this global routing table must never collide with a tenant's DB file.
	dir := filepath.Join(dataDir, "dataroom_index")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("dataroom: mkdir %s: %w", dir, err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "link_index.db"))
	if err != nil {
		return nil, fmt.Errorf("dataroom: open link index: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS link_index (link_id TEXT PRIMARY KEY, org TEXT NOT NULL);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("dataroom: init link index: %w", err)
	}
	return &linkIndex{db: db}, nil
}

// put records (or re-points) a link id to its owning org. Idempotent.
func (x *linkIndex) put(linkID, org string) error {
	_, err := x.db.Exec(
		`INSERT INTO link_index (link_id,org) VALUES (?,?) ON CONFLICT(link_id) DO UPDATE SET org=excluded.org`,
		linkID, org)
	return err
}

// org resolves a link id to its org, or ("",false) if the id is unknown.
func (x *linkIndex) org(linkID string) (string, bool, error) {
	var org string
	err := x.db.QueryRow(`SELECT org FROM link_index WHERE link_id=?`, linkID).Scan(&org)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return org, true, nil
}

func (x *linkIndex) Close() error { return x.db.Close() }
