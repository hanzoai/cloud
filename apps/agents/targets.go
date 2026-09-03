package agents

import (
	"github.com/hanzoai/cloud/internal/stamp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/samples"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/zap-proto/zip"
)

// A target is a place an agent session can be dispatched to run: a laptop, a cloud
// box, a GPU host, or a whole cluster. It is the #48 link-a-compute client over the
// SAME agents.db (one store, one tenancy column) as sessions/events — NOT a rival
// device registry. It composes with the compute fleet rather than duplicating it: a
// session records the target id it runs on (agent_sessions.target), and the org's
// unified board (GET /v1/visor/fleet, apps/visor/board.go) unions these registered
// targets with its BYO workers (GET /v1/visor/fleet/workers), BYO clusters and Visor
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
	ID        string
	Org       string
	Owner     string // the VALIDATED principal (c.User()) that registered this machine; "" for a pre-migration row
	Label     string
	Kind      string // laptop | cloud | gpu | cluster | machine
	Status    string // online | offline | draining
	Capacity  string // free-form ("8 vCPU / 32G", "1× GB10") — human summary
	Host      string // hostname sessions on this machine report (maps sessions -> target)
	Spec      Spec   // static capability
	Metrics   Metrics
	MetricsAt int64
	CreatedAt int64
	UpdatedAt int64
}

// TargetLoad is the live session load on a target.
type TargetLoad struct {
	Sessions int // total sessions mapped to the target
	Running  int // of those, how many are currently running
}

// migrateTargets creates the targets table in the SAME agents.db (one store, one
// tenancy column). Idempotent (IF NOT EXISTS), called from migrate().
func (s *Store) migrateTargets() error {
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
  metrics_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_targets_org_created ON agent_targets(org, created_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate targets: %w", err)
	}
	// Forward, idempotent upgrade for target rows created before the capability +
	// metrics + owner columns existed. PRAGMA-guarded, so re-running on an upgraded
	// DB is a no-op — the DDL above covers fresh installs, this covers pre-existing
	// ones. A pre-owner row backfills owner='' (unowned) and is admin-only until its
	// owner re-registers (register binds the owner) — see registerTarget.
	if err := s.addColumns("agent_targets", map[string]string{
		"owner":      "TEXT NOT NULL DEFAULT ''",
		"spec":       "TEXT NOT NULL DEFAULT ''",
		"metrics":    "TEXT NOT NULL DEFAULT ''",
		"metrics_at": "INTEGER NOT NULL DEFAULT 0",
	}); err != nil {
		return err
	}
	return nil
}

const targetCols = `id,org,owner,label,kind,status,capacity,host,spec,metrics,metrics_at,created_at,updated_at`

func scanTarget(sc interface{ Scan(...any) error }) (Target, error) {
	var t Target
	var spec, metrics string
	err := sc.Scan(&t.ID, &t.Org, &t.Owner, &t.Label, &t.Kind, &t.Status, &t.Capacity, &t.Host,
		&spec, &metrics, &t.MetricsAt, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return t, err
	}
	t.Spec = decodeSpec(spec)
	t.Metrics = decodeMetrics(metrics)
	return t, nil
}

// CreateTarget inserts one target. The id is caller-generated (mint.ID("tgt")).
func (s *Store) CreateTarget(ctx context.Context, t Target) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_targets (`+targetCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Org, t.Owner, t.Label, t.Kind, t.Status, t.Capacity, t.Host,
		encodeSpec(t.Spec), encodeMetrics(t.Metrics), t.MetricsAt, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert target: %w", err)
	}
	return nil
}

// GetTarget returns the (org,id) target or errTargetNotFound. Org is part of the key
// so one tenant can never resolve another's target id.
func (s *Store) GetTarget(ctx context.Context, org, id string) (Target, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+targetCols+` FROM agent_targets WHERE org=? AND id=?`, org, id)
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
		`SELECT `+targetCols+` FROM agent_targets WHERE org=? ORDER BY created_at DESC, id ASC`, org)
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
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_targets SET owner=?, label=?, kind=?, status=?, capacity=?, host=?, spec=?, metrics=?, metrics_at=?, updated_at=?
		 WHERE org=? AND id=?`,
		t.Owner, t.Label, t.Kind, t.Status, t.Capacity, t.Host,
		encodeSpec(t.Spec), encodeMetrics(t.Metrics), t.MetricsAt, t.UpdatedAt, t.Org, t.ID)
	if err != nil {
		return fmt.Errorf("update target: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errTargetNotFound
	}
	return nil
}

