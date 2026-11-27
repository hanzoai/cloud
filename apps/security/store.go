package security

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/security/detect"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers
	// the "sqlite" database/sql name under both build tags (cgo →
	// mattn+SQLCipher, encrypted at rest; !cgo → pure-Go modernc). Importing
	// modernc directly instead would double-register "sqlite" under CGO and
	// panic at init. Blank import registers the driver.
	_ "github.com/hanzoai/sqlite"
)

var errNotFound = errors.New("security: scan not found")

// Scan is the org-scoped record of one submitted scan: what was scanned, when,
// and the finding tally by severity. The individual findings live in the
// findings table, joined by scan_id. Tenancy is the (org, project) columns,
// enforced on every query exactly like clients/git.
type Scan struct {
	ID        string
	Org       string
	Project   string // may be "" (org-level scan)
	Files     int
	Findings  int
	Critical  int
	High      int
	Medium    int
	Low       int
	CreatedAt int64
}

// StoredFinding is the persisted, redacted finding. It NEVER holds the raw
// secret — only the engine's masked Preview and SHA-256 Fingerprint (see
// engine.go). Scoped to (org, scan) so a tenant only ever reads its own.
type StoredFinding struct {
	ID          string
	ScanID      string
	Org         string
	RuleID      string
	RuleName    string
	Severity    string
	Path        string
	Line        int
	Preview     string
	Fingerprint string
	CreatedAt   int64
}

// Store is the findings database. ONE database — the deployment's own "security"
// subsystem, a SQLite file or a schema on hanzo-sql — holds every org's scans +
// findings; tenancy is the org column on both tables.
//
// Its SQL is the dialect both engines share (sdk/dialect) and its tests run
// on both (internal/sqltest).
type Store struct {
	db *sql.DB
}

