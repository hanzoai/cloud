package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var errNotFound = errors.New("sandbox: sandbox not found")

// Sandbox is one leased execution sandbox. It is the SAME record whether the
// lease is seconds long (a function invoke) or a week (a suspended coding
// session) — status and expiresAt carry the difference, not a second table.
//
// Pod is the sandbox's ADDRESS, and it is `json:"-"` on purpose. It is a pod
// name in a namespace a caller has no business knowing, and handing it out both
// maps the cluster for whatever runs inside a sandbox and invites a client to
// try reaching the sandbox directly, which would mean terminating auth somewhere
// other than the IAM edge. The predecessor returned a pod IP in every create,
// get and list response.
type Sandbox struct {
	// ID is the sandbox's server-minted handle and what every operation addresses
	// it by. The caller does not choose it.
	ID string `json:"id"`
	// Org is the org that holds the lease — the validated caller's, never a value
	// a request supplied. It is also the store's key, so a sandbox is not merely
	// filtered out of another org's answers; it is unreachable from them.
	Org string `json:"org"`
	// Kind is the resource family this row belongs to. Always "sandbox" here; it
	// exists because the store this shares is keyed across kinds.
	Kind string `json:"kind"`
	// Class is what the sandbox is FOR, and it decides the image, the working
	// directory and the isolation: "exec" for a code-interpreter call (workdir
	// /mnt/data, no project, bounded per org), "dev" for a workspace bound to a
	// project (workdir /work, single-attach), "desktop" for one with a screen.
	Class string `json:"class"`
	// Project is the project this sandbox is bound to. A dev or desktop sandbox
	// has one and is SINGLE-ATTACH under it, so asking twice resumes rather than
	// leasing a second; an exec sandbox has none.
	Project string `json:"project,omitempty"`
	// Status is where the sandbox is in its life: "pending" while the pod is
	// coming up, "running" once it can take work, "error" when it cannot. Only a
	// running sandbox takes an exec or mints an interactive ticket.
	Status string `json:"status"`
	// Image is the container image this sandbox is actually running — the one the
	// class chose, or an override the policy admitted. It is what ran, not what
	// was asked for.
	Image string `json:"image"`
	// Pod is the Kubernetes pod behind the sandbox. Deliberately NOT on the wire:
	// it is an internal address a caller can neither use nor need, and publishing
	// it would leak the cluster's naming into a tenant's view.
	Pod string `json:"-"`
	// Runtime is the isolation boundary this sandbox GOT, which is not always the
	// one it asked for: a caller states a preference and runtimeFor answers with
	// what the sandbox can actually have. Reported so a person comparing two
	// runtimes is comparing the runtimes they got rather than the ones they typed
	// — the difference between those two is the whole reason to record it.
	//
	// Empty means the node's default runtime, which is a real answer and not a
	// missing one.
	//
	// This is not a copy that can go stale. runtimeClassName is IMMUTABLE on a
	// pod, a sandbox's pod is created once and never recreated (restartPolicy
	// Never, no pool), and its name is never reused — so for as long as the pod
	// this row names exists, it is running this runtime. The alternative, asking
	// the apiserver on every read, buys nothing and costs a round trip per row.
	Runtime string `json:"runtime,omitempty"`
	// Volume is the persistent volume attached to the sandbox, when it has one. A
	// dev sandbox keeps its work across leases through it; an exec sandbox has
	// none and loses everything outside /mnt/data when the lease ends.
	Volume string `json:"volume,omitempty"`
	// Cluster is the attached cluster this sandbox runs on — the fleet-local
	// name the lease named — or empty for the home cluster. Immutable for the
	// life of the lease, like the pod it locates: every later call into the
	// sandbox reads it to reach the right apiserver.
	Cluster string `json:"cluster,omitempty"`
	// Error is why the sandbox could not come up, in plain words. Present only
	// with status "error", and it is the field to read rather than inferring a
	// cause from the absence of a pod.
	Error string `json:"error,omitempty"`
	// CreatedAt is when the lease was first taken, Unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// LastUsedAt is when the sandbox last did work, Unix seconds. The reaper reads
	// it: a sandbox idle past the idle window is reclaimed even inside its TTL,
	// because an idle lease is capacity nobody is using.
	LastUsedAt int64 `json:"lastUsedAt"`
	// ConnectedAt is when somebody was last known to have this sandbox's project
	// OPEN, Unix seconds. It is a fact with an EXPIRY rather than a flag: a
	// watcher restamps it every beat of its stream, and it goes stale on its own
	// when the stream dies, so nothing has to be turned off by a process that may
	// not be there any more. The reaper reads it to choose WHICH idle allowance
	// applies — see lifecycle.go.
	//
	// Zero means nobody has said so, which puts the sandbox on the short clock.
	ConnectedAt int64 `json:"connectedAt,omitempty"`
	// ExpiresAt is when the lease ends, Unix seconds. Past it the reaper may take
	// the sandbox at any time; it is a deadline, not a guarantee of survival until
	// then, since an idle sandbox goes sooner.
	ExpiresAt int64 `json:"expiresAt,omitempty"`
	// Payer is WHOSE BOOKS pay for the time this lease is held — principal.Ledger
	// at the act that took it, which is not the same value as Org: a platform
	// SuperAdmin leasing inside somebody else's org spends their own. The one-shot
	// lease fee already lands on it (api.go), and the recurring runtime charge has
	// to land on the same wallet or one lease is billed to two payers.
	//
	// It is `json:"-"`: a caller cannot choose it, and reading back whose ledger a
	// lease is charged to is the billing surface's job, not this row's.
	Payer string `json:"-"`
	// MeteredAt is the second through which this lease's runtime has been billed —
	// [cloud.Running.MeteredAt]. Zero means the meter has never seen it, which is
	// charged as nothing rather than as a debt back to the epoch.
	MeteredAt int64 `json:"-"`
}

