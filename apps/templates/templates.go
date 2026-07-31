// Package templates mounts /v1/templates — the Hanzo starter-kit gallery, in TWO
// layers that never mix:
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
// Surface:
//
//	GET    /v1/templates          public catalog + (validated caller) that org's own -> {data:[Template]}
//	GET    /v1/templates/:slug    one template: the caller org's own, else public     -> Template
//	POST   /v1/templates          publish a template PRIVATE to the caller's org      -> 201 Template
//	PUT    /v1/templates/:slug    replace the caller org's own template               -> Template
//	DELETE /v1/templates/:slug    delete the caller org's own template                -> 204
package templates

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

// Template is one starter kit as the console gallery browser consumes it. The
// `Source`/`Preview` URLs point at the live gallery (gallery.hanzo.ai) for a
// public entry and at whatever the customer supplies for one of their own;
// `Demo` is the deployed site itself.
//
// Org is the OWNER of a private template, stamped by the SERVER from the row's
// key. It is empty on every public catalog entry — that emptiness is what the
// console badges "yours" on, and it is never read from a request body.
type Template struct {
	// Slug is the template's id: lowercase alphanumeric with dashes, max 40. On a
	// replace the path owns it and a body slug is ignored.
	Slug string `json:"slug"`
	// Title is the display name; required, max 200 characters.
	Title string `json:"title"`
	// Category groups the entry in the gallery browser.
	Category string `json:"category"`
	// Description is the gallery blurb, max 4096 characters.
	Description string `json:"description"`
	// Framework is the stack the kit is built on ("next", "astro", …).
	Framework string `json:"framework"`
	// Features are the selling-point bullets, max 32.
	Features []string `json:"features"`
	// UseCase is what the kit is for, in one phrase.
	UseCase string `json:"useCase"`
	// Tier is a public-gallery curation rank; server-owned, ignored on a write.
	Tier *int `json:"tier,omitempty"`
	// Rating is a public-gallery curation score; server-owned, ignored on a write.
	Rating *float64 `json:"rating,omitempty"`
	// Source is the repository the fork clones from, max 4096 characters.
	Source string `json:"source"`
	// Preview is the screenshot or preview URL, max 4096 characters.
	Preview  string    `json:"preview"`
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
	Source    string `json:"source"`
}

