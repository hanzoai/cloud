package agents

// targets_http.go — the run-target routes: register, list, read, beat and delete
// a machine, and who may act on one (callerOf, targetOwns). The row, its store
// and its view are targets.go's.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// targetOps binds the service to the typed target ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.registerTarget), which
// is also the only bound form cmd/zipdoc can lift prose from.
type targetOps struct{ s *cloud.Service[state] }

// callerOf is caller() for a typed op: the validated principal behind the
// request — the owner of a machine it registers in a customer's org, the actor a
// session is recorded under. Empty off the HTTP path, where there is no request
// and therefore no caller — which fails closed, since an empty owner never
// satisfies the ownership arm of targetOwns.
func callerOf(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return caller(c)
	}
	return ""
}

// targetOwns reports whether the caller may MANAGE t — mint or rotate its claim
// key, claim, report, patch, delete, adopt it while unowned. A machine belongs to
// the person who registered it (least privilege, AC-6): its owner may manage it,
// a SuperAdmin may, and in a customer's org an org admin (self-service org
// management, the admin-org model's isAdmin) may manage any of the org's
// machines. In a personal org the org-admin arm is gone. An UNOWNED
// (pre-migration) row is an admin's alone. Fail-closed: no request, an empty
// caller or an empty owner manages nothing.
//
// It needs the REQUEST rather than the tenant because org-admin-ness lives in a
// header (X-User-IsOrgAdmin) that principal.OrgFrom does not carry.
func targetOwns(ctx context.Context, t Target) bool {
	c, ok := cloud.Request(ctx)
	if !ok {
		return false
	}
	if principal.Super(c) || principal.IsOrgAdmin(c) && !personal(t.Org) {
		return true
	}
	me, err := self(ctx, t.Org)
	return err == nil && owner(t, me)
}

// targetRef addresses one target. The id is the path segment: the URL is the
// addressing authority, so it binds from there whatever a body says.
type targetRef struct {
	// ID is the target to act on, from the path.
	ID string `json:"id"`
}

// targetList is a page of the caller org's targets.
type targetList struct {
	// Targets is every target registered to the caller's org; in a held org, the
	// caller's own.
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
	// Terminal publishes, or with "" clears, the machine's live terminal URL (https).
	// Omit it and the stored terminal is left alone, so a heartbeat from a process
	// that publishes none never wipes another's. The server stamps the time.
	Terminal *string `json:"terminal"`
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
	// TYPED ops, declared on the group itself: zip.Get and friends take any
	// Router since v1.18.0, so the prefix is part of each op's path and every
	// projection — the document, the MCP tool, the CLI command, the call plane —
	// follows from this one registration.
	// Declared on the GROUP, so each op's path is the group's prefix composed with
	// its leaf — the same composition the router does, and the identity every
	// projection keys on. cmd/zipdoc resolves the prefix the same way as of zip
	// v1.18.3, so the doc comments below reach the document and the MCP tool list.
	o := targetOps{s: s}
	g := app.Group("/v1/agent")
	g.Post("/targets", o.registerTarget)
	g.Get("/targets", o.listTargets)
	g.Get("/targets/:id", o.getTarget)
	g.Patch("/targets/:id", o.patchTarget)
	g.Delete("/targets/:id", o.deleteTarget)
	g.Post("/targets/:id/folders", o.grantFolder)
	g.Get("/targets/:id/folders", o.listFolders)
	g.Delete("/targets/:id/folders", o.revokeFolder)
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
	// Terminal is the https URL this machine's live terminal is published at.
	// Optional: omit it to leave the stored one alone, send "" to clear it (the
	// link is ending). The server stamps the time, and the terminal shows on the
	// machine only while the machine beats and restates it within the 90-second
	// window.
	Terminal *string `json:"terminal"`
}

