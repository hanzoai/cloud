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
	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors mapped to HTTP status by the handlers:
//
//	errNotFound → 404, errUnknownCode → 404, errSelfAttribution → 400,
//	errCodeTaken → 409, errInvalidCode → 400, errInsufficientPending → 400,
//	errCycle → 400.
var (
	errNotFound            = errors.New("affiliates: not found")
	errUnknownCode         = errors.New("affiliates: unknown affiliate code")
	errSelfAttribution     = errors.New("affiliates: cannot attribute yourself")
	errCodeTaken           = errors.New("affiliates: code already taken")
	errInvalidCode         = errors.New("affiliates: invalid code")
	errInsufficientPending = errors.New("affiliates: payout exceeds pending commission")
	// errCycle is returned when setting a referredBy edge would close a loop in the
	// upline graph (the proposed referrer is already a descendant of the referred
	// party). Set-once edges + this guard keep the graph a forest.
	errCycle = errors.New("affiliates: referral would create a cycle in the upline")
)

// walkCap bounds a cycle check or upline climb over the referredBy graph. The graph
// is a forest by construction (set-once edges + the cycle guard), so a real chain is
// short; the cap is only a corruption/DoS backstop.
const walkCap = 64

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
	OwnerUser     string `json:"-"` // the user who applied; the head of this affiliate's user-referral chain
	Code          string `json:"code"`
	RequestedCode string `json:"-"` // vanity code requested at apply, pending staff approval
	Status        string `json:"status"`
	RateBps       int64  `json:"rateBps"`      // the affiliate's DIRECT (L1) commission rate; upline levels use platform constants
	AccruedCents  int64  `json:"accruedCents"` // lifetime commission accrued
	PaidCents     int64  `json:"paidCents"`    // lifetime commission paid out
	// Handle is the OPT-IN public leaderboard display name. Empty ⟹ NOT listed on the
	// public leaderboard by name; the affiliate's own rank stays private-visible to
	// itself. The org identity is NEVER exposed on the leaderboard — only this
	// self-chosen handle, for affiliates who opt in.
	Handle      string `json:"handle,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	ApprovedAt  int64  `json:"approvedAt"`
	SuspendedAt int64  `json:"suspendedAt"`
}

// PendingCents is the commission earned but not yet paid (never negative).
func (a Affiliate) PendingCents() int64 {
	if a.PaidCents >= a.AccruedCents {
		return 0
	}
	return a.AccruedCents - a.PaidCents
}

// AffiliateReferral is one referred_org → affiliate attribution edge — ALSO the
// referredBy graph edge. ReferredOrg is UNIQUE across the table (an org is
// attributed to at most one affiliate, ever — first-touch, i.e. referredBy is
// set-once/immutable), which is also the idempotency key for POST /v1/affiliates/
// attribute. ReferrerOrg is the attributing affiliate's own org, denormalized so the
// upline climb is a pure edge walk (referred_org → referrer_org → …) with no per-hop
// join. The recursive walk over these edges IS the multi-level upline.
type AffiliateReferral struct {
	ID          string `json:"id"`
	AffiliateID string `json:"affiliateId"`
	ReferredOrg string `json:"referredOrg"`
	ReferrerOrg string `json:"referrerOrg"`
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

// Accrual is one per-period PROFIT-SHARE event (the affiliate_event) — the derived
// share-ledger row keyed by referrer. For the source org's period it records the
// source's gross metered spend (SpendCents, the customer charge, unchanged truth),
// the MARGIN Hanzo earned on it (MarginCents = spend × the platform margin fraction),
// and the affiliate's SHARE of that margin (CommissionCents = margin × the level
// rate). The share comes OUT OF Hanzo's margin — never the customer's bill — so the
// invariant CommissionCents ≤ MarginCents holds by construction (level rate ≤ 100%).
// UNIQUE(affiliate, referred, period) makes the sweep at-most-once per period — the
// commission latch. Level is the upline distance from the source org to this
// affiliate (1=direct, 2, 3); a given (affiliate, source, period) has exactly one
// level because the graph is a forest.
type Accrual struct {
	ID              string `json:"id"`
	AffiliateID     string `json:"affiliateId"`
	ReferredOrg     string `json:"referredOrg"`
	Period          string `json:"period"`
	Level           int    `json:"level"`
	SpendCents      int64  `json:"spendCents"`  // the source org's gross charge (revenue), unchanged truth
	MarginCents     int64  `json:"marginCents"` // Hanzo's margin on that spend — the share base
	CommissionCents int64  `json:"commissionCents"`
	CreatedAt       int64  `json:"createdAt"`
}

// Link is one shareable referral link an affiliate created: a unique code (in the
// SAME global directory as an affiliate's primary code, so either resolves an
// attribution) plus a label and a click counter. The primary code is mirrored as a
// link row on approval so click tracking is uniform across every code. Signups and
// conversions are DERIVED (counted from the attribution + accrual tables by code),
// never stored, so they can never drift from the ledger.
type Link struct {
	ID          string `json:"id"`
	AffiliateID string `json:"-"`
	Code        string `json:"code"`
	Label       string `json:"label"`
	Clicks      int64  `json:"clicks"`
	CreatedAt   int64  `json:"createdAt"`
}

// PeriodEarning is one row of an affiliate's per-period share ledger: the margin base
// and the share earned in that period, summed over every referred org + level.
type PeriodEarning struct {
	Period          string `json:"period"`
	MarginCents     int64  `json:"marginCents"`
	CommissionCents int64  `json:"commissionCents"`
}

// OrgEarning is one row of an affiliate's per-referred-org contribution: AGGREGATE
// margin + share attributed to that org across all periods. It is the affiliate's OWN
// downline (orgs it referred) — aggregate only, never the referred org's raw usage.
type OrgEarning struct {
	ReferredOrg     string `json:"referredOrg"`
	MarginCents     int64  `json:"marginCents"`
	CommissionCents int64  `json:"commissionCents"`
}

// LeaderboardEntry is one ranked affiliate for the privacy-preserving leaderboard:
// the opt-in handle (never the org), the aggregate accrued share, and the referred
// count. The affiliate id is carried only so the handler can flag the caller's OWN
// row; it is never emitted for other affiliates.
type LeaderboardEntry struct {
	AffiliateID   string `json:"-"`
	Handle        string `json:"handle"`
	AccruedCents  int64  `json:"accruedCents"`
	ReferredCount int    `json:"referredCount"`
}

// Store is the affiliates database. ONE SQLite file holds every org's affiliate
// record, attribution edges, accrual events, and payouts. A code→affiliate lookup
// is a GLOBAL directory by design (a referred org presents a code minted by ANY
// affiliate); every /v1/affiliates read is scoped by the caller's org server-side.
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	db, err := cek.Open(path)
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
  owner_user     TEXT NOT NULL DEFAULT '',
  code           TEXT NOT NULL DEFAULT '',
  requested_code TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL,
  rate_bps       INTEGER NOT NULL,
  accrued_cents  INTEGER NOT NULL DEFAULT 0,
  paid_cents     INTEGER NOT NULL DEFAULT 0,
  handle         TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  approved_at    INTEGER NOT NULL DEFAULT 0,
  suspended_at   INTEGER NOT NULL DEFAULT 0
);
-- A partial UNIQUE index: only a NON-EMPTY code must be unique (many affiliates
-- can share the '' placeholder while still applied/un-coded).
CREATE UNIQUE INDEX IF NOT EXISTS ux_affiliates_code ON affiliates(code) WHERE code <> '';

-- The referredBy graph edge (org level). referred_org UNIQUE ⇒ set-once/immutable.
CREATE TABLE IF NOT EXISTS affiliate_referrals (
  id           TEXT PRIMARY KEY,
  affiliate_id TEXT NOT NULL,
  referred_org TEXT NOT NULL UNIQUE,
  referrer_org TEXT NOT NULL DEFAULT '',
  code         TEXT NOT NULL,
  created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_aff_referrals_affiliate ON affiliate_referrals(affiliate_id, created_at);
-- ix_aff_referrals_referrer is created AFTER the ADD COLUMN pass below: on a store
-- whose affiliate_referrals predates referrer_org, CREATE TABLE IF NOT EXISTS is a
-- no-op, so indexing referrer_org here would fail ("no such column") before the
-- column is added. Column first, then its index.

-- The referredBy graph edge (user level): a user's referrer is set-once/immutable
-- (referred_user PRIMARY KEY) and cycle-checked, mirroring the org edge.
CREATE TABLE IF NOT EXISTS user_referrals (
  referred_user TEXT PRIMARY KEY,
  referrer_user TEXT NOT NULL,
  code          TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_user_referrals_referrer ON user_referrals(referrer_user);

CREATE TABLE IF NOT EXISTS affiliate_accruals (
  id               TEXT PRIMARY KEY,
  affiliate_id     TEXT NOT NULL,
  referred_org     TEXT NOT NULL,
  period           TEXT NOT NULL,
  level            INTEGER NOT NULL DEFAULT 1,
  spend_cents      INTEGER NOT NULL,
  margin_cents     INTEGER NOT NULL DEFAULT 0,
  commission_cents INTEGER NOT NULL,
  created_at       INTEGER NOT NULL,
  UNIQUE(affiliate_id, referred_org, period)
);
CREATE INDEX IF NOT EXISTS ix_aff_accruals_affiliate ON affiliate_accruals(affiliate_id, created_at);

-- Shareable referral links: a code namespace SHARED with affiliates.code (either
-- resolves an attribution). The primary code is mirrored here on approval so click
-- tracking is uniform. code is globally UNIQUE across links.
CREATE TABLE IF NOT EXISTS affiliate_links (
  id           TEXT PRIMARY KEY,
  affiliate_id TEXT NOT NULL,
  code         TEXT NOT NULL UNIQUE,
  label        TEXT NOT NULL DEFAULT '',
  clicks       INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_aff_links_affiliate ON affiliate_links(affiliate_id, created_at);

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
	// Forward-only additive migrations for stores created before these columns
	// existed. ADD COLUMN is idempotent-by-intent: a duplicate-column error means the
	// column is already present, which is success.
	for _, alter := range []string{
		`ALTER TABLE affiliates ADD COLUMN owner_user TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE affiliates ADD COLUMN handle TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE affiliate_referrals ADD COLUMN referrer_org TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE affiliate_accruals ADD COLUMN level INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE affiliate_accruals ADD COLUMN margin_cents INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := s.db.Exec(alter); err != nil && !isDuplicateColumn(err) {
			return fmt.Errorf("affiliates migrate alter: %w", err)
		}
	}
	// Indexes on newly-added columns come AFTER the ADD COLUMN pass, so they are
	// valid on both a fresh store and one migrated from an older schema.
	if _, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS ix_aff_referrals_referrer ON affiliate_referrals(referrer_org)`); err != nil {
		return fmt.Errorf("affiliates migrate index: %w", err)
	}
	// Backfill referrer_org for pre-migration edges from the owning affiliate's org.
	if _, err := s.db.Exec(
		`UPDATE affiliate_referrals SET referrer_org = (
		    SELECT org FROM affiliates WHERE affiliates.id = affiliate_referrals.affiliate_id)
		  WHERE referrer_org = ''`); err != nil {
		return fmt.Errorf("affiliates migrate backfill: %w", err)
	}
	return nil
}

// isDuplicateColumn reports whether err is the SQLite "duplicate column name" error
// an ADD COLUMN raises when the column already exists (idempotent migration signal),
// matched on message text so it holds under both the cgo and pure-Go drivers.
func isDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column")
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

const affiliateCols = `id,org,owner_user,code,requested_code,status,rate_bps,accrued_cents,paid_cents,handle,created_at,approved_at,suspended_at`

func scanAffiliate(sc interface{ Scan(...any) error }) (Affiliate, error) {
	var a Affiliate
	err := sc.Scan(&a.ID, &a.Org, &a.OwnerUser, &a.Code, &a.RequestedCode, &a.Status, &a.RateBps,
		&a.AccruedCents, &a.PaidCents, &a.Handle, &a.CreatedAt, &a.ApprovedAt, &a.SuspendedAt)
	return a, err
}

// Apply enrolls org as an affiliate at status=applied with the default rate,
// idempotently. requestedCode is the optional vanity code (validated + minted at
// approval); ownerUser is the applying user (the head of this affiliate's user chain).
// A repeat apply returns the EXISTING record (first apply wins). Returns (affiliate,
// created).
func (s *Store) Apply(ctx context.Context, id, org, ownerUser, requestedCode string, rateBps int64) (Affiliate, bool, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO affiliates (id, org, owner_user, requested_code, status, rate_bps, created_at)
		 VALUES (?,?,?,?,?,?,strftime('%s','now'))`,
		id, org, ownerUser, requestedCode, StatusApplied, rateBps)
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
// un-approved affiliate has no code). It matches the affiliate's PRIMARY code first,
// then any secondary shareable link code — both live in the ONE global code
// directory. Trims + lower-cases the client-supplied code. Only an approved
// affiliate resolves (a suspended/applied affiliate's codes stop attributing).
func (s *Store) AffiliateForCode(ctx context.Context, code string) (Affiliate, error) {
	code = normalizeCode(code)
	if code == "" {
		return Affiliate{}, errUnknownCode
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+affiliateCols+` FROM affiliates WHERE code=? AND status=?`, code, StatusApproved)
	a, err := scanAffiliate(row)
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Affiliate{}, fmt.Errorf("resolve code: %w", err)
	}
	// Fall back to a secondary link code → its owning affiliate (must be approved).
	var affID string
	lerr := s.db.QueryRowContext(ctx, `SELECT affiliate_id FROM affiliate_links WHERE code=?`, code).Scan(&affID)
	if errors.Is(lerr, sql.ErrNoRows) {
		return Affiliate{}, errUnknownCode
	}
	if lerr != nil {
		return Affiliate{}, fmt.Errorf("resolve link code: %w", lerr)
	}
	owner, gerr := s.getByID(ctx, affID)
	if gerr != nil {
		return Affiliate{}, errUnknownCode
	}
	if owner.Status != StatusApproved {
		return Affiliate{}, errUnknownCode
	}
	return owner, nil
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

