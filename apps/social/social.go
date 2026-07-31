// Package social mounts the Hanzo Cloud /v1/social/* surface: a native-Go,
// per-org social-media store on Base/SQLite. It is the in-process fold of the live
// social stack (github.com/hanzoai/social — the social-backend / social-frontend /
// social-orchestrator pods, a Postiz-style scheduler) onto the ONE cloud framework
// (zip/Fiber + cloud.Deps + per-org SQLite) — the same shape every other in-repo
// subsystem uses (clients/crm is the twin, clients/marketing the sibling fold), NOT
// a proxy to the standalone social pods.
//
// Two entities, faithful to the live stack's Public API (see clients/content
// publish.go, which already talks to it): an Account is a connected channel (the
// stack's "integration": GET /public/v1/integrations), and a Post is content
// published or scheduled to a channel (POST /public/v1/posts {type:now|schedule,
// date, …}). Scheduling is not a third entity — it is a Post with Status=="scheduled"
// carrying a future ScheduleAt.
//
// The publish edge (publish.go) and the scheduler (scheduler.go) ARE folded: a post
// fans out to its channel's connected accounts through the Publisher seam, on an
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
//	POST   /v1/social/accounts           connect an account             -> Account (201)
//	GET    /v1/social/accounts/:id       account detail                 -> Account
//	PUT    /v1/social/accounts/:id       update an account              -> Account
//	DELETE /v1/social/accounts/:id       disconnect an account
//	GET    /v1/social/posts              list posts (?status=)           -> {data:[…]}
//	POST   /v1/social/posts              create/schedule a post          -> Post (201)
//	GET    /v1/social/posts/:id          post detail                    -> Post
//	PUT    /v1/social/posts/:id          update a post                  -> Post
//	DELETE /v1/social/posts/:id          delete a post
//	POST   /v1/social/posts/:id/publish  publish a post now              -> Post
//
// serve.go auto-registers GET /v1/social/health (this subsystem does not set
// OwnsHealth, so the generic always-ok liveness route serves it).
//
// EVERY ROUTE IS A TYPED OP (zip.Get/Post/Put/Delete with concrete In/Out structs),
// so the surface is ONE registry with N projections: REST, the OpenAPI document, the
// MCP tool list and the CLI all derive from these same registrations. Handler prose
// is lifted into the spec at build time by cmd/zipdoc.
package social

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
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

// providers is the validation set for both an Account's provider and a Post's target
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
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("social.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("social.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("social.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("social.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "social.db"))
	if err != nil {
		return fmt.Errorf("social.Mount: open store: %w", err)
	}
	// Reset any post left mid-publish by a previous process crash (→ failed, retryable)
	// BEFORE the scheduler starts, so a stuck claim never wedges a scheduled post.
	if n, err := store.RecoverStuckPublishing(context.Background(), time.Now().Unix()); err != nil {
		_ = store.Close()
		return fmt.Errorf("social.Mount: recover: %w", err)
	} else if n > 0 {
		deps.Logger.Warn("social: reset interrupted publishes to failed", "count", n)
	}
	b := cloud.NewBase(deps, "social")
	s := &cloud.Service[state]{Base: b, State: state{store: store, pub: newPublisher(deps)}}
	mounted = s
	stopScheduler = startScheduler(s)

	routes(app, s)

	b.Log.Info("social mounted", "brand", deps.Brand)
	return nil
}

// routes registers the social surface: the account + post CRUD + the summary roll-up.
// The bridge goes on FIRST — fiber runs middleware in registration order, so one
// installed after these leaves would never run — because a typed op is handed only a
// context and reads its validated org off the value the bridge parks there.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	app.Group("/v1/social").Use(cloud.Bridge())

	// A typed op takes the ABSOLUTE path — the registry keys on it — so every
	// registration below spells /v1/social in full.
	zip.Get(z, "/v1/social/summary", o.summary)
	zip.Get(z, "/v1/social/providers", o.listProviders)

	zip.Get(z, "/v1/social/accounts", o.listAccounts)
	zip.Post(z, "/v1/social/accounts", o.createAccount, zip.WithStatus(http.StatusCreated))
	zip.Get(z, "/v1/social/accounts/:id", o.getAccount)
	zip.Put(z, "/v1/social/accounts/:id", o.updateAccount)
	zip.Delete(z, "/v1/social/accounts/:id", o.deleteAccount)

	zip.Get(z, "/v1/social/posts", o.listPosts)
	zip.Post(z, "/v1/social/posts", o.createPost, zip.WithStatus(http.StatusCreated))
	zip.Get(z, "/v1/social/posts/:id", o.getPost)
	zip.Put(z, "/v1/social/posts/:id", o.updatePost)
	zip.Delete(z, "/v1/social/posts/:id", o.deletePost)
	zip.Post(z, "/v1/social/posts/:id/publish", o.publishPost)
}

