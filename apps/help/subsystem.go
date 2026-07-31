// subsystem.go mounts the Hanzo Support PUBLIC plane at /v1/help/*. It is the thin
// surface on top of the framework DocType store (agent CRUD lives at the generic,
// role-gated /v1/framework/hd-*) for the one thing the secure-by-default engine
// deliberately cannot do: serve help.hanzo.ai's anonymous face.
//
//   - GET  /v1/help/articles            the public knowledge base (Published + public only)
//   - GET  /v1/help/articles/:slug      one public article (re-checked, fail-closed)
//   - GET  /v1/help/categories          KB sections that front a public article
//   - POST /v1/help/tickets             a customer files a ticket (bounded intake)
//
// SECURITY — the anonymous org is NEVER client-chosen. Every public endpoint serves
// exactly ONE org, resolved SERVER-SIDE at mount (publicOrg): an explicit operator
// override, else the deployment brand (white-label: hanzo/zoo/lux), matching the crm
// Startup-intake convention. A request's X-Org-Id is IGNORED here, so a caller can
// never read or write another tenant's help center, and the reads are gated to
// status=Published AND is_public=1 (re-checked on a direct fetch) so a Draft or an
// internal article never leaks. Unset (no brand, no override) leaves the plane
// fail-closed: every endpoint 404s until the operator names the org.
//
// Per-client edge RATE LIMITING is the ingress's job (see routes): behind hanzoai/
// ingress the app sees only the ingress as the socket peer, so an app-level per-IP
// limiter would throttle every customer against ONE shared bucket. This plane bounds
// each request instead and leaves the edge limit to the layer that knows the client.
package help

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/framework"
	"github.com/zap-proto/zip"
)

// Public-plane safety limits — an unauthenticated surface, so bound everything.
const (
	maxSubject          = 300
	maxMessage          = 16 * 1024 // a customer message / ticket description
	maxSender           = 320       // RFC 5321 max email length
	maxIntakeBytes      = 64 * 1024 // whole intake body cap (content is a subject + message + email, far under this)
	defaultArticleLimit = 50
	maxArticleLimit     = 200
)

// state carries the resolved public-center org — the ONE tenant the anonymous
// endpoints serve, fixed at mount and never read from a request.
type state struct {
	publicOrg string
}

// Mount wires the /v1/help public plane. The agent plane (triage, authoring, the
// conversation thread) is the framework's generic role-gated surface
// (/v1/framework/hd-*); this adds ONLY the public help center. It owns no store —
// every read/write delegates to the framework in-process API.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("help.Mount: nil app")
	}
	// The public plane is registered as TYPED ops, which live on the *zip.App's
	// registry — the one value OpenAPI, MCP and the CLI are projected from. A Router
	// not backed by one must fail the mount rather than serve routes no projection knows.
	if cloud.ZipApp(app) == nil {
		return fmt.Errorf("help.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	return cloud.Mount(app, deps, "help", build, routes)
}

func build(b cloud.Base) (state, error) {
	org := publicOrg(b.Brand)
	if org == "" {
		b.Log.Warn("help public plane inert: no public org (set CLOUD_HELP_PUBLIC_ORG or a brand)")
	} else {
		b.Log.Info("help public plane mounted", "publicOrg", org, "brand", b.Brand)
	}
	return state{publicOrg: org}, nil
}

// publicOrg resolves the ONE org whose help center is served anonymously. Which
// org's data becomes publicly readable is a deliberate operator decision, so an
// explicit override wins; otherwise it follows the deployment brand. Empty leaves
// the plane fail-closed.
func publicOrg(brand string) string {
	if v := strings.TrimSpace(os.Getenv("CLOUD_HELP_PUBLIC_ORG")); v != "" {
		return v
	}
	return strings.TrimSpace(brand)
}

// routes mounts the four public endpoints under /v1/help. Per-client edge rate
// limiting is the INGRESS's job: hanzoai/ingress terminates the client connection, so
// it is the only layer that sees the real, unspoofable client IP. Behind it the app's
// socket peer IS the ingress, so an app-level per-IP limiter (c.Fiber().IP()) keys
// every customer to ONE shared bucket — a global throttle plus a trivial DoS — and
// X-Forwarded-For is client-settable (a fresh value per request evades the limit AND
// grows the bucket map without bound). So this plane does NOT rate-limit per IP; it
// bounds each request (body size, content clips, fail-closed validation) and delegates
// the edge limit to the ingress — the house pattern ("ingress carries the edge limit").
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	// The body cap FIRST, as middleware: fiber runs middleware in registration
	// order, and a typed op's decoder reads the body before the handler runs, so the
	// bound has to sit in front of the leaves to still be a PRE-parse bound.
	app.Group("/v1/help").Use(capIntake)

	zip.Get(z, "/v1/help/articles", o.listArticles)
	zip.Get(z, "/v1/help/articles/:slug", o.getArticle)
	zip.Get(z, "/v1/help/categories", o.listCategories)
	zip.Post(z, "/v1/help/tickets", o.fileTicket, zip.WithStatus(http.StatusCreated))
}

