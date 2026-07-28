package translate

// memory.go — the translation memory. HIP-0516 makes it NORMATIVE: every string
// keys on (source_text, target, glossary_version, tier), a hit returns the stored
// value unchanged, and only new or changed source strings reach an engine. That is
// what makes a locale rebuild idempotent under a non-deterministic model.
//
// It is also the review lane's home. An entry carries a State on the ladder
// machine -> suggested -> approved -> published, and put() enforces the one
// property people need in order to trust the pipeline: a machine write may create a
// row or refresh one still at machine, and nothing else. Human work survives every
// rebuild.
//
// One memory per org: a distinct org resolves to a distinct
// {DataDir}/orgs/{slug}/translate.db (HIP-0302 physical isolation), so a query in
// one org can never reach another's rows.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
)

// State is an entry's position on the review ladder.
type State string

const (
	StateMachine   State = "machine"
	StateSuggested State = "suggested"
	StateApproved  State = "approved"
	StatePublished State = "published"
)

// Entry is one remembered translation. Source+Target+Tier+Glossary are its
// identity (the HIP-0516 tuple); Text and State are what the lane holds.
type Entry struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Tier   Tier   `json:"tier"`
	// Glossary is the glossary VERSION the entry was translated under — the digest
	// version() derives from the terms, so changing a term changes the key and the
	// stale rendering can never be served.
	Glossary  string `json:"glossary_version,omitempty"`
	Text      string `json:"text"`
	State     State  `json:"state"`
	Actor     string `json:"actor,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

// key is the ONE identity of an entry: the HIP-0516 tuple folded to a digest, so
// the tuple is the primary key without an unbounded composite index. The parts are
// NUL-separated, so no concatenation of one tuple can collide with another.
func key(source, target, glossary string, tier Tier) string {
	h := sha256.New()
	for _, part := range []string{source, target, glossary, string(tier)} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// version is a glossary's identity: the digest of its canonical (sorted) terms.
// Deriving the version FROM the terms rather than taking a caller-supplied number
// makes the memory self-invalidating — an edited glossary yields a new key on every
// affected string, and nobody has to remember to bump anything. An empty glossary
// versions as "".
func version(g map[string]string) string {
	if len(g) == 0 {
		return ""
	}
	terms := make([]string, 0, len(g))
	for t := range g {
		terms = append(terms, t)
	}
	sort.Strings(terms)
	h := sha256.New()
	for _, t := range terms {
		for _, part := range []string{t, g[t]} {
			_, _ = h.Write([]byte(part))
			_, _ = h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// memory is one org's translation memory over its own SQLite file.
type memory struct{ db *sql.DB }

// open wraps an org DB — already opened and pragma'd by cloud.OrgDB — into a
// memory, running its migration. It is the open func cloud.OrgStore calls once per
// org file.
func open(db *sql.DB) (*memory, error) {
	m := &memory{db: db}
	const ddl = `
CREATE TABLE IF NOT EXISTS entry (
  key        TEXT PRIMARY KEY,
  source     TEXT NOT NULL,
  target     TEXT NOT NULL,
  glossary   TEXT NOT NULL DEFAULT '',
  tier       TEXT NOT NULL,
  text       TEXT NOT NULL,
  state      TEXT NOT NULL DEFAULT 'machine',
  actor      TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_entry_target ON entry(target, updated_at DESC);
CREATE INDEX IF NOT EXISTS ix_entry_state ON entry(state, updated_at DESC);`
	if _, err := db.Exec(ddl); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("translate migrate: %w", err)
	}
	return m, nil
}

func (m *memory) Close() error { return m.db.Close() }

// get returns the entry stored under key. A hit is served verbatim — that is the
// idempotence contract.
func (m *memory) get(ctx context.Context, k string) (Entry, bool, error) {
	var e Entry
	err := m.db.QueryRowContext(ctx, `
SELECT source, target, tier, glossary, text, state, actor, updated_at FROM entry WHERE key=?`, k).
		Scan(&e.Source, &e.Target, &e.Tier, &e.Glossary, &e.Text, &e.State, &e.Actor, &e.UpdatedAt)
	if err == sql.ErrNoRows {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("translate get: %w", err)
	}
	return e, true, nil
}

// put writes e under its tuple key. `human` decides who may overwrite an existing
// row, and it is the whole review-lane guarantee:
//
//   - a MACHINE write updates only a row still at StateMachine, so an approved or
//     published string is immune to machine churn — a rebuild can never silently
//     revert human work, which is the failure that makes people distrust a
//     translation pipeline;
//   - a HUMAN write always wins.
func (m *memory) put(ctx context.Context, e Entry, human bool) error {
	_, err := m.db.ExecContext(ctx, `
INSERT INTO entry (key, source, target, glossary, tier, text, state, actor, updated_at)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(key) DO UPDATE SET
  text=excluded.text, state=excluded.state, actor=excluded.actor, updated_at=excluded.updated_at
WHERE ? OR entry.state=?`,
		key(e.Source, e.Target, e.Glossary, e.Tier),
		e.Source, e.Target, e.Glossary, string(e.Tier), e.Text, string(e.State), e.Actor, e.UpdatedAt,
		human, string(StateMachine))
	if err != nil {
		return fmt.Errorf("translate put: %w", err)
	}
	return nil
}

// list returns the org's entries newest first, optionally narrowed to one target
// language and/or one review state.
func (m *memory) list(ctx context.Context, target string, st State, limit int) ([]Entry, error) {
	q := `SELECT source, target, tier, glossary, text, state, actor, updated_at FROM entry`
	var where []string
	var args []any
	if target != "" {
		where = append(where, "target=?")
		args = append(args, target)
	}
	if st != "" {
		where = append(where, "state=?")
		args = append(args, string(st))
	}
	for i, w := range where {
		if i == 0 {
			q += " WHERE " + w
			continue
		}
		q += " AND " + w
	}
	q += " ORDER BY updated_at DESC, source ASC LIMIT ?"
	args = append(args, limit)

	rows, err := m.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("translate list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Source, &e.Target, &e.Tier, &e.Glossary, &e.Text, &e.State, &e.Actor, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("translate list: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
