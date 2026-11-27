package translate

// memory.go — the translation memory. HIP-0516 makes it NORMATIVE: every string
// keys on its tuple, a hit returns the stored value unchanged, and only new or
// changed source strings reach an engine. That is what makes a locale rebuild
// idempotent under a non-deterministic model.
//
// The tuple is (source_text, source language, target, glossary_version, tier,
// format, model). The last three are what make one string's renderings distinct:
// the same text through another model, or as html rather than text, never answers
// with the other's stored value. The source language is the one the caller
// DECLARED; a string translated with it detected is stored under none, and serves
// a later call that declares one (recall).
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
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// State is an entry's position on the review ladder.
type State string

const (
	StateMachine   State = "machine"
	StateSuggested State = "suggested"
	StateApproved  State = "approved"
	StatePublished State = "published"
)

// MemoryEntry is one remembered translation. Source, Lang, Target, Tier, Format,
// Model and Glossary are its identity; Text and State are what the lane holds.
type MemoryEntry struct {
	// Source is the ORIGINAL string this entry translates. Part of the identity.
	Source string `json:"source"`
	// Target is the target language tag (BCP-47, e.g. "es" or "pt-BR"). Part of the
	// identity.
	Target string `json:"target"`
	// Tier is the engine tier the entry belongs to, quality or bulk. Part of the
	// identity: the two tiers keep separate renderings of the same source.
	Tier Tier `json:"tier"`
	// Glossary is the glossary VERSION the entry was translated under — the digest
	// version() derives from the terms, so changing a term changes the key and the
	// stale rendering can never be served.
	Glossary string `json:"glossary_version,omitempty"`
	// Text is the stored translation. A memory hit returns it verbatim, which is the
	// idempotence contract.
	Text string `json:"text"`
	// State is the entry's position on the review ladder: machine, suggested,
	// approved or published.
	State State `json:"state"`
	// Actor is the validated user id that last wrote this entry by hand. Empty on an
	// entry an engine produced, and on one written before attribution existed.
	Actor string `json:"actor,omitempty"`
	// UpdatedAt is the unix second the entry last changed.
	UpdatedAt int64 `json:"updated_at"`
	// The parts below came after the ones above, and follow them: the call plane
	// lays a struct out in field order, so a field is only ever appended.

	// Lang is the source language the translate call declared, empty when the
	// engine detected it. Part of the identity; an entry with none also serves a
	// call that declares one.
	Lang string `json:"lang"`
	// Format is the markup the source carried: text, html or markdown. Part of the
	// identity.
	Format Format `json:"format"`
	// Model is the quality-tier model the translate call named, empty for the
	// default. Part of the identity: one model's rendering never answers for
	// another's.
	Model string `json:"model"`
	// RequestID is the X-Request-Id of the translate call whose engine produced
	// the entry. Empty on an entry a human wrote, and on one older than the field.
	RequestID string `json:"request_id,omitempty"`
}

