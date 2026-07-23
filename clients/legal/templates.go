package legal

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"
)

// templates.go is the PURE generation engine plus the built-in standardized library.
// Render is deterministic — it takes a Template and a merge-field map and returns the
// exact same bytes every time (no clock, no I/O, no randomness). Any date is a merge
// FIELD supplied by the caller, never time.Now, so a rendered contract is reproducible
// and its production is auditable.

// MissingFields returns the declared fields absent (or blank) in data, sorted. The
// engine fails closed on any missing field rather than rendering a blank into a legal
// document — a silent blank in a contract is a defect, not a convenience.
func MissingFields(t Template, data map[string]string) []string {
	var missing []string
	for _, f := range t.Fields {
		if strings.TrimSpace(data[f.Key]) == "" {
			missing = append(missing, f.Key)
		}
	}
	sort.Strings(missing)
	return missing
}

// Render executes a template against its merge data and returns the rendered document
// bytes. It fails closed on a missing required field (before AND during execution via
// missingkey=error). The CounselNotice is prepended whenever the template is
// CounselReview OR its category REQUIRES counsel review (formation/equity securities),
// so the boundary rides on the document itself — the engine cannot emit a
// securities-class document without it, regardless of how the flag was set.
func Render(t Template, data map[string]string) ([]byte, error) {
	if missing := MissingFields(t, data); len(missing) > 0 {
		return nil, fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
	}
	tmpl, err := template.New(t.ID).Option("missingkey=error").Parse(t.Body)
	if err != nil {
		return nil, fmt.Errorf("parse template %q: %w", t.ID, err)
	}
	var buf bytes.Buffer
	if t.CounselReview || counselRequired(t.Category) {
		buf.WriteString(CounselNotice)
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render template %q: %w", t.ID, err)
	}
	return buf.Bytes(), nil
}

