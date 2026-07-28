package git

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// subscriptions.go is the control plane for the two git-lifecycle reactors'
// per-repo config: a repo→Slack-channel subscription (notify.go consumes it) and a
// repo→downstream mirror target (mirror_out.go consumes it). Both live under
// /v1/git/repos/:name/* with the SAME org-scoping every repo route uses — the org
// comes from the validated principal (never a path/body value), the repo must
// exist in the caller's scope, and a cross-org caller 404s exactly like get/delete.

// ── shared scope resolver ────────────────────────────────────────────────────

// repoScope validates the :name of a control-plane route against the caller's
// tenant, 404-ing a repo the caller cannot see. The existence check uses the
// tenant's project sub-scope, so a cross-tenant name is a 404 and never reaches
// the subscription/mirror store. Returns the normalized repo name.
//
// It takes a context and a tenant rather than a *zip.Ctx so the ONE resolver
// serves both handler shapes: a typed op has only the context, and the raw
// creators pass theirs in.
func repoScope(s *cloud.Service[state], ctx context.Context, t tenant, rawName string) (string, error) {
	name := normalizeName(rawName)
	if name == "" || !nameRE.MatchString(name) {
		return "", zip.ErrBadRequest("invalid repo name")
	}
	store, serr := storeFor(s, t.org)
	if serr != nil {
		return "", zip.Errorf(http.StatusInternalServerError, "open store: %v", serr)
	}
	if _, gerr := store.Get(ctx, t.org, t.project, name); gerr != nil {
		return "", zip.ErrNotFound("repo not found")
	}
	return name, nil
}

// scoped is the preamble every repo-keyed typed op runs: resolve the tenant off
// the request context, then validate the repo name against it.
func (o ops) scoped(ctx context.Context, rawName string) (tenant, string, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return tenant{}, "", err
	}
	name, err := repoScope(o.s, ctx, t, rawName)
	return t, name, err
}

// ── subscriptions ────────────────────────────────────────────────────────────

// channelRE bounds a Slack channel reference — an id (C…/G…), a #name, or a bare
// name — to a safe token: no spaces, quotes, control chars, or JSON-breaking
// bytes, so a malformed/injected channel can never smuggle structure into the
// chat.postMessage body.
var channelRE = regexp.MustCompile(`^#?[A-Za-z0-9._-]{1,80}$`)

type subscribeReq struct {
	Channel string   `json:"channel"`
	Events  []string `json:"events"`
}

// subscriptionView is one repo→Slack-channel subscription.
type subscriptionView struct {
	// ID is the subscription's identifier ("sub_…"), the handle to delete it by.
	ID string `json:"id"`
	// Repo is the repo whose lifecycle events are delivered.
	Repo string `json:"repo"`
	// Channel is the Slack channel id or name the notifier posts to.
	Channel string `json:"channel"`
	// Events is the kind filter; absent means every deliverable kind.
	Events []string `json:"events"`
	// CreatedAt is RFC 3339 UTC.
	CreatedAt string `json:"createdAt"`
}

// subscriptionList is the collection envelope for a repo's subscriptions.
type subscriptionList struct {
	// Data holds the repo's subscriptions.
	Data []subscriptionView `json:"data"`
}

// childRef addresses one child row of a repo: a subscription or a mirror target.
type childRef struct {
	// Name is the repo, from the :name path segment.
	Name string `json:"name"`
	// ID is the row to remove, from the :id path segment.
	ID string `json:"id"`
}

func subscriptionToView(v Subscription) subscriptionView {
	return subscriptionView{
		ID: v.ID, Repo: v.Repo, Channel: v.Channel,
		Events: splitEvents(v.Events), CreatedAt: rfc3339(v.CreatedAt),
	}
}