// Attribute records a referredOrg → affiliate edge (referrer = affiliateOrg),
// idempotently — this is the set-once referredBy edge of the upline graph. The
// referred org is the VALIDATED caller (never client-supplied). One-per-referred-org
// (UNIQUE) makes a repeat attribute a no-op returning the FIRST edge (first-touch
// wins, immutable). Self-attribution is refused; an edge that would close an upline
// loop is refused (errCycle). Returns (edge, created).
func (s *Store) Attribute(ctx context.Context, id, affiliateID, referredOrg, affiliateOrg, code string) (AffiliateReferral, bool, error) {
	if referredOrg == affiliateOrg {
		return AffiliateReferral{}, false, errSelfAttribution
	}
	// Cycle guard: if the proposed referrer (affiliateOrg) can already reach
	// referredOrg by climbing its own upline, the new edge would close a loop.
	if cyc, err := s.wouldCycleOrg(ctx, referredOrg, affiliateOrg); err != nil {
		return AffiliateReferral{}, false, err
	} else if cyc {
		return AffiliateReferral{}, false, errCycle
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO affiliate_referrals (id, affiliate_id, referred_org, referrer_org, code, created_at)
		 VALUES (?,?,?,?,?,strftime('%s','now'))`,
		id, affiliateID, referredOrg, affiliateOrg, normalizeCode(code))
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

const referralCols = `id,affiliate_id,referred_org,referrer_org,code,created_at`

func scanReferral(sc interface{ Scan(...any) error }) (AffiliateReferral, error) {
	var r AffiliateReferral
	err := sc.Scan(&r.ID, &r.AffiliateID, &r.ReferredOrg, &r.ReferrerOrg, &r.Code, &r.CreatedAt)
	return r, err
}

// ── the referredBy graph (upline walk + cycle guard) ───────────────────────────

// referrerOrgOf returns the org that referred `org` (its direct upline), or ("",
// false) when org has no referredBy edge (it is a root).
func (s *Store) referrerOrgOf(ctx context.Context, org string) (string, bool, error) {
	var r string
	err := s.db.QueryRowContext(ctx, `SELECT referrer_org FROM affiliate_referrals WHERE referred_org=?`, org).Scan(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("referrer of %q: %w", org, err)
	}
	return r, r != "", nil
}

// UplineOrgs returns the ancestors of sourceOrg by climbing referredBy edges, up to
// `depth` levels — index 0 is the direct (L1) referrer, index 1 its referrer (L2),
// etc. The climb stops at a root, at `depth`, or on a revisited node (forest-safety).
func (s *Store) UplineOrgs(ctx context.Context, sourceOrg string, depth int) ([]string, error) {
	out := make([]string, 0, depth)
	seen := map[string]bool{sourceOrg: true}
	cur := sourceOrg
	for len(out) < depth {
		r, ok, err := s.referrerOrgOf(ctx, cur)
		if err != nil {
			return nil, err
		}
		if !ok || seen[r] {
			break
		}
		out = append(out, r)
		seen[r] = true
		cur = r
	}
	return out, nil
}

// wouldCycleOrg reports whether adding the edge referred→referrer would close a loop:
// it climbs referrer's existing upline and returns true if it reaches referred.
func (s *Store) wouldCycleOrg(ctx context.Context, referred, referrer string) (bool, error) {
	seen := map[string]bool{}
	cur := referrer
	for i := 0; i < walkCap; i++ {
		if cur == referred {
			return true, nil
		}
		if seen[cur] {
			return false, nil // pre-existing loop guard — stop, don't spin
		}
		seen[cur] = true
		r, ok, err := s.referrerOrgOf(ctx, cur)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		cur = r
	}
	return false, nil
}

// DownlineByLevel walks DOWN from ancestorOrg over referredBy edges to `depth`,
// returning source org → its level below ancestorOrg (1=direct child, 2, 3). This is
// the per-affiliate accrual set for the lazy dashboard sweep (mirror of UplineOrgs).
func (s *Store) DownlineByLevel(ctx context.Context, ancestorOrg string, depth int) (map[string]int, error) {
	levels := make(map[string]int)
	frontier := []string{ancestorOrg}
	seen := map[string]bool{ancestorOrg: true}
	for level := 1; level <= depth && len(frontier) > 0; level++ {
		next := make([]string, 0, len(frontier))
		for _, parent := range frontier {
			kids, err := s.childrenOf(ctx, parent)
			if err != nil {
				return nil, err
			}
			for _, k := range kids {
				if seen[k] {
					continue
				}
				seen[k] = true
				levels[k] = level
				next = append(next, k)
			}
		}
		frontier = next
	}
	return levels, nil
}

// childrenOf returns the orgs directly referred by referrerOrg (its L1 downline).
func (s *Store) childrenOf(ctx context.Context, referrerOrg string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT referred_org FROM affiliate_referrals WHERE referrer_org=?`, referrerOrg)
	if err != nil {
		return nil, fmt.Errorf("children of %q: %w", referrerOrg, err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0, 8)
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// AllReferredOrgs returns every org that has a referredBy edge (the source set the
// admin sweep folds over), oldest-first, bounded.
func (s *Store) AllReferredOrgs(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT referred_org FROM affiliate_referrals ORDER BY created_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("all referred orgs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0, 32)
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ── the referredBy graph (user level: set-once + cycle-checked) ─────────────────

// SetUserReferrer records referredUser → referrerUser, set-once (PRIMARY KEY) and
// cycle-checked (mirrors the org edge). Returns created=false when the user already
// has a referrer (immutable — first wins) or the pair is a self/cycle no-op signalled
// by the error. errSelfAttribution when referred==referrer; errCycle on a loop.
func (s *Store) SetUserReferrer(ctx context.Context, referredUser, referrerUser, code string) (bool, error) {
	if referredUser == "" || referrerUser == "" {
		return false, nil // nothing to link (anonymous) — best-effort no-op
	}
	if referredUser == referrerUser {
		return false, errSelfAttribution
	}
	if cyc, err := s.wouldCycleUser(ctx, referredUser, referrerUser); err != nil {
		return false, err
	} else if cyc {
		return false, errCycle
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_referrals (referred_user, referrer_user, code, created_at)
		 VALUES (?,?,?,strftime('%s','now'))`,
		referredUser, referrerUser, normalizeCode(code))
	if err == nil {
		return true, nil
	}
	if isUnique(err) {
		return false, nil // already has a referrer — immutable, first-touch wins
	}
	return false, fmt.Errorf("set user referrer: %w", err)
}

func (s *Store) referrerUserOf(ctx context.Context, user string) (string, bool, error) {
	var r string
	err := s.db.QueryRowContext(ctx, `SELECT referrer_user FROM user_referrals WHERE referred_user=?`, user).Scan(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("referrer of user %q: %w", user, err)
	}
	return r, r != "", nil
}

// UplineUsers returns a user's ancestors up to `depth` (index 0 = direct referrer).
func (s *Store) UplineUsers(ctx context.Context, user string, depth int) ([]string, error) {
	out := make([]string, 0, depth)
	seen := map[string]bool{user: true}
	cur := user
	for len(out) < depth {
		r, ok, err := s.referrerUserOf(ctx, cur)
		if err != nil {
			return nil, err
		}
		if !ok || seen[r] {
			break
		}
		out = append(out, r)
		seen[r] = true
		cur = r
	}
	return out, nil
}

func (s *Store) wouldCycleUser(ctx context.Context, referred, referrer string) (bool, error) {
	seen := map[string]bool{}
	cur := referrer
	for i := 0; i < walkCap; i++ {
		if cur == referred {
			return true, nil
		}
		if seen[cur] {
			return false, nil
		}
		seen[cur] = true
		r, ok, err := s.referrerUserOf(ctx, cur)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		cur = r
	}
	return false, nil
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

// ── analytics (the /v1/admin/referrals cross-tenant board) ─────────────────────

// ReferredOrgCounts returns the total number of distinct referred orgs and the
// number that have CONVERTED (produced at least one positive commission accrual) —
// the conversion numerator/denominator for the admin analytics board.
func (s *Store) ReferredOrgCounts(ctx context.Context) (total, converted int, err error) {
	if err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM affiliate_referrals`).Scan(&total); err != nil {
		return 0, 0, fmt.Errorf("count referred orgs: %w", err)
	}
	if err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT referred_org) FROM affiliate_accruals WHERE commission_cents > 0`).Scan(&converted); err != nil {
		return 0, 0, fmt.Errorf("count converted orgs: %w", err)
	}
	return total, converted, nil
}

// AccruedByLevel returns level → total commission accrued at that upline level, the
// analytics breakdown of platform commission liability by depth.
func (s *Store) AccruedByLevel(ctx context.Context) (map[int]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT level, COALESCE(SUM(commission_cents),0) FROM affiliate_accruals GROUP BY level`)
	if err != nil {
		return nil, fmt.Errorf("accrued by level: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int]int64)
	for rows.Next() {
		var level int
		var cents int64
		if err := rows.Scan(&level, &cents); err != nil {
			return nil, err
		}
		out[level] = cents
	}
	return out, rows.Err()
}

// ── accrual (create-or-top-up, converges to the period's month-end spend) ──────

// bump moves an affiliate's accrued balance by delta cents inside tx (0 is a no-op).
// The one place accrued_cents is advanced by an accrual, so the balance and the accrual
// rows move together atomically.
func bump(ctx context.Context, tx *sql.Tx, affiliateID string, delta int64) error {
	if delta == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE affiliates SET accrued_cents = accrued_cents + ? WHERE id=?`, delta, affiliateID); err != nil {
		return fmt.Errorf("accrue balance: %w", err)
	}
	return nil
}

// Accrue records — or, for the still-open current period, TOPS UP — the one accrual for
// (affiliate, referredOrg, period) and moves the affiliate's accrued balance by the
// change, in a single transaction. UNIQUE(affiliate, referred, period) is the latch key.
//
// Why top-up: the period key is monthly and the source spend is MONTH-TO-DATE (commerce's
// usage rollup), so one period is swept many times as the month fills in. The FIRST sweep
// inserts the row; each later sweep in the SAME period recomputes the share from the
// current (higher) month-to-date spend and SETS the row to it, adding only the positive
// delta to accrued_cents. The share therefore CONVERGES to the true month-end value
// instead of freezing at whatever partial spend the first sweep happened to read.
//
// Monotone + bounded by construction:
//   - month-to-date spend never decreases, so a later commissionCents is never smaller; a
//     lower-or-equal recompute (an unchanged, late, or corrected reading) is a NO-OP, so
//     accrued_cents only ever RISES and never overshoots the month-end value (each reading
//     ≤ the final month-to-date).
//   - the row always stores margin + commission from the SAME spend reading, so the
//     per-event invariant commissionCents ≤ marginCents holds at EVERY step, and the level
//     schedule cap keeps Σ(commission) over the levels ≤ that source's margin — the money
//     invariant is untouched by the top-up.
//
// Returns moved=true when THIS call changed the balance (an insert or a top-up), so the
// caller counts real accrual movements and writes one audit row per movement. level is the
// upline distance recorded for analytics (1=direct, 2, 3), fixed per (affiliate, source)
// by the forest, so only the spend-derived columns move on a top-up.
func (s *Store) Accrue(ctx context.Context, accrualID, affiliateID, referredOrg, period string, level int, spendCents, marginCents, commissionCents, now int64) (moved bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("accrual tx: %w", err)
	}
	var existing int64
	err = tx.QueryRowContext(ctx,
		`SELECT commission_cents FROM affiliate_accruals WHERE affiliate_id=? AND referred_org=? AND period=?`,
		affiliateID, referredOrg, period).Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// First sweep of this period → insert the accrual and add the full share.
		if _, ierr := tx.ExecContext(ctx,
			`INSERT INTO affiliate_accruals (id, affiliate_id, referred_org, period, level, spend_cents, margin_cents, commission_cents, created_at)
			 VALUES (?,?,?,?,?,?,?,?,?)`,
			accrualID, affiliateID, referredOrg, period, level, spendCents, marginCents, commissionCents, now); ierr != nil {
			_ = tx.Rollback()
			if isUnique(ierr) {
				return false, nil // lost an insert race — the winner already accrued it
			}
			return false, fmt.Errorf("insert accrual: %w", ierr)
		}
		if berr := bump(ctx, tx, affiliateID, commissionCents); berr != nil {
			_ = tx.Rollback()
			return false, berr
		}
	case err != nil:
		_ = tx.Rollback()
		return false, fmt.Errorf("read accrual: %w", err)
	default:
		// Period already open → top up toward the current (higher) share only. A lower or
		// equal recompute leaves the row untouched, so accrued_cents never decreases.
		if commissionCents <= existing {
			_ = tx.Rollback()
			return false, nil
		}
		if _, uerr := tx.ExecContext(ctx,
			`UPDATE affiliate_accruals SET spend_cents=?, margin_cents=?, commission_cents=?
			  WHERE affiliate_id=? AND referred_org=? AND period=?`,
			spendCents, marginCents, commissionCents, affiliateID, referredOrg, period); uerr != nil {
			_ = tx.Rollback()
			return false, fmt.Errorf("top up accrual: %w", uerr)
		}
		if berr := bump(ctx, tx, affiliateID, commissionCents-existing); berr != nil {
			_ = tx.Rollback()
			return false, berr
		}
	}
	if cerr := tx.Commit(); cerr != nil {
		return false, fmt.Errorf("accrual commit: %w", cerr)
	}
	return true, nil
}

const accrualCols = `id,affiliate_id,referred_org,period,level,spend_cents,margin_cents,commission_cents,created_at`

func scanAccrual(sc interface{ Scan(...any) error }) (Accrual, error) {
	var a Accrual
	err := sc.Scan(&a.ID, &a.AffiliateID, &a.ReferredOrg, &a.Period, &a.Level,
		&a.SpendCents, &a.MarginCents, &a.CommissionCents, &a.CreatedAt)
	return a, err
}

// AccrualsForSource returns every accrual generated by ONE source org's spend, across
// the upline levels — the per-event share-ledger rows for that source. It is the
// drill-down that proves the invariant Σ(commission) ≤ margin for a source, and each
// row's MarginCents is the same source margin base. Ordered by level.
func (s *Store) AccrualsForSource(ctx context.Context, referredOrg string) ([]Accrual, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+accrualCols+` FROM affiliate_accruals WHERE referred_org=? ORDER BY level ASC, created_at ASC`, referredOrg)
	if err != nil {
		return nil, fmt.Errorf("accruals for source: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Accrual, 0, maxDepthCap)
	for rows.Next() {
		a, err := scanAccrual(rows)
		if err != nil {
			return nil, fmt.Errorf("scan accrual: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// maxDepthCap bounds the initial slice for a source's per-level accruals (the handler
// caps the real upline depth; this is only a make() hint).
const maxDepthCap = 8

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

// ── self-service profile + admin rate ──────────────────────────────────────────

// SetHandle sets an affiliate's opt-in public leaderboard handle (empty clears it,
// removing the affiliate from the public board by name). Returns the refreshed row.
func (s *Store) SetHandle(ctx context.Context, id, handle string) (Affiliate, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE affiliates SET handle=? WHERE id=?`, handle, id)
	if err != nil {
		return Affiliate{}, fmt.Errorf("set handle: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Affiliate{}, errNotFound
	}
	return s.getByID(ctx, id)
}

// SetRate sets an affiliate's DIRECT (L1) commission rate in basis points. The admin
// handler validates the range (it must leave headroom for the L2/L3 upline so the
// per-event share can never exceed the margin); the store persists it.
func (s *Store) SetRate(ctx context.Context, id string, rateBps int64) (Affiliate, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE affiliates SET rate_bps=? WHERE id=?`, rateBps, id)
	if err != nil {
		return Affiliate{}, fmt.Errorf("set rate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Affiliate{}, errNotFound
	}
	return s.getByID(ctx, id)
}

// ── shareable links ─────────────────────────────────────────────────────────────

const linkCols = `id,affiliate_id,code,label,clicks,created_at`

func scanLink(sc interface{ Scan(...any) error }) (Link, error) {
	var l Link
	err := sc.Scan(&l.ID, &l.AffiliateID, &l.Code, &l.Label, &l.Clicks, &l.CreatedAt)
	return l, err
}

// CreateLink records a new shareable link. The code must be valid + free across the
// ONE global directory (no affiliate primary code, no other link). errInvalidCode on
// a malformed code, errCodeTaken on a collision. Returns the created row.
func (s *Store) CreateLink(ctx context.Context, id, affiliateID, code, label string, now int64) (Link, error) {
	code = normalizeCode(code)
	if !validCode(code) {
		return Link{}, errInvalidCode
	}
	var exists string
	switch err := s.db.QueryRowContext(ctx, `SELECT id FROM affiliates WHERE code=?`, code).Scan(&exists); {
	case err == nil:
		return Link{}, errCodeTaken // collides with an affiliate's primary code
	case !errors.Is(err, sql.ErrNoRows):
		return Link{}, fmt.Errorf("check primary code: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO affiliate_links (id, affiliate_id, code, label, created_at) VALUES (?,?,?,?,?)`,
		id, affiliateID, code, label, now)
	if err != nil {
		if isUnique(err) {
			return Link{}, errCodeTaken
		}
		return Link{}, fmt.Errorf("create link: %w", err)
	}
	return Link{ID: id, AffiliateID: affiliateID, Code: code, Label: label, CreatedAt: now}, nil
}

// EnsureLink idempotently mirrors an affiliate's PRIMARY code as a link row (called on
// approval so click tracking is uniform across every code). A pre-existing code is a
// silent no-op — never an error.
func (s *Store) EnsureLink(ctx context.Context, id, affiliateID, code, label string, now int64) error {
	code = normalizeCode(code)
	if code == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO affiliate_links (id, affiliate_id, code, label, created_at) VALUES (?,?,?,?,?)`,
		id, affiliateID, code, label, now)
	if err != nil && !isUnique(err) {
		return fmt.Errorf("ensure link: %w", err)
	}
	return nil
}

// ListLinks returns an affiliate's links, primary first (created_at ASC), bounded.
func (s *Store) ListLinks(ctx context.Context, affiliateID string, limit int) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+linkCols+` FROM affiliate_links WHERE affiliate_id=? ORDER BY created_at ASC LIMIT ?`, affiliateID, limit)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Link, 0, 8)
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, fmt.Errorf("scan link: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CountLinks returns how many links an affiliate has (the per-affiliate cap guard).
func (s *Store) CountLinks(ctx context.Context, affiliateID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM affiliate_links WHERE affiliate_id=?`, affiliateID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count links: %w", err)
	}
	return n, nil
}

// FlushClicks folds a batch of coalesced click tallies (code → count) into
// affiliate_links in ONE transaction (clicks += n per code); an unknown code no-ops.
// This is the ONLY path that writes the vanity click counter to the money DB, and it is
// batched + read/shutdown-driven (never per-click), so a public click flood can never
// contend with the accrual/payout write path. Clicks are never read by any accrual or
// payout, so a tally lost on a flush error or a crash is harmless.
func (s *Store) FlushClicks(ctx context.Context, tally map[string]int64) error {
	if len(tally) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("clicks tx: %w", err)
	}
	for code, n := range tally {
		if n <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE affiliate_links SET clicks=clicks+? WHERE code=?`, n, code); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("flush clicks: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("clicks commit: %w", err)
	}
	return nil
}

// SignupsByCode returns code → count of orgs THIS affiliate attributed with that code.
// Scoped by affiliate_id (the leading bound predicate).
func (s *Store) SignupsByCode(ctx context.Context, affiliateID string) (map[string]int, error) {
	return s.countByCode(ctx,
		`SELECT code, COUNT(*) FROM affiliate_referrals WHERE affiliate_id=? GROUP BY code`, affiliateID)
}

// ConversionsByCode returns code → count of DISTINCT referred orgs (attributed with
// that code) that produced positive commission for THIS affiliate. Scoped by
// affiliate_id (the leading bound predicate).
func (s *Store) ConversionsByCode(ctx context.Context, affiliateID string) (map[string]int, error) {
	return s.countByCode(ctx,
		`SELECT ar.code, COUNT(DISTINCT ar.referred_org)
		   FROM affiliate_referrals ar
		  WHERE ar.affiliate_id=?
		    AND EXISTS (SELECT 1 FROM affiliate_accruals acc
		                 WHERE acc.affiliate_id = ar.affiliate_id
		                   AND acc.referred_org = ar.referred_org
		                   AND acc.commission_cents > 0)
		  GROUP BY ar.code`, affiliateID)
}

func (s *Store) countByCode(ctx context.Context, q, affiliateID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, q, affiliateID)
	if err != nil {
		return nil, fmt.Errorf("count by code: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int)
	for rows.Next() {
		var code string
		var n int
		if err := rows.Scan(&code, &n); err != nil {
			return nil, fmt.Errorf("scan count by code: %w", err)
		}
		out[normalizeCode(code)] = n
	}
	return out, rows.Err()
}

// ── earnings (the per-affiliate share-ledger projection) ────────────────────────

// EarningsByPeriod returns an affiliate's per-period share ledger (margin base +
// share earned), newest period first, bounded. Scoped by affiliate_id (leading bound).
func (s *Store) EarningsByPeriod(ctx context.Context, affiliateID string, limit int) ([]PeriodEarning, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT period, COALESCE(SUM(margin_cents),0), COALESCE(SUM(commission_cents),0)
		   FROM affiliate_accruals WHERE affiliate_id=? GROUP BY period ORDER BY period DESC LIMIT ?`,
		affiliateID, limit)
	if err != nil {
		return nil, fmt.Errorf("earnings by period: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]PeriodEarning, 0, 16)
	for rows.Next() {
		var e PeriodEarning
		if err := rows.Scan(&e.Period, &e.MarginCents, &e.CommissionCents); err != nil {
			return nil, fmt.Errorf("scan period earning: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EarningsByReferredOrg returns the AGGREGATE margin + share an affiliate earned per
// org it DIRECTLY referred (level 1), largest share first, bounded. Restricting to the
// direct level means the breakdown never exposes a sub-downline org's identity to an
// upline affiliate that did not refer it (deeper-level earnings still count in the
// period + lifetime totals). Aggregate totals only — never the referred org's raw
// usage. Scoped by affiliate_id (leading bound predicate).
func (s *Store) EarningsByReferredOrg(ctx context.Context, affiliateID string, limit int) ([]OrgEarning, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT referred_org, COALESCE(SUM(margin_cents),0), COALESCE(SUM(commission_cents),0)
		   FROM affiliate_accruals WHERE affiliate_id=? AND level=1 GROUP BY referred_org
		  ORDER BY SUM(commission_cents) DESC, referred_org ASC LIMIT ?`,
		affiliateID, limit)
	if err != nil {
		return nil, fmt.Errorf("earnings by org: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]OrgEarning, 0, 16)
	for rows.Next() {
		var e OrgEarning
		if err := rows.Scan(&e.ReferredOrg, &e.MarginCents, &e.CommissionCents); err != nil {
			return nil, fmt.Errorf("scan org earning: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── leaderboard (privacy-preserving) ────────────────────────────────────────────

// LeaderboardTop returns the top approved affiliates by lifetime accrued share
// (descending, id tiebreak) with handle + referred count, bounded. The handler
// applies the opt-in privacy filter (only handled rows are public) and flags the
// caller's own row. NEVER exposes org identity.
func (s *Store) LeaderboardTop(ctx context.Context, limit int) ([]LeaderboardEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.id, a.handle, a.accrued_cents, COUNT(r.id)
		   FROM affiliates a LEFT JOIN affiliate_referrals r ON r.affiliate_id = a.id
		  WHERE a.status=?
		  GROUP BY a.id, a.handle, a.accrued_cents
		  ORDER BY a.accrued_cents DESC, a.id ASC LIMIT ?`,
		StatusApproved, limit)
	if err != nil {
		return nil, fmt.Errorf("leaderboard: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]LeaderboardEntry, 0, 32)
	for rows.Next() {
		var e LeaderboardEntry
		if err := rows.Scan(&e.AffiliateID, &e.Handle, &e.AccruedCents, &e.ReferredCount); err != nil {
			return nil, fmt.Errorf("scan leaderboard: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RankOf returns the exact 1-based rank of an affiliate among all APPROVED affiliates
// by accrued share (rank 1 = highest), plus the total approved count — computed over
// the WHOLE set (not a truncated list), so the caller's own rank is always accurate.
// Ties break by id (a stable, deterministic order). Returns (0,total,nil) if the
// affiliate is not approved (no rank).
func (s *Store) RankOf(ctx context.Context, id string, accruedCents int64) (rank, total int, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM affiliates WHERE status=?`, StatusApproved).Scan(&total); err != nil {
		return 0, 0, fmt.Errorf("leaderboard total: %w", err)
	}
	var ahead int
	if err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM affiliates
		  WHERE status=? AND id<>? AND (accrued_cents > ? OR (accrued_cents = ? AND id < ?))`,
		StatusApproved, id, accruedCents, accruedCents, id).Scan(&ahead); err != nil {
		return 0, total, fmt.Errorf("leaderboard rank: %w", err)
	}
	return ahead + 1, total, nil
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
