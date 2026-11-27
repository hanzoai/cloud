// Copyright © 2026 Hanzo AI. MIT License.

package datastore

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// fleet.go — THE UNIFIED GLOBAL FLEET AGGREGATION LAYER IN HANZO DATASTORE.
//
// Per-tenant billing books live in isolated per-org schemas on hanzo-sql (thousands of
// orgs), which is right for transactional isolation and wrong for fleet-wide analytics:
// folding 2,900+ orgs by fan-out RPC takes 90-120s and saturates Postgres.
//
// These four tables mirror each org's latest financial summary, subscriptions, and
// invoices into Hanzo Datastore (ClickHouse) as ReplacingMergeTree(updated_at) tables:
//   - hanzo.fleet_org_books      one row per org (cash, credit, limit, owed, spend windows)
//   - hanzo.fleet_subscriptions  one row per (org, subscription id)
//   - hanzo.fleet_invoices       one row per (org, invoice id)
//   - hanzo.fleet_sync_meta      hydration watermark per kind ("ledger", "subscriptions")
//
// Live writes in commerce upsert single orgs asynchronously (real-time within seconds);
// admin's first/periodic fold hydrates the full fleet in batch inserts; every fleet-wide
// read then answers in ~15ms from 1-3 ClickHouse queries instead of 17,000+ Postgres RPCs.

const (
	fleetOrgBooksTable     = "hanzo.fleet_org_books"
	fleetSubscriptionsTable = "hanzo.fleet_subscriptions"
	fleetInvoicesTable     = "hanzo.fleet_invoices"
	fleetSyncMetaTable     = "hanzo.fleet_sync_meta"
)

const fleetInsertChunk = 200

var fleetTableDDLs = []string{
	`CREATE TABLE IF NOT EXISTS hanzo.fleet_org_books (
		org String,
		books UInt8,
		cash_cents Int64,
		credit_cents Int64,
		limit_cents Int64,
		owed_cents Int64,
		spend_24h_cents Int64,
		paid_24h_cents Int64,
		spend_7d_cents Int64,
		paid_7d_cents Int64,
		spend_30d_cents Int64,
		paid_30d_cents Int64,
		spend_mtd_cents Int64,
		paid_mtd_cents Int64,
		mtd_month String,
		inv_more UInt8,
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (org)`,

	`CREATE TABLE IF NOT EXISTS hanzo.fleet_subscriptions (
		org String,
		id String,
		deleted UInt8,
		user_id String,
		plan_id String,
		plan_name String,
		status String,
		mrr_cents Int64,
		seats Int64,
		interval String,
		provider String,
		settled String,
		test UInt8,
		cancel_at_period_end UInt8,
		payload_json String,
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (org, id)`,

	`CREATE TABLE IF NOT EXISTS hanzo.fleet_invoices (
		org String,
		id String,
		deleted UInt8,
		number String,
		user_id String,
		subscription_id String,
		status String,
		amount_cents Int64,
		paid_cents Int64,
		currency String,
		issued_at String,
		paid_at String,
		payload_json String,
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (org, id)`,

	`CREATE TABLE IF NOT EXISTS hanzo.fleet_sync_meta (
		kind String,
		synced_at DateTime64(3),
		orgs UInt64,
		rows UInt64,
		mtd_month String,
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (kind)`,
}

var fleetTablesReady atomic.Bool

// EnsureFleetTables creates the hanzo database and unified fleet aggregation tables
// idempotently. Only latches on success so transient boot errors retry cleanly.
func EnsureFleetTables(ctx context.Context) error {
	if fleetTablesReady.Load() {
		return nil
	}
	if !Ready() {
		return fmt.Errorf("datastore not connected")
	}
	if err := Exec(ctx, createDatabase); err != nil {
		return fmt.Errorf("ensure database: %w", err)
	}
	for _, ddl := range fleetTableDDLs {
		if err := Exec(ctx, ddl); err != nil {
			return fmt.Errorf("ensure fleet table: %w", err)
		}
	}
	fleetTablesReady.Store(true)
	return nil
}

