// Package social is posting to every social account you own, now or on a schedule.
//
// An org's connected channels (X, Facebook, Instagram, LinkedIn, TikTok,
// YouTube, Threads) and the posts it publishes or schedules to them.
//
// Two entities. An account is a connected channel (the hanzoai/social stack's
// "integration": GET /public/v1/integrations), and a post is content published or
// scheduled to a channel (POST /public/v1/posts {type:now|schedule, date, …}).
// Scheduling is not a third entity — it is a post with Status=="scheduled"
// carrying a future ScheduleAt.
//
// This is the in-process fold of the standalone social pods onto the cloud
// framework. apps/content still reaches the SAME upstream over HTTP
// (channels.go → api.social.hanzo.ai), so social publishing has two paths today
// and this one — which owns the accounts, the scheduler and the publish edge — is
// the one. apps/marketing's content calendar is a third scheduled-post store; it
// has no publisher wired and answers 501.
//
// The publish edge (publish.go) and the scheduler (scheduler.go) ARE folded: a post
// fans out to its channel's connected accounts through the Publisher client, on an
// explicit publish, on create (when scheduled for now-or-earlier), and on the scheduler
// tick (scheduled → published when the time arrives). The provider push itself is the
// swappable Publisher edge; its fail-closed default is honest — no Hanzo deployment
// carries the provider OAuth-app credentials (providerCreds) the live orchestrator needs,
// so a publish reports exactly which credentials are missing (503) and NEVER fakes
// success. The per-account OAuth connect flow + native per-provider push are the honest
// remaining gap (see the fold report + GET /v1/social/providers).
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner claim (HIP-0026) — and NEVER a client-supplied header. Every store query
// filters WHERE org=?, so one tenant can never read or mutate another's data.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/social/summary            per-org roll-up (posts/scheduled/published/accounts)
//	GET    /v1/social/providers          publish-readiness per network (+ missing creds)
//	GET    /v1/social/accounts           list accounts (?provider=)      -> {data:[…]}
//	POST   /v1/social/accounts           connect an account             -> socialAccount (201)
//	GET    /v1/social/accounts/:id       account detail                 -> socialAccount
//	PUT    /v1/social/accounts/:id       update an account              -> socialAccount
//	DELETE /v1/social/accounts/:id       disconnect an account
//	GET    /v1/social/posts              list posts (?status=)           -> {data:[…]}
//	POST   /v1/social/posts              create/schedule a post          -> socialPost (201)
//	GET    /v1/social/posts/:id          post detail                    -> socialPost
//	PUT    /v1/social/posts/:id          update a post                  -> socialPost
//	DELETE /v1/social/posts/:id          delete a post
//	POST   /v1/social/posts/:id/publish  publish a post now              -> socialPost
//
// serve.go auto-registers GET /v1/social/health (this subsystem does not set
// OwnsHealth, so the generic always-ok liveness route serves it).
package social

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/shorten"
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
)

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

// Mount wires the social surface onto app per HIP-0106. It keeps a package global
// (mounted) for Shutdown, so it constructs the Service value directly — the same
// "complex flavour" clients/crm uses.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("social.Use:  nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("social.Use:  empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("social.Use:  open store: %w", err)
	}
	// Reset any post left mid-publish by a previous process crash (→ failed, retryable)
	// BEFORE the scheduler starts, so a stuck claim never wedges a scheduled post.
	if n, err := store.RecoverStuckPublishing(context.Background(), time.Now().Unix()); err != nil {
		_ = store.Close()
		return fmt.Errorf("social.Use:  recover: %w", err)
	} else if n > 0 {
		luxlog.Default().Warn("social: reset interrupted publishes to failed", "count", n)
	}
	b := cloud.NewBase(deps, "social")
	s := &cloud.Service[state]{Base: b, State: state{store: store, pub: newPublisher(deps)}}
	mounted = s
	stopScheduler = startScheduler(s)

	routes(app, s)

	b.Log.Info("social mounted", "brand", deps.Brand)
	return nil
}

// routes registers the social surface: the account + post CRUD, the publish
// action, and the two read-only roll-ups. All thirteen are typed ops — see
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
	zip.Get(g, "/summary", o.summary)
	zip.Get(g, "/providers", o.providers)

	zip.Get(g, "/accounts", o.listAccounts)
	zip.Post(g, "/accounts", o.createAccount, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/accounts/:id", o.getAccount)
	zip.Put(g, "/accounts/:id", o.updateAccount)
	zip.Delete(g, "/accounts/:id", o.deleteAccount)

	zip.Get(g, "/posts", o.listPosts)
	zip.Post(g, "/posts", o.createPost, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/posts/:id", o.getPost)
	zip.Put(g, "/posts/:id", o.updatePost)
	zip.Delete(g, "/posts/:id", o.deletePost)
	zip.Post(g, "/posts/:id/publish", o.publish)
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
		if u = shorten.Trim(u, maxField); u == "" {
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
	switch err {
	case errNotFound:
		return zip.ErrNotFound(notFoundMsg)
	case errConflict:
		return zip.ErrConflict("already exists")
	default:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
}

// mapPublishErr maps a publishPost control error to an honest HTTP status: not-found →
// 404, provider-not-configured → 503 (with the missing-credentials detail), else 500.
// Each is a returned zip error, which is what the untyped handler returned too — so
// typing moved no refusal: zip renders the same RFC 9457 problem members it already
// did (zip problem.go).
func mapPublishErr(err error) error {
	switch {
	case errors.Is(err, errNotFound):
		return zip.ErrNotFound("post not found")
	case errors.Is(err, errProviderNotConfigured):
		return zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	default:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
}

// Shutdown stops the scheduler and closes the social store, in that order (drain the
// background publisher before releasing its store). Idempotent.
func Shutdown() error {
	stopScheduler()
	stopScheduler = func() {}
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
