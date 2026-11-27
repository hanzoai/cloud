// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

// One person's history, for the platform operator.
//
//	GET /v1/admin/event/:org/:name                     their events, newest first
//	GET /v1/admin/event/:org/:name/sessions            their visits, each with its recording
//	GET /v1/admin/event/:org/:name/sessions/:session   one visit: its events and its recording
//
// The address is /v1/admin/<app>, the one place HIP-0139 lets an app answer outside
// /v1/<app> (apps/team/admin.go is the other). The rows are this app's, so they are
// read here rather than assembled by apps/admin over the plane.
//
// WHO. The path names an IAM account, <owner>/<name> — the admin console's own
// /users/:org/:name. IAM gives its id, the token's sub. The plane files a signed-in
// person's events under that sub or under <owner>/<name> (both spellings are live),
// and a visitor's events from before they signed in under an anonymous id the
// identify row links to them. All of them are matched. An email never reaches the
// plane — the scrubber drops it at ingest — so an email is resolved to an account
// through the directory (/v1/admin/users?q=) before it is asked about here.
//
// ACROSS TENANTS. An event lands in the org that admitted it: the site's org for a
// beacon on hanzo.ai, the person's own org for a console call. So the read spans
// every tenant, each row names the one it was filed under, and tenant narrows it.
//
// SuperAdmin only, on the one predicate (principal.Super, owner == "admin"). It is
// asked here rather than through apps/admin/core.Admit because that kernel links
// the admin board's paid vendors into whatever imports it, and this app is free.
// The boundary audits every request under /v1/admin/, so each read is on the trail
// with no second writer.

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"io"
	"net/http"
	"net/url"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/types"
	"github.com/zap-proto/zip"
)

// adminPrefix is the address. A constant because cmd/zipdoc resolves a typed op's
// path from the constant value of its Group argument.
const adminPrefix = "/v1/admin/event"

const (
	// personRange is the window a history reads when the caller names none.
	personRange = "30d"
	// historyLimit and historyMax bound one page of events or visits.
	historyLimit = 100
	historyMax   = 500
	// maxLinked bounds how many anonymous visitor ids one person is matched by.
	maxLinked = 500
)

// registerAdmin declares the three reads. The group is built in this file so
// cmd/zipdoc resolves the prefix from here.
func registerAdmin(app cloud.Router) {
	g := app.Group(adminPrefix)
	g.Get("/:org/:name", history, zip.WithOperationID("adminEventHistory"))
	g.Get("/:org/:name/sessions", sessions, zip.WithOperationID("adminEventSessions"))
	g.Get("/:org/:name/sessions/:session", playback, zip.WithOperationID("adminEventSession"))
}

