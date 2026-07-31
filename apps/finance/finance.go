// Package finance is the prepaid wallet your org pays from: deposits in, usage
// debits out, always balanced.
//
// It is a per-CUSTOMER, double-entry balance on the native ledger core
// (apps/treasury/ledger, the same engine the platform reserve posts to). It
// registers NO routes and NO ops — it is the in-process implementation of
// cloud's types.FinanceClient (package alias finance.Client, mirroring commerce.Client),
// the ONE money seam the ai prepaid gate, the admin grant, commerce's credit mint and
// the edge meter all bill through; billing is the customer-facing door onto it.
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
// entry, and a file's postings always sum to zero. Both writes are idempotent on their ref
// (the ledger's (kind,program,ref) idempotency): a usage debit on RequestID and a deposit
// on DepositInput.Ref, so a retried debit or a fixed-ref backfill posts AT MOST ONCE; a
// deposit with an empty Ref takes a fresh ref and stays additive (grants stack). Amounts
// are int64 minor units (USD cents) — no float ever touches a balance. A balance read is
// the settled ledger balance, clamped at zero; transient holds are the caller's in-pod
// concern, never persisted here.
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
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/apps/money"
	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/apps/treasury/ledger/sqlstore"
	"github.com/hanzoai/cloud/types"
)

// Client is the in-process inter-subsystem seam cloud's money paths call. It IS cloud's
// types.FinanceClient — one narrow interface (BalanceCents + Deposit + RecordUsage), kept
// as an alias so a value satisfies both names with no adapter (mirrors commerce.Client).
type Client = types.FinanceClient

// Kind classifies a wallet entry's economic meaning — money IN or money OUT — and is
// also the first half of the entry's idempotency key. Kept small and closed: two
// directions, two names.
//
// IT IS THE ONE VOCABULARY, and it is a TYPE rather than a pair of strings because
// both halves of the money surface read it. The ledger WRITES these kinds and the
// customer's finance pages CLASSIFY on them, and while the reader held its own string
// literals the two silently disagreed: the reader matched commerce's `deposit` /
// `withdraw` against entries this file has always written as `finance.deposit` /
// `finance.usage`, so over the peer path a customer's credits rendered empty, their
// usage totalled zero, and their own top-up signed NEGATIVE. Nothing failed — the
// strings just never met. Named constants of a named type make that disagreement a
// compile error instead of a wrong number on a customer's balance page.
type Kind string

const (
	KindDeposit Kind = "finance.deposit" // funding:platform → wallet (a prepaid grant/settlement)
	KindUsage   Kind = "finance.usage"   // wallet → revenue:platform (a metered debit)
	// KindUnknown is what a kind the ledger never wrote parses to. A reader treats it
	// as neither direction rather than guessing one — an entry nobody can classify
	// must not be silently counted as spend.
	KindUnknown Kind = ""
)

// ParseKind reads a kind back off a wire — the internal plane's Txn.Kind, or a row
// re-read from an older store. It is the ONE place the ledger's spellings are
// recognized, so a reader never compares an entry to a string literal.
func ParseKind(s string) Kind {
	switch Kind(strings.TrimSpace(s)) {
	case KindDeposit:
		return KindDeposit
	case KindUsage:
		return KindUsage
	default:
		return KindUnknown
	}
}

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

// Balance returns subject's settled prepaid balance as an exact 18-decimal USD money value
// within org, clamped at zero (a negative is never expected — the caller gates spend —
// but the clamp keeps the read honest). currency/test select the asset/ledger file; a
// single asset (USD, 18-decimal-precise) ships today.
func (f *ledgerFinance) Balance(ctx context.Context, org, subject, currency string, test bool) (money.Amount, error) {
	store, err := f.storeFor(org, test)
	if err != nil {
		return money.Zero(), err
	}
	bal, err := store.Balance(ctx, walletAcct(subject))
	if err != nil {
		// A REAL read failure (DB error / corrupt stored balance) is UNKNOWN, and
		// unknown is NOT "broke": surfacing it lets the caller's guard render a 503
		// instead of a genuine $0. Swallowing it here showed funded customers $0 and
		// made the prepaid gate refuse a funded org — the exact invariant the balance
		// callers document ("a balance that cannot be read is unknown, never zero").
		return money.Zero(), err
	}
	if bal.IsNeg() {
		return money.Zero(), nil
	}
	return bal, nil
}

// Deposit posts a balanced credit (funding:platform → wallet) to subject's wallet in
// org's file and returns the ledger entry id. Idempotent on in.Ref when set: a replay of
// the same non-empty Ref is a no-op returning the ORIGINAL entry id (checked inside the
// same transaction as the insert), so a fixed-ref backfill/settlement credits AT MOST
// ONCE. An empty Ref takes a fresh id, so grants stay additive (they stack).
func (f *ledgerFinance) Deposit(ctx context.Context, in types.DepositInput) (string, error) {
	if in.Amount.Sign() <= 0 {
		return "", fmt.Errorf("finance: deposit amount must be positive, got %s", in.Amount)
	}
	store, err := f.storeFor(in.Org, in.Test)
	if err != nil {
		return "", err
	}
	id, err := genID("dep")
	if err != nil {
		return "", err
	}
	ref := in.Ref
	if ref == "" {
		ref = id // no idempotency key → fresh ref, additive grant
	}
	entryID := id
	if err := store.Tx(ctx, func(tx ledger.Tx) error {
		if in.Ref != "" {
			existing, ok, ferr := tx.EntryByRef(string(KindDeposit), "", in.Ref)
			if ferr != nil {
				return ferr
			}
			if ok {
				entryID = existing.ID
				return nil // idempotent replay — already credited once
			}
		}
		e := ledger.JournalEntry{
			ID:        id,
			Kind:      string(KindDeposit),
			Ref:       ref,
			Memo:      in.Notes,
			Amount:    in.Amount,
			CreatedAt: time.Now().Unix(),
		}
		postings := []ledger.Posting{
			{Account: acctFunding, Amount: in.Amount.Neg()},
			{Account: walletAcct(in.Subject), Amount: in.Amount},
		}
		return tx.Insert(e, postings)
	}); err != nil {
		return "", fmt.Errorf("finance: deposit: %w", err)
	}
	return entryID, nil
}

