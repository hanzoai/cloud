package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// errConflict is returned when (org,key) or (project,number) already exists;
// errNotFound when a lookup misses. Handlers map these to HTTP 409 / 404.
var (
	errConflict = errors.New("task: already exists")
	errNotFound = errors.New("task: not found")
)

// Project is an org-scoped issue task project (a Linear "team"). Its Key is
// the org-unique, uppercase handle that both routes the URL (/v1/task/
// projects/:key) AND prefixes every issue identifier (KEY-<number>). Org
// isolation is the org column, filtered on every query.
//
// Distinct from projects.Project (not a duplicate): that is the Slug-keyed
// deployable site forming this task's physical tenant boundary; these
// KEY-prefixed teams are rows WITHIN it.
type Project struct {
	ID          string
	Org         string
	Key         string
	Name        string
	Description string
	CreatedAt   int64
	UpdatedAt   int64
}

// Issue is THE ONE engineering/project work-item primitive for Hanzo —
// polymorphic by Kind so a git issue, a pull request and a parent epic are all
// the same row, and every work-item surface (hanzo.team's board, a git repo's
// Issues/PRs tab, an agent's queue) is a FILTER over this table, never a second
// task. It is NOT the domain-record plane: helpdesk tickets, CMS content and
// CRM deals live on framework.DocType / crm and link here by ExtRef — see
// contract.go for the three-plane boundary. Number is monotonic PER PROJECT
// (KEY-1, KEY-2, …), allocated under the single-writer transaction in
// CreateIssue so it never races. Status is the board column; Labels is a
// comma-joined tag string (split at the HTTP boundary).
//
// The four discriminators (Kind, Source, Repo, ExtRef) are the alignment spine:
//   - Kind:   issue | pr | epic — what it IS (the small closed work-item set).
//   - Source: team | git | crm | helpdesk | cms | agent — which surface OPENED it.
//   - Repo:   the git repo it belongs to (Kind pr/issue from git); "" otherwise —
//     so a repo's Issues/PRs tab is `ListIssues(... Filter{Repo, Kind})`.
//   - ExtRef: the external anchor (PR branch, or a link INTO another plane).
//
// They are identity, set once at Create and immutable thereafter (Update touches
// only the mutable board state), so a row never migrates between surfaces.
//
// StartAt/DueAt are the row's SCHEDULE — the interval a timeline draws it on, in
// unix seconds, 0 meaning unset. They are mutable board state like Status: work
// gets rescheduled. There is no milestone table, because a milestone is not a
// second kind of thing: it is this row with a DueAt and no StartAt — an interval
// of zero length, a point in time. A dated epic is a phase (its children are
// already reachable by ExtRef); a dated issue is a bar. So the timeline is a
// FILTER + a projection over the one table, exactly like the board, and neither
// view can drift from the other's data.
type Issue struct {
	ID        string
	ProjectID string
	Org       string
	Number    int
	Kind      string // issue | pr | epic (default "issue")
	Source    string // team | git | crm | helpdesk | cms | agent (default "team")
	Repo      string // git repo binding; "" = not repo-bound
	// Channel binding — the collaboration channel this item belongs to (HIP-0523),
	// spelled "<space>_<channel>"; "" = not channel-bound.
	//
	// It sits BESIDE Repo rather than inside ExtRef or Labels, and both of those
	// were tried on paper first. ExtRef is single-valued and is the mirror's
	// idempotency key, so a channel written there displaces the anchor a GitHub
	// redelivery finds its own row by. A label is destroyed by any PATCH that
	// names a status or priority, because columnLabels replaces the whole set —
	// so the binding would vanish the first time somebody moved the card.
	//
	// The DIRECTION is the point: the ITEM names its channel, and the channel holds
	// nothing about the item. A channel carrying a list of issues would be the
	// parallel work-item store HIP-1160 §1 forbids, and it would drift the first
	// time an item was closed anywhere else.
	Channel     string
	ExtRef      string // external anchor (PR branch, or link into another plane)
	Title       string
	Description string
	Status      string
	Priority    string
	Assignee    string
	Labels      string
	StartAt     int64 // unix seconds the work starts; 0 = unset
	DueAt       int64 // unix seconds the work is due; 0 = unset
	CreatedAt   int64
	UpdatedAt   int64
}

