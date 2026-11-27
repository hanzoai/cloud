package tax

// profile.go — the org's own certificate of U.S. or foreign status: the facts on
// Form W-9 (Rev. March 2024), or on Form W-8BEN or W-8BEN-E (Rev. October 2021)
// for a foreign person (w8.go); every number sealed, the certification signed
// through /v1/legal, and the consent that decides whether 1099s reach this org
// electronically or on paper.

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/client"
	legalpeer "github.com/hanzoai/cloud/client/legal"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// Classification is line 3a of Form W-9.
type Classification string

const (
	// Individual is "Individual/sole proprietor" — and a single-member LLC that is
	// disregarded into an individual owner, which checks its owner's box.
	Individual  Classification = "individual"
	CCorp       Classification = "c_corp"
	SCorp       Classification = "s_corp"
	Partnership Classification = "partnership"
	TrustEstate Classification = "trust_estate"
	// LLCC, LLCS and LLCP are an LLC that is not disregarded, with the tax
	// classification it enters on line 3a: C, S or P.
	LLCC Classification = "llc_c"
	LLCS Classification = "llc_s"
	LLCP Classification = "llc_p"
)

// Valid reports whether c is one of the classifications above.
func (c Classification) Valid() bool { return c.Label() != "" }

// Corporate reports whether the payee is taxed as a corporation — the
// classifications the corporation exemption reaches.
func (c Classification) Corporate() bool {
	return c == CCorp || c == SCorp || c == LLCC || c == LLCS
}

// flows reports whether line 3b (foreign partners, owners or beneficiaries) can
// apply: a partnership, a trust or estate, or an LLC taxed as a partnership.
func (c Classification) flows() bool { return c == Partnership || c == TrustEstate || c == LLCP }

// Label is the box as Form W-9 words it.
func (c Classification) Label() string {
	switch c {
	case Individual:
		return "Individual/sole proprietor"
	case CCorp:
		return "C corporation"
	case SCorp:
		return "S corporation"
	case Partnership:
		return "Partnership"
	case TrustEstate:
		return "Trust/estate"
	case LLCC:
		return "LLC (C corporation)"
	case LLCS:
		return "LLC (S corporation)"
	case LLCP:
		return "LLC (Partnership)"
	}
	return ""
}

// Address is lines 5 and 6.
type Address struct {
	// Line1 is the number, street, and apartment or suite. No periods or "#":
	// IRIS rejects them in an address line.
	Line1 string `json:"line1"`
	// Line2 continues the street address, when there is more of it.
	Line2 string `json:"line2,omitempty"`
	// City is the city or town.
	City string `json:"city"`
	// State is the two-letter state or territory code.
	State string `json:"state"`
	// ZIP is five or nine digits.
	ZIP string `json:"zip"`
	// Country is ISO 3166-1 alpha-2; "US" when absent.
	Country string `json:"country,omitempty"`
}

func (a Address) complete() bool {
	return a.Line1 != "" && a.City != "" && a.State != "" && a.ZIP != ""
}

// Consent is the payee's affirmative consent to receive its statements
// electronically, in the Hanzo inbox, instead of on paper (Pub 1099 Part M).
type Consent struct {
	// Electronic is true while the consent stands. Withdrawing it sends every
	// statement furnished afterwards on paper.
	Electronic bool `json:"electronic"`
	// At is when the consent was given or withdrawn, unix seconds.
	At int64 `json:"at,omitempty"`
	// By is the IAM user who gave or withdrew it.
	By string `json:"by,omitempty"`
}

// Disclosure is the statement Pub 1099 Part M requires a recipient be shown
// before consenting, stated once and returned with every profile.
const Disclosure = "Electronic delivery of Forms 1099. By consenting, this organization agrees to receive " +
	"Copy B of every Form 1099-NEC and 1099-MISC furnished to it through Hanzo in its Hanzo tax inbox " +
	"(/v1/tax/inbox) instead of on paper. Scope and duration: the consent covers every statement any payer " +
	"furnishes through Hanzo, for every year, until withdrawn. If you do not consent, each statement is " +
	"owed to you on paper at the address in this profile. You may obtain a paper copy of any statement from " +
	"the payer that issued it. You may withdraw consent at any time by setting electronicConsent to false " +
	"on this profile; the withdrawal is confirmed on the profile and applies to statements furnished after " +
	"it. Hanzo stops furnishing statements electronically if this organization's account is closed. Keep " +
	"this profile's address current so a paper statement reaches you. To access, print and keep a " +
	"statement you need a browser or HTTP client able to read JSON and PDF; each statement stays in the " +
	"inbox and downloadable as a PDF at /v1/tax/inbox/{id}/pdf, and is not removed."

