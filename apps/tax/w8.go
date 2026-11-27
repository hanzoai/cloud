package tax

// w8.go — a foreign person's certificate of foreign status: Form W-8BEN for an
// individual, Form W-8BEN-E for an entity. It is the same profile as the W-9 —
// one per org, because an org is either a U.S. person or it is not — and it is
// exchanged, certified and sealed the same way: a payer reads it only under the
// payee's grant, the Part III (W-8BEN) or Part XXX (W-8BEN-E) certification is
// signed through /v1/legal by the admin who asked, and every identifying number
// is sealed under the owner's key and answered masked.
//
// THE SOURCES, read on irs.gov on 2026-09-24:
//
//   - Instructions for Form W-8BEN (Rev. October 2021), irs.gov/instructions/iw8ben,
//     and the form itself (Rev. October 2021).
//   - Instructions for Form W-8BEN-E (Rev. October 2021), irs.gov/instructions/iw8bene,
//     and the form itself (Rev. October 2021).
//   - Publication 515 (2026), irs.gov/publications/p515.
//   - Treas. Reg. §1.6041-4(a)(1) and §1.1441-1(b)(3), ecfr.gov.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/iam"
)

// Certificate is which form a profile is: the W-9 a U.S. person certifies, or the
// W-8 a foreign person does.
type Certificate string

const (
	// FormW9 is Form W-9: a U.S. person's TIN and certification. A profile
	// written before W-8s existed carries no form and is one.
	FormW9 Certificate = "w9"
	// FormW8BEN is Form W-8BEN: a foreign individual.
	FormW8BEN Certificate = "w8ben"
	// FormW8BENE is Form W-8BEN-E: a foreign entity.
	FormW8BENE Certificate = "w8bene"
)

// of is the form a stored record is, reading the empty form of a record written
// before W-8s existed as the W-9 it is.
func (f Certificate) of() Certificate {
	if f == "" {
		return FormW9
	}
	return f
}

// Foreign reports whether the form certifies foreign status.
func (f Certificate) Foreign() bool { return f == FormW8BEN || f == FormW8BENE }

// Valid reports whether f is one of the three forms.
func (f Certificate) Valid() bool { return f == FormW9 || f.Foreign() }

// Label is the form's own name.
func (f Certificate) Label() string {
	switch f.of() {
	case FormW8BEN:
		return "Form W-8BEN"
	case FormW8BENE:
		return "Form W-8BEN-E"
	}
	return "Form W-9"
}

// Chapter3 is W-8BEN-E line 4: the entity's chapter 3 status, as the form lists
// them.
type Chapter3 string

// chapter3 is every line 4 box on Form W-8BEN-E (Rev. October 2021), keyed by
// the value this surface takes.
var chapter3 = map[Chapter3]string{
	"corporation":                "Corporation",
	"partnership":                "Partnership",
	"simple_trust":               "Simple trust",
	"tax_exempt":                 "Tax-exempt organization",
	"complex_trust":              "Complex trust",
	"government_controlled":      "Foreign Government - Controlled Entity",
	"central_bank":               "Central Bank of Issue",
	"private_foundation":         "Private foundation",
	"estate":                     "Estate",
	"government_integral":        "Foreign Government - Integral Part",
	"grantor_trust":              "Grantor trust",
	"disregarded":                "Disregarded entity",
	"international_organization": "International organization",
}

// Label is the line 4 box as the form words it.
func (c Chapter3) Label() string { return chapter3[c] }

