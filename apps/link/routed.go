package link

// routed.go is the per-account SERVER-ROUTED usage counter — the usage-of-record
// for calls THIS gateway routed through a caller's linked account.
//
// WHY A SUMMING COUNTER, NOT THE account_usage SERIES. account_usage (datastore.go)
// is a ReplacingMergeTree of CUMULATIVE plan snapshots: a device poller re-reporting
// the SAME window as it fills, deduped by window instance, newest read wins. A
// server-routed call is the opposite shape — a DELTA (one request's exact tokens), a
// thing to ADD, never a re-observation to replace. Forcing deltas into that engine
// would collapse many calls that share a window onto one row and lose the count. So
// routed usage is an additive counter in SQLite, right beside the Links it
// aggregates: the same (org, subject) tenancy, the same file-is-the-boundary
// isolation, a transactional add, and correct sums by construction.
//
// THREE LEDGERS, THREE MEANINGS, NEVER CONFLATED (the package's standing rule):
//   - account_usage — the device collector's plan snapshots (SourceAccount, percent).
//   - routed_usage  — THIS counter: what the gateway routed per account (exact deltas).
//   - the money ledger (commerce/finance) — the org's charge of record.

import (
	"context"
	"fmt"
)

// routedDDL is the ONE definition of the routed-usage counter. PRIMARY KEY is the
// (org, subject, provider, account) identity, so an add UPSERTs the one row per
// account and the sum lives in place.
const routedDDL = `
CREATE TABLE IF NOT EXISTS routed_usage (
  org               TEXT NOT NULL,
  subject           TEXT NOT NULL,
  provider          TEXT NOT NULL,
  account           TEXT NOT NULL DEFAULT '',
  kind              TEXT NOT NULL DEFAULT '',
  requests          INTEGER NOT NULL DEFAULT 0,
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens      INTEGER NOT NULL DEFAULT 0,
  cost_cents        INTEGER NOT NULL DEFAULT 0,
  first_at          INTEGER NOT NULL,
  last_at           INTEGER NOT NULL,
  PRIMARY KEY (org, subject, provider, account)
);`

// migrateRouted creates the routed-usage counter. Called from openStore beside the
// links migration, on the same connection, so both share one schema lifecycle.
func (s *Store) migrateRouted() error {
	if _, err := s.db.Exec(routedDDL); err != nil {
		return fmt.Errorf("migrate routed_usage: %w", err)
	}
	return nil
}

// RoutedUsage is one account's summed server-routed usage — a row of the per-account
// breakdown the dashboard reads.
type RoutedUsage struct {
	Provider         string `json:"provider"`
	Account          string `json:"account,omitempty"`
	Kind             string `json:"kind"`
	Billing          string `json:"billing"` // BillingMode(Kind): plan | commerce
	Requests         int64  `json:"requests"`
	PromptTokens     int64  `json:"promptTokens"`
	CompletionTokens int64  `json:"completionTokens"`
	TotalTokens      int64  `json:"totalTokens"`
	CostCents        int64  `json:"costCents"`
	FirstAt          int64  `json:"-"`
	LastAt           int64  `json:"-"`
}

// AddRouted sums one served routed call into the caller's per-account counter. Org
// and subject are the SERVER's values (the validated principal the router passed),
// bound positionally — the account cannot carry them, so a row can only ever be the
// caller's OWN within their OWN org. costCents is the charge recorded for the call
// (0 for a subscription account, whose plan pays the provider directly).
//
// It does NOT attempt idempotency: a routed call is a fresh event, and each is one
// unit of usage. At-most-once for the CHARGE is the money ledger's job (its
// RequestID key), not this visibility counter's.
func (s *Store) AddRouted(ctx context.Context, org, subject string, a Account, kind string, res Result, costCents, now int64) error {
	if trim(org) == "" || trim(subject) == "" {
		return fmt.Errorf("routed usage: blank tenancy")
	}
	requests := int64(1)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO routed_usage
		   (org,subject,provider,account,kind,requests,prompt_tokens,completion_tokens,total_tokens,cost_cents,first_at,last_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(org,subject,provider,account) DO UPDATE SET
		   kind=excluded.kind,
		   requests=requests+excluded.requests,
		   prompt_tokens=prompt_tokens+excluded.prompt_tokens,
		   completion_tokens=completion_tokens+excluded.completion_tokens,
		   total_tokens=total_tokens+excluded.total_tokens,
		   cost_cents=cost_cents+excluded.cost_cents,
		   last_at=excluded.last_at`,
		org, subject, a.Provider, a.Profile, kind,
		requests, res.PromptTokens, res.CompletionTokens, res.TotalTokens, costCents, now, now)
	if err != nil {
		return fmt.Errorf("routed usage add: %w", err)
	}
	return nil
}

// RoutedTotals returns the caller's per-account server-routed usage, most-used
// first. Every query leads with `org = ? AND subject = ?` as bound parameters, so a
// caller reads ONLY their OWN accounts within their OWN org — the same fail-closed
// tenancy every other read in this package uses.
func (s *Store) RoutedTotals(ctx context.Context, org, subject string) ([]RoutedUsage, error) {
	if trim(org) == "" || trim(subject) == "" {
		return nil, fmt.Errorf("routed usage: blank tenancy")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider,account,kind,requests,prompt_tokens,completion_tokens,total_tokens,cost_cents,first_at,last_at
		   FROM routed_usage WHERE org=? AND subject=?
		   ORDER BY total_tokens DESC, provider ASC, account ASC`,
		org, subject)
	if err != nil {
		return nil, fmt.Errorf("routed usage list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]RoutedUsage, 0, 8)
	for rows.Next() {
		var u RoutedUsage
		if err := rows.Scan(&u.Provider, &u.Account, &u.Kind, &u.Requests,
			&u.PromptTokens, &u.CompletionTokens, &u.TotalTokens, &u.CostCents,
			&u.FirstAt, &u.LastAt); err != nil {
			return nil, fmt.Errorf("routed usage scan: %w", err)
		}
		u.Billing = BillingMode(u.Kind)
		out = append(out, u)
	}
	return out, rows.Err()
}
