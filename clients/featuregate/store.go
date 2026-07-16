// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package featuregate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	// The ONE Hanzo SQLite driver (registers "sqlite" under both build tags).
	// Same storage pattern as clients/authors / clients/affiliates.
	_ "github.com/hanzoai/sqlite"
)

// errNotFound is returned when a service slug (or a host) is not in the registry.
var errNotFound = errors.New("featuregate: service not found")

// Service is one hosted Hanzo service in the launch-control registry — the ONE
// source of truth for whether that service is in waitlist mode. Hosts are the
// public hostnames the service answers on (the key the guard / native middleware
// look a request up by). WaitlistMode ON = gated (only APPROVED users past the
// waitlist); OFF = open to any signed-up user. UpdatedBy records the admin who
// last flipped the mode (audit trail on the toggle itself).
type Service struct {
	Service      string   `json:"service"`
	DisplayName  string   `json:"displayName"`
	Hosts        []string `json:"hosts"`
	WaitlistMode bool     `json:"waitlistMode"`
	Description  string   `json:"description"`
	CreatedAt    int64    `json:"createdAt"`
	UpdatedAt    int64    `json:"updatedAt"`
	UpdatedBy    string   `json:"updatedBy"`
}

// SeedService is one row of the initial registry (the live hosted services). Seed
// is idempotent (INSERT OR IGNORE), so a restart NEVER overwrites an admin's live
// toggle — the seed only ever CREATES a missing row.
type SeedService struct {
	Service      string
	DisplayName  string
	Hosts        []string
	Description  string
	WaitlistMode bool // the launch default for a freshly-seeded service
}

