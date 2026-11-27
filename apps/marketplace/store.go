package marketplace

// store.go — ONE database for the deployment (a file, or on hanzo-sql one schema),
// holding what is sold, what is bought and what the parties said about it:
//
//	listings   an org's offer of one thing it owns — an agent, persona, app,
//	           skill, MCP server or tool — or of a person's time, optionally priced
//	installs   which org switched a listed tool on, for the listing's count
//	jobs       two-sided work one org buys from another, and every step of it
//	feedback   what each party of a settled job said about the other
//	payouts    the wallet each seller proved it controls, and the challenge it
//	           signed to prove it
//
// It is one database and not one per org because nothing here belongs to one org
// alone: the shop is every public listing, a job is a record between two orgs,
// and feedback is about one party written by the other. Every read is keyed by
// the org it is for — a publisher reads its own listings, a job only by its
// parties — and never answered to another.
//
// Its SQL is the dialect both engines share (sdk/dialect), and its tests run
// on both (internal/sqltest).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/tenure"

	// locus.Open is the ONE opener: it names the database in the system namespace
	// and opens it, as a schema on hanzo-sql when CLOUD_SQL_DSN is set and otherwise
	// as the encrypted SQLite file under the key cek derives from the process master,
	// with the single-connection cap that file's atomicity rests on.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/authz"
	"github.com/hanzoai/namespace"
)

// The kinds of thing a listing sells. The set is closed: a kind is how the shop
// is faceted and how ownership is proved, so an unknown one is refused.
const (
	KindAgent      = "agent"      // an agent of the seller's org, hired for jobs
	KindPersona    = "persona"    // a named agent voice of the seller's org, hired like an agent
	KindApp        = "app"        // a project the seller's org builds on the platform
	KindSkill      = "skill"      // a skill the seller's org authored
	KindMCP        = "mcp"        // an MCP server the seller's org registered
	KindTool       = "tool"       // one platform tool every org reaches, sold per call by the platform
	KindExpert     = "expert"     // a person in the seller's org who makes their time available, hired for jobs
	KindCapability = "capability" // a trained Kai capability adapter over Kai's shared representation
)

// Kinds is every kind, in the order the shop facets them.
var Kinds = []string{KindAgent, KindPersona, KindApp, KindSkill, KindMCP, KindTool, KindExpert, KindCapability}

// Listing is one marketplace offer: a thing the publishing org owns, surfaced for
// discovery and purchase, optionally monetized (Price>0) into a payout Recipient
// wallet of that org. Isolation is the (publisher_org, id) key — a publisher only
// ever mutates its OWN listings.
//
// PublisherOrg is also the PAYEE org: a listing is paid into a wallet of the org
// that published it and no other, which is what makes a cross-org credit
// unconstructible rather than merely unlikely (wallets resolves an id only within
// the org that is asked for).
type Listing struct {
	// ID is the listing's id, minted here as "lst_" + 16 hex characters. A
	// publisher cannot choose it: Create overwrites whatever arrives. It is unique
	// within PublisherOrg (the primary key is the pair), and it is the path segment
	// DELETE /v1/marketplace/listings/:id takes.
	ID string `json:"id"`
	// PublisherOrg is the org that published the listing, taken from the validated
	// principal and never off the wire. It is also the PAYEE org — Recipient is
	// resolved inside it — and the isolation key: a publisher reads and deletes
	// only rows carrying its own org.
	PublisherOrg string `json:"publisherOrg"`
	// Kind is what the listing sells: agent, persona, app, skill, mcp, tool,
	// expert or capability. Listings published before kinds existed sold tools,
	// and read as tool.
	Kind string `json:"kind"`
	// Tool names the thing sold, as the seller named it: the agent, app, skill,
	// capability or MCP server's name or id, or the tool's registry name. It was
	// proved the seller's own when the listing was published. For a tool or
	// capability it is what install switches on. For an expert it is the person's
	// IAM identity.
	Tool string `json:"tool"`
	// Ref is the id the thing's owning app knows it by, resolved when the listing
	// was published — the agent id for an agent named by its name. A tool's is the
	// x402 resource a dispatch of it is priced by: the source it resolves from and
	// its name, so a price follows the tool that runs and not a name another org's
	// own row may share. A capability's is capability:<org>:<sha256>.
	Ref string `json:"ref"`
	// Title is the shop-window name, required and refused past 200 bytes. It is
	// what discovery paints over the tool's registry name.
	Title string `json:"title"`
	// Description is the long copy. Publish REFUSES one past 4096 bytes rather
	// than truncating it, so what is stored is what was sent; empty is allowed.
	Description string `json:"description"`
	// Category groups the listing in the shop window. Free text — no vocabulary,
	// nothing validates it — and unlike Description it is silently cut to 4096
	// bytes rather than refused. Empty means ungrouped.
	Category string `json:"category"`
	// Price is the exact price, 18 decimal places of USD: per call for a tool or
	// capability, the price a job is offered at for an agent. Zero is free.
	Price money.Amount `json:"price"`
	// Currency is the ISO 4217 code Price is quoted in; Create defaults it to "USD"
	// when the publisher names none. It is a LABEL that travels to the shop window:
	// publish parses Price with money.ParseUSD and the x402 terms carry no
	// currency, so another code here changes what is displayed, not what is charged.
	Currency string `json:"currency"`
	// Recipient is the seller's payout WALLET id in PublisherOrg: the wallet x402
	// pays for a call or a job bought through this listing.
	Recipient string `json:"recipient"`
	// Public is whether other orgs can discover the listing. It also decides
	// ENFORCEMENT: only public rows reach the price table, so a private listing
	// with a price charges nobody. False leaves the row visible to its publisher
	// alone.
	Public bool `json:"public"`
	// Docs is where the listing's documentation lives: an https URL, or a path on
	// docs.hanzo.ai. Empty when the seller named none.
	Docs string `json:"docs,omitempty"`
	// Expert is the person behind a listing of kind expert (expert.go); absent on
	// every other kind.
	Expert *Expert `json:"expert,omitempty"`
	// Capability is the Kai capability metadata behind a listing of kind
	// capability (capability.go); absent on every other kind.
	Capability *CapabilitySpec `json:"capability,omitempty"`
	// CreatedAt is when the listing was published, in Unix SECONDS, minted at
	// insert. Every listing read orders by it descending, so it is the shop's
	// ordering key as well as its age.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when the listing was last edited, unix seconds; its creation
	// when it never was.
	UpdatedAt int64 `json:"updatedAt"`
}

// Store is the marketplace store (one database, the shared cloud store discipline).
type Store struct {
	db *sql.DB
	d  dialect.Dialect

	// fields seals what a person typed (sealed below) and blinds the brief a quote is found by.
	fields *tenure.Fields
	// esc keeps what is a credential in KMS (kept below).
	esc *escrow.Escrow
}

// subsystem names the store: the system namespace's "marketplace". The database and the
// sealer are both made from it, so a column's key and its schema cannot name two things.
const subsystem = "marketplace"

// Two job columns are credentials, and are KMS values (internal/escrow) in the org that wrote
// them, with the column holding the path:
//
//	jobs.payment       the buyer's signed x402 authorization, at
//	                   orgs/<buyer org>/marketplace/<job id>/payment@default; the rail is
//	                   handed it to hold and to settle
//	jobs.delivery_url  the link the seller delivered, which can carry a credential, at
//	                   orgs/<seller org>/marketplace/<job id>/delivery_url@default
//
// A file from before holds them in plaintext or sealed as a field; the open moves them.
var escrowed = []string{"payment", "delivery_url"}

// sealed is every sealed column, by table, with the columns that make the row's key. A
// value is bound to its row, so it cannot be carried to another job or org, and to its
// column, so it cannot be read as another. What the parties said, rests as ciphertext: the
// buyer's brief and the address that signed, the seller's delivery note and the reason of a
// dispute, what a party said in feedback, and the addresses a seller proves a payout wallet
// with: personal data, not secrets. A sealed value is different bytes every time, so the
// one a predicate reads, the brief a quote is found by, has a blind index beside it
// (jobs.brief_blind).
var sealed = []struct {
	table    string
	key, col []string
}{
	{"jobs", []string{"id"}, jobSealed},
	{"feedback", []string{"job", "from_org"}, []string{"comment"}},
	{"payouts", []string{"org"}, []string{"address"}},
	{"challenges", []string{"org"}, []string{"address"}},
}

