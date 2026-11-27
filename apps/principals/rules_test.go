package principals

// rules_test.go holds each rule of a clearance against the source it cites, on
// the pure decision: the facts go in, and the test reads what came out and which
// rule decided it.

import (
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/principal"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/apps/tax"
	"github.com/hanzoai/cloud/client"
)

// base is a payment that clears: two verified orgs, screened clear, the payer
// holding the payee's W-9 with its TIN, for services, on the ledger.
func base() Case {
	return Case{
		Payer: "acme", Payee: "globex", Cents: 100000, Category: tax.Services, Rail: client.RailLedger, Year: 2026,
		PayerID: iam.Identity{Status: idVerified}, PayeeID: iam.Identity{Status: idVerified},
		PayerScreen: iam.Screening{Status: screenClear}, PayeeScreen: iam.Screening{Status: screenClear},
		Held: client.PayeeForm{Status: "granted", Form: &client.TaxForm{Form: "w9", Name: "Globex", Classification: "individual",
			TIN: "***-**-6789", TINType: "ssn", Residence: "US", Country: "US", Valid: true}},
		Wallets: true,
	}
}

// foreignPayee is the payee on a W-8BEN-E the payer holds.
func foreignPayee(c Case, valid bool, treaty *iam.Treaty) Case {
	c.Payee = "wien"
	c.Held = client.PayeeForm{Status: "granted", Form: &client.TaxForm{Form: "w8bene", Name: "Wien Robotik GmbH",
		Country: "AT", Residence: "AT", Chapter3: "corporation", Chapter4: "active_nffe", ForeignTIN: "****5432",
		Certified: valid, Valid: valid, Treaty: treaty}}
	return c
}

func has[T any](xs []T, f func(T) bool) bool { return slices.ContainsFunc(xs, f) }

func step(code string) func(principal.Step) bool {
	return func(s principal.Step) bool { return s.Code == code }
}
func owes(code string) func(principal.Obligation) bool {
	return func(o principal.Obligation) bool { return o.Code == code }
}
func asks(code string) func(principal.Fact) bool {
	return func(f principal.Fact) bool { return f.Code == code }
}

func TestAPaymentThatClears(t *testing.T) {
	d := Decide(base())
	if !d.Allowed || len(d.Required) != 0 || d.Withholding.Rate != "0" || d.Withholding.Rule.Code != "tin_on_file" {
		t.Fatalf("decision = %+v, want cleared with nothing withheld", d)
	}
	if len(d.Settlement) != 2 || d.Settlement[0].Net != "1000.00" || d.Settlement[0].Withheld != "0.00" {
		t.Fatalf("settlement = %+v, want ledger and chain, net 1000.00", d.Settlement)
	}
	if !has(d.Reporting, owes("1099")) {
		t.Fatalf("services to an individual owe a 1099-NEC: %+v", d.Reporting)
	}
	for _, o := range d.Reporting {
		if o.Filer != "acme" {
			t.Fatalf("obligation %s names filer %q; every filer is the payer org, never Hanzo", o.Code, o.Filer)
		}
	}
	if !has(d.Rules, func(r principal.Decided) bool { return r.Field == "allowed" && r.Rule.Code == "cleared" }) {
		t.Fatalf("allowed carries no rule: %+v", d.Rules)
	}
}

// Payer's and payee's identity is verified before money moves.
func TestIdentityRequired(t *testing.T) {
	c := base()
	c.PayeeID = iam.Identity{Status: idPending}
	d := Decide(c)
	if d.Allowed || !has(d.Required, step("payee_identity")) || len(d.Settlement) != 0 {
		t.Fatalf("an unverified payee cleared: %+v", d)
	}
}

// A confirmed sanctions match blocks; a possible one waits on a platform reviewer;
// lists not fit to screen against clear nobody. Of the payee's verdict the payer
// reads only that the payment is held.
func TestSanctionsRules(t *testing.T) {
	for status, code := range map[string]string{
		screenBlocked: "payee_hold", screenReview: "payee_hold",
		screenUnavailable: "sanctions_unavailable", screenUnscreened: "sanctions_unscreened",
	} {
		c := base()
		c.PayeeScreen = iam.Screening{Status: status}
		d := Decide(c)
		if d.Allowed || !has(d.Required, step(code)) || len(d.Settlement) != 0 {
			t.Errorf("payee %s: decision = %+v, want not allowed with %s", status, d, code)
		}
	}
	c := base()
	c.PayerScreen = iam.Screening{Status: screenBlocked}
	if d := Decide(c); d.Allowed || !has(d.Required, step("sanctions_blocked")) {
		t.Fatal("a blocked payer cleared")
	}
	c = base()
	c.PayerScreen, c.PayeeScreen = iam.Screening{Status: screenUnavailable}, iam.Screening{Status: screenUnavailable}
	if d := Decide(c); len(d.Required) != 1 || d.Required[0].Code != "sanctions_unavailable" {
		t.Fatalf("lists unfit for payer and payee = %+v, want one step", d.Required)
	}
}

