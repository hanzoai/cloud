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

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/internal/shorten"
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

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/help openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the mounted Service so each op can be a method value — the only bound
// form cmd/zipdoc can lift prose from. It carries STATE and no logic.
type ops struct{ s *cloud.Service[state] }

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
	g := app.Group("/v1/help")
	// No cloud.Bridge here, deliberately. Bridge parks the VALIDATED org and the
	// request on the context for ops that need them; this plane serves exactly ONE
	// org, resolved server-side at mount (publicOrg) and never from a request, and
	// no op here reads any other request fact. Installing it would carry an
	// identity these routes must not consult.
	//
	// Declared on the GROUP: the op's path is the prefix composed with the leaf,
	// which is the identity every projection keys on, and cmd/zipdoc resolves the
	// prefix the same way, so the prose below reaches the document and the tool
	// list.
	o := ops{s: s}
	zip.Get(g, "/articles", o.listArticles)
	zip.Get(g, "/articles/:slug", o.getArticle)
	zip.Get(g, "/categories", o.listCategories)
	zip.Post(g, "/tickets", o.fileTicket, zip.WithStatus(http.StatusCreated))
}

// ---- public knowledge base ----

// helpArticleCard is one article in a public list: everything a card needs and
// nothing more. The body is deliberately absent — a list is a navigation surface,
// and the full text is one fetch away at /v1/help/articles/{slug}.
type helpArticleCard struct {
	// Slug is the article's stable public identifier and the path segment
	// /v1/help/articles/{slug} addresses it by.
	Slug string `json:"slug"`
	// Title is the article's headline.
	Title string `json:"title"`
	// Category is the name of the knowledge-base section the article sits in, or
	// empty when it is filed under none.
	Category string `json:"category"`
	// Excerpt is the short summary the author wrote for listings, or empty.
	Excerpt string `json:"excerpt"`
	// UpdatedAt is the unix second the article was last written, in the help
	// center's own store.
	UpdatedAt int64 `json:"updatedAt"`
}

// helpArticleList is the public knowledge base as a list.
type helpArticleList struct {
	// Data is the matching Published, public articles, newest write order last —
	// the store's order, not a ranking. Empty when the center has none.
	Data []helpArticleCard `json:"data"`
}

// helpArticlesQuery selects which public articles to list.
type helpArticlesQuery struct {
	// Category narrows the list to one knowledge-base section, matched against
	// the article's category by exact name. Empty lists every section.
	Category string `json:"category"`
	// Limit caps how many articles are returned. Anything that is not a positive
	// integer uses 50, and values above 200 are clamped to 200.
	Limit int `json:"limit"`
}

// listArticles returns the public knowledge base: the help center's Published,
// publicly-visible articles as cards. The org is server-fixed and the
// status/is_public filter is server-set, so neither the tenant nor the visibility
// can be widened by the caller. A deployment with no help center answers 404.
func (o ops) listArticles(ctx context.Context, in *helpArticlesQuery) (*helpArticleList, error) {
	org := o.s.State.publicOrg
	if org == "" {
		return nil, zip.ErrNotFound("help center not available")
	}
	filters := map[string]string{"status": "Published", "is_public": "1"}
	if cat := strings.TrimSpace(in.Category); cat != "" {
		filters["category"] = cat
	}
	docs, err := framework.Search(ctx, org, DTArticle, filters, articleLimit(in.Limit))
	if err != nil {
		o.s.Log.Warn("help: list articles", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "list articles")
	}
	out := make([]helpArticleCard, 0, len(docs))
	for _, d := range docs {
		// Defense in depth: never trust the SQL filter alone for a public read.
		if !isPublished(d) {
			continue
		}
		out = append(out, articleCard(d))
	}
	return &helpArticleList{Data: out}, nil
}

// helpArticle is one public article, whole. It is the card plus the body: the
// same fields a listing shows, so a client never has to reconcile two shapes.
type helpArticle struct {
	// Slug is the article's stable public identifier — the path segment it was
	// addressed by.
	Slug string `json:"slug"`
	// Title is the article's headline.
	Title string `json:"title"`
	// Category is the name of the knowledge-base section the article sits in, or
	// empty when it is filed under none.
	Category string `json:"category"`
	// Excerpt is the short summary the author wrote for listings, or empty.
	Excerpt string `json:"excerpt"`
	// Body is the article's rich-text content as the author saved it.
	Body string `json:"body"`
	// UpdatedAt is the unix second the article was last written.
	UpdatedAt int64 `json:"updatedAt"`
}