// usageHook, when set, is called (async, best-effort) after a successful usage debit.
// It is the dependency-inverted seam the usage-cap ALERT fires through WITHOUT finance
// importing commerce: the host (apps/commerce.go) registers a hook that reads the org's
// finance period spend and fires/debounces the alerts. Set once at boot.
var usageHook atomic.Pointer[func(org string, test bool, project, service string)]

// SetUsageHook installs the post-debit hook (the cap alert-fire). Pass nil to clear.
func SetUsageHook(h func(org string, test bool, project, service string)) {
	if h == nil {
		usageHook.Store(nil)
		return
	}
	usageHook.Store(&h)
}

// SumUsageSince returns the org's total metered usage (cents) recorded at/after
// `since` (unix seconds) — the finance-ledger source the ORG-WIDE usage cap enforces
// on. The finance Entry carries no project/service, so this is the org total (which
// the covering org-wide spend-alert row binds on); per-scope caps are a follow-up.
// Zero when the org has no store yet (never spent). Reads the sandbox books for a
// test org (test=true), so a test-mode cap counts only test spend.
func (f *ledgerFinance) SumUsageSince(ctx context.Context, org string, test bool, since int64) (int64, error) {
	store, err := f.storeFor(org, test)
	if err != nil {
		return 0, err
	}
	sum, err := store.SumByKindSince(ctx, string(KindUsage), since)
	if err != nil {
		return 0, err
	}
	return sum.Cents(), nil
}

// RecordUsage posts a balanced usage debit (wallet → revenue:platform) from subject's
// wallet. Idempotent on in.RequestID: a replay of the same request is a no-op inside the
// same transaction as the insert, so a retry debits AT MOST ONCE. A non-positive amount is
// a no-op.
func (f *ledgerFinance) RecordUsage(ctx context.Context, in types.UsageInput) error {
	_, _, err := f.RecordUsageOnce(ctx, in)
	return err
}

// RecordUsageOnce is RecordUsage with the ledger's idempotency ANSWERED rather than
// merely applied: entryID is the entry the debit is recorded under — the original one
// on a replay — and posted reports whether THIS call is the one that moved the money.
//
// It exists because a caller doing a second thing beside the debit has to know which
// call it is on. The GPU charge is that caller: it answers the customer a transaction
// id, and a replay that minted a fresh id told a retrying client it had bought a second
// GPU. The answer comes from INSIDE the same transaction as the insert, so two
// concurrent replays cannot both read posted=true and both act.
//
// RecordUsage is the whole types.FinanceClient contract and delegates here, so there is
// ONE body and one place the idempotency lives. Callers that need the answer resolve
// this method by interface assertion, the same widening ListUsage and SumUsageSince use.
// A non-positive amount is a no-op ("", false, nil): nothing was posted.
func (f *ledgerFinance) RecordUsageOnce(ctx context.Context, in types.UsageInput) (entryID string, posted bool, err error) {
	if in.Amount.Sign() <= 0 {
		return "", false, nil
	}
	store, serr := f.storeFor(in.Org, in.Test)
	if serr != nil {
		return "", false, serr
	}
	if terr := store.Tx(ctx, func(tx ledger.Tx) error {
		if in.RequestID != "" {
			existing, ok, ferr := tx.EntryByRef(string(KindUsage), "", in.RequestID)
			if ferr != nil {
				return ferr
			}
			if ok {
				entryID, posted = existing.ID, false
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
		e := ledger.JournalEntry{
			ID:        id,
			Kind:      string(KindUsage),
			Ref:       ref,
			Memo:      in.Model,
			Amount:    in.Amount,
			CreatedAt: time.Now().Unix(),
		}
		postings := []ledger.Posting{
			{Account: walletAcct(in.Subject), Amount: in.Amount.Neg()},
			{Account: acctRevenue, Amount: in.Amount},
		}
		if ierr := tx.Insert(e, postings); ierr != nil {
			return ierr
		}
		entryID, posted = id, true
		return nil
	}); terr != nil {
		return "", false, terr
	}

	// The debit is committed — fire the usage-cap alert on this crossing (async,
	// off the money path). Debounced inside FireSpendAlerts, so firing on an
	// idempotent replay too is harmless. finance never blocks or imports commerce.
	if p := usageHook.Load(); p != nil {
		h := *p
		go h(in.Org, in.Test, in.Project, in.Service)
	}
	return entryID, posted, nil
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
