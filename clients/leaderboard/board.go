// GET /v1/usage/leaderboard — the ranked board.
//
//	scope=personal  caller's rank + the top users of the caller's OWN org, identities
//	                anonymized except self + opted-in peers (the "as a participant" view)
//	scope=org       the caller's org board; identities NAMED only for an org admin /
//	                SuperAdmin (they may see their org's members), else anonymized
//	scope=global    the top ORGS; every org NAMED for a SuperAdmin, else only the orgs
//	                that opted into the public board — plus the caller's OWN org rank
//	metric=tokens|requests|cost   period=day|week|month|all   limit (clamped 1..100)
//
// Tenant isolation: org is principal.Org (validated), bound positionally in every
// query. Cross-org cost is SuperAdmin-only. Datastore down → honest-empty.
package leaderboard

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

func leaderboardHandler(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view the leaderboard")
	}
	scope := strings.ToLower(strings.TrimSpace(c.Query("scope")))
	if scope == "" {
		scope = "personal"
	}
	metricLabel := strings.ToLower(strings.TrimSpace(c.Query("metric")))
	if metricLabel == "" {
		metricLabel = "tokens"
	}
	metricCol, ok := resolveMetric(metricLabel)
	if !ok {
		return zip.ErrBadRequest("metric must be tokens|requests|cost")
	}
	w, err := resolvePeriod(c.Query("period"), nowFn())
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	limit := clampLimit(atoiDefault(c.Query("limit"), 0), 10)

	// Per-tenant analytics must never be cached by a browser or intermediary.
	c.SetHeader("Cache-Control", "no-store")

	base := LeaderboardView{
		Scope:  scope,
		Metric: metricLabel,
		Period: w.Label,
		Start:  windowStart(w),
		End:    dayLiteral(w.To),
		Rows:   []LeaderboardRow{},
		Source: rollupTable,
	}

	// Honest-empty when the warehouse is not connected or the rollup is not ready —
	// never a fabricated rank.
	if !datastoreEnabled() {
		return c.JSON(http.StatusOK, base)
	}
	ctx := c.Context()
	if err := EnsureUsageRollup(ctx); err != nil {
		s.Log.Debug("rollup ensure failed; leaderboard honest-empty", "err", err)
		return c.JSON(http.StatusOK, base)
	}

	switch scope {
	case "personal", "org":
		return userBoard(s, c, base, org, scope, metricLabel, metricCol, w, limit)
	case "global":
		return orgBoard(s, c, base, org, metricLabel, metricCol, w, limit)
	default:
		return zip.ErrBadRequest("scope must be personal|org|global")
	}
}

// userBoard ranks the users of the caller's own org (scope personal|org).
func userBoard(s *cloud.Service[state], c *zip.Ctx, base LeaderboardView, org, scope, metricLabel, metricCol string, w window, limit int) error {
	ctx := c.Context()
	base.Subject = "user"

	// NAMED disclosure only for an admin viewing the ORG board; personal is always the
	// anonymized participant view (self + opted-in peers only).
	named := scope == "org" && (principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c))
	// Peer cost is shown only on an explicit cost board (the ranked value) or to an
	// admin; otherwise cost is withheld for everyone but self.
	costVisible := metricLabel == "cost" || named

	sqlStr, args := buildUserBoardSQL(org, w, metricCol, limit)
	rows, err := queryDatastore(ctx, sqlStr, args...)
	if err != nil {
		s.Log.Debug("user board query failed; honest-empty", "org", org, "err", err)
		return c.JSON(http.StatusOK, base)
	}
	aggs := decodeAggRows(rows, "user_id")

	handles, herr := s.State.store.ListedHandles(ctx, org)
	if herr != nil {
		s.Log.Debug("listed handles read failed; anonymizing", "org", org, "err", herr)
		handles = map[string]string{}
	}
	selfID := selfLedgerID(c, org)
	selfHandle, selfListed := "", false
	if selfID != "" {
		if u, gerr := s.State.store.GetUser(ctx, selfID); gerr == nil {
			selfHandle, selfListed = u.Handle, u.Listed
		}
	}
	nc := nameCtx{selfUserID: selfID, selfHandle: selfHandle, named: named, handles: handles}
	base.Rows = buildUserRows(aggs, metricLabel, nc, costVisible)
	base.Available = true

	countSQL, countArgs := buildUserCountSQL(org, w)
	if n, e := scalar(ctx, countSQL, countArgs); e == nil {
		base.Total = n
	}
	if selfID != "" {
		base.Self = userSelfRank(ctx, s, org, selfID, selfHandle, selfListed, metricLabel, metricCol, w, base.Total)
	}
	return c.JSON(http.StatusOK, base)
}

