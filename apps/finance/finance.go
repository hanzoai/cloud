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
// (the ledger's (kind,program,ref) idempotency): a usage debit on UsageInput.Ref and a
// deposit on DepositInput.Ref, so a retried debit or a fixed-ref backfill posts AT MOST
// ONCE; an empty Ref takes a fresh, server-minted one and stays additive (grants stack,
// metered calls each bill). A usage ref is unique WITHIN THE WALLET it debits, so one
// subject's key can never swallow another's, and a ref that already names a posting of a
// DIFFERENT amount is refused rather than answered with the first one's entry — same key
// is not same act.
// Amounts are int64 minor units (USD cents) — no float ever touches a balance. A balance
// read is the settled ledger balance, clamped at zero; transient holds are the caller's
// in-pod concern, never persisted here.
package finance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/apps/treasury/ledger/sqlstore"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
	"github.com/hanzoai/namespace"
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

// ledgerFinance implements types.FinanceClient over one native ledger file per org.
type ledgerFinance struct {
	dataDir string

	mu     sync.Mutex
	stores map[ledgerName]*sqlstore.Store
}

// ledgerName is WHICH ledger file: whose it is, and whether it is the sandbox
// one. It is the cache key and it is also exactly what the opener takes, so a
// hit and a miss can never resolve to different files.
type ledgerName struct {
	ns        namespace.Namespace
	subsystem string
}

// compile-time proof ledgerFinance is the money seam.
var _ types.FinanceClient = (*ledgerFinance)(nil)

// New returns a finance client rooting each org's prepaid wallet ledger under
// dataDir, in that org's own namespace ("finance", or "finance-test" in sandbox
// mode). Files open lazily on first use.
func New(dataDir string) *ledgerFinance {
	return &ledgerFinance{dataDir: dataDir, stores: map[ledgerName]*sqlstore.Store{}}
}

