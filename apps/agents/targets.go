package agents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/text"

	"github.com/hanzoai/cloud/samples"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/principal"
)

// A target is a place an agent session can be dispatched to run: a laptop, a cloud
// box, a GPU host, or a whole cluster. It is the #48 link-a-compute client over the
// SAME agents.db (one store, one tenancy column) as sessions/events — NOT a rival
// device registry. It composes with the compute fleet rather than duplicating it: a
// session records the target id it runs on (agent_sessions.target), and the org's
// unified board (GET /v1/compute/fleet, apps/visor/board.go) unions these registered
// targets with its BYO workers (GET /v1/compute/fleet/workers), BYO clusters and Visor
// machines — reading this registry through the in-process client below rather than
// copying it.
//
//	POST   /v1/agent/targets        register a target -> Target
//	GET    /v1/agent/targets        list the org's targets (+ live session load)
//	GET    /v1/agent/targets/:id    one target + its running/total session counts
//	PATCH  /v1/agent/targets/:id    update label/kind/status/capacity/host
//	DELETE /v1/agent/targets/:id    deregister
//
// Every route is org-scoped through principal.Org (tenant), fail-closed — a tenant
// can never see or mutate another org's targets, exactly like sessions.
//
// A write carrying `metrics` IS a heartbeat, and a heartbeat is two facts, not one:
// the LAST sample (kept on the row, rendered by the views here) and one point in a
// utilization SERIES (appended to clients/samples). The row answers "is this machine
// alive and what is it doing now"; the series answers "how hot has it been". The
// append is best-effort and detached — see recordSample.

// Target kinds — the closed vocabulary of dispatch destinations.
const (
	TargetLaptop  = "laptop"
	TargetCloud   = "cloud"
	TargetGPU     = "gpu"
	TargetCluster = "cluster"
	TargetMachine = "machine"
)

// Target status — a registered target is online until marked otherwise.
const (
	TargetOnline   = "online"
	TargetOffline  = "offline"
	TargetDraining = "draining"
)

// LiveWindow bounds heartbeat freshness. A target that has heartbeated before but
// not within this window is reported offline no matter what its row says. Matched
// to the agent beat (30s) with slack so one missed beat does not flap a machine.
const LiveWindow = 90 * time.Second

const (
	maxTargetLabel    = 128
	maxTargetCapacity = 256
	maxTargetID       = 128
)

// EffectiveStatus is the ONE liveness answer every reader uses — the views here,
// the fleet board's agent fold, and the dispatch gate. The stored status records
// operator INTENT ("I am draining this box"); liveness is a FACT the heartbeat
// decides, and the fact wins. Without this a worker that died — or whose host was
// simply powered off — stays "online" forever, because nothing ever writes the row
// again to say otherwise. That is how the fleet board came to show two GPUs online
// that had last beaten five and nine days earlier.
//
// A target that has never heartbeated (MetricsAt == 0) keeps its stored status: it
// is a hand-registered destination, not a beating agent, and has no fact to check.
func (t Target) EffectiveStatus(now time.Time) string {
	if t.Status != TargetOnline || t.MetricsAt == 0 {
		return t.Status // offline/draining are intent; never-beaten has no fact
	}
	if now.Sub(time.Unix(t.MetricsAt, 0)) > LiveWindow {
		return TargetOffline
	}
	return TargetOnline
}

func validTargetKind(k string) bool {
	switch k {
	case TargetLaptop, TargetCloud, TargetGPU, TargetCluster, TargetMachine:
		return true
	}
	return false
}

func validTargetStatus(s string) bool {
	switch s {
	case TargetOnline, TargetOffline, TargetDraining:
		return true
	}
	return false
}

var errTargetNotFound = errors.New("agents: target not found")

