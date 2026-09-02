package dataroom

// trust.go is the trust centre: what an org publishes about its own security, and
// the endpoint where an outsider asks for the part only an auditor can vouch for.
//
// ONE RULE SHAPES IT. Anything the org asserts itself — its controls, its posture,
// its subprocessors, its policies, its CAIQ/SIG/VSA answers — is public, because a
// self-assessment gains nothing from being hidden. Anything an INDEPENDENT auditor
// signed is released only to a named party who asked and was answered. So the tier
// is a property of the artifact and it turns on who vouched for it.
//
// IT IS NOT A SECOND DOCUMENT STORE. The bytes are a dataroom document, a grant is
// a dataroom link — time-boxed, addressed to one party, never a public URL — and
// who opened what, page by page, is the view and page_view rows the data room
// already writes. That last one matters more than it looks: the access record a
// compliance programme owes is exactly the record the data room keeps anyway, so
// it is READ from there rather than logged somewhere new, and the two can never
// disagree about who saw a report.
//
// WHAT IS NEW HERE, AND WHY IT IS GO. Three tables (schema.go) have no counterpart
// in the bundle's upstream: the centre, the artifact and the ask. The bundle is a
// faithful port of that upstream's handlers, so inventing routes inside it would
// fork a fork — new logic in JavaScript, untyped, with nothing upstream to merge
// against. These live in Go instead, in this package, on the SAME per-tenant store
// the bundle writes (goja.BaseHost.Tx), so it is still one store, one schema and
// one home. Where the data room already answers — minting a link, recording a
// document, opening a room — this dispatches the bundle rather than restating it.
//
// TENANCY. Every managed read and write takes its org from the validated bearer
// and nothing else, so the store a caller reaches is decided before any input is
// read and no field can move it. The public reads take a SLUG, which resolves
// through an opt-in index (index.go) exactly as a share link's id does: an org that
// has not published is not addressable at all.
//
// SINGLE WRITER. A tenant store runs one connection, so a Tx may not be open across
// a Dispatch — the two would deadlock on it. Every operation below sequences its
// transactions and its dispatches; none nests them.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// grantDays is how long a grant lasts when the approver does not say. Two weeks
// is long enough to read a report and short enough that a leaked link goes cold.
const grantDays = 14

// maxGrantDays bounds a grant. An approver who wants longer is describing a
// customer relationship, not a document release.
const maxGrantDays = 365

// centerRow is the id of the one centre row per tenant. A fixed primary key is
// what makes a second centre unrepresentable rather than merely unwritten.
const centerRow = "center"

// kinds is the closed vocabulary a trust centre publishes in. It is closed because
// a page renders by kind: a value nothing knows how to draw would be published and
// invisible. Nothing here names a particular framework — a SOC 2 report and an ISO
// 27001 report are both `report`, told apart by the artifact's own framework field.
var kinds = map[string]bool{
	"report":        true, // an auditor's own report on an examination
	"letter":        true, // an auditor's written statement short of a report
	"policy":        true, // a policy the org maintains and follows
	"questionnaire": true, // a filled self-assessment: CAIQ, SIG, VSA
	"subprocessor":  true, // one third party the org discloses
	"article":       true, // a knowledge-base answer
	"update":        true, // a dated note on the programme
}

// slugRe is what may become a public address: a lowercase DNS-ish label, which is
// safe as a path segment and as a hostname if a centre ever gets its own.
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// mailRe is a deliberately loose shape check. It refuses what is obviously not an
// address so the queue does not fill with junk; it proves nothing about who holds
// the address, which is why a grant is addressed to it rather than trusting it.
var mailRe = regexp.MustCompile(`^[^@\s]+@[^@\s.]+\.[^@\s]+$`)

// ---- the rows ---------------------------------------------------------------

type center struct {
	Slug      string
	Name      string
	Published bool
	NDA       string
	RoomID    string
	UpdatedAt int64
}

type artifact struct {
	ID         string
	Kind       string
	Name       string
	Summary    string
	Framework  string
	Attester   string
	Tier       string
	DocumentID string
	Body       string
	Retired    bool
	CreatedAt  int64
	UpdatedAt  int64
}