// FleetOrgBook is one org's unified financial summary in hanzo.fleet_org_books.
type FleetOrgBook struct {
	Org           string
	Books         bool
	CashCents     int64
	CreditCents   int64
	LimitCents    int64
	OwedCents     int64
	Spend24hCents int64
	Paid24hCents  int64
	Spend7dCents  int64
	Paid7dCents   int64
	Spend30dCents int64
	Paid30dCents  int64
	SpendMTDCents int64
	PaidMTDCents  int64
	MTDMonth      string
	InvMore       bool
	UpdatedAt     time.Time
}

// FleetSubscription is one subscription row in hanzo.fleet_subscriptions.
type FleetSubscription struct {
	Org               string
	ID                string
	Deleted           bool
	UserID            string
	PlanID            string
	PlanName          string
	Status            string
	MRRCents          int64
	Seats             int64
	Interval          string
	Provider          string
	Settled           string
	Test              bool
	CancelAtPeriodEnd bool
	PayloadJSON       string
	UpdatedAt         time.Time
}

// FleetInvoice is one invoice row in hanzo.fleet_invoices.
type FleetInvoice struct {
	Org            string
	ID             string
	Deleted        bool
	Number         string
	UserID         string
	SubscriptionID string
	Status         string
	AmountCents    int64
	PaidCents      int64
	Currency       string
	IssuedAt       string
	PaidAt         string
	PayloadJSON    string
	UpdatedAt      time.Time
}

// FleetSync is the latest hydration watermark for a fleet mirror kind.
type FleetSync struct {
	Kind     string
	SyncedAt time.Time
	Orgs     uint64
	Rows     uint64
	MTDMonth string
}

// UpsertFleetOrgBooks writes org financial rows in bounded chunks.
func UpsertFleetOrgBooks(ctx context.Context, rows []FleetOrgBook) error {
	if len(rows) == 0 {
		return nil
	}
	if err := EnsureFleetTables(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	for start := 0; start < len(rows); start += fleetInsertChunk {
		end := min(start+fleetInsertChunk, len(rows))
		stmt, args := buildOrgBooksInsert(rows[start:end], now)
		if err := Exec(ctx, stmt, args...); err != nil {
			return err
		}
	}
	return nil
}

func buildOrgBooksInsert(rows []FleetOrgBook, fallback time.Time) (string, []any) {
	const cols = 17
	vals := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*cols)
	for _, r := range rows {
		ts := r.UpdatedAt
		if ts.IsZero() {
			ts = fallback
		}
		vals = append(vals, "(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args,
			r.Org, boolU8(r.Books), r.CashCents, r.CreditCents, r.LimitCents, r.OwedCents,
			r.Spend24hCents, r.Paid24hCents, r.Spend7dCents, r.Paid7dCents,
			r.Spend30dCents, r.Paid30dCents, r.SpendMTDCents, r.PaidMTDCents,
			r.MTDMonth, boolU8(r.InvMore), ts.UTC(),
		)
	}
	return `INSERT INTO ` + fleetOrgBooksTable + `
		(org, books, cash_cents, credit_cents, limit_cents, owed_cents,
		 spend_24h_cents, paid_24h_cents, spend_7d_cents, paid_7d_cents,
		 spend_30d_cents, paid_30d_cents, spend_mtd_cents, paid_mtd_cents,
		 mtd_month, inv_more, updated_at)
		VALUES ` + strings.Join(vals, ","), args
}

