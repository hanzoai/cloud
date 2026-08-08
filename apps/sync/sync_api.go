package sync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// sync_api.go is the /v1/sync control plane: the console's CRUD over the sync
// intent, provider-agnostic in shape. A create is an UPSERT (re-syncing the same
// source→target updates it) plus an optional immediate reconcile; a patch adjusts
// direction/trigger/actor; a delete tears down the derived outbound mirror so an
// unsynced repo never keeps pushing.

var orgRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

// gitHostForProvider is the clone host a git source provider must live on — the
// same {github.com, gitlab.com} the git plane's outbound-target allowlist admits,
// so a sync can never point at an untrusted host.
var gitHostForProvider = map[string]string{
	provGitHub: "github.com",
	provGitLab: "gitlab.com",
}

// endpointReq is one end of a sync: which platform, and which resource on it.
type endpointReq struct {
	// Connector names the stored credential to reach this endpoint with.
	Connector string `json:"connector,omitempty"`
	// Provider is the platform: github or gitlab for a source; a target defaults to
	// the native Hanzo Git plane.
	Provider string `json:"provider"`
	// Locator addresses the resource. For a git source it is the https clone URL on
	// the provider's own host, with no embedded credentials; for a native target it
	// is the repository name.
	Locator string `json:"locator"`
}

// syncReq declares a sync between two endpoints.
type syncReq struct {
	// Kind is what is being synced. Only "git" today, which is also the default.
	Kind string `json:"kind"`
	// Source is the upstream end. Required.
	Source endpointReq `json:"source"`
	// Target is the downstream end. Optional for git: a native repository named
	// after the source is derived when it is omitted.
	Target endpointReq `json:"target"`
	// Direction is both (the default), pull, push or off.
	Direction string `json:"direction"`
	// Trigger is what starts a reconcile: webhook (the default), poll or manual.
	Trigger string `json:"trigger"`
	// Actor is the identity the sync writes as, used as the loop guard so its own
	// writes do not re-trigger it. Defaults to the deployment's GIT_SYNC_ACTOR.
	Actor string `json:"actor"`
	// Run reconciles once immediately after the upsert, in the background.
	Run bool `json:"run"`
}

// syncOps binds the service to sync's typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, which is also the only bound
// form cmd/zipdoc can lift prose from.
type syncOps struct{ s *cloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's principal: it takes
// nothing off the wire. ONE of these for the whole package.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. It is an ALIAS
// for the unnamed empty struct, not a definition: zip keys the response on 204 only
// when the Out type has no name, so a defined type here would publish "200 with a
// body" about a route that answers 204 with none.
type noContent = struct{}

// syncRef addresses one sync. The id is the path segment: the URL is the addressing
// authority, so it binds from there whatever a body says.
type syncRef struct {
	// ID is the sync to act on, from the path.
	ID string `json:"id"`
}

// syncList is every sync link the caller's org has.
type syncList struct {
	// Data is the org's syncs, each with its endpoints, policy and last-synced time.
	Data []syncView `json:"data"`
}

// syncQueued acknowledges a reconcile handed to the background worker.
type syncQueued struct {
	// Queued is true when the reconcile was accepted; it has not run yet.
	Queued bool `json:"queued"`
	// ID is the sync the reconcile was queued for.
	ID string `json:"id"`
}

// orgOf is the validated org for a typed op — the one the gateway asserted and
// cloud.Bridge parked on the context, never a field of In. An In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the caller
// asserted for itself. A malformed org is refused as hard as a missing one: the org
// is a storage key here, so only the exact slug shape is admitted.
func orgOf(ctx context.Context) (string, error) {
	o, ok := principal.OrgFrom(ctx)
	if !ok || !orgRE.MatchString(o) {
		return "", zip.ErrUnauthorized("a validated principal is required")
	}
	return o, nil
}

type endpointView struct {
	Connector string `json:"connector,omitempty"`
	Provider  string `json:"provider"`
	Locator   string `json:"locator"`
}

type syncView struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind"`
	Source    endpointView `json:"source"`
	Target    endpointView `json:"target"`
	Direction string       `json:"direction"`
	Trigger   string       `json:"trigger"`
	Actor     string       `json:"actor,omitempty"`
	CreatedAt string       `json:"createdAt"`
	UpdatedAt string       `json:"updatedAt,omitempty"` // bumped on every reconcile — the last-synced time
}

