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

// The welcome grant, published for the same reason the balance read is: the ledger
// has one writer and it lives here.
//
// StarterGrant is middleware on EVERY app's chain, and it used to bail the moment
// it found no local ledger — which, once apps became their own binaries, is every
// process but this one. So a new account was never funded: it reached tracker or
// billing, the grant looked for a ledger that was one socket away, and returned
// silently. An org that should have started with the welcome credit started broke,
// and the paywall refused it correctly for a reason nobody had chosen.
//
// The idempotency key is the ACCOUNT and nothing else, so asking twice — from two
// processes, after a restart, or concurrently — grants once. That property lives in
// finance, which dedups inside the same transaction as the insert; this method only
// carries the question across.
const starterMethod = "finance.starter"

func exposeStarter() {
	cloud.Expose(starterMethod, func(ctx context.Context, who cloud.Ident, req []byte) ([]byte, error) {
		subject, _, err := cloud.BalanceReq(req)
		if err != nil {
			return nil, err
		}
		org := who.Org
		if org == "" {
			return nil, fmt.Errorf("starter: no org on the capability")
		}
		if subject == "" {
			subject = org
		}
		cents, err := cloud.GrantStarter(ctx, org, subject)
		if err != nil {
			return nil, fmt.Errorf("starter: %w", err)
		}
		return cloud.PutI64(cents), nil
	})
}

// The usage list, for the same reason as the balance and the grant: one writer,
// and it is here.
//
// The reply is the product's OWN response envelope, relayed verbatim as opaque
// bytes. That is not a JSON payload on the plane in the sense payloads.go forbids —
// there is no internal contract being spelled here, only a body the caller already
// knows how to render, moved across a process boundary it used to not have.
const usageMethod = "finance.usage"

// usageReadLimit matches what the co-resident reader asks for, so the page a
// customer sees does not change with which process answered.
const usageReadLimit = 2000

func exposeUsage() {
	cloud.Expose(usageMethod, func(ctx context.Context, who cloud.Ident, req []byte) ([]byte, error) {
		// subject rides the balance request shape; the usage read is org-scoped, so
		// the subject is carried for symmetry and the org comes from the capability.
		_, _, err := cloud.BalanceReq(req)
		if err != nil {
			return nil, err
		}
		org := who.Org
		if org == "" {
			return nil, fmt.Errorf("usage: no org on the capability")
		}
		fin := financeclient.Current()
		if fin == nil {
			return nil, fmt.Errorf("usage: no ledger in the process that owns it")
		}
		lister, ok := fin.(interface {
			ListUsage(context.Context, string, int) ([]financeclient.UsageRow, error)
		})
		if !ok {
			return nil, fmt.Errorf("usage: this ledger does not list usage")
		}
		rows, err := lister.ListUsage(ctx, org, usageReadLimit)
		if err != nil {
			return nil, fmt.Errorf("usage: %w", err)
		}
		out := make([]cloud.UsageRow, 0, len(rows))
		for _, r := range rows {
			out = append(out, cloud.UsageRow{ID: r.ID, Model: r.Model, Cents: r.Cents, CreatedAt: r.CreatedAt})
		}
		return cloud.PutUsageRows(out), nil
	})
}

// The ledger's entries. Three customer-facing pages read this one list, and all
// three answered 501 from a process that does not hold the ledger.
const txnsMethod = "finance.txns"

func exposeTxns() {
	cloud.Expose(txnsMethod, func(ctx context.Context, who cloud.Ident, req []byte) ([]byte, error) {
		if _, _, err := cloud.BalanceReq(req); err != nil {
			return nil, err
		}
		org := who.Org
		if org == "" {
			return nil, fmt.Errorf("txns: no org on the capability")
		}
		fin := financeclient.Current()
		if fin == nil {
			return nil, fmt.Errorf("txns: no ledger in the process that owns it")
		}
		lister, ok := fin.(interface {
			ListEntries(context.Context, string, int) ([]financeclient.TxnRow, error)
		})
		if !ok {
			return nil, fmt.Errorf("txns: this ledger does not list entries")
		}
		rows, err := lister.ListEntries(ctx, org, usageReadLimit)
		if err != nil {
			return nil, fmt.Errorf("txns: %w", err)
		}
		out := make([]cloud.Txn, 0, len(rows))
		for _, r := range rows {
			out = append(out, cloud.Txn{
				ID: r.ID, Kind: r.Kind, Ref: r.Ref, Memo: r.Memo,
				Atto:      r.Amount.AttoString(),
				CreatedAt: r.CreatedAt,
			})
		}
		return cloud.PutTxns(out), nil
	})
}