// chapter4 is W-8BEN-E line 5, the chapter 4 (FATCA) status, keyed by the value
// this surface takes and worded as the form words it.
var chapter4 = map[string]string{
	"nonparticipating_ffi":        "Nonparticipating FFI",
	"participating_ffi":           "Participating FFI",
	"reporting_model1_ffi":        "Reporting Model 1 FFI",
	"reporting_model2_ffi":        "Reporting Model 2 FFI",
	"registered_deemed_compliant": "Registered deemed-compliant FFI",
	"sponsored_ffi":               "Sponsored FFI",
	"certified_deemed_compliant":  "Certified deemed-compliant FFI",
	"owner_documented_ffi":        "Owner-documented FFI",
	"restricted_distributor":      "Restricted distributor",
	"nonreporting_iga_ffi":        "Nonreporting IGA FFI",
	"foreign_government":          "Foreign government, government of a U.S. possession, or foreign central bank of issue",
	"international_organization":  "International organization",
	"exempt_retirement_plan":      "Exempt retirement plans",
	"exempt_owned":                "Entity wholly owned by exempt beneficial owners",
	"territory_fi":                "Territory financial institution",
	"excepted_nonfinancial_group": "Excepted nonfinancial group entity",
	"excepted_startup":            "Excepted nonfinancial start-up company",
	"excepted_liquidation":        "Excepted nonfinancial entity in liquidation or bankruptcy",
	"501c":                        "501(c) organization",
	"nonprofit":                   "Nonprofit organization",
	"publicly_traded_nffe":        "Publicly traded NFFE or NFFE affiliate of a publicly traded corporation",
	"excepted_territory_nffe":     "Excepted territory NFFE",
	"active_nffe":                 "Active NFFE",
	"passive_nffe":                "Passive NFFE",
	"excepted_inter_affiliate":    "Excepted inter-affiliate FFI",
	"direct_reporting_nffe":       "Direct reporting NFFE",
	"sponsored_direct_reporting":  "Sponsored direct reporting NFFE",
	"not_financial_account":       "Account that is not a financial account",
}

// lobs are the limitation on benefits provisions W-8BEN-E line 14b lists, keyed
// by the value this surface takes and worded as the form words them (Form
// W-8BEN-E, Rev. October 2021, line 14b). "other" is the form's "Other (specify
// Article and paragraph)", which the claim's conditions state.
var lobs = map[string]string{
	"government":             "Government",
	"pension":                "Tax-exempt pension trust or pension fund",
	"tax_exempt":             "Other tax-exempt organization",
	"publicly_traded":        "Publicly traded corporation",
	"subsidiary":             "Subsidiary of a publicly traded corporation",
	"ownership_base_erosion": "Company that meets the ownership and base erosion test",
	"derivative_benefits":    "Company that meets the derivative benefits test",
	"active_business":        "Company with an item of income that meets active trade or business test",
	"discretionary":          "Favorable discretionary determination by the U.S. competent authority received",
	"no_lob":                 "No LOB article in treaty",
	"other":                  "Other",
}

// flowThrough are the line 4 statuses the W-8BEN-E instructions keep off the
// form unless the entity claims treaty benefits as a hybrid: "Do not use Form
// W-8BEN-E if ... You are a disregarded entity, branch, or flow-through entity
// for U.S. tax purposes. However, you may use this form if you are a disregarded
// entity or flow-through entity ... to claim treaty benefits because you are a
// hybrid entity liable to tax as a resident for treaty purposes"; a foreign
// partnership or grantor trust otherwise provides Form W-8IMY (Instructions for
// Form W-8BEN-E, Rev. 10-2021), and a disregarded entity's owner documents
// itself. Line 4 asks the same of all four: "is the entity a hybrid making a
// treaty claim?"
var flowThrough = []Chapter3{"partnership", "disregarded", "simple_trust", "grantor_trust"}

