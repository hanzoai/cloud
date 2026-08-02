package risk

// store.go is the tenant's record plane: decisions, rules, lists, suppressions,
// controls and the model's snapshot, in the tenant's OWN encrypted SQLite file.
//
// THE FILE IS THE TENANT BOUNDARY. cloud.OrgDB resolves
// {DataDir}/orgs/{orgSlug}/risk.db through the injective SanitizeOrg slugger, so
// two distinct orgs can never share a file and no segment can traverse out of
// DataDir. There is no `org` column on any table here, and there cannot be a
// cross-tenant read, because there is no statement that could express one.
//
// DURABLE FIRST, ANALYTICS AFTER. Every decision lands here and in the audit
// hash chain BEFORE the analytics copy is emitted to /v1/event. That door is
// best-effort by design — it answers {accepted, dropped} and the anonymous lane
// drops on purpose — so a compliance-grade record that rode it would be lost by
// a bus hiccup, invisibly. The copy is a copy.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/analytics"
	"github.com/zap-proto/zip"
)

// schema is the tenant file's whole shape. Applied on open; every statement is
// idempotent so a fresh file and an existing one converge.
const schema = `
CREATE TABLE IF NOT EXISTS decision (
	id          TEXT PRIMARY KEY,
	at          TEXT NOT NULL,
	stage       TEXT NOT NULL,
	kind        TEXT NOT NULL,
	subject     TEXT NOT NULL,
	action      TEXT NOT NULL,
	score       REAL NOT NULL,
	agency      TEXT NOT NULL,
	shadow      INTEGER NOT NULL,
	refusal     TEXT NOT NULL DEFAULT '',
	amount      INTEGER NOT NULL DEFAULT 0,
	currency    TEXT NOT NULL DEFAULT '',
	direction   TEXT NOT NULL DEFAULT '',
	idem        TEXT NOT NULL DEFAULT '',
	hits        TEXT NOT NULL DEFAULT '[]',
	causes      TEXT NOT NULL DEFAULT '[]',
	signals     TEXT NOT NULL DEFAULT '{}',
	digest      TEXT NOT NULL DEFAULT '',
	label       TEXT NOT NULL DEFAULT '',
	label_by    TEXT NOT NULL DEFAULT '',
	label_at    TEXT NOT NULL DEFAULT '',
	-- The COVERAGE of the aggregates this decision read. since is the instant
	-- this tenant's rings started, so a 30-day count computed from ten minutes of
	-- rings is readable as exactly that; strained says the tenant was at its own
	-- cardinality bound, so a count may be short of its own traffic. Both are
	-- stored rather than computed on read, because a retry under one idempotency
	-- key must answer with the original decision and not with today's coverage.
	since       TEXT NOT NULL DEFAULT '',
	strained    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS decision_at ON decision(at DESC);
CREATE INDEX IF NOT EXISTS decision_subject ON decision(kind, subject, at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS decision_idem ON decision(idem) WHERE idem != '';

CREATE TABLE IF NOT EXISTS rule (
	id      TEXT PRIMARY KEY,
	body    TEXT NOT NULL,
	at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS list (
	name    TEXT PRIMARY KEY,
	kind    TEXT NOT NULL DEFAULT 'deny',
	at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS entry (
	list    TEXT NOT NULL,
	value   TEXT NOT NULL,
	at      TEXT NOT NULL,
	by      TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (list, value)
);

CREATE TABLE IF NOT EXISTS suppression (
	id      TEXT PRIMARY KEY,
	rule    TEXT NOT NULL DEFAULT '',
	kind    TEXT NOT NULL DEFAULT '',
	subject TEXT NOT NULL DEFAULT '',
	until   TEXT NOT NULL DEFAULT '',
	reason  TEXT NOT NULL DEFAULT '',
	by      TEXT NOT NULL DEFAULT '',
	at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS control (
	id      TEXT PRIMARY KEY,
	kind    TEXT NOT NULL,
	subject TEXT NOT NULL,
	control TEXT NOT NULL,
	rate    REAL NOT NULL DEFAULT 0,
	until   TEXT NOT NULL DEFAULT '',
	reason  TEXT NOT NULL DEFAULT '',
	by      TEXT NOT NULL DEFAULT '',
	at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS control_subject ON control(kind, subject);

CREATE TABLE IF NOT EXISTS model (
	id      TEXT PRIMARY KEY,
	body    TEXT NOT NULL,
	at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS search (
	id      TEXT PRIMARY KEY,
	at      TEXT NOT NULL,
	status  TEXT NOT NULL,
	body    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS setting (
	key     TEXT PRIMARY KEY,
	value   TEXT NOT NULL
);
`

