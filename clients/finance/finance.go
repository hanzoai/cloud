// Package finance is the ZAP-native money subsystem: a per-CUSTOMER, double-entry
// PREPAID WALLET on the native ledger core. It is the in-process implementation of
// cloud's types.FinanceClient (package alias finance.Client, mirroring commerce.Client)
// — the ONE money seam the ai prepaid gate, the admin grant, and the edge meter all
// bill through.
//
// ONE LIGHTWEIGHT FILE PER ORG. Each org's books are an isolated Hanzo Base (SQLite)
// file at <dataDir>/orgs/<org>/finance.db (a separate <...>/finance-test.db for sandbox
// money, so test and live never mix). There is NO shared or relational database — the
// file IS the tenant boundary, so one org's wallet writes can never appear in another
// org's read. A file opens on first use and is cached; opens are serialized so a
// concurrent first touch opens exactly once.
//
// MONEY-SAFETY. Every write is a BALANCED double-entry posting inside the org's own
// file: a deposit is funding:platform → wallet (credit the customer, debit the platform
// float); a usage debit is wallet → revenue:platform (debit the customer, credit
// platform revenue) — so every customer debit IS a platform-revenue credit in one atomic
// entry, and a file's postings always sum to zero. Usage is idempotent on RequestID (the
// ledger's (kind,program,ref) idempotency), so a retried debit charges AT MOST ONCE; a
// deposit takes a fresh ref each call (grants are additive; DepositInput carries no
// idempotency key). Amounts are int64 minor units (USD cents) — no float ever touches a
// balance. A balance read is the settled ledger balance, clamped at zero; transient holds
// are the caller's in-pod concern, never persisted here.
package finance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/clients/treasury/ledger"
	"github.com/hanzoai/cloud/clients/treasury/ledger/sqlstore"
	"github.com/hanzoai/cloud/types"
)

// Client is the in-process inter-subsystem seam cloud's money paths call. It IS cloud's
// types.FinanceClient — one narrow interface (BalanceCents + Deposit + RecordUsage), kept
// as an alias so a value satisfies both names with no adapter (mirrors commerce.Client).
type Client = types.FinanceClient

// Entry kinds classify a wallet entry's economic meaning (also the first half of its
// idempotency key). Kept small and closed.
const (
	kindDeposit = "finance.deposit" // funding:platform → wallet (a prepaid grant/settlement)
	kindUsage   = "finance.usage"   // wallet → revenue:platform (a metered debit)
)

// Finance chart of accounts, WITHIN a single org's file. The file is the org boundary, so
// accounts carry no org prefix. One asset = USD minor units.
const (
	acctFunding = "funding:platform" // external credit source: a deposit debits it, wallet up
	acctRevenue = "revenue:platform" // platform revenue sink: a usage debit credits it, wallet down
	acctWallet  = "wallet"           // the org pool wallet; a per-user subject is "wallet:<user>"
)

// orgPattern is the safe file-path shape for an org directory — the SAME allowlist
// sqlstore's per-tenant opener guards with (a leading alphanumeric then [a-z0-9_-]; no
// path separators, no dots, no traversal). An org is used verbatim as a directory name
// only when it matches; anything else is refused, so a caller can never place a money
// file outside <dataDir>/orgs/ or reach another tenant's file.
var orgPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ledgerFinance implements types.FinanceClient over one native ledger file per org.
type ledgerFinance struct {
	dataDir string

	mu     sync.Mutex
	stores map[string]*sqlstore.Store // key: resolved db path (encodes org + test-mode)
}

// compile-time proof ledgerFinance is the money seam.
var _ types.FinanceClient = (*ledgerFinance)(nil)

// New returns a finance client rooting each org's prepaid wallet file under dataDir
// (<dataDir>/orgs/<org>/finance.db, or finance-test.db in sandbox mode). Files open
// lazily on first use.
func New(dataDir string) *ledgerFinance {
	return &ledgerFinance{dataDir: dataDir, stores: make(map[string]*sqlstore.Store)}
}

// storeFor resolves (opening + caching on first use) the org's ledger file. test picks
// the sandbox file so sandbox money never mixes with live. The org is validated against
// orgPattern FIRST, so it can never traverse the path or reach another tenant's file.
func (f *ledgerFinance) storeFor(org string, test bool) (*sqlstore.Store, error) {
	org = strings.TrimSpace(org)
	if !orgPattern.MatchString(org) {
		return nil, fmt.Errorf("finance: invalid org %q", org)
	}
	name := "finance.db"
	if test {
		name = "finance-test.db"
	}
	dir := filepath.Join(f.dataDir, "orgs", org)
	path := filepath.Join(dir, name)

	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.stores[path]; ok {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("finance: create org dir %q: %w", dir, err)
	}
	s, err := sqlstore.Open(path)
	if err != nil {
		return nil, fmt.Errorf("finance: open %q: %w", path, err)
	}
	f.stores[path] = s
	return s, nil
}

