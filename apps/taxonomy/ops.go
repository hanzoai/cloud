package taxonomy

// The surface. Every route here is a TYPED op: ONE registry entry that is at once
// the REST route, the OpenAPI operation with its schemas, the MCP tool, the CLI
// command and the generated SDK method. An untyped route is a route and nothing
// else.

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op — and off each field of its In
// and Out — into zipdoc_gen.go, which hands them to zip.Describe at init. Go
// drops comments at compile time, so this build-time pass is the ONLY way that
// prose reaches the published document, the MCP tool list and the generated SDKs.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Bounds on what a person may type into the editor. They are generous — this is
// display copy, not a credential — and exist so one paste cannot turn a catalogue
// every visitor loads into a megabyte.
const (
	maxID    = 64
	maxShort = 200  // a label, a name, an icon, a route
	maxLong  = 2000 // a summary or a description
	maxList  = 32   // tags on a taxon, brands on either
)

// Nothing here calls openapi.Public, and that is a different question from the
// one the gate below answers. PUBLIC-READABLE is about AUTHORIZATION: the
// catalogue read asks for no credential, so the marketing landing renders from it
// signed out. openapi.yaml is about AUDIENCE, and this deployment publishes the
// INFERENCE surface there and nothing else — a rule openapi's own compose enforces.
// A product catalogue is not inference, so it stays out of the published contract
// while remaining perfectly reachable.

// ops binds the store to the typed ops. A TypedHandler takes no service
// parameter, so the service arrives as a RECEIVER and every op is a method value
// — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *service }

func routes(app cloud.Router, s *service) {
	// cloud.Bridge is installed by whoever composes the app — the fused host at its
	// root — never here: the request the gate below reads is parked on the context
	// by that root install, ahead of every leaf.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		s.log.Error("taxonomy: router exposes no op registry; the catalogue would serve routes no projection knows")
		return
	}
	o := ops{s: s}
	g := app.Group("/v1/taxonomy")

	// The root of the surface, declared on the App with its WHOLE path rather than
	// as an empty leaf on the group: joining "/v1/taxonomy" with "" yields
	// "/v1/taxonomy/", a path this API has never served, and that is the string the
	// document, the operationId, the MCP tool and every SDK's URL would carry.
	zip.Get(zapp, "/v1/taxonomy", o.read)

	zip.Put(g, "/categories/:id", o.putCategory)
	zip.Delete(g, "/categories/:id", o.deleteCategory)
	zip.Put(g, "/taxa/:id", o.putTaxon)
	zip.Delete(g, "/taxa/:id", o.deleteTaxon)
}

// audience is the org whose rows the caller may SEE beside the platform's, and
// empty for a signed-out visitor. It is the read half of tenancy and it reads one
// value: the org on the validated principal, which the identity boundary mints
// and a client cannot supply.
func audience(ctx context.Context) string {
	org, _ := principal.OrgFrom(ctx)
	return org
}

// writable is the org whose rows the caller may CHANGE, or a refusal. It is the
// ONE place this package decides authority, and it decides between exactly the two
// scopes the platform already has — never a third notion of its own:
//
//	SuperAdmin (cloud.Super: validated AND a member of the reserved admin org)
//	           writes the PLATFORM catalogue, the rows every tenant sees.
//	an ORG ADMIN (principal.IsOrgAdmin — "admin of my own org", which
//	           SanitizeIdentity strips on ingress and re-mints only from a
//	           validated claim) writes THEIR OWN org's rows and no others.
//
// The two are kept apart deliberately, because conflating them is a privilege
// escalation and not a shortcut: an org admin reaching the platform rows would
// rename a category for every other tenant, so an admin whose own org IS the
// platform org is refused here too unless they are also platform sudo. That
// refusal is the escalation case, and it is one line.
//
// The owner is DERIVED and never named by the caller. There is no request field
// that says whose catalogue to write, so "write org B's row" is not a request this
// API can express — the worst a caller can do by naming B's id is write their own.
//
// It reads the REQUEST because admin-ness is a claim carried in a header, which
// principal.OrgFrom does not carry. It fails closed off the HTTP path, where there
// is no attested caller at all.
func writable(ctx context.Context) (string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", cloud.Super.Refusal()
	}
	if cloud.Super.Admits(cloud.AuthorityOf(c)) {
		return hanzo, nil
	}
	org, ok := principal.Org(c)
	if !ok || !principal.IsOrgAdmin(c) {
		return "", zip.ErrForbidden("an org admin edits their own catalogue; the platform's needs a SuperAdmin")
	}
	if org == hanzo {
		return "", zip.ErrForbidden("the platform catalogue is edited by a SuperAdmin, not by an admin of the platform's own org")
	}
	return org, nil
}

