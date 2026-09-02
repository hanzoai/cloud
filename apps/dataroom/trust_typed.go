package dataroom

// trust_typed.go is the trust centre's wire: the types a caller sees and the ops
// that answer them. Everything here is a typed op, so the whole surface reaches the
// published document, the MCP tool list, the CLI and the generated SDKs — an
// untyped route would publish an address and nothing else, which for a plane whose
// whole job is to be READ by outsiders would be the wrong half to publish.
//
// THREE AUDIENCES, AND THE GATE IS DIFFERENT FOR EACH.
//
//   - anonymous: the public reads and the ask. No principal at all. The org comes
//     from the published address, resolved through the opt-in index.
//   - the org itself: manage its own centre. The org comes from the validated
//     bearer, so these ops never see an org anybody named and cannot be pointed at
//     another tenant. Writing additionally requires the caller be an admin OF THAT
//     ORG — an ordinary member may read what their org publishes and may not change
//     what it releases.
//   - the platform: one cross-tenant roster, refused to anyone who is not a
//     SuperAdmin.
//
// EVERY GATE IS ASKED IN THE OP, NOT ONLY ON THE ROUTE. A typed op is also an MCP
// tool and an internal-plane op, and both invoke it DIRECTLY — no route, so no
// route middleware. A gate that lived only in a group would be a gate on one of
// three entry points. The routed group carries one too, so a refusal comes before
// the decoder has read a byte and a caller sending garbage is told about authority
// rather than about JSON.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// orgAdmin reports that the caller may change what their OWN org releases. A
// SuperAdmin also passes — while acting in an org, they are that org's admin too —
// and passing here grants nothing beyond the tenant the bearer already named.
func orgAdmin(ctx context.Context) bool {
	c, ok := cloud.Request(ctx)
	return ok && (principal.IsOrgAdmin(c) || principal.IsSuperAdmin(c))
}

// actor names who decided, for the record a grant leaves. It is the validated user,
// never anything the caller wrote.
func actor(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return c.User()
	}
	return ""
}

// manage resolves the caller's own org for a MANAGED read, refusing a caller with
// no validated org.
func manage(ctx context.Context) (string, error) { return principal.Acting(ctx) }

// mayWrite resolves the caller's own org for a managed WRITE, refusing a member who
// is not an admin of it.
func mayWrite(ctx context.Context) (string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", err
	}
	if !orgAdmin(ctx) {
		return "", zip.ErrForbidden("only an admin of this org may change its trust centre")
	}
	return org, nil
}

// ---- what a visitor sees ------------------------------------------------------

// trustItem is one published item as an OUTSIDER sees it.
type trustItem struct {
	// Available is "now" when the item can be read immediately, or "on request"
	// when it is released only to a party who asks and is answered. It is the one
	// field a page renders the difference from.
	Available string `json:"available"`
	// Body is the item's content, for the kinds that are text rather than a file —
	// an article, a subprocessor entry, a note. Empty for anything released on
	// request: a summary of a document is still the document.
	Body string `json:"body,omitempty"`
	// Framework is the standard the item speaks to, when it speaks to one.
	Framework string `json:"framework,omitempty"`
	// ID addresses the item — for reading it if it is available now, or for naming
	// it in a request if it is not.
	ID string `json:"id"`
	// Kind is what the item is: report, letter, policy, questionnaire,
	// subprocessor, article or update.
	Kind string `json:"kind"`
	// Name is the item's title.
	Name string `json:"name"`
	// Signed is "self" when the org states it itself and "auditor" when an
	// independent auditor put their name to it. It is the reason an item is
	// available now or on request, so a reader can see the rule rather than infer it.
	Signed string `json:"signed"`
	// Summary is a line about the item.
	Summary string `json:"summary,omitempty"`
	// UpdatedAt is when the item last changed, in unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// trustPage is an org's trust centre as an outsider sees it.
type trustPage struct {
	// Items is everything the centre publishes: what can be read now, and what
	// exists and is released on request.
	Items []trustItem `json:"items"`
	// Name is the org's display name for its centre.
	Name string `json:"name"`
	// Nda is the text a party must accept to ask for the gated items, verbatim.
	// Empty when the org asks for none.
	Nda string `json:"nda,omitempty"`
	// Slug is the centre's public address.
	Slug string `json:"slug"`
}

// slugRef addresses a trust centre by its public address.
type slugRef struct {
	// Slug is the centre's public address. It resolves only for an org that has
	// published; anything else is not found, so this cannot be used to learn which
	// orgs exist.
	Slug string `json:"slug"`
}

// ReadCenter answers an org's public trust centre: its name, the text a party must
// accept to ask for a document, and every item it publishes.
//
// An item is either available NOW — the things the org states itself, its policies,
// its filled questionnaires, its subprocessor list, its knowledge base — or
// available ON REQUEST, which is everything an independent auditor put their name
// to. Both are listed by name and kind, so a reader can see WHAT exists before
// asking for it; only the second withholds the content.
//
// No principal is involved and none is accepted: the org is resolved from the
// address, which answers only for a centre its owner has published. An address
// nobody publishes at is not found, the same answer an unpublished one gets.
func (o ops) readCenter(ctx context.Context, in *slugRef) (*trustPage, error) {
	org, c, err := o.centerOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	var rows []artifact
	if err := o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) (e error) { rows, e = liveArtifacts(tx); return }); err != nil {
		return nil, err
	}
	page := &trustPage{Items: make([]trustItem, 0, len(rows)), Name: c.Name, Nda: c.NDA, Slug: c.Slug}
	for _, a := range rows {
		it := trustItem{
			Available: "on request", Body: a.Body, Framework: a.Framework, ID: a.ID,
			Kind: a.Kind, Name: a.Name, Signed: a.Attester, Summary: a.Summary, UpdatedAt: a.UpdatedAt,
		}
		if a.Tier == "public" {
			it.Available = "now"
		}
		page.Items = append(page.Items, it)
	}
	return page, nil
}