// account asks IAM who <owner>/<name> is: their id (the token's sub) and their
// email. It replays the operator's own credential, so IAM authorizes the read as the
// SuperAdmin the boundary already validated. A var so a test answers for IAM.
var account = func(c *zip.Ctx, owner, name string) (id, email string, err error) {
	base := strings.TrimRight(strings.TrimSpace(iam.Base()), "/")
	if base == "" {
		return "", "", fmt.Errorf("iam endpoint not configured")
	}
	uURL := base + "/v1/iam/users/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(c.Context(), http.MethodGet, uURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	if ck := string(c.Fiber().Request().Header.Peek("Cookie")); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	if auth := c.Header("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", "", fmt.Errorf("iam unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("iam %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var u map[string]any
	if err := json.Unmarshal(body, &u); err != nil {
		return "", "", fmt.Errorf("iam user decode: %w", err)
	}
	if u == nil {
		return "", "", fmt.Errorf("iam user %s/%s: empty", owner, name)
	}
	id, _ = u["id"].(string)
	email, _ = u["email"].(string)
	return strings.TrimSpace(id), strings.TrimSpace(email), nil
}

// operator admits a SuperAdmin acting as themselves and nobody else, before
// anything is read. A request made as someone else carries that person's bits, and
// this read is not one an impersonation may make whoever the target is.
func operator(ctx context.Context) (*zip.Ctx, error) {
	c, ok := cloud.Request(ctx)
	if !ok || !principal.Super(c) || zip.CallerOf(ctx).ActedBy != "" {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	return c, nil
}

// The two states of the /v1/admin envelope.
const (
	statusOK  = "ok"
	statusErr = "error"
)

// ── wire ────────────────────────────────────────────────────────────────────

// historyIn names a person and the slice of their history to read.
type historyIn struct {
	// Org is the IAM org that owns the account, from the path.
	Org string `json:"org"`
	// Name is the account's name within Org, from the path.
	Name string `json:"name"`
	// ID is the account's IAM id (the token's sub) when the caller holds it already —
	// the directory row it opened. Given, IAM is not asked; empty, it is. It is matched
	// as given: a SuperAdmin naming another id reads that id's events, which they may
	// read anyway.
	ID string `json:"id"`
	// Range is a relative window: a count and a unit — 24h, 7d, 90d, any <N>h or
	// <N>d — or day, week, month, all. Default 30d. Ignored when both start and end
	// are given.
	Range string `json:"range"`
	// Start is the inclusive lower bound of a custom window, RFC3339. Requires end.
	Start string `json:"start"`
	// End is the exclusive upper bound of a custom window, RFC3339. Requires start.
	End string `json:"end"`
	// Tenant keeps only the rows filed under this org. Empty reads every tenant.
	Tenant string `json:"tenant"`
	// Limit is how many rows to return, newest first. Default 100, at most 500.
	Limit int `json:"limit"`
	// Cursor continues a listing: pass back the previous page's next. Omit it for
	// the first page.
	Cursor string `json:"cursor"`
}

// sessionsIn names a person and the window of visits to read.
type sessionsIn struct {
	// Org is the IAM org that owns the account, from the path.
	Org string `json:"org"`
	// Name is the account's name within Org, from the path.
	Name string `json:"name"`
	// ID is the account's IAM id (the token's sub) when the caller holds it already —
	// the directory row it opened. Given, IAM is not asked; empty, it is. It is matched
	// as given: a SuperAdmin naming another id reads that id's events, which they may
	// read anyway.
	ID string `json:"id"`
	// Range is a relative window: a count and a unit — 24h, 7d, 90d, any <N>h or
	// <N>d — or day, week, month, all. Default 30d. Ignored when both start and end
	// are given.
	Range string `json:"range"`
	// Start is the inclusive lower bound of a custom window, RFC3339. Requires end.
	Start string `json:"start"`
	// End is the exclusive upper bound of a custom window, RFC3339. Requires start.
	End string `json:"end"`
	// Tenant counts only the events filed under this org, so a visit that crossed
	// tenants is summarized from its rows here alone. Empty reads every tenant.
	Tenant string `json:"tenant"`
	// Limit is how many visits to return, the most recent first. Default 100, at
	// most 500.
	Limit int `json:"limit"`
}

// playbackIn names one visit of one person.
type playbackIn struct {
	// Org is the IAM org that owns the account, from the path.
	Org string `json:"org"`
	// Name is the account's name within Org, from the path.
	Name string `json:"name"`
	// Session is the visit's session id, from the path.
	Session string `json:"session"`
}

// personID is who a history read is about.
type personID struct {
	// Owner is the IAM org that owns the account.
	Owner string `json:"owner"`
	// Name is the account's name within Owner.
	Name string `json:"name"`
	// ID is the account's IAM id, the token's sub. Empty when IAM did not answer,
	// which degraded then names.
	ID string `json:"id,omitempty"`
	// Email is the account's address as IAM holds it. Empty when IAM did not answer
	// or holds none.
	Email string `json:"email,omitempty"`
	// IDs is every id this person's events were matched by: the IAM id,
	// <owner>/<name>, and each anonymous visitor id an identify row linked to them,
	// the links a verified write made first.
	IDs []string `json:"ids"`
	// Keys is the two ids that are the account itself: the IAM id and
	// <owner>/<name>. A row is attested when the boundary verified one of these.
	Keys []string `json:"keys"`
	// Partial is true when more visitor ids were linked than one read matches by,
	// so the oldest unverified links were left out.
	Partial bool `json:"partial"`
}

// personEvent is one thing the person did, or one error they hit.
type personEvent struct {
	// ID is the row's stable event id.
	ID string `json:"id"`
	// Timestamp is when it happened, RFC3339 UTC with the plane's nanoseconds.
	Timestamp string `json:"timestamp"`
	// Org is the tenant the event was filed under — the org that admitted it,
	// which need not be the account's own.
	Org string `json:"org"`
	// Signal is act for something the person did and error for a failure they hit.
	Signal string `json:"signal"`
	// Event is the event name, e.g. page_viewed or signup_completed.
	Event string `json:"event"`
	// Type is the kind within the signal: page, track, identify or group for an act.
	Type string `json:"type,omitempty"`
	// SessionID is the visit the event belongs to. Omitted when the client sent none.
	SessionID string `json:"sessionId,omitempty"`
	// Product is the surface that emitted the event. Omitted when absent.
	Product string `json:"product,omitempty"`
	// URL is the page the event fired on. Omitted when absent.
	URL string `json:"url,omitempty"`
	// Path is the URL's path component. Omitted when absent.
	Path string `json:"path,omitempty"`
	// DistinctID is the id the event was attributed to — one of the person's ids.
	DistinctID string `json:"distinctId,omitempty"`
	// Attested is true when the identity boundary verified the writer as this
	// person — their IAM id or <owner>/<name>. False means a client named them,
	// which any holder of a publishable key can: a visitor before sign-in, or a
	// claim nobody vouched for.
	Attested bool `json:"attested"`
	// Message is an error's message, already redacted at ingest. Omitted on an act.
	Message string `json:"message,omitempty"`
	// Impersonated is true when a platform operator wrote the event acting as
	// someone else.
	Impersonated bool `json:"impersonated"`
	// ImpersonatedBy is that operator, as <owner>/<name>. Omitted otherwise.
	ImpersonatedBy string `json:"impersonatedBy,omitempty"`
}

// personHistory is a person and a page of their events.
type personHistory struct {
	// Person is who the events are about, and every id they were matched by.
	Person personID `json:"person"`
	// Events is the page, newest first. Empty rather than absent when there are none.
	Events []personEvent `json:"events"`
}

// historyOut is the history envelope.
type historyOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the person and the page. Null when the read failed.
	Data *personHistory `json:"data"`
	// Next is the cursor for the following page, empty on the last one.
	Next string `json:"next"`
	// Degraded names a source that did not answer — "iam" — so a person matched by
	// <owner>/<name> alone reads as an outage rather than as a quiet history.
	Degraded []string `json:"degraded,omitempty"`
}

// personVisit is one session of one person.
type personVisit struct {
	// ID is the session id.
	ID string `json:"id"`
	// Orgs is every tenant the visit's events were filed under: a visit that
	// crossed from a site into the console spans both.
	Orgs []string `json:"orgs"`
	// Start is the visit's first event, RFC3339 UTC.
	Start string `json:"start"`
	// End is the visit's last event, RFC3339 UTC.
	End string `json:"end"`
	// Events is how many events the person sent in the visit.
	Events int64 `json:"events"`
	// Pages is how many of them were page views.
	Pages int64 `json:"pages"`
	// Product is the surface the visit was on.
	Product string `json:"product,omitempty"`
	// URL is the first page of the visit.
	URL string `json:"url,omitempty"`
	// Impersonated is true when a platform operator wrote any event of the visit
	// acting as someone else.
	Impersonated bool `json:"impersonated"`
	// Recording is the visit's session replay, or null when nothing was recorded.
	Recording *recording `json:"recording"`
}

// recording is what the replay pipeline holds for a visit.
type recording struct {
	// Start is the recording's first frame, RFC3339 UTC.
	Start string `json:"start"`
	// End is its last frame, RFC3339 UTC.
	End string `json:"end"`
	// Active is how long the person was active in it, in milliseconds.
	Active int64 `json:"active"`
	// Clicks is how many clicks it caught.
	Clicks int64 `json:"clicks"`
	// Keypresses is how many key presses it caught.
	Keypresses int64 `json:"keypresses"`
	// Bytes is the recording's size as the recorder sent it.
	Bytes int64 `json:"bytes"`
	// Blocks is how many stored blocks hold it.
	Blocks int `json:"blocks"`
	// URL is the first page recorded, with credential-shaped query values redacted.
	URL string `json:"url,omitempty"`
	// Project is the replay pipeline's project the recording is filed under.
	Project int64 `json:"project"`
}

// sessionsOut is the visits envelope.
type sessionsOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Person is who the visits are about. Null when the read failed.
	Person *personID `json:"person"`
	// Data is the visits, the most recent first. Null when the read failed.
	Data []personVisit `json:"data"`
	// Degraded names a source that did not answer — "iam", or "replay" when the
	// recordings could not be read and every recording reads null for that reason.
	Degraded []string `json:"degraded,omitempty"`
}

