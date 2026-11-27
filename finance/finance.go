package finance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/apps/treasury/ledger/sqlstore"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/namespace"
)

// Client is the in-process inter-subsystem client cloud's money paths call. It IS cloud's
// money.FinanceClient — one narrow interface (BalanceCents + Deposit + RecordUsage), kept
// as an alias so a value satisfies both names with no adapter (mirrors commerce.Client).
type Client = money.FinanceClient

// Kind classifies a wallet entry's economic meaning — money IN or money OUT — and is
// also the first half of the entry's idempotency key. Kept small and closed: money in
// is a deposit; money out is metered usage or a plan's payment, two names because the
// spend cap and the usage page count one and not the other.
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
	KindPayment Kind = "finance.payment" // wallet → revenue:platform (a plan period paid from the wallet)
	KindHold    Kind = "finance.hold"    // wallet → held:<wallet>:<ref> (a payment agreed, not yet made), and back
	KindSource  Kind = "finance.source"  // wallet → cash (earlier money shown to be a payment); the balance does not move
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
	case KindPayment:
		return KindPayment
	case KindHold:
		return KindHold
	case KindSource:
		return KindSource
	default:
		return KindUnknown
	}
}

// Finance chart of accounts, WITHIN a single org's file. The file is the org boundary, so
// accounts carry no org prefix. One asset = USD minor units.
const (
	acctFunding     = "funding:platform" // what Hanzo mints: a credit deposit debits it, wallet up
	acctFundingCash = "funding:cash"     // what payments brought in: a cash deposit debits it, cash up
	acctRevenue     = "revenue:platform" // platform revenue sink: a usage debit credits it, wallet down
	acctWallet      = "wallet"           // the org pool's credit; a per-user subject is "wallet:<user>"
	acctCash        = "cash"             // the org pool's cash; a per-user subject is "cash:<user>"
)

// ledgerFinance implements money.FinanceClient over one native ledger per org.
//
// WHERE those ledgers live is not this type's business — see ledgers.go. It holds
// the port and nothing else, so the same money rules run over a lone process's
// files and over the fenced per-org plane without one line of this file knowing
// which it got.
type ledgerFinance struct{ books Ledgers }

// compile-time proof ledgerFinance is the money client.
var _ money.FinanceClient = (*ledgerFinance)(nil)

// New returns a finance client over the given books.
//
// A lone process passes [Local]; a deployment with a fenced per-org plane passes
// that. The client is the same either way, which is the point of the port.
func New(books Ledgers) *ledgerFinance { return &ledgerFinance{books: books} }

// storeFor resolves the org's ledger. test picks the sandbox book, so sandbox
// money never mixes with live.
func (f *ledgerFinance) storeFor(org string, test bool) (*sqlstore.Store, error) {
	return f.books.For(org, test)
}

// nameOf folds an org into the name its ledger file is keyed by.
//
// IT LIVES HERE BECAUSE THIS FILE IS THE ONE WAY IN. Every namespace in the tree comes
// from one of two places — cloud's OrgNamespace, and this file, which is the
// second only because cloud imports this package and so the arrow cannot point
// back (TestOnlyOrgnsBuildsANamespace holds both). A third place is not a thing to
// add: the fold is injective, so a second implementation of it does not fail —
// it opens an empty ledger beside a real one and reports the customer's balance as
// zero. [Local] reaches through here for that reason rather than folding its own.
func nameOf(org string) (namespace.Namespace, error) {
	ns, err := namespace.OrgProject(org, "")
	if err != nil {
		return namespace.Namespace{}, fmt.Errorf("finance: %w", err)
	}
	return ns, nil
}

