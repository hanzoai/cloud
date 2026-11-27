package webhook

// store.go — one per-org (HIP-0302 physically-isolated) database holding the org's
// webhook endpoint registry: a SQLite file, or a schema on hanzo-sql. A distinct org
// resolves to a distinct {DataDir}/orgs/{slug}/webhooks.db (or schema), so one org can
// never read or mutate another's endpoints — the same per-org idiom apps/books uses
// (cloud.OrgStore over cloud.OrgDB). Each org owns its store; there is no cross-org
// query surface. Its SQL is the dialect both engines share (sdk/dialect).
//
// THE SIGNING SECRET AND THE URL ARE KMS VALUES (internal/escrow), not columns. The secret is
// the HMAC key a subscriber verifies deliveries with. The URL is where the deliveries go,
// and for a hosted receiver (Slack, Discord, Zapier, a CI trigger) the address IS the
// credential: whoever reads it can post as the org's endpoint. Each is kept in Hanzo KMS at
//
//	orgs/<org>/webhooks/<endpoint id>/{secret,url}@default
//
// and endpoint.secret and endpoint.url hold only "kms:" and that path. A read asks KMS, a
// delete asks KMS to forget, and the signer and the deliverer are the only readers. Left
// plain: ids, the event patterns, the status, the description, the timestamps and the
// delivery log.
//
// A file from before holds the URL in plaintext or sealed as a field, and the secret plain,
// concealed under org.Cipher.Conceal ("sealed." and base64, bound to the namespace and the
// subsystem "webhooks.secret" and to no row) or sealed as a field. The open reads each once,
// writes it to KMS, and replaces the column with its path; a row that already holds a path
// is not touched, so a second open does nothing.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/tenure"
)

// errNotFound is the store's one "no such row" sentinel; the API layer maps it to 404.
var errNotFound = errors.New("webhooks: endpoint not found")

const (
	// maxDeliveryRowsPerEndpoint caps the delivery log per endpoint: recordDelivery
	// prunes everything older than the newest N rows on every insert, so the log is
	// self-bounding without a background sweep.
	maxDeliveryRowsPerEndpoint = 500

	// maxDeliveryError bounds the stored (truncated) attempt error text.
	maxDeliveryError = 512
)

// usageCounts is a per-endpoint delivery tally over a time window: Deliveries is the
// number of COMPLETED deliveries (each attempt-group contributes exactly one terminal
// row — status 'ok' or 'failed'), Failures the subset that ultimately failed.
type usageCounts struct {
	Deliveries int
	Failures   int
}

// store wraps one org's *sql.DB. cloud.OrgStore opens (and migrates) it exactly once
// per org and calls Close on shutdown.
type store struct {
	db     *sql.DB
	cipher *tenure.Cipher // opens a value a file held before KMS; nil on a process with no data-plane master
	esc    *escrow.Escrow // keeps the URL and the signing secret
	byOrg  sync.Map       // org -> *org.Fields, made once each
}

// subsystem names the store: the database and the sealed columns' keys are made from
// it, so a column's key and its schema cannot name two things.
const subsystem = "webhooks"

// legacySecretSubsystem and legacySecretPrefix are what a secret from before the field
// format is concealed under. Only the migration reads them.
const (
	legacySecretSubsystem = "webhooks.secret"
	legacySecretPrefix    = "sealed."
)

// errNoMaster is the open of a value a file held before KMS, on a process with no data-plane
// master: only that open needs one.
var errNoMaster = errors.New("webhooks: no data-plane master on this process, so a value the file held before KMS cannot be opened")

// cipherOf is the cipher a master keys, or none when the process holds no usable one.
func cipherOf(master []byte) *tenure.Cipher {
	c, err := tenure.NewCipher(master)
	if err != nil {
		return nil
	}
	return c
}

// openStore is the open func cloud.OrgStore calls once per org: it wraps the
// already-opened *sql.DB, runs the (idempotent) migration, and moves what the file held
// before KMS into it. c opens that old value; k keeps the new ones. With no KMS an endpoint
// is neither kept nor read, so nothing is written anywhere else because KMS was missing.
func openStore(c *tenure.Cipher, k escrow.KMS) func(db *sql.DB) (*store, error) {
	return func(db *sql.DB) (*store, error) {
		s := &store{db: db, cipher: c, esc: escrow.New(k, "webhooks")}
		if err := s.migrate(); err != nil {
			_ = db.Close()
			return nil, err
		}
		return s, nil
	}
}

