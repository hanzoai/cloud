package sandbox

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var errNotFound = errors.New("sandbox: box not found")

// Box is one execution box. It is the SAME record whether the box lives for a
// second (an interpreter call) or a week (a suspended coding session) — status
// and expiresAt carry the difference, not a second table.
//
// Host is the in-cluster address of the pod currently serving it, and is empty
// for a suspended box. PVC survives suspend: it is where the checkout and the
// dependency caches live, and it is the reason resume is cheap.
type Box struct {
	ID         string `json:"id"`
	Org        string `json:"org"`
	Project    string `json:"project"`
	Class      string `json:"class"`
	Status     string `json:"status"` // pending | running | suspended | error
	Image      string `json:"image"`
	Host       string `json:"host,omitempty"`
	PVC        string `json:"pvc,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Target     string `json:"target,omitempty"` // the /v1/agents target this box registered as
	Error      string `json:"error,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt int64  `json:"lastUsedAt"`
	ExpiresAt  int64  `json:"expiresAt,omitempty"`
}

// Store is one org's box registry — ONE SQLite file per org at
// {DataDir}/orgs/{orgSlug}/sandbox.db (cloud.OrgDB, HIP-0302). Org isolation is
// PHYSICAL (a different file), with the org column kept as defence in depth so
// a query that somehow reached the wrong file still returns nothing.
type Store struct{ db *sql.DB }

func openStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS boxes (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  project      TEXT NOT NULL DEFAULT '',
  class        TEXT NOT NULL DEFAULT 'dev',
  status       TEXT NOT NULL DEFAULT 'pending',
  image        TEXT NOT NULL DEFAULT '',
  host         TEXT NOT NULL DEFAULT '',
  pvc          TEXT NOT NULL DEFAULT '',
  ref          TEXT NOT NULL DEFAULT '',
  target       TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_boxes_org_project ON boxes(org, project);
CREATE INDEX IF NOT EXISTS ix_boxes_org_status  ON boxes(org, status);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Put(ctx context.Context, b Box) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO boxes (id,org,project,class,status,image,host,pvc,ref,target,error,created_at,last_used_at,expires_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  status=excluded.status, image=excluded.image, host=excluded.host, pvc=excluded.pvc,
  ref=excluded.ref, target=excluded.target, error=excluded.error,
  last_used_at=excluded.last_used_at, expires_at=excluded.expires_at`,
		b.ID, b.Org, b.Project, b.Class, b.Status, b.Image, b.Host, b.PVC, b.Ref,
		b.Target, b.Error, b.CreatedAt, b.LastUsedAt, b.ExpiresAt)
	return err
}

func (s *Store) Get(ctx context.Context, org, id string) (Box, error) {
	// org is in the WHERE clause, always. The file is already per-org; this is
	// the second lock on the same door.
	row := s.db.QueryRowContext(ctx, selectCols+` WHERE org=? AND id=?`, org, id)
	return scanBox(row)
}

// Live is the single-attach check: the box, if any, currently holding this
// project's volume. Only running/pending count — a suspended box has released
// the pod and its volume is free to reattach.
func (s *Store) Live(ctx context.Context, org, project string) (Box, error) {
	row := s.db.QueryRowContext(ctx,
		selectCols+` WHERE org=? AND project=? AND status IN ('running','pending') LIMIT 1`, org, project)
	b, err := scanBox(row)
	if err == errNotFound {
		return Box{}, nil
	}
	return b, err
}

func (s *Store) List(ctx context.Context, org, project, status string) ([]Box, error) {
	q := selectCols + ` WHERE org=?`
	args := []any{org}
	if project != "" {
		q, args = q+` AND project=?`, append(args, project)
	}
	if status != "" {
		q, args = q+` AND status=?`, append(args, status)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Box{}
	for rows.Next() {
		var b Box
		if err := rows.Scan(&b.ID, &b.Org, &b.Project, &b.Class, &b.Status, &b.Image,
			&b.Host, &b.PVC, &b.Ref, &b.Target, &b.Error, &b.CreatedAt, &b.LastUsedAt, &b.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) Delete(ctx context.Context, org, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM boxes WHERE org=? AND id=?`, org, id)
	return err
}

const selectCols = `SELECT id,org,project,class,status,image,host,pvc,ref,target,error,created_at,last_used_at,expires_at FROM boxes`

func scanBox(row *sql.Row) (Box, error) {
	var b Box
	err := row.Scan(&b.ID, &b.Org, &b.Project, &b.Class, &b.Status, &b.Image,
		&b.Host, &b.PVC, &b.Ref, &b.Target, &b.Error, &b.CreatedAt, &b.LastUsedAt, &b.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Box{}, errNotFound
	}
	return b, err
}

func genID(prefix string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// pvcName is the per-(org, project) volume. Deterministic, so a resume finds the
// SAME disk without a lookup, and DNS-safe because Kubernetes object names are.
func pvcName(org, project string) string {
	n := "box-" + sanitize(org) + "-" + project
	if len(n) > 63 {
		n = n[:63]
	}
	return strings.Trim(n, "-")
}