// ---- asking -------------------------------------------------------------------

// trustAsk is a party asking to read what an auditor signed.
type trustAsk struct {
	goja.SizedIn
	// Accept must be true when the centre states an NDA. The text accepted is
	// recorded verbatim on the request, so a later edit to the NDA cannot rewrite
	// what this party agreed to.
	Accept bool `json:"accept,omitempty" url:"-"`
	// Email is where the grant will be sent, and the ONLY address the resulting
	// link admits. Required.
	Email string `json:"email,omitempty" url:"-"`
	// Item names one published item to ask for. Optional; omitting it asks for
	// everything released on request.
	Item string `json:"item,omitempty" url:"-"`
	// Party is the company the asker is from. Optional, and recorded as stated.
	Party string `json:"party,omitempty" url:"-"`
	// Reason is why they want it. Optional, and recorded as stated — it is what the
	// person deciding reads.
	Reason string `json:"reason,omitempty" url:"-"`
	// Slug is the centre's public address, taken from the path.
	Slug string `json:"slug"`
}

// UnmarshalJSON records the body size and keeps what was sent. Judgement belongs to
// the handler, which must be able to answer "too large" before it answers anything
// about the contents.
func (in *trustAsk) UnmarshalJSON(b []byte) error {
	type body trustAsk // sheds the method, so this does not recurse
	var v body
	in.Fill(maxBody, b, &v)
	v.SizedIn = in.SizedIn
	*in = trustAsk(v)
	return nil
}

// trustAsked is the receipt for a recorded request.
type trustAsked struct {
	// ID is the request's id, so the asker can be told about it later.
	ID string `json:"id"`
	// State is always "open": recording an ask decides nothing.
	State string `json:"state"`
}

// AskCenter records a request to read what an independent auditor signed, and
// answers with its id.
//
// The org that owns the centre decides. Nothing is released here and no link is
// minted: this writes the ask down, which is the whole promise the form makes.
// The write is the answer — a request that could not be stored is an error, never
// a receipt, so a form can never appear to have been sent and be gone.
//
// `email` is required and is the ONLY address the eventual grant will admit, so an
// address the asker cannot read is an ask that cannot be answered. Where the centre
// states an NDA, `accept` must be true and the text in force is recorded verbatim
// against the request.
//
// Asking twice for the same thing from the same address is the SAME ask: the second
// answers with the first's id rather than opening a second row, which is also what
// keeps an anonymous endpoint from filling a tenant's store.
func (o ops) askCenter(ctx context.Context, in *trustAsk) (*trustAsked, error) {
	if in.Oversize() {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	org, c, err := o.centerOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if !mailRe.MatchString(email) {
		return nil, zip.ErrBadRequest("a reachable email address is required")
	}
	if c.NDA != "" && !in.Accept {
		return nil, zip.ErrBadRequest("this centre asks you to accept its terms before requesting a document")
	}
	id, err := mintID("req_")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "id generation failed")
	}
	out := &trustAsked{ID: id, State: "open"}
	err = o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) error {
		if in.Item != "" {
			if _, ok, e := oneArtifact(tx, in.Item); e != nil {
				return e
			} else if !ok {
				return zip.ErrNotFound("no such item")
			}
		}
		res, e := tx.Exec(`
INSERT INTO trust_request (id,email,party,reason,artifact_id,nda,state,created_at)
VALUES (?,?,?,?,?,?,'open',?)
ON CONFLICT(email, COALESCE(artifact_id,'')) WHERE state='open' DO NOTHING`,
			id, email, nul(strings.TrimSpace(in.Party)), nul(strings.TrimSpace(in.Reason)),
			nul(in.Item), nul(c.NDA), millis())
		if e != nil {
			return e
		}
		// Nothing inserted means an open ask already stands. Answer with ITS id: the
		// asker gets a receipt for the ask that is really pending, rather than a
		// conflict for having pressed the button twice.
		if n, _ := res.RowsAffected(); n == 0 {
			return tx.QueryRow(
				`SELECT id FROM trust_request WHERE email=? AND COALESCE(artifact_id,'')=? AND state='open'`,
				email, in.Item).Scan(&out.ID)
		}
		return nil
	})
	if err != nil {
		if _, ok := err.(*zip.HTTPError); ok {
			return nil, err
		}
		o.s.Log.Error("trust request write failed", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "your request could not be recorded — nothing was sent")
	}
	return out, nil
}

