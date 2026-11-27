// Copyright © 2026 Hanzo AI. MIT License.

package datastore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// analytics_ingest.go — REPLAY-SAFE, BATCHED INGESTION & IDEMPOTENT ROLLUP BACKFILL.
//
// All monetary fields are integer minor currency units (int64) — NEVER floating point.
// Batch inserts are chunked to analyticsInsertChunk (200 rows) with positional `?`
// placeholders. Commerce ledger ingestion includes a bounded in-process idempotency
// dedup guard on entry_id so retries and duplicate deliveries never double-book flows.

const (
	analyticsInsertChunk = 200
	defaultLedgerDedup   = 100_000
)

// CommerceLedgerFact is one immutable append-only monetary flow event written to
// `analytics.fact_commerce_ledger`. All amounts are integer minor units; reversals
// and adjustments carry Sign = -1 rather than mutating prior rows.
type CommerceLedgerFact struct {
	EntryID       string
	Org           string
	Product       string
	Kind          string // deposit|usage|refund|chargeback|grant|adjustment|invoice_paid
	Sign          int8   // +1 or -1
	AmountMinor   int64
	CashMinor     int64
	CreditMinor   int64
	Currency      string
	FXRateNano    int64
	FXAt          time.Time
	SourceVersion uint64
	EventTS       time.Time
	IngestedAt    time.Time
}

// OrgBalanceState is the latest balance snapshot for one (org, currency) in
// `analytics.state_org_balances`.
type OrgBalanceState struct {
	Org         string
	Currency    string
	Books       bool
	CashMinor   int64
	CreditMinor int64
	LimitMinor  int64
	OwedMinor   int64
	Version     uint64
	UpdatedAt   time.Time
}

// SubscriptionState is the latest state for one (org, id) subscription in
// `analytics.state_subscriptions`.
type SubscriptionState struct {
	Org               string
	ID                string
	Deleted           bool
	UserID            string
	PlanID            string
	PlanName          string
	Status            string
	MRRMinor          int64
	Seats             int64
	Interval          string
	Provider          string
	Settled           string
	Currency          string
	Test              bool
	CancelAtPeriodEnd bool
	PayloadJSON       string
	Version           uint64
	UpdatedAt         time.Time
}

// InvoiceState is the latest state for one (org, id) invoice in
// `analytics.state_invoices`.
type InvoiceState struct {
	Org            string
	ID             string
	Deleted        bool
	Number         string
	UserID         string
	SubscriptionID string
	Status         string
	AmountMinor    int64
	PaidMinor      int64
	Currency       string
	IssuedAt       string
	PaidAt         string
	PayloadJSON    string
	Version        uint64
	UpdatedAt      time.Time
}

// DimOrg is one organization dimension row in `analytics.dim_orgs`.
type DimOrg struct {
	Org         string
	DisplayName string
	CreatedAt   time.Time
	IsInternal  bool
	Members     uint64
	Tier        string
	Status      string
	UpdatedAt   time.Time
}

// DimUser is one user dimension row in `analytics.dim_users`.
type DimUser struct {
	Org          string
	UserID       string
	Name         string
	Email        string
	CreatedAt    time.Time
	LastSigninAt time.Time
	IsAdmin      bool
	Forbidden    bool
	UpdatedAt    time.Time
}

var lastAnalyticsVersion atomic.Uint64

// NextAnalyticsVersion returns a strictly monotonic version seeded by UTC Unix
// milliseconds so concurrent mutations within the same millisecond never tie in
// ClickHouse ReplacingMergeTree(version) tables.
func NextAnalyticsVersion() uint64 {
	now := uint64(time.Now().UTC().UnixMilli())
	for {
		prev := lastAnalyticsVersion.Load()
		next := now
		if next <= prev {
			next = prev + 1
		}
		if lastAnalyticsVersion.CompareAndSwap(prev, next) {
			return next
		}
	}
}

// Watermark tracks pipeline freshness, lag, volume, and reconciliation drift in
// `analytics.watermarks`.
type Watermark struct {
	Pipeline            string
	Source              string
	WatermarkTS         time.Time
	LagMS               int64
	RowsProcessed       uint64
	ReconcileDriftMinor int64
	Status              string
	Detail              string
	UpdatedAt           time.Time
}

// ledgerDeduper is a bounded, thread-safe FIFO set of committed ledger entry_ids.
// Memory is strictly bounded to cap entries while preventing in-process replay
// duplicates across bursty retries.
type ledgerDeduper struct {
	mu       sync.Mutex
	cap      int
	ring     []string
	pos      int
	seen     map[string]struct{}
	inFlight map[string]struct{}
}

