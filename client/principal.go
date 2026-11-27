package client

// principal.go is what /v1/principal asks of the apps that own an org's economic
// facts.
//
// An org is an economic principal, and no one app holds all of it: company holds
// the legal entity and its founders' identity checks, tax holds the forms the org
// certified, wallet holds its addresses, agents holds the agents that act for it.
// Each op below answers for the CALLER's tenant only — the org rides the caller,
// and no input here carries an Org or Owner field — and answers facts, never a
// verdict: whether a payment may proceed is the reader's rule to apply, and an
// owner that pre-decided would be a second compliance engine that disagrees with
// the first the day a rule changes.
//
// The one input that names another org, TaxPayeeIn.Payee, names the other side
// of the CALLER's own relationship, and is answered only with what that payee
// granted the caller.

import "github.com/hanzoai/cloud/sdk/iam"

const (
	// CompanyEntity reads the caller's legal entity: the formation, and each
	// founder's identity-verification status.
	CompanyEntity = "company_entity"
	// TaxStanding reads the tax form the caller certified about itself: a W-9, a
	// W-8BEN or a W-8BEN-E, every identifying number masked.
	TaxStanding = "tax_standing"
	// TaxPayee reads what the caller, as a payer, holds for one payee: where its
	// form request stands, and the payee's form under a live grant.
	TaxPayee = "tax_payee"
	// WalletsList reads the caller's wallets: addresses, never key material.
	WalletsList = "wallets_list"
	// AgentsLineage reads one of the caller's agents and the chain of agents that
	// spawned it.
	AgentsLineage = "agents_lineage"
	// PrincipalIntent reads whether the caller asked /v1/principal whether it may
	// pay a payee — the evidence tax accepts, beside a payment already made, that
	// the caller is that payee's payer.
	PrincipalIntent = "principal_intent"
	// PrincipalClear decides whether the caller may pay a payee, before the
	// payment — the same decision POST /v1/principal/clearance records, answered
	// as the verdict a payer that must act on it needs.
	PrincipalClear = "principal_clear"
	// PrincipalStanding reads the caller's org as a principal, as far as the org
	// may see about itself: its identity verification, its own screening status,
	// and the statements the platform signed for its agents.
	PrincipalStanding = "principal_standing"
	// PrincipalStatements brings the statements signed for the caller's agents in
	// line with the tax form the caller certified: one for each agent while the
	// form is valid, none once it is not. Tax calls it when a certification
	// completes or a form changes; it asks tax and agents for the facts itself.
	PrincipalStatements = "principal_statements"
	// TaxInbox lists the Copy B statements furnished to the caller: the 1099s it
	// received.
	TaxInbox = "tax_inbox"
)

// EntityIn asks company for the caller's own entity. It carries nothing.
type EntityIn struct{}

// Entity is an org's legal entity as company formed or imported it.
type Entity struct {
	// Found is false when the org never began a formation.
	Found bool `json:"found"`
	// Name is the company name the entity is formed under.
	Name string `json:"name,omitempty"`
	// Structure is c-corp, llc or dao-llc.
	Structure string `json:"structure,omitempty"`
	// Jurisdiction is the U.S. state of formation: DE or WY.
	Jurisdiction string `json:"jurisdiction,omitempty"`
	// Stage is the formation's state; "company" is formed.
	Stage string `json:"stage,omitempty"`
	// Imported is true for an entity that existed before the org and was
	// imported rather than formed here.
	Imported bool `json:"imported,omitempty"`
	// Founders are the founding stakeholders — the beneficial owners the
	// formation identified — with each one's verification status.
	Founders []iam.Founder `json:"founders"`
}

// StandingIn asks tax for the caller's own form. It carries nothing.
type StandingIn struct{}

// TaxPayeeIn names the payee whose form the caller, as payer, holds.
type TaxPayeeIn struct {
	Payee string `json:"payee" validate:"required"`
}

// PayeeForm is what a payer holds for one payee.
type PayeeForm struct {
	// Status is where the payer's request for the payee's form stands: none,
	// requested, granted, declined or revoked.
	Status string `json:"status"`
	// Form is the payee's form — present only while the grant is live.
	Form *TaxForm `json:"form,omitempty"`
}

