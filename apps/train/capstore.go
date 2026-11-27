package train

// capstore.go — the org's captures, corrections and capabilities in its train.db.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/hanzoai/cloud/sdk/dialect"
)

const capSchema = `
CREATE TABLE IF NOT EXISTS settings (
  name TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS captures (
  decision TEXT NOT NULL,
  question TEXT NOT NULL,
  kind TEXT NOT NULL,
  labels TEXT NOT NULL,
  base TEXT NOT NULL,
  representation TEXT NOT NULL,
  capability TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  fired BIGINT NOT NULL DEFAULT 0,
  changed BIGINT NOT NULL DEFAULT 0,
  answered TEXT NOT NULL DEFAULT '',
  request TEXT NOT NULL DEFAULT '',
  at BIGINT NOT NULL,
  PRIMARY KEY (decision, question)
);
CREATE INDEX IF NOT EXISTS captures_at ON captures(at);
CREATE INDEX IF NOT EXISTS captures_version ON captures(version, fired);
CREATE TABLE IF NOT EXISTS corrections (
  id TEXT PRIMARY KEY,
  decision TEXT NOT NULL,
  question TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT '',
  labels TEXT NOT NULL DEFAULT '[]',
  answer TEXT NOT NULL,
  base TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  cohort TEXT NOT NULL DEFAULT '',
  by TEXT NOT NULL,
  at BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS corrections_one ON corrections(decision, question);
CREATE TABLE IF NOT EXISTS capabilities (
  name TEXT PRIMARY KEY,
  question TEXT NOT NULL,
  state TEXT NOT NULL,
  corrections TEXT NOT NULL DEFAULT '[]',
  installed BIGINT NOT NULL DEFAULT 0,
  since BIGINT NOT NULL DEFAULT 0,
  history TEXT NOT NULL DEFAULT '[]',
  by TEXT NOT NULL,
  created BIGINT NOT NULL,
  updated BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS versions (
  capability TEXT NOT NULL,
  version BIGINT NOT NULL,
  sha256 TEXT NOT NULL,
  base_sha256 TEXT NOT NULL,
  representation TEXT NOT NULL DEFAULT '',
  form TEXT NOT NULL,
  rank BIGINT NOT NULL,
  bytes BIGINT NOT NULL,
  passed BIGINT NOT NULL,
  evaluation TEXT NOT NULL,
  corrections TEXT NOT NULL,
  by TEXT NOT NULL,
  at BIGINT NOT NULL,
  PRIMARY KEY (capability, version)
);
CREATE INDEX IF NOT EXISTS versions_sha ON versions(sha256);`

// Question is what a capability answers: a question key and, under it, the
// question's type and option labels in order.
type Question struct {
	Key    string   `json:"key"`
	Type   string   `json:"type"`
	Labels []string `json:"labels"`
}

func (q Question) same(o Question) bool {
	return q.Key == o.Key && q.Type == o.Type && slices.Equal(q.Labels, o.Labels)
}

// captureRow is one question of a captured decision.
type captureRow struct {
	Decision, Question, Kind string
	Labels                   []string
	// Base is the label Kai answered; Representation what Kai read it by.
	Base, Representation string
	// Capability and Version name the org's capability that answered, and its
	// artifact; Fired says its answer was not the base's, Changed that its label
	// was not, Answered the label it gave.
	Capability, Version string
	Fired, Changed      bool
	Answered, Request   string
	At                  int64
}

const captureCols = `decision, question, kind, labels, base, representation, capability, version, fired, changed, answered, request, at`

func scanCapture(sc interface{ Scan(...any) error }) (captureRow, error) {
	var c captureRow
	var labels string
	err := sc.Scan(&c.Decision, &c.Question, &c.Kind, &labels, &c.Base, &c.Representation, &c.Capability, &c.Version, &c.Fired, &c.Changed, &c.Answered, &c.Request, &c.At)
	if err == nil {
		err = json.Unmarshal([]byte(labels), &c.Labels)
	}
	return c, err
}

// setting is a named setting of the org's, "" when unset.
func (s *store) setting(ctx context.Context, name string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE name = $1`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *store) set(ctx context.Context, name, value string) error {
	if err := dialect.Text(name, value); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (name, value) VALUES ($1,$2) ON CONFLICT(name) DO UPDATE SET value = excluded.value`, name, value)
	return err
}

