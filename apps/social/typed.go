package social

// typed.go is social's TYPED plane — the ops that carry In/Out types, and so the
// only social routes that reach the published document, the MCP tool list, the CLI
// and the generated SDKs. An untyped route contributes a path and a method and
// nothing else, so before this file every one of these thirteen operations
// published an operationId and no schema at all: an SDK offered `post_v1_social_posts`
// with nowhere to put the post, and an agent asking the fleet MCP server what it
// could do was never told an org's channels could be published to.
//
// WHAT IS TYPED: all thirteen. Nothing in this surface is wire-bound — every
// operation answers a value its handler assembled, which is what HIP-1153 §"The
// address" already said ("nothing about these shapes prevents typing — they are
// values"). untypedByDesign in typed_wire_test.go is therefore EMPTY, and the gate
// there requires it to stay that way or to gain a reason.
//
// THE SHAPES ARE THE STORE'S OWN ROWS. socialAccount and socialPost are the types
// the store scans into, published directly rather than copied into a parallel view:
// a second copy is a second thing to keep true, and the drift it invites is silent
// (a column added to the row, absent from the view, absent from every SDK). What
// the typed plane adds is the REQUEST half, which the rows cannot state — a create
// does not accept `id`, `createdAt` or `updatedAt`, because the server mints all
// three, so publishing the row as the request body would document three arguments
// no caller may send.
//
// FIELD ORDER IS LOAD-BEARING, TWICE. The list envelopes and the summary were
// map[string]any literals, and encoding/json writes a map in SORTED key order while
// a struct writes DECLARATION order. socialSummary's four fields are therefore
// declared alphabetically — accounts, posts, published, scheduled — so the typed
// answer is BYTE-identical to the map it replaces rather than merely equal as JSON.
// TestSummaryBytesDidNotMove asserts the bytes, which is what keeps the reason alive
// for the next reader who thinks the order is cosmetic.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each op below, and off every field of every
// In and Out, into zipdoc_gen.go — the ONLY way that prose reaches the published
// document, the MCP tool description a model reads to pick the tool, and the CLI's
// help, because Go drops comments at compile time. Run by `make -C apps/social
// describe` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the service to social's typed ops. A TypedHandler takes no service
// parameter, so the service arrives as a RECEIVER and every op is a method value —
// also the only bound form cmd/zipdoc can lift prose from, since a closure returned
// by a helper is a call expression with no doc comment to read.
type ops struct{ s *cloud.Service[state] }

// ---- the shapes the ops take ----

// rowRef addresses one stored row. The id is the path segment, and the URL is the
// addressing authority — it binds from there whatever else the request carries.
type rowRef struct {
	// ID is the account or post to act on, taken from the path.
	ID string `json:"id"`
}

// accountFilter narrows the account listing. Both fields are STRINGS carrying the
// caller's raw token, because the route's own parse is more forgiving than zip's:
// see limitOf.
type accountFilter struct {
	// Provider keeps only accounts on one network — x, facebook, instagram,
	// linkedin, tiktok, youtube or threads. Omit it for every network. It is
	// lower-cased and trimmed before it is matched, and a value that names no
	// network simply matches nothing rather than being refused.
	Provider string `json:"provider,omitempty"`
	// Limit bounds the page, defaulting to 200 and capped at 1000. It is a string
	// rather than an integer on purpose: the route parses it with a leading trim
	// and falls back to the default on anything it cannot read, so `?limit=%2050`
	// is a page of fifty today. An integer field would refuse the space and read
	// an unparseable value as zero, which is a different page.
	Limit string `json:"limit,omitempty"`
}

// postFilter narrows the post listing. Both fields are strings for the reason
// accountFilter's are.
type postFilter struct {
	// Status keeps only posts in one state — draft, scheduled, published or
	// failed. Omit it for every state. The transient publishing claim is not a
	// user-visible state and matching it is not useful.
	Status string `json:"status,omitempty"`
	// Limit bounds the page, defaulting to 200 and capped at 1000. A string for
	// the same reason accountFilter.Limit is.
	Limit string `json:"limit,omitempty"`
}