func syncToView(v Sync) syncView {
	return syncView{
		ID: v.ID, Kind: v.Kind,
		Source:    endpointView{Connector: v.Source.Connector, Provider: v.Source.Provider, Locator: v.Source.Locator},
		Target:    endpointView{Connector: v.Target.Connector, Provider: v.Target.Provider, Locator: v.Target.Locator},
		Direction: v.Direction, Trigger: v.Trigger, Actor: v.Actor,
		CreatedAt: rfc3339(v.CreatedAt), UpdatedAt: rfc3339(v.UpdatedAt),
	}
}

// Create declares a sync between two endpoints and returns it. It is an UPSERT:
// re-declaring the same source and target updates that link rather than piling up
// duplicates, so a console that re-submits is safe. The org comes from the validated
// principal, never from the request, so a sync can only ever bind endpoints inside
// the caller's own org. A git source must be an https clone URL on the provider's own
// host with no embedded credentials; a target left empty is derived as a native
// repository named after the source. With run=true the first reconcile is queued in
// the background, so a large initial import never blocks this response.
//
// Example: {"source": {"provider": "github", "locator": "https://github.com/acme/site"}, "run": true}
func (o syncOps) create(ctx context.Context, in *syncReq) (*syncView, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	body := *in
	kind := strings.ToLower(strings.TrimSpace(body.Kind))
	if kind == "" {
		kind = "git"
	}
	if kind != "git" {
		return nil, zip.ErrBadRequest("unsupported kind (only \"git\" today)")
	}
	direction := strings.ToLower(strings.TrimSpace(body.Direction))
	if direction == "" {
		direction = dirBoth
	}
	if !validDirection(direction) {
		return nil, zip.ErrBadRequest("direction must be both|pull|push|off")
	}
	trigger := strings.ToLower(strings.TrimSpace(body.Trigger))
	if trigger == "" {
		trigger = trigWebhook
	}
	if !validTrigger(trigger) {
		return nil, zip.ErrBadRequest("trigger must be webhook|poll|manual")
	}
	src, err := validateGitSource(body.Source)
	if err != nil {
		return nil, err
	}
	tgt := deriveGitTarget(body.Target, src)
	actor := strings.TrimSpace(body.Actor)
	if actor == "" {
		actor = strings.TrimSpace(os.Getenv("GIT_SYNC_ACTOR"))
	}
	store, err := storeFor(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	id, err := genID("sync")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	if err := store.Upsert(ctx, Sync{
		ID: id, Org: org, Kind: kind, Source: src, Target: tgt,
		Direction: direction, Trigger: trigger, Actor: actor,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "upsert sync: %v", err)
	}
	stored, err := store.GetByEndpoints(ctx, org, kind, src.Locator, tgt.Locator)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "read sync: %v", err)
	}
	if body.Run {
		spawnReconcile(store, stored)
	}
	v := syncToView(stored)
	return &v, nil
}

// patchSyncIn is a partial update of one sync's mutable policy. Every policy field is
// a pointer so an OMITTED field is left alone; the id comes from the path.
//
// A JSON null and an absent key arrive identically here — encoding/json leaves a
// pointer nil for both — and that is exactly what this route has always meant, since
// none of these fields is clearable: each one either takes a new legal value or keeps
// the stored one. The pointers therefore carry the wire unchanged rather than turning
// a "clear" into a no-op, which is the trap a pointer In sets on a route that DOES
// distinguish the two. Pinned by TestPatchSync_NullIsAbsent.
type patchSyncIn struct {
	// ID is the sync to update, from the path.
	ID string `json:"id"`
	// Direction is both, pull, push or off. Omitted, the stored direction stands.
	Direction *string `json:"direction"`
	// Trigger is webhook, poll or manual. Omitted, the stored trigger stands.
	Trigger *string `json:"trigger"`
	// Actor is the loop-guard identity the sync writes as. Omitted, the stored actor
	// stands.
	Actor *string `json:"actor"`
	// Source, Target and Kind are DECLARED HERE IN ORDER TO BE REFUSED.
	//
	// They are immutable by design — re-pointing a sync is a delete and a create, so
	// a link can never silently start syncing somewhere else — but an UNDECLARED
	// field is dropped by the binder before the handler sees it, so a request asking
	// to repoint answered 200, changed nothing, and said nothing. The operator then
	// believes a moved repository has been repointed and it has not.
	//
	// Live: a sync still naming github.com/hanzoai/cloud after the repository moved
	// to hanzo-inc/cloud failed every reconcile with "Repository not found", and the
	// PATCH that appeared to fix it did nothing at all. Declaring the fields is what
	// lets the documented immutability actually answer.
	Source *endpointReq `json:"source"`
	Target *endpointReq `json:"target"`
	Kind   *string      `json:"kind"`
}

