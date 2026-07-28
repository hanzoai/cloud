package experiments

// store.go — the experiment REGISTRY: one per-org (per HIP-0302, physical-file
// isolated) SQLite holding each experiment's definition + decision. This is the ONLY
// state the primitive owns; assignment lives in flags, outcomes in analytics,
// evidence in research. The org is the physical tenant boundary — a distinct org
// resolves to a distinct {DataDir}/orgs/{slug}/experiments.db, so one org's query can
// never reach another's rows. project is a first-class COLUMN, so an org's
// experiments across its projects aggregate in one store.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type store struct {
	db *sql.DB
}

// openStore wraps an org DB — already opened + pragma'd by cloud.OrgDB — into the
// experiments store, running its migration. It is the open func cloud.OrgStore calls
// once per org file.
func openStore(db *sql.DB) (*store, error) {
	s := &store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) Close() error { return s.db.Close() }

func (s *store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS experiment (
  project        TEXT NOT NULL DEFAULT '',
  id             TEXT NOT NULL,
  name           TEXT NOT NULL DEFAULT '',
  subject_kind   TEXT NOT NULL DEFAULT 'user',
  flag_key       TEXT NOT NULL,
  exposure_event TEXT NOT NULL DEFAULT '',
  metric_event   TEXT NOT NULL,
  variants       TEXT NOT NULL DEFAULT '[]',
  status         TEXT NOT NULL DEFAULT 'running',
  winner         TEXT NOT NULL DEFAULT '',
  created_by     TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL DEFAULT '',
  decided_by     TEXT NOT NULL DEFAULT '',
  decided_at     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (project, id)
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("experiments migrate: %w", err)
	}
	return nil
}

// create inserts a new experiment. It fails if (project, id) already exists — an
// experiment id is claimed once, never silently overwritten (a re-create would
// stomp the assignment flag mid-run).
func (s *store) create(ctx context.Context, e Experiment) error {
	variants, err := json.Marshal(e.Variants)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO experiment (project, id, name, subject_kind, flag_key, exposure_event, metric_event, variants, status, created_by, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		e.Project, e.ID, e.Name, string(e.SubjectKind), e.FlagKey, e.ExposureEvent, e.MetricEvent,
		string(variants), string(e.Status), e.CreatedBy, e.CreatedAt)
	if err != nil {
		return fmt.Errorf("experiments create: %w", err)
	}
	return nil
}

func (s *store) get(ctx context.Context, project, id string) (Experiment, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT project, id, name, subject_kind, flag_key, exposure_event, metric_event, variants, status, winner, created_by, created_at, decided_by, decided_at
FROM experiment WHERE project=? AND id=?`, project, id)
	e, err := scanExperiment(row)
	if err == sql.ErrNoRows {
		return Experiment{}, false, nil
	}
	if err != nil {
		return Experiment{}, false, err
	}
	return e, true, nil
}

func (s *store) list(ctx context.Context, project string) ([]Experiment, error) {
	q := `
SELECT project, id, name, subject_kind, flag_key, exposure_event, metric_event, variants, status, winner, created_by, created_at, decided_by, decided_at
FROM experiment`
	var args []any
	if project != "" {
		q += ` WHERE project=?`
		args = append(args, project)
	}
	q += ` ORDER BY project, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("experiments list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Experiment{}
	for rows.Next() {
		e, err := scanExperiment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// decide records the promotion of a winner: status -> decided, the winning variant,
// and who decided when. Idempotent-safe (a second decide re-stamps the same row).
func (s *store) decide(ctx context.Context, project, id, winner, by, at string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE experiment SET status=?, winner=?, decided_by=?, decided_at=? WHERE project=? AND id=?`,
		string(StatusDecided), winner, by, at, project, id)
	if err != nil {
		return fmt.Errorf("experiments decide: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// scanner unifies *sql.Row and *sql.Rows for scanExperiment.
type scanner interface {
	Scan(dest ...any) error
}

func scanExperiment(sc scanner) (Experiment, error) {
	var e Experiment
	var kind, status, variants string
	if err := sc.Scan(&e.Project, &e.ID, &e.Name, &kind, &e.FlagKey, &e.ExposureEvent, &e.MetricEvent,
		&variants, &status, &e.Winner, &e.CreatedBy, &e.CreatedAt, &e.DecidedBy, &e.DecidedAt); err != nil {
		return Experiment{}, err
	}
	e.SubjectKind = SubjectKind(kind)
	e.Status = Status(status)
	if err := json.Unmarshal([]byte(variants), &e.Variants); err != nil {
		return Experiment{}, fmt.Errorf("experiments: variants decode: %w", err)
	}
	return e, nil
}
