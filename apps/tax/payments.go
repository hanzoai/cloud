package tax

// payments.go — the payments the caller's org made to other orgs, read off the
// event plane, and what each one means for the payer's year.
//
// ONE READ PATH. Every rail that moves value between two orgs states the payment
// on the event plane as an economic event (client.EconomicEvent), and this is the
// one place tax reads them: the event app answers the caller's own events and
// nobody else's, whichever rail moved them. A rail is not a source here — it is a
// field on the event — so a rail that starts stating payments is in the next tax
// year without a line changing in this package.
//
// A payment is read as it was FIRST stated, and a payment a correction restates is
// read as the correction: the plane names the superseded ones, and they are left
// out, so a year never counts one payment twice.
//
// An event plane this deployment does not run, or one that does not answer, fails
// the read: a tax year computed from nothing is a wrong return that looks right.

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/hanzoai/cloud/client"
	eventpeer "github.com/hanzoai/cloud/client/event"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// read answers the economic events the caller's org is a party to in [from, to),
// oldest first, leaving out every one a correction restates.
func (o ops) read(ctx context.Context, from, to int64) ([]client.EconomicEvent, error) {
	var rows []client.EconomicEvent
	in := &client.EconomicsIn{From: from, To: to, Limit: pageOfEvents}
	for {
		out, err := eventpeer.EventEconomics(ctx, in)
		switch {
		case errors.Is(err, cloud.ErrNoPeer):
			return nil, zip.Errorf(http.StatusServiceUnavailable, "tax: this deployment runs no event plane, so no payment can be read")
		case err != nil:
			return nil, zip.Errorf(http.StatusBadGateway, "tax: event plane: %v", err)
		}
		for _, e := range out.Rows {
			if !slices.Contains(out.Superseded, e.ID) {
				rows = append(rows, e)
			}
		}
		if out.Next == "" {
			return rows, nil
		}
		in.Cursor = out.Next
	}
}

// pageOfEvents is how many events one read of the plane asks for: its most.
const pageOfEvents = 1000

// paid reports whether payer has ever paid payee — the relationship a W-9 request
// needs.
func (o ops) paid(ctx context.Context, payer, payee string) (bool, error) {
	rows, err := o.read(ctx, epoch, time.Now().Add(time.Hour).Unix())
	if err != nil {
		return false, err
	}
	for _, e := range rows {
		if e.Buyer == payer && e.Seller == payee {
			return true, nil
		}
	}
	return false, nil
}

// epoch is the start of the first tax year rules.go holds: no payment before it
// is anybody's to report here.
var epoch, _ = span(slices.Min(slices.Collect(maps.Keys(years))))

// span is a calendar year in unix seconds, UTC.
func span(year int) (int64, int64) {
	return time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC).Unix(),
		time.Date(year+1, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
}

// Line is one payment the payer made, classified.
type Line struct {
	// ID is the economic event's id.
	ID string `json:"id"`
	// Rail is how it moved: x402, ledger, chain, ach, wire, card, network or other.
	Rail string `json:"rail"`
	// Proof is the rail's own reference for the payment.
	Proof string `json:"proof"`
	// Paid is when, unix seconds.
	Paid int64 `json:"paid"`
	// AmountCents is its fair market value in U.S. dollars on the date paid, in
	// cents — for a U.S. dollar payment, what was paid.
	AmountCents int64 `json:"amountCents"`
	// Memo is what was bought, in the rail's words.
	Memo string `json:"memo,omitempty"`
	// Verdict is whether it is reportable, where, and the rules that decided it.
	Verdict Verdict `json:"verdict"`
}

// Total is one box's sum for one payee, tested against the year's threshold.
type Total struct {
	// Box is the form and box.
	Box Box `json:"box"`
	// AmountCents is the reportable payments in this box, summed exactly and then
	// rounded to cents.
	AmountCents int64 `json:"amountCents"`
	// ThresholdCents is the year's threshold for this box.
	ThresholdCents int64 `json:"thresholdCents"`
	// Reportable is true when the total is at or above the threshold.
	Reportable bool `json:"reportable"`
	// Rule states the comparison and its source.
	Rule Rule `json:"rule"`
	// Payments are the ids that sum into it.
	Payments []string `json:"payments"`

	sum money.Amount
}

// PayeeYear is everything the payer paid one payee in a year.
type PayeeYear struct {
	// Payee is the org paid.
	Payee string `json:"payee"`
	// Category is what the payer states it bought from this payee; services when
	// it has stated nothing.
	Category Category `json:"category"`
	// W9 is where the payer's W-9 request to this payee stands: none, requested,
	// granted, declined or revoked.
	W9 string `json:"w9"`
	// Lines are the payments, oldest first.
	Lines []Line `json:"lines"`
	// Totals are the reportable sums by box.
	Totals []Total `json:"totals"`
}

// Ledger is the caller's tax year as a payer.
type Ledger struct {
	// Year is the tax year.
	Year int `json:"year"`
	// ThresholdCents is the §6041(a) threshold for the year.
	ThresholdCents int64 `json:"thresholdCents"`
	// Source names where the year's numbers were read.
	Source string `json:"source"`
	// Payees is every org the caller paid, by org id.
	Payees []PayeeYear `json:"payees"`
}

// counterparty is what the payer knows about one payee: its own row, the
// payee's grant, and — under a live grant — the payee's W-9.
type counterparty struct {
	row    *payeeRow
	share  *shareRow
	w9     *record
	tin    TIN // the payee's, opened by the payee's own store
	certOK bool
}

func (cp counterparty) status() string {
	if cp.share == nil {
		return "none"
	}
	return cp.share.status
}