// replayFrame is one rrweb event of a recording, beside the browser window it came from.
type replayFrame struct {
	// Window is the browser tab the event was recorded in.
	Window string `json:"window"`
	// Event is the rrweb eventWithTime, verbatim: hand it to an rrweb player.
	Event json.RawMessage `json:"event"`
}

// replayBlock is one stored block of a recording.
type replayBlock struct {
	// Start is the block's first frame, RFC3339 UTC.
	Start string `json:"start"`
	// End is its last frame, RFC3339 UTC.
	End string `json:"end"`
	// Frames is how many frames the block gave.
	Frames int `json:"frames"`
	// Error says why the block gave none — unreadable, sealed, or outside the
	// replay bucket. Omitted when it was read.
	Error string `json:"error,omitempty"`
}

// playbackDetail is one visit, its events and its recording.
type playbackDetail struct {
	// Person is who the visit is about.
	Person personID `json:"person"`
	// Session is the visit.
	Session personVisit `json:"session"`
	// Events is every event the person sent in the visit, oldest first, so a player
	// can lay them on the recording's timeline.
	Events []personEvent `json:"events"`
	// Blocks is each stored block of the recording and whether it was read. Empty
	// when nothing was recorded.
	Blocks []replayBlock `json:"blocks"`
	// Frames is the recording, oldest first: the rrweb events to replay. Empty when
	// nothing was recorded or nothing could be read.
	Frames []replayFrame `json:"frames"`
	// Truncated is true when the recording was longer than one answer carries and
	// Frames stops early.
	Truncated bool `json:"truncated"`
}

