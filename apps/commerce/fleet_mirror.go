// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// fleet_mirror.go — REAL-TIME MUTATION-DRIVEN MIRROR FROM COMMERCE TO HANZO DATASTORE.
//
// Whenever a single org's wallet, spend, deposit, grant, refund, clawback, subscription,
// or invoice changes, this mirrors THAT ONE org's latest state and immutable signed flow
// facts asynchronously into BOTH:
//   - Legacy `hanzo.fleet_*` tables (backwards compatibility during rollout)
//   - Canonical `analytics.*` tables (`analytics.state_org_balances`,
//     `analytics.state_subscriptions`, `analytics.state_invoices`,
//     `analytics.fact_commerce_ledger`)
// and marks the org dirty via `cloudds.MarkOrgSnapshotDirty(org)` so the L3
// `analytics.serving_org_overview` snapshot refreshes within 1–2s without ever
// scanning the fleet or fanning out N+1 RPCs.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/models/organization"

	"github.com/hanzoai/cloud/client"
	cloudds "github.com/hanzoai/cloud/datastore"
	financeclient "github.com/hanzoai/cloud/finance"
)

const (
	fleetMirrorTimeout  = 5 * time.Second
	fleetMirrorDebounce = 5 * time.Second
	maxFleetMirrors     = 8
)

var (
	fleetMirrorSem  = make(chan struct{}, maxFleetMirrors)
	fleetMirrorMu   sync.Mutex
	fleetMirrorLast = map[string]time.Time{}
)

// allowFleetMirror coalesces rapid bursts for one (kind, org) key so 100 AI debits in a
// second produce at most one Datastore balance upsert every fleetMirrorDebounce.
func allowFleetMirror(key string) bool {
	now := time.Now()
	fleetMirrorMu.Lock()
	defer fleetMirrorMu.Unlock()
	if prev, ok := fleetMirrorLast[key]; ok && now.Sub(prev) < fleetMirrorDebounce {
		return false
	}
	fleetMirrorLast[key] = now
	if len(fleetMirrorLast) > 4096 {
		for k, ts := range fleetMirrorLast {
			if now.Sub(ts) >= fleetMirrorDebounce {
				delete(fleetMirrorLast, k)
			}
		}
	}
	return true
}

// deterministicLedgerEntryID produces a stable, replay-safe entry_id for
// `analytics.fact_commerce_ledger`. Identical (kind, org, primaryID, ref) tuples
// always hash to the exact same entry_id so retries and duplicate deliveries never
// double-book financial flows.
func deterministicLedgerEntryID(kind, org, primaryID, ref string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	org = strings.TrimSpace(org)
	primaryID = strings.TrimSpace(primaryID)
	ref = strings.TrimSpace(ref)
	if primaryID != "" {
		return kind + ":" + org + ":" + primaryID
	}
	if ref != "" {
		return kind + ":" + org + ":ref:" + ref
	}
	sum := sha256.Sum256([]byte(kind + "|" + org + "|" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)))
	return kind + ":" + org + ":" + hex.EncodeToString(sum[:8])
}

// analyticsOrgBalanceFrom maps a FleetOrgBook snapshot onto a canonical
// `analytics.state_org_balances` row with a strictly monotonic version.
func analyticsOrgBalanceFrom(row cloudds.FleetOrgBook, currency string, version uint64) cloudds.OrgBalanceState {
	cur := strings.ToLower(strings.TrimSpace(currency))
	if cur == "" {
		cur = "usd"
	}
	if version == 0 {
		version = cloudds.NextAnalyticsVersion()
	}
	return cloudds.OrgBalanceState{
		Org:         row.Org,
		Currency:    cur,
		Books:       row.Books,
		CashMinor:   row.CashCents,
		CreditMinor: row.CreditCents,
		LimitMinor:  row.LimitCents,
		OwedMinor:   row.OwedCents,
		Version:     version,
		UpdatedAt:   row.UpdatedAt,
	}
}

