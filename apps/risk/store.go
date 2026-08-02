package risk

// store.go is the tenant's record plane: decisions, rules, lists, suppressions,
// controls and the model's snapshot, in the tenant's OWN encrypted SQLite file —
// and the registry of live per-tenant state that holds it.
//
// THE FILE IS THE TENANT BOUNDARY. cloud.OrgStore resolves
// {DataDir}/orgs/{orgSlug}/risk.db through the injective SanitizeOrg slugger, so
// two distinct orgs can never share a file and no segment can traverse out of
// DataDir. There is no `org` column on any table here, and there cannot be a
// cross-tenant read, because there is no statement that could express one.
//
// IT IS AN OrgStore AND NOT A BARE OrgDB, and that is the durability of every
// record in it. OrgDB is the encrypted open and nothing more: the pod's volume
// is then the only copy, and cloud deploys Recreate at one replica. OrgStore
// hydrates the org's file from the durable object BEFORE opening it, fences the
// writer against a deposed one at a monotone lease round, and gives ship() the
// ship-before-ack step the two sibling durable planes (apps/research,
// apps/books) already take after every commit. A decision is the record an
// adverse action is defended with; it is not a record if a rollout can lose it.
//
// DURABLE FIRST, ANALYTICS AFTER. Every decision lands here and in the audit
// hash chain BEFORE the analytics copy is emitted to /v1/event. That door is
// best-effort by design — it answers {accepted, dropped} and the anonymous lane
// drops on purpose — so a compliance-grade record that rode it would be lost by
// a bus hiccup, invisibly. The copy is a copy.
//
// ONE CELL PER TENANT, NOTHING SHARED. A cell holds this tenant's own velocity
// rings and its own half-space forest beside its own file, each under its own
// bound (bound.go). No map inside any of them is indexed by more than one
// tenant, so there is no eviction, no read and no write that can cross a tenant
// even by mistake.

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
	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/velocity"
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
	shape       TEXT NOT NULL DEFAULT '',
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

// forward is every column added after the original schema, applied to a file
// that already exists. CREATE TABLE IF NOT EXISTS is a no-op on such a file, so
// without these a tenant that decided anything before the column existed would
// never gain it. A fresh file already has them and the duplicate is the expected
// no-op — the same idiom apps/wallets uses, for the same reason.
var forward = []string{
	`ALTER TABLE decision ADD COLUMN shape TEXT NOT NULL DEFAULT ''`,
}

// migrate applies the forward-adds. Any error that is not a column that is
// already there is fatal: a half-migrated file is a file whose reads mean
// something other than what they say.
func migrate(db *sql.DB) error {
	for _, stmt := range forward {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("risk: %s: %w", stmt, err)
		}
	}
	return nil
}

// plane is one tenant's opened file, which is what the org store holds. It owns
// its Close, which is the OrgStore contract.
type plane struct{ db *sql.DB }

func (p *plane) Close() error { return p.db.Close() }

// openPlane applies the schema, the forward migrations and the starter seed to a
// freshly-opened, pragma'd, cek-encrypted file. It is the OrgStore's open hook,
// so it runs exactly once per file per process.
func openPlane(db *sql.DB) (*plane, error) {
	if _, err := db.Exec(schema + qualitySchema); err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	if err := seed(db); err != nil {
		return nil, err
	}
	return &plane{db: db}, nil
}

// cell is ONE tenant's live state: its file, its own aggregates, its own model
// and when they started.
//
// The ARM is nil until the tenant's first request and nil again after its own
// idleness retires it. Nothing durable lives here — the model is snapshotted to
// the tenant's file before it is ever dropped — so a cell is a cache of exactly
// one tenant's memory, bounded by exactly that tenant's budget.
type cell struct {
	t  Tenant
	db *sql.DB

	mu      sync.Mutex
	vel     *velocity.Store
	model   *anomaly.Store
	since   time.Time
	touched time.Time
}

// arms hands back this tenant's two in-memory planes and the instant they
// started. Nil only for a cell that has been retired and not yet re-armed, which
// no request path can observe: shelf.of arms before it returns.
func (c *cell) arms() (*velocity.Store, *anomaly.Store, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vel, c.model, c.since
}

// strained reports that this tenant's aggregates are at their OWN cardinality
// bound, so a count read from them may under-state this tenant's own traffic.
// Published rather than logged, because the consumer is the tenant: a rule on
// `velocity.ip.1h.count >= 5` stops firing when the key it counts was dropped,
// and there is no other way for the tenant to learn its threshold is being
// measured against a partial ring.
func (c *cell) strained() bool {
	vel, _, _ := c.arms()
	return vel != nil && vel.Keys() >= maxKeys()
}

