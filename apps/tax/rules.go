package tax

// rules.go is the law this package applies, as data, with where each line came
// from. Nothing here does I/O: a payment and the facts about its payee go in, a
// verdict and the rule that produced it come out, so every classification is a
// table test away from being checked against the source it cites.
//
// THE SOURCES, read on irs.gov on 2026-09-23:
//
//   - Instructions for Forms 1099-MISC and 1099-NEC (Rev. December 2026),
//     irs.gov/instructions/i1099mec — the thresholds by box, the corporation
//     exemption and its attorney exception, the Form 1099-K exclusion, and the
//     furnish and file dates.
//   - Publication 1099 (2026), General Instructions for Certain Information
//     Returns, irs.gov/publications/p1099 — the $2,000 threshold and its indexing,
//     IRIS as the only intake system from filing season 2027, backup withholding
//     at 24% (Part N), electronic statements (Part M), TIN truncation, and the
//     weekend/holiday rule.
//   - Notice 2025-62 §2.02, irs.gov/pub/irs-drop/n-25-62.pdf — P.L. 119-21
//     (the One, Big, Beautiful Bill Act) §70433(a) raised the IRC §6041(a)
//     threshold from $600 to $2,000 for payments made after December 31, 2025 and
//     before January 1, 2027; §6041(h), added by §70433(b), indexes it for
//     inflation for payments made after December 31, 2026, rounded to the nearest
//     $100; §70433(c) made §6041A (nonemployee compensation) follow §6041(a).
//   - IRC §6050N(a) — royalties are reported at $10 or more, a threshold P.L.
//     119-21 did not touch (Instructions for Forms 1099-MISC and 1099-NEC, Rev.
//     12-2026, box 2: "gross royalty payments (or similar amounts) of $10 or more").
//   - Form W-9 (Rev. March 2024), Signature requirements, and Publication 1281
//     (Rev. 12-2023) — which IRC §3406 triggers reach these payments (forms.go,
//     backup).
//   - Notice 2014-21 Q&A-12, -13 and -14, irs.gov/pub/irs-drop/n-14-21.pdf — a
//     payment in virtual currency is reportable to the same extent as any payment
//     in property, at its fair market value in U.S. dollars on the date paid, and
//     is subject to backup withholding to the same extent.
//   - Publication 5718 (Processing Year 2026) §10.1 due-date chart,
//     irs.gov/pub/irs-pdf/p5718.pdf.
//   - Publication 515 (2026), irs.gov/publications/p515, and Treas. Reg.
//     §1.6041-4(a)(1) and §1.1441-1(b)(3), ecfr.gov, read 2026-09-24 — a payee
//     documented on a valid Form W-8 is outside Form 1099 reporting, and a payee
//     that is not documented is presumed a U.S. person.

import (
	"fmt"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/money"
)

// Kind is which information return a line belongs on.
type Kind string

const (
	NEC  Kind = "1099-NEC"
	MISC Kind = "1099-MISC"
)

// Category is what the payer bought from a payee. The rails know how money moved
// and never why, so the payer states it once per payee; services is the default
// because nonemployee compensation is what one company paying another for work
// most often is.
type Category string

const (
	Services    Category = "services"    // 1099-NEC box 1a
	Attorney    Category = "attorney"    // legal services: 1099-NEC box 1a, even to a corporation
	Rents       Category = "rents"       // 1099-MISC box 1
	Royalties   Category = "royalties"   // 1099-MISC box 2
	Other       Category = "other"       // 1099-MISC box 3
	Merchandise Category = "merchandise" // goods: not reported
)

// Valid reports whether c is one of the categories above.
func (c Category) Valid() bool {
	switch c {
	case Services, Attorney, Rents, Royalties, Other, Merchandise:
		return true
	}
	return false
}

// Box is one money box on a form.
type Box struct {
	Kind  Kind   `json:"kind"`
	Box   string `json:"box"`
	Label string `json:"label"`
}

// boxOf is where each category is reported. Merchandise has no box.
var boxOf = map[Category]Box{
	Services:  {NEC, "1a", "Nonemployee compensation"},
	Attorney:  {NEC, "1a", "Nonemployee compensation"},
	Rents:     {MISC, "1", "Rents"},
	Royalties: {MISC, "2", "Royalties"},
	Other:     {MISC, "3", "Other income"},
}

// withheld is box 4 on both forms — federal income tax withheld, which is where
// backup withholding is reported.
var withheld = map[Kind]Box{
	NEC:  {NEC, "4", "Federal income tax withheld"},
	MISC: {MISC, "4", "Federal income tax withheld"},
}

// Year is one tax year's published numbers. A year the IRS has not published is
// ABSENT, never guessed: from 2027 the threshold is indexed for inflation and
// only the IRS can say what it is.
type Year struct {
	Year int `json:"year"`
	// Threshold is the §6041(a) amount — at or above it, a box is reported:
	// 1099-NEC box 1a and 1099-MISC boxes 1 and 3.
	Threshold money.Amount `json:"threshold"`
	// Royalties is the 1099-MISC box 2 threshold — $10 under IRC §6050N(a), which
	// P.L. 119-21 did not move.
	Royalties money.Amount `json:"royalties"`
	// Source names where the numbers were read.
	Source string `json:"source"`
}

