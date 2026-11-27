// The link index is the ONE cross-tenant provider of dataroom: a public share link
// is opened anonymously by a visitor who supplies only the link id, so the leaf
// must map that id back to the owning org BEFORE it can select the tenant's
// store. That mapping is pure routing/infra (Go's job), kept out of the
// per-tenant domain bundle. It is one small database in the system namespace,
// "link_index": a schema on hanzo-sql, or a file under the data root when there is
// no DSN. It is written when a link is created and read on every viewer request,
// by whichever replica serves the visitor, so every replica must read the same one.

package dataroom

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	// locus.Use is the ONE opener: on hanzo-sql the index is a schema of the system
	// namespace, moved there from its file when the deployment's policy says to; without
	// a DSN the database is born encrypted under the key cek derives from the process
	// master and that namespace, with the single-connection cap already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	_ "github.com/hanzoai/sqlite"
)

// indexSubsystem names the store in the system namespace; manifest.SQLStores lists it.
const indexSubsystem = "link_index"

// indexWithin bounds one statement of the index: it is read on a visitor's request and
// written on an owner's, and a database that does not answer is a refusal, not a wait.
const indexWithin = 10 * time.Second

type linkIndex struct{ db *sql.DB }

func openLinkIndex(dataDir string) (*linkIndex, error) {
	// The SYSTEM namespace, distinct from NewBase's per-tenant tree
	// ({dataDir}/dataroom/): this global routing table belongs to no org, and the
	// platform partition is one no tenant name can render.
	return locus.Use(context.Background(), nil, namespace.System(), indexSubsystem, dataDir, func(db *sql.DB) (*linkIndex, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS link_index (link_id TEXT PRIMARY KEY, org TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS trust_index (slug TEXT PRIMARY KEY, org TEXT NOT NULL UNIQUE);
`)
			return err
		}); err != nil {
			return nil, fmt.Errorf("dataroom: init link index: %w", err)
		}
		return &linkIndex{db: db}, nil
	})
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
// address answering. The check and the write are one transaction under the store's
// write lock, so two replicas publishing one slug take it in turn and the second is
// told it is taken.
func (x *linkIndex) publish(slug, org string) error {
	ctx, cancel := context.WithTimeout(context.Background(), indexWithin)
	defer cancel()
	tx, err := dialect.Of(x.db).BeginWrite(ctx, x.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var owner string
	switch err := tx.QueryRowContext(ctx, `SELECT org FROM trust_index WHERE slug=$1`, slug).Scan(&owner); {
	case err == nil && owner != org:
		return errSlugTaken
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM trust_index WHERE org=$1`, org); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO trust_index (slug,org) VALUES ($1,$2)`, slug, org); err != nil {
		return err
	}
	return tx.Commit()
}

// errSlugTaken reports that another org already answers at that public address.
var errSlugTaken = errors.New("that address is taken")

// withdraw removes an org's public address. Its artifacts and grants are untouched
// — only the anonymous address stops resolving.
func (x *linkIndex) withdraw(org string) error {
	ctx, cancel := context.WithTimeout(context.Background(), indexWithin)
	defer cancel()
	_, err := x.db.ExecContext(ctx, `DELETE FROM trust_index WHERE org=$1`, org)
	return err
}

// resolve answers which org publishes at slug, or ("",false) when nobody does.
func (x *linkIndex) resolve(slug string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), indexWithin)
	defer cancel()
	var org string
	err := x.db.QueryRowContext(ctx, `SELECT org FROM trust_index WHERE slug=$1`, slug).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return org, true, nil
}

// centers lists every published trust center, address and owner. This is the ONE
// cross-tenant read in the subsystem and the only caller is the platform roster,
// which is refused to anyone who is not a SuperAdmin.
func (x *linkIndex) centers() ([]struct{ Slug, Org string }, error) {
	ctx, cancel := context.WithTimeout(context.Background(), indexWithin)
	defer cancel()
	rows, err := x.db.QueryContext(ctx, `SELECT slug,org FROM trust_index ORDER BY slug`)
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
	ctx, cancel := context.WithTimeout(context.Background(), indexWithin)
	defer cancel()
	_, err := x.db.ExecContext(ctx,
		`INSERT INTO link_index (link_id,org) VALUES ($1,$2) ON CONFLICT(link_id) DO UPDATE SET org=excluded.org`,
		linkID, org)
	return err
}

// org resolves a link id to its org, or ("",false) if the id is unknown.
func (x *linkIndex) org(linkID string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), indexWithin)
	defer cancel()
	var org string
	err := x.db.QueryRowContext(ctx, `SELECT org FROM link_index WHERE link_id=$1`, linkID).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return org, true, nil
}

func (x *linkIndex) Close() error { return x.db.Close() }
