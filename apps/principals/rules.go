package principals

// rules.go is the law a clearance applies, as data, with where each rule came
// from. Nothing here does I/O: the facts go in, a decision comes out, and every
// field of it carries the rule that decided it — so every clearance is a table
// test away from being checked against the source it cites.
//
// THE SOURCES, read on 2026-09-24:
//
//   - Publication 515 (2026), Withholding of Tax on Nonresident Aliens and Foreign
//     Entities, irs.gov/publications/p515 — the 30% rate, reduced rates on valid
//     documentation, source by place of performance (Chart B), FDAP's exclusion of
//     gains from the sale of property, Form 8233 for an individual's treaty claim
//     on personal services, backup withholding at 24%, Forms 1042, 1042-S and 945.
//   - IRC §1441(a), §1442(a), §861(a)(3), §862(a)(3), §3406(a)(1)(A),
//     uscode.house.gov; Treas. Reg. §1.1441-1(b)(3), §1.1441-6(b)(1),
//     §1.6041-4(a)(1), ecfr.gov.
//   - Instructions for Form 1042-S (2026), irs.gov/instructions/i1042s; for Form
//     1042 (2025), irs.gov/instructions/i1042; for Form 945 (2025),
//     irs.gov/instructions/i945; for Forms W-8BEN and W-8BEN-E (Rev. 10-2021).
//   - Council Directive (EU) 2021/514 (DAC7), OJ L 104/1, 25.3.2021, Annex V; the
//     OECD Model Rules for Reporting by Platform Operators (2020); The Platform
//     Operators (Due Diligence and Reporting Requirements) Regulations 2023, SI
//     2023/817; Income Tax Act (Canada) Part XX and the CRA's guidance on it;
//     Taxation Administration Act 1953 (Cth) Sch 1 s 396-55 item 15 and the
//     Commissioner's instrument F2023L00850.
//   - OFAC, Basic Information on OFAC and Sanctions, FAQ 91; Council Regulation
//     (EU) No 269/2014, Art. 2(2); SI 2019/855, reg. 12(1).
//
// The 1099 rules are apps/tax's, called rather than restated: one classifier and
// one threshold table, so a clearance and the forms tax prepares cannot disagree.

import (
	"fmt"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/principal"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/apps/tax"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/money"
)

// Case is everything one clearance decides on. The op gathers it; nothing in it
// is fetched here.
type Case struct {
	Payer, Payee string
	// Cents is the payment's gross amount in U.S. cents.
	Cents int64
	// Category is what the payer buys: services, attorney, rents, royalties,
	// other or merchandise (apps/tax's categories).
	Category tax.Category
	// Rail is the rail it moves on, one of client.Rails.
	Rail string
	// Performed is where the service is performed — for rents where the property
	// is, for royalties where it is used — ISO 3166-1 alpha-2, "" when not stated.
	Performed string
	// Platform is whether the payer operates the platform through which the payee
	// provides this to the platform's users; nil when not stated.
	Platform *bool
	// Year is the calendar year the payment is made in.
	Year int

	PayerID, PayeeID         iam.Identity
	PayerScreen, PayeeScreen iam.Screening
	// Held is what the payer holds for the payee: its request's status, and the
	// payee's form under a live grant. It is the only source of the payee's
	// status: what the payee certified about itself and has not granted is the
	// payee's, and a payer's duties turn on the documentation the payer holds.
	Held client.PayeeForm
	// Wallets is whether the payee holds an address a chain payment can go to.
	Wallets bool
}

