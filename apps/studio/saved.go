package studio

// saved.go — the four things a person saves: templates, flows, documents and
// brands. Each is one row of the docs table, its wire value as the body, its
// creator and whether the org shares it beside. A person reads their own and what
// the org shares, and changes only their own.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/orm/tenant"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk/mint"
)

// The kinds a template, a flow or a run may make.
var kinds = map[string]bool{
	"image": true, "poster": true, "document": true, "slides": true, "type": true, "model": true,
	"ad": true, "video": true, "film": true, "voice": true, "music": true, "other": true,
}

// Bounds on what is saved.
const (
	maxTitle = 120
	maxText  = 2000
	maxDoc   = 512 << 10 // a document's IR, as stored
	maxBrand = 64 << 10  // a brand's tokens, as stored
	maxSaved = 2000      // rows of one kind one org holds
)

// put writes a saved row, new or replacing the caller's own.
func (s *state) put(ctx context.Context, kind, id, me, sort string, shared bool, body any, created int64) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	row := docRow{Kind: kind, ID: id, Creator: me, Shared: shared, Sort: sort, Body: string(b), Created: created, Updated: now()}
	if created == 0 {
		n, err := s.db.Select[docRow]("docs", "id").Where(tenant.Eq("kind", kind)).Count(ctx)
		if err != nil {
			return err
		}
		if n >= maxSaved {
			return zip.ErrConflict("this org holds as many of these as it may; delete one")
		}
		row.Created = row.Updated
		return s.db.Insert(ctx, "docs", row)
	}
	_, err = s.db.Update(ctx, "docs", tenant.Row{"shared": shared, "sort": sort, "body": row.Body, "updated": row.Updated},
		tenant.And(tenant.Eq("kind", kind), tenant.Eq("id", id), mine(me)))
	return err
}

// got reads one saved row the caller may read, decoded into v.
func (s *state) got(ctx context.Context, kind, me, id string, v any) (docRow, error) {
	r, ok, err := s.db.Select[docRow]("docs").Where(tenant.And(tenant.Eq("kind", kind), tenant.Eq("id", id), visible(me))).First(ctx)
	if err != nil {
		return docRow{}, err
	}
	if !ok {
		return docRow{}, gone(kind)
	}
	if err := json.Unmarshal([]byte(r.Body), v); err != nil {
		return docRow{}, err
	}
	return r, nil
}

// list pages the saved rows of a kind the caller may read, most recently changed
// first.
func (s *state) list(ctx context.Context, kind, me, sort string, limit int, cursor string) ([]docRow, string, error) {
	cond := tenant.And(tenant.Eq("kind", kind), visible(me))
	if sort != "" {
		cond = tenant.And(cond, tenant.Eq("sort", sort))
	}
	limit, after, err := paging(limit, cursor)
	if err != nil {
		return nil, "", err
	}
	if after != nil {
		after.col = "updated"
		cond = tenant.And(cond, after.before())
	}
	rows, err := s.db.Select[docRow]("docs").Where(cond).OrderBy("updated DESC", "id DESC").Limit(limit + 1).All(ctx)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = cursorOf(rows[limit-1].Updated, rows[limit-1].ID)
	}
	return rows, next, nil
}

// remove deletes one of the caller's own saved rows.
func (s *state) remove(ctx context.Context, kind, me, id string) error {
	n, err := s.db.Delete(ctx, "docs", tenant.And(tenant.Eq("kind", kind), tenant.Eq("id", id), mine(me)))
	if err != nil {
		return err
	}
	if n == 0 {
		return zip.ErrNotFound(kind + " not found")
	}
	return nil
}

// stamped fills a saved value's owner and times from its row.
func stamped(ctx context.Context, r docRow) (org, creator string, shared bool, created, updated string) {
	return orgOf(ctx), r.Creator, r.Shared, stamp(r.Created), stamp(r.Updated)
}