var jobSealed = []string{"brief", "payer", "delivery_note", "dispute_reason"}

// Open opens (and migrates) the store under dir. On hanzo-sql dir is not read. master is
// cloud.Deps.MasterKey; with none the store does not open, so no value is ever written in
// the clear because a key was missing. k is cloud.Deps.KMS and keeps the credentials; with
// none a payment and a delivery link are neither kept nor read.
func Open(dir string, master []byte, k escrow.KMS) (*Store, error) {
	c, err := tenure.NewCipher(master)
	if err != nil {
		return nil, fmt.Errorf("open marketplace store: %w", err)
	}
	fields, err := c.Fields(namespace.System(), subsystem)
	if err != nil {
		return nil, fmt.Errorf("open marketplace store: %w", err)
	}
	st, err := locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*Store, error) {
		d := dialect.Of(db)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
			if err := migrate(ctx, d, tx); err != nil {
				return err
			}
			// A SQLite file from before the columns were sealed holds the briefs in
			// plaintext, which are blinded here, before the seal below destroys the
			// plaintext they are made from. hanzo-sql tables are born sealed.
			if d.Postgres() {
				return nil
			}
			return blindBriefs(ctx, tx, fields)
		}); err != nil {
			return nil, fmt.Errorf("marketplace: migrate: %w", err)
		}
		// Seal the rest in place, before any read, so the store never reads a value it
		// did not write and cmd/sqlmove copies ciphertext. Upgrade leaves a hanzo-sql
		// table alone. It is a walk in small transactions of its own, after the schema has
		// committed and before the credentials move to KMS, so it can take longer than the
		// minute the schema is held to.
		for _, t := range sealed {
			if _, err := fields.Upgrade(context.Background(), db, t.table, t.key, t.col); err != nil {
				return nil, fmt.Errorf("marketplace: migrate: %w", err)
			}
		}
		s := &Store{db: db, d: d, fields: fields, esc: escrow.New(k, "marketplace")}
		move, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		if err := s.moveToKMS(move); err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open marketplace store: %w", err)
	}
	return st, nil
}

// moveToKMS writes, once, the payments and delivery links a file holds from before KMS
// (plain, or sealed as a field) to KMS and replaces each column with its path. It runs
// outside the schema's transaction, because it calls KMS; it compares what it read, so
// another replica that moved the row first leaves nothing to change.
func (s *Store) moveToKMS(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, buyer_org, seller_org, payment, delivery_url FROM jobs
		  WHERE (payment <> '' AND substr(payment, 1, 4) <> 'kms:') OR (delivery_url <> '' AND substr(delivery_url, 1, 4) <> 'kms:')`)
	if err != nil {
		return fmt.Errorf("marketplace: migrate: %w", err)
	}
	type held struct{ id, buyer, seller, payment, url string }
	var found []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.id, &h.buyer, &h.seller, &h.payment, &h.url); err != nil {
			_ = rows.Close()
			return fmt.Errorf("marketplace: migrate: %w", err)
		}
		found = append(found, h)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("marketplace: migrate: %w", err)
	}
	for _, h := range found {
		payment, err := s.moved(ctx, h.buyer, h.id, "payment", h.payment)
		if err != nil {
			return err
		}
		url, err := s.moved(ctx, h.seller, h.id, "delivery_url", h.url)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET payment = $1, delivery_url = $2 WHERE id = $3 AND payment = $4 AND delivery_url = $5`,
			payment, url, h.id, h.payment, h.url); err != nil {
			return fmt.Errorf("marketplace: migrate: %w", err)
		}
	}
	return nil
}

// moved writes one value a file held before KMS to KMS, in org, and answers what the column
// holds instead. A column that holds a path already, or nothing, is as it is.
func (s *Store) moved(ctx context.Context, org, id, field, stored string) (string, error) {
	if stored == "" || escrow.Held(stored) {
		return stored, nil
	}
	value := stored
	if strings.HasPrefix(stored, "seal:") {
		var err error
		if value, err = s.open("jobs", field, stored, id); err != nil {
			return "", fmt.Errorf("marketplace: migrate: job %s: %w", id, err)
		}
	}
	ref, err := s.esc.Put(ctx, org, id, field, value)
	if err != nil {
		return "", fmt.Errorf("marketplace: migrate: job %s: %w", id, err)
	}
	return ref, nil
}

// blindBriefs gives every brief a file holds in plaintext its blind index, before Upgrade
// seals the brief: afterwards the plaintext is gone.
func blindBriefs(ctx context.Context, tx *sql.Tx, fields *tenure.Fields) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, brief FROM jobs WHERE brief_blind = '' AND brief <> '' AND substr(brief, 1, 5) <> 'seal:'`)
	if err != nil {
		return err
	}
	type held struct{ id, brief string }
	var found []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.id, &h.brief); err != nil {
			_ = rows.Close()
			return err
		}
		found = append(found, h)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, h := range found {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET brief_blind = $1 WHERE id = $2`, fields.Blind("jobs", "brief", h.brief), h.id); err != nil {
			return err
		}
	}
	return nil
}

// seal is the text to store in column of the row key names.
func (s *Store) seal(table, column, plain string, key ...string) (string, error) {
	r := s.fields.Row(table, key...)
	out := r.Seal(column, plain)
	return out, r.Err()
}

// open is the value a stored column of the row key names holds.
func (s *Store) open(table, column, stored string, key ...string) (string, error) {
	r := s.fields.Row(table, key...)
	out := r.Open(column, stored)
	return out, r.Err()
}

