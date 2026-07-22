package link

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// usage_test.go is the gate on the account-usage plane. The bar it holds:
//
//	ISOLATION   org+subject lead EVERY query as BOUND args; a co-tenant is
//	            unreachable, a blank tenancy fails closed.
//	IDEMPOTENCY a re-report of one window collapses to one row — asserted at the
//	            key (sample_test.go) AND at the engine/read contract here.
//	HONESTY     unavailable is not zero; a source is labelled; nothing is invented.
//
// LIVE COVERAGE, STATED PLAINLY: no datastore is reachable from this suite, so the
// DDL and the SQL are proven by CONTRACT (the exact statements, and the exact bound
// args, that the warehouse would receive) and not by execution. The dedup itself —
// that ClickHouse's ReplacingMergeTree collapses these keys and that argMax reads
// them back — is UNPROVEN LIVE and needs a warehouse smoke before the plane is
// trusted for anything that bills.

// ── isolation ────────────────────────────────────────────────────────────────

// argsLead asserts a query's first bound args are the tenancy, in order.
func argsLead(t *testing.T, what string, args []any, want ...any) {
	t.Helper()
	if len(args) < len(want) {
		t.Fatalf("%s: only %d args, want tenancy to lead with %v", what, len(args), want)
	}
	for i, w := range want {
		if args[i] != w {
			t.Fatalf("%s: arg %d = %v, want %v (tenancy MUST lead, bound)", what, i, args[i], w)
		}
	}
}

// TestQueriesAreTenantScoped: every read leads with org AND subject as BOUND
// parameters. This is the #1 property of the plane — a caller reads only their own
// accounts, within their own org.
func TestQueriesAreTenantScoped(t *testing.T) {
	from, to := now.Add(-24*time.Hour), now

	q, args := seriesQuery("acme", "alice", "claude", "", "", from, to)
	if !strings.Contains(q, "WHERE org = ? AND subject = ? AND provider = ?") {
		t.Fatalf("series WHERE must lead org+subject:\n%s", q)
	}
	argsLead(t, "series", args, "acme", "alice", "claude")

	q, args = summaryQuery("acme", "alice", from, to)
	if !strings.Contains(q, "WHERE org = ? AND subject = ?") {
		t.Fatalf("summary WHERE must lead org+subject:\n%s", q)
	}
	argsLead(t, "summary", args, "acme", "alice")

	q, args = hanzoQuery("acme", from, to)
	if !strings.Contains(q, "WHERE organization = ?") {
		t.Fatalf("hanzo WHERE must lead organization:\n%s", q)
	}
	argsLead(t, "hanzo", args, "acme")
}

// TestNoTenantValueIsInterpolated: a hostile org/subject/provider/account can never
// reach the SQL text — it is bound, always. If any of these appear in the statement
// itself, the plane is injectable.
func TestNoTenantValueIsInterpolated(t *testing.T) {
	evil := "acme' OR 1=1 --"
	from, to := now.Add(-time.Hour), now
	for _, tc := range []struct {
		name string
		q    string
	}{
		{"series/org", first(seriesQuery(evil, "alice", "claude", "", "", from, to))},
		{"series/subject", first(seriesQuery("acme", evil, "claude", "", "", from, to))},
		{"series/provider", first(seriesQuery("acme", "alice", evil, "", "", from, to))},
		{"series/account", first(seriesQuery("acme", "alice", "claude", evil, "", from, to))},
		{"series/window", first(seriesQuery("acme", "alice", "claude", "", evil, from, to))},
		{"summary/org", first(summaryQuery(evil, "alice", from, to))},
		{"summary/subject", first(summaryQuery("acme", evil, from, to))},
		{"hanzo/org", first(hanzoQuery(evil, from, to))},
	} {
		if strings.Contains(tc.q, evil) || strings.Contains(tc.q, "1=1") {
			t.Fatalf("%s: a caller value reached the SQL TEXT — injectable:\n%s", tc.name, tc.q)
		}
	}
}

// TestReadsFailClosedOnBlankTenancy: a blank org or subject must never reach the
// warehouse at all. If it did, the WHERE would match rows with a blank tenant
// rather than none.
func TestReadsFailClosedOnBlankTenancy(t *testing.T) {
	st := &Store{}
	ctx := t.Context()
	for _, tc := range []struct{ org, subject string }{
		{"", "alice"}, {"acme", ""}, {"", ""},
	} {
		if _, ok := st.Series(ctx, tc.org, tc.subject, "claude", "", "", now.Add(-time.Hour), now); ok {
			t.Fatalf("Series(%q,%q) must fail closed", tc.org, tc.subject)
		}
		if _, ok := st.AccountTotals(ctx, tc.org, tc.subject, now.Add(-time.Hour), now); ok {
			t.Fatalf("AccountTotals(%q,%q) must fail closed", tc.org, tc.subject)
		}
		if err := st.WriteSamples(ctx, tc.org, tc.subject, []Sample{{Provider: "claude"}}, now); err == nil {
			t.Fatalf("WriteSamples(%q,%q) must fail closed", tc.org, tc.subject)
		}
	}
	if _, ok := st.HanzoTotals(ctx, "", now.Add(-time.Hour), now); ok {
		t.Fatal("HanzoTotals(\"\") must fail closed")
	}
}

