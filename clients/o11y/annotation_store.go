package o11y

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// Annotation queues are the human-review workflow of the observability plane: a
// named queue (optionally bound to eval score-configs) holds items — traces,
// observations or sessions queued for a reviewer to score. The o11y span plane
// (llmobs) has flat annotations but NO queue entity, so this is the native queue
// backing the console AnnotationQueuesModule consumes at /v1/o11y/annotation-queues.
//
// Storage is Hanzo Base/SQLite (the eval-metastore discipline), NOT the datastore
// span plane: queues are durable relational config, not append-only telemetry.
// Tenant isolation is the `org` column on EVERY table + a mandatory predicate on
// EVERY query; `project` narrows within the org (principal.ProjectScope). The org
// is c.Org() as SanitizeIdentity minted it — never normalized (casing/trimming
// would collapse distinct owners into one bucket).

var (
	errQueueConflict = errors.New("o11y: annotation queue already exists")
	errQueueNotFound = errors.New("o11y: annotation queue not found")
	errItemNotFound  = errors.New("o11y: annotation queue item not found")
)

// annQueue is a named review queue, org+project scoped. (org,project,name) is
// unique. ScoreConfigIDs reference eval score-configs (/v1/evals/score-configs);
// they are opaque ids here, validated for shape only.
type annQueue struct {
	ID             string
	Org            string
	Project        string
	Name           string
	Description    string
	ScoreConfigIDs []string
	CreatedAt      int64
	UpdatedAt      int64
}

// annItem is one object queued for review. ObjectType is TRACE|OBSERVATION|SESSION
// and ObjectID is that object's id on the span plane. Status is PENDING|COMPLETED;
// CompletedAt is 0 until completed.
type annItem struct {
	ID          string
	Org         string
	Project     string
	QueueID     string
	ObjectType  string
	ObjectID    string
	Status      string
	Assignee    string
	CreatedAt   int64
	UpdatedAt   int64
	CompletedAt int64
}

// annStore is the annotation-queue metastore over one SQLite file
// ({DataDir}/o11y_annotations.db). MaxOpenConns(1) serializes writes against the
// file lock (the same discipline the eval metastore uses).
type annStore struct {
	db *sql.DB
}

func openAnnStore(path string) (*annStore, error) {
	db, err := cek.Open(path)
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
	s := &annStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *annStore) migrate() error {
	// PK = (org, id): the id is NEVER a global cross-org key (a global PK would leak
	// a cross-tenant existence oracle and stop two orgs sharing an id). Tenant
	// isolation is the org column on every row; project narrows within org.
	const ddl = `
CREATE TABLE IF NOT EXISTS annotation_queues (
  org              TEXT NOT NULL,
  project          TEXT NOT NULL DEFAULT '',
  id               TEXT NOT NULL,
  name             TEXT NOT NULL,
  description      TEXT NOT NULL DEFAULT '',
  score_config_ids TEXT NOT NULL DEFAULT '[]',
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  PRIMARY KEY (org, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_annq_org_proj_name ON annotation_queues(org, project, name);
CREATE INDEX IF NOT EXISTS ix_annq_org_proj_updated ON annotation_queues(org, project, updated_at);

CREATE TABLE IF NOT EXISTS annotation_queue_items (
  org          TEXT NOT NULL,
  project      TEXT NOT NULL DEFAULT '',
  id           TEXT NOT NULL,
  queue_id     TEXT NOT NULL,
  object_type  TEXT NOT NULL DEFAULT 'TRACE',
  object_id    TEXT NOT NULL,
  status       TEXT NOT NULL DEFAULT 'PENDING',
  assignee     TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  completed_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (org, id)
);
CREATE INDEX IF NOT EXISTS ix_annqi_org_queue_status ON annotation_queue_items(org, queue_id, status);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (s *annStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// ── encode helpers ────────────────────────────────────────────────────────────

func encodeStrList(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeStrList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var xs []string
	if err := json.Unmarshal([]byte(s), &xs); err != nil {
		return nil
	}
	return xs
}

func annErrIsUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ── queues ──────────────────────────────────────────────────────────────────

const annQueueCols = `id,org,project,name,description,score_config_ids,created_at,updated_at`

func scanAnnQueue(sc interface{ Scan(...any) error }) (annQueue, error) {
	var q annQueue
	var ids string
	err := sc.Scan(&q.ID, &q.Org, &q.Project, &q.Name, &q.Description, &ids, &q.CreatedAt, &q.UpdatedAt)
	q.ScoreConfigIDs = decodeStrList(ids)
	return q, err
}

// CreateQueue inserts a new queue. A duplicate (org,project,name) is errQueueConflict
// (a 409), never a silent overwrite of another reviewer's queue config.
func (s *annStore) CreateQueue(ctx context.Context, q annQueue) (annQueue, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO annotation_queues (`+annQueueCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		q.ID, q.Org, q.Project, q.Name, q.Description, encodeStrList(q.ScoreConfigIDs), q.CreatedAt, q.UpdatedAt)
	if annErrIsUnique(err) {
		return annQueue{}, errQueueConflict
	}
	if err != nil {
		return annQueue{}, fmt.Errorf("insert queue: %w", err)
	}
	return q, nil
}

