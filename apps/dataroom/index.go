// The link index is the ONE cross-tenant piece of dataroom: a public share link
// is opened anonymously by a visitor who supplies only the link id, so the leaf
// must map that id back to the owning org BEFORE it can select the tenant's
// SQLite store. That mapping is pure routing/infra (Go's job), kept out of the
// per-tenant domain bundle. It lives in a single small SQLite file under the data
// root and is written when a link is created, read on every viewer request.

package dataroom

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/cloud/sqlpool"
	"github.com/hanzoai/namespace"
	_ "github.com/hanzoai/sqlite"
)

type linkIndex struct{ db *sql.DB }

func openLinkIndex(dataDir string) (*linkIndex, error) {
	// The SYSTEM namespace, distinct from NewBase's per-tenant tree
	// ({dataDir}/dataroom/): this global routing table belongs to no org, and the
	// platform partition is one no tenant name can render.
	db, err := cek.Open(namespace.System(), "link_index", dataDir)
	if err != nil {
		return nil, fmt.Errorf("dataroom: open link index: %w", err)
	}
	sqlpool.Single(db)
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS link_index (link_id TEXT PRIMARY KEY, org TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS trust_index (slug TEXT PRIMARY KEY, org TEXT NOT NULL UNIQUE);
`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("dataroom: init link index: %w", err)
	}
	return &linkIndex{db: db}, nil
}

// The store keeps the name link_index because cek derives its key from that name
// and keeps the wrapped key beside the file as link_index.db.dek — renaming the
// store to suit a second table would strand the key and lose every mapping in it.
// The two tables answer the same question for two public addresses: which org owns
// the id a visitor arrived holding.

// publish points a trust center's public slug at its org, and is how an org OPTS IN
// to being readable by name. A public read resolves through here and through
// nothing else, so an org that has not published is not addressable — the same
// property the link index gives a share link, for the same reason: a tenant store
// must never be selected by a string the caller invented.
//
// The slug is unique across the deployment and an org holds at most one, so
// re-publishing under a new slug MOVES the center rather than leaving the old
// address answering.
func (x *linkIndex) publish(slug, org string) error {
	tx, err := x.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var owner string
	switch err := tx.QueryRow(`SELECT org FROM trust_index WHERE slug=?`, slug).Scan(&owner); {
	case err == nil && owner != org:
		return errSlugTaken
	case err != nil && err != sql.ErrNoRows:
		return err
	}
	if _, err := tx.Exec(`DELETE FROM trust_index WHERE org=?`, org); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO trust_index (slug,org) VALUES (?,?)`, slug, org); err != nil {
		return err
	}
	return tx.Commit()
}

// errSlugTaken reports that another org already answers at that public address.
var errSlugTaken = errors.New("that address is taken")

// withdraw removes an org's public address. Its artifacts and grants are untouched
// — only the anonymous address stops resolving.
func (x *linkIndex) withdraw(org string) error {
	_, err := x.db.Exec(`DELETE FROM trust_index WHERE org=?`, org)
	return err
}

// resolve answers which org publishes at slug, or ("",false) when nobody does.
func (x *linkIndex) resolve(slug string) (string, bool, error) {
	var org string
	err := x.db.QueryRow(`SELECT org FROM trust_index WHERE slug=?`, slug).Scan(&org)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return org, true, nil
}

// slugOf answers the org's own public address, or "" when it has not published.
func (x *linkIndex) slugOf(org string) (string, error) {
	var slug string
	err := x.db.QueryRow(`SELECT slug FROM trust_index WHERE org=?`, org).Scan(&slug)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return slug, err
}

// centers lists every published trust center, address and owner. This is the ONE
// cross-tenant read in the subsystem and the only caller is the platform roster,
// which is refused to anyone who is not a SuperAdmin.
func (x *linkIndex) centers() ([]struct{ Slug, Org string }, error) {
	rows, err := x.db.Query(`SELECT slug,org FROM trust_index ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []struct{ Slug, Org string }
	for rows.Next() {
		var r struct{ Slug, Org string }
		if err := rows.Scan(&r.Slug, &r.Org); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