// Certification is the W-9 Part II certification, signed through /v1/legal.
type Certification struct {
	// Status is none, pending (a signature is open) or certified.
	Status string `json:"status"`
	// Document is the /v1/legal document that carries the signature.
	Document string `json:"document,omitempty"`
	// Version is the profile version the signature covers. Changing any W-9 fact
	// makes a new version, and the certification no longer covers it.
	Version int `json:"version,omitempty"`
	// Signed is when legal reported the document signed, unix seconds.
	Signed int64 `json:"signed,omitempty"`
	// By is who signed it: the IAM user legal recorded completing the signature.
	By string `json:"by,omitempty"`
	// Signer is the IAM user the signature was opened for — the admin who asked.
	// Only that user's completion certifies: a completion anyone else recorded is
	// not their signature, and a fresh request is opened in its place.
	Signer string `json:"signer,omitempty"`
}

const (
	certNone      = "none"
	certPending   = "pending"
	certCertified = "certified"
)

// record is the stored profile: every fact on the form but the numbers, which
// are sealed in their own columns.
type record struct {
	// Form is which certificate this is; empty on a record written before W-8s
	// existed, which is a W-9.
	Form           Certificate    `json:"form,omitempty"`
	Name           string         `json:"name"`
	BusinessName   string         `json:"businessName,omitempty"`
	Classification Classification `json:"classification"`
	ForeignOwners  bool           `json:"foreignOwners,omitempty"`
	ExemptPayee    string         `json:"exemptPayee,omitempty"`
	FATCA          string         `json:"fatca,omitempty"`
	Address        Address        `json:"address"`
	Phone          string         `json:"phone,omitempty"`
	TINType        TINType        `json:"tinType"`
	Consent        Consent        `json:"consent"`
	Certification  Certification  `json:"certification"`
	// W8 is what a W-8BEN or W-8BEN-E certifies beyond name and address.
	W8 W8 `json:"w8,omitzero"`
}

// facts is the part of a record a W-9 certifies: a change to any of it is a new
// W-9. Consent and the certification itself are about the W-9, not on it.
func (r record) facts() record {
	r.Consent, r.Certification = Consent{}, Certification{}
	return r
}

// Profile is an org's form as it is answered: every number masked, always.
type Profile struct {
	// Name is line 1: the name on the income tax return, or of the foreign
	// individual or organization that is the beneficial owner.
	Name string `json:"name"`
	// BusinessName is line 2: the business or disregarded entity name.
	BusinessName string `json:"businessName,omitempty"`
	// Classification is line 3a.
	Classification Classification `json:"classification"`
	// ForeignOwners is line 3b: a flow-through entity with foreign partners,
	// owners or beneficiaries.
	ForeignOwners bool `json:"foreignOwners,omitempty"`
	// ExemptPayee is line 4's exempt payee code, 1–13.
	ExemptPayee string `json:"exemptPayee,omitempty"`
	// FATCA is line 4's FATCA exemption code, A–M.
	FATCA string `json:"fatca,omitempty"`
	// Address is lines 5 and 6.
	Address Address `json:"address"`
	// Phone is the number a 1099 this org files as PAYER prints for it.
	Phone string `json:"phone,omitempty"`
	// TIN is Part I, masked to its last four digits. The full number is answered
	// only by GET /v1/tax/w9/{id}/tin, to a payer this org granted it to.
	TIN string `json:"tin"`
	// TINType is which Part I box: ssn (an SSN or ITIN) or ein.
	TINType TINType `json:"tinType"`
	// Consent is the electronic-delivery consent. Only the org itself reads it: how
	// a payee takes delivery is not a payer's to know.
	Consent *Consent `json:"consent,omitempty"`
	// Certification is where the Part II signature stands.
	Certification Certification `json:"certification"`
	// Version counts the W-9's revisions; a certification covers exactly one.
	Version int `json:"version"`
	// UpdatedAt is when the profile last changed, unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
	// Disclosure is the electronic-delivery disclosure the consent is given against.
	Disclosure string `json:"disclosure,omitempty"`
	// Form is w9, w8ben or w8bene.
	Form Certificate `json:"form"`
	// ForeignTIN is a W-8's foreign tax identifying number, masked. The full
	// number is answered only with the full TIN read.
	ForeignTIN string `json:"foreignTin,omitempty"`
	// W8 is what a W-8BEN or W-8BEN-E certifies beyond name and address.
	W8 *W8 `json:"w8,omitempty"`
	// Expires is when a certified W-8 stops being valid, unix seconds: the last
	// day of the third calendar year after the year it was signed. A W-9 does not
	// expire.
	Expires int64 `json:"expires,omitempty"`
	// Valid is whether the form establishes what it certifies today — for a W-8,
	// certified for its current version and not expired; for a W-9, a TIN on file.
	Valid bool `json:"valid"`
}

