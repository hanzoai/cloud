package referral

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"

	// locus.Use is the ONE opener (it moves the store off its file first, when asked): on hanzo-sql it is the store's schema, and
	// otherwise the SQLite file is born encrypted under the key cek derives from the
	// process master and the system namespace, and comes back with the
	// single-connection cap already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// The ONE Hanzo SQLite driver (registers "sqlite" under both build tags).
	// Mirrors clients/crm / clients/prompts — one storage pattern.
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors mapped to HTTP status by the handlers:
//
//	errNotFound → 404, errSelfReferral → 400, errUnknownCode → 404.
var (
	errNotFound     = errors.New("referrals: not found")
	errSelfReferral = errors.New("referrals: cannot refer yourself")
	errUnknownCode  = errors.New("referrals: unknown referral code")
	errCodeTaken    = errors.New("referrals: referral code already taken")
	errInvalidCode  = errors.New("referrals: referral code must be 2-32 letters, numbers, hyphens or underscores")
)

// Status values. A referral advances signup → qualified. It never moves backward;
// qualified is terminal (the referee became a real customer, once).
//
// There is deliberately no third state: the old "credited" existed only to record
// that this package had minted platform credit, and it does not mint anything.
const (
	StatusSignup    = "signup"
	StatusQualified = "qualified"
)

// Referral is one referrer↔referee edge. RefereeOrg is UNIQUE across the table —
// an org can be referred at most once, ever (first-touch attribution), which is
// also the idempotency key for POST /v1/referral/claim.
//
// It holds attribution and nothing else. No amount, no ledger transaction: what a
// qualified referral is WORTH is an affiliate payable in commerce, and pricing it
// here would be a second answer to a question that already has one.
type Referral struct {
	ID          string `json:"id"`
	ReferrerOrg string `json:"-"`    // hidden: the code owner; never leak the other side's org to a referee
	RefereeOrg  string `json:"-"`    // hidden for the same reason (admin view re-exposes both)
	Code        string `json:"code"` // the referrer code used at claim
	Status      string `json:"status"`
	CreatedAt   int64  `json:"createdAt"`
	QualifiedAt int64  `json:"qualifiedAt"`
}

// Store is the referrals database. ONE database — a SQLite file or a schema on
// hanzo-sql — holds every org's codes + referral edges; there is no per-org WHERE
// on the codes table (a code→org lookup is a GLOBAL directory by design — the
// referee presents a code minted by ANY org), while every /v1/referral read is
// scoped by referrer_org server-side.
//
// Its SQL is the dialect both engines share (sdk/dialect) and its tests run
// on both (internal/sqltest).
type Store struct {
	db *sql.DB
	d  dialect.Dialect
}

