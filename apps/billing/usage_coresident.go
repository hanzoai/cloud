package billing

// usage_coresident.go — the co-resident source for GET /v1/billing/usage.
//
// WHY IT EXISTS. balance() already reads cloud's OWN finance ledger directly rather
// than proxying "/v1/billing/balance" through the commerce transport, because co-resident the
// ONLY registration of that path is balance() itself (commerce's api.Route() is behind
// //go:build cloud and never compiled here), so the S2S proxy re-dispatches BY PATH
// straight back into the same handler, which self-answers "sign in to view billing"
// (the in-proc hop carries no validated principal). usage() had the SAME defect on
// "/v1/billing/usage" — a valid caller's usage read re-entered usage() and failed. This
// file gives usage() the co-resident answer balance() already has: the usage ledger read
// straight from finance (the wallet→revenue debits RecordUsage wrote), off the
// self-dispatching hop. Split deploy (no co-resident finance) falls back to the commerce
// S2S read, unchanged.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
)

// coResidentUsage builds the customer usage envelope from cloud's OWN finance ledger
// when the money plane is co-resident (finance.Current() published AND exposes the
// usage read). It returns (body, true, nil) with the commerce-shaped
// {user,count,usage:[...]} envelope — enriched + optionally ?product=filtered /
// ?groupBy=product-reduced exactly like the proxied path — or (nil, false, nil) when
// this deployment runs no commerce at all, so the caller falls back to the commerce
// S2S read. This is what keeps /v1/billing/usage off the self-dispatching commerce transport
// hop; a real read failure surfaces as a non-nil error (never a masked-empty ledger).
//
// Absence is the ROUTER's word (cloud.ErrNoPeer), never an inference from a failed
// call: a dead peer read as an absent one falls back to a URL that is unset in exactly
// the deployments where the plane is the real path, and the customer is told billing is
// not configured on a fleet whose ledger is one socket away.
func coResidentUsage(ctx context.Context, org, product, groupBy string) ([]byte, bool, error) {
	fin := finance.Current()
	if fin == nil {
		// No ledger here — every process but commerce. Ask the one that has it
		// before falling back to the S2S read, which is 501 unless a commerce URL is
		// configured. That 501 is what a customer's usage page showed once apps
		// became their own binaries.
		ctx, cancel := context.WithTimeout(ctx, usagePeerTimeout)
		defer cancel()
		reply, err := cloud.Ask[struct{}, plane.UsageRows](cloud.For(ctx, org), "commerce",
			plane.FinanceUsage, &struct{}{})
		if err != nil {
			if errors.Is(err, cloud.ErrNoPeer) {
				return nil, false, nil // no commerce in this fleet → the configured S2S read
			}
			// The peer is here and the read failed. Ask's contract names ErrNoPeer as
			// the ONE error a caller may read as "fall back"; every other one is an
			// outage, and reading them all as absence is what turned a dead commerce
			// into a phantom split deploy and a 501 on a customer's usage page.
			return nil, true, fmt.Errorf("usage: commerce ledger read: %w", err)
		}
		if reply == nil {
			// A void reply is not an empty ledger. Nothing was read, so nothing is known.
			return nil, true, errors.New("usage: commerce answered nothing")
		}
		rows := make([]finance.UsageRow, 0, len(reply.Rows))
		for _, r := range reply.Rows {
			// Round DOWN, explicitly — the same choice balance.go:126 already made,
			// for the same reason, on a row read from the same ledger.
			//
			// Minor() REFUSES anything finer than a cent rather than round behind
			// the caller. A per-token AI charge is routinely finer than a cent, so
			// on this path the refusal was not an edge case: ONE sub-cent row in the
			// page failed the whole read, and the caller (billing.go:458) tests err
			// before coResident, so it answered 502 "billing upstream unreachable"
			// with nothing upstream involved. That is the 2026-08-03 balance bug
			// verbatim; balance and ai were converted then, this row was missed.
			// Measured 2026-08-06: /v1/billing/balance 200, /v1/billing/usage 502,
			// same org, same ledger, same process — only this call differed.
			cents, cerr := r.Amount.FloorMinor()
			if cerr != nil {
				// The peer ANSWERED and the reply did not parse — a real failure,
				// not an absent ledger. Report it as handled so it surfaces here
				// rather than falling through to a commerce proxy that is not
				// configured on this deployment and would mask it as a 501.
				return nil, true, fmt.Errorf("usage: commerce ledger row %s: %w", r.ID, cerr)
			}
			// Carry the EXACT debit, not just its rounding. usageEnvelope emits it as
			// `decimal`, and that field is the whole reason a page of sub-cent calls
			// totals correctly instead of totalling zero — dropping Amount here would
			// have traded the 502 for a silently understated bill.
			exact, perr := money.ParseUSD(r.Amount.Decimal)
			if perr != nil {
				return nil, true, fmt.Errorf("usage: commerce ledger row %s amount %q: %w", r.ID, r.Amount.Decimal, perr)
			}
			rows = append(rows, finance.UsageRow{ID: r.ID, Model: r.Model, Cents: cents, Amount: exact, CreatedAt: r.CreatedAt})
		}
		env := usageEnvelope(org, rows)
		if out, ok := enrichUsageLedger(env, product, groupBy); ok {
			return out, true, nil
		}
		return env, true, nil
	}
	// The usage read is an OPTIONAL capability (the base FinanceClient is
	// Balance+Deposit+RecordUsage); a finance impl without it falls back to the proxy.
	lister, ok := fin.(interface {
		ListUsage(context.Context, string, bool, int) ([]finance.UsageRow, error)
	})
	if !ok {
		return nil, false, nil
	}
	rows, err := lister.ListUsage(ctx, org, false, 2000)
	if err != nil {
		return nil, false, err
	}
	env := usageEnvelope(org, rows)
	if out, ok := enrichUsageLedger(env, product, groupBy); ok {
		return out, true, nil
	}
	return env, true, nil
}