// playbackOut is the one-visit envelope.
type playbackOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the visit. Null when the read failed.
	Data *playbackDetail `json:"data"`
	// Degraded names a source that did not answer: "iam", or "replay".
	Degraded []string `json:"degraded,omitempty"`
}

// ── the reads ───────────────────────────────────────────────────────────────

// History returns one person's events across every tenant, newest first: what they
// did and the errors they hit, each with the org it was filed under, its session,
// page and product, and whether a platform operator wrote it acting as them.
//
// The person is the IAM account <org>/<name>, matched by their IAM id, by
// <org>/<name>, and by every anonymous visitor id an identify row linked to them —
// so the visit before they signed in is in the history too. Default window 30d;
// tenant narrows to one org; pages by keyset, so pass next back as cursor.
// SuperAdmin only.
//
// Example: {"status": "ok", "msg": "", "data": {"person": {"owner": "acme", "name": "ada", "id": "9f2…", "email": "ada@acme.com", "ids": ["9f2…", "acme/ada", "0190…"]}, "events": [{"id": "e1", "timestamp": "2026-10-06T12:00:00.123Z", "org": "hanzo", "signal": "act", "event": "page_viewed", "type": "page", "sessionId": "0192…", "product": "site", "url": "https://hanzo.ai/pricing", "path": "/pricing", "distinctId": "9f2…", "impersonated": false}]}, "next": ""}
func history(ctx context.Context, in *historyIn) (*historyOut, error) {
	c, err := operator(ctx)
	if err != nil {
		return nil, err
	}
	start, end, err := personWindow(in.Range, in.Start, in.End)
	if err != nil {
		return nil, err
	}
	limit := pageOf(in.Limit)
	after, err := readCursor(in.Cursor)
	if err != nil {
		return nil, err
	}
	p, degraded, err := resolve(ctx, c, in.Org, in.Name, in.ID)
	if err != nil {
		return failed(err, func(msg string) *historyOut { return &historyOut{Status: statusErr, Msg: msg} })
	}
	sql, args := historySQL(p.IDs, start, end, strings.TrimSpace(in.Tenant), after, limit)
	rows, err := warehouseQuery(ctx, sql, args...)
	if err != nil {
		return &historyOut{Status: statusErr, Msg: "warehouse: " + err.Error(), Degraded: degraded}, nil
	}
	out := &historyOut{Status: statusOK, Data: &personHistory{Person: p, Events: eventsOf(rows, p.Keys)}, Degraded: degraded}
	if len(rows) == limit {
		last := rows[len(rows)-1]
		out.Next = writeCursor(aTime(last["time"]), asStr(last["org"]), asStr(last["id"]))
	}
	return out, nil
}

// Sessions returns one person's visits across every tenant, the most recent first:
// when each began and ended, how many events and pages it held, the page it began
// on, whether a platform operator acted in it as them, and its session replay when
// one was recorded — the recording's length, activity, size and first page. Open a
// recorded visit at /sessions/{session} to replay it.
//
// The person and the window are History's. SuperAdmin only.
//
// Example: {"status": "ok", "msg": "", "person": {"owner": "acme", "name": "ada", "ids": ["9f2…", "acme/ada"]}, "data": [{"id": "0192…", "orgs": ["hanzo"], "start": "2026-10-06T12:00:00Z", "end": "2026-10-06T12:09:30Z", "events": 14, "pages": 5, "product": "site", "url": "https://hanzo.ai/", "impersonated": false, "recording": null}]}
func sessions(ctx context.Context, in *sessionsIn) (*sessionsOut, error) {
	c, err := operator(ctx)
	if err != nil {
		return nil, err
	}
	start, end, err := personWindow(in.Range, in.Start, in.End)
	if err != nil {
		return nil, err
	}
	p, degraded, err := resolve(ctx, c, in.Org, in.Name, in.ID)
	if err != nil {
		return failed(err, func(msg string) *sessionsOut { return &sessionsOut{Status: statusErr, Msg: msg} })
	}
	sql, args := visitsSQL(p.IDs, start, end, strings.TrimSpace(in.Tenant), "", pageOf(in.Limit))
	rows, err := warehouseQuery(ctx, sql, args...)
	if err != nil {
		return &sessionsOut{Status: statusErr, Msg: "warehouse: " + err.Error(), Degraded: degraded}, nil
	}
	list := visitsOf(rows)
	ids := make([]string, len(list))
	for i, v := range list {
		ids[i] = v.ID
	}
	recs, err := recordingsOf(ctx, ids, p.IDs, start)
	if err != nil {
		degraded = append(degraded, "replay")
	}
	for i := range list {
		list[i].Recording = recs[list[i].ID]
	}
	return &sessionsOut{Status: statusOK, Person: &p, Data: list, Degraded: degraded}, nil
}

