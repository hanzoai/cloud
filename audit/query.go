package audit

// The read paths: filtered Query (for /v1/admin/audit) and Verify (the
// tamper-evidence walk for /v1/admin/audit/verify). Both are read-only — they
// issue SELECT only, never mutate — so exposing them can never weaken the
// append-only property.

import (
	"context"
	"database/sql"
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
	Org  string // actor_org exact match (the org acted IN)
	Sub  string // actor_sub exact match (a specific user)
	Home string // actor_home exact match — the org the actor came FROM.
	// Impersonated restricts to CROSS-ORG actions only (actor_home <> ''), which
	// is the question this control exists to answer: "show me every time a
	// platform admin acted inside a tenant that was not their own." Without it an
	// auditor would have to scan the whole trail to find the events that matter
	// most.
	Impersonated bool
	Action       string // action exact match
	// Actions matches any ONE of several action names — the question "show me
	// the rows that evidence this control", where a control is evidenced by a
	// SET of actions. One query rather than one per name, because the caller
	// needs them interleaved in time and bounded by one Limit; N queries would
	// have to merge and re-sort them, and would page each name separately.
	// Empty means unrestricted. Composes with Action as an AND, so naming both
	// is a contradiction rather than a widening.
	Actions    []string
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
	offset := max(f.Offset, 0)

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
	if f.Home != "" {
		add("actor_home = ?", f.Home)
	}
	if f.Impersonated {
		// No bound value — a fixed predicate, not caller input, so it stays
		// consistent with "no caller value ever reaches the SQL text".
		conds = append(conds, "actor_home <> ''")
	}
	if f.Action != "" {
		add("action = ?", f.Action)
	}
	if len(f.Actions) > 0 {
		// The placeholder list is built from the COUNT of values, never from the
		// values, so nothing a caller supplies reaches the SQL text — the same
		// rule every predicate above follows.
		marks := strings.TrimSuffix(strings.Repeat("?,", len(f.Actions)), ",")
		conds = append(conds, "action IN ("+marks+")")
		for _, a := range f.Actions {
			args = append(args, a)
		}
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

const selectCols = `seq, ts, actor_org, actor_sub, actor_email, actor_home, action, res_type, res_id,
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
		&rec.Seq, &ts, &rec.Actor.Org, &rec.Actor.Sub, &rec.Actor.Email, &rec.Actor.Home,
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

// Verdict is what a chain walk concluded. THREE states, not two: a chain that
// could not be READ is not a chain that passed, and a boolean has nowhere to put
// the difference. That distinction is the whole reason this is not a bool — an
// outage and a clean bill of health must never render the same.
type Verdict string

const (
	// Intact: every record's stored hash equals the recomputed hash AND the chain
	// links are continuous (each PrevHash == the prior record's Hash, seqs gapless
	// from 0).
	Intact Verdict = "intact"
	// Broken: the walk reached BrokenAt, and Reason says how the chain fails there.
	Broken Verdict = "broken"
	// Unread: the chain could not be walked at all, and Reason says why. Nothing is
	// known about its contents — it is neither a pass nor a break.
	Unread Verdict = "unread"
)

// Integrity is ONE chain's verification — the AU-9 evidence that THAT chain has
// not been tampered with. One chain, never the trail: a deployment holds one chain
// per process (see Name), and the whole family verified is a Trail.
type Integrity struct {
	// Name is the chain this verdict is about, e.g. "audit" or "audit-iam". It is
	// carried because a verdict with no chain on it reads as the whole trail's,
	// which is what a reader of a 128-chain deployment did.
	Name string `json:"name"`
	// Verdict is intact, broken or unread.
	Verdict Verdict `json:"verdict"`
	// Count is the number of records walked. Zero on an unread chain, where it
	// means "nothing was read", not "the chain is empty".
	Count uint64 `json:"count"`
	// Head is the hash of the last record (or the genesis anchor for an empty
	// chain). Pin this externally over time to detect tail-truncation.
	Head string `json:"head"`
	// BrokenAt is the seq of the FIRST record that failed verification, and -1
	// whenever the walk found no break (including an unread chain, where no seq
	// was reached).
	BrokenAt int64 `json:"brokenAt"`
	// Reason says HOW the chain fails at BrokenAt — a recomputed-hash mismatch, a
	// prev-hash discontinuity, or a seq gap — or, on an unread chain, why it could
	// not be walked at all. Absent on an intact chain, which is the only verdict
	// with nothing to explain.
	Reason string `json:"reason,omitempty"`
}

// Verify walks THIS recorder's chain and reports its integrity. The whole family
// a deployment keeps is the package-level Verify.
func (r *Recorder) Verify(ctx context.Context) (Integrity, error) {
	return walk(ctx, r.db, r.name)
}

// walk is the ONE chain walk, shared by the Recorder's own Verify and by the
// trail reader, so a chain cannot be judged by two different rules depending on
// whether the process that wrote it is the process asking.
//
// It reads the chain in seq order, recomputing each record's hash from its
// content + the running prev-hash and checking continuity. It is the
// tamper-detector: any modification (a changed field re-hashes differently), any
// deletion or reordering (a seq gap or a broken prev-hash link), or a forged row
// (its recomputed hash won't match unless the attacker also recomputed the entire
// suffix — which they cannot do without re-inserting every subsequent record) is
// reported with the exact seq where the chain first breaks.
//
// A returned ERROR means the chain could not be read; it is never a verdict about
// its contents, and a caller must not render one as the other. Cancelling ctx
// stops the walk — database/sql closes the rows and rs.Err() carries the reason —
// so a 1.7 GB family does not outlive the client that asked about it.
//
// Complexity is O(n) over the records; this streams row by row (no full
// materialization). If a chain grows past what an on-demand full walk should
// touch, walk a seq WINDOW (this is easily extended with a bound) or rely on the
// externally-pinned head.
func walk(ctx context.Context, db *sql.DB, name string) (Integrity, error) {
	rs, err := db.QueryContext(ctx,
		`SELECT `+selectCols+` FROM audit_log ORDER BY seq ASC`)
	if err != nil {
		return Integrity{}, fmt.Errorf("audit: verify query: %w", err)
	}
	defer func() { _ = rs.Close() }()

	prevHash := genesisPrevHash
	var expectSeq uint64
	var count uint64
	headHash := genesisPrevHash

	broke := func(seq uint64, reason string) Integrity {
		return Integrity{
			Name: name, Verdict: Broken, Count: count, Head: headHash,
			BrokenAt: int64(seq), Reason: reason,
		}
	}

	for rs.Next() {
		rec, scanErr := scanRecord(rs)
		if scanErr != nil {
			return Integrity{}, fmt.Errorf("audit: verify scan: %w", scanErr)
		}
		// Gapless, 0-based ordering.
		if rec.Seq != expectSeq {
			return broke(rec.Seq, fmt.Sprintf("seq gap: expected %d, got %d", expectSeq, rec.Seq)), nil
		}
		// Link continuity: this record must chain to the previous record's hash.
		if rec.PrevHash != prevHash {
			return broke(rec.Seq, "prev_hash discontinuity (a record was deleted, reordered, or altered)"), nil
		}
		// Content integrity: recompute the hash from the record's own fields.
		want, hErr := computeHash(rec, rec.PrevHash)
		if hErr != nil {
			return Integrity{}, fmt.Errorf("audit: verify hash: %w", hErr)
		}
		if want != rec.Hash {
			return broke(rec.Seq, "hash mismatch (record content was modified after it was written)"), nil
		}
		prevHash = rec.Hash
		headHash = rec.Hash
		expectSeq = rec.Seq + 1
		count++
	}
	if err := rs.Err(); err != nil {
		return Integrity{}, fmt.Errorf("audit: verify rows: %w", err)
	}
	return Integrity{Name: name, Verdict: Intact, Count: count, Head: headHash, BrokenAt: -1}, nil
}
