package tax

// forms.go — a 1099 from draft to the payee's inbox.
//
//	draft → reviewed → furnished   Copy B delivered to a payee that consented
//	                 → owed        Copy B owed on paper; `mailed` records the mailing
//	draft | reviewed → void
//	furnished | owed → correct     a NEW form supersedes it; the old one is untouched
//
// A form's statement is final the moment it leaves the payer: the schema refuses
// to update a furnished or void form and refuses to delete any form (store.go),
// so a correction is the only way a furnished return changes, and it is a second
// return that names the first — which is what the IRS asks for.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/zap-proto/zip"
)

// Status is where a form stands.
type Status string

const (
	Draft     Status = "draft"
	Reviewed  Status = "reviewed"
	Furnished Status = "furnished"
	Owed      Status = "owed"
	Void      Status = "void"
)

// final reports whether the statement has left the payer.
func (s Status) final() bool { return s == Furnished || s == Owed }

// Party is a payer or recipient as the form prints them.
type Party struct {
	// Org is the Hanzo org.
	Org string `json:"org"`
	// Name is W-9 line 1.
	Name string `json:"name"`
	// BusinessName is W-9 line 2, when there is one.
	BusinessName string `json:"businessName,omitempty"`
	// Address is where the form says they are.
	Address Address `json:"address"`
	// Phone is the payer's telephone number; a recipient has none on the form.
	Phone string `json:"phone,omitempty"`
	// TIN is masked here always. The recipient's is truncated on Copy B as Pub 1099
	// allows; the payer's prints in full on the form itself, where the law requires it.
	TIN string `json:"tin"`
	// TINType is ssn or ein; empty when no TIN is held.
	TINType TINType `json:"tinType,omitempty"`
}

// Amount is one box's figure.
type Amount struct {
	// Box is the box number, e.g. "1a".
	Box string `json:"box"`
	// Label is the box's caption on the form.
	Label string `json:"label"`
	// Cents is the amount in cents.
	Cents int64 `json:"cents"`
}

// Withholding is the backup withholding a form carries.
type Withholding struct {
	// Required is true when the payer holds no TIN for the payee — the IRC
	// §3406(a)(1)(A) trigger these forms can know. An uncertified W-9 is not one for
	// these payments, and says so in Reason without requiring anything.
	Required bool `json:"required"`
	// Rate is the backup withholding rate, in percent: 24.
	Rate int `json:"rate,omitempty"`
	// DueCents is what 24% of the reported boxes comes to — what should have been
	// withheld. Nothing on Hanzo's rails withholds, so box 4 reports zero, and the
	// payer is liable for the amount it did not withhold (IRC §3403).
	DueCents int64 `json:"dueCents,omitempty"`
	// Reason says why, and which rule — or, when nothing is required, what the payer
	// should still know.
	Reason string `json:"reason,omitempty"`
}

// content is a form's statement: everything the form says. It is written once
// per status move while the form is a draft, and never after it leaves the payer.
type content struct {
	Kind      Kind        `json:"kind"`
	Year      int         `json:"year"`
	Corrected bool        `json:"corrected"`
	Account   string      `json:"account"`
	Payer     Party       `json:"payer"`
	Recipient Party       `json:"recipient"`
	Boxes     []Amount    `json:"boxes"`
	Backup    Withholding `json:"backup"`
	Certified bool        `json:"certified"`
	W9        string      `json:"w9"`
	Due       Deadline    `json:"due"`
	Payments  []string    `json:"payments"`
	Delivery  string      `json:"delivery,omitempty"`
	Furnished int64       `json:"furnished,omitempty"`
	Reason    string      `json:"reason,omitempty"`
	Notes     []string    `json:"notes,omitempty"`
}

