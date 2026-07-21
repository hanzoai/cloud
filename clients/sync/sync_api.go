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
	"github.com/hanzoai/cloud/clients/principal"
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

type endpointReq struct {
	Connector string `json:"connector,omitempty"`
	Provider  string `json:"provider"`
	Locator   string `json:"locator"`
}

type syncReq struct {
	Kind      string      `json:"kind"`      // default "git"
	Source    endpointReq `json:"source"`    // {provider, locator[, connector]}
	Target    endpointReq `json:"target"`    // optional for git — derived from source
	Direction string      `json:"direction"` // default both
	Trigger   string      `json:"trigger"`   // default webhook
	Actor     string      `json:"actor"`     // optional loop-guard identity (default GIT_SYNC_ACTOR)
	Run       bool        `json:"run"`       // reconcile immediately (background) after create
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

// createSync upserts a sync and (optionally) kicks a background reconcile. The org
// comes from the validated principal — never a body field — so a sync can only bind
// endpoints within the caller's own org.
func createSync(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principalOrg(c)
	if !ok {
		return zip.ErrUnauthorized("a validated principal is required")
	}
	var body syncReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	kind := strings.ToLower(strings.TrimSpace(body.Kind))
	if kind == "" {
		kind = "git"
	}
	if kind != "git" {
		return zip.ErrBadRequest("unsupported kind (only \"git\" today)")
	}
	direction := strings.ToLower(strings.TrimSpace(body.Direction))
	if direction == "" {
		direction = dirBoth
	}
	if !validDirection(direction) {
		return zip.ErrBadRequest("direction must be both|pull|push|off")
	}
	trigger := strings.ToLower(strings.TrimSpace(body.Trigger))
	if trigger == "" {
		trigger = trigWebhook
	}
	if !validTrigger(trigger) {
		return zip.ErrBadRequest("trigger must be webhook|poll|manual")
	}
	src, err := validateGitSource(body.Source)
	if err != nil {
		return err
	}
	tgt := deriveGitTarget(body.Target, src)
	actor := strings.TrimSpace(body.Actor)
	if actor == "" {
		actor = strings.TrimSpace(os.Getenv("GIT_SYNC_ACTOR"))
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	id, err := genID("sync")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	if err := store.Upsert(c.Context(), Sync{
		ID: id, Org: org, Kind: kind, Source: src, Target: tgt,
		Direction: direction, Trigger: trigger, Actor: actor,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "upsert sync: %v", err)
	}
	stored, err := store.GetByEndpoints(c.Context(), org, kind, src.Locator, tgt.Locator)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "read sync: %v", err)
	}
	if body.Run {
		spawnReconcile(store, stored)
	}
	return c.JSON(http.StatusOK, syncToView(stored))
}

// patchSync updates a sync's mutable policy (direction, trigger, actor) in place —
// endpoints and kind are immutable (delete + recreate to re-point). A direction
// change reconciles the derived outbound mirror (git).
func patchSync(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principalOrg(c)
	if !ok {
		return zip.ErrUnauthorized("a validated principal is required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	cur, err := store.Get(c.Context(), org, strings.TrimSpace(c.Param("id")))
	if err != nil {
		return zip.ErrNotFound("sync not found")
	}
	var body struct {
		Direction *string `json:"direction"`
		Trigger   *string `json:"trigger"`
		Actor     *string `json:"actor"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.Direction != nil {
		d := strings.ToLower(strings.TrimSpace(*body.Direction))
		if !validDirection(d) {
			return zip.ErrBadRequest("direction must be both|pull|push|off")
		}
		cur.Direction = d
	}
	if body.Trigger != nil {
		tg := strings.ToLower(strings.TrimSpace(*body.Trigger))
		if !validTrigger(tg) {
			return zip.ErrBadRequest("trigger must be webhook|poll|manual")
		}
		cur.Trigger = tg
	}
	if body.Actor != nil {
		cur.Actor = strings.TrimSpace(*body.Actor)
	}
	cur.UpdatedAt = time.Now().Unix()
	if err := store.Upsert(c.Context(), cur); err != nil { // endpoints unchanged → updates in place
		return zip.Errorf(http.StatusInternalServerError, "update sync: %v", err)
	}
	// Reconcile the derived outbound Gitea push-mirror to the (possibly new) direction.
	if cur.Kind == "git" {
		reconcileOutboundMirror(c.Context(), s, cur, dirPushes(cur.Direction))
	}
	stored, err := store.Get(c.Context(), org, cur.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "read sync: %v", err)
	}
	return c.JSON(http.StatusOK, syncToView(stored))
}

// listSyncs returns every sync link for the caller's org.
func listSyncs(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principalOrg(c)
	if !ok {
		return zip.ErrUnauthorized("a validated principal is required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	out := make([]syncView, 0, len(rows))
	for _, v := range rows {
		out = append(out, syncToView(v))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// getSync returns one sync by id (org-scoped).
func getSync(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principalOrg(c)
	if !ok {
		return zip.ErrUnauthorized("a validated principal is required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	v, err := store.Get(c.Context(), org, strings.TrimSpace(c.Param("id")))
	if err != nil {
		return zip.ErrNotFound("sync not found")
	}
	return c.JSON(http.StatusOK, syncToView(v))
}

// deleteSync removes a sync and tears down its derived outbound mirror (so an
// unsynced repo can never keep force-pushing to the upstream).
func deleteSync(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principalOrg(c)
	if !ok {
		return zip.ErrUnauthorized("a validated principal is required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	id := strings.TrimSpace(c.Param("id"))
	sy, err := store.Get(c.Context(), org, id)
	if err != nil {
		return zip.ErrNotFound("sync not found")
	}
	// Tear down the outbound Gitea push-mirror (best-effort) so an unsynced repo never
	// keeps pushing to the upstream.
	if sy.Kind == "git" {
		reconcileOutboundMirror(c.Context(), s, sy, false)
	}
	if _, err := store.Delete(c.Context(), org, id); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// runSync reconciles one sync immediately (a manual re-sync / initial import), in a
// bounded background worker so a large mirror-in never blocks the request.
func runSync(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principalOrg(c)
	if !ok {
		return zip.ErrUnauthorized("a validated principal is required")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	sy, err := store.Get(c.Context(), org, strings.TrimSpace(c.Param("id")))
	if err != nil {
		return zip.ErrNotFound("sync not found")
	}
	spawnReconcile(store, sy)
	return c.JSON(http.StatusAccepted, map[string]any{"queued": true, "id": sy.ID})
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
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
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
