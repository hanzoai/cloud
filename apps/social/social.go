// Package social is posting to every social account you own, now or on a schedule.
//
// An org's connected channels (X, Facebook, Instagram, LinkedIn, TikTok,
// YouTube, Threads) and the posts it publishes or schedules to them.
//
// Two entities. An Account is a connected channel (the hanzoai/social stack's
// "integration": GET /public/v1/integrations), and a Post is content published or
// scheduled to a channel (POST /public/v1/posts {type:now|schedule, date, …}).
// Scheduling is not a third entity — it is a Post with Status=="scheduled"
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
package social

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/hanzoai/cloud/openapi"
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
	store, err := openStore(deps.DataDir)
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

// scoped is the tenancy sentence all thirteen operations share. Each is read
// alone in the document, so the boundary has to be stated on each one rather than
// once in a package comment no consumer of the spec ever sees.
const scoped = "\n\nA validated principal is required; 403 without one. Every row is keyed by the " +
	"caller's org taken from that principal and never from the request, so an id belonging " +
	"to another tenant reads as not found rather than as a refusal."

// The prose for this subsystem's thirteen operations. None is a typed op — each
// answers a value assembled in its handler — so there is no doc comment for
// zipdoc to lift and the prose is declared beside the route table instead.
func init() {
	openapi.Describe("/v1/social/summary", http.MethodGet,
		"Counts across your org's social presence",
		"Returns four counts for the caller's org: total posts, how many are scheduled, how "+
			"many have published, and how many accounts are connected. It is the dashboard "+
			"roll-up, computed over the org's own rows in one read."+scoped)
	openapi.Describe("/v1/social/providers", http.MethodGet,
		"Which networks this deployment can actually publish to",
		"Reports each supported network's publish-readiness: whether this deployment holds "+
			"the OAuth application credentials for it and, when it does not, exactly which "+
			"environment variables are missing.\n\n"+
			"This is a live read of the deployment's own configuration, not a static list of "+
			"networks — it answers \"can I connect this today\", which is what a connect "+
			"affordance and a pre-cutover checklist both need. It says nothing about whether "+
			"the caller has connected an account; that is the accounts listing."+scoped)
	openapi.Describe("/v1/social/accounts", http.MethodGet,
		"List the social accounts connected to your org",
		"Returns the org's connected accounts — each one's id, network, handle, status and "+
			"timestamps. `provider` filters to one network; `limit` bounds the page, "+
			"defaulting to 200 and capped at 1000.\n\n"+
			"An account's provider access token is NEVER included in any response on this "+
			"surface. Only the publisher reads it."+scoped)
	openapi.Describe("/v1/social/accounts", http.MethodPost,
		"Connect a social account to your org",
		"Records a social account for the org and answers 201 with the stored row, "+
			"including the generated id later calls address it by.\n\n"+
			"`provider` must be one of x, facebook, instagram, linkedin, tiktok, youtube or "+
			"threads, defaulting to x when omitted. `status` is one of connected, "+
			"disconnected or error, defaulting to connected. The handle is trimmed and "+
			"bounded at 1024 characters."+scoped)
	openapi.Describe("/v1/social/accounts/:id", http.MethodGet,
		"Read one connected account",
		"Returns one of the org's connected accounts by id — its network, handle, status "+
			"and timestamps — or 404. The provider access token is not part of the "+
			"response."+scoped)
	openapi.Describe("/v1/social/accounts/:id", http.MethodPut,
		"Replace one connected account",
		"Replaces the account's network, handle and status with what the body carries, and "+
			"answers with the stored row.\n\n"+
			"This is a REPLACEMENT, not a merge, which is the rule most easily got wrong: a "+
			"field the body omits is written as its default, so leaving out the handle "+
			"blanks it and leaving out the status resets it to connected. Send the whole "+
			"record. The same vocabularies as create apply, and an unknown network or status "+
			"is refused rather than coerced."+scoped)
	openapi.Describe("/v1/social/accounts/:id", http.MethodDelete,
		"Disconnect one account",
		"Removes one connected account from the org and answers 204 with no body; an id "+
			"that is not there is 404.\n\n"+
			"It removes the account record only. Posts that already published through it "+
			"keep their published state and their recorded external ids — this does not "+
			"retract anything from the network."+scoped)
	openapi.Describe("/v1/social/posts", http.MethodGet,
		"List your org's posts",
		"Returns the org's posts — content, channel, status, scheduled time, media and "+
			"timestamps. `status` filters to one of draft, scheduled, published or failed; "+
			"`limit` bounds the page, defaulting to 200 and capped at 1000."+scoped)
	openapi.Describe("/v1/social/posts", http.MethodPost,
		"Create a post, and publish it if it is already due",
		"Stores a post for the org and answers 201 with the stored row.\n\n"+
			"A post created as scheduled for a time that has already passed is published "+
			"IMMEDIATELY, and the row returned carries that outcome — this is the one "+
			"behaviour a reader would otherwise miss. A future-scheduled post is left for "+
			"the scheduler, and a draft is left alone. Publishing never fails the creation: "+
			"the post is stored either way, and a publish that could not run leaves the row "+
			"for the scheduler to retry.\n\n"+
			"`content` is required and bounded at 8192 characters; `channel` is one of the "+
			"seven supported networks, defaulting to x; `status` is one of draft, scheduled, "+
			"published or failed, defaulting to draft; up to 10 media URLs are kept, each "+
			"bounded at 1024 characters."+scoped)
	openapi.Describe("/v1/social/posts/:id", http.MethodGet,
		"Read one post",
		"Returns one of the org's posts by id, with its current status, scheduled time, "+
			"media and — once it has published — the account and external id it published "+
			"under. 404 when there is no such post for this org."+scoped)
	openapi.Describe("/v1/social/posts/:id", http.MethodPut,
		"Replace one post",
		"Replaces the post's content, channel, status, scheduled time and media with what "+
			"the body carries, and answers with the stored row.\n\n"+
			"A REPLACEMENT, not a merge: an omitted field is written as its default, so "+
			"omitting media clears it and omitting the status resets the post to draft. "+
			"`content` is required on every update. Unlike create, this never triggers a "+
			"publish — moving a post's scheduled time into the past here leaves it for the "+
			"scheduler; publish now is its own operation."+scoped)
	openapi.Describe("/v1/social/posts/:id", http.MethodDelete,
		"Delete one post",
		"Removes one post from the org and answers 204 with no body; an id that is not "+
			"there is 404.\n\n"+
			"It deletes the record here only. A post that has already published is not "+
			"retracted from the network by deleting it."+scoped)
	openapi.Describe("/v1/social/posts/:id/publish", http.MethodPost,
		"Publish one post now",
		"Publishes the post immediately to the connected accounts on its channel and "+
			"answers with the updated row, carrying the account and external id it "+
			"published under.\n\n"+
			"It is IDEMPOTENT: a post that has already published, or that another caller is "+
			"publishing right now, comes back unchanged rather than being posted twice. "+
			"That claim is taken before any network call, which is what makes a double "+
			"submit safe.\n\n"+
			"The two failure shapes differ on purpose. Having no connected account for the "+
			"channel is the caller's to fix, so it is recorded ON the post as failed with "+
			"the reason and answers normally. A deployment that lacks the network's own "+
			"credentials cannot publish for anyone, so that is a 503 naming exactly what is "+
			"missing."+scoped)
}