// Store is the feature-gate registry. ONE global SQLite file holds every hosted
// service's waitlist mode + its hostnames. It is a PLATFORM-WIDE config store (not
// per-tenant): the waitlist mode of hanzo.chat is one global value, toggled from
// admin.hanzo.ai, read by every enforcement point. Two tables, normalized:
//
//	feature_services(service PK, display_name, waitlist_mode, description, …)
//	feature_hosts(host PK, service FK)   -- host → service, the hot lookup index
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
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
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS feature_services (
  service       TEXT PRIMARY KEY,
  display_name  TEXT NOT NULL DEFAULT '',
  waitlist_mode INTEGER NOT NULL DEFAULT 0,
  description   TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  updated_by    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS feature_hosts (
  host    TEXT PRIMARY KEY,
  service TEXT NOT NULL,
  FOREIGN KEY(service) REFERENCES feature_services(service) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS ix_feature_hosts_service ON feature_hosts(service);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("featuregate migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// NormalizeHost reduces a request Host to the registry key: lowercased, trimmed,
// port stripped. ONE canonicalization for the seed, the toggle, and every lookup,
// so "Hanzo.Chat:443" and "hanzo.chat" resolve to the same service.
func NormalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h
}

// Seed inserts the initial registry idempotently (INSERT OR IGNORE on both tables),
// so a boot never clobbers a live admin toggle. Returns the number of services
// created (0 on a warm store).
func (s *Store) Seed(ctx context.Context, rows []SeedService, now int64) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("seed tx: %w", err)
	}
	created := 0
	for _, r := range rows {
		svc := strings.ToLower(strings.TrimSpace(r.Service))
		if svc == "" {
			continue
		}
		mode := 0
		if r.WaitlistMode {
			mode = 1
		}
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO feature_services
			   (service, display_name, waitlist_mode, description, created_at, updated_at, updated_by)
			 VALUES (?,?,?,?,?,?,?)`,
			svc, r.DisplayName, mode, r.Description, now, now, "seed")
		if err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("seed service %q: %w", svc, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			created++
		}
		// Hosts map to the service; INSERT OR IGNORE so a host already claimed by
		// ANY service is never re-pointed by a re-seed (first claim wins).
		for _, h := range r.Hosts {
			host := NormalizeHost(h)
			if host == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO feature_hosts (host, service) VALUES (?,?)`, host, svc); err != nil {
				_ = tx.Rollback()
				return 0, fmt.Errorf("seed host %q: %w", host, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("seed commit: %w", err)
	}
	return created, nil
}

// List returns every registered service (with its hosts), sorted by slug. This is
// the admin Services board.
func (s *Store) List(ctx context.Context) ([]Service, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT service, display_name, waitlist_mode, description, created_at, updated_at, updated_by
		   FROM feature_services`)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byService := map[string]*Service{}
	out := make([]Service, 0, 16)
	for rows.Next() {
		var svc Service
		var mode int
		if err := rows.Scan(&svc.Service, &svc.DisplayName, &mode, &svc.Description,
			&svc.CreatedAt, &svc.UpdatedAt, &svc.UpdatedBy); err != nil {
			return nil, fmt.Errorf("scan service: %w", err)
		}
		svc.WaitlistMode = mode != 0
		svc.Hosts = []string{}
		out = append(out, svc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		byService[out[i].Service] = &out[i]
	}

	hostRows, err := s.db.QueryContext(ctx, `SELECT host, service FROM feature_hosts`)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	defer func() { _ = hostRows.Close() }()
	for hostRows.Next() {
		var host, svc string
		if err := hostRows.Scan(&host, &svc); err != nil {
			return nil, fmt.Errorf("scan host: %w", err)
		}
		if s := byService[svc]; s != nil {
			s.Hosts = append(s.Hosts, host)
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

// Get returns one service by slug, or errNotFound.
func (s *Store) Get(ctx context.Context, service string) (Service, error) {
	svc := strings.ToLower(strings.TrimSpace(service))
	row := s.db.QueryRowContext(ctx,
		`SELECT service, display_name, waitlist_mode, description, created_at, updated_at, updated_by
		   FROM feature_services WHERE service=?`, svc)
	var out Service
	var mode int
	err := row.Scan(&out.Service, &out.DisplayName, &mode, &out.Description,
		&out.CreatedAt, &out.UpdatedAt, &out.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Service{}, errNotFound
	}
	if err != nil {
		return Service{}, fmt.Errorf("get service: %w", err)
	}
	out.WaitlistMode = mode != 0
	out.Hosts = []string{}
	hostRows, err := s.db.QueryContext(ctx, `SELECT host FROM feature_hosts WHERE service=? ORDER BY host`, svc)
	if err != nil {
		return Service{}, fmt.Errorf("get hosts: %w", err)
	}
	defer func() { _ = hostRows.Close() }()
	for hostRows.Next() {
		var h string
		if err := hostRows.Scan(&h); err != nil {
			return Service{}, fmt.Errorf("scan host: %w", err)
		}
		out.Hosts = append(out.Hosts, h)
	}
	return out, hostRows.Err()
}

// Upsert creates or updates a service (display name, description, hosts, initial
// mode) so a new hosted service can be onboarded from admin.<brand> WITHOUT a
// redeploy. On an existing service it updates the metadata + REPLACES the host set
// but PRESERVES the live waitlist_mode (a re-register never silently re-gates an
// opened service); on a NEW service it sets the given mode. Hosts already claimed
// by ANOTHER service are skipped (first-claim wins — a host maps to one service).
func (s *Store) Upsert(ctx context.Context, in Service, by string, now int64) (Service, error) {
	svc := strings.ToLower(strings.TrimSpace(in.Service))
	if svc == "" {
		return Service{}, fmt.Errorf("featuregate: service slug required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Service{}, fmt.Errorf("upsert tx: %w", err)
	}
	mode := 0
	if in.WaitlistMode {
		mode = 1
	}
	// INSERT the new row (mode = requested); on conflict keep the LIVE mode and
	// refresh only the metadata.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO feature_services
		   (service, display_name, waitlist_mode, description, created_at, updated_at, updated_by)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(service) DO UPDATE SET
		   display_name=excluded.display_name,
		   description=excluded.description,
		   updated_at=excluded.updated_at,
		   updated_by=excluded.updated_by`,
		svc, in.DisplayName, mode, in.Description, now, now, strings.TrimSpace(by)); err != nil {
		_ = tx.Rollback()
		return Service{}, fmt.Errorf("upsert service: %w", err)
	}
	// Replace THIS service's hosts (delete its own, re-add), leaving other
	// services' host claims untouched; a host owned by another service is skipped.
	if _, err := tx.ExecContext(ctx, `DELETE FROM feature_hosts WHERE service=?`, svc); err != nil {
		_ = tx.Rollback()
		return Service{}, fmt.Errorf("clear hosts: %w", err)
	}
	for _, h := range in.Hosts {
		host := NormalizeHost(h)
		if host == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO feature_hosts (host, service) VALUES (?,?)`, host, svc); err != nil {
			_ = tx.Rollback()
			return Service{}, fmt.Errorf("add host %q: %w", host, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Service{}, fmt.Errorf("upsert commit: %w", err)
	}
	return s.Get(ctx, svc)
}

// SetMode flips one service's waitlist mode and stamps who/when. errNotFound if the
// slug is unknown. Returns the updated service.
func (s *Store) SetMode(ctx context.Context, service string, mode bool, by string, now int64) (Service, error) {
	svc := strings.ToLower(strings.TrimSpace(service))
	m := 0
	if mode {
		m = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE feature_services SET waitlist_mode=?, updated_at=?, updated_by=? WHERE service=?`,
		m, now, strings.TrimSpace(by), svc)
	if err != nil {
		return Service{}, fmt.Errorf("set mode: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Service{}, errNotFound
	}
	return s.Get(ctx, svc)
}

// ModeForHost is the HOT lookup the enforcement points call once per request: it
// resolves a request host to its service and that service's waitlist mode. `known`
// is false when the host is not in the registry (an UN-GOVERNED host — the native
// middleware passes it through; the guard, attached only to gated hosts, fails
// safe to gated). host is normalized here so the caller passes the raw Host.
func (s *Store) ModeForHost(ctx context.Context, host string) (mode bool, service string, known bool, err error) {
	h := NormalizeHost(host)
	if h == "" {
		return false, "", false, nil
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT s.service, s.waitlist_mode
		   FROM feature_hosts h JOIN feature_services s ON s.service = h.service
		  WHERE h.host = ?`, h)
	var svc string
	var m int
	scanErr := row.Scan(&svc, &m)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return false, "", false, nil
	}
	if scanErr != nil {
		return false, "", false, fmt.Errorf("mode for host: %w", scanErr)
	}
	return m != 0, svc, true, nil
}