// ops binds the service to help's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.listArticles), which is
// also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// capIntake bounds a public-plane request body. The meaningful content is a subject
// plus a bounded message plus an email, far under this cap, so a larger body is abuse
// and is refused before anything parses it.
func capIntake(c *zip.Ctx) error {
	if len(c.Body()) > maxIntakeBytes {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request too large")
	}
	return c.Continue()
}

// ---- public projections ----

// ArticleQuery filters the public knowledge base.
type ArticleQuery struct {
	// Category narrows the list to one knowledge-base section, by category name.
	Category string `json:"category"`
	// Limit caps the articles returned; 0 means 50 and nothing above 200 is honoured.
	Limit int `json:"limit"`
}

// ArticleRef addresses one public article.
type ArticleRef struct {
	// Slug is the article's URL slug, which is also its document name.
	Slug string `json:"slug"`
}

// ArticleCard is the list projection of a public article — every public-safe field
// except the body.
type ArticleCard struct {
	// Slug is the article's URL slug.
	Slug string `json:"slug"`
	// Title is the article headline.
	Title string `json:"title"`
	// Category is the knowledge-base section the article sits in.
	Category string `json:"category"`
	// Excerpt is the short summary shown in a list.
	Excerpt string `json:"excerpt"`
	// UpdatedAt is the last edit, as a unix timestamp in seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// ArticleList is the public knowledge base.
type ArticleList struct {
	// Data is the published, public articles, newest content first as the store returns them.
	Data []ArticleCard `json:"data"`
}

// Article is one public article, with its body.
type Article struct {
	// Slug is the article's URL slug.
	Slug string `json:"slug"`
	// Title is the article headline.
	Title string `json:"title"`
	// Category is the knowledge-base section the article sits in.
	Category string `json:"category"`
	// Excerpt is the short summary shown in a list.
	Excerpt string `json:"excerpt"`
	// Body is the rendered article text.
	Body string `json:"body"`
	// UpdatedAt is the last edit, as a unix timestamp in seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// CategoryCard is one knowledge-base section in the public navigation.
type CategoryCard struct {
	// Name is the section's name, which is also its document name.
	Name string `json:"name"`
	// Description is the section blurb.
	Description string `json:"description"`
}

// CategoryList is the public center's navigation.
type CategoryList struct {
	// Data is the sections that front at least one published, public article.
	Data []CategoryCard `json:"data"`
}

// TicketIntake is the anonymous customer submission. Only these fields are honored;
// status, source and assignment are server-owned, so a customer can never open a
// ticket pre-assigned or in a non-Open state.
type TicketIntake struct {
	// Subject is the one-line summary. Required; clipped to 300 characters.
	Subject string `json:"subject" validate:"required"`
	// Description is the customer's message. Clipped to 16 KiB.
	Description string `json:"description"`
	// Email is the customer's address, and the party the ticket is filed against. Required.
	Email string `json:"email" validate:"required"`
	// Priority is one of low, medium, high or urgent; anything else means medium.
	Priority string `json:"priority"`
}

// TicketReceipt is what an anonymous submitter gets back.
type TicketReceipt struct {
	// Ticket is the opaque customer-facing reference; the internal ticket number is not disclosed.
	Ticket string `json:"ticket"`
	// Status is the ticket's lifecycle state, always Open on intake.
	Status string `json:"status"`
}

// ---- public knowledge base ----

// listArticles returns the help center's published, public articles without bodies. The
// tenant is fixed at mount and the published/public filter is server-set, so a caller
// can widen neither.
//
// Example: {"category": "billing", "limit": 20}
// Response: {"data": [{"slug": "reset-my-password", "title": "Reset my password", "category": "accounts", "excerpt": "Use the reset link on the sign-in page.", "updatedAt": 1780000000}]}
func (o ops) listArticles(ctx context.Context, in *ArticleQuery) (*ArticleList, error) {
	s := o.s
	org := s.State.publicOrg
	if org == "" {
		return nil, zip.ErrNotFound("help center not available")
	}
	filters := map[string]string{"status": "Published", "is_public": "1"}
	if cat := strings.TrimSpace(in.Category); cat != "" {
		filters["category"] = cat
	}
	docs, err := framework.Search(ctx, org, DTArticle, filters, articleLimit(in.Limit))
	if err != nil {
		s.Log.Warn("help: list articles", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "list articles")
	}
	out := make([]ArticleCard, 0, len(docs))
	for _, d := range docs {
		// Defense in depth: never trust the SQL filter alone for a public read.
		if !isPublished(d) {
			continue
		}
		out = append(out, articleCard(d))
	}
	return &ArticleList{Data: out}, nil
}

// getArticle returns one published, public article by slug. The article's document name
// IS its slug, and a missing, Draft, or internal (non-public) article is 404 —
// fail-closed, no existence oracle beyond "published + public".
//
// Example: {"slug": "reset-my-password"}
// Response: {"slug": "reset-my-password", "title": "Reset my password", "category": "accounts", "excerpt": "Use the reset link on the sign-in page.", "body": "<p>Open the sign-in page…</p>", "updatedAt": 1780000000}
func (o ops) getArticle(ctx context.Context, in *ArticleRef) (*Article, error) {
	s := o.s
	org := s.State.publicOrg
	slug := strings.TrimSpace(in.Slug)
	if org == "" || slug == "" {
		return nil, zip.ErrNotFound("article not found")
	}
	doc, err := framework.Get(ctx, org, DTArticle, slug)
	if err != nil {
		if !errors.Is(err, framework.ErrNotFound) {
			s.Log.Warn("help: get article", "org", org, "err", err)
		}
		return nil, zip.ErrNotFound("article not found")
	}
	if !isPublished(doc) {
		return nil, zip.ErrNotFound("article not found")
	}
	d := articleDetail(doc)
	return &d, nil
}

// listCategories returns the knowledge-base sections that front a public article. Only
// sections carrying at least one Published + public article are listed, so an internal
// (agent-only) category name or description never leaks; an org with no public articles
// has no sections (empty, not an error).
//
// Response: {"data": [{"name": "accounts", "description": "Signing in and account settings."}]}
func (o ops) listCategories(ctx context.Context, _ *struct{}) (*CategoryList, error) {
	s := o.s
	org := s.State.publicOrg
	if org == "" {
		return nil, zip.ErrNotFound("help center not available")
	}
	// The categories reachable from public articles. Same Published+public predicate
	// the KB serves, re-checked in Go (defense in depth), so this never widens beyond
	// what the article list already exposes.
	arts, err := framework.Search(ctx, org, DTArticle,
		map[string]string{"status": "Published", "is_public": "1"}, maxArticleLimit)
	if err != nil {
		s.Log.Warn("help: list categories (articles)", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "list categories")
	}
	public := make(map[string]bool, len(arts))
	for _, a := range arts {
		if isPublished(a) {
			if cat := strField(a, "category"); cat != "" {
				public[cat] = true
			}
		}
	}
	if len(public) == 0 {
		return &CategoryList{Data: []CategoryCard{}}, nil
	}
	docs, err := framework.Search(ctx, org, DTCategory, nil, maxArticleLimit)
	if err != nil {
		s.Log.Warn("help: list categories", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "list categories")
	}
	out := make([]CategoryCard, 0, len(docs))
	for _, d := range docs {
		if !public[d.Name] { // a category name IS its document name; the article Links it by that name
			continue
		}
		out = append(out, CategoryCard{Name: d.Name, Description: strField(d, "description")})
	}
	return &CategoryList{Data: out}, nil
}

// ---- customer intake ----

// fileTicket opens a support ticket from an anonymous customer submission. The ticket is
// created status Open, source portal, with the customer's message on the description,
// and that message is then recorded as the opening conversation entry — the description
// carries it regardless, so failing to write the entry loses nothing.
//
// Example: {"subject": "Cannot sign in", "description": "The reset link 404s.", "email": "sam@example.com", "priority": "high"}
// Response: {"ticket": "tkt_4c1e9b7a2d6f0538e4a7c9b1", "status": "Open"}
func (o ops) fileTicket(ctx context.Context, in *TicketIntake) (*TicketReceipt, error) {
	s := o.s
	org := s.State.publicOrg
	if org == "" {
		return nil, zip.ErrNotFound("help center not available")
	}
	if !framework.Installed(ctx, org, DTTicket) {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "help center not configured")
	}
	subject := clip(in.Subject, maxSubject)
	if subject == "" {
		return nil, zip.ErrBadRequest("subject is required")
	}
	email := clip(in.Email, maxSender)
	if email == "" {
		return nil, zip.ErrBadRequest("email is required")
	}
	message := clip(in.Description, maxMessage)

	// The customer-facing reference is opaque + random, so returning it to an anonymous
	// submitter reveals nothing about ticket volume; the monotonic name stays internal.
	ref, err := newPublicRef()
	if err != nil {
		s.Log.Warn("help: mint ticket ref", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "file ticket")
	}

	created, err := framework.Ingest(ctx, org, DTTicket, map[string]any{
		"subject":     subject,
		"description": message,
		"customer":    email,
		"priority":    normalizePriority(in.Priority),
		"status":      "Open",
		"source":      "portal",
		"public_ref":  ref,
	}, "")
	if err != nil {
		if framework.IsValidationError(err) {
			return nil, zip.ErrBadRequest(err.Error())
		}
		s.Log.Warn("help: file ticket", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "file ticket")
	}

	if message != "" {
		if _, cerr := framework.Ingest(ctx, org, DTCommunication, map[string]any{
			"ticket":      created.Name,
			"sender":      email,
			"sender_type": "customer",
			"body":        message,
			"channel":     "portal",
		}, ""); cerr != nil {
			// Non-fatal: the message is already on the ticket description.
			s.Log.Warn("help: opening message not recorded", "ticket", created.Name, "err", cerr)
		}
	}
	return &TicketReceipt{Ticket: ref, Status: "Open"}, nil
}

// newPublicRef mints the opaque, random customer-facing ticket reference — 96 bits of
// entropy, so it is collision-free across any realistic volume (the field is Unique, so
// a collision would 422 rather than alias, but it cannot happen in practice) and, being
// random, discloses nothing about ticket count or rate.
func newPublicRef() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "tkt_" + hex.EncodeToString(b[:]), nil
}

