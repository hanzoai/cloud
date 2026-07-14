package git

import (
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

// repoScope resolves (org, project, repo) for a control-plane route keyed on the
// :name param, failing closed on a missing principal and 404-ing a repo the caller
// cannot see. The repo-existence check uses the caller's project sub-scope, so a
// cross-tenant :name is a 404 and never reaches the subscription/mirror store.
func repoScope(s *cloud.Service[state], c *zip.Ctx) (orgID, project, name string, err error) {
	o, ok := org(c)
	if !ok {
		return "", "", "", zip.ErrForbidden("X-Org-Id required")
	}
	name = normalizeName(c.Param("name"))
	if name == "" || !nameRE.MatchString(name) {
		return "", "", "", zip.ErrBadRequest("invalid repo name")
	}
	project = projectScope(c)
	store, serr := storeFor(s, o)
	if serr != nil {
		return "", "", "", zip.Errorf(http.StatusInternalServerError, "open store: %v", serr)
	}
	if _, gerr := store.Get(c.Context(), o, project, name); gerr != nil {
		return "", "", "", zip.ErrNotFound("repo not found")
	}
	return o, project, name, nil
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

type subscriptionView struct {
	ID        string   `json:"id"`
	Repo      string   `json:"repo"`
	Channel   string   `json:"channel"`
	Events    []string `json:"events"`
	CreatedAt string   `json:"createdAt"`
}

func subscriptionToView(v Subscription) subscriptionView {
	return subscriptionView{
		ID: v.ID, Repo: v.Repo, Channel: v.Channel,
		Events: splitEvents(v.Events), CreatedAt: rfc3339(v.CreatedAt),
	}
}

// subscribe binds a Slack channel to a repo for lifecycle notifications.
func subscribe(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, name, herr := repoScope(s, c)
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

func listSubscriptions(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, name, herr := repoScope(s, c)
	if herr != nil {
		return herr
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.ListSubscriptions(c.Context(), org, project, name)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	out := make([]subscriptionView, 0, len(rows))
	for _, v := range rows {
		out = append(out, subscriptionToView(v))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

func unsubscribe(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, name, herr := repoScope(s, c)
	if herr != nil {
		return herr
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	deleted, err := store.DeleteSubscription(c.Context(), org, project, name, strings.TrimSpace(c.Param("id")))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if !deleted {
		return zip.ErrNotFound("subscription not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ── mirror targets ───────────────────────────────────────────────────────────

type mirrorTargetReq struct {
	Host string `json:"host"`
	URL  string `json:"url"`
}

type mirrorTargetView struct {
	ID        string `json:"id"`
	Repo      string `json:"repo"`
	Host      string `json:"host"`
	URL       string `json:"url"`
	CreatedAt string `json:"createdAt"`
}

func mirrorToView(v MirrorTarget) mirrorTargetView {
	return mirrorTargetView{ID: v.ID, Repo: v.Repo, Host: v.Host, URL: v.URL, CreatedAt: rfc3339(v.CreatedAt)}
}

// addMirror registers a downstream remote the repo's advanced refs are pushed to.
// The URL must be https to a host on the mirror allowlist (github.com / gitlab.com
// / git.hanzo.ai): the same set the mirror credential may be sent to, so a target
// can never capture the shared token or point the push at an internal service. Any
// embedded userinfo is stripped (credentials ride env-only at push time).
func addMirror(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, name, herr := repoScope(s, c)
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

func listMirrors(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, name, herr := repoScope(s, c)
	if herr != nil {
		return herr
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.ListMirrors(c.Context(), org, project, name)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	out := make([]mirrorTargetView, 0, len(rows))
	for _, v := range rows {
		out = append(out, mirrorToView(v))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

func deleteMirror(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, name, herr := repoScope(s, c)
	if herr != nil {
		return herr
	}
	store, err := storeFor(s, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	deleted, err := store.DeleteMirror(c.Context(), org, project, name, strings.TrimSpace(c.Param("id")))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if !deleted {
		return zip.ErrNotFound("mirror not found")
	}
	return c.NoContent(http.StatusNoContent)
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