// The tenant's file is opened, migrated and seeded by residency.open — one
// registry owns every per-tenant lifetime, so there is one place a file is
// opened and one place it is closed. This file owns the STATEMENTS.

// seed installs the starter rule set and the two lists the starter rules name,
// once, on a file that has none. A tenant whose rules are all deleted stays
// empty: re-seeding a deliberately emptied rule plane would resurrect rules an
// operator retired.
func seed(db *sql.DB) error {
	var seeded string
	err := db.QueryRow(`SELECT value FROM setting WHERE key = 'seeded'`).Scan(&seeded)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := stamp(time.Now())
	for _, r := range starter() {
		body, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO rule (id, body, at) VALUES (?, ?, ?)`, r.ID, string(body), now); err != nil {
			return err
		}
	}
	for _, n := range []string{"ip-deny", "email-deny", "ip-allow", "account-deny"} {
		kind := "deny"
		if strings.HasSuffix(n, "-allow") {
			kind = "allow"
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO list (name, kind, at) VALUES (?, ?, ?)`, n, kind, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO setting (key, value) VALUES ('seeded', ?)`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func unstamp(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// ── rules ───────────────────────────────────────────────────────────────────

// loadRules reads the tenant's whole rule set — BOUNDED, because it is read on
// the authorization path and every row it returns is evaluated on every
// decision. The cap is enforced at the write (putRule); the LIMIT here is the
// second half of the same bound, for rows that arrived by any other route.
func loadRules(db *sql.DB) ([]rule, error) {
	rows, err := db.Query(`SELECT body FROM rule ORDER BY id LIMIT ?`, ruleCap())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []rule
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var r rule
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func putRule(db *sql.DB, r rule) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	// The budget is a WRITE-time refusal and not a read-time truncation, because
	// a truncated rule set is a tenant's controls silently switching off.
	// Replacing an existing rule is always allowed: it adds no row.
	return within(db, "rule", "rules", ruleCap(), ruleMax, len(body), r.ID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO rule (id, body, at) VALUES (?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET body = excluded.body, at = excluded.at`,
			r.ID, string(body), stamp(time.Now()))
		return err
	})
}

// within runs a write inside the transaction that enforces the plane's budget.
//
// THE BUDGET IS BYTES AND THE COUNT IS DERIVED FROM IT (bound.go). Both halves
// are checked here: this row is at most rowMax, and the plane holds at most
// budget/rowMax rows. A count cap over rows the caller sizes is not a bound at
// all, which is the defect class this file was held for.
//
// IN ONE TRANSACTION, and that is not decoration. Counting outside it let N
// concurrent writes at cap-1 all see room and all commit: a bound that is read
// before the write it bounds is a suggestion.
//
// `id` is the row being written: an UPDATE to a row that already exists is not a
// growth and is never refused, which is what keeps a tenant at its cap able to
// fix a rule rather than only able to delete one. Written once and taken by every
// budgeted plane, so a new plane cannot get a subtly different rule.
func within(db *sql.DB, table, plane string, cap, rowMax, size int, id string, write func(*sql.Tx) error) error {
	if size > rowMax {
		return zip.Errorf(413, "one of this tenant's %s is %d bytes; this plane stores at most %d per row", plane, size, rowMax)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		return err
	}
	if n >= cap {
		grown := true
		if id != "" {
			var exists int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id).Scan(&exists); err != nil {
				return err
			}
			grown = exists == 0
		}
		if grown {
			return zip.Errorf(409, "%s", errCap(plane, cap).Error())
		}
	}
	if err := write(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func getRule(db *sql.DB, id string) (rule, error) {
	var body string
	if err := db.QueryRow(`SELECT body FROM rule WHERE id = ?`, id).Scan(&body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rule{}, zip.ErrNotFound("no such rule")
		}
		return rule{}, err
	}
	var r rule
	err := json.Unmarshal([]byte(body), &r)
	return r, err
}

