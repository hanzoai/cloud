package social

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
)

const (
	// maxContent caps a post body so an unbounded request can't amplify the shared
	// DB or a list response. Comfortably above every network's own limit.
	maxContent = 8192
	// maxField caps a single short label field (handle, ids, one media URL).
	maxField = 1024
	// maxMedia caps how many media URLs one post can carry.
	maxMedia = 10
	// defaultLimit / maxLimit bound list responses.
	defaultLimit = 200
	maxLimit     = 1000
)

// providers is the validation set for both an account's provider and a post's target
// channel — a create/update with an unknown provider is rejected; empty defaults to x.
// It is DERIVED from the ONE ordered vocabulary (providerOrder in publish.go), so adding
// a network in one place makes it valid, ordered, and cred-checkable everywhere.
var providers = providerSet()

// accountStatuses is the account connection lifecycle. Empty defaults to connected.
var accountStatuses = map[string]bool{
	"connected": true, "disconnected": true, "error": true,
}

// Post lifecycle states. These four are user-settable (validated on create/update). The
// store also holds a transient 'publishing' state during a publish attempt (the claim
// guard, see store.ClaimForPublish) which is NEVER user-settable and never counted.
const (
	statusDraft     = "draft"
	statusScheduled = "scheduled"
	statusPublished = "published"
	statusFailed    = "failed"
	statusMirrored  = "mirrored"
)

// postizSecretPath is the org-relative KMS path under which the Postiz secrets
// live. The names (POSTIZ_INGEST_TOKEN, POSTIZ_API_KEY) are documented here;
// their values never appear in code, tests, logs or commits.
const postizSecretPath = "orgs/admin/social/postiz"

// postizIngestSecretName names the ingest webhook token sealed in KMS under
// postizSecretPath.
const postizIngestSecretName = "POSTIZ_INGEST_TOKEN"

// postizAPIKeySecretName names the Postiz public API key sealed in KMS under
// postizSecretPath, used by the poller.
const postizAPIKeySecretName = "POSTIZ_API_KEY"

// postizTestPostID and postizTestIntegrationID are the Postiz Send-Test sample
// ids that must be ignored on ingest (no mirrored upsert, test event recorded).
const (
	postizTestPostID        = "cm6tcts4f0005qcwit25cis26"
	postizTestIntegrationID = "cm6s4uyou0001i2r47pxix6z1"
)

// postizIntegrationMap lists known Postiz integration ids for org hanzo (runbook §3).
// Presence (not the network name) is what orgFromIntegration keys on.
var postizIntegrationMap = map[string]string{
	"cmw8ytjjf0003luan9en61245": "x",
	"cmw8ytjjv0005luanxef9i7q0": "x",
	"cmw8ytjk60009luanc5s2ncyp": "linkedin",
	"cmw8ytjk00007luan2mam7a5d": "linkedin",
	"cmw8ytjl1000nluan3sjg8bhw": "bluesky",
	"cmw8ytjkl000fluanp0zo8gxl": "threads",
	"cmw8ytjl5000pluan53boc860": "mastodon",
	"cmw8ytjky000lluanpzmp5hyh": "facebook",
	"cmw9eo72v0001hsut5pscmrt8": "facebook",
	"cmw8ytjku000jluanm1re4eph": "reddit",
	"cmw8ytjl8000rluan5cu8elts": "discord",
	"cmw8ytjlc000tluanduyrxac6": "telegram",
	"cmw8ytjke000dluaniajpg0fm": "instagram",
	"cmw8ytjkp000hluanfjsd77qg": "tiktok",
	"cmw8ytjka000bluan3waw3bdc": "youtube",
	"cmw8ytjlf000vluano2mgjytn": "slack",
	"cmw8ytjlj000xluanb1ty9711": "devto",
	"cmw8ytjlm000zluanh14b7720": "hashnode",
	"cmw8ytjlr0011luan830ldxx3": "medium",
	"cmwbimw2g0001c36t8103qol2": "x",
}