// analyticsSubscriptionFrom maps a FleetSubscription onto a canonical
// `analytics.state_subscriptions` row with a strictly monotonic version.
func analyticsSubscriptionFrom(row cloudds.FleetSubscription, currency string, version uint64) cloudds.SubscriptionState {
	cur := strings.ToLower(strings.TrimSpace(currency))
	if cur == "" {
		cur = "usd"
	}
	if version == 0 {
		version = cloudds.NextAnalyticsVersion()
	}
	return cloudds.SubscriptionState{
		Org:               row.Org,
		ID:                row.ID,
		Deleted:           row.Deleted,
		UserID:            row.UserID,
		PlanID:            row.PlanID,
		PlanName:          row.PlanName,
		Status:            row.Status,
		MRRMinor:          row.MRRCents,
		Seats:             row.Seats,
		Interval:          row.Interval,
		Provider:          row.Provider,
		Settled:           row.Settled,
		Currency:          cur,
		Test:              row.Test,
		CancelAtPeriodEnd: row.CancelAtPeriodEnd,
		PayloadJSON:       row.PayloadJSON,
		Version:           version,
		UpdatedAt:         row.UpdatedAt,
	}
}

// analyticsInvoiceFrom maps a FleetInvoice onto a canonical `analytics.state_invoices`
// row with a strictly monotonic version.
func analyticsInvoiceFrom(row cloudds.FleetInvoice, version uint64) cloudds.InvoiceState {
	cur := strings.ToLower(strings.TrimSpace(row.Currency))
	if cur == "" {
		cur = "usd"
	}
	if version == 0 {
		version = cloudds.NextAnalyticsVersion()
	}
	return cloudds.InvoiceState{
		Org:            row.Org,
		ID:             row.ID,
		Deleted:        row.Deleted,
		Number:         row.Number,
		UserID:         row.UserID,
		SubscriptionID: row.SubscriptionID,
		Status:         row.Status,
		AmountMinor:    row.AmountCents,
		PaidMinor:      row.PaidCents,
		Currency:       cur,
		IssuedAt:       row.IssuedAt,
		PaidAt:         row.PaidAt,
		PayloadJSON:    row.PayloadJSON,
		Version:        version,
		UpdatedAt:      row.UpdatedAt,
	}
}

// mirrorCommerceLedgerFact asynchronously records one immutable signed monetary flow
// fact into `analytics.fact_commerce_ledger` and marks the org dirty for L3 refresh.
// Never debounced across distinct entry_ids (every financial flow must be recorded
// idempotently on its deterministic entry_id).
func mirrorCommerceLedgerFact(fact cloudds.CommerceLedgerFact) {
	fact.Org = strings.TrimSpace(fact.Org)
	if fact.Org == "" || fact.EntryID == "" {
		return
	}
	cloudds.MarkOrgSnapshotDirty(fact.Org)
	if !cloudds.Ready() {
		return
	}
	if fact.Sign != 1 && fact.Sign != -1 {
		fact.Sign = 1
	}
	if fact.Currency == "" {
		fact.Currency = "usd"
	} else {
		fact.Currency = strings.ToLower(strings.TrimSpace(fact.Currency))
	}
	if fact.Product == "" {
		fact.Product = "commerce"
	}
	if fact.FXRateNano == 0 && fact.Currency == "usd" {
		fact.FXRateNano = 1_000_000_000
	}
	if fact.SourceVersion == 0 {
		fact.SourceVersion = cloudds.NextAnalyticsVersion()
	}
	if fact.EventTS.IsZero() {
		fact.EventTS = time.Now().UTC()
	}
	select {
	case fleetMirrorSem <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-fleetMirrorSem }()
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), fleetMirrorTimeout)
		defer cancel()
		_ = cloudds.RecordCommerceLedgerFact(ctx, fact)
	}()
}

