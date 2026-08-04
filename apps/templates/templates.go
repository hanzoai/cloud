// Package templates is a gallery of starter kits you can deploy as they come.
//
// The Hanzo starter-kit gallery at /v1/templates, in TWO layers that never mix:
//
//   - the PUBLIC catalog: deployable app/site scaffolds (source of truth:
//     hanzoai/gallery), vendored so the unified `cloud` binary ships it with no
//     external dependency. Reference content — embedded, immutable, and with NO
//     write route, so nothing a customer does can add to it.
//   - a customer's OWN templates: rows in {DataDir}/templates.db keyed by the
//     gateway-minted org (principal.Org — never a request field), PRIVATE to that
//     org. Only that org lists, reads, edits, deletes, or forks them.
//
// Two layers rather than one visibility flag is the whole safety argument: a
// private template cannot surface in the public hanzo.app catalog by
// CONSTRUCTION — it lives in a different container, reached only by a query that
// binds org — not by a filter every future reader has to remember. An anonymous
// GET never touches the store at all.
//
// A slug is single-valued across both layers: publishing over a public slug is
// 409, so a slug still names exactly one template and no org can shadow the
// gallery.
//
// ONE template is ONE entry. The shapes it ships in — format, page, theme — are
// Variants inside that entry, chosen at fork time.
//
// Surface — every route is a TYPED op (zip.Get[In, Out] and friends), so each is
// ONE registry entry the document, the MCP tool, the CLI command and the
// generated SDK method are all projected from:
//
//	GET    /v1/templates          public catalog + (validated caller) that org's own -> {data:[StarterKit]}
//	GET    /v1/templates/:slug    one kit: the caller org's own, else public         -> StarterKit
//	POST   /v1/templates          publish a kit PRIVATE to the caller's org          -> 201 StarterKit
//	PUT    /v1/templates/:slug    replace the caller org's own kit                   -> StarterKit
//	DELETE /v1/templates/:slug    delete the caller org's own kit                    -> 204
//
// The value is StarterKit, not Template: the OpenAPI schema namespace is FLAT
// across the whole fleet and apps/guide already publishes a `Template` (a Guide
// playbook prompt/snippet, {id,title,body,enabled}). openapi.Weave refuses one name
// with two shapes — every generated SDK would bind whichever it read last — so the
// name that was not yet published is the one that yields, qualified by the value's
// own vocabulary ("Template is one starter kit", below) rather than by a place.
package templates

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

//go:embed catalog.json
var catalogJSON []byte

// slugRE bounds a customer-published slug to the same DNS-ish label shape a
// project slug uses (clients/projects), so a template slug can always become the
// forked project's slug.
var slugRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

const (
	maxTitle = 200
	maxText  = 4096
	maxItems = 32
)

// StarterKit is one starter kit as the console gallery browser consumes it. The
// `Source`/`Preview` URLs point at the live gallery (gallery.hanzo.ai) for a
// public entry and at whatever the customer supplies for one of their own;
// `Demo` is the deployed site itself.
//
// Org is the OWNER of a private template, stamped by the SERVER from the row's
// key. It is empty on every public catalog entry — that emptiness is what the
// console badges "yours" on, and it is never read from a request body.
type StarterKit struct {
	Slug        string   `json:"slug"`        // the kit's identity — lowercase alphanumeric with dashes, max 40
	Title       string   `json:"title"`       // display name
	Category    string   `json:"category"`    // groups the kit in the gallery browser ("Portfolio", "SaaS")
	Description string   `json:"description"` // the browse-card blurb
	Framework   string   `json:"framework"`   // the stack the kit is built on ("Next.js 14.2 + TS")
	Features    []string `json:"features"`    // the highlights the card lists, at most 32
	UseCase     string   `json:"useCase"`     // what the kit is for, in a phrase
	// Tier is public-gallery curation, carried verbatim from the embedded catalog.
	// No request can set it — neither write body has the field and neither builds a
	// kit carrying one — so it is absent on every customer-published kit.
	Tier *int `json:"tier,omitempty"`
	// Rating is public-gallery curation, on the same terms as Tier: catalog-only,
	// never accepted from a request, absent on a customer's own kit.
	Rating   *float64  `json:"rating,omitempty"`
	Source   string    `json:"source"`             // the repository the kit is forked from
	Preview  string    `json:"preview"`            // the still image the browse card renders
	Demo     string    `json:"demo,omitempty"`     // live demo (<slug>.hanzo.app), when deployed
	Variants []Variant `json:"variants,omitempty"` // the shapes this template ships in
	Org      string    `json:"org,omitempty"`      // owner of a PRIVATE template; empty in the public catalog
}