// TestFailClosedNoPrincipalOnUsage: an org header with NO validated user (the
// off-gateway forge) is refused on every usage route — read AND write.
func TestFailClosedNoPrincipalOnUsage(t *testing.T) {
	app := mountLink(t)
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/links/usage?provider=claude", nil},
		{http.MethodGet, "/v1/links/usage/summary", nil},
		{http.MethodPost, "/v1/links/usage", map[string]any{
			"provider": "claude", "machine": "m1", "window": "6h", "usedPct": 42}},
	} {
		if code, _ := req(t, app, tc.method, tc.path, "acme", "", tc.body); code != http.StatusForbidden {
			t.Fatalf("%s %s with no validated principal want 403, got %d", tc.method, tc.path, code)
		}
	}
}

// TestReportIsolatesTenants drives the DURABLE half end-to-end: two orgs and two
// users report usage for the SAME provider+account+machine. Each must get their
// OWN Link, and no one may see another's. (The warehouse half of the same isolation
// is held by TestQueriesAreTenantScoped, since no datastore is reachable here.)
func TestReportIsolatesTenants(t *testing.T) {
	app := mountLink(t)
	body := map[string]any{
		"provider": "claude", "account": "shared@x", "machine": "m1",
		"window": "6h", "lane": "five_hour", "windowMinutes": 300,
		"usedPct": 42, "confidence": "percentOnly", "plan": "Claude Max",
	}
	for _, who := range []struct{ org, user string }{
		{"acme", "alice"}, {"acme", "bob"}, {"evil", "mallory"},
	} {
		if code, b := req(t, app, http.MethodPost, "/v1/links/usage", who.org, who.user, body); code != http.StatusAccepted {
			t.Fatalf("%s/%s report want 202, got %d (%s)", who.org, who.user, code, b)
		}
	}
	// Each identity sees exactly ONE link: their own.
	for _, who := range []struct{ org, user string }{
		{"acme", "alice"}, {"acme", "bob"}, {"evil", "mallory"},
	} {
		code, b := req(t, app, http.MethodGet, "/v1/links", who.org, who.user, nil)
		if code != http.StatusOK {
			t.Fatalf("list %s/%s want 200, got %d", who.org, who.user, code)
		}
		var got struct {
			Links []linkView `json:"links"`
		}
		_ = json.Unmarshal(b, &got)
		if len(got.Links) != 1 {
			t.Fatalf("%s/%s sees %d links, want exactly its own 1 — cross-scope leak",
				who.org, who.user, len(got.Links))
		}
		if got.Links[0].User != who.user {
			t.Fatalf("%s/%s got another subject's link (%q)", who.org, who.user, got.Links[0].User)
		}
	}
}

// ── idempotency: the engine + read contract ──────────────────────────────────

// TestDedupKeyIsTheWindowInstanceNotTheClock is the schema half of the idempotency
// gate, and it pins the trap: the dedup key must be the WINDOW INSTANCE, and `ts`
// must be the VERSION, never part of the key. Keying on the observation clock would
// give every poll a distinct key, collapse nothing, and multiply every total by the
// poll rate.
func TestDedupKeyIsTheWindowInstanceNotTheClock(t *testing.T) {
	ddl := accountUsageDDL[0]
	if !strings.Contains(ddl, "ENGINE = ReplacingMergeTree(ts)") {
		t.Fatalf("the base table must REPLACE re-reports, versioned by ts:\n%s", ddl)
	}
	const key = "ORDER BY (org, subject, provider, account, lane, window_start)"
	if !strings.Contains(ddl, key) {
		t.Fatalf("dedup key must be %s:\n%s", key, ddl)
	}
	// ts in the key would defeat the engine entirely.
	if strings.Contains(ddl, "ORDER BY") && strings.Contains(orderByOf(ddl), "ts") {
		t.Fatalf("ts must NOT be in the dedup key — every poll would key a new row:\n%s", orderByOf(ddl))
	}
	// machine in the key would store one account-wide window once per device.
	if strings.Contains(orderByOf(ddl), "machine") {
		t.Fatalf("machine must NOT be in the dedup key — a plan's quota is the ACCOUNT's, not the device's:\n%s", orderByOf(ddl))
	}
	// Partitioning by the observation clock would put a re-report in a different
	// partition from the original, where ReplacingMergeTree can never collapse it.
	if !strings.Contains(ddl, "PARTITION BY toYYYYMM(window_start)") {
		t.Fatalf("must partition by window_start, or re-reports across a month boundary never collapse:\n%s", ddl)
	}
}

// orderByOf extracts the ORDER BY clause for precise assertions.
func orderByOf(ddl string) string {
	i := strings.Index(ddl, "ORDER BY")
	if i < 0 {
		return ""
	}
	rest := ddl[i:]
	if j := strings.Index(rest, "\n"); j > 0 {
		return rest[:j]
	}
	return rest
}