func dropRule(db *sql.DB, id string) error {
	res, err := db.Exec(`DELETE FROM rule WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return zip.ErrNotFound("no such rule")
	}
	return nil
}

// ── lists ───────────────────────────────────────────────────────────────────

// listMember is the membership test the rule algebra calls. It is loaded ONCE
// per decision into a map rather than queried per term, because a rule set can
// name the same list many times and the authorization window is not the place to
// find that out.
// It is also BOUNDED. Every entry becomes a map key on the authorization path,
// so an unbounded SELECT here is an unbounded read AND an unbounded allocation
// on every decision. The cap is enforced at the write (addEntries); the LIMIT is
// its second half. The order is stable so a truncation, if a row ever arrives by
// another route, is deterministic rather than whatever the page cache offered.
func loadLists(db *sql.DB) (map[string]map[string]bool, error) {
	rows, err := db.Query(`SELECT list, value FROM entry ORDER BY list, value LIMIT ?`, listCap())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var l, v string
		if err := rows.Scan(&l, &v); err != nil {
			return nil, err
		}
		if out[l] == nil {
			out[l] = map[string]bool{}
		}
		out[l][strings.ToLower(v)] = true
	}
	return out, rows.Err()
}

func listNames(db *sql.DB) ([]listView, error) {
	rows, err := db.Query(`SELECT l.name, l.kind, l.at, (SELECT COUNT(*) FROM entry e WHERE e.list = l.name)
		FROM list l ORDER BY l.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []listView
	for rows.Next() {
		var v listView
		var at string
		if err := rows.Scan(&v.Name, &v.Kind, &at, &v.Entries); err != nil {
			return nil, err
		}
		v.CreatedAt = at
		out = append(out, v)
	}
	return out, rows.Err()
}

func putList(db *sql.DB, name, kind string) error {
	_, err := db.Exec(`INSERT INTO list (name, kind, at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET kind = excluded.kind`, name, kind, stamp(time.Now()))
	return err
}

func addEntries(db *sql.DB, name string, values []string, by string) error {
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM list WHERE name = ?`, name).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return zip.ErrNotFound("no such list")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// The cap is over the tenant's WHOLE entry plane, not per list, because the
	// authorization path loads all of them into one map — a per-list cap would be
	// a bound on nothing, reachable by creating more lists.
	//
	// Checked ONCE, up front, against the batch's full length, and deliberately
	// pessimistic: a batch of duplicates counts as new. The alternative is a
	// membership query per value, which turns one write into N reads on the same
	// path the cap exists to keep cheap — and a bound that costs O(N) queries to
	// enforce is a second denial-of-service wearing the first one's clothes.
	// Counted INSIDE the transaction, so two concurrent adds cannot both see room
	// for the last slot.
	var held int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM entry`).Scan(&held); err != nil {
		return err
	}
	if held+len(values) > listCap() {
		return zip.Errorf(409, "%s", errCap("list entries", listCap()).Error())
	}
	now := stamp(time.Now())
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO entry (list, value, at, by) VALUES (?, ?, ?, ?)`,
			name, strings.ToLower(v), now, by); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func dropEntry(db *sql.DB, name, value string) error {
	res, err := db.Exec(`DELETE FROM entry WHERE list = ? AND value = ?`, name, strings.ToLower(value))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return zip.ErrNotFound("no such entry")
	}
	return nil
}

// ── suppressions ────────────────────────────────────────────────────────────

type suppression struct {
	ID      string
	Rule    string
	Kind    string
	Subject string
	Until   time.Time
	Reason  string
	By      string
	At      time.Time
}

// Bounded like the other two, and for the same reason: every suppression is
// matched against every hit of every decision.
func loadSuppressions(db *sql.DB) ([]suppression, error) {
	rows, err := db.Query(`SELECT id, rule, kind, subject, until, reason, by, at FROM suppression ORDER BY at DESC LIMIT ?`, supCap())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []suppression
	for rows.Next() {
		var s suppression
		var until, at string
		if err := rows.Scan(&s.ID, &s.Rule, &s.Kind, &s.Subject, &until, &s.Reason, &s.By, &at); err != nil {
			return nil, err
		}
		s.Until, s.At = unstamp(until), unstamp(at)
		out = append(out, s)
	}
	return out, rows.Err()
}

func putSuppression(db *sql.DB, s suppression) error {
	size := len(s.ID) + len(s.Rule) + len(s.Kind) + len(s.Subject) + len(s.Reason) + len(s.By) + 64
	return within(db, "suppression", "suppressions", supCap(), supMax, size, s.ID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO suppression (id, rule, kind, subject, until, reason, by, at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			s.ID, s.Rule, s.Kind, s.Subject, stamp(s.Until), s.Reason, s.By, stamp(s.At))
		return err
	})
}

func dropSuppression(db *sql.DB, id string) error {
	res, err := db.Exec(`DELETE FROM suppression WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return zip.ErrNotFound("no such suppression")
	}
	return nil
}