func (s *annStore) GetQueue(ctx context.Context, org, id string) (annQueue, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+annQueueCols+` FROM annotation_queues WHERE org=? AND id=?`, org, id)
	q, err := scanAnnQueue(row)
	if errors.Is(err, sql.ErrNoRows) {
		return annQueue{}, errQueueNotFound
	}
	if err != nil {
		return annQueue{}, fmt.Errorf("get queue: %w", err)
	}
	return q, nil
}

// ListQueues returns the org's queues in the project scope (project=="" is the
// whole-org view), newest first, bounded [offset,offset+limit), plus the total
// count for pagination meta.
func (s *annStore) ListQueues(ctx context.Context, org, project string, limit, offset int) ([]annQueue, int, error) {
	where := `WHERE org=?`
	args := []any{org}
	if project != "" {
		where += ` AND project=?`
		args = append(args, project)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM annotation_queues `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count queues: %w", err)
	}
	q := `SELECT ` + annQueueCols + ` FROM annotation_queues ` + where + ` ORDER BY updated_at DESC, name ASC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, q, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list queues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []annQueue
	for rows.Next() {
		x, err := scanAnnQueue(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan queue: %w", err)
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

// UpdateQueue patches name/description/score-config-ids of an existing queue. A
// duplicate name in the same (org,project) is errQueueConflict; a missing queue is
// errQueueNotFound.
func (s *annStore) UpdateQueue(ctx context.Context, q annQueue) (annQueue, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return annQueue{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var createdAt int64
	var project string
	row := tx.QueryRowContext(ctx, `SELECT created_at, project FROM annotation_queues WHERE org=? AND id=?`, q.Org, q.ID)
	switch err := row.Scan(&createdAt, &project); {
	case errors.Is(err, sql.ErrNoRows):
		return annQueue{}, errQueueNotFound
	case err != nil:
		return annQueue{}, fmt.Errorf("lookup queue: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE annotation_queues SET name=?,description=?,score_config_ids=?,updated_at=? WHERE org=? AND id=?`,
		q.Name, q.Description, encodeStrList(q.ScoreConfigIDs), q.UpdatedAt, q.Org, q.ID)
	if annErrIsUnique(err) {
		return annQueue{}, errQueueConflict
	}
	if err != nil {
		return annQueue{}, fmt.Errorf("update queue: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return annQueue{}, fmt.Errorf("commit: %w", err)
	}
	q.CreatedAt = createdAt
	q.Project = project
	return q, nil
}

// DeleteQueue removes a queue and its items in one transaction. Reports whether the
// queue existed.
func (s *annStore) DeleteQueue(ctx context.Context, org, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM annotation_queues WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete queue: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM annotation_queue_items WHERE org=? AND queue_id=?`, org, id); err != nil {
		return false, fmt.Errorf("delete items: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return n > 0, nil
}

// ── items ─────────────────────────────────────────────────────────────────────

const annItemCols = `id,org,project,queue_id,object_type,object_id,status,assignee,created_at,updated_at,completed_at`

func scanAnnItem(sc interface{ Scan(...any) error }) (annItem, error) {
	var it annItem
	err := sc.Scan(&it.ID, &it.Org, &it.Project, &it.QueueID, &it.ObjectType, &it.ObjectID,
		&it.Status, &it.Assignee, &it.CreatedAt, &it.UpdatedAt, &it.CompletedAt)
	return it, err
}

// AddItems inserts items into a queue in one transaction (all or nothing).
func (s *annStore) AddItems(ctx context.Context, items []annItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, it := range items {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO annotation_queue_items (`+annItemCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			it.ID, it.Org, it.Project, it.QueueID, it.ObjectType, it.ObjectID,
			it.Status, it.Assignee, it.CreatedAt, it.UpdatedAt, it.CompletedAt); err != nil {
			return fmt.Errorf("insert item: %w", err)
		}
	}
	return tx.Commit()
}

// ListItems returns a queue's items, oldest-first (review order), optionally
// filtered by status, bounded [offset,offset+limit), plus the total count.
func (s *annStore) ListItems(ctx context.Context, org, queueID, status string, limit, offset int) ([]annItem, int, error) {
	where := `WHERE org=? AND queue_id=?`
	args := []any{org, queueID}
	if status != "" {
		where += ` AND status=?`
		args = append(args, status)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM annotation_queue_items `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count items: %w", err)
	}
	q := `SELECT ` + annItemCols + ` FROM annotation_queue_items ` + where + ` ORDER BY created_at ASC, id ASC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, q, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []annItem
	for rows.Next() {
		x, err := scanAnnItem(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan item: %w", err)
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}

// UpdateItem sets an item's status (+ assignee, + completed_at when COMPLETED).
// A missing item (in this org) is errItemNotFound.
func (s *annStore) UpdateItem(ctx context.Context, org, id, status, assignee string, completedAt, updatedAt int64) (annItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return annItem{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `SELECT `+annItemCols+` FROM annotation_queue_items WHERE org=? AND id=?`, org, id)
	it, err := scanAnnItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return annItem{}, errItemNotFound
	}
	if err != nil {
		return annItem{}, fmt.Errorf("get item: %w", err)
	}
	it.Status, it.Assignee, it.CompletedAt, it.UpdatedAt = status, assignee, completedAt, updatedAt
	if _, err := tx.ExecContext(ctx,
		`UPDATE annotation_queue_items SET status=?,assignee=?,completed_at=?,updated_at=? WHERE org=? AND id=?`,
		it.Status, it.Assignee, it.CompletedAt, it.UpdatedAt, org, id); err != nil {
		return annItem{}, fmt.Errorf("update item: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return annItem{}, fmt.Errorf("commit: %w", err)
	}
	return it, nil
}

// QueueCounts returns (pending, completed) item counts for a queue — the detail
// view's at-a-glance progress.
func (s *annStore) QueueCounts(ctx context.Context, org, queueID string) (pending, completed int, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM annotation_queue_items WHERE org=? AND queue_id=? GROUP BY status`, org, queueID)
	if err != nil {
		return 0, 0, fmt.Errorf("count by status: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return 0, 0, fmt.Errorf("scan count: %w", err)
		}
		switch st {
		case statusCompleted:
			completed = n
		default:
			pending += n
		}
	}
	return pending, completed, rows.Err()
}

// totalPages is ceil(total/limit), at least 1 (an empty list is still one page).
func totalPages(total, limit int) int {
	if limit <= 0 {
		return 1
	}
	p := int(math.Ceil(float64(total) / float64(limit)))
	if p < 1 {
		return 1
	}
	return p
}