// migrate is the whole migration, on the one transaction Dialect.Migrate holds. It
// creates the tables, carries the ones an older release wrote forward, and settles
// what the shop shows.
func migrate(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS listings (
  id            TEXT NOT NULL,
  publisher_org TEXT NOT NULL,
  tool          TEXT NOT NULL,
  title         TEXT NOT NULL DEFAULT '',
  description   TEXT NOT NULL DEFAULT '',
  category      TEXT NOT NULL DEFAULT '',
  price         TEXT NOT NULL DEFAULT '0',
  currency      TEXT NOT NULL DEFAULT 'USD',
  recipient     TEXT NOT NULL DEFAULT '',
  public        BIGINT NOT NULL DEFAULT 0,
  created_at    BIGINT NOT NULL,
  PRIMARY KEY (publisher_org, id)
);
CREATE INDEX IF NOT EXISTS ix_listings_public_tool ON listings(public, tool);`); err != nil {
		return err
	}
	if err := migrateCents(ctx, d, tx); err != nil {
		return err
	}
	if err := carry(ctx, d, tx); err != nil {
		return err
	}
	if err := refold(ctx, tx); err != nil {
		return err
	}
	// A tool listing sells a platform tool, and only the platform publishes one.
	// One published before that was proved — by any org that could reach the tool,
	// and keyed by a name — is withdrawn from the shop and the price table: it was
	// a toll on a tool its publisher never owned. Its publisher still reads it.
	if _, err := tx.ExecContext(ctx, `UPDATE listings SET public = 0 WHERE kind = $1 AND (publisher_org != $2 OR ref NOT LIKE $3)`,
		KindTool, authz.AdminOrg, "tool:%"); err != nil {
		return fmt.Errorf("withdraw unproved tool listings: %w", err)
	}
	return nil
}

// column is one column added to a table after the table was first written.
type column struct{ name, def string }

// carry carries the listings and jobs tables forward and creates everything
// added beside them. `CREATE TABLE IF NOT EXISTS` never reaches a table that
// exists, so each column added to one is added here, asked of the table first,
// and carries the DEFAULT an existing row takes: a listing published before kinds
// existed sold a tool, and a job opened before attempts were kept has none.
func carry(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	if err := add(ctx, d, tx, "listings", []column{
		{"kind", "TEXT NOT NULL DEFAULT 'tool'"},
		{"ref", "TEXT NOT NULL DEFAULT ''"},
		{"docs", "TEXT NOT NULL DEFAULT ''"},
		{"updated_at", "BIGINT NOT NULL DEFAULT 0"},
		{"folded", "TEXT NOT NULL DEFAULT ''"},
		{"rate", "TEXT NOT NULL DEFAULT '0'"},
		{"areas", "TEXT NOT NULL DEFAULT ''"},
		{"languages", "TEXT NOT NULL DEFAULT ''"},
		{"timezone", "TEXT NOT NULL DEFAULT ''"},
		{"availability", "TEXT NOT NULL DEFAULT ''"},
		{"person", "TEXT NOT NULL DEFAULT ''"},
		{"verified", "BIGINT NOT NULL DEFAULT 0"},
		{"skills", "TEXT NOT NULL DEFAULT '[]'"},
		{"links", "TEXT NOT NULL DEFAULT '[]'"},
		{"capability_spec", "TEXT NOT NULL DEFAULT ''"},
	}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS ix_listings_shop ON listings(public, kind, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS ix_listings_person ON listings(publisher_org, person) WHERE kind = 'expert';
CREATE INDEX IF NOT EXISTS ix_listings_ref ON listings(public, ref);
CREATE INDEX IF NOT EXISTS ix_listings_org ON listings(publisher_org, created_at);

CREATE TABLE IF NOT EXISTS installs (
  org     TEXT NOT NULL,
  project TEXT NOT NULL,
  tool    TEXT NOT NULL,
  at      BIGINT NOT NULL,
  PRIMARY KEY (org, project, tool)
);
CREATE INDEX IF NOT EXISTS ix_installs_tool ON installs(tool);

CREATE TABLE IF NOT EXISTS jobs (
  id             TEXT PRIMARY KEY,
  listing        TEXT NOT NULL DEFAULT '',
  title          TEXT NOT NULL,
  buyer_org      TEXT NOT NULL,
  seller_org     TEXT NOT NULL,
  amount         TEXT NOT NULL,
  currency       TEXT NOT NULL DEFAULT 'USD',
  category       TEXT NOT NULL,
  rail           TEXT NOT NULL,
  status         TEXT NOT NULL,
  brief          TEXT NOT NULL DEFAULT '',
  performed      TEXT NOT NULL DEFAULT '',
  deadline       BIGINT NOT NULL DEFAULT 0,
  review         BIGINT NOT NULL,
  recipient      TEXT NOT NULL,
  clearance      TEXT NOT NULL,
  payment        TEXT NOT NULL DEFAULT '',
  payer          TEXT NOT NULL DEFAULT '',
  valid_before   BIGINT NOT NULL DEFAULT 0,
  network        TEXT NOT NULL DEFAULT '',
  asset          TEXT NOT NULL DEFAULT '',
  pay_to         TEXT NOT NULL DEFAULT '',
  settlement     TEXT NOT NULL DEFAULT '',
  delivery_note  TEXT NOT NULL DEFAULT '',
  delivery_url   TEXT NOT NULL DEFAULT '',
  delivery_hash  TEXT NOT NULL DEFAULT '',
  delivered_at   BIGINT NOT NULL DEFAULT 0,
  dispute_reason TEXT NOT NULL DEFAULT '',
  disputed_by    TEXT NOT NULL DEFAULT '',
  disputed_at    BIGINT NOT NULL DEFAULT 0,
  created_at     BIGINT NOT NULL,
  updated_at     BIGINT NOT NULL,
  tries          BIGINT NOT NULL DEFAULT 0,
  retry_at       BIGINT NOT NULL DEFAULT 0,
  ending         TEXT NOT NULL DEFAULT '',
  contested      BIGINT NOT NULL DEFAULT 0,
  attempt        TEXT NOT NULL DEFAULT '',
  asked          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_jobs_buyer ON jobs(buyer_org, created_at);
CREATE INDEX IF NOT EXISTS ix_jobs_seller ON jobs(seller_org, created_at);
CREATE INDEX IF NOT EXISTS ix_jobs_listing ON jobs(listing, status);
CREATE INDEX IF NOT EXISTS ix_jobs_status ON jobs(status, updated_at);

CREATE TABLE IF NOT EXISTS job_steps (
  job    TEXT NOT NULL,
  seq    BIGINT NOT NULL,
  status TEXT NOT NULL,
  at     BIGINT NOT NULL,
  by     TEXT NOT NULL,
  PRIMARY KEY (job, seq)
);

CREATE TABLE IF NOT EXISTS feedback (
  job      TEXT NOT NULL,
  from_org TEXT NOT NULL,
  about    TEXT NOT NULL,
  listing  TEXT NOT NULL DEFAULT '',
  rating   BIGINT NOT NULL,
  comment  TEXT NOT NULL DEFAULT '',
  at       BIGINT NOT NULL,
  PRIMARY KEY (job, from_org)
);
CREATE INDEX IF NOT EXISTS ix_feedback_about ON feedback(about);
CREATE INDEX IF NOT EXISTS ix_feedback_listing ON feedback(listing, about);

CREATE TABLE IF NOT EXISTS payouts (
  org      TEXT PRIMARY KEY,
  wallet   TEXT NOT NULL,
  address  TEXT NOT NULL,
  bound_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS challenges (
  org     TEXT PRIMARY KEY,
  wallet  TEXT NOT NULL,
  address TEXT NOT NULL,
  message TEXT NOT NULL,
  expires BIGINT NOT NULL
);`); err != nil {
		return err
	}
	if err := add(ctx, d, tx, "jobs", []column{
		{"attempt", "TEXT NOT NULL DEFAULT ''"},
		{"asked", "TEXT NOT NULL DEFAULT ''"},
		{"brief_blind", "TEXT NOT NULL DEFAULT ''"},
	}); err != nil {
		return err
	}
	// One job per attempt of a buyer's: the attempt is how a hire sent again finds
	// the job it made, and two jobs under one would make it ambiguous.
	if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS ix_jobs_attempt ON jobs(buyer_org, attempt) WHERE attempt != ''`); err != nil {
		return err
	}
	return nil
}

// add adds each column table does not have yet.
func add(ctx context.Context, d dialect.Dialect, tx *sql.Tx, table string, cols []column) error {
	for _, c := range cols {
		have, err := d.HasColumn(ctx, tx, table, c.name)
		if err != nil {
			return fmt.Errorf("inspect schema: %w", err)
		}
		if have {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+c.name+` `+c.def); err != nil {
			return fmt.Errorf("add %s.%s: %w", table, c.name, err)
		}
	}
	return nil
}

// folded is what the shop searches a listing by: its words, lowercased here, in
// Go, the way a query's words are. SQLite's lower() folds ASCII alone, so a title
// with a capital outside it — "Élan" — was not found by its own words.
func folded(l Listing) string {
	words := []string{l.Title, l.Description, l.Category, l.Tool, l.PublisherOrg}
	if l.Expert != nil {
		words = append(words, l.Expert.Areas...)
		words = append(words, l.Expert.Languages...)
		words = append(words, l.Expert.Skills...)
	}
	if l.Capability != nil {
		words = append(words, l.Capability.QuestionKey, l.Capability.QuestionType, l.Capability.Form, l.Capability.License, l.Capability.Representation)
		words = append(words, l.Capability.Labels...)
	}
	return strings.ToLower(strings.Join(words, " "))
}

// refold gives every listing written before the shop searched folded words its
// folded words. Idempotent by its own WHERE: a folded listing is never empty.
func refold(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT publisher_org, id, title, description, category, tool FROM listings WHERE folded = ''`)
	if err != nil {
		return fmt.Errorf("refold: %w", err)
	}
	var ls []Listing
	for rows.Next() {
		var l Listing
		if err := rows.Scan(&l.PublisherOrg, &l.ID, &l.Title, &l.Description, &l.Category, &l.Tool); err != nil {
			_ = rows.Close()
			return fmt.Errorf("refold: %w", err)
		}
		ls = append(ls, l)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("refold: %w", err)
	}
	for _, l := range ls {
		if _, err := tx.ExecContext(ctx, `UPDATE listings SET folded = $1 WHERE publisher_org = $2 AND id = $3`, folded(l), l.PublisherOrg, l.ID); err != nil {
			return fmt.Errorf("refold %s: %w", l.ID, err)
		}
	}
	return nil
}