// ---- projections & helpers ----

// articleCard is the light list projection (no body). Only public-safe fields.
func articleCard(d framework.Document) ArticleCard {
	return ArticleCard{
		Slug:      d.Name,
		Title:     strField(d, "title"),
		Category:  strField(d, "category"),
		Excerpt:   strField(d, "excerpt"),
		UpdatedAt: d.UpdatedAt,
	}
}

// articleDetail is the full projection (with body). Only public-safe fields.
func articleDetail(d framework.Document) Article {
	return Article{
		Slug:      d.Name,
		Title:     strField(d, "title"),
		Category:  strField(d, "category"),
		Excerpt:   strField(d, "excerpt"),
		Body:      strField(d, "body"),
		UpdatedAt: d.UpdatedAt,
	}
}

// isPublished reports whether a document is a Published AND public article — the
// exact public-visibility predicate, applied whether the doc came from a filtered
// list or a direct fetch.
func isPublished(d framework.Document) bool {
	status, _ := d.Data["status"].(string)
	return status == "Published" && isTruthy(d.Data["is_public"])
}

// isTruthy interprets a framework Check value (stored as int, read back as a JSON
// number) — and the other plausible encodings — as a boolean.
func isTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x == 1
	case int:
		return x == 1
	case int64:
		return x == 1
	case string:
		return x == "1" || x == "true"
	default:
		return false
	}
}

// strField reads a string field, defaulting to "" for a missing/non-string value.
func strField(d framework.Document, name string) string {
	s, _ := d.Data[name].(string)
	return s
}

// normalizePriority snaps a customer-supplied priority to the allowed ladder,
// defaulting to Medium — a customer never gets a 422 for an odd priority, and the
// value is always one the ticket Select accepts.
func normalizePriority(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "low":
		return "Low"
	case "high":
		return "High"
	case "urgent":
		return "Urgent"
	default:
		return "Medium"
	}
}

// clip trims and bounds a text field.
func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max]
	}
	return s
}

// articleLimit bounds the public list size (default 50, max 200).
func articleLimit(n int) int {
	if n <= 0 {
		return defaultArticleLimit
	}
	if n > maxArticleLimit {
		return maxArticleLimit
	}
	return n
}