// RegisterTarget registers a machine as an agent target, or re-links one that is
// already registered. Re-linking is idempotent and keyed on org+host+owner, so a
// machine that reconnects refreshes its own row rather than piling up duplicates;
// it answers 200, while a first registration answers 201.
//
// Example: {"label": "workshop", "kind": "gpu", "host": "gpu-01"}
func (o targetOps) registerTarget(ctx context.Context, in *targetReq) (*targetView, error) {
	sto, org, err := targetStore(ctx, &o.s.State)
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
	var published Target
	if err := published.publish(body.Terminal, now); err != nil {
		return nil, err
	}

	// The registering person OWNS this machine (least privilege): only they (or an
	// admin, see targetOwns) may later mint the claim key, claim runs, report,
	// patch, or delete it. tenant() already required a validated principal; in a
	// personal org it must also name a person (self), or the machine would be
	// nobody's.
	me, err := self(ctx, org)
	if err != nil {
		return nil, err
	}
	owner := me.ID
	if owner == "" {
		return nil, zip.ErrForbidden("a machine in " + org + " is its owner's own, and this call names no person to own it")
	}

	// Idempotent re-link: the SAME machine (org+host+owner) refreshes its existing
	// target rather than piling up duplicates, so mission-control shows one row per
	// machine with live spec/metrics. It resolves ONLY the caller's own row — under
	// their id, or under their row as an API key of theirs linked it before (self) —
	// or an UNOWNED pre-migration row the caller already manages (targetOwns: an
	// admin), which it ADOPTS by binding owner. A row owned by a different member
	// is never touched, and an unowned one is never taken by a member who merely
	// names its host — other members route their runs to it — so the caller falls
	// through to create its own. Only an explicit host keys this — an anonymous
	// target (no host) always creates.
	if host != "" {
		if existing, err := sto.GetLinkableTargetByHost(ctx, org, host, me); err == nil &&
			(me.Is(existing.Owner) || targetOwns(ctx, existing)) {
			existing.Owner = owner // bind an adopted unowned row, or carry one keyed on the row; no-op if already ours
			existing.Label, existing.Kind, existing.Status, existing.Capacity = label, kind, status, capacity
			existing.Spec, existing.Metrics, existing.MetricsAt = spec, metrics, metricsAt
			if body.Terminal != nil {
				existing.Terminal, existing.TerminalAt = published.Terminal, published.TerminalAt
			}
			existing.UpdatedAt = now
			if err := sto.UpdateTarget(ctx, existing); err != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
			}
			recordSample(o.s, existing) // a re-link carrying metrics IS a heartbeat
			v := sto.view(ctx, org, existing)
			return &v, nil
		}
	}

	id := mint.ID("tgt")
	t := Target{
		ID: id, Org: org, Owner: owner, Label: label, Kind: kind, Status: status,
		Capacity: capacity, Host: host, Spec: spec, Metrics: metrics, MetricsAt: metricsAt,
		Terminal: published.Terminal, TerminalAt: published.TerminalAt,
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
	v.Terminal = t.LiveTerminal(time.Now()) // the caller just registered it, so it is theirs
	return &v, nil
}

// ---- list ----

// ListTargets returns every machine registered to the caller's org, newest
// first, each with its live session load. In a held org, where self-serve
// signups land beside the platform's staff, it is only the caller's own.
func (o targetOps) listTargets(ctx context.Context, _ *cloud.Unit) (*targetList, error) {
	sto, org, err := targetStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	rows, err := sto.ListTargets(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]targetView, 0, len(rows))
	for _, t := range rows {
		if personal(org) && !targetOwns(ctx, t) {
			continue // another member's machine, in an org of strangers
		}
		out = append(out, sto.view(ctx, org, t))
	}
	return &targetList{Targets: out}, nil
}

// ---- detail ----

// GetTarget returns one registered machine, with its live session load.
//
// Example: {"id": "tgt_1"}
func (o targetOps) getTarget(ctx context.Context, in *targetRef) (*targetView, error) {
	sto, org, err := targetStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	if len(id) > maxTargetID {
		return nil, zip.ErrNotFound("target not found")
	}
	t, err := sto.GetTarget(ctx, org, id)
	if err == errTargetNotFound || err == nil && personal(org) && !targetOwns(ctx, t) {
		return nil, zip.ErrNotFound("target not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	v := sto.view(ctx, org, t)
	return &v, nil
}

// ---- patch ----

// PatchTarget updates one machine in place. Every field is optional; a field the
// request omits is left alone. A metrics patch IS a heartbeat — the server stamps
// its own clock, so a client can neither forge nor backdate staleness.
//
// Example: {"id": "tgt_1", "status": "draining"}
func (o targetOps) patchTarget(ctx context.Context, in *patchTargetIn) (*targetView, error) {
	sto, org, err := targetStore(ctx, &o.s.State)
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
	t = sto.carry(ctx, t)
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
	if body.Terminal != nil {
		if err := t.publish(body.Terminal, now); err != nil {
			return nil, err
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
	v := sto.view(ctx, org, t)
	return &v, nil
}

// ---- delete ----

// DeleteTarget deregisters one machine. Only its owner, or an org admin, may
// remove it; an unknown id, a cross-org id and a machine owned by someone else
// all answer the same not-found, so a probe learns nothing about what exists.
//
// Example: {"id": "tgt_1"}
func (o targetOps) deleteTarget(ctx context.Context, in *targetRef) (*targetDeleted, error) {
	sto, org, err := targetStore(ctx, &o.s.State)
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