// W8 is what a Form W-8BEN or W-8BEN-E certifies beyond the name and address
// every profile carries. The numbers are not here: the U.S. TIN is the profile's
// TIN and the foreign TIN is sealed beside it, never in this record.
type W8 struct {
	// Country is line 2: the country of citizenship (W-8BEN) or of incorporation
	// or organization (W-8BEN-E), ISO 3166-1 alpha-2.
	Country string `json:"country"`
	// Chapter3 is W-8BEN-E line 4. A W-8BEN's beneficial owner is an individual
	// and states none.
	Chapter3 Chapter3 `json:"chapter3,omitempty"`
	// Chapter4 is W-8BEN-E line 5, the FATCA status.
	Chapter4 string `json:"chapter4,omitempty"`
	// GIIN is W-8BEN-E line 9a, when the chapter 4 status carries one.
	GIIN string `json:"giin,omitempty"`
	// NoForeignTIN is W-8BEN line 6b / W-8BEN-E line 9b's alternative: the
	// jurisdiction of residence does not require or issue a foreign TIN.
	NoForeignTIN bool `json:"noForeignTin,omitempty"`
	// Birth is W-8BEN line 8, the individual's date of birth, YYYY-MM-DD.
	Birth string `json:"birth,omitempty"`
	// Treaty is the claim of treaty benefits (W-8BEN Part II lines 9-10, W-8BEN-E
	// Part III lines 14-15), when one is made.
	Treaty iam.Treaty `json:"treaty,omitzero"`
	// Capacity is the capacity in which the signer signs for the beneficial owner
	// — "Director", "Authorized officer". Required on a W-8BEN-E; on a W-8BEN, empty
	// when the beneficial owner signs.
	Capacity string `json:"capacity,omitempty"`
}

// claims reports whether the form makes a treaty claim.
func (w W8) claims() bool { return w.Treaty.Country != "" }

// incomes are the types of income a treaty claim here may cover — the payment
// categories a payer states, less merchandise, whose purchase is not income the
// treaty rate would reduce.
var incomes = []string{string(Services), string(Rents), string(Royalties), string(Other)}

// check validates a W-8's facts against its form. The name, address and numbers
// are the profile's and are checked by record.
func (w W8) check(f Certificate, addr Address, us TIN, foreign FTIN) error {
	switch {
	case !code2(w.Country):
		return errors.New("w8.country (line 2) is the two-letter ISO 3166-1 code of citizenship or incorporation")
	case w.Country == "US":
		return errors.New("a U.S. citizen or entity organized in the United States is a U.S. person: its form is the W-9")
	case addr.Line1 == "" || addr.City == "" || !code2(addr.Country):
		return errors.New("address needs line1, city and the two-letter country of permanent residence (W-8 line 3)")
	case addr.Country == "US":
		return errors.New("a permanent residence in the United States is not a foreign person's: its form is the W-9")
	case w.Birth != "" && !past(w.Birth):
		return errors.New("w8.birth is a date of birth, YYYY-MM-DD")
	case len(w.Capacity) > 100:
		return errors.New("w8.capacity is at most 100 characters")
	}
	switch f {
	case FormW8BEN:
		switch {
		case w.Chapter3 != "" || w.Chapter4 != "" || w.GIIN != "":
			return errors.New("chapter 3 and 4 statuses and a GIIN are on the W-8BEN-E; a W-8BEN's beneficial owner is an individual")
		case !us.Zero() && us.Type != SSN:
			return errors.New("an individual's U.S. TIN (W-8BEN line 5) is an SSN or ITIN")
		case w.Treaty.LOB != "":
			return errors.New("a limitation on benefits provision is W-8BEN-E line 14b; a W-8BEN has none")
		}
	case FormW8BENE:
		switch {
		case chapter3[w.Chapter3] == "":
			return errors.New("w8.chapter3 (W-8BEN-E line 4) is one of: " + keys(chapter3))
		case chapter4[w.Chapter4] == "":
			return errors.New("w8.chapter4 (W-8BEN-E line 5) is one of: " + keys(chapter4))
		case w.GIIN != "" && !giin(w.GIIN):
			return errors.New("w8.giin (W-8BEN-E line 9a) is 19 characters, XXXXXX.XXXXX.XX.XXX")
		case w.Birth != "":
			return errors.New("a date of birth is an individual's; an entity states none")
		case strings.TrimSpace(w.Capacity) == "":
			return errors.New("w8.capacity is required: a W-8BEN-E is signed by an authorized representative or officer, in a stated capacity")
		case !us.Zero() && us.Type != EIN:
			return errors.New("an entity's U.S. TIN (W-8BEN-E line 8) is an EIN")
		case slices.Contains(flowThrough, w.Chapter3) && !w.claims():
			return errors.New("a partnership, disregarded entity, simple trust or grantor trust uses the W-8BEN-E only as a hybrid entity " +
				"claiming treaty benefits; otherwise a flow-through entity provides Form W-8IMY and a disregarded entity's owner documents itself")
		case w.claims() && lobs[w.Treaty.LOB] == "":
			return errors.New("w8.treaty.lob (W-8BEN-E line 14b) is the limitation on benefits provision the entity meets, one of: " + keys(lobs))
		case w.Treaty.LOB == "other" && w.Treaty.Conditions == "":
			return errors.New("w8.treaty.conditions names the article and paragraph of an \"other\" limitation on benefits provision")
		}
	}
	if !w.claims() {
		return nil
	}
	t := w.Treaty
	switch {
	case !code2(t.Country):
		return errors.New("w8.treaty.country is the two-letter code of the treaty country of residence")
	case t.Country != addr.Country:
		// Line 3 is "the address of the country where you claim to be a resident
		// for purposes of that country's income tax", and line 9 / 14a is that
		// residence for the treaty: a claim that names another country contradicts
		// the address it is made from.
		return errors.New("w8.treaty.country must be the country of the permanent residence address")
	case strings.TrimSpace(t.Article) == "" || len(t.Article) > 32:
		return errors.New("w8.treaty.article names the article and paragraph claimed, e.g. 12(2)")
	case t.RateBps < 0 || t.RateBps >= StatutoryBps:
		return fmt.Errorf("w8.treaty.rateBps is the claimed rate in basis points, below the %d statutory rate", StatutoryBps)
	case !slices.Contains(incomes, t.Income):
		return errors.New("w8.treaty.income is services, rents, royalties or other")
	case len(t.Conditions) > 1000:
		return errors.New("w8.treaty.conditions is at most 1000 characters")
	case us.Zero() && foreign.Zero():
		// "To claim certain treaty benefits, you must complete line 5 by submitting
		// an SSN or ITIN, or line 6 by providing a foreign tax identification
		// number" (Instructions for Form W-8BEN, Rev. 10-2021, line 5); W-8BEN-E
		// line 8 likewise, unless a foreign TIN is on line 9b.
		return errors.New("a treaty claim needs a U.S. TIN or a foreign TIN on the form")
	}
	return nil
}