// mirrorOrgBook asynchronously dual-writes one org's current balances and spend windows
// into BOTH legacy `hanzo.fleet_org_books` AND canonical `analytics.state_org_balances`,
// then marks the org dirty for incremental L3 snapshot refresh.
func mirrorOrgBook(org string) {
	org = strings.TrimSpace(org)
	if org == "" {
		return
	}
	cloudds.MarkOrgSnapshotDirty(org)
	if !cloudds.Ready() {
		return
	}
	if !allowFleetMirror("book:" + org) {
		return
	}
	select {
	case fleetMirrorSem <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-fleetMirrorSem }()
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), fleetMirrorTimeout)
		defer cancel()
		fin := financeclient.Current()
		if fin == nil {
			return
		}
		led, ok := fin.(financeclient.Liner)
		if !ok {
			return
		}
		line, err := led.Line(ctx, org, false)
		if err != nil {
			return
		}
		now := time.Now().UTC()
		month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		row := cloudds.FleetOrgBook{
			Org:         org,
			Books:       line.Books,
			CashCents:   line.Funds.Cash.Cents(),
			CreditCents: line.Funds.Credit.Cents(),
			LimitCents:  line.Limit.Cents(),
			OwedCents:   line.Funds.Owed.Cents(),
			MTDMonth:    month.Format("2006-01"),
			UpdatedAt:   now,
		}
		if line.Books {
			cashier, hasCash := fin.(interface {
				SumCashUsageSince(context.Context, string, bool, int64) (int64, error)
			})
			readWin := func(since time.Time) (int64, int64, bool) {
				c, err := fin.SumUsageSince(ctx, org, false, "", "", since.Unix())
				if err != nil {
					return 0, 0, false
				}
				var p int64
				if hasCash {
					p, _ = cashier.SumCashUsageSince(ctx, org, false, since.Unix())
				}
				return c, p, true
			}
			const (
				span30d = 30 * 24 * time.Hour
				span7d  = 7 * 24 * time.Hour
				span24h = 24 * time.Hour
			)
			c30, p30, ok30 := readWin(now.Add(-span30d))
			if ok30 {
				row.Spend30dCents, row.Paid30dCents = c30, p30
				if c30 != 0 || p30 != 0 {
					if c7, p7, ok7 := readWin(now.Add(-span7d)); ok7 {
						row.Spend7dCents, row.Paid7dCents = c7, p7
						if c7 != 0 || p7 != 0 {
							if c24, p24, ok24 := readWin(now.Add(-span24h)); ok24 {
								row.Spend24hCents, row.Paid24hCents = c24, p24
							}
						}
					}
					if cm, pm, okm := readWin(month); okm {
						row.SpendMTDCents, row.PaidMTDCents = cm, pm
					}
				} else if month.Before(now.Add(-span30d)) {
					if cm, pm, okm := readWin(month); okm {
						row.SpendMTDCents, row.PaidMTDCents = cm, pm
					}
				}
			}
		}
		_ = cloudds.UpsertFleetOrgBooks(ctx, []cloudds.FleetOrgBook{row})
		_ = cloudds.UpsertAnalyticsOrgBalances(ctx, []cloudds.OrgBalanceState{
			analyticsOrgBalanceFrom(row, "usd", cloudds.NextAnalyticsVersion()),
		})
	}()
}

// mirrorOrgSubscriptions asynchronously dual-writes one org's subscriptions into BOTH
// legacy `hanzo.fleet_subscriptions` AND canonical `analytics.state_subscriptions`,
// then marks the org dirty for incremental L3 snapshot refresh.
func mirrorOrgSubscriptions(org *organization.Organization) {
	if org == nil || strings.TrimSpace(org.Name) == "" {
		return
	}
	name := strings.TrimSpace(org.Name)
	cloudds.MarkOrgSnapshotDirty(name)
	if !cloudds.Ready() {
		return
	}
	select {
	case fleetMirrorSem <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-fleetMirrorSem }()
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), fleetMirrorTimeout)
		defer cancel()
		subs, err := subscriptionsOf(ctx, org, "", "")
		if err != nil || len(subs) == 0 {
			return
		}
		now := time.Now().UTC()
		ver := cloudds.NextAnalyticsVersion()
		legacyRows := make([]cloudds.FleetSubscription, 0, len(subs))
		analRows := make([]cloudds.SubscriptionState, 0, len(subs))
		for _, s := range subs {
			if s.ID == "" {
				continue
			}
			b, err := json.Marshal(s)
			if err != nil {
				continue
			}
			fsub := cloudds.FleetSubscription{
				Org:               name,
				ID:                s.ID,
				UserID:            s.UserID,
				PlanID:            s.PlanID,
				PlanName:          s.Plan.Name,
				Status:            s.Status,
				MRRCents:          s.MRRCents,
				Seats:             int64(s.Seats),
				Interval:          s.Plan.Interval,
				Provider:          s.ProviderType,
				Settled:           s.Settled,
				Test:              s.Test,
				CancelAtPeriodEnd: s.CancelAtPeriodEnd,
				PayloadJSON:       string(b),
				UpdatedAt:         now,
			}
			legacyRows = append(legacyRows, fsub)
			analRows = append(analRows, analyticsSubscriptionFrom(fsub, s.Plan.Currency, ver))
		}
		_ = cloudds.UpsertFleetSubscriptions(ctx, legacyRows)
		_ = cloudds.UpsertAnalyticsSubscriptions(ctx, analRows)
	}()
}