// routes registers the social surface: the account + post CRUD + the summary roll-up.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/social")
	g.Get("/summary", cloud.Handle(s, summary))
	g.Get("/providers", cloud.Handle(s, listProviders))

	g.Get("/accounts", cloud.Handle(s, listAccounts))
	g.Post("/accounts", cloud.Handle(s, createAccount))
	g.Get("/accounts/:id", cloud.Handle(s, getAccount))
	g.Put("/accounts/:id", cloud.Handle(s, updateAccount))
	g.Delete("/accounts/:id", cloud.Handle(s, deleteAccount))

	g.Get("/posts", cloud.Handle(s, listPosts))
	g.Post("/posts", cloud.Handle(s, createPost))
	g.Get("/posts/:id", cloud.Handle(s, getPost))
	g.Put("/posts/:id", cloud.Handle(s, updatePost))
	g.Delete("/posts/:id", cloud.Handle(s, deletePost))
	g.Post("/posts/:id/publish", cloud.Handle(s, publishPostHandler))
}

// ---- shared helpers (mirror clients/crm + clients/marketing) ----

// tenant resolves the org — the tenant-isolation KEY — for a request. It uses
// principal.Org EXACTLY as SanitizeIdentity minted it from the validated IAM owner
// claim (HIP-0026): never lowercased, stripped, or truncated.
func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

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