// UpsertFleetSubscriptions writes subscription rows in bounded chunks.
func UpsertFleetSubscriptions(ctx context.Context, rows []FleetSubscription) error {
	if len(rows) == 0 {
		return nil
	}
	if err := EnsureFleetTables(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	for start := 0; start < len(rows); start += fleetInsertChunk {
		end := min(start+fleetInsertChunk, len(rows))
		stmt, args := buildSubscriptionsInsert(rows[start:end], now)
		if err := Exec(ctx, stmt, args...); err != nil {
			return err
		}
	}
	return nil
}

func buildSubscriptionsInsert(rows []FleetSubscription, fallback time.Time) (string, []any) {
	const cols = 16
	vals := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*cols)
	for _, r := range rows {
		ts := r.UpdatedAt
		if ts.IsZero() {
			ts = fallback
		}
		vals = append(vals, "(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args,
			r.Org, r.ID, boolU8(r.Deleted), r.UserID, r.PlanID, r.PlanName, r.Status,
			r.MRRCents, r.Seats, r.Interval, r.Provider, r.Settled,
			boolU8(r.Test), boolU8(r.CancelAtPeriodEnd), r.PayloadJSON, ts.UTC(),
		)
	}
	return `INSERT INTO ` + fleetSubscriptionsTable + `
		(org, id, deleted, user_id, plan_id, plan_name, status,
		 mrr_cents, seats, interval, provider, settled,
		 test, cancel_at_period_end, payload_json, updated_at)
		VALUES ` + strings.Join(vals, ","), args
}

// UpsertFleetInvoices writes invoice rows in bounded chunks.
func UpsertFleetInvoices(ctx context.Context, rows []FleetInvoice) error {
	if len(rows) == 0 {
		return nil
	}
	if err := EnsureFleetTables(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	for start := 0; start < len(rows); start += fleetInsertChunk {
		end := min(start+fleetInsertChunk, len(rows))
		stmt, args := buildInvoicesInsert(rows[start:end], now)
		if err := Exec(ctx, stmt, args...); err != nil {
			return err
		}
	}
	return nil
}

func buildInvoicesInsert(rows []FleetInvoice, fallback time.Time) (string, []any) {
	const cols = 14
	vals := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*cols)
	for _, r := range rows {
		ts := r.UpdatedAt
		if ts.IsZero() {
			ts = fallback
		}
		vals = append(vals, "(?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args,
			r.Org, r.ID, boolU8(r.Deleted), r.Number, r.UserID, r.SubscriptionID, r.Status,
			r.AmountCents, r.PaidCents, r.Currency, r.IssuedAt, r.PaidAt, r.PayloadJSON, ts.UTC(),
		)
	}
	return `INSERT INTO ` + fleetInvoicesTable + `
		(org, id, deleted, number, user_id, subscription_id, status,
		 amount_cents, paid_cents, currency, issued_at, paid_at, payload_json, updated_at)
		VALUES ` + strings.Join(vals, ","), args
}

// MarkFleetSynced records that a full fleet hydration of kind completed.
func MarkFleetSynced(ctx context.Context, kind string, orgs, rows uint64, mtdMonth string, syncedAt time.Time) error {
	if err := EnsureFleetTables(ctx); err != nil {
		return err
	}
	if syncedAt.IsZero() {
		syncedAt = time.Now().UTC()
	}
	return Exec(ctx,
		`INSERT INTO `+fleetSyncMetaTable+` (kind, synced_at, orgs, rows, mtd_month, updated_at) VALUES (?,?,?,?,?,?)`,
		kind, syncedAt.UTC(), orgs, rows, mtdMonth, time.Now().UTC(),
	)
}

// FleetSyncInfo returns the latest hydration watermark for kind.
func FleetSyncInfo(ctx context.Context, kind string) (FleetSync, bool, error) {
	if err := EnsureFleetTables(ctx); err != nil {
		return FleetSync{}, false, err
	}
	rows, err := Query(ctx, `
		SELECT
			argMax(synced_at, updated_at) AS synced_at,
			argMax(orgs, updated_at) AS orgs,
			argMax(rows, updated_at) AS rows,
			argMax(mtd_month, updated_at) AS mtd_month
		FROM `+fleetSyncMetaTable+`
		WHERE kind = ?
		GROUP BY kind`, kind)
	if err != nil {
		return FleetSync{}, false, err
	}
	if len(rows) == 0 {
		return FleetSync{}, false, nil
	}
	r := rows[0]
	synced := dsTime(r["synced_at"])
	if synced.IsZero() {
		return FleetSync{}, false, nil
	}
	return FleetSync{
		Kind:     kind,
		SyncedAt: synced,
		Orgs:     dsUint64(r["orgs"]),
		Rows:     dsUint64(r["rows"]),
		MTDMonth: dsString(r["mtd_month"]),
	}, true, nil
}