// TestReadsDedupExplicitly: ReplacingMergeTree collapses only when parts merge, on
// the server's own schedule — so a read issued before that merge SEES the
// duplicates. Correctness may never be delegated to the engine: the reader must
// dedup with argMax over the same key.
func TestReadsDedupExplicitly(t *testing.T) {
	q, _ := seriesQuery("acme", "alice", "claude", "", "", now.Add(-time.Hour), now)
	if !strings.Contains(q, "argMax(") {
		t.Fatalf("the series read must dedup with argMax — the engine's merge is not load-bearing:\n%s", q)
	}
	const group = "GROUP BY org, subject, provider, account, lane, window_start"
	if !strings.Contains(q, group) {
		t.Fatalf("the read must group by the dedup key EXACTLY (%s):\n%s", group, q)
	}
}

// TestSummarySumsDedupedRowsNotPolls is the sharpest read gate: the sum must be
// OUTSIDE and the dedup INSIDE. Summing the raw table would multiply every total by
// the poll rate — the same fact counted once per poll.
func TestSummarySumsDedupedRowsNotPolls(t *testing.T) {
	q, _ := summaryQuery("acme", "alice", now.Add(-24*time.Hour), now)
	inner := strings.Index(q, "argMax(")
	outer := strings.Index(q, "sum(")
	if inner < 0 || outer < 0 {
		t.Fatalf("summary must dedup (argMax) and then sum:\n%s", q)
	}
	if outer > inner {
		t.Fatalf("sum() must wrap the deduped subquery, not the raw table:\n%s", q)
	}
	if !strings.Contains(q, "GROUP BY provider, window") {
		t.Fatalf("summary must group per (provider, window) — window classes NEST, so summing across them double-counts:\n%s", q)
	}
}

// TestRollupIsDedupPreserving: a materialized view is an INSERT TRIGGER, not a view
// of the merged table — it sees every poll before ReplacingMergeTree collapses
// anything. So sumState() here would inflate a day's tokens by the poll rate and
// the base table could never undo it. The rollup must use dedup-preserving
// aggregates.
func TestRollupIsDedupPreserving(t *testing.T) {
	mv := accountUsageDDL[2]
	if strings.Contains(mv, "sumState(") {
		t.Fatalf("sumState in the rollup sums every POLL — it must be argMaxState:\n%s", mv)
	}
	for _, want := range []string{
		"argMaxState(total_tokens, ts)", "argMaxState(requests, ts)",
		"argMaxState(cost_cents, ts)", "maxState(used_pct)",
	} {
		if !strings.Contains(mv, want) {
			t.Fatalf("the rollup must carry %s:\n%s", want, mv)
		}
	}
	if !strings.Contains(mv, "GROUP BY org, subject, provider, account, day, lane, `window`, window_start") {
		t.Fatalf("the rollup must keep window-instance granularity, or its sums cannot be honest:\n%s", mv)
	}
	// The target's aggregate columns must match the MV's state functions.
	if !strings.Contains(accountUsageDDL[1], "AggregateFunction(argMax, UInt64, DateTime64(3, 'UTC'))") ||
		!strings.Contains(accountUsageDDL[1], "AggregateFunction(max, Float64)") {
		t.Fatalf("the rollup target's types must match the MV's states:\n%s", accountUsageDDL[1])
	}
}

// TestReservedWordIsQuoted: `window` is a ClickHouse keyword; every statement that
// names the column must quote it or the DDL and the reads fail at parse.
func TestReservedWordIsQuoted(t *testing.T) {
	for i, stmt := range accountUsageDDL {
		for _, bad := range []string{"\n  window ", "(window,", " window,"} {
			if strings.Contains(stmt, bad) {
				t.Fatalf("ddl[%d] names `window` unquoted (%q) — it is a reserved word:\n%s", i, bad, stmt)
			}
		}
	}
	if !strings.Contains(accountUsageInsert, "`window`") {
		t.Fatalf("the insert must quote `window`:\n%s", accountUsageInsert)
	}
	q, _ := seriesQuery("acme", "alice", "claude", "", "6h", now.Add(-time.Hour), now)
	if !strings.Contains(q, "`window` = ?") {
		t.Fatalf("the window filter must quote the column:\n%s", q)
	}
}

// TestInsertBindsEveryColumn: the placeholder count must equal the column count, or
// values land in the wrong columns.
func TestInsertBindsEveryColumn(t *testing.T) {
	cols := strings.Count(accountUsageInsert[:strings.Index(accountUsageInsert, "VALUES")], ",") + 1
	vals := strings.Count(accountUsageInsert[strings.Index(accountUsageInsert, "VALUES"):], "?")
	if cols != vals {
		t.Fatalf("insert has %d columns but %d placeholders", cols, vals)
	}
}

// ── fail-soft ────────────────────────────────────────────────────────────────