// years is the table. Thresholds apply by the year a payment is MADE.
var years = map[int]Year{
	2024: {2024, money.FromCents(60000), money.FromCents(1000),
		"IRC §6041(a) as in effect before P.L. 119-21 §70433: $600 (Notice 2025-62 §2.02); royalties $10 (IRC §6050N(a))"},
	2025: {2025, money.FromCents(60000), money.FromCents(1000),
		"IRC §6041(a) as in effect before P.L. 119-21 §70433: $600 for payments made before January 1, 2026 (Notice 2025-62 §2.02); royalties $10 (IRC §6050N(a))"},
	2026: {2026, money.FromCents(200000), money.FromCents(1000),
		"P.L. 119-21 §70433(a): $2,000 for payments made after December 31, 2025 and before January 1, 2027 (Notice 2025-62 §2.02; Instructions for Forms 1099-MISC and 1099-NEC, Rev. 12-2026); royalties $10 (IRC §6050N(a))"},
}

// YearOf answers a tax year's numbers, or says why there are none.
func YearOf(y int) (Year, error) {
	if t, ok := years[y]; ok {
		return t, nil
	}
	if y > 2026 {
		return Year{}, fmt.Errorf("the IRS has not published the %d threshold: IRC §6041(h), added by P.L. 119-21 §70433(b), indexes it for inflation from 2027, and this table holds only published figures", y)
	}
	return Year{}, fmt.Errorf("tax year %d predates payments on Hanzo's rails", y)
}

// thresholdFor is the threshold for one box in one year.
func (y Year) thresholdFor(b Box) money.Amount {
	if b.Kind == MISC && b.Box == "2" {
		return y.Royalties
	}
	return y.Threshold
}

// Deadline is when a form is due to its recipient and to the IRS.
type Deadline struct {
	// Furnish is the date Copy B is due to the recipient.
	Furnish string `json:"furnish"`
	// File is the date the return is due to the IRS, electronically (IRIS).
	File string `json:"file"`
}

// DeadlineOf answers the due dates for a form and year: January 31 to the
// recipient for both forms; to the IRS, January 31 for 1099-NEC (IRC §6071(c))
// and March 31 for an electronically filed 1099-MISC (Pub 5718 §10.1). A date on
// a Saturday, Sunday or legal holiday moves to the next day that is none of them
// (Pub 1099, Guide to Information Returns).
func DeadlineOf(k Kind, year int) Deadline {
	jan31 := time.Date(year+1, time.January, 31, 0, 0, 0, 0, time.UTC)
	file := jan31
	if k == MISC {
		file = time.Date(year+1, time.March, 31, 0, 0, 0, 0, time.UTC)
	}
	return Deadline{Furnish: business(jan31).Format(time.DateOnly), File: business(file).Format(time.DateOnly)}
}

