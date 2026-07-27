package agents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/samples"
	"github.com/zap-proto/zip"
)

// A target is a place an agent session can be dispatched to run: a laptop, a cloud
// box, a GPU host, or a whole cluster. It is the #48 link-a-compute seam over the
// SAME agents.db (one store, one tenancy column) as sessions/events — NOT a rival
// device registry. It composes with the compute fleet rather than duplicating it: a
// session records the target id it runs on (agent_sessions.target), and the org's
// unified board (GET /v1/fleet, clients/visor/board.go) unions these registered
// targets with its BYO workers (GET /v1/fleet/workers), BYO clusters and Visor
// machines — reading this registry through the in-process seam below rather than
// copying it.
//
//	POST   /v1/agents/targets        register a target -> Target
//	GET    /v1/agents/targets        list the org's targets (+ live session load)
//	GET    /v1/agents/targets/:id    one target + its running/total session counts
//	PATCH  /v1/agents/targets/:id    update label/kind/status/capacity/host
//	DELETE /v1/agents/targets/:id    deregister
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

// CreateTarget inserts one target. The id is caller-generated (genID("tgt")).
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

// ---- the in-process seam (org-scoped, fail-closed) ----
//
// TargetsForOrg / LoadOn are the exported twins of the list + detail reads above:
// the ONE way another in-process subsystem (the /v1/fleet board in clients/visor)
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
	if mounted == nil || mounted.State.store == nil {
		return nil, fmt.Errorf("agents: not mounted")
	}
	org = strings.TrimSpace(org)
	if org == "" || len(org) > principal.MaxOrgLen {
		return nil, fmt.Errorf("agents: invalid org")
	}
	return mounted.State.store.ListTargets(ctx, org)
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
	if mounted == nil || mounted.State.store == nil {
		return Target{}, fmt.Errorf("agents: not mounted")
	}
	org = strings.TrimSpace(org)
	ref = strings.TrimSpace(ref)
	if org == "" || len(org) > principal.MaxOrgLen {
		return Target{}, fmt.Errorf("agents: invalid org")
	}
	if ref == "" || len(ref) > maxTargetID {
		return Target{}, errTargetNotFound
	}
	// An id is exact and unambiguous — try it first.
	if t, err := mounted.State.store.GetTarget(ctx, org, ref); err == nil {
		return t, nil
	} else if err != errTargetNotFound {
		return Target{}, err
	}
	// Else an exact, case-folded label match within this org.
	rows, err := mounted.State.store.ListTargets(ctx, org)
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
// (target id OR host) mapping the HTTP views use, so the board and /v1/agents/
// targets can never disagree about what is running where.
func LoadOn(ctx context.Context, org, id, host string) (TargetLoad, error) {
	if mounted == nil || mounted.State.store == nil {
		return TargetLoad{}, fmt.Errorf("agents: not mounted")
	}
	org = strings.TrimSpace(org)
	if org == "" || len(org) > principal.MaxOrgLen {
		return TargetLoad{}, fmt.Errorf("agents: invalid org")
	}
	return mounted.State.store.SessionLoad(ctx, org, id, host)
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
//   - it never touches the response, so the /v1/agents/targets contract is
//     byte-identical whether the warehouse is present, absent or on fire;
//   - a failure is logged, never surfaced — a dropped sample must not cost a
//     machine its heartbeat.
//
// This is the shape the billing warehouse write already uses (`go zapWriteUsage`):
// the seam is synchronous, the CALLER owns the concurrency.
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
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Capacity  string   `json:"capacity,omitempty"`
	Host      string   `json:"host,omitempty"`
	Spec      *Spec    `json:"spec,omitempty"`
	Metrics   *Metrics `json:"metrics,omitempty"`
	MetricsAt string   `json:"metricsAt,omitempty"`
	Sessions  int      `json:"sessions"`
	Running   int      `json:"running"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

func toTargetView(t Target, load TargetLoad) targetView {
	v := targetView{
		ID: t.ID, Label: t.Label, Kind: t.Kind, Status: t.EffectiveStatus(time.Now()),
		Capacity: t.Capacity, Host: t.Host,
		Sessions: load.Sessions, Running: load.Running,
		CreatedAt: rfc3339(t.CreatedAt), UpdatedAt: rfc3339(t.UpdatedAt),
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
		v.MetricsAt = rfc3339(t.MetricsAt)
	}
	return v
}

// mountTargets registers the target routes. Called from Mount BEFORE the
// /v1/agents/:ref wildcard (Fiber matches in registration order) so "targets" is not
// captured as a ref. The static /v1/agents/targets precedes /v1/agents/targets/:id.
func mountTargets(s *cloud.Service[state], app cloud.Router) {
	g := app.Group("/v1/agents")
	g.Post("/targets", cloud.Handle(s, registerTarget))
	g.Get("/targets", cloud.Handle(s, listTargets))
	g.Get("/targets/:id", cloud.Handle(s, getTarget))
	g.Patch("/targets/:id", cloud.Handle(s, patchTarget))
	g.Delete("/targets/:id", cloud.Handle(s, deleteTarget))
	// The #48 route-work machine surface (claim-key, claim long-poll, report)
	// lives on the same target routes; register after the CRUD so the
	// extra-segment paths are unambiguous.
	mountRouting(s, app)
}

// ---- register ----

type targetReq struct {
	Label    string  `json:"label"`
	Kind     string  `json:"kind"`
	Status   string  `json:"status"`
	Capacity string  `json:"capacity"`
	Host     string  `json:"host"`
	Spec     Spec    `json:"spec"`
	Metrics  Metrics `json:"metrics"`
}

func registerTarget(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body targetReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	label := strings.TrimSpace(body.Label)
	if label == "" {
		return zip.ErrBadRequest("label is required")
	}
	if len(label) > maxTargetLabel {
		return zip.ErrBadRequest("label too long")
	}
	kind := strings.TrimSpace(body.Kind)
	if kind == "" {
		kind = TargetMachine
	}
	if !validTargetKind(kind) {
		return zip.ErrBadRequest("kind must be laptop|cloud|gpu|cluster|machine")
	}
	status := strings.TrimSpace(body.Status)
	if status == "" {
		status = TargetOnline
	}
	if !validTargetStatus(status) {
		return zip.ErrBadRequest("status must be online|offline|draining")
	}
	capacity := strings.TrimSpace(body.Capacity)
	if len(capacity) > maxTargetCapacity {
		return zip.ErrBadRequest("capacity too long")
	}
	host := strings.TrimSpace(body.Host)
	if len(host) > maxHost {
		return zip.ErrBadRequest("host too long")
	}
	if len(body.Spec.GPUs) > maxGPUs {
		return zip.ErrBadRequest("too many gpus")
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
	owner := caller(c)

	// Idempotent re-link: the SAME machine (org+host+owner) refreshes its existing
	// target rather than piling up duplicates, so mission-control shows one row per
	// machine with live spec/metrics. It resolves ONLY the caller's own row (or an
	// UNOWNED pre-migration row, which it ADOPTS by binding owner) — a row owned by a
	// different member is never touched, so a re-link can never hijack another's
	// machine; the caller falls through to create its own. Only an explicit host keys
	// this — an anonymous target (no host) always creates.
	if host != "" {
		if existing, err := s.State.store.GetLinkableTargetByHost(c.Context(), org, host, owner); err == nil {
			existing.Owner = owner // bind an adopted unowned row; no-op if already ours
			existing.Label, existing.Kind, existing.Status, existing.Capacity = label, kind, status, capacity
			existing.Spec, existing.Metrics, existing.MetricsAt = spec, metrics, metricsAt
			existing.UpdatedAt = now
			if err := s.State.store.UpdateTarget(c.Context(), existing); err != nil {
				return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
			}
			recordSample(s, existing) // a re-link carrying metrics IS a heartbeat
			load, _ := s.State.store.SessionLoad(c.Context(), org, existing.ID, existing.Host)
			return c.JSON(http.StatusOK, toTargetView(existing, load))
		}
	}

	id, err := genID("tgt")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	t := Target{
		ID: id, Org: org, Owner: owner, Label: label, Kind: kind, Status: status,
		Capacity: capacity, Host: host, Spec: spec, Metrics: metrics, MetricsAt: metricsAt,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.CreateTarget(c.Context(), t); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	recordSample(s, t) // a registration carrying metrics is the target's first sample
	return c.JSON(http.StatusCreated, toTargetView(t, TargetLoad{}))
}

// ---- list ----

func listTargets(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.State.store.ListTargets(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]targetView, 0, len(rows))
	for _, t := range rows {
		load, _ := s.State.store.SessionLoad(c.Context(), org, t.ID, t.Host)
		out = append(out, toTargetView(t, load))
	}
	return c.JSON(http.StatusOK, map[string]any{"targets": out})
}

// ---- detail ----

func getTarget(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := idParam(c)
	if len(id) > maxTargetID {
		return zip.ErrNotFound("target not found")
	}
	t, err := s.State.store.GetTarget(c.Context(), org, id)
	if err == errTargetNotFound {
		return zip.ErrNotFound("target not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	load, _ := s.State.store.SessionLoad(c.Context(), org, t.ID, t.Host)
	return c.JSON(http.StatusOK, toTargetView(t, load))
}

// ---- patch ----

type patchTargetReq struct {
	Label    *string  `json:"label"`
	Kind     *string  `json:"kind"`
	Status   *string  `json:"status"`
	Capacity *string  `json:"capacity"`
	Host     *string  `json:"host"`
	Spec     *Spec    `json:"spec"`
	Metrics  *Metrics `json:"metrics"` // present => a heartbeat; the server stamps its time
}

func patchTarget(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := idParam(c)
	t, err := s.State.store.GetTarget(c.Context(), org, id)
	if err == errTargetNotFound {
		return zip.ErrNotFound("target not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	// Only the machine's owner (or an org admin) may mutate it — a member cannot
	// reconfigure/drain another member's machine. Fail-closed to the SAME not-found
	// an unknown id gives, so a probe learns nothing about what exists.
	if !ownsTarget(c, t) {
		return zip.ErrNotFound("target not found")
	}
	var body patchTargetReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.Label != nil {
		nl := strings.TrimSpace(*body.Label)
		if nl == "" {
			return zip.ErrBadRequest("label cannot be empty")
		}
		if len(nl) > maxTargetLabel {
			return zip.ErrBadRequest("label too long")
		}
		t.Label = nl
	}
	if body.Kind != nil {
		nk := strings.TrimSpace(*body.Kind)
		if !validTargetKind(nk) {
			return zip.ErrBadRequest("kind must be laptop|cloud|gpu|cluster|machine")
		}
		t.Kind = nk
	}
	if body.Status != nil {
		ns := strings.TrimSpace(*body.Status)
		if !validTargetStatus(ns) {
			return zip.ErrBadRequest("status must be online|offline|draining")
		}
		t.Status = ns
	}
	if body.Capacity != nil {
		nc := strings.TrimSpace(*body.Capacity)
		if len(nc) > maxTargetCapacity {
			return zip.ErrBadRequest("capacity too long")
		}
		t.Capacity = nc
	}
	if body.Host != nil {
		nh := strings.TrimSpace(*body.Host)
		if len(nh) > maxHost {
			return zip.ErrBadRequest("host too long")
		}
		t.Host = nh
	}
	now := time.Now().Unix()
	if body.Spec != nil {
		if len(body.Spec.GPUs) > maxGPUs {
			return zip.ErrBadRequest("too many gpus")
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
	if err := s.State.store.UpdateTarget(c.Context(), t); err != nil {
		if err == errTargetNotFound {
			return zip.ErrNotFound("target not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	if body.Metrics != nil {
		recordSample(s, t) // THE heartbeat: append it to the fleet series too
	}
	load, _ := s.State.store.SessionLoad(c.Context(), org, t.ID, t.Host)
	return c.JSON(http.StatusOK, toTargetView(t, load))
}

// ---- delete ----

func deleteTarget(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := idParam(c)
	// Resolve + ownership-gate before deleting: only the machine's owner (or an org
	// admin) may deregister it. A cross-org id, an unknown id, and a non-owned id all
	// collapse to the same not-found — no oracle.
	t, err := s.State.store.GetTarget(c.Context(), org, id)
	if err == errTargetNotFound {
		return zip.ErrNotFound("target not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if !ownsTarget(c, t) {
		return zip.ErrNotFound("target not found")
	}
	deleted, err := s.State.store.DeleteTarget(c.Context(), org, id)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("target not found")
	}
	return c.JSON(http.StatusOK, map[string]any{"deleted": true, "id": id})
}