// settle is what makes a write REAL, and every write in this file ends in it.
//
// A committed transaction is a fact about this process's copy of the ledger. On a
// deployment with one writer that is the whole story and Ship says so. On a
// deployment with several, the copy has to reach the object store under the lease
// round that elected this replica before anybody may be told the money moved —
// because a successor opens the last ACKED state, and an entry that never shipped
// is an entry the successor has never heard of.
//
// So a ship that does not ack FAILS THE WRITE. The transaction stays committed
// locally, which sounds wrong and is the only safe answer available: an unacked
// ship is not a failed ship — the object may have landed and only the reply been
// lost — so a caller that deleted the entry on a timeout would drop a charge the
// durable copy holds. Left standing, the entry is either already durable or is
// discarded whole when this store next hydrates, and in both cases the CALLER was
// told no. A charge reported as failed and silently applied is the one outcome a
// customer can neither see nor dispute; a charge reported as failed and not
// applied is a retry.
func (f *ledgerFinance) settle(org string, test bool, what string) error {
	acked, err := f.books.Ship(org, test)
	if err != nil {
		return fmt.Errorf("finance: %s for %s was written and could not be made durable: %w", what, org, err)
	}
	if !acked {
		return fmt.Errorf("finance: %s for %s was written and not acknowledged — this replica no longer holds the org's books; retry on its writer", what, org)
	}
	return nil
}