// Patch updates one sync's mutable policy — direction, trigger and actor — in place.
// The endpoints and the kind are immutable: re-pointing a sync is a delete and a
// create, so a link can never silently start syncing somewhere else. A field the
// request omits is left as it was. Changing the direction immediately reconciles the
// derived outbound mirror, so turning push off stops the upstream being written to
// rather than merely recording the intent.
//
// Example: {"id": "sync_1", "direction": "pull"}
func (o syncOps) patch(ctx context.Context, in *patchSyncIn) (*syncView, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	cur, err := store.Get(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.ErrNotFound("sync not found")
	}
	body := *in
	// Refused WHOLE and BEFORE anything is applied: a request that asks to repoint
	// AND to change the direction must not have half of it honoured while the half
	// the caller cared about is dropped.
	if body.Source != nil || body.Target != nil || body.Kind != nil {
		return nil, zip.ErrBadRequest(
			"a sync's endpoints and kind are immutable: delete this sync and create the one you want, so a link never silently starts syncing somewhere else")
	}
	if body.Direction != nil {
		d := strings.ToLower(strings.TrimSpace(*body.Direction))
		if !validDirection(d) {
			return nil, zip.ErrBadRequest("direction must be both|pull|push|off")
		}
		cur.Direction = d
	}
	if body.Trigger != nil {
		tg := strings.ToLower(strings.TrimSpace(*body.Trigger))
		if !validTrigger(tg) {
			return nil, zip.ErrBadRequest("trigger must be webhook|poll|manual")
		}
		cur.Trigger = tg
	}
	if body.Actor != nil {
		cur.Actor = strings.TrimSpace(*body.Actor)
	}
	cur.UpdatedAt = time.Now().Unix()
	if err := store.Upsert(ctx, cur); err != nil { // endpoints unchanged → updates in place
		return nil, zip.Errorf(http.StatusInternalServerError, "update sync: %v", err)
	}
	// Reconcile the derived outbound push-mirror to the (possibly new) direction.
	if cur.Kind == "git" {
		reconcileOutboundMirror(ctx, s, cur, dirPushes(cur.Direction))
	}
	stored, err := store.Get(ctx, org, cur.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "read sync: %v", err)
	}
	v := syncToView(stored)
	return &v, nil
}

// List returns every sync link the caller's org has, each with its two endpoints, its
// direction and trigger policy, and the time it last reconciled. Scoped to the
// caller's own org — another tenant's links are structurally unreachable.
func (o syncOps) list(ctx context.Context, _ *noInput) (*syncList, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	out := make([]syncView, 0, len(rows))
	for _, v := range rows {
		out = append(out, syncToView(v))
	}
	return &syncList{Data: out}, nil
}

// Get returns one sync by id. It is org-scoped: an id belonging to another tenant is
// the same 404 an unknown id gives, so a probe learns nothing about what exists.
//
// Example: {"id": "sync_1"}
func (o syncOps) get(ctx context.Context, in *syncRef) (*syncView, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	v, err := store.Get(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.ErrNotFound("sync not found")
	}
	view := syncToView(v)
	return &view, nil
}

// Delete removes one sync and tears down the outbound mirror it derived, answering
// 204. The teardown is the point: without it an unsynced repository would keep
// force-pushing to the upstream it is no longer linked to. Org-scoped, so another
// tenant's id is the same 404 an unknown id gives.
//
// Example: {"id": "sync_1"}
func (o syncOps) delete(ctx context.Context, in *syncRef) (*noContent, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	id := strings.TrimSpace(in.ID)
	sy, err := store.Get(ctx, org, id)
	if err != nil {
		return nil, zip.ErrNotFound("sync not found")
	}
	// Tear down the outbound push-mirror (best-effort) so an unsynced repo never
	// keeps pushing to the upstream.
	if sy.Kind == "git" {
		reconcileOutboundMirror(ctx, s, sy, false)
	}
	if _, err := store.Delete(ctx, org, id); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return nil, nil
}

