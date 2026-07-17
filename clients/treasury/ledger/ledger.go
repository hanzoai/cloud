// Package ledger is the native, double-entry accounting core of the Hanzo finance
// stack — the store-agnostic engine that owns EVERY accounting rule (balanced
// postings, a non-negative reserve fund, idempotent journal entries, revenue-share
// math) and NOTHING about how those facts are persisted or served.
//
// It has ZERO coupling to the cloud binary: no zip/HTTP, no cloud.Deps, no IAM,
// no SQLite. Persistence is a PORT — the Store/Tx interfaces below — so the same
// core runs on Hanzo Base (HIP-0105 per-tenant SQLite), an in-memory fake, or any
// future backend by swapping the adapter. This is deliberate: the package is the
// SEED of the native `hanzoai/finance` central ledger (the Go replacement for the
// Formance Ledger/Wallets/Reconciliation stack). Lifting it to that repo is a
// directory move + import-path rewrite — no logic changes — because all the domain
// lives here and every dependency points INWARD (adapters depend on the core, never
// the reverse).
//
// The model is the classic one, reduced to its essence:
//
//   - An ACCOUNT is an id in a chart (e.g. "fund:reserve", "revenue:platform",
//     "payout:referral"). Its BALANCE is the signed sum of every posting to it.
//   - A JOURNAL ENTRY is an atomic set of POSTINGS whose signed amounts sum to
//     EXACTLY zero (double-entry: every debit has an equal credit). An entry that
//     does not balance is refused — the ledger can never be made inconsistent.
//   - Money in minor units (cents), int64. No floats ever touch a balance.
//
// The reserve fund is one account ("fund:reserve"): revenue-share accruals credit
// it, backed payouts debit it, and a debit that would drive it negative is refused.
// That single rule — the fund cannot overdraw — is what makes the growth-loop
// payouts a REAL, backed reserve instead of unbounded minting.
package ledger

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/clients/money"
)

// Canonical chart-of-accounts ids. ONE shared reserve pool with per-program payout
// sinks — a single reserve-health number plus a payouts-by-program breakdown,
// without fragmenting the fund. (A caller wanting isolated per-program funds would
// use distinct reserve account ids; the engine is agnostic — these are the Hanzo
// treasury's convention, kept in one place.)
const (
	// AccountReserve is the shared reserve fund — the OSS/affiliate/referral pool.
	// Positive balance == money available to back payouts. Never goes negative.
	AccountReserve = "fund:reserve"
	// AccountRevenue is the source counter-account: revenue-share accruals move
	// value FROM here INTO the fund, so its balance is the negative of the total
	// ever allocated to the reserve (its magnitude == lifetime accrued).
	AccountRevenue = "revenue:platform"
	// payoutPrefix namespaces the per-program payout sink accounts
	// ("payout:referral", "payout:affiliate", "payout:author"). A sink's balance is
	// the lifetime credit paid to that program, backed 1:1 by a fund debit.
	payoutPrefix = "payout:"
)

// Entry kinds classify a journal entry's economic meaning (also the first half of
// its idempotency key). Kept small and closed.
const (
	KindAccrual = "accrual" // revenue-share allocation: revenue:platform → fund:reserve
	KindPayout  = "payout"  // backed disbursement: fund:reserve → payout:<program>
	KindSeed    = "seed"    // an explicit capital injection into the fund (bootstrap)
)

// bpsDenominator is the basis-point denominator (10000 bps == 100%). Revenue share
// is integer math: share = revenue * bps / bpsDenominator (floor), so no fractional
// cent is ever created.
const bpsDenominator = 10000

// Sentinel errors. Callers map these to transport status; the engine never knows
// about HTTP.
var (
	// ErrUnbalanced is returned when an entry's postings do not sum to zero — a
	// double-entry violation the engine refuses to persist.
	ErrUnbalanced = errors.New("ledger: entry postings do not sum to zero")
	// ErrNonPositive is returned for a non-positive money amount where a positive
	// one is required (an accrual/payout of ≤ 0).
	ErrNonPositive = errors.New("ledger: amount must be positive")
	// ErrEmptyRef is returned when an idempotency ref is missing — every entry MUST
	// carry one so a retry is a no-op, never a double post.
	ErrEmptyRef = errors.New("ledger: entry ref is required")
)

