package referrals

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	// The ONE Hanzo SQLite driver (registers "sqlite" under both build tags).
	// Mirrors clients/crm / clients/prompts — one storage pattern.
	"github.com/hanzoai/cloud/internal/cek"
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors mapped to HTTP status by the handlers:
//
//	errNotFound → 404, errSelfReferral → 400, errUnknownCode → 404.
var (
	errNotFound     = errors.New("referrals: not found")
	errSelfReferral = errors.New("referrals: cannot refer yourself")
	errUnknownCode  = errors.New("referrals: unknown referral code")
)

// Status values. A referral advances signed_up → qualified → credited. It never
// moves backward; credited is terminal (the bonus was granted, once).
const (
	StatusSignedUp  = "signed_up"
	StatusQualified = "qualified"
	StatusCredited  = "credited"
)

// Referral is one referrer↔referee edge. RefereeOrg is UNIQUE across the table —
// an org can be referred at most once, ever (first-touch attribution), which is
// also the idempotency key for POST /v1/referrals/claim.
type Referral struct {
	ID                 string `json:"id"`
	ReferrerOrg        string `json:"-"`    // hidden: the code owner; never leak the other side's org to a referee
	RefereeOrg         string `json:"-"`    // hidden for the same reason (admin view re-exposes both)
	Code               string `json:"code"` // the referrer code used at claim
	Status             string `json:"status"`
	ReferrerGrantCents int64  `json:"referrerGrantCents"`
	RefereeGrantCents  int64  `json:"refereeGrantCents"`
	ReferrerTxn        string `json:"-"`
	RefereeTxn         string `json:"-"`
	CreatedAt          int64  `json:"createdAt"`
	QualifiedAt        int64  `json:"qualifiedAt"`
	CreditedAt         int64  `json:"creditedAt"`
}