// usageEnvelope renders finance usage rows as commerce's GetUsage envelope
// ({user,count,usage:[{transactionId,amount,metadata,createdAt}]}) — the exact shape the
// console's normalizeUsageRecords + this package's enrichUsageLedger already parse.
// amount is USD cents; metadata carries the metered unit (model) the debit recorded, so
// enrichUsageLedger can still attribute a product where the unit implies one.
func usageEnvelope(org string, rows []finance.UsageRow) []byte {
	type usageRow struct {
		TransactionID string `json:"transactionId"`
		Amount        int64  `json:"amount"`
		// Decimal is the SAME debit, exact — the ledger's 18-decimal value as a
		// decimal string, the spelling plane.Money already uses. `amount` stays
		// cents because that is the wire the console and enrichUsageLedger parse
		// today; this field is what lets a reader stop summing roundings. A page
		// of sub-cent calls totals correctly from `decimal` and totals ZERO from
		// `amount` — that difference is the 24% the console understated.
		Decimal   string         `json:"decimal,omitempty"`
		Metadata  map[string]any `json:"metadata"`
		CreatedAt string         `json:"createdAt"`
	}
	out := make([]usageRow, 0, len(rows))
	for _, r := range rows {
		md := map[string]any{}
		if r.Model != "" {
			md["model"] = r.Model
		}
		row := usageRow{
			TransactionID: r.ID,
			Amount:        r.Cents,
			Metadata:      md,
			CreatedAt:     time.Unix(r.CreatedAt, 0).UTC().Format(time.RFC3339),
		}
		// A zero Amount is a row built WITHOUT the exact value (a test double, or
		// a builder that predates the field) — a real debit is never zero, the
		// guards drop those before the ledger. Omit the field rather than assert
		// a 0 the cents beside it deny.
		if !r.Amount.IsZero() {
			row.Decimal = r.Amount.String()
		}
		out = append(out, row)
	}
	body, _ := json.Marshal(map[string]any{"user": org, "count": len(out), "usage": out})
	return body
}

// usagePeerTimeout bounds the cross-process usage read. A usage page is
// interactive: a slow ledger must surface rather than hold the request open.
const usagePeerTimeout = 10 * time.Second
