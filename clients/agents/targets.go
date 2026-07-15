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
	"github.com/zap-proto/zip"
)

// A target is a place an agent session can be dispatched to run: a laptop, a cloud
// box, a GPU host, or a whole cluster. It is the #48 link-a-compute seam over the
// SAME agents.db (one store, one tenancy column) as sessions/events — NOT a rival
// device registry. It composes with the compute fleet rather than duplicating it: a
// session records the target id it runs on (agent_sessions.target), and the mission-
// control devices view unions these registered targets with the org's BYO workers
// (GET /v1/fleet/workers) and BYO clusters (GET /v1/clusters) at the view layer — the
// console's established pattern for folding compute sources.
//
//	POST   /v1/agents/targets        register a target -> Target
//	GET    /v1/agents/targets        list the org's targets (+ live session load)
//	GET    /v1/agents/targets/:id    one target + its running/total session counts
//	PATCH  /v1/agents/targets/:id    update label/kind/status/capacity/host
//	DELETE /v1/agents/targets/:id    deregister
//
// Every route is org-scoped through principal.Org (tenant), fail-closed — a tenant
// can never see or mutate another org's targets, exactly like sessions.

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

const (
	maxTargetLabel    = 128
	maxTargetCapacity = 256
	maxTargetID       = 128
)

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

// Target is a registered agent run-target (metadata only). Owned by one org.
type Target struct {
	ID        string
	Org       string
	Label     string
	Kind      string // laptop | cloud | gpu | cluster | machine
	Status    string // online | offline | draining
	Capacity  string // free-form ("8 vCPU / 32G", "1× GB10")
	Host      string // hostname sessions on this machine report (maps sessions -> target)
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
  label      TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL DEFAULT 'machine',
  status     TEXT NOT NULL DEFAULT 'online',
  capacity   TEXT NOT NULL DEFAULT '',
  host       TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_targets_org_created ON agent_targets(org, created_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate targets: %w", err)
	}
	return nil
}

const targetCols = `id,org,label,kind,status,capacity,host,created_at,updated_at`

func scanTarget(sc interface{ Scan(...any) error }) (Target, error) {
	var t Target
	err := sc.Scan(&t.ID, &t.Org, &t.Label, &t.Kind, &t.Status, &t.Capacity, &t.Host,
		&t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// CreateTarget inserts one target. The id is caller-generated (genID("tgt")).
func (s *Store) CreateTarget(ctx context.Context, t Target) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_targets (`+targetCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Org, t.Label, t.Kind, t.Status, t.Capacity, t.Host, t.CreatedAt, t.UpdatedAt)
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
// so a cross-tenant id can never mutate another's target.
func (s *Store) UpdateTarget(ctx context.Context, t Target) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_targets SET label=?, kind=?, status=?, capacity=?, host=?, updated_at=?
		 WHERE org=? AND id=?`,
		t.Label, t.Kind, t.Status, t.Capacity, t.Host, t.UpdatedAt, t.Org, t.ID)
	if err != nil {
		return fmt.Errorf("update target: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errTargetNotFound
	}
	return nil
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

// ---- HTTP shapes (the published contract) ----

type targetView struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Capacity  string `json:"capacity,omitempty"`
	Host      string `json:"host,omitempty"`
	Sessions  int    `json:"sessions"`
	Running   int    `json:"running"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func toTargetView(t Target, load TargetLoad) targetView {
	return targetView{
		ID: t.ID, Label: t.Label, Kind: t.Kind, Status: t.Status,
		Capacity: t.Capacity, Host: t.Host,
		Sessions: load.Sessions, Running: load.Running,
		CreatedAt: rfc3339(t.CreatedAt), UpdatedAt: rfc3339(t.UpdatedAt),
	}
}

// mountTargets registers the target routes. Called from Mount BEFORE the
// /v1/agents/:ref wildcard (Fiber matches in registration order) so "targets" is not
// captured as a ref. The static /v1/agents/targets precedes /v1/agents/targets/:id.
func mountTargets(s *cloud.Service[state], app *zip.App) {
	app.Post("/v1/agents/targets", cloud.Handle(s, registerTarget))
	app.Get("/v1/agents/targets", cloud.Handle(s, listTargets))
	app.Get("/v1/agents/targets/:id", cloud.Handle(s, getTarget))
	app.Patch("/v1/agents/targets/:id", cloud.Handle(s, patchTarget))
	app.Delete("/v1/agents/targets/:id", cloud.Handle(s, deleteTarget))
}

// ---- register ----

type targetReq struct {
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Capacity string `json:"capacity"`
	Host     string `json:"host"`
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
	id, err := genID("tgt")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	t := Target{
		ID: id, Org: org, Label: label, Kind: kind, Status: status,
		Capacity: capacity, Host: host, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.CreateTarget(c.Context(), t); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
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
	Label    *string `json:"label"`
	Kind     *string `json:"kind"`
	Status   *string `json:"status"`
	Capacity *string `json:"capacity"`
	Host     *string `json:"host"`
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
	t.UpdatedAt = time.Now().Unix()
	if err := s.State.store.UpdateTarget(c.Context(), t); err != nil {
		if err == errTargetNotFound {
			return zip.ErrNotFound("target not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "update: %v", err)
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
	deleted, err := s.State.store.DeleteTarget(c.Context(), org, id)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("target not found")
	}
	return c.JSON(http.StatusOK, map[string]any{"deleted": true, "id": id})
}