// Store is the referrals database. ONE SQLite file holds every org's codes +
// referral edges; there is no per-org WHERE on the codes table (a code→org
// lookup is a GLOBAL directory by design — the referee presents a code minted by
// ANY org), while every /v1/referrals read is scoped by referrer_org server-side.
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
CREATE TABLE IF NOT EXISTS referral_codes (
  code       TEXT PRIMARY KEY,
  org        TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS referrals (
  id                   TEXT PRIMARY KEY,
  referrer_org         TEXT NOT NULL,
  referee_org          TEXT NOT NULL UNIQUE,
  code                 TEXT NOT NULL,
  status               TEXT NOT NULL,
  referrer_grant_cents INTEGER NOT NULL DEFAULT 0,
  referee_grant_cents  INTEGER NOT NULL DEFAULT 0,
  referrer_txn         TEXT NOT NULL DEFAULT '',
  referee_txn          TEXT NOT NULL DEFAULT '',
  created_at           INTEGER NOT NULL,
  qualified_at         INTEGER NOT NULL DEFAULT 0,
  credited_at          INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_referrals_referrer ON referrals(referrer_org, created_at);
CREATE INDEX IF NOT EXISTS ix_referrals_status   ON referrals(status);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("referrals migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// codeEncoding: RFC-4648 base32 without padding. 5 bytes → exactly 8 chars from
// the A-Z2-7 alphabet — uppercase, URL-safe, no ambiguous separators.
var codeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// deriveCode is the DETERMINISTIC short slug for an org: base32 of the first 5
// bytes of SHA-256("hanzo-referral:"+org). Same org ⇒ same code, always — so the
// code is stable and never needs regenerating. `n` disambiguates the vanishingly
// rare cross-org collision (deriveCode(org,0) taken by another org): re-hash with
// a salt suffix rather than hand a second org the same code.
func deriveCode(org string, n int) string {
	seed := "hanzo-referral:" + org
	if n > 0 {
		seed = fmt.Sprintf("%s#%d", seed, n)
	}
	sum := sha256.Sum256([]byte(seed))
	return codeEncoding.EncodeToString(sum[:5])
}

// EnsureCode returns the org's stable referral code, minting (and persisting) it
// on first use. Deterministic derivation + a persisted directory row give BOTH a
// stable code AND an O(1) reverse lookup (OrgForCode), with collision safety.
func (s *Store) EnsureCode(ctx context.Context, org string) (string, error) {
	var code string
	err := s.db.QueryRowContext(ctx, `SELECT code FROM referral_codes WHERE org=?`, org).Scan(&code)
	if err == nil {
		return code, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("lookup code: %w", err)
	}
	// Mint. Retry on the (astronomically unlikely) code collision with another org.
	for n := 0; n < 8; n++ {
		cand := deriveCode(org, n)
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO referral_codes (code, org, created_at) VALUES (?,?,strftime('%s','now'))`, cand, org)
		if err == nil {
			return cand, nil
		}
		// A concurrent minter for the SAME org won the race → read theirs.
		if e := s.db.QueryRowContext(ctx, `SELECT code FROM referral_codes WHERE org=?`, org).Scan(&code); e == nil {
			return code, nil
		}
		// Else it was a code collision with a DIFFERENT org → try the next salt.
		if !isUnique(err) {
			return "", fmt.Errorf("mint code: %w", err)
		}
	}
	return "", fmt.Errorf("mint code: exhausted collision retries for %q", org)
}

// OrgForCode reverse-resolves a referral code to its owning org. Trims + upper-
// cases the client-supplied code so a link pasted in any case still resolves.
func (s *Store) OrgForCode(ctx context.Context, code string) (string, error) {
	code = normalizeCode(code)
	if code == "" {
		return "", errUnknownCode
	}
	var org string
	err := s.db.QueryRowContext(ctx, `SELECT org FROM referral_codes WHERE code=?`, code).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errUnknownCode
	}
	if err != nil {
		return "", fmt.Errorf("resolve code: %w", err)
	}
	return org, nil
}

// Claim records a referrer↔referee edge, idempotently. The referee is the
// VALIDATED caller (never client-supplied). One-per-referee (UNIQUE) makes a
// repeat claim — with the same OR a different code — a no-op that returns the
// FIRST recorded referral (first-touch attribution wins). Self-referral is
// refused. Returns (referral, created).
func (s *Store) Claim(ctx context.Context, id, referrerOrg, refereeOrg, code string) (Referral, bool, error) {
	if referrerOrg == refereeOrg {
		return Referral{}, false, errSelfReferral
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO referrals (id, referrer_org, referee_org, code, status, created_at)
		 VALUES (?,?,?,?,?,strftime('%s','now'))`,
		id, referrerOrg, refereeOrg, normalizeCode(code), StatusSignedUp)
	if err == nil {
		r, gerr := s.get(ctx, id)
		return r, true, gerr
	}
	if isUnique(err) {
		// The referee was already referred — idempotent: return the existing edge.
		r, gerr := s.getByReferee(ctx, refereeOrg)
		return r, false, gerr
	}
	return Referral{}, false, fmt.Errorf("claim: %w", err)
}

// LatchCredit atomically CLAIMS the one-and-only grant for a referral: it sets
// credited_at (+ status=credited, grant amounts, qualified_at if unset) ONLY when
// credited_at is still 0. RowsAffected==1 means THIS caller won the race and must
// perform the deposits; 0 means another sweep already granted (never double-pay).
// credited_at is the idempotency latch — the money is guaranteed at-most-once.
func (s *Store) LatchCredit(ctx context.Context, id string, referrerCents, refereeCents, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE referrals
		    SET status='credited',
		        qualified_at = CASE WHEN qualified_at=0 THEN ? ELSE qualified_at END,
		        credited_at  = ?,
		        referrer_grant_cents = ?,
		        referee_grant_cents  = ?
		  WHERE id=? AND credited_at=0`,
		now, now, referrerCents, refereeCents, id)
	if err != nil {
		return false, fmt.Errorf("latch credit: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetTxns records the two ledger transaction ids after the deposits land (best-
// effort receipt; the latch, not this, is the idempotency authority).
func (s *Store) SetTxns(ctx context.Context, id, referrerTxn, refereeTxn string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE referrals SET referrer_txn=?, referee_txn=? WHERE id=?`, referrerTxn, refereeTxn, id)
	if err != nil {
		return fmt.Errorf("set txns: %w", err)
	}
	return nil
}

const referralCols = `id,referrer_org,referee_org,code,status,referrer_grant_cents,referee_grant_cents,referrer_txn,referee_txn,created_at,qualified_at,credited_at`

func scanReferral(sc interface{ Scan(...any) error }) (Referral, error) {
	var r Referral
	err := sc.Scan(&r.ID, &r.ReferrerOrg, &r.RefereeOrg, &r.Code, &r.Status,
		&r.ReferrerGrantCents, &r.RefereeGrantCents, &r.ReferrerTxn, &r.RefereeTxn,
		&r.CreatedAt, &r.QualifiedAt, &r.CreditedAt)
	return r, err
}

func (s *Store) get(ctx context.Context, id string) (Referral, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+referralCols+` FROM referrals WHERE id=?`, id)
	r, err := scanReferral(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Referral{}, errNotFound
	}
	if err != nil {
		return Referral{}, fmt.Errorf("get referral: %w", err)
	}
	return r, nil
}

func (s *Store) getByReferee(ctx context.Context, refereeOrg string) (Referral, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+referralCols+` FROM referrals WHERE referee_org=?`, refereeOrg)
	r, err := scanReferral(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Referral{}, errNotFound
	}
	if err != nil {
		return Referral{}, fmt.Errorf("get referral by referee: %w", err)
	}
	return r, nil
}

