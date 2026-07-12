// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	cur "github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	commerceorg "github.com/hanzoai/cloud/clients/commerce/pkg/org"
)

// BalanceCents returns subject's available prepaid balance (USD cents) in org, read
// DIRECTLY from the co-resident embedded commerce ledger — no HTTP hop. It is the native
// twin of the /v1/billing/balance read (billing.zapGetBalance): resolve the org's own
// datastore namespace, tally the subject's iam-user transactions in the currency, and
// return Balance - Holds clamped at zero. It reuses the SAME currentEmbedded seam the
// in-process entitlement client resolves through.
//
// This is the read the money cutover (admin/finance backfill) and the admin cockpit's
// credit panels use when commerce runs in the SAME binary: the admin commerce HTTP client
// dials an unroutable in-proc address and reads $0, which would silently migrate/report
// nothing. When commerce is NOT co-resident this returns an ERROR (never 0), so a caller
// can tell "not wired" from a real zero balance and fail loud rather than move money on a
// phantom figure. Subject is lowercased + trimmed; an empty currency defaults to usd.
func BalanceCents(ctx context.Context, org, subject, currency string, test bool) (int64, error) {
	org = strings.TrimSpace(org)
	subject = strings.ToLower(strings.TrimSpace(subject))
	if org == "" || subject == "" {
		return 0, fmt.Errorf("commerce.BalanceCents: empty org or subject")
	}

	// Commerce must be co-resident to read its ledger in-process. An unresolvable Embedded
	// means the ledger is unreadable here; return an ERROR so a caller never mistakes a
	// missing wiring for a real $0 balance (the fail-loud contract).
	if e := currentEmbedded(); e == nil || e.app == nil {
		return 0, fmt.Errorf("commerce.BalanceCents: commerce not co-resident; cannot read balance (org=%s subject=%s)", org, subject)
	}

	// Resolve org → its datastore namespace via commerce's OWN canonical resolver — the
	// SAME binding CheckEntitlement and the money path use, so a read can never cross orgs.
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		return 0, fmt.Errorf("commerce.BalanceCents: resolve org %q: %w", org, err)
	}

	code := cur.Type(strings.ToLower(strings.TrimSpace(currency)))
	if code == "" {
		code = cur.USD
	}

	datas, err := util.GetTransactionsByCurrency(o.Namespaced(ctx), subject, "iam-user", code, test)
	if err != nil {
		return 0, fmt.Errorf("commerce.BalanceCents: balance query for %q: %w", subject, err)
	}

	var available cur.Cents
	if d, ok := datas.Data[code]; ok {
		available = d.Balance - d.Holds
	}
	if available < 0 {
		available = 0
	}
	return int64(available), nil
}