// ---- the org's own centre -----------------------------------------------------

// trustGrantView is a grant that has been made: who holds it, and until when.
type trustGrantView struct {
	// Email is the one address the link admits.
	Email string `json:"email"`
	// ExpiresAt is when the grant closes, in unix milliseconds.
	ExpiresAt int64 `json:"expiresAt"`
	// Item is the item granted, empty when the whole released tier was granted.
	Item string `json:"item,omitempty"`
	// Link is the share link's id — the token the party opens. Reading it here does
	// not widen it: the link admits only Email whoever holds the id.
	Link string `json:"link"`
	// Live is whether the grant is still open at the time of reading.
	Live bool `json:"live"`
}

// trustAskView is one request as the org deciding it sees it.
type trustAskView struct {
	// CreatedAt is when the ask arrived, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// DecidedAt is when it was answered, in unix milliseconds; 0 while open.
	DecidedAt int64 `json:"decidedAt,omitempty"`
	// DecidedBy is who answered it.
	DecidedBy string `json:"decidedBy,omitempty"`
	// Email is the address that asked, as stated and UNVERIFIED — it names a party
	// and proves nothing, which is why the grant is addressed to it rather than
	// trusting it.
	Email string `json:"email"`
	// ExpiresAt is when a granted ask closes, in unix milliseconds.
	ExpiresAt int64 `json:"expiresAt,omitempty"`
	// ID is the request's id.
	ID string `json:"id"`
	// Item is the item asked for, empty when the whole released tier was asked for.
	Item string `json:"item,omitempty"`
	// Link is the share link a granted ask became.
	Link string `json:"link,omitempty"`
	// Nda is the text this party accepted, verbatim as it stood when they accepted.
	Nda string `json:"nda,omitempty"`
	// Note is what the decider wrote when refusing.
	Note string `json:"note,omitempty"`
	// Party is the company the asker stated.
	Party string `json:"party,omitempty"`
	// Reason is why they said they want it.
	Reason string `json:"reason,omitempty"`
	// State is open, granted or refused.
	State string `json:"state"`
}