// Get re-reads a referral by id (post-grant refresh).
func (s *Store) Get(ctx context.Context, id string) (Referral, error) { return s.get(ctx, id) }

// ListByReferrer returns an org's referrals (the people IT referred), newest
// first, bounded by limit.
func (s *Store) ListByReferrer(ctx context.Context, referrerOrg string, limit int) ([]Referral, error) {
	return s.query(ctx, `SELECT `+referralCols+` FROM referrals WHERE referrer_org=? ORDER BY created_at DESC LIMIT ?`, referrerOrg, limit)
}

// ListPending returns referrals still awaiting the qualify check (status
// signed_up), oldest first, bounded — the sweep + the lazy-on-read check fold
// over this set. optReferrer scopes to one referrer ("" = all, the admin sweep).
func (s *Store) ListPending(ctx context.Context, optReferrer string, limit int) ([]Referral, error) {
	if optReferrer == "" {
		return s.query(ctx, `SELECT `+referralCols+` FROM referrals WHERE status=? ORDER BY created_at ASC LIMIT ?`, StatusSignedUp, limit)
	}
	return s.query(ctx, `SELECT `+referralCols+` FROM referrals WHERE status=? AND referrer_org=? ORDER BY created_at ASC LIMIT ?`, StatusSignedUp, optReferrer, limit)
}

// ListAll returns every referral newest-first (the admin directory), bounded.
func (s *Store) ListAll(ctx context.Context, limit int) ([]Referral, error) {
	return s.query(ctx, `SELECT `+referralCols+` FROM referrals ORDER BY created_at DESC LIMIT ?`, limit)
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Referral, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list referrals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Referral, 0, 16)
	for rows.Next() {
		r, err := scanReferral(rows)
		if err != nil {
			return nil, fmt.Errorf("scan referral: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// normalizeCode trims + upper-cases a code so a link works in any case; the
// base32 alphabet is uppercase, so this is lossless for a real code.
func normalizeCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// isUnique reports whether err is a SQLite UNIQUE/PRIMARY-KEY constraint
// violation (the idempotency + collision signal), matched on message text so it
// holds under BOTH the cgo and pure-Go drivers.
func isUnique(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique") || strings.Contains(m, "constraint failed") || strings.Contains(m, "primary key")
}