// Store is one org's sandbox registry — ONE SQLite file per org at
// {DataDir}/orgs/{orgSlug}/sandbox.db (cloud.OrgDB, HIP-0302). Org isolation is
// PHYSICAL (a different file), with the org column kept as defence in depth so a
// query that somehow reached the wrong file still returns nothing.
type Store struct{ db *sql.DB }

func openStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS sandbox (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  kind         TEXT NOT NULL DEFAULT 'sandbox',
  class        TEXT NOT NULL DEFAULT 'exec',
  project      TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'pending',
  image        TEXT NOT NULL DEFAULT '',
  pod          TEXT NOT NULL DEFAULT '',
  runtime      TEXT NOT NULL DEFAULT '',
  volume       TEXT NOT NULL DEFAULT '',
  cluster      TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  connected_at INTEGER NOT NULL DEFAULT 0,
  expires_at   INTEGER NOT NULL DEFAULT 0,
  payer        TEXT NOT NULL DEFAULT '',
  metered_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_machines_org_project ON sandbox(org, project);
CREATE INDEX IF NOT EXISTS ix_machines_org_status  ON sandbox(org, status);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// runtime: added after the initial schema. Fresh DBs get it from the CREATE
	// above; a DB that predates it gains it here, and its existing rows read as
	// the node default — which is what they in fact ran. SQLite has no ADD COLUMN
	// IF NOT EXISTS, so the duplicate-column error is the expected no-op.
	if _, err := s.db.Exec(`ALTER TABLE sandbox ADD COLUMN runtime TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate runtime column: %w", err)
	}
	// connected_at: same story. Existing rows read as 0, which means "nobody has
	// said they are watching" — the honest answer for a row written before anyone
	// could say it, and the one that puts them on the short clock until a watcher
	// speaks up.
	if _, err := s.db.Exec(`ALTER TABLE sandbox ADD COLUMN connected_at INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate connected_at column: %w", err)
	}
	// payer + metered_at: the runtime meter. A lease that predates the meter reads
	// payer "" and metered_at 0, and BOTH of those are refusals rather than
	// defaults — cloud.RuntimeCharge bills nothing for a row with no payer, and
	// starts the clock at now for a row with no watermark. So shipping the meter
	// charges nobody for hours that were free when they ran, and never guesses
	// whose wallet an old lease belonged to.
	for _, ddl := range []string{
		`ALTER TABLE sandbox ADD COLUMN payer TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sandbox ADD COLUMN metered_at INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := s.db.Exec(ddl); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate runtime meter columns: %w", err)
		}
	}
	if _, err := s.db.Exec(`ALTER TABLE sandbox ADD COLUMN cluster TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate cluster column: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Put(ctx context.Context, m Sandbox) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sandbox (id,org,kind,class,project,status,image,pod,runtime,volume,cluster,error,created_at,last_used_at,connected_at,expires_at,payer,metered_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  status=excluded.status, image=excluded.image, pod=excluded.pod, runtime=excluded.runtime,
  volume=excluded.volume,
  error=excluded.error, last_used_at=excluded.last_used_at, expires_at=excluded.expires_at`,
		m.ID, m.Org, m.Kind, m.Class, m.Project, m.Status, m.Image, m.Pod, m.Runtime, m.Volume,
		m.Cluster, m.Error, m.CreatedAt, m.LastUsedAt, m.ConnectedAt, m.ExpiresAt, m.Payer, m.MeteredAt)
	return err
}

// Get is org-scoped in the WHERE clause, always. The file is already per-org;
// this is the second lock on the same path.
func (s *Store) Get(ctx context.Context, org, id string) (Sandbox, error) {
	row := s.db.QueryRowContext(ctx, selectCols+` WHERE org=? AND id=?`, org, id)
	return scanMachine(row)
}

// Live is the single-attach check: the sandbox, if any, currently holding this
// project's volume.
func (s *Store) Live(ctx context.Context, org, project string) (Sandbox, error) {
	row := s.db.QueryRowContext(ctx,
		selectCols+` WHERE org=? AND project=? AND status IN ('running','pending') LIMIT 1`, org, project)
	m, err := scanMachine(row)
	if err == errNotFound {
		return Sandbox{}, nil
	}
	return m, err
}

func (s *Store) List(ctx context.Context, org, project, status string) ([]Sandbox, error) {
	q := selectCols + ` WHERE org=?`
	args := []any{org}
	if project != "" {
		q, args = q+` AND project=?`, append(args, project)
	}
	if status != "" {
		q, args = q+` AND status=?`, append(args, status)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Sandbox{}
	for rows.Next() {
		var m Sandbox
		if err := rows.Scan(&m.ID, &m.Org, &m.Kind, &m.Class, &m.Project, &m.Status,
			&m.Image, &m.Pod, &m.Runtime, &m.Volume, &m.Cluster, &m.Error, &m.CreatedAt, &m.LastUsedAt, &m.ConnectedAt,
			&m.ExpiresAt, &m.Payer, &m.MeteredAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Live counts the org's sandboxes of one class that are still holding a pod.
//
// A COUNT and not a len(List): List is LIMIT 200, so a cap read through it would
// stop counting at 200 and stop refusing at exactly the point refusing matters.
func (s *Store) LiveOfClass(ctx context.Context, org, class string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sandbox WHERE org=? AND class=? AND status IN ('running','pending')`,
		org, class).Scan(&n)
	return n, err
}

// Extend pushes one sandbox's lease out to at, and only ever FORWARD.
//
// The `expires_at<?` guard is what makes it monotonic: a stale caller with an
// older `at` cannot pull a lease in and kill a sandbox early. A lease may be
// lengthened by work and shortened only by ending it, which is the property the
// reaper relies on to be safe to run every minute from more than one place.
// Watched stamps ATTENTION: a live stream said somebody has this project open.
//
// MONOTONIC — `connected_at=MAX(connected_at,?)`. Presence arrives from a
// heartbeat over a socket, so two beats can land out of order and an older one
// would otherwise move the clock BACKWARDS, briefly making a watched sandbox
// look stale to a sweep running in that window.
//
// It is also why Put does not write this column on conflict: an update carrying
// a row read before the last beat would undo it. One writer per fact.
func (s *Store) Watched(ctx context.Context, org, id string, at int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sandbox SET connected_at=MAX(connected_at,?) WHERE org=? AND id=?`, at, org, id)
	return err
}

func (s *Store) Extend(ctx context.Context, org, id string, at int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sandbox SET expires_at=? WHERE org=? AND id=? AND expires_at>0 AND expires_at<?`,
		at, org, id, at)
	return err
}

