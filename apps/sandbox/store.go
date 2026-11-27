package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
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
	// Actor is the subject who LEASED this sandbox — the validated caller of the
	// act that took the lease, never a value a request supplied. A sandbox is its
	// lessee's: it holds their session, and a coding run's holds a write key to a
	// repository and the codebase's secrets. So the org gates which store a row is
	// in, and this gates who in the org may act on it (holder.holds): the lessee,
	// or an admin of the org, or a SuperAdmin. Empty is a lease taken by the org's
	// own background work, which no person holds.
	Actor string `json:"actor,omitempty"`
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
	// coming up, "running" once it can take work, "error" when it cannot, and
	// "parked" when its pod is stopped and its disk kept, until a resume. Only a
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
	// project sandbox keeps its work across leases through the project's disk; a
	// sandbox with no project has one only if its lease asked for a disk of its
	// own, which keeps its files across a park and is deleted with the lease.
	// Without either, everything is lost with the pod.
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
	// CreatedAt is when the lease was taken, Unix seconds — by the lease that made
	// the sandbox, and again by each resume, which gives a parked sandbox a new pod
	// on a new lease. The ceiling on a sandbox's life runs from it.
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
	// Payer is WHO pays for the time this lease is held: the subject of the account
	// cloud.PayerOf resolved at the act that took it ("<org>" for a pooled org's
	// wallet, "<org>/<name>" for a person's), which is not the same value as Org — a
	// platform SuperAdmin leasing inside somebody else's org spends their own, and in
	// the signup org each member pays from their own wallet. The one-shot lease fee
	// already lands on it (api.go), and the recurring runtime charge has to land on
	// the same wallet or one lease is billed to two payers. A subject reads back as
	// the same account through account.PayerOf, so the column needs nothing more.
	//
	// It is `json:"-"`: a caller cannot choose it, and reading back whose ledger a
	// lease is charged to is the billing surface's job, not this row's.
	Payer string `json:"-"`
	// MeteredAt is the second through which this lease's runtime has been billed —
	// [cloud.Running.MeteredAt]. Zero means the meter has never seen it, which is
	// charged as nothing rather than as a debt back to the epoch.
	MeteredAt int64 `json:"-"`
	// DiskMeteredAt is the second through which this lease's STORAGE has been
	// billed. It is its own watermark because storage and runtime measure
	// different spans: runtime stops when the pod does, storage runs for as long
	// as the volume exists — through every parked hour, which is the whole reason
	// a parked lease is not free. Zero means the meter has never seen it, which
	// starts the clock at now rather than billing backwards.
	DiskMeteredAt int64 `json:"-"`
}

// Store is one org's sandbox registry — ONE database per org (cloud.OrgDB, HIP-0302):
// a SQLite file at {DataDir}/orgs/{orgSlug}/sandbox.db, or on hanzo-sql a schema of
// the org's namespace. Org isolation is PHYSICAL (a different file or schema), with
// the org column kept as defence in depth so a query that somehow reached the wrong
// one still returns nothing. Its SQL is the dialect both engines share
// (sdk/dialect).
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
  actor        TEXT NOT NULL DEFAULT '',
  class        TEXT NOT NULL DEFAULT 'exec',
  project      TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'pending',
  image        TEXT NOT NULL DEFAULT '',
  pod          TEXT NOT NULL DEFAULT '',
  runtime      TEXT NOT NULL DEFAULT '',
  volume       TEXT NOT NULL DEFAULT '',
  cluster      TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  created_at   BIGINT NOT NULL,
  last_used_at BIGINT NOT NULL,
  connected_at BIGINT NOT NULL DEFAULT 0,
  expires_at   BIGINT NOT NULL DEFAULT 0,
  payer        TEXT NOT NULL DEFAULT '',
  metered_at   BIGINT NOT NULL DEFAULT 0,
  disk_metered_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_machines_org_project ON sandbox(org, project);
