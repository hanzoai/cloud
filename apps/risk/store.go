package risk

// store.go is the tenant's record plane: decisions, rules, lists, suppressions,
// controls and the model's snapshot, in the tenant's OWN encrypted SQLite file.
//
// THE FILE IS THE TENANT BOUNDARY. cloud.OrgStore resolves
// {DataDir}/orgs/{orgSlug}/risk.db through the injective SanitizeOrg slugger, so
// two distinct orgs can never share a file and no segment can traverse out of
// DataDir. There is no `org` column on any table here, and there cannot be a
// cross-tenant read, because there is no statement that could express one. It is
// also what makes the file DURABLE rather than merely local — see ship.go.
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
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/analytics"
	"github.com/hanzoai/namespace"
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
	-- The model VERSION that decided, empty when the shipped model did. The
	-- digest beside it names a GEOMETRY, and two versions of one shape share
	-- one — so the digest alone cannot answer "which model version declined
	-- this customer", which is the question an adverse action has to answer.
	fit         TEXT NOT NULL DEFAULT '',
	label       TEXT NOT NULL DEFAULT '',
	label_by    TEXT NOT NULL DEFAULT '',
	label_at    TEXT NOT NULL DEFAULT ''
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

CREATE TABLE IF NOT EXISTS fit (
	id      TEXT PRIMARY KEY,
	at      TEXT NOT NULL,
	by      TEXT NOT NULL DEFAULT '',
	algo    TEXT NOT NULL,
	shape   TEXT NOT NULL DEFAULT '{}',
	source  TEXT NOT NULL DEFAULT '{}',
	metrics TEXT NOT NULL DEFAULT '{}',
	profile TEXT NOT NULL DEFAULT '{}',
	digest  TEXT NOT NULL DEFAULT '',
	role    TEXT NOT NULL DEFAULT 'candidate',
	status  TEXT NOT NULL DEFAULT 'queued',
	refusal TEXT NOT NULL DEFAULT '',
	served  TEXT NOT NULL DEFAULT '',
	retired TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS fit_at ON fit(at DESC);
-- ONE champion and at most ONE challenger, enforced at the index and not in the
-- code that writes it. Two concurrent promotions then lose at the index rather
-- than both succeeding, and there is no path — a bug, a retry, a race — that can
-- leave a tenant with two models both believing they decide.
CREATE UNIQUE INDEX IF NOT EXISTS fit_champion ON fit(role) WHERE role = 'champion';
CREATE UNIQUE INDEX IF NOT EXISTS fit_challenger ON fit(role) WHERE role = 'challenger';

CREATE TABLE IF NOT EXISTS fit_move (
	id      TEXT PRIMARY KEY,
	fit     TEXT NOT NULL,
	at      TEXT NOT NULL,
	by      TEXT NOT NULL DEFAULT '',
	was     TEXT NOT NULL,
	now     TEXT NOT NULL,
	reason  TEXT NOT NULL DEFAULT '',
	stood   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS fit_move_fit ON fit_move(fit, at);

-- The challenger's answer to a decision the champion already made. It is its own
-- table and not a column on the decision, because the decision is the RECORD of
-- what was decided, and amending a record after the fact with what something
-- else would have decided makes it a record that can be rewritten.
CREATE TABLE IF NOT EXISTS challenge (
	decision  TEXT PRIMARY KEY,
	at        TEXT NOT NULL,
	champion  TEXT NOT NULL DEFAULT '',
	incumbent REAL NOT NULL DEFAULT 0,
	fit       TEXT NOT NULL,
	score     REAL NOT NULL DEFAULT 0,
	cut       REAL NOT NULL DEFAULT 0,
	alert     INTEGER NOT NULL DEFAULT 0,
	scored    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS challenge_fit ON challenge(fit, at DESC);

CREATE TABLE IF NOT EXISTS drift (
	id      TEXT PRIMARY KEY,
	fit     TEXT NOT NULL,
	at      TEXT NOT NULL,
	says    TEXT NOT NULL,
	rows    INTEGER NOT NULL DEFAULT 0,
	scored  INTEGER NOT NULL DEFAULT 0,
	stated  REAL NOT NULL DEFAULT 0,
	cleared TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS drift_fit ON drift(fit, cleared);
`

// plane is one tenant's record file, as cloud.OrgStore holds it. The store
// hands out a freshly-opened, pragma'd, encrypted-at-rest handle and takes back
// something that owns its Close; the migration runs here, once per file.
type plane struct{ db *sql.DB }

func (p *plane) Close() error { return p.db.Close() }

func openPlane(db *sql.DB) (*plane, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	if err := widen(db); err != nil {
		return nil, err
	}
	if err := seed(db); err != nil {
		return nil, err
	}
	return &plane{db: db}, nil
}

// shelf is this app's per-tenant record planes, held by cloud.OrgStore.
//
// IT IS OrgStore AND NOT A MAP OVER OrgDB, AND THAT IS THE DURABILITY. OrgDB
// opens a file; OrgStore is the fleet's per-org file door — the same one
// apps/research, apps/books and fifteen others walk through — and it is where
// three properties live that a hand-rolled map does not have: the org's durable
// snapshot is HYDRATED into the local file before it is opened, the ha election
// decides WHETHER THIS REPLICA MAY WRITE it, and Sync ships the file back
// FENCED at the lease round. cloud deploys Recreate at one replica, so without
// them an ungraceful termination loses every acknowledged decision since the
// volume was last intact and nothing hydrates it back. This app was the only one
// in the fleet still opening OrgDB directly.
//
// The namespace is minted here and nowhere else in this package, through the one
// door cloud publishes: the only way to build one is from a validated org, so a
// file this app opens cannot be addressed by anything a caller sent. The ORG
// half is what OrgNamespace takes — cloud does its own brand scoping through
// DataDir and the deployment, so handing it the qualified key would put the
// brand in the path twice.
type shelf struct {
	stores *cloud.OrgStore[*plane]
	pier   *pier

	mu    sync.Mutex
	known map[Tenant]namespace.Namespace
	// marks is the row-change count each tenant's file carried at its last
	// ACKNOWLEDGED ship — the whole of the "did this request write anything"
	// question. See ship.go's changed().
	marks map[Tenant]int64
}

func newShelf(b cloud.Base) *shelf {
	s := &shelf{
		stores: cloud.NewOrgStore(b, "risk", openPlane),
		known:  map[Tenant]namespace.Namespace{},
		marks:  map[Tenant]int64{},
	}
	s.pier = newPier(s.shipOnce)
	return s
}

// open resolves the tenant's file, hydrating and migrating it on first touch.
func (s *shelf) open(t Tenant) (*sql.DB, error) {
	ns, err := cloud.OrgNamespace(t.org(), "")
	if err != nil {
		return nil, err
	}
	p, err := s.stores.For(ns)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.known[t] = ns
	s.mu.Unlock()
	return p.db, nil
}

// shipOnce ships ONE tenant's file to its durable object, fenced. It is the
// pier's engine and the only caller — everything else asks the pier, which
// coalesces, so a thousand concurrent writes cost one ship rather than a
// thousand.
func (s *shelf) shipOnce(t Tenant) (bool, error) {
	s.mu.Lock()
	ns, ok := s.known[t]
	s.mu.Unlock()
	if !ok {
		return false, fmt.Errorf("risk: a tenant's file was shipped before it was opened")
	}
	return s.stores.Sync(ns)
}

func (s *shelf) close() {
	_ = s.stores.CloseAll()
	s.pier.reset()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.known = map[Tenant]namespace.Namespace{}
	s.marks = map[Tenant]int64{}
}

// tenants lists the tenants this process currently holds open. Used by the
// shutdown path to snapshot every resident model — a rollout must not silently
// reset every tenant to warming.
func (s *shelf) tenants() []Tenant {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tenant, 0, len(s.known))
	for t := range s.known {
		out = append(out, t)
	}
	return out
}

// widen adds the columns a file created by an earlier shape of this schema does
// not have. `CREATE TABLE IF NOT EXISTS` converges a MISSING table and says
// nothing about a table that exists with fewer columns, so this is the other
// half of "a fresh file and an existing one converge".
//
// Idempotent by outcome rather than by dialect: SQLite has no ADD COLUMN IF NOT
// EXISTS, so the one error it can return for a column already present is read
// and treated as the success it is. Anything else is a file this process must
// not serve from.
func widen(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE decision ADD COLUMN fit TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(stmt); err != nil && !hasColumn(err) {
			return err
		}
	}
	return nil
}

// hasColumn reports the one error ADD COLUMN returns for a column that is
// already there. Read off the message rather than a driver code, for the same
// reason isUnique is: this package must not become the one place that knows
// which SQLite driver is linked.
func hasColumn(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

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

func loadRules(db *sql.DB) ([]rule, error) {
	rows, err := db.Query(`SELECT body FROM rule ORDER BY id`)
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
	_, err = db.Exec(`INSERT INTO rule (id, body, at) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET body = excluded.body, at = excluded.at`,
		r.ID, string(body), stamp(time.Now()))
	return err
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
func loadLists(db *sql.DB) (map[string]map[string]bool, error) {
	rows, err := db.Query(`SELECT list, value FROM entry`)
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

func loadSuppressions(db *sql.DB) ([]suppression, error) {
	rows, err := db.Query(`SELECT id, rule, kind, subject, until, reason, by, at FROM suppression ORDER BY at DESC`)
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
	_, err := db.Exec(`INSERT INTO suppression (id, rule, kind, subject, until, reason, by, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Rule, s.Kind, s.Subject, stamp(s.Until), s.Reason, s.By, stamp(s.At))
	return err
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
	q += ` ORDER BY at DESC`
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
	_, err := db.Exec(`INSERT INTO control (id, kind, subject, control, rate, until, reason, by, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Kind, c.Subject, c.Control, c.Rate, stamp(c.Until), c.Reason, c.By, stamp(c.At))
	return err
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
func putDecision(db *sql.DB, o observation, out outcome, digest, fit, idem string) error {
	hits, _ := json.Marshal(out.hits)
	causes, _ := json.Marshal(out.causes)
	signals, _ := json.Marshal(o.signals)
	_, err := db.Exec(`INSERT INTO decision
		(id, at, stage, kind, subject, action, score, agency, shadow, refusal, amount, currency, direction, idem, hits, causes, signals, digest, fit)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		out.id, stamp(o.at), o.stage, o.kind, o.subject, out.action, out.score, out.agency,
		boolInt(out.shadow), out.refusal, o.amount, o.currency, o.direction,
		idem, string(hits), string(causes), string(signals), digest, fit)
	if err != nil && idem != "" && isUnique(err) {
		return errIdemTaken
	}
	return err
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

// activityRow is one decision as the live activity view counts it: the three
// dimensions it tallies and the activations behind them, and nothing else. A
// narrow projection rather than the whole record, because the view aggregates
// hundreds of rows and renders none of them.
type activityRow struct {
	action  string
	agency  string
	refusal string
	hits    []hit
}

// activityRows reads what the activity view aggregates, in ONE statement.
//
// It exists because the view used to read a page and then re-read every row of
// it for its hits: five hundred sequential round trips on the tenant's ONE
// connection (cloud.OrgDB sets MaxOpenConns(1)), which every decision for that
// tenant queues behind — a read that stops the control it is reporting on. The
// hits were on the row the first statement already touched.
func activityRows(db *sql.DB, limit int) ([]activityRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := db.Query(
		`SELECT action, agency, refusal, hits FROM decision ORDER BY at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []activityRow
	for rows.Next() {
		var r activityRow
		var hitsJSON string
		if err := rows.Scan(&r.action, &r.agency, &r.refusal, &hitsJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(hitsJSON), &r.hits)
		out = append(out, r)
	}
	return out, rows.Err()
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
	q := `SELECT id, at, stage, kind, subject, action, score, agency, shadow, refusal, label, fit FROM decision`
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
		var shadow int
		if err := rows.Scan(&d.ID, &d.At, &d.Stage, &d.Kind, &d.Subject, &d.Action, &d.Score,
			&d.Agency, &shadow, &d.Refusal, &d.Label, &d.Fit); err != nil {
			return nil, err
		}
		d.Shadow = shadow != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

type decisionRow struct {
	ID, At, Stage, Kind, Subject, Action, Agency, Refusal, Label string
	// Fit is the model version that decided, empty when the shipped model did.
	Fit    string
	Score  float64
	Shadow bool
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

// replayHistory reads the tenant's OLDEST recorded decisions back into
// replayable form — a model's life from the beginning, which is what a sandbox
// replay wants. For the most recent ones, see replaySince.
func replayHistory(db *sql.DB, limit int) ([]replayed, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := db.Query(replayColumns+` FROM decision ORDER BY at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanReplayed(rows)
}

// replaySince reads the tenant's MOST RECENT decisions after an instant, back in
// chronological order.
//
// It exists because replayHistory takes the OLDEST rows, which is right for a
// sandbox that wants a model's whole life and wrong for anything asking "what
// has happened lately": on a tenant with more history than the cap, the oldest
// page can be entirely before the instant asked about, and the answer is then
// permanently empty. Drift reads through here for exactly that reason.
func replaySince(db *sql.DB, since time.Time, limit int) ([]replayed, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := db.Query(replayColumns+` FROM decision WHERE at > ? ORDER BY at DESC LIMIT ?`,
		stamp(since), limit)
	if err != nil {
		return nil, err
	}
	out, err := scanReplayed(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// replayColumns is the one column list both readers take, so a column added to
// the decode cannot reach one reader and not the other.
const replayColumns = `SELECT id, at, stage, kind, subject, agency, score, amount, currency,
		direction, signals, hits, label`

// scanReplayed decodes rows into replayable form. ONE decoder for both readers.
//
// The facts are reconstituted from what was STORED, not recomputed from today's
// rings: a replay that re-read live aggregates would score a year-old
// transaction against this morning's velocity, which answers a question nobody
// asked. Everything the live path could see is on the row.
func scanReplayed(rows *sql.Rows) ([]replayed, error) {
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

func decisionDetail(db *sql.DB, id string) (decisionRow, []hit, []byte, string, error) {
	var d decisionRow
	var shadow int
	var hitsJSON, causesJSON, digest string
	err := db.QueryRow(`SELECT id, at, stage, kind, subject, action, score, agency, shadow, refusal, label, hits, causes, digest, fit
		FROM decision WHERE id = ?`, id).
		Scan(&d.ID, &d.At, &d.Stage, &d.Kind, &d.Subject, &d.Action, &d.Score, &d.Agency,
			&shadow, &d.Refusal, &d.Label, &hitsJSON, &causesJSON, &digest, &d.Fit)
	if errors.Is(err, sql.ErrNoRows) {
		return d, nil, nil, "", zip.ErrNotFound("no such decision")
	}
	if err != nil {
		return d, nil, nil, "", err
	}
	d.Shadow = shadow != 0
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

// errNoState is "this tenant's file holds no learned state under that key". It
// is a NAMED value rather than a fresh error per call so a caller can tell it
// apart from a file it could not read — one is an answer and the other is a
// retry, and conflating them either silences a control for the life of the
// process or reads the file on every request forever.
var errNoState = zip.ErrNotFound("no snapshot")

func getModel(db *sql.DB, id string) ([]byte, error) {
	var body string
	err := db.QueryRow(`SELECT body FROM model WHERE id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoState
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
	ev := analytics.SinkEvent{
		MessageID:  newID("ev"),
		Name:       name,
		DistinctID: org,
		Time:       time.Now().UTC(),
		Properties: props,
	}
	go analytics.PublishEvents(org, []analytics.SinkEvent{ev})
}