// StatutoryBps is the chapter 3 rate on U.S.-source FDAP income paid to a foreign
// person, in basis points: 30% (IRC §1441(a), §1442(a); Pub 515 (2026)).
const StatutoryBps = 3000

// Expires is when a W-8 signed at signed stops being valid: "a period starting
// on the date the form is signed and ending on the last day of the third
// succeeding calendar year, unless a change in circumstances makes any
// information on the form incorrect" (Instructions for Form W-8BEN and for Form
// W-8BEN-E, Rev. 10-2021). A change in circumstances is any change to the form's
// facts, which makes a new version the signature does not cover.
//
// The instructions also let a form "remain in effect indefinitely" under the
// conditions of Treas. Reg. §1.1441-1(e)(4)(ii); those turn on the payee being
// reported on Form 1042-S every year and on facts this surface does not hold, so
// no form here is treated as indefinite.
//
// The date a form is signed is the signer's, and the platform holds an instant,
// not the signer's zone. So the year is read where that instant is earliest —
// UTC-12 — and the last day ends where it ends first — UTC+14: the form is valid
// through December 31 wherever the signer is, and never a day, let alone a year,
// past it. A form signed on the evening of December 31 in the Americas is 2021's
// in its signer's hand and 2022's in UTC; read in UTC it would stay valid for a
// year the instructions do not give it.
func Expires(signed int64) int64 {
	y := time.Unix(signed, 0).In(earliest).Year()
	return time.Date(y+4, time.January, 1, 0, 0, 0, 0, latest).Unix() - 1
}

// earliest and latest are the zones in use at the two ends of the calendar day.
var (
	earliest = time.FixedZone("UTC-12", -12*60*60)
	latest   = time.FixedZone("UTC+14", 14*60*60)
)