// Posting is one leg of a journal entry: a signed amount against an account. The
// sign carries direction (a debit is negative to the drawn-down account, the equal
// credit positive to the funded account); the engine only requires the legs to sum
// to zero.
type Posting struct {
	Account string       `json:"account"`
	Amount  money.Amount `json:"amount"` // signed 18-decimal USD (1e-18); Σ over an entry == 0
}

// Entry is one balanced journal entry. Ref (with Kind+Program) is the idempotency
// key: re-posting the same (Kind, Program, Ref) is a no-op that returns the
// original — so a crashed-and-retried payout debits the fund AT MOST ONCE.
type Entry struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind"`
	Program   string       `json:"program,omitempty"` // referral|affiliate|author for payouts; "" otherwise
	Ref       string       `json:"ref"`               // idempotency ref (unique within Kind+Program)
	Memo      string       `json:"memo,omitempty"`
	Amount    money.Amount `json:"amount"` // the entry's magnitude (display; postings hold the signed truth)
	CreatedAt int64        `json:"createdAt"`
	Postings  []Posting    `json:"postings,omitempty"`
}

// Policy is the revenue-share configuration: the fraction of net platform revenue,
// in basis points, that a sweep accrues into the reserve fund. One value, one
// place; SuperAdmin adjusts it.
type Policy struct {
	RevenueShareBps int64 `json:"revenueShareBps"`
	UpdatedAt       int64 `json:"updatedAt"`
}

// Report is the reserve-fund health snapshot: what's available now, what's been
// accrued and paid over all time, and the per-program payout breakdown. Every
// figure is derived from the same postings, so it always reconciles
// (Fund == Accrued − Paid).
type Report struct {
	ReserveCents     int64            `json:"reserveCents"`     // fund:reserve balance (available now)
	AccruedCents     int64            `json:"accruedCents"`     // lifetime revenue-share into the fund
	PaidCents        int64            `json:"paidCents"`        // lifetime backed payouts out of the fund
	ByProgramCents   map[string]int64 `json:"byProgramCents"`   // program → lifetime paid
	Policy           Policy           `json:"policy"`           // current revenue-share policy
	SolventForPayout bool             `json:"solventForPayout"` // reserve > 0: at least some payout is backable
}

// Tx is the transactional port: the engine performs a read-then-write (balance
// guard, idempotency check, insert) inside a single Store-provided transaction so
// concurrent debits can never overdraw the fund. Adapters implement it over their
// native transaction; the engine composes the DOMAIN rules on top.
type Tx interface {
	// EntryByRef returns the existing entry for (kind, program, ref) if present —
	// the idempotency lookup performed inside the same tx as the insert.
	EntryByRef(kind, program, ref string) (Entry, bool, error)
	// Balance returns an account's signed balance as visible inside this tx.
	Balance(account string) (money.Amount, error)
	// Insert upserts every referenced account then writes the entry and its
	// postings. The engine has already validated the postings balance and the ref
	// is free; the adapter only persists.
	Insert(e Entry, postings []Posting) error
}

// Store is the persistence port. The engine owns all rules; the Store owns storage
// and atomicity only. A Base/SQLite adapter, an in-memory fake, or a future backend
// all satisfy this — the engine never changes.
type Store interface {
	// Tx runs fn inside one serializable transaction, committing on nil and rolling
	// back on error.
	Tx(ctx context.Context, fn func(Tx) error) error
	// Balance returns an account's signed balance (read path, no tx).
	Balance(ctx context.Context, account string) (money.Amount, error)
	// BalancesWithPrefix returns balances for every account whose id has prefix
	// (e.g. "payout:") — the per-program rollup.
	BalancesWithPrefix(ctx context.Context, prefix string) (map[string]money.Amount, error)
	// Entries returns the most recent entries (newest first, with postings), bounded.
	Entries(ctx context.Context, limit int) ([]Entry, error)
	// Policy returns the current revenue-share policy (zero value if never set).
	Policy(ctx context.Context) (Policy, error)
	// SetPolicy persists the revenue-share policy.
	SetPolicy(ctx context.Context, p Policy) error
}

