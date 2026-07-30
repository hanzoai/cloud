package captable

// typed.go is captable's TYPED read plane — the ops that carry In/Out types, and
// therefore the only captable routes that reach the published document, the MCP
// tool list, the CLI and the generated SDKs. An untyped route contributes a path
// and a method and nothing else.
//
// WHY ONLY THE READS. Every /v1/captable/* route relays the goja bundle's own
// (status, body) verbatim — including the four non-2xx envelopes the BUNDLE
// authors: 400 {success,message,errors} for a validation failure, 404/409
// {success,message}, and the top-level catch's 500. A zip typed op cannot express
// that: its failure path renders zip's {status,code,error} envelope, and its
// success path declares exactly ONE 2xx. So a route whose bundle handler can
// answer a non-2xx CANNOT be typed without moving the wire, and stays untyped
// with the reason written at its registration in captable.go.
//
// The eleven ops below are the routes whose bundle handler has NO reachable
// non-2xx branch: it ends in okRes(...) on every path, so 200 is the only answer
// and the body is exactly the shape modelled here. (getCompany and capTable each
// carry a defensive notFound("company not found"); it is DEAD by construction —
// seedCompany INSERTs the row under OnOpen, which basestore.openLocked runs
// before any dispatch can reach the bundle, and no route ever deletes it.)
//
// FIELD ORDER IS LOAD-BEARING, ONCE. The bundle's rows cross the goja boundary as
// map[string]any and are serialised by encoding/json, which sorts object keys.
// Every model below therefore declares its fields in ALPHABETICAL json-tag order,
// so the typed response is BYTE-identical to the relay it replaces — not merely
// equal as JSON. TestTypedReadsAreByteIdenticalToTheBundle pins that against the
// bundle's own bytes, so a field added out of order fails rather than drifting.
//
// NULLABILITY IS THE SCHEMA'S. A column the DDL (schema.go) leaves nullable
// arrives as JSON null, so it is a POINTER here; a NOT NULL column is a value. An
// INTEGER column is int64 and a REAL column float64, matching what the driver
// hands the bridge.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// ops binds the service to the typed cap-table ops. A TypedHandler takes no
// service parameter, so the service arrives as a RECEIVER and every op is a
// method value — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// tenantOf is the validated org for a typed op — the one the gateway asserted and
// cloud.Bridge parked on the context, never a field of In. An In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the
// caller asserted for itself. It is the same principal.Org answer the untyped
// dispatch reads off the request, so both planes gate identically.
func tenantOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire. The cap-table reads are org-scoped collections, so
// the org IS the address and there is no parameter to bind.
type noInput struct{}