// storeFor resolves (opening + caching on first use) the org's ledger file. test picks
// the sandbox ledger so sandbox money never mixes with live.
//
// The org becomes a NAMESPACE first — the one injective slugger, which is also what
// keys the file — so it can never traverse the path, reach another tenant's ledger,
// or fold two distinct orgs onto one wallet.
func (f *ledgerFinance) storeFor(org string, test bool) (*sqlstore.Store, error) {
	ns, err := namespace.OrgProject(org, "")
	if err != nil {
		return nil, fmt.Errorf("finance: %w", err)
	}
	name := ledgerName{ns: ns, subsystem: "finance"}
	if test {
		name.subsystem = "finance-test"
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.stores[name]; ok {
		return s, nil
	}
	s, err := sqlstore.Open(name.ns, name.subsystem, f.dataDir)
	if err != nil {
		return nil, fmt.Errorf("finance: open %s ledger for %s: %w", name.subsystem, name.ns, err)
	}
	f.stores[name] = s
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
//
// A REPLAY IS THE SAME MONEY TO THE SAME WALLET, and a ref hit that is not that is
// [ErrRefTaken] rather than a credit or a borrowed entry id — the idempotency key carries
// neither subject nor amount, so being the same key is not being the same payment.
//
// AND A DEPOSIT THAT FAILS ON MONEY ALREADY IN THE BOOKS ANSWERS WITH IT. The
// in-transaction dedup covers the replay it can see; it cannot cover a transaction that
// never reached its own read — the write it lost to a concurrent poster of the same Ref,
// or the request context that died between the charge and the post. Both leave the same
// state: the ref is credited and this caller was handed an error for it, which at a
// credit door is a 500 on a card that cleared. So a failed transaction asks the only
// question it has left — is the money there under this ref? — and a yes is the same
// answer the replay branch gives ([creditedUnder]).
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
		posted, ferr := depositByRef(tx, in)
		if ferr != nil {
			return ferr
		}
		if posted != "" {
			entryID = posted
			return nil // idempotent replay — already credited once
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
		// A REF THAT IS SOMEBODY ELSE'S IS NOT A FAILURE A RE-READ CAN CLEAR.
		if errors.Is(err, ErrRefTaken) {
			return "", err
		}
		posted, taken := creditedUnder(ctx, store, in)
		if taken != nil {
			// The transaction died for its own reason AND the ref turns out to be another
			// payment's. That is the answer worth giving: retrying this deposit under this
			// ref can never succeed, and "context canceled" would send the caller round the
			// loop that cannot end.
			return "", taken
		}
		if posted != "" {
			return posted, nil
		}
		return "", fmt.Errorf("finance: deposit: %w", err)
	}
	return entryID, nil
}

// ErrRefTaken is what a deposit gets when its Ref is ALREADY posted for a different
// (subject, amount): the ref names a payment that is not this one.
//
// It is an error and not an entry, and that is the whole finding. The idempotency key is
// (kind, program, ref) — no subject, no amount — so any two deposits sharing a ref in one
// org's books collide, and the replay branch answered the SECOND of them with the FIRST
// one's entry id. The caller was told SUCCESS for money that was never credited: alice's
// $1 posted, bob's $500 returned alice's entry, bob's wallet stayed at zero and nothing
// anywhere said so. Today the settlement ref is a Square payment id and no two payments
// share one, which is the only reason this has never fired; a webhook replay, a backfill
// or the credit RPC choosing a ref of its own is all it takes.
//
// A conflict cannot be resolved here. Crediting anyway would break the exactly-once the
// ref exists to give, and answering with the other payment's entry is the swallow itself.
// So it is refused, loudly, and the caller picks a ref that is its own.
//
// EXPORTED for the reason [ErrRefReused] is, and it is the deposit twin of exactly that:
// a conflict is the ONE deposit failure no retry can clear, so a caller that renders it
// as a transient billing fault sends a customer round a loop that cannot end. The credit
// door tells the two apart to say whether its refusal is terminal (commerce settle.go),
// and the backfill reads its own fixed ref back through it (migrate.go). One error, one
// meaning, both sides of the money plane.
var ErrRefTaken = errors.New("already used for a different (subject,amount)")

// depositByRef is the ONE reading of what a deposit's Ref already holds: the original
// entry id when the SAME (subject, amount) is posted under it — a genuine replay, which
// answers with the first credit and posts nothing — the empty string when the ref is free,
// and [ErrRefTaken] when the ref is posted for a different payment.
//
// Both callers ask the same question and must not answer it two ways: the in-transaction
// branch asks it against the insert's own tx (so two concurrent replays cannot both post),
// and [creditedUnder] asks it on a detached one after a failure. Only the transaction
// differs, so only the transaction is theirs.
func depositByRef(tx ledger.Tx, in types.DepositInput) (string, error) {
	if in.Ref == "" {
		return "", nil // no key → a fresh ref; an empty-Ref grant stacks and never replays
	}
	e, ok, err := tx.EntryByRef(string(KindDeposit), "", in.Ref)
	if err != nil || !ok {
		return "", err
	}
	// THE WHOLE POSTING, not the key. A replay is the same money to the same wallet; a ref
	// hit that differs in either is another payment, however identical the key.
	if credited(e) != walletAcct(in.Subject) || e.Amount.Cmp(in.Amount) != 0 {
		return "", fmt.Errorf("finance: deposit ref %q %w", in.Ref, ErrRefTaken)
	}
	return e.ID, nil
}

// credited is the wallet a posted deposit funded: its POSITIVE leg. A deposit is exactly
// two legs (funding:platform → wallet), so the credited account is the one the money moved
// to. An entry with no positive leg is not a deposit this file wrote and matches nothing.
func credited(e ledger.JournalEntry) string {
	for _, p := range e.Postings {
		if p.Amount.Sign() > 0 {
			return p.Account
		}
	}
	return ""
}

// creditReadBudget bounds the one read a failed deposit is allowed to make. It is short
// because the caller is already past its own deadline in the case this exists for.
const creditReadBudget = 5 * time.Second

// creditedUnder is [depositByRef] asked again, on a transaction of its own, after the
// deposit's own transaction failed. It answers the entry THIS deposit is already posted
// under, [ErrRefTaken] when the ref turns out to be a different payment's, and ("", nil)
// when the ref is free or the question could not be asked at all.
//
// A read failure is simply "no" rather than an error of its own: a deposit that cannot
// prove the money landed still reports the error it already had. An empty ref is never
// idempotent — an empty-Ref deposit takes a fresh one and stacks — so it is never
// "already credited".
//
// It reads on a context DETACHED from the caller's, bounded on its own, and that is the
// whole point rather than a detail: the caller's context is exactly what may have just
// died, and a question that can only be asked while the asker is still waiting cannot
// answer the case it was written for. Nothing is written here and the read is a single
// indexed lookup.
func creditedUnder(ctx context.Context, store *sqlstore.Store, in types.DepositInput) (string, error) {
	if in.Ref == "" {
		return "", nil
	}
	ask, cancel := context.WithTimeout(context.WithoutCancel(ctx), creditReadBudget)
	defer cancel()
	var id string
	err := store.Tx(ask, func(tx ledger.Tx) error {
		posted, ferr := depositByRef(tx, in)
		id = posted
		return ferr
	})
	if errors.Is(err, ErrRefTaken) {
		return "", err
	}
	if err != nil {
		return "", nil
	}
	return id, nil
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
// wallet. Idempotent on in.Ref WITHIN THAT WALLET: a replay of the same act is a no-op
// inside the same transaction as the insert, so a retry debits AT MOST ONCE. A
// non-positive amount is a no-op.
func (f *ledgerFinance) RecordUsage(ctx context.Context, in types.UsageInput) error {
	_, _, err := f.RecordUsageOnce(ctx, in)
	return err
}

// usageProgram is the SCOPE a usage ref is unique within: the wallet the money leaves.
//
// The ledger keys an entry by (kind, program, ref), and usage used to leave program
// empty — so one org-wide namespace held every subject's refs and the FIRST subject to
// take a ref owned it for everyone. Two people in one org sharing a ref meant the second
// one's call debited nobody.
//
// It is [walletAcct], not a second rule about who pays: the scope a ref is unique within
// is exactly the account the posting debits, so the key and the money can never name two
// different wallets.
func usageProgram(subject string) string { return walletAcct(subject) }

// ErrRefReused is a usage ref that already names a DIFFERENT charge in this wallet.
// Answered rather than silently deduped: a key that means "this is the act I already
// paid for" is a lie when the amount differs, and answering the first entry would hand
// back free work for the price of a repeated key.
//
// EXPORTED because a surface that lets a caller NAME the act — the GPU charge — has to
// tell that caller its key is taken, and a conflict rendered as a billing outage sends
// it into a retry that will never clear. It is the usage twin of the deposit's own
// refusal, so the money plane answers a reused key one way.
var ErrRefReused = errors.New("names a different charge")

// usageByRef answers the entry THIS usage is already posted under, inside the caller's
// own transaction, or ("", nil) when the ref is free.
//
// THE WHOLE POSTING, not the key — the same rule [depositByRef] states for a payment: a
// replay is the same money out of the same wallet, and a ref hit that differs in the
// amount is another charge, however identical the key. The wallet cannot differ here
// (it IS the program the ref was looked up in), so the amount is what is left to check.
//
// An empty ref matches nothing: the debit takes a fresh, server-minted ref and stands
// alone.
func usageByRef(tx ledger.Tx, in types.UsageInput) (string, error) {
	if in.Ref == "" {
		return "", nil
	}
	e, ok, err := tx.EntryByRef(string(KindUsage), usageProgram(in.Subject), in.Ref)
	if err != nil || !ok {
		return "", err
	}
	if e.Amount.Cmp(in.Amount) != 0 {
		return "", fmt.Errorf("finance: usage ref %q %w", in.Ref, ErrRefReused)
	}
	return e.ID, nil
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
//
// The key is (usage, THIS WALLET, in.Ref) — see [usageProgram]. in.Ref names ONE act and
// is the SERVER's word for it, never a header the caller chose: an idempotency key a
// payer can pick is a payer who can pick to be billed once for a thousand calls.
func (f *ledgerFinance) RecordUsageOnce(ctx context.Context, in types.UsageInput) (entryID string, posted bool, err error) {
	if in.Amount.Sign() <= 0 {
		return "", false, nil
	}
	store, serr := f.storeFor(in.Org, in.Test)
	if serr != nil {
		return "", false, serr
	}
	if terr := store.Tx(ctx, func(tx ledger.Tx) error {
		replay, ferr := usageByRef(tx, in)
		if ferr != nil {
			return ferr
		}
		if replay != "" {
			entryID, posted = replay, false
			return nil // idempotent replay — already debited once
		}
		id, gerr := genID("use")
		if gerr != nil {
			return gerr
		}
		ref := in.Ref
		if ref == "" {
			ref = id // no act id → the entry's own, server-minted (a single debit that stands alone)
		}
		e := ledger.JournalEntry{
			ID:        id,
			Kind:      string(KindUsage),
			Program:   usageProgram(in.Subject),
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