// trustItemView is one item as its OWNER sees it — the same row the public sees,
// plus what the public must not: its tier, whether it is retired, and the document
// its bytes live in.
type trustItemView struct {
	// Attester is who vouched for it: self or auditor.
	Attester string `json:"attester"`
	// Body is the item's content for the kinds that are text rather than a file.
	Body string `json:"body,omitempty"`
	// CreatedAt is when it was published, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// Document is the data-room document holding its bytes, empty when it has none.
	Document string `json:"document,omitempty"`
	// Framework is the standard it speaks to, when it speaks to one.
	Framework string `json:"framework,omitempty"`
	// ID addresses the item.
	ID string `json:"id"`
	// Kind is one of report, letter, policy, questionnaire, subprocessor, article
	// or update — the closed set the public centre knows how to draw.
	Kind string `json:"kind"`
	// Name is the label the centre lists it under.
	Name string `json:"name"`
	// Retired is whether it has been withdrawn. A retired item is absent from the
	// public centre and cannot be granted; it is kept because a grant already made
	// over it is part of the record.
	Retired bool `json:"retired"`
	// Summary is a line about it.
	Summary string `json:"summary,omitempty"`
	// Tier is public or gated. Gated is the default and an auditor-signed item can
	// only ever be gated.
	Tier string `json:"tier"`
	// UpdatedAt is when it last changed, in unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// trustDesk is the org's whole trust centre in one read: its settings, everything
// it holds, the queue waiting on it, and the grants that are live.
type trustDesk struct {
	// Grants is every grant that has been made, newest first.
	Grants []trustGrantView `json:"grants"`
	// Items is everything the org holds, both tiers, retired included.
	Items []trustItemView `json:"items"`
	// Name is the centre's display name.
	Name string `json:"name"`
	// Nda is the text a party must accept before asking.
	Nda string `json:"nda,omitempty"`
	// Published is whether the centre answers at its public address.
	Published bool `json:"published"`
	// Requests is every ask, newest first, open ones included.
	Requests []trustAskView `json:"requests"`
	// Slug is the public address, empty until the centre is published.
	Slug string `json:"slug"`
}

// ReadDesk answers the caller org's OWN trust centre: its settings, every item it
// holds in both tiers, the requests waiting on it, and the grants it has made.
//
// The org is the caller's, taken from the validated bearer and from nothing else,
// so this op cannot be pointed at another tenant — there is no field for one. An
// org that has never opened a centre reads back an empty one rather than an error,
// because having no trust centre is an ordinary state and this is the read that
// tells you so.
func (o ops) readDesk(ctx context.Context, _ *cloud.Unit) (*trustDesk, error) {
	org, err := manage(ctx)
	if err != nil {
		return nil, err
	}
	var c center
	var items []artifact
	var asks []ask
	if err := o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) error {
		var e error
		if c, e = loadCenter(tx); e != nil {
			return e
		}
		if items, e = allArtifacts(tx); e != nil {
			return e
		}
		asks, e = allAsks(tx)
		return e
	}); err != nil {
		return nil, err
	}
	now := millis()
	desk := &trustDesk{
		Grants: []trustGrantView{}, Items: make([]trustItemView, 0, len(items)),
		Name: c.Name, Nda: c.NDA, Published: c.Published,
		Requests: make([]trustAskView, 0, len(asks)), Slug: c.Slug,
	}
	for _, a := range items {
		desk.Items = append(desk.Items, trustItemView{
			Attester: a.Attester, Body: a.Body, CreatedAt: a.CreatedAt, Document: a.DocumentID,
			Framework: a.Framework, ID: a.ID, Kind: a.Kind, Name: a.Name, Retired: a.Retired,
			Summary: a.Summary, Tier: a.Tier, UpdatedAt: a.UpdatedAt,
		})
	}
	for _, r := range asks {
		desk.Requests = append(desk.Requests, trustAskView{
			CreatedAt: r.CreatedAt, DecidedAt: r.DecidedAt, DecidedBy: r.DecidedBy, Email: r.Email,
			ExpiresAt: r.ExpiresAt, ID: r.ID, Item: r.ArtifactID, Link: r.LinkID, Nda: r.NDA,
			Note: r.Note, Party: r.Party, Reason: r.Reason, State: r.State,
		})
		if r.State == "granted" && r.LinkID != "" {
			desk.Grants = append(desk.Grants, trustGrantView{
				Email: r.Email, ExpiresAt: r.ExpiresAt, Item: r.ArtifactID, Link: r.LinkID,
				Live: r.ExpiresAt == 0 || r.ExpiresAt > now,
			})
		}
	}
	return desk, nil
}

// trustSettings is what an org decides about its own centre.
type trustSettings struct {
	goja.SizedIn
	// Name is the centre's display name. Required to publish.
	Name string `json:"name,omitempty" url:"-"`
	// Nda is the text a party must accept before asking for a document. Optional;
	// empty asks for no acceptance. The text in force is copied onto each request as
	// it is accepted, so editing it never changes what anyone already agreed to.
	Nda string `json:"nda,omitempty" url:"-"`
	// Publish makes the centre answer at its public address. False withdraws it: the
	// address stops answering while every item, grant and record stays exactly as it
	// was, so withdrawing is reversible and loses nothing.
	Publish bool `json:"publish,omitempty" url:"-"`
	// Slug is the public address to answer at — a lowercase label of letters,
	// digits and hyphens. Required to publish, unique across the deployment, and one
	// org holds one: publishing under a new address MOVES the centre rather than
	// leaving the old one answering.
	Slug string `json:"slug,omitempty" url:"-"`
}

// UnmarshalJSON records the body size and keeps what was sent.
func (in *trustSettings) UnmarshalJSON(b []byte) error {
	type body trustSettings // sheds the method, so this does not recurse
	var v body
	in.Fill(maxBody, b, &v)
	v.SizedIn = in.SizedIn
	*in = trustSettings(v)
	return nil
}

// SetCenter opens, publishes or withdraws the caller org's trust centre and answers
// with the centre as it now stands.
//
// Publishing requires a name and an address, and the address must be free: another
// org already answering there is a conflict, never a takeover. Withdrawing closes
// the public endpoint only — items, grants and the access record are untouched, so
// an org can go quiet and come back without losing anything.
//
// Only an admin of the org may call it. The org is the caller's own, so there is no
// field naming one and no way to point this at another tenant.
func (o ops) setCenter(ctx context.Context, in *trustSettings) (*trustDesk, error) {
	if in.Oversize() {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	org, err := mayWrite(ctx)
	if err != nil {
		return nil, err
	}
	var c center
	if err := o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) (e error) { c, e = loadCenter(tx); return }); err != nil {
		return nil, err
	}
	if in.Name != "" {
		c.Name = strings.TrimSpace(in.Name)
	}
	c.NDA = strings.TrimSpace(in.Nda)
	if in.Slug != "" {
		c.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	}
	c.Published = in.Publish
	if c.Published {
		if c.Name == "" {
			return nil, zip.ErrBadRequest("a published centre needs a name")
		}
		if !slugRe.MatchString(c.Slug) {
			return nil, zip.ErrBadRequest("an address is 2-63 characters of lowercase letters, digits and hyphens")
		}
		switch err := o.s.State.index.publish(c.Slug, org); {
		case err == errSlugTaken:
			return nil, zip.Errorf(http.StatusConflict, "another trust centre already answers at that address")
		case err != nil:
			o.s.Log.Error("trust index write failed", "err", err)
			return nil, zip.Errorf(http.StatusInternalServerError, "address could not be claimed")
		}
	} else if err := o.s.State.index.withdraw(org); err != nil {
		o.s.Log.Error("trust index withdraw failed", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "address could not be released")
	}
	if err := o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) error { return saveCenter(tx, c) }); err != nil {
		o.s.Log.Error("trust centre write failed", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "trust centre could not be saved")
	}
	return o.readDesk(ctx, nil)
}