// IDs is every sandbox this org still claims, with NO LIMIT — which is the whole
// reason it is not just List.
//
// The orphan sweep subtracts this set from the pods in the cluster, so a truncated
// answer here does not mean "fewer rows": it means every sandbox past the cutoff is
// reported as unclaimed and its pod is deleted while its owner is working in it.
// List's `LIMIT 200` is right for a page a human reads and catastrophic for a set a
// sweep differences against, so the sweep gets its own query rather than a bigger
// limit somebody has to keep ahead of the fleet.
func (s *Store) IDs(ctx context.Context, org string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM sandbox WHERE org=?`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// Held is every lease this org is currently HOLDING — the rows the runtime meter
// bills — with NO LIMIT, for the same reason IDs has none.
//
// List's `LIMIT 200` is right for a page a human reads and wrong for a set money
// is computed over: truncation there does not mean "a shorter page", it means
// every lease past row 200 is free.
//
// The predicate is `holding`, the same one Live and LiveOfClass use for "this
// sandbox is holding a pod", so what the fleet counts as occupied capacity and
// what it charges for cannot disagree.
func (s *Store) Held(ctx context.Context, org string) ([]Sandbox, error) {
	return s.rows(ctx, ` WHERE org=? AND status IN ('running','pending')`, org)
}

// Rows is every sandbox this org has, with NO LIMIT — the set the REAPER sweeps.
//
// It is wider than Held, and the difference is the whole point. Held answers "what
// is this org being charged for", which is `holding` and excludes a sandbox whose
// start failed. The reaper asks a different question — "what might still have a POD
// behind it" — and the answer includes exactly those failed rows: a start that
// timed out left the pod running and only gave up WAITING for it, so a set that
// stops at `holding` leaves that pod alive with nothing left that will ever ask
// about it. Two questions, two queries, neither borrowing the other's predicate.
//
// It is also why this is not `List`. List is `ORDER BY created_at DESC LIMIT 200`,
// so past two hundred sandboxes it returns the NEWEST two hundred and drops the
// oldest — which are precisely the ones most likely to be over their lease. The
// reaper reading it billed those forever and ended them never; a sweep is a set to
// difference against, not a page to show, and every set here is unbounded for that
// reason (IDs, Held, and now this one).
func (s *Store) Rows(ctx context.Context, org string) ([]Sandbox, error) {
	return s.rows(ctx, ` WHERE org=?`, org)
}

// Org is the org this file belongs to, as its own rows spell it — the RAW string a
// caller was validated as, not a fold of it.
//
// It exists because a Kubernetes object outlives the process that made it, and the
// sweeps identify one by a label built from this value. The file is per-org, so
// every row agrees and one is enough; an empty file has no answer and says so with
// the empty string rather than a guess.
func (s *Store) Org(ctx context.Context) (string, error) {
	var org string
	err := s.db.QueryRowContext(ctx, `SELECT org FROM sandbox LIMIT 1`).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return org, err
}

// rows is the scan every unbounded set above shares, so a column added to the table
// is added to one scan rather than to three that must be kept in step.
func (s *Store) rows(ctx context.Context, where string, args ...any) ([]Sandbox, error) {
	rows, err := s.db.QueryContext(ctx, selectCols+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Sandbox{}
	for rows.Next() {
		var m Sandbox
		if err := rows.Scan(&m.ID, &m.Org, &m.Kind, &m.Class, &m.Project, &m.Status,
			&m.Image, &m.Pod, &m.Runtime, &m.Volume, &m.Cluster, &m.Error, &m.CreatedAt, &m.LastUsedAt, &m.ConnectedAt,
			&m.ExpiresAt, &m.Payer, &m.MeteredAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Advance moves one lease's runtime watermark from was to now, and reports
// whether THIS caller is the one that moved it.
//
// The `metered_at=?` in the WHERE clause is the whole of it: two sweeps that read
// the same watermark both try to advance it, exactly one row is affected, and the
// loser charges nothing. It is what makes a span billable once however many
// sweepers, pods or retries run it — see [cloud.RuntimeCharge], which calls this
// BEFORE it emits a debit and never after.
//
// Put deliberately does not write this column on conflict, the same rule
// connected_at states one function up: one writer per fact.
func (s *Store) Advance(ctx context.Context, org, id string, was, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sandbox SET metered_at=? WHERE org=? AND id=? AND metered_at=?`, now, org, id, was)
	if err != nil {
		return false, fmt.Errorf("advance runtime meter: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) Delete(ctx context.Context, org, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sandbox WHERE org=? AND id=?`, org, id)
	return err
}

const selectCols = `SELECT id,org,kind,class,project,status,image,pod,runtime,volume,cluster,error,created_at,last_used_at,connected_at,expires_at,payer,metered_at FROM sandbox`

func scanMachine(row *sql.Row) (Sandbox, error) {
	var m Sandbox
	err := row.Scan(&m.ID, &m.Org, &m.Kind, &m.Class, &m.Project, &m.Status,
		&m.Image, &m.Pod, &m.Runtime, &m.Volume, &m.Cluster, &m.Error, &m.CreatedAt, &m.LastUsedAt, &m.ConnectedAt,
		&m.ExpiresAt, &m.Payer, &m.MeteredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Sandbox{}, errNotFound
	}
	return m, err
}

func genID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return IDPrefix + hex.EncodeToString(b[:]), nil
}