// The rules, named once so the decision and its tests cite the same words.
var (
	ruleIdentity = principal.Rule{Code: "identity", Reason: "Hanzo policy: the payer and the payee of a payment on Hanzo's rails are identified before " +
		"money moves — every founder of the org's formation (its beneficial owners as the formation identified them) verified by " +
		"the licensed identity-verification provider or confirmed by a Hanzo platform reviewer. It is the platform's own rule, " +
		"modeled on the beneficial-ownership identification in 31 CFR §1010.230; it is a policy of this platform, not a statement " +
		"that the regulation reaches either org."}
	ruleBlocked = principal.Rule{Code: "sanctions_blocked", Reason: "An org is a confirmed match to a designated person. \"U.S. persons are " +
		"generally prohibited from dealing with the individuals and entities on the SDN List\" (OFAC, Basic Information on OFAC " +
		"and Sanctions, FAQ 91); \"No funds or economic resources shall be made available, directly or indirectly, to or for the " +
		"benefit of\" a listed person (Council Regulation (EU) No 269/2014, Art. 2(2)); \"A person ('P') must not make funds " +
		"available directly or indirectly to a designated person\" (SI 2019/855, reg. 12(1))."}
	ruleReview = principal.Rule{Code: "sanctions_review", Reason: "A name an org is known by is a possible match to a designated person. A " +
		"Hanzo platform reviewer decides it on the other identifiers before any payment to or from the org clears; an org " +
		"never decides its own match."}
	ruleUnavailable = principal.Rule{Code: "sanctions_unavailable", Reason: "The OFAC, UN, EU and UK lists are not all loaded and fresh, " +
		"so no org can be screened: an answer from a partial or stale set would read as clear for an org designated only on " +
		"what is missing (luxfi/aml pkg/screen, Readiness.Unfit)."}
	ruleUnscreened = principal.Rule{Code: "sanctions_unscreened", Reason: "An org with no name on file cannot be screened, and an " +
		"unscreened org is not a clear one."}
	ruleW9 = principal.Rule{Code: "w9_tin", Reason: "\"You must withhold 24% (backup withholding rate) from certain reportable payments made " +
		"to a U.S. person that is subject to Form 1099 reporting if ... The U.S. person has not provided its taxpayer " +
		"identification number (TIN) in the manner required\" (Publication 515 (2026); IRC §3406(a)(1)(A); Pub 1281, Rev. 12-2023). " +
		"The duty follows the reporting threshold: \"the minimum threshold amount for reporting certain payments ... and/or perform " +
		"backup withholding on those payments increased to $2,000\" for tax years beginning after 2025 (Publication 1099 (2026))."}
	ruleTIN = principal.Rule{Code: "tin_on_file", Reason: "The payer holds the payee's W-9 with its TIN, so the missing-TIN trigger of IRC " +
		"§3406(a)(1)(A) does not apply. Backup withholding would still follow an IRS notice that the TIN is incorrect " +
		"(§3406(a)(1)(B)), which reaches the payer and not this platform."}
	ruleNotReportable = principal.Rule{Code: "not_reportable", Reason: "Backup withholding reaches reportable payments only (IRC §3406(b)), " +
		"and apps/tax's classification finds this one is not reported on Form 1099 by the payer."}
	ruleW8 = principal.Rule{Code: "w8", Reason: "A foreign payee documents its status on Form W-8BEN (an individual) or W-8BEN-E (an " +
		"entity). Without a valid one \"you must withhold 30% from the gross amount paid to a foreign payee unless you can " +
		"reliably associate the payment with valid documentation\", and \"You are not permitted to apply a reduced rate of " +
		"chapter 3 withholding based on a payee's presumed status if documentation is required\" (Publication 515 (2026)); a " +
		"payment not associated with documentation is otherwise presumed made to a U.S. person (Treas. Reg. §1.1441-1(b)(3)), " +
		"which brings Form 1099 and backup withholding with it."}
	ruleNRA = principal.Rule{Code: "nra_30", Reason: "U.S.-source FDAP income paid to a foreign person is withheld at 30% (IRC §1441(a) for " +
		"a nonresident alien individual or foreign partnership, §1442(a) for a foreign corporation): \"Most types of U.S. source " +
		"income received by a foreign person are subject to U.S. tax of 30%\" (Publication 515 (2026))."}
	ruleTreaty = principal.Rule{Code: "treaty_rate", Reason: "The payee's valid W-8 claims a treaty rate for this type of income, and \"a " +
		"withholding agent may rely on a claim that a beneficial owner is entitled to a reduced rate of withholding based upon an " +
		"income tax treaty if, prior to the payment, the withholding agent can reliably associate the payment with a beneficial " +
		"owner withholding certificate\" (Treas. Reg. §1.1441-6(b)(1))."}
	rule8233 = principal.Rule{Code: "form_8233", Reason: "An individual's treaty exemption on pay for personal services performed in the " +
		"United States is claimed on Form 8233, not Form W-8BEN: \"Independent nonresident alien contractors use Form 8233 to " +
		"claim an exemption from withholding under a tax treaty\" (Publication 515 (2026)); the W-8BEN is not used by \"a " +
		"nonresident alien individual who claims exemption from withholding on compensation for independent or dependent " +
		"personal services performed in the United States\" (Instructions for Form W-8BEN, Rev. 10-2021)."}
	ruleUSSource = principal.Rule{Code: "us_source", Reason: "The income is from U.S. sources: pay for personal services is sourced \"where " +
		"the services are performed\", rents \"where the property is located\", and royalties \"where the property is used\" " +
		"(Publication 515 (2026), Chart B; IRC §861(a)(3)–(4))."}
	ruleForeignSource = principal.Rule{Code: "foreign_source", Reason: "The income is from sources outside the United States: \"The place " +
		"where the services are performed determines the source of the income, regardless of where the contract was made, the " +
		"place of payment, or the residence of the payer\" (Publication 515 (2026); IRC §862(a)(3)); rents and royalties follow " +
		"the property (Chart B). Chapter 3 withholding and Form 1042-S reach U.S.-source amounts only (Instructions for Form " +
		"1042-S (2026))."}
	ruleGoods = principal.Rule{Code: "goods", Reason: "A payment for goods is not FDAP income — \"FDAP income is all income except: Gains " +
		"from the sale of property\" (Publication 515 (2026)) — so chapter 3 does not withhold on it, and Form 1099 does not " +
		"report it (Instructions for Forms 1099-MISC and 1099-NEC, Exceptions)."}
	ruleUnknown = principal.Rule{Code: "status_unknown", Reason: "Whether the payee is a U.S. or a foreign person decides everything else — " +
		"backup withholding and Form 1099 for one, chapter 3 withholding and Form 1042-S for the other — and the payer holds no " +
		"form of the payee's that says which. What the payee certified about itself is not the payer's until the payee grants it, " +
		"and a payment the payer \"cannot, prior to the payment, reliably associate ... with valid documentation\" falls to the " +
		"presumption rules (Treas. Reg. §1.1441-1(b)(3)(i)); this platform clears no payment on a presumption, so the form comes " +
		"first."}
	ruleLapsed = principal.Rule{Code: "w8_lapsed", Reason: "The payer holds the payee's Form W-8, but it is uncertified for its current facts or " +
		"past its validity — \"the last day of the third succeeding calendar year\" after it was signed, or until a change in " +
		"circumstances (Instructions for Forms W-8BEN and W-8BEN-E, Rev. 10-2021) — so the payer cannot reliably associate the " +
		"payment with valid documentation (Treas. Reg. §1.1441-1(b)(3)(i)), and no treaty rate may rest on it (§1.1441-6(b)(1)). " +
		"The payee renews it by certifying it again."}
	ruleHold = principal.Rule{Code: "payee_hold", Reason: "The platform holds a fact about the payee that stops this payment until a Hanzo " +
		"platform reviewer resolves it. What it is belongs to the payee and is not answered to a payer; the payer may contact " +
		"support, and the payee sees it on its own principal."}
	ruleSettle = principal.Rule{Code: "net_settlement", Reason: "Nothing on Hanzo's rails withholds tax: the payer pays the payee the net " +
		"amount and deposits what it withheld itself, reported on Form 1042 for chapter 3 and on Form 945 for backup " +
		"withholding (Publication 515 (2026): \"A payer files a tax return on Form 945 to report backup withholding\")."}
	ruleNone    = principal.Rule{Code: "not_cleared", Reason: "No settlement method is offered for a payment that is not cleared."}
	ruleAllowed = principal.Rule{Code: "cleared", Reason: "Nothing is required before the payment and no fact it turns on is unknown."}
	ruleHeld    = principal.Rule{Code: "required", Reason: "A step is required before the payment, or a fact it turns on is unknown; see " +
		"required_before_payment and facts_required."}
	rule1042S = principal.Rule{Code: "1042s", Reason: "\"Every withholding agent ... must file an information return on Form 1042-S\" for " +
		"\"amounts from U.S. sources paid to foreign persons (including persons presumed to be foreign)\", and \"must file a Form " +
		"1042-S even if you did not withhold tax under chapter 3 because the income was exempt from tax under a U.S. tax treaty\" " +
		"— to the IRS and the recipient \"by March 15 of the following calendar year\" (Instructions for Form 1042-S (2026))."}
	rule1042 = principal.Rule{Code: "1042", Reason: "\"Every U.S. and foreign withholding agent that is required to file a Form 1042-S must " +
		"also file an annual return on Form 1042\" (Publication 515 (2026)), \"by the 15th day of the 3rd month after the end of " +
		"the calendar year (March 15)\" (Instructions for Form 1042)."}
	rule945 = principal.Rule{Code: "945", Reason: "\"If you withhold or are required to withhold federal income tax (including backup " +
		"withholding) from nonpayroll payments, you must file Form 945\", due January 31 of the following year, the next " +
		"business day when that falls on a weekend or legal holiday (Instructions for Form 945)."}
	rulePlatform = principal.Rule{Code: "platform_operator", Reason: "Platform-operator reporting (EU DAC7, and the OECD Model Rules as the " +
		"UK, Canada and others adopted them) is owed by the operator of a platform that connects a seller with its users, for " +
		"the seller — not by every payer. Whether the payer is that operator for this payee decides whether it owes one."}
	rule8ECI = principal.Rule{Code: "eci", Reason: "Income effectively connected with the conduct of a U.S. trade or business is not " +
		"documented on Form W-8BEN-E: \"Instead, provide Form W-8ECI\" (Instructions for Form W-8BEN-E, Rev. 10-2021), and the " +
		"withholding follows that form."}
	ruleWhere = principal.Rule{Code: "source_unknown", Reason: "The source of the income decides whether chapter 3 withholds on it, and the " +
		"source is where the service is performed, or where rented property is located or licensed property used " +
		"(Publication 515 (2026), Chart B)."}
)