CREATE INDEX IF NOT EXISTS ix_machines_org_status  ON sandbox(org, status);
CREATE TABLE IF NOT EXISTS sandbox_ended (
  id       TEXT PRIMARY KEY,
  org      TEXT NOT NULL,
  actor    TEXT NOT NULL DEFAULT '',
  class    TEXT NOT NULL DEFAULT '',
  project  TEXT NOT NULL DEFAULT '',
  reason   TEXT NOT NULL DEFAULT '',
  detail   TEXT NOT NULL DEFAULT '',
  ended_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_sandbox_ended_at ON sandbox_ended(ended_at);
`
	// One transaction through tx alone (Dialect.Migrate). A column is added only
	// when the table lacks it: a failed ALTER would abort the transaction on
	// PostgreSQL, so none is tried and forgiven.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d := dialect.Of(s.db)
	return d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		for _, add := range []struct{ column, def string }{
			// runtime: added after the initial schema. Fresh DBs get it from the CREATE
			// above; a DB that predates it gains it here, and its existing rows read as
			// the node default — which is what they in fact ran.
			{"runtime", "TEXT NOT NULL DEFAULT ''"},
			// connected_at: same story. Existing rows read as 0, which means "nobody has
			// said they are watching" — the honest answer for a row written before anyone
			// could say it, and the one that puts them on the short clock until a watcher
			// speaks up.
			{"connected_at", "BIGINT NOT NULL DEFAULT 0"},
			// disk_metered_at: the STORAGE meter, and a SECOND watermark rather than a
			// share of the runtime one. Two meters on one column cannot both advance it —
			// the CAS in Advance would have each undoing the other's claim — and these two
			// deliberately do not run together: runtime stops when the pod does, storage
			// goes on for as long as the volume exists. A row that predates the column
			// reads 0, which starts its disk clock at now rather than billing backwards for
			// every hour it was parked before anybody was charging for it.
			{"disk_metered_at", "BIGINT NOT NULL DEFAULT 0"},
			// payer + metered_at: the runtime meter. A lease that predates the meter reads
			// payer "" and metered_at 0, and BOTH of those are refusals rather than
			// defaults — cloud.RuntimeCharge bills nothing for a row with no payer, and
			// starts the clock at now for a row with no watermark. So shipping the meter
			// charges nobody for hours that were free when they ran, and never guesses
			// whose wallet an old lease belonged to.
			{"payer", "TEXT NOT NULL DEFAULT ''"},
			{"metered_at", "BIGINT NOT NULL DEFAULT 0"},
			{"cluster", "TEXT NOT NULL DEFAULT ''"},
			// actor: who leased the row. A row that predates it reads "", which is the
			// org's own and no person's — so a member reaches it only as an admin, the
			// closed answer for a lease nobody recorded the lessee of.
			{"actor", "TEXT NOT NULL DEFAULT ''"},
		} {
			has, err := d.HasColumn(ctx, tx, "sandbox", add.column)
			if err != nil {
				return fmt.Errorf("migrate %s column: %w", add.column, err)
			}
			if has {
				continue
			}
			if _, err := tx.ExecContext(ctx, `ALTER TABLE sandbox ADD COLUMN `+add.column+` `+add.def); err != nil {
				return fmt.Errorf("migrate %s column: %w", add.column, err)
			}
		}
		return nil
	})
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Put(ctx context.Context, m Sandbox) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sandbox (id,org,kind,actor,class,project,status,image,pod,runtime,volume,cluster,error,created_at,last_used_at,connected_at,expires_at,payer,metered_at,disk_metered_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
ON CONFLICT(id) DO UPDATE SET
  status=excluded.status, image=excluded.image, pod=excluded.pod, runtime=excluded.runtime,
  volume=excluded.volume,
  error=excluded.error, last_used_at=excluded.last_used_at, expires_at=excluded.expires_at`,
		m.ID, m.Org, m.Kind, m.Actor, m.Class, m.Project, m.Status, m.Image, m.Pod, m.Runtime, m.Volume,
		m.Cluster, m.Error, m.CreatedAt, m.LastUsedAt, m.ConnectedAt, m.ExpiresAt, m.Payer, m.MeteredAt, m.DiskMeteredAt)
	return err
}