// GetLinkableTargetByHost returns the target for (org,host) that the caller `owner`
// may re-link — its OWN row, else an UNOWNED (pre-migration) row it may adopt —
// preferring the exact-owner match, newest first. A row owned by a DIFFERENT
// principal is NEVER returned, so a re-link can never clobber another member's
// machine: the caller gets its own row or (falling through in registerTarget) a fresh
// one. errTargetNotFound when nothing linkable exists.
func (s *Store) GetLinkableTargetByHost(ctx context.Context, org, host, owner string) (Target, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return Target{}, errTargetNotFound
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+targetCols+` FROM agent_targets
		 WHERE org=? AND host=? AND (owner=? OR owner='')
		 ORDER BY CASE WHEN owner=? THEN 0 ELSE 1 END, created_at DESC, id ASC LIMIT 1`,
		org, host, owner, owner)
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
		`SELECT `+targetCols+` FROM agent_targets WHERE org=? AND host=? ORDER BY created_at DESC, id ASC LIMIT 1`,
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
// the ONE way another in-process subsystem (the /v1/visor/fleet board in clients/visor)
// reads this registry WITHOUT an HTTP hop back through the gateway — the same
// shape ListForOrg gives the agent registry. They are two ORTHOGONAL values on
// purpose: a target is what the machine IS, its load is what is running on it, and
// a caller that only needs the inventory does not pay for the rollups.
//
// ISOLATION: org is the ONLY tenant key and is threaded verbatim into the
// org-scoped store methods, so a caller for org A can never enumerate or resolve
// org B's targets. The caller MUST pass an org it already validated server-side
// (principal.Org), never a raw client header.

// TargetsForOrg returns the org's registered run-targets from the in-process
// store, newest first. Fails closed when the subsystem is not mounted or the org
// is empty/oversized.
func TargetsForOrg(ctx context.Context, org string) ([]Target, error) {
	sto, org, err := mountedStore(org)
	if err != nil {
		return nil, err
	}
	return sto.ListTargets(ctx, org)
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
	if t, err := sto.GetTarget(ctx, org, ref); err == nil {
		return t, nil
	} else if err != errTargetNotFound {
		return Target{}, err
	}
	// Else an exact, case-folded label match within this org.
	rows, err := sto.ListTargets(ctx, org)
	if err != nil {
		return Target{}, err
	}
	for _, t := range rows { // ListTargets is newest-first: the live row wins a label tie
		if strings.EqualFold(strings.TrimSpace(t.Label), ref) {
			return t, nil
		}
	}
	return Target{}, errTargetNotFound
}

// LoadOn returns the live session load on one of the org's targets — the same
// (target id OR host) mapping the HTTP views use, so the board and /v1/agent/
// targets can never disagree about what is running where.
func LoadOn(ctx context.Context, org, id, host string) (TargetLoad, error) {
	sto, org, err := mountedStore(org)
	if err != nil {
		return TargetLoad{}, err
	}
	return sto.SessionLoad(ctx, org, id, host)
}

// DeleteTarget removes an org's target. Sessions keep their recorded target id (a
// historical fact); a detached target simply stops appearing in the registry.
func (s *Store) DeleteTarget(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_targets WHERE org=? AND id=?`, org, id)
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
func (s *Store) SessionLoad(ctx context.Context, org, id, host string) (TargetLoad, error) {
	var total, running int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status='running' THEN 1 ELSE 0 END),0)
		 FROM agent_sessions
		 WHERE org=? AND (target=? OR (?<>'' AND host=?))`,
		org, id, host, host).Scan(&total, &running)
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
}