// TestReportSucceedsWithoutTheWarehouse is the availability gate: with NO datastore
// (this suite's real condition), a report must still be accepted and the Link row —
// the durable truth the accounts overview renders — must still be refreshed. Losing
// a poll of history may never fail a report or block a device.
func TestReportSucceedsWithoutTheWarehouse(t *testing.T) {
	app := mountLink(t)
	code, b := req(t, app, http.MethodPost, "/v1/links/usage", "acme", "alice", map[string]any{
		"provider": "claude", "account": "alice@x", "machine": "m1", "plan": "Claude Max",
		"kind": "subscription", "window": "6h", "lane": "five_hour", "windowMinutes": 300,
		"usedPct": 47.5, "confidence": "percentOnly",
	})
	if code != http.StatusAccepted {
		t.Fatalf("report want 202 with no warehouse, got %d (%s)", code, b)
	}
	var got reportResp
	_ = json.Unmarshal(b, &got)
	if got.Accepted != 1 {
		t.Fatalf("accepted = %d, want 1", got.Accepted)
	}
	// Honest: it says the history was NOT stored rather than implying it was.
	if got.Stored {
		t.Fatal("stored must be false with no warehouse — the report must not claim history it does not have")
	}
	if len(got.Links) != 1 {
		t.Fatalf("the report must refresh the Link row, got %d links", len(got.Links))
	}

	// The durable truth is live and current, with the snapshot the dash renders.
	code, b = req(t, app, http.MethodGet, "/v1/links", "acme", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d", code)
	}
	var list struct {
		Links []linkView `json:"links"`
	}
	_ = json.Unmarshal(b, &list)
	if len(list.Links) != 1 || list.Links[0].Provider != "claude" {
		t.Fatalf("the accounts overview must show the reported account: %+v", list.Links)
	}
	if list.Links[0].Status != StatusLinked || list.Links[0].LastSeen == "" {
		t.Fatalf("the report must mark the account live + seen: %+v", list.Links[0])
	}
	var u Usage
	if err := json.Unmarshal(list.Links[0].Usage, &u); err != nil {
		t.Fatalf("usage snapshot: %v (%s)", err, list.Links[0].Usage)
	}
	if u.SessionPct != 47.5 {
		t.Fatalf("the 6h lane must drive sessionPct (headroom), got %v", u.SessionPct)
	}
	if u.Confidence != ConfidencePercentOnly {
		t.Fatalf("confidence must survive to the snapshot, got %q", u.Confidence)
	}
	// A subscription account bills the PLAN — a usage report never creates a charge.
	if list.Links[0].Billing != BillingPlan {
		t.Fatalf("a subscription must bill the plan, got %q", list.Links[0].Billing)
	}
}

// TestDashIsUnavailableNotEmpty: with no warehouse the dash must say so. Reporting
// `available:false` is the difference between "we cannot tell you" and the lie "you
// used nothing".
func TestDashIsUnavailableNotEmpty(t *testing.T) {
	app := mountLink(t)
	code, b := req(t, app, http.MethodGet, "/v1/links/usage?provider=claude", "acme", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("dash want 200, got %d (%s)", code, b)
	}
	var got dashResp
	_ = json.Unmarshal(b, &got)
	if got.Available {
		t.Fatal("available must be false with no warehouse")
	}
	if got.Source != SourceAccount || got.Scope != ScopeUser {
		t.Fatalf("the dash must label its source+scope: %+v", got)
	}
	if got.Windows == nil || got.Current == nil {
		t.Fatal("empty collections must serialize as [] , never null")
	}
}

// ── the global view ──────────────────────────────────────────────────────────

// TestSummaryLabelsItsSources is the honesty gate on the union: the two ledgers mean
// different things, so every row must say which it came from and whose usage it
// covers — and the response must report each side's availability independently, so
// half a warehouse never turns the other half into zeros.
func TestSummaryLabelsItsSources(t *testing.T) {
	app := mountLink(t)
	code, b := req(t, app, http.MethodGet, "/v1/links/usage/summary?range=7d", "acme", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("summary want 200, got %d (%s)", code, b)
	}
	var got summaryResp
	_ = json.Unmarshal(b, &got)
	if got.Range != "7d" {
		t.Fatalf("range = %q, want 7d", got.Range)
	}
	if got.Account.Scope != ScopeUser || got.Account.Source != accountUsageTable {
		t.Fatalf("the account side must be labelled user-scoped: %+v", got.Account)
	}
	if got.Hanzo.Scope != ScopeOrg || got.Hanzo.Source != cloudUsageTable {
		t.Fatalf("the hanzo side must be labelled org-scoped: %+v", got.Hanzo)
	}
	if got.Account.Available || got.Hanzo.Available {
		t.Fatal("both sides must report unavailable with no warehouse")
	}
	if got.Rows == nil {
		t.Fatal("rows must serialize as [], never null")
	}
}

