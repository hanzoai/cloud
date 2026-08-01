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
	"sync"
	"time"

	"github.com/hanzoai/cloud"
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
`

// shelf holds the lazily-opened per-tenant handles. A file is opened, migrated
// and seeded once on first touch and cached by tenant key. Opens are serialised
// so a concurrent first touch opens exactly once.
type shelf struct {
	dataDir string

	mu  sync.Mutex
	dbs map[Tenant]*sql.DB
}

func newShelf(dataDir string) *shelf { return &shelf{dataDir: dataDir, dbs: map[Tenant]*sql.DB{}} }

// open resolves the tenant's file. The ORG half is what cloud.OrgDB takes — it
// does its own brand scoping through DataDir and the deployment, so handing it
// the qualified key would put the brand in the path twice.
func (s *shelf) open(t Tenant) (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if db, ok := s.dbs[t]; ok {
		return db, nil
	}
	db, err := cloud.OrgDB(s.dataDir, t.org(), "", "risk")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := seed(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.dbs[t] = db
	return db, nil
}

func (s *shelf) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, db := range s.dbs {
		_ = db.Close()
		delete(s.dbs, k)
	}
}

// tenants lists the tenants this process currently holds open. Used by the
// shutdown path to snapshot every resident model — a rollout must not silently
// reset every tenant to warming.
func (s *shelf) tenants() []Tenant {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tenant, 0, len(s.dbs))
	for t := range s.dbs {
		out = append(out, t)
	}
	return out
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

func putDecision(db *sql.DB, o observation, out outcome, digest string) error {
	hits, _ := json.Marshal(out.hits)
	causes, _ := json.Marshal(out.causes)
	signals, _ := json.Marshal(o.signals)
	_, err := db.Exec(`INSERT INTO decision
		(id, at, stage, kind, subject, action, score, agency, shadow, refusal, amount, currency, direction, idem, hits, causes, signals, digest)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		out.id, stamp(o.at), o.stage, o.kind, o.subject, out.action, out.score, out.agency,
		boolInt(out.shadow), out.refusal, o.amount, o.currency, o.direction,
		"", string(hits), string(causes), string(signals), digest)
	return err
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

func claimIdem(db *sql.DB, id, idem string) error {
	if idem == "" {
		return nil
	}
	_, err := db.Exec(`UPDATE decision SET idem = ? WHERE id = ?`, idem, id)
	return err
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
	q := `SELECT id, at, stage, kind, subject, action, score, agency, shadow, refusal, label FROM decision`
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
			&d.Agency, &shadow, &d.Refusal, &d.Label); err != nil {
			return nil, err
		}
		d.Shadow = shadow != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

type decisionRow struct {
	ID, At, Stage, Kind, Subject, Action, Agency, Refusal, Label string
	Score                                                        float64
	Shadow                                                       bool
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

func decisionDetail(db *sql.DB, id string) (decisionRow, []hit, []byte, string, error) {
	var d decisionRow
	var shadow int
	var hitsJSON, causesJSON, digest string
	err := db.QueryRow(`SELECT id, at, stage, kind, subject, action, score, agency, shadow, refusal, label, hits, causes, digest
		FROM decision WHERE id = ?`, id).
		Scan(&d.ID, &d.At, &d.Stage, &d.Kind, &d.Subject, &d.Action, &d.Score, &d.Agency,
			&shadow, &d.Refusal, &d.Label, &hitsJSON, &causesJSON, &digest)
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
func emit(org, name string, props map[string]any) {
	if org == "" {
		return
	}
	analytics.PublishEvents(org, []analytics.SinkEvent{{
		MessageID:  newID("ev"),
		Name:       name,
		DistinctID: org,
		Time:       time.Now().UTC(),
		Properties: props,
	}})
}