func limitOf(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
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

func createAccount(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	var body Account
	if err := c.Bind(&body); err != nil {
		return err
	}
	provider, okPr := normProvider(body.Provider)
	if !okPr {
		return zip.ErrBadRequest("provider must be one of x, facebook, instagram, linkedin, tiktok, youtube, threads")
	}
	status, okSt := normAccountStatus(body.Status)
	if !okSt {
		return zip.ErrBadRequest("status must be one of connected, disconnected, error")
	}
	id := mint.ID("acct")
	now := time.Now().Unix()
	acct := Account{
		ID: id, Org: org, Provider: provider, Handle: clip(body.Handle), Status: status,
		CreatedAt: now, UpdatedAt: now,
	}
	saved, err := s.State.store.CreateAccount(c.Context(), acct)
	if err != nil {
		return mapErr(err, "")
	}
	return c.JSON(http.StatusCreated, saved)
}

func listAccounts(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	rows, err := s.State.store.ListAccounts(c.Context(), org, provider, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func getAccount(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	acct, err := s.State.store.GetAccount(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "account not found")
	}
	return c.JSON(http.StatusOK, acct)
}

func updateAccount(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	var body Account
	if err := c.Bind(&body); err != nil {
		return err
	}
	provider, okPr := normProvider(body.Provider)
	if !okPr {
		return zip.ErrBadRequest("provider must be one of x, facebook, instagram, linkedin, tiktok, youtube, threads")
	}
	status, okSt := normAccountStatus(body.Status)
	if !okSt {
		return zip.ErrBadRequest("status must be one of connected, disconnected, error")
	}
	acct := Account{
		ID: idParam(c), Org: org, Provider: provider, Handle: clip(body.Handle), Status: status,
		UpdatedAt: time.Now().Unix(),
	}
	saved, err := s.State.store.UpdateAccount(c.Context(), acct)
	if err != nil {
		return mapErr(err, "account not found")
	}
	return c.JSON(http.StatusOK, saved)
}

func deleteAccount(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	deleted, err := s.State.store.DeleteAccount(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("account not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- posts ----

func createPost(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	var body Post
	if err := c.Bind(&body); err != nil {
		return err
	}
	content := clipBody(body.Content)
	if content == "" {
		return zip.ErrBadRequest("content is required")
	}
	channel, okCh := normProvider(body.Channel)
	if !okCh {
		return zip.ErrBadRequest("channel must be one of x, facebook, instagram, linkedin, tiktok, youtube, threads")
	}
	status, okSt := normPostStatus(body.Status)
	if !okSt {
		return zip.ErrBadRequest("status must be one of draft, scheduled, published, failed")
	}
	id := mint.ID("post")
	now := time.Now().Unix()
	post := Post{
		ID: id, Org: org, Content: content, Channel: channel, Status: status,
		ScheduleAt: nonNeg(body.ScheduleAt), Media: normMedia(body.Media), CreatedAt: now, UpdatedAt: now,
	}
	saved, err := s.State.store.CreatePost(c.Context(), post)
	if err != nil {
		return mapErr(err, "")
	}
	// On-create fanout: a post scheduled for now-or-earlier publishes immediately
	// (best effort — the post is already stored; the publish outcome, published or
	// failed, is recorded on it and returned). A future-scheduled post is left for the
	// scheduler. A publish NEVER fails the 201: the post exists regardless. Only the
	// two outcome-bearing results (published, or a fail-closed not-configured) update
	// the returned record; an infra error leaves it 'scheduled' for the scheduler.
	if saved.Status == statusScheduled && saved.ScheduleAt <= now {
		if updated, perr := publishPost(c.Context(), s, org, saved.ID); perr == nil || errors.Is(perr, errProviderNotConfigured) {
			saved = updated
		}
	}
	return c.JSON(http.StatusCreated, saved)
}

func listPosts(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	rows, err := s.State.store.ListPosts(c.Context(), org, status, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func getPost(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	post, err := s.State.store.GetPost(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "post not found")
	}
	return c.JSON(http.StatusOK, post)
}

func updatePost(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	var body Post
	if err := c.Bind(&body); err != nil {
		return err
	}
	content := clipBody(body.Content)
	if content == "" {
		return zip.ErrBadRequest("content is required")
	}
	channel, okCh := normProvider(body.Channel)
	if !okCh {
		return zip.ErrBadRequest("channel must be one of x, facebook, instagram, linkedin, tiktok, youtube, threads")
	}
	status, okSt := normPostStatus(body.Status)
	if !okSt {
		return zip.ErrBadRequest("status must be one of draft, scheduled, published, failed")
	}
	post := Post{
		ID: idParam(c), Org: org, Content: content, Channel: channel, Status: status,
		ScheduleAt: nonNeg(body.ScheduleAt), Media: normMedia(body.Media), UpdatedAt: time.Now().Unix(),
	}
	saved, err := s.State.store.UpdatePost(c.Context(), post)
	if err != nil {
		return mapErr(err, "post not found")
	}
	return c.JSON(http.StatusOK, saved)
}

func deletePost(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	deleted, err := s.State.store.DeletePost(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("post not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- publish ----

// publishPostHandler publishes a post NOW to its channel's connected accounts (the
// explicit publish action, the twin of the on-create fanout). Idempotent: re-publishing
// an already-published post returns it unchanged. 404 if the post is not the org's; 503
// (with the exact missing credentials) if the deployment cannot publish the provider.
func publishPostHandler(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	post, err := publishPost(c.Context(), s, org, idParam(c))
	if err != nil {
		return mapPublishErr(err)
	}
	return c.JSON(http.StatusOK, post)
}

// listProviders reports each network's publish-readiness: whether this deployment has
// its OAuth-app credentials and, if not, exactly which env vars are missing. Honest and
// live (reads the environment), never fabricated — the console's connect affordance and
// the coordinator's pre-cutover checklist of what to supply.
func listProviders(_ *cloud.Service[state], c *zip.Ctx) error {
	if _, ok := tenant(c); !ok {
		return principal.Refused(c)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": providerCapabilities()})
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

func summary(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	posts, scheduled, published, accounts, err := s.State.store.Counts(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"posts": posts, "scheduled": scheduled, "published": published, "accounts": accounts,
	})
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
