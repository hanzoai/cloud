package tax

// w9.go — one company asking another for its W-9, and the other deciding.
//
// The exchange is for the payee's FORM, whichever it certified: a U.S. person's
// W-9, or a foreign person's W-8BEN or W-8BEN-E (w8.go). One grant covers it, and
// a payer never chooses which form it reads — the payee's status decides that.
//
// The request is the PAYER's act and is written into the PAYEE's file, because
// the answer is the payee's: grant, decline, revoke. The payer keeps its own row —
// who it asked and what it pays them for — and reads the payee's W-9 only while
// the grant stands, every read re-checking it in the payee's file. There is no
// copy of a W-9 anywhere a revocation cannot reach, except the TIN on a 1099 the
// payer already prepared: the law requires the payer to keep that, and it is
// never answered in full outside the filing export.

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

const (
	shareRequested = "requested"
	shareGranted   = "granted"
	shareDeclined  = "declined"
	shareRevoked   = "revoked"
)

// W9 is one W-9 relationship between a payer and a payee, from the caller's side.
type W9 struct {
	// ID addresses the relationship on both sides.
	ID string `json:"id"`
	// Role is the caller's side of it: payer or payee.
	Role string `json:"role"`
	// Payer is the org that asked.
	Payer string `json:"payer"`
	// Payee is the org whose W-9 it is.
	Payee string `json:"payee"`
	// Status is requested, granted, declined or revoked.
	Status string `json:"status"`
	// Category is what the payer states it pays this payee for. Only the payer
	// sees it.
	Category Category `json:"category,omitempty"`
	// Match is the IRS TIN Matching result the payer recorded, when it recorded one.
	Match *Match `json:"match,omitempty"`
	// Profile is the payee's W-9, TIN masked — present only for the payer, only
	// while the grant is live.
	Profile *Profile `json:"profile,omitempty"`
	// RequestedAt is when the payer asked, unix seconds.
	RequestedAt int64 `json:"requestedAt"`
	// DecidedAt is when the payee last granted, declined or revoked.
	DecidedAt int64 `json:"decidedAt,omitempty"`
}

// shareData is the decision's who and when.
type shareData struct {
	DecidedAt int64  `json:"decidedAt,omitempty"`
	DecidedBy string `json:"decidedBy,omitempty"`
}

// Match is a payer's record of an IRS TIN Matching result for a payee.
type Match struct {
	// Result is what IRS TIN Matching answered: matched or mismatched.
	Result string `json:"result"`
	// At is when the payer recorded it, unix seconds.
	At int64 `json:"at"`
	// By is the admin who recorded it.
	By string `json:"by"`
}

// payeeData is the payer's own notes on a payee.
type payeeData struct {
	Match *Match `json:"match,omitempty"`
}

// w9Request asks a counterparty for its W-9.
type w9Request struct {
	// Payee is the org id of the company asked. The caller must have paid it on
	// Hanzo's rails.
	Payee string `json:"payee"`
	// Category is what the caller pays it for: services, attorney, rents,
	// royalties, other or merchandise. Services when omitted.
	Category Category `json:"category,omitempty"`
}