// Form is one 1099 as the payer sees it.
type Form struct {
	// ID is the form's id, "f1099_"-prefixed.
	ID string `json:"id"`
	// Kind is 1099-NEC or 1099-MISC.
	Kind Kind `json:"kind"`
	// Year is the calendar year the payments were made.
	Year int `json:"year"`
	// Status is draft, reviewed, furnished, owed or void.
	Status Status `json:"status"`
	// Corrected is true for a form that corrects one already furnished.
	Corrected bool `json:"corrected"`
	// Supersedes is the form this one corrects.
	Supersedes string `json:"supersedes,omitempty"`
	// SupersededBy is the form that corrects this one, once there is one.
	SupersededBy string `json:"supersededBy,omitempty"`
	// Account is the account number the form prints — the original form's id,
	// which a correction keeps, so the IRS can tie the two.
	Account string `json:"account"`
	// Payer is the filer.
	Payer Party `json:"payer"`
	// Recipient is the payee.
	Recipient Party `json:"recipient"`
	// Boxes are the figures, box 4 (federal income tax withheld) included.
	Boxes []Amount `json:"boxes"`
	// Backup is the backup withholding the form carries.
	Backup Withholding `json:"backup"`
	// Certified is whether the payee's W-9 was certified when the form was prepared.
	Certified bool `json:"certified"`
	// W9 is where the payer's W-9 request stood when the form was prepared.
	W9 string `json:"w9"`
	// Due is when Copy B is due to the payee and the return to the IRS.
	Due Deadline `json:"due"`
	// Payments are the rail payment ids summed into the boxes.
	Payments []string `json:"payments"`
	// Delivery is electronic (Copy B in the payee's inbox) or paper, once furnished.
	Delivery string `json:"delivery,omitempty"`
	// FurnishedAt is when Copy B was delivered in the inbox, or marked owed on paper.
	FurnishedAt int64 `json:"furnishedAt,omitempty"`
	// MailedAt is when the payer recorded mailing a paper Copy B.
	MailedAt int64 `json:"mailedAt,omitempty"`
	// Reason is why a corrected form was made.
	Reason string `json:"reason,omitempty"`
	// Notes are what still needs a person, in words.
	Notes []string `json:"notes,omitempty"`
	// CreatedAt is when the form was prepared, unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when its status last moved, unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

func (o ops) formView(ctx context.Context, st *store, r *formRow) (*Form, error) {
	var c content
	if err := decode(r.content, &c); err != nil {
		return nil, err
	}
	next, err := st.successor(ctx, r.id)
	if err != nil {
		return nil, err
	}
	f := &Form{
		ID: r.id, Kind: r.kind, Year: r.year, Status: r.status, Corrected: c.Corrected,
		Supersedes: r.supersedes, SupersededBy: next, Account: c.Account, Payer: c.Payer, Recipient: c.Recipient,
		Boxes: c.Boxes, Backup: c.Backup, Certified: c.Certified, W9: c.W9, Due: c.Due, Payments: c.Payments,
		Delivery: c.Delivery, FurnishedAt: c.Furnished, Reason: c.Reason, Notes: c.Notes,
		CreatedAt: r.created, UpdatedAt: r.updated,
	}
	if r.status == Furnished && c.Delivery == "paper" {
		f.MailedAt = r.updated
	}
	return f, nil
}

// payerParty is the caller's own profile as the filer a form names. A form needs
// the filer's name, address and TIN, so a payer without them prepares nothing.
func (o ops) payerParty(ctx context.Context, org string, st *store) (Party, TIN, error) {
	r, n, row, err := o.loadProfile(ctx, org, st)
	t := n.us
	if err == errNotFound || (err == nil && t.Zero()) {
		return Party{}, TIN{}, zip.Errorf(http.StatusConflict, "write this org's tax profile first: a 1099 names its filer's name, address and TIN")
	}
	if err != nil {
		return Party{}, TIN{}, err
	}
	return Party{Org: org, Name: r.Name, BusinessName: r.BusinessName, Address: r.Address, Phone: r.Phone,
		TIN: t.String(), TINType: t.Type}, row.tin, nil
}

// recipient is the payee as a form prints it, and its TIN, which the payer's store seals
// under the payer's key when the form is written. Without a live grant the payer knows the
// org id and nothing else.
func (o ops) recipient(payee string, cp counterparty) (Party, TIN) {
	if cp.w9 == nil {
		return Party{Org: payee, Name: payee}, TIN{}
	}
	p := Party{Org: payee, Name: cp.w9.Name, BusinessName: cp.w9.BusinessName, Address: cp.w9.Address, TINType: cp.w9.TINType}
	if cp.tin.Zero() {
		return p, TIN{}
	}
	p.TIN = cp.tin.String()
	return p, cp.tin
}

// backup states whether a form carries backup withholding, by IRC §3406(a)(1).
//
// For the payments these forms report — nonemployee compensation, rents,
// royalties, other income — §3406 reaches a payee in two cases a payer can be in:
// (A) the payee did not furnish a TIN, and (B) the IRS notified the payer that the
// TIN furnished is incorrect (a CP2100 or CP2100A "B" notice). (C) and (D) — an
// underreporting notice, and a failure to certify against one — apply to
// reportable interest and dividends only. Not signing the W-9 certification is
// not a trigger for these payments: the payee "must give your correct TIN, but you
// do not have to sign the certification unless you have been notified that you
// have previously given an incorrect TIN" (Form W-9, Rev. March 2024, Signature
// requirements, item 4, "Other payments"; Pub 1281, Rev. 12-2023, Q&A 20).
//
// So (A) is decided here, from what the payer holds. (B) turns on an IRS notice
// this surface never sees, and an uncertified W-9 is a warning, stated with the
// rule, never a withholding.
func backup(cp counterparty, payee TIN, reported int64) Withholding {
	switch {
	case !cp.granted():
		return withhold(reported, "The payee has not granted its W-9 to the payer, so the payer holds no TIN for it: "+
			"backup withholding applies (IRC §3406(a)(1)(A); Pub 1281, Rev. 12-2023, Missing TIN(s)).")
	case payee.Zero():
		return withhold(reported, "The payee's W-9 carries no TIN: backup withholding applies "+
			"(IRC §3406(a)(1)(A); Pub 1281, Rev. 12-2023, Missing TIN(s)).")
	case !cp.certOK:
		return Withholding{Reason: uncertified}
	}
	return Withholding{}
}

// uncertified is the warning an uncertified W-9 carries on these forms.
const uncertified = "Not required, but note: the payee's W-9 is not certified for its current version. For these payments " +
	"a payee must furnish its correct TIN but need not sign the certification unless it was notified that it previously gave an " +
	"incorrect TIN (Form W-9, Rev. March 2024, Signature requirements, item 4; Pub 1281, Rev. 12-2023, Q&A 20). If the IRS has " +
	"sent this payer a CP2100 or CP2100A notice naming this payee, backup withholding applies under IRC §3406(a)(1)(B) until the " +
	"payee furnishes a certified TIN (Pub 1281, Q&A 19); that notice reaches the payer, not this surface."

// withhold is the withholding (A) requires: 24% of the reported boxes.
func withhold(reported int64, reason string) Withholding {
	return Withholding{Required: true, Rate: BackupRate, DueCents: due(reported), Reason: reason}
}

// due is 24% of reported cents, rounded half up to the cent.
func due(reported int64) int64 { return (reported*BackupRate + 50) / 100 }

// reported sums a form's money boxes — every box but box 4, which is the
// withholding itself.
func reported(k Kind, boxes []Amount) int64 {
	var n int64
	for _, b := range boxes {
		if b.Box != withheld[k].Box {
			n += b.Cents
		}
	}
	return n
}

// notes says what a form still needs from a person.
func notes(c content) []string {
	var out []string
	if c.W9 != shareGranted {
		out = append(out, "No W-9 from this payee: its name, address and TIN are unknown. Request it with POST /v1/tax/w9.")
	}
	if c.Backup.Required {
		out = append(out, fmt.Sprintf("Backup withholding of %s applies and was not withheld on Hanzo's rails; the payer owes it.", money.Cents(c.Backup.DueCents)))
	}
	if c.W9 == shareGranted && !c.Certified {
		out = append(out, "The payee's W-9 is not certified; no backup withholding follows from that alone for these payments (see backup).")
	}
	if c.Recipient.TIN == "" {
		out = append(out, "No recipient TIN: the return is filed without one, which the IRS treats as a missing TIN.")
	}
	return out
}

// build is the statement for one payee, kind and year from the derived totals.
func (o ops) build(payerP Party, payee string, cp counterparty, k Kind, year int, totals []Total) (content, TIN) {
	rp, payeeTIN := o.recipient(payee, cp)
	c := content{Kind: k, Year: year, Payer: payerP, Recipient: rp, Certified: cp.certOK, W9: cp.status(),
		Due: DeadlineOf(k, year)}
	for _, t := range totals {
		c.Boxes = append(c.Boxes, Amount{Box: t.Box.Box, Label: t.Box.Label, Cents: t.AmountCents})
		c.Payments = append(c.Payments, t.Payments...)
	}
	c.Boxes = append(c.Boxes, Amount{Box: withheld[k].Box, Label: withheld[k].Label})
	c.Backup = backup(cp, payeeTIN, reported(k, c.Boxes))
	c.Notes = notes(c)
	return c, payeeTIN
}

// prepareIn names the tax year to prepare.
type prepareIn struct {
	// Year is the calendar year the payments were made in.
	Year int `json:"year"`
}

// Prepared is what a preparation did.
type Prepared struct {
	// Year is the tax year.
	Year int `json:"year"`
	// Forms are the drafts made or refreshed.
	Forms []Form `json:"forms"`
	// Skipped says, per payee and form, why nothing was prepared — a form already
	// furnished is never re-prepared, only corrected.
	Skipped []string `json:"skipped"`
}

// Prepares the caller org's 1099 drafts for a tax year, as the PAYER: one
// 1099-NEC and/or 1099-MISC per payee whose reportable payments on Hanzo's rails
// reach the year's threshold, from the same derivation GET /v1/tax/payments
// answers. Org admins only.
//
// Each draft carries the payee as its W-9 names it — under a live grant, and
// otherwise only its org id — its boxes, box 4, the due dates, the payments it
// sums, and backup withholding at 24% when the payer holds no TIN for the payee
// (IRC §3406(a)(1)(A)) — an uncertified W-9 is stated as a warning, not a
// withholding, because these payments do not require the certification. Preparing
// again refreshes a draft or reviewed form in place (a reviewed one goes back to
// draft, to be reviewed again); one payee has at most one live form per kind and
// year, so a second preparation racing the first is refused, never doubled. A form
// already furnished is left alone and named in skipped — change it by correcting
// it. Nothing is furnished to anyone and nothing is filed with the IRS.
//
// Example: {"year": 2026}
func (o ops) prepare(ctx context.Context, in *prepareIn) (*Prepared, error) {
	payer, st, err := o.admin(ctx, "prepare 1099s", true)
	if err != nil {
		return nil, err
	}
	payerP, payerTIN, err := o.payerParty(ctx, payer, st)
	if err != nil {
		return nil, err
	}
	l, cps, err := o.derive(ctx, payer, st, in.Year)
	if err != nil {
		return nil, err
	}
	existing, err := st.forms(ctx, in.Year)
	if err != nil {
		return nil, err
	}
	out := &Prepared{Year: in.Year, Forms: []Form{}, Skipped: []string{}}
	for _, py := range l.Payees {
		for _, k := range []Kind{NEC, MISC} {
			var totals []Total
			for _, t := range py.Totals {
				if t.Box.Kind == k && t.Reportable {
					totals = append(totals, t)
				}
			}
			if len(totals) == 0 {
				continue
			}
			f, why, err := o.prepareOne(ctx, st, payer, payerP, payerTIN, py.Payee, cps[py.Payee], k, in.Year, totals, existing)
			if err != nil {
				return nil, err
			}
			if why != "" {
				out.Skipped = append(out.Skipped, why)
				continue
			}
			out.Forms = append(out.Forms, *f)
		}
	}
	noStore(ctx)
	return out, nil
}

func (o ops) prepareOne(ctx context.Context, st *store, payer string, payerP Party, payerTIN TIN, payee string,
	cp counterparty, k Kind, year int, totals []Total, existing []formRow) (*Form, string, error) {
	var draft *formRow
	for i := range existing {
		r := &existing[i]
		if r.payee != payee || r.kind != k || r.status == Void {
			continue
		}
		if r.status.final() {
			return nil, fmt.Sprintf("%s %d to %s is %s; correct it to change it (POST /v1/tax/forms/%s/correct)", k, year, payee, r.status, r.id), nil
		}
		if r.supersedes == "" {
			draft = r
		}
	}
	c, payeeTIN := o.build(payerP, payee, cp, k, year, totals)
	ch, usage, err := o.charge(ctx, actPrepare)
	if err != nil {
		return nil, "", err
	}
	defer ch.Release()
	t := now()
	row := formRow{payee: payee, year: year, kind: k, status: Draft, payerTIN: payerTIN, payeeTIN: payeeTIN, updated: t}
	if draft != nil {
		row.id, row.created = draft.id, draft.created
		c.Account = row.id
		row.content = encode(c)
		if err := st.updateForm(ctx, row, draft.status); err != nil {
			return nil, "", conflict(err)
		}
	} else {
		row.id, row.created = mint.ID("f1099"), t
		c.Account = row.id
		row.content = encode(c)
		if err := st.insertForm(ctx, row); err != nil {
			return nil, "", conflict(err)
		}
	}
	ch.Debit(usage)
	if err := o.commit(ctx, payer, "tax.form.prepared", audit.Resource{Type: "tax.form", ID: row.id},
		map[string]any{"payee": payee, "kind": k, "year": year, "status": Draft, "backup": c.Backup.Required}); err != nil {
		return nil, "", err
	}
	f, err := o.formView(ctx, st, &row)
	return f, "", err
}

func conflict(err error) error {
	if err == errMoved {
		return zip.Errorf(http.StatusConflict, "the form changed while this ran; read it and try again")
	}
	return err
}

// formsIn filters the forms listing.
type formsIn struct {
	// Year narrows the list to one tax year; absent lists every year.
	Year int `json:"year,omitempty"`
}

// formList is the caller's forms.
type formList struct {
	// Data are the forms, oldest first.
	Data []Form `json:"data"`
}

// Lists the 1099s the caller org prepared as payer, oldest first, voided and
// superseded ones included — a return that was furnished stays on the record.
// Org admins only.
func (o ops) forms(ctx context.Context, in *formsIn) (*formList, error) {
	_, st, err := o.admin(ctx, "read 1099s", false)
	if err != nil {
		return nil, err
	}
	rows, err := st.forms(ctx, in.Year)
	if err != nil {
		return nil, err
	}
	out := &formList{Data: []Form{}}
	for i := range rows {
		f, err := o.formView(ctx, st, &rows[i])
		if err != nil {
			return nil, err
		}
		out.Data = append(out.Data, *f)
	}
	noStore(ctx)
	return out, nil
}

// formRef addresses one form.
type formRef struct {
	// ID is the form id, "f1099_"-prefixed.
	ID string `json:"id"`
}

func (o ops) load(ctx context.Context, act string, write bool, id string) (string, *store, *formRow, error) {
	org, st, err := o.admin(ctx, act, write)
	if err != nil {
		return "", nil, nil, err
	}
	r, err := st.form(ctx, id)
	if err == errNotFound {
		return "", nil, nil, zip.ErrNotFound("form not found")
	}
	if err != nil {
		return "", nil, nil, err
	}
	return org, st, r, nil
}

// Reads one 1099 the caller org prepared as payer, every box and both parties,
// TINs masked. Org admins only; another org's form is not found.
func (o ops) form(ctx context.Context, in *formRef) (*Form, error) {
	_, st, r, err := o.load(ctx, "read a 1099", false, in.ID)
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	return o.formView(ctx, st, r)
}

// move changes a form's status after checking it may.
func (o ops) move(ctx context.Context, org string, st *store, r *formRow, to Status, action string, c *content, from ...Status) (*Form, error) {
	if !slices.Contains(from, r.status) {
		return nil, zip.Errorf(http.StatusConflict, "the form is %s; it cannot become %s", r.status, to)
	}
	prev := r.status
	next := *r
	next.status, next.updated = to, now()
	if c != nil {
		next.content = encode(*c)
	}
	if err := st.updateForm(ctx, next, prev); err != nil {
		return nil, conflict(err)
	}
	if err := o.commit(ctx, org, action, audit.Resource{Type: "tax.form", ID: r.id},
		map[string]any{"payee": r.payee, "kind": r.kind, "year": r.year, "from": prev, "status": to}); err != nil {
		return nil, err
	}
	noStore(ctx)
	return o.formView(ctx, st, &next)
}

// Marks a draft 1099 reviewed — a person has checked it. Only a reviewed form can
// be furnished. Org admins only.
func (o ops) review(ctx context.Context, in *formRef) (*Form, error) {
	org, st, r, err := o.load(ctx, "review a 1099", true, in.ID)
	if err != nil {
		return nil, err
	}
	return o.move(ctx, org, st, r, Reviewed, "tax.form.reviewed", nil, Draft)
}

// Voids a draft or reviewed 1099, which is then never furnished or filed. A form
// already furnished cannot be voided: it is corrected, and a correction to zero
// is how a furnished return is withdrawn (Pub 1099 Part H). Org admins only.
func (o ops) void(ctx context.Context, in *formRef) (*Form, error) {
	org, st, r, err := o.load(ctx, "void a 1099", true, in.ID)
	if err != nil {
		return nil, err
	}
	if r.status.final() {
		return nil, zip.Errorf(http.StatusConflict, "the form is %s: a furnished return is corrected, never voided — correct it with zero amounts", r.status)
	}
	return o.move(ctx, org, st, r, Void, "tax.form.voided", nil, Draft, Reviewed)
}

// Furnishes Copy B of a reviewed 1099 to its payee. Org admins only.
//
// When the payee's tax profile consents to electronic delivery, Copy B is
// delivered into the payee's /v1/tax/inbox and the form is furnished. Otherwise it
// is OWED on paper at the payee's address: mailing it is the payer's, and
// POST /v1/tax/forms/{id}/mailed records it. Either way the statement is final
// from this call — the form can no longer change, only be corrected. Calling it
// again on a form furnished electronically re-delivers Copy B if the inbox lacks
// it, and changes nothing else.
//
// It does not file anything with the IRS, and it does not send the notice of
// posting Pub 1099 Part M asks a furnisher to give an electronic recipient; the
// statement is in the recipient's inbox, and telling them so is the payer's.
func (o ops) furnish(ctx context.Context, in *formRef) (*Form, error) {
	org, st, r, err := o.load(ctx, "furnish a 1099", true, in.ID)
	if err != nil {
		return nil, err
	}
	var c content
	if err := decode(r.content, &c); err != nil {
		return nil, err
	}
	if r.status == Furnished && c.Delivery == "electronic" {
		if err := o.deliver(ctx, org, r, c); err != nil {
			return nil, err
		}
		noStore(ctx)
		return o.formView(ctx, st, r)
	}
	if r.status != Reviewed {
		return nil, zip.Errorf(http.StatusConflict, "the form is %s; review it before furnishing it", r.status)
	}
	est, err := o.other(r.payee)
	if err != nil {
		return nil, err
	}
	electronic := false
	if prof, err := est.profile(ctx); err == nil {
		var pr record
		if err := decode(prof.data, &pr); err != nil {
			return nil, err
		}
		electronic = pr.Consent.Electronic
	} else if err != errNotFound {
		return nil, err
	}
	if !electronic && !c.Recipient.Address.complete() {
		return nil, zip.Errorf(http.StatusConflict,
			"the payee has not consented to electronic delivery and the form holds no mailing address for it; request its W-9")
	}
	ch, usage, err := o.charge(ctx, actFurnish)
	if err != nil {
		return nil, err
	}
	defer ch.Release()
	c.Furnished, c.Delivery = now(), "paper"
	to, action := Owed, "tax.form.owed"
	if electronic {
		c.Delivery, to, action = "electronic", Furnished, "tax.form.furnished"
	}
	// The form is made final FIRST. A delivery that fails after it is repaired by
	// calling this again; a delivery that landed for a form that then failed to
	// move would be a statement the payer's record does not know it sent.
	f, err := o.move(ctx, org, st, r, to, action, &c, Reviewed)
	if err != nil {
		return nil, err
	}
	if electronic {
		if err := o.deliver(ctx, org, r, c); err != nil {
			return nil, err
		}
	}
	ch.Debit(usage)
	return f, nil
}

// deliver writes Copy B into the payee's inbox, once. The payer's own TIN travels
// resealed under the PAYEE's key, because the payee's copy prints it in full.
func (o ops) deliver(ctx context.Context, payer string, r *formRow, c content) error {
	est, err := o.other(r.payee)
	if err != nil {
		return err
	}
	if _, err := est.statement(ctx, r.id); err == nil {
		return nil
	} else if err != errNotFound {
		return err
	}
	if err := est.receive(ctx, receivedRow{id: r.id, payer: payer, year: r.year, kind: r.kind,
		supersedes: r.supersedes, content: encode(c), payerTIN: r.payerTIN, furnished: c.Furnished}); err != nil {
		return err
	}
	return o.ship(r.payee)
}

// Records that the payer mailed a paper Copy B it owed. Org admins only.
//
// Mailing is the payer's act, done outside Hanzo; this is the record of it, made
// by the person who did it and kept on the audit trail. Nothing here mails
// anything.
func (o ops) mailed(ctx context.Context, in *formRef) (*Form, error) {
	org, st, r, err := o.load(ctx, "record a mailed 1099", true, in.ID)
	if err != nil {
		return nil, err
	}
	return o.move(ctx, org, st, r, Furnished, "tax.form.mailed", nil, Owed)
}

// correctIn is a correction.
type correctIn struct {
	// ID is the furnished form to correct, from the path.
	ID string `json:"id"`
	// Reason says what was wrong. Required; it goes on the record.
	Reason string `json:"reason"`
	// Boxes are the corrected amounts, by box. Omitted, the boxes are derived
	// again from the payments and the payee's W-9 is read again — which is also how a
	// wrong name or TIN is corrected. Zero in every box withdraws the return.
	Boxes []Amount `json:"boxes,omitempty"`
}

// Corrects a furnished (or owed) 1099 with a NEW form that supersedes it. Org
// admins only.
//
// The furnished form is never changed: the correction is a second form, marked
// CORRECTED, carrying the original's account number and naming the form it
// supersedes, and it goes through review and furnishing like any other. State the
// corrected boxes, or omit them to derive the year again from the payments and the
// payee's current W-9. A form already superseded is corrected by correcting its
// successor. It files nothing with the IRS.
//
// Example: {"reason": "a December settlement was refunded", "boxes": [{"box": "1a", "cents": 250000}]}
func (o ops) correct(ctx context.Context, in *correctIn) (*Form, error) {
	org, st, r, err := o.load(ctx, "correct a 1099", true, in.ID)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(in.Reason)
	switch {
	case !r.status.final():
		return nil, zip.Errorf(http.StatusConflict, "the form is %s: only a furnished form is corrected; change the draft instead", r.status)
	case reason == "":
		return nil, zip.ErrBadRequest("reason is required: a correction says what was wrong")
	}
	if next, err := st.successor(ctx, r.id); err != nil {
		return nil, err
	} else if next != "" {
		return nil, zip.Errorf(http.StatusConflict, "the form is already superseded by %s; correct that one", next)
	}
	var old content
	if err := decode(r.content, &old); err != nil {
		return nil, err
	}
	c, payeeTIN := old, r.payeeTIN
	if len(in.Boxes) > 0 {
		boxes, err := corrected(old, in.Boxes)
		if err != nil {
			return nil, err
		}
		c.Boxes = boxes
		if c.Backup.Required {
			// What should have been withheld follows the corrected figures: a return
			// corrected to zero owes nothing.
			c.Backup.DueCents = due(reported(c.Kind, c.Boxes))
		}
	} else {
		payerP, _, err := o.payerParty(ctx, org, st)
		if err != nil {
			return nil, err
		}
		l, cps, err := o.derive(ctx, org, st, r.year)
		if err != nil {
			return nil, err
		}
		var totals []Total
		for _, py := range l.Payees {
			if py.Payee != r.payee {
				continue
			}
			for _, t := range py.Totals {
				if t.Box.Kind == r.kind {
					totals = append(totals, t) // below threshold too: a correction reports what was paid
				}
			}
		}
		c, payeeTIN = o.build(payerP, r.payee, cps[r.payee], r.kind, r.year, totals)
	}
	c.Corrected, c.Account, c.Reason = true, old.Account, reason
	c.Delivery, c.Furnished = "", 0
	c.Notes = notes(c)
	t := now()
	row := formRow{id: mint.ID("f1099"), payee: r.payee, year: r.year, kind: r.kind, status: Draft,
		supersedes: r.id, content: encode(c), payerTIN: r.payerTIN, payeeTIN: payeeTIN, created: t, updated: t}
	if err := st.insertForm(ctx, row); err != nil {
		return nil, conflict(err)
	}
	if err := o.commit(ctx, org, "tax.form.corrected", audit.Resource{Type: "tax.form", ID: row.id},
		map[string]any{"payee": r.payee, "kind": r.kind, "year": r.year, "supersedes": r.id, "status": Draft}); err != nil {
		return nil, err
	}
	noStore(ctx)
	return o.formView(ctx, st, &row)
}

// corrected applies stated box amounts to a form's boxes. A box the form does not
// have is refused rather than added: a correction changes figures, not the form.
func corrected(old content, in []Amount) ([]Amount, error) {
	out := slices.Clone(old.Boxes)
	for _, b := range in {
		i := slices.IndexFunc(out, func(a Amount) bool { return a.Box == b.Box })
		if i < 0 {
			return nil, zip.ErrBadRequest(fmt.Sprintf("%s has no box %q here", old.Kind, b.Box))
		}
		if b.Cents < 0 {
			return nil, zip.ErrBadRequest("an amount is never negative")
		}
		out[i].Cents = b.Cents
	}
	return out, nil
}

// ---- the payee's inbox ------------------------------------------------------

// Statement is one Copy B furnished to the caller org.
type Statement struct {
	// ID is the form id the payer issued it under.
	ID string `json:"id"`
	// Payer is the org that furnished it.
	Payer Party `json:"payer"`
	// Recipient is this org, as the form names it, TIN truncated.
	Recipient Party `json:"recipient"`
	// Kind is 1099-NEC or 1099-MISC.
	Kind Kind `json:"kind"`
	// Year is the calendar year of the payments.
	Year int `json:"year"`
	// Corrected is true for a statement that corrects an earlier one.
	Corrected bool `json:"corrected"`
	// Supersedes is the earlier statement this corrects.
	Supersedes string `json:"supersedes,omitempty"`
	// SupersededBy is the later statement that corrects this one.
	SupersededBy string `json:"supersededBy,omitempty"`
	// Account is the account number the form prints.
	Account string `json:"account"`
	// Boxes are the figures.
	Boxes []Amount `json:"boxes"`
	// Backup is the backup withholding the payer applied.
	Backup Withholding `json:"backup"`
	// FurnishedAt is when it was delivered, unix seconds.
	FurnishedAt int64 `json:"furnishedAt"`
}

func statementOf(r *receivedRow, next string) (*Statement, error) {
	var c content
	if err := decode(r.content, &c); err != nil {
		return nil, err
	}
	return &Statement{ID: r.id, Payer: c.Payer, Recipient: c.Recipient, Kind: r.kind, Year: r.year,
		Corrected: c.Corrected, Supersedes: r.supersedes, SupersededBy: next, Account: c.Account,
		Boxes: c.Boxes, Backup: c.Backup, FurnishedAt: r.furnished}, nil
}

// statementList is the caller's inbox.
type statementList struct {
	// Data are the statements, oldest first.
	Data []Statement `json:"data"`
}

// Lists the Copy B statements furnished to the caller org — every 1099 a payer on
// Hanzo delivered to it electronically, corrected ones beside the ones they
// supersede. Any member may read it; nothing here can be changed or removed.
func (o ops) inbox(ctx context.Context, in *formsIn) (*statementList, error) {
	_, st, err := o.member(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := st.statements(ctx, in.Year)
	if err != nil {
		return nil, err
	}
	next := map[string]string{}
	for _, r := range rows {
		if r.supersedes != "" {
			next[r.supersedes] = r.id
		}
	}
	out := &statementList{Data: []Statement{}}
	for i := range rows {
		s, err := statementOf(&rows[i], next[rows[i].id])
		if err != nil {
			return nil, err
		}
		out.Data = append(out.Data, *s)
	}
	noStore(ctx)
	return out, nil
}

// Reads one Copy B furnished to the caller org. Any member may read it; another
// org's statement is not found.
func (o ops) statement(ctx context.Context, in *formRef) (*Statement, error) {
	_, st, err := o.member(ctx)
	if err != nil {
		return nil, err
	}
	r, err := st.statement(ctx, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("statement not found")
	}
	if err != nil {
		return nil, err
	}
	rows, err := st.statements(ctx, r.year)
	if err != nil {
		return nil, err
	}
	next := ""
	for _, x := range rows {
		if x.supersedes == r.id {
			next = x.id
		}
	}
	noStore(ctx)
	return statementOf(r, next)
}