// helpArticleRef addresses one public article.
type helpArticleRef struct {
	// Slug is the article's public identifier, from the path. It IS the document
	// name in the help center's store.
	Slug string `json:"slug"`
}

// getArticle returns one public article by slug, with its body. A missing, Draft,
// or internal (non-public) article is 404 — fail-closed, so this route is no
// existence oracle for anything beyond "published and public".
func (o ops) getArticle(ctx context.Context, in *helpArticleRef) (*helpArticle, error) {
	org := o.s.State.publicOrg
	slug := strings.TrimSpace(in.Slug)
	if org == "" || slug == "" {
		return nil, zip.ErrNotFound("article not found")
	}
	doc, err := framework.Get(ctx, org, DTArticle, slug)
	if err != nil {
		if !errors.Is(err, framework.ErrNotFound) {
			o.s.Log.Warn("help: get article", "org", org, "err", err)
		}
		return nil, zip.ErrNotFound("article not found")
	}
	if !isPublished(doc) {
		return nil, zip.ErrNotFound("article not found")
	}
	v := articleDetail(doc)
	return &v, nil
}

// helpCategory is one knowledge-base section a visitor can browse.
type helpCategory struct {
	// Name is the section's name, and the value an article's category matches.
	Name string `json:"name"`
	// Description is the section's blurb, or empty.
	Description string `json:"description"`
}

// helpCategoryList is the public center's navigation.
type helpCategoryList struct {
	// Data is the sections that front at least one public article. Empty when the
	// center publishes none.
	Data []helpCategory `json:"data"`
}

// helpCategoriesQuery is the empty input of an op that takes nothing: this plane's category list
// is the whole navigation and has no filter.
type helpCategoriesQuery struct{}

// listCategories returns the knowledge-base sections for the public center's
// navigation — but ONLY the sections that front at least one Published, public
// article, so an internal (agent-only) category name or description never leaks. A
// section with no public article is invisible; a center with no public articles has
// no sections, which is an empty list rather than an error.
func (o ops) listCategories(ctx context.Context, _ *helpCategoriesQuery) (*helpCategoryList, error) {
	org := o.s.State.publicOrg
	if org == "" {
		return nil, zip.ErrNotFound("help center not available")
	}
	// The categories reachable from public articles. Same Published+public predicate
	// the KB serves, re-checked in Go (defense in depth), so this never widens beyond
	// what the article list already exposes.
	arts, err := framework.Search(ctx, org, DTArticle,
		map[string]string{"status": "Published", "is_public": "1"}, maxArticleLimit)
	if err != nil {
		o.s.Log.Warn("help: list categories (articles)", "org", org, "err", err)
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
		return &helpCategoryList{Data: []helpCategory{}}, nil
	}
	docs, err := framework.Search(ctx, org, DTCategory, nil, maxArticleLimit)
	if err != nil {
		o.s.Log.Warn("help: list categories", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "list categories")
	}
	out := make([]helpCategory, 0, len(docs))
	for _, d := range docs {
		if !public[d.Name] { // a category name IS its document name; the article Links it by that name
			continue
		}
		out = append(out, helpCategory{Name: d.Name, Description: strField(d, "description")})
	}
	return &helpCategoryList{Data: out}, nil
}

// ---- customer intake ----

// helpTicketIntake is the anonymous customer submission. Only these fields are honored;
// status/source/assignment are server-owned (a customer can never open a ticket
// pre-assigned or in a non-Open state).
//
// The two unexported fields are the request's SIZE and its SHAPE, recorded by
// UnmarshalJSON below and read back in the handler. They exist because this route
// decides four different statuses in a fixed order — 404 no center, 503 not
// configured, 413 too large, 400 unusable — and a typed op never sees the request,
// so zip would otherwise decode (and 400) the body before the first three gates
// ran. Unexported means they reach no schema and no URL binding: they are not
// something a caller sends.
//
// It preserves that order for an oversized body and for one that parses to the
// wrong shape, and NOT for a body that is not valid JSON at all: encoding/json
// validates the whole document before invoking any custom Unmarshaler, so zip's
// decoder refuses a syntax error before this method is ever called.
// TestSyntacticallyInvalidJSONIs400Early measures exactly that, so the one delta
// typing this route took is a number in the suite rather than a claim here.
type helpTicketIntake struct {
	oversize  bool
	malformed bool

	// Subject is the one-line summary of the problem. Required; longer than 300
	// characters is clipped rather than refused.
	Subject string `json:"subject"`
	// Description is the customer's message. Optional; it becomes the ticket's
	// description AND the opening entry of its conversation thread. Clipped at
	// 16 KiB.
	Description string `json:"description"`
	// Email is how the support team replies. Required; clipped at 320 characters
	// (the RFC 5321 maximum). It is recorded as the ticket's customer, and it is
	// not verified.
	Email string `json:"email"`
	// Priority is Low, Medium, High or Urgent, case-insensitively. Anything else —
	// including omitting it — is recorded as Medium rather than refused.
	Priority string `json:"priority"`
}

