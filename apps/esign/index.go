// The token index is the ONE cross-tenant piece of esign: a signing link is
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

package esign

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hanzoai/cloud/sqlpool"
	_ "github.com/hanzoai/sqlite"
)

type tokenIndex struct{ db *sql.DB }

func openTokenIndex(dataDir string) (*tokenIndex, error) {
	// The SYSTEM namespace, distinct from NewBase's per-tenant tree
	// ({dataDir}/esign/): this global routing table belongs to no org, and the
	// platform partition is one no tenant name can render.
	db, err := sqlpool.Open("token_index", dataDir)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS token_index (token TEXT PRIMARY KEY, org TEXT NOT NULL);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("esign: init token index: %w", err)
	}
	return &tokenIndex{db: db}, nil
}

// put records (or re-points) a signing token to its owning org. Idempotent.
func (x *tokenIndex) put(token, org string) error {
	_, err := x.db.Exec(
		`INSERT INTO token_index (token,org) VALUES (?,?) ON CONFLICT(token) DO UPDATE SET org=excluded.org`,
		token, org)
	return err
}

// org resolves a signing token to its org, or ("",false) if the token is unknown.
func (x *tokenIndex) org(token string) (string, bool, error) {
	var org string
	err := x.db.QueryRow(`SELECT org FROM token_index WHERE token=?`, token).Scan(&org)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return org, true, nil
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
func (x *tokenIndex) record(route, org string, body []byte) error {
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
		if err := x.put(minted.Token, org); err != nil {
			return err
		}
	}
	for _, r := range minted.Recipients {
		if r.Token == "" {
			continue
		}
		if err := x.put(r.Token, org); err != nil {
			return err
		}
	}
	return nil
}

func (x *tokenIndex) Close() error { return x.db.Close() }