// Balance returns subject's settled prepaid balance as an exact 18-decimal USD money value
// within org, clamped at zero (a negative is never expected — the caller gates spend —
// but the clamp keeps the read honest). currency/test select the asset/ledger file; a
// single asset (USD, 18-decimal-precise) ships today.
func (f *ledgerFinance) Balance(ctx context.Context, org, subject, currency string, test bool) (money.Amount, error) {
	// A REAL read failure (DB error / corrupt stored balance) is UNKNOWN, and unknown
	// is NOT "broke": surfacing it lets the caller's guard render a 503 instead of a
	// genuine $0 ("a balance that cannot be read is unknown, never zero").
	funds, err := f.Funds(ctx, org, subject, test)
	if err != nil {
		return money.Zero(), err
	}
	return funds.Total(), nil
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
// credit endpoint is a 500 on a card that cleared. So a failed transaction asks the only
// question it has left — is the money there under this ref? — and a yes is the same
// answer the replay branch gives ([creditedUnder]).
func (f *ledgerFinance) Deposit(ctx context.Context, in money.DepositInput) (string, error) {
	if in.Amount.Sign() <= 0 {
		return "", fmt.Errorf("finance: deposit amount must be positive, got %s", in.Amount)
	}
	if in.Cash && strings.TrimSpace(in.Ref) == "" {
		return "", errors.New("finance: a cash deposit names the payment that funded it: ref is required")
	}
	store, err := f.storeFor(in.Org, in.Test)
	if err != nil {
		return "", err
	}
	id := mint.ID("dep")
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
		funding, bucket := sourceOf(in)
		postings := []ledger.Posting{
			{Account: funding, Amount: in.Amount.Neg()},
			{Account: bucket, Amount: in.Amount},
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
	if err := f.settle(in.Org, in.Test, "the deposit"); err != nil {
		return "", err
	}
	// The money is in the wallet and acknowledged: tell whoever was refused for want of
	// it. Told by whichever call settles it, a replay included: the call that posted the
	// money may have failed to settle and been retried by the payment processor, and then
	// the retry is the only one that gets this far. A listener that hears it twice asks
	// the stores of record and finds nothing new.
	told(in.Org)
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
// endpoint tells the two apart to say whether its refusal is terminal (commerce settle.go),
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
func depositByRef(tx ledger.Tx, in money.DepositInput) (string, error) {
	if in.Ref == "" {
		return "", nil // no key → a fresh ref; an empty-Ref grant stacks and never replays
	}
	e, ok, err := tx.EntryByRef(string(KindDeposit), "", in.Ref)
	if err != nil || !ok {
		return "", err
	}
	// THE WHOLE POSTING, not the key. A replay is the same money to the same wallet from
	// the same source; a ref hit that differs in any of them is another payment, however
	// identical the key.
	if e.Amount.Cmp(in.Amount) != 0 {
		return "", fmt.Errorf("finance: deposit ref %q %w", in.Ref, ErrRefTaken)
	}
	if _, bucket := sourceOf(in); credited(e) == bucket {
		return e.ID, nil
	}
	// A payment credited before the ledger kept cash apart is this same payment
	// arriving again as cash: it was credited once, and moves nothing now. It reads as
	// cash once it is sourced ([ledgerFinance.Source]).
	if in.Cash && credited(e) == walletAcct(in.Subject) {
		return e.ID, nil
	}
	return "", fmt.Errorf("finance: deposit ref %q %w", in.Ref, ErrRefTaken)
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
func creditedUnder(ctx context.Context, store *sqlstore.Store, in money.DepositInput) (string, error) {
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
// It is the dependency-inverted client the usage-cap ALERT fires through WITHOUT finance
// importing commerce: the host (apps/commerce/mount.go) registers a hook that reads the org's
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

// depositHook, when set, is called (async, best-effort) once money that was not in an org's
// wallet is in it and acknowledged: a deposit, a refund, a clawback restored, a hold returned,
// a credit line raised. It is how the rest of the deployment learns that a refusal for want of
// funds may have cleared (internal/funds) without finance importing whoever waits on it. It
// only announces: it moves, prices and limits nothing, and it can neither fail nor delay the
// movement of money (told). A replay of a deposit already posted calls it too, when it
// settles: see Deposit.
var depositHook atomic.Pointer[func(org string)]

// told announces that money has reached org's wallet, on a goroutine of its own, to the hook
// if there is one. A hook that panics is logged and forgotten: whoever listens is not a reason
// for a payment to fail, or to wait.
func told(org string) {
	p := depositHook.Load()
	if p == nil {
		return
	}
	h := *p
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("finance: the hook told of money reaching a wallet failed", "org", org, "err", r)
			}
		}()
		h(org)
	}()
}

// SetDepositHook installs the hook told when money reaches a wallet. Pass nil to clear.
func SetDepositHook(h func(org string)) {
	if h == nil {
		depositHook.Store(nil)
		return
	}
	depositHook.Store(&h)
}

// SumUsageSince returns the org's metered usage (cents) recorded at/after `since`
// (unix seconds) in one (project, service) scope — the finance-ledger source a usage
// cap enforces on. An empty project or service does not narrow that axis, so the
// org-wide cap passes ("", "") and sums every debit, while a cap scoped to one
// service sums only the debits that service recorded. Zero when the org has no store
// yet (never spent). Reads the sandbox books for a test org (test=true), so a
// test-mode cap counts only test spend.
func (f *ledgerFinance) SumUsageSince(ctx context.Context, org string, test bool, project, service string, since int64) (int64, error) {
	if !f.books.Has(org, test) {
		return 0, nil
	}
	store, err := f.storeFor(org, test)
	if err != nil {
		return 0, err
	}
	sum, err := store.SumByKindSince(ctx, string(KindUsage), project, service, since)
	if err != nil {
		return 0, err
	}
	return sum.Cents(), nil
}

// RecordUsage counts one served call and posts what it moved: a balanced usage debit
// (wallet → revenue:platform) when a wallet pays, nothing beyond the count when a plan
// covered it or the platform absorbed it. Idempotent on in.Ref WITHIN THAT WALLET: a
// replay of the same act is a no-op inside the same transaction as the insert, so a
// retry counts and debits AT MOST ONCE. A negative amount is refused.
func (f *ledgerFinance) RecordUsage(ctx context.Context, in money.UsageInput) error {
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
func usageByRef(tx ledger.Tx, kind Kind, in money.UsageInput) (string, error) {
	if in.Ref == "" {
		return "", nil
	}
	e, ok, err := tx.EntryByRef(string(kind), usageProgram(in.Subject), in.Ref)
	if err != nil || !ok {
		return "", err
	}
	if e.Amount.Cmp(in.Amount) != 0 {
		return "", fmt.Errorf("finance: usage ref %q %w", in.Ref, ErrRefReused)
	}
	return e.ID, nil
}

// paidAsUsage answers the metered debit a payment's ref already names in this
// wallet for the same amount — the payment, posted as usage when it was one.
//
// The payer's half of an x402 settlement was a metered debit before the rail drew
// payments, and a claim that binary left in doubt is finished by this one under
// the same settlement ref. Read under the payment's own kind alone, the ref is
// free and the payer is charged a second time for one settlement. The same money
// out of the same wallet under the same name is the same act, whichever kind
// wrote it. A metered debit never reads a payment this way: a call is admitted
// before it runs and is charged for, whatever its name.
func paidAsUsage(tx ledger.Tx, in money.UsageInput) (string, error) {
	if in.Ref == "" {
		return "", nil
	}
	e, ok, err := tx.EntryByRef(string(KindUsage), usageProgram(in.Subject), in.Ref)
	if err != nil || !ok || e.Amount.Cmp(in.Amount) != 0 {
		return "", err
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
// RecordUsage is the whole money.FinanceClient contract and delegates here, so there is
// ONE body and one place the idempotency lives. Callers that need the answer resolve
// this method by interface assertion, the same widening ListUsage and SumUsageSince use.
// A call that moved no money — zero, a plan's, the platform's — is counted and answers
// ("", false, nil): no entry was posted.
//
// The key is (usage, THIS WALLET, in.Ref) — see [usageProgram]. in.Ref names ONE act and
// is the SERVER's word for it, never a header the caller chose: an idempotency key a
// payer can pick is a payer who can pick to be billed once for a thousand calls.
func (f *ledgerFinance) RecordUsageOnce(ctx context.Context, in money.UsageInput) (entryID string, posted bool, err error) {
	return f.debit(ctx, in, KindUsage)
}

// debit is the ONE wallet-debit body, for both kinds of money out. A payment
// ([Draw]) is refused when the wallet cannot cover it in full; a metered debit
// (RecordUsageOnce) lands whatever the wallet holds, because its call was admitted
// before it ran. Only metered usage is spend, so only it fires the usage hook.
//
// A payment agreed before it was made is paid from its HOLD first — the money set
// aside under the same ref ([Holder]) — and the wallet covers only what the hold
// does not, so a held payment lands however the wallet stands by now. What the
// hold kept beyond the payment returns to the wallet in the same entry.
func (f *ledgerFinance) debit(ctx context.Context, in money.UsageInput, kind Kind) (entryID string, posted bool, err error) {
	if in.Amount.IsNeg() {
		return "", false, fmt.Errorf("finance: a %s of %s is below zero", kind, in.Amount)
	}
	if kind == KindPayment && in.Amount.Sign() == 0 {
		return "", false, nil // a payment of nothing pays nothing; it is not a served call
	}
	store, serr := f.storeFor(in.Org, in.Test)
	if serr != nil {
		return "", false, serr
	}
	if terr := store.Tx(ctx, func(tx ledger.Tx) error {
		var perr error
		entryID, posted, perr = record(ctx, tx, in, kind)
		return perr
	}); terr != nil {
		return "", false, terr
	}
	// SHIPPED BEFORE THE ALERT, not after. The alert reads the org's spend and may
	// suspend the account on it, so firing on a debit that never became durable
	// would cap a customer on money the books do not hold.
	if serr := f.settle(in.Org, in.Test, "the "+string(kind)+" debit"); serr != nil {
		return "", false, serr
	}
	if kind != KindUsage || entryID == "" {
		return entryID, posted, nil // nothing a wallet paid: no spend for the alert to read
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

// RecordUsages posts metered debits of ONE org's books, each exactly as RecordUsage
// posts it and idempotent on its Ref the same way, in one transaction and one ship:
// the drain of the usage stream hands it a batch, so the ship each debit used to wait
// on is paid once per batch. All or none of the batch lands.
func (f *ledgerFinance) RecordUsages(ctx context.Context, ins []money.UsageInput) error {
	if len(ins) == 0 {
		return nil
	}
	org, test := ins[0].Org, ins[0].Test
	for _, in := range ins {
		if in.Org != org || in.Test != test {
			return fmt.Errorf("finance: a usage batch names one org's books, not %s and %s", org, in.Org)
		}
	}
	store, err := f.storeFor(org, test)
	if err != nil {
		return err
	}
	for _, in := range ins {
		if in.Amount.IsNeg() {
			return fmt.Errorf("finance: a usage of %s is below zero", in.Amount)
		}
	}
	moved := map[[2]string]bool{}
	if err := store.Tx(ctx, func(tx ledger.Tx) error {
		for _, in := range ins {
			entry, _, perr := record(ctx, tx, in, KindUsage)
			if perr != nil {
				return perr
			}
			if entry != "" {
				moved[[2]string{in.Project, in.Service}] = true
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := f.settle(org, test, "a usage batch"); err != nil {
		return err
	}
	if p := usageHook.Load(); p != nil {
		h := *p
		seen := map[[2]string]bool{}
		for _, in := range ins {
			k := [2]string{in.Project, in.Service}
			if moved[k] && !seen[k] {
				seen[k] = true
				go h(org, test, in.Project, in.Service)
			}
		}
	}
	return nil
}

// user is the usage log the ledger keeps beside the journal (sqlstore.Use).
type user interface {
	UseByRef(program, ref string) (sqlstore.Use, bool, error)
	InsertUse(sqlstore.Use) error
}

// record writes one served call inside tx. A payment is a journal entry alone
// ([post]). A metered call is counted in the usage log, and its journal debit is
// posted only when a wallet pays for it: a plan's call and the platform's are
// counted and move nothing. posted reports that THIS call moved money. A ref already
// counted is a replay and writes nothing; one counted at a different amount is
// [ErrRefReused].
func record(ctx context.Context, tx ledger.Tx, in money.UsageInput, kind Kind) (entryID string, posted bool, err error) {
	if kind != KindUsage {
		return post(tx, in, kind)
	}
	log, ok := tx.(user)
	if !ok {
		return "", false, errors.New("finance: this ledger keeps no usage log")
	}
	program := usageProgram(in.Subject)
	if in.Ref != "" {
		prior, seen, uerr := log.UseByRef(program, in.Ref)
		if uerr != nil {
			return "", false, uerr
		}
		if seen {
			if prior.Amount.Cmp(in.Amount) != 0 {
				return "", false, fmt.Errorf("finance: usage ref %q %w", in.Ref, ErrRefReused)
			}
			return prior.Entry, false, nil
		}
	}
	use := sqlstore.Use{
		Program: program, Ref: in.Ref,
		Model: in.Model, Provider: in.Provider, Class: strings.TrimSpace(in.Class), Units: in.Units,
		Amount: in.Amount, Cost: in.Cost, PaidBy: strings.TrimSpace(in.PaidBy),
		Project: strings.TrimSpace(in.Project), Service: strings.TrimSpace(in.Service),
		Actor: strings.TrimSpace(in.Actor), Key: strings.TrimSpace(in.Key),
		CreatedAt: time.Now().Unix(),
	}
	switch {
	case use.PaidBy == money.PaidByPlan || use.PaidBy == money.PaidByHanzo:
		// Counted, and nobody's wallet moves: the plan includes it or the platform
		// absorbs it.
	case in.Amount.Sign() == 0:
		use.PaidBy = money.PaidByHanzo // a priced-at-nothing call is the platform's
	default:
		var by string
		entryID, posted, by, err = postUsage(ctx, tx, in)
		if err != nil {
			return "", false, err
		}
		if !posted {
			// The journal already holds this act from before the usage log existed;
			// it is listed from there, and counting it again would show it twice.
			return entryID, false, nil
		}
		use.Entry, use.PaidBy = entryID, by
	}
	use.ID = use.Entry
	if use.ID == "" {
		use.ID = mint.ID("use")
	}
	if use.Ref == "" {
		use.Ref = use.ID
	}
	if err := log.InsertUse(use); err != nil {
		return "", false, err
	}
	return entryID, posted, nil
}

// postUsage writes a metered call's wallet debit inside tx, drawn in the owner's
// order ([drawUsage]): cash, the org's credit line (or open line when on account),
// then granted credit. Anything beyond what the wallet and its explicit line cover
// is never posted as negative cash (no customer incurs debt without a credit line).
// by names who paid it. A ref the journal already holds for the same amount is a
// replay and posts nothing.
func postUsage(ctx context.Context, tx ledger.Tx, in money.UsageInput) (entryID string, posted bool, by string, err error) {
	replay, ferr := usageByRef(tx, KindUsage, in)
	if ferr != nil {
		return "", false, "", ferr
	}
	if replay != "" {
		return replay, false, "", nil
	}
	rawCash, rawCredit, berr := bucketsIn(tx, in.Subject)
	if berr != nil {
		return "", false, "", berr
	}
	limit, lerr := limitIn(tx, in.Subject)
	if lerr != nil {
		return "", false, "", lerr
	}
	funds := opened(fundsWith(rawCash, rawCredit, limit), lined(in.Subject) && onAccount(ctx, in.Org))
	s := drawUsage(in.Amount, funds, in.Cash)
	collected := s.cash.Add(s.line).Add(s.credit)
	if collected.IsZero() {
		// Wallet and explicit line hold nothing for this call: record in the usage
		// log as platform-absorbed without driving cash below zero.
		return "", true, money.PaidByHanzo, nil
	}
	now := time.Now().Unix()
	if lined(in.Subject) && funds.Owed.IsZero() && s.line.Sign() > 0 {
		// The org's own account owed nothing and draws on its credit line from this
		// call on: the moment the debt's age is read from (Line.Since).
		o, ok := tx.(interface{ Owe(at int64) error })
		if !ok {
			return "", false, "", errors.New("finance: this ledger keeps no record of when a debt began")
		}
		if oerr := o.Owe(now); oerr != nil {
			return "", false, "", oerr
		}
	}
	var legs []ledger.Posting
	if a := s.cash.Add(s.line); a.Sign() != 0 {
		legs = append(legs, ledger.Posting{Account: cashAcct(in.Subject), Amount: a.Neg()})
	}
	if s.credit.Sign() != 0 {
		legs = append(legs, ledger.Posting{Account: walletAcct(in.Subject), Amount: s.credit.Neg()})
	}
	legs = append(legs, ledger.Posting{Account: acctRevenue, Amount: collected})
	id := mint.ID("use")
	ref := in.Ref
	if ref == "" {
		ref = id
	}
	if ierr := tx.Insert(ledger.JournalEntry{
		ID:        id,
		Kind:      string(KindUsage),
		Program:   usageProgram(in.Subject),
		Ref:       ref,
		Memo:      in.Model,
		Project:   strings.TrimSpace(in.Project),
		Service:   strings.TrimSpace(in.Service),
		Amount:    collected,
		CreatedAt: now,
	}, legs); ierr != nil {
		return "", false, "", ierr
	}
	return id, true, s.paidBy(), nil
}

// post writes one payment inside tx: nothing when its Ref already names one (an
// idempotent replay), else the balanced entry. A payment draws its holds first and is
// refused when the wallet cannot cover the rest, which it draws cash first ([draw]).
// It never draws the credit line: the line pays for served calls, in arrears.
func post(tx ledger.Tx, in money.UsageInput, kind Kind) (entryID string, posted bool, err error) {
	// A payment's hold is named by the payment's own ref: a retry after a refund
	// posts under the ref's next attempt and still pays from what was set aside.
	holdRef := in.Ref
	if kind == KindPayment && in.Ref != "" {
		open, oerr := openRef(tx, usageProgram(in.Subject), in.Ref)
		if oerr != nil {
			return "", false, oerr
		}
		in.Ref = open
	}
	replay, ferr := usageByRef(tx, kind, in)
	if ferr == nil && replay == "" && kind == KindPayment {
		replay, ferr = paidAsUsage(tx, in)
	}
	if ferr != nil {
		return "", false, ferr
	}
	if replay != "" {
		return replay, false, nil // idempotent replay — already debited once
	}
	cash, wallet := cashAcct(in.Subject), walletAcct(in.Subject)
	rest := in.Amount
	// What each bucket gets back: a hold's surplus returns to the bucket it was taken
	// from, in the same entry that pays from it.
	back := map[string]money.Amount{cash: money.Zero(), wallet: money.Zero()}
	var legs []ledger.Posting
	if kind == KindPayment && in.Ref != "" {
		for _, bucket := range []string{cash, wallet} { // cash first
			held := heldAcct(bucket, holdRef)
			h, herr := tx.Balance(held)
			if herr != nil {
				return "", false, herr
			}
			if h.Sign() <= 0 {
				continue
			}
			use := minAmount(rest, h)
			rest = rest.Sub(use)
			back[bucket] = h.Sub(use)
			legs = append(legs, ledger.Posting{Account: held, Amount: h.Neg()})
		}
	}
	rawCash, rawCredit, berr := bucketsIn(tx, in.Subject)
	if berr != nil {
		return "", false, berr
	}
	if total := fundsOf(rawCash, rawCredit).Total(); total.Cmp(rest) < 0 {
		return "", false, fmt.Errorf("finance: draw %s from %s: %w (%s of %s)", in.Ref, wallet, ErrShortWallet, total, rest)
	}
	fromCash, fromCredit := draw(rest, rawCash)
	if in.Cash {
		// The policy refused granted credit for what was bought: cash pays all of it,
		// and credit is never drawn.
		fromCash, fromCredit = rest, money.Zero()
	}
	if a := back[cash].Sub(fromCash); a.Sign() != 0 {
		legs = append(legs, ledger.Posting{Account: cash, Amount: a})
	}
	if a := back[wallet].Sub(fromCredit); a.Sign() != 0 {
		legs = append(legs, ledger.Posting{Account: wallet, Amount: a})
	}
	legs = append(legs, ledger.Posting{Account: acctRevenue, Amount: in.Amount})
	id := mint.ID("use")
	ref := in.Ref
	if ref == "" {
		ref = id // no act id → the entry's own, server-minted (a single debit that stands alone)
	}
	e := ledger.JournalEntry{
		ID:        id,
		Kind:      string(kind),
		Program:   usageProgram(in.Subject),
		Ref:       ref,
		Memo:      in.Model,
		Project:   strings.TrimSpace(in.Project),
		Service:   strings.TrimSpace(in.Service),
		Amount:    in.Amount,
		CreatedAt: time.Now().Unix(),
	}
	if ierr := tx.Insert(e, legs); ierr != nil {
		return "", false, ierr
	}
	return id, true, nil
}

// Close closes every cached org store (best-effort), returning the first error.
func (f *ledgerFinance) Close() error {
	if c, ok := f.books.(interface{ Close() error }); ok {
		return c.Close()
	}
	// The durable plane's books are the deployment's, not this client's: closing
	// them here would ship and release every org's lease out from under whoever
	// else holds a handle. Their close is CloseAll, on the shutdown path.
	return nil
}

// walletAcct maps a billing subject to its account WITHIN the per-org file. The file IS
// the org boundary, so the account carries NO org prefix: the org pool is "wallet"; a
// per-user subject "<org>/<user>" is "wallet:<user>". Rule: split on the FIRST "/"; a
// non-empty suffix (any further "/" flattened to ":") yields "wallet:<suffix>", else
// "wallet". Lowercased + trimmed so "Acme/Bob" and "acme/bob" are the one wallet.
func walletAcct(subject string) string { return account(acctWallet, subject) }

// account is subject's account under root, by the rule [walletAcct] states. Both of a
// wallet's buckets — its credit and its cash — are named by it, so the two can never
// disagree about whose they are.
func account(root, subject string) string {
	subject = strings.ToLower(strings.TrimSpace(subject))
	if _, after, ok := strings.Cut(subject, "/"); ok {
		if suffix := strings.ReplaceAll(after, "/", ":"); suffix != "" {
			return root + ":" + suffix
		}
	}
	return root
}