func title(s string, max int, what string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > max {
		return "", zip.ErrBadRequest(what + " is 1 to " + itoa(max) + " characters")
	}
	return s, nil
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// ── templates ────────────────────────────────────────────────────────────

func (s *state) template(ctx context.Context, me, id string) (Template, error) {
	if t, ok := catalogTemplate(id); ok {
		return t, nil
	}
	var t Template
	r, err := s.got(ctx, "template", me, id, &t)
	if err != nil {
		return Template{}, err
	}
	t.ID = r.ID
	t.Org, t.Creator, t.Shared, t.Created, t.Updated = stamped(ctx, r)
	return t, nil
}

// Lists templates: the public catalog (org empty) first, then your org's own
// that you may read. Filter by kind. A cursor pages your org's templates.
func (o ops) templates(ctx context.Context, in *templatesIn) (*TemplatePage, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	out := &TemplatePage{Data: []Template{}}
	if in.Cursor == "" {
		for _, t := range catalog() {
			if in.Kind == "" || t.Kind == in.Kind {
				out.Data = append(out.Data, t)
			}
		}
	}
	rows, next, err := o.s.State.list(ctx, "template", me, in.Kind, in.Limit, in.Cursor)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		var t Template
		if json.Unmarshal([]byte(r.Body), &t) != nil {
			continue
		}
		t.ID = r.ID
		t.Org, t.Creator, t.Shared, t.Created, t.Updated = stamped(ctx, r)
		out.Data = append(out.Data, t)
	}
	out.Next = next
	return out, nil
}

// Reads one template: a catalog slug or one of your org's templates you may read.
func (o ops) templateGet(ctx context.Context, in *ref) (*Template, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	t, err := o.s.State.template(ctx, me, in.ID)
	if err != nil {
		return nil, notFound(err)
	}
	return &t, nil
}

// Saves a template to your org: a graph and the inputs schema its runs are
// started with. It is yours until you share it.
//
// Example: {"kind": "poster", "title": "Event poster", "graph": {"steps": [{"id": "html", "op": "chat", "with": {"prompt": "{{input.brief}}"}}]}}
func (o ops) templateCreate(ctx context.Context, in *TemplateWrite) (*Template, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	t, err := o.templateFrom(Template{Kind: in.Kind, Title: in.Title, Description: in.Description, Inputs: in.Inputs, Graph: in.Graph, Preview: in.Preview})
	if err != nil {
		return nil, err
	}
	id := mint.ID("tpl")
	if err := o.s.State.put(ctx, "template", id, me, t.Kind, in.Shared, t, 0); err != nil {
		return nil, err
	}
	return o.templateGet(ctx, &ref{ID: id})
}