// ReadFleetOrgBooks returns the latest financial summary for every org in ONE query.
func ReadFleetOrgBooks(ctx context.Context) (map[string]FleetOrgBook, error) {
	if err := EnsureFleetTables(ctx); err != nil {
		return nil, err
	}
	rows, err := Query(ctx, `
		SELECT
			org,
			argMax(books, updated_at) AS books,
			argMax(cash_cents, updated_at) AS cash_cents,
			argMax(credit_cents, updated_at) AS credit_cents,
			argMax(limit_cents, updated_at) AS limit_cents,
			argMax(owed_cents, updated_at) AS owed_cents,
			argMax(spend_24h_cents, updated_at) AS spend_24h_cents,
			argMax(paid_24h_cents, updated_at) AS paid_24h_cents,
			argMax(spend_7d_cents, updated_at) AS spend_7d_cents,
			argMax(paid_7d_cents, updated_at) AS paid_7d_cents,
			argMax(spend_30d_cents, updated_at) AS spend_30d_cents,
			argMax(paid_30d_cents, updated_at) AS paid_30d_cents,
			argMax(spend_mtd_cents, updated_at) AS spend_mtd_cents,
			argMax(paid_mtd_cents, updated_at) AS paid_mtd_cents,
			argMax(mtd_month, updated_at) AS mtd_month,
			argMax(inv_more, updated_at) AS inv_more,
			max(updated_at) AS updated_at
		FROM `+fleetOrgBooksTable+`
		GROUP BY org`)
	if err != nil {
		return nil, err
	}
	out := make(map[string]FleetOrgBook, len(rows))
	for _, r := range rows {
		org := dsString(r["org"])
		if org == "" {
			continue
		}
		out[org] = FleetOrgBook{
			Org:           org,
			Books:         dsInt64(r["books"]) != 0,
			CashCents:     dsInt64(r["cash_cents"]),
			CreditCents:   dsInt64(r["credit_cents"]),
			LimitCents:    dsInt64(r["limit_cents"]),
			OwedCents:     dsInt64(r["owed_cents"]),
			Spend24hCents: dsInt64(r["spend_24h_cents"]),
			Paid24hCents:  dsInt64(r["paid_24h_cents"]),
			Spend7dCents:  dsInt64(r["spend_7d_cents"]),
			Paid7dCents:   dsInt64(r["paid_7d_cents"]),
			Spend30dCents: dsInt64(r["spend_30d_cents"]),
			Paid30dCents:  dsInt64(r["paid_30d_cents"]),
			SpendMTDCents: dsInt64(r["spend_mtd_cents"]),
			PaidMTDCents:  dsInt64(r["paid_mtd_cents"]),
			MTDMonth:      dsString(r["mtd_month"]),
			InvMore:       dsInt64(r["inv_more"]) != 0,
			UpdatedAt:     dsTime(r["updated_at"]),
		}
	}
	return out, nil
}