// Status is what the payer may treat the payee as.
const (
	statusUS      = "us"
	statusForeign = "foreign"
	statusUnknown = "unknown"
)

// Decide applies the rules to one Case.
func Decide(c Case) principal.Decision {
	d := &decision{Decision: principal.Decision{Required: []principal.Step{}, Reporting: []principal.Obligation{},
		Settlement: []principal.Method{}, Facts: []principal.Fact{}}}
	d.identity(c)
	d.sanctions(c)
	d.tax(c)
	d.platform(c)
	d.settle(c)
	return d.Decision
}

// decision is a Decision as the rules make it, with the cents withheld beside
// it for settlement.
type decision struct {
	principal.Decision
	withheld int64
}

// identity requires payer and payee verified.
func (d *decision) identity(c Case) {
	if c.PayerID.Status != idVerified {
		d.Required = append(d.Required, principal.Step{Code: "payer_identity", Who: "payer", Where: "POST /v1/company/kyc",
			What: "The payer's founders complete identity verification: " + c.PayerID.Reason, Rule: ruleIdentity})
	}
	if c.PayeeID.Status != idVerified {
		d.Required = append(d.Required, principal.Step{Code: "payee_identity", Who: "payee",
			What: "The payee's founders complete identity verification.", Rule: ruleIdentity})
	}
}