// read is the ONE response path for a typed read: resolve the tenant, run the
// bundle route on that tenant's store, and decode its body into out. It is only
// ever called for a route whose bundle handler cannot answer a non-2xx (see the
// package note), so a non-2xx here is an internal failure — the SAME answer the
// untyped dispatch already gives when the host itself fails, rather than a second
// 500 shape relayed from inside the engine.
func (o ops) read(ctx context.Context, route string, out any) error {
	org, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	resp, err := o.s.State.host.Dispatch(ctx, org, goja.BaseRequest{Route: route})
	if err != nil {
		o.s.Log.Error("captable dispatch failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "captable dispatch failed")
	}
	if resp.Status/100 != 2 {
		o.s.Log.Error("captable read answered non-2xx", "route", route, "status", resp.Status)
		return zip.Errorf(http.StatusInternalServerError, "captable dispatch failed")
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		o.s.Log.Error("captable response decode failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "captable dispatch failed")
	}
	return nil
}

// ---- company ----

// captableCompany is the tenant's cap-table root record.
type captableCompany struct {
	// CreatedAt is when the company row was seeded, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// ID is the company id, which is the tenant's own org id.
	ID string `json:"id"`
	// IncorporationCountry is the ISO country the entity is incorporated in.
	IncorporationCountry string `json:"incorporationCountry"`
	// IncorporationState is the state or province of incorporation.
	IncorporationState string `json:"incorporationState"`
	// IncorporationType is the entity kind, e.g. LLC or C_CORP.
	IncorporationType string `json:"incorporationType"`
	// Name is the company's legal name.
	Name string `json:"name"`
	// PublicID is the company's shareable public identifier.
	PublicID string `json:"publicId"`
	// UpdatedAt is when the company row last changed, in unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// GetCompany returns the caller org's cap-table company record. The row is
// seeded when the tenant's store first opens, so it always exists; its name and
// incorporation details are set with PUT /v1/captable/company.
func (o ops) getCompany(ctx context.Context, _ *noInput) (*captableCompany, error) {
	var out captableCompany
	if err := o.read(ctx, "company.get", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- stakeholders ----

// captableStakeholder is one holder of equity in the company — a founder,
// employee, investor or institution.
type captableStakeholder struct {
	// City is the stakeholder's city, if recorded.
	City *string `json:"city"`
	// CompanyName is the name of the company whose cap table this is.
	CompanyName string `json:"companyName"`
	// Country is the stakeholder's two-letter country code.
	Country string `json:"country"`
	// CreatedAt is when the stakeholder was added, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// CurrentRelationship is how the stakeholder relates to the company, e.g.
	// FOUNDER, INVESTOR or EMPLOYEE.
	CurrentRelationship string `json:"currentRelationship"`
	// Email is the stakeholder's email, unique within the company.
	Email string `json:"email"`
	// ID is the stakeholder id.
	ID string `json:"id"`
	// InstitutionName names the institution, when the stakeholder is one.
	InstitutionName *string `json:"institutionName"`
	// Name is the stakeholder's full name.
	Name string `json:"name"`
	// StakeholderType is INDIVIDUAL or INSTITUTION.
	StakeholderType string `json:"stakeholderType"`
	// State is the stakeholder's state or province, if recorded.
	State *string `json:"state"`
	// StreetAddress is the stakeholder's street address, if recorded.
	StreetAddress *string `json:"streetAddress"`
	// TaxID is the stakeholder's tax identifier, if recorded.
	TaxID *string `json:"taxId"`
	// Zipcode is the stakeholder's postal code, if recorded.
	Zipcode *string `json:"zipcode"`
}

// captableStakeholders is the caller org's stakeholders, newest first. The
// response is the bare array, not an envelope.
type captableStakeholders []captableStakeholder

// ListStakeholders returns the caller org's stakeholders, newest first. The
// response is a bare JSON array, not an envelope. Each row carries the holder's
// contact and address fields alongside the company's name.
func (o ops) listStakeholders(ctx context.Context, _ *noInput) (*captableStakeholders, error) {
	var out captableStakeholders
	if err := o.read(ctx, "stakeholders.list", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- share classes ----

// captableShareClass is one class of stock the company has authorized.
type captableShareClass struct {
	// ClassType is COMMON or PREFERRED.
	ClassType string `json:"classType"`
	// CompanyName is the name of the company whose cap table this is.
	CompanyName string `json:"companyName"`
	// ConversionRights describes what the class converts into, e.g.
	// CONVERTS_TO_FUTURE_ROUND.
	ConversionRights string `json:"conversionRights"`
	// ID is the share class id.
	ID string `json:"id"`
	// Idx is the class's 1-based position within the company, in creation order.
	Idx int64 `json:"idx"`
	// InitialSharesAuthorized is how many shares of this class are authorized.
	InitialSharesAuthorized int64 `json:"initialSharesAuthorized"`
	// LiquidationPreferenceMultiple is the preference multiple on liquidation.
	LiquidationPreferenceMultiple float64 `json:"liquidationPreferenceMultiple"`
	// Name is the class name, e.g. "Common" or "Series A Preferred".
	Name string `json:"name"`
	// ParValue is the par value per share.
	ParValue float64 `json:"parValue"`
	// ParticipationCapMultiple caps participation on liquidation; 0 is uncapped.
	ParticipationCapMultiple float64 `json:"participationCapMultiple"`
	// Prefix is the certificate prefix, CS for common and PS for preferred.
	Prefix string `json:"prefix"`
	// PricePerShare is the issue price per share.
	PricePerShare float64 `json:"pricePerShare"`
	// Seniority orders classes in a liquidation waterfall; higher is more senior.
	Seniority int64 `json:"seniority"`
	// VotesPerShare is how many votes one share of this class carries.
	VotesPerShare int64 `json:"votesPerShare"`
}

// captableShareClasses is the caller org's share classes, in creation order. The
// response is the bare array, not an envelope.
type captableShareClasses []captableShareClass

// ListShareClasses returns the caller org's share classes, in creation order. A
// share class is what a certificate is issued in, and every class the company
// has authorized appears. The response is a bare JSON array, not an envelope.
func (o ops) listShareClasses(ctx context.Context, _ *noInput) (*captableShareClasses, error) {
	var out captableShareClasses
	if err := o.read(ctx, "shareClasses.list", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- equity plans ----

// captableEquityPlan is an option pool: a reserve of shares set aside to grant.
type captableEquityPlan struct {
	// BoardApprovalDate is the ISO date the board approved the plan.
	BoardApprovalDate *string `json:"boardApprovalDate"`
	// Comments is free-form notes on the plan.
	Comments *string `json:"comments"`
	// CreatedAt is when the plan was recorded, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// DefaultCancellatonBehavior is what happens to cancelled grants, RETIRE or
	// RETURN_TO_POOL. The key is spelled as the cap-table wire spells it.
	DefaultCancellatonBehavior string `json:"defaultCancellatonBehavior"`
	// ID is the equity plan id.
	ID string `json:"id"`
	// InitialSharesReserved is how many shares the plan reserves.
	InitialSharesReserved int64 `json:"initialSharesReserved"`
	// Name is the plan name, e.g. "2026 Stock Option Plan".
	Name string `json:"name"`
	// PlanEffectiveDate is the ISO date the plan takes effect.
	PlanEffectiveDate *string `json:"planEffectiveDate"`
	// ShareClassID is the class the reserved shares come from.
	ShareClassID string `json:"shareClassId"`
}

// captableEquityPlans is the caller org's equity plans.
type captableEquityPlans struct {
	// Data is every equity plan on the caller org's cap table, newest first.
	Data []captableEquityPlan `json:"data"`
}

// ListEquityPlans returns the caller org's equity plans, newest first. An equity
// plan is an option pool: a reserve of shares, drawn from one share class, that
// option grants are written against.
func (o ops) listEquityPlans(ctx context.Context, _ *noInput) (*captableEquityPlans, error) {
	var out captableEquityPlans
	if err := o.read(ctx, "equityPlans.list", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- shares ----

// captableShare is one issued share certificate.
type captableShare struct {
	// CapitalContribution is the cash paid for the certificate, if recorded.
	CapitalContribution *float64 `json:"capitalContribution"`
	// CertificateID is the certificate number, unique within the company.
	CertificateID string `json:"certificateId"`
	// CompanyLegends are the restrictive legends printed on the certificate.
	CompanyLegends []string `json:"companyLegends"`
	// ID is the share id.
	ID string `json:"id"`
	// IssueDate is the ISO date the certificate was issued.
	IssueDate *string `json:"issueDate"`
	// PricePerShare is the price paid per share, if recorded.
	PricePerShare *float64 `json:"pricePerShare"`
	// Quantity is how many shares the certificate covers.
	Quantity int64 `json:"quantity"`
	// ShareClassID is the class the shares belong to.
	ShareClassID string `json:"shareClassId"`
	// ShareClassName is that class's name.
	ShareClassName string `json:"shareClassName"`
	// ShareClassType is that class's type, COMMON or PREFERRED.
	ShareClassType string `json:"shareClassType"`
	// StakeholderID is the holder of the certificate.
	StakeholderID string `json:"stakeholderId"`
	// StakeholderName is that holder's name.
	StakeholderName string `json:"stakeholderName"`
	// Status is ACTIVE or DRAFT.
	Status string `json:"status"`
}

// captableShares is the caller org's issued share certificates.
type captableShares struct {
	// Data is every issued share certificate, newest first.
	Data []captableShare `json:"data"`
}

// ListShares returns the caller org's share certificates, newest first. Each row
// is joined to its holder and its share class, so a certificate names who holds
// it and what class it is in without a second call.
func (o ops) listShares(ctx context.Context, _ *noInput) (*captableShares, error) {
	var out captableShares
	if err := o.read(ctx, "shares.list", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- options ----

// captableOption is one option grant against an equity plan.
type captableOption struct {
	// CliffYears is how many years before any of the grant vests.
	CliffYears int64 `json:"cliffYears"`
	// EquityPlanID is the plan the grant draws from.
	EquityPlanID string `json:"equityPlanId"`
	// EquityPlanName is that plan's name.
	EquityPlanName string `json:"equityPlanName"`
	// ExercisePrice is the strike price per share.
	ExercisePrice float64 `json:"exercisePrice"`
	// ExpirationDate is the ISO date the grant expires.
	ExpirationDate *string `json:"expirationDate"`
	// GrantID is the grant number, unique within the company.
	GrantID string `json:"grantId"`
	// ID is the option id.
	ID string `json:"id"`
	// IssueDate is the ISO date the grant was issued.
	IssueDate *string `json:"issueDate"`
	// Quantity is how many shares the grant covers.
	Quantity int64 `json:"quantity"`
	// StakeholderID is the grantee.
	StakeholderID string `json:"stakeholderId"`
	// StakeholderName is that grantee's name.
	StakeholderName string `json:"stakeholderName"`
	// Status is the grant's state, e.g. DRAFT, ACTIVE, EXERCISED, EXPIRED or
	// CANCELLED. Only non-terminal grants dilute the cap table.
	Status string `json:"status"`
	// Type is the grant kind, ISO or NSO.
	Type string `json:"type"`
	// VestingYears is the total vesting period in years.
	VestingYears int64 `json:"vestingYears"`
}

// captableOptions is the caller org's option grants.
type captableOptions struct {
	// Data is every option grant, newest first.
	Data []captableOption `json:"data"`
}

// ListOptions returns the caller org's option grants, newest first. Each row is
// joined to its grantee and its equity plan. Grants that are EXERCISED, EXPIRED
// or CANCELLED are listed here but do not dilute the cap table.
func (o ops) listOptions(ctx context.Context, _ *noInput) (*captableOptions, error) {
	var out captableOptions
	if err := o.read(ctx, "options.list", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- SAFEs ----

// captableSafe is one SAFE (simple agreement for future equity).
type captableSafe struct {
	// Capital is the cash the investor put in.
	Capital float64 `json:"capital"`
	// DiscountRate is the discount to the next round's price, if any.
	DiscountRate *float64 `json:"discountRate"`
	// ID is the SAFE id.
	ID string `json:"id"`
	// IssueDate is the ISO date the SAFE was signed.
	IssueDate *string `json:"issueDate"`
	// MFN is true when the SAFE carries a most-favoured-nation clause.
	MFN bool `json:"mfn"`
	// ProRata is true when the SAFE carries pro-rata rights.
	ProRata bool `json:"proRata"`
	// PublicID is the SAFE's shareable identifier, unique within the company.
	PublicID string `json:"publicId"`
	// StakeholderID is the investor.
	StakeholderID string `json:"stakeholderId"`
	// StakeholderName is that investor's name.
	StakeholderName string `json:"stakeholderName"`
	// Status is the SAFE's state, e.g. DRAFT or ACTIVE.
	Status string `json:"status"`
	// Type is POST_MONEY or PRE_MONEY.
	Type string `json:"type"`
	// ValuationCap is the valuation cap, if any.
	ValuationCap *float64 `json:"valuationCap"`
}

// captableSafes is the caller org's SAFEs.
type captableSafes struct {
	// Data is every SAFE, newest first.
	Data []captableSafe `json:"data"`
}

// ListSafes returns the caller org's SAFEs, newest first. A SAFE is a simple
// agreement for future equity: its capital sits OUTSIDE issued equity until it
// converts, so it is not part of the share counts.
func (o ops) listSafes(ctx context.Context, _ *noInput) (*captableSafes, error) {
	var out captableSafes
	if err := o.read(ctx, "safes.list", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- convertible notes ----

// captableNote is one convertible note.
type captableNote struct {
	// Capital is the principal the investor lent.
	Capital float64 `json:"capital"`
	// ConversionCap is the valuation cap on conversion, if any.
	ConversionCap *float64 `json:"conversionCap"`
	// DiscountRate is the discount to the next round's price, if any.
	DiscountRate *float64 `json:"discountRate"`
	// ID is the note id.
	ID string `json:"id"`
	// InterestRate is the annual interest rate, if any.
	InterestRate *float64 `json:"interestRate"`
	// IssueDate is the ISO date the note was signed.
	IssueDate *string `json:"issueDate"`
	// PublicID is the note's shareable identifier, unique within the company.
	PublicID string `json:"publicId"`
	// StakeholderID is the investor.
	StakeholderID string `json:"stakeholderId"`
	// StakeholderName is that investor's name.
	StakeholderName string `json:"stakeholderName"`
	// Status is the note's state, e.g. DRAFT or ACTIVE.
	Status string `json:"status"`
	// Type is the instrument kind, e.g. NOTE.
	Type string `json:"type"`
}

// captableNotes is the caller org's convertible notes.
type captableNotes struct {
	// Data is every convertible note, newest first.
	Data []captableNote `json:"data"`
}

// ListConvertibles returns the caller org's convertible notes, newest first. A
// note's principal sits OUTSIDE issued equity until it converts, so it is not
// part of the share counts.
func (o ops) listConvertibles(ctx context.Context, _ *noInput) (*captableNotes, error) {
	var out captableNotes
	if err := o.read(ctx, "convertibles.list", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- rounds ----

// captableRound is one fundraising round.
type captableRound struct {
	// CloseDate is the ISO date the round closed, once it has.
	CloseDate *string `json:"closeDate"`
	// CreatedAt is when the round was recorded, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// ID is the round id.
	ID string `json:"id"`
	// Name is the round name, e.g. "Series A".
	Name string `json:"name"`
	// PreMoneyValuation is the pre-money valuation, for a priced round.
	PreMoneyValuation *float64 `json:"preMoneyValuation"`
	// PricePerShare is the price per share, for a priced round.
	PricePerShare *float64 `json:"pricePerShare"`
	// RaisedAmount is how much has been invested so far.
	RaisedAmount float64 `json:"raisedAmount"`
	// RoundType is PRICED, SAFE or CONVERTIBLE_NOTE.
	RoundType string `json:"roundType"`
	// ShareClassID is the class a priced round issues into.
	ShareClassID *string `json:"shareClassId"`
	// Status is OPEN or CLOSED.
	Status string `json:"status"`
	// TargetAmount is how much the round set out to raise.
	TargetAmount float64 `json:"targetAmount"`
}

// captableRounds is the caller org's fundraising rounds.
type captableRounds struct {
	// Data is every round, newest first.
	Data []captableRound `json:"data"`
}

// ListRounds returns the caller org's fundraising rounds, newest first. A round
// groups a fundraising event; a PRICED round also carries the share class and
// price per share it issues at.
func (o ops) listRounds(ctx context.Context, _ *noInput) (*captableRounds, error) {
	var out captableRounds
	if err := o.read(ctx, "rounds.list", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- investments ----

// captableInvestment is one investor's cheque into a round.
type captableInvestment struct {
	// Amount is the cash invested.
	Amount float64 `json:"amount"`
	// Date is the ISO date of the investment.
	Date *string `json:"date"`
	// ID is the investment id.
	ID string `json:"id"`
	// RoundID is the round the cheque went into.
	RoundID string `json:"roundId"`
	// ShareClassID is the class shares were issued in, for a priced round.
	ShareClassID *string `json:"shareClassId"`
	// Shares is how many shares the investment bought; 0 when the round issues
	// no equity at the time of investment.
	Shares int64 `json:"shares"`
	// StakeholderID is the investor.
	StakeholderID string `json:"stakeholderId"`
	// StakeholderName is that investor's name.
	StakeholderName string `json:"stakeholderName"`
}

// captableInvestments is the caller org's investments across every round.
type captableInvestments struct {
	// Data is every investment across every round, newest first.
	Data []captableInvestment `json:"data"`
}

// ListInvestments returns the caller org's investments, newest first. It spans
// every round, so it is the flat ledger of cheques written into the company,
// each naming its investor and the round it went into.
func (o ops) listInvestments(ctx context.Context, _ *noInput) (*captableInvestments, error) {
	var out captableInvestments
	if err := o.read(ctx, "rounds.investments.list", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- the computed cap table ----

// captableSummaryCompany names the company the summary is computed for.
type captableSummaryCompany struct {
	// ID is the company id, which is the tenant's own org id.
	ID string `json:"id"`
	// Name is the company's legal name.
	Name string `json:"name"`
}

// captableTotals is the company-wide share count.
type captableTotals struct {
	// FullyDilutedShares is outstandingShares plus grantedOptions.
	FullyDilutedShares int64 `json:"fullyDilutedShares"`
	// GrantedOptions is the shares under non-terminal option grants — grants that
	// are EXERCISED, EXPIRED or CANCELLED are excluded, so nothing double-counts.
	GrantedOptions int64 `json:"grantedOptions"`
	// OutstandingShares is the sum of every issued share certificate.
	OutstandingShares int64 `json:"outstandingShares"`
	// ShareClasses is how many share classes the company has authorized.
	ShareClasses int64 `json:"shareClasses"`
	// Stakeholders is how many stakeholders the company has.
	Stakeholders int64 `json:"stakeholders"`
}

// captableHolding is one stakeholder's position on a fully-diluted basis.
type captableHolding struct {
	// FullyDiluted is shares plus options.
	FullyDiluted int64 `json:"fullyDiluted"`
	// Name is the stakeholder's name.
	Name string `json:"name"`
	// Options is the shares under this stakeholder's non-terminal option grants.
	Options int64 `json:"options"`
	// OwnershipPct is fullyDiluted as a percentage of the company's
	// fullyDilutedShares, rounded to two decimals; 0 when nothing is issued.
	OwnershipPct float64 `json:"ownershipPct"`
	// Shares is the shares this stakeholder holds by certificate.
	Shares int64 `json:"shares"`
	// StakeholderID is the stakeholder.
	StakeholderID string `json:"stakeholderId"`
}

// captableClassHolding is one share class's authorized-versus-issued position.
type captableClassHolding struct {
	// Authorized is how many shares of the class are authorized.
	Authorized int64 `json:"authorized"`
	// ClassType is COMMON or PREFERRED.
	ClassType string `json:"classType"`
	// Issued is how many shares of the class have been issued.
	Issued int64 `json:"issued"`
	// Name is the class name.
	Name string `json:"name"`
	// ShareClassID is the share class.
	ShareClassID string `json:"shareClassId"`
}

// captableInstrumentTotal is a count of convertible instruments and the capital
// they carry.
type captableInstrumentTotal struct {
	// Capital is the total capital across those instruments.
	Capital float64 `json:"capital"`
	// Count is how many instruments there are.
	Count int64 `json:"count"`
}

// captableConvertibles is the capital raised on instruments that have not yet
// converted into equity, so it is NOT part of the share counts above.
type captableConvertibles struct {
	// Notes is the convertible notes total.
	Notes captableInstrumentTotal `json:"notes"`
	// Safes is the SAFEs total.
	Safes captableInstrumentTotal `json:"safes"`
}

// captableRoundTotals is the fundraising rollup.
type captableRoundTotals struct {
	// Count is how many rounds the company has recorded.
	Count int64 `json:"count"`
	// TotalRaised is the sum of every round's raised amount.
	TotalRaised float64 `json:"totalRaised"`
}

// captableSummary is the computed cap table: who owns what, on a fully-diluted
// basis, plus the capital sitting on unconverted instruments.
type captableSummary struct {
	// ByShareClass is each share class's authorized-versus-issued position, in
	// class creation order.
	ByShareClass []captableClassHolding `json:"byShareClass"`
	// ByStakeholder is each stakeholder's position, largest holding first.
	ByStakeholder []captableHolding `json:"byStakeholder"`
	// Company names the company the cap table is computed for.
	Company captableSummaryCompany `json:"company"`
	// Convertibles is the capital on SAFEs and notes that have not converted.
	Convertibles captableConvertibles `json:"convertibles"`
	// Rounds is the fundraising rollup.
	Rounds captableRoundTotals `json:"rounds"`
	// Totals is the company-wide share count.
	Totals captableTotals `json:"totals"`
}

// GetSummary computes the caller org's cap table. It answers who owns what on a
// fully-diluted basis: outstanding shares, granted options, per-stakeholder
// ownership percentages, each share class's authorized versus issued position,
// and the capital sitting on SAFEs and convertible notes that have not yet
// converted. Only non-terminal option grants dilute — EXERCISED, EXPIRED and
// CANCELLED grants are excluded, so equity issued through an exercised option is
// never counted twice.
func (o ops) getSummary(ctx context.Context, _ *noInput) (*captableSummary, error) {
	var out captableSummary
	if err := o.read(ctx, "captable", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// The ops above are REGISTERED in captable.go's routes(), beside the untyped
// relays, so one function shows the whole /v1/captable table and whether each
// route is typed. cmd/zipdoc resolves a group's prefix by finding its assignment
// in the SAME file as the registration, which is the other reason the whole table
// lives there.