// ---- publishing an item -------------------------------------------------------

// trustPublish is an item to publish on the caller org's centre.
type trustPublish struct {
	goja.SizedIn
	// Attester is who vouched for it: "self" for anything the org states itself, or
	// "auditor" for anything an independent auditor put their name to. REQUIRED, and
	// anything other than "self" is read as "auditor" — the safe direction, since an
	// auditor-signed item can only ever be released on request.
	Attester string `json:"attester,omitempty" url:"-"`
	// Body is the item's content for the kinds that are text rather than a file: an
	// article, a subprocessor entry, a dated note.
	Body string `json:"body,omitempty" url:"-"`
	// Document is a data-room document holding the item's bytes, uploaded first
	// through POST /v1/dataroom/documents. Optional: an item can be content with no
	// file. The document must already exist in the caller org's own store.
	Document string `json:"document,omitempty" url:"-"`
	// Framework is the standard it speaks to. Optional and free text — the value is
	// the org's own, not a list this API keeps.
	Framework string `json:"framework,omitempty" url:"-"`
	// Kind is what the item is: report, letter, policy, questionnaire, subprocessor,
	// article or update. Required.
	Kind string `json:"kind,omitempty" url:"-"`
	// Name is the item's title. Required.
	Name string `json:"name,omitempty" url:"-"`
	// Summary is a line about it. Optional.
	Summary string `json:"summary,omitempty" url:"-"`
	// Tier is who may read it: "public" or "gated". It DEFAULTS TO GATED and
	// anything that is not exactly "public" is gated, so an item published by a
	// caller that says nothing is private and someone has to release it on purpose.
	// "public" is refused for an auditor-signed item.
	Tier string `json:"tier,omitempty" url:"-"`
}

// UnmarshalJSON records the body size and keeps what was sent.
func (in *trustPublish) UnmarshalJSON(b []byte) error {
	type body trustPublish // sheds the method, so this does not recurse
	var v body
	in.Fill(maxBody, b, &v)
	v.SizedIn = in.SizedIn
	*in = trustPublish(v)
	return nil
}

// Publish puts an item on the caller org's trust centre and answers with it.
//
// The item is GATED unless it says otherwise, so a kind nobody has thought of yet
// arrives private and someone has to release it deliberately — that default is what
// keeps an auditor's report from becoming readable because a field went unset. An
// item whose attester is "auditor" cannot be public at all: the database refuses the
// pair, so no path through this API can publish one.
//
// A file is optional and is uploaded FIRST, through POST /v1/dataroom/documents,
// then named here — the data room is the one place bytes enter, so a trust centre
// document is an ordinary data-room document and inherits its storage, its grants
// and its page-by-page access record. A gated item that has a file is added to the
// org's release room, which is what lets a party be granted the whole gated tier in
// one link.
//
// Only an admin of the org may call it.
func (o ops) publish(ctx context.Context, in *trustPublish) (*trustItemView, error) {
	if in.Oversize() {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	org, err := mayWrite(ctx)
	if err != nil {
		return nil, err
	}
	kind := strings.TrimSpace(strings.ToLower(in.Kind))
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("an item needs a name")
	}
	if strings.TrimSpace(in.Attester) == "" {
		return nil, zip.ErrBadRequest(`attester is required: "self" for what you state yourself, "auditor" for what an independent auditor signed`)
	}
	tier, attester := normal(in.Tier, in.Attester)
	if attester == "auditor" && strings.EqualFold(strings.TrimSpace(in.Tier), "public") {
		return nil, zip.ErrBadRequest("an item an independent auditor signed is released on request, never published")
	}
	id, err := mintID("item_")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "id generation failed")
	}

	// The document must be the caller org's own. It is resolved through the data
	// room in the caller's tenant store, so another org's id is simply not found —
	// and the check runs BEFORE the row is written, so an item never names a
	// document that is not there.
	var c center
	if in.Document != "" {
		var doc dataroomDocumentOne
		if err := o.bundle(ctx, org, "documents.get", map[string]string{"id": in.Document}, nil, &doc); err != nil {
			return nil, err
		}
	}
	if err := o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) (e error) { c, e = loadCenter(tx); return }); err != nil {
		return nil, err
	}
	if in.Document != "" && tier == "gated" {
		room, err := o.vault(ctx, org, c)
		if err != nil {
			return nil, err
		}
		// Already in the room is not a failure — the room is a set, and the item
		// being in it is the state this is reaching for.
		if err := o.bundle(ctx, org, "datarooms.addDocument", map[string]string{"id": room},
			map[string]any{"documentId": in.Document}, nil); err != nil {
			var be *goja.BundleErr
			if !errors.As(err, &be) || be.Status != http.StatusConflict {
				return nil, err
			}
		}
	}

	t := millis()
	if err := o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) error {
		_, e := tx.Exec(`INSERT INTO trust_artifact (`+artifactCols+`) VALUES (?,?,?,?,?,?,?,?,?,0,?,?)`,
			id, kind, name, nul(strings.TrimSpace(in.Summary)), nul(strings.TrimSpace(in.Framework)),
			attester, tier, nul(in.Document), nul(in.Body), t, t)
		return e
	}); err != nil {
		o.s.Log.Error("trust item write failed", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "item could not be published")
	}
	return &trustItemView{
		Attester: attester, Body: in.Body, CreatedAt: t, Document: in.Document,
		Framework: strings.TrimSpace(in.Framework), ID: id, Kind: kind, Name: name,
		Summary: strings.TrimSpace(in.Summary), Tier: tier, UpdatedAt: t,
	}, nil
}