// BalanceCents returns subject's settled prepaid balance in cents within org, clamped at
// zero (a negative is never expected — the caller gates spend — but the clamp keeps the
// read honest). currency/test select the asset/ledger file; a single asset (USD cents)
// ships today.
func (f *ledgerFinance) BalanceCents(ctx context.Context, org, subject, currency string, test bool) (int64, error) {
	store, err := f.storeFor(org, test)
	if err != nil {
		return 0, err
	}
	bal, _ := store.Balance(ctx, walletAcct(subject))
	if bal < 0 {
		bal = 0
	}
	return bal, nil
}

// Deposit posts a balanced credit (funding:platform → wallet) to subject's wallet in
// org's file and returns the ledger entry id. Each deposit takes a FRESH ref, so grants
// are additive (DepositInput carries no idempotency key).
func (f *ledgerFinance) Deposit(ctx context.Context, in types.DepositInput) (string, error) {
	if in.Cents <= 0 {
		return "", fmt.Errorf("finance: deposit amount must be positive, got %d", in.Cents)
	}
	store, err := f.storeFor(in.Org, in.Test)
	if err != nil {
		return "", err
	}
	id, err := genID("dep")
	if err != nil {
		return "", err
	}
	e := ledger.Entry{
		ID:          id,
		Kind:        kindDeposit,
		Ref:         id, // fresh id → additive grant
		Memo:        in.Notes,
		AmountCents: in.Cents,
		CreatedAt:   time.Now().Unix(),
	}
	postings := []ledger.Posting{
		{Account: acctFunding, Amount: -in.Cents},
		{Account: walletAcct(in.Subject), Amount: in.Cents},
	}
	if err := store.Tx(ctx, func(tx ledger.Tx) error {
		return tx.Insert(e, postings)
	}); err != nil {
		return "", fmt.Errorf("finance: deposit: %w", err)
	}
	return id, nil
}

// RecordUsage posts a balanced usage debit (wallet → revenue:platform) from subject's
// wallet. Idempotent on in.RequestID: a replay of the same request is a no-op inside the
// same transaction as the insert, so a retry debits AT MOST ONCE. A non-positive amount is
// a no-op.
func (f *ledgerFinance) RecordUsage(ctx context.Context, in types.UsageInput) error {
	if in.Cents <= 0 {
		return nil
	}
	store, err := f.storeFor(in.Org, in.Test)
	if err != nil {
		return err
	}
	return store.Tx(ctx, func(tx ledger.Tx) error {
		if in.RequestID != "" {
			_, ok, ferr := tx.EntryByRef(kindUsage, "", in.RequestID)
			if ferr != nil {
				return ferr
			}
			if ok {
				return nil // idempotent replay — already debited once
			}
		}
		id, gerr := genID("use")
		if gerr != nil {
			return gerr
		}
		ref := in.RequestID
		if ref == "" {
			ref = id // no request id → fresh ref (non-idempotent, still a single debit)
		}
		e := ledger.Entry{
			ID:          id,
			Kind:        kindUsage,
			Ref:         ref,
			Memo:        in.Model,
			AmountCents: in.Cents,
			CreatedAt:   time.Now().Unix(),
		}
		postings := []ledger.Posting{
			{Account: walletAcct(in.Subject), Amount: -in.Cents},
			{Account: acctRevenue, Amount: in.Cents},
		}
		return tx.Insert(e, postings)
	})
}

// Close closes every cached org store (best-effort), returning the first error.
func (f *ledgerFinance) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var firstErr error
	for path, s := range f.stores {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(f.stores, path)
	}
	return firstErr
}

// walletAcct maps a billing subject to its account WITHIN the per-org file. The file IS
// the org boundary, so the account carries NO org prefix: the org pool is "wallet"; a
// per-user subject "<org>/<user>" is "wallet:<user>". Rule: split on the FIRST "/"; a
// non-empty suffix (any further "/" flattened to ":") yields "wallet:<suffix>", else
// "wallet". Lowercased + trimmed so "Acme/Bob" and "acme/bob" are the one wallet.
func walletAcct(subject string) string {
	subject = strings.ToLower(strings.TrimSpace(subject))
	if i := strings.IndexByte(subject, '/'); i >= 0 {
		if suffix := strings.ReplaceAll(subject[i+1:], "/", ":"); suffix != "" {
			return acctWallet + ":" + suffix
		}
	}
	return acctWallet
}

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits), stdlib
// only — the SAME idiom the ledger core uses, so finance carries no id dependency.
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("finance: rng: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