func openStore(dir string) (*Store, error) {
	s, err := locus.Use(context.Background(), nil, namespace.System(), "security", dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error { return s.migrate(ctx, tx) }); err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open security store: %w", err)
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS scans (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  project    TEXT NOT NULL DEFAULT '',
  files      BIGINT NOT NULL DEFAULT 0,
  findings   BIGINT NOT NULL DEFAULT 0,
  critical   BIGINT NOT NULL DEFAULT 0,
  high       BIGINT NOT NULL DEFAULT 0,
  medium     BIGINT NOT NULL DEFAULT 0,
  low        BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_scans_org_created ON scans(org, created_at DESC);

CREATE TABLE IF NOT EXISTS findings (
  id          TEXT PRIMARY KEY,
  scan_id     TEXT NOT NULL,
  org         TEXT NOT NULL,
  rule_id     TEXT NOT NULL,
  rule_name   TEXT NOT NULL,
  severity    TEXT NOT NULL,
  path        TEXT NOT NULL DEFAULT '',
  line        BIGINT NOT NULL DEFAULT 0,
  preview     TEXT NOT NULL DEFAULT '',
  fingerprint TEXT NOT NULL DEFAULT '',
  created_at  BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_findings_org_scan ON findings(org, scan_id);
CREATE INDEX IF NOT EXISTS ix_findings_org_sev ON findings(org, severity);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const scanCols = `id,org,project,files,findings,critical,high,medium,low,created_at`

func scanScan(sc interface{ Scan(...any) error }) (Scan, error) {
	var r Scan
	err := sc.Scan(&r.ID, &r.Org, &r.Project, &r.Files, &r.Findings,
		&r.Critical, &r.High, &r.Medium, &r.Low, &r.CreatedAt)
	return r, err
}

// SaveScan persists a scan header and all its findings in ONE transaction, so a
// scan is never half-written (a reader either sees the whole scan or none of
// it). The findings' Org is forced to the scan's Org here, so a caller can't
// smuggle a cross-tenant row in via the finding list.
func (s *Store) SaveScan(ctx context.Context, sc Scan, fs []StoredFinding) error {
	if err := dialect.Text(sc.ID, sc.Org, sc.Project); err != nil {
		return err
	}
	for _, f := range fs {
		if err := dialect.Text(f.ID, f.RuleID, f.RuleName, f.Severity, f.Path, f.Preview, f.Fingerprint); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO scans (`+scanCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		sc.ID, sc.Org, sc.Project, sc.Files, sc.Findings,
		sc.Critical, sc.High, sc.Medium, sc.Low, sc.CreatedAt); err != nil {
		return fmt.Errorf("insert scan: %w", err)
	}
	for _, f := range fs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO findings (id,scan_id,org,rule_id,rule_name,severity,path,line,preview,fingerprint,created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			f.ID, sc.ID, sc.Org, f.RuleID, f.RuleName, f.Severity,
			f.Path, f.Line, f.Preview, f.Fingerprint, sc.CreatedAt); err != nil {
			return fmt.Errorf("insert finding: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// GetScan returns the scan header for (org,id) or errNotFound. The org
// predicate is the isolation boundary — a scan id from another tenant misses.
func (s *Store) GetScan(ctx context.Context, org, id string) (Scan, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+scanCols+` FROM scans WHERE org=$1 AND id=$2`, org, id)
	r, err := scanScan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Scan{}, errNotFound
	}
	if err != nil {
		return Scan{}, fmt.Errorf("get scan: %w", err)
	}
	return r, nil
}

// ListScans returns the tenant's scans, most recent first, capped at limit.
func (s *Store) ListScans(ctx context.Context, org string, limit int) ([]Scan, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+scanCols+` FROM scans WHERE org=$1 ORDER BY created_at DESC, id DESC LIMIT $2`, org, limit)
	if err != nil {
		return nil, fmt.Errorf("list scans: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Scan
	for rows.Next() {
		r, err := scanScan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const findingCols = `id,scan_id,org,rule_id,rule_name,severity,path,line,preview,fingerprint,created_at`

func scanFinding(sc interface{ Scan(...any) error }) (StoredFinding, error) {
	var f StoredFinding
	err := sc.Scan(&f.ID, &f.ScanID, &f.Org, &f.RuleID, &f.RuleName, &f.Severity,
		&f.Path, &f.Line, &f.Preview, &f.Fingerprint, &f.CreatedAt)
	return f, err
}

// ListFindings returns a tenant's findings. When scanID is non-empty it narrows
// to that scan; when minSeverity is non-empty it drops anything ranked below
// it. Ordered worst-first. Both filters compose in SQL so a huge history never
// materializes in memory.
func (s *Store) ListFindings(ctx context.Context, org, scanID, minSeverity string, limit int) ([]StoredFinding, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	var args []any
	bind := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	q := `SELECT ` + findingCols + ` FROM findings WHERE org=` + bind(org)
	if scanID != "" {
		q += ` AND scan_id=` + bind(scanID)
	}
	if detect.SeverityRank(minSeverity) > 0 {
		// Enumerate the severities at or above the floor — there is no rank
		// function and an IN list is index-friendly. The names are a fixed
		// enum from the engine (never user input), so quoting them into the
		// SQL is injection-safe.
		var keep []string
		for _, sev := range detect.SeveritiesAtOrAbove(minSeverity) {
			keep = append(keep, "'"+sev+"'")
		}
		q += ` AND severity IN (` + strings.Join(keep, ",") + `)`
	}
	// worst-first: map severity to rank in SQL via a CASE so ordering matches
	// the engine's in-memory sort.
	q += ` ORDER BY CASE severity
	         WHEN 'critical' THEN 4 WHEN 'high' THEN 3
	         WHEN 'medium' THEN 2 WHEN 'low' THEN 1 ELSE 0 END DESC,
	       created_at DESC, line ASC LIMIT ` + bind(limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list findings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []StoredFinding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, fmt.Errorf("scan finding: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFinding returns one finding for (org,id) or errNotFound.
func (s *Store) GetFinding(ctx context.Context, org, id string) (StoredFinding, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+findingCols+` FROM findings WHERE org=$1 AND id=$2`, org, id)
	f, err := scanFinding(row)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredFinding{}, errNotFound
	}
	if err != nil {
		return StoredFinding{}, fmt.Errorf("get finding: %w", err)
	}
	return f, nil
}