// sanctions requires payer and payee screened clear. The payer reads its own
// verdict; of the payee's it reads only what it must act on — that the lists
// could not be screened against, that the payee states nothing to screen, or
// that the payment is held — never that the payee screened close to anything.
func (d *decision) sanctions(c Case) {
	switch c.PayerScreen.Status {
	case screenClear:
	case screenBlocked:
		d.Required = append(d.Required, principal.Step{Code: "sanctions_blocked", Who: "none",
			What: "No payment may be made: the payer is a confirmed match to a designated person.", Rule: ruleBlocked})
	case screenReview:
		d.Required = append(d.Required, principal.Step{Code: "sanctions_review", Who: "Hanzo platform reviewer",
			What: "A possible sanctions match on the payer is decided.", Rule: ruleReview})
	case screenUnscreened:
		d.Required = append(d.Required, principal.Step{Code: "sanctions_unscreened", Who: "payer",
			What: "The payer states a name to screen: a formation or a tax form.", Rule: ruleUnscreened})
	default:
		d.Required = append(d.Required, unavailable)
	}
	switch c.PayeeScreen.Status {
	case screenClear:
	case screenBlocked, screenReview:
		d.Required = append(d.Required, principal.Step{Code: "payee_hold", Who: "Hanzo platform reviewer",
			What: "This payment cannot clear now. Contact support.", Rule: ruleHold})
	case screenUnscreened:
		d.Required = append(d.Required, principal.Step{Code: "sanctions_unscreened", Who: "payee",
			What: "The payee states its name: a formation or a tax form.", Rule: ruleUnscreened})
	default:
		if !slices.Contains(d.Required, unavailable) {
			d.Required = append(d.Required, unavailable)
		}
	}
}

// unavailable is the step lists that cannot be screened against require; it is
// the platform's, whoever the payer and payee are.
var unavailable = principal.Step{Code: "sanctions_unavailable", Who: "Hanzo platform",
	What: "Screening runs against the current lists; until they are loaded and fresh, no payment clears.", Rule: ruleUnavailable}