func newLedgerDeduper(capacity int) *ledgerDeduper {
	if capacity <= 0 {
		capacity = defaultLedgerDedup
	}
	return &ledgerDeduper{
		cap:      capacity,
		ring:     make([]string, capacity),
		seen:     make(map[string]struct{}, min(capacity, 1024)),
		inFlight: make(map[string]struct{}),
	}
}

// filter returns the subset of rows whose EntryID has neither been committed nor
// already appeared earlier in the same batch. Does NOT reserve or commit the returned
// rows.
func (d *ledgerDeduper) filter(rows []CommerceLedgerFact) []CommerceLedgerFact {
	d.mu.Lock()
	defer d.mu.Unlock()

	batchSeen := make(map[string]struct{}, len(rows))
	out := make([]CommerceLedgerFact, 0, len(rows))
	for _, r := range rows {
		if _, ok := d.seen[r.EntryID]; ok {
			continue
		}
		if _, ok := d.inFlight[r.EntryID]; ok {
			continue
		}
		if _, ok := batchSeen[r.EntryID]; ok {
			continue
		}
		batchSeen[r.EntryID] = struct{}{}
		out = append(out, r)
	}
	return out
}

// acquire atomically filters out committed and in-flight EntryIDs and marks the
// returned unique rows as in-flight so concurrent duplicate deliveries never
// double-insert. Callers MUST either commit(chunk) on success or release(remaining)
// on error.
func (d *ledgerDeduper) acquire(rows []CommerceLedgerFact) []CommerceLedgerFact {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]CommerceLedgerFact, 0, len(rows))
	for _, r := range rows {
		if _, ok := d.seen[r.EntryID]; ok {
			continue
		}
		if _, ok := d.inFlight[r.EntryID]; ok {
			continue
		}
		d.inFlight[r.EntryID] = struct{}{}
		out = append(out, r)
	}
	return out
}

// release clears in-flight reservations for failed writes so they remain retryable.
func (d *ledgerDeduper) release(rows []CommerceLedgerFact) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range rows {
		delete(d.inFlight, r.EntryID)
	}
}

// commit records successfully persisted entry_ids into the bounded dedup ring and
// clears their in-flight reservations.
func (d *ledgerDeduper) commit(rows []CommerceLedgerFact) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, r := range rows {
		delete(d.inFlight, r.EntryID)
		if _, ok := d.seen[r.EntryID]; ok {
			continue
		}
		if prev := d.ring[d.pos]; prev != "" {
			delete(d.seen, prev)
		}
		d.ring[d.pos] = r.EntryID
		d.pos = (d.pos + 1) % d.cap
		d.seen[r.EntryID] = struct{}{}
	}
}

var processLedgerDeduper = newLedgerDeduper(defaultLedgerDedup)

func validateCommerceLedgerFact(r CommerceLedgerFact) error {
	if r.EntryID == "" {
		return fmt.Errorf("commerce ledger fact missing entry_id")
	}
	if r.Org == "" {
		return fmt.Errorf("commerce ledger fact %q missing org", r.EntryID)
	}
	if r.Sign != 1 && r.Sign != -1 {
		return fmt.Errorf("commerce ledger fact %q invalid sign %d (must be +1 or -1)", r.EntryID, r.Sign)
	}
	return nil
}

// InsertCommerceLedgerFacts validates, deduplicates (both intra-batch and against
// recently committed entry_ids), and inserts immutable flow facts into
// `analytics.fact_commerce_ledger` in bounded 200-row chunks.
func InsertCommerceLedgerFacts(ctx context.Context, rows []CommerceLedgerFact) error {
	if len(rows) == 0 {
		return nil
	}
	for _, r := range rows {
		if err := validateCommerceLedgerFact(r); err != nil {
			return err
		}
	}
	unique := processLedgerDeduper.acquire(rows)
	if len(unique) == 0 {
		return nil
	}
	committed := 0
	defer func() {
		if committed < len(unique) {
			processLedgerDeduper.release(unique[committed:])
		}
	}()
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	for start := 0; start < len(unique); start += analyticsInsertChunk {
		end := min(start+analyticsInsertChunk, len(unique))
		chunk := unique[start:end]
		stmt, args := buildCommerceLedgerInsert(chunk, now)
		if err := Exec(ctx, stmt, args...); err != nil {
			return err
		}
		processLedgerDeduper.commit(chunk)
		committed = end
	}
	return nil
}

