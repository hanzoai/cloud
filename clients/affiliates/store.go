package affiliates

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	// The ONE Hanzo SQLite driver (registers "sqlite" under both build tags).
	// Mirrors clients/referrals / clients/crm — one storage pattern.
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors mapped to HTTP status by the handlers:
//
//	errNotFound → 404, errUnknownCode → 404, errSelfAttribution → 400,
//	errCodeTaken → 409, errInvalidCode → 400, errInsufficientPending → 400.
var (
	errNotFound            = errors.New("affiliates: not found")
	errUnknownCode         = errors.New("affiliates: unknown affiliate code")
	errSelfAttribution     = errors.New("affiliates: cannot attribute yourself")
	errCodeTaken           = errors.New("affiliates: code already taken")
	errInvalidCode         = errors.New("affiliates: invalid code")
	errInsufficientPending = errors.New("affiliates: payout exceeds pending commission")
)

// Status values. An affiliate advances applied → approved (and can be suspended).
// Only an APPROVED affiliate has a code and accrues commission.
const (
	StatusApplied   = "applied"
	StatusApproved  = "approved"
	StatusSuspended = "suspended"
)

// Affiliate is one partner org enrolled in the commission program. Org is UNIQUE
// (one affiliate per org); Code is UNIQUE across affiliates (minted on approval,
// vanity opt-in). RateBps is the commission rate in basis points (2000 = 20%).
type Affiliate struct {
	ID            string `json:"id"`
	Org           string `json:"-"` // the affiliate's own org; admin view re-exposes it
	Code          string `json:"code"`
	RequestedCode string `json:"-"` // vanity code requested at apply, pending staff approval
	Status        string `json:"status"`
	RateBps       int64  `json:"rateBps"`
	AccruedCents  int64  `json:"accruedCents"` // lifetime commission accrued
	PaidCents     int64  `json:"paidCents"`    // lifetime commission paid out
	CreatedAt     int64  `json:"createdAt"`
	ApprovedAt    int64  `json:"approvedAt"`
	SuspendedAt   int64  `json:"suspendedAt"`
}

// PendingCents is the commission earned but not yet paid (never negative).
func (a Affiliate) PendingCents() int64 {
	if a.PaidCents >= a.AccruedCents {
		return 0
	}
	return a.AccruedCents - a.PaidCents
}

// AffiliateReferral is one referred_org → affiliate attribution edge. ReferredOrg
// is UNIQUE across the table (an org is attributed to at most one affiliate, ever
// — first-touch), which is also the idempotency key for POST /v1/affiliates/attribute.
type AffiliateReferral struct {
	ID          string `json:"id"`
	AffiliateID string `json:"affiliateId"`
	ReferredOrg string `json:"referredOrg"`
	Code        string `json:"code"`
	CreatedAt   int64  `json:"createdAt"`
}