// Asks another org for its W-9, as the org that pays it. Org admins only.
//
// A request is refused unless the caller has paid that org on Hanzo's rails, or
// has asked /v1/principal whether it may — a W-9 is asked for by a payer, of a payee,
// and the payments, or a payment the caller is about to make, are what say which
// companies those are. The form is needed BEFORE the first payment: withholding
// is decided when a payment is made, not when it is reported. The request lands with the payee, whose admin grants or
// declines it; until it is granted the caller sees its status and nothing of the
// payee's W-9. Asking again updates the category, and re-opens a request the
// payee declined or revoked.
//
// Category is the caller's statement of what it pays this payee for, and decides
// the 1099 box: services and attorney go to 1099-NEC box 1a, rents, royalties
// and other to 1099-MISC boxes 1, 2 and 3; merchandise is not reported.
//
// Example: {"payee": "acme", "category": "services"}
func (o ops) requestW9(ctx context.Context, in *w9Request) (*W9, error) {
	payer, pst, err := o.admin(ctx, "request a W-9", true)
	if err != nil {
		return nil, err
	}
	payee := strings.TrimSpace(in.Payee)
	cat := in.Category
	if cat == "" {
		cat = Services
	}
	switch {
	case !principal.NamesOrg(payee):
		return nil, zip.ErrBadRequest("payee is an org id")
	case payee == payer:
		return nil, zip.ErrBadRequest("an org does not request its own W-9")
	case !cat.Valid():
		return nil, zip.ErrBadRequest("category is services, attorney, rents, royalties, other or merchandise")
	}
	ok, err := o.payer(ctx, payer, payee)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, zip.Errorf(http.StatusConflict,
			"no payment from this org to %s on Hanzo's rails, and no clearance to pay it asked of /v1/principal: "+
				"a W-9 is requested only by an org that pays or is about to pay the payee", payee)
	}
	est, err := o.other(payee)
	if err != nil {
		return nil, err
	}
	t := now()
	sh, err := est.shareFor(ctx, payer)
	switch {
	case err == errNotFound:
		sh = &shareRow{id: mint.ID("w9"), payer: payer, status: shareRequested, data: encode(shareData{}), created: t, updated: t}
		if err := est.putShare(ctx, *sh); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case sh.status == shareDeclined || sh.status == shareRevoked:
		sh.status, sh.updated = shareRequested, t
		if err := est.putShare(ctx, *sh); err != nil {
			return nil, err
		}
	}
	row, err := pst.payee(ctx, payee)
	switch {
	case err == errNotFound:
		row = &payeeRow{payee: payee, data: encode(payeeData{}), created: t}
	case err != nil:
		return nil, err
	}
	row.share, row.category, row.updated = sh.id, cat, t
	if err := pst.putPayee(ctx, *row); err != nil {
		return nil, err
	}
	if err := o.commit(ctx, payer, "tax.w9.requested", audit.Resource{Type: "tax.w9", ID: sh.id},
		map[string]any{"payer": payer, "payee": payee, "category": cat, "status": sh.status}, payee); err != nil {
		return nil, err
	}
	return o.payerView(ctx, payer, row, sh, false)
}

// payerView is a relationship as the payer sees it: the payee's W-9 only under a
// live grant.
func (o ops) payerView(ctx context.Context, payer string, row *payeeRow, sh *shareRow, profile bool) (*W9, error) {
	var pd payeeData
	if err := decode(row.data, &pd); err != nil {
		return nil, err
	}
	var sd shareData
	if err := decode(sh.data, &sd); err != nil {
		return nil, err
	}
	w := &W9{ID: sh.id, Role: "payer", Payer: payer, Payee: row.payee, Status: sh.status, Category: row.category,
		Match: pd.Match, RequestedAt: sh.created, DecidedAt: sd.DecidedAt}
	if profile && sh.status == shareGranted {
		est, err := o.other(row.payee)
		if err != nil {
			return nil, err
		}
		r, n, prow, err := o.loadProfile(ctx, row.payee, est)
		switch {
		case err == errNotFound:
		case err != nil:
			return nil, err
		default:
			p := view(r, n, prow)
			p.Consent, p.Disclosure = nil, "" // the payee's delivery choice is not the payer's to read
			// Whether it is certified is the payer's to know; which document and which
			// of the payee's people signed it are not.
			p.Certification = Certification{Status: p.Certification.Status, Version: p.Certification.Version, Signed: p.Certification.Signed}
			w.Profile = p
		}
	}
	return w, nil
}

func payeeView(payee string, sh *shareRow) (*W9, error) {
	var sd shareData
	if err := decode(sh.data, &sd); err != nil {
		return nil, err
	}
	return &W9{ID: sh.id, Role: "payee", Payer: sh.payer, Payee: payee, Status: sh.status,
		RequestedAt: sh.created, DecidedAt: sd.DecidedAt}, nil
}

// w9List is both sides of the caller's W-9 relationships.
type w9List struct {
	// AsPayer are the W-9s the caller asked for.
	AsPayer []W9 `json:"asPayer"`
	// AsPayee are the requests for the caller's own W-9.
	AsPayee []W9 `json:"asPayee"`
}

// Lists the caller org's W-9 relationships on both sides: the W-9s it asked its
// payees for, with where each stands, and the requests other orgs made for its
// own W-9. Org admins only. No W-9 content is in the list; read one to see it.
func (o ops) listW9(ctx context.Context, _ *cloud.Unit) (*w9List, error) {
	org, st, err := o.admin(ctx, "read W-9 requests", false)
	if err != nil {
		return nil, err
	}
	out := &w9List{AsPayer: []W9{}, AsPayee: []W9{}}
	rows, err := st.payees(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		est, err := o.other(rows[i].payee)
		if err != nil {
			return nil, err
		}
		sh, err := est.share(ctx, rows[i].share)
		if err == errNotFound || (err == nil && sh.payer != org) {
			continue
		}
		if err != nil {
			return nil, err
		}
		w, err := o.payerView(ctx, org, &rows[i], sh, false)
		if err != nil {
			return nil, err
		}
		out.AsPayer = append(out.AsPayer, *w)
	}
	shares, err := st.shares(ctx)
	if err != nil {
		return nil, err
	}
	for i := range shares {
		w, err := payeeView(org, &shares[i])
		if err != nil {
			return nil, err
		}
		out.AsPayee = append(out.AsPayee, *w)
	}
	noStore(ctx)
	return out, nil
}