// Session returns one visit of one person, ready to replay: the visit, every event
// the person sent in it oldest first, and the recording — each stored block and
// whether it could be read, and the rrweb frames in order, verbatim, for an rrweb
// player. A visit that was not recorded answers with its events and no frames.
//
// The visit must be the person's: a session id the plane holds none of their events
// under is 404, whoever else it belongs to. A recording is read only from the
// replay pipeline's own bucket, and an answer carries at most 32 MiB of frames —
// past that, truncated is true. SuperAdmin only.
//
// Example: {"status": "ok", "msg": "", "data": {"person": {"owner": "acme", "name": "ada", "ids": ["9f2…", "acme/ada"]}, "session": {"id": "0192…", "orgs": ["hanzo"], "start": "2026-10-06T12:00:00Z", "end": "2026-10-06T12:09:30Z", "events": 14, "pages": 5, "impersonated": false, "recording": null}, "events": [], "blocks": [], "frames": [], "truncated": false}}
func playback(ctx context.Context, in *playbackIn) (*playbackOut, error) {
	c, err := operator(ctx)
	if err != nil {
		return nil, err
	}
	session := strings.TrimSpace(in.Session)
	if !validSessionID(session) {
		return nil, zip.ErrBadRequest("session must be 1-70 characters of ASCII letters, digits or '-'")
	}
	p, degraded, err := resolve(ctx, c, in.Org, in.Name, "")
	if err != nil {
		return failed(err, func(msg string) *playbackOut { return &playbackOut{Status: statusErr, Msg: msg} })
	}
	// The visit, unbounded in time: a session id is the whole key, and one the
	// person sent no event under is not theirs.
	sql, args := visitsSQL(p.IDs, time.Unix(0, 0), time.Now().Add(maxClockSkew), "", session, 1)
	rows, err := warehouseQuery(ctx, sql, args...)
	if err != nil {
		return &playbackOut{Status: statusErr, Msg: "warehouse: " + err.Error(), Degraded: degraded}, nil
	}
	found := visitsOf(rows)
	if len(found) == 0 {
		return nil, zip.ErrNotFound("no session " + session + " for " + p.Owner + "/" + p.Name)
	}
	d := &playbackDetail{Person: p, Session: found[0], Events: []personEvent{}, Blocks: []replayBlock{}, Frames: []replayFrame{}}
	start, _ := time.Parse(time.RFC3339Nano, d.Session.Start)

	sql, args = sessionEventsSQL(p.IDs, session)
	rows, err = warehouseQuery(ctx, sql, args...)
	if err != nil {
		return &playbackOut{Status: statusErr, Msg: "warehouse: " + err.Error(), Degraded: degraded}, nil
	}
	d.Events = eventsOf(rows, p.Keys)

	recs, err := recordingsOf(ctx, []string{session}, p.IDs, start)
	if err != nil {
		return &playbackOut{Status: statusOK, Data: d, Degraded: append(degraded, "replay")}, nil
	}
	d.Session.Recording = recs[session]
	if d.Session.Recording == nil {
		return &playbackOut{Status: statusOK, Data: d, Degraded: degraded}, nil
	}
	sql, args = blocksSQL(session, p.IDs, start)
	rows, err = warehouseQuery(ctx, sql, args...)
	if err != nil {
		return &playbackOut{Status: statusOK, Data: d, Degraded: append(degraded, "replay")}, nil
	}
	blocks, aligned := blocksOf(rows)
	if !aligned {
		return &playbackOut{Status: statusOK, Data: d, Degraded: append(degraded, "replay")}, nil
	}
	d.Blocks, d.Frames, d.Truncated = replay(ctx, blocks)
	return &playbackOut{Status: statusOK, Data: d, Degraded: degraded}, nil
}

// ── who ─────────────────────────────────────────────────────────────────────