// TestSourcesAreStampedServerSide: the source and scope of a row are facts about
// WHICH ledger answered — never data from it. A row from the account meter can never
// present itself as Hanzo's cost of record, even if the warehouse returns a column
// saying so.
func TestSourcesAreStampedServerSide(t *testing.T) {
	hostile := map[string]any{
		"provider": "claude", "source": SourceHanzo, "scope": ScopeOrg,
		"total_tokens": uint64(10), "cost_cents": uint64(999), "confidence_rank": uint8(0),
	}
	acct := accountTotalOf(hostile)
	if acct.Source != SourceAccount || acct.Scope != ScopeUser {
		t.Fatalf("an account row must be stamped account/user, got %s/%s", acct.Source, acct.Scope)
	}
	hz := hanzoTotalOf(map[string]any{"provider": "anthropic", "requests": uint64(5)})
	if hz.Source != SourceHanzo || hz.Scope != ScopeOrg {
		t.Fatalf("a hanzo row must be stamped hanzo/org, got %s/%s", hz.Source, hz.Scope)
	}
	if hz.Confidence != ConfidenceExact {
		t.Fatalf("cloud_usage counts calls we billed — it is exact, got %q", hz.Confidence)
	}
	// A Hanzo row carries NO window: cloud_usage is a per-call ledger, not a window
	// meter. Inventing a class would invent a quota.
	if hz.Window != "" {
		t.Fatalf("a hanzo row must not claim a window class, got %q", hz.Window)
	}
}

// TestBothSidesShareOneRange: the union compares one period. Two resolvers could
// drift and silently compare a day of plan usage against a week of spend.
func TestBothSidesShareOneRange(t *testing.T) {
	for _, label := range []string{"1h", "24h", "7d", "30d", ""} {
		from, to, err := resolveRange(label, now)
		if err != nil {
			t.Fatalf("range %q: %v", label, err)
		}
		_, aArgs := summaryQuery("acme", "alice", from, to)
		_, hArgs := hanzoQuery("acme", from, to)
		// The account side binds instants; the Hanzo side binds the datastore's
		// literal form of the SAME instants.
		if aArgs[2] != from.UTC() || aArgs[3] != to.UTC() {
			t.Fatalf("account side lost the range: %v", aArgs)
		}
		if hArgs[1] != dsTime(from) || hArgs[2] != dsTime(to) {
			t.Fatalf("hanzo side lost the range: %v", hArgs)
		}
	}
}

// TestUnknownRangeIsRefused: an unknown range must be an error, never a silent
// default — a caller who asked for a window we do not have must be told, not shown
// a different one and left to believe it.
func TestUnknownRangeIsRefused(t *testing.T) {
	for _, bad := range []string{"90d", "1y", "all", "custom", "1h; DROP TABLE", "24H"} {
		if _, _, err := resolveRange(bad, now); err == nil {
			t.Fatalf("range %q must be refused", bad)
		}
	}
	app := mountLink(t)
	if code, _ := req(t, app, http.MethodGet, "/v1/links/usage/summary?range=90d", "acme", "alice", nil); code != http.StatusBadRequest {
		t.Fatalf("an unknown range want 400, got %d", code)
	}
}

// ── the boundary ─────────────────────────────────────────────────────────────

// TestReportRejectsUnknownVocabulary: the closed vocabularies are 400s, never
// silently rewritten — a caller whose window we did not understand must be told, or
// their dash quietly fills with a class they never reported.
func TestReportRejectsUnknownVocabulary(t *testing.T) {
	app := mountLink(t)
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"no provider", map[string]any{"machine": "m1", "window": "6h"}},
		{"no machine", map[string]any{"provider": "claude", "window": "6h"}},
		{"no window", map[string]any{"provider": "claude", "machine": "m1"}},
		{"legacy window", map[string]any{"provider": "claude", "machine": "m1", "window": "weekly"}},
		{"cased window", map[string]any{"provider": "claude", "machine": "m1", "window": "Week"}},
		{"5h window", map[string]any{"provider": "claude", "machine": "m1", "window": "5h"}},
		{"bad kind", map[string]any{"provider": "claude", "machine": "m1", "window": "6h", "kind": "root"}},
		{"bad windowStart", map[string]any{"provider": "claude", "machine": "m1", "window": "6h", "windowStart": "yesterday"}},
		{"bad resetsAt", map[string]any{"provider": "claude", "machine": "m1", "window": "6h", "resetsAt": "soon"}},
	} {
		if code, b := req(t, app, http.MethodPost, "/v1/links/usage", "acme", "alice", tc.body); code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d (%s)", tc.name, code, b)
		}
	}
	if code, _ := req(t, app, http.MethodGet, "/v1/links/usage?provider=claude&window=weekly", "acme", "alice", nil); code != http.StatusBadRequest {
		t.Fatalf("dash with a non-canonical window want 400, got %d", code)
	}
	if code, _ := req(t, app, http.MethodGet, "/v1/links/usage", "acme", "alice", nil); code != http.StatusBadRequest {
		t.Fatalf("dash with no provider want 400, got %d", code)
	}
}