// status is what the payer may treat the payee as, the form it rests on, and the
// rule that decided it. It is read off the form the payee granted the payer and
// nothing else: a W-9 is a U.S. person, a valid W-8 a foreign one, and a W-8 that
// is not valid — like no form at all — documents neither.
func status(c Case) (string, *client.TaxForm, principal.Rule) {
	if c.Held.Status != "granted" || c.Held.Form == nil || c.Held.Form.Form == "" {
		return statusUnknown, nil, ruleUnknown
	}
	f := c.Held.Form
	switch {
	case f.Form == "w9":
		return statusUS, f, principal.Rule{Code: "w9_held", Reason: "The payer holds the payee's W-9: it is a U.S. person."}
	case f.Valid:
		return statusForeign, f, principal.Rule{Code: "w8_held", Reason: "The payer holds the payee's valid " + label(f.Form) + ": it is a foreign person."}
	}
	return statusUnknown, f, ruleLapsed
}

// tax decides withholding and the U.S. information returns.
func (d *decision) tax(c Case) {
	st, held, why := status(c)
	d.Status = st
	d.Rules = append(d.Rules, principal.Decided{Field: "status", Rule: why})
	switch {
	case st == statusUS:
		d.domestic(c, held)
	case st == statusForeign:
		d.foreign(c, held)
	case c.Category == tax.Merchandise:
		d.Withholding = principal.Withholding{Rate: "0", Amount: usd(0), Reason: "A payment for goods is withheld on by neither chapter.", Rule: ruleGoods}
	case held != nil:
		d.Required = append(d.Required, principal.Step{Code: "w8", Who: "payee", Where: "POST /v1/tax/profile/certify",
			What: "The payee renews its " + label(held.Form) + ": it is uncertified for its current facts or expired, and certifying it again renews it.",
			Rule: ruleLapsed})
		d.Withholding = principal.Withholding{Reason: "Undetermined until the payee's form is valid again.", Rule: ruleLapsed}
	default:
		d.Required = append(d.Required, principal.Step{Code: "tax_form", Who: "payee", Where: "POST /v1/tax/w9",
			What: "The payee grants the payer its W-9 (a U.S. person) or its W-8BEN or W-8BEN-E (a foreign person).", Rule: ruleUnknown})
		d.Facts = append(d.Facts, principal.Fact{Code: "us_person", Blocks: true, Rule: ruleUnknown,
			Question: "Is the payee a U.S. person? Its form answers it: backup withholding at 24% on a U.S. person without a TIN, " +
				"chapter 3 withholding at 30% on U.S.-source income to a foreign person, reduced only by a treaty rate a valid W-8 claims."})
		d.Withholding = principal.Withholding{Reason: "Undetermined until the payee's status is documented.", Rule: ruleUnknown}
	}
	d.Rules = append(d.Rules, principal.Decided{Field: "withholding", Rule: d.Withholding.Rule})
}

