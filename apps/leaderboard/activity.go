// GET /v1/usage/activity — the per-day contribution series (GitHub-style heatmap +
// timeline) for ONE authorized subject.
//
//	subject=user    the caller's OWN activity (default); another user only for an org
//	                admin / SuperAdmin, and only a user WITHIN the caller's org
//	subject=org     the caller's OWN org (default); another org only for a SuperAdmin
//	subject=project honest-empty — per-project attribution is not in the usage ledger
//	                yet (documented gap; lights up when cloud_usage gains a project
//	                column, a one-line projection change)
//	from,to         optional day range (default 90d; clamped to 366d)
//
// Authorization is resolved SERVER-SIDE from the validated principal; a caller can
// never widen scope past what they're entitled to. org is always the leading bound
// predicate in the query.

package leaderboard

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// activityQuery selects whose activity to read and over what days. Every field rides
// the query string and every one is optional.
type activityQuery struct {
	// Subject is what the series is about: "user" (default), "org" or "project".
	Subject string `json:"subject"`
	// ID names the subject within what the caller is entitled to see. Omitted (or
	// "me") it is the caller themselves, or their own org. Another user requires org
	// admin and must belong to the caller's org; another org requires a SuperAdmin.
	ID string `json:"id"`
	// From is the first day of the range, "2006-01-02". Defaults to 90 days back.
	From string `json:"from"`
	// To is the last day of the range, "2006-01-02". Defaults to today; the span is
	// clamped to 366 days.
	To string `json:"to"`
}

// Activity returns the per-day usage series for ONE authorized subject — the points a
// contribution heatmap and a timeline are drawn from, gap-filled so every day in the
// range is present. Authorization is resolved server-side from the validated
// principal, so a caller can never widen the subject past what they are entitled to:
// a non-admin reads only themselves and their own org. subject=project answers empty
// with a note, because the usage ledger records no project column yet. When the
// warehouse is not connected the series answers empty with available=false rather
// than fabricated days.
//
// Example: {"subject": "user", "from": "2026-01-01", "to": "2026-03-31"}
func (o boardOps) activity(ctx context.Context, in *activityQuery) (*ActivityView, error) {
	org, err := tenantOf(ctx, "sign in to view activity")
	if err != nil {
		return nil, err
	}
	subject := strings.ToLower(strings.TrimSpace(in.Subject))
	if subject == "" {
		subject = "user"
	}
	id := strings.TrimSpace(in.ID)
	w, err := resolveRange(in.From, in.To, nowFn())
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}

	noStore(ctx)
	base := ActivityView{
		Subject: subject,
		ID:      id,
		From:    dayLiteral(w.From),
		To:      dayLiteral(w.To),
		Days:    []ActivityPoint{},
		Source:  rollupTable,
	}

	switch subject {
	case "user":
		target, status, msg := resolveUserSubject(selfIDOf(ctx, org), adminOf(ctx), org, id)
		if status != 0 {
			return nil, zip.Errorf(status, "%s", msg)
		}
		base.ID = target
		return runActivity(ctx, o.s, base, org, target, w)

	case "org":
		effOrg, status, msg := resolveOrgSubject(superOf(ctx), org, id)
		if status != 0 {
			return nil, zip.Errorf(status, "%s", msg)
		}
		base.ID = effOrg
		return runActivity(ctx, o.s, base, effOrg, "", w)

	case "project":
		// A project is a sub-scope of the caller's OWN org (the validated principal
		// already gates that at `tenant`). Project attribution is not recorded in the
		// usage ledger (cloud_usage has no project column), so this is an HONEST empty —
		// never fabricated. The id is echoed but reaches no query, so no cross-tenant
		// surface exists; it lights up when the ledger gains a project column.
		base.Available = false
		base.Note = "per-project usage attribution is not recorded in the usage ledger yet"
		return &base, nil

	default:
		return nil, zip.ErrBadRequest("subject must be user|org|project")
	}
}

// runActivity executes the per-day query for a resolved+authorized subject and
// assembles the gap-filled series. Datastore down / query blip → honest-empty.
func runActivity(ctx context.Context, s *cloud.Service[state], base ActivityView, effOrg, subjectUser string, w window) (*ActivityView, error) {
	if !datastoreEnabled() {
		return &base, nil
	}
	if err := EnsureUsageRollup(ctx); err != nil {
		s.Log.Debug("rollup ensure failed; activity honest-empty", "err", err)
		return &base, nil
	}
	sqlStr, args := buildActivitySQL(effOrg, subjectUser, w)
	rows, err := queryDatastore(ctx, sqlStr, args...)
	if err != nil {
		s.Log.Debug("activity query failed; honest-empty", "org", effOrg, "err", err)
		return &base, nil
	}
	base.Days, base.Totals = buildActivitySeries(w, rows)
	base.Available = true
	return &base, nil
}

// ── authorization (PURE — no Ctx, no I/O — so the policy is unit-tested directly) ──

// resolveUserSubject decides the target user's ledger id for a user-activity read.
// self = the caller's own ledger id (may be ""); admin = the caller may view org
// members (org admin or SuperAdmin). Self (empty/"me"/own id/own name) is always
// allowed; ANY other user requires admin AND must belong to the caller's org. Returns
// (target, status, msg); status 0 = allowed, else the HTTP status to answer.
func resolveUserSubject(self string, admin bool, org, id string) (string, int, string) {
	if id == "" || strings.EqualFold(id, "me") || id == self || (self != "" && id == nameOf(self)) {
		if self == "" {
			return "", http.StatusBadRequest, "cannot resolve your identity (missing user name)"
		}
		return self, 0, ""
	}
	if !admin {
		return "", http.StatusForbidden, "you can only view your own activity"
	}
	target := normalizeUserID(id, org)
	if target == "" {
		return "", http.StatusForbidden, "user is not in your org"
	}
	return target, 0, ""
}

// normalizeUserID maps a requested user id to a ledger "owner/name" WITHIN org. A
// slashed id must be prefixed by this org (else it belongs to another org → refuse);
// a bare name becomes "<org>/<name>". The read always binds organization=org, so a
// resolved id can only ever be a user of the caller's org.
func normalizeUserID(id, org string) string {
	if strings.Contains(id, "/") {
		if strings.HasPrefix(id, org+"/") {
			return id
		}
		return "" // different org — refuse (a super switches X-Org-Id to that org instead)
	}
	return org + "/" + id
}

// resolveOrgSubject decides the effective org for an org-activity read. Own org
// (empty or matching) is always allowed; another org requires a SuperAdmin. Returns
// (effectiveOrg, status, msg); status 0 = allowed.
func resolveOrgSubject(super bool, org, id string) (string, int, string) {
	if id == "" || id == org {
		return org, 0, ""
	}
	if super {
		if len(id) > principal.MaxOrgLen {
			return "", http.StatusBadRequest, "org id too long"
		}
		return id, 0, ""
	}
	return "", http.StatusForbidden, "you can only view your own org"
}