// matches decides whether a suppression mutes a hit. An expired suppression
// mutes nothing: `until` in the past is the shelf's own expiry, so a forgotten
// mute stops muting instead of quietly staying on forever.
func (s suppression) matches(h hit, o observation, now time.Time) bool {
	if !s.Until.IsZero() && now.After(s.Until) {
		return false
	}
	if s.Rule != "" && s.Rule != h.Rule {
		return false
	}
	if s.Kind != "" && s.Kind != o.kind {
		return false
	}
	if s.Subject != "" && s.Subject != o.subject {
		return false
	}
	return s.Rule != "" || s.Kind != "" || s.Subject != ""
}

// ── controls ────────────────────────────────────────────────────────────────

type control struct {
	ID      string
	Kind    string
	Subject string
	Control string
	Rate    float64
	Until   time.Time
	Reason  string
	By      string
	At      time.Time
}

// controls are DECLARATIONS the money plane reads. Risk never moves money:
// hanzoai/commerce owns payouts, disputes and balances, and the integration is
// commerce reading this list before a payout and calling decide at
// authorization. That is also what makes the product processor-agnostic — risk
// takes signals and returns a judgement and touches no processor.
var controlKinds = map[string]bool{"reserve": true, "payout-hold": true, "block": true}

func loadControls(db *sql.DB, kind, subject string) ([]control, error) {
	q := `SELECT id, kind, subject, control, rate, until, reason, by, at FROM control`
	var args []any
	if kind != "" && subject != "" {
		q += ` WHERE kind = ? AND subject = ?`
		args = append(args, kind, subject)
	}
	q += ` ORDER BY at DESC LIMIT ?`
	args = append(args, controlCap())
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []control
	for rows.Next() {
		var c control
		var until, at string
		if err := rows.Scan(&c.ID, &c.Kind, &c.Subject, &c.Control, &c.Rate, &until, &c.Reason, &c.By, &at); err != nil {
			return nil, err
		}
		c.Until, c.At = unstamp(until), unstamp(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

func putControl(db *sql.DB, c control) error {
	size := len(c.ID) + len(c.Kind) + len(c.Subject) + len(c.Control) + len(c.Reason) + len(c.By) + 64
	return within(db, "control", "controls", controlCap(), controlMax, size, c.ID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO control (id, kind, subject, control, rate, until, reason, by, at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.Kind, c.Subject, c.Control, c.Rate, stamp(c.Until), c.Reason, c.By, stamp(c.At))
		return err
	})
}

func dropControl(db *sql.DB, id string) error {
	res, err := db.Exec(`DELETE FROM control WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return zip.ErrNotFound("no such control")
	}
	return nil
}

// ── decisions ───────────────────────────────────────────────────────────────

// putDecision records the decision and CLAIMS its idempotency key in ONE
// statement.
//
// Writing the row and then claiming the key in a second statement leaves a
// window: two concurrent requests carrying the same key both find no row, both
// insert, and the second one's claim then violates the unique index — so a
// caller that retried correctly gets a 500. Here the key is part of the insert,
// so the loser of the race is refused by the index BEFORE a second decision
// exists, and the caller reads the winner's answer back. Which is what an
// idempotency key promises: one decision, one set of counters moved.
//
// The unique index is PARTIAL (`WHERE idem != ”`), so decisions made without a
// key do not collide with each other.
func putDecision(db *sql.DB, o observation, out outcome, digest, idem string, since time.Time, strained bool) error {
	hits, _ := json.Marshal(out.hits)
	causes, _ := json.Marshal(out.causes)
	signals, _ := json.Marshal(o.signals)
	_, err := db.Exec(`INSERT INTO decision
		(id, at, stage, kind, subject, action, score, agency, shadow, refusal, amount, currency, direction, idem, hits, causes, signals, digest, since, strained)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		out.id, stamp(o.at), o.stage, o.kind, o.subject, out.action, out.score, out.agency,
		boolInt(out.shadow), out.refusal, o.amount, o.currency, o.direction,
		idem, string(hits), string(causes), string(signals), digest, stamp(since), boolInt(strained))
	if err != nil && idem != "" && isUnique(err) {
		return errIdemTaken
	}
	return err
}

// prune drops the oldest decisions past this tenant's retention.
//
// A RING, NOT A REFUSAL, and that is the difference between this plane and the
// governed ones. Refusing a rule write costs the tenant a rule it can retry;
// refusing a DECISION costs it the authorization it asked for, so the log gives
// up its oldest rows instead — and says so, on the page, rather than leaving a
// reader to wonder why last quarter is missing.
func prune(db *sql.DB) error {
	_, err := db.Exec(`DELETE FROM decision WHERE id IN (
		SELECT id FROM decision ORDER BY at DESC, id DESC LIMIT -1 OFFSET ?)`, recordCap())
	return err
}

// retention reports how many decisions this tenant's log holds and the instant
// of the oldest one — the window every read of the log is a window into.
func retention(db *sql.DB) (int, string, error) {
	var n int
	var oldest sql.NullString
	err := db.QueryRow(`SELECT COUNT(*), MIN(at) FROM decision`).Scan(&n, &oldest)
	if err != nil {
		return 0, "", err
	}
	return n, oldest.String, nil
}

// errIdemTaken says another request already recorded a decision under this key.
// The caller reads that decision and returns it — the retry gets the original
// answer rather than an error or a second decision.
var errIdemTaken = errors.New("risk: this idempotency key already named a decision")

// isUnique reports a UNIQUE-constraint violation without importing a driver.
// The message is stable across both SQLite drivers cloud can be built with, and
// the alternative — a driver-specific error code — would make this file the one
// place in the package that knows which driver is linked.
func isUnique(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE")
}

// byIdem returns the decision a repeated idempotency key already produced. The
// key is scoped to the tenant's own file, so two tenants using the same key
// never see each other's answer.
func byIdem(db *sql.DB, idem string) (string, bool, error) {
	if idem == "" {
		return "", false, nil
	}
	var id string
	err := db.QueryRow(`SELECT id FROM decision WHERE idem = ?`, idem).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

// decisionsPage reads a page of decisions. Every filter is an EQUALITY on a
// column this file owns, bound positionally; there is no free-text predicate and
// no ordering a caller can name, so there is no statement to inject into.
func decisionsPage(db *sql.DB, kind, subject, action, stage string, limit int) ([]decisionRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var where []string
	var args []any
	for _, f := range []struct {
		col, val string
	}{{"kind", kind}, {"subject", subject}, {"action", action}, {"stage", stage}} {
		if f.val != "" {
			where = append(where, f.col+" = ?")
			args = append(args, f.val)
		}
	}
	q := `SELECT id, at, stage, kind, subject, action, score, agency, shadow, refusal, label, since, strained FROM decision`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []decisionRow
	for rows.Next() {
		var d decisionRow
		var shadow, strained int
		if err := rows.Scan(&d.ID, &d.At, &d.Stage, &d.Kind, &d.Subject, &d.Action, &d.Score,
			&d.Agency, &shadow, &d.Refusal, &d.Label, &d.Since, &strained); err != nil {
			return nil, err
		}
		d.Shadow, d.Strained = shadow != 0, strained != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

type decisionRow struct {
	ID, At, Stage, Kind, Subject, Action, Agency, Refusal, Label, Since string
	Score                                                               float64
	Shadow, Strained                                                    bool
}

// replayed is one recorded decision, reconstituted enough to re-evaluate a
// candidate rule against it: the observation as it was, the facts the live path
// read, which rules fired, and what a human later concluded.
type replayed struct {
	obs   observation
	facts factSet
	rules []string
	label string
}

// replayHistory reads the tenant's recent decisions back into replayable form.
//
// The facts are reconstituted from what was STORED, not recomputed from today's
// rings: a replay that re-read live aggregates would score a year-old
// transaction against this morning's velocity, which answers a question nobody
// asked. Everything the live path could see is on the row.
func replayHistory(db *sql.DB, limit int) ([]replayed, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := db.Query(`SELECT id, at, stage, kind, subject, agency, score, amount, currency,
		direction, signals, hits, label FROM decision ORDER BY at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []replayed
	for rows.Next() {
		var id, at, stage, kind, subject, agency, currency, direction, signalsJSON, hitsJSON, lbl string
		var score float64
		var amount int64
		if err := rows.Scan(&id, &at, &stage, &kind, &subject, &agency, &score, &amount,
			&currency, &direction, &signalsJSON, &hitsJSON, &lbl); err != nil {
			return nil, err
		}
		signals := map[string]string{}
		_ = json.Unmarshal([]byte(signalsJSON), &signals)
		var hits []hit
		_ = json.Unmarshal([]byte(hitsJSON), &hits)

		o := observation{
			id: id, at: unstamp(at), stage: stage, kind: kind, subject: subject,
			agency: agency, amount: amount, currency: currency, direction: direction,
			signals: signals,
		}
		f := factSet{
			scalar: map[string]string{
				"stage": stage, "subject.kind": kind, "subject.id": subject, "agency": agency,
				"amount.currency": currency, "amount.direction": direction,
			},
			number: map[string]float64{"amount.nano": float64(amount), "model.score": score},
		}
		for k, v := range signals {
			f.scalar["signal."+k] = v
		}
		r := replayed{obs: o, facts: f, label: lbl}
		for _, h := range hits {
			r.rules = append(r.rules, h.Rule)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// seenSignals is the half of the field catalogue that is the TENANT's own: the
// signal keys it has actually sent, read back off its recent decisions. A rule
// builder offering "every signal in general" is offering fields that do not
// exist here; this is the set that does.
func seenSignals(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT signals FROM decision ORDER BY at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		m := map[string]string{}
		_ = json.Unmarshal([]byte(body), &m)
		for k := range m {
			seen[k] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, "signal."+k)
	}
	sort.Strings(out)
	return out, nil
}

// activityRow is one decision as the activity view reads it: the four columns
// that view aggregates and nothing else.
type activityRow struct {
	Action  string
	Agency  string
	Refusal string
	Hits    []hit
}

// recent reads the last n decisions WITH their evidence, in ONE query.
//
// The activity view used to page the decisions and then call decisionDetail per
// row — 501 queries for a 500-row page, on an ungated read. The hits are a
// column of the decision row, so there was never a second query to make.
func recent(db *sql.DB, n int) ([]activityRow, error) {
	rows, err := db.Query(`SELECT action, agency, refusal, hits FROM decision ORDER BY at DESC, id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []activityRow
	for rows.Next() {
		var a activityRow
		var hitsJSON string
		if err := rows.Scan(&a.Action, &a.Agency, &a.Refusal, &hitsJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(hitsJSON), &a.Hits)
		out = append(out, a)
	}
	return out, rows.Err()
}

func decisionDetail(db *sql.DB, id string) (decisionRow, []hit, []byte, string, error) {
	var d decisionRow
	var shadow, strained int
	var hitsJSON, causesJSON, digest string
	err := db.QueryRow(`SELECT id, at, stage, kind, subject, action, score, agency, shadow, refusal, label, hits, causes, digest, since, strained
		FROM decision WHERE id = ?`, id).
		Scan(&d.ID, &d.At, &d.Stage, &d.Kind, &d.Subject, &d.Action, &d.Score, &d.Agency,
			&shadow, &d.Refusal, &d.Label, &hitsJSON, &causesJSON, &digest, &d.Since, &strained)
	if errors.Is(err, sql.ErrNoRows) {
		return d, nil, nil, "", zip.ErrNotFound("no such decision")
	}
	if err != nil {
		return d, nil, nil, "", err
	}
	d.Shadow, d.Strained = shadow != 0, strained != 0
	var hits []hit
	_ = json.Unmarshal([]byte(hitsJSON), &hits)
	return d, hits, []byte(causesJSON), digest, nil
}

// label records the outcome a human concluded. The decider is SERVER-SET from
// the validated principal and never taken off the wire: the engine's own
// resolutions carry an unauthenticated `by`, and importing that gap into a
// product that BILLS on the decision would make the attribution worthless.
func label(db *sql.DB, id, verdict, by string) error {
	res, err := db.Exec(`UPDATE decision SET label = ?, label_by = ?, label_at = ? WHERE id = ?`,
		verdict, by, stamp(time.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return zip.ErrNotFound("no such decision")
	}
	return nil
}

var labels = map[string]bool{"fraud": true, "legitimate": true, "chargeback": true, "abuse": true, "unknown": true}

// ── model state + searches ──────────────────────────────────────────────────

func putModel(db *sql.DB, id string, body []byte) error {
	_, err := db.Exec(`INSERT INTO model (id, body, at) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET body = excluded.body, at = excluded.at`,
		id, string(body), stamp(time.Now()))
	return err
}

func getModel(db *sql.DB, id string) ([]byte, error) {
	var body string
	err := db.QueryRow(`SELECT body FROM model WHERE id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, zip.ErrNotFound("no snapshot")
	}
	return []byte(body), err
}

func putSearch(db *sql.DB, id, status string, body []byte) error {
	_, err := db.Exec(`INSERT INTO search (id, at, status, body) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET status = excluded.status, body = excluded.body`,
		id, stamp(time.Now()), status, string(body))
	return err
}

func getSearch(db *sql.DB, id string) (string, []byte, error) {
	var status, body string
	err := db.QueryRow(`SELECT status, body FROM search WHERE id = ?`, id).Scan(&status, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, zip.ErrNotFound("no such search")
	}
	return status, []byte(body), err
}

// ── settings ────────────────────────────────────────────────────────────────

// mode is the tenant's live/shadow switch. SHADOW IS THE DEFAULT and the default
// is not configurable: a model that quietly went live and started declining
// payments is the worst failure available here, so going live is an act somebody
// performs and can be asked about.
func mode(db *sql.DB) string {
	var v string
	if err := db.QueryRow(`SELECT value FROM setting WHERE key = 'mode'`).Scan(&v); err != nil || v != "live" {
		return "shadow"
	}
	return "live"
}

func setSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT INTO setting (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func getSetting(db *sql.DB, key string) string {
	var v string
	_ = db.QueryRow(`SELECT value FROM setting WHERE key = ?`, key).Scan(&v)
	return v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ── small conversions shared across the package ─────────────────────────────

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	case uint64:
		return float64(n)
	case uint32:
		return float64(n)
	case uint16:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func sortStrings(s []string) { sort.Strings(s) }

// emit sends the ANALYTICS COPY of an already-durable record onto the platform
// bus, through analytics.PublishEvents — the ONE way a product event reaches
// that plane, because the package that owns the stream, the subject grammar and
// the envelope must be the only publisher of all three.
//
// It runs LAST and it is fail-soft, deliberately. The door answers
// {accepted, dropped} and the anonymous lane drops BY DESIGN, so it is a stream
// to watch and never the record to keep: the decision is already in the tenant's
// file and in the audit chain before this line runs. Wired the other way, a bus
// hiccup loses evidence and the loss is invisible.
// It is also DETACHED. PublishEvents dials the bus and publishes inline, and
// analytics itself only ever calls it from a goroutine for exactly that reason
// ("a slow bus costs a goroutine and never an ingest"). On this package's hot
// path the equivalent would be a bus dial inside a card processor's
// authorization window — the record is already durable by the time this runs, so
// there is nothing for the caller to wait for.
func emit(org, name string, props map[string]any) {
	if org == "" {
		return
	}
	ev := detach(org, name, props)
	go analytics.PublishEvents(ev.DistinctID, []analytics.SinkEvent{ev})
}

// detach builds the event as a SELF-CONTAINED VALUE: nothing it returns points
// at memory the request owns.
//
// THIS IS THE WHOLE REASON emit IS SAFE TO RUN IN A GOROUTINE. fasthttp owns the
// byte buffers behind header values and the request path and REUSES them for the
// next request on the connection, so `sc.org` (X-Org-Id), `by(sc)` (X-User-Id)
// and every path parameter are strings pointing at memory the server is about to
// overwrite. Publishing them from a goroutine that outlives the request marshals
// whatever the NEXT request wrote there — on a shared pod, another tenant's user
// id under this tenant's DistinctID. It is a data race under -race and a silent
// wrong record without it.
//
// OWNERSHIP IS TAKEN HERE AND NOWHERE ELSE, for the same reason the wire door is
// one middleware and not a rule per field: a caller cannot be asked to remember
// which of its strings came off the request, and the caller that forgets is the
// one that reopens the hole. The copy is O(the event), once per governance write
// or decision, against a bus dial — it is not on any budget worth counting.
func detach(org, name string, props map[string]any) analytics.SinkEvent {
	return analytics.SinkEvent{
		MessageID:  newID("ev"),
		Name:       name,
		DistinctID: strings.Clone(org),
		Time:       time.Now().UTC(),
		Properties: own(props),
	}
}

// own returns a copy of the properties that shares no memory with the caller —
// neither the map, which the caller may reuse, nor the text inside it.
//
// It recurs through the shapes JSON can carry rather than only the ones emit
// happens to pass today. A property added next year as a []string would
// otherwise arrive aliased with the fix already in the file, which is the defect
// pattern this package keeps meeting: a rule written for the fields that exist.
func own(v any) map[string]any {
	m, _ := ownValue(v).(map[string]any)
	return m
}

func ownValue(v any) any {
	switch t := v.(type) {
	case string:
		return strings.Clone(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[strings.Clone(k)] = ownValue(val)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(t))
		for k, val := range t {
			out[strings.Clone(k)] = strings.Clone(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = ownValue(val)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i, val := range t {
			out[i] = strings.Clone(val)
		}
		return out
	default:
		// Numbers, booleans, times and nil are copied by assignment: there is no
		// backing array for the server to reuse underneath them.
		return v
	}
}