// postStatuses is the user-settable post lifecycle vocabulary. Empty defaults to draft.
var postStatuses = map[string]bool{
	statusDraft: true, statusScheduled: true, statusPublished: true, statusFailed: true,
}

// state is social's own data: the per-org store and the publish edge. Shared deps
// (logger, brand) live in the embedded cloud.Base, reached as s.Log / s.Brand.
type state struct {
	store *Store
	pub   Publisher
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// stopScheduler stops the background due-post scheduler. Set by Mount, called by
// Shutdown; the no-op default keeps Shutdown safe before/without a Mount.
var stopScheduler = func() {}

// stopPostizPoller stops the postiz poller loop. Set by Mount, called by Shutdown;
var stopPostizPoller = func() {}

// Mount wires the social surface onto app per HIP-0106. It keeps a package global
// (mounted) for Shutdown, so it constructs the Service value directly — the same
// "complex flavour" clients/crm uses.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("social: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("social.Use:  empty DataDir")
	}
	store, err := openStore(cloud.DataDir(), deps.MasterKey, deps.KMS)
	if err != nil {
		return fmt.Errorf("social.Use:  open store: %w", err)
	}
	b := cloud.NewBase(deps, "social")
	s := &cloud.Service[state]{Base: b, State: state{store: store, pub: newPublisher(deps)}}
	// Reset any post a process that is gone left mid-publish (→ failed, retryable)
	// BEFORE the scheduler starts, so a stuck claim never wedges a scheduled post. Only
	// a stale one: replicas that are publishing now are not this process's to fail.
	if err := recoverStale(s); err != nil {
		_ = store.Close()
		return fmt.Errorf("social.Use:  recover: %w", err)
	}
	mounted = s
	stopScheduler = startScheduler(s)
	stopPostizPoller = startPostizPoller(s)

	exposeTarget()
	exposePlane()
	routes(app, s)

	b.Log.Info("social mounted", "brand", cloud.Brand())
	return nil
}

// routes registers the social surface: the account + post CRUD, the publish
// action, the org's app credentials, and the two read-only roll-ups. All of them
// are typed ops — see
// typed.go — so each is one registry entry carrying its REST route, its OpenAPI
// operation, its MCP tool, its CLI command and its generated SDK method.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/social")
	// Bridge FIRST. A typed op receives only a context, so the validated org
	// reaches it by being parked there — never as an In field, which is
	// caller-supplied and would be a cross-tenant read the caller asserted for
	// itself. fiber runs middleware in registration order, so one installed after
	// the leaves below would never run. Serve installs this binary-wide too, but
	// this package's own tests do not run Serve, so a typed op relying on that
	// would 403 in every test here and work only in production.
	g.Use(cloud.Bridge())

	// Declared on the GROUP, so each op's path is the prefix composed with its
	// leaf — the same composition the router does, and the identity every
	// projection keys on. cmd/zipdoc resolves the prefix the same way, so the doc
	// comments reach the document and the tool list.
	o := ops{s: s}
	g.Get("/summary", o.summary)
	g.Get("/providers", o.providers)
	g.Get("/credentials", o.credentials)
	g.Put("/credentials", o.putCredentials)

	g.Get("/accounts", o.listAccounts)
	g.Post("/accounts", o.createAccount, zip.WithStatus(http.StatusCreated))
	g.Get("/accounts/:id", o.getAccount)
	g.Put("/accounts/:id", o.updateAccount)
	g.Delete("/accounts/:id", o.deleteAccount)

	g.Get("/posts", o.listPosts)
	g.Post("/posts", o.createPost, zip.WithStatus(http.StatusCreated))
	g.Get("/posts/:id", o.getPost)
	g.Put("/posts/:id", o.updatePost)
	g.Delete("/posts/:id", o.deletePost)
	g.Post("/posts/:id/publish", o.publish)

	// POST /v1/social/ingest/postiz/:ingestToken — the path token IS the auth (KMS secret
	// POSTIZ_INGEST_TOKEN at path social/postiz). Registered outside Bridge auth:
	// no bearer, no org from the caller — the token is the credential.
	app.Raw(http.MethodPost, "/v1/social/ingest/postiz/:ingestToken", ingestPostizRoute)

	mountChannelRoutes(app, s)
	mountPostRoutes(app, s)
}