// w9Ref addresses one relationship.
type w9Ref struct {
	// ID is the relationship's id, "w9_"-prefixed.
	ID string `json:"id"`
}

// side resolves a relationship id to the caller's side of it. The payee side is
// the payee's own file; the payer side is found through the payer's own row, and
// the share it names must name the payer back — so an id reaches only a
// relationship the caller is party to, and any other id is not found.
func (o ops) side(ctx context.Context, org string, st *store, id string) (*payeeRow, *shareRow, error) {
	if sh, err := st.share(ctx, id); err == nil {
		return nil, sh, nil
	} else if err != errNotFound {
		return nil, nil, err
	}
	row, err := st.payeeByShare(ctx, id)
	if err == errNotFound {
		return nil, nil, zip.ErrNotFound("W-9 request not found")
	}
	if err != nil {
		return nil, nil, err
	}
	est, err := o.other(row.payee)
	if err != nil {
		return nil, nil, err
	}
	sh, err := est.share(ctx, id)
	if err == errNotFound || (err == nil && sh.payer != org) {
		return nil, nil, zip.ErrNotFound("W-9 request not found")
	}
	if err != nil {
		return nil, nil, err
	}
	return row, sh, nil
}

// Reads one W-9 relationship from the caller's side. Org admins only.
//
// As the PAYER, the answer carries the payee's W-9 — every line, the TIN masked
// to its last four digits, and whether it is certified — but only while the
// payee's grant is live; before it is granted, and after it is declined or
// revoked, there is no W-9 in the answer. As the PAYEE, it is the request and
// where it stands.
func (o ops) w9(ctx context.Context, in *w9Ref) (*W9, error) {
	org, st, err := o.admin(ctx, "read a W-9", false)
	if err != nil {
		return nil, err
	}
	row, sh, err := o.side(ctx, org, st, in.ID)
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	if row == nil {
		return payeeView(org, sh)
	}
	return o.payerView(ctx, org, row, sh, true)
}

// tinOut is a payee's numbers in full.
type tinOut struct {
	// TIN is the payee's full U.S. taxpayer identification number, hyphenated as
	// the IRS writes it; empty when a W-8 carries none.
	TIN string `json:"tin"`
	// TINType is ssn or ein.
	TINType TINType `json:"tinType,omitempty"`
	// ForeignTIN is a W-8 payee's foreign tax identifying number as issued, which
	// Form 1042-S carries for a foreign recipient.
	ForeignTIN string `json:"foreignTin,omitempty"`
}

// Reads a payee's TIN in full — and, for a payee on a W-8, its foreign TIN, which
// the payer's Form 1042-S carries. Only an org admin of the PAYER, only while the
// payee's grant is live, and every read is on the audit trail before a number is
// answered — a read the trail cannot record is refused.
//
// This is the one place a TIN leaves the platform in full, apart from the IRS
// filing export. SuperAdmin is not admitted: platform authority is not a reason
// to read a company's TIN.
func (o ops) tin(ctx context.Context, in *w9Ref) (*tinOut, error) {
	org, st, err := o.own(ctx, "read a TIN in full", false)
	if err != nil {
		return nil, err
	}
	row, sh, err := o.side(ctx, org, st, in.ID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, zip.ErrForbidden("the payee reads its own TIN in its profile, masked; a full read is the payer's")
	}
	if sh.status != shareGranted {
		return nil, zip.ErrForbidden("the payee has not granted its W-9 to this org")
	}
	est, err := o.other(row.payee)
	if err != nil {
		return nil, err
	}
	_, n, _, err := o.loadProfile(ctx, row.payee, est)
	if err == errNotFound || (err == nil && n.none()) {
		return nil, zip.ErrNotFound("the payee has no TIN on its profile")
	}
	if err != nil {
		return nil, err
	}
	if err := o.audited(ctx, org, "tax.w9.tin_read", audit.Resource{Type: "tax.w9", ID: sh.id},
		map[string]any{"payer": org, "payee": row.payee}); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "tax: the read could not be recorded, so it was not answered")
	}
	noStore(ctx)
	return &tinOut{TIN: n.us.full(), TINType: n.us.Type, ForeignTIN: n.foreign.full()}, nil
}

