package featuregate

// The launch-registry — the host→service map + service display metadata. It is
// deliberately MODE-FREE: a service's waitlist mode is NOT a column here, it is the
// platform switch waitlist.<svc> evaluated through the ONE flag engine (clients/flags,
// composed one-way from waitlist.go). This store answers only "which service owns this
// host, and what is its display metadata" — the config the decide needs, with the
// decision itself owned by the flag engine.
//
// It rides the SAME per-(org,project) OrgDB machinery as the flag defs (opened via
// cloud.OrgStore, encrypted at rest via cek); the registry is PLATFORM-global, so it
// lives in the reserved platform/platform tenant — one waitlist.db for the deployment.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrServiceNotFound is returned when a service slug is not in the registry.
var ErrServiceNotFound = errors.New("featuregate: waitlist service not found")

// ServiceRow is one hosted service in the registry (host→service + metadata). The
// waitlist MODE is intentionally absent — it is the platform switch waitlist.<svc>,
// read through the engine; ListWaitlistServices composes the two into a ServiceView.
type ServiceRow struct {
	Service     string   `json:"service"`
	DisplayName string   `json:"displayName"`
	Description string   `json:"description"`
	Hosts       []string `json:"hosts"`
	CreatedAt   int64    `json:"createdAt"`
	UpdatedAt   int64    `json:"updatedAt"`
	UpdatedBy   string   `json:"updatedBy"`
}

// waitlistStore is the registry over one OrgDB handle. Two tables, normalized:
//
//	wl_services(service PK, display_name, description, …)
//	wl_hosts(host PK, service FK)   -- host → service, the hot lookup index
type waitlistStore struct {
	db *sql.DB
}

// openWaitlistStore migrates the registry schema over an already-opened (pragma'd,
// cek-wrapped) OrgDB handle — the same open contract as flags' openStore for flag defs.
func openWaitlistStore(db *sql.DB) (*waitlistStore, error) {
	const schema = `
CREATE TABLE IF NOT EXISTS wl_services (
    service      TEXT PRIMARY KEY,
    display_name TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    updated_by   TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS wl_hosts (
    host    TEXT PRIMARY KEY,
    service TEXT NOT NULL,
    FOREIGN KEY(service) REFERENCES wl_services(service) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS ix_wl_hosts_service ON wl_hosts(service);
`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("featuregate: waitlist migrate: %w", err)
	}
	return &waitlistStore{db: db}, nil
}

func (s *waitlistStore) Close() error { return s.db.Close() }

// NormalizeHost reduces a request Host to the registry key: lowercased, trimmed,
// port stripped. ONE canonicalization for the seed, onboard, and every lookup, so
// "Hanzo.Chat:443" and "hanzo.chat" resolve to the same service.
func NormalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h
}

// Seed inserts the initial registry idempotently (INSERT OR IGNORE on both tables),
// so a boot never clobbers a runtime onboard. Returns the number of services created
// (0 on a warm store).
func (s *waitlistStore) Seed(ctx context.Context, rows []SeedService, now int64) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("waitlist seed tx: %w", err)
	}
	created := 0
	for _, r := range rows {
		svc := strings.ToLower(strings.TrimSpace(r.Service))
		if svc == "" {
			continue
		}
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO wl_services (service, display_name, description, created_at, updated_at, updated_by)
			 VALUES (?,?,?,?,?,?)`,
			svc, r.DisplayName, r.Description, now, now, "seed")
		if err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("waitlist seed service %q: %w", svc, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			created++
		}
		for _, h := range r.Hosts {
			host := NormalizeHost(h)
			if host == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO wl_hosts (host, service) VALUES (?,?)`, host, svc); err != nil {
				_ = tx.Rollback()
				return 0, fmt.Errorf("waitlist seed host %q: %w", host, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("waitlist seed commit: %w", err)
	}
	return created, nil
}