// mine reports whether the caller may change this row, which is also the rule for
// SHOWING an unpublished one: staging is only useful to whoever can unstage it, and
// a row nobody can see is not staged, it is lost. One predicate, both questions.
func mine(writer, owner string) bool { return writer != "" && writer == owner }

// keyed is a row that knows whose it is and what it is called — the two halves of
// its primary key. Both tables answer it, so the projection below is written once.
type keyed interface{ key() (owner, id string) }

func (c Category) key() (string, string) { return c.Owner, c.ID }
func (t Taxon) key() (string, string)    { return t.Owner, t.ID }

// own settles a collision between the caller's org and the platform on the same
// id, in the caller's favour, preserving order. It is the ONE place that rule
// lives, applied identically to categories and to taxa, so a console can never see
// one id twice and never has to guess which of two rows it is looking at.
//
// Ids are unique per ORG rather than globally, and that is deliberate: a global id
// would make one customer's write fail because ANOTHER customer already used the
// name, and that refusal would be an observation of a tenant they may not observe.
// The price of keeping tenants blind to each other is that a collision with the
// platform is possible, so this decides it — rather than leaving the console to
// take whichever row it read last.
func own[T keyed](rows []T, org string) []T {
	if org == "" || org == hanzo {
		return rows
	}
	shadowed := make(map[string]bool, len(rows))
	for _, r := range rows {
		if owner, id := r.key(); owner == org {
			shadowed[id] = true
		}
	}
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		if owner, id := r.key(); owner != org && shadowed[id] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// ── the wire ────────────────────────────────────────────────────────────────

// Taxonomy is the whole catalogue: every category in display order, each carrying
// the taxa filed under it in theirs.
type Taxonomy struct {
	// Categories are the groupings, in display order, each with its own taxa.
	Categories []Category `json:"categories"`
}

// Category is one grouping of products — the sections the console's navigation and
// the marketing landing are built from.
type Category struct {
	// Owner is the org this category belongs to: the platform's own org for a
	// category every tenant sees, or your org for one you added. It tells a console
	// which rows it may offer to edit.
	Owner string `json:"owner"`
	// ID is the stable slug this category is addressed by, e.g. "observe".
	ID string `json:"id"`
	// Label is the display name, e.g. "Observe".
	Label string `json:"label"`
	// Summary is the one line describing what the category groups, shown as the
	// header copy on its landing page.
	Summary string `json:"summary"`
	// Order is where the category sits among its siblings, ascending.
	Order int `json:"order"`
	// Brands are the brands whose console shows this category. Absent means every
	// brand.
	Brands []string `json:"brands,omitempty"`
	// Taxa are the products filed under this category, in display order.
	Taxa []Taxon `json:"taxa"`
}

// Taxon is one product in the catalogue: what it is called, where it sits, and how
// it opens.
type Taxon struct {
	// Owner is the org this product belongs to: the platform's own org for one
	// every tenant sees, or your org for one you added. Where two rows share an id,
	// yours is the one served.
	Owner string `json:"owner"`
	// ID is the stable slug this taxon is addressed by, e.g. "vector".
	ID string `json:"id"`
	// Name is the display name, e.g. "Vector".
	Name string `json:"name"`
	// Description is the one line shown beneath the name in the catalogue and nav.
	Description string `json:"description"`
	// Category is the id of the category this taxon is filed under.
	Category string `json:"category"`
	// Tags are free-form labels for search and grouping across categories.
	Tags []string `json:"tags,omitempty"`
	// Icon names the icon the surface renders, e.g. "Database". It is a NAME, not
	// an image: which icon set draws it is the rendering surface's business.
	Icon string `json:"icon,omitempty"`
	// Route is the in-console path this taxon opens, e.g. "/vector". Set for a
	// product the console renders itself; empty for an external one.
	Route string `json:"route,omitempty"`
	// Href is the absolute URL an external product launches, for the taxa that
	// genuinely live at their own domain. Empty for an in-console product.
	Href string `json:"href,omitempty"`
	// Brands are the brands whose console shows this taxon. Absent means every
	// brand its category admits.
	Brands []string `json:"brands,omitempty"`
	// Order is where the taxon sits within its category, ascending.
	Order int `json:"order"`
	// Published is whether the taxon is shown. An unpublished taxon is served only
	// to an editor, so a product can be staged before anyone sees it.
	Published bool `json:"published"`
}

// readIn narrows the catalogue read.
type readIn struct {
	// Brand returns only what that brand's console shows — the categories it
	// admits, and within them the taxa scoped to it. Empty returns everything.
	Brand string `json:"brand"`
}

// idIn addresses one row by the id in its path. A DELETE takes its input from the
// URL and carries no request body, so this is the whole input.
type idIn struct {
	// ID is the slug to act on, from the path.
	ID string `json:"id"`
}

// deleted names what was removed.
type deleted struct {
	// Deleted is the id that no longer exists.
	Deleted string `json:"deleted"`
}

// categoryIn is one whole category as a PUT writes it. Every field but the path's
// id carries `url:"-"`: zip's binder fills an In field from the QUERY as well as
// the body, so a body field left URL-bindable would silently start accepting
// `?label=`, and a query string could rename a category the body never mentioned.
type categoryIn struct {
	// ID is the category slug to write, from the path.
	ID string `json:"id"`
	// Label is the display name. Required.
	Label string `json:"label" url:"-"`
	// Summary is the one line describing what the category groups.
	Summary string `json:"summary" url:"-"`
	// Order is where the category sits among its siblings, ascending.
	Order int `json:"order" url:"-"`
	// Brands are the brands whose console shows it. Omit for every brand.
	Brands []string `json:"brands" url:"-"`
}

// taxonIn is one whole taxon as a PUT writes it. Same `url:"-"` rule as
// categoryIn, and for the same reason.
type taxonIn struct {
	// ID is the taxon slug to write, from the path.
	ID string `json:"id"`
	// Name is the display name. Required.
	Name string `json:"name" url:"-"`
	// Description is the one line shown beneath the name.
	Description string `json:"description" url:"-"`
	// Category is the id of an EXISTING category to file it under. Required.
	Category string `json:"category" url:"-"`
	// Tags are free-form labels for search and grouping across categories.
	Tags []string `json:"tags" url:"-"`
	// Icon names the icon the surface renders, e.g. "Database".
	Icon string `json:"icon" url:"-"`
	// Route is the in-console path it opens, e.g. "/vector". Give this or href,
	// never both.
	Route string `json:"route" url:"-"`
	// Href is the absolute URL an external product launches. Give this or route,
	// never both.
	Href string `json:"href" url:"-"`
	// Brands are the brands whose console shows it. Omit for every brand its
	// category admits.
	Brands []string `json:"brands" url:"-"`
	// Order is where it sits within its category, ascending.
	Order int `json:"order" url:"-"`
	// Published is whether it is shown. Omitted means published — a taxon someone
	// took the trouble to write is meant to be seen, and hiding one is the
	// deliberate act.
	Published *bool `json:"published" url:"-"`
}

// ── handlers ────────────────────────────────────────────────────────────────

// Read returns the product catalogue as this caller sees it: the PLATFORM
// catalogue — Hanzo's own products, the part that is true for everyone — plus the
// caller's own org's rows, every category in display order and each carrying the
// products filed under it in theirs. Another customer's rows are never in it. It
// is readable signed out, and a signed-out visitor gets the platform catalogue
// alone, which is what the marketing landing renders from.
//
// Where the caller's org and the platform hold the same id, the caller's own row
// is the one served. That rule exists because ids are unique per ORG and not
// globally — two customers may each have a "crm", and refusing the second would
// tell one of them the other exists — so a collision with the platform is possible
// by construction and something has to win deterministically. Yours does: your own
// catalogue is the one you edited.
//
// `?brand=` narrows it the way a brand's own console does: only the categories
// that brand admits, and within them only the taxa scoped to it. An unpublished
// row is served only to whoever may edit it — a SuperAdmin for the platform's, an
// org admin for their own — so a product can be staged before anyone sees it
// without becoming invisible to the person staging it.
func (o ops) read(ctx context.Context, in *readIn) (*Taxonomy, error) {
	org := audience(ctx)
	// A refusal here is "you may edit nothing", which is a perfectly good answer for
	// a reader: it means no unpublished row is shown. The read never fails on it.
	writer, _ := writable(ctx)

	cats, err := o.s.store.Categories(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "read taxonomy: %v", err)
	}
	taxa, err := o.s.store.Taxa(ctx, org, "")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "read taxonomy: %v", err)
	}
	brand := strings.TrimSpace(in.Brand)

	filed := make(map[string][]Taxon, len(cats))
	for _, e := range own(taxa, org) {
		if !e.Published && !mine(writer, e.Owner) {
			continue
		}
		if !shows(brand, e.Brands) {
			continue
		}
		filed[e.Category] = append(filed[e.Category], e)
	}
	out := &Taxonomy{Categories: make([]Category, 0, len(cats))}
	for _, c := range own(cats, org) {
		if !shows(brand, c.Brands) {
			continue
		}
		c.Taxa = filed[c.ID]
		if c.Taxa == nil {
			c.Taxa = []Taxon{}
		}
		out.Categories = append(out.Categories, c)
	}
	return out, nil
}