// capture records a decision's captured questions, in place of what it held.
func (s *store) capture(ctx context.Context, rows []captureRow) error {
	for _, c := range rows {
		if err := dialect.Text(c.Decision, c.Question, c.Kind, c.Base, c.Representation, c.Capability, c.Version, c.Answered, c.Request); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, c := range rows {
		labels, _ := json.Marshal(c.Labels)
		if _, err := tx.ExecContext(ctx, `INSERT INTO captures (`+captureCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT(decision, question) DO UPDATE SET kind = excluded.kind, labels = excluded.labels, base = excluded.base, representation = excluded.representation,
  capability = excluded.capability, version = excluded.version, fired = excluded.fired, changed = excluded.changed, answered = excluded.answered,
  request = excluded.request, at = excluded.at`,
			c.Decision, c.Question, c.Kind, string(labels), c.Base, c.Representation, c.Capability, c.Version, bit(c.Fired), bit(c.Changed), c.Answered, c.Request, c.At); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *store) captures(ctx context.Context, q string, args ...any) ([]captureRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+captureCols+` FROM captures `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []captureRow
	for rows.Next() {
		c, err := scanCapture(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// captured is a decision's capture of one question, or errNotFound.
func (s *store) captured(ctx context.Context, decision, question string) (captureRow, error) {
	c, err := scanCapture(s.db.QueryRowContext(ctx, `SELECT `+captureCols+` FROM captures WHERE decision = $1 AND question = $2`, decision, question))
	if errors.Is(err, sql.ErrNoRows) {
		return captureRow{}, errNotFound
	}
	return c, err
}

// base is the representation the org's newest capture was read by: what Kai
// serves now; "" before any capture.
func (s *store) base(ctx context.Context) (string, error) {
	var sha string
	err := s.db.QueryRowContext(ctx, `SELECT representation FROM captures ORDER BY at DESC, decision DESC, question DESC LIMIT 1`).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return sha, err
}

// expired are the decisions captured before cutoff that no correction names, at
// most n.
func (s *store) expired(ctx context.Context, cutoff int64, n int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT c.decision FROM captures c WHERE c.at < $1
AND NOT EXISTS (SELECT 1 FROM corrections k WHERE k.decision = c.decision) LIMIT $2`, cutoff, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *store) uncapture(ctx context.Context, decision string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM captures WHERE decision = $1`, decision)
	return err
}

const correctionCols = `id, decision, question, kind, labels, answer, base, note, cohort, by, at`

// correction as a row holds it; Type, Labels and Base are empty when the
// decision was not captured.
func (s *store) scanCorrection(sc interface{ Scan(...any) error }) (correction, error) {
	var c correction
	var labels string
	err := sc.Scan(&c.ID, &c.Decision, &c.Question, &c.Type, &labels, &c.Answer, &c.Base, &c.Note, &c.Cohort, &c.By, &c.At)
	if err == nil {
		err = json.Unmarshal([]byte(labels), &c.Labels)
	}
	if err != nil {
		return c, err
	}
	row := s.fields.Row("corrections", c.ID)
	c.Note = row.Open("note", c.Note)
	return c, row.Err()
}

func (s *store) correct(ctx context.Context, c correction) error {
	if err := dialect.Text(c.ID, c.Decision, c.Question, c.Type, c.Answer, c.Base, c.Note, c.Cohort, c.By); err != nil {
		return err
	}
	labels, _ := json.Marshal(c.Labels)
	row := s.fields.Row("corrections", c.ID)
	note := row.Seal("note", c.Note)
	if err := row.Err(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO corrections (`+correctionCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		c.ID, c.Decision, c.Question, c.Type, string(labels), c.Answer, c.Base, note, c.Cohort, c.By, c.At)
	return err
}

func (s *store) corrections(ctx context.Context, q string, args ...any) ([]correction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+correctionCols+` FROM corrections `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []correction
	for rows.Next() {
		c, err := s.scanCorrection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *store) uncorrect(ctx context.Context, decision, id string) (bool, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM corrections WHERE decision = $1 AND id = $2`, decision, id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

const capabilityCols = `name, question, state, corrections, installed, since, history, by, created, updated`

func scanCapability(sc interface{ Scan(...any) error }) (capability, error) {
	var c capability
	var q, corr, hist string
	err := sc.Scan(&c.Name, &q, &c.State, &corr, &c.Installed, &c.since, &hist, &c.By, &c.Created, &c.Updated)
	if err == nil {
		err = errors.Join(json.Unmarshal([]byte(q), &c.Question), json.Unmarshal([]byte(corr), &c.Corrections), json.Unmarshal([]byte(hist), &c.history))
	}
	return c, err
}

func (s *store) capabilities(ctx context.Context, q string, args ...any) ([]capability, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+capabilityCols+` FROM capabilities `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []capability
	for rows.Next() {
		c, err := scanCapability(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *store) capability(ctx context.Context, name string) (capability, error) {
	c, err := scanCapability(s.db.QueryRowContext(ctx, `SELECT `+capabilityCols+` FROM capabilities WHERE name = $1`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return capability{}, errNotFound
	}
	return c, err
}

// keep writes a capability whole.
func (s *store) keep(ctx context.Context, c capability) error {
	q, _ := json.Marshal(c.Question)
	corr, _ := json.Marshal(c.Corrections)
	hist, _ := json.Marshal(c.history)
	_, err := s.db.ExecContext(ctx, `INSERT INTO capabilities (`+capabilityCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT(name) DO UPDATE SET question = excluded.question, state = excluded.state, corrections = excluded.corrections,
  installed = excluded.installed, since = excluded.since, history = excluded.history, updated = excluded.updated`,
		c.Name, string(q), c.State, string(corr), c.Installed, c.since, string(hist), c.By, c.Created, c.Updated)
	return err
}

// unmake deletes a capability and its versions.
func (s *store) unmake(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM versions WHERE capability = $1`, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM capabilities WHERE name = $1`, name); err != nil {
		return err
	}
	return tx.Commit()
}

const versionCols = `capability, version, sha256, base_sha256, representation, form, rank, bytes, passed, evaluation, corrections, by, at`

func scanVersion(sc interface{ Scan(...any) error }) (Version, error) {
	var v Version
	var name, eval, corr string
	err := sc.Scan(&name, &v.Version, &v.SHA256, &v.BaseSHA256, &v.Representation, &v.Form, &v.Rank, &v.Bytes, &v.Passed, &eval, &corr, &v.By, &v.At)
	if err == nil {
		v.Evaluation = json.RawMessage(eval)
		err = json.Unmarshal([]byte(corr), &v.Corrections)
	}
	return v, err
}

func (s *store) versions(ctx context.Context, name string) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+versionCols+` FROM versions WHERE capability = $1 ORDER BY version DESC`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// version is one version of a capability, or errNotFound.
func (s *store) version(ctx context.Context, name string, n int) (Version, error) {
	v, err := scanVersion(s.db.QueryRowContext(ctx, `SELECT `+versionCols+` FROM versions WHERE capability = $1 AND version = $2`, name, n))
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, errNotFound
	}
	return v, err
}

// version records a new version and the capability as it stands after it, in
// one transaction: versions are numbered from 1 and never rewritten.
func (s *store) addVersion(ctx context.Context, name string, v Version, c capability) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	corr, _ := json.Marshal(v.Corrections)
	if _, err := tx.ExecContext(ctx, `INSERT INTO versions (`+versionCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		name, v.Version, v.SHA256, v.BaseSHA256, v.Representation, v.Form, v.Rank, v.Bytes, bit(v.Passed), string(v.Evaluation), string(corr), v.By, v.At); err != nil {
		return err
	}
	q, _ := json.Marshal(c.Question)
	cc, _ := json.Marshal(c.Corrections)
	hist, _ := json.Marshal(c.history)
	if _, err := tx.ExecContext(ctx, `UPDATE capabilities SET question = $1, state = $2, corrections = $3, installed = $4, since = $5, history = $6, updated = $7 WHERE name = $8`,
		string(q), c.State, string(cc), c.Installed, c.since, string(hist), c.Updated, name); err != nil {
		return err
	}
	return tx.Commit()
}