// domestic is a U.S. payee: backup withholding without a TIN, and the 1099.
func (d *decision) domestic(c Case, held *client.TaxForm) {
	pe := tax.Payee{Category: c.Category}
	if held != nil {
		pe.W9, pe.Classification, pe.ExemptPayee = true, tax.Classification(held.Classification), held.ExemptPayee
	}
	v := tax.Classify(client.EconomicEvent{Rail: c.Rail}, pe)
	last := principal.Rule(v.Rules[len(v.Rules)-1])
	switch {
	case !v.Reportable:
		d.Withholding = principal.Withholding{Rate: "0", Amount: usd(0), Reason: last.Reason, Rule: ruleNotReportable}
	case held == nil || held.TIN == "":
		d.Required = append(d.Required, principal.Step{Code: "w9", Who: "payee", Where: "POST /v1/tax/w9",
			What: "The payee furnishes its W-9 with its TIN.", Rule: ruleW9})
		d.withhold("backup", tax.BackupRate*100, c.Cents,
			"The payer holds no TIN for this U.S. payee: paid without one, 24% of this payment is withheld as backup withholding "+
				"once the year's reportable total to the payee reaches the threshold.", ruleW9)
		d.Facts = append(d.Facts, principal.Fact{Code: "year_total", Rule: ruleW9,
			Question: "Does this payment bring the year's total paid to this payee in the 1099 box it is reported in to the threshold? " +
				"Backup withholding and the 1099 turn on it; GET /v1/tax/payments sums the year so far."})
		d.Reporting = append(d.Reporting, principal.Obligation{Code: "945", Form: "945", Filer: c.Payer, Jurisdiction: "US",
			Authority: "IRS", Due: fmt.Sprintf("January 31, %d", c.Year+1), When: "when backup withholding is withheld", Rule: rule945})
	default:
		d.Withholding = principal.Withholding{Rate: "0", Amount: usd(0), Reason: "The payer holds the payee's TIN.", Rule: ruleTIN}
	}
	if !v.Reportable {
		d.Rules = append(d.Rules, principal.Decided{Field: "reporting_obligations", Rule: last})
		return
	}
	box := *v.Box
	due := tax.DeadlineOf(box.Kind, c.Year)
	when := ""
	if y, err := tax.YearOf(c.Year); err != nil {
		when = err.Error()
		d.Facts = append(d.Facts, principal.Fact{Code: "threshold", Rule: last,
			Question: fmt.Sprintf("What is the IRS's %d threshold for %s box %s (the IRS publishes it; it is not yet here)?", c.Year, box.Kind, box.Box)})
	} else {
		t := y.Threshold
		if box.Kind == tax.MISC && box.Box == "2" {
			t = y.Royalties
		}
		when = fmt.Sprintf("when the %d total paid to this payee in %s box %s reaches %s (%s)", c.Year, box.Kind, box.Box, money.Cents(t.Cents()), y.Source)
	}
	d.Reporting = append(d.Reporting, principal.Obligation{Code: "1099", Form: string(box.Kind), Filer: c.Payer, Jurisdiction: "US",
		Authority: "IRS", Due: "furnish to the payee by " + due.Furnish + "; file by " + due.File, When: when, Rule: last})
}

// foreign is a foreign payee on a valid W-8 the payer holds: chapter 3 on
// U.S.-source income, and the 1042-S.
func (d *decision) foreign(c Case, held *client.TaxForm) {
	if c.Category == tax.Merchandise {
		d.Withholding = principal.Withholding{Rate: "0", Amount: usd(0), Reason: "A payment for goods is not FDAP income.", Rule: ruleGoods}
		return
	}
	if c.Performed == "" {
		d.Facts = append(d.Facts, principal.Fact{Code: "performed", Blocks: true, Rule: ruleWhere,
			Question: "Where is the service performed — or, for rents, where is the property; for royalties, where is it used?"})
		d.Withholding = principal.Withholding{Reason: "Undetermined until the source of the income is known.", Rule: ruleWhere}
		return
	}
	if c.Performed != "US" {
		d.Withholding = principal.Withholding{Rate: "0", Amount: usd(0), Reason: "Performed outside the United States: foreign-source income, " +
			"which chapter 3 does not withhold on and Form 1042-S does not report.", Rule: ruleForeignSource}
		d.Rules = append(d.Rules, principal.Decided{Field: "reporting_obligations", Rule: ruleForeignSource})
		return
	}
	d.Rules = append(d.Rules, principal.Decided{Field: "source", Rule: ruleUSSource})
	rate, why, reason := StatutoryBps, ruleNRA, "U.S.-source income paid to a foreign person: 30% is withheld."
	if held.Treaty != nil && covers(held.Treaty.Income, c.Category) {
		switch {
		case held.Form == "w8ben" && (c.Category == tax.Services || c.Category == tax.Attorney):
			why, reason = rule8233, "The W-8BEN claims a treaty rate, but an individual's treaty exemption on pay for personal "+
				"services performed in the United States is claimed on Form 8233: 30% is withheld until one is furnished."
			d.Facts = append(d.Facts, principal.Fact{Code: "form_8233", Rule: rule8233,
				Question: "Will the payee furnish Form 8233 to claim its treaty exemption on this compensation?"})
		case held.Treaty.RateBps < rate:
			rate, why = held.Treaty.RateBps, ruleTreaty
			reason = fmt.Sprintf("The payee's valid %s claims %s%% under Article %s of the treaty with %s for %s.",
				label(held.Form), pct(rate), held.Treaty.Article, held.Treaty.Country, held.Treaty.Income)
		}
	}
	if held.Form == "w8bene" && (c.Category == tax.Services || c.Category == tax.Attorney) {
		d.Facts = append(d.Facts, principal.Fact{Code: "eci", Rule: rule8ECI,
			Question: "Is this income effectively connected with the payee's conduct of a U.S. trade or business?"})
	}
	d.withhold("3", rate, c.Cents, reason, why)
	next := c.Year + 1
	d.Reporting = append(d.Reporting,
		principal.Obligation{Code: "1042-S", Form: "1042-S", Filer: c.Payer, Jurisdiction: "US", Authority: "IRS",
			Due: fmt.Sprintf("March 15, %d, to the IRS and the payee", next), Rule: rule1042S},
		principal.Obligation{Code: "1042", Form: "1042", Filer: c.Payer, Jurisdiction: "US", Authority: "IRS",
			Due: fmt.Sprintf("March 15, %d", next), Rule: rule1042})
}

