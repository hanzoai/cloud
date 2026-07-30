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
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/plane"
)

// coResidentUsage builds the customer usage envelope from cloud's OWN finance ledger
// when the money plane is co-resident (finance.Current() published AND exposes the
// usage read). It returns (body, true, nil) with the commerce-shaped
// {user,count,usage:[...]} envelope — enriched + optionally ?product=filtered /
// ?groupBy=product-reduced exactly like the proxied path — or (nil, false, nil) when
// finance is not co-resident (split deploy), so the caller falls back to the commerce
// S2S read. This is what keeps /v1/billing/usage off the self-dispatching commerce transport
// hop; a real read failure surfaces as a non-nil error (never a masked-empty ledger).
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
		if err != nil || reply == nil {
			return nil, false, nil // no peer either → the configured S2S read
		}
		rows := make([]finance.UsageRow, 0, len(reply.Rows))
		for _, r := range reply.Rows {
			cents, cerr := r.Amount.Minor()
			if cerr != nil {
				return nil, false, cerr
			}
			rows = append(rows, finance.UsageRow{ID: r.ID, Model: r.Model, Cents: cents, CreatedAt: r.CreatedAt})
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
		ListUsage(context.Context, string, int) ([]finance.UsageRow, error)
	})
	if !ok {
		return nil, false, nil
	}
	rows, err := lister.ListUsage(ctx, org, 2000)
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