// decide moves the payee's side of a relationship. from is the statuses the move
// is allowed from.
func (o ops) decide(ctx context.Context, in *w9Ref, act, to string, from ...string) (*W9, error) {
	org, st, err := o.admin(ctx, act, true)
	if err != nil {
		return nil, err
	}
	sh, err := st.share(ctx, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("W-9 request not found")
	}
	if err != nil {
		return nil, err
	}
	allowed := false
	for _, f := range from {
		allowed = allowed || sh.status == f
	}
	if !allowed {
		return nil, zip.Errorf(http.StatusConflict, "the request is %s; it cannot be %s", sh.status, to)
	}
	if to == shareGranted {
		// A W-9 without its TIN is not one to grant; a W-8 may lawfully carry no
		// number at all (a foreign TIN is required only in cases the instructions
		// list), and is granted as it is.
		if r, n, _, err := o.loadProfile(ctx, org, st); err == errNotFound || (err == nil && !r.Form.of().Foreign() && n.us.Zero()) {
			return nil, zip.Errorf(http.StatusConflict, "write this org's tax profile before granting its W-9")
		} else if err != nil {
			return nil, err
		}
	}
	by := ""
	if c, ok := cloud.Request(ctx); ok {
		by = c.User()
	}
	t := now()
	sh.status, sh.updated, sh.data = to, t, encode(shareData{DecidedAt: t, DecidedBy: by})
	if err := st.putShare(ctx, *sh); err != nil {
		return nil, err
	}
	if err := o.commit(ctx, org, "tax.w9."+to, audit.Resource{Type: "tax.w9", ID: sh.id},
		map[string]any{"payer": sh.payer, "payee": org, "status": to}); err != nil {
		return nil, err
	}
	return payeeView(org, sh)
}

// Grants the payer that asked a live read of this org's W-9 — every line, the TIN
// masked, and the full TIN to that payer's org admins alone, each read audited.
// Org admins of the payee only. Needs a tax profile to grant.
func (o ops) grant(ctx context.Context, in *w9Ref) (*W9, error) {
	return o.decide(ctx, in, "grant a W-9", shareGranted, shareRequested, shareDeclined, shareRevoked)
}

// Declines a request for this org's W-9. The payer keeps seeing only the status.
// Org admins of the payee only.
func (o ops) decline(ctx context.Context, in *w9Ref) (*W9, error) {
	return o.decide(ctx, in, "decline a W-9", shareDeclined, shareRequested)
}

// Revokes a W-9 this org granted. From the moment it is revoked the payer reads
// nothing of the W-9 and no full TIN. A 1099 the payer already prepared keeps the
// TIN it was prepared with — the payer must keep it to file — and that TIN is
// never answered in full outside the payer's IRS filing export. Org admins of the
// payee only.
func (o ops) revoke(ctx context.Context, in *w9Ref) (*W9, error) {
	return o.decide(ctx, in, "revoke a W-9", shareRevoked, shareGranted)
}

// matchIn is an IRS TIN Matching result.
type matchIn struct {
	// ID is the relationship, from the path.
	ID string `json:"id"`
	// Result is what IRS TIN Matching answered for this payee's name and TIN:
	// matched or mismatched.
	Result string `json:"result"`
}

// Records the result of IRS TIN Matching for a payee, as the payer. Org admins of
// the payer only, on a live grant.
//
// It does not run TIN Matching. That is an IRS e-Services program a payer enrolls
// in and runs itself, on the name and TIN it holds; this records what the IRS
// answered so it sits beside the W-9 and on the audit trail. Nothing here ever
// records a match the payer did not report.
//
// Example: {"result": "matched"}
func (o ops) match(ctx context.Context, in *matchIn) (*W9, error) {
	org, st, err := o.admin(ctx, "record a TIN Matching result", true)
	if err != nil {
		return nil, err
	}
	if in.Result != "matched" && in.Result != "mismatched" {
		return nil, zip.ErrBadRequest("result is matched or mismatched")
	}
	row, sh, err := o.side(ctx, org, st, in.ID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, zip.ErrForbidden("TIN Matching is the payer's to run and record")
	}
	if sh.status != shareGranted {
		return nil, zip.ErrForbidden("the payee has not granted its W-9 to this org")
	}
	c, _ := cloud.Request(ctx)
	pd := payeeData{Match: &Match{Result: in.Result, At: now(), By: c.User()}}
	row.data, row.updated = encode(pd), now()
	if err := st.putPayee(ctx, *row); err != nil {
		return nil, err
	}
	if err := o.commit(ctx, org, "tax.w9.matched", audit.Resource{Type: "tax.w9", ID: sh.id},
		map[string]any{"payer": org, "payee": row.payee, "result": in.Result}); err != nil {
		return nil, err
	}
	return o.payerView(ctx, org, row, sh, true)
}
