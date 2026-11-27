// Copyright © 2026 Hanzo AI. MIT License.

package cyber

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// github.com/hanzoai/sqlite is the one Hanzo SQLite driver (SQLCipher under CGO,
	// pure-Go modernc under !cgo). Blank import registers the "sqlite" driver.
	_ "github.com/hanzoai/sqlite"
)

var errNotFound = errors.New("cyber: not found")

// Engagement is an authorized cyber assessment workspace scoped to an org.
type Engagement struct {
	ID        string
	Org       string
	Name      string
	Target    string
	Scope     []string
	Mode      string
	Status    string
	Synthetic bool
	CreatedAt int64
	UpdatedAt int64
}

// Task is an autonomous or operator-guided cyber objective inside an engagement.
type Task struct {
	ID           string
	Org          string
	EngagementID string
	Title        string
	Goal         string
	Status       string
	Phase        string
	CreatedAt    int64
	UpdatedAt    int64
}

// GraphNode is one node in a task reasoning graph (goal, intent, finding, hint, action).
type GraphNode struct {
	ID        string
	Org       string
	TaskID    string
	Kind      string
	Label     string
	Detail    string
	Status    string
	Severity  string
	ParentID  string
	CreatedAt int64
}

// Finding is a security finding with optional verbatim proof bound to captured HTTP traffic.
type Finding struct {
	ID               string
	Org              string
	EngagementID     string
	TaskID           string
	Title            string
	Severity         string
	Category         string
	CWE              string
	MitreTechnique   string
	MitreTactic      string
	Status           string
	Target           string
	EvidenceQuote    string
	TrafficID        string
	Verified         bool
	VerificationNote string
	Remediation      string
	CreatedAt        int64
	UpdatedAt        int64
}

// TrafficRecord is a captured HTTP request/response exchange used for analysis and verbatim proof.
type TrafficRecord struct {
	ID              string
	Org             string
	EngagementID    string
	TaskID          string
	Method          string
	URL             string
	Host            string
	StatusCode      int
	RequestHeaders  string
	RequestBody     string
	ResponseHeaders string
	ResponseBody    string
	LatencyMS       int
	CreatedAt       int64
}

// Approval is a Kai risk-scored action gate awaiting or recording human decision.
type Approval struct {
	ID           string
	Org          string
	EngagementID string
	TaskID       string
	Action       string
	Target       string
	Tool         string
	RiskScore    int
	RiskLevel    string
	KaiRationale string
	Status       string
	DecidedBy    string
	DecidedAt    int64
	CreatedAt    int64
}

// InterceptRule is a scope or action-governance rule applied to cyber operations.
type InterceptRule struct {
	ID           string
	Org          string
	EngagementID string
	Name         string
	MatchHost    string
	MatchPath    string
	Action       string
	Enabled      bool
	CreatedAt    int64
}

// Store persists org-isolated cyber operations state in the shared SQL dialect.
type Store struct {
	db *sql.DB
}