// Target is a registered agent run-target. Owned by one org. Spec is its static
// capability (os/arch/cpus/memory/gpus) and Metrics its last live heartbeat
// (loadavg/memory/gpu-util); MetricsAt is the unix second that heartbeat was recorded
// (0 = never). See targetspec.go for the value plane.
type Target struct {
	ID         string
	Org        string
	Owner      string // who registered this machine (self): their subject in a personal org, their validated user id elsewhere; "" for a pre-migration row
	Label      string
	Kind       string // laptop | cloud | gpu | cluster | machine
	Status     string // online | offline | draining
	Capacity   string // free-form ("8 vCPU / 32G", "1× GB10") — human summary
	Host       string // hostname sessions on this machine report (maps sessions -> target)
	Spec       Spec   // static capability
	Metrics    Metrics
	MetricsAt  int64
	Terminal   string // the URL the machine published its live terminal at; "" when none (see LiveTerminal)
	TerminalAt int64  // unix second the server stamped it
	CreatedAt  int64
	UpdatedAt  int64
}

// TargetLoad is the live session load on a target.
type TargetLoad struct {
	Sessions int // total sessions mapped to the target
	Running  int // of those, how many are currently running
}

// migrateTargets creates the targets table in the SAME agents.db (one store, one
// tenancy column). Idempotent (IF NOT EXISTS), called from migrate().
func (s *Store) migrateTargets(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS agent_targets (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  owner      TEXT NOT NULL DEFAULT '',
  label      TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL DEFAULT 'machine',
  status     TEXT NOT NULL DEFAULT 'online',
  capacity   TEXT NOT NULL DEFAULT '',
  host       TEXT NOT NULL DEFAULT '',
  spec       TEXT NOT NULL DEFAULT '',
  metrics    TEXT NOT NULL DEFAULT '',
  metrics_at BIGINT NOT NULL DEFAULT 0,
  terminal    TEXT NOT NULL DEFAULT '',
  terminal_at BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_targets_org_created ON agent_targets(org, created_at);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate targets: %w", err)
	}
	// Forward, idempotent upgrade for target rows created before the capability +
	// metrics + owner columns existed. column-guarded, so re-running on an upgraded
	// DB is a no-op — the DDL above covers fresh installs, this covers pre-existing
	// ones. A pre-owner row backfills owner='' (unowned) and is admin-only until its
	// owner re-registers (register binds the owner) — see registerTarget.
	if err := s.addColumns(ctx, tx, "agent_targets", map[string]string{
		"owner":       "TEXT NOT NULL DEFAULT ''",
		"spec":        "TEXT NOT NULL DEFAULT ''",
		"metrics":     "TEXT NOT NULL DEFAULT ''",
		"metrics_at":  "BIGINT NOT NULL DEFAULT 0",
		"terminal":    "TEXT NOT NULL DEFAULT ''",
		"terminal_at": "BIGINT NOT NULL DEFAULT 0",
	}); err != nil {
		return err
	}
	return nil
}

const targetCols = `id,org,owner,label,kind,status,capacity,host,spec,metrics,metrics_at,terminal,terminal_at,created_at,updated_at`

func scanTarget(sc interface{ Scan(...any) error }) (Target, error) {
	var t Target
	var spec, metrics string
	err := sc.Scan(&t.ID, &t.Org, &t.Owner, &t.Label, &t.Kind, &t.Status, &t.Capacity, &t.Host,
		&spec, &metrics, &t.MetricsAt, &t.Terminal, &t.TerminalAt, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return t, err
	}
	t.Spec = decodeSpec(spec)
	t.Metrics = decodeMetrics(metrics)
	return t, nil
}

// targetText refuses a target whose text one engine could not store
// (dialect.Text), before a statement is written for it.
func targetText(t Target) error {
	return dialect.Text(t.ID, t.Org, t.Owner, t.Label, t.Kind, t.Status, t.Capacity, t.Host)
}