// Store is one (org, project)'s task database — ONE database per project: a SQLite
// file at {DataDir}/orgs/{orgSlug}/projects/{projectSlug}/tracker.db (opened via
// cloud.OrgDB), or on hanzo-sql a schema of that name. task is project-scoped: the
// physical boundary is the IAM project, with the org column retained as
// defense-in-depth. On a file MaxOpenConns(1) serializes writes against the file
// lock; on hanzo-sql CreateIssue takes the store's write lock (BeginWrite), which
// makes the number allocation a safe read-modify-write either way. Its SQL is the
// dialect both engines share (sdk/dialect).
type Store struct {
	db *sql.DB
}

// openStore wraps an org DB — already opened + pragma'd by cloud.OrgDB —
// into the task store, running its migration. It is the open func the shared
// cloud.OrgStore cache calls once per project file.
func openStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate brings the database to the current schema in ONE transaction that holds
// the store's migration lock; everything below goes through tx.
func (s *Store) migrate() error {
	d := dialect.Of(s.db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error { return migrateIn(ctx, d, tx) }); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func migrateIn(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	// Migrate in dependency order: base tables, then the forward-added spine
	// columns, then the indexes that reference those columns. A CREATE INDEX over a
	// column a pre-existing (legacy) table lacks fails ("no such column"), which
	// fails mount and crashloops the pod on deploy — so any index over an
	// ALTER-added column (kind, repo) MUST come after the ADD COLUMN pass, never in
	// the base DDL.
	const ddl = `
CREATE TABLE IF NOT EXISTS projects (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  key          TEXT NOT NULL,
  name         TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_projects_org_key ON projects(org, key);
CREATE INDEX IF NOT EXISTS ix_projects_org_updated ON projects(org, updated_at);

CREATE TABLE IF NOT EXISTS issues (
  id           TEXT PRIMARY KEY,
  project_id   TEXT NOT NULL,
  org          TEXT NOT NULL,
  number       BIGINT NOT NULL,
  kind         TEXT NOT NULL DEFAULT 'issue',
  source       TEXT NOT NULL DEFAULT 'team',
  repo         TEXT NOT NULL DEFAULT '',
  ext_ref      TEXT NOT NULL DEFAULT '',
  title        TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'backlog',
  priority     TEXT NOT NULL DEFAULT 'none',
  assignee     TEXT NOT NULL DEFAULT '',
  labels       TEXT NOT NULL DEFAULT '',
  start_at     BIGINT NOT NULL DEFAULT 0,
  due_at       BIGINT NOT NULL DEFAULT 0,
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_issues_project_number ON issues(project_id, number);
CREATE INDEX IF NOT EXISTS ix_issues_org_project_status ON issues(org, project_id, status);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	// Additive columns for DBs created before the polymorphic-spine alignment, each
	// added only when the table does not carry it yet. Every column carries a DEFAULT
	// so existing rows read as native project issues (kind='issue', source='team').
	for _, col := range []struct{ name, decl string }{
		{"kind", "TEXT NOT NULL DEFAULT 'issue'"},
		{"source", "TEXT NOT NULL DEFAULT 'team'"},
		{"repo", "TEXT NOT NULL DEFAULT ''"},
		{"ext_ref", "TEXT NOT NULL DEFAULT ''"},
		// The schedule pair, forward-added the same way: an existing row reads as
		// unscheduled (0/0) and renders on the board exactly as before, so the
		// timeline is additive to a live task rather than a rewrite of it.
		{"start_at", "BIGINT NOT NULL DEFAULT 0"},
		{"due_at", "BIGINT NOT NULL DEFAULT 0"},
		// The channel binding, forward-added the same way: an existing row reads as
		// bound to no room and renders on every board exactly as before, so a
		// channel's task list is additive to a live task rather than a rewrite.
		{"room", "TEXT NOT NULL DEFAULT ''"},
		// The request a row was filed for (agent pull requests), so a retry of the call that
		// filed it answers the row and files no second.
		{"request", "TEXT NOT NULL DEFAULT ''"},
	} {
		has, err := d.HasColumn(ctx, tx, "issues", col.name)
		if err != nil {
			return fmt.Errorf("add column %s: %w", col.name, err)
		}
		if has {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE issues ADD COLUMN `+col.name+` `+col.decl); err != nil {
			return fmt.Errorf("add column %s: %w", col.name, err)
		}
	}
	// Indexes over the ALTER-added spine columns (repo, kind) — created only now
	// that the ADD COLUMN pass above guarantees they exist on every issues table.
	// On a legacy DB whose issues table predates the spine, CREATE TABLE IF NOT
	// EXISTS no-ops, so indexing repo/kind in the base DDL would fail ("no such
	// column") before the column is added. Column first, then its index.
	const spineIdx = `
CREATE INDEX IF NOT EXISTS ix_issues_org_repo ON issues(org, repo);
CREATE INDEX IF NOT EXISTS ix_issues_org_kind ON issues(org, kind);
-- ExtRef lookup: the external-mirror upsert (GitHub issue → task) finds the
-- existing row for an anchor by (org, project, ext_ref) so a webhook redelivery or
-- a backfill re-run updates in place instead of duplicating.
CREATE INDEX IF NOT EXISTS ix_issues_extref ON issues(org, project_id, ext_ref);
-- Timeline read: the gantt asks one board for the rows that carry a schedule, so
-- the due date is indexed within the tenant the same way status is. An unscheduled
-- board costs the index nothing (every row shares the 0 key) and a scheduled one
-- reads its window without touching the rows that have no dates.
CREATE INDEX IF NOT EXISTS ix_issues_org_project_due ON issues(org, project_id, due_at);
-- A channel's task list: one room's items, read straight out of the tenant's own
-- index. It is keyed (org, room) rather than (org, project, room) because a room
-- is addressed across every board of the org — the work a channel is about does
-- not have to live on one repository's board, and asking board by board is the
-- question this index exists to avoid.
CREATE INDEX IF NOT EXISTS ix_issues_org_room ON issues(org, room);
-- One row per request within a project: the retry of a call that files a row names it.
CREATE UNIQUE INDEX IF NOT EXISTS ux_issues_request ON issues(project_id, request) WHERE request <> '';
`
	if _, err := tx.ExecContext(ctx, spineIdx); err != nil {
		return fmt.Errorf("spine index: %w", err)
	}
	if !d.Postgres() {
		if err := scrubIssues(ctx, tx); err != nil {
			return fmt.Errorf("scrub issues: %w", err)
		}
	}
	return nil
}