func (cp counterparty) granted() bool { return cp.share != nil && cp.share.status == shareGranted }

func (cp counterparty) category() Category {
	if cp.row != nil && cp.row.category.Valid() {
		return cp.row.category
	}
	return Services
}

// counterparty reads what payer knows about payee. The payee's W-9 is read only
// under a live grant; everything else of the payee's stays in its file.
func (o ops) counterparty(ctx context.Context, payer string, pst *store, payee string) (counterparty, error) {
	var cp counterparty
	row, err := pst.payee(ctx, payee)
	switch {
	case err == errNotFound:
		return cp, nil
	case err != nil:
		return cp, err
	}
	cp.row = row
	est, err := o.other(payee)
	if err != nil {
		return cp, err
	}
	sh, err := est.share(ctx, row.share)
	switch {
	case err == errNotFound:
		return cp, nil
	case err != nil:
		return cp, err
	}
	if sh.payer != payer {
		return cp, nil // a share id this payer does not hold names nothing of the payer's
	}
	cp.share = sh
	if !cp.granted() {
		return cp, nil
	}
	prof, err := est.profile(ctx)
	switch {
	case err == errNotFound:
		return cp, nil
	case err != nil:
		return cp, err
	}
	var r record
	if err := decode(prof.data, &r); err != nil {
		return cp, err
	}
	cp.w9, cp.tin = &r, prof.tin
	cp.certOK = r.Certification.Status == certCertified && r.Certification.Version == prof.version
	return cp, nil
}

// derive is the payer's year: every payment it made to another org, classified,
// and summed per payee and box against the year's threshold.
func (o ops) derive(ctx context.Context, payer string, pst *store, year int) (*Ledger, map[string]counterparty, error) {
	y, err := YearOf(year)
	if err != nil {
		return nil, nil, zip.Errorf(http.StatusUnprocessableEntity, "tax: %v", err)
	}
	from, to := span(year)
	rows, err := o.read(ctx, from, to)
	if err != nil {
		return nil, nil, err
	}
	l := &Ledger{Year: year, ThresholdCents: y.Threshold.Cents(), Source: y.Source}
	byPayee := map[string]*PayeeYear{}
	cps := map[string]counterparty{}
	var order []string
	for _, e := range rows {
		if e.Buyer != payer || e.Seller == payer {
			continue // a payment the caller received, or moved between its own books
		}
		py, ok := byPayee[e.Seller]
		if !ok {
			cp, err := o.counterparty(ctx, payer, pst, e.Seller)
			if err != nil {
				return nil, nil, err
			}
			cps[e.Seller] = cp
			py = &PayeeYear{Payee: e.Seller, Category: cp.category(), W9: cp.status()}
			byPayee[e.Seller] = py
			order = append(order, e.Seller)
		}
		amount, err := money.ParseUSD(e.FMV)
		if err != nil {
			return nil, nil, zip.Errorf(http.StatusBadGateway, "tax: payment %s: fair market value: %v", e.ID, err)
		}
		held := cps[e.Seller]
		v := Classify(e, held.w9.payee(held.certOK, py.Category))
		py.Lines = append(py.Lines, Line{ID: e.ID, Rail: e.Rail, Proof: e.Proof, Paid: e.At,
			AmountCents: amount.Cents(), Memo: e.Service, Verdict: v})
		if v.Reportable {
			py.add(*v.Box, e.ID, amount)
		}
	}
	for _, payee := range order {
		py := byPayee[payee]
		for i := range py.Totals {
			t := &py.Totals[i]
			t.AmountCents = t.sum.Cents()
			t.ThresholdCents = y.thresholdFor(t.Box).Cents()
			t.Reportable = t.sum.Cmp(y.thresholdFor(t.Box)) >= 0
			t.Rule = thresholdRule(y, t.Box, t.sum)
		}
		l.Payees = append(l.Payees, *py)
	}
	return l, cps, nil
}

// add sums one reportable payment into its box.
func (py *PayeeYear) add(b Box, id string, amount money.Amount) {
	for i := range py.Totals {
		if py.Totals[i].Box == b {
			py.Totals[i].sum = py.Totals[i].sum.Add(amount)
			py.Totals[i].Payments = append(py.Totals[i].Payments, id)
			return
		}
	}
	py.Totals = append(py.Totals, Total{Box: b, sum: money.Zero().Add(amount), Payments: []string{id}})
}

// yearIn names a tax year.
type yearIn struct {
	// Year is the calendar year the payments were made in, e.g. 2026.
	Year int `json:"year"`
}

// Returns the caller org's tax year as a PAYER: every payment it made to another
// org in that calendar year, on any rail, as the event plane records it, each
// classified — reportable or not, in which 1099 box, and the rule that decided it,
// with its source — and each payee's totals tested against the year's threshold.
//
// The rules applied: a payment settled by a card or a third-party settlement
// network is reported on Form 1099-K by the settlement entity, not here; a payment
// for merchandise is not reported; a payment to a corporation is generally not
// reported, except for legal services; a payment in a digital asset is reported at
// its fair market value on the date paid; a box is reported only at or above the
// year's threshold — $600 through 2025, $2,000 for 2026 under P.L. 119-21, and not
// guessed for a year the IRS has not yet published. A payee's classification comes
// only from a W-9 it granted the caller; without one, nothing establishes an
// exemption.
//
// Org admins only. It reads; it prepares nothing and files nothing.
func (o ops) payments(ctx context.Context, in *yearIn) (*Ledger, error) {
	org, st, err := o.admin(ctx, "read the tax year", false)
	if err != nil {
		return nil, err
	}
	l, _, err := o.derive(ctx, org, st, in.Year)
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	return l, nil
}
