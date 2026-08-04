package reference

// override.go is a tenant's own say over the shared baseline: the entries this
// organisation allows or denies whatever the published data says.
//
// ISOLATION IS PHYSICAL, NOT PREDICATED. An override lives in the
// organisation's OWN SQLite file, reached through cloud.OrgNamespace — the one
// door a validated org walks through — so a distinct organisation is a distinct
// file and a query in one cannot reach another's rows. There is no `org` column
// to forget in a WHERE clause, because there is no shared table. This is the
// same physical isolation every other per-entity store in the binary has
// (HIP-0302), and it is why the cross-tenant write in this design is
// unrepresentable rather than merely refused.
//
// The wire agrees with the store: no In struct carries a scope, an org or a
// tenant field, so a caller cannot even NAME another organisation. The
// namespace is minted from the validated principal and from nothing else.
//
// THE BOUND IS PER TENANT. An organisation may hold maxOverrides entries per
// set, and the write past that is refused. A shared cap would let one
// organisation's list quietly evict another's; a per-tenant cap means a tenant
// can only ever degrade itself.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// maxOverrides is how many entries one organisation may hold in one set.
const maxOverrides = 10_000

// maxActor bounds, IN BYTES, the writer recorded on a row.
//
// It is a term of [row], and [row] is what [ownVolume] is computed from, so
// leaving it unbounded left the published per-organisation ceiling stating a
// figure nothing held to: the writer is [actor]'s reading of the X-User-Id the
// request carries, so it was a caller-sized value stored [maxOverrides] times in
// every set the catalog publishes, on the ONE volume every organisation's file
// sits on. A count over caller-sized values is not a byte bound.
//
// The bound is at the store door rather than at the wire op because the row is
// what the budget is about: bounded where a row is written, it holds for every
// path that reaches the store and not only the one a reviewer happened to read.
// 128 bytes is three times the UUID IAM mints.
const maxActor = 128

// errActor separates the two refusals [put] can produce. The row cap is a
// CONFLICT — the organisation already holds what it holds — and an over-long
// writer is a BAD REQUEST, because it is a value that arrived on this call.
var errActor = errors.New("the writer on this row is past its bound")

// row bounds ONE stored override in bytes: the three bounded strings plus the
// verdict and the timestamp, generously rounded up. Every term is DERIVED from
// the bound the door enforces — a term written down independently here is a
// ceiling that stops tracking the thing it is a ceiling on.
const row = maxKey + maxNote + maxActor

// ownVolume is what one organisation may occupy on the shared volume: rows per
// set, times the sets the catalog publishes, times [row].
//
// It is COMPUTED rather than asserted, because the three constants that decide
// it live in three files and the product is the thing that actually matters —
// every org's SQLite file sits on the ONE volume this deployment mounts, so a
// per-tenant bound is only a tenant-isolation property once somebody can say
// what it comes to. Adding a set moves it, which is the point.
//
// TestTheVolumeOneOrgMayOccupyIsStated pins the figure, so raising any of the
// three is an act with the consequence next to it rather than a side effect
// three files away.
func ownVolume() int64 { return int64(maxOverrides) * int64(len(Catalog())) * row }

// The two verdicts an override can carry. An override is a DECISION, unlike a
// baseline entry, which carries facts and leaves the decision to policy — the
// tenant is the only party entitled to say "for us, this one is fine".
const (
	Allow = "allow"
	Deny  = "deny"
)

// verdictOK is the closed vocabulary. Anything else is refused at the door: a
// free-text verdict cannot be counted, tested or acted on.
func verdictOK(v string) bool { return v == Allow || v == Deny }

// overrides is one organisation's override store.
type overrides struct{ db *sql.DB }

// openOverrides migrates and wraps a freshly opened per-org database.
//
// `set` is quoted because it is a keyword in the dialect and the word is the
// one the wire uses; renaming the column would put a second name on one concept
// for the sake of the parser.
func openOverrides(db *sql.DB) (*overrides, error) {
	const schema = `
CREATE TABLE IF NOT EXISTS override (
    "set"   TEXT NOT NULL,
    key     TEXT NOT NULL,
    verdict TEXT NOT NULL,
    note    TEXT NOT NULL DEFAULT '',
    at      TEXT NOT NULL,
    by      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY ("set", key)
);`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("reference: migrate overrides: %w", err)
	}
	return &overrides{db: db}, nil
}

func (o *overrides) Close() error { return o.db.Close() }

// ReferenceOverride is one entry a tenant laid over the baseline.
type ReferenceOverride struct {
	// Key is the member this organisation is speaking about.
	Key string `json:"key"`
	// Verdict is allow or deny.
	Verdict string `json:"verdict"`
	// Note is why, in the operator's own words. Optional, and bounded.
	Note string `json:"note,omitempty"`
	// At is when it was written, RFC 3339.
	At string `json:"at"`
	// By is who wrote it.
	By string `json:"by,omitempty"`
}

