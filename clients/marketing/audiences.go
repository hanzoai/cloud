// Copyright © 2026 Hanzo AI. MIT License.

package marketing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// audiences.go is the cohort engine. An Audience is a saved filter over the
// org's product analytics — "distinct users who did EVENT within the last N
// days" — evaluated live against the shared hanzo.events warehouse through the
// ai/object datastore seam (the SAME lens clients/analytics reads).
//
// TENANCY & SAFETY. Every query leads with `tenant_id = ?` (the IAM org slug,
// the events table's canonical org column) as a BOUND arg, and the event name +
// time bound are bound too — never string-interpolated — so a cohort can neither
// read another org's events nor inject SQL. When the warehouse is not wired
// (DatastoreEnabled == false) the preview is honest-empty (Available=false),
// never a fabricated number.
//
// SCOPE. Cohorts resolve distinct_ids and counts. Product analytics scrubs PII,
// so distinct_ids are cohort identifiers, not deliverable addresses — enrolling
// a cohort into a drip requires resolving addresses through the CRM/identity, an
// honest boundary the fold does not paper over.

const (
	audDefaultWindowDays = 30
	audMaxWindowDays     = 3650
	audSampleLimit       = 1000
)

// eventsTable is the shared web/commerce/UI wide event table (org column
// tenant_id), honest-empty until the collector emits.
const eventsTable = "hanzo.events"

// Audience is a saved cohort filter.
type Audience struct {
	ID         string `json:"id"`
	Org        string `json:"-"`
	Name       string `json:"name"`
	Event      string `json:"event"`
	WindowDays int    `json:"windowDays"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
}

// AudiencePreview is a live cohort evaluation.
type AudiencePreview struct {
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
	Count     int64    `json:"count"`
	Sample    []string `json:"sample"`
	Source    string   `json:"source"`
}

func (s *Store) migrateAudiences() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS marketing_audiences (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  name         TEXT NOT NULL,
  event        TEXT NOT NULL,
  window_days  INTEGER NOT NULL DEFAULT 30,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_marketing_audiences_org ON marketing_audiences(org, updated_at);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("marketing migrate audiences: %w", err)
	}
	return nil
}

func (s *Store) CreateAudience(ctx context.Context, a Audience) (Audience, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO marketing_audiences (id,org,name,event,window_days,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`,
		a.ID, a.Org, a.Name, a.Event, a.WindowDays, a.CreatedAt, a.UpdatedAt); err != nil {
		return Audience{}, fmt.Errorf("insert audience: %w", err)
	}
	return a, nil
}

func (s *Store) GetAudience(ctx context.Context, org, id string) (Audience, error) {
	var a Audience
	err := s.db.QueryRowContext(ctx,
		`SELECT id,org,name,event,window_days,created_at,updated_at FROM marketing_audiences WHERE org=? AND id=?`, org, id).
		Scan(&a.ID, &a.Org, &a.Name, &a.Event, &a.WindowDays, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Audience{}, errNotFound
	}
	if err != nil {
		return Audience{}, fmt.Errorf("get audience: %w", err)
	}
	return a, nil
}

func (s *Store) ListAudiences(ctx context.Context, org string, limit int) ([]Audience, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,name,event,window_days,created_at,updated_at FROM marketing_audiences WHERE org=? ORDER BY updated_at DESC LIMIT ?`, org, limit)
	if err != nil {
		return nil, fmt.Errorf("list audiences: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Audience, 0, 16)
	for rows.Next() {
		var a Audience
		if err := rows.Scan(&a.ID, &a.Org, &a.Name, &a.Event, &a.WindowDays, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAudience(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM marketing_audiences WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete audience: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// evalAudience resolves a cohort live against hanzo.events. It is honest-empty
// (Available=false) when the warehouse is not wired, and otherwise returns the
// distinct-user count plus a bounded sample of distinct_ids. tenant_id, event
// and the time bound are ALL bound args — one tenancy invariant, no injection.
func evalAudience(ctx context.Context, org string, a Audience) AudiencePreview {
	out := AudiencePreview{Source: eventsTable, Sample: []string{}}
	if !aiobject.DatastoreEnabled() {
		out.Reason = "analytics warehouse not configured"
		return out
	}
	sinceLit := time.Now().UTC().AddDate(0, 0, -a.WindowDays).Format("2006-01-02 15:04:05")

	countRows, err := aiobject.DatastoreQuery(ctx,
		"SELECT uniqExact(distinct_id) AS n FROM "+eventsTable+" WHERE tenant_id = ? AND event = ? AND timestamp >= ?",
		org, a.Event, sinceLit)
	if err != nil {
		out.Reason = "warehouse query failed"
		return out
	}
	if len(countRows) > 0 {
		out.Count = toInt64(countRows[0]["n"])
	}

	memberRows, err := aiobject.DatastoreQuery(ctx,
		"SELECT DISTINCT distinct_id FROM "+eventsTable+" WHERE tenant_id = ? AND event = ? AND timestamp >= ? LIMIT ?",
		org, a.Event, sinceLit, audSampleLimit)
	if err != nil {
		out.Reason = "warehouse query failed"
		return out
	}
	for _, r := range memberRows {
		if id := toString(r["distinct_id"]); id != "" {
			out.Sample = append(out.Sample, id)
		}
	}
	out.Available = true
	return out
}

// ---- handlers ----

func createAudience(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	var body Audience
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := clip(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	event := clip(body.Event)
	if event == "" {
		return zip.ErrBadRequest("event is required")
	}
	window := body.WindowDays
	if window <= 0 {
		window = audDefaultWindowDays
	}
	if window > audMaxWindowDays {
		window = audMaxWindowDays
	}
	id, err := genID("aud")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	a, err := s.State.store.CreateAudience(c.Context(), Audience{
		ID: id, Org: org, Name: name, Event: event, WindowDays: window, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return mapErr(err, "")
	}
	return c.JSON(http.StatusCreated, a)
}

func listAudiences(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	rows, err := s.State.store.ListAudiences(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func getAudience(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	a, err := s.State.store.GetAudience(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "audience not found")
	}
	return c.JSON(http.StatusOK, a)
}

func deleteAudience(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	deleted, err := s.State.store.DeleteAudience(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("audience not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// previewAudience evaluates the cohort live and returns count + sample.
func previewAudience(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	a, err := s.State.store.GetAudience(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "audience not found")
	}
	return c.JSON(http.StatusOK, evalAudience(c.Context(), org, a))
}