// key is the ONE identity of an entry: its tuple folded to a digest, so the tuple
// is the primary key without an unbounded composite index. The parts are
// NUL-separated, so no concatenation of one tuple can collide with another.
func (e MemoryEntry) key() string {
	h := sha256.New()
	for _, part := range []string{e.Source, e.Lang, e.Target, e.Glossary, string(e.Tier), string(e.Format), e.Model} {
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

// memory is one org's translation memory over its own database: a SQLite file, or on
// hanzo-sql a schema of the org's namespace. Its SQL is the dialect both engines share
// (sdk/dialect).
type memory struct{ db *sql.DB }

// columns is every column a row is read back through, in MemoryEntry order.
// format is read through COALESCE because a row an older binary wrote has none
// until the next open moves it (rekey).
const columns = `source, target, tier, glossary, text, state, actor, updated_at, lang, COALESCE(format, 'text'), model, request_id`

func scan(row interface{ Scan(...any) error }, e *MemoryEntry) error {
	return row.Scan(&e.Source, &e.Target, &e.Tier, &e.Glossary, &e.Text, &e.State, &e.Actor, &e.UpdatedAt,
		&e.Lang, &e.Format, &e.Model, &e.RequestID)
}

// grown are the columns the key grew, as ADD COLUMN clauses. A file made before
// them gains them on open, and its rows arrive with format NULL — which is what
// rekey looks for, and what a row an older binary writes during a rolling upgrade
// still has, since it names none of these.
var grown = []struct{ name, ddl string }{
	{"lang", "lang TEXT NOT NULL DEFAULT ''"},
	{"format", "format TEXT"},
	{"model", "model TEXT NOT NULL DEFAULT ''"},
	{"request_id", "request_id TEXT NOT NULL DEFAULT ''"},
}

// open wraps an org DB — already opened by cloud.OrgDB — into a
// memory, running its migration. It is the open func cloud.OrgStore calls once per
// org file.
func open(db *sql.DB) (*memory, error) {
	m := &memory{db: db}
	if err := m.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("translate migrate: %w", err)
	}
	return m, nil
}

// migrate brings any file to the current shape: the table, the columns the key
// grew, the indexes, and every row moved to the key it has now.
func (m *memory) migrate(ctx context.Context) error {
	const table = `
CREATE TABLE IF NOT EXISTS entry (
  key        TEXT PRIMARY KEY,
  source     TEXT NOT NULL,
  target     TEXT NOT NULL,
  glossary   TEXT NOT NULL DEFAULT '',
  tier       TEXT NOT NULL,
  text       TEXT NOT NULL,
  state      TEXT NOT NULL DEFAULT 'machine',
  actor      TEXT NOT NULL DEFAULT '',
  updated_at BIGINT NOT NULL
)`
	const indexes = `
CREATE INDEX IF NOT EXISTS ix_entry_target ON entry(target, updated_at DESC);
CREATE INDEX IF NOT EXISTS ix_entry_state ON entry(state, updated_at DESC);
CREATE INDEX IF NOT EXISTS ix_entry_unmoved ON entry(key) WHERE format IS NULL;`
	// The shape is one transaction through tx alone (Dialect.Migrate): two replicas
	// opening the file at once do not run its DDL against each other. A column is
	// added only when the table lacks it.
	d := dialect.Of(m.db)
	if err := d.Migrate(ctx, m.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, table); err != nil {
			return err
		}
		for _, c := range grown {
			has, err := d.HasColumn(ctx, tx, "entry", c.name)
			if err != nil {
				return err
			}
			if has {
				continue
			}
			if _, err := tx.ExecContext(ctx, `ALTER TABLE entry ADD COLUMN `+c.ddl); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, indexes)
		return err
	}); err != nil {
		return err
	}
	return m.rekey(ctx)
}

// rekey moves every row written under the key before it carried the source
// language, format and model — format IS NULL — to the keys it has now: no source
// declared, the default model, and EACH format. Which format the call that made a
// row named was never recorded, so the row answers all three: an approved html
// string stays approved for the html call that comes next, rather than being
// re-translated over. A call that declares a source reads it too (recall).
//
// It streams: rekeyBatch rows at a time, each batch one transaction, so a large
// memory is never held in RAM at once. It runs on every open, so a row an older
// binary writes during a rolling upgrade moves the next time the file opens. A row
// meeting one already under a new key is settled by put's ladder — human work
// wins, machine work refreshes only machine work — and the old row goes either way.
func (m *memory) rekey(ctx context.Context) error {
	for {
		n, err := m.rekeyBatch(ctx)
		if err != nil || n < rekeyBatch {
			return err
		}
	}
}

// rekeyBatch is how many old rows one transaction moves.
const rekeyBatch = 500

// formats are the formats an old row is filed under.
var formats = []Format{FormatText, FormatHTML, FormatMarkdown}

func (m *memory) rekeyBatch(ctx context.Context) (int, error) {
	// A batch reads rows and then writes what it read, so it holds the store's write
	// lock: two replicas opening the file at once move disjoint batches in turn rather
	// than the same one together.
	tx, err := dialect.Of(m.db).BeginWrite(ctx, m.db)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
SELECT key, source, target, tier, glossary, text, state, actor, updated_at FROM entry WHERE format IS NULL LIMIT $1`, rekeyBatch)
	if err != nil {
		return 0, err
	}
	type moved struct {
		old string
		e   MemoryEntry
	}
	var batch []moved
	for rows.Next() {
		var r moved
		if err := rows.Scan(&r.old, &r.e.Source, &r.e.Target, &r.e.Tier, &r.e.Glossary,
			&r.e.Text, &r.e.State, &r.e.Actor, &r.e.UpdatedAt); err != nil {
			_ = rows.Close()
			return 0, err
		}
		batch = append(batch, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	for _, r := range batch {
		for _, f := range formats {
			e := r.e
			e.Format = f
			if err := move(ctx, tx, e); err != nil {
				return 0, err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM entry WHERE key=$1`, r.old); err != nil {
			return 0, err
		}
	}
	return len(batch), tx.Commit()
}

func (m *memory) Close() error { return m.db.Close() }