// PutCategory creates or replaces one category and returns it as stored. The id in
// the URL is the one it is filed under whatever the body says, so a category can
// never be written under a name it was not addressed by — which also makes create
// and replace the same act, and is why there is no POST beside this.
//
// Platform SuperAdmin only: one catalogue serves every tenant, so an org admin who
// could rename a category would rename it for all of them.
//
// Example: {"label": "Observe", "summary": "Traces, metrics, logs and alerts.", "order": 6}
func (o ops) putCategory(ctx context.Context, in *categoryIn) (*Category, error) {
	owner, err := writable(ctx)
	if err != nil {
		return nil, err
	}
	id, err := slug(in.ID)
	if err != nil {
		return nil, err
	}
	label, err := text("label", in.Label, maxShort, true)
	if err != nil {
		return nil, err
	}
	summary, err := text("summary", in.Summary, maxLong, false)
	if err != nil {
		return nil, err
	}
	brands, err := list("brands", in.Brands)
	if err != nil {
		return nil, err
	}
	c := Category{Owner: owner, ID: id, Label: label, Summary: summary, Order: in.Order, Brands: brands}
	if err := o.s.store.PutCategory(ctx, c); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "put category: %v", err)
	}
	// Read back what is filed under it rather than answering with an empty list.
	// Renaming a category that holds 37 products and being told it holds none is a
	// lie the editor would render.
	if c.Taxa, err = o.s.store.Taxa(ctx, owner, id); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "put category: %v", err)
	}
	if c.Taxa == nil {
		c.Taxa = []Taxon{}
	}
	return &c, nil
}