// socialAccountBody is what a caller may say about an account. It is deliberately NOT the
// stored row: id, createdAt and updatedAt are minted by the server, so publishing
// the row here would document three arguments no caller may send, and the account's
// provider access token is never accepted or returned on this surface at all.
//
// Every field carries `url:"-"`. zip binds the query OVER a decoded body
// (typed.go:507 binds query, :508 binds path, both after the body decode at :488),
// and a body method publishes no query parameters — so without the opt-out each
// field below would gain an undeclared, higher-authority `?field=` twin this route
// has never read.
type socialAccountBody struct {
	// Handle is the account's public name on the network, as the customer knows it
	// (`@acme`). Trimmed and bounded at 1024 characters.
	//
	// Example: "@acme"
	Handle string `json:"handle,omitempty" url:"-"`
	// Provider is the network this account is on: x, facebook, instagram, linkedin,
	// tiktok, youtube or threads. Omitted means x. Anything else is refused rather
	// than coerced, because a stored account on a network that does not exist can
	// never be published through.
	//
	// Example: "x"
	Provider string `json:"provider,omitempty" url:"-"`
	// Status is the connection lifecycle: connected, disconnected or error.
	// Omitted means connected. Only a connected account is a publish target.
	Status string `json:"status,omitempty" url:"-"`
}

// socialAccountWrite replaces one account: the id from the path plus the whole record.
//
// ID carries `json:"-"` so the published request body does NOT offer it and the
// decoder cannot read one — a body naming another account is invisible here, and
// `url:"id"` is what still binds the path segment (openapi.go:884 urlFieldName
// prefers the url tag; openapi.go:720 skips a `json:"-"` field when building the
// body schema).
type socialAccountWrite struct {
	// ID is the account to replace, taken from the path.
	ID string `json:"-" url:"id"`
	// Handle is the account's public name on the network. Omitting it BLANKS the
	// stored handle: this is a replacement, not a merge.
	//
	// Example: "@acme"
	Handle string `json:"handle,omitempty" url:"-"`
	// Provider is the network this account is on: x, facebook, instagram, linkedin,
	// tiktok, youtube or threads. Omitted means x.
	//
	// Example: "x"
	Provider string `json:"provider,omitempty" url:"-"`
	// Status is the connection lifecycle: connected, disconnected or error.
	// Omitting it RESETS the account to connected.
	Status string `json:"status,omitempty" url:"-"`
}

// socialPostBody is what a caller may say about a post. As with socialAccountBody it is not the
// stored row: id and the timestamps are the server's, and so are accountId,
// externalId and error, which only a publish attempt may write. Every field carries
// `url:"-"` for the query-outranks-body reason socialAccountBody states.
type socialPostBody struct {
	// Channel is the network to publish to: x, facebook, instagram, linkedin,
	// tiktok, youtube or threads. Omitted means x.
	//
	// Example: "x"
	Channel string `json:"channel,omitempty" url:"-"`
	// Content is the post's text. Required — an empty body is a 400 — and bounded
	// at 8192 characters, comfortably above every network's own limit.
	//
	// Example: "Shipping today."
	Content string `json:"content" url:"-"`
	// Media is the post's attached media as URLs, at most 10, each bounded at 1024
	// characters. Blank entries are dropped. Omitting it CLEARS any stored media.
	Media []string `json:"media,omitempty" url:"-"`
	// ScheduleAt is when to publish, as a unix timestamp in SECONDS. 0 means
	// unscheduled. A negative value is clamped to 0. It only matters for a post
	// whose status is scheduled.
	ScheduleAt int64 `json:"scheduleAt,omitempty" url:"-"`
	// Status is the post's lifecycle state: draft, scheduled, published or failed.
	// Omitted means draft. The transient publishing claim is never settable here —
	// accepting it from a request would let a caller wedge or replay the guard that
	// stops two publishers double-posting the same row.
	Status string `json:"status,omitempty" url:"-"`
}