// TaxForm is one certificate of U.S. or foreign status, as tax holds it.
// Identifying numbers are masked to their last four characters; the full numbers
// never cross this plane.
type TaxForm struct {
	// Form is w9, w8ben or w8bene.
	Form string `json:"form"`
	// Name is line 1 of the form.
	Name string `json:"name"`
	// BusinessName is W-9 line 2.
	BusinessName string `json:"businessName,omitempty"`
	// Country is where the person is: US for a W-9, the country of citizenship
	// (W-8BEN) or of incorporation (W-8BEN-E) otherwise. ISO 3166-1 alpha-2.
	Country string `json:"country"`
	// Residence is the country of the permanent residence address.
	Residence string `json:"residence"`
	// Address is the address on the form, one line.
	Address string `json:"address,omitempty"`
	// Birth is a W-8BEN individual's date of birth (line 8), YYYY-MM-DD.
	Birth string `json:"birth,omitempty"`
	// Classification is W-9 line 3a.
	Classification string `json:"classification,omitempty"`
	// ExemptPayee is W-9 line 4's exempt payee code.
	ExemptPayee string `json:"exemptPayee,omitempty"`
	// Chapter3 is W-8BEN-E line 4, or "individual" for a W-8BEN.
	Chapter3 string `json:"chapter3,omitempty"`
	// Chapter4 is W-8BEN-E line 5, the FATCA status.
	Chapter4 string `json:"chapter4,omitempty"`
	// TIN is the U.S. TIN, masked; empty when none is on the form.
	TIN string `json:"tin,omitempty"`
	// TINType is ssn (an SSN or ITIN) or ein, when a U.S. TIN is on the form.
	TINType string `json:"tinType,omitempty"`
	// ForeignTIN is the foreign TIN, masked; empty when none is on the form.
	ForeignTIN string `json:"foreignTin,omitempty"`
	// Certified is whether the signature covers the form's current version.
	Certified bool `json:"certified"`
	// Signed is when it was signed, unix seconds.
	Signed int64 `json:"signed,omitempty"`
	// Expires is when a W-8 stops being valid, unix seconds; zero for a W-9,
	// which does not expire.
	Expires int64 `json:"expires,omitempty"`
	// Valid is whether the form establishes what it certifies today: certified
	// for its current version and, for a W-8, not expired.
	Valid bool `json:"valid"`
	// Treaty is a W-8's treaty claim, when it makes one.
	Treaty *iam.Treaty `json:"treaty,omitempty"`
}

// WalletsIn asks wallet for the caller's wallets. It carries nothing.
type WalletsIn struct{}

// Wallets are the caller's wallets.
type Wallets struct {
	Wallets []iam.WalletBrief `json:"wallets"`
}

// LineageIn names one of the caller's agents: its id or its name.
type LineageIn struct {
	Ref string `json:"ref" validate:"required"`
}

// Lineage is one agent and the agents that spawned it, nearest first. Every
// agent in it is the caller's: an agent is spawned inside the org of the agent
// that spawns it, and no other.
type Lineage struct {
	Agent   iam.AgentBrief   `json:"agent"`
	Parents []iam.AgentBrief `json:"parents"`
}

// IntentIn names a payee.
type IntentIn struct {
	Payee string `json:"payee" validate:"required"`
}

// Intent says whether the caller asked to pay the payee.
type Intent struct {
	Asked bool `json:"asked"`
}

// ClearIn is a payment the caller means to make.
type ClearIn struct {
	// Payee is the org to be paid.
	Payee string `json:"payee" validate:"required"`
	// Amount is the gross amount in U.S. dollars, in whole cents: "1250.00".
	Amount string `json:"amount" validate:"required"`
	// Category is what the payment is for, in the words the economic event says
	// it in (economic.go): service, goods, transfer or royalty. The rules decide
	// what that is for tax; service when empty.
	Category string `json:"category,omitempty"`
	// Rail is how it will move, one of Rails; ledger when empty.
	Rail string `json:"rail,omitempty"`
	// Performed is where the work is performed, ISO 3166-1 alpha-2.
	Performed string `json:"performed,omitempty"`
	// Again is the clearance this one asks again: a payment already agreed on it,
	// cleared once more before its money moves, as a job's funding and release ask
	// again of the clearance its quote was given. It must be the payer's own, to the
	// same payee. The payer's hourly bound counts payments, so an ask again counts
	// against the payment it repeats — at most twelve an hour — and not the payer.
	Again string `json:"again,omitempty"`
}