// valid reports whether a certified form establishes what it certifies at now: a
// W-8 until it expires. A W-9 certified for its version does not expire.
func (r record) valid(certified bool, now int64) bool {
	if !certified {
		return false
	}
	return !r.Form.of().Foreign() || now <= Expires(r.Certification.Signed)
}

// payee is what the payer knows about a payee when it classifies a payment, from
// the form the payee granted it (nil when none) and whether that form's
// certification covers its current version: a valid W-8 establishes foreign
// status; a W-8 that is not valid establishes nothing; a W-9 carries the
// classification the exemptions turn on.
func (r *record) payee(certified bool, cat Category) Payee {
	pe := Payee{Category: cat}
	switch {
	case r == nil:
	case r.Form.of().Foreign() && r.valid(certified, now()):
		pe.Foreign = true
	case r.Form.of().Foreign():
		pe.Lapsed = true
	default:
		pe.W9, pe.Classification, pe.ExemptPayee = true, r.Classification, r.ExemptPayee
	}
	return pe
}

// FTIN is a foreign taxpayer identification number. Its shape is the issuing
// jurisdiction's, so nothing here checks more than its characters and length. Its
// String is masked, so an FTIN that reaches a formatter by accident prints four
// characters at most.
type FTIN struct{ value string }

// ParseFTIN reads a foreign TIN, or says which rule it breaks without repeating it.
func ParseFTIN(s string) (FTIN, error) {
	v := strings.Join(strings.Fields(s), " ")
	if v == "" || len(v) > 40 {
		return FTIN{}, fmt.Errorf("%w: a foreign TIN is 1 to 40 characters", errTIN)
	}
	for _, r := range v {
		ok := r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || strings.ContainsRune(" -./", r)
		if !ok {
			return FTIN{}, fmt.Errorf("%w: a foreign TIN is letters, digits, spaces, hyphens, periods and slashes", errTIN)
		}
	}
	return FTIN{value: v}, nil
}

// String masks the number: its last four characters when it is long enough that
// four do not give it away, and nothing otherwise.
func (f FTIN) String() string {
	switch {
	case f.value == "":
		return ""
	case len(f.value) < 8:
		return "****"
	}
	return "****" + f.value[len(f.value)-4:]
}

// GoString keeps %#v masked too.
func (f FTIN) GoString() string { return f.String() }

// Zero reports that no number is held.
func (f FTIN) Zero() bool { return f.value == "" }

// full is the number as issued. Only the audited full read calls it.
func (f FTIN) full() string { return f.value }

// ids are a profile's numbers, opened: the U.S. TIN and the foreign TIN.
type ids struct {
	us      TIN
	foreign FTIN
}

// none reports that the profile holds no number at all.
func (n ids) none() bool { return n.us.Zero() && n.foreign.Zero() }

// formOf is a profile as the plane answers it: every number masked, and whether
// it is valid today.
func formOf(r record, n ids, row *profileRow) *client.TaxForm {
	certified := r.Certification.Status == certCertified && r.Certification.Version == row.version
	f := &client.TaxForm{
		Form: string(r.Form.of()), Name: r.Name, BusinessName: r.BusinessName,
		Country: "US", Residence: r.Address.Country, Address: oneLine(r.Address),
		Classification: string(r.Classification), ExemptPayee: r.ExemptPayee,
		TIN: n.us.String(), TINType: string(n.us.Type), ForeignTIN: n.foreign.String(), Certified: certified,
	}
	if certified {
		f.Signed = r.Certification.Signed
	}
	if r.Form.of() == FormW9 {
		// A W-9 is valid for these payments with its TIN, certified or not: the
		// payee "must give your correct TIN, but you do not have to sign the
		// certification unless you have been notified that you have previously given
		// an incorrect TIN" (Form W-9, Rev. March 2024, Signature requirements, item 4).
		f.Valid = !n.us.Zero()
		return f
	}
	w := r.W8
	f.Country, f.Birth, f.Chapter3, f.Chapter4 = w.Country, w.Birth, string(w.Chapter3), w.Chapter4
	if r.Form == FormW8BEN {
		f.Chapter3 = "individual"
	}
	if certified {
		f.Expires = Expires(r.Certification.Signed)
	}
	f.Valid = r.valid(certified, now())
	if w.claims() {
		t := w.Treaty
		f.Treaty = &t
	}
	return f
}

