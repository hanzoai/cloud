// Copyright © 2026 Hanzo AI. MIT License.

package marketing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/zap-proto/zip"
)

// calendar.go is the content calendar: scheduled social posts stored as
// documents, published by a task-executed hook when their time arrives. It rides
// the SAME durable sweep as the drip engine (drip.go) — one clock, two due
// queues — so a scheduled post survives restarts and publishes at most once (the
// publish is CLAIMED by a scheduled→publishing transition before it fires).
//
// PUBLISH SEAM — HONEST. A post is pushed through publisherFor(channel). Today no
// in-process social-publish connector is wired: clients/social's provider push is
// fail-closed (no deployment carries the OAuth-app credentials) and the
// clients/automations connector registry is package-private. So every social
// channel returns an honest 501 (errNotImplemented) and the post is recorded
// failed with the exact reason — never a faked "published". publisherFor is the
// single extension point: registering a real connector there lights the channel
// up with no other change.

// calendarChannels is the set of publishable target networks. It mirrors the
// clients/social provider vocabulary (the platforms a post can target).
var calendarChannels = map[string]bool{
	"x": true, "facebook": true, "instagram": true, "linkedin": true,
	"tiktok": true, "youtube": true, "threads": true,
}

// calendar post lifecycle.
const (
	calDraft     = "draft"
	calScheduled = "scheduled"
	calPublished = "published"
	calFailed    = "failed"
	calCanceled  = "canceled"
)

// errNotImplemented marks a channel that has no wired publish connector yet. It
// surfaces as HTTP 501 on the sync endpoint and as a recorded failure in the
// sweep.
var errNotImplemented = errors.New("marketing: publish connector not implemented")

// CalendarPost is a scheduled content document. It is also the INPUT of create
// and update: the wire shape is the same record either way. On create the server
// assigns ID/CreatedAt/UpdatedAt; on update ID comes from the path. Status,
// PublishedAt and Error are always server-owned — a caller cannot declare a post
// published.
type CalendarPost struct {
	// ID is the server-assigned post id ("cal_" + 128 random bits).
	ID  string `json:"id"`
	Org string `json:"-"`
	// Title is the post's internal label, capped at 1024 bytes.
	Title string `json:"title"`
	// Body is the post text. Required.
	Body string `json:"body"`
	// Channel is the target network: x, facebook, instagram, linkedin, tiktok,
	// youtube or threads. Required — a post must name where it goes.
	Channel string `json:"channel"`
	// ScheduledAt is the unix publish time; 0 leaves the post a draft, and any
	// value makes it "scheduled" for the durable sweep to pick up.
	ScheduledAt int64 `json:"scheduledAt"`
	// Status is draft, scheduled, published, failed or canceled. Server-owned.
	Status string `json:"status"`
	// PublishedAt is when the publish succeeded; 0 until it does.
	PublishedAt int64 `json:"publishedAt"`
	// Error is the exact reason the last publish attempt failed — the honest
	// record behind a "failed" status, never a faked success.
	Error string `json:"error,omitempty"`
	// CreatedAt and UpdatedAt are unix seconds, both server-assigned.
	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

func (s *Store) migrateCalendar() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS marketing_calendar (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  title        TEXT NOT NULL DEFAULT '',
  body         TEXT NOT NULL DEFAULT '',
  channel      TEXT NOT NULL,
  scheduled_at INTEGER NOT NULL DEFAULT 0,
  status       TEXT NOT NULL DEFAULT 'draft',
  published_at INTEGER NOT NULL DEFAULT 0,
  error        TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_marketing_calendar_org ON marketing_calendar(org, scheduled_at);
CREATE INDEX IF NOT EXISTS ix_marketing_calendar_due ON marketing_calendar(status, scheduled_at);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("marketing migrate calendar: %w", err)
	}
	return nil
}

const calendarCols = `id,org,title,body,channel,scheduled_at,status,published_at,error,created_at,updated_at`