// count is how many entries this organisation holds in one set.
func (o *overrides) count(set string) (int, error) {
	var n int
	err := o.db.QueryRow(`SELECT count(*) FROM override WHERE "set" = ?`, set).Scan(&n)
	return n, err
}

// put writes entries, replacing an existing key. Idempotent on (set, key): the
// same entry written twice is one entry.
//
// The whole batch is one transaction, so a batch that would cross the per-set
// bound writes nothing rather than half of itself — a half-applied deny list is
// worse than a refused one, because nobody can tell which half applied.
func (o *overrides) put(set string, in []ReferenceOverride, by string, now time.Time) (int, error) {
	if len(in) == 0 {
		return 0, nil
	}
	// Refused rather than shortened: the writer is who an adverse action is
	// attributed to, and a truncated identity names someone who does not exist.
	if len(by) > maxActor {
		return 0, fmt.Errorf("%w: it is %d bytes and the bound is %d; a stored writer is a term of the %d MiB every organisation may hold",
			errActor, len(by), maxActor, ownVolume()>>20)
	}
	tx, err := o.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var held int
	if err := tx.QueryRow(`SELECT count(*) FROM override WHERE "set" = ?`, set).Scan(&held); err != nil {
		return 0, err
	}
	var fresh int
	for _, e := range in {
		var exists int
		if err := tx.QueryRow(`SELECT count(*) FROM override WHERE "set" = ? AND key = ?`, set, e.Key).Scan(&exists); err != nil {
			return 0, err
		}
		if exists == 0 {
			fresh++
		}
	}
	if held+fresh > maxOverrides {
		return 0, fmt.Errorf("this org already holds %d overrides in %q and the bound is %d", held, set, maxOverrides)
	}

	stamp := now.UTC().Format(time.RFC3339)
	for _, e := range in {
		if _, err := tx.Exec(
			`INSERT INTO override ("set", key, verdict, note, at, by) VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT("set", key) DO UPDATE SET verdict = excluded.verdict, note = excluded.note, at = excluded.at, by = excluded.by`,
			set, e.Key, e.Verdict, e.Note, stamp, by); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(in), nil
}

// clear removes one entry, reporting whether there was one.
func (o *overrides) clear(set, key string) (bool, error) {
	res, err := o.db.Exec(`DELETE FROM override WHERE "set" = ? AND key = ?`, set, key)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// list pages one set's entries in key order.
func (o *overrides) list(set, after string, limit int) ([]ReferenceOverride, error) {
	rows, err := o.db.Query(
		`SELECT key, verdict, note, at, by FROM override WHERE "set" = ? AND key > ? ORDER BY key LIMIT ?`,
		set, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ReferenceOverride, 0, limit)
	for rows.Next() {
		var e ReferenceOverride
		if err := rows.Scan(&e.Key, &e.Verdict, &e.Note, &e.At, &e.By); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// pick resolves the MOST SPECIFIC override among a bounded candidate list.
//
// The candidates come from the SAME function the baseline lookup uses
// (candidates, in resolve.go), most specific first, so an override and a
// baseline entry are matched by one rule. Two matchers would mean a tenant's
// deny of tempbox.example covering mail.tempbox.example in the baseline's sense
// but not in their own, which is a surprise nobody can debug.
//
// One query with a bounded IN list, never a scan of the tenant's whole set.
func (o *overrides) pick(set string, candidates []string) (ReferenceOverride, bool, error) {
	if len(candidates) == 0 {
		return ReferenceOverride{}, false, nil
	}
	args := make([]any, 0, len(candidates)+1)
	args = append(args, set)
	holes := make([]string, 0, len(candidates))
	for _, c := range candidates {
		holes = append(holes, "?")
		args = append(args, c)
	}
	rows, err := o.db.Query(
		`SELECT key, verdict, note, at, by FROM override WHERE "set" = ? AND key IN (`+strings.Join(holes, ",")+`)`,
		args...)
	if err != nil {
		return ReferenceOverride{}, false, err
	}
	defer rows.Close()
	found := map[string]ReferenceOverride{}
	for rows.Next() {
		var e ReferenceOverride
		if err := rows.Scan(&e.Key, &e.Verdict, &e.Note, &e.At, &e.By); err != nil {
			return ReferenceOverride{}, false, err
		}
		found[e.Key] = e
	}
	if err := rows.Err(); err != nil {
		return ReferenceOverride{}, false, err
	}
	// candidates is ordered most specific first, so the first hit is the answer.
	for _, c := range candidates {
		if e, ok := found[c]; ok {
			return e, true, nil
		}
	}
	return ReferenceOverride{}, false, nil
}