// The verdicts a clearance comes to, for a payer that branches on it.
const (
	// ClearCleared: nothing is required first; the payment may settle for Net.
	ClearCleared = "cleared"
	// ClearNeedsForm: the payee must furnish its tax form (W-9 or W-8) to the
	// payer first, and nothing else stands in the way.
	ClearNeedsForm = "needs_form"
	// ClearBlocked: the payment cannot clear now, for a reason the payer is told
	// only as the steps it names.
	ClearBlocked = "blocked"
)

// Cleared is one recorded clearance, as the payer acts on it. The whole decision
// — every rule, obligation and fact — is GET /v1/principal/clearance/{id}.
type Cleared struct {
	// ID is the clearance, "clr_"-prefixed.
	ID string `json:"id"`
	// Verdict is cleared, needs_form or blocked.
	Verdict string `json:"verdict"`
	// Required are the codes of the steps required before the payment.
	Required []string `json:"required"`
	// Net is what the payee receives on the rail asked about, U.S. dollars; empty
	// unless cleared.
	Net string `json:"net,omitempty"`
	// Withheld is what the payer keeps back and deposits itself, U.S. dollars.
	Withheld string `json:"withheld,omitempty"`
}

// PrincipalIn asks for the caller's own standing. It carries nothing.
type PrincipalIn struct{}

// Standing is the caller's org as a principal, as far as the org may see about
// itself. It names no match and no list entry: those are its admins' to read at
// GET /v1/principal, and a designation's reviewer's to decide.
type Standing struct {
	// Identity is the founders' identity verification: verified, pending, failed
	// or none.
	Identity string `json:"identity"`
	// Entity is the org's legal entity's formation stage — "company" once formed —
	// or empty when the org began none.
	Entity string `json:"entity,omitempty"`
	// Sanctions is the org's own screening: clear, review, blocked, unscreened or
	// unavailable.
	Sanctions string `json:"sanctions"`
	// Reason says, in words, why Sanctions is what it is.
	Reason string `json:"reason"`
	// Statements are the statements signed for the org's agents, newest first.
	Statements []Statement `json:"statements"`
}

// The kinds of statement the platform signs about an agent.
const (
	// StatementTax is a TaxPrincipalCredential: the agent's principal holds a
	// certified, valid tax form of the class it names.
	StatementTax = "tax"
)

// Statement is one statement the platform signed about an agent, and whether it
// still stands.
type Statement struct {
	// ID is the statement's jti.
	ID string `json:"id"`
	// Agent is the agent it is about.
	Agent string `json:"agent"`
	// Org is the principal the agent answers to.
	Org string `json:"org"`
	// Kind is what it states: tax.
	Kind string `json:"kind"`
	// Form is the class of tax form it rests on: w9, w8ben or w8bene.
	Form string `json:"form,omitempty"`
	// Issued is when it was signed, unix seconds.
	Issued int64 `json:"issued"`
	// Expires is when it stops standing, unix seconds; zero for a W-9, which
	// stands until the form changes.
	Expires int64 `json:"expires,omitempty"`
	// Revoked is when it was withdrawn, unix seconds; zero while it stands.
	Revoked int64 `json:"revoked,omitempty"`
	// Status is live, expired or revoked.
	Status string `json:"status"`
	// JWS is the signed statement itself — the credential an agent presents —
	// answered only to the org it is about.
	JWS string `json:"jws,omitempty"`
}

// StatementsIn asks principal to bring the caller's statements in line with its
// form. It carries nothing: the org rides the caller, and the facts are asked of
// their owners.
type StatementsIn struct{}

// Reconciled says what one reconcile changed.
type Reconciled struct {
	Issued  int `json:"issued"`
	Revoked int `json:"revoked"`
}

// InboxIn narrows the caller's inbox to one year; zero is every year.
type InboxIn struct {
	Year int `json:"year,omitempty"`
}

// Inbox is the 1099s furnished to the caller, oldest first.
type Inbox struct {
	Received []Received `json:"received"`
}

// Received is one Copy B furnished to the caller, without its figures.
type Received struct {
	// ID is the form id the payer issued it under.
	ID string `json:"id"`
	// Payer is the org that furnished it.
	Payer string `json:"payer"`
	// Kind is 1099-NEC or 1099-MISC.
	Kind string `json:"kind"`
	// Year is the calendar year of the payments.
	Year int `json:"year"`
	// Corrected is true for a statement that corrects an earlier one.
	Corrected bool `json:"corrected,omitempty"`
	// Superseded is true for one a later correction replaced.
	Superseded bool `json:"superseded,omitempty"`
	// Furnished is when it was delivered, unix seconds.
	Furnished int64 `json:"furnished"`
}