// TestBatchIsBounded: one report is capped, so a hostile client cannot bloat the
// warehouse in a single call.
func TestBatchIsBounded(t *testing.T) {
	app := mountLink(t)
	batch := make([]map[string]any, 0, maxSamples+1)
	for i := 0; i <= maxSamples; i++ {
		batch = append(batch, map[string]any{
			"provider": "claude", "machine": "m1", "window": "6h",
			"lane": fmt.Sprintf("lane-%d", i), "usedPct": 1,
		})
	}
	if code, _ := req(t, app, http.MethodPost, "/v1/links/usage", "acme", "alice",
		map[string]any{"samples": batch}); code != http.StatusBadRequest {
		t.Fatalf("an over-cap batch want 400, got %d", code)
	}
	if code, _ := req(t, app, http.MethodPost, "/v1/links/usage", "acme", "alice",
		map[string]any{"samples": []map[string]any{}}); code != http.StatusBadRequest {
		t.Fatalf("an empty batch want 400, got %d", code)
	}
}

// TestReportAcceptsOneOrMany: a poller reports its lanes in ONE call; a simple
// client posts one sample. Both are the same route.
func TestReportAcceptsOneOrMany(t *testing.T) {
	app := mountLink(t)
	// Claude's real shape: four lanes, three of them the SAME 10080 minutes — they
	// must survive as three distinct meters.
	code, b := req(t, app, http.MethodPost, "/v1/links/usage", "acme", "alice", map[string]any{
		"samples": []map[string]any{
			{"provider": "claude", "account": "a@x", "machine": "m1", "window": "6h",
				"lane": "five_hour", "windowMinutes": 300, "usedPct": 47, "confidence": "percentOnly"},
			{"provider": "claude", "account": "a@x", "machine": "m1", "window": "week",
				"lane": "seven_day", "windowMinutes": 10080, "usedPct": 12, "confidence": "percentOnly"},
			{"provider": "claude", "account": "a@x", "machine": "m1", "window": "week",
				"lane": "seven_day_sonnet", "windowMinutes": 10080, "usedPct": 30, "confidence": "percentOnly"},
			{"provider": "claude", "account": "a@x", "machine": "m1", "window": "week",
				"lane": "seven_day_opus", "windowMinutes": 10080, "usedPct": 80, "confidence": "percentOnly"},
		},
	})
	if code != http.StatusAccepted {
		t.Fatalf("batch want 202, got %d (%s)", code, b)
	}
	var got reportResp
	_ = json.Unmarshal(b, &got)
	if got.Accepted != 4 {
		t.Fatalf("accepted = %d, want 4", got.Accepted)
	}
	// Four lanes on ONE account fold into ONE Link — the identity is the account.
	if len(got.Links) != 1 {
		t.Fatalf("four lanes of one account must refresh ONE link, got %d", len(got.Links))
	}
}

// TestLanesSharingAWindowKeepDistinctKeys is the collision gate. Claude's
// seven_day, seven_day_sonnet and seven_day_opus are three DIFFERENT meters at the
// SAME 10080 minutes. Keyed by window class they would collapse onto each other and
// two of the three would be silently overwritten; only `lane` tells them apart.
func TestLanesSharingAWindowKeepDistinctKeys(t *testing.T) {
	resets := now.Add(3 * 24 * time.Hour)
	keys := map[string]bool{}
	for _, lane := range []string{"seven_day", "seven_day_sonnet", "seven_day_opus"} {
		s := Sample{Provider: "claude", Account: "a@x", Machine: "m1", Window: WindowWeek,
			Lane: lane, WindowMinutes: 10080, ResetsAt: resets}.Sanitize(now)
		// The dedup key is (…, lane, window_start): same instant, distinct lanes.
		k := s.Lane + "|" + s.WindowStart.String()
		if keys[k] {
			t.Fatalf("lane %q collided — two of Claude's three weekly meters would be lost", lane)
		}
		keys[k] = true
	}
	if len(keys) != 3 {
		t.Fatalf("want 3 distinct keys, got %d", len(keys))
	}
}

// ── projections ──────────────────────────────────────────────────────────────

// TestSnapshotNeverSumsAcrossWindows: window classes NEST — a 6h lane's tokens are
// also inside the week lane's. The snapshot takes the widest single lane, never a
// sum, or an account's tokens inflate with every extra lane its meter reports.
func TestSnapshotNeverSumsAcrossWindows(t *testing.T) {
	group := []Sample{
		{Window: Window6h, Lane: "five_hour", UsedPct: 47, TotalTokens: 1000, Confidence: ConfidenceExact},
		{Window: WindowWeek, Lane: "seven_day", UsedPct: 12, TotalTokens: 9000, Confidence: ConfidenceExact},
	}
	var u Usage
	if err := json.Unmarshal([]byte(snapshotOf(group)), &u); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if u.Tokens != 9000 {
		t.Fatalf("tokens = %d, want the WIDEST lane's 9000 — never the 10000 sum", u.Tokens)
	}
	if u.SessionPct != 47 || u.WeeklyPct != 12 {
		t.Fatalf("each percent must come from its own lane: %+v", u)
	}
	// headroomPct (the route policy) reads the tighter lane — 100-47.
	if h := headroomPct(&u); h != 53 {
		t.Fatalf("headroom = %v, want 53", h)
	}
}