func scanCalendar(sc interface{ Scan(...any) error }) (CalendarPost, error) {
	var p CalendarPost
	err := sc.Scan(&p.ID, &p.Org, &p.Title, &p.Body, &p.Channel, &p.ScheduledAt, &p.Status,
		&p.PublishedAt, &p.Error, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (s *Store) CreateCalendarPost(ctx context.Context, p CalendarPost) (CalendarPost, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO marketing_calendar (`+calendarCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Org, p.Title, p.Body, p.Channel, p.ScheduledAt, p.Status, p.PublishedAt, p.Error, p.CreatedAt, p.UpdatedAt); err != nil {
		return CalendarPost{}, fmt.Errorf("insert calendar post: %w", err)
	}
	return p, nil
}

func (s *Store) GetCalendarPost(ctx context.Context, org, id string) (CalendarPost, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+calendarCols+` FROM marketing_calendar WHERE org=? AND id=?`, org, id)
	p, err := scanCalendar(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CalendarPost{}, errNotFound
	}
	if err != nil {
		return CalendarPost{}, fmt.Errorf("get calendar post: %w", err)
	}
	return p, nil
}

func (s *Store) ListCalendarPosts(ctx context.Context, org, status string, limit int) ([]CalendarPost, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if status == "" {
		rows, err = s.db.QueryContext(ctx, `SELECT `+calendarCols+` FROM marketing_calendar WHERE org=? ORDER BY scheduled_at DESC LIMIT ?`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT `+calendarCols+` FROM marketing_calendar WHERE org=? AND status=? ORDER BY scheduled_at DESC LIMIT ?`, org, status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list calendar posts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]CalendarPost, 0, 16)
	for rows.Next() {
		p, err := scanCalendar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdateCalendarPost(ctx context.Context, p CalendarPost) (CalendarPost, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE marketing_calendar SET title=?,body=?,channel=?,scheduled_at=?,status=?,updated_at=? WHERE org=? AND id=?`,
		p.Title, p.Body, p.Channel, p.ScheduledAt, p.Status, p.UpdatedAt, p.Org, p.ID)
	if err != nil {
		return CalendarPost{}, fmt.Errorf("update calendar post: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return CalendarPost{}, errNotFound
	}
	return s.GetCalendarPost(ctx, p.Org, p.ID)
}

func (s *Store) DeleteCalendarPost(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM marketing_calendar WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete calendar post: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DueCalendarPosts returns scheduled posts whose time has arrived, across all
// orgs (each carries its own org; publish is org-scoped).
func (s *Store) DueCalendarPosts(ctx context.Context, now int64, limit int) ([]CalendarPost, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+calendarCols+` FROM marketing_calendar WHERE status=? AND scheduled_at>0 AND scheduled_at<=? ORDER BY scheduled_at LIMIT ?`,
		calScheduled, now, limit)
	if err != nil {
		return nil, fmt.Errorf("due calendar posts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]CalendarPost, 0, 16)
	for rows.Next() {
		p, err := scanCalendar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClaimCalendarPost transitions scheduled → publishing for exactly one publisher.
// Only the claimer (rows==1) publishes, so overlapping sweeps never double-post.
func (s *Store) ClaimCalendarPost(ctx context.Context, org, id string, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE marketing_calendar SET status='publishing', updated_at=? WHERE org=? AND id=? AND status=?`,
		now, org, id, calScheduled)
	if err != nil {
		return false, fmt.Errorf("claim calendar post: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) MarkCalendarPublished(ctx context.Context, org, id string, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE marketing_calendar SET status=?, published_at=?, error='', updated_at=? WHERE org=? AND id=?`,
		calPublished, now, now, org, id)
	if err != nil {
		return fmt.Errorf("mark published: %w", err)
	}
	return nil
}

func (s *Store) MarkCalendarFailed(ctx context.Context, org, id, msg string, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE marketing_calendar SET status=?, error=?, updated_at=? WHERE org=? AND id=?`,
		calFailed, msg, now, org, id)
	if err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	return nil
}

// ---- publish seam ----

// publisher pushes one post to its channel. Registering a real connector here is
// the single change that lights up a channel.
type publisher func(ctx context.Context, s *cloud.Service[state], p CalendarPost) error

// publisherFor resolves the connector for a channel. Today none is wired (see the
// package doc), so it always reports ok=false and callers return an honest 501.
func publisherFor(channel string) (publisher, bool) {
	return nil, false
}

// publishPost pushes a post through its channel connector, or returns
// errNotImplemented (→ 501) naming the seam a real connector would plug into.
func publishPost(ctx context.Context, s *cloud.Service[state], p CalendarPost) error {
	pub, ok := publisherFor(p.Channel)
	if !ok {
		return fmt.Errorf("%w: no publish connector for channel %q — connect an OAuth app via /v1/social/providers or wire an automations connector",
			errNotImplemented, p.Channel)
	}
	return pub(ctx, s, p)
}

// processDueCalendar publishes every due scheduled post (claimed once). A channel
// with no connector records an honest failure and drops out of the due set, so
// the sweep never loops on it.
func processDueCalendar(ctx context.Context, s *cloud.Service[state], now int64, limit int) (int, error) {
	due, err := s.State.store.DueCalendarPosts(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, p := range due {
		claimed, err := s.State.store.ClaimCalendarPost(ctx, p.Org, p.ID, now)
		if err != nil {
			s.Log.Warn("calendar: claim", "post", p.ID, "err", err)
			continue
		}
		if !claimed {
			continue
		}
		if perr := publishPost(ctx, s, p); perr != nil {
			_ = s.State.store.MarkCalendarFailed(ctx, p.Org, p.ID, perr.Error(), now)
			continue
		}
		_ = s.State.store.MarkCalendarPublished(ctx, p.Org, p.ID, now)
		published++
	}
	return published, nil
}

// ---- handlers ----

// normCalendarChannel validates a target network (no default — a post must name
// its channel).
func normCalendarChannel(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" || !calendarChannels[v] {
		return v, false
	}
	return v, true
}

// PostRef addresses one calendar post.
type PostRef struct {
	// ID is the post id from the path, as returned by create.
	ID string `json:"id"`
}

// PostQuery filters the calendar list.
type PostQuery struct {
	// Status keeps only posts in that state (draft, scheduled, published,
	// failed, canceled). Empty means every post.
	Status string `json:"status"`
	// Limit caps the rows returned; 0 means 200 and nothing above 1000 is honoured.
	Limit int `json:"limit"`
}

// PostList is a page of calendar posts, soonest scheduled first.
type PostList struct {
	Data []CalendarPost `json:"data"`
}

// createCalendarPost adds a post to the content calendar. Channel and body are
// required. A scheduledAt in the future makes the post "scheduled" and the
// durable sweep publishes it when it comes due — claimed once, so a post
// publishes at most once; without one it stays a draft.
//
// Example: {"title": "Launch day", "body": "Hanzo Cloud is live.", "channel": "x", "scheduledAt": 1780000000}
func (o ops) createCalendarPost(ctx context.Context, in *CalendarPost) (*CalendarPost, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	channel, okCh := normCalendarChannel(in.Channel)
	if !okCh {
		return nil, zip.ErrBadRequest("channel must be one of x, facebook, instagram, linkedin, tiktok, youtube, threads")
	}
	if clip(in.Body) == "" {
		return nil, zip.ErrBadRequest("body is required")
	}
	status := calDraft
	if in.ScheduledAt > 0 {
		status = calScheduled
	}
	id := mint.ID("cal")
	now := time.Now().Unix()
	p, err := o.s.State.store.CreateCalendarPost(ctx, CalendarPost{
		ID: id, Org: org, Title: clip(in.Title), Body: in.Body, Channel: channel,
		ScheduledAt: in.ScheduledAt, Status: status, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return nil, mapErr(err, "")
	}
	cloud.Created(ctx)
	return &p, nil
}

// listCalendarPosts returns the org's calendar, soonest scheduled first,
// optionally narrowed to one status.
//
// Example: {"status": "scheduled", "limit": 50}
func (o ops) listCalendarPosts(ctx context.Context, in *PostQuery) (*PostList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListCalendarPosts(ctx, org, strings.ToLower(strings.TrimSpace(in.Status)), limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &PostList{Data: rows}, nil
}

// getCalendarPost returns one of the caller org's posts, including the exact
// error behind a failed publish. A post belonging to another org reads as not
// found.
//
// Example: {"id": "cal_1d7f3b9e5a2c8046f1b3d5a7c9e02468"}
func (o ops) getCalendarPost(ctx context.Context, in *PostRef) (*CalendarPost, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	p, err := o.s.State.store.GetCalendarPost(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "post not found")
	}
	return &p, nil
}

// updateCalendarPost replaces a post's editable fields. It is a full write, not
// a patch, and it RESETS the lifecycle from the schedule: a scheduledAt makes
// the post "scheduled" again and none makes it a draft — so editing a failed
// post requeues it rather than leaving it stuck.
//
// Example: {"title": "Launch day", "body": "Hanzo Cloud is live — try it free.", "channel": "x", "scheduledAt": 1780003600}
func (o ops) updateCalendarPost(ctx context.Context, in *CalendarPost) (*CalendarPost, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	channel, okCh := normCalendarChannel(in.Channel)
	if !okCh {
		return nil, zip.ErrBadRequest("unknown channel")
	}
	status := calDraft
	if in.ScheduledAt > 0 {
		status = calScheduled
	}
	p, err := o.s.State.store.UpdateCalendarPost(ctx, CalendarPost{
		ID: strings.TrimSpace(in.ID), Org: org, Title: clip(in.Title), Body: in.Body, Channel: channel,
		ScheduledAt: in.ScheduledAt, Status: status, UpdatedAt: time.Now().Unix(),
	})
	if err != nil {
		return nil, mapErr(err, "post not found")
	}
	return &p, nil
}

// deleteCalendarPost removes one of the caller org's posts and answers 204. A
// post already published is deleted from the calendar only — nothing is
// retracted from the network it went out on.
//
// Example: {"id": "cal_1d7f3b9e5a2c8046f1b3d5a7c9e02468"}
func (o ops) deleteCalendarPost(ctx context.Context, in *PostRef) (*struct{}, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := o.s.State.store.DeleteCalendarPost(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("post not found")
	}
	return nil, nil
}

// publishCalendarPost publishes a post NOW, synchronously, whatever its
// schedule. No social connector is wired today, so every channel answers an
// honest 501 naming the seam a real one would plug into, and the post is
// recorded failed with that exact reason — never a faked "published".
//
// Example: {"id": "cal_1d7f3b9e5a2c8046f1b3d5a7c9e02468"}
func (o ops) publishCalendarPost(ctx context.Context, in *PostRef) (*CalendarPost, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	p, err := o.s.State.store.GetCalendarPost(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "post not found")
	}
	now := time.Now().Unix()
	if perr := publishPost(ctx, o.s, p); perr != nil {
		_ = o.s.State.store.MarkCalendarFailed(ctx, org, p.ID, perr.Error(), now)
		if errors.Is(perr, errNotImplemented) {
			return nil, zip.Errorf(http.StatusNotImplemented, "%v", perr)
		}
		return nil, zip.Errorf(http.StatusBadGateway, "publish: %v", perr)
	}
	if err := o.s.State.store.MarkCalendarPublished(ctx, org, p.ID, now); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "publish: %v", err)
	}
	p, _ = o.s.State.store.GetCalendarPost(ctx, org, p.ID)
	return &p, nil
}
