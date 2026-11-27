package o11y

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"

	// locus.Open is the ONE opener: with CLOUD_SQL_DSN set it is the store's schema
	// on hanzo-sql, and otherwise the SQLite file is born encrypted under the key cek
	// derives from the process master and this namespace.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	_ "github.com/hanzoai/sqlite"
)

// Annotation queues are the human-review workflow of the observability plane: a
// named queue (optionally bound to eval score-configs) holds items — traces,
// observations or sessions queued for a reviewer to score. The o11y span plane
// (llmobs) has flat annotations but NO queue entity, so this is the native queue
// backing the console AnnotationQueuesModule consumes at /v1/o11y/reviews.
//
// Storage is relational — a SQLite file, or a schema on hanzo-sql — (the
// eval-metastore discipline), NOT the datastore span plane: queues are durable
// relational config, not append-only telemetry. Its SQL is the dialect both engines
// share (sdk/dialect) and its tests run on both (internal/sqltest).
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
// unique. ScoreConfigIDs reference eval score-configs (/v1/eval/score-configs);
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

// annStore is the annotation-queue metastore over one database — the system
// namespace's "o11y_annotations", a SQLite file or a schema on hanzo-sql. A file
// serializes writes against its one connection (the same discipline the eval
// metastore uses); a read then write asks the engine for that with BeginWrite.
type annStore struct {
	db *sql.DB
	d  dialect.Dialect
}

func openAnnStore(dir string) (*annStore, error) {
	st, err := locus.Use(context.Background(), nil, namespace.System(), "o11y_annotations", dir, func(db *sql.DB) (*annStore, error) {
		s := &annStore{db: db, d: dialect.Of(db)}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.d.Migrate(ctx, db, func(tx *sql.Tx) error { return s.migrate(ctx, tx) }); err != nil {
			return nil, fmt.Errorf("o11y_annotations: migrate: %w", err)
		}
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open o11y_annotations store: %w", err)
	}
	return st, nil
}

func (s *annStore) migrate(ctx context.Context, tx *sql.Tx) error {
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
  created_at       BIGINT NOT NULL,
  updated_at       BIGINT NOT NULL,
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
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL,
  completed_at BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (org, id)
);
CREATE INDEX IF NOT EXISTS ix_annqi_org_queue_status ON annotation_queue_items(org, queue_id, status);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
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

// binder collects a statement's arguments as it is written and hands back the
// placeholder of each, so the numbers rise in the order the text is composed.
type binder struct{ args []any }

func (b *binder) bind(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
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
	if err := dialect.Text(q.ID, q.Org, q.Project, q.Name, q.Description); err != nil {
		return annQueue{}, err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO annotation_queues (`+annQueueCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		q.ID, q.Org, q.Project, q.Name, q.Description, encodeStrList(q.ScoreConfigIDs), q.CreatedAt, q.UpdatedAt)
	if dialect.Unique(err) {
		return annQueue{}, errQueueConflict
	}
	if err != nil {
		return annQueue{}, fmt.Errorf("insert queue: %w", err)
	}
	return q, nil
}

func (s *annStore) GetQueue(ctx context.Context, org, id string) (annQueue, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+annQueueCols+` FROM annotation_queues WHERE org=$1 AND id=$2`, org, id)
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
	var b binder
	where := `WHERE org=` + b.bind(org)
	if project != "" {
		where += ` AND project=` + b.bind(project)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM annotation_queues `+where, b.args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count queues: %w", err)
	}
	q := `SELECT ` + annQueueCols + ` FROM annotation_queues ` + where + ` ORDER BY updated_at DESC, name ASC LIMIT ` + b.bind(limit) + ` OFFSET ` + b.bind(offset)
	rows, err := s.db.QueryContext(ctx, q, b.args...)
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
	if err := dialect.Text(q.Name, q.Description); err != nil {
		return annQueue{}, err
	}
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return annQueue{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var createdAt int64
	var project string
	row := tx.QueryRowContext(ctx, `SELECT created_at, project FROM annotation_queues WHERE org=$1 AND id=$2`, q.Org, q.ID)
	switch err := row.Scan(&createdAt, &project); {
	case errors.Is(err, sql.ErrNoRows):
		return annQueue{}, errQueueNotFound
	case err != nil:
		return annQueue{}, fmt.Errorf("lookup queue: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE annotation_queues SET name=$1,description=$2,score_config_ids=$3,updated_at=$4 WHERE org=$5 AND id=$6`,
		q.Name, q.Description, encodeStrList(q.ScoreConfigIDs), q.UpdatedAt, q.Org, q.ID)
	if dialect.Unique(err) {
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
	res, err := tx.ExecContext(ctx, `DELETE FROM annotation_queues WHERE org=$1 AND id=$2`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete queue: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM annotation_queue_items WHERE org=$1 AND queue_id=$2`, org, id); err != nil {
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
	for _, it := range items {
		if err := dialect.Text(it.ID, it.Org, it.Project, it.QueueID, it.ObjectType, it.ObjectID, it.Status, it.Assignee); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, it := range items {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO annotation_queue_items (`+annItemCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
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
	var b binder
	where := `WHERE org=` + b.bind(org) + ` AND queue_id=` + b.bind(queueID)
	if status != "" {
		where += ` AND status=` + b.bind(status)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM annotation_queue_items `+where, b.args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count items: %w", err)
	}
	q := `SELECT ` + annItemCols + ` FROM annotation_queue_items ` + where + ` ORDER BY created_at ASC, id ASC LIMIT ` + b.bind(limit) + ` OFFSET ` + b.bind(offset)
	rows, err := s.db.QueryContext(ctx, q, b.args...)
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
	if err := dialect.Text(status, assignee); err != nil {
		return annItem{}, err
	}
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return annItem{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `SELECT `+annItemCols+` FROM annotation_queue_items WHERE org=$1 AND id=$2`, org, id)
	it, err := scanAnnItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return annItem{}, errItemNotFound
	}
	if err != nil {
		return annItem{}, fmt.Errorf("get item: %w", err)
	}
	it.Status, it.Assignee, it.CompletedAt, it.UpdatedAt = status, assignee, completedAt, updatedAt
	if _, err := tx.ExecContext(ctx,
		`UPDATE annotation_queue_items SET status=$1,assignee=$2,completed_at=$3,updated_at=$4 WHERE org=$5 AND id=$6`,
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
		`SELECT status, COUNT(*) FROM annotation_queue_items WHERE org=$1 AND queue_id=$2 GROUP BY status`, org, queueID)
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