// Backend is the ledger-of-record PORT the treasury posts through. TWO adapters
// satisfy it: the native engine below (Base/SQLite — the offline/default backend, so
// the reserve fund ships today) and the Formance adapter in clients/treasury/formance
// (the Postgres-backed Formance Ledger, the production ledger of record when
// FORMANCE_LEDGER_URL is wired — HIP finance stack). The treasury client holds the
// Hanzo policy/fund/payout logic ABOVE this port; the port GUARANTEES double-entry +
// the reserve overdraw guard (Formance via Numscript source semantics; the native
// engine via its balance guard). The two adapters are byte-compatible on the accounts
// that matter (fund:reserve, payout:<program>), so switching backend is a config flip.
type Backend interface {
	// Name identifies the backend of record ("native" | "formance").
	Name() string
	Policy(ctx context.Context) (Policy, error)
	SetPolicy(ctx context.Context, bps, now int64) (Policy, error)
	Accrue(ctx context.Context, period string, revenueCents, now int64) (Entry, bool, error)
	Seed(ctx context.Context, ref, memo string, amountCents, now int64) (Entry, bool, error)
	DebitReserve(ctx context.Context, program, ref, memo string, amountCents, now int64) (Entry, bool, bool, error)
	Snapshot(ctx context.Context) (Report, error)
	Entries(ctx context.Context, limit int) ([]Entry, error)
	ReserveCents(ctx context.Context) (int64, error)
	Root(ctx context.Context) ([32]byte, int, error)
	// AccountsWithPrefix returns account→balance for every account under prefix — the
	// scope-aware read primitive: a per-org caller reads its own "org:<tenant>:"
	// prefix; SuperAdmin reads house prefixes ("fund:", "payout:", "revenue:").
	AccountsWithPrefix(ctx context.Context, prefix string) (map[string]int64, error)
}

// PolicyStore is the small native config store the Formance backend borrows to hold
// the Hanzo revenue-share policy (which Formance does not model — it is Hanzo config,
// not accounting). sqlstore satisfies it, so policy persists identically regardless of
// which backend owns the journal.
type PolicyStore interface {
	Policy(ctx context.Context) (Policy, error)
	SetPolicy(ctx context.Context, p Policy) error
}

// Ledger is the native double-entry engine bound to a Store — the offline/default
// Backend. It is safe for concurrent use to the extent the Store's Tx is serializable
// (the SQLite adapter serializes on a single connection, so the balance guard holds
// under load).
type Ledger struct {
	store Store
}

// New binds the engine to a persistence adapter.
func New(store Store) *Ledger { return &Ledger{store: store} }

// Name reports the native backend id. (Backend interface.)
func (l *Ledger) Name() string { return "native" }

// compile-time proof the native engine satisfies the ledger-of-record port.
var _ Backend = (*Ledger)(nil)

// Store exposes the underlying adapter for read-only queries the engine does not
// wrap (recent-entries listing, policy read) — the cloud handler layer uses it
// directly rather than the engine re-exporting every read.
func (l *Ledger) Store() Store { return l.store }

// PayoutAccount is the sink account id for a program's backed payouts.
func PayoutAccount(program string) string { return payoutPrefix + program }

// ── domain operations ────────────────────────────────────────────────────────