// podName is the sandbox's address, minted once per sandbox and NEVER REUSED.
// That is the whole property: an address that can be recycled is an address a
// second tenant can be handed, and no credential check downstream can undo it.
func podName(id string) string { return "m-" + strings.TrimPrefix(id, IDPrefix) }

// volumeName is the per-(org, project) disk. Deterministic, so a resume finds
// the SAME disk without a lookup, and DNS-safe because Kubernetes object names
// are.
//
// THE HASH IS LOad-BEARING. A name built from slug(org) alone folds distinct
// orgs together — slug lowercases, strips everything outside [a-z0-9-] and caps
// the length, which is exactly the fold principal.Org refuses to do because
// "folding collapses DISTINCT owners into one bucket, itself a cross-org break".
// "Acme" and "acme", "acme" and "acme!", and any two orgs agreeing in their
// first N characters all produced ONE volume name. The suffix is over the
// UNFOLDED org and project, so the readable part can be squeezed to fit 63
// characters without ever making two tenants share a disk.
func volumeName(org, project string) string {
	sum := sha256.Sum256([]byte(org + "\x00" + project))
	tail := "-" + hex.EncodeToString(sum[:5])
	head := "m-" + slug(org) + "-" + slug(project)
	if max := 63 - len(tail); len(head) > max {
		head = head[:max]
	}
	return strings.TrimRight(head, "-") + tail
}

// slug makes a Kubernetes-safe label out of arbitrary text. It is LOSSY, which
// is why nothing that must stay distinct is ever derived from it alone.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == '_', r == '/', r == '.', r == ' ':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 48 {
		out = strings.Trim(out[:48], "-")
	}
	return out
}