// loadProfile reads an org's profile and opens its numbers. errNotFound when the
// org has none.
func (o ops) loadProfile(ctx context.Context, org string, st *store) (record, ids, *profileRow, error) {
	row, err := st.profile(ctx)
	if err != nil {
		return record{}, ids{}, nil, err
	}
	var r record
	if err := decode(row.data, &r); err != nil {
		return record{}, ids{}, nil, err
	}
	return r, ids{us: row.tin, foreign: row.ftin}, row, nil
}

func view(r record, n ids, row *profileRow) *Profile {
	p := &Profile{
		Form: r.Form.of(), Name: r.Name, BusinessName: r.BusinessName, Classification: r.Classification,
		ForeignOwners: r.ForeignOwners, ExemptPayee: r.ExemptPayee, FATCA: r.FATCA,
		Address: r.Address, Phone: r.Phone, TIN: n.us.String(), TINType: r.TINType,
		ForeignTIN: n.foreign.String(),
		Consent:    &r.Consent, Certification: r.Certification,
		Version: row.version, UpdatedAt: row.updated, Disclosure: Disclosure,
	}
	f := formOf(r, n, row)
	p.Expires, p.Valid = f.Expires, f.Valid
	if r.Form.of().Foreign() {
		w := r.W8
		p.W8 = &w
	}
	return p
}

// Returns the caller org's own tax profile — every line of its Form W-9, W-8BEN or
// W-8BEN-E, every number masked to its last four characters, where the
// certification stands, whether a W-8 is valid and when it expires, and whether
// the org consents to receive its 1099s electronically.
//
// Any member may read it. 404 until an admin writes one. No full number is ever in
// this answer.
func (o ops) profile(ctx context.Context, _ *cloud.Unit) (*Profile, error) {
	org, st, err := o.member(ctx)
	if err != nil {
		return nil, err
	}
	r, n, row, err := o.loadProfile(ctx, org, st)
	if err == errNotFound {
		return nil, zip.ErrNotFound("no tax profile yet")
	}
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	return view(r, n, row), nil
}