// Payout is one recorded disbursement of accrued commission. A "credits" method
// issues a commerce grant (Txn set); cash methods (wire/paypal/…) are record-only.
type Payout struct {
	ID          string `json:"id"`
	AffiliateID string `json:"affiliateId"`
	AmountCents int64  `json:"amountCents"`
	Method      string `json:"method"`
	Reference   string `json:"reference"`
	Txn         string `json:"txn,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
}

// Accrual is one per-period commission event (the affiliate_event): the referred
// org's spend for that period × the affiliate's rate. UNIQUE(affiliate, referred,
// period) makes the sweep at-most-once per period — the commission latch.
type Accrual struct {
	ID              string `json:"id"`
	AffiliateID     string `json:"affiliateId"`
	ReferredOrg     string `json:"referredOrg"`
	Period          string `json:"period"`
	SpendCents      int64  `json:"spendCents"`
	CommissionCents int64  `json:"commissionCents"`
	CreatedAt       int64  `json:"createdAt"`
}

// Store is the affiliates database. ONE SQLite file holds every org's affiliate
// record, attribution edges, accrual events, and payouts. A code→affiliate lookup
// is a GLOBAL directory by design (a referred org presents a code minted by ANY
// affiliate); every /v1/affiliates read is scoped by the caller's org server-side.
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS affiliates (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL UNIQUE,
  code           TEXT NOT NULL DEFAULT '',
  requested_code TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL,
  rate_bps       INTEGER NOT NULL,
  accrued_cents  INTEGER NOT NULL DEFAULT 0,
  paid_cents     INTEGER NOT NULL DEFAULT 0,
  created_at     INTEGER NOT NULL,
  approved_at    INTEGER NOT NULL DEFAULT 0,
  suspended_at   INTEGER NOT NULL DEFAULT 0
);
-- A partial UNIQUE index: only a NON-EMPTY code must be unique (many affiliates
-- can share the '' placeholder while still applied/un-coded).
CREATE UNIQUE INDEX IF NOT EXISTS ux_affiliates_code ON affiliates(code) WHERE code <> '';

CREATE TABLE IF NOT EXISTS affiliate_referrals (
  id           TEXT PRIMARY KEY,
  affiliate_id TEXT NOT NULL,
  referred_org TEXT NOT NULL UNIQUE,
  code         TEXT NOT NULL,
  created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_aff_referrals_affiliate ON affiliate_referrals(affiliate_id, created_at);

CREATE TABLE IF NOT EXISTS affiliate_accruals (
  id               TEXT PRIMARY KEY,
  affiliate_id     TEXT NOT NULL,
  referred_org     TEXT NOT NULL,
  period           TEXT NOT NULL,
  spend_cents      INTEGER NOT NULL,
  commission_cents INTEGER NOT NULL,
  created_at       INTEGER NOT NULL,
  UNIQUE(affiliate_id, referred_org, period)
);
CREATE INDEX IF NOT EXISTS ix_aff_accruals_affiliate ON affiliate_accruals(affiliate_id, created_at);

CREATE TABLE IF NOT EXISTS affiliate_payouts (
  id           TEXT PRIMARY KEY,
  affiliate_id TEXT NOT NULL,
  amount_cents INTEGER NOT NULL,
  method       TEXT NOT NULL,
  reference    TEXT NOT NULL DEFAULT '',
  txn          TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_aff_payouts_affiliate ON affiliate_payouts(affiliate_id, created_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("affiliates migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// ── codes ─────────────────────────────────────────────────────────────────────

// codeEncoding: RFC-4648 base32 without padding, lowercased for a readable vanity
// slug. 5 bytes → 8 chars.
var codeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// deriveCode is the DETERMINISTIC fallback slug for an affiliate that requested no
// vanity code: lowercase base32 of the first 5 bytes of SHA-256("hanzo-affiliate:"+
// org). `n` disambiguates the vanishingly rare cross-affiliate collision.
func deriveCode(org string, n int) string {
	seed := "hanzo-affiliate:" + org
	if n > 0 {
		seed = fmt.Sprintf("%s#%d", seed, n)
	}
	sum := sha256.Sum256([]byte(seed))
	return strings.ToLower(codeEncoding.EncodeToString(sum[:5]))
}

// normalizeCode trims + lower-cases a code so a link works in any case; vanity
// codes are stored + compared lowercase.
func normalizeCode(code string) string { return strings.ToLower(strings.TrimSpace(code)) }

// validCode enforces the vanity charset: 3–32 chars of [a-z0-9-], not starting or
// ending with a hyphen. Applied to a normalized (lowercased) code.
func validCode(code string) bool {
	if len(code) < 3 || len(code) > 32 {
		return false
	}
	if code[0] == '-' || code[len(code)-1] == '-' {
		return false
	}
	for _, r := range code {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

// ── affiliate records ─────────────────────────────────────────────────────────

const affiliateCols = `id,org,code,requested_code,status,rate_bps,accrued_cents,paid_cents,created_at,approved_at,suspended_at`

func scanAffiliate(sc interface{ Scan(...any) error }) (Affiliate, error) {
	var a Affiliate
	err := sc.Scan(&a.ID, &a.Org, &a.Code, &a.RequestedCode, &a.Status, &a.RateBps,
		&a.AccruedCents, &a.PaidCents, &a.CreatedAt, &a.ApprovedAt, &a.SuspendedAt)
	return a, err
}

// Apply enrolls org as an affiliate at status=applied with the default rate,
// idempotently. requestedCode is the optional vanity code (validated + minted at
// approval). A repeat apply returns the EXISTING record (first apply wins).
// Returns (affiliate, created).
func (s *Store) Apply(ctx context.Context, id, org, requestedCode string, rateBps int64) (Affiliate, bool, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO affiliates (id, org, requested_code, status, rate_bps, created_at)
		 VALUES (?,?,?,?,?,strftime('%s','now'))`,
		id, org, requestedCode, StatusApplied, rateBps)
	if err == nil {
		a, gerr := s.getByID(ctx, id)
		return a, true, gerr
	}
	if isUnique(err) {
		a, gerr := s.GetByOrg(ctx, org)
		return a, false, gerr
	}
	return Affiliate{}, false, fmt.Errorf("apply: %w", err)
}