// fires counts what an installed version did since it went in: every fire, those
// that kept the base's label, those whose label a later correction put back
// (damage), and those no correction contradicts (repairs).
func (s *store) fires(ctx context.Context, name, sha string, since int64) (Live, error) {
	var l Live
	var last sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT
  COUNT(*),
  COALESCE(SUM(CASE WHEN c.changed = 0 THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN c.changed = 1 AND EXISTS (SELECT 1 FROM corrections k WHERE k.decision = c.decision AND k.question = c.question AND k.answer = c.base) THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN c.changed = 1 AND NOT EXISTS (SELECT 1 FROM corrections k WHERE k.decision = c.decision AND k.question = c.question AND k.answer != c.answered) THEN 1 ELSE 0 END), 0),
  MAX(c.at)
FROM captures c WHERE c.capability = $1 AND c.version = $2 AND c.fired = 1 AND c.at >= $3`, name, sha, since).Scan(&l.Fires, &l.Neutral, &l.Damage, &l.Repairs, &last)
	if err != nil {
		return Live{}, err
	}
	l.LastFired = last.Int64
	if l.Fires > 0 {
		l.RepairRate = float64(l.Repairs) / float64(l.Fires)
		l.DamageRate = float64(l.Damage) / float64(l.Fires)
	}
	return l, nil
}

// installs are the org's capabilities as the decision service installs them:
// each installed version's artifact, base, form and route, by name.
func (s *store) installs(ctx context.Context) ([]Install, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.name, c.question, c.since, v.sha256, v.base_sha256, v.representation, v.form
FROM capabilities c JOIN versions v ON v.capability = c.name AND v.version = c.installed
WHERE c.installed > 0 ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Install
	for rows.Next() {
		var in Install
		var qs string
		if err := rows.Scan(&in.Name, &qs, &in.Published, &in.SHA256, &in.BaseSHA256, &in.Representation, &in.Form); err != nil {
			return nil, err
		}
		var q Question
		if err := json.Unmarshal([]byte(qs), &q); err != nil {
			return nil, err
		}
		in.Route = *q.route()
		out = append(out, in)
	}
	return out, rows.Err()
}

// installedRecords are the published records of the org's installed
// capabilities, but for except, in the shape a job's published record has: what
// a publish weighs a new route against.
func (s *store) installedRecords(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, except string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, question FROM capabilities WHERE installed > 0 AND name != $1`, except)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, qs string
		if err := rows.Scan(&name, &qs); err != nil {
			return nil, err
		}
		var question Question
		if err := json.Unmarshal([]byte(qs), &question); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(published{Name: name, Kind: "capability", Route: question.route()})
		out = append(out, string(b))
	}
	return out, rows.Err()
}