// migrateCents carries a pre-exact store forward: `price_cents INTEGER` becomes
// `price TEXT` holding the 18-decimal magnitude money.Amount stores. One cent is
// 10^16 atto, so the conversion is exact in both directions and the old column is
// dropped in the same transaction — there is no window where two columns each claim
// to be the price. A store that never had price_cents is left alone.
//
// The `price` column is ADDED HERE, not by the CREATE above. `CREATE TABLE IF NOT
// EXISTS` is a no-op against a table that already exists, so a store written before
// prices were exact arrives with price_cents and no price at all — and the UPDATE
// would fail on a column that does not exist, failing Open, failing Mount, and
// stopping the binary on every deployment that ever published a listing. Adding the
// column is the migration's first statement for exactly that reason.
func migrateCents(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	legacy, err := d.HasColumn(ctx, tx, "listings", "price_cents")
	if err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	if !legacy {
		return nil
	}
	exact, err := d.HasColumn(ctx, tx, "listings", "price")
	if err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}

	stmts := []string{
		`UPDATE listings SET price = CAST(price_cents AS TEXT) || '0000000000000000' WHERE price_cents > 0`,
		`ALTER TABLE listings DROP COLUMN price_cents`,
	}
	if !exact {
		stmts = append([]string{`ALTER TABLE listings ADD COLUMN price TEXT NOT NULL DEFAULT '0'`}, stmts...)
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate price (%s): %w", stmt, err)
		}
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// ── listings ──────────────────────────────────────────────────────────────────

// columns is the listing projection, written once so every read decodes the same
// shape in the same order as query's Scan.
const columns = `id, publisher_org, kind, tool, ref, title, description, category, price, currency, recipient, public, docs, created_at, updated_at,
	rate, areas, languages, timezone, availability, person, verified, skills, links, capability_spec`

