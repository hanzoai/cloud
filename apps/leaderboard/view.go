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
	// Rank is this subject's 1-based standing in the window, 1 being the top. Rows are
	// ordered by the ranked metric descending (ties broken by request count), and a
	// board is always the top of the list — there is no offset paging — so the first
	// row is always rank 1 and rank is also the row's index + 1.
	Rank int `json:"rank"`
	// Handle is the display identity to render: the peer's chosen handle if they opted
	// into public listing, their username if the viewer is an admin of their org, the
	// org's chosen display name (or its id) on an org board, and the literal
	// "Anonymous" when the identity is withheld. Never an email or a raw ledger id.
	Handle string `json:"handle"`
	// Anonymous is true when this subject's identity was withheld and Handle is the
	// "Anonymous" placeholder: the metric is real, the name is not. Render it as an
	// unnamed row, never as someone actually called Anonymous.
	Anonymous bool `json:"anonymous"`
	// Self marks the caller's own row so a client can highlight it in place. At most
	// one row carries it, and it is never set on an org board — org rows carry no user
	// identity, so the caller's own org standing arrives in LeaderboardView.Self.
	Self bool `json:"self"`
	// Requests is how many AI requests this subject made in the window. Volume is not
	// sensitive, so it is reported for every row including anonymized ones.
	Requests int64 `json:"requests"`
	// Tokens is prompt+completion tokens this subject spent in the window. Like
	// Requests it is reported for every row.
	Tokens int64 `json:"tokens"`
	// CostCents is this subject's spend in whole US cents (not dollars, not
	// millicents). It is 0 unless the viewer is entitled to this subject's spend —
	// their own row, an admin looking at their own org, an explicitly cost-ranked
	// board, or a platform admin on the global board — so 0 means "withheld or zero",
	// and the two are deliberately indistinguishable.
	CostCents int64 `json:"costCents"`
	// Metric is the value the board was ranked by, copied from Requests, Tokens or
	// CostCents according to the request's metric. It is what a client sizes bars
	// against without having to know which metric was asked for.
	Metric int64 `json:"metric"`
}

// SelfRank is the caller's own standing on a user board, INCLUDED even when the
// caller falls outside the top-N page. Ranked=false means the caller has no usage
// in the window (unranked) — the client shows "—", never a fabricated rank.
type SelfRank struct {
	// Ranked is false when the caller holds no position: they had no usage in the
	// window, or (on the global board) their org has not opted into public listing and
	// so is not ranked against a set it never joined. Rank is then 0 and means nothing.
	Ranked bool `json:"ranked"`
	// Rank is the caller's 1-based standing, computed as (subjects whose windowed
	// metric strictly exceeds the caller's) + 1. It is exact against the whole ranked
	// universe, not just the returned page, so it can far exceed len(rows). Read it
	// only when Ranked.
	Rank int `json:"rank"`
	// OfTotal is the size of the universe Rank is out of — "rank N of OfTotal". On a
	// user board that is the org's users with any usage in the window; on the global
	// board it is every active org for a platform admin, and the count of opted-in
	// orgs for everyone else.
	OfTotal int64 `json:"ofTotal"`
	// Requests is the caller's own request count in the window, 0 if they were idle.
	Requests int64 `json:"requests"`
	// Tokens is the caller's own prompt+completion tokens in the window.
	Tokens int64 `json:"tokens"`
	// CostCents is the caller's own spend in whole US cents. Always populated — your
	// own spend is never withheld from you — so here 0 really does mean zero.
	CostCents int64 `json:"costCents"`
	// Metric is whichever of the three values above the board was ranked by, so a
	// client can compare the caller against the rows without re-reading the request.
	// Metric <= 0 is exactly the case that leaves Ranked false.
	Metric int64 `json:"metric"`
	// Handle is how the caller appears on this board: their chosen handle, falling back
	// to their username, on a user board; their org id on the global board. Present
	// even when unlisted — this is the caller looking at themselves.
	Handle string `json:"handle"`
	// Listed says whether the caller is publicly visible on this board: opted in on a
	// user board, org opted in (or the viewer is a platform admin) on the global one.
	// False is the prompt to offer the opt-in, and explains an unranked global self.
	Listed bool `json:"listed"`
}