// profileIn is a W-9, W-8BEN or W-8BEN-E, as the org's admin states it.
type profileIn struct {
	// Name is line 1: the name on the income tax return, or the foreign
	// individual's or organization's name. Required.
	Name string `json:"name"`
	// BusinessName is line 2, when it differs from line 1.
	BusinessName string `json:"businessName,omitempty"`
	// Classification is W-9 line 3a: individual, c_corp, s_corp, partnership,
	// trust_estate, llc_c, llc_s or llc_p. Required on a W-9; absent on a W-8.
	Classification Classification `json:"classification"`
	// ForeignOwners is line 3b; only a partnership, trust/estate or llc_p may set it.
	ForeignOwners bool `json:"foreignOwners,omitempty"`
	// ExemptPayee is the exempt payee code, 1–13. Entities only.
	ExemptPayee string `json:"exemptPayee,omitempty"`
	// FATCA is the FATCA exemption code, A–M. Entities only.
	FATCA string `json:"fatca,omitempty"`
	// Address is W-9 lines 5 and 6, or a W-8's line 3 permanent residence address
	// outside the United States (state and zip where the country uses them).
	// Required: a statement owed on paper is mailed there.
	Address Address `json:"address"`
	// Phone is printed on the 1099s this org files as payer.
	Phone string `json:"phone,omitempty"`
	// TIN is W-9 Part I, or a W-8's U.S. TIN (W-8BEN line 5, W-8BEN-E line 8),
	// digits with or without hyphens. Required on a W-9's first write; on a W-8,
	// only for a treaty claim with no foreign TIN. Omitted afterwards, the stored
	// number is kept, so an address change never re-sends it.
	TIN string `json:"tin,omitempty"`
	// TINType is ssn (an SSN or ITIN) or ein. An entity other than an individual
	// states an EIN.
	TINType TINType `json:"tinType,omitempty"`
	// ElectronicConsent gives (true) or withdraws (false) consent to receive 1099s
	// in the Hanzo inbox, against the disclosure every profile answers with.
	// Omitted, the stored consent stands.
	ElectronicConsent *bool `json:"electronicConsent,omitempty"`
	// Form is w9 (a U.S. person), w8ben (a foreign individual) or w8bene (a
	// foreign entity). w9 when omitted.
	Form Certificate `json:"form,omitempty"`
	// ForeignTIN is a W-8's foreign tax identifying number (W-8BEN line 6a,
	// W-8BEN-E line 9b), as the jurisdiction of residence issues it. Omitted
	// afterwards, the stored number is kept; the empty string with
	// w8.noForeignTin clears it.
	ForeignTIN string `json:"foreignTin,omitempty"`
	// W8 is the rest of a W-8: the country of citizenship or incorporation, the
	// chapter 3 and chapter 4 statuses of an entity, a date of birth, the treaty
	// claim, and the signer's capacity. Required on a W-8; absent on a W-9.
	W8 *W8 `json:"w8,omitempty"`
}

// Writes the caller org's tax profile — its Form W-9, or its Form W-8BEN or
// W-8BEN-E when it is a foreign person — and answers it, every number masked. Org
// admins only.
//
// A U.S. TIN is validated before it is kept — nine digits; an EIN's prefix must be
// one the IRS assigns; an SSN never begins 000, 666 or 9xx and has no zero group
// or serial; an ITIN's middle digits are in the IRS ranges. A foreign TIN is the
// issuing jurisdiction's and is checked only for its characters. Every number is
// sealed under this org's own data key before it is written. Validating the
// FORMAT is all this does: whether the IRS has the number on file under this name
// is IRS TIN Matching, which a payer runs through IRS e-Services.
//
// A W-8 is refused where the form itself says it is the wrong one: a U.S. citizen,
// a U.S. permanent residence or an entity organized in the United States is a U.S.
// person and files a W-9. A treaty claim needs a U.S. or foreign TIN on the form
// (Instructions for Form W-8BEN, line 5; for Form W-8BEN-E, line 8), and names the
// country of the permanent residence address.
//
// Changing any fact on the form makes a new version, and the certification no
// longer covers it: certify again. For a W-8 that is the "change in circumstances"
// the instructions end its validity on. Changing only the consent does not.
//
// Example: {"name": "Acme Robotics, Inc.", "classification": "c_corp",
// "address": {"line1": "1 Market St", "city": "San Francisco", "state": "CA", "zip": "94105"},
// "tin": "12-3456789", "tinType": "ein", "electronicConsent": true}
func (o ops) putProfile(ctx context.Context, in *profileIn) (*Profile, error) {
	org, st, err := o.admin(ctx, "write the tax profile", true)
	if err != nil {
		return nil, err
	}
	prev, prevIDs, prevRow, err := o.loadProfile(ctx, org, st)
	fresh := err == errNotFound
	if err != nil && !fresh {
		return nil, err
	}
	r, n, err := in.record(prev, prevIDs)
	if err != nil {
		return nil, err
	}
	if in.ElectronicConsent != nil && *in.ElectronicConsent != prev.Consent.Electronic {
		by := ""
		if c, ok := cloud.Request(ctx); ok {
			by = c.User()
		}
		r.Consent = Consent{Electronic: *in.ElectronicConsent, At: now(), By: by}
	}
	version, recertify := 1, true
	if !fresh {
		version = prevRow.version
		if recertify = r.facts() != prev.facts() || n != prevIDs; recertify {
			version++
		}
	}
	if recertify {
		r.Certification = Certification{Status: certNone}
	}
	row := profileRow{version: version, data: encode(r), tin: n.us, ftin: n.foreign, updated: now()}
	if err := st.putProfile(ctx, row); err != nil {
		return nil, err
	}
	if err := o.commit(ctx, org, "tax.profile.updated", audit.Resource{Type: "tax.profile", ID: org},
		map[string]any{"form": r.Form.of(), "version": version, "recertify": recertify, "electronic": r.Consent.Electronic}); err != nil {
		return nil, err
	}
	o.restate(ctx, org)
	noStore(ctx)
	return view(r, n, &row), nil
}