// resolve turns the path's account into every id the plane files them under. IAM
// names the sub; an IAM that does not answer leaves the person matched by
// <owner>/<name> and their linked visitors alone, and says so in degraded rather
// than reading as a quieter history.
func resolve(ctx context.Context, c *zip.Ctx, owner, name, known string) (personID, []string, error) {
	owner, name = strings.TrimSpace(owner), strings.TrimSpace(name)
	if owner == "" || name == "" {
		return personID{}, nil, zip.ErrBadRequest("org and name are required")
	}
	p := personID{Owner: owner, Name: name}
	var degraded []string
	if known = strings.TrimSpace(known); known != "" {
		p.ID = known
	} else if id, email, err := account(c, owner, name); err != nil {
		degraded = append(degraded, "iam")
	} else {
		p.ID, p.Email = id, email
	}
	p.Keys = compact([]string{p.ID, owner + "/" + name})
	if err := requireDatastore(ctx); err != nil {
		return personID{}, nil, err
	}
	sql, args := linkedSQL(p.Keys)
	rows, err := warehouseQuery(ctx, sql, args...)
	if err != nil {
		return personID{}, nil, fmt.Errorf("warehouse: %w", err)
	}
	if len(rows) > maxLinked {
		rows, p.Partial = rows[:maxLinked], true
	}
	linked := make([]string, 0, len(rows))
	for _, r := range rows {
		linked = append(linked, asStr(r["anonymous_id"]))
	}
	p.IDs = compact(append(slices.Clone(p.Keys), linked...))
	return p, degraded, nil
}