// trustEdit changes an item already published. Every field is optional and an
// omitted one is left as it stands, so a caller changing one thing does not have to
// restate the rest and cannot blank a field by forgetting it.
type trustEdit struct {
	goja.SizedIn
	// Body replaces the item's content.
	Body *string `json:"body,omitempty" url:"-"`
	// Document replaces the file the item points at — this is how a report is
	// superseded by its next edition. The new document must already exist in the
	// caller org's own store.
	Document *string `json:"document,omitempty" url:"-"`
	// Framework replaces the standard it speaks to.
	Framework *string `json:"framework,omitempty" url:"-"`
	// ID is the item to change, taken from the path.
	ID string `json:"id"`
	// Name replaces its title.
	Name *string `json:"name,omitempty" url:"-"`
	// Retired withdraws the item, or true→false restores it. A retired item leaves
	// the public centre at once and can no longer be granted; grants already made
	// over it stand, because they are part of the record.
	Retired *bool `json:"retired,omitempty" url:"-"`
	// Summary replaces the line about it.
	Summary *string `json:"summary,omitempty" url:"-"`
	// Tier moves it between public and gated. Moving an auditor-signed item to
	// public is refused.
	Tier *string `json:"tier,omitempty" url:"-"`
}

// UnmarshalJSON records the body size and keeps what was sent.
func (in *trustEdit) UnmarshalJSON(b []byte) error {
	type body trustEdit // sheds the method, so this does not recurse
	var v body
	in.Fill(maxBody, b, &v)
	v.SizedIn = in.SizedIn
	*in = trustEdit(v)
	return nil
}

// Amend changes an item on the caller org's trust centre — replace its file with a
// newer edition, move it between public and gated, rewrite what it says, or retire
// it — and answers with the item as it now stands.
//
// Retiring is the withdrawal: the item leaves the public centre immediately and can
// no longer be granted, while grants already made over it stand, because a release
// that happened is part of the record and un-happening it in the record would be a
// lie. Restoring is the same call with retired false.
//
// Moving an item an independent auditor signed to the public tier is refused, and
// refused by the database rather than only here. Only an admin of the org may call
// it, and the item is resolved in that org's own store, so another org's id is not
// found.
func (o ops) amend(ctx context.Context, in *trustEdit) (*trustItemView, error) {
	if in.Oversize() {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	org, err := mayWrite(ctx)
	if err != nil {
		return nil, err
	}
	if in.Document != nil && *in.Document != "" {
		var doc dataroomDocumentOne
		if err := o.bundle(ctx, org, "documents.get", map[string]string{"id": *in.Document}, nil, &doc); err != nil {
			return nil, err
		}
	}
	var a artifact
	err = o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) error {
		cur, ok, e := oneArtifact(tx, in.ID)
		if e != nil {
			return e
		}
		if !ok {
			return zip.ErrNotFound("no such item")
		}
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = strings.TrimSpace(*v)
			}
		}
		set(&cur.Name, in.Name)
		set(&cur.Summary, in.Summary)
		set(&cur.Framework, in.Framework)
		set(&cur.Body, in.Body)
		set(&cur.DocumentID, in.Document)
		if in.Retired != nil {
			cur.Retired = *in.Retired
		}
		if in.Tier != nil {
			if cur.Attester == "auditor" && strings.EqualFold(strings.TrimSpace(*in.Tier), "public") {
				return zip.ErrBadRequest("an item an independent auditor signed is released on request, never published")
			}
			cur.Tier, _ = normal(*in.Tier, cur.Attester)
		}
		cur.UpdatedAt = millis()
		if _, e := tx.Exec(`
UPDATE trust_artifact SET name=?,summary=?,framework=?,body=?,document_id=?,tier=?,retired=?,updated_at=? WHERE id=?`,
			cur.Name, nul(cur.Summary), nul(cur.Framework), nul(cur.Body), nul(cur.DocumentID),
			cur.Tier, cur.Retired, cur.UpdatedAt, cur.ID); e != nil {
			return e
		}
		a = cur
		return nil
	})
	if err != nil {
		if _, ok := err.(*zip.HTTPError); ok {
			return nil, err
		}
		o.s.Log.Error("trust item update failed", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "item could not be changed")
	}
	return &trustItemView{
		Attester: a.Attester, Body: a.Body, CreatedAt: a.CreatedAt, Document: a.DocumentID,
		Framework: a.Framework, ID: a.ID, Kind: a.Kind, Name: a.Name, Retired: a.Retired,
		Summary: a.Summary, Tier: a.Tier, UpdatedAt: a.UpdatedAt,
	}, nil
}