func buildCommerceLedgerInsert(rows []CommerceLedgerFact, fallback time.Time) (string, []any) {
	const cols = 14
	vals := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*cols)
	fb := fallback.UTC()
	for _, r := range rows {
		eventTS := r.EventTS
		if eventTS.IsZero() {
			eventTS = fb
		}
		ingestedAt := r.IngestedAt
		if ingestedAt.IsZero() {
			ingestedAt = fb
		}
		fxAt := r.FXAt
		if fxAt.IsZero() {
			fxAt = eventTS
		}
		cur := r.Currency
		if cur == "" {
			cur = "usd"
		}
		vals = append(vals, "(?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args,
			r.EntryID, r.Org, r.Product, r.Kind, r.Sign,
			r.AmountMinor, r.CashMinor, r.CreditMinor, cur,
			r.FXRateNano, fxAt.UTC(), r.SourceVersion, eventTS.UTC(), ingestedAt.UTC(),
		)
	}
	return `INSERT INTO ` + AnalyticsFactCommerceLedgerTable + `
		(entry_id, org, product, kind, sign,
		 amount_minor, cash_minor, credit_minor, currency,
		 fx_rate_nano, fx_at, source_version, event_ts, ingested_at)
		VALUES ` + strings.Join(vals, ","), args
}

// UpsertOrgBalanceStates writes latest per-(org, currency) balance snapshots into
// `analytics.state_org_balances` in bounded 200-row chunks.
func UpsertOrgBalanceStates(ctx context.Context, rows []OrgBalanceState) error {
	if len(rows) == 0 {
		return nil
	}
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	for start := 0; start < len(rows); start += analyticsInsertChunk {
		end := min(start+analyticsInsertChunk, len(rows))
		stmt, args := buildOrgBalanceStatesInsert(rows[start:end], now)
		if err := Exec(ctx, stmt, args...); err != nil {
			return err
		}
	}
	return nil
}

func buildOrgBalanceStatesInsert(rows []OrgBalanceState, fallback time.Time) (string, []any) {
	const cols = 9
	vals := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*cols)
	fb := fallback.UTC()
	for _, r := range rows {
		ts := r.UpdatedAt
		if ts.IsZero() {
			ts = fb
		}
		cur := r.Currency
		if cur == "" {
			cur = "usd"
		}
		ver := r.Version
		if ver == 0 {
			ver = uint64(ts.UTC().UnixMilli())
		}
		vals = append(vals, "(?,?,?,?,?,?,?,?,?)")
		args = append(args,
			r.Org, cur, boolU8(r.Books),
			r.CashMinor, r.CreditMinor, r.LimitMinor, r.OwedMinor,
			ver, ts.UTC(),
		)
	}
	return `INSERT INTO ` + AnalyticsStateOrgBalancesTable + `
		(org, currency, books, cash_minor, credit_minor, limit_minor, owed_minor, version, updated_at)
		VALUES ` + strings.Join(vals, ","), args
}

// UpsertSubscriptionStates writes latest subscription states into
// `analytics.state_subscriptions` in bounded 200-row chunks.
func UpsertSubscriptionStates(ctx context.Context, rows []SubscriptionState) error {
	if len(rows) == 0 {
		return nil
	}
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	for start := 0; start < len(rows); start += analyticsInsertChunk {
		end := min(start+analyticsInsertChunk, len(rows))
		stmt, args := buildSubscriptionStatesInsert(rows[start:end], now)
		if err := Exec(ctx, stmt, args...); err != nil {
			return err
		}
	}
	return nil
}

func buildSubscriptionStatesInsert(rows []SubscriptionState, fallback time.Time) (string, []any) {
	const cols = 18
	vals := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*cols)
	fb := fallback.UTC()
	for _, r := range rows {
		ts := r.UpdatedAt
		if ts.IsZero() {
			ts = fb
		}
		cur := r.Currency
		if cur == "" {
			cur = "usd"
		}
		ver := r.Version
		if ver == 0 {
			ver = uint64(ts.UTC().UnixMilli())
		}
		vals = append(vals, "(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args,
			r.Org, r.ID, boolU8(r.Deleted), r.UserID, r.PlanID, r.PlanName, r.Status,
			r.MRRMinor, r.Seats, r.Interval, r.Provider, r.Settled, cur,
			boolU8(r.Test), boolU8(r.CancelAtPeriodEnd), r.PayloadJSON, ver, ts.UTC(),
		)
	}
	return `INSERT INTO ` + AnalyticsStateSubscriptionsTable + `
		(org, id, deleted, user_id, plan_id, plan_name, status,
		 mrr_minor, seats, interval, provider, settled, currency,
		 test, cancel_at_period_end, payload_json, version, updated_at)
		VALUES ` + strings.Join(vals, ","), args
}

// UpsertInvoiceStates writes latest invoice states into `analytics.state_invoices`
// in bounded 200-row chunks.
func UpsertInvoiceStates(ctx context.Context, rows []InvoiceState) error {
	if len(rows) == 0 {
		return nil
	}
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	for start := 0; start < len(rows); start += analyticsInsertChunk {
		end := min(start+analyticsInsertChunk, len(rows))
		stmt, args := buildInvoiceStatesInsert(rows[start:end], now)
		if err := Exec(ctx, stmt, args...); err != nil {
			return err
		}
	}
	return nil
}

