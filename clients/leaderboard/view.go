// Response shapes + the PURE naming / anonymization / value-coercion functions.
// I/O-free so the privacy policy is unit-tested directly with plain values.
//
// PRIVACY MODEL (the product rule).
//   - A leaderboard row carries an AGGREGATE metric + a display identity. The
//     identity is revealed only when the viewer is authorized to see it:
//   - self          → always ("you")
//   - opted-in peer  → the handle THEY chose (opt-in store)
//   - admin viewer   → the member's username (org admin sees org members;
//     SuperAdmin sees anyone) — the name half of user_id,
//     no IAM round-trip, no cross-service call
//   - everyone else  → "Anonymous" (identity withheld; metric still shown)
//   - Cross-ORG isolation is enforced upstream by the org-bound SQL: a user board
//     only ever contains the caller's own org's rows; an org board carries only
//     org-level aggregates (never a user identity). So a row can never carry
//     ANOTHER tenant's user detail.
package leaderboard

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// ── Leaderboard response ──────────────────────────────────────────────────────

// LeaderboardRow is one ranked subject (a user or an org). Handle is the display
// identity per the privacy model; Anonymous marks a withheld identity; Self marks
// the caller's own row. Requests/Tokens are non-sensitive volume aggregates;
// CostCents is populated ONLY when the viewer is authorized to see this subject's
// spend (self, admin, or an explicit cost board — see costVisible). Metric is the
// value the board is ranked by (for bar sizing on the client).
type LeaderboardRow struct {
	Rank      int    `json:"rank"`
	Handle    string `json:"handle"`
	Anonymous bool   `json:"anonymous"`
	Self      bool   `json:"self"`
	Requests  int64  `json:"requests"`
	Tokens    int64  `json:"tokens"`
	CostCents int64  `json:"costCents"`
	Metric    int64  `json:"metric"`
}

// SelfRank is the caller's own standing on a user board, INCLUDED even when the
// caller falls outside the top-N page. Ranked=false means the caller has no usage
// in the window (unranked) — the client shows "—", never a fabricated rank.
type SelfRank struct {
	Ranked    bool   `json:"ranked"`
	Rank      int    `json:"rank"`
	OfTotal   int64  `json:"ofTotal"`
	Requests  int64  `json:"requests"`
	Tokens    int64  `json:"tokens"`
	CostCents int64  `json:"costCents"`
	Metric    int64  `json:"metric"`
	Handle    string `json:"handle"`
	Listed    bool   `json:"listed"` // is the caller publicly listed (opted in)
}

// LeaderboardView is the whole leaderboard response.
type LeaderboardView struct {
	Scope     string           `json:"scope"`   // personal|org|global
	Subject   string           `json:"subject"` // user|org
	Metric    string           `json:"metric"`  // tokens|requests|cost
	Period    string           `json:"period"`  // day|week|month|all|custom
	Start     string           `json:"start"`   // "" for all
	End       string           `json:"end"`
	Rows      []LeaderboardRow `json:"rows"`
	Self      *SelfRank        `json:"self,omitempty"`
	Total     int64            `json:"total"` // ranked subjects in the window
	Available bool             `json:"available"`
	Source    string           `json:"source"`
}

// ── Activity response ─────────────────────────────────────────────────────────

// ActivityPoint is one day of a subject's usage — the atom of the contribution
// heatmap + timeline. CostCents populated only when the viewer may see spend.
type ActivityPoint struct {
	Day       string `json:"day"` // "2006-01-02"
	Requests  int64  `json:"requests"`
	Tokens    int64  `json:"tokens"`
	CostCents int64  `json:"costCents"`
}

// ActivityTotals are the window sums + heatmap-scaling hints.
type ActivityTotals struct {
	Requests    int64 `json:"requests"`
	Tokens      int64 `json:"tokens"`
	CostCents   int64 `json:"costCents"`
	ActiveDays  int   `json:"activeDays"` // days with any usage
	MaxTokens   int64 `json:"maxTokens"`  // busiest day's tokens (heatmap intensity ceiling)
	MaxRequests int64 `json:"maxRequests"`
}