func (s *Store) getByID(ctx context.Context, id string) (Affiliate, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+affiliateCols+` FROM affiliates WHERE id=?`, id)
	a, err := scanAffiliate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Affiliate{}, errNotFound
	}
	if err != nil {
		return Affiliate{}, fmt.Errorf("get affiliate: %w", err)
	}
	return a, nil
}

// GetByID re-reads an affiliate by id (post-mutation refresh).
func (s *Store) GetByID(ctx context.Context, id string) (Affiliate, error) { return s.getByID(ctx, id) }

// GetByOrg reads the affiliate record for org, or errNotFound.
func (s *Store) GetByOrg(ctx context.Context, org string) (Affiliate, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+affiliateCols+` FROM affiliates WHERE org=?`, org)
	a, err := scanAffiliate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Affiliate{}, errNotFound
	}
	if err != nil {
		return Affiliate{}, fmt.Errorf("get affiliate by org: %w", err)
	}
	return a, nil
}

// AffiliateForCode reverse-resolves an affiliate code to its APPROVED owner (an
// un-approved affiliate has no code). Trims + lower-cases the client-supplied code.
func (s *Store) AffiliateForCode(ctx context.Context, code string) (Affiliate, error) {
	code = normalizeCode(code)
	if code == "" {
		return Affiliate{}, errUnknownCode
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+affiliateCols+` FROM affiliates WHERE code=? AND status=?`, code, StatusApproved)
	a, err := scanAffiliate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Affiliate{}, errUnknownCode
	}
	if err != nil {
		return Affiliate{}, fmt.Errorf("resolve code: %w", err)
	}
	return a, nil
}

// Approve moves an affiliate to approved and mints its code: wantCode wins, else
// the requested vanity code, else a deterministic derived slug. A non-empty code
// is validated + uniqueness-enforced (errCodeTaken on collision with another
// affiliate). Idempotent for the same resolved code.
func (s *Store) Approve(ctx context.Context, id, wantCode string, now int64) (Affiliate, error) {
	a, err := s.getByID(ctx, id)
	if err != nil {
		return Affiliate{}, err
	}
	code := normalizeCode(wantCode)
	if code == "" {
		code = normalizeCode(a.RequestedCode)
	}
	if code != "" {
		if !validCode(code) {
			return Affiliate{}, errInvalidCode
		}
		if err := s.setApproved(ctx, id, code, now); err != nil {
			if isUnique(err) {
				return Affiliate{}, errCodeTaken
			}
			return Affiliate{}, err
		}
		return s.getByID(ctx, id)
	}
	// No requested/explicit code → derive a stable slug, salt-retry on collision.
	for n := 0; n < 8; n++ {
		cand := deriveCode(a.Org, n)
		if err := s.setApproved(ctx, id, cand, now); err == nil {
			return s.getByID(ctx, id)
		} else if !isUnique(err) {
			return Affiliate{}, err
		}
	}
	return Affiliate{}, fmt.Errorf("approve: exhausted derived-code collision retries for %q", a.Org)
}

func (s *Store) setApproved(ctx context.Context, id, code string, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE affiliates
		    SET status=?, code=?, approved_at = CASE WHEN approved_at=0 THEN ? ELSE approved_at END, suspended_at=0
		  WHERE id=?`,
		StatusApproved, code, now, id)
	return err
}

// Suspend moves an affiliate to suspended (its code stops resolving for new
// attribution; earned commission is unaffected). errNotFound if missing.
func (s *Store) Suspend(ctx context.Context, id string, now int64) (Affiliate, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE affiliates SET status=?, suspended_at=? WHERE id=?`, StatusSuspended, now, id)
	if err != nil {
		return Affiliate{}, fmt.Errorf("suspend: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Affiliate{}, errNotFound
	}
	return s.getByID(ctx, id)
}

// ListAll returns every affiliate newest-first (the admin directory), bounded.
func (s *Store) ListAll(ctx context.Context, limit int) ([]Affiliate, error) {
	return s.queryAffiliates(ctx, `SELECT `+affiliateCols+` FROM affiliates ORDER BY created_at DESC LIMIT ?`, limit)
}

// ListApproved returns every approved affiliate (the sweep set), oldest-first.
func (s *Store) ListApproved(ctx context.Context, limit int) ([]Affiliate, error) {
	return s.queryAffiliates(ctx, `SELECT `+affiliateCols+` FROM affiliates WHERE status=? ORDER BY created_at ASC LIMIT ?`, StatusApproved, limit)
}