// DeleteCategory removes one empty category. A category that still has taxa
// filed under it is refused with 409 and a count: deleting the label off a group
// must never silently take the products wearing it, and the alternative — orphan
// rows naming a category that no longer exists — is a catalogue that cannot be
// rendered. Move or delete its taxa first. An id no category holds is a 404.
func (o ops) deleteCategory(ctx context.Context, in *idIn) (*deleted, error) {
	owner, err := writable(ctx)
	if err != nil {
		return nil, err
	}
	id, err := slug(in.ID)
	if err != nil {
		return nil, err
	}
	n, err := o.s.store.CountTaxa(ctx, owner, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete category: %v", err)
	}
	if n > 0 {
		return nil, zip.Errorf(http.StatusConflict,
			"category %q still holds %d taxa; move or delete them first", id, n)
	}
	removed, err := o.s.store.DeleteCategory(ctx, owner, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete category: %v", err)
	}
	if !removed {
		return nil, zip.ErrNotFound("category not found")
	}
	return &deleted{Deleted: id}, nil
}

// PutTaxon creates or replaces one product and returns it as stored. The id in the
// URL is the one it is filed under whatever the body says. The category must
// already exist — a taxon naming a category that does not is refused with 400
// rather than stored where nothing can render it.
//
// A taxon opens exactly one way: `route` for a product the console renders
// itself, or `href` for one that genuinely lives at its own domain. Giving both,
// or neither, is refused.
//
// Platform SuperAdmin only.
//
// Example: {"name": "Vector", "description": "Managed vector search.", "category": "data", "icon": "Database", "route": "/vector", "tags": ["search"], "order": 3}
func (o ops) putTaxon(ctx context.Context, in *taxonIn) (*Taxon, error) {
	owner, err := writable(ctx)
	if err != nil {
		return nil, err
	}
	id, err := slug(in.ID)
	if err != nil {
		return nil, err
	}
	name, err := text("name", in.Name, maxShort, true)
	if err != nil {
		return nil, err
	}
	description, err := text("description", in.Description, maxLong, false)
	if err != nil {
		return nil, err
	}
	category, err := slug(strings.TrimSpace(in.Category))
	if err != nil {
		return nil, zip.ErrBadRequest("category is required and must be a slug")
	}
	icon, err := text("icon", in.Icon, maxShort, false)
	if err != nil {
		return nil, err
	}
	route, err := text("route", in.Route, maxShort, false)
	if err != nil {
		return nil, err
	}
	href, err := text("href", in.Href, maxShort, false)
	if err != nil {
		return nil, err
	}
	if (route == "") == (href == "") {
		return nil, zip.ErrBadRequest("give exactly one of route (an in-console product) or href (an external one)")
	}
	// Each has one shape, and neither is checked anywhere else: a route that is not
	// rooted resolves against whatever page the reader is on, and an href that is not
	// absolute is a launch tile that goes nowhere. Both render as a dead link long
	// after the write that made them succeeded.
	if route != "" && !strings.HasPrefix(route, "/") {
		return nil, zip.ErrBadRequest("route is an in-console path and must start with /")
	}
	if href != "" && !strings.HasPrefix(href, "https://") && !strings.HasPrefix(href, "http://") {
		return nil, zip.ErrBadRequest("href is an external URL and must start with https://")
	}
	tags, err := list("tags", in.Tags)
	if err != nil {
		return nil, err
	}
	brands, err := list("brands", in.Brands)
	if err != nil {
		return nil, err
	}
	known, err := o.s.store.HasCategory(ctx, owner, category)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "put taxon: %v", err)
	}
	if !known {
		return nil, zip.ErrBadRequest("no such category: " + category)
	}
	e := Taxon{
		Owner: owner, ID: id, Name: name, Description: description, Category: category,
		Tags: tags, Icon: icon, Route: route, Href: href, Brands: brands,
		Order: in.Order, Published: in.Published == nil || *in.Published,
	}
	if err := o.s.store.PutTaxon(ctx, e); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "put taxon: %v", err)
	}
	return &e, nil
}