// ValidateOverride reports whether t is a storable override: its body PARSES and every
// merge field the body references is DECLARED in t.Fields. An undeclared reference is
// refused because the fail-closed missing-field check (MissingFields) only inspects
// DECLARED fields and missingkey=error only catches ABSENT keys — so an undeclared
// field passed as an empty string would otherwise render a silent blank into a legal
// document. Requiring body refs ⊆ declared fields makes MissingFields authoritative.
func ValidateOverride(t Template) error {
	refs, err := bodyFieldRefs(t.Body)
	if err != nil {
		return fmt.Errorf("template does not parse: %w", err)
	}
	declared := make(map[string]bool, len(t.Fields))
	for _, f := range t.Fields {
		declared[f.Key] = true
	}
	var undeclared []string
	for ref := range refs {
		if !declared[ref] {
			undeclared = append(undeclared, ref)
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		return fmt.Errorf("template body references undeclared fields: %s", strings.Join(undeclared, ", "))
	}
	return nil
}

// bodyFieldRefs returns the set of top-level merge fields a template body references
// ({{.key}} directly, or inside an if/range/with control or a pipeline). It parses the
// body and walks the parse tree, so it sees exactly what execution would substitute.
func bodyFieldRefs(body string) (map[string]bool, error) {
	tmpl, err := template.New("").Option("missingkey=error").Parse(body)
	if err != nil {
		return nil, err
	}
	refs := map[string]bool{}
	for _, t := range tmpl.Templates() {
		if t.Tree != nil {
			collectFieldRefs(t.Tree.Root, refs)
		}
	}
	return refs, nil
}

// collectFieldRefs walks a parse tree, recording the leading identifier of every field
// access ({{.key ...}} → "key"). It descends into the control nodes an override body
// may legitimately use (if/range/with, plus embedded template invocations).
func collectFieldRefs(n parse.Node, refs map[string]bool) {
	switch v := n.(type) {
	case *parse.ListNode:
		if v == nil {
			return
		}
		for _, c := range v.Nodes {
			collectFieldRefs(c, refs)
		}
	case *parse.ActionNode:
		collectPipeRefs(v.Pipe, refs)
	case *parse.IfNode:
		collectPipeRefs(v.Pipe, refs)
		collectFieldRefs(v.List, refs)
		collectFieldRefs(v.ElseList, refs)
	case *parse.RangeNode:
		collectPipeRefs(v.Pipe, refs)
		collectFieldRefs(v.List, refs)
		collectFieldRefs(v.ElseList, refs)
	case *parse.WithNode:
		collectPipeRefs(v.Pipe, refs)
		collectFieldRefs(v.List, refs)
		collectFieldRefs(v.ElseList, refs)
	case *parse.TemplateNode:
		collectPipeRefs(v.Pipe, refs)
	}
}

// collectPipeRefs records field refs from every argument of every command in a
// pipeline, descending into nested pipelines (e.g. {{.a | printf .b}}).
func collectPipeRefs(p *parse.PipeNode, refs map[string]bool) {
	if p == nil {
		return
	}
	for _, cmd := range p.Cmds {
		for _, arg := range cmd.Args {
			switch a := arg.(type) {
			case *parse.FieldNode:
				if len(a.Ident) > 0 {
					refs[a.Ident[0]] = true
				}
			case *parse.PipeNode:
				collectPipeRefs(a, refs)
			}
		}
	}
}

// field is a terse constructor for the library below.
func field(key, label string) Field { return Field{Key: key, Label: label} }

// builtins is the standardized template library. Each is Version 1, Origin "builtin".
// The set spans every category; an org overrides any of them with its own body
// (store.go), and the resolved catalog is override-or-builtin. Formation and equity
// instruments are CounselReview. This is a representative production set; new
// templates are DATA added here, not engine changes.
var builtins = []Template{
	// ---- ops ----
	{
		ID: "nda", Category: CategoryOps, Title: "Mutual Non-Disclosure Agreement", Version: 1, Origin: "builtin",
		Fields: []Field{field("effective_date", "Effective date"), field("company_name", "Company"), field("counterparty_name", "Counterparty"), field("governing_law", "Governing law (state)")},
		Body: `# Mutual Non-Disclosure Agreement

This Mutual Non-Disclosure Agreement is entered into as of {{.effective_date}} between **{{.company_name}}** and **{{.counterparty_name}}** (each a "Party").

## 1. Confidential Information
Each Party may disclose confidential business, technical, or financial information to the other. The receiving Party shall use it solely to evaluate or pursue the parties' business relationship.

## 2. Obligations
The receiving Party shall (a) protect the disclosing Party's Confidential Information with at least reasonable care, and (b) not disclose it to third parties without prior written consent.

## 3. Exclusions
Confidential Information does not include information that is public, independently developed, or rightfully received from a third party.

## 4. Term
This Agreement remains in effect for three (3) years from the Effective Date.

## 5. Governing Law
This Agreement is governed by the laws of the State of {{.governing_law}}.
`,
	},
	{
		ID: "msa", Category: CategoryOps, Title: "Master Services Agreement", Version: 1, Origin: "builtin",
		Fields: []Field{field("effective_date", "Effective date"), field("company_name", "Company"), field("client_name", "Client"), field("payment_terms", "Payment terms"), field("governing_law", "Governing law (state)")},
		Body: `# Master Services Agreement

This Master Services Agreement is effective {{.effective_date}} between **{{.company_name}}** ("Provider") and **{{.client_name}}** ("Client").

## 1. Services
Provider will perform the services described in one or more Statements of Work executed under this Agreement.

## 2. Fees and Payment
Client will pay the fees stated in each Statement of Work. {{.payment_terms}}

## 3. Intellectual Property
Deliverables are assigned to Client upon full payment; Provider retains its pre-existing and general-purpose materials.

## 4. Confidentiality
Each party protects the other's confidential information consistent with the parties' non-disclosure obligations.

## 5. Limitation of Liability
Except for breaches of confidentiality or IP, neither party's liability exceeds the fees paid under the applicable Statement of Work.

## 6. Governing Law
This Agreement is governed by the laws of the State of {{.governing_law}}.
`,
	},
	{
		ID: "offer-letter", Category: CategoryOps, Title: "Employment Offer Letter", Version: 1, Origin: "builtin",
		Fields: []Field{field("date", "Date"), field("company_name", "Company"), field("candidate_name", "Candidate"), field("role_title", "Role"), field("annual_salary", "Annual salary"), field("start_date", "Start date"), field("equity_grant", "Equity grant")},
		Body: `# Offer of Employment — {{.company_name}}

{{.date}}

Dear {{.candidate_name}},

We are pleased to offer you the position of **{{.role_title}}** at {{.company_name}}.

- **Base salary:** {{.annual_salary}} per year, paid on the company's regular schedule.
- **Equity:** {{.equity_grant}}, subject to board approval and the company's equity plan.
- **Start date:** {{.start_date}}.

Your employment is at-will. This offer is contingent on your authorization to work and on signing the company's Confidential Information and Invention Assignment Agreement.

Sincerely,
{{.company_name}}
`,
	},
	// ---- equity ----
	{
		ID: "safe", Category: CategoryEquity, Title: "SAFE (Simple Agreement for Future Equity)", Version: 1, Origin: "builtin", CounselReview: true,
		Fields: []Field{field("date", "Date"), field("company_name", "Company"), field("investor_name", "Investor"), field("purchase_amount", "Purchase amount"), field("valuation_cap", "Valuation cap"), field("discount_rate", "Discount rate"), field("governing_law", "Governing law (state)")},
		Body: `# Simple Agreement for Future Equity

**{{.company_name}}** (the "Company") and **{{.investor_name}}** (the "Investor"), dated {{.date}}.

In exchange for the payment of **{{.purchase_amount}}** (the "Purchase Amount"), the Company issues the Investor the right to certain shares of the Company's capital stock, subject to the terms below.

## 1. Conversion
On an Equity Financing, this SAFE converts into shares at the lower of (a) the price implied by the **{{.valuation_cap}}** post-money valuation cap, or (b) a **{{.discount_rate}}** discount to the round price.

## 2. Liquidity / Dissolution
On a Liquidity Event or Dissolution before conversion, the Investor is entitled to the Purchase Amount, with the priority stated in the Company's standard SAFE terms.

## 3. Governing Law
This instrument is governed by the laws of the State of {{.governing_law}}. It is a security and is not a debt instrument.
`,
	},
	{
		ID: "subscription-agreement", Category: CategoryEquity, Title: "Securities Subscription Agreement", Version: 1, Origin: "builtin", CounselReview: true,
		Fields: []Field{field("date", "Date"), field("company_name", "Company"), field("investor_name", "Investor"), field("security_type", "Security"), field("subscription_amount", "Subscription amount"), field("share_count", "Number of shares"), field("governing_law", "Governing law (state)")},
		Body: `# Subscription Agreement

Dated {{.date}}, between **{{.company_name}}** (the "Company") and **{{.investor_name}}** (the "Investor").

## 1. Subscription
The Investor subscribes for **{{.share_count}}** shares of the Company's {{.security_type}} for an aggregate purchase price of **{{.subscription_amount}}**.

## 2. Investor Representations
The Investor represents that it is acquiring the securities for its own account, for investment, and understands the securities are not registered and are subject to transfer restrictions.

## 3. Company Representations
The Company is duly organized and has authorized the issuance of the securities under this Agreement.

## 4. Governing Law
This Agreement is governed by the laws of the State of {{.governing_law}}.
`,
	},
	{
		ID: "option-grant", Category: CategoryEquity, Title: "Stock Option Grant Notice", Version: 1, Origin: "builtin", CounselReview: true,
		Fields: []Field{field("date", "Date"), field("company_name", "Company"), field("optionholder_name", "Optionholder"), field("shares", "Shares"), field("exercise_price", "Exercise price per share"), field("vesting_schedule", "Vesting schedule")},
		Body: `# Stock Option Grant Notice — {{.company_name}}

Dated {{.date}}. Granted to **{{.optionholder_name}}** (the "Optionholder") under the Company's Equity Incentive Plan.

- **Shares subject to option:** {{.shares}}
- **Exercise price per share:** {{.exercise_price}}
- **Vesting:** {{.vesting_schedule}}

This option is subject to the terms of the Plan and the Company's standard Stock Option Agreement, which govern in the event of any conflict. This notice is not tax advice; consult your advisor regarding the tax treatment of the grant and any early-exercise election.
`,
	},
	// ---- formation-adjacent ----
	{
		ID: "ip-assignment", Category: CategoryFormation, Title: "Confidential Information and Invention Assignment Agreement", Version: 1, Origin: "builtin", CounselReview: true,
		Fields: []Field{field("date", "Date"), field("company_name", "Company"), field("assignor_name", "Assignor"), field("governing_law", "Governing law (state)")},
		Body: `# Confidential Information and Invention Assignment Agreement

Dated {{.date}}, between **{{.company_name}}** (the "Company") and **{{.assignor_name}}** (the "Assignor").

## 1. Assignment of Inventions
The Assignor assigns to the Company all right, title, and interest in inventions, works, and intellectual property conceived or made within the scope of, or using the resources of, the Company.

## 2. Confidentiality
The Assignor will hold the Company's confidential information in confidence and use it solely for the Company's benefit.

## 3. Prior Inventions
Inventions made before the relationship and listed in an appendix are excluded from this assignment.

## 4. Governing Law
This Agreement is governed by the laws of the State of {{.governing_law}}.
`,
	},
	{
		ID: "83b-election", Category: CategoryFormation, Title: "Section 83(b) Election", Version: 1, Origin: "builtin", CounselReview: true,
		Fields: []Field{field("taxpayer_name", "Taxpayer"), field("taxpayer_address", "Address"), field("taxpayer_tin", "Taxpayer ID"), field("property_description", "Property"), field("transfer_date", "Date of transfer"), field("fair_market_value", "Fair market value"), field("amount_paid", "Amount paid"), field("taxable_year", "Taxable year")},
		Body: `# Election Under Section 83(b) of the Internal Revenue Code

The undersigned taxpayer elects, under Section 83(b) of the Internal Revenue Code, to include in gross income the excess (if any) of the fair market value of the property described below over the amount paid for it.

1. **Taxpayer:** {{.taxpayer_name}}, {{.taxpayer_address}}, Taxpayer ID {{.taxpayer_tin}}.
2. **Property:** {{.property_description}}.
3. **Date of transfer:** {{.transfer_date}}. **Taxable year:** {{.taxable_year}}.
4. **Fair market value at transfer:** {{.fair_market_value}}. **Amount paid:** {{.amount_paid}}.
5. The taxpayer will file this election with the IRS within 30 days of the transfer and furnish a copy to the company.

This election must be filed within 30 days of the transfer; the 30-day deadline is statutory and cannot be extended. Confirm filing details with your tax advisor.
`,
	},
	// ---- sales ----
	{
		ID: "tos", Category: CategorySales, Title: "Terms of Service", Version: 1, Origin: "builtin",
		Fields: []Field{field("effective_date", "Effective date"), field("company_name", "Company"), field("service_name", "Service"), field("governing_law", "Governing law (state)"), field("contact_email", "Contact email")},
		Body: `# Terms of Service

Effective {{.effective_date}}. These Terms govern your use of **{{.service_name}}**, operated by **{{.company_name}}**.

## 1. Use of the Service
You may use the Service in compliance with these Terms and applicable law. You are responsible for your account and content.

## 2. Fees
Paid features are billed as described at purchase. Fees are non-refundable except as required by law.

## 3. Disclaimer
The Service is provided "as is" without warranties to the extent permitted by law.

## 4. Limitation of Liability
To the extent permitted by law, {{.company_name}}'s liability is limited to the amounts you paid in the twelve months before the claim.

## 5. Governing Law and Contact
These Terms are governed by the laws of the State of {{.governing_law}}. Contact: {{.contact_email}}.
`,
	},
	{
		ID: "dpa", Category: CategorySales, Title: "Data Processing Addendum", Version: 1, Origin: "builtin",
		Fields: []Field{field("effective_date", "Effective date"), field("company_name", "Processor"), field("customer_name", "Controller"), field("processing_purpose", "Purpose of processing"), field("governing_law", "Governing law (state)")},
		Body: `# Data Processing Addendum

Effective {{.effective_date}}, between **{{.customer_name}}** ("Controller") and **{{.company_name}}** ("Processor").

## 1. Scope
The Processor processes personal data on the Controller's behalf solely for: {{.processing_purpose}}.

## 2. Processor Obligations
The Processor will (a) process personal data only on documented instructions, (b) ensure persons authorized to process it are bound by confidentiality, and (c) implement appropriate technical and organizational security measures.

## 3. Sub-processors
The Processor may engage sub-processors under written terms no less protective than this Addendum, and remains responsible for their performance.

## 4. Data Subject Rights and Breach
The Processor will assist the Controller in responding to data-subject requests and will notify the Controller without undue delay after becoming aware of a personal-data breach.

## 5. Governing Law
This Addendum is governed by the laws of the State of {{.governing_law}}.
`,
	},
}

// builtinByID indexes the library for O(1) resolution.
var builtinByID = func() map[string]Template {
	m := make(map[string]Template, len(builtins))
	for _, t := range builtins {
		m[t.ID] = t
	}
	return m
}()

// Builtins returns a copy of the standardized library (stable order by id).
func Builtins() []Template {
	out := make([]Template, len(builtins))
	copy(out, builtins)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// builtin returns the built-in template by id, or false.
func builtin(id string) (Template, bool) {
	t, ok := builtinByID[id]
	return t, ok
}