// compact drops empty and repeated ids, keeping the first of each in order.
func compact(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// ── SQL ─────────────────────────────────────────────────────────────────────
//
// Every value a caller influences is a bound parameter: the ids, the window, the
// tenant, the session and the cursor. The list of ids is bound one placeholder per
// id, so its length — never its content — shapes the statement.

// marks is n bound placeholders for an IN list.
func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

// strArgs widens ids to bound arguments.
func strArgs(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// whoClause matches a row to the person on any of the three id columns, and the
// args it binds: ids three times, once per column.
func whoClause(ids []string) (string, []any) {
	in := "(" + marks(len(ids)) + ")"
	a := strArgs(ids)
	return "(person_id IN " + in + " OR distinct_id IN " + in + " OR anonymous_id IN " + in + ")",
		slices.Concat(a, a, a)
}

// linkedSQL finds the anonymous visitor ids an identify row — or any row the person
// was signed in for — links to the person's own ids. person_id carries a bloom
// index, so this is a skip-index read, not a scan.
//
// A link is a CLAIM unless the boundary verified the writer: anyone holding a
// publishable key can post person_id = someone and an anonymous id beside it. So
// the links a verified write made come first, then the newest, and one past the
// cap is asked for so a cut is reported rather than silent.
func linkedSQL(keys []string) (string, []any) {
	in := "(" + marks(len(keys)) + ")"
	a := strArgs(keys)
	return "SELECT anonymous_id, max(attributes['" + attrSubject + "'] IN " + in + ") AS verified, max(time) AS seen" +
			" FROM " + factTable +
			" WHERE signal = ? AND person_id IN " + in + " AND anonymous_id != ''" +
			" GROUP BY anonymous_id ORDER BY verified DESC, seen DESC LIMIT " + itoa(maxLinked+1) + limits,
		slices.Concat(a, []any{string(signalAct)}, a)
}

// limits bounds what one operator read may cost the shared warehouse: a history
// matches on three id columns across every tenant, which no sort key serves.
const limits = " SETTINGS max_execution_time = 30"

// historySQL is one page of a person's events, newest first, keyset-paged on
// (time, id) so a page boundary inside a batch stamped with one clock skips nothing.
func historySQL(ids []string, start, end time.Time, tenant string, after *historyCursor, limit int) (string, []any) {
	who, wargs := whoClause(ids)
	where := "signal IN (?, ?) AND time >= ? AND time < ? AND " + who
	args := append([]any{string(signalAct), string(signalError), tsLiteral(start), tsLiteral(end)}, wargs...)
	if tenant != "" {
		where += " AND org = ?"
		args = append(args, tenant)
	}
	if after != nil {
		where += " AND (time, org, id) < (toDateTime64(?, 9, 'UTC'), ?, ?)"
		args = append(args, after.time, after.org, after.id)
	}
	return "SELECT " + eventColumns + " FROM " + factTable + " WHERE " + where +
			" ORDER BY time DESC, org DESC, id DESC LIMIT " + itoa(limit) + limits,
		args
}

// eventColumns is what a person's event reads, once for both reads that list them.
const eventColumns = "org, id, time, signal, name, kind, distinct_id, session_id, product, url, path, message, " +
	"attributes['" + attrImpersonated + "'] AS operated, attributes['" + attrImpersonatedBy + "'] AS operator, " +
	"attributes['" + attrSubject + "'] AS verified"

// visitsSQL folds a person's acts into visits, the most recent first. A visit is
// the browser's session, so it is one row whichever tenants its events were filed
// under. A non-empty session narrows to that one visit.
func visitsSQL(ids []string, start, end time.Time, tenant, session string, limit int) (string, []any) {
	who, wargs := whoClause(ids)
	where := "signal = ? AND session_id != '' AND time >= ? AND time < ? AND " + who
	args := append([]any{string(signalAct), tsLiteral(start), tsLiteral(end)}, wargs...)
	if tenant != "" {
		where += " AND org = ?"
		args = append(args, tenant)
	}
	if session != "" {
		where += " AND session_id = ?"
		args = append(args, session)
	}
	return "SELECT session_id, groupUniqArray(org) AS orgs, min(time) AS started, max(time) AS ended, " +
			"count() AS events, countIf(kind = 'page') AS pages, any(product) AS surface, " +
			"argMin(url, time) AS entry, max(attributes['" + attrImpersonated + "'] = 'true') AS operated" +
			" FROM " + factTable + " WHERE " + where +
			" GROUP BY session_id ORDER BY ended DESC LIMIT " + itoa(limit) + limits,
		args
}

// sessionEventsSQL is every event the person sent in one visit, oldest first,
// whichever tenant each was filed under.
func sessionEventsSQL(ids []string, session string) (string, []any) {
	who, wargs := whoClause(ids)
	args := append([]any{string(signalAct), string(signalError), session}, wargs...)
	return "SELECT " + eventColumns + " FROM " + factTable +
			" WHERE signal IN (?, ?) AND session_id = ? AND " + who +
			" ORDER BY time, org, id LIMIT " + itoa(historyMax*10) + limits,
		args
}

// replayTable is the replay pipeline's summary of each recording, one row per
// stored block, merged per session. It is the insights warehouse's, keyed by the
// pipeline's project rather than by org, so a recording is matched to a person by
// its session AND by the visitor id the recorder filed it under: a session id
// another tenant reused does not match a person it was never recorded for.
const replayTable = "insights.session_replay_events"

// recordingsSQL summarizes the recordings of some visits. The lower bound is a day
// before the window, because the table partitions on the recording's first frame.
func recordingsSQL(sessions, ids []string, since time.Time) (string, []any) {
	args := append(append([]any{tsLiteral(since.Add(-24 * time.Hour))}, strArgs(sessions)...), strArgs(ids)...)
	return "SELECT session_id, any(team_id) AS project, min(min_first_timestamp) AS started, " +
			"max(max_last_timestamp) AS ended, sum(active_milliseconds) AS active, sum(click_count) AS clicks, " +
			"sum(keypress_count) AS keypresses, sum(size) AS bytes, length(" + realBlocks + ") AS blocks, " +
			"argMinMerge(first_url) AS entry" +
			" FROM " + replayTable +
			" WHERE min_first_timestamp >= ? AND session_id IN (" + marks(len(sessions)) + ")" +
			" AND distinct_id IN (" + marks(len(ids)) + ")" +
			" GROUP BY session_id HAVING max(is_deleted) = 0" + limits,
		args
}

// realBlocks is a recording's stored block urls. The pipeline's own view writes a
// row with no block as an empty url beside a NULL timestamp, and groupArray drops
// the NULL, so the url array is filtered the same way or the arrays misalign.
const realBlocks = "arrayFilter(u -> u != '', groupArrayArray(block_urls))"

// blocksSQL lists one recording's stored blocks. The three arrays are aggregated
// over the same rows in the same pass and the urls filtered as realBlocks says, so
// element i of each names one block; blocksOf refuses arrays that still disagree.
func blocksSQL(session string, ids []string, since time.Time) (string, []any) {
	args := append([]any{tsLiteral(since.Add(-24 * time.Hour)), session}, strArgs(ids)...)
	return "SELECT groupArrayArray(block_first_timestamps) AS starts, groupArrayArray(block_last_timestamps) AS ends, " +
			realBlocks + " AS urls" +
			" FROM " + replayTable +
			" WHERE min_first_timestamp >= ? AND session_id = ? AND distinct_id IN (" + marks(len(ids)) + ")" +
			" GROUP BY session_id HAVING max(is_deleted) = 0" + limits,
		args
}

// ── rows ────────────────────────────────────────────────────────────────────

// eventsOf renders a person's rows. A URL is stored as the page sent it, so a
// credential in its query — a reset or sign-in link — is redacted here, at the read,
// by the one scrubber there is.
func eventsOf(rows []map[string]any, keys []string) []personEvent {
	out := make([]personEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, personEvent{
			ID: asStr(r["id"]), Timestamp: nanos(aTime(r["time"])), Org: asStr(r["org"]),
			Signal: asStr(r["signal"]), Event: asStr(r["name"]), Type: asStr(r["kind"]),
			SessionID: asStr(r["session_id"]), Product: asStr(r["product"]),
			URL: scrubText(asStr(r["url"])), Path: scrubText(asStr(r["path"])), DistinctID: asStr(r["distinct_id"]),
			Attested:       slices.Contains(keys, asStr(r["verified"])),
			Message:        asStr(r["message"]),
			Impersonated:   asStr(r["operated"]) == "true",
			ImpersonatedBy: asStr(r["operator"]),
		})
	}
	return out
}

func visitsOf(rows []map[string]any) []personVisit {
	out := make([]personVisit, 0, len(rows))
	for _, r := range rows {
		orgs := stringsOf(r["orgs"])
		slices.Sort(orgs)
		out = append(out, personVisit{
			ID: asStr(r["session_id"]), Orgs: orgs,
			Start: nanos(aTime(r["started"])), End: nanos(aTime(r["ended"])),
			Events: aInt64(r["events"]), Pages: aInt64(r["pages"]),
			Product: asStr(r["surface"]), URL: scrubText(asStr(r["entry"])),
			Impersonated: aInt64(r["operated"]) > 0,
		})
	}
	return out
}

// recordingsOf summarizes the recordings of some visits, keyed by session. No
// visits, no read.
func recordingsOf(ctx context.Context, sessions, ids []string, since time.Time) (map[string]*recording, error) {
	out := map[string]*recording{}
	if len(sessions) == 0 || len(ids) == 0 {
		return out, nil
	}
	sql, args := recordingsSQL(sessions, ids, since)
	rows, err := warehouseQuery(ctx, sql, args...)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		out[asStr(r["session_id"])] = &recording{
			Start: nanos(aTime(r["started"])), End: nanos(aTime(r["ended"])),
			Active: aInt64(r["active"]), Clicks: aInt64(r["clicks"]), Keypresses: aInt64(r["keypresses"]),
			Bytes: aInt64(r["bytes"]), Blocks: int(aInt64(r["blocks"])),
			// The pipeline derives first_url itself, past the ingest scrubber, so it is
			// redacted here, at the read, by the one scrubber there is.
			URL:     scrubText(asStr(r["entry"])),
			Project: aInt64(r["project"]),
		}
	}
	return out, nil
}