// DeleteTaxon removes one product from the catalogue. An id no taxon holds is a
// 404. To take a product out of view without losing what was written about it, set
// `published` to false instead.
func (o ops) deleteTaxon(ctx context.Context, in *idIn) (*deleted, error) {
	owner, err := writable(ctx)
	if err != nil {
		return nil, err
	}
	id, err := slug(in.ID)
	if err != nil {
		return nil, err
	}
	removed, err := o.s.store.DeleteTaxon(ctx, owner, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete taxon: %v", err)
	}
	if !removed {
		return nil, zip.ErrNotFound("taxon not found")
	}
	return &deleted{Deleted: id}, nil
}

// ── the boundary ────────────────────────────────────────────────────────────

// shows applies a brand scope: an empty scope is every brand, and an empty query
// asks for everything. It is the ONE reading of the Brands field, so a category
// and a taxon are always scoped the same way.
func shows(brand string, scope []string) bool {
	if brand == "" || len(scope) == 0 {
		return true
	}
	for _, b := range scope {
		if b == brand {
			return true
		}
	}
	return false
}

// slug validates an id. It is deliberately narrow — lowercase, digits and the
// dash — because an id is a URL segment, a console route and a JSON key at once,
// and anything looser makes one of the three quote or escape it.
func slug(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", zip.ErrBadRequest("id is required")
	}
	if len(v) > maxID {
		return "", zip.ErrBadRequest("id is over 64 characters")
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return "", zip.ErrBadRequest("id may hold lowercase letters, digits and dashes only")
		}
	}
	return v, nil
}

// text bounds one display string. Everything here is copy a person types into an
// editor, so the only questions are whether it is there when it must be and
// whether one paste can make the catalogue enormous.
func text(field, v string, max int, required bool) (string, error) {
	v = strings.TrimSpace(v)
	if required && v == "" {
		return "", zip.ErrBadRequest(field + " is required")
	}
	if len(v) > max {
		return "", zip.Errorf(http.StatusBadRequest, "%s is over %d characters", field, max)
	}
	return v, nil
}

// list bounds and cleans one list of short slugs, dropping blanks so a trailing
// comma in the editor does not store an empty tag.
func list(field string, v []string) ([]string, error) {
	if len(v) > maxList {
		return nil, zip.Errorf(http.StatusBadRequest, "%s holds more than %d values", field, maxList)
	}
	var out []string
	for _, s := range v {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(s) > maxID {
			return nil, zip.Errorf(http.StatusBadRequest, "a %s value is over %d characters", field, maxID)
		}
		out = append(out, s)
	}
	return out, nil
}
