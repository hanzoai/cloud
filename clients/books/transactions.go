package books

// transactions.go — the unified, filterable TRANSACTIONS read: GET /v1/books/transactions.
// One row per booked voucher, projected across every source (bank_txn, scan, commerce) into
// a single shape {date, description, vendor, category, source, amountCents, voucherId}, newest
// first, filterable by date window, category, and vendor. It is strictly READ-ONLY over the
// immutable ledger — it never posts — so it can restate the books a hundred ways but never
// move them.
//
// A "transaction" here is a VOUCHER: its amount is the voucher's total debit (== total
// credit), its category is the P&L account it touched (the expense or income line), and its
// vendor is resolved from the source metadata (a bank row's merchant, or the scan/commerce
// description). This turns the double-entry ledger into the single-line register a human reads.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// Txn is one register row — a booked voucher projected to a single line.
type Txn struct {
	Date         string `json:"date"`
	Description  string `json:"description"`
	Vendor       string `json:"vendor,omitempty"`
	Category     string `json:"category"` // COA account number of the P&L line
	CategoryName string `json:"categoryName,omitempty"`
	Source       string `json:"source"` // source_kind: bank_txn | scan | commerce_txn
	AmountCents  int64  `json:"amountCents"`
	VoucherID    int64  `json:"voucherId"`
}

// txnFilter is the parsed query for a transactions read.
type txnFilter struct {
	from, to string // RFC3339 posting-time window (inclusive)
	category string // COA account number or slug
	vendor   string // case-insensitive substring
	limit    int
}

// transactionsHandler answers GET /v1/books/transactions?from&to&category&vendor&limit for
// the caller's OWN org.
func transactionsHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view transactions")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	f := txnFilter{
		from:     strings.TrimSpace(c.Query("from")),
		to:       strings.TrimSpace(c.Query("to")),
		category: strings.TrimSpace(c.Query("category")),
		vendor:   strings.TrimSpace(c.Query("vendor")),
		limit:    atoiDefault(c.Query("limit"), 200),
	}
	rows, err := st.listTransactions(c.Context(), f)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "transactions read failed")
	}
	return booksJSON(c, map[string]any{"transactions": rows})
}

// glLeg is one leg loaded for the projection.
type glLeg struct {
	voucherID  int64
	postingAt  string
	desc       string
	sourceKind string
	sourceID   string
	account    string
	debit      int64
	credit     int64
}

// listTransactions projects the booked ledger to register rows. It loads the legs (with an
// optional posting-time window), groups them by voucher, and for each voucher computes the
// amount (total debit), the P&L category line, and the vendor (from bank metadata or the
// description). Category and vendor filters are applied after projection; the newest `limit`
// surviving rows are returned.
func (s *store) listTransactions(ctx context.Context, f txnFilter) ([]Txn, error) {
	q := `SELECT g.voucher_id, g.posting_at, v.description, g.source_kind, g.source_id, g.account, g.debit, g.credit
	      FROM gl_entry g JOIN voucher v ON v.id = g.voucher_id`
	var args []any
	var where []string
	if f.from != "" {
		where = append(where, "g.posting_at >= ?")
		args = append(args, f.from)
	}
	if f.to != "" {
		where = append(where, "g.posting_at <= ?")
		args = append(args, f.to)
	}
	for i, w := range where {
		if i == 0 {
			q += " WHERE " + w
		} else {
			q += " AND " + w
		}
	}
	q += " ORDER BY g.voucher_id DESC, g.id ASC"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("books listTransactions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Group legs by voucher, preserving newest-first voucher order.
	var order []int64
	byVoucher := map[int64][]glLeg{}
	for rows.Next() {
		var l glLeg
		if err := rows.Scan(&l.voucherID, &l.postingAt, &l.desc, &l.sourceKind, &l.sourceID, &l.account, &l.debit, &l.credit); err != nil {
			return nil, err
		}
		if _, seen := byVoucher[l.voucherID]; !seen {
			order = append(order, l.voucherID)
		}
		byVoucher[l.voucherID] = append(byVoucher[l.voucherID], l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	merchants, err := s.bankMerchants(ctx)
	if err != nil {
		return nil, err
	}

	wantCategory := ""
	if f.category != "" {
		wantCategory = categoryAccount(f.category)
	}
	wantVendor := strings.ToLower(f.vendor)

	out := []Txn{}
	for _, vid := range order {
		t := projectVoucher(byVoucher[vid], merchants)
		if wantCategory != "" && t.Category != wantCategory {
			continue
		}
		if wantVendor != "" && !strings.Contains(strings.ToLower(t.Vendor+" "+t.Description), wantVendor) {
			continue
		}
		out = append(out, t)
		if f.limit > 0 && len(out) >= f.limit {
			break
		}
	}
	return out, nil
}

// projectVoucher collapses one voucher's legs into a register row: amount = total debit,
// category = the P&L (expense/income) account with the largest absolute movement, vendor =
// bank merchant for a bank source else the description.
func projectVoucher(legs []glLeg, merchants map[string]string) Txn {
	if len(legs) == 0 {
		return Txn{}
	}
	head := legs[0]
	var total, best int64
	category := ""
	for _, l := range legs {
		total += l.debit
		if a, ok := accountByNumber[l.account]; ok && (a.Type == Expense || a.Type == Income) {
			if mv := abs64(l.debit - l.credit); mv >= best {
				best, category = mv, l.account
			}
		}
	}
	vendor := ""
	if head.sourceKind == bankSourceKind {
		vendor = merchants[head.sourceID]
	}
	if vendor == "" {
		vendor = head.desc
	}
	return Txn{
		Date:         head.postingAt,
		Description:  head.desc,
		Vendor:       strings.TrimSpace(vendor),
		Category:     category,
		CategoryName: accountName(category),
		Source:       head.sourceKind,
		AmountCents:  total,
		VoucherID:    head.voucherID,
	}
}

// bankMerchants maps a bank voucher's source_id (connector:external_id) to its merchant, so
// a bank-sourced transaction shows the payee. Empty when no bank rows exist.
func (s *store) bankMerchants(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT connector, external_id, merchant, description FROM bank_txn`)
	if err != nil {
		return nil, fmt.Errorf("books bankMerchants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var connector, externalID, merchant, description string
		if err := rows.Scan(&connector, &externalID, &merchant, &description); err != nil {
			return nil, err
		}
		out[connector+":"+externalID] = firstNonEmpty(strings.TrimSpace(merchant), strings.TrimSpace(description))
	}
	return out, rows.Err()
}

// accountName returns an account's human name from the fixed chart ("" if unknown).
func accountName(number string) string {
	if a, ok := accountByNumber[number]; ok {
		return a.Name
	}
	return ""
}