// Accrue posts the revenue-share allocation for a period: it reads the policy,
// computes share = revenue * bps / 10000, and posts (revenue:platform −share,
// fund:reserve +share). Idempotent by period (ref "accrual:<period>") so re-running
// a sweep for the same period never double-accrues. A zero share (no policy, or
// revenue too small to yield a cent) is a no-op returning created=false.
func (l *Ledger) Accrue(ctx context.Context, period string, revenueCents, now int64) (Entry, bool, error) {
	period = strings.TrimSpace(period)
	if period == "" {
		return Entry{}, false, ErrEmptyRef
	}
	if revenueCents < 0 {
		return Entry{}, false, ErrNonPositive
	}
	pol, err := l.store.Policy(ctx)
	if err != nil {
		return Entry{}, false, fmt.Errorf("read policy: %w", err)
	}
	share := revenueCents * pol.RevenueShareBps / bpsDenominator
	if share <= 0 {
		return Entry{}, false, nil // nothing to accrue this period
	}
	id, err := genID("acc")
	if err != nil {
		return Entry{}, false, err
	}
	shareAmt := money.FromCents(share)
	e := Entry{
		ID:        id,
		Kind:      KindAccrual,
		Ref:       "accrual:" + period,
		Memo:      fmt.Sprintf("revenue-share %d bps of %d cents (period %s)", pol.RevenueShareBps, revenueCents, period),
		Amount:    shareAmt,
		CreatedAt: now,
	}
	postings := []Posting{
		{Account: AccountRevenue, Amount: shareAmt.Neg()},
		{Account: AccountReserve, Amount: shareAmt},
	}
	return l.post(ctx, e, postings)
}

// Seed posts an explicit capital injection into the reserve fund (revenue:platform
// −amount, fund:reserve +amount), idempotent by ref. It bootstraps a fresh fund so
// backed payouts can begin before the first revenue-share sweep. Distinct kind
// (seed) so it is auditable apart from an accrual.
func (l *Ledger) Seed(ctx context.Context, ref, memo string, amountCents, now int64) (Entry, bool, error) {
	if amountCents <= 0 {
		return Entry{}, false, ErrNonPositive
	}
	if strings.TrimSpace(ref) == "" {
		return Entry{}, false, ErrEmptyRef
	}
	id, err := genID("seed")
	if err != nil {
		return Entry{}, false, err
	}
	amt := money.FromCents(amountCents)
	e := Entry{ID: id, Kind: KindSeed, Ref: ref, Memo: memo, Amount: amt, CreatedAt: now}
	postings := []Posting{
		{Account: AccountRevenue, Amount: amt.Neg()},
		{Account: AccountReserve, Amount: amt},
	}
	return l.post(ctx, e, postings)
}

// DebitReserve is the BACKED-PAYOUT primitive: it posts (fund:reserve −amount,
// payout:<program> +amount) ONLY when the reserve can cover it, atomically. It
// returns:
//
//   - backed=true, created=true  → the fund had the funds; the entry was posted.
//     The caller MUST now credit the recipient wallet (the external commerce
//     deposit): fund down, wallet up — reconciled.
//   - backed=true, created=false → an entry for this ref already exists (idempotent
//     replay); the fund was debited exactly once on the first call.
//   - backed=false               → INSUFFICIENT RESERVE; nothing was posted. The
//     caller MUST NOT credit the wallet — the payout is honestly pending/blocked
//     until the fund is replenished.
//
// The balance read and the insert happen in one Store transaction, so two
// concurrent debits cannot both pass the guard and overdraw the fund. Idempotency
// by (KindPayout, program, ref) makes a retry a no-op — the fund is charged AT MOST
// ONCE per ref, the mirror of the loops' own at-most-once credit latch.
func (l *Ledger) DebitReserve(ctx context.Context, program, ref, memo string, amountCents, now int64) (entry Entry, backed, created bool, err error) {
	program = strings.TrimSpace(program)
	ref = strings.TrimSpace(ref)
	if amountCents <= 0 {
		return Entry{}, false, false, ErrNonPositive
	}
	if program == "" || ref == "" {
		return Entry{}, false, false, ErrEmptyRef
	}
	id, gerr := genID("pay")
	if gerr != nil {
		return Entry{}, false, false, gerr
	}
	amt := money.FromCents(amountCents)
	e := Entry{ID: id, Kind: KindPayout, Program: program, Ref: ref, Memo: memo, Amount: amt, CreatedAt: now}
	postings := []Posting{
		{Account: AccountReserve, Amount: amt.Neg()},
		{Account: PayoutAccount(program), Amount: amt},
	}
	if verr := validateBalanced(postings); verr != nil {
		return Entry{}, false, false, verr
	}
	txErr := l.store.Tx(ctx, func(tx Tx) error {
		existing, ok, ferr := tx.EntryByRef(KindPayout, program, ref)
		if ferr != nil {
			return ferr
		}
		if ok {
			entry, backed, created = existing, true, false // already debited once
			return nil
		}
		bal, berr := tx.Balance(AccountReserve)
		if berr != nil {
			return berr
		}
		if bal.Cmp(amt) < 0 {
			backed = false // insufficient reserve — post nothing, leave the fund intact
			return nil
		}
		if ierr := tx.Insert(e, postings); ierr != nil {
			return ierr
		}
		e.Postings = postings
		entry, backed, created = e, true, true
		return nil
	})
	if txErr != nil {
		return Entry{}, false, false, txErr
	}
	return entry, backed, created, nil
}