// record validates the input into a stored record and its numbers, keeping what
// the input leaves out of the previous one.
func (in *profileIn) record(prev record, prevIDs ids) (record, ids, error) {
	form := in.Form.of()
	if form != prev.Form.of() {
		prevIDs = ids{} // a number certified on one form is not carried onto another
	}
	r := record{
		Form: form, Name: clean(in.Name), BusinessName: clean(in.BusinessName), Classification: in.Classification,
		ForeignOwners: in.ForeignOwners, ExemptPayee: strings.TrimSpace(in.ExemptPayee),
		FATCA: strings.ToUpper(strings.TrimSpace(in.FATCA)), Phone: strings.TrimSpace(in.Phone),
		TINType: in.TINType,
		Address: Address{
			Line1: clean(in.Address.Line1), Line2: clean(in.Address.Line2), City: clean(in.Address.City),
			State: strings.ToUpper(strings.TrimSpace(in.Address.State)), ZIP: digits(in.Address.ZIP),
			Country: strings.ToUpper(strings.TrimSpace(in.Address.Country)),
		},
		Consent: prev.Consent, Certification: prev.Certification,
	}
	switch {
	case !form.Valid():
		return record{}, ids{}, zip.ErrBadRequest("form is w9, w8ben or w8bene")
	case r.Name == "":
		return record{}, ids{}, zip.ErrBadRequest("name (line 1) is required")
	case slices.ContainsFunc([]string{r.Name, r.BusinessName, r.Address.Line1, r.Address.Line2, r.Address.City}, leads):
		// A payer carries these into a spreadsheet to file them (iris.go), where a
		// leading =, +, - or @ is a formula, not a name.
		return record{}, ids{}, zip.ErrBadRequest("a name or address line does not begin with =, +, - or @")
	}
	if form.Foreign() {
		return in.foreign(r, prevIDs)
	}
	if r.Address.Country == "" {
		r.Address.Country = "US"
	}
	switch {
	case in.W8 != nil || in.ForeignTIN != "":
		return record{}, ids{}, zip.ErrBadRequest("w8 and foreignTin are on a W-8; a W-9 carries neither")
	case !r.Classification.Valid():
		return record{}, ids{}, zip.ErrBadRequest("classification (W-9 line 3a) is individual, c_corp, s_corp, partnership, trust_estate, llc_c, llc_s or llc_p")
	case r.ForeignOwners && !r.Classification.flows():
		return record{}, ids{}, zip.ErrBadRequest("foreignOwners (W-9 line 3b) applies only to a partnership, trust/estate or LLC taxed as a partnership")
	case r.Classification == Individual && (r.ExemptPayee != "" || r.FATCA != ""):
		return record{}, ids{}, zip.ErrBadRequest("exemption codes (W-9 line 4) apply only to certain entities, not individuals")
	case r.ExemptPayee != "" && !exemptPayeeCode(r.ExemptPayee):
		return record{}, ids{}, zip.ErrBadRequest("exemptPayee is a code from 1 to 13")
	case r.FATCA != "" && (len(r.FATCA) != 1 || r.FATCA[0] < 'A' || r.FATCA[0] > 'M'):
		return record{}, ids{}, zip.ErrBadRequest("fatca is a code from A to M")
	case !r.Address.complete():
		return record{}, ids{}, zip.ErrBadRequest("address needs line1, city, state and zip (W-9 lines 5 and 6)")
	case len(r.Address.ZIP) != 5 && len(r.Address.ZIP) != 9:
		return record{}, ids{}, zip.ErrBadRequest("zip is five or nine digits")
	case !code2(r.Address.State):
		return record{}, ids{}, zip.ErrBadRequest("state is the two-letter state or territory code")
	case !code2(r.Address.Country):
		return record{}, ids{}, zip.ErrBadRequest("country is the two-letter ISO 3166-1 code")
	case r.Classification != Individual && r.TINType != EIN:
		return record{}, ids{}, zip.ErrBadRequest("an entity states an EIN (W-9 Part I)")
	}
	t, err := in.us(r.TINType, prevIDs.us)
	if err != nil {
		return record{}, ids{}, err
	}
	if t.Zero() {
		return record{}, ids{}, zip.ErrBadRequest("tin (W-9 Part I) is required")
	}
	return r, ids{us: t}, nil
}

