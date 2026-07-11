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
// carrying a future ScheduleAt. The orchestrator's real provider push / OAuth channel
// connect / media pipeline from the live stack are NOT folded here yet — this is the
// reviewable domain + CRUD seam they hang off (see the fold report).
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner claim (HIP-0026) — and NEVER a client-supplied header. Every store query
// filters WHERE org=?, so one tenant can never read or mutate another's data.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/social/summary            per-org roll-up (posts/scheduled/published/accounts)
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
//
// serve.go auto-registers GET /v1/social/health (this subsystem does not set
// OwnsHealth, so the generic always-ok liveness route serves it).
package social

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

const (
	// maxContent caps a post body so an unbounded request can't amplify the shared
	// DB or a list response. Comfortably above every network's own limit.
	maxContent = 8192
	// maxField caps a single short label field (handle, ids).
	maxField = 1024
	// defaultLimit / maxLimit bound list responses.
	defaultLimit = 200
	maxLimit     = 1000
)

// providers is the network vocabulary for both an Account's provider and a Post's
// target channel, drawn from the live stack's integration providers. A create/update
// with an unknown provider is rejected; empty defaults to x.
var providers = map[string]bool{
	"x": true, "facebook": true, "instagram": true, "linkedin": true,
	"tiktok": true, "youtube": true, "threads": true,
}

// accountStatuses is the account connection lifecycle. Empty defaults to connected.
var accountStatuses = map[string]bool{
	"connected": true, "disconnected": true, "error": true,
}

// postStatuses is the post lifecycle. Empty defaults to draft.
var postStatuses = map[string]bool{
	"draft": true, "scheduled": true, "published": true, "failed": true,
}

// state is social's own data; shared deps (logger, brand) live in the embedded
// cloud.Base, reached as s.Log / s.Brand.
type state struct {
	store *Store
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires the social surface onto app per HIP-0106. It keeps a package global
// (mounted) for Shutdown, so it constructs the Service value directly — the same
// "complex flavour" clients/crm uses.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("social.Mount: nil zip.App")
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
	b := cloud.NewBase(deps, "social")
	s := &cloud.Service[state]{Base: b, State: state{store: store}}
	mounted = s

	routes(app, s)

	b.Log.Info("social mounted", "brand", deps.Brand)
	return nil
}

// routes registers the social surface: the account + post CRUD + the summary roll-up.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/social/summary", cloud.Handle(s, summary))

	app.Get("/v1/social/accounts", cloud.Handle(s, listAccounts))
	app.Post("/v1/social/accounts", cloud.Handle(s, createAccount))
	app.Get("/v1/social/accounts/:id", cloud.Handle(s, getAccount))
	app.Put("/v1/social/accounts/:id", cloud.Handle(s, updateAccount))
	app.Delete("/v1/social/accounts/:id", cloud.Handle(s, deleteAccount))

	app.Get("/v1/social/posts", cloud.Handle(s, listPosts))
	app.Post("/v1/social/posts", cloud.Handle(s, createPost))
	app.Get("/v1/social/posts/:id", cloud.Handle(s, getPost))
	app.Put("/v1/social/posts/:id", cloud.Handle(s, updatePost))
	app.Delete("/v1/social/posts/:id", cloud.Handle(s, deletePost))
}

// ---- shared helpers (mirror clients/crm + clients/marketing) ----

// tenant resolves the org — the tenant-isolation KEY — for a request. It uses
// principal.Org EXACTLY as SanitizeIdentity minted it from the validated IAM owner
// claim (HIP-0026): never lowercased, stripped, or truncated.
func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

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
		return zip.ErrForbidden("X-Org-Id required")
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
	id, err := genID("acct")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
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
		return zip.ErrForbidden("X-Org-Id required")
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
		return zip.ErrForbidden("X-Org-Id required")
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
		return zip.ErrForbidden("X-Org-Id required")
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
		return zip.ErrForbidden("X-Org-Id required")
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
		return zip.ErrForbidden("X-Org-Id required")
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
	id, err := genID("post")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	post := Post{
		ID: id, Org: org, Content: content, Channel: channel, Status: status,
		ScheduleAt: nonNeg(body.ScheduleAt), CreatedAt: now, UpdatedAt: now,
	}
	saved, err := s.State.store.CreatePost(c.Context(), post)
	if err != nil {
		return mapErr(err, "")
	}
	return c.JSON(http.StatusCreated, saved)
}

func listPosts(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
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
		return zip.ErrForbidden("X-Org-Id required")
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
		return zip.ErrForbidden("X-Org-Id required")
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
		ScheduleAt: nonNeg(body.ScheduleAt), UpdatedAt: time.Now().Unix(),
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
		return zip.ErrForbidden("X-Org-Id required")
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

// ---- summary ----

func summary(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	posts, scheduled, published, accounts, err := s.State.store.Counts(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"posts": posts, "scheduled": scheduled, "published": published, "accounts": accounts,
	})
}

// Shutdown closes the social store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