// shelf is the bounded registry of live tenants over the durable org store.
//
// Admission, the per-tenant bound, the reclaim and the model reload each have
// exactly ONE site here, so a new op cannot reach a tenant's state by another
// route and skip one of them.
type shelf struct {
	stores *cloud.OrgStore[*plane]
	log    logger

	mu    sync.Mutex
	cells map[Tenant]*cell
	// refused counts admissions turned away at the ceiling, and refusedAt is
	// when the last one was. Both are on the probe: a pod that is refusing
	// tenants must page an operator rather than be found in a support ticket.
	refused   int64
	refusedAt time.Time
}

// logger is the slice of the service's logger this file uses. Narrow on purpose:
// the shelf is reached from teardown and from a background sweep as well as from
// a request, and taking the whole service would be reaching for state it has no
// business touching.
type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

func newShelf(b cloud.Base) *shelf {
	return &shelf{
		stores: cloud.NewOrgStore[*plane](b, "risk", openPlane),
		log:    b.Log,
		cells:  map[Tenant]*cell{},
	}
}

// errFull is the capacity refusal. 503 and not 403: nothing about the caller is
// wrong, this pod is out of room, and a retry against a pod with room succeeds.
var errFull = zip.Errorf(503, "this node is at its tenant capacity and will not evict another tenant's state to make room")

// of resolves the caller's cell, admitting and arming it when this process has
// not seen it. Every op reaches its tenant through here.
//
// AT THE CEILING A NEW TENANT IS REFUSED, never admitted by dropping an
// incumbent. The sweep runs first, so room that a tenant's own idleness has
// freed is taken before anybody is turned away — but the only thing that can
// ever free a ring is the silence of the tenant that owns it.
func (s *shelf) of(t Tenant) (*cell, error) {
	now := time.Now()
	s.mu.Lock()
	c, held := s.cells[t]
	if !held {
		if len(s.cells) >= tenantMax() {
			s.retireLocked(now, idleReclaim())
		}
		if len(s.cells) >= tenantMax() {
			s.refused, s.refusedAt = s.refused+1, now
			s.mu.Unlock()
			s.log.Error("risk: refusing a new tenant — this node is at its tenant ceiling and will not evict an incumbent to make room",
				"tenant", t.String(), "tenants", tenantMax())
			return nil, errFull
		}
		// Touched at BIRTH, so a sweep between this line and the tenant's first
		// answer cannot retire a cell that has not served a request yet.
		c = &cell{t: t, touched: now}
		s.cells[t] = c
	}
	s.mu.Unlock()

	c.mu.Lock()
	c.touched = now
	c.mu.Unlock()

	if err := s.openCell(c); err != nil {
		s.abandon(t, c, held)
		return nil, err
	}
	if err := s.arm(c); err != nil {
		s.abandon(t, c, held)
		return nil, err
	}
	return c, nil
}