// Variant is one SHAPE of a template: the same design in another format
// (html/react/bootstrap), on another page (folio's about/contact/grid-3), or in
// another theme. A variant is an option resolved at fork time from what the
// user asks for — never a catalog row of its own, which is what made one
// portfolio template read as 26 templates and one dashboard as 2.
type Variant struct {
	ID        string `json:"id"`                  // selector, unique within the template ("react", "grid-3-fluid")
	Label     string `json:"label"`               // human label for the picker
	Kind      string `json:"kind"`                // the axis it varies: format | page | theme
	Framework string `json:"framework,omitempty"` // only when it differs from the template's
	Source    string `json:"source"`              // the repository this shape is forked from; the synthesized default shape carries the template's own
}

// Variant resolves a variant id against the template and is the ONE place the
// resolution rule lives. The empty id means "no preference" and yields the
// template's first (default) shape; a template that ships in a single shape
// answers with itself, so callers never branch on len(Variants).
func (t StarterKit) Variant(id string) (Variant, bool) {
	if len(t.Variants) == 0 {
		if id != "" {
			return Variant{}, false
		}
		return Variant{ID: "default", Label: t.Title, Kind: "format",
			Framework: t.Framework, Source: t.Source}, true
	}
	if id == "" {
		id = t.Variants[0].ID
	}
	for _, v := range t.Variants {
		if v.ID == id {
			if v.Framework == "" {
				v.Framework = t.Framework
			}
			return v, true
		}
	}
	return Variant{}, false
}

// catalog decodes and validates the embedded gallery once (drops entries with no
// slug/title so a browse row can never be a dead card, and clears Org so a
// catalog entry can never claim to be some org's private template).
var catalog = sync.OnceValues(func() ([]StarterKit, error) {
	var all []StarterKit
	if err := json.Unmarshal(catalogJSON, &all); err != nil {
		return nil, fmt.Errorf("templates: decode embedded catalog: %w", err)
	}
	out := make([]StarterKit, 0, len(all))
	for _, t := range all {
		if t.Slug == "" || t.Title == "" {
			continue
		}
		if t.Features == nil {
			t.Features = []string{}
		}
		t.Org = ""
		out = append(out, t)
	}
	return out, nil
})

// state is templates' own data: the per-org private store. The public catalog is
// not in it — that stays the package-level embedded OnceValues.
type state struct{ store *Store }

var mounted *cloud.Service[state]

