package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// errNotFound is returned when a bot (or its events) is not present in the
// org's file; handlers map it to HTTP 404.
var errNotFound = errors.New("bot: not found")

// Bot is one Hanzo Bot that has announced itself and is expected to keep
// saying so. It is the registry row behind /v1/bot: a loop running somewhere
// — on a person's laptop, or in a sandbox this cloud placed it in — that can
// be listed, reached, suspended and resumed.
//
// Org is the tenant boundary: one bot.db per org, filtered WHERE org=? on
// every query.
//
// Where says which of the two the row is. A local bot runs on hardware the
// cloud does not own, announces outward, and is reachable only at whatever
// URL it publishes. A cloud bot runs in a sandbox this cloud placed it in, so
// the cloud knows where it is and may suspend it. The distinction decides who
// may stop a bot, not what a bot can do.
//
// Edition says what the loop is allowed to drive. A plain bot answers.
// A computer bot has a machine to use, a browser bot has a browser. The word
// is the capability boundary the sandbox is built to, so it is recorded here
// rather than inferred from what a bot happens to call.
//
// Resume carries whatever a suspended bot needs to come back as itself — a
// checkpoint reference, opaque here. The gateway does not read it; it holds
// it so that a bot suspended on one host can resume on another. It is empty
// for a bot that has never suspended.
type Bot struct {
	ID          string
	Org         string
	Project     string
	User        string
	Name        string
	Where       string // local | cloud
	Edition     string // plain | computer | browser
	Model       string
	Host        string
	URL         string // where a caller reaches this bot, if it publishes one
	Status      string
	Resume      string
	StartedAt   int64
	UpdatedAt   int64
	SuspendedAt int64
	EndedAt     int64
}

// Event is something a bot reported: a notification it raised, a suspension,
// a resumption, an error. The gateway keeps them so a console can say what a
// bot has been doing without holding a connection open to it.
type Event struct {
	ID        string
	BotID     string
	Org       string
	Kind      string
	Message   string
	CreatedAt int64
}

// Filter narrows a bot list. Empty fields do not constrain. Live selects the
// bots that have neither ended nor suspended — the console's default question,
// "what is running right now".
type Filter struct {
	Status  string
	Where   string
	Edition string
	Host    string
	Project string
	Live    bool
}

// Store is one org's bot registry.
type Store struct{ db *sql.DB }

const botCols = "id, org, project, usr, name, whr, edition, model, host, url, status, resume, started_at, updated_at, suspended_at, ended_at"