// A U.S. payee with no TIN on file: W-9 required, and 24% backup withholding
// (IRC §3406(a)(1)(A)).
func TestUSPayeeWithoutTIN(t *testing.T) {
	c := base()
	c.Held.Form.TIN, c.Held.Form.TINType, c.Held.Form.Valid = "", "", false
	d := Decide(c)
	if d.Status != statusUS || d.Allowed || !has(d.Required, step("w9")) {
		t.Fatalf("decision = %+v, want a U.S. payee owing its W-9", d)
	}
	w := d.Withholding
	if w.Chapter != "backup" || w.Rate != "24" || w.Amount != "240.00" || w.Rule.Code != "w9_tin" {
		t.Fatalf("withholding = %+v, want 24%% backup, 240.00", w)
	}
	if !has(d.Reporting, owes("945")) || !has(d.Reporting, owes("1099")) {
		t.Fatalf("reporting = %+v, want Form 945 for the backup withholding and the 1099", d.Reporting)
	}
}

// Payments to a corporation are not reported on Form 1099 — except for legal
// services — and so carry no backup withholding.
func TestCorporationExemptFrom1099(t *testing.T) {
	c := base()
	c.Held.Form.Classification = "c_corp"
	d := Decide(c)
	if has(d.Reporting, owes("1099")) || d.Withholding.Rate != "0" || !d.Allowed {
		t.Fatalf("a corporation = %+v, want no 1099 and nothing withheld", d)
	}
	if !has(d.Rules, func(r principal.Decided) bool {
		return r.Field == "reporting_obligations" && r.Rule.Code == "corporation"
	}) {
		t.Fatalf("the corporation exemption is not the rule named: %+v", d.Rules)
	}
	c.Category = tax.Attorney
	if d := Decide(c); !has(d.Reporting, owes("1099")) {
		t.Fatalf("legal services to a corporation owe a 1099-NEC: %+v", d.Reporting)
	}
}

// A W-8 the payer holds that is not valid documents nothing: the payee renews it,
// and nothing is withheld on a guess — no 30% on a presumption of foreign status,
// no treaty rate, no 1042-S (Treas. Reg. §1.1441-1(b)(3)(i), §1.1441-6(b)(1)).
func TestLapsedW8DocumentsNothing(t *testing.T) {
	c := foreignPayee(base(), false, &iam.Treaty{Country: "AT", Article: "12(2)", RateBps: 1000, Income: "royalties"})
	c.Category, c.Performed = tax.Royalties, "US"
	d := Decide(c)
	if d.Status != statusUnknown || d.Allowed || !has(d.Required, step("w8")) {
		t.Fatalf("decision = %+v, want the payee's status unknown until it renews its W-8", d)
	}
	if w := d.Withholding; w.Rate != "" || w.Rule.Code != "w8_lapsed" {
		t.Fatalf("withholding = %+v, want none decided on a lapsed form", w)
	}
	if len(d.Reporting) != 0 {
		t.Fatalf("reporting = %+v, want nothing named on a lapsed form", d.Reporting)
	}
}

// A payee whose form the payer does not hold is unknown to the payer, whatever
// the payee certified about itself.
func TestUndocumentedPayeeIsUnknown(t *testing.T) {
	c := base()
	c.Held, c.Performed, c.Category = client.PayeeForm{Status: "requested"}, "US", tax.Royalties
	d := Decide(c)
	if d.Status != statusUnknown || d.Withholding.Rate != "" || !has(d.Required, step("tax_form")) || has(d.Reporting, owes("1042-S")) {
		t.Fatalf("an undocumented payee = %+v, want its form required and nothing withheld on a guess", d)
	}
}

// The treaty rate a valid W-8 claims for this income is applied.
func TestTreatyRateOnAValidW8(t *testing.T) {
	c := foreignPayee(base(), true, &iam.Treaty{Country: "AT", Article: "12(2)", RateBps: 1000, Income: "royalties"})
	c.Category, c.Performed = tax.Royalties, "US"
	d := Decide(c)
	if w := d.Withholding; w.Rate != "10" || w.Amount != "100.00" || w.Rule.Code != "treaty_rate" {
		t.Fatalf("withholding = %+v, want the 10%% treaty rate", w)
	}
	if !d.Allowed || d.Settlement[0].Net != "900.00" || d.Settlement[0].Withheld != "100.00" {
		t.Fatalf("decision = %+v, want cleared at 900.00 net", d)
	}
	if !has(d.Reporting, owes("1042-S")) {
		t.Fatalf("a treaty-reduced payment is still reported on Form 1042-S: %+v", d.Reporting)
	}
	// A claim for another type of income reduces nothing.
	c.Category = tax.Rents
	if w := Decide(c).Withholding; w.Rate != "30" {
		t.Fatalf("a royalties claim applied to rents: %+v", w)
	}
	// An individual's treaty claim on personal services performed in the U.S. is Form 8233's.
	c = foreignPayee(base(), true, &iam.Treaty{Country: "AT", Article: "7", RateBps: 0, Income: "services"})
	c.Held.Form.Form, c.Performed = "w8ben", "US"
	d = Decide(c)
	if d.Withholding.Rate != "30" || d.Withholding.Rule.Code != "form_8233" || !has(d.Facts, asks("form_8233")) {
		t.Fatalf("a W-8BEN services claim = %+v, want 30%% pending Form 8233", d)
	}
}