// subscribe binds a Slack channel to a repo for lifecycle notifications. Raw,
// not a typed op: it answers 201, and zip's typed registrar has no status seam.
func subscribe(s *cloud.Service[state], c *zip.Ctx) error {
	t, herr := tenantFrom(c)
	if herr != nil {
		return herr
	}
	org, project := t.org, t.project
	name, herr := repoScope(s, c.Context(), t, c.Param("name"))
	if herr != nil {
		return herr
	}
	var body subscribeReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	channel := strings.TrimSpace(body.Channel)
	if !channelRE.MatchString(channel) {
		return zip.ErrBadRequest("channel must be a Slack channel id or name")
	}
	events, err := normalizeEvents(body.Events)
	if err != nil {
		return err
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	id, err := genID("sub")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	v := Subscription{
		ID: id, Org: org, Project: project, Repo: name,
		Channel: channel, Events: events, CreatedAt: time.Now().Unix(),
	}
	if err := store.CreateSubscription(c.Context(), v); err != nil {
		if err == errConflict {
			return zip.ErrConflict("channel already subscribed to this repo")
		}
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return c.JSON(http.StatusCreated, subscriptionToView(v))
}

// listSubscriptions returns a repo's Slack subscriptions — which channels the
// lifecycle notifier posts this repo's push and deploy events to.
//
// Example: {"name": "widgets"}
//
//	Response: {"data": [{"id": "sub_7c2e", "repo": "widgets", "channel": "#builds",
//		"events": ["push.landed"], "createdAt": "2026-07-01T10:00:00Z"}]}
func (o ops) listSubscriptions(ctx context.Context, in *repoRef) (*subscriptionList, error) {
	t, name, err := o.scoped(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.ListSubscriptions(ctx, t.org, t.project, name)
	if err != nil {
		return nil, internalErr(err)
	}
	out := make([]subscriptionView, 0, len(rows))
	for _, v := range rows {
		out = append(out, subscriptionToView(v))
	}
	return &subscriptionList{Data: out}, nil
}

// unsubscribe removes one Slack subscription from a repo; the notifier stops
// posting that repo's events to that channel. Answers 204 with no body. An id
// that is not this repo's subscription is not found.
//
// Example: {"name": "widgets", "id": "sub_7c2e"}
func (o ops) unsubscribe(ctx context.Context, in *childRef) (*noContent, error) {
	t, name, err := o.scoped(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	deleted, err := store.DeleteSubscription(ctx, t.org, t.project, name, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, internalErr(err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("subscription not found")
	}
	return nil, nil
}

// ── mirror targets ───────────────────────────────────────────────────────────

type mirrorTargetReq struct {
	Host string `json:"host"`
	URL  string `json:"url"`
}

// mirrorTargetView is one downstream remote a repo's refs are pushed to.
type mirrorTargetView struct {
	// ID is the target's identifier ("mir_…"), the handle to remove it by.
	ID string `json:"id"`
	// Repo is the repo whose advanced refs are pushed downstream.
	Repo string `json:"repo"`
	// Host is the target's lowercased hostname, taken from URL and never the body.
	Host string `json:"host"`
	// URL is the canonical https remote, with any embedded credentials stripped.
	URL string `json:"url"`
	// CreatedAt is RFC 3339 UTC.
	CreatedAt string `json:"createdAt"`
}

// mirrorList is the collection envelope for a repo's mirror targets.
type mirrorList struct {
	// Data holds the repo's outbound mirror targets.
	Data []mirrorTargetView `json:"data"`
}

func mirrorToView(v MirrorTarget) mirrorTargetView {
	return mirrorTargetView{ID: v.ID, Repo: v.Repo, Host: v.Host, URL: v.URL, CreatedAt: rfc3339(v.CreatedAt)}
}

// addMirror registers a downstream remote the repo's advanced refs are pushed to.
// The URL must be https to a host on the mirror allowlist (github.com / gitlab.com
// / git.hanzo.ai): the same set the mirror credential may be sent to, so a target
// can never capture the shared token or point the push at an internal service. Any
// embedded userinfo is stripped (credentials ride env-only at push time).
//
// Raw, not a typed op: it answers 201, and zip's typed registrar has no status seam.
func addMirror(s *cloud.Service[state], c *zip.Ctx) error {
	t, herr := tenantFrom(c)
	if herr != nil {
		return herr
	}
	org, project := t.org, t.project
	name, herr := repoScope(s, c.Context(), t, c.Param("name"))
	if herr != nil {
		return herr
	}
	var body mirrorTargetReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	target, host, err := validateMirrorTarget(body.URL)
	if err != nil {
		return err
	}
	// An explicit body.host is a hint; the authoritative host is the URL's — reject
	// a mismatch so the stored (host,url) pair can never disagree.
	if h := strings.ToLower(strings.TrimSpace(body.Host)); h != "" && h != host {
		return zip.ErrBadRequest("host does not match url host")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	id, err := genID("mir")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	v := MirrorTarget{
		ID: id, Org: org, Project: project, Repo: name,
		Host: host, URL: target, CreatedAt: time.Now().Unix(),
	}
	if err := store.CreateMirror(c.Context(), v); err != nil {
		if err == errConflict {
			return zip.ErrConflict("a mirror to this host already exists for the repo")
		}
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return c.JSON(http.StatusCreated, mirrorToView(v))
}

// listMirrors returns a repo's outbound mirror targets — the downstream remotes
// the mirror reactor pushes to whenever a push lands here.
//
// Example: {"name": "widgets"}
//
//	Response: {"data": [{"id": "mir_2d90", "repo": "widgets", "host": "github.com",
//		"url": "https://github.com/acme/widgets.git",
//		"createdAt": "2026-07-01T10:00:00Z"}]}
func (o ops) listMirrors(ctx context.Context, in *repoRef) (*mirrorList, error) {
	t, name, err := o.scoped(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.ListMirrors(ctx, t.org, t.project, name)
	if err != nil {
		return nil, internalErr(err)
	}
	out := make([]mirrorTargetView, 0, len(rows))
	for _, v := range rows {
		out = append(out, mirrorToView(v))
	}
	return &mirrorList{Data: out}, nil
}

// deleteMirror removes one outbound mirror target; later pushes stop being
// forwarded to it. Answers 204 with no body. Nothing is done to the downstream
// remote itself — only this repo's intent to push there is dropped.
//
// Example: {"name": "widgets", "id": "mir_2d90"}
func (o ops) deleteMirror(ctx context.Context, in *childRef) (*noContent, error) {
	t, name, err := o.scoped(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	deleted, err := store.DeleteMirror(ctx, t.org, t.project, name, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, internalErr(err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("mirror not found")
	}
	return nil, nil
}

// ── validation helpers ───────────────────────────────────────────────────────

// validateMirrorTarget parses + hardens a downstream mirror URL: https only, a
// host on the OUTBOUND-target allowlist (mirrorOutHostAllowed — {github.com,
// gitlab.com}, the local git host deliberately excluded), userinfo stripped.
// Returns the canonical URL + its lowercased host.
func validateMirrorTarget(raw string) (canonical, host string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", zip.ErrBadRequest("url is required")
	}
	u, perr := url.Parse(raw)
	if perr != nil || u.Host == "" || u.Scheme != "https" {
		return "", "", zip.ErrBadRequest("url must be an https git URL")
	}
	u.User = nil // credentials ride env-only at push time, never a stored/argv value
	h := strings.ToLower(u.Hostname())
	if !mirrorOutHostAllowed(h) {
		return "", "", zip.ErrBadRequest("host is not an allowed mirror target")
	}
	return u.String(), h, nil
}

// notifyKinds is the wire-name vocabulary a subscription may filter on — exactly
// the kinds the notifier DELIVERS. build.started is intentionally absent: it is
// emitted for other reactors but never posted to Slack, so subscribing to it would
// be a silent no-op — reject it at subscribe time instead (Red INFO-a).
var notifyKinds = map[string]bool{
	string(cloud.LifecyclePushLanded):   true,
	string(cloud.LifecycleDeployLive):   true,
	string(cloud.LifecycleDeployFailed): true,
}

// normalizeEvents validates the requested event filter against the deliverable
// kinds and returns a stable, deduped CSV. Empty (no filter) means every
// deliverable kind.
func normalizeEvents(events []string) (string, error) {
	seen := map[string]bool{}
	var out []string
	for _, e := range events {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !notifyKinds[e] {
			return "", zip.ErrBadRequest("event kind is not deliverable to Slack: " + e)
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return strings.Join(out, ","), nil
}

// splitEvents expands a stored CSV back to a slice (nil for the all-kinds default).
func splitEvents(csv string) []string {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	return strings.Split(csv, ",")
}
