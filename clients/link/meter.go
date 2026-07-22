package link

// meter.go is the reference Meter: it records a served routed call to (a) the
// per-account routed-usage counter — so the dashboard's per-account breakdown and
// the global view reflect what the GATEWAY routed, not just device-collector
// snapshots — and (b), for an api-key account, the org's commerce money ledger.
//
// BILLING FOLLOWS THE ACCOUNT (the registry's standing rule). BillingMode(kind)
// decides: a subscription account's plan pays the provider directly, so it is
// metered for VISIBILITY only and never charged; an api-key account bills via
// commerce on the existing gateway meter. So a routed call is metered exactly once
// per meaning — usage always, money only when the account's kind bills money — and
// never double-charged.
//
// A CREDENTIAL NEVER REACHES HERE. The Meter is handed the account IDENTITY, the
// kind, and the served token counts — never the Credential. There is no code path
// by which metering could observe or emit a secret.

import (
	"context"
	"time"

	"github.com/hanzoai/cloud/clients/metering"
	luxlog "github.com/luxfi/log"
)

// meterProvider is the commerce provider/service label a routed metered debit
// carries — the SAME "ai" label the inference meter (metered_ai.go) uses, so routed
// spend sums into the org's ai scope exactly like any other inference.
const meterProvider = "ai"

// Pricer returns the retail charge, in whole USD cents, for one served routed call.
// It is injected so the router never fabricates a price: the operator sets the BYO
// platform-fee policy, and the default (ZeroPrice) charges nothing — usage is still
// metered, but no money is invented.
type Pricer func(res Result) int64

// ZeroPrice charges nothing. It is the honest default for bring-your-own-account
// routing: the customer pays the provider directly on their own key, so absent an
// operator-set platform fee, Hanzo records the USAGE without inventing a charge.
func ZeroPrice(Result) int64 { return 0 }

// routeMeter is the reference Meter over the routed-usage counter + the commerce
// metering client.
type routeMeter struct {
	store   *Store
	billing *metering.Client
	price   Pricer
	log     luxlog.Logger
	now     func() time.Time
}

// NewMeter builds the reference Meter. A nil billing client disables the money debit
// (usage is still counted); a nil price defaults to ZeroPrice; a nil clock defaults
// to time.Now.
func NewMeter(store *Store, billing *metering.Client, price Pricer, log luxlog.Logger) Meter {
	if price == nil {
		price = ZeroPrice
	}
	return &routeMeter{store: store, billing: billing, price: price, log: log, now: time.Now}
}

// RecordRouted records one served routed call. It never blocks or fails the served
// request: the money debit is best-effort and the usage add is fail-soft, so a
// metering hiccup costs a row of history and nothing else.
func (m *routeMeter) RecordRouted(ctx context.Context, org, subject string, a Account, kind string, res Result) {
	var cents int64
	// Money: only an api-key account bills via commerce; a subscription is plan-paid.
	if BillingMode(kind) == BillingCommerce {
		cents = m.price(res)
		if cents > 0 && m.billing != nil && m.billing.Enabled() {
			// Append-only debit on the org's balance, tagged as ai inference — the
			// SAME ledger and labels the inference meter uses, so routed spend shows in
			// /v1/billing/usage beside every other call.
			if _, err := m.billing.Record(ctx, metering.Usage{
				User:             org,
				Org:              org,
				Provider:         meterProvider,
				Service:          meterProvider,
				Model:            res.Model,
				AmountCents:      cents,
				PromptTokens:     int(res.PromptTokens),
				CompletionTokens: int(res.CompletionTokens),
				TotalTokens:      int(res.TotalTokens),
				Status:           "success",
			}); err != nil && m.log != nil {
				m.log.Debug("routed metered debit skipped", "org", org, "account", a.String(), "err", err.Error())
			}
		}
	}
	// Usage: the per-account counter, always — this is the visibility ledger the
	// per-account breakdown reads. Fail-soft.
	if m.store != nil {
		if err := m.store.AddRouted(ctx, org, subject, a, kind, res, cents, m.now().Unix()); err != nil && m.log != nil {
			m.log.Debug("routed usage add skipped", "org", org, "account", a.String(), "err", err.Error())
		}
	}
}