// Mount registers the templates surface. templates is a "complex" mount now
// (a package-global `mounted` so Lookup is ONE door for the projects fork flow,
// and a shutdown that closes the store), so it builds the Service value directly.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("templates.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("templates.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("templates.Mount: empty DataDir")
	}
	// templates registers TYPED ops, which live on the *zip.App's registry — the
	// one value OpenAPI, MCP and the CLI are projected from. A Router that is not
	// backed by one must fail the mount rather than serve routes no projection knows.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("templates.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	// Validate the embedded gallery here, failing the mount closed on a malformed
	// catalog rather than on the first browse.
	if _, err := catalog(); err != nil {
		return fmt.Errorf("templates.Mount: %w", err)
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("templates.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "templates"), State: state{store: store}}
	mounted = s

	routes(app, zapp, s)

	s.Log.Info("templates gallery", "prefix", "/v1/templates", "brand", deps.Brand)
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the whole gallery surface as TYPED ops: one registry entry
// each, which is what the document, the MCP tool, the CLI command and the
// generated SDK method are all projected from.
//
// Every op takes the ABSOLUTE path on the app rather than a leaf on the group,
// because the collection root IS /v1/templates: declaring it as the empty leaf
// of a Group("/v1/templates") names /v1/templates/ — a path this API has never
// served — and op.Path is the identity every projection keys on.
//
// Registration order is match order: the static collection before the :slug
// forms, exactly as before.
func routes(app cloud.Router, zapp *zip.App, s *cloud.Service[state]) {
	// A typed op receives only a context, so the validated org reaches it from the
	// request on that context. Whoever composes the app parks it there — at the
	// root, ahead of these leaves, since fiber runs middleware in registration
	// order. This surface installs none of its own: one it installed for itself
	// could only hang on a /v1/templates node, and every op below registers on
	// zapp, the root app, so that node would carry middleware over an empty subtree
	// and zip refuses to compose it.

	o := ops{s: s}
	zip.Get(zapp, "/v1/templates", o.browse)
	zip.Post(zapp, "/v1/templates", o.publish, zip.WithStatus(http.StatusCreated))
	zip.Get(zapp, "/v1/templates/:slug", o.get)
	zip.Put(zapp, "/v1/templates/:slug", o.replace)
	zip.Delete(zapp, "/v1/templates/:slug", o.remove)
}

// ops binds the store to the typed ops. A TypedHandler takes no service
// parameter, so the service arrives as a RECEIVER and every op is a method value
// — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// tenant is the VALIDATED org for a typed op — the one the gateway asserted and
// cloud.Bridge parked on the context, never a field of In. An In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the
// caller asserted for itself. Fails closed off the HTTP path.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return org, nil
}

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. It is an
// ALIAS for the unnamed empty struct, not a definition: zip keys the response on
// 204 only when the Out type has no name, so a defined type here would publish
// "200 with a body" about a route that answers 204 with none.
type noContent = struct{}

// kitRef addresses one starter kit. The slug is the path segment: the URL is the
// addressing authority, so it binds from there whatever a body says.
type kitRef struct {
	// Slug is the starter kit to act on, from the path.
	Slug string `json:"slug"`
}

// kitList is the gallery as one browse answers it.
type kitList struct {
	// Data is the public catalog followed by the caller org's own kits.
	Data []StarterKit `json:"data"`
}

// publishKitIn is a starter kit published PRIVATE to the caller's org.
//
// The fields are spelled out rather than embedded from a shared body struct
// because zipdoc keys a field's prose on the OUTER type's name and go/types does
// not promote an embedded struct's fields, so an embedded carrier publishes its
// shape with no prose on any field of it.
//
// `url:"-"` on every field is what keeps this a BODY: zip's binder fills an In
// field from the query string as well as the body, and this route has never taken
// a kit's fields there — without the opt-out `?slug=other` would silently
// redirect the write the body asked for.
//
// Tier and Rating are absent by design: they are public-gallery curation, the
// server clears them on every write, and a request property the server always
// discards is one a generated client should never offer.
type publishKitIn struct {
	// Slug is the kit's identity — lowercase alphanumeric with dashes, max 40.
	Slug string `json:"slug" url:"-"`
	// Title is the display name. Required, max 200 characters.
	Title string `json:"title" url:"-"`
	// Category groups the kit in the gallery browser.
	Category string `json:"category" url:"-"`
	// Description is the browse-card blurb, max 4096 characters.
	Description string `json:"description" url:"-"`
	// Framework is the stack the kit is built on ("Next.js 14").
	Framework string `json:"framework" url:"-"`
	// Features are the highlights the card lists, at most 32.
	Features []string `json:"features" url:"-"`
	// UseCase is what the kit is for, in a phrase.
	UseCase string `json:"useCase" url:"-"`
	// Source is the repository the kit is forked from, max 4096 characters.
	Source string `json:"source" url:"-"`
	// Preview is the still image the browse card renders, max 4096 characters.
	Preview string `json:"preview" url:"-"`
	// Demo is the deployed site itself, when there is one.
	Demo string `json:"demo" url:"-"`
	// Variants are the shapes this kit ships in, at most 32; the fork picks one.
	Variants []Variant `json:"variants" url:"-"`
}

// replaceKitIn is a full replacement of the caller org's own starter kit. The
// slug comes from the PATH — it binds last, so the body can never rename a kit —
// and every other field carries `url:"-"` for the reason publishKitIn names.
type replaceKitIn struct {
	// Slug is the kit to replace, from the path.
	Slug string `json:"slug"`
	// Title is the display name. Required, max 200 characters.
	Title string `json:"title" url:"-"`
	// Category groups the kit in the gallery browser.
	Category string `json:"category" url:"-"`
	// Description is the browse-card blurb, max 4096 characters.
	Description string `json:"description" url:"-"`
	// Framework is the stack the kit is built on ("Next.js 14").
	Framework string `json:"framework" url:"-"`
	// Features are the highlights the card lists, at most 32.
	Features []string `json:"features" url:"-"`
	// UseCase is what the kit is for, in a phrase.
	UseCase string `json:"useCase" url:"-"`
	// Source is the repository the kit is forked from, max 4096 characters.
	Source string `json:"source" url:"-"`
	// Preview is the still image the browse card renders, max 4096 characters.
	Preview string `json:"preview" url:"-"`
	// Demo is the deployed site itself, when there is one.
	Demo string `json:"demo" url:"-"`
	// Variants are the shapes this kit ships in, at most 32; the fork picks one.
	Variants []Variant `json:"variants" url:"-"`
}

// kit is the one StarterKit value a write echoes, built from the fields a caller
// may set. It is the ONE place the publish and replace bodies become a kit, so
// the two routes cannot drift in what they accept.
func (in publishKitIn) kit() StarterKit {
	return StarterKit{
		Slug: in.Slug, Title: in.Title, Category: in.Category, Description: in.Description,
		Framework: in.Framework, Features: in.Features, UseCase: in.UseCase,
		Source: in.Source, Preview: in.Preview, Demo: in.Demo, Variants: in.Variants,
	}
}

// kit is replaceKitIn's counterpart, and the slug it carries is the path's.
func (in replaceKitIn) kit() StarterKit {
	return publishKitIn{
		Slug: in.Slug, Title: in.Title, Category: in.Category, Description: in.Description,
		Framework: in.Framework, Features: in.Features, UseCase: in.UseCase,
		Source: in.Source, Preview: in.Preview, Demo: in.Demo, Variants: in.Variants,
	}.kit()
}

// browse lists the public starter-kit catalog plus, for a validated caller, that
// org's own private kits. No request field can widen the scope: the org comes
// from the validated principal, so an anonymous or cross-org caller structurally
// sees the public catalog only.
func (o ops) browse(ctx context.Context, _ *noInput) (*kitList, error) {
	cat, err := catalog()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "templates: %v", err)
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return &kitList{Data: cat}, nil
	}
	mine, err := o.s.State.store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "templates: %v", err)
	}
	// Copy rather than append onto cat: cat is the shared package slice and can
	// carry spare capacity, so appending would write this caller's private rows
	// into the catalog every other request reads.
	out := make([]StarterKit, 0, len(cat)+len(mine))
	return &kitList{Data: append(append(out, cat...), mine...)}, nil
}