// post is the generic balanced-entry writer used by Accrue/Seed: validate the legs
// sum to zero, then insert idempotently by ref inside one transaction.
func (l *Ledger) post(ctx context.Context, e Entry, postings []Posting) (Entry, bool, error) {
	if strings.TrimSpace(e.Ref) == "" {
		return Entry{}, false, ErrEmptyRef
	}
	if err := validateBalanced(postings); err != nil {
		return Entry{}, false, err
	}
	var out Entry
	var created bool
	err := l.store.Tx(ctx, func(tx Tx) error {
		existing, ok, ferr := tx.EntryByRef(e.Kind, e.Program, e.Ref)
		if ferr != nil {
			return ferr
		}
		if ok {
			out, created = existing, false
			return nil
		}
		if ierr := tx.Insert(e, postings); ierr != nil {
			return ierr
		}
		e.Postings = postings
		out, created = e, true
		return nil
	})
	if err != nil {
		return Entry{}, false, err
	}
	return out, created, nil
}

// ── read/reporting ───────────────────────────────────────────────────────────

// ReserveCents is the fund's available balance — the ceiling on total backable
// payouts right now.
func (l *Ledger) ReserveCents(ctx context.Context) (int64, error) {
	bal, err := l.store.Balance(ctx, AccountReserve)
	if err != nil {
		return 0, err
	}
	return bal.Cents(), nil
}

// Snapshot computes the reserve-fund health report. Fund == Accrued − Paid always,
// because all three are the same postings viewed three ways — it can never drift.
func (l *Ledger) Snapshot(ctx context.Context) (Report, error) {
	reserve, err := l.store.Balance(ctx, AccountReserve)
	if err != nil {
		return Report{}, fmt.Errorf("reserve balance: %w", err)
	}
	payouts, err := l.store.BalancesWithPrefix(ctx, payoutPrefix)
	if err != nil {
		return Report{}, fmt.Errorf("payout balances: %w", err)
	}
	byProgram := make(map[string]int64, len(payouts))
	var paid int64
	for acct, bal := range payouts {
		program := strings.TrimPrefix(acct, payoutPrefix)
		byProgram[program] = bal.Cents()
		paid += bal.Cents()
	}
	pol, err := l.store.Policy(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("policy: %w", err)
	}
	reserveCents := reserve.Cents()
	return Report{
		ReserveCents:     reserveCents,
		AccruedCents:     reserveCents + paid, // lifetime into the fund
		PaidCents:        paid,
		ByProgramCents:   byProgram,
		Policy:           pol,
		SolventForPayout: reserve.Sign() > 0,
	}, nil
}

// Entries returns the most recent journal entries (newest first, with postings).
func (l *Ledger) Entries(ctx context.Context, limit int) ([]Entry, error) {
	return l.store.Entries(ctx, limit)
}

// AccountsWithPrefix returns account→balance (in cents, the Backend facade unit) for
// accounts under prefix.
func (l *Ledger) AccountsWithPrefix(ctx context.Context, prefix string) (map[string]int64, error) {
	bals, err := l.store.BalancesWithPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(bals))
	for acct, bal := range bals {
		out[acct] = bal.Cents()
	}
	return out, nil
}