// Shift moves one row to m's state, and only from one of the states named: the
// compare-and-set every change of a sandbox's state after its create goes
// through, so a park, a resume, a start and a retirement that race each land once
// or not at all. A Put would write whatever its caller read, and bring back a row
// a retirement had just deleted. It reports whether this call moved the row.
//
// It writes the state and the lease's own facts with it — the pod, the payer and
// the three clocks — and nothing a meter or a watcher owns.
func (s *Store) Shift(ctx context.Context, m Sandbox, from ...string) (bool, error) {
	if len(from) == 0 {
		return false, nil // no state to move from, so no row to move
	}
	args := []any{m.Status, m.Pod, m.Payer, m.CreatedAt, m.LastUsedAt, m.ExpiresAt, m.Error, m.Org, m.ID}
	for _, f := range from {
		args = append(args, f)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE sandbox SET status=$1, pod=$2, payer=$3, created_at=$4, last_used_at=$5, expires_at=$6, error=$7
		 WHERE org=$8 AND id=$9 AND status IN (`+dialect.Marks(10, len(from))+`)`,
		args...)
	if err != nil {
		return false, fmt.Errorf("shift sandbox: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Swap moves a running row onto m.Pod in m.Status, and only from the pod named.
// The pod is the compare: two calls that found the same lost pod start one new
// pod between them, and a call holding a stale read cannot move a row another
// call has already moved.
func (s *Store) Swap(ctx context.Context, m Sandbox, from string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sandbox SET status=$1, pod=$2 WHERE org=$3 AND id=$4 AND status=$5 AND pod=$6`,
		m.Status, m.Pod, m.Org, m.ID, statusRunning, from)
	if err != nil {
		return false, fmt.Errorf("swap sandbox pod: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Get is org-scoped in the WHERE clause, always. The file is already per-org;
// this is the second lock on the same path.
func (s *Store) Get(ctx context.Context, org, id string) (Sandbox, error) {
	row := s.db.QueryRowContext(ctx, selectCols+` WHERE org=$1 AND id=$2`, org, id)
	return scanMachine(row)
}

// Live is the single-attach check: the sandbox, if any, currently holding this
// project's volume. owner narrows it to one person's project (projectOwner).
func (s *Store) Live(ctx context.Context, org, owner, project string) (Sandbox, error) {
	q, args := ownedBy(` WHERE org=$1 AND project=$2 AND status IN ('running','pending')`, []any{org, project}, owner)
	row := s.db.QueryRowContext(ctx, selectCols+q+` LIMIT 1`, args...)
	m, err := scanMachine(row)
	if err == errNotFound {
		return Sandbox{}, nil
	}
	return m, err
}

// Holding is the sandbox holding this project's disk — live, or parked with the
// disk kept for it — other than self, if any. It is the single-attach rule's
// question, which is wider than Live's: a parked sandbox still holds its disk,
// and a second sandbox on the project would bill that one disk twice. owner
// narrows it to one person's project (projectOwner).
func (s *Store) Holding(ctx context.Context, org, owner, project, self string) (Sandbox, error) {
	q, args := ownedBy(` WHERE org=$1 AND project=$2 AND id<>$3 AND status IN ('running','pending','parked')`,
		[]any{org, project, self}, owner)
	row := s.db.QueryRowContext(ctx, selectCols+q+` LIMIT 1`, args...)
	m, err := scanMachine(row)
	if err == errNotFound {
		return Sandbox{}, nil
	}
	return m, err
}

// ownedBy narrows a project query to one person's project when the project is a
// person's rather than the org's (projectOwner). Empty owner is the org's project.
func ownedBy(q string, args []any, owner string) (string, []any) {
	if owner == "" {
		return q, args
	}
	return q + ` AND actor=$` + strconv.Itoa(len(args)+1), append(args, owner)
}

// List is one page of the org's sandboxes that `of` holds, newest first: every
// row for an admin, the caller's own otherwise. The narrowing is in the query and
// not after it, so a member's page is a page of THEIR sandboxes rather than their
// share of the org's first two hundred.
func (s *Store) List(ctx context.Context, org, project, status string, of holder) ([]Sandbox, error) {
	admin := of.adminOf(org)
	if !admin && of.unkeyed {
		return []Sandbox{}, nil // a person with no subject to hold one by holds none
	}
	q := selectCols + ` WHERE org=$1`
	args := []any{org}
	if !admin {
		args = append(args, of.actor)
		q += ` AND actor=$` + strconv.Itoa(len(args))
	}
	if project != "" {
		args = append(args, project)
		q += ` AND project=$` + strconv.Itoa(len(args))
	}
	if status != "" {
		args = append(args, status)
		q += ` AND status=$` + strconv.Itoa(len(args))
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Sandbox{}
	for rows.Next() {
		var m Sandbox
		if err := rows.Scan(&m.ID, &m.Org, &m.Kind, &m.Actor, &m.Class, &m.Project, &m.Status,
			&m.Image, &m.Pod, &m.Runtime, &m.Volume, &m.Cluster, &m.Error, &m.CreatedAt, &m.LastUsedAt, &m.ConnectedAt,
			&m.ExpiresAt, &m.Payer, &m.MeteredAt, &m.DiskMeteredAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LivePaid counts the paid sandboxes that still hold a pod and count against one
// account, every class together (quota.go): every paid one in the org when the
// account is the org, whoever paid, and otherwise that account's own.
//
// A COUNT and not a len(List): List is LIMIT 200, so a quota read through it would
// stop counting at 200 and stop refusing at exactly the point refusing matters.
func (s *Store) LivePaid(ctx context.Context, org, acct string) (int64, error) {
	q := `SELECT COUNT(*) FROM sandbox WHERE org=$1 AND payer<>'' AND status IN ('running','pending')`
	args := []any{org}
	if acct != org {
		q, args = q+` AND payer=$2`, append(args, acct)
	}
	var n int64
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n)
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
// MONOTONIC — `connected_at=CASE WHEN connected_at>$1 THEN connected_at ELSE $1 END`. Presence arrives from a
// heartbeat over a socket, so two beats can land out of order and an older one
// would otherwise move the clock BACKWARDS, briefly making a watched sandbox
// look stale to a sweep running in that window.
//
// It is also why Put does not write this column on conflict: an update carrying
// a row read before the last beat would undo it. One writer per fact.
func (s *Store) Watched(ctx context.Context, org, id string, at int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sandbox SET connected_at=CASE WHEN connected_at>$1 THEN connected_at ELSE $1 END WHERE org=$2 AND id=$3`, at, org, id)
	return err
}

// Touch stamps USE, and only on a sandbox that has a pod to use. One writer per
// fact, like Watched: a Put carries the whole row as its caller read it, and the
// row can have been parked since. A parked sandbox is not in use, and its stamp
// is the start of its hold (lifecycle.go), which a refused call must not move.
func (s *Store) Touch(ctx context.Context, org, id string, at int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sandbox SET last_used_at=CASE WHEN last_used_at>$1 THEN last_used_at ELSE $1 END WHERE org=$2 AND id=$3 AND status IN ('running','pending')`,
		at, org, id)
	return err
}

func (s *Store) Extend(ctx context.Context, org, id string, at int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sandbox SET expires_at=$1 WHERE org=$2 AND id=$3 AND expires_at>0 AND expires_at<$4`,
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
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM sandbox WHERE org=$1`, org)
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
// The predicate is `holding`, the same one Live and LivePaid use for "this
// sandbox is holding a pod", so what the fleet counts as occupied capacity and
// what it charges for cannot disagree.
func (s *Store) Held(ctx context.Context, org string) ([]Sandbox, error) {
	return s.rows(ctx, ` WHERE org=$1 AND status IN ('running','pending')`, org)
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
	return s.rows(ctx, ` WHERE org=$1`, org)
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
		if err := rows.Scan(&m.ID, &m.Org, &m.Kind, &m.Actor, &m.Class, &m.Project, &m.Status,
			&m.Image, &m.Pod, &m.Runtime, &m.Volume, &m.Cluster, &m.Error, &m.CreatedAt, &m.LastUsedAt, &m.ConnectedAt,
			&m.ExpiresAt, &m.Payer, &m.MeteredAt, &m.DiskMeteredAt); err != nil {
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
// AdvanceDisk is Advance for the storage meter's own watermark. Separate column,
// separate compare-and-set: the two meters claim different spans of the same
// lease and a shared watermark would have each rolling the other back.
func (s *Store) AdvanceDisk(ctx context.Context, org, id string, was, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sandbox SET disk_metered_at=$1 WHERE org=$2 AND id=$3 AND disk_metered_at=$4`, now, org, id, was)
	if err != nil {
		return false, fmt.Errorf("advance storage meter: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) Advance(ctx context.Context, org, id string, was, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sandbox SET metered_at=$1 WHERE org=$2 AND id=$3 AND metered_at=$4`, now, org, id, was)
	if err != nil {
		return false, fmt.Errorf("advance runtime meter: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) Delete(ctx context.Context, org, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sandbox WHERE org=$1 AND id=$2`, org, id)
	return err
}

// ended is what is left of a sandbox the reaper ended: who held it and why it
// went. The row is deleted with the lease, so without this the reason lived only
// in the reaper's log, and a coding run that lost its sandbox could say only that
// it was gone.
type tombstone struct {
	ID, Org, Actor, Class, Project string
	// Reason is the clock or ceiling that ended it, as clocks.ends names it.
	Reason string
	// Detail is a line more, when there is one: what a failed start said, or what a
	// lease cost against its cap.
	Detail  string
	EndedAt int64
}

// endedKeep is how long a tombstone is kept. A week: longer than any run is left
// unread, and short enough that the table holds about a week of reaps.
const endedKeep = 7 * 24 * time.Hour

// Bury records why a sandbox ended, and drops tombstones older than endedKeep.
// It is written BEFORE the row is deleted, so the ship that makes the deletion
// durable carries it too (settle).
func (s *Store) Bury(ctx context.Context, e tombstone) error {
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO sandbox_ended (id,org,actor,class,project,reason,detail,ended_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT(id) DO UPDATE SET
  reason=excluded.reason, detail=excluded.detail, ended_at=excluded.ended_at`,
		e.ID, e.Org, e.Actor, e.Class, e.Project, e.Reason, e.Detail, e.EndedAt); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sandbox_ended WHERE ended_at<$1`,
		e.EndedAt-int64(endedKeep/time.Second))
	return err
}

// Ended is the tombstone of a sandbox the reaper ended, if it is still kept.
// errNotFound when there is none: the sandbox never existed, was ended by its
// holder, or ended longer ago than endedKeep.
func (s *Store) Ended(ctx context.Context, org, id string, now int64) (tombstone, error) {
	var e tombstone
	err := s.db.QueryRowContext(ctx,
		`SELECT id,org,actor,class,project,reason,detail,ended_at FROM sandbox_ended WHERE org=$1 AND id=$2 AND ended_at>=$3`,
		org, id, now-int64(endedKeep/time.Second)).
		Scan(&e.ID, &e.Org, &e.Actor, &e.Class, &e.Project, &e.Reason, &e.Detail, &e.EndedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return tombstone{}, errNotFound
	}
	return e, err
}

const selectCols = `SELECT id,org,kind,actor,class,project,status,image,pod,runtime,volume,cluster,error,created_at,last_used_at,connected_at,expires_at,payer,metered_at,disk_metered_at FROM sandbox`

func scanMachine(row *sql.Row) (Sandbox, error) {
	var m Sandbox
	err := row.Scan(&m.ID, &m.Org, &m.Kind, &m.Actor, &m.Class, &m.Project, &m.Status,
		&m.Image, &m.Pod, &m.Runtime, &m.Volume, &m.Cluster, &m.Error, &m.CreatedAt, &m.LastUsedAt, &m.ConnectedAt,
		&m.ExpiresAt, &m.Payer, &m.MeteredAt, &m.DiskMeteredAt)
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

// podName is a pod's address, minted once per pod and NEVER REUSED. That is the
// whole property: an address that can be recycled is an address a second tenant
// can be handed, and no credential check downstream can undo it. A sandbox's
// first pod is named from its id; a resume mints its pod a fresh id (park.go).
func podName(id string) string { return "m-" + strings.TrimPrefix(id, IDPrefix) }

// diskName is the disk a sandbox with no project owns (Spec.Disk): one per
// sandbox and named by it, so nothing but its own row can ever name it — which is
// what lets it be deleted with that row. The id is minted from 96 random bits, so
// no hash is needed to keep two tenants apart, and the "d-" keeps it out of the
// shape every project disk has.
func diskName(id string) string { return "d-" + strings.TrimPrefix(id, IDPrefix) }

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
//
// owner is who the project belongs to inside the org (projectOwner): empty in an
// org of colleagues, whose projects are the org's, and the person in the signup
// org, where every member is a customer of their own. It goes into the hash and
// not the readable part, so an org's disks keep the names they have.
func volumeName(org, owner, project string) string {
	key := org + "\x00" + project
	if owner != "" {
		key = org + "\x00" + owner + "\x00" + project
	}
	sum := sha256.Sum256([]byte(key))
	// A person's disk shares its readable head with every other person's on the
	// same project name (everybody's first machine is cloud-1), so only the hash
	// keeps strangers apart: 128 bits for it, where 40 collide near a million
	// sign-ups. An org's disks keep their 40, and so their names.
	width := 5
	if owner != "" {
		width = 16
	}
	tail := "-" + hex.EncodeToString(sum[:width])
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
