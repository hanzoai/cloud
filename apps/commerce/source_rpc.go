// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// source_rpc.go — WHICH EARLIER DEPOSITS WERE PAYMENTS, and what the books owe in cash.
//
// finance keeps a wallet's cash apart from its credit (finance/cash.go), and every
// deposit written before it did sits in credit. Two kinds of record say which of those
// were payments: commerce's own receipt of a settled processor payment, which this
// process reads, and an operator's record of a payment taken outside the card flow — a
// wire, an invoice paid in the processor's dashboard — which the admin surface reads
// off its grant audit trail and names here. finance does the rest: it replays each
// wallet cash first and moves the unspent part into cash, once.

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud/client"
	financeclient "github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/transaction"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	"github.com/zap-proto/zip"
)

func exposeSource() {
	cloud.Plane().Post("/finance/source", planeSource,
		zip.WithOperationID(client.FinanceSource),
		zip.WithSummary("Record which earlier deposits were payments"))
	cloud.Plane().Post("/finance/cash", planeCash,
		zip.WithOperationID(client.FinanceCash),
		zip.WithSummary("What this org's books owe customers in cash"))
	cloud.Plane().Post("/finance/wires", planeWires,
		zip.WithOperationID(client.FinanceWires),
		zip.WithSummary("This org's deposits keyed on a bank wire, and which were reversed"))
}

// Answers every deposit in the caller org's books whose ref names a bank wire
// ("wire:…", any case), oldest first, each with whether a reversal undid it. It is
// the books' own answer to "does this org hold that wire", which the admin grant
// asks before it records one, so a trail row lost after the money moved cannot
// hide the money. The ORG comes from the CALLER.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeWires(ctx context.Context, _ *cloud.Unit) (*client.WireDeposits, error) {
	org, err := callerOrg(ctx, "wires")
	if err != nil {
		return nil, err
	}
	led, err := ledgerOf("wires")
	if err != nil {
		return nil, err
	}
	rows, err := led.Deposits(ctx, org, false)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "wires %s: %v", org, err)
	}
	out := &client.WireDeposits{Rows: []client.WireDeposit{}}
	for _, d := range rows {
		if !strings.HasPrefix(strings.ToLower(d.Ref), "wire:") {
			continue
		}
		out.Rows = append(out.Rows, client.WireDeposit{ID: d.ID, Ref: d.Ref, Wallet: d.Wallet,
			Amount: client.Amount(d.Amount.Unwrap()), CreatedAt: d.CreatedAt, Reversed: d.Reversed})
	}
	return out, nil
}

// sourcer is the ledger that keeps cash apart from credit.
type sourcer interface {
	Deposits(context.Context, string, bool) ([]financeclient.DepositRow, error)
	Source(context.Context, string, bool, map[string]bool, bool) ([]financeclient.Sourced, error)
	Cash(context.Context, string, bool) (financeclient.OrgCash, error)
}

func ledgerOf(op string) (sourcer, error) {
	fin, err := books(op)
	if err != nil {
		return nil, err
	}
	s, ok := fin.(sourcer)
	if !ok {
		return nil, fmt.Errorf("%s: this ledger does not keep cash apart", op)
	}
	return s, nil
}