// socialPostWrite replaces one post: the id from the path plus the whole record. ID is
// `json:"-" url:"id"` for the reason socialAccountWrite.ID is.
type socialPostWrite struct {
	// ID is the post to replace, taken from the path.
	ID string `json:"-" url:"id"`
	// Channel is the network to publish to: x, facebook, instagram, linkedin,
	// tiktok, youtube or threads. Omitted means x.
	//
	// Example: "x"
	Channel string `json:"channel,omitempty" url:"-"`
	// Content is the post's text. Required on every update, and bounded at 8192
	// characters.
	//
	// Example: "Shipping today."
	Content string `json:"content" url:"-"`
	// Media is the post's attached media as URLs, at most 10. Omitting it CLEARS
	// any stored media: this is a replacement, not a merge.
	Media []string `json:"media,omitempty" url:"-"`
	// ScheduleAt is when to publish, as a unix timestamp in SECONDS. 0 means
	// unscheduled. Moving it into the past here does NOT publish the post — that is
	// the scheduler's to notice, or the publish operation's.
	ScheduleAt int64 `json:"scheduleAt,omitempty" url:"-"`
	// Status is the post's lifecycle state: draft, scheduled, published or failed.
	// Omitting it RESETS the post to draft.
	Status string `json:"status,omitempty" url:"-"`
}

// ---- the shapes the ops give ----

// socialAccounts is a page of the caller org's connected accounts.
type socialAccounts struct {
	// Data is the accounts, most-recently-updated first, bounded by the limit.
	Data []socialAccount `json:"data"`
}

// socialPosts is a page of the caller org's posts.
type socialPosts struct {
	// Data is the posts, most-recently-updated first, bounded by the limit.
	Data []socialPost `json:"data"`
}

// socialProviders is every supported network's publish-readiness on THIS
// deployment.
type socialProviders struct {
	// Data is one row per supported network, in the product's fixed order: x,
	// facebook, instagram, linkedin, tiktok, youtube, threads.
	Data []socialProvider `json:"data"`
}

// socialSummary is the per-org roll-up behind the module's overview cards.
//
// The fields are declared ALPHABETICALLY because this answer was a map[string]any
// and encoding/json writes a map in sorted key order, while a struct writes
// declaration order — so alphabetical is what keeps the bytes identical to the map
// this replaced. TestSummaryBytesDidNotMove holds it.
type socialSummary struct {
	// Accounts is how many accounts the org has connected, in any status.
	Accounts int `json:"accounts"`
	// Posts is how many posts the org has, in any state.
	Posts int `json:"posts"`
	// Published is how many of them have published.
	Published int `json:"published"`
	// Scheduled is how many of them are waiting for their scheduled time.
	Scheduled int `json:"scheduled"`
}

// ---- summary + providers ----

// summary returns four counts for the caller's org: total posts, how many are
// scheduled, how many have published, and how many accounts are connected. It is
// the dashboard roll-up, computed over the org's own rows in one read.
func (o ops) summary(ctx context.Context, _ *cloud.Unit) (*socialSummary, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	posts, scheduled, published, accounts, err := o.s.State.store.Counts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return &socialSummary{Accounts: accounts, Posts: posts, Published: published, Scheduled: scheduled}, nil
}

// providers reports each supported network's publish-readiness: whether this
// deployment holds the OAuth application credentials for it and, when it does not,
// exactly which environment variables are missing.
//
// This is a live read of the deployment's own configuration, not a static list of
// networks — it answers "can I connect this today", which is what a connect
// affordance and a pre-cutover checklist both need. It says nothing about whether
// the caller has connected an account; that is the accounts listing.
func (o ops) providers(ctx context.Context, _ *cloud.Unit) (*socialProviders, error) {
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	return &socialProviders{Data: providerCapabilities()}, nil
}

// ---- accounts ----