// get returns the entry stored under key. A hit is served verbatim — that is the
// idempotence contract.
func (m *memory) get(ctx context.Context, k string) (MemoryEntry, bool, error) {
	var e MemoryEntry
	err := scan(m.db.QueryRowContext(ctx, `SELECT `+columns+` FROM entry WHERE key=$1`, k), &e)
	if err == sql.ErrNoRows {
		return MemoryEntry{}, false, nil
	}
	if err != nil {
		return MemoryEntry{}, false, fmt.Errorf("translate get: %w", err)
	}
	return e, true, nil
}

// recall is the memory's answer for e's tuple. When e declares a source language
// and nothing is stored under it, the entry made for the same string with the
// source undeclared answers: a string first translated with its language detected
// — every entry made before the key carried the language among them — is not lost
// to a caller that starts declaring it, and the reverse never happens.
func (m *memory) recall(ctx context.Context, e MemoryEntry) (MemoryEntry, bool, error) {
	got, found, err := m.get(ctx, e.key())
	if err != nil || found || e.Lang == "" {
		return got, found, err
	}
	e.Lang = ""
	return m.get(ctx, e.key())
}

// put writes e under its tuple key. `human` decides who may overwrite an existing
// row, and it is the whole review-lane guarantee:
//
//   - a MACHINE write updates only a row still at StateMachine, so an approved or
//     published string is immune to machine churn — a rebuild can never silently
//     revert human work, which is the failure that makes people distrust a
//     translation pipeline;
//   - a HUMAN write always wins.
func (m *memory) put(ctx context.Context, e MemoryEntry, human bool) error {
	_, err := m.db.ExecContext(ctx, `
INSERT INTO entry (key, source, lang, target, glossary, tier, format, model, text, state, actor, request_id, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT(key) DO UPDATE SET
  text=excluded.text, state=excluded.state, actor=excluded.actor, request_id=excluded.request_id,
  updated_at=excluded.updated_at
WHERE $14 OR entry.state=$15`,
		e.key(),
		e.Source, e.Lang, e.Target, e.Glossary, string(e.Tier), string(e.Format), e.Model,
		e.Text, string(e.State), e.Actor, e.RequestID, e.UpdatedAt,
		human, string(StateMachine))
	if err != nil {
		return fmt.Errorf("translate put: %w", err)
	}
	return nil
}

// move files an old row under a new key without undoing anything newer there. A
// row already under that key is replaced only when it is machine work and the
// old row is human work or newer; human work is replaced only by human work at
// least as far up the ladder and at least as new. Rows written during a rolling
// upgrade meet each other here, in either order, and the reviewed and the
// recent one stands.
func move(ctx context.Context, tx *sql.Tx, e MemoryEntry) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO entry (key, source, lang, target, glossary, tier, format, model, text, state, actor, request_id, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT(key) DO UPDATE SET
  text=excluded.text, state=excluded.state, actor=excluded.actor, request_id=excluded.request_id,
  updated_at=excluded.updated_at
WHERE CASE WHEN entry.state='machine'
  THEN excluded.state<>'machine' OR excluded.updated_at>entry.updated_at
  ELSE `+rank("excluded.state")+` >= `+rank("entry.state")+` AND excluded.updated_at>=entry.updated_at END`,
		e.key(),
		e.Source, e.Lang, e.Target, e.Glossary, string(e.Tier), string(e.Format), e.Model,
		e.Text, string(e.State), e.Actor, e.RequestID, e.UpdatedAt)
	if err != nil {
		return fmt.Errorf("translate move: %w", err)
	}
	return nil
}

// rank is a state's height on the review ladder, as SQL over the column col.
func rank(col string) string {
	return "CASE " + col + " WHEN 'published' THEN 3 WHEN 'approved' THEN 2 WHEN 'suggested' THEN 1 ELSE 0 END"
}

// list returns the org's entries newest first, optionally narrowed to one target
// language and/or one review state.
func (m *memory) list(ctx context.Context, target string, st State, limit int) ([]MemoryEntry, error) {
	var q strings.Builder
	q.WriteString(`SELECT ` + columns + ` FROM entry`)
	var where []string
	var args []any
	if target != "" {
		args = append(args, target)
		where = append(where, "target=$"+strconv.Itoa(len(args)))
	}
	if st != "" {
		args = append(args, string(st))
		where = append(where, "state=$"+strconv.Itoa(len(args)))
	}
	for i, w := range where {
		if i == 0 {
			q.WriteString(" WHERE " + w)
			continue
		}
		q.WriteString(" AND " + w)
	}
	args = append(args, limit)
	q.WriteString(" ORDER BY updated_at DESC, source ASC LIMIT $" + strconv.Itoa(len(args)))

	rows, err := m.db.QueryContext(ctx, q.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("translate list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []MemoryEntry{}
	for rows.Next() {
		var e MemoryEntry
		if err := scan(rows, &e); err != nil {
			return nil, fmt.Errorf("translate list: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