func openStore(dir string) (*Store, error) {
	s, err := locus.Use(context.Background(), nil, namespace.System(), "referrals", dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db, d: dialect.Of(db)}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.d.Migrate(ctx, db, func(tx *sql.Tx) error { return s.migrate(ctx, tx) }); err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open referrals store: %w", err)
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS referral_codes (
  code       TEXT PRIMARY KEY,
  org        TEXT NOT NULL UNIQUE,
  created_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS referrals (
  id           TEXT PRIMARY KEY,
  referrer_org TEXT NOT NULL,
  referee_org  TEXT NOT NULL UNIQUE,
  code         TEXT NOT NULL,
  status       TEXT NOT NULL,
  created_at   BIGINT NOT NULL,
  qualified_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_referrals_referrer ON referrals(referrer_org, created_at);
CREATE INDEX IF NOT EXISTS ix_referrals_status   ON referrals(status);

-- One vocabulary: the pre-rename rows said 'signed_up'. Idempotent.
UPDATE referrals SET status='signup' WHERE status='signed_up';

-- The credit era is over. Rows an earlier build marked 'credited' recorded that a
-- referee had qualified AND that platform credit was minted for it; the first half
-- is still true and is the state they keep, the second half is not a thing this
-- table records any more. The grant/txn columns are left to rot in place on an
-- existing file (SQLite DROP COLUMN is not portable across the versions in the
-- fleet, and no code reads or writes them): NOT NULL DEFAULT means an insert that
-- names only the live columns still succeeds. New files never get them.
UPDATE referrals SET status='qualified' WHERE status='credited';
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
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
	err := s.db.QueryRowContext(ctx, `SELECT code FROM referral_codes WHERE org=$1`, org).Scan(&code)
	if err == nil {
		return code, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("lookup code: %w", err)
	}
	// Mint. Retry on the (astronomically unlikely) code collision with another org.
	for n := range 8 {
		cand := deriveCode(org, n)
		err := s.db.QueryRowContext(ctx,
			`INSERT INTO referral_codes (code, org, created_at) VALUES ($1,$2,$3)
			 ON CONFLICT DO NOTHING RETURNING code`, cand, org, time.Now().Unix()).Scan(&code)
		if err == nil {
			return code, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("mint code: %w", err)
		}
		// Nothing was written. A concurrent minter for the SAME org won the race → read
		// theirs.
		if e := s.db.QueryRowContext(ctx, `SELECT code FROM referral_codes WHERE org=$1`, org).Scan(&code); e == nil {
			return code, nil
		}
		// Else it was a code collision with a DIFFERENT org → try the next salt.
	}
	return "", fmt.Errorf("mint code: exhausted collision retries for %q", org)
}

// SetCode updates the org's referral code to a custom vanity code.
func (s *Store) SetCode(ctx context.Context, org, raw string) (string, error) {
	code := normalizeCode(raw)
	if len(code) < 2 || len(code) > 32 {
		return "", errInvalidCode
	}
	for _, r := range code {
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return "", errInvalidCode
		}
	}
	if _, err := s.EnsureCode(ctx, org); err != nil {
		return "", err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE referral_codes SET code=$1 WHERE org=$2`, code, org)
	if err != nil {
		if dialect.Unique(err) {
			return "", errCodeTaken
		}
		return "", fmt.Errorf("update code: %w", err)
	}
	return code, nil
}

// OrgForCode reverse-resolves a referral code to its owning org. Trims + upper-
// cases the client-supplied code so a link pasted in any case still resolves.
func (s *Store) OrgForCode(ctx context.Context, code string) (string, error) {
	code = normalizeCode(code)
	if code == "" {
		return "", errUnknownCode
	}
	var org string
	err := s.db.QueryRowContext(ctx, `SELECT org FROM referral_codes WHERE code=$1`, code).Scan(&org)
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
	if err := dialect.Text(id, referrerOrg, refereeOrg, code); err != nil {
		return Referral{}, false, err
	}
	var claimed string
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO referrals (id, referrer_org, referee_org, code, status, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (referee_org) DO NOTHING RETURNING id`,
		id, referrerOrg, refereeOrg, normalizeCode(code), StatusSignup, time.Now().Unix()).Scan(&claimed)
	if err == nil {
		r, gerr := s.get(ctx, id)
		return r, true, gerr
	}
	if errors.Is(err, sql.ErrNoRows) {
		// The referee was already referred — idempotent: return the existing edge.
		r, gerr := s.getByReferee(ctx, refereeOrg)
		return r, false, gerr
	}
	return Referral{}, false, fmt.Errorf("claim: %w", err)
}

// LatchQualified atomically advances a referral to qualified: it sets status +
// qualified_at ONLY while qualified_at is still 0. RowsAffected==1 means THIS
// caller won the race and observed the transition; 0 means a concurrent sweep
// already made it. qualified_at is the idempotency latch, so the edge qualifies
// at-most-once and the audit trail records one transition, not two.
func (s *Store) LatchQualified(ctx context.Context, id string, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE referrals
		    SET status='qualified',
		        qualified_at = $1
		  WHERE id=$2 AND qualified_at=0`,
		now, id)
	if err != nil {
		return false, fmt.Errorf("latch qualified: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

const referralCols = `id,referrer_org,referee_org,code,status,created_at,qualified_at`

func scanReferral(sc interface{ Scan(...any) error }) (Referral, error) {
	var r Referral
	err := sc.Scan(&r.ID, &r.ReferrerOrg, &r.RefereeOrg, &r.Code, &r.Status,
		&r.CreatedAt, &r.QualifiedAt)
	return r, err
}

func (s *Store) get(ctx context.Context, id string) (Referral, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+referralCols+` FROM referrals WHERE id=$1`, id)
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
	row := s.db.QueryRowContext(ctx, `SELECT `+referralCols+` FROM referrals WHERE referee_org=$1`, refereeOrg)
	r, err := scanReferral(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Referral{}, errNotFound
	}
	if err != nil {
		return Referral{}, fmt.Errorf("get referral by referee: %w", err)
	}
	return r, nil
}

// Get re-reads a referral by id (post-transition refresh).
func (s *Store) Get(ctx context.Context, id string) (Referral, error) { return s.get(ctx, id) }

// ListByReferrer returns an org's referrals (the people IT referred), newest
// first, bounded by limit.
func (s *Store) ListByReferrer(ctx context.Context, referrerOrg string, limit int) ([]Referral, error) {
	return s.query(ctx, `SELECT `+referralCols+` FROM referrals WHERE referrer_org=$1 ORDER BY created_at DESC LIMIT $2`, referrerOrg, limit)
}

// ListPending returns referrals still awaiting the qualify check (status
// signup), oldest first, bounded — the admin sweep folds
// over this set. optReferrer scopes to one referrer ("" = all, the admin sweep).
func (s *Store) ListPending(ctx context.Context, optReferrer string, limit int) ([]Referral, error) {
	if optReferrer == "" {
		return s.query(ctx, `SELECT `+referralCols+` FROM referrals WHERE status=$1 ORDER BY created_at ASC LIMIT $2`, StatusSignup, limit)
	}
	return s.query(ctx, `SELECT `+referralCols+` FROM referrals WHERE status=$1 AND referrer_org=$2 ORDER BY created_at ASC LIMIT $3`, StatusSignup, optReferrer, limit)
}

// Signups lists the moments, in Unix seconds and oldest first, referred orgs signed
// up at or after since: at most 100,000 of them, and nothing about who.
func (s *Store) Signups(ctx context.Context, since int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT created_at FROM referrals WHERE created_at >= $1 ORDER BY created_at LIMIT 100000`, since)
	if err != nil {
		return nil, fmt.Errorf("list referral signups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			return nil, fmt.Errorf("scan referral signup: %w", err)
		}
		out = append(out, at)
	}
	return out, rows.Err()
}