// abandon drops a cell THIS call created and could not bring up. Only that one:
// a cell an earlier call published is another request's tenant, and removing it
// because this one failed would be the cross-tenant reach in miniature.
func (s *shelf) abandon(t Tenant, c *cell, held bool) {
	if held {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cells[t] != c {
		return
	}
	delete(s.cells, t)
}

// openCell resolves the tenant's file once, through the org store. The ORG half
// is what cloud.OrgNamespace takes — the deployment does its own brand scoping
// through DataDir, so handing it the qualified key would put the brand in the
// path twice.
func (s *shelf) openCell(c *cell) error {
	c.mu.Lock()
	if c.db != nil {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	ns, err := cloud.OrgNamespace(c.t.org(), "")
	if err != nil {
		return err
	}
	p, err := s.stores.For(ns)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.db = p.db
	c.mu.Unlock()
	return nil
}

// arm gives a cell its aggregates and its model and restores what the tenant had
// learned. Idempotent: an already-armed cell is untouched.
//
// THE RELOAD IS HERE AND ONLY HERE. A "restored" latch held beside the model —
// which is what this package had — outlives the model it describes: a model
// dropped by anything is then never reloaded, and the tenant scores nothing for
// the rest of the process's life while reporting only that it is warming. A cell
// that has no model has no latch either, because the latch IS the model.
func (s *shelf) arm(c *cell) error {
	c.mu.Lock()
	if c.vel != nil && c.model != nil {
		c.mu.Unlock()
		return nil
	}
	db := c.db
	c.mu.Unlock()

	vel := aggregates()
	model, err := forest(anomaly.Config{}, vel)
	if err != nil {
		return err
	}
	// Read before the cell is armed, so a concurrent arm cannot see a
	// half-restored model. No snapshot is the normal first run.
	if err := restore(db, model, c.t); err != nil {
		s.log.Warn("risk: a tenant's learned state could not be restored; it starts warming",
			"tenant", c.t.String(), "err", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vel != nil && c.model != nil { // lost the race; the winner's arm stands
		return nil
	}
	c.vel, c.model, c.since = vel, model, time.Now().UTC()
	return nil
}

// open is the file-only door, for the paths that need the record plane and not
// the arms.
func (s *shelf) open(t Tenant) (*sql.DB, error) {
	c, err := s.of(t)
	if err != nil {
		return nil, err
	}
	return c.db, nil
}

// commit is the ONE way a write to a tenant's record plane is acknowledged: the
// write runs against that tenant's own file and the file ships before the call
// returns.
//
// The two steps are one function so that "written" and "durable" cannot come
// apart at a call site. A write followed by a remembered ship is a rule; a write
// that IS a ship is a shape — and the failure it rules out is the one nobody
// sees, where a plane answers 200, a rollout takes the pod, and the record the
// answer promised is not there.
func (s *shelf) commit(t Tenant, write func(db *sql.DB) error) error {
	c, err := s.of(t)
	if err != nil {
		return err
	}
	if err := write(c.db); err != nil {
		return err
	}
	return s.ship(t)
}

// ship is the ship-before-ack step: it names the same file the write went to and
// ships THAT one, fenced at the lease round, so a write and its ship can never
// address different files.
//
// A ship that is not acknowledged is an ERROR and never a warning. Unacked means
// this pod is not the org's elected writer or was deposed mid-request, so the
// local row is not the org's record — answering 200 over it would tell a caller
// a decision is on file when a takeover will not find it. On a local-only
// deployment (no object store configured) Sync acks trivially and this costs
// nothing.
func (s *shelf) ship(t Tenant) error {
	ns, err := cloud.OrgNamespace(t.org(), "")
	if err != nil {
		return err
	}
	acked, err := s.stores.Sync(ns)
	if err != nil {
		return fmt.Errorf("risk: this record was written locally and not shipped, so it is not durable yet: %w", err)
	}
	if !acked {
		return zip.Errorf(503, "this record was written locally but this node is not this organisation's elected writer, "+
			"so it is not durable; retry — an idempotency key makes the retry exact")
	}
	return nil
}

// retireLocked releases the arms of every tenant that has been SILENT for at
// least idle: its model is written to its own file first, then its aggregates
// and its forest go and its cell is dropped. Caller holds s.mu.
//
// IT IS NOT EVICTION, AND THE DIFFERENCE IS THE WHOLE POINT. The trigger is the
// retired tenant's own silence, so no tenant's traffic can ever cost another
// tenant a ring. Nothing is lost either: idle is floored at the longest window
// (bound.go), so every ring of every key this tenant owns has already rotated to
// zero, and the model is snapshotted before it is dropped.
//
// The FILE is not closed. The org store owns that handle and hands the same one
// back on the tenant's next request, which is also why a search worker holding a
// db for its whole budget cannot be handed a closed file by a sweep.
func (s *shelf) retireLocked(now time.Time, idle time.Duration) {
	for t, c := range s.cells {
		c.mu.Lock()
		if now.Sub(c.touched) < idle || c.model == nil {
			c.mu.Unlock()
			continue
		}
		if err := keep(c.db, c.t, c.model); err != nil {
			// Never drop a model we could not write down: the cell is HELD and the
			// next sweep tries again. Losing learned state to a housekeeping pass is
			// exactly the silent disarm this file exists to prevent.
			s.log.Error("risk: a retiring tenant's learned state was not kept, so its cell is held", "tenant", t.String(), "err", err)
			c.mu.Unlock()
			continue
		}
		idleFor := now.Sub(c.touched)
		c.vel, c.model = nil, nil
		c.mu.Unlock()
		delete(s.cells, t)
		s.log.Info("risk: a tenant was retired after its own idleness; its learned state is on its own file",
			"tenant", t.String(), "idle", idleFor.String())
	}
}

// sweep is the background retire. It runs on a timer so memory comes back from a
// silent tenant without waiting for a busy one to need it — a version that only
// reclaimed under pressure would make one tenant's arrival the reason another's
// rings went away, which is the thing being ruled out.
func (s *shelf) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retireLocked(time.Now(), idleReclaim())
}

// close snapshots every armed tenant and closes every file. It is what makes a
// rollout survivable: cloud deploys Recreate at one replica, so every deploy
// drops the process, and a model that comes back with nothing learned declines
// to score for its whole warm period.
func (s *shelf) close() (kept, failed int) {
	s.mu.Lock()
	for t, c := range s.cells {
		c.mu.Lock()
		if c.model != nil {
			if err := keep(c.db, c.t, c.model); err != nil {
				failed++
				s.log.Error("risk: a tenant's learned state was not kept", "tenant", t.String(), "err", err)
			} else {
				kept++
			}
		}
		c.vel, c.model, c.db = nil, nil, nil
		c.mu.Unlock()
		delete(s.cells, t)
	}
	s.mu.Unlock()
	// CloseAll ships each file to its durable object one last time, so the state
	// a rollout drops is the state the next process hydrates.
	if err := s.stores.CloseAll(); err != nil {
		s.log.Error("risk: an org store did not close cleanly", "err", err)
	}
	return kept, failed
}

// tenants lists the tenants this process currently holds armed, in a stable
// order.
func (s *shelf) tenants() []Tenant {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tenant, 0, len(s.cells))
	for t := range s.cells {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// strained names the tenants whose own aggregates are at their own cardinality
// bound. Each one is degrading ITSELF and nobody else, which is the property the
// bound exists for — and it is still worth saying out loud, because the tenant's
// own rules are now reading a partial ring.
func (s *shelf) strained() []string {
	s.mu.Lock()
	cells := make([]*cell, 0, len(s.cells))
	for _, c := range s.cells {
		cells = append(cells, c)
	}
	s.mu.Unlock()
	out := []string{}
	for _, c := range cells {
		if c.strained() {
			out = append(out, c.t.String())
		}
	}
	sort.Strings(out)
	return out
}

// count reports how many tenants this process holds, how many admissions it has
// refused and when the last refusal was. All three are on the probe.
func (s *shelf) count() (tenants int, refused int64, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cells), s.refused, s.refusedAt
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

// putDecision records the decision AND ITS GRADING, and CLAIMS the idempotency
// key, in ONE transaction.
//
// Writing the row and then claiming the key in a second statement leaves a
// window: two concurrent requests carrying the same key both find no row, both
// insert, and the second one's claim then violates the unique index — so a
// caller that retried correctly gets a 500. Here the key is part of the insert,
// so the loser of the race is refused by the index BEFORE a second decision
// exists, and the caller reads the winner's answer back. Which is what an
// idempotency key promises: one decision, one set of counters moved.
//
// The unique index is PARTIAL —
//
//	WHERE idem != ''
//
// — so decisions made without a key do not collide with each other. (The
// predicate is an indented block rather than prose because gofmt rewrites a
// doubled apostrophe inside a doc sentence into a curly quote, and a predicate
// nobody can paste into sqlite is not documentation.)
//
// THE VERDICT IS IN THE SAME TRANSACTION, and that is the whole reason this
// takes one. Two independent statements can land one and not the other, and the
// half that lands is the half that acts: a BLOCK with no probability, no
// principal reasons and nothing saying why — which is exactly what an
// adverse-action regime does not allow, served on the surface a disputing
// customer's packet is built from. Either both rows exist or neither does.
//
// SHAPE is recorded beside the score because a score is a coordinate and a
// coordinate means nothing without the system it was taken in. It is what lets a
// later fit read only the rows it can honestly fit, instead of stamping today's
// coordinates on history taken under yesterday's.
func putDecision(db *sql.DB, o observation, out outcome, digest, shape, idem string, v verdict) error {
	hits, _ := json.Marshal(out.hits)
	causes, _ := json.Marshal(out.causes)
	signals, _ := json.Marshal(o.signals)
	reasons, err := json.Marshal(v.reasons)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`INSERT INTO decision
		(id, at, stage, kind, subject, action, score, agency, shadow, refusal, amount, currency, direction, idem, hits, causes, signals, digest, shape)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		out.id, stamp(o.at), o.stage, o.kind, o.subject, out.action, out.score, out.agency,
		boolInt(out.shadow), out.refusal, o.amount, o.currency, o.direction,
		idem, string(hits), string(causes), string(signals), digest, shape,
	); err != nil {
		if idem != "" && isUnique(err) {
			return errIdemTaken
		}
		return err
	}
	var p any
	if v.probability != nil {
		p = *v.probability
	}
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO verdict (decision, at, probability, calibration, policy, reasons, refusal)
		 VALUES (?,?,?,?,?,?,?)`,
		out.id, stamp(o.at), p, v.calibration, v.policy, string(reasons), v.refusal,
	); err != nil {
		return err
	}
	return tx.Commit()
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
