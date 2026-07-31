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

// endpointReq is one side of a sync as the caller states it.
type endpointReq struct {
	// Connector optionally names the stored credential to authenticate with.
	Connector string `json:"connector,omitempty"`
	// Provider is the platform: github or gitlab for a source, native for a target.
	Provider string `json:"provider"`
	// Locator addresses the resource — an https clone URL for git, a repo name natively.
	Locator string `json:"locator"`
}

// syncReq creates (upserts) one sync link.
type syncReq struct {
	// Kind is the sync family; empty means git, the only kind today.
	Kind string `json:"kind"`
	// Source is the upstream side: provider github or gitlab, an https clone URL on
	// that provider's own host, no userinfo. Required.
	Source endpointReq `json:"source"`
	// Target is the downstream side; omit it for git and it derives a native Hanzo
	// Git repo named after the source.
	Target endpointReq `json:"target"`
	// Direction is both, pull, push or off; empty means both.
	Direction string `json:"direction"`
	// Trigger is webhook, poll or manual; empty means webhook.
	Trigger string `json:"trigger"`
	// Actor is the loop-guard identity whose own writes are ignored; empty takes
	// GIT_SYNC_ACTOR.
	Actor string `json:"actor"`
	// Run reconciles once in the background as soon as the link is stored.
	Run bool `json:"run"`
}

// syncRef addresses one sync link by id.
type syncRef struct {
	// ID is the sync id, as returned by create.
	ID string `json:"id"`
}

// syncPatch is the mutable policy of a link. An omitted field is left alone;
// endpoints and kind are immutable (delete and recreate to re-point).
type syncPatch struct {
	// ID is the sync id from the path.
	ID string `json:"id"`
	// Direction is both, pull, push or off.
	Direction *string `json:"direction"`
	// Trigger is webhook, poll or manual.
	Trigger *string `json:"trigger"`
	// Actor is the loop-guard identity whose own writes are ignored.
	Actor *string `json:"actor"`
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

// syncList is every sync link in the caller's org.
type syncList struct {
	// Data is the org's links; an empty array when nothing is synced.
	Data []syncView `json:"data"`
}

// runAck is the acknowledgement of a queued manual reconcile.
type runAck struct {
	// Queued is always true — the reconcile runs in a bounded background worker.
	Queued bool `json:"queued"`
	// ID echoes the sync being reconciled.
	ID string `json:"id"`
}

// ops binds the service to sync's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, the only bound form
// cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// tenant resolves the validated org every op is scoped by. It is what
// SanitizeIdentity minted from the IAM owner claim, carried across the typed seam by
// cloud.Bridge — never an input field.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok || !orgRE.MatchString(org) {
		return "", zip.ErrUnauthorized("a validated principal is required")
	}
	return org, nil
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

// createSync links a source repository to a target and stores the sync policy. It
// is an UPSERT — re-stating the same source→target pair updates that link rather
// than making a second one — and with run set it reconciles once immediately in the
// background. The org comes from the validated principal, never the body, so a link
// can only bind endpoints within the caller's own org.
//
// Example: {"source": {"provider": "github", "locator": "https://github.com/acme/widgets.git"}, "direction": "both", "trigger": "webhook", "run": true}
func (o ops) createSync(ctx context.Context, in *syncReq) (*syncView, error) {
	s := o.s
	org, err := tenant(ctx)
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

// patchSync updates a link's mutable policy — direction, trigger, actor — in place;
// an omitted field is left alone. Endpoints and kind are immutable: delete and
// recreate to re-point. Changing direction reconciles the derived outbound mirror.
//
// Example: {"id": "sync_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60", "direction": "pull"}
func (o ops) patchSync(ctx context.Context, in *syncPatch) (*syncView, error) {
	s := o.s
	org, err := tenant(ctx)
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
	if in.Direction != nil {
		d := strings.ToLower(strings.TrimSpace(*in.Direction))
		if !validDirection(d) {
			return nil, zip.ErrBadRequest("direction must be both|pull|push|off")
		}
		cur.Direction = d
	}
	if in.Trigger != nil {
		tg := strings.ToLower(strings.TrimSpace(*in.Trigger))
		if !validTrigger(tg) {
			return nil, zip.ErrBadRequest("trigger must be webhook|poll|manual")
		}
		cur.Trigger = tg
	}
	if in.Actor != nil {
		cur.Actor = strings.TrimSpace(*in.Actor)
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

// listSyncs returns every sync link in the caller's org, with each link's last
// reconcile time.
func (o ops) listSyncs(ctx context.Context, _ *struct{}) (*syncList, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(s, org)
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

// getSync returns one sync link. A link in another org reads as not found.
//
// Example: {"id": "sync_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) getSync(ctx context.Context, in *syncRef) (*syncView, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(s, org)
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

// deleteSync removes a sync link and tears down its derived outbound mirror, so an
// unsynced repository can never keep pushing to the upstream. Answers 204.
//
// Example: {"id": "sync_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) deleteSync(ctx context.Context, in *syncRef) (*struct{}, error) {
	s := o.s
	org, err := tenant(ctx)
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

// runSync reconciles one link now — the manual re-sync, and the initial import for a
// source that cannot webhook. It answers 202 immediately and the reconcile runs in a
// bounded background worker, so a large mirror-in never blocks the request.
//
// Example: {"id": "sync_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) runSync(ctx context.Context, in *syncRef) (*runAck, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(s, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	sy, err := store.Get(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.ErrNotFound("sync not found")
	}
	spawnReconcile(store, sy)
	return &runAck{Queued: true, ID: sy.ID}, nil
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

// principalOrg resolves the validated org for the request (gateway-minted, IAM-
// validated), rejecting a malformed org.
func principalOrg(c *zip.Ctx) (string, bool) {
	o, ok := principal.Org(c)
	if !ok || !orgRE.MatchString(o) {
		return "", false
	}
	return o, true
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