// openStore is the cloud.NewOrgStore factory: it receives the org's already-open
// *sql.DB and installs the schema. Idempotent (IF NOT EXISTS), so reopen is a
// no-op.
func openStore(db *sql.DB) (*Store, error) {
	const schema = `
CREATE TABLE IF NOT EXISTS bots (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  project      TEXT NOT NULL DEFAULT '',
  usr          TEXT NOT NULL DEFAULT '',
  name         TEXT NOT NULL DEFAULT '',
  whr          TEXT NOT NULL,
  edition      TEXT NOT NULL DEFAULT 'plain',
  model        TEXT NOT NULL DEFAULT '',
  host         TEXT NOT NULL DEFAULT '',
  url          TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'starting',
  resume       TEXT NOT NULL DEFAULT '',
  started_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  suspended_at INTEGER NOT NULL DEFAULT 0,
  ended_at     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS bots_org_status  ON bots(org, status);
CREATE INDEX IF NOT EXISTS bots_org_project ON bots(org, project);

CREATE TABLE IF NOT EXISTS bot_events (
  id         TEXT PRIMARY KEY,
  bot_id     TEXT NOT NULL,
  org        TEXT NOT NULL,
  kind       TEXT NOT NULL,
  message    TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS bot_events_bid ON bot_events(bot_id, created_at);
`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("bot schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Create inserts a new bot row.
func (s *Store) Create(ctx context.Context, x Bot) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO bots (`+botCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		x.ID, x.Org, x.Project, x.User, x.Name, x.Where, x.Edition, x.Model,
		x.Host, x.URL, x.Status, x.Resume, x.StartedAt, x.UpdatedAt, x.SuspendedAt, x.EndedAt)
	return err
}

// Get reads one bot. It takes org so a caller cannot read across the tenant
// boundary by guessing an id.
func (s *Store) Get(ctx context.Context, org, id string) (Bot, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+botCols+` FROM bots WHERE org=? AND id=?`, org, id)
	return scanBot(row)
}

// List returns the org's bots, newest first, narrowed by f.
func (s *Store) List(ctx context.Context, org string, f Filter) ([]Bot, error) {
	q := `SELECT ` + botCols + ` FROM bots WHERE org=?`
	args := []any{org}
	if f.Status != "" {
		q += ` AND status=?`
		args = append(args, f.Status)
	}
	if f.Where != "" {
		q += ` AND whr=?`
		args = append(args, f.Where)
	}
	if f.Edition != "" {
		q += ` AND edition=?`
		args = append(args, f.Edition)
	}
	if f.Host != "" {
		q += ` AND host=?`
		args = append(args, f.Host)
	}
	if f.Project != "" {
		q += ` AND project=?`
		args = append(args, f.Project)
	}
	if f.Live {
		q += ` AND status NOT IN ('ended','error','suspended')`
	}
	q += ` ORDER BY started_at DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Bot{}
	for rows.Next() {
		x, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Update writes the fields a bot may change after it has announced itself.
// Empty strings and zero times leave a column alone, so a heartbeat that
// carries only a status does not blank the URL a bot published earlier.
func (s *Store) Update(ctx context.Context, org, id string, x Bot) (Bot, error) {
	sets := []string{"updated_at=?"}
	args := []any{x.UpdatedAt}
	if x.Status != "" {
		sets = append(sets, "status=?")
		args = append(args, x.Status)
	}
	if x.URL != "" {
		sets = append(sets, "url=?")
		args = append(args, x.URL)
	}
	if x.Model != "" {
		sets = append(sets, "model=?")
		args = append(args, x.Model)
	}
	if x.Resume != "" {
		sets = append(sets, "resume=?")
		args = append(args, x.Resume)
	}
	if x.SuspendedAt != 0 {
		sets = append(sets, "suspended_at=?")
		args = append(args, x.SuspendedAt)
	}
	if x.EndedAt != 0 {
		sets = append(sets, "ended_at=?")
		args = append(args, x.EndedAt)
	}
	args = append(args, org, id)

	res, err := s.db.ExecContext(ctx,
		`UPDATE bots SET `+strings.Join(sets, ", ")+` WHERE org=? AND id=?`, args...)
	if err != nil {
		return Bot{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Bot{}, errNotFound
	}
	return s.Get(ctx, org, id)
}

// Delete forgets a bot and everything it reported.
func (s *Store) Delete(ctx context.Context, org, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM bots WHERE org=? AND id=?`, org, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM bot_events WHERE org=? AND bot_id=?`, org, id)
	return err
}

// AddEvent records something a bot reported.
func (s *Store) AddEvent(ctx context.Context, e Event) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO bot_events (id, bot_id, org, kind, message, created_at) VALUES (?,?,?,?,?,?)`,
		e.ID, e.BotID, e.Org, e.Kind, e.Message, e.CreatedAt)
	return err
}

// Events returns what a bot reported, newest first, at most limit rows.
func (s *Store) Events(ctx context.Context, org, botID string, limit int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, bot_id, org, kind, message, created_at FROM bot_events
		 WHERE org=? AND bot_id=? ORDER BY created_at DESC LIMIT ?`, org, botID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.BotID, &e.Org, &e.Kind, &e.Message, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// scanner is what both *sql.Row and *sql.Rows satisfy, so one scan serves Get
// and List.
type scanner interface{ Scan(...any) error }

func scanBot(sc scanner) (Bot, error) {
	var x Bot
	err := sc.Scan(&x.ID, &x.Org, &x.Project, &x.User, &x.Name, &x.Where, &x.Edition,
		&x.Model, &x.Host, &x.URL, &x.Status, &x.Resume,
		&x.StartedAt, &x.UpdatedAt, &x.SuspendedAt, &x.EndedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Bot{}, errNotFound
	}
	return x, err
}

// Close releases the org's file. cloud.OrgStore requires it.
func (s *Store) Close() error { return s.db.Close() }