// scrubIssues repairs pre-dialect SQLite rows whose title or description
// holds invalid UTF-8 or NUL bytes so PostgreSQL's text type accepts them on move.
func scrubIssues(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, title, description FROM issues`)
	if err != nil {
		return err
	}
	type fix struct{ id, title, desc string }
	var fixes []fix
	for rows.Next() {
		var f fix
		if err := rows.Scan(&f.id, &f.title, &f.desc); err != nil {
			_ = rows.Close()
			return err
		}
		if dialect.Text(f.title, f.desc) != nil {
			f.title = strings.ToValidUTF8(strings.ReplaceAll(f.title, "\x00", ""), "\uFFFD")
			f.desc = strings.ToValidUTF8(strings.ReplaceAll(f.desc, "\x00", ""), "\uFFFD")
			fixes = append(fixes, f)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, f := range fixes {
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET title=$1, description=$2 WHERE id=$3`, f.title, f.desc, f.id); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const projectCols = `id,org,key,name,description,created_at,updated_at`

func scanProject(sc interface{ Scan(...any) error }) (Project, error) {
	var p Project
	err := sc.Scan(&p.ID, &p.Org, &p.Key, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// CreateProject inserts one project. A UNIQUE(org,key) violation surfaces as
// errConflict.
func (s *Store) CreateProject(ctx context.Context, p Project) error {
	if err := dialect.Text(p.ID, p.Org, p.Key, p.Name, p.Description); err != nil {
		return fmt.Errorf("insert project: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO projects (`+projectCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		p.ID, p.Org, p.Key, p.Name, p.Description, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		if dialect.Unique(err) {
			return errConflict
		}
		return fmt.Errorf("insert project: %w", err)
	}
	return nil
}

// GetProject returns the project for (org,key) or errNotFound.
func (s *Store) GetProject(ctx context.Context, org, key string) (Project, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+projectCols+` FROM projects WHERE org=$1 AND key=$2`, org, key)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, errNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

// ListProjects returns every project for org, most-recently-updated first.
func (s *Store) ListProjects(ctx context.Context, org string) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+projectCols+` FROM projects WHERE org=$1 ORDER BY updated_at DESC, id ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProject overwrites the mutable fields (name, description). org+key+id+
// created_at are immutable.
func (s *Store) UpdateProject(ctx context.Context, p Project) error {
	if err := dialect.Text(p.Name, p.Description); err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET name=$1,description=$2,updated_at=$3 WHERE org=$4 AND key=$5`,
		p.Name, p.Description, p.UpdatedAt, p.Org, p.Key)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

// DeleteProject removes a project and all its issues in one transaction. Reports
// whether a project row was deleted.
func (s *Store) DeleteProject(ctx context.Context, org, key string) (bool, error) {
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM projects WHERE org=$1 AND key=$2`, org, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get for delete: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE project_id=$1`, id); err != nil {
		return false, fmt.Errorf("delete issues: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id=$1`, id); err != nil {
		return false, fmt.Errorf("delete project: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

const issueCols = `id,project_id,org,number,kind,source,repo,room,ext_ref,title,description,status,priority,assignee,labels,start_at,due_at,created_at,updated_at`

func scanIssue(sc interface{ Scan(...any) error }) (Issue, error) {
	var i Issue
	err := sc.Scan(&i.ID, &i.ProjectID, &i.Org, &i.Number, &i.Kind, &i.Source, &i.Repo,
		&i.Channel, &i.ExtRef, &i.Title, &i.Description, &i.Status, &i.Priority, &i.Assignee,
		&i.Labels, &i.StartAt, &i.DueAt, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

// CreateIssue allocates the next per-project number and inserts the issue in ONE
// transaction. The transaction is the store's only writer for its whole duration
// (a file's single connection, or BeginWrite's lock on hanzo-sql), so the
// MAX(number)+1 read and the INSERT are serialized against any concurrent create —
// the number can never be handed out twice.
func (s *Store) CreateIssue(ctx context.Context, i Issue) (Issue, error) {
	return s.CreateIssueOnce(ctx, i, "")
}

// CreateIssueOnce files i, and files it once per request within its project: a row already
// filed under request is answered as it is. An empty request files a new row each time. The
// lookup and the insert are one write transaction, so two callers of one request cannot
// both file.
func (s *Store) CreateIssueOnce(ctx context.Context, i Issue, request string) (Issue, error) {
	if err := dialect.Text(i.ID, i.ProjectID, i.Org, i.Kind, i.Source, i.Repo, i.Channel, i.ExtRef, i.Title, i.Description, i.Status, i.Priority, i.Assignee, i.Labels); err != nil {
		return Issue{}, fmt.Errorf("insert issue: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Issue{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if request != "" {
		if err := dialect.Text(request); err != nil {
			return Issue{}, fmt.Errorf("insert issue: %w", err)
		}
		had, err := scanIssue(tx.QueryRowContext(ctx,
			`SELECT `+issueCols+` FROM issues WHERE project_id=$1 AND request=$2`, i.ProjectID, request))
		switch {
		case err == nil:
			return had, nil
		case !errors.Is(err, sql.ErrNoRows):
			return Issue{}, fmt.Errorf("find issue: %w", err)
		}
	}
	var next int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(number),0)+1 FROM issues WHERE project_id=$1`, i.ProjectID).Scan(&next); err != nil {
		return Issue{}, fmt.Errorf("next number: %w", err)
	}
	i.Number = next
	if i.Kind == "" {
		i.Kind = "issue"
	}
	if i.Source == "" {
		i.Source = "team"
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO issues (`+issueCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		i.ID, i.ProjectID, i.Org, i.Number, i.Kind, i.Source, i.Repo, i.Channel, i.ExtRef,
		i.Title, i.Description, i.Status, i.Priority, i.Assignee, i.Labels,
		i.StartAt, i.DueAt, i.CreatedAt, i.UpdatedAt); err != nil {
		return Issue{}, fmt.Errorf("insert issue: %w", err)
	}
	if request != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET request=$1 WHERE id=$2`, request, i.ID); err != nil {
			return Issue{}, fmt.Errorf("insert issue: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, fmt.Errorf("commit: %w", err)
	}
	return i, nil
}

// GetIssue returns one issue scoped to (org, project, number) or errNotFound.
func (s *Store) GetIssue(ctx context.Context, org, projectID string, number int) (Issue, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+issueCols+` FROM issues WHERE org=$1 AND project_id=$2 AND number=$3`, org, projectID, number)
	i, err := scanIssue(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, errNotFound
	}
	if err != nil {
		return Issue{}, fmt.Errorf("get issue: %w", err)
	}
	return i, nil
}

// GetIssueByExtRef returns the issue anchored to extRef within (org, project) or
// errNotFound — the idempotency lookup for the external-mirror upsert. ExtRef is not
// globally unique (it is unique per external system), so it is always scoped to the
// project the mirror files into.
func (s *Store) GetIssueByExtRef(ctx context.Context, org, projectID, extRef string) (Issue, error) {
	// An empty anchor is a miss, never a match. Every native issue defaults to an
	// empty ext_ref, so a blank anchor would otherwise select an unrelated team issue
	// — and the mirror upsert, whose whole contract is "found ⇒ update in place",
	// would overwrite it with external content. The miss is the only safe answer.
	if strings.TrimSpace(extRef) == "" {
		return Issue{}, errNotFound
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+issueCols+` FROM issues WHERE org=$1 AND project_id=$2 AND ext_ref=$3 ORDER BY number ASC LIMIT 1`,
		org, projectID, extRef)
	i, err := scanIssue(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, errNotFound
	}
	if err != nil {
		return Issue{}, fmt.Errorf("get issue by extref: %w", err)
	}
	return i, nil
}

// IssueFilter narrows ListIssues. All fields optional (zero = no constraint).
// This is the ONE knob every work-item surface turns: hanzo.team's board passes
// {Status}, its timeline passes {Scheduled}, a git repo's Issues tab passes
// {Repo, Kind:"issue"}, its PRs tab {Repo, Kind:"pr"}, an agent's queue
// {Source:"agent"}.
type IssueFilter struct {
	Status string
	Kind   string
	Repo   string
	// Channel keeps only items bound to that collaboration channel — the query a
	// channel view runs to draw its own task list. It spans every board in the
	// store, because a channel's work is not confined to one repository's board.
	Channel string
	Source  string
	// Scheduled keeps only rows that carry a schedule (a start, a due date, or
	// both) — the timeline's slice. It is the one filter that is a predicate
	// rather than an equality, because "has a date" is what a gantt selects on.
	Scheduled bool
	// Assignee keeps only issues held by that person; "" means anyone. Pair it
	// with Status to answer "what am I working on".
	Assignee string
	// Text matches title or description, case-insensitively. It is the one
	// filter that is a SEARCH rather than a selection, which is why it is a
	// substring and not an equality — a person looking for an issue knows a word
	// from it, not its number.
	Text string
}

// ListIssues returns issues for a project narrowed by IssueFilter. Ordered by
// status then number so a status-grouped view renders deterministically and a
// single-column board reads oldest-first.
func (s *Store) ListIssues(ctx context.Context, org, projectID string, f IssueFilter) ([]Issue, error) {
	// projectID "" means EVERY project this store holds. Cross-project is the
	// same query with one fewer predicate, not a second function: an issue
	// mirrored from GitHub lands in the org's default project, so "find the issue
	// about X" cannot be answered by asking one project at a time.
	args := []any{org}
	// bind adds a value and names its placeholder, so the numbers rise in the order
	// the text is written.
	bind := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	q := `SELECT ` + issueCols + ` FROM issues WHERE org=$1`
	if projectID != "" {
		q += ` AND project_id=` + bind(projectID)
	}
	add := func(col, val string) {
		if val != "" {
			q += ` AND ` + col + `=` + bind(val)
		}
	}
	add("status", f.Status)
	add("kind", f.Kind)
	add("repo", f.Repo)
	add("room", f.Channel)
	add("source", f.Source)
	if a := strings.TrimPrefix(strings.TrimSpace(f.Assignee), "@"); a != "" {
		q += ` AND LOWER(assignee)=LOWER(` + bind(a) + `)`
	}
	if f.Scheduled {
		q += ` AND (start_at>0 OR due_at>0)`
	}
	if t := strings.TrimSpace(f.Text); t != "" {
		like := bind("%" + likeEscape(t) + "%")
		q += ` AND (lower(title) LIKE lower(` + like + `) ESCAPE '\' OR lower(description) LIKE lower(` + like + `) ESCAPE '\')`
	}
	q += ` ORDER BY status ASC, number ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, fmt.Errorf("scan issue: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// UpdateIssue overwrites the mutable fields of an issue scoped to (org, project,
// number). id+project_id+org+number+created_at are immutable. Channel is written
// back as read, so a caller that read the row keeps its binding; the mirror
// upsert is the one writer that sets it.
func (s *Store) UpdateIssue(ctx context.Context, i Issue) error {
	if err := dialect.Text(i.Title, i.Description, i.Status, i.Priority, i.Assignee, i.Labels, i.Channel); err != nil {
		return fmt.Errorf("update issue: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE issues SET title=$1,description=$2,status=$3,priority=$4,assignee=$5,labels=$6,room=$7,start_at=$8,due_at=$9,updated_at=$10
		 WHERE org=$11 AND project_id=$12 AND number=$13`,
		i.Title, i.Description, i.Status, i.Priority, i.Assignee, i.Labels, i.Channel,
		i.StartAt, i.DueAt, i.UpdatedAt,
		i.Org, i.ProjectID, i.Number)
	if err != nil {
		return fmt.Errorf("update issue: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

// DeleteIssue removes one issue scoped to (org, project, number). Reports
// whether a row was deleted.
func (s *Store) DeleteIssue(ctx context.Context, org, projectID string, number int) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM issues WHERE org=$1 AND project_id=$2 AND number=$3`, org, projectID, number)
	if err != nil {
		return false, fmt.Errorf("delete issue: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// likeEscape neutralises the wildcards in a user's search text so a query for
// "50%" finds "50%" and not everything. The ESCAPE clause above names the
// backslash; without this a search box is a way to select the whole table.
//
// THE LITERALS ARE BACKTICKED, so what is written is what SQLite sees. Both this
// and the clause above were written with the doubling a "…" string would have
// needed, which put TWO backslashes in each: `ESCAPE '\\'` is a two-character
// escape expression, SQLite requires exactly one, and every search — the only
// way to read an issue's description on this surface — answered 500. Nothing
// caught it because the escape only appears once the caller passes text.
func likeEscape(v string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(v)
}