// LeaderboardView is the whole leaderboard response.
type LeaderboardView struct {
	// Scope echoes the board that was served: personal|org|global.
	Scope string `json:"scope"`
	// Subject is what the rows stand for — "user" on a personal or org board, "org" on
	// the global one. It tells a client whether Handle names a person or a company.
	Subject string `json:"subject"`
	// Metric echoes the value ranked: tokens|requests|cost.
	Metric string `json:"metric"`
	// Period is the window's canonical label: day|week|month|all. The server resolves
	// aliases (7d, 30d, today, …) to these, so this may differ from what was sent.
	Period string `json:"period"`
	// Start is the first day counted, "2006-01-02" inclusive. Empty for period=all,
	// which has no lower bound at all.
	Start string `json:"start"`
	// End is the EXCLUSIVE upper bound of the window, "2006-01-02" — the day after the
	// last one counted. A board through today reports tomorrow's date here.
	End string `json:"end"`
	// Rows are the ranked subjects, best first, at most the requested limit of them.
	// Always a list, never null: an empty one means nothing was read, not an error.
	Rows []LeaderboardRow `json:"rows"`
	// Self is the caller's own standing, reported even when they fall outside Rows.
	// Absent when the caller's ledger identity cannot be resolved, or when the query
	// behind it failed — never faked to keep the shape tidy.
	Self *SelfRank `json:"self,omitempty"`
	// Total is how many subjects were ranked in the window — the org's active users, or
	// the active/opted-in orgs on the global board. It is the universe the ranks are
	// out of, so it is normally larger than len(rows).
	Total int64 `json:"total"`
	// Available is false when the usage warehouse is not connected or its rollup is not
	// ready. Rows is then empty because nothing could be read — not because nobody used
	// anything. Show that difference; never render an unavailable board as a real one.
	Available bool `json:"available"`
	// Source names the table these numbers were aggregated from (the derived daily
	// rollup, hanzo.usage_rollup_daily), so an operator can tell exactly what was read.
	Source string `json:"source"`
}

// ── Activity response ─────────────────────────────────────────────────────────

// ActivityPoint is one day of a subject's usage — the atom of the contribution
// heatmap + timeline. CostCents populated only when the viewer may see spend.
type ActivityPoint struct {
	// Day is the UTC calendar day this point covers, "2006-01-02".
	Day string `json:"day"`
	// Requests is the subject's request count on this day. 0 is a real, quiet day: the
	// series is gap-filled, so every day in the range is present whether or not
	// anything happened.
	Requests int64 `json:"requests"`
	// Tokens is prompt+completion tokens on this day — normally the heatmap's
	// intensity, scaled against ActivityTotals.MaxTokens.
	Tokens int64 `json:"tokens"`
	// CostCents is the day's spend in whole US cents. A series is only ever returned
	// for a subject the caller is authorized to see, so this is never withheld: 0 means
	// no spend that day.
	CostCents int64 `json:"costCents"`
}

// ActivityTotals are the window sums + heatmap-scaling hints.
type ActivityTotals struct {
	// Requests is the sum of Days[].Requests over the whole window.
	Requests int64 `json:"requests"`
	// Tokens is the sum of Days[].Tokens over the whole window.
	Tokens int64 `json:"tokens"`
	// CostCents is the window's spend in whole US cents, the sum of Days[].CostCents.
	CostCents int64 `json:"costCents"`
	// ActiveDays counts the days with any usage at all — the streak/consistency number.
	// Compare it against len(days) for the share of days the subject showed up.
	ActiveDays int `json:"activeDays"`
	// MaxTokens is the busiest single day's token count: the ceiling to normalize a
	// token heatmap against, so the darkest cell is that day. 0 for an idle window,
	// which a client must not divide by.
	MaxTokens int64 `json:"maxTokens"`
	// MaxRequests is the same ceiling for a request-based heatmap — the busiest single
	// day's request count, 0 for an idle window.
	MaxRequests int64 `json:"maxRequests"`
}

// ActivityView is the per-day series for one authorized subject.
type ActivityView struct {
	// Subject echoes what the series is about: user|org|project.
	Subject string `json:"subject"`
	// ID is the subject the server actually read, after resolving "me"/empty to the
	// caller and bounding it to what they may see — a ledger "owner/name" for a user,
	// an org id for an org. Echoed so a client can confirm whose series it holds.
	ID string `json:"id"`
	// From is the first day in Days, "2006-01-02" inclusive.
	From string `json:"from"`
	// To is the EXCLUSIVE upper bound, "2006-01-02" — the day AFTER the last point in
	// Days. A request for to=2026-03-31 answers to=2026-04-01 with 2026-03-31 last.
	To string `json:"to"`
	// Days is the gap-filled series, one point per calendar day from From up to (not
	// including) To, in ascending order, zero-valued days included. Always a list,
	// never null.
	Days []ActivityPoint `json:"days"`
	// Totals are the window's sums plus the busiest-day ceilings a heatmap scales
	// against. Derived from Days — nothing here is read separately.
	Totals ActivityTotals `json:"totals"`
	// Available is false when nothing could be read: the warehouse is not connected,
	// the rollup is not ready, or the subject is one the ledger cannot attribute (see
	// Note). Days is then empty because there is no answer, not because there was no
	// activity.
	Available bool `json:"available"`
	// Source names the table the series was aggregated from (the derived daily rollup,
	// hanzo.usage_rollup_daily).
	Source string `json:"source"`
	// Note explains an empty-but-not-broken answer in plain words — today only
	// subject=project, which the usage ledger records no column for. Present only when
	// there is something to say; show it instead of an empty chart.
	Note string `json:"note,omitempty"`
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
//
// The window total adds the DAYS, each already rounded to the cent by the query,
// because a calendar whose squares do not add up to the figure beside them is a bug
// report waiting to happen. That costs at most half a cent per day shown, against a
// row-level rounding's half cent per CALL — and the days and the total agree.
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