// CreateTarget inserts one target. The id is caller-generated (mint.ID("tgt")).
func (s *Store) CreateTarget(ctx context.Context, t Target) error {
	if err := targetText(t); err != nil {
		return fmt.Errorf("insert target: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_targets (`+targetCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		t.ID, t.Org, t.Owner, t.Label, t.Kind, t.Status, t.Capacity, t.Host,
		encodeSpec(t.Spec), encodeMetrics(t.Metrics), t.MetricsAt, t.Terminal, t.TerminalAt, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert target: %w", err)
	}
	return nil
}

// GetTarget returns the (org,id) target or errTargetNotFound. Org is part of the key
// so one tenant can never resolve another's target id.
func (s *Store) GetTarget(ctx context.Context, org, id string) (Target, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+targetCols+` FROM agent_targets WHERE org=$1 AND id=$2`, org, id)
	t, err := scanTarget(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Target{}, errTargetNotFound
	}
	if err != nil {
		return Target{}, fmt.Errorf("get target: %w", err)
	}
	return t, nil
}

// ListTargets returns an org's targets, newest first.
func (s *Store) ListTargets(ctx context.Context, org string) ([]Target, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+targetCols+` FROM agent_targets WHERE org=$1 ORDER BY created_at DESC, id ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Target
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, fmt.Errorf("scan target: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTarget persists mutable fields for an existing (org,id) target. Scoped by org
// so a cross-tenant id can never mutate another's target. owner is persisted too so a
// relink can BIND a previously-unowned row (registerTarget) and a patch preserves the
// owner it read; no client-facing patch field sets owner, so it never moves by mutation.
func (s *Store) UpdateTarget(ctx context.Context, t Target) error {
	if err := targetText(t); err != nil {
		return fmt.Errorf("update target: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_targets SET owner=$1, label=$2, kind=$3, status=$4, capacity=$5, host=$6, spec=$7, metrics=$8, metrics_at=$9, terminal=$10, terminal_at=$11, updated_at=$12
		 WHERE org=$13 AND id=$14`,
		t.Owner, t.Label, t.Kind, t.Status, t.Capacity, t.Host,
		encodeSpec(t.Spec), encodeMetrics(t.Metrics), t.MetricsAt, t.Terminal, t.TerminalAt, t.UpdatedAt, t.Org, t.ID)
	if err != nil {
		return fmt.Errorf("update target: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errTargetNotFound
	}
	return nil
}

// GetLinkableTargetByHost returns the target for (org,host) that the caller `me`
// may re-link — its OWN row, under their id or, as an API key of theirs wrote it
// before, their row (principal.Self.Is), else an UNOWNED (pre-migration) row,
// which registerTarget adopts only for a caller who already manages it —
// preferring the id, then the row, newest first. A row owned by a DIFFERENT
// principal is NEVER returned, so a re-link can never clobber another member's
// machine: the caller gets its own row or (falling through in registerTarget) a fresh
// one. errTargetNotFound when nothing linkable exists.
func (s *Store) GetLinkableTargetByHost(ctx context.Context, org, host string, me iam.Self) (Target, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return Target{}, errTargetNotFound
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+targetCols+` FROM agent_targets
		 WHERE org=$1 AND host=$2 AND (owner=$3 OR owner=$4 OR owner='')
		 ORDER BY CASE owner WHEN $3 THEN 0 WHEN $4 THEN 1 ELSE 2 END, created_at DESC, id ASC LIMIT 1`,
		org, host, me.ID, me.Row)
	t, err := scanTarget(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Target{}, errTargetNotFound
	}
	if err != nil {
		return Target{}, fmt.Errorf("get linkable target by host: %w", err)
	}
	return t, nil
}

// GetTargetByHost returns an org's target reporting the given host, or
// errTargetNotFound. It is how a re-link of the SAME machine finds its existing target
// (idempotent register) instead of creating a duplicate. Org-scoped: a host string can
// never resolve another tenant's target. Newest wins if a host was ever double-listed.
func (s *Store) GetTargetByHost(ctx context.Context, org, host string) (Target, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return Target{}, errTargetNotFound
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+targetCols+` FROM agent_targets WHERE org=$1 AND host=$2 ORDER BY created_at DESC, id ASC LIMIT 1`,
		org, host)
	t, err := scanTarget(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Target{}, errTargetNotFound
	}
	if err != nil {
		return Target{}, fmt.Errorf("get target by host: %w", err)
	}
	return t, nil
}