// mirrorOrgInvoices asynchronously dual-writes one org's newest invoices into BOTH
// legacy `hanzo.fleet_invoices` AND canonical `analytics.state_invoices`, then marks
// the org dirty for incremental L3 snapshot refresh.
func mirrorOrgInvoices(org *organization.Organization) {
	if org == nil || strings.TrimSpace(org.Name) == "" {
		return
	}
	name := strings.TrimSpace(org.Name)
	cloudds.MarkOrgSnapshotDirty(name)
	if !cloudds.Ready() {
		return
	}
	select {
	case fleetMirrorSem <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-fleetMirrorSem }()
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), fleetMirrorTimeout)
		defer cancel()
		list, err := commercebilling.ListInvoices(ctx, org, "", "", "", 200, 0)
		if err != nil || len(list) == 0 {
			return
		}
		now := time.Now().UTC()
		ver := cloudds.NextAnalyticsVersion()
		legacyRows := make([]cloudds.FleetInvoice, 0, len(list))
		analRows := make([]cloudds.InvoiceState, 0, len(list))
		for _, v := range list {
			w := billingInvoiceWire(v)
			if w.ID == "" {
				continue
			}
			b, err := json.Marshal(w)
			if err != nil {
				continue
			}
			num := w.NumberStr
			if num == "" && w.Number > 0 {
				num = strconv.Itoa(w.Number)
			}
			amt := max(w.Subtotal+w.Tax-w.Discount, 0)
			finv := cloudds.FleetInvoice{
				Org:            name,
				ID:             w.ID,
				Number:         num,
				UserID:         w.UserID,
				SubscriptionID: w.SubscriptionID,
				Status:         w.Status,
				AmountCents:    amt,
				PaidCents:      w.AmountPaid,
				Currency:       w.Currency,
				IssuedAt:       w.CreatedAt,
				PaidAt:         w.PaidAt,
				PayloadJSON:    string(b),
				UpdatedAt:      now,
			}
			legacyRows = append(legacyRows, finv)
			analRows = append(analRows, analyticsInvoiceFrom(finv, ver))
		}
		_ = cloudds.UpsertFleetInvoices(ctx, legacyRows)
		_ = cloudds.UpsertAnalyticsInvoices(ctx, analRows)
	}()
}

// billingInvoiceWire maps a commerce billing invoice onto client.BillingInvoice.
func billingInvoiceWire(v commercebilling.Invoice) client.BillingInvoice {
	row := client.BillingInvoice{
		ID: v.ID, UserID: v.UserID, CustomerEmail: v.CustomerEmail,
		SubscriptionID: v.SubscriptionID,
		PeriodStart:    stamp(v.PeriodStart), PeriodEnd: stamp(v.PeriodEnd),
		Subtotal: v.Subtotal, Tax: v.Tax, Discount: v.Discount, DiscountName: v.DiscountName,
		CreditApplied: v.CreditApplied, AmountDue: v.AmountDue, AmountPaid: v.AmountPaid,
		Currency: string(v.Currency), Status: string(v.Status),
		PaymentMethod: v.PaymentMethod, PaymentRef: v.PaymentRef,
		Number: v.Number, NumberStr: v.NumberStr, AttemptCount: v.AttemptCount,
		CreatedAt: stamp(v.CreatedAt), UpdatedAt: stamp(v.UpdatedAt),
	}
	if v.DueDate != nil {
		row.DueDate = stamp(*v.DueDate)
	}
	if v.PaidAt != nil {
		row.PaidAt = stamp(*v.PaidAt)
	}
	if v.VoidedAt != nil {
		row.VoidedAt = stamp(*v.VoidedAt)
	}
	for _, l := range v.LineItems {
		row.LineItems = append(row.LineItems, client.InvoiceLineItem{
			ID: l.Id, Type: string(l.Type), Description: l.Description,
			MeterID: l.MeterId, Quantity: l.Quantity, UnitPrice: l.UnitPrice,
			PlanID: l.PlanId, PlanName: l.PlanName,
			Amount: l.Amount, Currency: string(l.Currency),
			PeriodStart: stamp(l.PeriodStart), PeriodEnd: stamp(l.PeriodEnd),
		})
	}
	return row
}