func openStore(dir string) (*Store, error) {
	s, err := locus.Use(context.Background(), nil, namespace.System(), "cyber", dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error { return s.migrate(ctx, tx) }); err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open cyber store: %w", err)
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS cyber_engagements (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  name       TEXT NOT NULL,
  target     TEXT NOT NULL DEFAULT '',
  scope_json TEXT NOT NULL DEFAULT '[]',
  mode       TEXT NOT NULL DEFAULT 'enclave',
  status     TEXT NOT NULL DEFAULT 'active',
  synthetic  BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_cyber_eng_org ON cyber_engagements(org, created_at DESC);

CREATE TABLE IF NOT EXISTS cyber_tasks (
  id            TEXT PRIMARY KEY,
  org           TEXT NOT NULL,
  engagement_id TEXT NOT NULL,
  title         TEXT NOT NULL,
  goal          TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'active',
  phase         TEXT NOT NULL DEFAULT 'recon',
  created_at    BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_cyber_tasks_org_eng ON cyber_tasks(org, engagement_id, created_at DESC);

CREATE TABLE IF NOT EXISTS cyber_nodes (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  task_id    TEXT NOT NULL,
  kind       TEXT NOT NULL,
  label      TEXT NOT NULL,
  detail     TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'active',
  severity   TEXT NOT NULL DEFAULT '',
  parent_id  TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_cyber_nodes_org_task ON cyber_nodes(org, task_id, created_at ASC);

CREATE TABLE IF NOT EXISTS cyber_findings (
  id                TEXT PRIMARY KEY,
  org               TEXT NOT NULL,
  engagement_id     TEXT NOT NULL,
  task_id           TEXT NOT NULL DEFAULT '',
  title             TEXT NOT NULL,
  severity          TEXT NOT NULL DEFAULT 'medium',
  category          TEXT NOT NULL DEFAULT '',
  cwe               TEXT NOT NULL DEFAULT '',
  mitre_technique   TEXT NOT NULL DEFAULT '',
  mitre_tactic      TEXT NOT NULL DEFAULT '',
  status            TEXT NOT NULL DEFAULT 'open',
  target            TEXT NOT NULL DEFAULT '',
  evidence_quote    TEXT NOT NULL DEFAULT '',
  traffic_id        TEXT NOT NULL DEFAULT '',
  verified          BIGINT NOT NULL DEFAULT 0,
  verification_note TEXT NOT NULL DEFAULT '',
  remediation       TEXT NOT NULL DEFAULT '',
  created_at        BIGINT NOT NULL,
  updated_at        BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_cyber_findings_org_eng ON cyber_findings(org, engagement_id, created_at DESC);

CREATE TABLE IF NOT EXISTS cyber_traffic (
  id               TEXT PRIMARY KEY,
  org              TEXT NOT NULL,
  engagement_id    TEXT NOT NULL,
  task_id          TEXT NOT NULL DEFAULT '',
  method           TEXT NOT NULL DEFAULT 'GET',
  url              TEXT NOT NULL,
  host             TEXT NOT NULL DEFAULT '',
  status_code      BIGINT NOT NULL DEFAULT 200,
  request_headers  TEXT NOT NULL DEFAULT '',
  request_body     TEXT NOT NULL DEFAULT '',
  response_headers TEXT NOT NULL DEFAULT '',
  response_body    TEXT NOT NULL DEFAULT '',
  latency_ms       BIGINT NOT NULL DEFAULT 0,
  created_at       BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_cyber_traffic_org_eng ON cyber_traffic(org, engagement_id, created_at DESC);

CREATE TABLE IF NOT EXISTS cyber_approvals (
  id            TEXT PRIMARY KEY,
  org           TEXT NOT NULL,
  engagement_id TEXT NOT NULL,
  task_id       TEXT NOT NULL DEFAULT '',
  action        TEXT NOT NULL,
  target        TEXT NOT NULL DEFAULT '',
  tool          TEXT NOT NULL DEFAULT '',
  risk_score    BIGINT NOT NULL DEFAULT 50,
  risk_level    TEXT NOT NULL DEFAULT 'medium',
  kai_rationale TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'pending',
  decided_by    TEXT NOT NULL DEFAULT '',
  decided_at    BIGINT NOT NULL DEFAULT 0,
  created_at    BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_cyber_approvals_org_eng ON cyber_approvals(org, engagement_id, created_at DESC);

CREATE TABLE IF NOT EXISTS cyber_intercept_rules (
  id            TEXT PRIMARY KEY,
  org           TEXT NOT NULL,
  engagement_id TEXT NOT NULL DEFAULT '',
  name          TEXT NOT NULL,
  match_host    TEXT NOT NULL DEFAULT '*',
  match_path    TEXT NOT NULL DEFAULT '/*',
  action        TEXT NOT NULL DEFAULT 'require_approval',
  enabled       BIGINT NOT NULL DEFAULT 1,
  created_at    BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_cyber_rules_org ON cyber_intercept_rules(org, created_at DESC);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

func boolToInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func encodeScope(scope []string) string {
	if len(scope) == 0 {
		return "[]"
	}
	b, err := json.Marshal(scope)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeScope(raw string) []string {
	var out []string
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

// ---- Engagements ----

func (s *Store) SaveEngagement(ctx context.Context, e Engagement) error {
	scopeJSON := encodeScope(e.Scope)
	if err := dialect.Text(e.ID, e.Org, e.Name, e.Target, scopeJSON, e.Mode, e.Status); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cyber_engagements (id,org,name,target,scope_json,mode,status,synthetic,created_at,updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		 ON CONFLICT (id) DO UPDATE SET
		   name = EXCLUDED.name,
		   target = EXCLUDED.target,
		   scope_json = EXCLUDED.scope_json,
		   mode = EXCLUDED.mode,
		   status = EXCLUDED.status,
		   updated_at = EXCLUDED.updated_at`,
		e.ID, e.Org, e.Name, e.Target, scopeJSON, e.Mode, e.Status, boolToInt(e.Synthetic), e.CreatedAt, e.UpdatedAt)
	return err
}

func (s *Store) ListEngagements(ctx context.Context, org string) ([]Engagement, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,name,target,scope_json,mode,status,synthetic,created_at,updated_at
		 FROM cyber_engagements WHERE org=$1 ORDER BY created_at DESC, id DESC`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Engagement
	for rows.Next() {
		var e Engagement
		var scopeRaw string
		var syn int64
		if err := rows.Scan(&e.ID, &e.Org, &e.Name, &e.Target, &scopeRaw, &e.Mode, &e.Status, &syn, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.Scope = decodeScope(scopeRaw)
		e.Synthetic = syn != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) GetEngagement(ctx context.Context, org, id string) (Engagement, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id,org,name,target,scope_json,mode,status,synthetic,created_at,updated_at
		 FROM cyber_engagements WHERE org=$1 AND id=$2`, org, id)
	var e Engagement
	var scopeRaw string
	var syn int64
	if err := row.Scan(&e.ID, &e.Org, &e.Name, &e.Target, &scopeRaw, &e.Mode, &e.Status, &syn, &e.CreatedAt, &e.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Engagement{}, errNotFound
		}
		return Engagement{}, err
	}
	e.Scope = decodeScope(scopeRaw)
	e.Synthetic = syn != 0
	return e, nil
}

// ---- Tasks & Graph Nodes ----

func (s *Store) SaveTask(ctx context.Context, t Task) error {
	if err := dialect.Text(t.ID, t.Org, t.EngagementID, t.Title, t.Goal, t.Status, t.Phase); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cyber_tasks (id,org,engagement_id,title,goal,status,phase,created_at,updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (id) DO UPDATE SET
		   title = EXCLUDED.title,
		   goal = EXCLUDED.goal,
		   status = EXCLUDED.status,
		   phase = EXCLUDED.phase,
		   updated_at = EXCLUDED.updated_at`,
		t.ID, t.Org, t.EngagementID, t.Title, t.Goal, t.Status, t.Phase, t.CreatedAt, t.UpdatedAt)
	return err
}

func (s *Store) GetTask(ctx context.Context, org, id string) (Task, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id,org,engagement_id,title,goal,status,phase,created_at,updated_at
		 FROM cyber_tasks WHERE org=$1 AND id=$2`, org, id)
	var t Task
	if err := row.Scan(&t.ID, &t.Org, &t.EngagementID, &t.Title, &t.Goal, &t.Status, &t.Phase, &t.CreatedAt, &t.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, errNotFound
		}
		return Task{}, err
	}
	return t, nil
}

func (s *Store) ListTasks(ctx context.Context, org, engagementID string) ([]Task, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if engagementID != "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id,org,engagement_id,title,goal,status,phase,created_at,updated_at
			 FROM cyber_tasks WHERE org=$1 AND engagement_id=$2 ORDER BY created_at DESC, id DESC`,
			org, engagementID)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id,org,engagement_id,title,goal,status,phase,created_at,updated_at
			 FROM cyber_tasks WHERE org=$1 ORDER BY created_at DESC, id DESC`,
			org)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Org, &t.EngagementID, &t.Title, &t.Goal, &t.Status, &t.Phase, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) SaveNode(ctx context.Context, n GraphNode) error {
	if err := dialect.Text(n.ID, n.Org, n.TaskID, n.Kind, n.Label, n.Detail, n.Status, n.Severity, n.ParentID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cyber_nodes (id,org,task_id,kind,label,detail,status,severity,parent_id,created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		 ON CONFLICT (id) DO UPDATE SET
		   label = EXCLUDED.label,
		   detail = EXCLUDED.detail,
		   status = EXCLUDED.status,
		   severity = EXCLUDED.severity`,
		n.ID, n.Org, n.TaskID, n.Kind, n.Label, n.Detail, n.Status, n.Severity, n.ParentID, n.CreatedAt)
	return err
}

func (s *Store) ListNodes(ctx context.Context, org, taskID string) ([]GraphNode, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,task_id,kind,label,detail,status,severity,parent_id,created_at
		 FROM cyber_nodes WHERE org=$1 AND task_id=$2 ORDER BY created_at ASC, id ASC`, org, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []GraphNode
	for rows.Next() {
		var n GraphNode
		if err := rows.Scan(&n.ID, &n.Org, &n.TaskID, &n.Kind, &n.Label, &n.Detail, &n.Status, &n.Severity, &n.ParentID, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ---- Findings ----

const findingCols = `id,org,engagement_id,task_id,title,severity,category,cwe,mitre_technique,mitre_tactic,status,target,evidence_quote,traffic_id,verified,verification_note,remediation,created_at,updated_at`

func scanFinding(sc interface{ Scan(...any) error }) (Finding, error) {
	var f Finding
	var ver int64
	err := sc.Scan(
		&f.ID, &f.Org, &f.EngagementID, &f.TaskID, &f.Title, &f.Severity, &f.Category,
		&f.CWE, &f.MitreTechnique, &f.MitreTactic, &f.Status, &f.Target,
		&f.EvidenceQuote, &f.TrafficID, &ver, &f.VerificationNote, &f.Remediation,
		&f.CreatedAt, &f.UpdatedAt,
	)
	f.Verified = ver != 0
	return f, err
}

func (s *Store) SaveFinding(ctx context.Context, f Finding) error {
	if err := dialect.Text(
		f.ID, f.Org, f.EngagementID, f.TaskID, f.Title, f.Severity, f.Category,
		f.CWE, f.MitreTechnique, f.MitreTactic, f.Status, f.Target,
		f.EvidenceQuote, f.TrafficID, f.VerificationNote, f.Remediation,
	); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cyber_findings (`+findingCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		 ON CONFLICT (id) DO UPDATE SET
		   title = EXCLUDED.title,
		   severity = EXCLUDED.severity,
		   status = EXCLUDED.status,
		   evidence_quote = EXCLUDED.evidence_quote,
		   traffic_id = EXCLUDED.traffic_id,
		   verified = EXCLUDED.verified,
		   verification_note = EXCLUDED.verification_note,
		   remediation = EXCLUDED.remediation,
		   updated_at = EXCLUDED.updated_at`,
		f.ID, f.Org, f.EngagementID, f.TaskID, f.Title, f.Severity, f.Category,
		f.CWE, f.MitreTechnique, f.MitreTactic, f.Status, f.Target,
		f.EvidenceQuote, f.TrafficID, boolToInt(f.Verified), f.VerificationNote, f.Remediation,
		f.CreatedAt, f.UpdatedAt)
	return err
}

func (s *Store) GetFinding(ctx context.Context, org, id string) (Finding, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+findingCols+` FROM cyber_findings WHERE org=$1 AND id=$2`, org, id)
	f, err := scanFinding(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Finding{}, errNotFound
	}
	return f, err
}

func (s *Store) ListFindings(ctx context.Context, org, engagementID, severity, status string) ([]Finding, error) {
	var (
		args  = []any{org}
		where = []string{"org=$1"}
	)
	if engagementID != "" {
		args = append(args, engagementID)
		where = append(where, fmt.Sprintf("engagement_id=$%d", len(args)))
	}
	if severity != "" {
		args = append(args, severity)
		where = append(where, fmt.Sprintf("severity=$%d", len(args)))
	}
	if status != "" {
		args = append(args, status)
		where = append(where, fmt.Sprintf("status=$%d", len(args)))
	}
	q := `SELECT ` + findingCols + ` FROM cyber_findings WHERE ` + strings.Join(where, " AND ") + ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---- Traffic ----

const trafficCols = `id,org,engagement_id,task_id,method,url,host,status_code,request_headers,request_body,response_headers,response_body,latency_ms,created_at`

func scanTraffic(sc interface{ Scan(...any) error }) (TrafficRecord, error) {
	var tr TrafficRecord
	err := sc.Scan(
		&tr.ID, &tr.Org, &tr.EngagementID, &tr.TaskID, &tr.Method, &tr.URL, &tr.Host,
		&tr.StatusCode, &tr.RequestHeaders, &tr.RequestBody, &tr.ResponseHeaders, &tr.ResponseBody,
		&tr.LatencyMS, &tr.CreatedAt,
	)
	return tr, err
}

func (s *Store) SaveTraffic(ctx context.Context, tr TrafficRecord) error {
	if err := dialect.Text(
		tr.ID, tr.Org, tr.EngagementID, tr.TaskID, tr.Method, tr.URL, tr.Host,
		tr.RequestHeaders, tr.RequestBody, tr.ResponseHeaders, tr.ResponseBody,
	); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cyber_traffic (`+trafficCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		 ON CONFLICT (id) DO NOTHING`,
		tr.ID, tr.Org, tr.EngagementID, tr.TaskID, tr.Method, tr.URL, tr.Host,
		tr.StatusCode, tr.RequestHeaders, tr.RequestBody, tr.ResponseHeaders, tr.ResponseBody,
		tr.LatencyMS, tr.CreatedAt)
	return err
}

func (s *Store) GetTraffic(ctx context.Context, org, id string) (TrafficRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+trafficCols+` FROM cyber_traffic WHERE org=$1 AND id=$2`, org, id)
	tr, err := scanTraffic(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TrafficRecord{}, errNotFound
	}
	return tr, err
}

func (s *Store) ListTraffic(ctx context.Context, org, engagementID, host string) ([]TrafficRecord, error) {
	var (
		args  = []any{org}
		where = []string{"org=$1"}
	)
	if engagementID != "" {
		args = append(args, engagementID)
		where = append(where, fmt.Sprintf("engagement_id=$%d", len(args)))
	}
	if host != "" {
		args = append(args, host)
		where = append(where, fmt.Sprintf("host=$%d", len(args)))
	}
	q := `SELECT ` + trafficCols + ` FROM cyber_traffic WHERE ` + strings.Join(where, " AND ") + ` ORDER BY created_at DESC, id DESC LIMIT 200`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TrafficRecord
	for rows.Next() {
		tr, err := scanTraffic(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}

// ---- Approvals ----

const approvalCols = `id,org,engagement_id,task_id,action,target,tool,risk_score,risk_level,kai_rationale,status,decided_by,decided_at,created_at`

func scanApproval(sc interface{ Scan(...any) error }) (Approval, error) {
	var a Approval
	err := sc.Scan(
		&a.ID, &a.Org, &a.EngagementID, &a.TaskID, &a.Action, &a.Target, &a.Tool,
		&a.RiskScore, &a.RiskLevel, &a.KaiRationale, &a.Status, &a.DecidedBy, &a.DecidedAt, &a.CreatedAt,
	)
	return a, err
}

func (s *Store) SaveApproval(ctx context.Context, a Approval) error {
	if err := dialect.Text(
		a.ID, a.Org, a.EngagementID, a.TaskID, a.Action, a.Target, a.Tool,
		a.RiskLevel, a.KaiRationale, a.Status, a.DecidedBy,
	); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cyber_approvals (`+approvalCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		 ON CONFLICT (id) DO UPDATE SET
		   status = EXCLUDED.status,
		   decided_by = EXCLUDED.decided_by,
		   decided_at = EXCLUDED.decided_at`,
		a.ID, a.Org, a.EngagementID, a.TaskID, a.Action, a.Target, a.Tool,
		a.RiskScore, a.RiskLevel, a.KaiRationale, a.Status, a.DecidedBy, a.DecidedAt, a.CreatedAt)
	return err
}

func (s *Store) GetApproval(ctx context.Context, org, id string) (Approval, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+approvalCols+` FROM cyber_approvals WHERE org=$1 AND id=$2`, org, id)
	a, err := scanApproval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, errNotFound
	}
	return a, err
}

func (s *Store) ListApprovals(ctx context.Context, org, engagementID, status string) ([]Approval, error) {
	var (
		args  = []any{org}
		where = []string{"org=$1"}
	)
	if engagementID != "" {
		args = append(args, engagementID)
		where = append(where, fmt.Sprintf("engagement_id=$%d", len(args)))
	}
	if status != "" {
		args = append(args, status)
		where = append(where, fmt.Sprintf("status=$%d", len(args)))
	}
	q := `SELECT ` + approvalCols + ` FROM cyber_approvals WHERE ` + strings.Join(where, " AND ") + ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- Intercept Rules ----

func (s *Store) SaveRule(ctx context.Context, r InterceptRule) error {
	if err := dialect.Text(r.ID, r.Org, r.EngagementID, r.Name, r.MatchHost, r.MatchPath, r.Action); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cyber_intercept_rules (id,org,engagement_id,name,match_host,match_path,action,enabled,created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (id) DO UPDATE SET
		   name = EXCLUDED.name,
		   match_host = EXCLUDED.match_host,
		   match_path = EXCLUDED.match_path,
		   action = EXCLUDED.action,
		   enabled = EXCLUDED.enabled`,
		r.ID, r.Org, r.EngagementID, r.Name, r.MatchHost, r.MatchPath, r.Action, boolToInt(r.Enabled), r.CreatedAt)
	return err
}

func (s *Store) ListRules(ctx context.Context, org, engagementID string) ([]InterceptRule, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if engagementID != "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id,org,engagement_id,name,match_host,match_path,action,enabled,created_at
			 FROM cyber_intercept_rules WHERE org=$1 AND (engagement_id=$2 OR engagement_id='') ORDER BY created_at DESC, id DESC`,
			org, engagementID)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id,org,engagement_id,name,match_host,match_path,action,enabled,created_at
			 FROM cyber_intercept_rules WHERE org=$1 ORDER BY created_at DESC, id DESC`,
			org)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []InterceptRule
	for rows.Next() {
		var r InterceptRule
		var en int64
		if err := rows.Scan(&r.ID, &r.Org, &r.EngagementID, &r.Name, &r.MatchHost, &r.MatchPath, &r.Action, &en, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Enabled = en != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteRule(ctx context.Context, org, id string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM cyber_intercept_rules WHERE org=$1 AND id=$2`, org, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errNotFound
	}
	return nil
}