func (s *Store) queryAffiliates(ctx context.Context, q string, args ...any) ([]Affiliate, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list affiliates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Affiliate, 0, 16)
	for rows.Next() {
		a, err := scanAffiliate(rows)
		if err != nil {
			return nil, fmt.Errorf("scan affiliate: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── attribution edges ─────────────────────────────────────────────────────────

// Attribute records a referredOrg → affiliate edge, idempotently. The referred org
// is the VALIDATED caller (never client-supplied). One-per-referred-org (UNIQUE)
// makes a repeat attribute a no-op returning the FIRST edge (first-touch wins).
// Self-attribution (an affiliate's own org) is refused. Returns (edge, created).
func (s *Store) Attribute(ctx context.Context, id, affiliateID, referredOrg, affiliateOrg, code string) (AffiliateReferral, bool, error) {
	if referredOrg == affiliateOrg {
		return AffiliateReferral{}, false, errSelfAttribution
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO affiliate_referrals (id, affiliate_id, referred_org, code, created_at)
		 VALUES (?,?,?,?,strftime('%s','now'))`,
		id, affiliateID, referredOrg, normalizeCode(code))
	if err == nil {
		r, gerr := s.getReferralByReferred(ctx, referredOrg)
		return r, true, gerr
	}
	if isUnique(err) {
		r, gerr := s.getReferralByReferred(ctx, referredOrg)
		return r, false, gerr
	}
	return AffiliateReferral{}, false, fmt.Errorf("attribute: %w", err)
}

const referralCols = `id,affiliate_id,referred_org,code,created_at`

func scanReferral(sc interface{ Scan(...any) error }) (AffiliateReferral, error) {
	var r AffiliateReferral
	err := sc.Scan(&r.ID, &r.AffiliateID, &r.ReferredOrg, &r.Code, &r.CreatedAt)
	return r, err
}

func (s *Store) getReferralByReferred(ctx context.Context, referredOrg string) (AffiliateReferral, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+referralCols+` FROM affiliate_referrals WHERE referred_org=?`, referredOrg)
	r, err := scanReferral(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AffiliateReferral{}, errNotFound
	}
	if err != nil {
		return AffiliateReferral{}, fmt.Errorf("get referral: %w", err)
	}
	return r, nil
}

// ListReferrals returns an affiliate's attribution edges (the orgs it referred),
// newest-first, bounded.
func (s *Store) ListReferrals(ctx context.Context, affiliateID string, limit int) ([]AffiliateReferral, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+referralCols+` FROM affiliate_referrals WHERE affiliate_id=? ORDER BY created_at DESC LIMIT ?`, affiliateID, limit)
	if err != nil {
		return nil, fmt.Errorf("list referrals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]AffiliateReferral, 0, 16)
	for rows.Next() {
		r, err := scanReferral(rows)
		if err != nil {
			return nil, fmt.Errorf("scan referral: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountReferrals returns how many orgs an affiliate has referred.
func (s *Store) CountReferrals(ctx context.Context, affiliateID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM affiliate_referrals WHERE affiliate_id=?`, affiliateID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count referrals: %w", err)
	}
	return n, nil
}

// ReferralCountsByAffiliate returns affiliate_id → referred-org count in ONE
// GROUP BY (the admin directory's per-row count, no N+1 fan-out).
func (s *Store) ReferralCountsByAffiliate(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT affiliate_id, COUNT(*) FROM affiliate_referrals GROUP BY affiliate_id`)
	if err != nil {
		return nil, fmt.Errorf("count referrals by affiliate: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int)
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scan referral count: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ── accrual latch (the commission event, at-most-once per period) ─────────────

// LatchAccrual atomically records ONE accrual for (affiliate, referredOrg, period)
// and adds the commission to the affiliate's accrued balance — in a single
// transaction. RowsAffected on the INSERT is the latch: a UNIQUE violation means
// this period was already accrued (returns false, no error, no double-accrual).
// Returns won=true only when THIS call created the accrual and moved the balance.
func (s *Store) LatchAccrual(ctx context.Context, accrualID, affiliateID, referredOrg, period string, spendCents, commissionCents, now int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("accrual tx: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO affiliate_accruals (id, affiliate_id, referred_org, period, spend_cents, commission_cents, created_at)
		 VALUES (?,?,?,?,?,?,?)`,
		accrualID, affiliateID, referredOrg, period, spendCents, commissionCents, now)
	if err != nil {
		_ = tx.Rollback()
		if isUnique(err) {
			return false, nil // already accrued this period — idempotent
		}
		return false, fmt.Errorf("insert accrual: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE affiliates SET accrued_cents = accrued_cents + ? WHERE id=?`, commissionCents, affiliateID); err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("accrue balance: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("accrual commit: %w", err)
	}
	return true, nil
}

// ── payouts ───────────────────────────────────────────────────────────────────

// RecordPayout atomically RESERVES amountCents against the affiliate's pending
// commission (accrued − paid) and records a payout row — in one transaction. The
// WHERE guard `(accrued_cents − paid_cents) >= amount` makes it impossible to pay
// out more than is owed, even under concurrency (RowsAffected 0 → errInsufficient
// Pending). The commerce grant (for a credits payout) happens AFTER, outside the
// tx; SetPayoutTxn records the receipt. errNotFound if the affiliate is missing.
func (s *Store) RecordPayout(ctx context.Context, payoutID, affiliateID string, amountCents int64, method, reference string, now int64) (Payout, error) {
	if amountCents <= 0 {
		return Payout{}, errInsufficientPending
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Payout{}, fmt.Errorf("payout tx: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE affiliates SET paid_cents = paid_cents + ?
		  WHERE id=? AND (accrued_cents - paid_cents) >= ?`,
		amountCents, affiliateID, amountCents)
	if err != nil {
		_ = tx.Rollback()
		return Payout{}, fmt.Errorf("reserve payout: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		_ = tx.Rollback()
		// Distinguish missing affiliate from insufficient pending for a clear 404 vs 400.
		if _, gerr := s.getByID(ctx, affiliateID); gerr == errNotFound {
			return Payout{}, errNotFound
		}
		return Payout{}, errInsufficientPending
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO affiliate_payouts (id, affiliate_id, amount_cents, method, reference, created_at)
		 VALUES (?,?,?,?,?,?)`,
		payoutID, affiliateID, amountCents, method, reference, now); err != nil {
		_ = tx.Rollback()
		return Payout{}, fmt.Errorf("insert payout: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Payout{}, fmt.Errorf("payout commit: %w", err)
	}
	return Payout{ID: payoutID, AffiliateID: affiliateID, AmountCents: amountCents, Method: method, Reference: reference, CreatedAt: now}, nil
}

// VoidPayout reverses a RecordPayout that could not be BACKED by the treasury
// reserve: it deletes the payout row and restores the reserved amount to pending
// (paid_cents −= amount), in one transaction. It is the compensating action when the
// fund cannot cover a payout the pending-guard already reserved — so a blocked payout
// leaves the affiliate's pending intact, honestly, instead of silently burning it.
func (s *Store) VoidPayout(ctx context.Context, payoutID, affiliateID string, amountCents int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("void tx: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM affiliate_payouts WHERE id=?`, payoutID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete payout: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE affiliates SET paid_cents = paid_cents - ? WHERE id=?`, amountCents, affiliateID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("restore pending: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("void commit: %w", err)
	}
	return nil
}

// SetPayoutTxn records the commerce ledger transaction id after a credits payout
// deposit lands (best-effort receipt; the pending reservation is the authority).
func (s *Store) SetPayoutTxn(ctx context.Context, payoutID, txn string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE affiliate_payouts SET txn=? WHERE id=?`, txn, payoutID)
	if err != nil {
		return fmt.Errorf("set payout txn: %w", err)
	}
	return nil
}

const payoutCols = `id,affiliate_id,amount_cents,method,reference,txn,created_at`

func scanPayout(sc interface{ Scan(...any) error }) (Payout, error) {
	var p Payout
	err := sc.Scan(&p.ID, &p.AffiliateID, &p.AmountCents, &p.Method, &p.Reference, &p.Txn, &p.CreatedAt)
	return p, err
}

// ListPayouts returns an affiliate's payout history, newest-first, bounded.
func (s *Store) ListPayouts(ctx context.Context, affiliateID string, limit int) ([]Payout, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+payoutCols+` FROM affiliate_payouts WHERE affiliate_id=? ORDER BY created_at DESC LIMIT ?`, affiliateID, limit)
	if err != nil {
		return nil, fmt.Errorf("list payouts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Payout, 0, 8)
	for rows.Next() {
		p, err := scanPayout(rows)
		if err != nil {
			return nil, fmt.Errorf("scan payout: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// isUnique reports whether err is a SQLite UNIQUE/PRIMARY-KEY constraint violation
// (the idempotency + collision signal), matched on message text so it holds under
// BOTH the cgo and pure-Go drivers.
func isUnique(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique") || strings.Contains(m, "constraint failed") || strings.Contains(m, "primary key")
}