// ReadFleetSubscriptions returns every non-deleted subscription across all orgs in ONE query.
func ReadFleetSubscriptions(ctx context.Context) (map[string][]FleetSubscription, error) {
	if err := EnsureFleetTables(ctx); err != nil {
		return nil, err
	}
	rows, err := Query(ctx, `
		SELECT
			org,
			id,
			argMax(deleted, updated_at) AS deleted,
			argMax(user_id, updated_at) AS user_id,
			argMax(plan_id, updated_at) AS plan_id,
			argMax(plan_name, updated_at) AS plan_name,
			argMax(status, updated_at) AS status,
			argMax(mrr_cents, updated_at) AS mrr_cents,
			argMax(seats, updated_at) AS seats,
			argMax(interval, updated_at) AS interval,
			argMax(provider, updated_at) AS provider,
			argMax(settled, updated_at) AS settled,
			argMax(test, updated_at) AS test,
			argMax(cancel_at_period_end, updated_at) AS cancel_at_period_end,
			argMax(payload_json, updated_at) AS payload_json,
			max(updated_at) AS updated_at
		FROM `+fleetSubscriptionsTable+`
		GROUP BY org, id
		HAVING deleted = 0`)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]FleetSubscription)
	for _, r := range rows {
		org := dsString(r["org"])
		if org == "" {
			continue
		}
		out[org] = append(out[org], FleetSubscription{
			Org:               org,
			ID:                dsString(r["id"]),
			UserID:            dsString(r["user_id"]),
			PlanID:            dsString(r["plan_id"]),
			PlanName:          dsString(r["plan_name"]),
			Status:            dsString(r["status"]),
			MRRCents:          dsInt64(r["mrr_cents"]),
			Seats:             dsInt64(r["seats"]),
			Interval:          dsString(r["interval"]),
			Provider:          dsString(r["provider"]),
			Settled:           dsString(r["settled"]),
			Test:              dsInt64(r["test"]) != 0,
			CancelAtPeriodEnd: dsInt64(r["cancel_at_period_end"]) != 0,
			PayloadJSON:       dsString(r["payload_json"]),
			UpdatedAt:         dsTime(r["updated_at"]),
		})
	}
	return out, nil
}

// ReadFleetInvoices returns every non-deleted invoice across all orgs in ONE query.
func ReadFleetInvoices(ctx context.Context) (map[string][]FleetInvoice, error) {
	if err := EnsureFleetTables(ctx); err != nil {
		return nil, err
	}
	rows, err := Query(ctx, `
		SELECT
			org,
			id,
			argMax(deleted, updated_at) AS deleted,
			argMax(number, updated_at) AS number,
			argMax(user_id, updated_at) AS user_id,
			argMax(subscription_id, updated_at) AS subscription_id,
			argMax(status, updated_at) AS status,
			argMax(amount_cents, updated_at) AS amount_cents,
			argMax(paid_cents, updated_at) AS paid_cents,
			argMax(currency, updated_at) AS currency,
			argMax(issued_at, updated_at) AS issued_at,
			argMax(paid_at, updated_at) AS paid_at,
			argMax(payload_json, updated_at) AS payload_json,
			max(updated_at) AS updated_at
		FROM `+fleetInvoicesTable+`
		GROUP BY org, id
		HAVING deleted = 0`)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]FleetInvoice)
	for _, r := range rows {
		org := dsString(r["org"])
		if org == "" {
			continue
		}
		out[org] = append(out[org], FleetInvoice{
			Org:            org,
			ID:             dsString(r["id"]),
			Number:         dsString(r["number"]),
			UserID:         dsString(r["user_id"]),
			SubscriptionID: dsString(r["subscription_id"]),
			Status:         dsString(r["status"]),
			AmountCents:    dsInt64(r["amount_cents"]),
			PaidCents:      dsInt64(r["paid_cents"]),
			Currency:       dsString(r["currency"]),
			IssuedAt:       dsString(r["issued_at"]),
			PaidAt:         dsString(r["paid_at"]),
			PayloadJSON:    dsString(r["payload_json"]),
			UpdatedAt:      dsTime(r["updated_at"]),
		})
	}
	return out, nil
}

func boolU8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func dsString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return ""
	}
}

func dsInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int32:
		return int64(x)
	case int:
		return int64(x)
	case uint64:
		if x > math.MaxInt64 {
			return math.MaxInt64
		}
		return int64(x)
	case uint32:
		return int64(x)
	case uint8:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	default:
		return 0
	}
}

func dsUint64(v any) uint64 {
	switch x := v.(type) {
	case uint64:
		return x
	case uint32:
		return uint64(x)
	case uint8:
		return uint64(x)
	case int64:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case int:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case float64:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case string:
		n, _ := strconv.ParseUint(strings.TrimSpace(x), 10, 64)
		return n
	default:
		return 0
	}
}

func dsTime(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x.UTC()
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, x); err == nil {
				return t.UTC()
			}
		}
		return time.Time{}
	default:
		return time.Time{}
	}
}
