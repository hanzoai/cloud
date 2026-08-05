package audit

// The read paths: filtered Query (for /v1/admin/audit) and Verify (the
// tamper-evidence walk for /v1/admin/audit/verify). Both are read-only — they
// issue SELECT only, never mutate — so exposing them can never weaken the
// append-only property.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Filter narrows a Query. Zero-value fields are ignored (no constraint), so an
// empty Filter returns the most-recent Limit records. Time bounds are inclusive
// and compared against the RFC3339Nano ts column lexicographically (RFC3339 is
// order-preserving as text, so a string range is a correct time range).
type Filter struct {
	Org        string    // actor_org exact match (tenant scope)
	Sub        string    // actor_sub exact match (a specific user)
	Action     string    // action exact match
	Resource   string    // res_type exact match
	ResourceID string    // res_id exact match (a specific resource instance)
	Result     string    // outcome result: success|deny|error
	Since      time.Time // ts >= Since (UTC)
	Until      time.Time // ts <= Until (UTC)
	Limit      int       // max rows (default 100, cap 1000)
	Offset     int       // pagination offset
}

// Query returns records matching f, newest first, and the total count matching
// the same predicate (ignoring Limit/Offset) for pagination. All predicates are
// parameterized — never string-interpolated — so a filter value can never inject
// SQL. Column names in the WHERE come from a fixed allowlist below, not caller
// input.
func (r *Recorder) Query(ctx context.Context, f Filter) (rows []Record, total int, err error) {
	where, args := f.build()
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	countQ := `SELECT COUNT(*) FROM audit_log` + where
	if err = r.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("audit: count: %w", err)
	}

	listQ := `SELECT ` + selectCols + ` FROM audit_log` + where +
		` ORDER BY seq DESC LIMIT ? OFFSET ?`
	listArgs := append(append([]any{}, args...), limit, offset)
	rs, err := r.db.QueryContext(ctx, listQ, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("audit: query: %w", err)
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		rec, scanErr := scanRecord(rs)
		if scanErr != nil {
			return nil, 0, fmt.Errorf("audit: scan: %w", scanErr)
		}
		rows = append(rows, rec)
	}
	return rows, total, rs.Err()
}

