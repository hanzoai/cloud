package marketplace

// payments.go closes BOTH halves of the pay-per-use door this subsystem opens the
// moment a listing is allowed to declare a price. A price nobody can pay is not a
// price; it is a lie in the shop window.
//
//	registry  →  x402.Publish     the resource→Terms table x402 enforces against:
//	                              what a listed tool costs and which wallet is paid.
//	charger   →  tools.SetCharger the seam tools.Registry.Dispatch settles through,
//	                              which is x402.Settle on that same resource.
//
// ONE table, two doors onto it. The marketplace declares what is priced and who is
// paid; x402 enforces it; the tool plane knows neither. Nothing else in the fleet
// decides what a listed tool costs.
//
// CO-RESIDENCY. All three seams are process-globals (x402.reg, tools.std,
// wallets.mounted), so this wiring binds within ONE process. See the package doc in
// marketplace.go for what that means for the split fleet.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/apps/x402"
)

// resourcePrefix namespaces a TOOL as an x402 resource. x402 resources are opaque
// ids and the Enforce middleware keys on request PATHS, so the prefix is what keeps
// the two key spaces from ever colliding: no path begins "tool:", so publishing this
// table can never accidentally put a price on a route, and a tool price can never be
// bought by hitting a URL.
//
// A tool needs its own id at all because every tool call arrives on the SAME route,
// POST /v1/tools/call, with the tool named in the body — the path cannot say which
// capability is being bought.
const resourcePrefix = "tool:"

func resourceOf(tool string) string { return resourcePrefix + tool }

func toolOf(resource string) (string, bool) {
	name, ok := strings.CutPrefix(resource, resourcePrefix)
	return name, ok && name != ""
}

// registry is the x402 price table: resource → Terms, answered from the listing
// store. It is the ONE authority on what a listed tool costs and who is paid for it.
type registry struct{ store *Store }

// Price resolves a resource's payment terms for x402.
//
// The recipient is taken from the ROW — the listing's own publisher org and the
// payout wallet it named — never from anything the caller said. wallets resolves a
// wallet id only within the org it is asked for, so terms built this way cannot name
// a wallet outside the publishing org, and a buyer cannot redirect a credit at all.
//
// ok=false means FREE (no enforcement): an unlisted tool, a listing with no price,
// and any resource that is not a tool id (every request path the Enforce middleware
// asks about) all land here. A store failure is an ERROR, so x402 fails closed
// rather than serving a priced tool for nothing.
func (g *registry) Price(ctx context.Context, resource string) (x402.Terms, bool, error) {
	tool, ok := toolOf(resource)
	if !ok {
		return x402.Terms{}, false, nil // not a tool resource — this table prices nothing else
	}
	l, ok, err := g.store.CheapestPublicForTool(ctx, tool)
	if err != nil {
		return x402.Terms{}, false, fmt.Errorf("marketplace: price %s: %w", resource, err)
	}
	if !ok {
		return x402.Terms{}, false, nil
	}
	return x402.Terms{
		Amount:            l.Price,
		RecipientOrg:      l.PublisherOrg,
		RecipientWalletID: l.Recipient,
	}, true, nil
}

// charger is the tool plane's payment seam: it settles a tool call through the x402
// flow — challenge, verify, settle once — against the price table above.
//
// It holds NO state. Everything it needs is already somewhere authoritative: the
// tool name arrives as the argument, the payer is the attested principal on the
// context, and the price and payee are the registry's. That is the whole point of
// the seam being this narrow.
type charger struct{}

// Charge settles one tool call, or refuses it.
//
// A FREE tool costs nothing and returns nil — including off the HTTP path, where
// there is no request to attest a payer, because a free call needs none.
//
// A PRICED tool with no payment is refused with tools.ErrPaymentRequired, which the
// tool surface answers 402 with; x402 has already written the challenge to the
// response's X-Payment-Required header, so the client learns the amount, the payee
// address and the chain, signs an ERC-3009 authorization over exactly those terms,
// and retries. Every other failure — the rail down, the payee wallet unresolvable,
// a caller with no billable ledger — is returned as-is and fails the call CLOSED. A
// tool that cannot be paid for is never served free.
func (charger) Charge(ctx context.Context, tool string) error {
	err := x402.Settle(ctx, resourceOf(tool))
	if errors.Is(err, x402.ErrPaymentRequired) {
		return fmt.Errorf("%w: %v", tools.ErrPaymentRequired, err)
	}
	return err
}