func toTargetView(t Target, load TargetLoad) targetView {
	v := targetView{
		ID: t.ID, Label: t.Label, Kind: t.Kind, Status: t.EffectiveStatus(time.Now()),
		Capacity: t.Capacity, Host: t.Host,
		Sessions: load.Sessions, Running: load.Running,
		CreatedAt: stamp.Unix(t.CreatedAt), UpdatedAt: stamp.Unix(t.UpdatedAt),
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
		v.MetricsAt = stamp.Unix(t.MetricsAt)
	}
	return v
}

// targetOps binds the service to the typed target ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.registerTarget), which
// is also the only bound form cmd/zipdoc can lift prose from.
type targetOps struct{ s *cloud.Service[state] }

// callerOf is caller() for a typed op: the validated principal behind the
// request — the owner of a machine it registers, the actor a session is recorded
// under. Empty off the HTTP path, where there is no request and therefore no
// caller — which fails closed, since an empty owner never satisfies the ownership
// arm of targetOwns.
func callerOf(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return caller(c)
	}
	return ""
}

// targetOwns is ownsTarget() for a typed op. It needs the REQUEST rather than the
// tenant because org-admin-ness lives in a header (X-User-IsOrgAdmin) that
// principal.OrgFrom does not carry. False off the HTTP path: no request, no
// attested caller, no management rights.
func targetOwns(ctx context.Context, t Target) bool {
	if c, ok := cloud.Request(ctx); ok {
		return ownsTarget(c, t)
	}
	return false
}

// targetRef addresses one target. The id is the path segment: the URL is the
// addressing authority, so it binds from there whatever a body says.
type targetRef struct {
	// ID is the target to act on, from the path.
	ID string `json:"id"`
}

// targetList is a page of the caller org's targets.
type targetList struct {
	// Targets is every target registered to the caller's org.
	Targets []targetView `json:"targets"`
}

// targetDeleted acknowledges a deregistration.
type targetDeleted struct {
	// Deleted is true when the target was removed.
	Deleted bool `json:"deleted"`
	// ID is the target that was removed.
	ID string `json:"id"`
}

