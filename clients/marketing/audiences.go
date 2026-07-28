// Copyright © 2026 Hanzo AI. MIT License.

package marketing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/datastore"
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
// so a distinct_id is a cohort IDENTIFIER, never a deliverable address; addresses
// come from the identity store (roster.go), which is what resolveAudience joins
// the two through. That join is the whole reason an audience can be mailed at all.
//
// AN AUDIENCE WITH NO EVENT IS EVERY CUSTOMER. The event filter is optional: an
// audience that names none is the org's whole IAM roster — "announce to every
// customer" — and needs no warehouse at all, so it resolves even before the
// analytics collector is wired.

const (
	audDefaultWindowDays = 30
	audMaxWindowDays     = 3650
	audSampleLimit       = 1000
	// audResolveLimit bounds the cohort read on the SEND path. It is far larger
	// than audSampleLimit because that one feeds a preview list while this one
	// decides who actually receives the mail — under-reading here would silently
	// drop customers from an announcement.
	audResolveLimit = 100000
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

// AudiencePreview is a live audience evaluation: how big the cohort is, and — the
// question that decides whether a send is worth making — how many real customers
// it actually reaches.
type AudiencePreview struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Count is the cohort size: distinct warehouse identifiers for an event
	// audience, mailable customers for an event-less (whole-org) one.
	Count int64 `json:"count"`
	// Deliverable is how many de-duplicated addresses a send would reach, and
	// Unmatched how many cohort identifiers named no customer. Unmatched is
	// reported rather than hidden: it is the honest explanation for a cohort of
	// 500 that mails 3.
	Deliverable int      `json:"deliverable"`
	Unmatched   int      `json:"unmatched"`
	Sample      []string `json:"sample"` // cohort identifiers, never addresses
	Source      string   `json:"source"`
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

// cohortIDs is the ONE cohort query: the distinct_ids the audience's event
// selected inside its window, bounded by limit. tenant_id, event and the time
// bound are ALL bound args — one tenancy invariant, no injection.
func cohortIDs(ctx context.Context, org string, a Audience, limit int) ([]string, error) {
	if !datastore.Ready() {
		return nil, errWarehouse
	}
	sinceLit := time.Now().UTC().AddDate(0, 0, -a.WindowDays).Format("2006-01-02 15:04:05")
	rows, err := datastore.Query(ctx,
		"SELECT DISTINCT distinct_id FROM "+eventsTable+" WHERE tenant_id = ? AND event = ? AND timestamp >= ? LIMIT ?",
		org, a.Event, sinceLit, limit)
	if err != nil {
		return nil, fmt.Errorf("warehouse query: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if id := toString(r["distinct_id"]); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// reach is an audience resolved to exactly who a send would go to, plus the facts
// that explain the number.
type reach struct {
	addresses []string // de-duplicated, normalized, deliverable
	cohort    []string // the warehouse identifiers the event selected (none for a roster audience)
	unmatched int      // cohort identifiers naming no customer of this org
}

// resolveAudience turns an audience into deliverable addresses by joining the
// cohort to the org's IAM roster. It is the ONE resolution path — the preview and
// the enrollment fan-out both call it, so what an operator is shown is exactly
// what would be mailed.
//
// An audience with no event is the whole roster: every mailable customer, no
// warehouse involved. With an event, only the roster users the cohort actually
// names are kept; an identifier that matches no customer is counted, never
// invented into an address.
func resolveAudience(ctx context.Context, org string, a Audience) (reach, error) {
	roster, err := rosterFn(org)
	if err != nil {
		return reach{}, err
	}
	if a.Event == "" {
		return reach{addresses: addresses(roster)}, nil
	}
	ids, err := cohortIDs(ctx, org, a, audResolveLimit)
	if err != nil {
		return reach{}, err
	}
	return matchCohort(roster, ids), nil
}

// evalAudience presents a resolution. It is honest-empty (Available=false, with
// the reason) when the roster or the warehouse cannot be read, never a fabricated
// number.
func evalAudience(ctx context.Context, org string, a Audience) AudiencePreview {
	out := AudiencePreview{Source: eventsTable, Sample: []string{}}
	if a.Event == "" {
		out.Source = "iam:" + org
	}
	r, err := resolveAudience(ctx, org, a)
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	out.Available = true
	out.Deliverable = len(r.addresses)
	out.Unmatched = r.unmatched
	out.Count = int64(len(r.cohort))
	if a.Event == "" {
		out.Count = int64(len(r.addresses))
	}
	if len(r.cohort) > audSampleLimit {
		r.cohort = r.cohort[:audSampleLimit]
	}
	out.Sample = append(out.Sample, r.cohort...)
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
	// No event means no filter: the audience is every mailable customer in the org.
	event := clip(body.Event)
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