// ActivityView is the per-day series for one authorized subject.
type ActivityView struct {
	Subject   string          `json:"subject"` // user|org|project
	ID        string          `json:"id"`      // resolved subject id (echoed)
	From      string          `json:"from"`
	To        string          `json:"to"`
	Days      []ActivityPoint `json:"days"`
	Totals    ActivityTotals  `json:"totals"`
	Available bool            `json:"available"`
	Source    string          `json:"source"`
	Note      string          `json:"note,omitempty"` // honest note (e.g. project attribution not in the ledger)
}

// ── Naming / anonymization (pure) ─────────────────────────────────────────────

// nameCtx is the viewer context that decides identity disclosure for user rows.
type nameCtx struct {
	selfUserID string            // caller's ledger user_id ("owner/name"), "" if unknown
	selfHandle string            // caller's chosen handle, "" → username
	named      bool              // viewer may see ALL identities (admin/superadmin for this scope)
	handles    map[string]string // opted-in user_id → chosen handle
}

// displayUser resolves a user row's display per the privacy model.
func displayUser(userID string, nc nameCtx) (handle string, anonymous, self bool) {
	if nc.selfUserID != "" && userID == nc.selfUserID {
		h := nc.selfHandle
		if h == "" {
			h = nameOf(userID)
		}
		return h, false, true
	}
	if nc.named {
		return nameOf(userID), false, false
	}
	if h, ok := nc.handles[userID]; ok && strings.TrimSpace(h) != "" {
		return h, false, false
	}
	return "Anonymous", true, false
}

// displayOrg resolves an org row's display: SuperAdmin sees the slug; otherwise the
// org's chosen public display (or the slug when it opted in without one — the org
// board only ever contains opted-in orgs for a non-super viewer).
func displayOrg(org string, super bool, displays map[string]string) string {
	if super {
		return org
	}
	if d, ok := displays[org]; ok && strings.TrimSpace(d) != "" {
		return d
	}
	return org
}

// nameOf extracts the username (name half) from a "owner/name" ledger user_id.
// A bare id (no slash) is returned as-is.
func nameOf(userID string) string {
	if i := strings.LastIndex(userID, "/"); i >= 0 && i+1 < len(userID) {
		return userID[i+1:]
	}
	return userID
}

// ── Row assembly (pure) ───────────────────────────────────────────────────────

// aggRow is a decoded rollup aggregate for one subject (user or org).
type aggRow struct {
	id         string // user_id or organization
	requests   int64
	tokens     int64
	prompt     int64
	completion int64
	costCents  int64
}

// decodeAggRows decodes datastore rows keyed by `idCol` ("user_id" | "organization").
func decodeAggRows(rows []map[string]any, idCol string) []aggRow {
	out := make([]aggRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, aggRow{
			id:         aString(r[idCol]),
			requests:   aInt64(r["requests"]),
			tokens:     aInt64(r["total_tokens"]),
			prompt:     aInt64(r["prompt_tokens"]),
			completion: aInt64(r["completion_tokens"]),
			costCents:  aInt64(r["cost_cents"]),
		})
	}
	return out
}

// metricValue picks the ranked metric value from a decoded row.
func metricValue(a aggRow, metric string) int64 {
	switch metric {
	case "requests":
		return a.requests
	case "cost":
		return a.costCents
	default: // tokens
		return a.tokens
	}
}

// buildUserRows turns decoded user aggregates (already ordered by the metric DESC)
// into leaderboard rows, applying the naming policy and cost visibility. costVisible
// says whether peer cost may be shown (a cost board, or an admin viewer).
func buildUserRows(aggs []aggRow, metric string, nc nameCtx, costVisible bool) []LeaderboardRow {
	rows := make([]LeaderboardRow, 0, len(aggs))
	for i, a := range aggs {
		handle, anon, self := displayUser(a.id, nc)
		row := LeaderboardRow{
			Rank:      i + 1,
			Handle:    handle,
			Anonymous: anon,
			Self:      self,
			Requests:  a.requests,
			Tokens:    a.tokens,
			Metric:    metricValue(a, metric),
		}
		if self || nc.named || costVisible {
			row.CostCents = a.costCents
		}
		rows = append(rows, row)
	}
	return rows
}