// ---- the in-process client (org-scoped, fail-closed) ----
//
// TargetsForOrg / LoadOn are the exported twins of the list + detail reads above:
// the ONE way another in-process subsystem (the /v1/compute/fleet board in clients/visor)
// reads this registry WITHOUT an HTTP hop back through the gateway — the same
// shape ListForOrg gives the agent registry. They are two ORTHOGONAL values on
// purpose: a target is what the machine IS, its load is what is running on it, and
// a caller that only needs the inventory does not pay for the rollups.
//
// ISOLATION: org is the ONLY tenant key and is threaded verbatim into the
// org-scoped store methods, so a caller for org A can never enumerate or resolve
// org B's targets. The caller MUST pass an org it already validated server-side
// (principal.Org), never a raw client header.

// TargetsForOrg returns the org's registered run-targets the caller on ctx
// reaches, from the in-process store, newest first: in a personal org, their own
// (every one, for a SuperAdmin). It filters here, at the registry, so no reader
// of it — the fleet board, its samples — can list a stranger's machine. Fails
// closed when the subsystem is not mounted or the org is empty/oversized.
func TargetsForOrg(ctx context.Context, org string) ([]Target, error) {
	sto, org, err := mountedStore(org)
	if err != nil {
		return nil, err
	}
	rows, err := sto.ListTargets(ctx, org)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, t := range rows {
		if reaches(ctx, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// Readable names the linked machines of org whose fleet series the caller on ctx
// may read, for the series query to narrow to (samples.Query.Held). A linked
// machine's series is as much its owner's as the machine is, so in a personal org
// (held) it is the machines the caller reaches (TargetsForOrg), and none when
// the registry cannot be read. In a customer's org every member reads every
// machine's series, and a SuperAdmin reaches every machine anywhere: held is
// false and ids nil.
func Readable(ctx context.Context, org string) (ids []string, held bool) {
	if !personal(org) || principal.SuperFrom(ctx) {
		return nil, false
	}
	ids = []string{}
	if ts, err := TargetsForOrg(ctx, org); err == nil {
		for _, t := range ts {
			ids = append(ids, t.ID)
		}
	}
	return ids, true
}

// ResolveTarget resolves a human's target REFERENCE — a target id or its friendly
// label (the hostname the CLI registers) — to the org's target, org-scoped and
// fail-closed. It is the ONE way a trigger surface (the Slack `code: <repo> on
// <target>` grammar, a console picker) turns "on evo" into a target id without
// leaking another tenant's inventory: an id or label that resolves to no target in
// THIS org returns errTargetNotFound, never another org's machine.
//
// Precedence: an exact id match wins (ids are unambiguous), else an exact,
// case-folded label match (newest first, so a re-registered machine's live row is
// preferred). A reference that matches neither is not found — the caller renders an
// honest error and NEVER falls back to a local run.
//
// In a personal org a label is matched among the caller's own machines alone. A
// label is any name a member gives their machine, so matching it across members
// would hand a run meant for the sender's "spark" to the stranger who named theirs
// "spark" first; a SuperAdmin reaches another's machine there by its exact id.
func ResolveTarget(ctx context.Context, org, ref string) (Target, error) {
	sto, org, err := mountedStore(org)
	if err != nil {
		return Target{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > maxTargetID {
		return Target{}, errTargetNotFound
	}
	// An id is exact and unambiguous — try it first.
	if t, err := sto.GetTarget(ctx, org, ref); err == nil && reaches(ctx, t) {
		return t, nil
	} else if err != nil && err != errTargetNotFound {
		return Target{}, err
	}
	// Else an exact, case-folded label match within this org — in a personal
	// org, among the caller's own machines.
	me, err := self(ctx, org)
	if err != nil {
		return Target{}, err
	}
	rows, err := sto.ListTargets(ctx, org)
	if err != nil {
		return Target{}, err
	}
	for _, t := range rows { // ListTargets is newest-first: the live row wins a label tie
		if strings.EqualFold(strings.TrimSpace(t.Label), ref) && (!personal(org) || owner(t, me)) {
			return t, nil
		}
	}
	return Target{}, errTargetNotFound
}

// LoadOn returns the live session load on one of the org's targets — the same
// mapping the HTTP views use (SessionLoad), so the board and /v1/agent/targets
// can never disagree about what is running where. In a personal org it answers
// only for a machine the caller reaches.
func LoadOn(ctx context.Context, org, id, host string) (TargetLoad, error) {
	sto, org, err := mountedStore(org)
	if err != nil {
		return TargetLoad{}, err
	}
	if personal(org) {
		if _, err := sto.reachable(ctx, org, id); err != nil {
			return TargetLoad{}, err
		}
	}
	return sto.SessionLoad(ctx, org, id, host)
}

// DeleteTarget removes an org's target. Sessions keep their recorded target id (a
// historical fact); a detached target simply stops appearing in the registry.
func (s *Store) DeleteTarget(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_targets WHERE org=$1 AND id=$2`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete target: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SessionLoad returns how many of an org's sessions are mapped to a target: those
// explicitly dispatched to it (target == id) OR reporting its host (host == host, when
// the target has a host). One exact query (no double count) per target — the list is
// small so the per-row cost matches the sessions list's own rollups.
//
// In a personal org a session counts on a machine by the machine's id alone. A
// host is free text every member's sessions report, so two strangers' laptops of
// one name would otherwise each count the other's sessions.
func (s *Store) SessionLoad(ctx context.Context, org, id, host string) (TargetLoad, error) {
	if personal(org) {
		host = ""
	}
	var total, running int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status='running' THEN 1 ELSE 0 END),0)
		 FROM agent_sessions
		 WHERE org=$1 AND (target=$2 OR ($3<>'' AND host=$3))`,
		org, id, host).Scan(&total, &running)
	if err != nil {
		return TargetLoad{}, fmt.Errorf("session load: %w", err)
	}
	return TargetLoad{Sessions: total, Running: running}, nil
}

// ---- the fleet time series ----
//
// A heartbeat is the ONE moment this process learns what a linked machine is
// doing, so it is also where the fleet's utilization series is fed. The target row
// keeps the LAST sample (the snapshot the views render, unchanged); clients/samples
// keeps every sample over time. Two different questions — "is it alive now" and
// "how hot has it been" — so two homes, one write.

// sampleTimeout bounds the warehouse write. Generous (the insert is one small row
// in-cluster) but finite, so a wedged datastore can never hold the goroutine open.
const sampleTimeout = 5 * time.Second

// sampleOf projects a target's server-stamped heartbeat into a fleet sample. PURE
// (no clock, no I/O, no store) so the whole projection is unit-testable and the
// caller decides when it runs.
//
// cost_cents is 0: an agent run-target is the operator's OWN machine (a laptop, a
// dialed-in box) — the fleet meters its utilization, it does not resell it. A
// priced source (visor/cloud) fills that column from its own resale price.
func sampleOf(t Target) samples.Sample {
	var model string
	if len(t.Spec.GPUs) > 0 {
		// The representative accelerator: the count already rides in GPUs, so the
		// first card's model names the row. A heterogeneous host is rare enough
		// that naming its first card beats inventing a summary string here.
		model = t.Spec.GPUs[0].Model
		if model == "" {
			model = t.Spec.GPUs[0].Vendor
		}
	}
	return samples.Sample{
		Org:    t.Org,
		Source: samples.SourceAgent,
		Unit:   t.ID,
		Host:   t.Host,
		Kind:   t.Kind,
		At:     time.Unix(t.MetricsAt, 0).UTC(),

		CPUs:     t.Spec.CPUs,
		Memory:   t.Spec.Memory,
		MemUsed:  t.Metrics.MemUsed,
		MemFree:  t.Metrics.MemFree,
		Load1:    t.Metrics.Load1,
		Load5:    t.Metrics.Load5,
		Load15:   t.Metrics.Load15,
		GPUUtil:  t.Metrics.GPUUtil,
		GPUs:     len(t.Spec.GPUs),
		GPUModel: model,

		CPUUtil:       t.Metrics.CPUUtil,
		CPUTemp:       t.Metrics.CPUTemp,
		GPUTemp:       t.Metrics.GPUTemp,
		GPUPower:      t.Metrics.GPUPower,
		GPUPowerLimit: t.Metrics.GPUPowerLimit,
		GPUMemUsed:    t.Metrics.GPUMemUsed,
		GPUMemTotal:   t.Metrics.GPUMemTotal,
		DiskUsed:      t.Metrics.DiskUsed,
		DiskTotal:     t.Metrics.DiskTotal,
		DiskRead:      t.Metrics.DiskRead,
		DiskWrite:     t.Metrics.DiskWrite,
		NetRx:         t.Metrics.NetRx,
		NetTx:         t.Metrics.NetTx,
		Model:         t.Metrics.Model,
		Decode:        t.Metrics.Decode,
		Prefill:       t.Metrics.Prefill,
		TTFT:          t.Metrics.TTFT,
		Running:       t.Metrics.Running,
		Waiting:       t.Metrics.Waiting,
		KVCache:       t.Metrics.KVCache,
	}
}

// recordSample appends a heartbeat to the fleet series. Best-effort and DETACHED
// on purpose — the warehouse is never in the heartbeat's critical path:
//
//   - it runs on its own bounded context, so neither a slow datastore nor the
//     client hanging up mid-request can stall or cancel the write;
//   - it never touches the response, so the /v1/agent/targets contract is
//     byte-identical whether the warehouse is present, absent or on fire;
//   - a failure is logged, never surfaced — a dropped sample must not cost a
//     machine its heartbeat.
//
// This is the shape the billing warehouse write already uses (`go zapWriteUsage`):
// the client is synchronous, the CALLER owns the concurrency.
func recordSample(s *cloud.Service[state], t Target) {
	if t.MetricsAt == 0 {
		return // no heartbeat in this write — nothing to append
	}
	sample := sampleOf(t) // project on the caller's goroutine: t must not escape mutably
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sampleTimeout)
		defer cancel()
		if err := samples.Record(ctx, sample); err != nil {
			s.Log.Warn("fleet sample write failed", "org", sample.Org, "unit", sample.Unit, "err", err)
		}
	}()
}

// ---- HTTP shapes (the published contract) ----

type targetView struct {
	// ID is the machine's handle, minted as "tgt_" + 32 hex characters. It is what a
	// session records to say it ran here, and what every later patch, claim or
	// delete addresses.
	ID string `json:"id"`
	// Label is the name a person gave the machine ("workshop"), up to 128
	// characters. Required at register, free text, and the only field here meant for
	// reading rather than matching.
	Label string `json:"label"`
	// Kind is what sort of destination this is, from a closed five: laptop | cloud |
	// gpu | cluster | machine. A register that named none is a `machine`.
	Kind string `json:"kind"`
	// Status is the EFFECTIVE liveness — online | offline | draining — not the
	// stored one. offline and draining are operator INTENT and are reported as they
	// stand; `online` is checked against the heartbeat, and a machine that has beaten
	// before but not in the last 90 seconds reports offline whatever its row says. A
	// target that has NEVER beaten keeps its stored status, because a hand-registered
	// destination has no fact to check.
	Status string `json:"status"`
	// Capacity is a human summary of what the machine has ("8 vCPU / 32G", "1× GB10"),
	// up to 256 characters. Prose for a card — Spec is the same thing in a form a
	// scheduler can read, and nothing derives one from the other.
	Capacity string `json:"capacity,omitempty"`
	// Host is the hostname sessions on this machine report, and it is a JOIN KEY, not
	// a label: a session naming this host counts against the load below even when it
	// names no target id, and a re-link of the same (org, host, owner) refreshes this
	// row instead of creating a second. Empty means the machine is addressable only
	// by ID.
	Host string `json:"host,omitempty"`
	// Spec is what the machine IS — os, arch, cores, RAM, accelerators — the static
	// half, changed only when something reports it again. Absent when nothing has
	// ever been reported, and a scheduler reads absence as "cannot satisfy a floor"
	// rather than as "no limits".
	Spec *Spec `json:"spec,omitempty"`
	// Metrics is what the machine was DOING at its last heartbeat — loadavg, memory,
	// accelerator utilization. Absent when it has never beaten. It is a SNAPSHOT:
	// the series over time lives in the fleet samples, not here.
	Metrics *Metrics `json:"metrics,omitempty"`
	// MetricsAt is when that heartbeat was recorded, RFC 3339 in UTC, and the SERVER
	// stamps it — a client cannot backdate or forge the staleness clock. Absent means
	// never beaten, which is exactly the case where Status is taken at its word.
	MetricsAt string `json:"metricsAt,omitempty"`
	// Sessions is how many of the org's sessions are mapped to this machine, by
	// target id OR by matching Host. All of them, whatever their status.
	Sessions int `json:"sessions"`
	// Running is how many of those are in `running` right now — the number a
	// dispatcher weighs against Capacity. paused sessions are in Sessions and not
	// here.
	Running int `json:"running"`
	// CreatedAt is when the machine was first registered, RFC 3339 in UTC. A re-link
	// refreshes the row and leaves this alone, so it dates the machine and not the
	// connection.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is the last write to the row, same format — which for a beating
	// machine is its last heartbeat, since a heartbeat IS a write.
	UpdatedAt string `json:"updatedAt"`
	// Serving says whether a run the caller sends here now would be taken: the
	// machine is the caller's (or the caller is an org admin), and it is online
	// with a runner that polled for work within the last 90 seconds — the online
	// and runner half is what dispatch checks (TargetDispatchable). A heartbeat
	// alone keeps a machine online, not serving, and a colleague's machine never
	// serves the caller.
	Serving bool `json:"serving"`
	// Folders are the folders on this machine the web may start runs in, as its
	// owner granted them: a run may work in one or under one. A machine reads them
	// on every heartbeat and drops any folder that is no longer here.
	Folders []string `json:"folders"`
	// Terminal is where the machine's live terminal can be watched, an https URL.
	// It is the machine's own fact, kept alive by the machine's heartbeat: present
	// only while the machine is online and has restated it within the last 90
	// seconds, so it does not outlive the shell it points at, and it does not
	// depend on any session surviving the session reaper. Shown only to the
	// machine's owner (or an org admin); a colleague's machine never carries it.
	Terminal string `json:"terminal,omitempty"`
}

// view is one machine as the routes publish it to the caller: its row, its live
// session load, and whether it serves the caller.
func (s *Store) view(ctx context.Context, org string, t Target) targetView {
	load, _ := s.SessionLoad(ctx, org, t.ID, t.Host)
	v := toTargetView(t, load)
	owns := targetOwns(ctx, t)
	v.Serving = owns && s.serves(ctx, t, time.Now()) == nil
	if owns {
		v.Terminal = t.LiveTerminal(time.Now()) // a colleague's machine never shows its terminal
	}
	active, _ := s.Folders(ctx, org, t.ID)
	v.Folders = paths(active)
	return v
}

func toTargetView(t Target, load TargetLoad) targetView {
	v := targetView{
		ID: t.ID, Label: t.Label, Kind: t.Kind, Status: t.EffectiveStatus(time.Now()),
		Capacity: t.Capacity, Host: t.Host,
		Sessions: load.Sessions, Running: load.Running,
		CreatedAt: text.Unix(t.CreatedAt), UpdatedAt: text.Unix(t.UpdatedAt),
		Folders: []string{},
	}
	if !t.Spec.IsZero() {
		spec := t.Spec
		v.Spec = &spec
	}
	if !t.Metrics.IsZero() {
		m := t.Metrics
		m.At = t.MetricsAt
		v.Metrics = &m
	}
	if t.MetricsAt > 0 {
		v.MetricsAt = text.Unix(t.MetricsAt)
	}
	return v
}