// ---- shared helpers (mirror clients/crm + clients/marketing) ----

// ops binds the service to social's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, the only bound form
// cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// tenant resolves the org — the tenant-isolation KEY — EXACTLY as SanitizeIdentity
// minted it from the validated IAM owner claim (HIP-0026), carried across the typed
// seam by cloud.Bridge: never lowercased, stripped, truncated, or read from the input.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// ---- wire types ----

// Ref addresses one account or post by id.
type Ref struct {
	// ID is the record id from the path, as returned by create.
	ID string `json:"id"`
}

// AccountPage is the bound + filter an account list accepts.
type AccountPage struct {
	// Provider narrows to one network (x, facebook, instagram, linkedin, tiktok,
	// youtube, threads); empty means every connected account.
	Provider string `json:"provider"`
	// Limit caps the rows returned; 0 means 200 and nothing above 1000 is honoured.
	Limit int `json:"limit"`
}

// PostPage is the bound + filter a post list accepts.
type PostPage struct {
	// Status narrows to one lifecycle state (draft, scheduled, published, failed);
	// empty means every post.
	Status string `json:"status"`
	// Limit caps the rows returned; 0 means 200 and nothing above 1000 is honoured.
	Limit int `json:"limit"`
}

// AccountList is a page of connected accounts.
type AccountList struct {
	// Data is the page; an empty array when the org has connected no account.
	Data []Account `json:"data"`
}

// PostList is a page of posts.
type PostList struct {
	// Data is the page; an empty array when the org has written no post.
	Data []Post `json:"data"`
}

// ProviderList is each network's publish-readiness in this deployment.
type ProviderList struct {
	// Data is one row per supported network, in the fixed provider order.
	Data []ProviderCapability `json:"data"`
}

// Summary is the org's social roll-up.
type Summary struct {
	// Posts is how many posts the org has, in any state.
	Posts int `json:"posts"`
	// Scheduled is how many are waiting for their send time.
	Scheduled int `json:"scheduled"`
	// Published is how many have gone out.
	Published int `json:"published"`
	// Accounts is how many channels are connected.
	Accounts int `json:"accounts"`
}