// buildOrgRows turns decoded org aggregates into leaderboard rows. Org boards carry
// no user identity; cost is shown only when costVisible (SuperAdmin — enforced by
// the handler, which rejects a non-super global cost board upfront).
func buildOrgRows(aggs []aggRow, metric string, super bool, displays map[string]string, costVisible bool) []LeaderboardRow {
	rows := make([]LeaderboardRow, 0, len(aggs))
	for i, a := range aggs {
		row := LeaderboardRow{
			Rank:     i + 1,
			Handle:   displayOrg(a.id, super, displays),
			Requests: a.requests,
			Tokens:   a.tokens,
			Metric:   metricValue(a, metric),
		}
		if costVisible {
			row.CostCents = a.costCents
		}
		rows = append(rows, row)
	}
	return rows
}

// buildActivitySeries turns sparse per-day rollup rows into an evenly-spaced,
// gap-filled series over the window — the continuous calendar the heatmap + timeline
// render. Pure. Totals carry the heatmap-scaling ceilings (busiest day) and the
// active-day count.
func buildActivitySeries(w window, rows []map[string]any) ([]ActivityPoint, ActivityTotals) {
	type agg struct{ req, tok, cost int64 }
	idx := make(map[string]agg, len(rows))
	for _, r := range rows {
		d := dayKey(r["day"])
		if d == "" {
			continue
		}
		idx[d] = agg{aInt64(r["requests"]), aInt64(r["total_tokens"]), aInt64(r["cost_cents"])}
	}
	from := w.From
	if !w.HasFrom {
		from = w.To.Add(-365 * 24 * time.Hour) // defensive: activity always sets HasFrom
	}
	days := make([]ActivityPoint, 0, 64)
	var tot ActivityTotals
	for t := from.UTC().Truncate(24 * time.Hour); t.Before(w.To); t = t.Add(24 * time.Hour) {
		key := t.UTC().Format("2006-01-02")
		a := idx[key]
		days = append(days, ActivityPoint{Day: key, Requests: a.req, Tokens: a.tok, CostCents: a.cost})
		tot.Requests += a.req
		tot.Tokens += a.tok
		tot.CostCents += a.cost
		if a.req > 0 || a.tok > 0 || a.cost > 0 {
			tot.ActiveDays++
		}
		if a.tok > tot.MaxTokens {
			tot.MaxTokens = a.tok
		}
		if a.req > tot.MaxRequests {
			tot.MaxRequests = a.req
		}
	}
	return days, tot
}

// dayKey decodes a rollup Date cell to "2006-01-02" (the driver decodes Date →
// time.Time; a JSON transport path may give a string).
func dayKey(v any) string {
	switch d := v.(type) {
	case time.Time:
		return d.UTC().Format("2006-01-02")
	case string:
		s := strings.TrimSpace(d)
		if len(s) >= 10 {
			return s[:10]
		}
		return s
	default:
		return ""
	}
}

// ── value coercion ────────────────────────────────────────────────────────────
//
// The direct datastore driver decodes sum(UInt*) to uint64 and count() to uint64;
// the JSON transport fallback may decode to float64/json.Number/string. These
// coercers accept all so a transport change can never crash a read (identical
// discipline to clients/usage query.go).

func aInt64(v any) int64 {
	switch n := v.(type) {
	case nil:
		return 0
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case uint:
		return int64(n)
	case uint64:
		return int64(n)
	case uint32:
		return int64(n)
	case uint16:
		return int64(n)
	case uint8:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return int64(f)
		}
		return 0
	default:
		return 0
	}
}

func aString(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case []byte:
		return string(s)
	default:
		return ""
	}
}