// patchTargetIn is a partial update. Every field is optional — a nil field is
// left alone — and the id comes from the path.
//
// The mutable fields are spelled out HERE, not embedded from a body struct with
// one user. They used to be embedded, and the shipped consequence was measurable:
// zip's schema walk takes only EXPORTED fields and an embedded field of an
// unexported type is not one, so openapi.yaml published this request body as
// `{id}` alone and no generated client could send a single mutable field.
type patchTargetIn struct {
	// ID is the target to update, from the path.
	ID string `json:"id"`
	// Label renames the machine, up to 128 characters. Empty STRING is refused — a
	// target with no name is a row nobody can pick out of a fleet.
	Label *string `json:"label"`
	// Kind re-files it under laptop | cloud | gpu | cluster | machine.
	Kind *string `json:"kind"`
	// Status sets operator INTENT: online | offline | draining. Draining is how a
	// machine is taken out of dispatch without ending what is already on it. What
	// comes back may still read offline, because the heartbeat outranks the intent.
	Status *string `json:"status"`
	// Capacity rewrites the human summary, up to 256 characters. "" clears it.
	Capacity *string `json:"capacity"`
	// Host re-points the hostname sessions are matched by. Moving it moves the load:
	// the session counts follow the new name from the next read.
	Host *string `json:"host"`
	// Spec replaces the static capability whole, sanitized and clamped the same way
	// a register's is.
	Spec *Spec `json:"spec"`
	// Metrics replaces the live sample, and sending one IS A HEARTBEAT: the server
	// stamps the time and appends the point to the fleet series. Sending an all-zero
	// sample CLEARS the heartbeat — the machine goes back to having no liveness fact
	// at all, and its stored status is taken at face value again.
	Metrics *Metrics `json:"metrics"`
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// mountTargets registers the target routes. Called from Mount BEFORE the
// /v1/agent/:ref wildcard (Fiber matches in registration order) so "targets" is not
// captured as a ref. The static /v1/agent/targets precedes /v1/agent/targets/:id.
func mountTargets(s *cloud.Service[state], app cloud.Router) {
	g := app.Group("/v1/agent")
	// TYPED ops, declared on the group itself: zip.Get and friends take any
	// Router since v1.18.0, so the prefix is part of each op's path and every
	// projection — the document, the MCP tool, the CLI command, the call plane —
	// follows from this one registration.
	// Declared on the GROUP, so each op's path is the group's prefix composed with
	// its leaf — the same composition the router does, and the identity every
	// projection keys on. cmd/zipdoc resolves the prefix the same way as of zip
	// v1.18.3, so the doc comments below reach the document and the MCP tool list.
	o := targetOps{s: s}
	zip.Post(g, "/targets", o.registerTarget)
	zip.Get(g, "/targets", o.listTargets)
	zip.Get(g, "/targets/:id", o.getTarget)
	zip.Patch(g, "/targets/:id", o.patchTarget)
	zip.Delete(g, "/targets/:id", o.deleteTarget)
	// The #48 route-work machine surface (key, claim long-poll, report)
	// lives on the same target routes; register after the CRUD so the
	// extra-segment paths are unambiguous.
	mountRouting(s, app)
}

// ---- register ----

type targetReq struct {
	// Label is the name to show for this machine, up to 128 characters. REQUIRED —
	// it is the only field here a person reads.
	Label string `json:"label"`
	// Kind is laptop | cloud | gpu | cluster | machine. Empty registers a `machine`;
	// anything outside the five is a 400.
	Kind string `json:"kind"`
	// Status is online | offline | draining. Empty registers `online`. It states
	// INTENT — a heartbeat is what decides whether an online machine is actually
	// reachable, so declaring online does not make it so.
	Status string `json:"status"`
	// Capacity is a human summary of the machine's size, up to 256 characters. Prose
	// only; a scheduler reads Spec.
	Capacity string `json:"capacity"`
	// Host is the hostname sessions on this machine will report. It is what makes a
	// re-link IDEMPOTENT: the same (org, host, owner) refreshes the existing row and
	// answers 200, while a request with no host always creates a new target and
	// answers 201. It never adopts a row owned by somebody else.
	Host string `json:"host"`
	// Spec is the machine's static capability — os, arch, cores, RAM, accelerators.
	// Every field is bounded on write and at most 32 accelerators are accepted, so
	// what comes back may be clamped. Omit it for a destination nothing probes.
	Spec Spec `json:"spec"`
	// Metrics is a live sample, and sending one IS A HEARTBEAT: it refreshes the row
	// and starts the 90-second liveness window, and it is appended to the fleet
	// series as one point. Its own `at` is ignored — the server stamps the time, so
	// a client can never age or backdate its own machine. Omit it to register a
	// machine without claiming it is alive.
	Metrics Metrics `json:"metrics"`
}

// RegisterTarget registers a machine as an agent target, or re-links one that is
// already registered. Re-linking is idempotent and keyed on org+host+owner, so a
// machine that reconnects refreshes its own row rather than piling up duplicates;
// it answers 200, while a first registration answers 201.
//
// Example: {"label": "workshop", "kind": "gpu", "host": "gpu-01"}
func (o targetOps) registerTarget(ctx context.Context, in *targetReq) (*targetView, error) {
	sto, org, err := tenantStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	body := *in
	label := strings.TrimSpace(body.Label)
	if label == "" {
		return nil, zip.ErrBadRequest("label is required")
	}
	if len(label) > maxTargetLabel {
		return nil, zip.ErrBadRequest("label too long")
	}
	kind := strings.TrimSpace(body.Kind)
	if kind == "" {
		kind = TargetMachine
	}
	if !validTargetKind(kind) {
		return nil, zip.ErrBadRequest("kind must be laptop|cloud|gpu|cluster|machine")
	}
	status := strings.TrimSpace(body.Status)
	if status == "" {
		status = TargetOnline
	}
	if !validTargetStatus(status) {
		return nil, zip.ErrBadRequest("status must be online|offline|draining")
	}
	capacity := strings.TrimSpace(body.Capacity)
	if len(capacity) > maxTargetCapacity {
		return nil, zip.ErrBadRequest("capacity too long")
	}
	host := strings.TrimSpace(body.Host)
	if len(host) > maxHost {
		return nil, zip.ErrBadRequest("host too long")
	}
	if len(body.Spec.GPUs) > maxGPUs {
		return nil, zip.ErrBadRequest("too many gpus")
	}
	spec := body.Spec.Sanitize()
	metrics := body.Metrics.Sanitize()
	now := time.Now().Unix()
	metricsAt := int64(0)
	if !metrics.IsZero() {
		metricsAt = now // the server owns the staleness clock; a client can't forge it
	}

	// The registering principal OWNS this machine (least privilege): only it (or an
	// org admin) may later mint the claim key, claim runs, report, patch, or delete
	// it. tenant() already required a validated principal, so this is non-empty.
	owner := callerOf(ctx)

	// Idempotent re-link: the SAME machine (org+host+owner) refreshes its existing
	// target rather than piling up duplicates, so mission-control shows one row per
	// machine with live spec/metrics. It resolves ONLY the caller's own row (or an
	// UNOWNED pre-migration row, which it ADOPTS by binding owner) — a row owned by a
	// different member is never touched, so a re-link can never hijack another's
	// machine; the caller falls through to create its own. Only an explicit host keys
	// this — an anonymous target (no host) always creates.
	if host != "" {
		if existing, err := sto.GetLinkableTargetByHost(ctx, org, host, owner); err == nil {
			existing.Owner = owner // bind an adopted unowned row; no-op if already ours
			existing.Label, existing.Kind, existing.Status, existing.Capacity = label, kind, status, capacity
			existing.Spec, existing.Metrics, existing.MetricsAt = spec, metrics, metricsAt
			existing.UpdatedAt = now
			if err := sto.UpdateTarget(ctx, existing); err != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
			}
			recordSample(o.s, existing) // a re-link carrying metrics IS a heartbeat
			load, _ := sto.SessionLoad(ctx, org, existing.ID, existing.Host)
			v := toTargetView(existing, load)
			return &v, nil
		}
	}

	id := mint.ID("tgt")
	t := Target{
		ID: id, Org: org, Owner: owner, Label: label, Kind: kind, Status: status,
		Capacity: capacity, Host: host, Spec: spec, Metrics: metrics, MetricsAt: metricsAt,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := sto.CreateTarget(ctx, t); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	recordSample(o.s, t) // a registration carrying metrics is the target's first sample
	// 201 only on the CREATE branch — the re-link above answers 200, which is
	// the correct REST distinction and the reason this op cannot declare a single
	// zip.WithStatus. See LLM.md: the conditional-status class waits for
	// multi-status responses in zip.
	cloud.Created(ctx)
	v := toTargetView(t, TargetLoad{})
	return &v, nil
}

// ---- list ----

// ListTargets returns every machine registered to the caller's org, newest
// first, each with its live session load.
func (o targetOps) listTargets(ctx context.Context, _ *cloud.Unit) (*targetList, error) {
	sto, org, err := tenantStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	rows, err := sto.ListTargets(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]targetView, 0, len(rows))
	for _, t := range rows {
		load, _ := sto.SessionLoad(ctx, org, t.ID, t.Host)
		out = append(out, toTargetView(t, load))
	}
	return &targetList{Targets: out}, nil
}

