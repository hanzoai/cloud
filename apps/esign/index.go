// The token index is the ONE cross-tenant provider of esign: a signing link is
// opened anonymously by a recipient who holds only the token, so the leaf must
// map that token back to the owning org BEFORE it can select the tenant's
// SQLite store. Selecting the store from the caller's own `:org` segment first
// is what let ANY string mint a tenant database — opening a per-tenant store
// creates the encrypted file and runs the schema DDL, so a refusal computed
// afterwards arrives after the file exists. The token is the credential, so the
// token is what resolves first.
//
// That mapping is pure routing (Go's job), kept out of the per-tenant domain
// bundle. It lives in a single small SQLite file under the data root, written
// when a recipient is added or a document is sent, read on every signing
// request. It is the same shape dataroom's link index has, for the same reason.
//
// It is the one store of esign that runs on hanzo-sql. The documents themselves are
// the goja Base's, per-tenant files whose SQL is written by the signing bundle in
// SQLite's dialect, and they are not ported with it.
//
// THE TOKEN IS NOT STORED. It is a bearer credential, looked up by its value and never
// read back, so the row is keyed by its blind index (internal/org, field.go): an HMAC of
// the token under the namespace's blind key. A reader of the table, a snapshot or a
// backup holds no token that opens a signing link, and cannot tell which rows share an
// org from the key alone. A file from before this keyed its rows by the token itself;
// the migration re-keys them once, in place, before any read.

package esign

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
	_ "github.com/hanzoai/sqlite"
)

// subsystem names the index: the system namespace's "token_index". The database and the
// blind key are both made from it, so the key and its schema cannot name two things.
const subsystem = "token_index"

type tokenIndex struct {
	db     *sql.DB
	fields *tenure.Fields
}

// blind is the key a token is stored under.
func (x *tokenIndex) blind(token string) string {
	return x.fields.Blind("token_index", "token", token)
}

// openTokenIndex opens the index. master is cloud.Deps.MasterKey; with none the index
// does not open, so no row is ever keyed by a bare token because a key was missing.
func openTokenIndex(dataDir string, master []byte) (*tokenIndex, error) {
	c, err := tenure.NewCipher(master)
	if err != nil {
		return nil, fmt.Errorf("esign: init token index: %w", err)
	}
	fields, err := c.Fields(namespace.System(), subsystem)
	if err != nil {
		return nil, fmt.Errorf("esign: init token index: %w", err)
	}
	// The SYSTEM namespace, distinct from NewBase's per-tenant tree
	// ({dataDir}/esign/): this global routing table belongs to no org, and the
	// platform partition is one no tenant name can render.
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dataDir, func(db *sql.DB) (*tokenIndex, error) {
		x := &tokenIndex{db: db, fields: fields}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		d := dialect.Of(db)
		if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
			// A file from before the tokens were blinded has a `token` column holding the
			// credential itself. Set it aside, make the table, and carry each row across under
			// its blind. hanzo-sql tables are born blinded.
			var legacy bool
			if !d.Postgres() {
				var err error
				if legacy, err = d.HasColumn(ctx, tx, "token_index", "token"); err != nil {
					return err
				}
				if legacy {
					if _, err := tx.ExecContext(ctx, `ALTER TABLE token_index RENAME TO token_index_plain`); err != nil {
						return err
					}
				}
			}
			if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS token_index (blind TEXT PRIMARY KEY, org TEXT NOT NULL)`); err != nil {
				return err
			}
			if legacy {
				return x.rekey(ctx, tx)
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("esign: init token index: %w", err)
		}
		return x, nil
	})
}

// rekey moves the rows of the plaintext table into the blinded one and drops it.
func (x *tokenIndex) rekey(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT token, org FROM token_index_plain`)
	if err != nil {
		return err
	}
	var held [][2]string
	for rows.Next() {
		var token, owner string
		if err := rows.Scan(&token, &owner); err != nil {
			_ = rows.Close()
			return err
		}
		held = append(held, [2]string{token, owner})
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, h := range held {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO token_index (blind, org) VALUES ($1,$2) ON CONFLICT(blind) DO UPDATE SET org=excluded.org`,
			x.blind(h[0]), h[1]); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DROP TABLE token_index_plain`)
	return err
}

// put records (or re-points) a signing token to its owning org. Idempotent.
func (x *tokenIndex) put(token, owner string) error {
	if err := dialect.Text(token, owner); err != nil {
		return fmt.Errorf("esign: index token: %w", err)
	}
	_, err := x.db.Exec(
		`INSERT INTO token_index (blind,org) VALUES ($1,$2) ON CONFLICT(blind) DO UPDATE SET org=excluded.org`,
		x.blind(token), owner)
	return err
}

// org resolves a signing token to its org, or ("",false) if the token is unknown.
func (x *tokenIndex) org(token string) (string, bool, error) {
	if token == "" {
		return "", false, nil
	}
	var owner string
	err := x.db.QueryRow(`SELECT org FROM token_index WHERE blind=$1`, x.blind(token)).Scan(&owner)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return owner, true, nil
}

// record indexes the tokens an owner call just minted. Exactly two routes mint
// them — adding a recipient answers that recipient's token, and sending a
// document answers the token of every signing recipient — and every other route
// is a no-op, so this reads the answer rather than being told which route it is
// looking at.
//
// Both points are indexed because a token is live the moment it exists: the
// signing endpoint looks the token up and never asks whether the document was sent.
// Sending re-points every token it names, which is what heals an index that has
// fallen behind its tenant store.
func (x *tokenIndex) record(route, owner string, body []byte) error {
	switch route {
	case "recipients.add", "documents.send":
	default:
		return nil
	}
	var minted struct {
		Token      string `json:"token"`
		Recipients []struct {
			Token string `json:"token"`
		} `json:"recipients"`
	}
	if err := json.Unmarshal(body, &minted); err != nil {
		return fmt.Errorf("esign: read minted tokens from %s: %w", route, err)
	}
	if minted.Token != "" {
		if err := x.put(minted.Token, owner); err != nil {
			return err
		}
	}
	for _, r := range minted.Recipients {
		if r.Token == "" {
			continue
		}
		if err := x.put(r.Token, owner); err != nil {
			return err
		}
	}
	return nil
}

func (x *tokenIndex) Close() error { return x.db.Close() }