// fields is the sealer for one org's rows: its namespace is the one the org's database
// opens under.
func (s *store) fields(orgID string) (*tenure.Fields, error) {
	if s.cipher == nil {
		return nil, errNoMaster
	}
	if f, ok := s.byOrg.Load(orgID); ok {
		return f.(*tenure.Fields), nil
	}
	ns, err := principal.OrgNamespace(orgID, "")
	if err != nil {
		return nil, err
	}
	f, err := s.cipher.Fields(ns, subsystem)
	if err != nil {
		return nil, fmt.Errorf("webhooks: %w", err)
	}
	s.byOrg.Store(orgID, f)
	return f, nil
}

// row names an endpoint's row for sealing.
func (s *store) row(orgID, id string) (*tenure.Row, error) {
	f, err := s.fields(orgID)
	if err != nil {
		return nil, err
	}
	return f.Row("endpoint", orgID, id), nil
}

func (s *store) Close() error { return s.db.Close() }

func (s *store) migrate() error {
	d := dialect.Of(s.db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ddl := `
CREATE TABLE IF NOT EXISTS endpoint (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  url         TEXT NOT NULL,
  events      TEXT NOT NULL DEFAULT '[]',
  secret      TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'active',
  description TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS endpoint_status ON endpoint(status);

CREATE TABLE IF NOT EXISTS delivery (
  id          ` + d.Identity() + `,
  endpoint_id TEXT    NOT NULL,
  delivery_id TEXT    NOT NULL,
  subject     TEXT    NOT NULL,
  attempt     BIGINT  NOT NULL,
  status      TEXT    NOT NULL,
  http_status BIGINT  NOT NULL DEFAULT 0,
  error       TEXT    NOT NULL DEFAULT '',
  duration_ms BIGINT  NOT NULL DEFAULT 0,
  created     TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS delivery_endpoint ON delivery(endpoint_id, id DESC);`
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("webhooks migrate: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	return s.moveToKMS(ctx)
}

// moveToKMS moves, once, the endpoints a file holds from before KMS: a URL that is plain or a
// sealed field, and a secret that is plain, concealed or a sealed field. Each is written to
// KMS and the column replaced by its path; a column that holds a path is not touched, so a
// second open reads and writes nothing. It runs outside the schema's transaction: it calls
// KMS, which must not be done holding the store's write lock.
func (s *store) moveToKMS(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, url, secret FROM endpoint
		  WHERE (url <> '' AND substr(url, 1, 4) <> 'kms:') OR (secret <> '' AND substr(secret, 1, 4) <> 'kms:')`)
	if err != nil {
		return fmt.Errorf("webhooks migrate: %w", err)
	}
	type held struct{ id, org, url, secret string }
	var found []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.id, &h.org, &h.url, &h.secret); err != nil {
			_ = rows.Close()
			return fmt.Errorf("webhooks migrate: %w", err)
		}
		found = append(found, h)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("webhooks migrate: %w", err)
	}
	for _, h := range found {
		url, secret := h.url, h.secret
		if !escrow.Held(url) {
			if url, err = s.moved(ctx, h.org, h.id, "url", h.url); err != nil {
				return err
			}
		}
		if !escrow.Held(secret) {
			if secret, err = s.moved(ctx, h.org, h.id, "secret", h.secret); err != nil {
				return err
			}
		}
		// Compare-and-set on what was read: another replica that moved the row first
		// leaves nothing here to change.
		if _, err := s.db.ExecContext(ctx, `UPDATE endpoint SET url = $1, secret = $2 WHERE id = $3 AND url = $4 AND secret = $5`,
			url, secret, h.id, h.url, h.secret); err != nil {
			return fmt.Errorf("webhooks migrate: %w", err)
		}
	}
	return nil
}

// moved writes one value a file held before KMS to KMS, and answers what the column holds
// instead. An empty value is kept nowhere.
func (s *store) moved(ctx context.Context, orgID, id, field, stored string) (string, error) {
	value, err := s.old(orgID, id, field, stored)
	if err != nil {
		return "", fmt.Errorf("webhooks migrate: endpoint %s: %w", id, err)
	}
	ref, err := s.esc.Put(ctx, orgID, id, field, value)
	if err != nil {
		return "", fmt.Errorf("webhooks migrate: endpoint %s: %w", id, err)
	}
	return ref, nil
}

// old is the plaintext of a value a file holds from before KMS: the plain value, a secret
// concealed under the org's key, or a sealed field.
func (s *store) old(orgID, id, field, stored string) (string, error) {
	if strings.HasPrefix(stored, "seal:") {
		r, err := s.row(orgID, id)
		if err != nil {
			return "", err
		}
		v := r.Open(field, stored)
		return v, r.Err()
	}
	return s.unconceal(orgID, stored)
}

// unconceal is the plaintext of a secret a file holds from before the field format: the
// plain value, or one concealed under the org's key.
func (s *store) unconceal(orgID, stored string) (string, error) {
	enc, ok := strings.CutPrefix(stored, legacySecretPrefix)
	if !ok {
		return stored, nil
	}
	if s.cipher == nil {
		return "", errNoMaster
	}
	ns, err := principal.OrgNamespace(orgID, "")
	if err != nil {
		return "", err
	}
	b, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("the concealed secret is not a concealed secret: %w", err)
	}
	pt, err := s.cipher.Open(ns, legacySecretSubsystem, b)
	if err != nil {
		return "", fmt.Errorf("open the concealed secret: %w", err)
	}
	return string(pt), nil
}

// create inserts a new endpoint row. The caller has already minted id + secret and
// stamped created/updated. The URL and the secret are kept in KMS first, and the row names
// them; a row that cannot be written forgets them again.
func (s *store) create(ctx context.Context, e Endpoint) error {
	if err := dialect.Text(e.ID, e.Org, e.URL, encodeEvents(e.Events), e.Status, e.Description, e.CreatedAt, e.UpdatedAt); err != nil {
		return fmt.Errorf("webhooks create: %w", err)
	}
	url, err := s.esc.Put(ctx, e.Org, e.ID, "url", e.URL)
	if err != nil {
		return fmt.Errorf("webhooks create: %w", err)
	}
	secret, err := s.esc.Put(ctx, e.Org, e.ID, "secret", e.Secret)
	if err != nil {
		_ = s.esc.Drop(ctx, e.Org, url)
		return fmt.Errorf("webhooks create: %w", err)
	}
	if _, err = s.db.ExecContext(ctx,
		`INSERT INTO endpoint (id, org, url, events, secret, status, description, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		e.ID, e.Org, url, encodeEvents(e.Events), secret, e.Status, e.Description, e.CreatedAt, e.UpdatedAt); err != nil {
		_ = s.esc.Drop(ctx, e.Org, url)
		_ = s.esc.Drop(ctx, e.Org, secret)
		return fmt.Errorf("webhooks create: %w", err)
	}
	return nil
}

// reveal fills in what e's row names in KMS: its URL, its signing secret, or both. A
// read of the registry never asks KMS; only a delivery, a test and a listing that shows
// the address do.
func (s *store) reveal(ctx context.Context, e *Endpoint, url, secret bool) error {
	var err error
	if url {
		if e.URL, err = s.esc.Get(ctx, e.Org, e.urlRef); err != nil {
			return fmt.Errorf("webhooks: endpoint %s: %w", e.ID, err)
		}
	}
	if secret {
		if e.Secret, err = s.esc.Get(ctx, e.Org, e.secretRef); err != nil {
			return fmt.Errorf("webhooks: endpoint %s: %w", e.ID, err)
		}
	}
	return nil
}

// list returns every endpoint in this org's file (newest first).
// Each carries its URL; the signing secret is not read.
func (s *store) list(ctx context.Context) ([]Endpoint, error) {
	eps, err := s.query(ctx, `SELECT id, org, url, events, secret, status, description, created_at, updated_at
		FROM endpoint ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	for i := range eps {
		if err := s.reveal(ctx, &eps[i], true, false); err != nil {
			return nil, err
		}
	}
	return eps, nil
}

// listActive returns only the org's ACTIVE endpoints — the set the dispatcher matches
// an event against. A disabled endpoint is never returned here, so it is never delivered.
// They name their URL and secret and carry neither: the dispatcher reveals the ones an
// event matches.
func (s *store) listActive(ctx context.Context) ([]Endpoint, error) {
	return s.query(ctx, `SELECT id, org, url, events, secret, status, description, created_at, updated_at
		FROM endpoint WHERE status = 'active' ORDER BY id`)
}

func (s *store) query(ctx context.Context, q string, args ...any) ([]Endpoint, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("webhooks query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Endpoint{}
	for rows.Next() {
		e, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// get returns one endpoint by id, or errNotFound. It names its URL and secret and carries
// neither; reveal fills them in.
func (s *store) get(ctx context.Context, id string) (Endpoint, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, org, url, events, secret, status, description, created_at, updated_at
		 FROM endpoint WHERE id = $1`, id)
	e, err := s.scan(row)
	if err == sql.ErrNoRows {
		return Endpoint{}, errNotFound
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("webhooks get: %w", err)
	}
	return e, nil
}

// update mutates the caller-editable fields (url, events, status, description) of an
// existing endpoint of orgID, stamping updated_at. The secret and created_at are immutable. It
// returns the stored row with its URL, or errNotFound.
func (s *store) update(ctx context.Context, orgID, id, url string, events []string, status, description, updatedAt string) (Endpoint, error) {
	if err := dialect.Text(id, url, encodeEvents(events), status, description, updatedAt); err != nil {
		return Endpoint{}, fmt.Errorf("webhooks update: %w", err)
	}
	if _, err := s.get(ctx, id); err != nil {
		return Endpoint{}, err
	}
	ref, err := s.esc.Put(ctx, orgID, id, "url", url)
	if err != nil {
		return Endpoint{}, fmt.Errorf("webhooks update: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE endpoint SET url = $1, events = $2, status = $3, description = $4, updated_at = $5 WHERE id = $6 AND org = $7`,
		ref, encodeEvents(events), status, description, updatedAt, id, orgID)
	if err != nil {
		return Endpoint{}, fmt.Errorf("webhooks update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Endpoint{}, errNotFound
	}
	e, err := s.get(ctx, id)
	if err != nil {
		return Endpoint{}, err
	}
	e.URL = url
	return e, nil
}

// del removes an endpoint and asks KMS to forget its URL and its secret, reporting whether
// a row was deleted.
func (s *store) del(ctx context.Context, id string) (bool, error) {
	e, err := s.get(ctx, id)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM endpoint WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("webhooks delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	for _, ref := range []string{e.urlRef, e.secretRef} {
		if err := s.esc.Drop(ctx, e.Org, ref); err != nil {
			return true, fmt.Errorf("webhooks delete: the endpoint is gone but KMS still holds a value: %w", err)
		}
	}
	return true, nil
}

// setSecret rotates an endpoint's signing secret, stamping updated_at and leaving every
// other field (url/events/status/description/created_at) untouched. The new secret
// replaces the old one in KMS at the same path. It returns the stored row with the new
// secret, so the caller can reveal it once, or errNotFound.
func (s *store) setSecret(ctx context.Context, orgID, id, secret, updatedAt string) (Endpoint, error) {
	if err := dialect.Text(id, updatedAt); err != nil {
		return Endpoint{}, fmt.Errorf("webhooks set secret: %w", err)
	}
	if _, err := s.get(ctx, id); err != nil {
		return Endpoint{}, err
	}
	ref, err := s.esc.Put(ctx, orgID, id, "secret", secret)
	if err != nil {
		return Endpoint{}, fmt.Errorf("webhooks set secret: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE endpoint SET secret = $1, updated_at = $2 WHERE id = $3 AND org = $4`, ref, updatedAt, id, orgID)
	if err != nil {
		return Endpoint{}, fmt.Errorf("webhooks set secret: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Endpoint{}, errNotFound
	}
	e, err := s.get(ctx, id)
	if err != nil {
		return Endpoint{}, err
	}
	e.Secret = secret
	return e, nil
}

// recordDelivery appends ONE delivery-attempt row, then prunes the endpoint's log back to
// the newest maxDeliveryRowsPerEndpoint rows (opportunistic retention on insert — no
// background sweep). It is called best-effort from the delivery goroutine; the caller
// swallows any error so a log write can never affect delivery.
func (s *store) recordDelivery(ctx context.Context, r DeliveryRow) error {
	if err := dialect.Text(r.EndpointID, r.DeliveryID, r.Subject, r.Status, r.Error, r.Created); err != nil {
		return fmt.Errorf("webhooks record delivery: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO delivery (endpoint_id, delivery_id, subject, attempt, status, http_status, error, duration_ms, created)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		r.EndpointID, r.DeliveryID, r.Subject, r.Attempt, r.Status, r.HTTPStatus, r.Error, r.DurationMs, r.Created); err != nil {
		return fmt.Errorf("webhooks record delivery: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM delivery WHERE endpoint_id = $1 AND id NOT IN (
		   SELECT id FROM delivery WHERE endpoint_id = $2 ORDER BY id DESC LIMIT $3)`,
		r.EndpointID, r.EndpointID, maxDeliveryRowsPerEndpoint); err != nil {
		return fmt.Errorf("webhooks prune delivery: %w", err)
	}
	return nil
}

// deliveries returns an endpoint's delivery-attempt log, newest first, capped at limit and
// optionally filtered to one status (e.g. "failed"). An empty status means all rows.
func (s *store) deliveries(ctx context.Context, endpointID string, limit int, status string) ([]DeliveryRow, error) {
	q := `SELECT endpoint_id, delivery_id, subject, attempt, status, http_status, error, duration_ms, created
	      FROM delivery WHERE endpoint_id = $1`
	args := []any{endpointID}
	if status != "" {
		q += ` AND status = $2`
		args = append(args, status)
	}
	q += fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("webhooks deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []DeliveryRow{}
	for rows.Next() {
		var r DeliveryRow
		if err := rows.Scan(&r.EndpointID, &r.DeliveryID, &r.Subject, &r.Attempt, &r.Status, &r.HTTPStatus, &r.Error, &r.DurationMs, &r.Created); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// usage tallies every endpoint's completed/failed deliveries since the given RFC3339
// instant in ONE grouped aggregate (created is stored as sortable RFC3339-UTC, so a
// lexical >= is a time comparison). Endpoints with no rows in the window are simply
// absent from the map — the caller defaults them to a zero tally.
func (s *store) usage(ctx context.Context, since string) (map[string]usageCounts, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT endpoint_id,
		        CAST(SUM(CASE WHEN status IN ('ok','failed') THEN 1 ELSE 0 END) AS BIGINT),
		        CAST(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS BIGINT)
		 FROM delivery WHERE created >= $1 GROUP BY endpoint_id`, since)
	if err != nil {
		return nil, fmt.Errorf("webhooks usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]usageCounts{}
	for rows.Next() {
		var id string
		var deliveries, failures int
		if err := rows.Scan(&id, &deliveries, &failures); err != nil {
			return nil, err
		}
		out[id] = usageCounts{Deliveries: deliveries, Failures: failures}
	}
	return out, rows.Err()
}

// scanner abstracts *sql.Row and *sql.Rows so scan serves both get and query.
type scanner interface {
	Scan(dest ...any) error
}

func (s *store) scan(r scanner) (Endpoint, error) {
	var e Endpoint
	var events string
	if err := r.Scan(&e.ID, &e.Org, &e.urlRef, &events, &e.secretRef, &e.Status, &e.Description, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return Endpoint{}, err
	}
	e.Events = decodeEvents(events)
	return e, nil
}

// encodeEvents serializes the subject-pattern list to the stored JSON text. A nil or
// empty list stores "[]" (which decodes back to an empty slice → "all events").
func encodeEvents(events []string) string {
	if len(events) == 0 {
		return "[]"
	}
	b, err := json.Marshal(events)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// decodeEvents parses the stored JSON text back to the pattern list. A malformed or
// empty value decodes to an empty slice (matches everything), never an error.
func decodeEvents(s string) []string {
	if s == "" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []string{}
	}
	return out
}