func buildInvoiceStatesInsert(rows []InvoiceState, fallback time.Time) (string, []any) {
	const cols = 15
	vals := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*cols)
	fb := fallback.UTC()
	for _, r := range rows {
		ts := r.UpdatedAt
		if ts.IsZero() {
			ts = fb
		}
		cur := r.Currency
		if cur == "" {
			cur = "usd"
		}
		ver := r.Version
		if ver == 0 {
			ver = uint64(ts.UTC().UnixMilli())
		}
		vals = append(vals, "(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args,
			r.Org, r.ID, boolU8(r.Deleted), r.Number, r.UserID, r.SubscriptionID, r.Status,
			r.AmountMinor, r.PaidMinor, cur, r.IssuedAt, r.PaidAt, r.PayloadJSON, ver, ts.UTC(),
		)
	}
	return `INSERT INTO ` + AnalyticsStateInvoicesTable + `
		(org, id, deleted, number, user_id, subscription_id, status,
		 amount_minor, paid_minor, currency, issued_at, paid_at, payload_json, version, updated_at)
		VALUES ` + strings.Join(vals, ","), args
}

// UpsertDimOrgs writes organization dimension rows into `analytics.dim_orgs`
// in bounded 200-row chunks.
func UpsertDimOrgs(ctx context.Context, rows []DimOrg) error {
	if len(rows) == 0 {
		return nil
	}
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	for start := 0; start < len(rows); start += analyticsInsertChunk {
		end := min(start+analyticsInsertChunk, len(rows))
		stmt, args := buildDimOrgsInsert(rows[start:end], now)
		if err := Exec(ctx, stmt, args...); err != nil {
			return err
		}
	}
	return nil
}

func buildDimOrgsInsert(rows []DimOrg, fallback time.Time) (string, []any) {
	const cols = 8
	vals := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*cols)
	fb := fallback.UTC()
	for _, r := range rows {
		ts := r.UpdatedAt
		if ts.IsZero() {
			ts = fb
		}
		created := r.CreatedAt
		if created.IsZero() {
			created = ts
		}
		vals = append(vals, "(?,?,?,?,?,?,?,?)")
		args = append(args,
			r.Org, r.DisplayName, created.UTC(), boolU8(r.IsInternal),
			r.Members, r.Tier, r.Status, ts.UTC(),
		)
	}
	return `INSERT INTO ` + AnalyticsDimOrgsTable + `
		(org, display_name, created_at, is_internal, members, tier, status, updated_at)
		VALUES ` + strings.Join(vals, ","), args
}

// RecordCommerceLedgerFact validates, deduplicates on EntryID, and appends one
// immutable signed commerce flow fact (+1/-1) into `analytics.fact_commerce_ledger`,
// then marks the org dirty for incremental L3 snapshot refresh.
func RecordCommerceLedgerFact(ctx context.Context, fact CommerceLedgerFact) error {
	if err := InsertCommerceLedgerFacts(ctx, []CommerceLedgerFact{fact}); err != nil {
		return err
	}
	MarkOrgSnapshotDirty(fact.Org)
	return nil
}

// UpsertAnalyticsOrgBalances writes latest per-(org, currency) balance snapshots
// into `analytics.state_org_balances` with strictly monotonic versions and notifies
// the incremental L3 org snapshot refresher.
func UpsertAnalyticsOrgBalances(ctx context.Context, rows []OrgBalanceState) error {
	if len(rows) == 0 {
		return nil
	}
	stamped := make([]OrgBalanceState, len(rows))
	orgs := make([]string, 0, len(rows))
	for i, r := range rows {
		if r.Version == 0 {
			r.Version = NextAnalyticsVersion()
		}
		stamped[i] = r
		if r.Org != "" {
			orgs = append(orgs, r.Org)
		}
	}
	if err := UpsertOrgBalanceStates(ctx, stamped); err != nil {
		return err
	}
	MarkOrgsSnapshotDirty(orgs...)
	return nil
}

// UpsertAnalyticsSubscriptions writes latest subscription states into
// `analytics.state_subscriptions` with strictly monotonic versions and notifies
// the incremental L3 org snapshot refresher.
func UpsertAnalyticsSubscriptions(ctx context.Context, rows []SubscriptionState) error {
	if len(rows) == 0 {
		return nil
	}
	stamped := make([]SubscriptionState, len(rows))
	orgs := make([]string, 0, len(rows))
	for i, r := range rows {
		if r.Version == 0 {
			r.Version = NextAnalyticsVersion()
		}
		stamped[i] = r
		if r.Org != "" {
			orgs = append(orgs, r.Org)
		}
	}
	if err := UpsertSubscriptionStates(ctx, stamped); err != nil {
		return err
	}
	MarkOrgsSnapshotDirty(orgs...)
	return nil
}