// past reports whether s is a YYYY-MM-DD date before today.
func past(s string) bool {
	t, err := time.Parse(time.DateOnly, s)
	return err == nil && t.Before(time.Now()) && t.Year() >= 1900
}

// giin reports whether s has a GIIN's shape: 19 characters, XXXXXX.XXXXX.XX.XXX.
func giin(s string) bool {
	if len(s) != 19 || s[6] != '.' || s[12] != '.' || s[15] != '.' {
		return false
	}
	for i, r := range s {
		if i == 6 || i == 12 || i == 15 {
			continue
		}
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// keys lists a table's keys, sorted, for a refusal that names every choice.
func keys[K ~string, V any](m map[K]V) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, string(k))
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}

// pack is the text a foreign TIN is sealed as, tagged so a sealed U.S. TIN can never be
// opened as one or the other way round. The zero FTIN packs to the empty string.
func (f FTIN) pack() string {
	if f.Zero() {
		return ""
	}
	return ftinTag + f.value
}

// unpackFTIN reads what pack wrote. Nothing packed is the zero FTIN.
func unpackFTIN(s string) (FTIN, error) {
	if s == "" {
		return FTIN{}, nil
	}
	v, ok := strings.CutPrefix(s, ftinTag)
	if !ok || v == "" {
		return FTIN{}, fmt.Errorf("tax: a sealed foreign TIN is malformed")
	}
	return FTIN{value: v}, nil
}

const ftinTag = "ftin:"

// certificationOf is the /v1/legal template a form's certification is signed on,
// and the fields rendered into it.
func certificationOf(r record, n ids, version int) (string, []client.LegalField) {
	switch r.Form.of() {
	case FormW8BEN, FormW8BENE:
		w := r.W8
		fields := []client.LegalField{
			{Key: "name", Value: r.Name},
			{Key: "country", Value: w.Country},
			{Key: "address", Value: oneLine(r.Address)},
			{Key: "us_tin", Value: orNone(n.us.String())},
			{Key: "foreign_tin", Value: orNone(n.foreign.String())},
			{Key: "treaty", Value: treatyLine(w)},
			{Key: "capacity", Value: orNone(w.Capacity)},
			{Key: "profile", Value: fmt.Sprint(version)},
		}
		if r.Form == FormW8BENE {
			fields = append(fields,
				client.LegalField{Key: "chapter3", Value: w.Chapter3.Label()},
				client.LegalField{Key: "chapter4", Value: chapter4[w.Chapter4]})
			return "w8bene-certification", fields
		}
		return "w8ben-certification", fields
	}
	return "w9-certification", []client.LegalField{
		{Key: "name", Value: r.Name},
		{Key: "business_name", Value: orNone(r.BusinessName)},
		{Key: "classification", Value: r.Classification.Label()},
		{Key: "tin", Value: n.us.String()},
		{Key: "address", Value: oneLine(r.Address)},
		{Key: "profile", Value: fmt.Sprint(version)},
	}
}

// treatyLine is a treaty claim in the form's own sentence, or "none".
func treatyLine(w W8) string {
	if !w.claims() {
		return "none"
	}
	t := w.Treaty
	s := fmt.Sprintf("resident of %s; Article %s; %s%% rate on %s", t.Country, t.Article, pct(t.RateBps), t.Income)
	if t.LOB != "" {
		s += "; limitation on benefits (line 14b): " + lobs[t.LOB]
	}
	if t.Conditions != "" {
		s += "; conditions: " + t.Conditions
	}
	return s
}

// pct renders basis points as a percentage: 1000 is "10", 1250 is "12.5".
func pct(bps int) string {
	if bps%100 == 0 {
		return fmt.Sprint(bps / 100)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%02d", bps/100, bps%100), "0"), ".")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