// foreign validates a W-8: the W-9's lines are absent, the address is a
// permanent residence outside the United States, and the rest is w8.go's check.
func (in *profileIn) foreign(r record, prevIDs ids) (record, ids, error) {
	if in.W8 == nil {
		return record{}, ids{}, zip.ErrBadRequest("w8 is required on a " + r.Form.Label())
	}
	// A foreign address is written as its own country writes it: a province is a
	// name as often as a code, and a postal code carries letters ("SW1A 1AA").
	r.Address.State, r.Address.ZIP = clean(in.Address.State), clean(in.Address.ZIP)
	switch {
	case r.BusinessName != "" || r.Classification != "" || r.ForeignOwners || r.ExemptPayee != "" || r.FATCA != "":
		return record{}, ids{}, zip.ErrBadRequest("business name, classification and the exemption codes are lines of the W-9; a W-8 carries none of them")
	case len(r.Address.State) > 50 || len(r.Address.ZIP) > 16:
		return record{}, ids{}, zip.ErrBadRequest("state or province is at most 50 characters, and the postal code at most 16")
	case leads(r.Address.State) || leads(r.Address.ZIP):
		return record{}, ids{}, zip.ErrBadRequest("an address line does not begin with =, +, - or @")
	}
	w := *in.W8
	w.Country = strings.ToUpper(strings.TrimSpace(w.Country))
	w.Chapter4, w.GIIN = strings.TrimSpace(w.Chapter4), strings.ToUpper(strings.TrimSpace(w.GIIN))
	w.Birth, w.Capacity = strings.TrimSpace(w.Birth), clean(w.Capacity)
	w.Treaty.Country = strings.ToUpper(strings.TrimSpace(w.Treaty.Country))
	w.Treaty.Article, w.Treaty.Income = strings.TrimSpace(w.Treaty.Article), strings.TrimSpace(w.Treaty.Income)
	w.Treaty.Conditions, w.Treaty.LOB = clean(w.Treaty.Conditions), strings.TrimSpace(w.Treaty.LOB)
	if r.TINType == "" && in.TIN == "" {
		r.TINType = prevIDs.us.Type
	}
	us, err := in.us(r.TINType, prevIDs.us)
	if err != nil {
		return record{}, ids{}, err
	}
	f := prevIDs.foreign
	switch {
	case w.NoForeignTIN:
		f = FTIN{}
	case in.ForeignTIN != "":
		if f, err = ParseFTIN(in.ForeignTIN); err != nil {
			return record{}, ids{}, zip.ErrBadRequest(err.Error())
		}
	}
	if err := w.check(r.Form, r.Address, us, f); err != nil {
		return record{}, ids{}, zip.ErrBadRequest(err.Error())
	}
	r.W8, r.TINType = w, us.Type
	return r, ids{us: us, foreign: f}, nil
}

// us reads the U.S. TIN the input states, or keeps the one on file when it states
// none. A type change must carry its number.
func (in *profileIn) us(typ TINType, prev TIN) (TIN, error) {
	if strings.TrimSpace(in.TIN) == "" {
		if !prev.Zero() && prev.Type != typ {
			return TIN{}, zip.ErrBadRequest("tinType changed: send the tin with it")
		}
		return prev, nil
	}
	t, err := ParseTIN(typ, in.TIN)
	if err != nil {
		return TIN{}, zip.ErrBadRequest(err.Error())
	}
	return t, nil
}

func exemptPayeeCode(c string) bool {
	switch c {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13":
		return true
	}
	return false
}