// Create inserts a listing (id generated).
func (s *Store) Create(ctx context.Context, l Listing) (Listing, error) {
	l.ID = "lst_" + randHex(8)
	l.CreatedAt = time.Now().Unix()
	l.UpdatedAt = l.CreatedAt
	if l.Currency == "" {
		l.Currency = "USD"
	}
	if l.Kind == "" {
		l.Kind = KindTool
	}
	p := l.Expert.columns()
	cSpec := l.Capability.column()
	if err := dialect.Text(l.PublisherOrg, l.Tool, l.Ref, l.Title, l.Description, l.Category, l.Currency, l.Recipient, l.Docs,
		p.areas, p.languages, p.timezone, p.availability, p.person, p.skills, p.links, cSpec); err != nil {
		return Listing{}, fmt.Errorf("marketplace: create: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO listings (`+columns+`, folded) VALUES (`+dialect.Marks(1, 26)+`)`,
		l.ID, l.PublisherOrg, l.Kind, l.Tool, l.Ref, l.Title, l.Description, l.Category, l.Price.AttoString(),
		l.Currency, l.Recipient, boolInt(l.Public), l.Docs, l.CreatedAt, l.UpdatedAt,
		p.rate, p.areas, p.languages, p.timezone, p.availability, p.person, p.verified, p.skills, p.links, cSpec, folded(l)); err != nil {
		return Listing{}, fmt.Errorf("marketplace: create: %w", err)
	}
	return l, nil
}

// Update rewrites a listing's editable fields, keyed by (publisher_org, id). It
// reports whether a row was changed. An expert's person and verified are not
// editable: the first is who published the listing, the second is staff's word
// (Verify).
func (s *Store) Update(ctx context.Context, l Listing) (bool, error) {
	l.UpdatedAt = time.Now().Unix()
	p := l.Expert.columns()
	cSpec := l.Capability.column()
	if err := dialect.Text(l.Tool, l.Ref, l.Title, l.Description, l.Category, l.Currency, l.Recipient, l.Docs,
		p.areas, p.languages, p.timezone, p.availability, p.skills, p.links, cSpec); err != nil {
		return false, fmt.Errorf("marketplace: update: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE listings SET title=$1, description=$2, category=$3, price=$4, currency=$5, recipient=$6, public=$7, docs=$8, updated_at=$9,
		 rate=$10, areas=$11, languages=$12, timezone=$13, availability=$14, skills=$15, links=$16, capability_spec=$17, folded=$18 WHERE publisher_org=$19 AND id=$20`,
		l.Title, l.Description, l.Category, l.Price.AttoString(), l.Currency, l.Recipient, boolInt(l.Public), l.Docs,
		l.UpdatedAt, p.rate, p.areas, p.languages, p.timezone, p.availability, p.skills, p.links, cSpec, folded(l), l.PublisherOrg, l.ID)
	if err != nil {
		return false, fmt.Errorf("marketplace: update: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Get returns one of org's own listings.
func (s *Store) Get(ctx context.Context, org, id string) (Listing, bool, error) {
	rows, err := s.query(ctx, `SELECT `+columns+` FROM listings WHERE publisher_org=$1 AND id=$2`, org, id)
	if err != nil || len(rows) == 0 {
		return Listing{}, false, err
	}
	return rows[0], true, nil
}

// Public returns one public listing by id, whoever published it.
func (s *Store) Public(ctx context.Context, id string) (Listing, bool, error) {
	rows, err := s.query(ctx, `SELECT `+columns+` FROM listings WHERE public=1 AND id=$1`, id)
	if err != nil || len(rows) == 0 {
		return Listing{}, false, err
	}
	return rows[0], true, nil
}

// experts returns the public expert listings, newest first, at most limit.
func (s *Store) experts(ctx context.Context, limit int) ([]Listing, error) {
	return s.query(ctx, `SELECT `+columns+` FROM listings WHERE public=1 AND kind=$1 ORDER BY created_at DESC, id LIMIT $2`, KindExpert, limit)
}

// ListByOrg returns a publisher's own listings, newest first.
func (s *Store) ListByOrg(ctx context.Context, org string) ([]Listing, error) {
	return s.query(ctx, `SELECT `+columns+` FROM listings WHERE publisher_org=$1 ORDER BY created_at DESC`, org)
}

// search narrows the shop: every field is a filter, and an empty one keeps all.
type search struct {
	words    []string // each, lowercased, must appear in the title, description, category, thing or seller
	seller   string
	area     string // an expert's area
	kind     string
	category string
	price    string // free or priced
	rating   int    // at least this mean rating
}

// facet is one dimension of the shop, as SQL groups it and filters it. Each
// facet's counts leave its own filter out, so a buyer sees what choosing another
// value would give.
type facet struct {
	name  string
	group string // the expression a listing's value is
	where string // the filter, %s where the value's mark goes
	value any
	on    bool
}

// likeEscape makes s match only itself inside a LIKE pattern written with ESCAPE '\'.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// rated is every public listing with its sellers' ratings from settled jobs'
// buyers — the table the shop searches, faceted and paged in the store, never in
// memory, so no listing is past any window.
const rated = `WITH r AS (SELECT f.listing AS id, COUNT(*) AS n, SUM(f.rating) AS s FROM feedback f
	JOIN jobs j ON j.id = f.job AND f.about = j.seller_org GROUP BY f.listing),
l AS (SELECT listings.*, COALESCE(r.n, 0) AS n, COALESCE(r.s, 0) AS s FROM listings LEFT JOIN r ON r.id = listings.id
	WHERE listings.public = 1) `

// catalog is one page of the shop: the listings matched, newest first, how many
// matched in all, and the facets.
func (s *Store) catalog(ctx context.Context, q search, limit, offset int) ([]Listing, int, Facets, error) {
	out := Facets{Kind: map[string]int{}, Category: map[string]int{}, Price: map[string]int{}, Rating: map[string]int{}}
	priced := `CASE WHEN price = '0' AND rate = '0' THEN 'free' ELSE 'priced' END`
	facets := []facet{
		{"kind", `kind`, `kind = %s`, q.kind, q.kind != ""},
		{"category", `category`, `category = %s`, q.category, q.category != ""},
		{"price", priced, priced + ` = %s`, q.price, q.price != ""},
		{"rating", `CASE WHEN n = 0 THEN 'unrated' ELSE CAST(CAST(ROUND(1.0 * s / n) AS INTEGER) AS TEXT) END`, `n > 0 AND s >= %s * n`,
			q.rating, q.rating > 0},
	}
	// where is the search with every facet's filter but skip's. A word is searched for
	// in the folded words and an area in the list of them, as a pattern that means only
	// itself.
	where := func(skip string) (string, *dialect.Args) {
		var b dialect.Args
		conds := []string{"1 = 1"}
		for _, w := range q.words {
			conds = append(conds, `folded LIKE `+b.Mark("%"+likeEscape(w)+"%")+` ESCAPE '\'`)
		}
		if q.seller != "" {
			conds = append(conds, `publisher_org = `+b.Mark(q.seller))
		}
		if q.area != "" {
			conds = append(conds, `(',' || areas || ',') LIKE `+b.Mark("%"+likeEscape(","+q.area+",")+"%")+` ESCAPE '\'`)
		}
		for _, f := range facets {
			if f.on && f.name != skip {
				conds = append(conds, fmt.Sprintf(f.where, b.Mark(f.value)))
			}
		}
		return ` WHERE ` + strings.Join(conds, " AND "), &b
	}
	sinks := map[string]map[string]int{"kind": out.Kind, "category": out.Category, "price": out.Price, "rating": out.Rating}
	for _, f := range facets {
		w, b := where(f.name)
		rows, err := s.db.QueryContext(ctx, rated+`SELECT `+f.group+`, COUNT(*) FROM l`+w+` GROUP BY 1`, b.Values()...)
		if err != nil {
			return nil, 0, out, fmt.Errorf("marketplace: shop facets: %w", err)
		}
		for rows.Next() {
			var v sql.NullString
			var n int
			if err := rows.Scan(&v, &n); err != nil {
				_ = rows.Close()
				return nil, 0, out, fmt.Errorf("marketplace: shop facets: %w", err)
			}
			if v.String != "" {
				sinks[f.name][v.String] = n
			}
		}
		if err := rows.Close(); err != nil {
			return nil, 0, out, fmt.Errorf("marketplace: shop facets: %w", err)
		}
	}
	w, b := where("")
	var total int
	if err := s.db.QueryRowContext(ctx, rated+`SELECT COUNT(*) FROM l`+w, b.Values()...).Scan(&total); err != nil {
		return nil, 0, out, fmt.Errorf("marketplace: shop: %w", err)
	}
	page, err := s.query(ctx, rated+`SELECT `+columns+` FROM l`+w+` ORDER BY created_at DESC, id LIMIT `+b.Mark(limit)+` OFFSET `+b.Mark(offset),
		b.Values()...)
	if err != nil {
		return nil, 0, out, err
	}
	return page, total, out, nil
}

// counted is how many listings org has published.
func (s *Store) counted(ctx context.Context, org string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM listings WHERE publisher_org = $1`, org).Scan(&n); err != nil {
		return 0, fmt.Errorf("marketplace: count listings: %w", err)
	}
	return n, nil
}

// PublicByTool returns the cheapest public tool listing per resource — the
// shop-window overlay discovery paints onto the catalog, keyed as a dispatch is
// priced. Only the platform's tool listings are in it.
func (s *Store) PublicByTool(ctx context.Context) (map[string]Listing, error) {
	rows, err := s.query(ctx, `SELECT `+columns+` FROM listings WHERE public=1 AND kind=$1 AND publisher_org=$2`, KindTool, authz.AdminOrg)
	if err != nil {
		return nil, err
	}
	out := map[string]Listing{}
	for _, l := range rows {
		if cur, ok := out[l.Ref]; !ok || l.Price.Cmp(cur.Price) < 0 {
			out[l.Ref] = l
		}
	}
	return out, nil
}

// CheapestPublicForTool returns the cheapest public MONETIZED tool listing for a
// tool resource — the row the x402 price table answers from. One indexed lookup
// (ix_listings_ref covers public+ref) on the dispatch hot path. Only the platform
// sells a tool per call, so only its rows are read: a row any other org holds was
// never proved and prices nothing.
//
// The minimum is taken in Go, on money.Amount, not in SQL. An 18-decimal price is
// stored as its exact magnitude and $10 is 10^19 — past int64 — so ORDER BY CAST(…
// AS INTEGER) would silently mis-order the expensive half of the shop. Money is
// compared by the type that owns comparison, in one place, always.
//
// A query failure is RETURNED, never swallowed: the payment gate must fail closed on
// a blip, not conclude the tool is free.
func (s *Store) CheapestPublicForTool(ctx context.Context, resource string) (Listing, bool, error) {
	rows, err := s.query(ctx, `SELECT `+columns+` FROM listings WHERE public=1 AND ref=$1 AND kind=$2 AND publisher_org=$3`,
		resource, KindTool, authz.AdminOrg)
	if err != nil {
		return Listing{}, false, err
	}
	var best Listing
	found := false
	for _, l := range rows {
		if l.Price.Sign() <= 0 {
			continue // a free listing never shadows a priced one
		}
		if !found || l.Price.Cmp(best.Price) < 0 {
			best, found = l, true
		}
	}
	return best, found, nil
}

// CheapestPublicForCapability returns the cheapest public MONETIZED capability
// listing for a capability resource (capability:<org>:<sha256>) — the row the
// x402 price table answers from when a monetized capability fires.
func (s *Store) CheapestPublicForCapability(ctx context.Context, resource string) (Listing, bool, error) {
	rows, err := s.query(ctx, `SELECT `+columns+` FROM listings WHERE public=1 AND ref=$1 AND kind=$2`,
		resource, KindCapability)
	if err != nil {
		return Listing{}, false, err
	}
	var best Listing
	found := false
	for _, l := range rows {
		if l.Price.Sign() <= 0 {
			continue
		}
		if !found || l.Price.Cmp(best.Price) < 0 {
			best, found = l, true
		}
	}
	return best, found, nil
}

// PublicCapability returns a public capability listing matching toolOrID (by
// listing ID, capability name, or capability:<org>:<sha256> ref), newest first.
func (s *Store) PublicCapability(ctx context.Context, toolOrID string) (Listing, bool, error) {
	rows, err := s.query(ctx, `SELECT `+columns+` FROM listings WHERE public=1 AND kind=$1 AND (id=$2 OR tool=$3 OR ref=$4) ORDER BY created_at DESC LIMIT 1`,
		KindCapability, toolOrID, toolOrID, toolOrID)
	if err != nil || len(rows) == 0 {
		return Listing{}, false, err
	}
	return rows[0], true, nil
}

// Delete removes a listing for (publisher_org, id). Reports whether a row was removed.
func (s *Store) Delete(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM listings WHERE publisher_org=$1 AND id=$2`, org, id)
	if err != nil {
		return false, fmt.Errorf("marketplace: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Listing, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("marketplace: query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Listing
	for rows.Next() {
		var l Listing
		var pub int
		var price string
		var p expertColumns
		var cSpec string
		if err := rows.Scan(&l.ID, &l.PublisherOrg, &l.Kind, &l.Tool, &l.Ref, &l.Title, &l.Description, &l.Category,
			&price, &l.Currency, &l.Recipient, &pub, &l.Docs, &l.CreatedAt, &l.UpdatedAt,
			&p.rate, &p.areas, &p.languages, &p.timezone, &p.availability, &p.person, &p.verified, &p.skills, &p.links, &cSpec); err != nil {
			return nil, err
		}
		if l.Price, err = money.ParseInt(price); err != nil {
			return nil, fmt.Errorf("marketplace: listing %s has an unreadable price %q: %w", l.ID, price, err)
		}
		if l.Kind == KindExpert {
			if l.Expert, err = p.expert(); err != nil {
				return nil, fmt.Errorf("marketplace: listing %s has an unreadable rate: %w", l.ID, err)
			}
		}
		if l.Kind == KindCapability {
			if l.Capability, err = parseCapabilityColumn(cSpec); err != nil {
				return nil, fmt.Errorf("marketplace: listing %s has an unreadable capability spec: %w", l.ID, err)
			}
		}
		if l.UpdatedAt == 0 {
			l.UpdatedAt = l.CreatedAt
		}
		l.Public = pub != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

// ── installs ──────────────────────────────────────────────────────────────────

// installed records that org switched tool on in project, or off.
func (s *Store) installed(ctx context.Context, org, project, tool string, on bool) error {
	q, args := `DELETE FROM installs WHERE org=$1 AND project=$2 AND tool=$3`, []any{org, project, tool}
	if on {
		q, args = `INSERT INTO installs (org, project, tool, at) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, append(args, time.Now().Unix())
	}
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("marketplace: install record: %w", err)
	}
	return nil
}

// ── jobs ──────────────────────────────────────────────────────────────────────

// jobRow is one job as the store holds it: the Job its parties read, and what
// only the marketplace keeps — the signed payment and the clearance behind it.
type jobRow struct {
	Job
	recipient string // the seller's payout wallet id the rail pays
	payment   string // the buyer's signed authorization, the PAYMENT-SIGNATURE value; on a quote, the last one presented. A KMS value: the reads that serve a job to act on it fill it
	payer     string // the address that signed it
	until     int64  // when the authorization stops being acceptable
	review    int64  // seconds after delivery before the job releases itself
	ending    string // the unpaid ending decided and not yet reached, as stored; empty for none
	contested bool   // the clock disputed it, delivered, because its payment stopped clearing
	asked     string // the digest of the hire that quoted it (asked), which its attempt must ask again

	paymentRef string // the path the row holds in place of payment
}

// ending is an unpaid ending a job was given and has not yet reached: the rail is
// returning what the job set aside, and until it has, the job takes no other step.
type ending struct {
	// To is declined, cancelled or refunded.
	To string `json:"to"`
	// By is whose act it was.
	By string `json:"by"`
	// Detail is what the trail records of the act.
	Detail map[string]any `json:"detail,omitempty"`
}

// end reads the ending a job was given.
func (j jobRow) end() (ending, error) {
	var e ending
	if err := json.Unmarshal([]byte(j.ending), &e); err != nil || e.To == "" {
		return e, fmt.Errorf("marketplace: job %s has an unreadable ending: %q", j.ID, j.ending)
	}
	return e, nil
}

const jobCols = `id, listing, title, buyer_org, seller_org, amount, currency, category, rail, status, brief, performed,
	deadline, review, recipient, clearance, payment, payer, valid_before, network, asset, pay_to, settlement,
	delivery_note, delivery_url, delivery_hash, delivered_at, dispute_reason, disputed_by, disputed_at, created_at, updated_at, ending, contested,
	attempt, asked`

func (s *Store) scanJob(sc interface{ Scan(...any) error }) (jobRow, error) {
	var j jobRow
	var amount string
	var d Delivery
	var dp Dispute
	err := sc.Scan(&j.ID, &j.Listing, &j.Title, &j.BuyerOrg, &j.SellerOrg, &amount, &j.Currency, &j.Category, &j.Escrow.Rail,
		&j.Status, &j.Brief, &j.Performed, &j.Deadline, &j.review, &j.recipient, &j.Clearance, &j.paymentRef, &j.payer, &j.until,
		&j.Escrow.Network, &j.Escrow.Contract, &j.Escrow.PayTo, &j.Escrow.TxHash,
		&d.Note, &d.urlRef, &d.Hash, &d.At, &dp.Reason, &dp.By, &dp.At, &j.CreatedAt, &j.UpdatedAt, &j.ending, &j.contested,
		&j.Attempt, &j.asked)
	if err != nil {
		return j, err
	}
	r := s.fields.Row("jobs", j.ID)
	j.Brief, j.payer = r.Open("brief", j.Brief), r.Open("payer", j.payer)
	d.Note, dp.Reason = r.Open("delivery_note", d.Note), r.Open("dispute_reason", dp.Reason)
	if err := r.Err(); err != nil {
		return j, fmt.Errorf("marketplace: job %s: %w", j.ID, err)
	}
	if j.ending != "" {
		e, err := j.end()
		if err != nil {
			return j, err
		}
		j.Ending = e.To
	}
	if j.Amount, err = money.ParseInt(amount); err != nil {
		return j, fmt.Errorf("marketplace: job %s has an unreadable amount: %w", j.ID, err)
	}
	j.Review = j.review
	if d.At > 0 {
		j.Delivery = &d
	}
	if dp.At > 0 {
		dp.Contested = j.contested
		j.Dispute = &dp
	}
	return j, nil
}

// putJob inserts a new job with its first step, and runs after in the same
// transaction.
func (s *Store) putJob(ctx context.Context, j jobRow, by string, after func() error) error {
	if err := dialect.Text(j.ID, j.Listing, j.Title, j.BuyerOrg, j.SellerOrg, j.Currency, j.Category, j.Escrow.Rail, j.Status,
		j.Brief, j.Performed, j.recipient, j.Clearance, j.payment, j.payer, j.Escrow.Network, j.Escrow.Contract, j.Escrow.PayTo,
		j.Escrow.TxHash, j.Attempt, j.asked, by); err != nil {
		return fmt.Errorf("marketplace: put job: %w", err)
	}
	r := s.fields.Row("jobs", j.ID)
	brief, payer := r.Seal("brief", j.Brief), r.Seal("payer", j.payer)
	if err := r.Err(); err != nil {
		return fmt.Errorf("marketplace: put job: %w", err)
	}
	payment, err := s.esc.Put(ctx, j.BuyerOrg, j.ID, "payment", j.payment)
	if err != nil {
		return fmt.Errorf("marketplace: put job: %w", err)
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs (`+jobCols+`, brief_blind) VALUES (`+dialect.Marks(1, 37)+`)`,
			j.ID, j.Listing, j.Title, j.BuyerOrg, j.SellerOrg, j.Amount.AttoString(), j.Currency, j.Category, j.Escrow.Rail,
			j.Status, brief, j.Performed, j.Deadline, j.review, j.recipient, j.Clearance, payment, payer, j.until,
			j.Escrow.Network, j.Escrow.Contract, j.Escrow.PayTo, j.Escrow.TxHash, "", "", "", 0, "", "", 0,
			j.CreatedAt, j.UpdatedAt, "", 0, j.Attempt, j.asked, s.fields.Blind("jobs", "brief", j.Brief)); err != nil {
			_ = s.esc.Drop(ctx, j.BuyerOrg, payment)
			return fmt.Errorf("marketplace: put job: %w", err)
		}
		if err := step(ctx, tx, j.ID, j.Status, by, j.CreatedAt); err != nil {
			return err
		}
		return after()
	})
}

// move takes a job from one of the states in from to next, applying set to the
// row in the same statement, and records the step. It reports whether the job was
// in one of from, with the ending its caller read: a job that moved on first is
// not moved again, so two parties racing one transition cannot both win it, and a
// job given an unpaid ending since it was read takes no step but to that ending.
// Every move clears the ending it was read with, so the one move from a job that
// is ending — to the ending, or to released when the rail says it was paid —
// closes it.
func (s *Store) move(ctx context.Context, tx *sql.Tx, id string, from []string, ending, next, by string, set map[string]any) (bool, error) {
	now := time.Now().Unix()
	// A job that moved starts its clock afresh: the tries the sweep counted were
	// against the state it left.
	var b dialect.Args
	cols := []string{"status = " + b.Mark(next), "updated_at = " + b.Mark(now), "tries = 0", "retry_at = 0", "ending = ''"}
	for _, col := range slices.Sorted(maps.Keys(set)) {
		val := set[col]
		if text, ok := val.(string); ok {
			if err := dialect.Text(text); err != nil {
				return false, fmt.Errorf("marketplace: move job: %s: %w", col, err)
			}
			// A column is written the way it is kept whichever statement names it: sealed,
			// under the row's own binding, or in KMS, in the org that wrote it.
			switch {
			case slices.Contains(jobSealed, col):
				var err error
				if val, err = s.seal("jobs", col, text, id); err != nil {
					return false, fmt.Errorf("marketplace: move job: %s: %w", col, err)
				}
			case slices.Contains(escrowed, col):
				var buyer, seller string
				if err := tx.QueryRowContext(ctx, `SELECT buyer_org, seller_org FROM jobs WHERE id = $1`, id).Scan(&buyer, &seller); err != nil {
					return false, fmt.Errorf("marketplace: move job: %s: %w", col, err)
				}
				owner := buyer
				if col == "delivery_url" {
					owner = seller
				}
				var err error
				if val, err = s.esc.Put(ctx, owner, id, col, text); err != nil {
					return false, fmt.Errorf("marketplace: move job: %s: %w", col, err)
				}
			}
		}
		cols = append(cols, col+" = "+b.Mark(val))
	}
	where := "id = " + b.Mark(id) + " AND ending = " + b.Mark(ending)
	states := make([]string, len(from))
	for i, f := range from {
		states[i] = b.Mark(f)
	}
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET `+strings.Join(cols, ", ")+` WHERE `+where+` AND status IN (`+strings.Join(states, ",")+`)`, b.Values()...)
	if err != nil {
		return false, fmt.Errorf("marketplace: move job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	return true, step(ctx, tx, id, next, by, now)
}

func step(ctx context.Context, tx *sql.Tx, job, status, by string, at int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO job_steps (job, seq, status, at, by)
		VALUES ($1, (SELECT COALESCE(MAX(seq), 0) + 1 FROM job_steps WHERE job = $1), $2, $3, $4)`, job, status, at, by); err != nil {
		return fmt.Errorf("marketplace: job step: %w", err)
	}
	return nil
}

// job reads one job, with its history.
func (s *Store) job(ctx context.Context, id string) (jobRow, bool, error) {
	j, err := s.scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return jobRow{}, false, nil
	}
	if err != nil {
		return jobRow{}, false, fmt.Errorf("marketplace: job: %w", err)
	}
	if j.History, err = s.steps(ctx, id); err != nil {
		return jobRow{}, false, err
	}
	if err := s.reveal(ctx, &j, true); err != nil {
		return jobRow{}, false, err
	}
	return j, true, nil
}