// business rolls a due date forward past a weekend. Neither January 31 nor
// March 31, nor the two days after either, is a federal or District of Columbia
// legal holiday in any year, so a weekend is the only thing that moves them.
func business(d time.Time) time.Time {
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// BackupRate is the backup withholding rate: 24% (Pub 1099 Part N; IRC §3406(a)(1)).
const BackupRate = 24

// Rule is WHY a payment or a total landed where it did, in words a reviewer can
// check against the source it names.
type Rule struct {
	// Code is the rule's stable name, for a caller that branches on it.
	Code string `json:"code"`
	// Reason is the rule, stated.
	Reason string `json:"reason"`
}

// The rules, named once so the classifier and its tests cite the same words.
var (
	ruleCard = Rule{"card", "Paid with a payment card: the payment settlement entity reports it on Form 1099-K under IRC §6050W, " +
		"so it is not reported on Form 1099-MISC or 1099-NEC (Instructions for Forms 1099-MISC and 1099-NEC, Rev. 12-2026)."}
	ruleNetwork = Rule{"network", "Settled through a third-party settlement organization: reported on Form 1099-K under IRC §6050W, " +
		"not on Form 1099-MISC or 1099-NEC (Instructions for Forms 1099-MISC and 1099-NEC, Rev. 12-2026)."}
	ruleMerchandise = Rule{"merchandise", "Payments for merchandise are not reported (Instructions for Forms 1099-MISC and 1099-NEC, Exceptions)."}
	ruleCorporation = Rule{"corporation", "Payments to a corporation, including an LLC treated as a C or S corporation, are generally not reported " +
		"(Instructions for Forms 1099-MISC and 1099-NEC, Exceptions; Treas. Reg. §1.6041-3(p))."}
	ruleExempt   = Rule{"exempt_payee", "The payee's W-9 states an exempt payee code (1–5) that exempts it from this reporting (Instructions for Form W-9, Rev. 03-2024, Line 4)."}
	ruleAttorney = Rule{"attorney", "Payments for legal services are reported even when the attorney is a corporation: " +
		"\"The exemption from reporting payments made to corporations does not apply to payments for legal services\" (Instructions for Forms 1099-MISC and 1099-NEC, Rev. 12-2026)."}
	ruleAsset = Rule{"digital_asset", "Paid in a digital asset: reportable to the same extent as any payment in property, at its fair market value " +
		"in U.S. dollars on the date paid (Notice 2014-21 Q&A-12, Q&A-13)."}
	ruleReportable = Rule{"reportable", "Paid in the course of the payer's trade or business, to a payee that is not exempt, for what the payer categorised this payee as."}
	ruleUnknown    = Rule{"no_w9", "Reportable: without a W-9 the payer holds nothing that establishes an exemption, so it cannot treat the payee as exempt."}
	ruleForeign    = Rule{"foreign", "The payee is a foreign person documented on a valid Form W-8BEN or W-8BEN-E: \"Foreign persons who provide a valid Form W-8 " +
		"... are exempt from backup withholding and Form 1099 reporting\" (Publication 515 (2026); Treas. Reg. §1.6041-4(a)(1)). A U.S.-source " +
		"payment to it is reported on Form 1042-S instead — see /v1/principal/clearance."}
	ruleLapsed = Rule{"w8_lapsed", "Reportable: the payee's Form W-8 is uncertified for its current facts or expired, so it establishes nothing, and " +
		"\"a payment that the withholding agent cannot reliably associate with documentation is presumed to be made to a U.S. person\" " +
		"(Treas. Reg. §1.1441-1(b)(3)). The payee renews it by certifying again."}
)

// thresholdRule states a total against the year's threshold.
func thresholdRule(y Year, b Box, total money.Amount) Rule {
	t := y.thresholdFor(b)
	if total.Cmp(t) >= 0 {
		return Rule{"over_threshold", fmt.Sprintf("%s box %s totals %s, at or above the %s threshold for %d (%s).",
			b.Kind, b.Box, dollars(total), dollars(t), y.Year, y.Source)}
	}
	return Rule{"below_threshold", fmt.Sprintf("%s box %s totals %s, below the %s threshold for %d, so no form is required (%s).",
		b.Kind, b.Box, dollars(total), dollars(t), y.Year, y.Source)}
}

// Payee is what a payer knows about a payee when it classifies: what it buys from
// it, and — only under a live grant — how the payee is classified.
type Payee struct {
	Category       Category
	W9             bool // the payer holds a live grant of the payee's W-9
	Classification Classification
	ExemptPayee    string
	Foreign        bool // the payer holds a live grant of a valid W-8
	Lapsed         bool // the payer holds a live grant of a W-8 that is not valid
}

// Verdict is one payment, classified.
type Verdict struct {
	Reportable bool `json:"reportable"`
	// Box is where it is reported, when it is.
	Box *Box `json:"box,omitempty"`
	// Rules are every rule that applied, in the order applied — the last one is
	// the verdict's reason.
	Rules []Rule `json:"rules"`
}

// Classify decides whether one payment is reportable on a form the PAYER files,
// and in which box. The rail decides the 1099-K exclusions and whether it was paid
// in a digital asset; nothing else about the event does. The threshold is not
// applied here: it is a property of a year's total per payee and box, never of a
// single payment.
func Classify(e client.EconomicEvent, pe Payee) Verdict {
	var v Verdict
	switch e.Rail {
	case client.RailCard:
		return Verdict{Rules: []Rule{ruleCard}}
	case client.RailNetwork:
		return Verdict{Rules: []Rule{ruleNetwork}}
	}
	if e.OnChain() {
		v.Rules = append(v.Rules, ruleAsset)
	}
	if pe.Category == Merchandise {
		return Verdict{Rules: append(v.Rules, ruleMerchandise)}
	}
	if pe.Foreign {
		return Verdict{Rules: append(v.Rules, ruleForeign)}
	}
	cat := pe.Category
	if !cat.Valid() {
		cat = Services
	}
	switch {
	case pe.Lapsed:
		v.Rules = append(v.Rules, ruleLapsed)
	case !pe.W9:
		v.Rules = append(v.Rules, ruleUnknown)
	case exemptCode(pe.ExemptPayee) && pe.ExemptPayee != "5":
		// Codes 1–4 name governments and tax-exempt organizations; none of them is
		// an attorney, so no exception reaches them.
		return Verdict{Rules: append(v.Rules, ruleExempt)}
	case pe.Classification.Corporate() || pe.ExemptPayee == "5":
		if cat != Attorney {
			return Verdict{Rules: append(v.Rules, ruleCorporation)}
		}
		v.Rules = append(v.Rules, ruleCorporation, ruleAttorney)
	default:
		v.Rules = append(v.Rules, ruleReportable)
	}
	b := boxOf[cat]
	v.Reportable, v.Box = true, &b
	return v
}

// exemptCode reports whether code is one of the exempt payee codes that exempt a
// payee from §6041/§6041A reporting: 1 through 5 (Form W-9 instructions, the
// "Payments over $600 required to be reported" row).
func exemptCode(code string) bool {
	switch code {
	case "1", "2", "3", "4", "5":
		return true
	}
	return false
}

// dollars renders an exact amount to cents, the unit every box on the form is in.
func dollars(a money.Amount) string { return money.Cents(a.Cents()).String() }