// Answers which of the caller org's earlier deposits were payments, and records them.
//
// A deposit is a payment when an operator names it with the record it was read off,
// or when its own note is one a settlement path wrote AND commerce holds the settled
// receipt for that processor reference and amount. Everything else stays credit —
// money the platform minted. Apply false answers the whole table and moves nothing;
// apply true posts each wallet's source entry, exactly once per wallet.
//
// A named deposit that is not in these books, or is already cash, is refused before
// anything posts.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSource(ctx context.Context, in *client.SourceIn) (*client.Sourcing, error) {
	org, err := callerOrg(ctx, "source")
	if err != nil {
		return nil, err
	}
	// Naming a deposit a payment opens paid models to its org: a platform decision,
	// taken by a SuperAdmin through the admin surface, never by a peer on its own word.
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("source: SuperAdmin required")
	}
	led, err := ledgerOf("source")
	if err != nil {
		return nil, err
	}
	rows, err := led.Deposits(ctx, org, false)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source %s: %v", org, err)
	}
	named := map[string]string{}
	for _, p := range in.Paid {
		named[p.Entry] = strings.TrimSpace(p.Basis)
	}
	out := &client.Sourcing{Deposits: []client.SourceDeposit{}, Wallets: []client.SourceWallet{}}
	paid := map[string]bool{}
	for _, r := range rows {
		d := client.SourceDeposit{
			Entry: r.ID, Wallet: r.Wallet, Ref: r.Ref, Memo: r.Memo,
			Amount: client.Amount(r.Amount.Unwrap()), CreatedAt: r.CreatedAt, Reversed: r.Reversed,
			Cash: r.Cash(),
		}
		switch basis, ok := named[r.ID]; {
		case r.Sourced:
			d.Basis = "sourced as a payment"
		case d.Cash:
			d.Basis = "deposited as cash"
		case ok:
			if basis == "" {
				return nil, zip.ErrBadRequest(fmt.Sprintf("source %s: %s is named without the record it was read off", org, r.ID))
			}
			d.Cash, d.Basis = true, basis
		default:
			if b, serr := settledDeposit(ctx, org, r); serr != nil {
				return nil, zip.Errorf(http.StatusBadGateway, "source %s: %v", org, serr)
			} else if b != "" {
				d.Cash, d.Basis = true, b
			}
		}
		if d.Cash && !r.Cash() {
			paid[r.ID] = true
		}
		delete(named, r.ID)
		out.Deposits = append(out.Deposits, d)
	}
	for id := range named {
		return nil, zip.ErrBadRequest(fmt.Sprintf("source %s: %s is not a deposit in these books", org, id))
	}
	if len(paid) == 0 {
		return out, nil
	}
	done, err := led.Source(ctx, org, false, paid, in.Apply)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source %s: %v", org, err)
	}
	for _, w := range done {
		out.Wallets = append(out.Wallets, client.SourceWallet{
			Wallet: w.Wallet, Paid: client.Amount(w.Paid.Unwrap()), Cash: client.Amount(w.Cash.Unwrap()),
			Balance: client.Amount(w.Balance.Unwrap()), Entries: w.Entries, Holds: w.Holds,
			Posted: w.Posted, Done: w.Done,
		})
	}
	return out, nil
}

// settlementNote reads the processor reference out of a note a settlement path wrote:
// the card core's receipt ("Top-up via square (ref: <ref>)") and the provider webhook
// ("settlement <ref>"). Any other note names none.
var settlementNote = regexp.MustCompile(`^(?:Top-up via [a-z0-9_-]+ \(ref: ([^)\s]+)\)|settlement (\S+))$`)

// settledDeposit answers the basis on which a deposit is a settled processor payment,
// or "" when it is not one: its note names its own ref as a processor reference, and
// commerce's books hold the settled deposit receipt stamped with that reference for
// that amount. The note alone is not enough — a minted credit's reason is free text.
func settledDeposit(ctx context.Context, org string, r financeclient.DepositRow) (string, error) {
	m := settlementNote.FindStringSubmatch(strings.TrimSpace(r.Memo))
	if m == nil {
		return "", nil
	}
	ref := m[1] + m[2]
	if ref != r.Ref {
		return "", nil
	}
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		return "", fmt.Errorf("resolve org %q: %w", org, err)
	}
	t := transaction.New(datastore.New(o.Namespaced(ctx)))
	found, err := t.Query().Filter("SourceId=", ref).Get()
	if err != nil {
		return "", fmt.Errorf("read the receipt for %s: %w", ref, err)
	}
	if !found || t.Type != transaction.Deposit || int64(t.Amount) != r.Amount.Cents() {
		return "", nil
	}
	return fmt.Sprintf("settled %s payment %s (commerce receipt %s)", t.SourceKind, ref, t.Id()), nil
}

// Answers what the caller org's books owe customers in cash: every dollar payments
// brought in, the cash still unspent, what cash paid for, and the minted credit still
// unspent. An org with no ledger answers Books false and is never handed an empty one.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCash(ctx context.Context, _ *cloud.Unit) (*client.OrgCash, error) {
	org, err := callerOrg(ctx, "cash")
	if err != nil {
		return nil, err
	}
	led, err := ledgerOf("cash")
	if err != nil {
		return nil, err
	}
	c, err := led.Cash(ctx, org, false)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "cash %s: %v", org, err)
	}
	return &client.OrgCash{
		Books:  c.Books,
		Paid:   client.Amount(c.Paid.Unwrap()),
		Owed:   client.Amount(c.Owed.Unwrap()),
		Spent:  client.Amount(c.Spent.Unwrap()),
		Credit: client.Amount(c.Credit.Unwrap()),
	}, nil
}