// TestSnapshotKeepsNumbersAndFlagTogether: a count and the confidence that
// qualifies it must come from ONE meter, or the console trusts a number the flag
// never covered.
func TestSnapshotKeepsNumbersAndFlagTogether(t *testing.T) {
	group := []Sample{
		{Window: Window6h, Lane: "five_hour", UsedPct: 47, Confidence: ConfidencePercentOnly},
		{Window: WindowMonth, Lane: "month", TotalTokens: 500, CostCents: 250,
			Currency: "USD", Confidence: ConfidenceExact},
	}
	var u Usage
	_ = json.Unmarshal([]byte(snapshotOf(group)), &u)
	if u.Tokens != 500 || u.SpendCents != 250 || u.Currency != "USD" {
		t.Fatalf("counters must come from the widest lane: %+v", u)
	}
	if u.Confidence != ConfidenceExact {
		t.Fatalf("confidence must be the widest lane's own (%q), got %q", ConfidenceExact, u.Confidence)
	}
}

// TestCurrentIsTheNewestInstancePerLane: the dash headline is each lane's live
// state — the newest instance, one per lane.
func TestCurrentIsTheNewestInstancePerLane(t *testing.T) {
	// Rows arrive newest-first, as the read orders them.
	rows := []Sample{
		{Account: "a@x", Lane: "five_hour", Window: Window6h, WindowStart: now, UsedPct: 47},
		{Account: "a@x", Lane: "seven_day", Window: WindowWeek, WindowStart: now, UsedPct: 12},
		{Account: "a@x", Lane: "five_hour", Window: Window6h, WindowStart: now.Add(-6 * time.Hour), UsedPct: 99},
	}
	got := currentOf(rows)
	if len(got) != 2 {
		t.Fatalf("current must be one row per lane, got %d", len(got))
	}
	if got[0].Lane != "five_hour" || got[0].UsedPct != 47 {
		t.Fatalf("current must be the NEWEST instance (47%%), got %+v", got[0])
	}
	// Ordered by window breadth, so a dash renders 6h then week.
	if windowRank(got[0].Window) > windowRank(got[1].Window) {
		t.Fatalf("current must order by window breadth: %+v", got)
	}
}

// TestPercentOnlyKeepsItsZeros: Claude reports percentOnly and NO tokens. Those
// zeros mean UNKNOWN, and the flag is what says so — the view must carry it, and
// must not invent counters.
func TestPercentOnlyKeepsItsZeros(t *testing.T) {
	v := toSampleView(Sample{
		Provider: "claude", Lane: "five_hour", Window: Window6h, WindowMinutes: 300,
		UsedPct: 47.5, Confidence: ConfidencePercentOnly,
	})
	if v.Confidence != ConfidencePercentOnly {
		t.Fatalf("the flag must survive to the wire, got %q", v.Confidence)
	}
	b, _ := json.Marshal(v)
	// omitempty drops the unknown counters entirely rather than asserting 0.
	for _, absent := range []string{"totalTokens", "requests", "costCents"} {
		if strings.Contains(string(b), absent) {
			t.Fatalf("%s must be OMITTED when unknown, not sent as 0: %s", absent, b)
		}
	}
	if !strings.Contains(string(b), `"usedPct":47.5`) {
		t.Fatalf("the one real number must be present: %s", b)
	}
	// windowMinutes carries the TRUTH (300 = 5h), never the class's nominal 360.
	if v.WindowMinutes != 300 {
		t.Fatalf("windowMinutes must be the meter's own 300, got %d", v.WindowMinutes)
	}
}

// ── the absent instant ───────────────────────────────────────────────────────

// TestAbsentInstantIsStorable is the gate on a bug that would have corrupted almost
// every row: Go's zero time is YEAR 1, which a DateTime64 column cannot represent —
// and an absent resets_at is the COMMON case, since most meters report no boundary.
// Binding the zero time would break or silently corrupt the insert. Absence must be
// the epoch sentinel on the way in, and absent again on the way out — never a date.
func TestAbsentInstantIsStorable(t *testing.T) {
	if got := dsInstant(time.Time{}); !got.Equal(epoch) {
		t.Fatalf("an absent instant must store as the epoch, got %s (year %d)", got, got.Year())
	}
	if y := dsInstant(time.Time{}).Year(); y < 1900 {
		t.Fatalf("year %d is outside DateTime64 — the insert would break", y)
	}
	real := now.Add(time.Hour)
	if got := dsInstant(real); !got.Equal(real.UTC()) {
		t.Fatalf("a real instant must pass through, got %s", got)
	}
	// And it round-trips back to ABSENT, so the wire omits it rather than claiming
	// a window reset in 1970.
	if got := dsTimeOf(epoch); !got.IsZero() {
		t.Fatalf("the epoch must read back as absent, got %s", got)
	}
	if got := dsTimeOf(real); !got.Equal(real.UTC()) {
		t.Fatalf("a real instant must read back, got %s", got)
	}
	if v := toSampleView(Sample{Lane: "l", Window: Window6h}); v.ResetsAt != "" || v.WindowStart != "" {
		t.Fatalf("an absent instant must be omitted on the wire, got %+v", v)
	}
}