// UpsertAnalyticsInvoices writes latest invoice states into
// `analytics.state_invoices` with strictly monotonic versions and notifies
// the incremental L3 org snapshot refresher.
func UpsertAnalyticsInvoices(ctx context.Context, rows []InvoiceState) error {
	if len(rows) == 0 {
		return nil
	}
	stamped := make([]InvoiceState, len(rows))
	orgs := make([]string, 0, len(rows))
	for i, r := range rows {
		if r.Version == 0 {
			r.Version = NextAnalyticsVersion()
		}
		stamped[i] = r
		if r.Org != "" {
			orgs = append(orgs, r.Org)
		}
	}
	if err := UpsertInvoiceStates(ctx, stamped); err != nil {
		return err
	}
	MarkOrgsSnapshotDirty(orgs...)
	return nil
}

// UpsertDimUsers writes user dimension rows into `analytics.dim_users` in
// bounded 200-row chunks.
func UpsertDimUsers(ctx context.Context, rows []DimUser) error {
	if len(rows) == 0 {
		return nil
	}
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	for start := 0; start < len(rows); start += analyticsInsertChunk {
		end := min(start+analyticsInsertChunk, len(rows))
		stmt, args := buildDimUsersInsert(rows[start:end], now)
		if err := Exec(ctx, stmt, args...); err != nil {
			return err
		}
	}
	return nil
}

func buildDimUsersInsert(rows []DimUser, fallback time.Time) (string, []any) {
	const cols = 9
	vals := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*cols)
	fb := fallback.UTC()
	for _, r := range rows {
		ts := r.UpdatedAt
		if ts.IsZero() {
			ts = fb
		}
		created := r.CreatedAt
		if created.IsZero() {
			created = ts
		}
		lastSignin := r.LastSigninAt
		if lastSignin.IsZero() {
			lastSignin = time.Unix(0, 0).UTC()
		}
		vals = append(vals, "(?,?,?,?,?,?,?,?,?)")
		args = append(args,
			r.Org, r.UserID, r.Name, r.Email,
			created.UTC(), lastSignin.UTC(),
			boolU8(r.IsAdmin), boolU8(r.Forbidden), ts.UTC(),
		)
	}
	return `INSERT INTO ` + AnalyticsDimUsersTable + `
		(org, user_id, name, email, created_at, last_signin_at, is_admin, forbidden, updated_at)
		VALUES ` + strings.Join(vals, ","), args
}

// ReadAnalyticsOrgBalances loads the latest per-org USD balances from
// `analytics.state_org_balances` in a single ClickHouse query.
func ReadAnalyticsOrgBalances(ctx context.Context) (map[string]OrgBalanceState, error) {
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := Query(ctx, `
		SELECT
			org,
			currency,
			argMax(books, version) AS books,
			argMax(cash_minor, version) AS cash_minor,
			argMax(credit_minor, version) AS credit_minor,
			argMax(limit_minor, version) AS limit_minor,
			argMax(owed_minor, version) AS owed_minor,
			max(version) AS version,
			max(updated_at) AS updated_at
		FROM `+AnalyticsStateOrgBalancesTable+`
		GROUP BY org, currency`)
	if err != nil {
		return nil, err
	}
	out := make(map[string]OrgBalanceState, len(rows))
	for _, r := range rows {
		org := dsString(r["org"])
		if org == "" {
			continue
		}
		cur := dsString(r["currency"])
		if cur == "" {
			cur = "usd"
		}
		curState := OrgBalanceState{
			Org:         org,
			Currency:    cur,
			Books:       dsUint64(r["books"]) != 0,
			CashMinor:   dsInt64(r["cash_minor"]),
			CreditMinor: dsInt64(r["credit_minor"]),
			LimitMinor:  dsInt64(r["limit_minor"]),
			OwedMinor:   dsInt64(r["owed_minor"]),
			Version:     dsUint64(r["version"]),
			UpdatedAt:   dsTime(r["updated_at"]),
		}
		if prev, exists := out[org]; !exists || cur == "usd" || prev.Currency != "usd" {
			out[org] = curState
		}
	}
	return out, nil
}

// UpsertWatermark writes a pipeline watermark checkpoint into `analytics.watermarks`.
func UpsertWatermark(ctx context.Context, w Watermark) error {
	if w.Pipeline == "" {
		return fmt.Errorf("watermark missing pipeline")
	}
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return err
	}
	stmt, args := buildWatermarkInsert(w, time.Now().UTC())
	return Exec(ctx, stmt, args...)
}

