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
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

func activityHandler(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view activity")
	}
	subject := strings.ToLower(strings.TrimSpace(c.Query("subject")))
	if subject == "" {
		subject = "user"
	}
	id := strings.TrimSpace(c.Query("id"))
	w, err := resolveRange(c.Query("from"), c.Query("to"), nowFn())
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}

	c.SetHeader("Cache-Control", "no-store")
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
		self := selfLedgerID(c, org)
		admin := principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c)
		target, status, msg := resolveUserSubject(self, admin, org, id)
		if status != 0 {
			return zip.Errorf(status, "%s", msg)
		}
		base.ID = target
		return runActivity(s, c, base, org, target, w)

	case "org":
		effOrg, status, msg := resolveOrgSubject(principal.IsSuperAdmin(c), org, id)
		if status != 0 {
			return zip.Errorf(status, "%s", msg)
		}
		base.ID = effOrg
		return runActivity(s, c, base, effOrg, "", w)

	case "project":
		// A project is a sub-scope of the caller's OWN org (the validated principal
		// already gates that at `tenant`). Project attribution is not recorded in the
		// usage ledger (cloud_usage has no project column), so this is an HONEST empty —
		// never fabricated. The id is echoed but reaches no query, so no cross-tenant
		// surface exists; it lights up when the ledger gains a project column.
		base.Available = false
		base.Note = "per-project usage attribution is not recorded in the usage ledger yet"
		return c.JSON(http.StatusOK, base)

	default:
		return zip.ErrBadRequest("subject must be user|org|project")
	}
}

// runActivity executes the per-day query for a resolved+authorized subject and
// assembles the gap-filled series. Datastore down / query blip → honest-empty.
func runActivity(s *cloud.Service[state], c *zip.Ctx, base ActivityView, effOrg, subjectUser string, w window) error {
	if !datastoreEnabled() {
		return c.JSON(http.StatusOK, base)
	}
	ctx := c.Context()
	if err := EnsureUsageRollup(ctx); err != nil {
		s.Log.Debug("rollup ensure failed; activity honest-empty", "err", err)
		return c.JSON(http.StatusOK, base)
	}
	sqlStr, args := buildActivitySQL(effOrg, subjectUser, w)
	rows, err := queryDatastore(ctx, sqlStr, args...)
	if err != nil {
		s.Log.Debug("activity query failed; honest-empty", "org", effOrg, "err", err)
		return c.JSON(http.StatusOK, base)
	}
	base.Days, base.Totals = buildActivitySeries(w, rows)
	base.Available = true
	return c.JSON(http.StatusOK, base)
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