// limitOf bounds a caller's page size: absent, unparseable or non-positive means
// defaultLimit, and nothing above maxLimit is honoured.
func limitOf(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// clip trims and bounds a short text field to maxField.
func clip(s string) string { return clipN(s, maxField) }

// clipBody trims and bounds a post body to maxContent.
func clipBody(s string) string { return clipN(s, maxContent) }

func clipN(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
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
		if u = clip(u); u == "" {
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

// ---- accounts ----

// createAccount records a connected social channel for the caller's org. Provider
// defaults to x and must be one this deployment knows; status defaults to connected.
// The id and timestamps of the input are ignored — the server assigns them. No OAuth
// is performed here: this stores the channel, it does not authorize it.
//
// Example: {"provider": "linkedin", "handle": "@hanzoai", "status": "connected"}
func (o ops) createAccount(ctx context.Context, in *Account) (*Account, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	body := *in
	provider, okPr := normProvider(body.Provider)
	if !okPr {
		return nil, zip.ErrBadRequest("provider must be one of x, facebook, instagram, linkedin, tiktok, youtube, threads")
	}
	status, okSt := normAccountStatus(body.Status)
	if !okSt {
		return nil, zip.ErrBadRequest("status must be one of connected, disconnected, error")
	}
	id, err := genID("acct")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	acct := Account{
		ID: id, Org: org, Provider: provider, Handle: clip(body.Handle), Status: status,
		CreatedAt: now, UpdatedAt: now,
	}
	saved, err := s.State.store.CreateAccount(ctx, acct)
	if err != nil {
		return nil, mapErr(err, "")
	}
	return &saved, nil
}

// listAccounts returns the caller org's connected channels, optionally narrowed to
// one network.
//
// Example: {"provider": "linkedin", "limit": 50}
func (o ops) listAccounts(ctx context.Context, in *AccountPage) (*AccountList, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	rows, err := s.State.store.ListAccounts(ctx, org, provider, limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &AccountList{Data: rows}, nil
}

// getAccount returns one of the caller org's connected channels. An account in
// another org reads as not found. The provider token is never serialized.
//
// Example: {"id": "acct_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) getAccount(ctx context.Context, in *Ref) (*Account, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	acct, err := s.State.store.GetAccount(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "account not found")
	}
	return &acct, nil
}

// updateAccount replaces a connected channel's provider, handle and status. Every
// field is rewritten from the input, so send the whole record. The stored provider
// token is untouched.
//
// Example: {"id": "acct_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60", "provider": "linkedin", "handle": "@hanzoai", "status": "disconnected"}
func (o ops) updateAccount(ctx context.Context, in *Account) (*Account, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	body := *in
	provider, okPr := normProvider(body.Provider)
	if !okPr {
		return nil, zip.ErrBadRequest("provider must be one of x, facebook, instagram, linkedin, tiktok, youtube, threads")
	}
	status, okSt := normAccountStatus(body.Status)
	if !okSt {
		return nil, zip.ErrBadRequest("status must be one of connected, disconnected, error")
	}
	acct := Account{
		ID: strings.TrimSpace(in.ID), Org: org, Provider: provider, Handle: clip(body.Handle), Status: status,
		UpdatedAt: time.Now().Unix(),
	}
	saved, err := s.State.store.UpdateAccount(ctx, acct)
	if err != nil {
		return nil, mapErr(err, "account not found")
	}
	return &saved, nil
}

// deleteAccount disconnects a channel and answers 204. Posts already published
// through it are left as they are.
//
// Example: {"id": "acct_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) deleteAccount(ctx context.Context, in *Ref) (*struct{}, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := s.State.store.DeleteAccount(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("account not found")
	}
	return nil, nil
}

// ---- posts ----

// createPost writes a post for the caller's org. Content is required; channel
// defaults to x and status to draft. A post created as scheduled for now or earlier
// publishes immediately and comes back carrying that outcome; one scheduled for the
// future is left to the scheduler. A failed publish never fails the create — the post
// exists either way.
//
// Example: {"content": "We shipped it.", "channel": "linkedin", "status": "scheduled", "scheduleAt": 1780000000, "media": ["https://cdn.example.com/a.png"]}
func (o ops) createPost(ctx context.Context, in *Post) (*Post, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	body := *in
	content := clipBody(body.Content)
	if content == "" {
		return nil, zip.ErrBadRequest("content is required")
	}
	channel, okCh := normProvider(body.Channel)
	if !okCh {
		return nil, zip.ErrBadRequest("channel must be one of x, facebook, instagram, linkedin, tiktok, youtube, threads")
	}
	status, okSt := normPostStatus(body.Status)
	if !okSt {
		return nil, zip.ErrBadRequest("status must be one of draft, scheduled, published, failed")
	}
	id, err := genID("post")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	post := Post{
		ID: id, Org: org, Content: content, Channel: channel, Status: status,
		ScheduleAt: nonNeg(body.ScheduleAt), Media: normMedia(body.Media), CreatedAt: now, UpdatedAt: now,
	}
	saved, err := s.State.store.CreatePost(ctx, post)
	if err != nil {
		return nil, mapErr(err, "")
	}
	// On-create fanout: a post scheduled for now-or-earlier publishes immediately
	// (best effort — the post is already stored; the publish outcome, published or
	// failed, is recorded on it and returned). A future-scheduled post is left for the
	// scheduler. A publish NEVER fails the 201: the post exists regardless. Only the
	// two outcome-bearing results (published, or a fail-closed not-configured) update
	// the returned record; an infra error leaves it 'scheduled' for the scheduler.
	if saved.Status == statusScheduled && saved.ScheduleAt <= now {
		if updated, perr := publishPost(ctx, s, org, saved.ID); perr == nil || errors.Is(perr, errProviderNotConfigured) {
			saved = updated
		}
	}
	return &saved, nil
}