// get returns one starter kit: the caller org's own by that slug, else the public
// catalog's. A slug another org owns reads as not found.
//
// Example: {"slug": "folio"}
func (o ops) get(ctx context.Context, in *kitRef) (*StarterKit, error) {
	org, _ := principal.OrgFrom(ctx) // "" for an anonymous caller — public catalog only
	t, ok := Lookup(ctx, org, strings.ToLower(strings.TrimSpace(in.Slug)))
	if !ok {
		return nil, zip.ErrNotFound("template not found")
	}
	return &t, nil
}

// publish creates a starter kit PRIVATE to the caller's org and answers 201 with
// the stored kit. The owner is stamped by the server, so a body "org" is never
// trusted; publishing over a public-catalog slug is 409, so a slug still names
// exactly one kit.
//
// Example: {"slug": "acme-portal", "title": "Acme Internal Portal", "framework": "Next.js 14"}
func (o ops) publish(ctx context.Context, in *publishKitIn) (*StarterKit, error) {
	return o.write(ctx, in.kit(), true)
}

// replace overwrites the caller org's OWN starter kit at the path slug, answering
// the stored kit. A slug they do not own is 404, never a create: the UPDATE binds
// org, so a PUT can never reach another org's kit.
//
// Example: {"slug": "acme-portal", "title": "Acme Internal Portal v2"}
func (o ops) replace(ctx context.Context, in *replaceKitIn) (*StarterKit, error) {
	return o.write(ctx, in.kit(), false)
}