func (s *Store) steps(ctx context.Context, id string) ([]Step, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, at, by FROM job_steps WHERE job = $1 ORDER BY seq`, id)
	if err != nil {
		return nil, fmt.Errorf("marketplace: job steps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Step{}
	for rows.Next() {
		var st Step
		if err := rows.Scan(&st.Status, &st.At, &st.By); err != nil {
			return nil, fmt.Errorf("marketplace: job steps: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// jobsOf lists the jobs org is the buyer or the seller of, newest first: a buyer's
// include its quotes and the ones being funded, which a seller never sees. status
// narrows to one state when not empty.
func (s *Store) jobsOf(ctx context.Context, seller bool, org, status string, limit int) ([]jobRow, error) {
	var b dialect.Args
	var q string
	if seller {
		q = `SELECT ` + jobCols + ` FROM jobs WHERE seller_org = ` + b.Mark(org)
		q += ` AND status NOT IN (` + b.Mark(StatusQuoted) + `, ` + b.Mark(StatusFunding) + `)`
	} else {
		q = `SELECT ` + jobCols + ` FROM jobs WHERE buyer_org = ` + b.Mark(org)
	}
	if status != "" {
		q += ` AND status = ` + b.Mark(status)
	}
	return s.jobs(ctx, false, q+` ORDER BY created_at DESC LIMIT `+b.Mark(limit), b.Values()...)
}

// jobs reads the jobs q selects, each with its history and its delivery link. A job read to
// act on also carries its signed payment (payments), which a listing for a party does not.
func (s *Store) jobs(ctx context.Context, payments bool, q string, args ...any) ([]jobRow, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("marketplace: jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []jobRow{}
	for rows.Next() {
		j, err := s.scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Read once the cursor is closed: a file's one connection serves one query at a time,
	// and a reveal may wait on KMS.
	_ = rows.Close()
	for i := range out {
		if out[i].History, err = s.steps(ctx, out[i].ID); err != nil {
			return nil, err
		}
		if err := s.reveal(ctx, &out[i], payments); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// reveal fills in what a job's row names in KMS: the link it was delivered by, in the
// seller's org, and when payment is asked for, the buyer's signed payment.
func (s *Store) reveal(ctx context.Context, j *jobRow, payment bool) error {
	if j.Delivery != nil {
		url, err := s.esc.Get(ctx, j.SellerOrg, j.Delivery.urlRef)
		if err != nil {
			return fmt.Errorf("marketplace: job %s: %w", j.ID, err)
		}
		j.Delivery.URL = url
	}
	if payment {
		held, err := s.esc.Get(ctx, j.BuyerOrg, j.paymentRef)
		if err != nil {
			return fmt.Errorf("marketplace: job %s: %w", j.ID, err)
		}
		j.payment = held
	}
	return nil
}

// due lists the jobs the clock has work for at now: one given an unpaid ending
// it has not reached, a delivered job past its review window, an open or accepted
// one past its deadline, a dispute the clock raised itself (contested) or one
// past the arbiter's time, a quote nobody paid or whose funding never opened it.
//
// A job whose step failed waits out its backoff (later) and is asked last among
// those due, so jobs that cannot move now — a rail that is down, a chain that is
// not bound, a payment that does not clear — never stand in front of one that
// can. None is ever dropped: a job whose money is set aside is tried, a day apart
// at most, until it ends.
func (s *Store) due(ctx context.Context, now int64) ([]jobRow, error) {
	return s.jobs(ctx, true, `SELECT `+jobCols+` FROM jobs WHERE retry_at <= $1 AND (
		ending != '' OR
		(status = $2 AND delivered_at + review < $3) OR
		(status IN ($4, $5) AND deadline > 0 AND deadline < $6) OR
		(status = $7 AND (contested = 1 OR disputed_at + $8 < $9)) OR
		(status IN ($10, $11) AND created_at + $12 < $13))
		ORDER BY tries, retry_at, updated_at LIMIT $14`,
		now, StatusDelivered, now, StatusOpen, StatusAccepted, now, StatusDisputed, int64(disputeTimeout.Seconds()), now,
		StatusQuoted, StatusFunding, int64(quoteTTL.Seconds()), now, sweepBatch)
}

// decide gives a job in one of the states in from, and ending nowhere yet, the
// unpaid ending e: from here it takes no step but to e, or to released if the rail
// says it was paid. It answers the ending as stored, and whether the job was there
// to be given it.
func decide(ctx context.Context, tx *sql.Tx, id string, from []string, e ending) (string, bool, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return "", false, fmt.Errorf("marketplace: end job: %w", err)
	}
	var bd dialect.Args
	set := `ending = ` + bd.Mark(string(b)) + `, updated_at = ` + bd.Mark(time.Now().Unix())
	where := `id = ` + bd.Mark(id)
	states := make([]string, len(from))
	for i, f := range from {
		states[i] = bd.Mark(f)
	}
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET `+set+` WHERE `+where+` AND ending = '' AND status IN (`+strings.Join(states, ",")+`)`, bd.Values()...)
	if err != nil {
		return "", false, fmt.Errorf("marketplace: end job: %w", err)
	}
	n, _ := res.RowsAffected()
	return string(b), n > 0, nil
}