// List returns every registered service (with its hosts), sorted by slug.
func (s *waitlistStore) List(ctx context.Context) ([]ServiceRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT service, display_name, description, created_at, updated_at, updated_by FROM wl_services`)
	if err != nil {
		return nil, fmt.Errorf("list waitlist services: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byService := map[string]*ServiceRow{}
	out := make([]ServiceRow, 0, 16)
	for rows.Next() {
		var r ServiceRow
		if err := rows.Scan(&r.Service, &r.DisplayName, &r.Description, &r.CreatedAt, &r.UpdatedAt, &r.UpdatedBy); err != nil {
			return nil, fmt.Errorf("scan waitlist service: %w", err)
		}
		r.Hosts = []string{}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		byService[out[i].Service] = &out[i]
	}
	hostRows, err := s.db.QueryContext(ctx, `SELECT host, service FROM wl_hosts`)
	if err != nil {
		return nil, fmt.Errorf("list waitlist hosts: %w", err)
	}
	defer func() { _ = hostRows.Close() }()
	for hostRows.Next() {
		var host, svc string
		if err := hostRows.Scan(&host, &svc); err != nil {
			return nil, fmt.Errorf("scan waitlist host: %w", err)
		}
		if r := byService[svc]; r != nil {
			r.Hosts = append(r.Hosts, host)
		}
	}
	if err := hostRows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		sort.Strings(out[i].Hosts)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out, nil
}

// Get returns one service by slug, or ErrServiceNotFound.
func (s *waitlistStore) Get(ctx context.Context, service string) (ServiceRow, error) {
	svc := strings.ToLower(strings.TrimSpace(service))
	row := s.db.QueryRowContext(ctx,
		`SELECT service, display_name, description, created_at, updated_at, updated_by FROM wl_services WHERE service=?`, svc)
	var out ServiceRow
	err := row.Scan(&out.Service, &out.DisplayName, &out.Description, &out.CreatedAt, &out.UpdatedAt, &out.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceRow{}, ErrServiceNotFound
	}
	if err != nil {
		return ServiceRow{}, fmt.Errorf("get waitlist service: %w", err)
	}
	out.Hosts = []string{}
	hostRows, err := s.db.QueryContext(ctx, `SELECT host FROM wl_hosts WHERE service=? ORDER BY host`, svc)
	if err != nil {
		return ServiceRow{}, fmt.Errorf("get waitlist hosts: %w", err)
	}
	defer func() { _ = hostRows.Close() }()
	for hostRows.Next() {
		var h string
		if err := hostRows.Scan(&h); err != nil {
			return ServiceRow{}, fmt.Errorf("scan waitlist host: %w", err)
		}
		out.Hosts = append(out.Hosts, h)
	}
	return out, hostRows.Err()
}

// Upsert creates or updates a service's metadata + REPLACES its host set (a host
// already claimed by ANOTHER service is skipped — first claim wins). It never
// touches the mode: the mode is the waitlist.<svc> switch, flipped through
// SetWaitlistMode. Returns the stored row.
func (s *waitlistStore) Upsert(ctx context.Context, in ServiceRow, by string, now int64) (ServiceRow, error) {
	svc := strings.ToLower(strings.TrimSpace(in.Service))
	if svc == "" {
		return ServiceRow{}, fmt.Errorf("featuregate: waitlist service slug required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ServiceRow{}, fmt.Errorf("waitlist upsert tx: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wl_services (service, display_name, description, created_at, updated_at, updated_by)
		 VALUES (?,?,?,?,?,?)
		 ON CONFLICT(service) DO UPDATE SET
		   display_name=excluded.display_name,
		   description=excluded.description,
		   updated_at=excluded.updated_at,
		   updated_by=excluded.updated_by`,
		svc, in.DisplayName, in.Description, now, now, strings.TrimSpace(by)); err != nil {
		_ = tx.Rollback()
		return ServiceRow{}, fmt.Errorf("upsert waitlist service: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wl_hosts WHERE service=?`, svc); err != nil {
		_ = tx.Rollback()
		return ServiceRow{}, fmt.Errorf("clear waitlist hosts: %w", err)
	}
	for _, h := range in.Hosts {
		host := NormalizeHost(h)
		if host == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO wl_hosts (host, service) VALUES (?,?)`, host, svc); err != nil {
			_ = tx.Rollback()
			return ServiceRow{}, fmt.Errorf("add waitlist host %q: %w", host, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ServiceRow{}, fmt.Errorf("waitlist upsert commit: %w", err)
	}
	return s.Get(ctx, svc)
}

// ServiceForHost is the HOT lookup the decide calls once per request: it resolves a
// request host to its owning service. known is false for an un-governed host (the
// caller treats it as pass-through). host is normalized here so the caller passes
// the raw Host.
func (s *waitlistStore) ServiceForHost(ctx context.Context, host string) (service string, known bool, err error) {
	h := NormalizeHost(host)
	if h == "" {
		return "", false, nil
	}
	var svc string
	scanErr := s.db.QueryRowContext(ctx, `SELECT service FROM wl_hosts WHERE host=?`, h).Scan(&svc)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return "", false, nil
	}
	if scanErr != nil {
		return "", false, fmt.Errorf("waitlist service for host: %w", scanErr)
	}
	return svc, true, nil
}
