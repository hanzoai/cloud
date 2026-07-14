package marketplace

import (
	"context"

	"github.com/hanzoai/cloud/clients/tools"
)

// pricer fills the tool plane's Pricer seam: it maps a tool name to the cheapest
// public monetized listing's price + recipient wallet, so a per-call dispatch of a
// monetized tool settles to the seller through the x402 Charger. Returns nil for an
// unlisted or free tool (no charge).
type pricer struct {
	store *Store
}

func (p *pricer) PriceFor(ctx context.Context, _ tools.Scope, tool string) *tools.Price {
	l, ok := p.store.CheapestPublicForTool(ctx, tool)
	if !ok {
		return nil
	}
	return &tools.Price{AmountCents: l.PriceCents, Currency: l.Currency, Recipient: l.Recipient}
}