// userSelfRank computes the caller's own standing even when they fall outside the
// top-N page. Rank = (users whose metric strictly exceeds the caller's) + 1.
func userSelfRank(ctx context.Context, s *cloud.Service[state], org, selfID, selfHandle string, selfListed bool, metricLabel, metricCol string, w window, total int64) *SelfRank {
	sqlStr, args := buildSelfAggSQL(org, selfID, w)
	rows, err := queryDatastore(ctx, sqlStr, args...)
	if err != nil {
		s.Log.Debug("self agg query failed", "org", org, "err", err)
		return nil
	}
	handle := selfHandle
	if handle == "" {
		handle = nameOf(selfID)
	}
	self := &SelfRank{Handle: handle, Listed: selfListed, OfTotal: total}
	if len(rows) > 0 {
		a := decodeAggRows(rows, "user_id")[0]
		self.Requests, self.Tokens, self.CostCents = a.requests, a.tokens, a.costCents
		self.Metric = metricValue(a, metricLabel)
	}
	if self.Metric <= 0 {
		return self // no usage in the window → unranked (client shows "—")
	}
	aboveSQL, aboveArgs := buildAboveCountSQL(org, w, metricCol, self.Metric)
	if above, e := scalar(ctx, aboveSQL, aboveArgs); e == nil {
		self.Rank = int(above) + 1
		self.Ranked = true
	}
	return self
}

// orgBoard ranks organizations (scope global).
func orgBoard(s *cloud.Service[state], c *zip.Ctx, base LeaderboardView, org, metricLabel, metricCol string, w window, limit int) error {
	ctx := c.Context()
	base.Subject = "org"
	super := principal.IsSuperAdmin(c)

	// Cross-org SPEND is platform-admin-only; an org opting into the public board
	// consents to volume (tokens/requests), not to publishing its bill.
	if metricLabel == "cost" && !super {
		return zip.ErrForbidden("the global cost leaderboard is restricted to platform admins")
	}
	costVisible := super

	// Visibility set: SuperAdmin sees every org (orgs=nil); everyone else sees only the
	// orgs that opted into the public board.
	var orgs []string
	displays := map[string]string{}
	callerOrgListed := false
	if !super {
		d, err := s.State.store.ListedOrgs(ctx)
		if err != nil {
			s.Log.Debug("listed orgs read failed; honest-empty", "err", err)
			d = map[string]string{}
		}
		displays = d
		orgs = keysOf(d)
		_, callerOrgListed = d[org]
	}

	base.Available = true
	// Total = the size of the ranked universe (all orgs for a super; the opted-in set
	// otherwise) — a bare count, never a platform-wide leak for a regular caller.
	if super {
		countSQL, countArgs := buildOrgCountSQL(w, nil)
		if n, e := scalar(ctx, countSQL, countArgs); e == nil {
			base.Total = n
		}
	} else {
		base.Total = int64(len(orgs))
	}

	// The caller's OWN org rank (self) — the gamification hook. Ranked within the same
	// universe the board shows.
	base.Self = orgSelfRank(ctx, s, org, metricLabel, metricCol, w, super, orgs, callerOrgListed, base.Total)

	if !super && len(orgs) == 0 {
		return c.JSON(http.StatusOK, base) // no org opted in yet → empty board, self-rank only
	}

	sqlStr, args := buildOrgBoardSQL(w, metricCol, limit, orgs)
	rows, err := queryDatastore(ctx, sqlStr, args...)
	if err != nil {
		s.Log.Debug("org board query failed; honest-empty", "err", err)
		base.Rows = []LeaderboardRow{}
		return c.JSON(http.StatusOK, base)
	}
	aggs := decodeAggRows(rows, "organization")
	base.Rows = buildOrgRows(aggs, metricLabel, super, displays, costVisible)
	return c.JSON(http.StatusOK, base)
}

// orgSelfRank computes the caller's own org standing. For a regular caller whose org
// is NOT on the public board, it stays unranked (Listed=false) — a prompt to opt in
// rather than a leak of where the org sits in a set it didn't join.
func orgSelfRank(ctx context.Context, s *cloud.Service[state], org, metricLabel, metricCol string, w window, super bool, orgs []string, callerOrgListed bool, total int64) *SelfRank {
	sqlStr, args := buildOrgAggSQL(org, w)
	rows, err := queryDatastore(ctx, sqlStr, args...)
	if err != nil {
		return nil
	}
	self := &SelfRank{Handle: org, OfTotal: total, Listed: super || callerOrgListed}
	if len(rows) > 0 {
		a := decodeAggRows(rows, "organization")[0]
		self.Requests, self.Tokens, self.CostCents = a.requests, a.tokens, a.costCents
		self.Metric = metricValue(a, metricLabel)
	}
	if self.Metric <= 0 {
		return self
	}
	// A regular caller whose org has not opted in is not ranked against the public set.
	if !super && !callerOrgListed {
		return self
	}
	aboveSQL, aboveArgs := buildOrgAboveCountSQL(w, metricCol, self.Metric, orgs)
	if above, e := scalar(ctx, aboveSQL, aboveArgs); e == nil {
		self.Rank = int(above) + 1
		self.Ranked = true
	}
	return self
}

// ── small helpers ─────────────────────────────────────────────────────────────

// scalar runs a single-column, single-row aggregate and returns its value. The
// builders return (sql, args); this is the thin adapter that runs them.
func scalar(ctx context.Context, sqlStr string, args []any) (int64, error) {
	rows, err := queryDatastore(ctx, sqlStr, args...)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	for _, v := range rows[0] { // single column
		return aInt64(v), nil
	}
	return 0, nil
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func windowStart(w window) string {
	if !w.HasFrom {
		return ""
	}
	return dayLiteral(w.From)
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