// ---- detail ----

// GetTarget returns one registered machine, with its live session load.
//
// Example: {"id": "tgt_1"}
func (o targetOps) getTarget(ctx context.Context, in *targetRef) (*targetView, error) {
	sto, org, err := tenantStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	if len(id) > maxTargetID {
		return nil, zip.ErrNotFound("target not found")
	}
	t, err := sto.GetTarget(ctx, org, id)
	if err == errTargetNotFound {
		return nil, zip.ErrNotFound("target not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	load, _ := sto.SessionLoad(ctx, org, t.ID, t.Host)
	v := toTargetView(t, load)
	return &v, nil
}

// ---- patch ----

// PatchTarget updates one machine in place. Every field is optional; a field the
// request omits is left alone. A metrics patch IS a heartbeat — the server stamps
// its own clock, so a client can neither forge nor backdate staleness.
//
// Example: {"id": "tgt_1", "status": "draining"}
func (o targetOps) patchTarget(ctx context.Context, in *patchTargetIn) (*targetView, error) {
	sto, org, err := tenantStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	t, err := sto.GetTarget(ctx, org, id)
	if err == errTargetNotFound {
		return nil, zip.ErrNotFound("target not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	// Only the machine's owner (or an org admin) may mutate it — a member cannot
	// reconfigure/drain another member's machine. Fail-closed to the SAME not-found
	// an unknown id gives, so a probe learns nothing about what exists.
	if !targetOwns(ctx, t) {
		return nil, zip.ErrNotFound("target not found")
	}
	body := *in
	if body.Label != nil {
		nl := strings.TrimSpace(*body.Label)
		if nl == "" {
			return nil, zip.ErrBadRequest("label cannot be empty")
		}
		if len(nl) > maxTargetLabel {
			return nil, zip.ErrBadRequest("label too long")
		}
		t.Label = nl
	}
	if body.Kind != nil {
		nk := strings.TrimSpace(*body.Kind)
		if !validTargetKind(nk) {
			return nil, zip.ErrBadRequest("kind must be laptop|cloud|gpu|cluster|machine")
		}
		t.Kind = nk
	}
	if body.Status != nil {
		ns := strings.TrimSpace(*body.Status)
		if !validTargetStatus(ns) {
			return nil, zip.ErrBadRequest("status must be online|offline|draining")
		}
		t.Status = ns
	}
	if body.Capacity != nil {
		nc := strings.TrimSpace(*body.Capacity)
		if len(nc) > maxTargetCapacity {
			return nil, zip.ErrBadRequest("capacity too long")
		}
		t.Capacity = nc
	}
	if body.Host != nil {
		nh := strings.TrimSpace(*body.Host)
		if len(nh) > maxHost {
			return nil, zip.ErrBadRequest("host too long")
		}
		t.Host = nh
	}
	now := time.Now().Unix()
	if body.Spec != nil {
		if len(body.Spec.GPUs) > maxGPUs {
			return nil, zip.ErrBadRequest("too many gpus")
		}
		t.Spec = body.Spec.Sanitize()
	}
	if body.Metrics != nil {
		// A metrics patch IS a heartbeat: refresh the sample and stamp the server's
		// own clock (a client can never forge or backdate the staleness time).
		t.Metrics = body.Metrics.Sanitize()
		if t.Metrics.IsZero() {
			t.MetricsAt = 0
		} else {
			t.MetricsAt = now
		}
	}
	t.UpdatedAt = now
	if err := sto.UpdateTarget(ctx, t); err != nil {
		if err == errTargetNotFound {
			return nil, zip.ErrNotFound("target not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	if body.Metrics != nil {
		recordSample(o.s, t) // THE heartbeat: append it to the fleet series too
	}
	load, _ := sto.SessionLoad(ctx, org, t.ID, t.Host)
	v := toTargetView(t, load)
	return &v, nil
}

// ---- delete ----

// DeleteTarget deregisters one machine. Only its owner, or an org admin, may
// remove it; an unknown id, a cross-org id and a machine owned by someone else
// all answer the same not-found, so a probe learns nothing about what exists.
//
// Example: {"id": "tgt_1"}
func (o targetOps) deleteTarget(ctx context.Context, in *targetRef) (*targetDeleted, error) {
	sto, org, err := tenantStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	// Resolve + ownership-gate before deleting: only the machine's owner (or an org
	// admin) may deregister it. A cross-org id, an unknown id, and a non-owned id all
	// collapse to the same not-found — no oracle.
	t, err := sto.GetTarget(ctx, org, id)
	if err == errTargetNotFound {
		return nil, zip.ErrNotFound("target not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if !targetOwns(ctx, t) {
		return nil, zip.ErrNotFound("target not found")
	}
	deleted, err := sto.DeleteTarget(ctx, org, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("target not found")
	}
	return &targetDeleted{Deleted: true, ID: id}, nil
}