// code2 reports whether s is two letters A–Z: a state, territory or country code.
func code2(s string) bool {
	return len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

// clean trims a name or address line and collapses its inner spaces: IRIS rejects
// a leading, trailing or double space (Pub 5717 §3.5.3.1).
func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

// digits keeps only the digits of s.
func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Signs the caller org's certification — Form W-9 Part II, Form W-8BEN Part III or
// Form W-8BEN-E Part XXX — through /v1/legal's e-signature. The org's own admins
// only, and the admin is the signer: a SuperAdmin acting inside the org is not a
// person who can certify for it.
//
// The first call renders the certification — the IRS's own certification text over
// this profile's facts, every number truncated — as a /v1/legal document and opens
// its signature request with the calling admin as signer; the answer is `pending` with
// the document id. The signer completes it where every /v1/legal signature is
// completed (POST /v1/legal/documents/{id}/sign/complete). Calling this again then
// asks legal whether the document is signed and by whom: signed by the signer, the
// profile is recorded certified; not yet signed, it answers `pending` again; signed
// by anyone else — another member, a key — or voided, it is not the signer's
// certification, and a fresh request is opened in its place.
//
// A certification covers one profile version: writing any fact on the form
// afterwards voids it. A certified W-8 is valid through the last day of the third
// calendar year after the year it was signed (Instructions for Form W-8BEN and
// W-8BEN-E, Rev. 10-2021); certifying it again after that renews it. It is not a
// statement that a TIN matches IRS records — that is IRS TIN Matching, which only
// a payer enrolled in IRS e-Services can run.
func (o ops) certify(ctx context.Context, _ *cloud.Unit) (*Profile, error) {
	org, st, err := o.own(ctx, "certify the W-9", true)
	if err != nil {
		return nil, err
	}
	r, n, row, err := o.loadProfile(ctx, org, st)
	if err == errNotFound {
		return nil, zip.ErrNotFound("no tax profile to certify; write one first")
	}
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	cert := r.Certification
	replaced := ""
	switch {
	case cert.Status == certCertified && cert.Version == row.version && r.valid(true, now()):
		return view(r, n, row), nil
	case cert.Status == certPending && cert.Version == row.version:
		doc, err := legalpeer.LegalDocument(ctx, &client.LegalDocumentIn{ID: cert.Document})
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "tax: legal: %v", err)
		}
		switch {
		case doc.Status == "signed" && doc.SignedBy == cert.Signer:
			cert.Status, cert.Signed, cert.By = certCertified, doc.Signed, doc.SignedBy
			r.Certification = cert
			if err := o.saveRecord(ctx, st, row, r); err != nil {
				return nil, err
			}
			if err := o.commit(ctx, org, "tax.profile.certified", audit.Resource{Type: "tax.profile", ID: org},
				map[string]any{"version": row.version, "document": cert.Document}); err != nil {
				return nil, err
			}
			o.restate(ctx, org)
			return view(r, n, row), nil
		case doc.Status != "signed" && doc.Status != "voided":
			return view(r, n, row), nil // still out for signature
		}
		replaced = cert.Document // signed by someone other than its signer, or voided
	}
	c, _ := cloud.Request(ctx)
	signer := strings.Clone(c.User())
	template, fields := certificationOf(r, n, row.version)
	doc, err := legalpeer.LegalSign(ctx, &client.LegalSignIn{
		Template: template,
		Fields:   fields,
		Signers:  []client.LegalSigner{{Name: cmp.Or(c.UserName(), signer), Email: c.UserEmail()}},
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "tax: legal: %v", err)
	}
	r.Certification = Certification{Status: certPending, Document: doc.ID, Version: row.version, Signer: signer}
	if err := o.saveRecord(ctx, st, row, r); err != nil {
		return nil, err
	}
	after := map[string]any{"version": row.version, "document": doc.ID}
	if replaced != "" {
		after["replaces"] = replaced
	}
	if err := o.commit(ctx, org, "tax.profile.certification_requested", audit.Resource{Type: "tax.profile", ID: org}, after); err != nil {
		return nil, err
	}
	return view(r, n, row), nil
}

// saveRecord rewrites the stored record at the same version and TIN, only if the
// row is still the one read: a certification recorded over a profile an admin
// changed meanwhile would put the old facts — and the old TIN — back, certified.
func (o ops) saveRecord(ctx context.Context, st *store, row *profileRow, r record) error {
	row.data, row.updated = encode(r), now()
	if err := st.rewriteProfile(ctx, *row); err != nil {
		if err == errMoved {
			return zip.Errorf(http.StatusConflict, "the profile changed while this ran; read it and certify again")
		}
		return err
	}
	return nil
}

func oneLine(a Address) string {
	parts := []string{a.Line1}
	if a.Line2 != "" {
		parts = append(parts, a.Line2)
	}
	return strings.Join(append(parts, a.City, a.State+" "+a.ZIP, a.Country), ", ")
}
