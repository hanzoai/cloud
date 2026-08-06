package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var errNotFound = errors.New("sandbox: sandbox not found")

// Sandbox is one leased execution sandbox. It is the SAME record whether the
// lease is seconds long (a function invoke) or a week (a suspended coding
// session) — status and expiresAt carry the difference, not a second table.
//
// Pod is the sandbox's ADDRESS, and it is `json:"-"` on purpose. It is a pod
// name in a namespace a caller has no business knowing, and handing it out both
// maps the cluster for whatever runs inside a sandbox and invites a client to
// try reaching the sandbox directly, which would mean terminating auth somewhere
// other than the IAM edge. The predecessor returned a pod IP in every create,
// get and list response.
type Sandbox struct {
	ID         string `json:"id"`
	Org        string `json:"org"`
	Kind       string `json:"kind"`
	Class      string `json:"class"`
	Project    string `json:"project,omitempty"`
	Status     string `json:"status"` // pending | running | error
	Image      string `json:"image"`
	Pod        string `json:"-"`
	Volume     string `json:"volume,omitempty"`
	Error      string `json:"error,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt int64  `json:"lastUsedAt"`
	ExpiresAt  int64  `json:"expiresAt,omitempty"`
}

// Store is one org's sandbox registry — ONE SQLite file per org at
// {DataDir}/orgs/{orgSlug}/sandbox.db (cloud.OrgDB, HIP-0302). Org isolation is
// PHYSICAL (a different file), with the org column kept as defence in depth so a
// query that somehow reached the wrong file still returns nothing.
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
CREATE TABLE IF NOT EXISTS sandbox (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  kind         TEXT NOT NULL DEFAULT 'sandbox',
  class        TEXT NOT NULL DEFAULT 'exec',
  project      TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'pending',
  image        TEXT NOT NULL DEFAULT '',
  pod          TEXT NOT NULL DEFAULT '',
  volume       TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_machines_org_project ON sandbox(org, project);
CREATE INDEX IF NOT EXISTS ix_machines_org_status  ON sandbox(org, status);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Put(ctx context.Context, m Sandbox) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sandbox (id,org,kind,class,project,status,image,pod,volume,error,created_at,last_used_at,expires_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  status=excluded.status, image=excluded.image, pod=excluded.pod, volume=excluded.volume,
  error=excluded.error, last_used_at=excluded.last_used_at, expires_at=excluded.expires_at`,
		m.ID, m.Org, m.Kind, m.Class, m.Project, m.Status, m.Image, m.Pod, m.Volume,
		m.Error, m.CreatedAt, m.LastUsedAt, m.ExpiresAt)
	return err
}

// Get is org-scoped in the WHERE clause, always. The file is already per-org;
// this is the second lock on the same door.
func (s *Store) Get(ctx context.Context, org, id string) (Sandbox, error) {
	row := s.db.QueryRowContext(ctx, selectCols+` WHERE org=? AND id=?`, org, id)
	return scanMachine(row)
}

// Live is the single-attach check: the sandbox, if any, currently holding this
// project's volume.
func (s *Store) Live(ctx context.Context, org, project string) (Sandbox, error) {
	row := s.db.QueryRowContext(ctx,
		selectCols+` WHERE org=? AND project=? AND status IN ('running','pending') LIMIT 1`, org, project)
	m, err := scanMachine(row)
	if err == errNotFound {
		return Sandbox{}, nil
	}
	return m, err
}

func (s *Store) List(ctx context.Context, org, project, status string) ([]Sandbox, error) {
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
	out := []Sandbox{}
	for rows.Next() {
		var m Sandbox
		if err := rows.Scan(&m.ID, &m.Org, &m.Kind, &m.Class, &m.Project, &m.Status,
			&m.Image, &m.Pod, &m.Volume, &m.Error, &m.CreatedAt, &m.LastUsedAt, &m.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Expired is what a reaper reads: every sandbox whose lease has run out.
func (s *Store) Expired(ctx context.Context, org string, now int64) ([]Sandbox, error) {
	rows, err := s.db.QueryContext(ctx,
		selectCols+` WHERE org=? AND expires_at>0 AND expires_at<? ORDER BY expires_at LIMIT 200`, org, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Sandbox{}
	for rows.Next() {
		var m Sandbox
		if err := rows.Scan(&m.ID, &m.Org, &m.Kind, &m.Class, &m.Project, &m.Status,
			&m.Image, &m.Pod, &m.Volume, &m.Error, &m.CreatedAt, &m.LastUsedAt, &m.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Delete(ctx context.Context, org, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sandbox WHERE org=? AND id=?`, org, id)
	return err
}

const selectCols = `SELECT id,org,kind,class,project,status,image,pod,volume,error,created_at,last_used_at,expires_at FROM sandbox`

func scanMachine(row *sql.Row) (Sandbox, error) {
	var m Sandbox
	err := row.Scan(&m.ID, &m.Org, &m.Kind, &m.Class, &m.Project, &m.Status,
		&m.Image, &m.Pod, &m.Volume, &m.Error, &m.CreatedAt, &m.LastUsedAt, &m.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Sandbox{}, errNotFound
	}
	return m, err
}

func genID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return IDPrefix + hex.EncodeToString(b[:]), nil
}

// podName is the sandbox's address, minted once per sandbox and NEVER REUSED.
// That is the whole property: an address that can be recycled is an address a
// second tenant can be handed, and no credential check downstream can undo it.
func podName(id string) string { return "m-" + strings.TrimPrefix(id, IDPrefix) }

// volumeName is the per-(org, project) disk. Deterministic, so a resume finds
// the SAME disk without a lookup, and DNS-safe because Kubernetes object names
// are.
//
// THE HASH IS LOad-BEARING. A name built from slug(org) alone folds distinct
// orgs together — slug lowercases, strips everything outside [a-z0-9-] and caps
// the length, which is exactly the fold principal.Org refuses to do because
// "folding collapses DISTINCT owners into one bucket, itself a cross-org break".
// "Acme" and "acme", "acme" and "acme!", and any two orgs agreeing in their
// first N characters all produced ONE volume name. The suffix is over the
// UNFOLDED org and project, so the readable part can be squeezed to fit 63
// characters without ever making two tenants share a disk.
func volumeName(org, project string) string {
	sum := sha256.Sum256([]byte(org + "\x00" + project))
	tail := "-" + hex.EncodeToString(sum[:5])
	head := "m-" + slug(org) + "-" + slug(project)
	if max := 63 - len(tail); len(head) > max {
		head = head[:max]
	}
	return strings.TrimRight(head, "-") + tail
}

// slug makes a Kubernetes-safe label out of arbitrary text. It is LOSSY, which
// is why nothing that must stay distinct is ever derived from it alone.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == '_', r == '/', r == '.', r == ' ':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 48 {
		out = strings.Trim(out[:48], "-")
	}
	return out
}