// remove deletes the caller org's OWN starter kit. A slug they do not own is a
// 404, never a delete: the DELETE binds org.
//
// Example: {"slug": "acme-portal"}
func (o ops) remove(ctx context.Context, in *kitRef) (*noContent, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	gone, err := o.s.State.store.Delete(ctx, org, strings.ToLower(strings.TrimSpace(in.Slug)))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "templates: %v", err)
	}
	if !gone {
		return nil, zip.ErrNotFound("template not found")
	}
	return nil, nil
}

// write is the ONE publish/replace path: validate, stamp the SERVER's org, store.
// create=true inserts (409 on a slug the org already holds), create=false
// replaces (404 when they hold none).
func (o ops) write(ctx context.Context, t StarterKit, create bool) (*StarterKit, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	t.Slug = strings.ToLower(strings.TrimSpace(t.Slug))
	if !slugRE.MatchString(t.Slug) {
		return nil, zip.ErrBadRequest("slug must be lowercase alphanumeric with dashes (max 40)")
	}
	if t.Title = strings.TrimSpace(t.Title); t.Title == "" || len(t.Title) > maxTitle {
		return nil, zip.ErrBadRequest("title is required (max 200)")
	}
	if len(t.Description) > maxText || len(t.Source) > maxText || len(t.Preview) > maxText {
		return nil, zip.ErrBadRequest("description/source/preview too long")
	}
	if len(t.Features) > maxItems || len(t.Variants) > maxItems {
		return nil, zip.ErrBadRequest("too many features/variants")
	}
	if t.Features == nil {
		t.Features = []string{}
	}
	// One slug, one kit: an org may not publish over a public-catalog slug, so
	// forking that slug can never mean two different things.
	if _, clash := public(t.Slug); clash {
		return nil, zip.ErrConflict("slug is taken by the public catalog")
	}
	t.Org = org // SERVER-stamped owner; a body "org" is overwritten, never trusted

	switch err := o.s.State.store.Put(ctx, t, create, time.Now().Unix()); {
	case errors.Is(err, errConflict):
		return nil, zip.ErrConflict("template already exists")
	case errors.Is(err, sql.ErrNoRows):
		return nil, zip.ErrNotFound("template not found")
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "templates: %v", err)
	}
	return &t, nil
}

// Shutdown closes the per-org store. Idempotent.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	var err error
	if mounted.State.store != nil {
		err = mounted.State.store.Close()
	}
	mounted = nil
	return err
}

// List returns the validated PUBLIC starter-kit catalog (the SAME slice the
// anonymous HTTP GET serves). Read-only reference content; callers must not
// mutate the returned slice. Private org templates are deliberately NOT here —
// they are reachable only through Lookup, which takes the org it isolates on.
func List() ([]StarterKit, error) { return catalog() }

// Lookup resolves ONE template for a caller org: that org's OWN private template
// first, then the public catalog. It is the single door other subsystems (the
// projects fork flow) read templates through, so "which templates may this org
// use" is answered in exactly one place. org "" (anonymous/unvalidated) resolves
// against the public catalog only.
func Lookup(ctx context.Context, org, slug string) (StarterKit, bool) {
	if org != "" && mounted != nil {
		t, ok, err := mounted.State.store.Get(ctx, org, slug)
		if err != nil {
			mounted.Log.Warn("template lookup", "org", org, "slug", slug, "err", err)
		}
		if ok {
			return t, true
		}
	}
	return public(slug)
}

// public returns the PUBLIC-catalog template with the given slug.
func public(slug string) (StarterKit, bool) {
	cat, err := catalog()
	if err != nil {
		return StarterKit{}, false
	}
	for _, t := range cat {
		if t.Slug == slug {
			return t, true
		}
	}
	return StarterKit{}, false
}