// templateFrom checks a template.
func (o ops) templateFrom(t Template) (Template, error) {
	var err error
	if t.Title, err = title(t.Title, maxTitle, "title"); err != nil {
		return t, err
	}
	if !kinds[t.Kind] {
		return t, zip.ErrBadRequest("kind is one of image, poster, document, slides, type, model, ad, video, film, voice, music or other")
	}
	if len(t.Description) > maxText {
		return t, zip.ErrBadRequest("description is at most 2000 characters")
	}
	if _, err := check(t.Graph); err != nil {
		return t, err
	}
	if t.Inputs == nil {
		t.Inputs = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	t.ID, t.Org, t.Creator, t.Created, t.Updated = "", "", "", "", ""
	return t, nil
}

// Changes one of your templates; absent fields stay as they are. Catalog
// templates cannot be changed.
func (o ops) templatePatch(ctx context.Context, in *TemplatePatch) (*Template, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	var t Template
	r, err := o.s.State.got(ctx, "template", me, in.ID, &t)
	if err != nil || r.Creator != me {
		return nil, zip.ErrNotFound("template not found")
	}
	if in.Title != nil {
		t.Title = *in.Title
	}
	if in.Description != nil {
		t.Description = *in.Description
	}
	if in.Inputs != nil {
		t.Inputs = in.Inputs
	}
	if in.Graph != nil {
		t.Graph = *in.Graph
	}
	if in.Preview != nil {
		t.Preview = *in.Preview
	}
	shared := r.Shared
	if in.Shared != nil {
		shared = *in.Shared
	}
	if t, err = o.templateFrom(t); err != nil {
		return nil, err
	}
	if err := o.s.State.put(ctx, "template", in.ID, me, t.Kind, shared, t, r.Created); err != nil {
		return nil, err
	}
	return o.templateGet(ctx, &ref{ID: in.ID})
}

// Deletes one of your templates.
func (o ops) templateDelete(ctx context.Context, in *ref) (*cloudUnit, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	return nil, o.s.State.remove(ctx, "template", me, in.ID)
}

// ── flows ────────────────────────────────────────────────────────────────

func (s *state) flow(ctx context.Context, me, id string) (Flow, error) {
	var f Flow
	r, err := s.got(ctx, "flow", me, id, &f)
	if err != nil {
		return Flow{}, err
	}
	f.ID = r.ID
	f.Org, f.Creator, f.Shared, f.Created, f.Updated = stamped(ctx, r)
	return f, nil
}

func flowFrom(f Flow) (Flow, error) {
	var err error
	if f.Name, err = title(f.Name, maxTitle, "name"); err != nil {
		return f, err
	}
	if f.Kind != "" && !kinds[f.Kind] {
		return f, zip.ErrBadRequest("kind is one of image, poster, document, slides, type, model, ad, video, film, voice, music or other")
	}
	if len(f.Description) > maxText {
		return f, zip.ErrBadRequest("description is at most 2000 characters")
	}
	if _, err := check(f.Graph); err != nil {
		return f, err
	}
	f.ID, f.Org, f.Creator, f.Created, f.Updated = "", "", "", "", ""
	return f, nil
}

// Lists the flows you may read, most recently changed first.
func (o ops) flows(ctx context.Context, in *pageIn) (*FlowPage, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := o.s.State.list(ctx, "flow", me, "", in.Limit, in.Cursor)
	if err != nil {
		return nil, err
	}
	out := &FlowPage{Data: []Flow{}, Next: next}
	for _, r := range rows {
		var f Flow
		if json.Unmarshal([]byte(r.Body), &f) != nil {
			continue
		}
		f.ID = r.ID
		f.Org, f.Creator, f.Shared, f.Created, f.Updated = stamped(ctx, r)
		out.Data = append(out.Data, f)
	}
	return out, nil
}

// Reads one flow you may read.
func (o ops) flowGet(ctx context.Context, in *ref) (*Flow, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	f, err := o.s.State.flow(ctx, me, in.ID)
	if err != nil {
		return nil, notFound(err)
	}
	return &f, nil
}

// Saves a graph as a flow of yours. Start it with POST /v1/studio/runs {"flow": id}.
func (o ops) flowCreate(ctx context.Context, in *FlowWrite) (*Flow, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	f, err := flowFrom(Flow{Name: in.Name, Description: in.Description, Kind: in.Kind, Inputs: in.Inputs, Graph: in.Graph})
	if err != nil {
		return nil, err
	}
	id := mint.ID("flw")
	if err := o.s.State.put(ctx, "flow", id, me, "", in.Shared, f, 0); err != nil {
		return nil, err
	}
	return o.flowGet(ctx, &ref{ID: id})
}

// Changes one of your flows; absent fields stay as they are.
func (o ops) flowPatch(ctx context.Context, in *FlowPatch) (*Flow, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	var f Flow
	r, err := o.s.State.got(ctx, "flow", me, in.ID, &f)
	if err != nil || r.Creator != me {
		return nil, zip.ErrNotFound("flow not found")
	}
	if in.Name != nil {
		f.Name = *in.Name
	}
	if in.Description != nil {
		f.Description = *in.Description
	}
	if in.Kind != nil {
		f.Kind = *in.Kind
	}
	if in.Inputs != nil {
		f.Inputs = in.Inputs
	}
	if in.Graph != nil {
		f.Graph = *in.Graph
	}
	shared := r.Shared
	if in.Shared != nil {
		shared = *in.Shared
	}
	if f, err = flowFrom(f); err != nil {
		return nil, err
	}
	if err := o.s.State.put(ctx, "flow", in.ID, me, "", shared, f, r.Created); err != nil {
		return nil, err
	}
	return o.flowGet(ctx, &ref{ID: in.ID})
}

// Deletes one of your flows.
func (o ops) flowDelete(ctx context.Context, in *ref) (*cloudUnit, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	return nil, o.s.State.remove(ctx, "flow", me, in.ID)
}

// ── documents ────────────────────────────────────────────────────────────

func (s *state) document(ctx context.Context, me, id string) (Document, error) {
	var d Document
	r, err := s.got(ctx, "document", me, id, &d)
	if err != nil {
		return Document{}, err
	}
	d.ID = r.ID
	d.Org, d.Creator, d.Shared, d.Created, d.Updated = stamped(ctx, r)
	return d, nil
}

func documentFrom(d Document) (Document, error) {
	var err error
	if d.Title, err = title(d.Title, 200, "title"); err != nil {
		return d, err
	}
	switch d.Kind {
	case "doc":
		if len(d.IR.Blocks) == 0 || len(d.IR.Blocks) > 2000 || len(d.IR.Slides) > 0 {
			return d, zip.ErrBadRequest("a doc is 1 to 2000 blocks and no slides")
		}
	case "deck":
		if len(d.IR.Slides) == 0 || len(d.IR.Slides) > 200 || len(d.IR.Blocks) > 0 {
			return d, zip.ErrBadRequest("a deck is 1 to 200 slides and no blocks")
		}
	default:
		return d, zip.ErrBadRequest("kind is doc or deck")
	}
	if b, _ := json.Marshal(d.IR); len(b) > maxDoc {
		return d, zip.ErrBadRequest("a document is at most 512 KiB")
	}
	d.ID, d.Org, d.Creator, d.Created, d.Updated = "", "", "", "", ""
	return d, nil
}

// Lists the documents and decks you may read, most recently changed first.
func (o ops) documents(ctx context.Context, in *documentsIn) (*DocumentPage, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := o.s.State.list(ctx, "document", me, in.Kind, in.Limit, in.Cursor)
	if err != nil {
		return nil, err
	}
	out := &DocumentPage{Data: []Document{}, Next: next}
	for _, r := range rows {
		var d Document
		if json.Unmarshal([]byte(r.Body), &d) != nil {
			continue
		}
		d.ID = r.ID
		d.Org, d.Creator, d.Shared, d.Created, d.Updated = stamped(ctx, r)
		out.Data = append(out.Data, d)
	}
	return out, nil
}

// Reads one document you may read.
func (o ops) documentGet(ctx context.Context, in *ref) (*Document, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	d, err := o.s.State.document(ctx, me, in.ID)
	if err != nil {
		return nil, notFound(err)
	}
	return &d, nil
}

// Writes a document (kind doc, its IR blocks) or a deck (kind deck, its slides).
// Render it with a run: template document or slides, input.document its id.
//
// Example: {"kind": "deck", "title": "Q3 review", "ir": {"slides": [{"layout": "title", "title": "Q3 review"}]}}
func (o ops) documentCreate(ctx context.Context, in *DocumentWrite) (*Document, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	d, err := documentFrom(Document{Kind: in.Kind, Title: in.Title, Brand: in.Brand, IR: in.IR})
	if err != nil {
		return nil, err
	}
	id := mint.ID("doc")
	if err := o.s.State.put(ctx, "document", id, me, d.Kind, in.Shared, d, 0); err != nil {
		return nil, err
	}
	return o.documentGet(ctx, &ref{ID: id})
}

// Changes one of your documents; absent fields stay as they are.
func (o ops) documentPatch(ctx context.Context, in *DocumentPatch) (*Document, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	var d Document
	r, err := o.s.State.got(ctx, "document", me, in.ID, &d)
	if err != nil || r.Creator != me {
		return nil, zip.ErrNotFound("document not found")
	}
	if in.Title != nil {
		d.Title = *in.Title
	}
	if in.Brand != nil {
		d.Brand = *in.Brand
	}
	if in.IR != nil {
		d.IR = *in.IR
	}
	shared := r.Shared
	if in.Shared != nil {
		shared = *in.Shared
	}
	if d, err = documentFrom(d); err != nil {
		return nil, err
	}
	if err := o.s.State.put(ctx, "document", in.ID, me, d.Kind, shared, d, r.Created); err != nil {
		return nil, err
	}
	return o.documentGet(ctx, &ref{ID: in.ID})
}

// Deletes one of your documents.
func (o ops) documentDelete(ctx context.Context, in *ref) (*cloudUnit, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	return nil, o.s.State.remove(ctx, "document", me, in.ID)
}

// ── brands ───────────────────────────────────────────────────────────────

func (s *state) brand(ctx context.Context, me, id string) (Brand, error) {
	var b Brand
	r, err := s.got(ctx, "brand", me, id, &b)
	if err != nil {
		return Brand{}, err
	}
	b.ID = r.ID
	b.Org, b.Creator, b.Shared, b.Created, b.Updated = stamped(ctx, r)
	return b, nil
}

func brandFrom(b Brand) (Brand, error) {
	var err error
	if b.Name, err = title(b.Name, maxTitle, "name"); err != nil {
		return b, err
	}
	if len(b.Tokens) == 0 {
		return b, zip.ErrBadRequest("tokens is a design-token document")
	}
	if enc, _ := json.Marshal(b.Tokens); len(enc) > maxBrand {
		return b, zip.ErrBadRequest("a brand's tokens are at most 64 KiB")
	}
	if len(b.Logos) > 16 {
		return b, zip.ErrBadRequest("a brand has at most 16 logos")
	}
	for _, l := range b.Logos {
		if !assetID.MatchString(l) {
			return b, zip.ErrBadRequest("a logo is a Library item id")
		}
	}
	b.ID, b.Org, b.Creator, b.Created, b.Updated = "", "", "", "", ""
	return b, nil
}

// Lists the brands you may read — palettes and type systems as design tokens.
func (o ops) brands(ctx context.Context, in *pageIn) (*BrandPage, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := o.s.State.list(ctx, "brand", me, "", in.Limit, in.Cursor)
	if err != nil {
		return nil, err
	}
	out := &BrandPage{Data: []Brand{}, Next: next}
	for _, r := range rows {
		var b Brand
		if json.Unmarshal([]byte(r.Body), &b) != nil {
			continue
		}
		b.ID = r.ID
		b.Org, b.Creator, b.Shared, b.Created, b.Updated = stamped(ctx, r)
		out.Data = append(out.Data, b)
	}
	return out, nil
}

// Reads one brand you may read.
func (o ops) brandGet(ctx context.Context, in *ref) (*Brand, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	b, err := o.s.State.brand(ctx, me, in.ID)
	if err != nil {
		return nil, notFound(err)
	}
	return &b, nil
}

// Saves a brand: its tokens in the W3C design-token format (color, font, type,
// space, radius groups) and up to 16 logo items. A run started with it reads the
// tokens as brand.*.
//
// Example: {"name": "Acme", "tokens": {"color": {"primary": {"$value": "#ff5a1f", "$type": "color"}}}}
func (o ops) brandCreate(ctx context.Context, in *BrandWrite) (*Brand, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	b, err := brandFrom(Brand{Name: in.Name, Tokens: in.Tokens, Logos: in.Logos})
	if err != nil {
		return nil, err
	}
	id := mint.ID("brd")
	if err := o.s.State.put(ctx, "brand", id, me, "", in.Shared, b, 0); err != nil {
		return nil, err
	}
	return o.brandGet(ctx, &ref{ID: id})
}

// Changes one of your brands; absent fields stay as they are.
func (o ops) brandPatch(ctx context.Context, in *BrandPatch) (*Brand, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	var b Brand
	r, err := o.s.State.got(ctx, "brand", me, in.ID, &b)
	if err != nil || r.Creator != me {
		return nil, zip.ErrNotFound("brand not found")
	}
	if in.Name != nil {
		b.Name = *in.Name
	}
	if in.Tokens != nil {
		b.Tokens = in.Tokens
	}
	if in.Logos != nil {
		b.Logos = in.Logos
	}
	shared := r.Shared
	if in.Shared != nil {
		shared = *in.Shared
	}
	if b, err = brandFrom(b); err != nil {
		return nil, err
	}
	if err := o.s.State.put(ctx, "brand", in.ID, me, "", shared, b, r.Created); err != nil {
		return nil, err
	}
	return o.brandGet(ctx, &ref{ID: in.ID})
}

// Deletes one of your brands.
func (o ops) brandDelete(ctx context.Context, in *ref) (*cloudUnit, error) {
	ctx, me, err := o.who(ctx)
	if err != nil {
		return nil, err
	}
	return nil, o.s.State.remove(ctx, "brand", me, in.ID)
}
