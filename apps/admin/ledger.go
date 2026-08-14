package admin

// ledger.go — hanzo.cloud_usage, read once for every board that asks about AI usage.
//
// The ledger is one row per served request: whose it was, which model, how many tokens,
// what it cost. Four boards ask it four questions — the fleet total, the daily curve,
// the split by tenant, the split by model — and until now three of them spelled the
// table name themselves and two carried their own copy of the same aggregate. That is
// the shape the o11y/aimetrics comment already warns about: one fact stated twice
// drifts, and a wrong column does not raise here — the caller's `err == nil` swallows
// it and the board renders zeros that look like a quiet fleet.
//
// So the ledger is stated once. A scope carries the window and, optionally, the tenant;
// a builder turns a scope into a QUERY — the statement and the arguments it binds, in
// one value, so the two cannot be passed around separately and drift. Everything
// interpolated is a server-side constant (the bucket interval, a row cap); every value
// a caller supplies is bound.
//
// The overview and the tenant directory used to answer this question from the money
// plane and hardcode the token counters to zero, which is why admin.hanzo.ai read $0.00
// and 0 tokens across a month in which the fleet served fifteen thousand requests. Money
// and usage are different questions with different owners: commerce owns the wallet,
// this table owns what was served.

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
)

// ledgerTable is the AI usage ledger. The ai plane owns its writes; admin only reads.
const ledgerTable = "hanzo.cloud_usage"

// errLedgerUnwired reports that this deployment has no warehouse connected, which is a
// different answer from "nothing was served" and must never be rendered as one. A board
// that folds it into a zero says the fleet was idle; a board that reports it says it
// cannot see. Only the second is true.
var errLedgerUnwired = errors.New("usage ledger not connected")

// ledgerScope is a read's window and, optionally, its tenant.
type ledgerScope struct {
	Since time.Time
	Org   string // empty reads the whole fleet
}

// ledgerQuery is a built read: the statement and the arguments it binds, together.
type ledgerQuery struct {
	SQL  string
	Args []any
}

// where returns the shared predicate and its bound arguments. The fleet form binds one
// argument, the tenant form two; nothing is interpolated.
func (s ledgerScope) where() (string, []any) {
	if s.Org == "" {
		return " WHERE timestamp >= ?", []any{chTS(s.Since)}
	}
	return " WHERE timestamp >= ? AND organization = ?", []any{chTS(s.Since), s.Org}
}

// ── builders (unit-tested) ──

// ledgerTotals folds the whole window into one row. It selects every column any board
// renders and lets each take what it needs, which is cheaper than four near-identical
// scans of the same partition.
func ledgerTotals(s ledgerScope) ledgerQuery {
	w, args := s.where()
	return ledgerQuery{
		SQL: "SELECT count() AS requests, sum(total_tokens) AS tokens, " +
			"sum(prompt_tokens) AS prompt_tokens, sum(completion_tokens) AS completion_tokens, " +
			datastore.Spend + " AS cost_cents, countIf(status = 'error') AS errors, " +
			"uniqExact(organization) AS orgs, uniqExact(model) AS models " +
			"FROM " + ledgerTable + w,
		Args: args,
	}
}

// ledgerSeries buckets the window. interval is a server-side constant (o11yBucket),
// never a caller's string.
func ledgerSeries(s ledgerScope, interval string) ledgerQuery {
	w, args := s.where()
	return ledgerQuery{
		SQL: "SELECT toStartOfInterval(timestamp, INTERVAL " + interval + ") AS ts, " +
			"count() AS requests, sum(total_tokens) AS tokens, " + datastore.Spend + " AS cost_cents, " +
			"countIf(status = 'error') AS errors " +
			"FROM " + ledgerTable + w + " GROUP BY ts ORDER BY ts",
		Args: args,
	}
}