// A service performed outside the United States is foreign-source income:
// nothing is withheld under chapter 3 and no 1042-S is owed.
func TestServicePerformedAbroad(t *testing.T) {
	c := foreignPayee(base(), true, nil)
	c.Performed = "AT"
	d := Decide(c)
	if w := d.Withholding; w.Rate != "0" || w.Rule.Code != "foreign_source" {
		t.Fatalf("withholding = %+v, want nothing under the foreign-source rule", w)
	}
	if has(d.Reporting, owes("1042-S")) || has(d.Reporting, owes("1099")) || !d.Allowed {
		t.Fatalf("decision = %+v, want cleared with no 1042-S and no 1099", d)
	}
	// Where it is performed is a fact the rule turns on: unstated, it blocks.
	c.Performed = ""
	if d := Decide(c); d.Allowed || !has(d.Facts, asks("performed")) || d.Withholding.Rate != "" {
		t.Fatalf("an unstated place of performance = %+v, want a blocking fact and no guessed rate", d)
	}
}

// Platform-operator reporting names the payer as filer, and only when it is the
// payee's platform.
func TestPlatformReporting(t *testing.T) {
	yes, no := true, false
	c := foreignPayee(base(), true, nil)
	c.Performed = "AT"
	d := Decide(c)
	if has(d.Reporting, owes("dac7")) || !has(d.Facts, asks("platform")) {
		t.Fatalf("unstated platform = %+v, want the question and no guessed obligation", d)
	}
	c.Platform = &yes
	d = Decide(c)
	i := slices.IndexFunc(d.Reporting, owes("dac7"))
	if i < 0 {
		t.Fatalf("an EU-resident seller on the payer's platform owes a DAC7 report: %+v", d.Reporting)
	}
	if o := d.Reporting[i]; o.Filer != "acme" || o.Jurisdiction != "EU" || o.Due != "January 31, 2027" {
		t.Fatalf("DAC7 obligation = %+v, want filed by acme with the EU, by January 31", o)
	}
	c.Platform = &no
	if d := Decide(c); has(d.Reporting, owes("dac7")) {
		t.Fatal("a payer that is not the payee's platform owes a DAC7 report")
	}
	// Royalties are not a relevant activity.
	c.Platform, c.Category = &yes, tax.Royalties
	if d := Decide(c); has(d.Reporting, owes("dac7")) {
		t.Fatal("royalties are not a DAC7 relevant activity")
	}
	// The UK's regulations name HMRC.
	c = foreignPayee(base(), true, nil)
	c.Held.Form.Residence, c.Performed, c.Platform = "GB", "GB", &yes
	if d := Decide(c); !has(d.Reporting, owes("uk_mrdp")) {
		t.Fatalf("a UK-resident seller = %+v, want the UK report", d.Reporting)
	}
}

// Status unknown: the form is required, and nothing is guessed.
func TestUnknownStatus(t *testing.T) {
	c := base()
	c.Held = client.PayeeForm{Status: "none"}
	d := Decide(c)
	if d.Status != statusUnknown || d.Allowed || !has(d.Required, step("tax_form")) || !has(d.Facts, asks("us_person")) || d.Withholding.Rate != "" {
		t.Fatalf("decision = %+v, want the form required, a blocking fact, and no rate", d)
	}
	c.Category = tax.Merchandise
	if d := Decide(c); has(d.Required, step("tax_form")) || d.Withholding.Rule.Code != "goods" {
		t.Fatalf("goods = %+v, want no form required and nothing withheld", d)
	}
}

// Every field of every decision names the rule that decided it.
func TestEveryFieldNamesItsRule(t *testing.T) {
	cases := []Case{base()}
	c := base()
	c.Held = client.PayeeForm{Status: "none"}
	cases = append(cases, c)
	c = foreignPayee(base(), true, &iam.Treaty{Country: "AT", Article: "12(2)", RateBps: 1000, Income: "royalties"})
	c.Category, c.Performed = tax.Royalties, "US"
	cases = append(cases, c)
	for i, c := range cases {
		d := Decide(c)
		for _, f := range []string{"status", "withholding", "allowed", "settlement_methods"} {
			if !has(d.Rules, func(r principal.Decided) bool { return r.Field == f && r.Rule.Code != "" && r.Rule.Reason != "" }) {
				t.Errorf("case %d: %s names no rule", i, f)
			}
		}
		for _, s := range d.Required {
			if s.Rule.Code == "" || s.Rule.Reason == "" {
				t.Errorf("case %d: step %s names no rule", i, s.Code)
			}
		}
		for _, o := range d.Reporting {
			if o.Rule.Code == "" || o.Rule.Reason == "" || o.Filer != c.Payer {
				t.Errorf("case %d: obligation %s = %+v", i, o.Code, o)
			}
		}
	}
}