// ---- deciding ------------------------------------------------------------------

// trustDecision answers one request.
type trustDecision struct {
	goja.SizedIn
	// Days is how long the grant stays open, from now. Optional; 14 by default and
	// 365 at most — a longer release is describing a customer relationship rather
	// than a document.
	Days int `json:"days,omitempty" url:"-"`
	// ID is the request to answer, taken from the path.
	ID string `json:"id"`
	// Note is why. Recorded on the request either way, and it is what the record
	// shows a year later.
	Note string `json:"note,omitempty" url:"-"`
}

// UnmarshalJSON records the body size and keeps what was sent.
func (in *trustDecision) UnmarshalJSON(b []byte) error {
	type body trustDecision // sheds the method, so this does not recurse
	var v body
	in.Fill(maxBody, b, &v)
	v.SizedIn = in.SizedIn
	*in = trustDecision(v)
	return nil
}

// trustGranted is what a grant produced.
type trustGranted struct {
	// Delivery is empty when the asker was mailed, and otherwise says what happened
	// instead — so an approver is never left believing a mail went out that did not.
	Delivery string `json:"delivery,omitempty"`
	// ExpiresAt is when the grant closes, in unix milliseconds.
	ExpiresAt int64 `json:"expiresAt"`
	// Link is the share link's id. The link admits only the address that asked.
	Link string `json:"link"`
	// State is "granted".
	State string `json:"state"`
}

// Grant answers a request by opening access: it mints a share link over what was
// asked for, addressed to the address that asked and closing at expiry, records the
// decision, and mails the asker.
//
// The link is NEVER a public URL. It carries the asker's address on its allow list,
// so forwarding it to somebody else does not open it, and it expires. What the
// party then does with it — which document, which page, for how long — is recorded
// by the data room's own view tracking, which is where the access record for this
// release lives; there is no second log.
//
// A request that was already answered is refused rather than answered twice, so a
// second click cannot mint a second link. Only an admin of the org may call it, and
// the request is resolved in that org's own store, so another org's request id is
// not found — which is also what stops one org deciding another's queue.
//
// Mail is best effort and the grant does not depend on it: a deployment that sends
// no mail still records the grant and says so in `delivery`, so the approver knows
// to pass the address on themselves.
func (o ops) grant(ctx context.Context, in *trustDecision) (*trustGranted, error) {
	if in.Oversize() {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	org, err := mayWrite(ctx)
	if err != nil {
		return nil, err
	}
	days := in.Days
	if days <= 0 {
		days = grantDays
	}
	if days > maxGrantDays {
		return nil, zip.ErrBadRequest("a grant may stay open for at most a year")
	}
	var c center
	var a ask
	if err := o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) error {
		var e error
		if c, e = loadCenter(tx); e != nil {
			return e
		}
		var ok bool
		if a, ok, e = oneAsk(tx, in.ID); e != nil {
			return e
		} else if !ok {
			return zip.ErrNotFound("no such request")
		}
		if a.State != "open" {
			return zip.Errorf(http.StatusConflict, "that request was already %s", a.State)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	until := time.Now().AddDate(0, 0, days).UnixMilli()
	link, err := o.release(ctx, org, c, a, until)
	if err != nil {
		return nil, err
	}
	if err := o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) error {
		res, e := tx.Exec(`
UPDATE trust_request SET state='granted', link_id=?, expires_at=?, note=?, decided_by=?, decided_at=?
 WHERE id=? AND state='open'`, link, until, nul(strings.TrimSpace(in.Note)), nul(actor(ctx)), millis(), in.ID)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return zip.Errorf(http.StatusConflict, "that request was answered while this one was in flight")
		}
		return nil
	}); err != nil {
		if _, ok := err.(*zip.HTTPError); ok {
			return nil, err
		}
		o.s.Log.Error("trust grant write failed", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "the grant could not be recorded")
	}
	return &trustGranted{
		Delivery:  o.deliver(ctx, org, o.s.State.domain, a.Email, link, until),
		ExpiresAt: until, Link: link, State: "granted",
	}, nil
}

