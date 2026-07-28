// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	financeclient "github.com/hanzoai/cloud/apps/finance"
)

// The prepaid ledger is a per-org SQLite store with ONE writer, so exactly one
// process may open it — and that process is the one that mounts commerce, because
// wireFinance builds the ledger only when commerce is enabled. Every other app
// therefore has no ledger to read, which is not a configuration gap to fill in but
// the single-writer property working as intended.
//
// So the ledger is asked, not opened. This is the method that answers, over the
// internal plane (ZAP on the callee's unix socket), and it is why billing can
// report a balance from a process that must never touch the file.
//
// It stays deliberately dumb: the caller resolves WHOSE balance it wants and this
// reads it. Subject resolution needs the request — the billing account claim, the
// user name the edge minted — and pushing that here would mean a second copy of
// principal.Subject deriving a payer from an Ident that does not carry the same
// facts. One resolver, at the edge that has the request.
const balanceMethod = "finance.balance"

// exposeBalance publishes the ledger read on the internal plane. Mount calls it.
//
// The request codec is cloud.PutBalanceReq/BalanceReq and the reply is a bare
// scalar (cloud.PutI64) — the wire contract lives in cloud/payloads.go so both
// ends read it from ONE definition and cannot drift. It carries no org field at
// all; see below.
func exposeBalance() {
	cloud.Expose(balanceMethod, func(ctx context.Context, who cloud.Ident, req []byte) ([]byte, error) {
		subject, currency, err := cloud.BalanceReq(req)
		if err != nil {
			return nil, fmt.Errorf("balance: decode: %w", err)
		}
		// The ORG is taken from the delegated capability, never from the payload: a
		// caller that could name its own org would be naming another tenant's books.
		// The payload cannot express an org, so this is the ONLY way one is chosen.
		// The subject is the caller's to choose, but only within that org — it is a
		// wallet inside the ledger the capability already pinned.
		org := who.Org
		if org == "" {
			return nil, cloud.Fault(403, "balance: no org on the capability")
		}
		if subject == "" {
			subject = org
		}
		if currency == "" {
			currency = "usd"
		}

		fin := financeclient.Current()
		if fin == nil {
			// This process mounts commerce, so the ledger is built before routes are
			// served. Nil here means the boot order changed, and answering zero would
			// report every account as broke.
			return nil, fmt.Errorf("balance: no ledger in the process that owns it")
		}
		bal, err := fin.Balance(ctx, org, subject, currency, false)
		if err != nil {
			return nil, fmt.Errorf("balance: read %s/%s: %w", org, subject, err)
		}
		return cloud.PutI64(bal.Cents()), nil
	})
}