// covers reports whether a treaty claim for income covers a payment category.
func covers(income string, cat tax.Category) bool {
	if cat == tax.Attorney {
		cat = tax.Services
	}
	return income == string(cat)
}

// withhold is a withholding at bps of gross, rounded half up to the cent.
func (d *decision) withhold(chapter string, bps int, gross int64, reason string, r principal.Rule) {
	c := (gross*int64(bps) + 5000) / 10000
	d.Withholding, d.withheld = principal.Withholding{Chapter: chapter, Rate: pct(bps), Amount: usd(c), Reason: reason, Rule: r}, c
}

// regime is one jurisdiction's platform-operator reporting.
type regime struct {
	code, law, form, authority string
	// due is the deadline for a payment in year y.
	due func(y int) string
	// covers is the categories that are a relevant activity there.
	covers []tax.Category
	rule   principal.Rule
}

// eu is the European Union's Member States, whose residents are DAC7's
// reportable sellers.
var eu = strings.Fields(`AT BE BG HR CY CZ DK EE FI FR DE GR IE IT LV LT LU MT NL PL PT RO SK SI ES SE`)

// regimes are the platform-operator reporting regimes this table holds, keyed by
// the seller's country of residence.
var regimes = map[string]regime{
	"EU": {code: "dac7", law: "Council Directive (EU) 2021/514 (DAC7)", form: "DAC7 seller report (DPI XML)",
		authority: "the competent authority of the EU Member State where the payer is resident, or where a non-Union operator registered",
		due:       func(y int) string { return fmt.Sprintf("January 31, %d", y+1) },
		covers:    []tax.Category{tax.Services, tax.Attorney, tax.Rents, tax.Merchandise},
		rule: principal.Rule{Code: "dac7", Reason: "A Reporting Platform Operator reports each Reportable Seller — \"any Active Seller, other than " +
			"an Excluded Seller, that is resident in a Member State\" — for the Relevant Activities it facilitates (the rental of " +
			"immovable property, a Personal Service, the sale of Goods, the rental of any mode of transport), \"no later than 31 " +
			"January of the year following\" (Council Directive (EU) 2021/514, Annex V, Sections I and III). A seller of goods with " +
			"fewer than 30 sales and no more than EUR 2 000 in the period, a governmental entity and a listed entity are excluded."}},
	"GB": {code: "uk_mrdp", law: "The Platform Operators (Due Diligence and Reporting Requirements) Regulations 2023 (SI 2023/817)",
		form: "Digital platform report (HMRC XML schema)", authority: "HM Revenue & Customs",
		due:    func(y int) string { return fmt.Sprintf("January 31, %d", y+1) },
		covers: []tax.Category{tax.Services, tax.Attorney, tax.Rents, tax.Merchandise},
		rule: principal.Rule{Code: "uk_mrdp", Reason: "\"a reporting platform operator must, for each reportable period, make a report to HMRC ... on " +
			"or before the 31st January following the end of the reportable period\" (SI 2023/817, reg. 4(1)), in force from 1 January " +
			"2024; the goods de minimis \"does not apply to: the provision of services; rental of transport; rental of immoveable " +
			"property\" (HMRC, Check if you need to register as a digital platform operator)."}},
	"CA": {code: "ca_part_xx", law: "Income Tax Act (Canada), Part XX", form: "Part XX information return (CRA XML schema)",
		authority: "Canada Revenue Agency",
		due:       func(y int) string { return fmt.Sprintf("January 31, %d", y+1) },
		covers:    []tax.Category{tax.Services, tax.Attorney, tax.Rents},
		rule: principal.Rule{Code: "ca_part_xx", Reason: "\"The return must be electronically filed by January 31 of the year following the calendar " +
			"year in which the seller is identified as a reportable seller\", \"in prescribed form using an XML Schema\"; the first " +
			"reportable period was 2024 (CRA, Guidance on the Reporting Rules for Digital Platform Operators, ¶6.1, ¶6.3, ¶6.23)."}},
	"AU": {code: "au_serr", law: "Taxation Administration Act 1953 (Cth), Sch 1 s 396-55 item 15", form: "Sharing economy report",
		authority: "Australian Taxation Office",
		due: func(y int) string {
			return "31 days after the half-year the supply is made in (July 31 for January–June, January 31 for July–December)"
		},
		covers: []tax.Category{tax.Services, tax.Attorney, tax.Rents},
		rule: principal.Rule{Code: "au_serr", Reason: "An operator of an electronic distribution platform reports supplies made through it that " +
			"are connected with Australia, other than supplies of ownership of goods or real property and financial supplies (Sch 1 " +
			"s 396-55 item 15), for each six-month period from 1 January and 1 July, from 1 July 2024 for supplies other than taxi " +
			"travel and short-term accommodation (Taxation Administration (Reporting by Electronic Distribution Platform " +
			"Operators) Legislative Instrument 2023, ss 4–5), within 31 days after the period ends."}},
}