// trustRefused is what a refusal produced.
type trustRefused struct {
	// State is "refused".
	State string `json:"state"`
}

// Refuse answers a request by declining it, recording who declined and why.
//
// Nothing is released and no link is minted. The refusal STAYS on the record beside
// the ask — a request that was turned down is part of the access record exactly as
// one that was granted is, and deleting it would leave a queue that only ever shows
// the decisions somebody liked.
//
// A request that was already answered is refused rather than answered twice. Only an
// admin of the org may call it, and the request is resolved in that org's own store,
// so another org's request id is not found.
func (o ops) refuse(ctx context.Context, in *trustDecision) (*trustRefused, error) {
	if in.Oversize() {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	org, err := mayWrite(ctx)
	if err != nil {
		return nil, err
	}
	if err := o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) error {
		if _, ok, e := oneAsk(tx, in.ID); e != nil {
			return e
		} else if !ok {
			return zip.ErrNotFound("no such request")
		}
		res, e := tx.Exec(`
UPDATE trust_request SET state='refused', note=?, decided_by=?, decided_at=? WHERE id=? AND state='open'`,
			nul(strings.TrimSpace(in.Note)), nul(actor(ctx)), millis(), in.ID)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return zip.Errorf(http.StatusConflict, "that request was already answered")
		}
		return nil
	}); err != nil {
		if _, ok := err.(*zip.HTTPError); ok {
			return nil, err
		}
		o.s.Log.Error("trust refusal write failed", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "the refusal could not be recorded")
	}
	return &trustRefused{State: "refused"}, nil
}

// ---- the platform's roster -----------------------------------------------------

// trustRoster is one org's centre as the platform sees it.
type trustRoster struct {
	// Grants is how many grants that org has made.
	Grants int64 `json:"grants"`
	// Items is how many items it publishes, retired excluded.
	Items int64 `json:"items"`
	// Name is the centre's display name.
	Name string `json:"name"`
	// Open is how many requests are waiting on it — the number that says whether
	// anybody is being kept waiting.
	Open int64 `json:"open"`
	// Org is the tenant that owns it.
	Org string `json:"org"`
	// Slug is its public address.
	Slug string `json:"slug"`
}

// trustRosters is every published trust centre in the deployment.
type trustRosters struct {
	// Centers is every published centre, by address.
	Centers []trustRoster `json:"centers"`
}

// Roster lists every published trust centre in the deployment with the size of its
// queue — the platform's view of who is running one and who is leaving people
// waiting.
//
// It is the ONE cross-tenant read in this subsystem and it is refused to anyone who
// is not a SuperAdmin: a member of the reserved admin org, the same predicate every
// other subsystem asks. An org's own admin is a different, org-scoped fact and does
// not pass here — reading it as platform authority is how one customer comes to see
// every other customer's queue.
//
// It counts and does not read: no item, request, address or grant of any org's
// crosses into the answer.
func (o ops) roster(ctx context.Context, _ *cloud.Unit) (*trustRosters, error) {
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return nil, cloud.Super.Refusal()
	}
	rows, err := o.s.State.index.centers()
	if err != nil {
		o.s.Log.Error("trust index read failed", "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "roster read failed")
	}
	out := &trustRosters{Centers: make([]trustRoster, 0, len(rows))}
	for _, r := range rows {
		row := trustRoster{Org: r.Org, Slug: r.Slug}
		if err := o.s.State.host.Tx(ctx, r.Org, func(tx *sql.Tx) error {
			c, e := loadCenter(tx)
			if e != nil {
				return e
			}
			row.Name = c.Name
			if e = tx.QueryRow(`SELECT count(*) FROM trust_artifact WHERE retired=0`).Scan(&row.Items); e != nil {
				return e
			}
			if e = tx.QueryRow(`SELECT count(*) FROM trust_request WHERE state='open'`).Scan(&row.Open); e != nil {
				return e
			}
			return tx.QueryRow(`SELECT count(*) FROM trust_request WHERE state='granted'`).Scan(&row.Grants)
		}); err != nil {
			// One unreadable tenant must not blank the roster. The row is reported
			// with what is known and the reason is logged, which is the honest
			// answer — omitting it silently would report the fleet as smaller than
			// it is.
			o.s.Log.Error("trust roster read failed", "org", r.Org, "err", err)
		}
		out.Centers = append(out.Centers, row)
	}
	return out, nil
}