// Root computes a deterministic commitment over the ENTIRE journal plus the reserve
// balance: a SHA-256 hash-chain of every entry (in a canonical created-at,id order)
// and each of its postings, terminated by the fund balance. It is what the Hanzo L1
// anchor commits on-chain — a change to any historical posting changes the root, so
// the on-chain value makes the off-chain books tamper-evident. Returns the 32-byte
// root and the number of entries covered. (A flat hash-chain, not a Merkle tree:
// the treasury's entry volume is modest and the anchor needs a single commitment,
// not per-entry inclusion proofs — YAGNI until it does.)
func (l *Ledger) Root(ctx context.Context) (root [32]byte, entryCount int, err error) {
	entries, err := l.store.Entries(ctx, 0) // 0 = all
	if err != nil {
		return [32]byte{}, 0, fmt.Errorf("root: list entries: %w", err)
	}
	reserve, err := l.store.Balance(ctx, AccountReserve)
	if err != nil {
		return [32]byte{}, 0, fmt.Errorf("root: reserve balance: %w", err)
	}
	return ComputeRoot(entries, reserve), len(entries), nil
}

// ComputeRoot hashes a journal (in a canonical created-at,id order) plus the reserve
// balance into a 32-byte commitment — the value the Hanzo L1 anchor commits on-chain.
// Amounts are serialized as their EXACT 18-decimal USD decimal string (the on-chain uint256
// value), so the off-chain preimage and the on-chain balance agree bit-for-bit. Both
// backends (native + Formance) use it, so the on-chain root is computed one way
// regardless of which ledger owns the books.
func ComputeRoot(entries []Entry, reserve money.Amount) [32]byte {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt != sorted[j].CreatedAt {
			return sorted[i].CreatedAt < sorted[j].CreatedAt
		}
		return sorted[i].ID < sorted[j].ID
	})
	h := sha256.New()
	for _, e := range sorted {
		fmt.Fprintf(h, "%s|%s|%s|%s|%s|%d\n", e.ID, e.Kind, e.Program, e.Ref, e.Amount.AttoString(), e.CreatedAt)
		for _, p := range e.Postings {
			fmt.Fprintf(h, "\t%s|%s\n", p.Account, p.Amount.AttoString())
		}
	}
	fmt.Fprintf(h, "reserve|%s\n", reserve.AttoString())
	var root [32]byte
	copy(root[:], h.Sum(nil))
	return root
}

// Policy returns the current revenue-share policy.
func (l *Ledger) Policy(ctx context.Context) (Policy, error) { return l.store.Policy(ctx) }

// SetPolicy persists a revenue-share policy after validating the basis points are
// in range [0, 10000] (0%–100%).
func (l *Ledger) SetPolicy(ctx context.Context, bps, now int64) (Policy, error) {
	if bps < 0 || bps > bpsDenominator {
		return Policy{}, fmt.Errorf("revenueShareBps must be in [0,%d], got %d", bpsDenominator, bps)
	}
	p := Policy{RevenueShareBps: bps, UpdatedAt: now}
	if err := l.store.SetPolicy(ctx, p); err != nil {
		return Policy{}, err
	}
	return p, nil
}

// ── invariants + helpers ─────────────────────────────────────────────────────

// validateBalanced enforces the one non-negotiable rule: an entry's postings sum to
// exactly zero and there are at least two legs. A violation is refused before any
// write, so the ledger can never be persisted in an unbalanced state.
func validateBalanced(postings []Posting) error {
	if len(postings) < 2 {
		return ErrUnbalanced
	}
	sum := money.Zero()
	for _, p := range postings {
		sum = sum.Add(p.Amount)
	}
	if !sum.IsZero() {
		return ErrUnbalanced
	}
	return nil
}

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits) —
// stdlib only, so the core carries no id-library dependency into hanzoai/finance.
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("ledger: rng: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