// limitOf reads the caller's ?limit token. It TRIMS before parsing and falls back
// to the default on anything it cannot read, which is why the field carrying it is
// a string: zip's own setScalar (typed.go:407) parses with strconv.ParseInt and no
// trim, and leaves an unreadable value at the field's ZERO — so an int field would
// refuse `?limit=%2050`, which is a page of fifty here, and could not tell
// `?limit=0` from `?limit=abc`. One value, one parse rule.
func limitOf(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// normProvider lower-cases + defaults (empty → x) and validates against the fixed
// provider vocabulary.
func normProvider(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "x", true
	}
	return v, providers[v]
}

// normAccountStatus lower-cases + defaults (empty → connected) and validates.
func normAccountStatus(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "connected", true
	}
	return v, accountStatuses[v]
}

// normPostStatus lower-cases + defaults (empty → draft) and validates.
func normPostStatus(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "draft", true
	}
	return v, postStatuses[v]
}

// nonNeg clamps a signed amount to >= 0 (schedule timestamps are never negative).
func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// normMedia trims each media URL, drops empties, bounds each to maxField and the
// whole list to maxMedia, and returns a non-nil slice. This is the ONE place a
// post's media is sanitized on write (create + update), mirroring how content and
// channel are normalized — the store just persists what this returns.
func normMedia(in []string) []string {
	out := make([]string, 0, len(in))
	for _, u := range in {
		if u = text.Trim(u, maxField); u == "" {
			continue
		}
		out = append(out, u)
		if len(out) >= maxMedia {
			break
		}
	}
	return out
}

// mapErr maps a store sentinel error to the right HTTP error. Non-sentinel errors
// become a 500 with the wrapped message.
func mapErr(err error, notFoundMsg string) error {
	if errors.Is(err, dialect.ErrText) {
		return zip.ErrBadRequest(err.Error())
	}
	switch err {
	case errNotFound:
		return zip.ErrNotFound(notFoundMsg)
	case errConflict:
		return zip.ErrConflict("already exists")
	default:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
}

// mapMirrorErr maps a mirrored-post control error to an honest HTTP status: a mirrored
// post is read-only, so publishing or scheduling it is a 409.
func mapMirrorErr(err error) error {
	switch err {
	case errConflict:
		return zip.ErrConflict("origin=postiz is read-only")
	default:
		return mapErr(err, "post not found")
	}
}

// mapPublishErr maps a publishPost control error to an honest HTTP status: not-found →
// 404, provider-not-configured → 503 (with the missing-credentials detail), a mirrored
// post → 409 (origin=postiz is read-only), else 500.
// Each is a returned zip error, which is what the untyped handler returned too — so
// typing moved no refusal: zip renders the same RFC 9457 problem members it already
// did (zip problem.go).
func mapPublishErr(err error) error {
	switch {
	case errors.Is(err, errNotFound):
		return zip.ErrNotFound("post not found")
	case errors.Is(err, errProviderNotConfigured):
		return zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	case err == errConflict:
		return zip.ErrConflict("origin=postiz is read-only")
	default:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
}

// ingestPostizRoute adapts ingestPostiz to a zip handler: it reads the bearer-free
// path token, decodes the body, and delegates. It runs outside Bridge auth because
// the token IS the credential — no org, no bearer, nothing to validate upstream.
func ingestPostizRoute(c *zip.Ctx) error {
	if mounted == nil || mounted.State.store == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "social not mounted")
	}
	token := c.Param("ingestToken")
	body := c.Body()
	code, out, err := ingestPostiz(c.Context(), mounted, token, body)
	if err != nil {
		return err
	}
	c.Status(code)
	return c.JSON(code, out)
}

// Shutdown stops the scheduler and closes the social store, in that order (drain the
// background publisher before releasing its store). Idempotent.
func Shutdown() error {
	stopScheduler()
	stopScheduler = func() {}
	stopPostizPoller()
	stopPostizPoller = func() {}
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