type ask struct {
	ID         string
	Email      string
	Party      string
	Reason     string
	ArtifactID string
	NDA        string
	State      string
	Note       string
	LinkID     string
	ExpiresAt  int64
	DecidedBy  string
	DecidedAt  int64
	CreatedAt  int64
}

// ---- store ------------------------------------------------------------------

func millis() int64 { return time.Now().UnixMilli() }

// text reads a nullable TEXT column as the empty string. Absent and empty are the
// same fact for every field here — a summary nobody wrote and a summary someone
// cleared both render as nothing — so carrying the distinction would be carrying a
// difference no caller can act on.
func text(s sql.NullString) string { return s.String }

// nul renders "" back as SQL NULL, so a cleared field reads as absent rather than
// as an empty string a later index or constraint would have to special-case.
func nul(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// loadCenter reads the tenant's centre. A tenant that has never opened one reads
// back the zero value rather than an error: not having a trust centre is an
// ordinary state, and the managed read answers it as one.
func loadCenter(tx *sql.Tx) (center, error) {
	var c center
	var nda, room sql.NullString
	err := tx.QueryRow(`SELECT slug,name,published,nda,room_id,updated_at FROM trust_center WHERE id=?`, centerRow).
		Scan(&c.Slug, &c.Name, &c.Published, &nda, &room, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return center{}, nil
	}
	c.NDA, c.RoomID = text(nda), text(room)
	return c, err
}

func saveCenter(tx *sql.Tx, c center) error {
	t := millis()
	_, err := tx.Exec(`
INSERT INTO trust_center (id,slug,name,published,nda,room_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET slug=excluded.slug, name=excluded.name, published=excluded.published,
                              nda=excluded.nda, room_id=excluded.room_id, updated_at=excluded.updated_at`,
		centerRow, c.Slug, c.Name, c.Published, nul(c.NDA), nul(c.RoomID), t, t)
	return err
}

// artifactCols is the column list every artifact read shares, so a column added to
// one read cannot go missing from another.
const artifactCols = `id,kind,name,summary,framework,attester,tier,document_id,body,retired,created_at,updated_at`

func scanArtifacts(rows *sql.Rows) ([]artifact, error) {
	defer func() { _ = rows.Close() }()
	out := []artifact{}
	for rows.Next() {
		var a artifact
		var summary, framework, doc, body sql.NullString
		if err := rows.Scan(&a.ID, &a.Kind, &a.Name, &summary, &framework, &a.Attester, &a.Tier,
			&doc, &body, &a.Retired, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.Summary, a.Framework, a.DocumentID, a.Body = text(summary), text(framework), text(doc), text(body)
		out = append(out, a)
	}
	return out, rows.Err()
}

// allArtifacts is the MANAGED read: everything, both tiers, retired included, for
// the org that owns them.
func allArtifacts(tx *sql.Tx) ([]artifact, error) {
	rows, err := tx.Query(`SELECT ` + artifactCols + ` FROM trust_artifact ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return scanArtifacts(rows)
}

// liveArtifacts is the PUBLIC read, and the WHERE and the CASEs are the boundary
// rather than a rule the projector is trusted to remember. A retired item is gone,
// and a gated item's document reference and content are NULL in the result set
// itself — so a projector written later that forgets to check the tier still
// cannot leak them. `body` matters as much as the file: a gated item's content is
// not a summary, and answering it would release by paraphrase exactly what the
// grant exists to control.
//
// Only the `body` half is measurable from outside today, because trustItem carries
// no document field at all, and that is worth knowing rather than glossing:
// deleting the document_id CASE breaks nothing a test can see. It stays because
// the reason it is here does not depend on the current projector — the next field
// somebody adds to trustItem is the one that would have leaked.
func liveArtifacts(tx *sql.Tx) ([]artifact, error) {
	rows, err := tx.Query(`
SELECT id,kind,name,summary,framework,attester,tier,
       CASE WHEN tier='public' THEN document_id END,
       CASE WHEN tier='public' THEN body END,
       retired,created_at,updated_at
  FROM trust_artifact WHERE retired=0 ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return scanArtifacts(rows)
}

// publicArtifact resolves one artifact a visitor may read in full. The tier and the
// retirement are in the WHERE, so an id that names a gated or retired item is not
// found — the same answer as an id that never existed, which is what keeps the
// lookup from reporting what the gated tier contains.
func publicArtifact(tx *sql.Tx, id string) (artifact, bool, error) {
	rows, err := tx.Query(`SELECT `+artifactCols+` FROM trust_artifact WHERE id=? AND tier='public' AND retired=0`, id)
	if err != nil {
		return artifact{}, false, err
	}
	as, err := scanArtifacts(rows)
	if err != nil || len(as) == 0 {
		return artifact{}, false, err
	}
	return as[0], true, nil
}

func oneArtifact(tx *sql.Tx, id string) (artifact, bool, error) {
	rows, err := tx.Query(`SELECT `+artifactCols+` FROM trust_artifact WHERE id=?`, id)
	if err != nil {
		return artifact{}, false, err
	}
	as, err := scanArtifacts(rows)
	if err != nil || len(as) == 0 {
		return artifact{}, false, err
	}
	return as[0], true, nil
}

const askCols = `id,email,party,reason,artifact_id,nda,state,note,link_id,expires_at,decided_by,decided_at,created_at`

func scanAsks(rows *sql.Rows) ([]ask, error) {
	defer func() { _ = rows.Close() }()
	out := []ask{}
	for rows.Next() {
		var a ask
		var party, reason, art, nda, note, link, by sql.NullString
		var exp, at sql.NullInt64
		if err := rows.Scan(&a.ID, &a.Email, &party, &reason, &art, &nda, &a.State, &note, &link,
			&exp, &by, &at, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Party, a.Reason, a.ArtifactID = text(party), text(reason), text(art)
		a.NDA, a.Note, a.LinkID, a.DecidedBy = text(nda), text(note), text(link), text(by)
		a.ExpiresAt, a.DecidedAt = exp.Int64, at.Int64
		out = append(out, a)
	}
	return out, rows.Err()
}

func allAsks(tx *sql.Tx) ([]ask, error) {
	rows, err := tx.Query(`SELECT ` + askCols + ` FROM trust_request ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return scanAsks(rows)
}

func oneAsk(tx *sql.Tx, id string) (ask, bool, error) {
	rows, err := tx.Query(`SELECT `+askCols+` FROM trust_request WHERE id=?`, id)
	if err != nil {
		return ask{}, false, err
	}
	as, err := scanAsks(rows)
	if err != nil || len(as) == 0 {
		return ask{}, false, err
	}
	return as[0], true, nil
}

// ---- composition with the data room ------------------------------------------

// mintID is the id shape for this plane's own rows, prefixed like the bundle's so
// an id says what it addresses on sight.
func mintID(prefix string) (string, error) {
	k, err := randKey()
	if err != nil {
		return "", err
	}
	return prefix + k, nil
}

// bundle runs one data-room route on the tenant's store and decodes its answer. A
// non-2xx is the data room's own refusal and is relayed with its status and bytes
// intact, so a caller sees what it would have seen calling the data room directly.
func (o ops) bundle(ctx context.Context, org, route string, params map[string]string, body any, out any) error {
	resp, err := o.s.State.host.Dispatch(ctx, org, goja.BaseRequest{Route: route, Params: params, Body: body})
	if err != nil {
		o.s.Log.Error("dataroom dispatch failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "dataroom dispatch failed")
	}
	if resp.Status/100 != 2 {
		return &goja.BundleErr{Status: resp.Status, Body: resp.Body, Msg: bundleMessage(resp.Status, resp.Body)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		o.s.Log.Error("dataroom response decode failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "dataroom dispatch failed")
	}
	return nil
}

// vault returns the id of the room every gated document sits in, opening it on
// first use. One room per tenant is what lets a party be granted the whole gated
// tier in one link, and it is what the data room's own analytics then roll up.
//
// It is called OUTSIDE any transaction of ours: the dispatch opens its own, and a
// tenant store runs one connection.
func (o ops) vault(ctx context.Context, org string, c center) (string, error) {
	if c.RoomID != "" {
		return c.RoomID, nil
	}
	var made struct {
		Dataroom struct {
			ID string `json:"id"`
		} `json:"dataroom"`
	}
	name := c.Name
	if name == "" {
		name = "Trust centre"
	}
	if err := o.bundle(ctx, org, "datarooms.create", nil, map[string]any{
		"name":        name,
		"description": "Documents released on request.",
	}, &made); err != nil {
		return "", err
	}
	if made.Dataroom.ID == "" {
		return "", zip.Errorf(http.StatusInternalServerError, "dataroom dispatch failed")
	}
	c.RoomID = made.Dataroom.ID
	if err := o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) error { return saveCenter(tx, c) }); err != nil {
		return "", err
	}
	return c.RoomID, nil
}

// release mints the grant: a data-room link over the target, closing at expiry and
// admitting ONE address. It is never a public URL — the link carries the asker's
// address on its allow list, so forwarding it to somebody else does not open it.
//
// The index write is part of the operation and comes BEFORE the decision is
// recorded. A visitor resolves the owning org through that index, so a link absent
// from it opens for nobody; recording a grant that cannot be opened would be
// reporting work that did not happen. A failure between the two leaves a link that
// is inert — its id has been handed to no one — and the next attempt mints another.
func (o ops) release(ctx context.Context, org string, c center, a ask, until int64) (string, error) {
	body := map[string]any{
		"name":           "Trust centre — " + a.Email,
		"emailProtected": true,
		"allowList":      []string{a.Email},
		"allowDownload":  true,
		"expiresAt":      until,
	}
	if a.ArtifactID != "" {
		var art artifact
		var found bool
		if err := o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) (err error) {
			art, found, err = oneArtifact(tx, a.ArtifactID)
			return
		}); err != nil {
			return "", err
		}
		if !found || art.DocumentID == "" {
			return "", zip.ErrBadRequest("that item has no document to release")
		}
		body["documentId"] = art.DocumentID
	} else {
		room, err := o.vault(ctx, org, c)
		if err != nil {
			return "", err
		}
		body["dataroomId"] = room
	}
	var made struct {
		Link struct {
			ID string `json:"id"`
		} `json:"link"`
	}
	if err := o.bundle(ctx, org, "links.create", nil, body, &made); err != nil {
		return "", err
	}
	if made.Link.ID == "" {
		return "", zip.Errorf(http.StatusInternalServerError, "dataroom dispatch failed")
	}
	if err := o.s.State.index.put(made.Link.ID, org); err != nil {
		o.s.Log.Error("dataroom link index write failed", "link", made.Link.ID, "err", err)
		return "", zip.Errorf(http.StatusInternalServerError, "link index write failed")
	}
	return made.Link.ID, nil
}

// deliver tells the asker their grant is open, on the org's own mail provider. It
// answers what actually happened rather than assuming: a deployment that runs no
// notify is a real arrangement, and the approver is told to pass the address on
// themselves instead of being left to believe a mail was sent.
//
// It cannot fail the grant. The grant is recorded and the link is live; a mail that
// did not go out is a fact to report, not a reason to withdraw an access decision
// somebody made.
func (o ops) deliver(ctx context.Context, org, at, email, link string, until int64) string {
	body := "Your request has been granted.\n\n" +
		"Open it here: " + at + "/v1/dataroom/view/" + link + "\n\n" +
		"It admits " + email + " and closes on " + time.UnixMilli(until).UTC().Format(time.RFC1123) + "."
	_, err := cloud.Ask[plane.Send, plane.Sent](cloud.For(ctx, org), "notify", plane.NotifySend, &plane.Send{
		Org: org, Channel: "email", To: email, Subject: "Your document request", Body: body,
	})
	switch {
	case err == nil:
		return ""
	case errors.Is(err, cloud.ErrNoPeer):
		return "This deployment sends no mail — pass the address on yourself."
	default:
		o.s.Log.Error("trust grant mail failed", "err", err)
		return "The grant is live but the mail did not go out: " + err.Error()
	}
}

// ---- reads that need no transaction of the caller's ---------------------------

// centerOf resolves a public address to the org that publishes there and that
// org's centre, refusing anything that is not published. It is the one place every
// anonymous read goes through, so "unpublished" and "no such address" are the same
// answer and neither reports whether an org exists.
func (o ops) centerOf(ctx context.Context, slug string) (string, center, error) {
	org, ok, err := o.s.State.index.resolve(strings.ToLower(strings.TrimSpace(slug)))
	if err != nil {
		o.s.Log.Error("trust index read failed", "err", err)
		return "", center{}, zip.Errorf(http.StatusInternalServerError, "address lookup failed")
	}
	if !ok {
		return "", center{}, zip.ErrNotFound("no trust centre at that address")
	}
	var c center
	if err := o.s.State.host.Tx(ctx, org, func(tx *sql.Tx) (e error) { c, e = loadCenter(tx); return }); err != nil {
		return "", center{}, err
	}
	if !c.Published {
		return "", center{}, zip.ErrNotFound("no trust centre at that address")
	}
	return org, c, nil
}

// normal folds an artifact's declared tier and attester to the safe answer. A tier
// that is not exactly "public" is gated, so a typo, an empty field and a value
// invented next year all land private — the failure this design exists to prevent
// is a report becoming readable because nobody set a field. An attester that is not
// exactly "self" is an auditor, which is the same reasoning one column over.
//
// The database enforces the pairing on top of this (schema.go), so the two cannot
// disagree and no path through Go can publish what an auditor signed.
func normal(tier, attester string) (string, string) {
	if strings.TrimSpace(strings.ToLower(attester)) != "self" {
		return "gated", "auditor"
	}
	if strings.TrimSpace(strings.ToLower(tier)) != "public" {
		return "gated", "self"
	}
	return "public", "self"
}

// trustFile streams a PUBLIC item's bytes to anyone. Untyped because the answer is
// a byte stream under the document's own content type.
//
// Every narrowing is in the lookup rather than in a check this handler is trusted
// to remember: publicArtifact's WHERE carries the tier and the retirement, and
// centerOf refuses an unpublished centre, so a gated item, a retired item and an
// item of an org that has withdrawn are all simply not found — the same answer as
// an id that never existed, which is what keeps this from reporting what the gated
// tier holds.
func trustFile(s *cloud.Service[state], c *zip.Ctx) error {
	o := ops{s: s}
	org, _, err := o.centerOf(c.Context(), c.Param("slug"))
	if err != nil {
		return err
	}
	var a artifact
	var found bool
	if err := s.State.host.Tx(c.Context(), org, func(tx *sql.Tx) (e error) {
		a, found, e = publicArtifact(tx, c.Param("item"))
		return
	}); err != nil {
		s.Log.Error("trust item read failed", "err", err)
		return zip.Errorf(http.StatusInternalServerError, "item read failed")
	}
	if !found || a.DocumentID == "" {
		return zip.ErrNotFound("no such item")
	}
	resp, err := s.State.host.Dispatch(c.Context(), org, goja.BaseRequest{
		Route: "documents.file", Params: map[string]string{"id": a.DocumentID},
	})
	if err != nil {
		s.Log.Error("dataroom dispatch failed", "route", "documents.file", "err", err)
		return zip.Errorf(http.StatusInternalServerError, "dataroom dispatch failed")
	}
	return streamFile(s, c, resp)
}

// checkKind refuses a kind the page cannot draw, naming what it accepts.
func checkKind(kind string) error {
	if kinds[kind] {
		return nil
	}
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, k)
	}
	return zip.ErrBadRequest(fmt.Sprintf("kind must be one of: %s", strings.Join(sorted(names), ", ")))
}

func sorted(s []string) []string {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s
}