// nanos renders a plane time: RFC3339 with nanoseconds, UTC.
func nanos(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// itoa is a validated int rendered into a LIMIT.
func itoa(n int) string { return strconv.Itoa(n) }

// ── window, page, cursor ────────────────────────────────────────────────────

// personWindow is the window grammar every lens reads, defaulting to 30 days.
func personWindow(rng, start, end string) (time.Time, time.Time, error) {
	if strings.TrimSpace(start) == "" && strings.TrimSpace(end) == "" {
		rng = cmp.Or(strings.TrimSpace(rng), personRange)
	}
	w, err := types.ParseWindow(rng, start, end, time.Now())
	if err != nil {
		return time.Time{}, time.Time{}, zip.ErrBadRequest(err.Error())
	}
	return w.Start, w.End, nil
}

// pageOf clamps a page size: the default at or below zero, the maximum above it.
func pageOf(n int) int {
	if n <= 0 {
		return historyLimit
	}
	return min(n, historyMax)
}

// historyCursor is where the previous page ended: the last row's time, at the plane's
// nanosecond precision, its org and its id. The org is part of the key because ids
// are the client's and two tenants can hold one.
type historyCursor struct {
	time string // "2006-01-02 15:04:05.999999999", UTC
	org  string
	id   string
}

const cursorLayout = "2006-01-02 15:04:05.000000000"

func writeCursor(t time.Time, org, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(cursorLayout) + "\x00" + org + "\x00" + id))
}

// readCursor parses a cursor this read issued; anything else is a 400, never a
// silent first page.
func readCursor(s string) (*historyCursor, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	parts := strings.Split(string(raw), "\x00")
	if err != nil || len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return nil, errBadCursor
	}
	if _, err := time.Parse(cursorLayout, parts[0]); err != nil {
		return nil, errBadCursor
	}
	return &historyCursor{time: parts[0], org: parts[1], id: parts[2]}, nil
}

var errBadCursor = zip.ErrBadRequest("cursor is not one this listing issued")

// failed answers a read that could not complete: a caller's mistake keeps its own
// status, and anything upstream is the envelope's error at 200, the shape every
// /v1/admin read answers a failed source with.
func failed[T any](err error, envelope func(string) *T) (*T, error) {
	var he *zip.HTTPError
	if errors.As(err, &he) && he.Status < 500 {
		return nil, err
	}
	return envelope(err.Error()), nil
}