// Variant resolves a variant id against the template and is the ONE place the
// resolution rule lives. The empty id means "no preference" and yields the
// template's first (default) shape; a template that ships in a single shape
// answers with itself, so callers never branch on len(Variants).
func (t Template) Variant(id string) (Variant, bool) {
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
var catalog = sync.OnceValues(func() ([]Template, error) {
	var all []Template
	if err := json.Unmarshal(catalogJSON, &all); err != nil {
		return nil, fmt.Errorf("templates: decode embedded catalog: %w", err)
	}
	out := make([]Template, 0, len(all))
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
	// Validate the embedded gallery here, failing the mount closed on a malformed
	// catalog rather than on the first browse.
	if _, err := catalog(); err != nil {
		return fmt.Errorf("templates.Mount: %w", err)
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("templates.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "templates.db"))
	if err != nil {
		return fmt.Errorf("templates.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "templates"), State: state{store: store}}
	mounted = s

	// Every route is a TYPED op, and the op registry — the one value OpenAPI, MCP
	// and the CLI are projected from — lives on the *zip.App. A Router that is not
	// backed by one has nowhere to put them, so the mount fails rather than serving
	// routes no projection knows about.
	z := cloud.ZipApp(app)
	if z == nil {
		return fmt.Errorf("templates.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	o := ops{s: s}
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every op below resolves
	// its (optional) org through it. Bounded to templates' own subtree.
	app.Group("/v1/templates").Use(cloud.Bridge())
	// Absolute paths: the registry keys on the path, not on a group prefix.
	zip.Get(z, "/v1/templates", o.list)
	zip.Post(z, "/v1/templates", o.publish, zip.WithStatus(http.StatusCreated))
	zip.Get(z, "/v1/templates/:slug", o.get)
	zip.Put(z, "/v1/templates/:slug", o.replace)
	zip.Delete(z, "/v1/templates/:slug", o.remove)

	s.Log.Info("templates gallery", "prefix", "/v1/templates", "brand", deps.Brand)
	return nil
}

// ops binds the service to templates' typed handlers: a typed handler takes only
// a context and its decoded In, so the service arrives as a RECEIVER.
type ops struct{ s *cloud.Service[state] }

// TemplateRef addresses one template by slug.
type TemplateRef struct {
	// Slug is the template's id, lowercase alphanumeric with dashes.
	Slug string `json:"slug"`
}

// TemplateList is the gallery read: the public catalog, plus the caller org's own
// private templates when the caller is validated.
type TemplateList struct {
	// Data is the templates, public entries first.
	Data []Template `json:"data"`
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
func List() ([]Template, error) { return catalog() }

// Lookup resolves ONE template for a caller org: that org's OWN private template
// first, then the public catalog. It is the single door other subsystems (the
// projects fork flow) read templates through, so "which templates may this org
// use" is answered in exactly one place. org "" (anonymous/unvalidated) resolves
// against the public catalog only.
func Lookup(ctx context.Context, org, slug string) (Template, bool) {
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
func public(slug string) (Template, bool) {
	cat, err := catalog()
	if err != nil {
		return Template{}, false
	}
	for _, t := range cat {
		if t.Slug == slug {
			return t, true
		}
	}
	return Template{}, false
}

// list returns the public starter-kit catalog plus, for a validated caller, that org's own templates.
// No request field can widen the scope: the org comes from the gateway-minted,
// JWT-validated principal, so an anonymous or cross-org caller structurally sees
// the public catalog only.
//
// Response: {"data": [{"slug": "folio", "title": "Portfolio", "framework": "next"}]}
func (o ops) list(ctx context.Context, _ *struct{}) (*TemplateList, error) {
	cat, err := catalog()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "templates: %v", err)
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return &TemplateList{Data: cat}, nil
	}
	mine, err := o.s.State.store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "templates: %v", err)
	}
	// Copy rather than append onto cat: cat is the shared package slice and can
	// carry spare capacity, so appending would write this caller's private rows
	// into the catalog every other request reads.
	out := make([]Template, 0, len(cat)+len(mine))
	return &TemplateList{Data: append(append(out, cat...), mine...)}, nil
}

// get returns one template: the caller org's own if they have that slug, else the public one.
// An unknown slug is a 404; an anonymous caller resolves against the public catalog only.
//
// Example: {"slug": "folio"}
func (o ops) get(ctx context.Context, in *TemplateRef) (*Template, error) {
	org, _ := principal.OrgFrom(ctx) // "" for an anonymous caller — public catalog only
	t, ok := Lookup(ctx, org, strings.ToLower(strings.TrimSpace(in.Slug)))
	if !ok {
		return nil, zip.ErrNotFound("template not found")
	}
	return &t, nil
}

// publish creates a template private to the caller's org and answers 201.
// The slug must be free in both layers — publishing over a public-catalog slug is
// 409 — and the owner, tier and rating are stamped by the server, never by the body.
//
// Example: {"slug": "acme-landing", "title": "Acme Landing", "framework": "next", "source": "https://github.com/acme/landing"}
func (o ops) publish(ctx context.Context, in *Template) (*Template, error) {
	return o.write(ctx, in, "")
}

// replace overwrites the caller org's own template at :slug, keeping the path's identity.
// A slug the caller does not own is a 404 — the UPDATE binds org, so a PUT can
// never reach another org's row — and the body cannot rename the template.
//
// Example: {"slug": "acme-landing", "title": "Acme Landing v2", "framework": "next"}
func (o ops) replace(ctx context.Context, in *Template) (*Template, error) {
	return o.write(ctx, in, in.Slug)
}

// write is the ONE publish/replace path: validate, stamp the SERVER's org, store.
// slug=="" means create. On replace the path already owns the identity: the path
// param binds onto Template.Slug AFTER the body is decoded, so the body cannot rename.
func (o ops) write(ctx context.Context, in *Template, slug string) (*Template, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	t := *in
	if slug != "" {
		t.Slug = slug // on replace the path owns the identity; the body cannot rename
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
	// One slug, one template: an org may not publish over a public-catalog slug,
	// so forking that slug can never mean two different things.
	if _, clash := public(t.Slug); clash {
		return nil, zip.ErrConflict("slug is taken by the public catalog")
	}
	// Curation fields belong to the public gallery, not to a customer to assert.
	t.Tier, t.Rating = nil, nil
	t.Org = org // SERVER-stamped owner; a body "org" is overwritten, never trusted

	err := o.s.State.store.Put(ctx, t, slug == "", time.Now().Unix())
	switch {
	case errors.Is(err, errConflict):
		return nil, zip.ErrConflict("template already exists")
	case errors.Is(err, sql.ErrNoRows):
		return nil, zip.ErrNotFound("template not found")
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "templates: %v", err)
	}
	return &t, nil
}

// remove deletes the caller org's own template and answers 204.
// A slug they do not own is a 404, never a delete: the DELETE binds org.
//
// Example: {"slug": "acme-landing"}
func (o ops) remove(ctx context.Context, in *TemplateRef) (*struct{}, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
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