// later records that the sweep's step on a job failed, and when to try it again:
// a minute after the first failure, doubling to a day.
func (s *Store) later(ctx context.Context, id string, now int64) error {
	const doubled = `(CAST($2 AS BIGINT) << CAST(CASE WHEN tries < 11 THEN tries ELSE 11 END AS INTEGER))`
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET retry_at = $1 + CASE WHEN `+doubled+` < $3 THEN `+doubled+` ELSE $3 END, tries = tries + 1
		WHERE id = $4`, now, int64(time.Minute.Seconds()), int64((24 * time.Hour).Seconds()), id); err != nil {
		return fmt.Errorf("marketplace: defer job: %w", err)
	}
	return nil
}

// quoted is buyer's newest quote for exactly j's terms made since since, under no
// attempt — the one a hire asked again without one answers, rather than clearing
// and quoting the same work anew.
func (s *Store) quoted(ctx context.Context, j jobRow, since int64) (jobRow, bool, error) {
	rows, err := s.jobs(ctx, true, `SELECT `+jobCols+` FROM jobs WHERE buyer_org = $1 AND status IN ($2, $3) AND created_at > $4
		AND attempt = '' AND listing = $5 AND seller_org = $6 AND title = $7 AND brief_blind = $8 AND amount = $9 AND category = $10
		AND deadline = $11 AND review = $12 AND performed = $13 AND rail = $14 AND recipient = $15 ORDER BY created_at DESC LIMIT 1`,
		j.BuyerOrg, StatusQuoted, StatusFunding, since, j.Listing, j.SellerOrg, j.Title, s.fields.Blind("jobs", "brief", j.Brief), j.Amount.AttoString(), j.Category,
		j.Deadline, j.review, j.Performed, j.Escrow.Rail, j.recipient)
	if err != nil || len(rows) == 0 {
		return jobRow{}, false, err
	}
	return rows[0], true, nil
}

// attempted is the job buyer's attempt made, whatever it has come to. No attempt
// made none.
func (s *Store) attempted(ctx context.Context, buyer, attempt string) (jobRow, bool, error) {
	rows, err := s.jobs(ctx, true, `SELECT `+jobCols+` FROM jobs WHERE buyer_org = $1 AND attempt = $2 AND attempt != ''`, buyer, attempt)
	if err != nil || len(rows) == 0 {
		return jobRow{}, false, err
	}
	return rows[0], true, nil
}

// dropQuote deletes a quote nobody opened, and its steps.
func (s *Store) dropQuote(ctx context.Context, id string) error {
	var buyer, ref string
	dropped := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT buyer_org, payment FROM jobs WHERE id = $1 AND status IN ($2, $3)`,
			id, StatusQuoted, StatusFunding).Scan(&buyer, &ref); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("marketplace: drop quote: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE id = $1 AND status IN ($2, $3)`, id, StatusQuoted, StatusFunding)
		if err != nil {
			return fmt.Errorf("marketplace: drop quote: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		dropped = true
		_, err = tx.ExecContext(ctx, `DELETE FROM job_steps WHERE job = $1`, id)
		return err
	})
	if err != nil || !dropped {
		return err
	}
	// The quote is gone, and the authorization it held is not payable by anything.
	return s.esc.Drop(ctx, buyer, ref)
}