// TestEveryValidWindowKeysARepresentableInstant: a sample whose meter reported no
// duration and no reset must STILL key a storable instant — via the class's nominal
// length — or it would land on the zero instant and break the insert.
func TestEveryValidWindowKeysARepresentableInstant(t *testing.T) {
	for _, w := range []string{Window6h, WindowDay, WindowWeek, WindowMonth} {
		got := Sample{Provider: "hanzo", Machine: "m1", Window: w}.Sanitize(now)
		if got.WindowStart.IsZero() {
			t.Fatalf("window %q keyed the zero instant — the insert would break", w)
		}
		if got.WindowStart.Year() < 1900 {
			t.Fatalf("window %q keyed year %d — outside DateTime64", w, got.WindowStart.Year())
		}
		// The nominal is a derivation aid ONLY: it must never be reported as the
		// meter's own measurement.
		if got.WindowMinutes != 0 {
			t.Fatalf("window %q invented windowMinutes=%d — the meter never said that",
				w, got.WindowMinutes)
		}
	}
	// A meter that DOES report its duration keeps the truth: Claude's 6h-class lane
	// really is 300 minutes, and the nominal 360 must never overwrite it.
	got := Sample{Provider: "claude", Machine: "m1", Window: Window6h, WindowMinutes: 300}.Sanitize(now)
	if got.WindowMinutes != 300 {
		t.Fatalf("the meter's 300 was overwritten with the class nominal: %d", got.WindowMinutes)
	}
}

// ── confidence ───────────────────────────────────────────────────────────────

// TestWeakestConfidenceWins: an aggregate over lanes may only be as trustworthy as
// its WEAKEST part. A sum whose parts were partly unknown is not exact — it is
// missing whatever those lanes never reported.
func TestWeakestConfidenceWins(t *testing.T) {
	// Ranked by belief: exact is best, unknown is worst.
	if !(confidenceRank(ConfidenceExact) < confidenceRank(ConfidenceEstimated) &&
		confidenceRank(ConfidenceEstimated) < confidenceRank(ConfidencePercentOnly) &&
		confidenceRank(ConfidencePercentOnly) < confidenceRank(ConfidenceUnknown)) {
		t.Fatal("confidence must rank by how much a reader may believe it")
	}
	// An unrecognised value is the WORST, never accidentally trusted.
	if confidenceRank("totally-legit") != confidenceRank(ConfidenceUnknown) {
		t.Fatal("an unrecognised confidence must rank as unknown")
	}
	for _, c := range []string{ConfidenceExact, ConfidenceEstimated, ConfidencePercentOnly, ConfidenceUnknown} {
		if got := confidenceOfRank(int64(confidenceRank(c))); got != c {
			t.Fatalf("rank round-trip: %q → %q", c, got)
		}
	}
	if got := confidenceOfRank(99); got != ConfidenceUnknown {
		t.Fatalf("an out-of-range rank must decode as unknown, got %q", got)
	}
}

// TestConfidenceRankExprIsBuiltFromConstants: the warehouse's ranking is GENERATED
// from the Go one, so the two can never drift — a drift would still run and just
// rank wrongly, invisibly. It also pins that only this package's own constants are
// interpolated: no caller value reaches the expression.
func TestConfidenceRankExprIsBuiltFromConstants(t *testing.T) {
	got := confidenceRankExpr
	for _, c := range []string{ConfidenceExact, ConfidenceEstimated, ConfidencePercentOnly} {
		want := fmt.Sprintf("confidence = '%s', %d", c, confidenceRank(c))
		if !strings.Contains(got, want) {
			t.Fatalf("the expression must rank %q as %d:\n%s", c, confidenceRank(c), got)
		}
	}
	if !strings.HasPrefix(got, "multiIf(") || !strings.HasSuffix(got, fmt.Sprintf("%d)", confidenceRank(ConfidenceUnknown))) {
		t.Fatalf("malformed rank expression: %s", got)
	}
	// The summary must aggregate on the RANK, never on the string — alphabetical
	// order over confidence values is a coincidence, not a semantic.
	q, _ := summaryQuery("acme", "alice", now.Add(-time.Hour), now)
	if !strings.Contains(q, "max("+confidenceRankExpr+")") {
		t.Fatalf("summary must take max() of the RANK:\n%s", q)
	}
	if strings.Contains(q, "max(confidence)") {
		t.Fatalf("summary must not order confidence STRINGS alphabetically:\n%s", q)
	}
}

// TestAccountTotalDecodesTheWeakestConfidence closes the loop from the query's rank
// back to a labelled row.
func TestAccountTotalDecodesTheWeakestConfidence(t *testing.T) {
	for rank, want := range map[uint8]string{
		0: ConfidenceExact, 1: ConfidenceEstimated,
		2: ConfidencePercentOnly, 3: ConfidenceUnknown,
	} {
		got := accountTotalOf(map[string]any{"provider": "claude", "confidence_rank": rank})
		if got.Confidence != want {
			t.Fatalf("rank %d → %q, want %q", rank, got.Confidence, want)
		}
	}
}

// first returns a query builder's SQL, dropping its args.
func first(q string, _ []any) string { return q }
