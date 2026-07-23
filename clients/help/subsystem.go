// subsystem.go mounts the Hanzo Support PUBLIC plane at /v1/help/*. It is the thin
// surface on top of the framework DocType store (agent CRUD lives at the generic,
// role-gated /v1/framework/hd-*) for the one thing the secure-by-default engine
// deliberately cannot do: serve help.hanzo.ai's anonymous face.
//
//   - GET  /v1/help/articles            the public knowledge base (Published + public only)
//   - GET  /v1/help/articles/:slug      one public article (re-checked, fail-closed)
//   - GET  /v1/help/categories          KB sections for navigation
//   - POST /v1/help/tickets             a customer files a ticket (rate-limited intake)
//
// SECURITY — the anonymous org is NEVER client-chosen. Every public endpoint serves
// exactly ONE org, resolved SERVER-SIDE at mount (publicOrg): an explicit operator
// override, else the deployment brand (white-label: hanzo/zoo/lux), matching the crm
// Startup-intake convention. A request's X-Org-Id is IGNORED here, so a caller can
// never read or write another tenant's help center, and the reads are gated to
// status=Published AND is_public=1 (re-checked on a direct fetch) so a Draft or an
// internal article never leaks. Unset (no brand, no override) leaves the plane
// fail-closed: every endpoint 404s until the operator names the org.
package help

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/framework"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// Public-plane safety limits — an unauthenticated surface, so bound everything.
const (
	maxSubject          = 300
	maxMessage          = 16 * 1024 // a customer message / ticket description
	maxSender           = 320       // RFC 5321 max email length
	defaultArticleLimit = 50
	maxArticleLimit     = 200
	// intake* strictly bound customer ticket creation per client IP (spam/DoS on an
	// unauthenticated write). read* modestly bound public reads; ingress carries the
	// primary edge limit.
	intakeRateLimit  = 10
	intakeRateWindow = time.Minute
	readRateLimit    = 120
	readRateWindow   = time.Minute
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
func Mount(app *zip.App, deps cloud.Deps) error {
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

func routes(app *zip.App, s *cloud.Service[state]) {
	ipKey := func(c *zip.Ctx) string { return c.Fiber().IP() }

	// Public KB reads — a modest per-IP limit over the shared store.
	read := app.Group("/v1/help", middleware.RateLimit(middleware.RateLimitConfig{
		Limit: readRateLimit, Window: readRateWindow, KeyFn: ipKey,
	}))
	read.Get("/articles", cloud.Handle(s, listArticles))
	read.Get("/articles/:slug", cloud.Handle(s, getArticle))
	read.Get("/categories", cloud.Handle(s, listCategories))

	// Public customer intake — a strict per-IP limit on an unauthenticated write.
	app.Group("/v1/help", middleware.RateLimit(middleware.RateLimitConfig{
		Limit: intakeRateLimit, Window: intakeRateWindow, KeyFn: ipKey,
	})).Post("/tickets", cloud.Handle(s, fileTicket))
}

// ---- public knowledge base ----

// listArticles returns the public knowledge base: the public org's Published +
// public articles. The org is server-fixed and the status/is_public filter is
// server-set, so neither the tenant nor the visibility can be widened by the caller.
func listArticles(s *cloud.Service[state], c *zip.Ctx) error {
	org := s.State.publicOrg
	if org == "" {
		return zip.ErrNotFound("help center not available")
	}
	filters := map[string]string{"status": "Published", "is_public": "1"}
	if cat := strings.TrimSpace(c.Query("category")); cat != "" {
		filters["category"] = cat
	}
	docs, err := framework.Search(c.Context(), org, DTArticle, filters, articleLimit(c))
	if err != nil {
		s.Log.Warn("help: list articles", "org", org, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "list articles")
	}
	out := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		// Defense in depth: never trust the SQL filter alone for a public read.
		if !isPublished(d) {
			continue
		}
		out = append(out, articleCard(d))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// getArticle returns one public article by slug (the article's document name IS its
// slug). A missing, Draft, or internal (non-public) article is 404 — fail-closed, no
// existence oracle beyond "published + public".
func getArticle(s *cloud.Service[state], c *zip.Ctx) error {
	org := s.State.publicOrg
	slug := strings.TrimSpace(c.Param("slug"))
	if org == "" || slug == "" {
		return zip.ErrNotFound("article not found")
	}
	doc, err := framework.Get(c.Context(), org, DTArticle, slug)
	if err != nil {
		if !errors.Is(err, framework.ErrNotFound) {
			s.Log.Warn("help: get article", "org", org, "err", err)
		}
		return zip.ErrNotFound("article not found")
	}
	if !isPublished(doc) {
		return zip.ErrNotFound("article not found")
	}
	return c.JSON(http.StatusOK, articleDetail(doc))
}

// listCategories returns the KB sections for the public center's navigation. Missing
// module → empty (not an error): an org with no help center has no sections.
func listCategories(s *cloud.Service[state], c *zip.Ctx) error {
	org := s.State.publicOrg
	if org == "" {
		return zip.ErrNotFound("help center not available")
	}
	docs, err := framework.Search(c.Context(), org, DTCategory, nil, maxArticleLimit)
	if err != nil {
		s.Log.Warn("help: list categories", "org", org, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "list categories")
	}
	out := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		out = append(out, map[string]any{
			"name":        d.Name,
			"description": strField(d, "description"),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// ---- customer intake ----

// ticketIntake is the anonymous customer submission. Only these fields are honored;
// status/source/assignment are server-owned (a customer can never open a ticket
// pre-assigned or in a non-Open state).
type ticketIntake struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	Email       string `json:"email"`
	Priority    string `json:"priority"`
}

// fileTicket files a customer ticket into the public org. It creates the ticket
// (status Open, source portal) with the customer's message on the description, then
// records that message as the opening conversation entry. The description carries
// the message regardless, so a failure to write the opening entry loses nothing.
func fileTicket(s *cloud.Service[state], c *zip.Ctx) error {
	org := s.State.publicOrg
	if org == "" {
		return zip.ErrNotFound("help center not available")
	}
	if !framework.Installed(c.Context(), org, DTTicket) {
		return zip.Errorf(http.StatusServiceUnavailable, "help center not configured")
	}
	var in ticketIntake
	if err := c.Bind(&in); err != nil {
		return err
	}
	subject := clip(in.Subject, maxSubject)
	if subject == "" {
		return zip.ErrBadRequest("subject is required")
	}
	email := clip(in.Email, maxSender)
	if email == "" {
		return zip.ErrBadRequest("email is required")
	}
	message := clip(in.Description, maxMessage)

	created, err := framework.Ingest(c.Context(), org, DTTicket, map[string]any{
		"subject":     subject,
		"description": message,
		"customer":    email,
		"priority":    normalizePriority(in.Priority),
		"status":      "Open",
		"source":      "portal",
	}, "")
	if err != nil {
		if framework.IsValidationError(err) {
			return zip.ErrBadRequest(err.Error())
		}
		s.Log.Warn("help: file ticket", "org", org, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "file ticket")
	}

	if message != "" {
		if _, cerr := framework.Ingest(c.Context(), org, DTCommunication, map[string]any{
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
	return c.JSON(http.StatusCreated, map[string]any{"ticket": created.Name, "status": "Open"})
}

// ---- projections & helpers ----

// articleCard is the light list projection (no body). Only public-safe fields.
func articleCard(d framework.Document) map[string]any {
	return map[string]any{
		"slug":      d.Name,
		"title":     strField(d, "title"),
		"category":  strField(d, "category"),
		"excerpt":   strField(d, "excerpt"),
		"updatedAt": d.UpdatedAt,
	}
}

// articleDetail is the full projection (with body). Only public-safe fields.
func articleDetail(d framework.Document) map[string]any {
	return map[string]any{
		"slug":      d.Name,
		"title":     strField(d, "title"),
		"category":  strField(d, "category"),
		"excerpt":   strField(d, "excerpt"),
		"body":      strField(d, "body"),
		"updatedAt": d.UpdatedAt,
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

// articleLimit bounds the public list size (?limit=, default 50, max 200).
func articleLimit(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
		return defaultArticleLimit
	}
	if n > maxArticleLimit {
		return maxArticleLimit
	}
	return n
}