// payable reports whether a job is one the rail may be asked to pay: funded and
// not yet ended, or a quote waiting for its payment or being funded by one.
func (s *Store) payable(ctx context.Context, id string) (jobRow, bool, error) {
	j, found, err := s.job(ctx, id)
	if err != nil || !found {
		return jobRow{}, false, err
	}
	switch j.Status {
	case StatusQuoted, StatusFunding, StatusOpen, StatusAccepted, StatusDelivered, StatusDisputed:
		return j, true, nil
	}
	return jobRow{}, false, nil
}

// ── reputation ────────────────────────────────────────────────────────────────

// tally is a reputation's counts before they are rendered.
type tally struct {
	ratings, stars      int64
	installs            int64
	completed, disputed int64
}

// reputations aggregates, for each listing id asked, the feedback its sellers
// were given on jobs bought through it, the orgs that installed its tool and the
// jobs it completed and saw disputed.
func (s *Store) reputations(ctx context.Context, ls []Listing) (map[string]*tally, error) {
	out := map[string]*tally{}
	if len(ls) == 0 {
		return out, nil
	}
	ids := make([]any, 0, len(ls))
	for _, l := range ls {
		out[l.ID] = &tally{}
		ids = append(ids, l.ID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.listing, COUNT(*), SUM(f.rating) FROM feedback f
		JOIN jobs j ON j.id = f.job WHERE f.listing IN (`+dialect.Marks(1, len(ids))+`) AND f.about = j.seller_org GROUP BY f.listing`, ids...)
	if err != nil {
		return nil, fmt.Errorf("marketplace: reputation: %w", err)
	}
	for rows.Next() {
		var id string
		var n, sum int64
		if err := rows.Scan(&id, &n, &sum); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("marketplace: reputation: %w", err)
		}
		out[id].ratings, out[id].stars = n, sum
	}
	_ = rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT listing,
		SUM(CASE WHEN status = $1 THEN 1 ELSE 0 END),
		SUM(CASE WHEN disputed_at > 0 THEN 1 ELSE 0 END)
		FROM jobs WHERE listing IN (`+dialect.Marks(2, len(ids))+`) GROUP BY listing`, append([]any{StatusReleased}, ids...)...)
	if err != nil {
		return nil, fmt.Errorf("marketplace: reputation: %w", err)
	}
	for rows.Next() {
		var id string
		var done, disputed int64
		if err := rows.Scan(&id, &done, &disputed); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("marketplace: reputation: %w", err)
		}
		out[id].completed, out[id].disputed = done, disputed
	}
	_ = rows.Close()
	var tools []any
	for _, l := range ls {
		if l.Kind == KindTool {
			tools = append(tools, l.Tool)
		}
	}
	if len(tools) == 0 {
		return out, nil
	}
	rows, err = s.db.QueryContext(ctx, `SELECT tool, COUNT(DISTINCT org) FROM installs WHERE tool IN (`+
		dialect.Marks(1, len(tools))+`) GROUP BY tool`, tools...)
	if err != nil {
		return nil, fmt.Errorf("marketplace: reputation: %w", err)
	}
	defer func() { _ = rows.Close() }()
	installs := map[string]int64{}
	for rows.Next() {
		var tool string
		var n int64
		if err := rows.Scan(&tool, &n); err != nil {
			return nil, fmt.Errorf("marketplace: reputation: %w", err)
		}
		installs[tool] = n
	}
	for _, l := range ls {
		if l.Kind == KindTool {
			out[l.ID].installs = installs[l.Tool]
		}
	}
	return out, rows.Err()
}

// sellerTally aggregates everything said about one org as a seller.
func (s *Store) sellerTally(ctx context.Context, org string) (tally, error) {
	var t tally
	var sum sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(rating) FROM feedback WHERE about = $1 AND from_org != $2
		AND job IN (SELECT id FROM jobs WHERE seller_org = $3)`, org, org, org).Scan(&t.ratings, &sum); err != nil {
		return t, fmt.Errorf("marketplace: reputation: %w", err)
	}
	t.stars = sum.Int64
	if err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status = $1 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN disputed_at > 0 THEN 1 ELSE 0 END), 0)
		FROM jobs WHERE seller_org = $2`, StatusReleased, org).Scan(&t.completed, &t.disputed); err != nil {
		return t, fmt.Errorf("marketplace: reputation: %w", err)
	}
	return t, nil
}

// ── payouts ───────────────────────────────────────────────────────────────────

// payout is the wallet org proved it controls, if it has.
func (s *Store) payout(ctx context.Context, org string) (Payout, bool, error) {
	p := Payout{}
	err := s.db.QueryRowContext(ctx, `SELECT wallet, address, bound_at FROM payouts WHERE org = $1`, org).
		Scan(&p.Wallet, &p.Address, &p.BoundAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Payout{}, false, nil
	}
	if err != nil {
		return Payout{}, false, fmt.Errorf("marketplace: payout: %w", err)
	}
	if p.Address, err = s.open("payouts", "address", p.Address, org); err != nil {
		return Payout{}, false, fmt.Errorf("marketplace: payout: %w", err)
	}
	p.Bound = true
	return p, true, nil
}

// challenge records the message org must sign to bind wallet, replacing any
// earlier one.
func (s *Store) challenge(ctx context.Context, org, wallet, address, message string, expires int64) error {
	address, err := s.seal("challenges", "address", address, org)
	if err != nil {
		return fmt.Errorf("marketplace: challenge: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO challenges (org, wallet, address, message, expires) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT(org) DO UPDATE SET wallet=excluded.wallet, address=excluded.address, message=excluded.message,
		expires=excluded.expires`, org, wallet, address, message, expires); err != nil {
		return fmt.Errorf("marketplace: challenge: %w", err)
	}
	return nil
}

// pending is org's outstanding challenge.
func (s *Store) pending(ctx context.Context, org string) (wallet, address, message string, expires int64, found bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT wallet, address, message, expires FROM challenges WHERE org = $1`, org).
		Scan(&wallet, &address, &message, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", 0, false, nil
	}
	if err != nil {
		return "", "", "", 0, false, fmt.Errorf("marketplace: challenge: %w", err)
	}
	if address, err = s.open("challenges", "address", address, org); err != nil {
		return "", "", "", 0, false, fmt.Errorf("marketplace: challenge: %w", err)
	}
	return wallet, address, message, expires, true, nil
}

// ── plumbing ──────────────────────────────────────────────────────────────────

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("marketplace: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("marketplace: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b)
}