// platform names the platform-operator report the payer owes for this payee, if
// the payer is the operator of the platform the payee sells through.
func (d *decision) platform(c Case) {
	if c.Platform != nil && !*c.Platform {
		return
	}
	held := c.Held.Form
	if held == nil || c.Held.Status != "granted" {
		if c.Platform != nil {
			d.Facts = append(d.Facts, principal.Fact{Code: "seller_residence", Rule: rulePlatform,
				Question: "Where is the payee resident? Platform-operator reporting turns on the seller's residence, which the " +
					"payee's form states once it grants it."})
		}
		return
	}
	where := held.Residence
	key := where
	if slices.Contains(eu, where) {
		key = "EU"
	}
	r, ok := regimes[key]
	switch {
	case !ok && where != "US":
		if c.Platform != nil {
			d.Facts = append(d.Facts, principal.Fact{Code: "platform_regime", Rule: rulePlatform,
				Question: "Does " + where + " require a platform operator to report sellers resident there? Its rules are not in this table."})
		}
		return
	case !ok, !slices.Contains(r.covers, c.Category):
		return
	case c.Platform == nil:
		d.Facts = append(d.Facts, principal.Fact{Code: "platform", Rule: rulePlatform,
			Question: "Does the payer operate the platform through which the payee provides this to the platform's users? " +
				"If it does, it owes " + r.form + " under " + r.law + "."})
		return
	}
	d.Reporting = append(d.Reporting, principal.Obligation{Code: r.code, Form: r.form, Filer: c.Payer, Jurisdiction: key,
		Authority: r.authority, Due: r.due(c.Year), When: "as the operator of the platform the payee sells through", Rule: r.rule})
}

// settle decides whether the payment is cleared and how it may move.
func (d *decision) settle(c Case) {
	blocked := len(d.Required) > 0 || slices.ContainsFunc(d.Facts, func(f principal.Fact) bool { return f.Blocks })
	d.Allowed = !blocked
	if blocked {
		d.Rules = append(d.Rules, principal.Decided{Field: "allowed", Rule: ruleHeld}, principal.Decided{Field: "settlement_methods", Rule: ruleNone})
		return
	}
	d.Rules = append(d.Rules, principal.Decided{Field: "allowed", Rule: ruleAllowed}, principal.Decided{Field: "settlement_methods", Rule: ruleSettle})
	net, kept := usd(c.Cents-d.withheld), usd(d.withheld)
	d.Settlement = append(d.Settlement, principal.Method{Rail: client.RailLedger, Net: net, Withheld: kept, Rule: ruleSettle})
	if c.Wallets {
		d.Settlement = append(d.Settlement, principal.Method{Rail: client.RailChain, Net: net, Withheld: kept, Rule: ruleSettle})
	}
}

// StatutoryBps is the chapter 3 rate, in basis points.
const StatutoryBps = tax.StatutoryBps

func label(form string) string {
	switch form {
	case "w8ben":
		return "Form W-8BEN"
	case "w8bene":
		return "Form W-8BEN-E"
	}
	return "Form W-9"
}

// pct renders basis points as a percentage: 3000 is "30", 1250 is "12.5".
func pct(bps int) string {
	if bps%100 == 0 {
		return fmt.Sprint(bps / 100)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%02d", bps/100, bps%100), "0"), ".")
}

// usd renders cents as the decimal every amount on the wire is: "1234.50".
func usd(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}