// UnmarshalJSON records the body's size and whether it decoded into this shape, and
// refuses NOTHING — every judgement stays in fileTicket, in the order the route has
// always decided it. A body that is valid JSON but not an object (an array, a bare
// scalar) leaves every field at its zero value and is recorded as unusable, which
// the handler answers with the same 400 it always did — after its two earlier gates.
func (in *helpTicketIntake) UnmarshalJSON(b []byte) error {
	type body helpTicketIntake // sheds the method, so this does not recurse
	var v body
	oversize := len(b) > maxIntakeBytes
	malformed := json.Unmarshal(b, &v) != nil
	*in = helpTicketIntake(v)
	in.oversize, in.malformed = oversize, malformed
	return nil
}

// helpTicketFiled is the receipt an anonymous submitter gets back.
type helpTicketFiled struct {
	// Ticket is the opaque, random customer-facing reference ("tkt_" + 24 hex
	// characters). It is NOT the ticket's internal name: that name is sequential,
	// and handing it out would disclose the center's ticket volume.
	Ticket string `json:"ticket"`
	// Status is the lifecycle state the ticket was filed in — always "Open".
	Status string `json:"status"`
}

// fileTicket files a customer support ticket into the public help center. It
// creates the ticket (status Open, source portal) with the customer's message on
// the description, then records that same message as the opening entry of the
// ticket's conversation thread; the description carries it regardless, so failing
// to write that entry loses nothing. Answers 201 with an opaque reference.
//
// A deployment with no help center answers 404, one whose center has not installed
// the Help model answers 503, and a body over 64 KiB answers 413 — in that order,
// which is the order the route has always decided them in.
func (o ops) fileTicket(ctx context.Context, in *helpTicketIntake) (*helpTicketFiled, error) {
	org := o.s.State.publicOrg
	if org == "" {
		return nil, zip.ErrNotFound("help center not available")
	}
	if !framework.Installed(ctx, org, DTTicket) {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "help center not configured")
	}
	// The whole body is bounded BEFORE anything reads it: the meaningful content is
	// a subject + a bounded message + an email, far under this cap, so a larger body
	// is abuse. Recorded at decode, decided here — after the two gates above, which
	// is the order the untyped handler used.
	if in.oversize {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "request too large")
	}
	if in.malformed {
		return nil, zip.ErrBadRequest("invalid request body")
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
		o.s.Log.Warn("help: mint ticket ref", "org", org, "err", err)
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
		o.s.Log.Warn("help: file ticket", "org", org, "err", err)
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
			o.s.Log.Warn("help: opening message not recorded", "ticket", created.Name, "err", cerr)
		}
	}
	return &helpTicketFiled{Ticket: ref, Status: "Open"}, nil
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
func articleCard(d framework.Document) helpArticleCard {
	return helpArticleCard{
		Slug:      d.Name,
		Title:     strField(d, "title"),
		Category:  strField(d, "category"),
		Excerpt:   strField(d, "excerpt"),
		UpdatedAt: d.UpdatedAt,
	}
}

// articleDetail is the full projection (with body). Only public-safe fields.
func articleDetail(d framework.Document) helpArticle {
	return helpArticle{
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
		return shorten.To(s, max)
	}
	return s
}

// articleLimit bounds the public list size (?limit=, default 50, max 200). A
// value zip could not read as an integer arrives as 0 and takes the default,
// which is what the untyped handler did with a value strconv.Atoi rejected.
func articleLimit(n int) int {
	if n <= 0 {
		return defaultArticleLimit
	}
	if n > maxArticleLimit {
		return maxArticleLimit
	}
	return n
}