// ledgerByOrg splits the window by tenant. A cap of zero returns every tenant — the
// directory lists the whole fleet, the o11y board only its head.
func ledgerByOrg(s ledgerScope, cap int) ledgerQuery {
	w, args := s.where()
	return ledgerQuery{
		SQL: "SELECT organization AS org, count() AS requests, sum(total_tokens) AS tokens, " +
			datastore.Spend + " AS cost_cents FROM " + ledgerTable + w +
			" GROUP BY org ORDER BY requests DESC" + ledgerCap(cap),
		Args: args,
	}
}

// ledgerByModel splits the window by model. Rows carrying no model are the ledger's
// unattributed writes; they would render as a nameless slice of the board.
func ledgerByModel(s ledgerScope, cap int) ledgerQuery {
	w, args := s.where()
	return ledgerQuery{
		SQL: "SELECT model, count() AS requests, sum(total_tokens) AS tokens, " +
			datastore.Spend + " AS cost_cents FROM " + ledgerTable + w +
			" AND model != '' GROUP BY model ORDER BY requests DESC" + ledgerCap(cap),
		Args: args,
	}
}

// ledgerByActor ranks the window by SPEND rather than by traffic, because the question
// this row answers is whose bill it is — a cheap chatty caller is not the one an
// operator is looking for. An unattributed row (empty user_id) names nobody and is left
// out; a row naming an application is not, since that is the finding.
func ledgerByActor(s ledgerScope, cap int) ledgerQuery {
	w, args := s.where()
	return ledgerQuery{
		SQL: "SELECT user_id AS actor, count() AS requests, sum(total_tokens) AS tokens, " +
			datastore.Spend + " AS cost_cents FROM " + ledgerTable + w +
			" AND user_id != '' GROUP BY actor ORDER BY cost_cents DESC, requests DESC" + ledgerCap(cap),
		Args: args,
	}
}

// ledgerCap renders a row cap. Non-positive means every row, which is a real ask.
func ledgerCap(n int) string {
	if n <= 0 {
		return ""
	}
	return " LIMIT " + strconv.Itoa(n)
}

// ── reads ──

// ledgerRows runs a built read best-effort: nil on any failure, which every board
// already treats as "this slice stays empty". Boards that must tell an empty fleet from
// a blind one use the reporting reads below instead.
func ledgerRows(ctx context.Context, q ledgerQuery) []map[string]any {
	rows, err := datastore.Query(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil
	}
	return rows
}

// ledgerFold is one folded window of the ledger.
type ledgerFold struct {
	Requests  int64
	Tokens    int64
	CostCents int64
}

// foldLedger folds the window and REPORTS its failure, so the overview can mark the
// source degraded rather than publish an undercount that reads healthy.
func foldLedger(ctx context.Context, s ledgerScope) (ledgerFold, error) {
	if !datastore.Ready() {
		return ledgerFold{}, errLedgerUnwired
	}
	q := ledgerTotals(s)
	rows, err := datastore.Query(ctx, q.SQL, q.Args...)
	if err != nil {
		return ledgerFold{}, err
	}
	return foldOf(firstRowOr(rows)), nil
}

// foldLedgerByOrg folds the window per tenant, keyed by org slug. ONE query answers the
// whole directory; the alternative is a read per org, and this fleet has eighty-one.
func foldLedgerByOrg(ctx context.Context, s ledgerScope) (map[string]ledgerFold, error) {
	if !datastore.Ready() {
		return nil, errLedgerUnwired
	}
	q := ledgerByOrg(s, 0)
	rows, err := datastore.Query(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ledgerFold, len(rows))
	for _, r := range rows {
		out[chStr(r["org"])] = foldOf(r)
	}
	return out, nil
}

// foldOf reads the three counters every caller of a fold renders.
func foldOf(r map[string]any) ledgerFold {
	return ledgerFold{
		Requests:  chInt64(r["requests"]),
		Tokens:    chInt64(r["tokens"]),
		CostCents: chInt64(r["cost_cents"]),
	}
}