// listPosts returns the caller org's posts, optionally narrowed to one lifecycle
// state.
//
// Example: {"status": "scheduled", "limit": 50}
func (o ops) listPosts(ctx context.Context, in *PostPage) (*PostList, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	rows, err := s.State.store.ListPosts(ctx, org, status, limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &PostList{Data: rows}, nil
}

// getPost returns one of the caller org's posts, including its publish outcome. A
// post in another org reads as not found.
//
// Example: {"id": "post_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) getPost(ctx context.Context, in *Ref) (*Post, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	post, err := s.State.store.GetPost(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "post not found")
	}
	return &post, nil
}

// updatePost replaces a post's content, channel, status, schedule and media. Content
// is required and every field is rewritten from the input, so send the whole record.
// The publish results (account, external id, error) are server-owned and untouched.
//
// Example: {"id": "post_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60", "content": "We shipped it.", "channel": "linkedin", "status": "draft"}
func (o ops) updatePost(ctx context.Context, in *Post) (*Post, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	body := *in
	content := clipBody(body.Content)
	if content == "" {
		return nil, zip.ErrBadRequest("content is required")
	}
	channel, okCh := normProvider(body.Channel)
	if !okCh {
		return nil, zip.ErrBadRequest("channel must be one of x, facebook, instagram, linkedin, tiktok, youtube, threads")
	}
	status, okSt := normPostStatus(body.Status)
	if !okSt {
		return nil, zip.ErrBadRequest("status must be one of draft, scheduled, published, failed")
	}
	post := Post{
		ID: strings.TrimSpace(in.ID), Org: org, Content: content, Channel: channel, Status: status,
		ScheduleAt: nonNeg(body.ScheduleAt), Media: normMedia(body.Media), UpdatedAt: time.Now().Unix(),
	}
	saved, err := s.State.store.UpdatePost(ctx, post)
	if err != nil {
		return nil, mapErr(err, "post not found")
	}
	return &saved, nil
}

// deletePost removes a post and answers 204. A post already published is deleted
// from the record only — nothing is retracted from the network it went out on.
//
// Example: {"id": "post_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) deletePost(ctx context.Context, in *Ref) (*struct{}, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := s.State.store.DeletePost(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("post not found")
	}
	return nil, nil
}

// ---- publish ----

// publishPost pushes a post to its channel's connected accounts now — the explicit
// publish, twin of the on-create fanout. Idempotent: an already-published post comes
// back unchanged. A push the deployment cannot make answers 503 naming exactly which
// credentials are missing; a provider that rejects the push is recorded on the post as
// failed and returned, never reported as a server fault.
//
// Example: {"id": "post_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) publishPost(ctx context.Context, in *Ref) (*Post, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	post, err := publishPost(ctx, s, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapPublishErr(err)
	}
	return &post, nil
}

// listProviders reports each network's publish-readiness: whether this deployment
// holds that network's OAuth-app credentials and, when it does not, exactly which
// environment variables are missing. Read live from the environment, never fabricated.
//
// Response: {"data": [{"provider": "x", "credentialsConfigured": false, "missingCredentials": ["X_CLIENT_ID", "X_CLIENT_SECRET"]}]}
func (o ops) listProviders(ctx context.Context, _ *struct{}) (*ProviderList, error) {
	if _, err := tenant(ctx); err != nil {
		return nil, err
	}
	return &ProviderList{Data: providerCapabilities()}, nil
}

// mapPublishErr maps a publishPost control error to an honest HTTP status: not-found →
// 404, provider-not-configured → 503 (with the missing-credentials detail), else 500.
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

// ---- summary ----

// summary reports the caller org's post counts by state and how many channels are
// connected.
//
// Response: {"posts": 42, "scheduled": 5, "published": 30, "accounts": 3}
func (o ops) summary(ctx context.Context, _ *struct{}) (*Summary, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	posts, scheduled, published, accounts, err := s.State.store.Counts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return &Summary{Posts: posts, Scheduled: scheduled, Published: published, Accounts: accounts}, nil
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