// build assembles the parameterized WHERE clause from the non-zero filter
// fields. Each fragment uses a fixed column name and a ? placeholder, so no
// caller value ever reaches the SQL text.
func (f Filter) build() (string, []any) {
	var conds []string
	var args []any
	add := func(frag string, val any) {
		conds = append(conds, frag)
		args = append(args, val)
	}
	if f.Org != "" {
		add("actor_org = ?", f.Org)
	}
	if f.Sub != "" {
		add("actor_sub = ?", f.Sub)
	}
	if f.Action != "" {
		add("action = ?", f.Action)
	}
	if f.Resource != "" {
		add("res_type = ?", f.Resource)
	}
	if f.ResourceID != "" {
		add("res_id = ?", f.ResourceID)
	}
	if f.Result != "" {
		add("result = ?", f.Result)
	}
	if !f.Since.IsZero() {
		add("ts >= ?", f.Since.UTC().Format(time.RFC3339Nano))
	}
	if !f.Until.IsZero() {
		add("ts <= ?", f.Until.UTC().Format(time.RFC3339Nano))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

const selectCols = `seq, ts, actor_org, actor_sub, actor_email, action, res_type, res_id,
  auth_method, is_admin, result, status, reason, source_ip, user_agent,
  request_id, method, path, before, after, prev_hash, hash`

// scanRecord reconstructs a Record from a row of selectCols.
func scanRecord(sc interface{ Scan(...any) error }) (Record, error) {
	var (
		rec           Record
		ts            string
		isAdmin       int
		before, after string
	)
	if err := sc.Scan(
		&rec.Seq, &ts, &rec.Actor.Org, &rec.Actor.Sub, &rec.Actor.Email,
		&rec.Action, &rec.Resource.Type, &rec.Resource.ID,
		&rec.Auth.Method, &isAdmin, &rec.Outcome.Result, &rec.Outcome.Status, &rec.Outcome.Reason,
		&rec.SourceIP, &rec.UserAgent, &rec.RequestID, &rec.Method, &rec.Path,
		&before, &after, &rec.PrevHash, &rec.Hash,
	); err != nil {
		return Record{}, err
	}
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		rec.Time = t
	}
	rec.Auth.IsAdmin = isAdmin != 0
	if before != "" {
		rec.Before = json.RawMessage(before)
	}
	if after != "" {
		rec.After = json.RawMessage(after)
	}
	return rec, nil
}

// Integrity is the result of a Verify walk — the AU-9 evidence that the trail has
// not been tampered with.
type Integrity struct {
	// OK is true iff every record's stored hash equals the recomputed hash AND the
	// chain links are continuous (each PrevHash == the prior record's Hash, seqs
	// gapless from 0).
	OK bool `json:"ok"`
	// Count is the number of records walked.
	Count uint64 `json:"count"`
	// HeadHash is the hash of the last record (or the genesis anchor for an empty
	// chain). Pin this externally over time to detect tail-truncation.
	HeadHash string `json:"headHash"`
	// BrokenAt is the seq of the FIRST record that failed verification, or -1 when
	// OK. Reason describes the break (recomputed-hash mismatch, prev-hash
	// discontinuity, or a seq gap).
	BrokenAt int64  `json:"brokenAt"`
	Reason   string `json:"reason,omitempty"`
}

// Verify walks the entire chain in seq order, recomputing each record's hash from
// its content + the running prev-hash and checking continuity. It is the
// tamper-detector: any modification (a changed field re-hashes differently), any
// deletion or reordering (a seq gap or a broken prev-hash link), or a forged row
// (its recomputed hash won't match unless the attacker also recomputed the entire
// suffix — which they cannot do without re-inserting every subsequent record) is
// reported with the exact seq where the chain first breaks.
//
// Complexity is O(n) over the records; for very large trails this streams row by
// row (no full materialization). At cloud's audit volume this is fine; if a trail
// grows past what an on-demand full walk should touch, verify a seq WINDOW
// (Verify is easily extended with a bound) or rely on the externally-pinned head.
func (r *Recorder) Verify(ctx context.Context) (Integrity, error) {
	rs, err := r.db.QueryContext(ctx,
		`SELECT `+selectCols+` FROM audit_log ORDER BY seq ASC`)
	if err != nil {
		return Integrity{}, fmt.Errorf("audit: verify query: %w", err)
	}
	defer func() { _ = rs.Close() }()

	prevHash := genesisPrevHash
	var expectSeq uint64
	var count uint64
	headHash := genesisPrevHash

	for rs.Next() {
		rec, scanErr := scanRecord(rs)
		if scanErr != nil {
			return Integrity{}, fmt.Errorf("audit: verify scan: %w", scanErr)
		}
		// Gapless, 0-based ordering.
		if rec.Seq != expectSeq {
			return Integrity{
				OK: false, Count: count, HeadHash: headHash,
				BrokenAt: int64(rec.Seq),
				Reason:   fmt.Sprintf("seq gap: expected %d, got %d", expectSeq, rec.Seq),
			}, nil
		}
		// Link continuity: this record must chain to the previous record's hash.
		if rec.PrevHash != prevHash {
			return Integrity{
				OK: false, Count: count, HeadHash: headHash,
				BrokenAt: int64(rec.Seq),
				Reason:   "prev_hash discontinuity (a record was deleted, reordered, or altered)",
			}, nil
		}
		// Content integrity: recompute the hash from the record's own fields.
		want, hErr := computeHash(rec, rec.PrevHash)
		if hErr != nil {
			return Integrity{}, fmt.Errorf("audit: verify hash: %w", hErr)
		}
		if want != rec.Hash {
			return Integrity{
				OK: false, Count: count, HeadHash: headHash,
				BrokenAt: int64(rec.Seq),
				Reason:   "hash mismatch (record content was modified after it was written)",
			}, nil
		}
		prevHash = rec.Hash
		headHash = rec.Hash
		expectSeq = rec.Seq + 1
		count++
	}
	if err := rs.Err(); err != nil {
		return Integrity{}, fmt.Errorf("audit: verify rows: %w", err)
	}
	return Integrity{OK: true, Count: count, HeadHash: headHash, BrokenAt: -1}, nil
}
