package sessions

import (
	"database/sql"
	"fmt"
	"time"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both cgo and pure-Go build tags). Blank
	// import registers the driver; importing modernc directly would double-register.
	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// The sessions STORE holds one row per live coding session, following the
// settings-store discipline — Hanzo Base/SQLite, MaxOpenConns(1) to serialize
// writes against the file lock, one file.
//
// Isolation is `subject` on every statement: a session is visible to the org that
// registered it and to nobody else. A terminal is a live shell on a developer's
// machine, so cross-tenant reads are not a feature to be added later.
type Store struct {
	db *sql.DB
}

// Session is one live coding session. ID is chosen by the host agent and is
// stable across heartbeats, so re-registering the same session updates a row
// instead of accumulating duplicates.
type Session struct {
	ID        string `json:"id"`
	Subject   string `json:"-"`
	Host      string `json:"host"`
	Workspace string `json:"workspace"`
	Repo      string `json:"repo,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Agent     string `json:"agent,omitempty"`
	URL       string `json:"url"`
	StartedAt int64  `json:"startedAt"`
	BeatAt    int64  `json:"beatAt"`
}

func openStore(path string) (*Store, error) {
	db, err := cek.Open(cek.Global, path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS sessions (
  subject    TEXT    NOT NULL,
  id         TEXT    NOT NULL,
  host       TEXT    NOT NULL,
  workspace  TEXT    NOT NULL,
  repo       TEXT    NOT NULL DEFAULT '',
  branch     TEXT    NOT NULL DEFAULT '',
  agent      TEXT    NOT NULL DEFAULT '',
  url        TEXT    NOT NULL,
  started_at INTEGER NOT NULL,
  beat_at    INTEGER NOT NULL,
  PRIMARY KEY (subject, id)
);
CREATE INDEX IF NOT EXISTS sessions_beat ON sessions(beat_at);`
	_, err := s.db.Exec(ddl)
	return err
}

// Beat upserts a session. started_at survives a heartbeat so the UI can show how
// long a session has been running; every other field is refreshed, because a
// session that moves branch mid-run should say so.
func (s *Store) Beat(v Session) error {
	const q = `
INSERT INTO sessions (subject, id, host, workspace, repo, branch, agent, url, started_at, beat_at)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(subject, id) DO UPDATE SET
  host=excluded.host, workspace=excluded.workspace, repo=excluded.repo,
  branch=excluded.branch, agent=excluded.agent, url=excluded.url,
  beat_at=excluded.beat_at`
	_, err := s.db.Exec(q, v.Subject, v.ID, v.Host, v.Workspace, v.Repo, v.Branch,
		v.Agent, v.URL, v.StartedAt, v.BeatAt)
	return err
}

// List returns the caller's sessions that have beaten within ttl, newest beat
// first. Liveness is a read-time predicate rather than a reaper: a host that
// loses power stops beating, and stops being listed, without anything having to
// notice it died.
func (s *Store) List(subject string, now time.Time, ttl time.Duration) ([]Session, error) {
	const q = `
SELECT id, host, workspace, repo, branch, agent, url, started_at, beat_at
FROM sessions WHERE subject=? AND beat_at >= ? ORDER BY beat_at DESC`
	rows, err := s.db.Query(q, subject, now.Add(-ttl).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		v := Session{Subject: subject}
		if err := rows.Scan(&v.ID, &v.Host, &v.Workspace, &v.Repo, &v.Branch,
			&v.Agent, &v.URL, &v.StartedAt, &v.BeatAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Delete removes one of the caller's sessions. Deleting a session that is not
// there is not an error: a host that exits twice should not have to care.
func (s *Store) Delete(subject, id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE subject=? AND id=?`, subject, id)
	return err
}

// Prune drops rows that stopped beating long ago. Reads already ignore them; this
// only keeps the file from growing without bound.
func (s *Store) Prune(now time.Time, keep time.Duration) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE beat_at < ?`, now.Add(-keep).Unix())
	return err
}

func (s *Store) Close() error { return s.db.Close() }
