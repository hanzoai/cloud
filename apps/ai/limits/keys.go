// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package limits

// keys.go — an API key's own limits on model calls: the models it may use and
// what it may spend. Spend is kept one row per key per calendar month (UTC), added
// to as each call the key made is recorded; the key's total is the sum of its
// months. Amounts are nano-dollars, as everywhere in this store.
//
// Every place a model call is admitted asks Key, and every place one is recorded
// calls Spend: the model API (apps/ai) and Zen (apps/zen) both.

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/principal"
)

// The refusals of a model call made on an API key. Both answer 402 naming what the
// key may not do, and neither is handed to a fallback model: the key's owner set
// the limit, and only they lift it.
const (
	CodeKeyBudget = "key_budget"
	CodeKeyModel  = "key_model"
)

// Key answers whether the API key a model call arrived on may make it: the model
// its limit names, and the budget it has left. The key and its grant are the
// boundary's attestation of the request in flight (principal.Minted); a call on
// any other credential, or a key with neither limit, is not asked. A spend that
// cannot be read is an error, and the gates refuse on one: a cap the owner set is
// not lifted by an outage.
func (l *Limiter) Key(ctx context.Context, model string) (*Hit, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, nil
	}
	p, ok := principal.Minted(c)
	if !ok || p.Key == "" {
		return nil, nil
	}
	g := cloud.Grant(p.Limit)
	if model != "" && !g.Covers("model", model) {
		return &Hit{Code: CodeKeyModel, Name: "model", Model: model,
			Message: fmt.Sprintf("This API key may not use %s. Edit its permissions to allow it.", model)}, nil
	}
	month, total := g.Budget()
	if month == 0 && total == 0 {
		return nil, nil
	}
	spentMonth, spentTotal, err := l.Spent(ctx, p.Org, p.Key)
	if err != nil {
		return nil, fmt.Errorf("key spend unreadable: %w", err)
	}
	if month > 0 && spentMonth >= month*nanosPerCent {
		t := l.now().UTC()
		next := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		return &Hit{Code: CodeKeyBudget, Name: "month", ResetsAt: next,
			Message: fmt.Sprintf("This API key has spent its %s monthly budget. Raise its budget, or wait until %s.",
				dollars(month), next.Format("January 2"))}, nil
	}
	if total > 0 && spentTotal >= total*nanosPerCent {
		return &Hit{Code: CodeKeyBudget, Name: "total",
			Message: fmt.Sprintf("This API key has spent its %s budget. Raise its budget to keep using it.", dollars(total))}, nil
	}
	return nil, nil
}

// dollars renders cents as dollars: 5000 → "$50.00".
func dollars(cents int64) string { return fmt.Sprintf("$%d.%02d", cents/100, cents%100) }

// NanosUp is an amount in nano-dollars, rounded up, so a sub-nano charge still
// counts against a key's budget.
func NanosUp(a money.Amount) int64 {
	atto := a.Atto()
	if atto.Sign() <= 0 {
		return 0
	}
	n, r := new(big.Int).QuoRem(atto, big.NewInt(1_000_000_000), new(big.Int))
	if r.Sign() > 0 {
		n.Add(n, big.NewInt(1))
	}
	return n.Int64()
}

// keySchema is the per-key spend table, created with the rest of the store.
const keySchema = `
CREATE TABLE IF NOT EXISTS keyspend (
  org   TEXT    NOT NULL,
  key   TEXT    NOT NULL,
  month TEXT    NOT NULL,
  nanos BIGINT  NOT NULL,
  PRIMARY KEY (org, key, month)
);`

// month names the calendar month (UTC) a spend is counted in.
func month(t time.Time) string { return t.UTC().Format("2006-01") }

// Spend adds what one call made on key, filed in org, cost in nano-dollars to the
// key's month.
func (l *Limiter) Spend(ctx context.Context, org, key string, nanos int64) error {
	if nanos <= 0 {
		return nil
	}
	if err := dialect.Text(org, key); err != nil {
		return err
	}
	// keyspend.nanos is named: beside excluded, a bare nanos is ambiguous on PostgreSQL.
	_, err := l.store.db.ExecContext(ctx, `
INSERT INTO keyspend (org, key, month, nanos) VALUES ($1, $2, $3, $4)
ON CONFLICT (org, key, month) DO UPDATE SET nanos = keyspend.nanos + excluded.nanos`,
		org, key, month(l.now()), nanos)
	return err
}

// Spent is what key has spent this calendar month and in all, in nano-dollars.
func (l *Limiter) Spent(ctx context.Context, org, key string) (thisMonth, total int64, err error) {
	if err := dialect.Text(org, key); err != nil {
		return 0, 0, err // a cap is not lifted by a key the store cannot read
	}
	err = l.store.db.QueryRowContext(ctx, `
SELECT CAST(COALESCE(SUM(CASE WHEN month = $1 THEN nanos ELSE 0 END), 0) AS BIGINT), CAST(COALESCE(SUM(nanos), 0) AS BIGINT)
FROM keyspend WHERE org = $2 AND key = $3`, month(l.now()), org, key).Scan(&thisMonth, &total)
	return thisMonth, total, err
}