// listAccounts returns the org's connected accounts — each one's id, network,
// handle, status and timestamps, most-recently-updated first.
//
// An account's provider access token is NEVER included in any response on this
// surface. Only the publisher reads it.
func (o ops) listAccounts(ctx context.Context, in *accountFilter) (*socialAccounts, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListAccounts(ctx, org,
		strings.ToLower(strings.TrimSpace(in.Provider)), limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &socialAccounts{Data: rows}, nil
}

// createAccount records a social account for the org and answers 201 with the
// stored row, including the generated id later calls address it by.
func (o ops) createAccount(ctx context.Context, in *socialAccountBody) (*socialAccount, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	acct, err := accountFrom(mint.ID("acct"), org, in.Provider, in.Handle, in.Status)
	if err != nil {
		return nil, err
	}
	acct.CreatedAt = time.Now().Unix()
	acct.UpdatedAt = acct.CreatedAt
	saved, err := o.s.State.store.CreateAccount(ctx, acct)
	if err != nil {
		return nil, mapErr(err, "")
	}
	return &saved, nil
}

// getAccount returns one of the org's connected accounts by id — its network,
// handle, status and timestamps — or 404. The provider access token is not part of
// the response.
func (o ops) getAccount(ctx context.Context, in *rowRef) (*socialAccount, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	acct, err := o.s.State.store.GetAccount(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "account not found")
	}
	return &acct, nil
}

// updateAccount replaces the account's network, handle and status with what the
// body carries, and answers with the stored row.
//
// This is a REPLACEMENT, not a merge, which is the rule most easily got wrong: a
// field the body omits is written as its default, so leaving out the handle blanks
// it and leaving out the status resets it to connected. Send the whole record. The
// same vocabularies as create apply, and an unknown network or status is refused
// rather than coerced.
func (o ops) updateAccount(ctx context.Context, in *socialAccountWrite) (*socialAccount, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	acct, err := accountFrom(strings.TrimSpace(in.ID), org, in.Provider, in.Handle, in.Status)
	if err != nil {
		return nil, err
	}
	acct.UpdatedAt = time.Now().Unix()
	saved, err := o.s.State.store.UpdateAccount(ctx, acct)
	if err != nil {
		return nil, mapErr(err, "account not found")
	}
	return &saved, nil
}

// deleteAccount removes one connected account from the org and answers 204 with no
// body; an id that is not there is 404.
//
// It removes the account record only. Posts that already published through it keep
// their published state and their recorded external ids — this does not retract
// anything from the network.
func (o ops) deleteAccount(ctx context.Context, in *rowRef) (*cloud.Unit, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := o.s.State.store.DeleteAccount(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("account not found")
	}
	return nil, nil
}

// ---- posts ----

// listPosts returns the org's posts — content, channel, status, scheduled time,
// media and timestamps — most-recently-updated first.
func (o ops) listPosts(ctx context.Context, in *postFilter) (*socialPosts, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListPosts(ctx, org,
		strings.ToLower(strings.TrimSpace(in.Status)), limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &socialPosts{Data: rows}, nil
}

// createPost stores a post for the org and answers 201 with the stored row.
//
// A post created as scheduled for a time that has already passed is published
// IMMEDIATELY, and the row returned carries that outcome — this is the one behaviour
// a reader would otherwise miss. A future-scheduled post is left for the scheduler,
// and a draft is left alone. Publishing never fails the creation: the post is stored
// either way, and a publish that could not run leaves the row for the scheduler to
// retry.
func (o ops) createPost(ctx context.Context, in *socialPostBody) (*socialPost, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	post, err := postFrom(mint.ID("post"), org, in.Content, in.Channel, in.Status, in.ScheduleAt, in.Media)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	post.CreatedAt, post.UpdatedAt = now, now
	saved, err := o.s.State.store.CreatePost(ctx, post)
	if err != nil {
		return nil, mapErr(err, "")
	}
	// On-create fanout: a post scheduled for now-or-earlier publishes immediately
	// (best effort — the post is already stored; the publish outcome, published or
	// failed, is recorded on it and returned). A future-scheduled post is left for
	// the scheduler. A publish NEVER fails the 201: the post exists regardless. Only
	// the two outcome-bearing results (published, or a fail-closed not-configured)
	// update the returned record; an infra error leaves it 'scheduled' for the
	// scheduler.
	if saved.Status == statusScheduled && saved.ScheduleAt <= now {
		if updated, perr := publishPost(ctx, o.s, org, saved.ID); perr == nil || errors.Is(perr, errProviderNotConfigured) {
			saved = updated
		}
	}
	return &saved, nil
}

// getPost returns one of the org's posts by id, with its current status, scheduled
// time, media and — once it has published — the account and external id it published
// under. 404 when there is no such post for this org.
func (o ops) getPost(ctx context.Context, in *rowRef) (*socialPost, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	post, err := o.s.State.store.GetPost(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "post not found")
	}
	return &post, nil
}