// Run reconciles one sync now — the manual re-sync, and the initial import for a link
// created without run=true. The work is handed to a bounded background worker and the
// call answers 202 immediately, so a large mirror-in never holds the request open;
// queued=true means accepted, not finished.
//
// Example: {"id": "sync_1"}
func (o syncOps) run(ctx context.Context, in *syncRef) (*syncQueued, error) {
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	sy, err := store.Get(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.ErrNotFound("sync not found")
	}
	spawnReconcile(store, sy)
	return &syncQueued{Queued: true, ID: sy.ID}, nil
}

// ── validation + helpers ─────────────────────────────────────────────────────

// validateGitSource checks a git source endpoint: provider ∈ {github,gitlab}, an
// https clone URL, no userinfo, on the provider's canonical host. Returns the
// canonicalized endpoint.
func validateGitSource(e endpointReq) (Endpoint, error) {
	provider := strings.ToLower(strings.TrimSpace(e.Provider))
	host, ok := gitHostForProvider[provider]
	if !ok {
		return Endpoint{}, zip.ErrBadRequest("source.provider must be github or gitlab")
	}
	raw := strings.TrimSpace(e.Locator)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return Endpoint{}, zip.ErrBadRequest("source.locator must be an https clone URL")
	}
	if !strings.EqualFold(u.Hostname(), host) {
		return Endpoint{}, zip.ErrBadRequest("source.locator host must be " + host)
	}
	u.User = nil // credentials ride env-only at fetch time, never a stored value
	if repoNameFromLocator(u.String()) == "" {
		return Endpoint{}, zip.ErrBadRequest("source.locator must name a repository")
	}
	return Endpoint{Connector: strings.TrimSpace(e.Connector), Provider: provider, Locator: u.String()}, nil
}

// deriveGitTarget fills a git target from the request, defaulting to a native
// Hanzo Git repo of the source's short name when the caller omits it.
func deriveGitTarget(e endpointReq, src Endpoint) Endpoint {
	provider := strings.ToLower(strings.TrimSpace(e.Provider))
	if provider == "" {
		provider = provNative
	}
	locator := strings.TrimSpace(e.Locator)
	if locator == "" {
		locator = repoNameFromLocator(src.Locator)
	}
	return Endpoint{Connector: strings.TrimSpace(e.Connector), Provider: provider, Locator: locator}
}

// reconcileOutboundMirror ensures (push) or tears down (!push) the native repo's
// outbound mirror to the sync's upstream — the native git plane's mirror_out lifecycle
// does the actual pushing; here we only declare or remove the target via the ONE
// outbound-target registrar (cloud.EnsureGitMirror). Best-effort: a miss is logged,
// never fatal to the CRUD op (a webhook / re-run reconciles). push=false removes the
// target; push=true ensures it.
func reconcileOutboundMirror(ctx context.Context, s *cloud.Service[state], sy Sync, push bool) {
	native := normalizeGitName(sy.Target.Locator)
	if err := cloud.EnsureGitMirror(ctx, sy.Org, "", native, sy.Source.Locator, push); err != nil {
		s.Log.Warn("sync: outbound mirror", "sync", sy.ID, "push", push, "err", err)
	}
}

// reconcileSem bounds concurrent background reconciles across all orgs so an import
// storm can't exhaust goroutines/FDs or hammer a provider's rate limit.
var reconcileSem = make(chan struct{}, 4)

// spawnReconcile runs one sync's reconcile detached (cancel-immune) under a global
// slot + timeout + panic-recover, so the HTTP response returns immediately and a
// large mirror-in never blocks it. Best-effort: runOne logs a failure; the sync
// stays and a webhook/retry re-syncs.
func spawnReconcile(store *store, sy Sync) {
	s := mounted.Load()
	if s == nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		select {
		case reconcileSem <- struct{}{}:
		case <-time.After(30 * time.Second):
			s.Log.Warn("sync: reconcile queue full", "sync", sy.ID)
			return
		}
		defer func() { <-reconcileSem }()
		ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
		defer cancel()
		runOne(ctx, store, sy, Event{Provider: sy.Source.Provider, Org: sy.Org, Manual: true})
	}()
}

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// rfc3339 formats a unix time as RFC3339 UTC ("" for 0).
func rfc3339(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}