// Tally counts the referrals of each referrer org named, by status, in one
// grouped read. An org with none is absent from the answer.
func (s *Store) Tally(ctx context.Context, orgs []string) (map[string]statusCounts, error) {
	out := map[string]statusCounts{}
	if len(orgs) == 0 {
		return out, nil
	}
	args := make([]any, len(orgs))
	for i, o := range orgs {
		args[i] = o
	}
	q := `SELECT referrer_org, status, COUNT(*) FROM referrals WHERE referrer_org IN (` + dialect.Marks(1, len(orgs)) + `) GROUP BY referrer_org, status`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("tally referrals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var org, status string
		var n int
		if err := rows.Scan(&org, &status, &n); err != nil {
			return nil, fmt.Errorf("scan referral tally: %w", err)
		}
		c := out[org]
		c.Total += n
		switch status {
		case StatusSignup:
			c.Signup += n
		case StatusQualified:
			c.Qualified += n
		}
		out[org] = c
	}
	return out, rows.Err()
}

// ListAll returns every referral newest-first (the admin directory), bounded.
func (s *Store) ListAll(ctx context.Context, limit int) ([]Referral, error) {
	return s.query(ctx, `SELECT `+referralCols+` FROM referrals ORDER BY created_at DESC LIMIT $1`, limit)
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