func buildWatermarkInsert(w Watermark, fallback time.Time) (string, []any) {
	fb := fallback.UTC()
	updated := w.UpdatedAt
	if updated.IsZero() {
		updated = fb
	}
	wmTS := w.WatermarkTS
	if wmTS.IsZero() {
		wmTS = updated
	}
	return `INSERT INTO ` + AnalyticsWatermarksTable + `
		(pipeline, source, watermark_ts, lag_ms, rows_processed, reconcile_drift_minor, status, detail, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		[]any{
			w.Pipeline, w.Source, wmTS.UTC(), w.LagMS, w.RowsProcessed,
			w.ReconcileDriftMinor, w.Status, w.Detail, updated.UTC(),
		}
}

// ReadWatermark returns the latest watermark checkpoint for pipeline.
func ReadWatermark(ctx context.Context, pipeline string) (Watermark, bool, error) {
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return Watermark{}, false, err
	}
	rows, err := Query(ctx, `
		SELECT
			argMax(source, updated_at) AS source,
			argMax(watermark_ts, updated_at) AS watermark_ts,
			argMax(lag_ms, updated_at) AS lag_ms,
			argMax(rows_processed, updated_at) AS rows_processed,
			argMax(reconcile_drift_minor, updated_at) AS reconcile_drift_minor,
			argMax(status, updated_at) AS status,
			argMax(detail, updated_at) AS detail,
			max(updated_at) AS updated_at
		FROM `+AnalyticsWatermarksTable+`
		WHERE pipeline = ?
		GROUP BY pipeline`, pipeline)
	if err != nil {
		return Watermark{}, false, err
	}
	if len(rows) == 0 {
		return Watermark{}, false, nil
	}
	r := rows[0]
	wmTS := dsTime(r["watermark_ts"])
	if wmTS.IsZero() {
		return Watermark{}, false, nil
	}
	return Watermark{
		Pipeline:            pipeline,
		Source:              dsString(r["source"]),
		WatermarkTS:         wmTS,
		LagMS:               dsInt64(r["lag_ms"]),
		RowsProcessed:       dsUint64(r["rows_processed"]),
		ReconcileDriftMinor: dsInt64(r["reconcile_drift_minor"]),
		Status:              dsString(r["status"]),
		Detail:              dsString(r["detail"]),
		UpdatedAt:           dsTime(r["updated_at"]),
	}, true, nil
}

type rollupBackfillSpec struct {
	pipeline   string
	sourceDB   string
	sourceName string
	target     string
	tsCol      string
	bucketCol  string
	insertHead string
	selectBody string
	whereExtra string
	groupBy    string
}

var analyticsRollupBackfills = []rollupBackfillSpec{
	{
		pipeline:   "backfill_rollup_ai_hourly",
		sourceDB:   "hanzo",
		sourceName: "cloud_usage",
		target:     AnalyticsRollupAIHourlyTable,
		tsCol:      "timestamp",
		bucketCol:  "hour",
		insertHead: `INSERT INTO analytics.rollup_ai_hourly`,
		selectBody: `SELECT
			toStartOfHour(timestamp) AS hour,
			toLowCardinality(organization) AS org,
			toLowCardinality(model) AS model,
			toLowCardinality(provider) AS provider,
			countState() AS requests,
			sumState(toInt64(total_tokens)) AS tokens,
			sumState(toInt64(prompt_tokens)) AS prompt_tokens,
			sumState(toInt64(completion_tokens)) AS completion_tokens,
			sumState(toInt64(cache_read_tokens)) AS cache_read_tokens,
			sumState(cost_nano) AS cost_nano,
			sumState(billed_nano) AS billed_nano,
			sumState(margin_nano) AS margin_nano,
			sumState(cache_saved_nano) AS cache_saved_nano,
			sumState(toUInt64(status = 'error')) AS errors,
			uniqState(user_id) AS users
		FROM hanzo.cloud_usage`,
		groupBy: `GROUP BY hour, org, model, provider`,
	},
	{
		pipeline:   "backfill_rollup_ai_daily",
		sourceDB:   "hanzo",
		sourceName: "cloud_usage",
		target:     AnalyticsRollupAIDailyTable,
		tsCol:      "timestamp",
		bucketCol:  "day",
		insertHead: `INSERT INTO analytics.rollup_ai_daily`,
		selectBody: `SELECT
			toDate(timestamp) AS day,
			toLowCardinality(organization) AS org,
			toLowCardinality(model) AS model,
			toLowCardinality(provider) AS provider,
			countState() AS requests,
			sumState(toInt64(total_tokens)) AS tokens,
			sumState(toInt64(prompt_tokens)) AS prompt_tokens,
			sumState(toInt64(completion_tokens)) AS completion_tokens,
			sumState(toInt64(cache_read_tokens)) AS cache_read_tokens,
			sumState(cost_nano) AS cost_nano,
			sumState(billed_nano) AS billed_nano,
			sumState(margin_nano) AS margin_nano,
			sumState(cache_saved_nano) AS cache_saved_nano,
			sumState(toUInt64(status = 'error')) AS errors,
			uniqState(user_id) AS users
		FROM hanzo.cloud_usage`,
		groupBy: `GROUP BY day, org, model, provider`,
	},
	{
		pipeline:   "backfill_rollup_o11y_hourly",
		sourceDB:   "event",
		sourceName: "span",
		target:     AnalyticsRollupO11yHourlyTable,
		tsCol:      "time",
		bucketCol:  "hour",
		insertHead: `INSERT INTO analytics.rollup_o11y_hourly`,
		selectBody: `SELECT
			toDateTime(toStartOfHour(time)) AS hour,
			org,
			service,
			countState() AS spans,
			sumState(toUInt64(status = 'error')) AS errors,
			quantileTDigestState(0.50)(duration) AS p50_ns,
			quantileTDigestState(0.95)(duration) AS p95_ns,
			quantileTDigestState(0.99)(duration) AS p99_ns
		FROM event.span`,
		groupBy: `GROUP BY hour, org, service`,
	},
	{
		pipeline:   "backfill_rollup_ai_ttft_hourly",
		sourceDB:   "event",
		sourceName: "span",
		target:     AnalyticsRollupAITTFTHourlyTable,
		tsCol:      "time",
		bucketCol:  "hour",
		insertHead: `INSERT INTO analytics.rollup_ai_ttft_hourly`,
		selectBody: `SELECT
			toDateTime(toStartOfHour(time)) AS hour,
			org,
			toLowCardinality(if(attributes['gen_ai.response.model'] != '', attributes['gen_ai.response.model'], attributes['gen_ai.request.model'])) AS model,
			sumState(toUInt64(mapContains(attributes, 'hanzo.ai.ttft_ms'))) AS ttft_count,
			quantileTDigestIfState(0.50)(toFloat64OrZero(attributes['hanzo.ai.ttft_ms']), toUInt8(mapContains(attributes, 'hanzo.ai.ttft_ms'))) AS ttft_p50,
			quantileTDigestIfState(0.95)(toFloat64OrZero(attributes['hanzo.ai.ttft_ms']), toUInt8(mapContains(attributes, 'hanzo.ai.ttft_ms'))) AS ttft_p95,
			quantileTDigestIfState(0.99)(toFloat64OrZero(attributes['hanzo.ai.ttft_ms']), toUInt8(mapContains(attributes, 'hanzo.ai.ttft_ms'))) AS ttft_p99,
			countState() AS gen_count,
			sumState(toFloat64OrZero(attributes['_o11y.gen_ai.total_cost'])) AS gen_cost_usd,
			sumState(toUInt64OrZero(if(attributes['gen_ai.usage.input_tokens'] != '', attributes['gen_ai.usage.input_tokens'], attributes['_o11y.gen_ai.usage.input_tokens']))) AS gen_input_tokens,
			sumState(toUInt64OrZero(if(attributes['gen_ai.usage.output_tokens'] != '', attributes['gen_ai.usage.output_tokens'], attributes['_o11y.gen_ai.usage.output_tokens']))) AS gen_output_tokens,
			sumState(toUInt64(status = 'error')) AS gen_errors,
			sumState(if(duration > 0, toUInt64(duration), toUInt64(0))) AS gen_duration_sum_ns,
			sumState(toUInt64(duration > 0)) AS gen_duration_count,
			quantileTDigestIfState(0.95)(toUInt64(duration), toUInt8(duration > 0)) AS gen_p95_ns
		FROM event.span`,
		whereExtra: `kind = 'client' AND mapContains(attributes, 'gen_ai.system')`,
		groupBy: `GROUP BY
			toDateTime(toStartOfHour(time)),
			org,
			toLowCardinality(if(attributes['gen_ai.response.model'] != '', attributes['gen_ai.response.model'], attributes['gen_ai.request.model']))`,
	},
	{
		pipeline:   "backfill_rollup_session_activation_hourly",
		sourceDB:   "event",
		sourceName: "span",
		target:     AnalyticsRollupSessionActivationHourlyTable,
		tsCol:      "time",
		bucketCol:  "hour",
		insertHead: `INSERT INTO analytics.rollup_session_activation_hourly`,
		selectBody: `SELECT
			toDateTime(toStartOfHour(time)) AS hour,
			org,
			toLowCardinality(attributes['hanzo.session.activation']) AS kind,
			countState() AS activations,
			sumState(toUInt64(status = 'error')) AS errors,
			quantileTDigestIfState(0.50)(toUInt64(duration), toUInt8(status != 'error')) AS p50_ns,
			quantileTDigestIfState(0.99)(toUInt64(duration), toUInt8(status != 'error')) AS p99_ns
		FROM event.span`,
		whereExtra: `name = 'hanzo.session.activate' AND attributes['hanzo.session.activation'] != ''`,
		groupBy: `GROUP BY
			toDateTime(toStartOfHour(time)),
			org,
			toLowCardinality(attributes['hanzo.session.activation'])`,
	},
	{
		pipeline:   "backfill_rollup_product_hourly",
		sourceDB:   "event",
		sourceName: "fact",
		target:     AnalyticsRollupProductHourlyTable,
		tsCol:      "time",
		bucketCol:  "hour",
		insertHead: `INSERT INTO analytics.rollup_product_hourly`,
		selectBody: `SELECT
			toDateTime(toStartOfHour(time)) AS hour,
			org,
			product,
			name,
			countState() AS events,
			uniqState(person_id) AS users,
			uniqState(session_id) AS sessions,
			sumState(bytes) AS bytes
		FROM event.fact`,
		groupBy: `GROUP BY hour, org, product, name`,
	},
}

// BackfillAnalyticsRollups idempotently populates the telemetry rollup tables
// (`rollup_ai_hourly`, `rollup_ai_daily`, `rollup_o11y_hourly`, `rollup_ai_ttft_hourly`,
// `rollup_session_activation_hourly`, `rollup_product_hourly`) directly from existing
// `hanzo.cloud_usage`, `event.span`, and `event.fact` tables without duplicating raw rows
// or double-counting live MV writes.
func BackfillAnalyticsRollups(ctx context.Context) error {
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return err
	}
	for _, spec := range analyticsRollupBackfills {
		if err := backfillOneRollup(ctx, spec); err != nil {
			return fmt.Errorf("backfill %s: %w", spec.target, err)
		}
	}
	return nil
}

func backfillOneRollup(ctx context.Context, spec rollupBackfillSpec) error {
	source := spec.sourceDB + "." + spec.sourceName
	if spec.sourceDB != "hanzo" {
		ok, err := tableExists(ctx, spec.sourceDB, spec.sourceName)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	wm, hasWM, err := ReadWatermark(ctx, spec.pipeline)
	if err != nil {
		return err
	}
	// Once initial backfill completes, the live MV owns all subsequent ingestion.
	if hasWM && wm.Status == "ok" {
		return nil
	}

	// Measure current source bounds so backfill never overlaps concurrent live writes.
	bounds, err := Query(ctx, `SELECT count() AS c, max(`+spec.tsCol+`) AS max_ts FROM `+source)
	if err != nil {
		return err
	}
	if len(bounds) == 0 || dsUint64(bounds[0]["c"]) == 0 {
		return nil
	}
	srcCount := dsUint64(bounds[0]["c"])
	upperBound := dsTime(bounds[0]["max_ts"])
	if upperBound.IsZero() {
		return nil
	}

	var stmt string
	var args []any
	detail := "full historical backfill"

	extra := ""
	if spec.whereExtra != "" {
		extra = " AND " + spec.whereExtra
	}

	if hasWM && !wm.WatermarkTS.IsZero() {
		if !upperBound.After(wm.WatermarkTS) {
			return nil
		}
		stmt = spec.insertHead + "\n" + spec.selectBody + "\nWHERE " + spec.tsCol + " > ? AND " + spec.tsCol + " <= ?" + extra + "\n" + spec.groupBy
		args = []any{wm.WatermarkTS.UTC(), upperBound.UTC()}
		detail = "incremental backfill since watermark"
	} else {
		// If the live MV has already populated recent buckets before backfill ran,
		// only backfill strictly prior to the earliest existing bucket so we never
		// double-count live MV rows.
		existing, err := Query(ctx, `SELECT count() AS c, min(`+spec.bucketCol+`) AS first_bucket FROM `+spec.target)
		if err != nil {
			return err
		}
		if len(existing) > 0 && dsUint64(existing[0]["c"]) > 0 {
			firstBucket := dsTime(existing[0]["first_bucket"])
			if !firstBucket.IsZero() {
				stmt = spec.insertHead + "\n" + spec.selectBody + "\nWHERE " + spec.tsCol + " < ?" + extra + "\n" + spec.groupBy
				args = []any{firstBucket.UTC()}
				detail = "pre-mv historical backfill"
			}
		} else {
			stmt = spec.insertHead + "\n" + spec.selectBody + "\nWHERE " + spec.tsCol + " <= ?" + extra + "\n" + spec.groupBy
			args = []any{upperBound.UTC()}
		}
	}

	if stmt != "" {
		if err := Exec(ctx, stmt, args...); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	return UpsertWatermark(ctx, Watermark{
		Pipeline:      spec.pipeline,
		Source:        source,
		WatermarkTS:   upperBound.UTC(),
		LagMS:         max(now.Sub(upperBound).Milliseconds(), 0),
		RowsProcessed: srcCount,
		Status:        "ok",
		Detail:        detail,
		UpdatedAt:     now,
	})
}