// updatePost replaces the post's content, channel, status, scheduled time and media
// with what the body carries, and answers with the stored row.
//
// A REPLACEMENT, not a merge: an omitted field is written as its default, so
// omitting media clears it and omitting the status resets the post to draft.
// `content` is required on every update. Unlike create, this never triggers a
// publish — moving a post's scheduled time into the past here leaves it for the
// scheduler; publish now is its own operation.
func (o ops) updatePost(ctx context.Context, in *socialPostWrite) (*socialPost, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	post, err := postFrom(strings.TrimSpace(in.ID), org, in.Content, in.Channel, in.Status, in.ScheduleAt, in.Media)
	if err != nil {
		return nil, err
	}
	post.UpdatedAt = time.Now().Unix()
	saved, err := o.s.State.store.UpdatePost(ctx, post)
	if err != nil {
		return nil, mapErr(err, "post not found")
	}
	return &saved, nil
}

// deletePost removes one post from the org and answers 204 with no body; an id that
// is not there is 404.
//
// It deletes the record here only. A post that has already published is not
// retracted from the network by deleting it.
func (o ops) deletePost(ctx context.Context, in *rowRef) (*cloud.Unit, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := o.s.State.store.DeletePost(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("post not found")
	}
	return nil, nil
}

// publish publishes the post immediately to the connected accounts on its channel
// and answers with the updated row, carrying the account and external id it
// published under.
//
// It is IDEMPOTENT: a post that has already published, or that another caller is
// publishing right now, comes back unchanged rather than being posted twice. That
// claim is taken before any network call, which is what makes a double submit safe.
//
// The two failure shapes differ on purpose. Having no connected account for the
// channel is the caller's to fix, so it is recorded ON the post as failed with the
// reason and answers normally. A deployment that lacks the network's own credentials
// cannot publish for anyone, so that is a 503 naming exactly what is missing.
func (o ops) publish(ctx context.Context, in *rowRef) (*socialPost, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	post, err := publishPost(ctx, o.s, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapPublishErr(err)
	}
	return &post, nil
}

// ---- the two write normalisers, shared by create and replace ----
//
// One function per entity, so a create and a replacement can never disagree about
// what a provider, a status or a media list means. They are the same validation the
// untyped handlers ran, moved off the request and onto the values.

// accountFrom validates a caller's account fields against the fixed vocabularies and
// returns the row to store, or the 400 the vocabulary refused.
func accountFrom(id, org, provider, handle, status string) (socialAccount, error) {
	p, ok := normProvider(provider)
	if !ok {
		return socialAccount{}, zip.ErrBadRequest("provider must be one of x, facebook, instagram, linkedin, tiktok, youtube, threads")
	}
	st, ok := normAccountStatus(status)
	if !ok {
		return socialAccount{}, zip.ErrBadRequest("status must be one of connected, disconnected, error")
	}
	return socialAccount{ID: id, Org: org, Provider: p, Handle: clip(handle), Status: st}, nil
}

// postFrom validates a caller's post fields against the fixed vocabularies and
// returns the row to store, or the 400 the vocabulary refused.
func postFrom(id, org, content, channel, status string, scheduleAt int64, media []string) (socialPost, error) {
	body := clipBody(content)
	if body == "" {
		return socialPost{}, zip.ErrBadRequest("content is required")
	}
	ch, ok := normProvider(channel)
	if !ok {
		return socialPost{}, zip.ErrBadRequest("channel must be one of x, facebook, instagram, linkedin, tiktok, youtube, threads")
	}
	st, ok := normPostStatus(status)
	if !ok {
		return socialPost{}, zip.ErrBadRequest("status must be one of draft, scheduled, published, failed")
	}
	return socialPost{
		ID: id, Org: org, Content: body, Channel: ch, Status: st,
		ScheduleAt: nonNeg(scheduleAt), Media: normMedia(media),
	}, nil
}
