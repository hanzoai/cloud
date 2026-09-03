package git

import (
	"github.com/hanzoai/cloud/internal/stamp"
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/zap-proto/zip"
)

// subscriptions.go is the control plane for the two git-lifecycle reactors'
// per-repo config: a repo→Slack-channel subscription (notify.go consumes it) and a
// repo→downstream mirror target (mirror_out.go consumes it). Both live under
// /v1/git/repos/:name/* with the SAME org-scoping every repo route uses — the org
// comes from the validated principal (never a path/body value), the repo must
// exist in the caller's scope, and a cross-org caller 404s exactly like get/delete.

// ── shared scope resolver ────────────────────────────────────────────────────

// scoped is the preamble every repo-keyed op runs: resolve the tenant off the
// request context, then validate the :name against it — 404-ing a repo the caller
// cannot see. The existence check uses the tenant's project sub-scope, so a
// cross-tenant name is a 404 and never reaches the subscription/mirror store.
// Returns the tenant and the normalized repo name.
func (o ops) scoped(ctx context.Context, rawName string) (tenant, string, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return tenant{}, "", err
	}
	name := normalizeName(rawName)
	if name == "" || !nameRE.MatchString(name) {
		return t, "", zip.ErrBadRequest("invalid repo name")
	}
	store, serr := storeFor(o.s, t.org)
	if serr != nil {
		return t, "", zip.Errorf(http.StatusInternalServerError, "open store: %v", serr)
	}
	if _, gerr := store.Get(ctx, t.org, t.project, name); gerr != nil {
		return t, "", zip.ErrNotFound("repo not found")
	}
	return t, name, nil
}

// ── subscriptions ────────────────────────────────────────────────────────────

// channelRE bounds a Slack channel reference — an id (C…/G…), a #name, or a bare
// name — to a safe token: no spaces, quotes, control chars, or JSON-breaking
// bytes, so a malformed/injected channel can never smuggle structure into the
// chat.postMessage body.
var channelRE = regexp.MustCompile(`^#?[A-Za-z0-9._-]{1,80}$`)

// subscribeReq is the subscribe request: the repo comes from the URL, the
// channel and event filter from the body.
type subscribeReq struct {
	// Name is the repo to subscribe, from the :name path segment.
	Name string `json:"name"`
	// Channel is the Slack channel the notifier posts to — an id (C…/G…), a
	// #name, or a bare name. Required.
	Channel string `json:"channel"`
	// Events narrows delivery to these lifecycle kinds (push.landed,
	// deploy.live, deploy.failed). Omit it to receive every deliverable kind; a
	// kind that is never posted to Slack is refused rather than silently dropped.
	Events []string `json:"events"`
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
		Events: splitEvents(v.Events), CreatedAt: stamp.Unix(v.CreatedAt),
	}
}

// subscribe binds a Slack channel to a repo, so the lifecycle notifier posts
// that repo's push and deploy events there. Answers 201. The same channel twice
// on one repo is a 409; a repo outside the caller's scope is a 404, exactly as
// reading it is.
//
// Example: {"name": "widgets", "channel": "#builds", "events": ["push.landed"]}
func (o ops) subscribe(ctx context.Context, in *subscribeReq) (*subscriptionView, error) {
	t, name, herr := o.scoped(ctx, in.Name)
	if herr != nil {
		return nil, herr
	}
	channel := strings.TrimSpace(in.Channel)
	if !channelRE.MatchString(channel) {
		return nil, zip.ErrBadRequest("channel must be a Slack channel id or name")
	}
	events, err := normalizeEvents(in.Events)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	v := Subscription{
		ID: mint.ID("sub"), Org: t.org, Project: t.project, Repo: name,
		Channel: channel, Events: events, CreatedAt: time.Now().Unix(),
	}
	if err := store.CreateSubscription(ctx, v); err != nil {
		if err == errConflict {
			return nil, zip.ErrConflict("channel already subscribed to this repo")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	view := subscriptionToView(v)
	return &view, nil
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
func (o ops) unsubscribe(ctx context.Context, in *childRef) (*cloud.Unit, error) {
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

// mirrorTargetReq is the add-a-mirror request: the repo comes from the URL, the
// downstream remote from the body.
type mirrorTargetReq struct {
	// Name is the repo whose advanced refs are pushed downstream, from the :name
	// path segment.
	Name string `json:"name"`
	// Host is an optional assertion of the target's hostname. The authoritative
	// host is the one in URL; a value that disagrees with it is refused.
	Host string `json:"host"`
	// URL is the downstream https git remote. Must be https to an allowlisted
	// host (github.com / gitlab.com); any embedded credentials are stripped.
	// Required.
	URL string `json:"url"`
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
	return mirrorTargetView{ID: v.ID, Repo: v.Repo, Host: v.Host, URL: v.URL, CreatedAt: stamp.Unix(v.CreatedAt)}
}

// addTarget registers a downstream remote the repo's advanced refs are pushed to
// whenever a push lands here. Answers 201. The URL must be https to a host on the
// mirror allowlist (github.com / gitlab.com): the same set the mirror credential
// may be sent to, so a target can never capture the shared token or point the push
// at an internal service. Any embedded userinfo is stripped — credentials ride
// env-only at push time and never enter the stored URL. One mirror per host per
// repo; a second is a 409.
//
// Example: {"name": "widgets", "url": "https://github.com/acme/widgets.git"}
func (o ops) addTarget(ctx context.Context, in *mirrorTargetReq) (*mirrorTargetView, error) {
	t, name, herr := o.scoped(ctx, in.Name)
	if herr != nil {
		return nil, herr
	}
	target, host, err := validateMirrorTarget(in.URL)
	if err != nil {
		return nil, err
	}
	// An explicit in.Host is a hint; the authoritative host is the URL's — reject
	// a mismatch so the stored (host,url) pair can never disagree.
	if h := strings.ToLower(strings.TrimSpace(in.Host)); h != "" && h != host {
		return nil, zip.ErrBadRequest("host does not match url host")
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	v := MirrorTarget{
		ID: mint.ID("mir"), Org: t.org, Project: t.project, Repo: name,
		Host: host, URL: target, CreatedAt: time.Now().Unix(),
	}
	if err := store.CreateMirror(ctx, v); err != nil {
		if err == errConflict {
			return nil, zip.ErrConflict("a mirror to this host already exists for the repo")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	view := mirrorToView(v)
	return &view, nil
}

// listTargets returns a repo's outbound mirror targets — the downstream remotes
// the mirror reactor pushes to whenever a push lands here.
//
// Example: {"name": "widgets"}
//
//	Response: {"data": [{"id": "mir_2d90", "repo": "widgets", "host": "github.com",
//		"url": "https://github.com/acme/widgets.git",
//		"createdAt": "2026-07-01T10:00:00Z"}]}
func (o ops) listTargets(ctx context.Context, in *repoRef) (*mirrorList, error) {
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

// deleteTarget removes one outbound mirror target; later pushes stop being
// forwarded to it. Answers 204 with no body. Nothing is done to the downstream
// remote itself — only this repo's intent to push there is dropped.
//
// Example: {"name": "widgets", "id": "mir_2d90"}
func (o ops) deleteTarget(ctx context.Context, in *childRef) (*cloud.Unit, error) {
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
