package captable

// writes.go is the BODY-CARRYING half of captable's typed plane. typed.go types
// the routes whose whole input is a path segment; these three carry a request
// body and are typed anyway, because their bodies are made only of fields the
// bundle reads as STRINGS.
//
// WHY THE OTHER ELEVEN WRITES ARE STILL RELAYS, AND WHY THESE THREE ARE NOT.
// The bundle validates with coercing helpers (goja/src/validate.ts), and the
// coercion that blocks typing is `num`/`intNum`/`optNum`: each takes a number OR
// a numeric string (z.coerce.number), so `{"parValue":"1.5"}` is a 201 today. A
// Go float64 field refuses that string with a 400 the route has never sent, and a
// float64 CANNOT carry the rejected token onward either — it has eight bytes and
// nowhere to put `"abc"` — so a typed numeric field also moves the bundle's
// {success,message,errors} envelope to zip's {status,code,error}. Both are
// changes to what the route accepts and answers, so those eleven stay relays and
// each names its own coerced field at its registration in captable.go.
//
// These three have NO numeric field. Every field they carry goes to `reqString`,
// `optString` or `optDateString`, and `scalar` below carries any of those to the
// bundle byte for byte — so the bundle remains the ONLY validator, the one that
// decides both what is accepted and which envelope says no.
//
// WHAT IS STILL NOT IDENTICAL. Two orderings move, both for requests that carry
// no valid tenant, because zip decodes the body before the handler runs while
// the relay resolved the org first: a caller with no org whose body is malformed
// JSON, or whose body exceeds maxBody, now sees zip's 400 or the 413 instead of
// the 403 it saw. Every request that HAS a tenant is answered exactly as before,
// which sizedIn keeps true for the 413 (see below).

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/goja"
	"github.com/zap-proto/zip"
)

// write is the ONE response path for a body-carrying typed op: refuse an
// oversized body with the relay's 413, run the bundle route on the caller's
// tenant with the assembled body, and decode the 2xx answer into out. A non-2xx
// is the BUNDLE's, relayed through goja.BundleErr exactly as the reads do.
func (o ops) write(ctx context.Context, route string, size goja.SizedIn, params map[string]string, fields map[string]goja.BodyField, out any) error {
	org, err := principal.Acting(ctx)
	if err != nil {
		return err
	}
	// After the tenant, before the work — the order the relay used.
	if size.Oversize() {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	body, err := goja.Body(fields)
	if err != nil {
		o.s.Log.Error("captable body assembly failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "captable dispatch failed")
	}
	return o.run(ctx, org, route, params, body, out)
}

// captableUpdated is the answer to a successful cap-table update: the bundle's
// {success, message} envelope, whose fields are declared in alphabetical json
// order because the answer is re-marshalled by encoding/json, which sorts them.
type captableUpdated struct {
	// Message is the human sentence the cap table wrote, e.g. "Company updated".
	Message string `json:"message"`
	// Success is true when the update was applied.
	Success bool `json:"success"`
}

// ---- the company record ----

// captableCompanyUpdate is the company details a tenant can set on its cap-table
// root record.
type captableCompanyUpdate struct {
	goja.SizedIn
	// IncorporationCountry is the ISO country the entity is incorporated in.
	// Optional; omitted, null or empty clears it. Any JSON scalar is accepted
	// and stored as its text.
	IncorporationCountry goja.Scalar `json:"incorporationCountry,omitempty"`
	// IncorporationState is the state or province of incorporation. Optional;
	// omitted, null or empty clears it. Any JSON scalar is accepted and stored
	// as its text.
	IncorporationState goja.Scalar `json:"incorporationState,omitempty"`
	// IncorporationType is the entity kind, e.g. LLC or C_CORP. Optional;
	// omitted, null or empty clears it. Any JSON scalar is accepted and stored
	// as its text.
	IncorporationType goja.Scalar `json:"incorporationType,omitempty"`
	// Name is the company's legal name. Required, and it must be a non-empty
	// string — anything else is refused with the cap table's own validation
	// error.
	Name goja.Scalar `json:"name,omitempty"`
}

// UnmarshalJSON keeps the caller's tokens and records the body size; it refuses
// nothing, leaving every judgement to the cap table. See sizedIn.fill.
func (in *captableCompanyUpdate) UnmarshalJSON(b []byte) error {
	type body captableCompanyUpdate // sheds the method, so this does not recurse
	var v body
	in.Fill(maxBody, b, &v)
	v.SizedIn = in.SizedIn
	*in = captableCompanyUpdate(v)
	return nil
}

// UpdateCompany sets the caller org's company name and incorporation details.
// The name is required; the three incorporation fields are optional and each is
// stored as empty when omitted, so a call that sends only a name CLEARS them.
// The company row itself is seeded when the tenant's store first opens, so this
// never creates one.
func (o ops) updateCompany(ctx context.Context, in *captableCompanyUpdate) (*captableUpdated, error) {
	var out captableUpdated
	err := o.write(ctx, "company.update", in.SizedIn, nil, map[string]goja.BodyField{
		"incorporationCountry": in.IncorporationCountry,
		"incorporationState":   in.IncorporationState,
		"incorporationType":    in.IncorporationType,
		"name":                 in.Name,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- one stakeholder, partially ----

// captableStakeholderPatch is the fields of one stakeholder a caller can change.
// EVERY field is optional and independent: a key that is absent is left alone,
// and a key that is present is written as sent — including an explicit null,
// which clears the column. A request that names none of them is refused.
type captableStakeholderPatch struct {
	goja.SizedIn
	// City is the stakeholder's city.
	City goja.Scalar `json:"city,omitempty"`
	// CurrentRelationship is how the stakeholder relates to the company, e.g.
	// FOUNDER, INVESTOR or EMPLOYEE. This route stores it as sent — unlike
	// adding a stakeholder, it is not checked against the vocabulary.
	CurrentRelationship goja.Scalar `json:"currentRelationship,omitempty"`
	// Email is the stakeholder's email. This route stores it as sent — unlike
	// adding a stakeholder, it is not checked for shape or uniqueness.
	Email goja.Scalar `json:"email,omitempty"`
	// ID is the stakeholder to update. It is the path segment: the URL is the
	// addressing authority, and the org it is resolved in comes from the
	// caller's principal, so an id from another tenant is simply not found.
	ID string `json:"id"`
	// InstitutionName names the institution, when the stakeholder is one.
	InstitutionName goja.Scalar `json:"institutionName,omitempty"`
	// Name is the stakeholder's full name.
	Name goja.Scalar `json:"name,omitempty"`
	// StakeholderType is INDIVIDUAL or INSTITUTION. This route stores it as
	// sent — unlike adding a stakeholder, it is not checked against the
	// vocabulary.
	StakeholderType goja.Scalar `json:"stakeholderType,omitempty"`
	// State is the stakeholder's state or province.
	State goja.Scalar `json:"state,omitempty"`
	// StreetAddress is the stakeholder's street address.
	StreetAddress goja.Scalar `json:"streetAddress,omitempty"`
	// TaxID is the stakeholder's tax identifier.
	TaxID goja.Scalar `json:"taxId,omitempty"`
	// Zipcode is the stakeholder's postal code.
	Zipcode goja.Scalar `json:"zipcode,omitempty"`
}

// UnmarshalJSON keeps the caller's tokens and records the body size; it refuses
// nothing, leaving every judgement to the cap table. See sizedIn.fill.
func (in *captableStakeholderPatch) UnmarshalJSON(b []byte) error {
	type body captableStakeholderPatch // sheds the method, so this does not recurse
	var v body
	in.Fill(maxBody, b, &v)
	v.SizedIn = in.SizedIn
	*in = captableStakeholderPatch(v)
	return nil
}

// UpdateStakeholder changes one of the caller org's stakeholders. It is a
// PARTIAL update: only the fields the request names are written, and a field
// sent as null clears that column. A request that names no updatable field is
// refused, and an id this org does not hold is not found.
//
// The values are stored as sent. Unlike adding a stakeholder, this route does
// not check the email's shape or the type and relationship vocabularies, so it
// can record a value that adding one would have rejected.
func (o ops) updateStakeholder(ctx context.Context, in *captableStakeholderPatch) (*captableUpdated, error) {
	var out captableUpdated
	err := o.write(ctx, "stakeholders.update", in.SizedIn, map[string]string{"id": in.ID}, map[string]goja.BodyField{
		"city":                in.City,
		"currentRelationship": in.CurrentRelationship,
		"email":               in.Email,
		"institutionName":     in.InstitutionName,
		"name":                in.Name,
		"stakeholderType":     in.StakeholderType,
		"state":               in.State,
		"streetAddress":       in.StreetAddress,
		"taxId":               in.TaxID,
		"zipcode":             in.Zipcode,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- closing a round ----

// captableRoundCloseRequest closes one of the caller org's open rounds.
type captableRoundCloseRequest struct {
	goja.SizedIn
	// CloseDate is the date to record the round as closed on. Optional: omitted,
	// null or empty records TODAY. Any JSON scalar is accepted and stored as its
	// text, and the text is stored unparsed, so a caller that wants an ISO date
	// sends one.
	CloseDate goja.Scalar `json:"closeDate,omitempty"`
	// ID is the round to close. It is the path segment: the URL is the
	// addressing authority, and the org it is resolved in comes from the
	// caller's principal, so an id from another tenant is simply not found.
	ID string `json:"id"`
}

// UnmarshalJSON keeps the caller's tokens and records the body size; it refuses
// nothing, leaving every judgement to the cap table. See sizedIn.fill.
func (in *captableRoundCloseRequest) UnmarshalJSON(b []byte) error {
	type body captableRoundCloseRequest // sheds the method, so this does not recurse
	var v body
	in.Fill(maxBody, b, &v)
	v.SizedIn = in.SizedIn
	*in = captableRoundCloseRequest(v)
	return nil
}

// CloseRound closes one of the caller org's fundraising rounds, recording the
// close date and moving its status to CLOSED. Only an OPEN round can be closed:
// a round that is already closed — like an id this org does not hold — is not
// found. Closing a round does not change what was invested in it.
func (o ops) closeRound(ctx context.Context, in *captableRoundCloseRequest) (*captableUpdated, error) {
	var out captableUpdated
	err := o.write(ctx, "rounds.close", in.SizedIn, map[string]string{"id": in.ID}, map[string]goja.BodyField{
		"closeDate": in.CloseDate,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- a share transfer, and an investment into an open round ----
//
// THE DEBT THESE PAY, and how it was paid. Eleven writes here were listed in
// `typingOwed` rather than in `untypedByDesign`, because nothing about them is
// unexpressible — the mechanism below already existed and three ops already used
// it. What blocked them was that `goja.Body` assembles ONLY the fields declared,
// so a name that does not match the bundle's is dropped SILENTLY: a share issuance
// missing its price is accepted as a smaller write rather than refused, and wrong
// equity data nothing reports is worse than an untyped route.
//
// The field names were never unknowable, only unread: the bundle is a Go module
// dependency, so its route source sits in the module cache at the version go.mod
// pins. Every field below is transcribed from
// github.com/hanzoai/captable@v1.0.0/goja/src/routes, and the two chosen first are
// the two smallest — three fields and four — so the pattern is proved end to end
// before it is applied to a seventeen-field share issuance.
//
// It also closes an asymmetry that was worse than either extreme: the DELETES on
// this plane were already typed, so an agent could delete a share issuance through
// the fleet's MCP server and not create one.

// captableShareTransfer moves shares from the holder of one certificate to another
// stakeholder.
type captableShareTransfer struct {
	goja.SizedIn
	// ShareID is the certificate being transferred. Required, and it must name a
	// share in the caller org's own company — a share id from another tenant
	// resolves to not-found rather than to another company's certificate.
	ShareID goja.Scalar `json:"shareId"`
	// ToStakeholderID is who receives them. Required, and it must reference a
	// stakeholder of the SAME company; one that does not is refused 400 rather
	// than creating a dangling holder.
	ToStakeholderID goja.Scalar `json:"toStakeholderId"`
	// Quantity is how many shares to move. OPTIONAL, and its absence is not zero:
	// omitted or null transfers the WHOLE certificate. A value outside 1..held is
	// refused, so a partial transfer can never over-issue.
	//
	// A full transfer REASSIGNS the certificate and mints no new one; a partial
	// transfer splits it and answers with the new share's id. That is the fact a
	// caller needs from the answer, not from this comment.
	Quantity goja.Scalar `json:"quantity,omitempty"`
	// CertificateID names the NEW certificate a partial transfer issues, and is
	// required for one — a partial transfer without it is refused 400. It is unused
	// by a full transfer, which reassigns the existing certificate.
	//
	// It must be unique within the company; reusing one is refused 409. Declaring
	// it is not optional in the way the tag suggests: omitting this field from the
	// Go type would leave `quantity` accepted and every PARTIAL transfer answering
	// "certificateId is required" with no way for a caller to supply it — the
	// silent-drop failure this whole conversion was blocked on, one field wide.
	CertificateID goja.Scalar `json:"certificateId,omitempty"`
}

// UnmarshalJSON records the body without judging it, so the relay's own 413 is
// still decided on SIZE before the shape is considered.
func (in *captableShareTransfer) UnmarshalJSON(b []byte) error {
	type v captableShareTransfer
	var out v
	in.Fill(maxBody, b, &out)
	out.SizedIn = in.SizedIn
	*in = captableShareTransfer(out)
	return nil
}

// TransferShares moves shares from one stakeholder to another.
//
// Omit `quantity` to transfer the whole certificate, which REASSIGNS it and mints
// no new share. Send a quantity below the amount held to SPLIT it — the source
// certificate keeps the remainder, and a split additionally requires
// `certificateId` for the new certificate, which must be unique in the company.
// A quantity outside 1..held is refused, so a transfer can never over-issue.
//
// Both outcomes answer 200: a transfer records a movement between holders and
// mints no security of its own, which is why this is not a 201 the way an
// investment is.
func (o ops) transferShares(ctx context.Context, in *captableShareTransfer) (*captableTransferred, error) {
	var out captableTransferred
	err := o.write(ctx, "shares.transfer", in.SizedIn, nil, map[string]goja.BodyField{
		"shareId":         in.ShareID,
		"toStakeholderId": in.ToStakeholderID,
		"quantity":        in.Quantity,
		"certificateId":   in.CertificateID,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// captableTransferred is what a transfer answers.
type captableTransferred struct {
	// Message is the human sentence the cap table wrote, e.g. "Share transferred".
	Message string `json:"message"`
	// NewShareID names the certificate a PARTIAL transfer created. It is null on a
	// full transfer, which reassigns the existing certificate instead of splitting
	// it — so null here means "no new certificate", never "the transfer failed".
	NewShareID *string `json:"newShareId"`
	// Success is true when the transfer was applied.
	Success bool `json:"success"`
	// Transferred is how many shares moved.
	Transferred int64 `json:"transferred"`
}

// captableInvestmentIn is one investor's money into an OPEN round.
type captableInvestmentIn struct {
	goja.SizedIn
	// ID is the round to invest in. The URL is the addressing authority — a path
	// segment binds after the body and after the query — so the address decides
	// which round is written whatever a body claims.
	ID string `json:"id"`
	// StakeholderID is the investor. Required, and it must reference a stakeholder
	// of the same company.
	StakeholderID goja.Scalar `json:"stakeholderId"`
	// Amount is the money invested, in the round's own currency. Required and must
	// be positive.
	Amount goja.Scalar `json:"amount"`
	// Date is when the investment was made, YYYY-MM-DD. Optional: omitted, it is
	// recorded as today.
	Date goja.Scalar `json:"date,omitempty"`
	// Comments is a free-text note kept with the investment. Optional.
	Comments goja.Scalar `json:"comments,omitempty"`
}

// UnmarshalJSON records the body without judging it, so the 413 stays a decision
// about SIZE.
func (in *captableInvestmentIn) UnmarshalJSON(b []byte) error {
	type v captableInvestmentIn
	var out v
	in.Fill(maxBody, b, &out)
	out.SizedIn = in.SizedIn
	*in = captableInvestmentIn(out)
	return nil
}

// AddInvestment records one investor's money into an open round.
//
// The round must be OPEN; investing into a closed one is refused. Where the round
// carries a price per share, the investment also issues the shares it buys and the
// answer names them.
func (o ops) addInvestment(ctx context.Context, in *captableInvestmentIn) (*captableInvested, error) {
	var out captableInvested
	err := o.write(ctx, "rounds.investments.add", in.SizedIn, map[string]string{"id": in.ID},
		map[string]goja.BodyField{
			"stakeholderId": in.StakeholderID,
			"amount":        in.Amount,
			"date":          in.Date,
			"comments":      in.Comments,
		}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// captableInvested is what an investment answers.
type captableInvested struct {
	// ID is the investment record's id.
	ID string `json:"id"`
	// Message is the human sentence the cap table wrote.
	Message string `json:"message"`
	// NewShareID names the certificate the investment issued, when the round
	// carries a price per share. Null when the round prices later, which is a
	// recorded investment and not a failure.
	NewShareID *string `json:"newShareId"`
	// Success is true when the investment was recorded.
	Success bool `json:"success"`
}

// ---- the remaining equity writes ----
//
// Eight ops, every field transcribed from the bundle's own route source at the
// version go.mod pins. They were listed as OWED rather than refused, and the thing
// that blocked them was a belief rather than a wire: that the accepted field names
// were not readable here. The bundle is a Go module dependency; its source is in
// the module cache.
//
// The statuses are the bundle's and are DECLARED rather than inherited, because a
// typed op writes its own: every create answers created() -> 201, and the share
// class AMEND answers okRes() -> 200, because replacing terms mints nothing.

// captableShareClassIn is the body of createShareClass.
//
// Every field is transcribed from the bundle route this relays to
// (github.com/hanzoai/captable goja/src/routes, "shareClasses.create") — goja.Body
// assembles ONLY what is declared here, so a name that does not match is
// dropped silently rather than refused.
type captableShareClassIn struct {
	goja.SizedIn
	// ClassType is COMMON or PREFERRED.
	ClassType goja.Scalar `json:"classType"`
	// Name is the share class's name, e.g. "Series A Preferred".
	Name goja.Scalar `json:"name"`
	// InitialSharesAuthorized is how many shares this class may issue. A whole number.
	InitialSharesAuthorized goja.Scalar `json:"initialSharesAuthorized"`
	// BoardApprovalDate is when the board approved the class, YYYY-MM-DD.
	BoardApprovalDate goja.Scalar `json:"boardApprovalDate"`
	// StockholderApprovalDate is when the stockholders approved it, YYYY-MM-DD.
	StockholderApprovalDate goja.Scalar `json:"stockholderApprovalDate"`
	// VotesPerShare is how many votes one share of this class carries. A whole number; 0 for non-voting.
	VotesPerShare goja.Scalar `json:"votesPerShare"`
	// ParValue is the nominal per-share value on the certificate, in the company's currency.
	ParValue goja.Scalar `json:"parValue"`
	// PricePerShare is the issue price for this class, in the company's currency.
	PricePerShare goja.Scalar `json:"pricePerShare"`
	// Seniority orders liquidation preference: LOWER is more senior, so 1 is paid before 2.
	Seniority goja.Scalar `json:"seniority"`
	// ConversionRights is CONVERTS_TO_FUTURE_ROUND or CONVERTS_TO_SHARE_CLASS_ID. The second requires convertsToShareClassId.
	ConversionRights goja.Scalar `json:"conversionRights"`
	// ConvertsToShareClassID names the class this one converts into. Required when conversionRights is CONVERTS_TO_SHARE_CLASS_ID, ignored otherwise.
	ConvertsToShareClassID goja.Scalar `json:"convertsToShareClassId,omitempty"`
	// LiquidationPreferenceMultiple is the multiple of invested capital paid out before junior classes — 1 for a 1x preference.
	LiquidationPreferenceMultiple goja.Scalar `json:"liquidationPreferenceMultiple"`
	// ParticipationCapMultiple caps participation after the preference is paid, as a multiple of invested capital. 0 for no cap.
	ParticipationCapMultiple goja.Scalar `json:"participationCapMultiple"`
}

// UnmarshalJSON records the body without judging it, so the relay's 413 stays a
// decision about SIZE rather than about shape.
func (in *captableShareClassIn) UnmarshalJSON(b []byte) error {
	type v captableShareClassIn
	var out v
	in.Fill(maxBody, b, &out)
	out.SizedIn = in.SizedIn
	*in = captableShareClassIn(out)
	return nil
}

// CreateShareClass defines a new class of shares.
//
// Every field but convertsToShareClassId is required — a class is the instrument
// every later issuance prices against, so a partially-specified one would silently
// mis-value every share issued into it. `seniority` orders liquidation preference
// with LOWER first.
func (o ops) createShareClass(ctx context.Context, in *captableShareClassIn) (*captableCreated, error) {
	var out captableCreated
	err := o.write(ctx, "shareClasses.create", in.SizedIn, nil, map[string]goja.BodyField{
		"classType":                     in.ClassType,
		"name":                          in.Name,
		"initialSharesAuthorized":       in.InitialSharesAuthorized,
		"boardApprovalDate":             in.BoardApprovalDate,
		"stockholderApprovalDate":       in.StockholderApprovalDate,
		"votesPerShare":                 in.VotesPerShare,
		"parValue":                      in.ParValue,
		"pricePerShare":                 in.PricePerShare,
		"seniority":                     in.Seniority,
		"conversionRights":              in.ConversionRights,
		"convertsToShareClassId":        in.ConvertsToShareClassID,
		"liquidationPreferenceMultiple": in.LiquidationPreferenceMultiple,
		"participationCapMultiple":      in.ParticipationCapMultiple,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// captableShareClassAmend is the body of amendShareClass.
//
// Every field is transcribed from the bundle route this relays to
// (github.com/hanzoai/captable goja/src/routes, "shareClasses.update") — goja.Body
// assembles ONLY what is declared here, so a name that does not match is
// dropped silently rather than refused.
type captableShareClassAmend struct {
	goja.SizedIn
	// ID addresses the resource. The URL is the addressing authority — a path
	// segment binds after the body and after the query — so the address decides
	// which row is written whatever a body claims.
	ID string `json:"id"`
	// ClassType is COMMON or PREFERRED.
	ClassType goja.Scalar `json:"classType"`
	// Name is the share class's name, e.g. "Series A Preferred".
	Name goja.Scalar `json:"name"`
	// InitialSharesAuthorized is how many shares this class may issue. A whole number.
	InitialSharesAuthorized goja.Scalar `json:"initialSharesAuthorized"`
	// BoardApprovalDate is when the board approved the class, YYYY-MM-DD.
	BoardApprovalDate goja.Scalar `json:"boardApprovalDate"`
	// StockholderApprovalDate is when the stockholders approved it, YYYY-MM-DD.
	StockholderApprovalDate goja.Scalar `json:"stockholderApprovalDate"`
	// VotesPerShare is how many votes one share of this class carries. A whole number; 0 for non-voting.
	VotesPerShare goja.Scalar `json:"votesPerShare"`
	// ParValue is the nominal per-share value on the certificate, in the company's currency.
	ParValue goja.Scalar `json:"parValue"`
	// PricePerShare is the issue price for this class, in the company's currency.
	PricePerShare goja.Scalar `json:"pricePerShare"`
	// Seniority orders liquidation preference: LOWER is more senior, so 1 is paid before 2.
	Seniority goja.Scalar `json:"seniority"`
	// ConversionRights is CONVERTS_TO_FUTURE_ROUND or CONVERTS_TO_SHARE_CLASS_ID. The second requires convertsToShareClassId.
	ConversionRights goja.Scalar `json:"conversionRights"`
	// ConvertsToShareClassID names the class this one converts into. Required when conversionRights is CONVERTS_TO_SHARE_CLASS_ID, ignored otherwise.
	ConvertsToShareClassID goja.Scalar `json:"convertsToShareClassId,omitempty"`
	// LiquidationPreferenceMultiple is the multiple of invested capital paid out before junior classes — 1 for a 1x preference.
	LiquidationPreferenceMultiple goja.Scalar `json:"liquidationPreferenceMultiple"`
	// ParticipationCapMultiple caps participation after the preference is paid, as a multiple of invested capital. 0 for no cap.
	ParticipationCapMultiple goja.Scalar `json:"participationCapMultiple"`
}

// UnmarshalJSON records the body without judging it, so the relay's 413 stays a
// decision about SIZE rather than about shape.
func (in *captableShareClassAmend) UnmarshalJSON(b []byte) error {
	type v captableShareClassAmend
	var out v
	in.Fill(maxBody, b, &out)
	out.SizedIn = in.SizedIn
	*in = captableShareClassAmend(out)
	return nil
}

// AmendShareClass replaces one share class's terms.
//
// It is a full REPLACE and not a merge, despite the PATCH: every field is written
// as sent, so a field omitted is written empty rather than left alone. Send the
// whole class. The method is PATCH because the resource is addressed by id, not
// because the body is partial — and getting that backwards silently blanks terms
// every later issuance prices against.
func (o ops) amendShareClass(ctx context.Context, in *captableShareClassAmend) (*captableUpdated, error) {
	var out captableUpdated
	err := o.write(ctx, "shareClasses.update", in.SizedIn, map[string]string{"id": in.ID}, map[string]goja.BodyField{
		"classType":                     in.ClassType,
		"name":                          in.Name,
		"initialSharesAuthorized":       in.InitialSharesAuthorized,
		"boardApprovalDate":             in.BoardApprovalDate,
		"stockholderApprovalDate":       in.StockholderApprovalDate,
		"votesPerShare":                 in.VotesPerShare,
		"parValue":                      in.ParValue,
		"pricePerShare":                 in.PricePerShare,
		"seniority":                     in.Seniority,
		"conversionRights":              in.ConversionRights,
		"convertsToShareClassId":        in.ConvertsToShareClassID,
		"liquidationPreferenceMultiple": in.LiquidationPreferenceMultiple,
		"participationCapMultiple":      in.ParticipationCapMultiple,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// captableEquityPlanIn is the body of createEquityPlan.
//
// Every field is transcribed from the bundle route this relays to
// (github.com/hanzoai/captable goja/src/routes, "equityPlans.create") — goja.Body
// assembles ONLY what is declared here, so a name that does not match is
// dropped silently rather than refused.
type captableEquityPlanIn struct {
	goja.SizedIn
	// Name is the plan's name, e.g. "2026 Stock Option Plan".
	Name goja.Scalar `json:"name"`
	// BoardApprovalDate is when the board adopted the plan, YYYY-MM-DD.
	BoardApprovalDate goja.Scalar `json:"boardApprovalDate"`
	// InitialSharesReserved is how many shares the plan may grant. A whole number.
	InitialSharesReserved goja.Scalar `json:"initialSharesReserved"`
	// ShareClassID is the class the plan grants from. It must name a class of THIS company.
	ShareClassID goja.Scalar `json:"shareClassId"`
	// DefaultCancellatonBehavior is what happens to cancelled grants: RETIRE, RETURN_TO_POOL, HOLD_AS_CAPITAL_STOCK or DEFINED_PER_PLAN_SECURITY.
	//
	// The key is spelled `defaultCancellatonBehavior` — one `l` — because that is
	// the wire, and correcting it here would silently drop the field.
	DefaultCancellatonBehavior goja.Scalar `json:"defaultCancellatonBehavior"`
	// PlanEffectiveDate is when the plan takes effect, YYYY-MM-DD. Optional.
	PlanEffectiveDate goja.Scalar `json:"planEffectiveDate,omitempty"`
	// Comments is a free-text note kept with the plan. Optional.
	Comments goja.Scalar `json:"comments,omitempty"`
}

// UnmarshalJSON records the body without judging it, so the relay's 413 stays a
// decision about SIZE rather than about shape.
func (in *captableEquityPlanIn) UnmarshalJSON(b []byte) error {
	type v captableEquityPlanIn
	var out v
	in.Fill(maxBody, b, &out)
	out.SizedIn = in.SizedIn
	*in = captableEquityPlanIn(out)
	return nil
}

// CreateEquityPlan opens an equity plan that options are granted from.
func (o ops) createEquityPlan(ctx context.Context, in *captableEquityPlanIn) (*captableCreated, error) {
	var out captableCreated
	err := o.write(ctx, "equityPlans.create", in.SizedIn, nil, map[string]goja.BodyField{
		"name":                       in.Name,
		"boardApprovalDate":          in.BoardApprovalDate,
		"initialSharesReserved":      in.InitialSharesReserved,
		"shareClassId":               in.ShareClassID,
		"defaultCancellatonBehavior": in.DefaultCancellatonBehavior,
		"planEffectiveDate":          in.PlanEffectiveDate,
		"comments":                   in.Comments,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// captableShareIn is the body of issueShares.
//
// Every field is transcribed from the bundle route this relays to
// (github.com/hanzoai/captable goja/src/routes, "shares.add") — goja.Body
// assembles ONLY what is declared here, so a name that does not match is
// dropped silently rather than refused.
type captableShareIn struct {
	goja.SizedIn
	// StakeholderID is who receives the shares. It must name a stakeholder of THIS company.
	StakeholderID goja.Scalar `json:"stakeholderId"`
	// ShareClassID is the class being issued. It must name a class of THIS company.
	ShareClassID goja.Scalar `json:"shareClassId"`
	// CertificateID is the certificate number. It must be UNIQUE within the company; reusing one is refused 409.
	CertificateID goja.Scalar `json:"certificateId"`
	// Quantity is how many shares to issue. A whole number above 0.
	Quantity goja.Scalar `json:"quantity"`
	// Status is ACTIVE, DRAFT, SIGNED or PENDING.
	Status goja.Scalar `json:"status"`
	// PricePerShare is what was paid per share. Optional.
	PricePerShare goja.Scalar `json:"pricePerShare,omitempty"`
	// CapitalContribution is cash paid for the shares. Optional.
	CapitalContribution goja.Scalar `json:"capitalContribution,omitempty"`
	// IPContribution is intellectual property assigned as consideration, valued in the company's currency. Optional.
	IPContribution goja.Scalar `json:"ipContribution,omitempty"`
	// DebtCancelled is debt forgiven as consideration. Optional.
	DebtCancelled goja.Scalar `json:"debtCancelled,omitempty"`
	// OtherContributions is any other consideration, valued in the company's currency. Optional.
	OtherContributions goja.Scalar `json:"otherContributions,omitempty"`
	// CliffYears is how long before ANY of the grant vests. A whole number; 0 for no cliff.
	CliffYears goja.Scalar `json:"cliffYears"`
	// VestingYears is the total vesting period. A whole number; 0 for fully vested at issue.
	VestingYears goja.Scalar `json:"vestingYears"`
	// CompanyLegends are the restrictive legends printed on the certificate.
	//
	// A LIST, and it must be sent as one: the bundle substitutes an EMPTY list for
	// anything that is not an array, so a single string would be accepted and
	// silently discarded — the certificate issued with no legends and nothing
	// reporting it.
	CompanyLegends goja.ScalarList `json:"companyLegends"`
	// IssueDate is when the shares were issued, YYYY-MM-DD.
	IssueDate goja.Scalar `json:"issueDate"`
	// Rule144Date is when the Rule 144 holding period starts, YYYY-MM-DD. Optional.
	Rule144Date goja.Scalar `json:"rule144Date,omitempty"`
	// VestingStartDate is when vesting begins, YYYY-MM-DD. Optional; defaults to the issue date.
	VestingStartDate goja.Scalar `json:"vestingStartDate,omitempty"`
	// BoardApprovalDate is when the board approved the issuance, YYYY-MM-DD.
	BoardApprovalDate goja.Scalar `json:"boardApprovalDate"`
}

// UnmarshalJSON records the body without judging it, so the relay's 413 stays a
// decision about SIZE rather than about shape.
func (in *captableShareIn) UnmarshalJSON(b []byte) error {
	type v captableShareIn
	var out v
	in.Fill(maxBody, b, &out)
	out.SizedIn = in.SizedIn
	*in = captableShareIn(out)
	return nil
}

// IssueShares issues a share certificate to a stakeholder.
//
// The certificate id must be UNIQUE within the company — a duplicate is refused
// 409, not silently merged — and both the stakeholder and the share class must
// belong to this company, so an id from another tenant is a 400 rather than a
// cross-company issuance.
func (o ops) issueShares(ctx context.Context, in *captableShareIn) (*captableCreated, error) {
	var out captableCreated
	err := o.write(ctx, "shares.add", in.SizedIn, nil, map[string]goja.BodyField{
		"stakeholderId":       in.StakeholderID,
		"shareClassId":        in.ShareClassID,
		"certificateId":       in.CertificateID,
		"quantity":            in.Quantity,
		"status":              in.Status,
		"pricePerShare":       in.PricePerShare,
		"capitalContribution": in.CapitalContribution,
		"ipContribution":      in.IPContribution,
		"debtCancelled":       in.DebtCancelled,
		"otherContributions":  in.OtherContributions,
		"cliffYears":          in.CliffYears,
		"vestingYears":        in.VestingYears,
		"companyLegends":      in.CompanyLegends,
		"issueDate":           in.IssueDate,
		"rule144Date":         in.Rule144Date,
		"vestingStartDate":    in.VestingStartDate,
		"boardApprovalDate":   in.BoardApprovalDate,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// captableOptionIn is the body of grantOptions.
//
// Every field is transcribed from the bundle route this relays to
// (github.com/hanzoai/captable goja/src/routes, "options.add") — goja.Body
// assembles ONLY what is declared here, so a name that does not match is
// dropped silently rather than refused.
type captableOptionIn struct {
	goja.SizedIn
	// GrantID is the grant's identifier. It must be UNIQUE within the company; reusing one is refused 409.
	GrantID goja.Scalar `json:"grantId"`
	// StakeholderID is who receives the grant. It must name a stakeholder of THIS company.
	StakeholderID goja.Scalar `json:"stakeholderId"`
	// EquityPlanID is the plan the options come from. It must name a plan of THIS company.
	EquityPlanID goja.Scalar `json:"equityPlanId"`
	// Quantity is how many options to grant. A whole number above 0.
	Quantity goja.Scalar `json:"quantity"`
	// ExercisePrice is the strike price per share, in the company's currency.
	ExercisePrice goja.Scalar `json:"exercisePrice"`
	// Type is ISO, NSO or RSU.
	Type goja.Scalar `json:"type"`
	// Status is DRAFT, ACTIVE, EXERCISED, EXPIRED or CANCELLED.
	Status goja.Scalar `json:"status"`
	// CliffYears is how long before ANY of the grant vests. A whole number; 0 for no cliff.
	CliffYears goja.Scalar `json:"cliffYears"`
	// VestingYears is the total vesting period. A whole number.
	VestingYears goja.Scalar `json:"vestingYears"`
	// IssueDate is when the grant was made, YYYY-MM-DD.
	IssueDate goja.Scalar `json:"issueDate"`
	// ExpirationDate is when unexercised options lapse, YYYY-MM-DD.
	ExpirationDate goja.Scalar `json:"expirationDate"`
	// VestingStartDate is when vesting begins, YYYY-MM-DD.
	VestingStartDate goja.Scalar `json:"vestingStartDate"`
	// BoardApprovalDate is when the board approved the grant, YYYY-MM-DD.
	BoardApprovalDate goja.Scalar `json:"boardApprovalDate"`
	// Rule144Date is when the Rule 144 holding period starts, YYYY-MM-DD.
	Rule144Date goja.Scalar `json:"rule144Date"`
	// Notes is a free-text note kept with the grant. Optional.
	Notes goja.Scalar `json:"notes,omitempty"`
}

// UnmarshalJSON records the body without judging it, so the relay's 413 stays a
// decision about SIZE rather than about shape.
func (in *captableOptionIn) UnmarshalJSON(b []byte) error {
	type v captableOptionIn
	var out v
	in.Fill(maxBody, b, &out)
	out.SizedIn = in.SizedIn
	*in = captableOptionIn(out)
	return nil
}

// GrantOptions grants options to a stakeholder from an equity plan.
func (o ops) grantOptions(ctx context.Context, in *captableOptionIn) (*captableCreated, error) {
	var out captableCreated
	err := o.write(ctx, "options.add", in.SizedIn, nil, map[string]goja.BodyField{
		"grantId":           in.GrantID,
		"stakeholderId":     in.StakeholderID,
		"equityPlanId":      in.EquityPlanID,
		"quantity":          in.Quantity,
		"exercisePrice":     in.ExercisePrice,
		"type":              in.Type,
		"status":            in.Status,
		"cliffYears":        in.CliffYears,
		"vestingYears":      in.VestingYears,
		"issueDate":         in.IssueDate,
		"expirationDate":    in.ExpirationDate,
		"vestingStartDate":  in.VestingStartDate,
		"boardApprovalDate": in.BoardApprovalDate,
		"rule144Date":       in.Rule144Date,
		"notes":             in.Notes,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// captableSafeIn is the body of recordSafe.
//
// Every field is transcribed from the bundle route this relays to
// (github.com/hanzoai/captable goja/src/routes, "safes.create") — goja.Body
// assembles ONLY what is declared here, so a name that does not match is
// dropped silently rather than refused.
type captableSafeIn struct {
	goja.SizedIn
	// PublicID is the SAFE's identifier. It must be UNIQUE within the company; reusing one is refused 409.
	PublicID goja.Scalar `json:"publicId"`
	// StakeholderID is the investor. It must name a stakeholder of THIS company.
	StakeholderID goja.Scalar `json:"stakeholderId"`
	// Capital is the money invested, in the company's currency.
	Capital goja.Scalar `json:"capital"`
	// Type is PRE_MONEY or POST_MONEY — which side of the new money the cap is measured on.
	Type goja.Scalar `json:"type"`
	// Status is DRAFT, ACTIVE, PENDING, EXPIRED or CANCELLED.
	Status goja.Scalar `json:"status"`
	// IssueDate is when the SAFE was signed, YYYY-MM-DD.
	IssueDate goja.Scalar `json:"issueDate"`
	// BoardApprovalDate is when the board approved it, YYYY-MM-DD.
	BoardApprovalDate goja.Scalar `json:"boardApprovalDate"`
	// ValuationCap is the valuation the SAFE converts at, at most. Optional; omit for an uncapped SAFE.
	ValuationCap goja.Scalar `json:"valuationCap,omitempty"`
	// DiscountRate is the discount to the next round's price, as a percentage. Optional.
	DiscountRate goja.Scalar `json:"discountRate,omitempty"`
	// AdditionalTerms is free text for anything the fields above do not carry. Optional.
	AdditionalTerms goja.Scalar `json:"additionalTerms,omitempty"`
}

// UnmarshalJSON records the body without judging it, so the relay's 413 stays a
// decision about SIZE rather than about shape.
func (in *captableSafeIn) UnmarshalJSON(b []byte) error {
	type v captableSafeIn
	var out v
	in.Fill(maxBody, b, &out)
	out.SizedIn = in.SizedIn
	*in = captableSafeIn(out)
	return nil
}

// RecordSafe records a SAFE — a simple agreement for future equity.
func (o ops) recordSafe(ctx context.Context, in *captableSafeIn) (*captableCreated, error) {
	var out captableCreated
	err := o.write(ctx, "safes.create", in.SizedIn, nil, map[string]goja.BodyField{
		"publicId":          in.PublicID,
		"stakeholderId":     in.StakeholderID,
		"capital":           in.Capital,
		"type":              in.Type,
		"status":            in.Status,
		"issueDate":         in.IssueDate,
		"boardApprovalDate": in.BoardApprovalDate,
		"valuationCap":      in.ValuationCap,
		"discountRate":      in.DiscountRate,
		"additionalTerms":   in.AdditionalTerms,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// captableConvertibleIn is the body of recordConvertible.
//
// Every field is transcribed from the bundle route this relays to
// (github.com/hanzoai/captable goja/src/routes, "convertibles.create") — goja.Body
// assembles ONLY what is declared here, so a name that does not match is
// dropped silently rather than refused.
type captableConvertibleIn struct {
	goja.SizedIn
	// PublicID is the note's identifier. It must be UNIQUE within the company; reusing one is refused 409.
	PublicID goja.Scalar `json:"publicId"`
	// StakeholderID is the lender. It must name a stakeholder of THIS company.
	StakeholderID goja.Scalar `json:"stakeholderId"`
	// Capital is the principal lent, in the company's currency.
	Capital goja.Scalar `json:"capital"`
	// Type is CCD, OCD or NOTE.
	Type goja.Scalar `json:"type"`
	// Status is DRAFT, ACTIVE, PENDING, EXPIRED or CANCELLED.
	Status goja.Scalar `json:"status"`
	// IssueDate is when the note was issued, YYYY-MM-DD.
	IssueDate goja.Scalar `json:"issueDate"`
	// BoardApprovalDate is when the board approved it, YYYY-MM-DD.
	BoardApprovalDate goja.Scalar `json:"boardApprovalDate"`
	// ConversionCap is the valuation the note converts at, at most. Optional.
	ConversionCap goja.Scalar `json:"conversionCap,omitempty"`
	// DiscountRate is the discount to the next round's price, as a percentage. Optional.
	DiscountRate goja.Scalar `json:"discountRate,omitempty"`
	// InterestRate is the annual interest the principal accrues, as a percentage. Optional.
	InterestRate goja.Scalar `json:"interestRate,omitempty"`
	// AdditionalTerms is free text for anything the fields above do not carry. Optional.
	AdditionalTerms goja.Scalar `json:"additionalTerms,omitempty"`
}

// UnmarshalJSON records the body without judging it, so the relay's 413 stays a
// decision about SIZE rather than about shape.
func (in *captableConvertibleIn) UnmarshalJSON(b []byte) error {
	type v captableConvertibleIn
	var out v
	in.Fill(maxBody, b, &out)
	out.SizedIn = in.SizedIn
	*in = captableConvertibleIn(out)
	return nil
}

// RecordConvertible records a convertible note.
func (o ops) recordConvertible(ctx context.Context, in *captableConvertibleIn) (*captableCreated, error) {
	var out captableCreated
	err := o.write(ctx, "convertibles.create", in.SizedIn, nil, map[string]goja.BodyField{
		"publicId":          in.PublicID,
		"stakeholderId":     in.StakeholderID,
		"capital":           in.Capital,
		"type":              in.Type,
		"status":            in.Status,
		"issueDate":         in.IssueDate,
		"boardApprovalDate": in.BoardApprovalDate,
		"conversionCap":     in.ConversionCap,
		"discountRate":      in.DiscountRate,
		"interestRate":      in.InterestRate,
		"additionalTerms":   in.AdditionalTerms,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// captableRoundIn is the body of openRound.
//
// Every field is transcribed from the bundle route this relays to
// (github.com/hanzoai/captable goja/src/routes, "rounds.create") — goja.Body
// assembles ONLY what is declared here, so a name that does not match is
// dropped silently rather than refused.
type captableRoundIn struct {
	goja.SizedIn
	// Name is the round's name, e.g. "Series A".
	Name goja.Scalar `json:"name"`
	// RoundType is PRICED, SAFE or CONVERTIBLE.
	RoundType goja.Scalar `json:"roundType"`
	// TargetAmount is how much the round aims to raise, in the company's currency.
	TargetAmount goja.Scalar `json:"targetAmount"`
	// ShareClassID is the class investors receive. It must name a class of THIS company.
	ShareClassID goja.Scalar `json:"shareClassId"`
	// PricePerShare is the price investors pay per share. An investment into a round with a price ISSUES the shares it buys; one into a round priced later records the money alone.
	PricePerShare goja.Scalar `json:"pricePerShare"`
	// PreMoneyValuation is the company's valuation before the new money. Optional.
	PreMoneyValuation goja.Scalar `json:"preMoneyValuation,omitempty"`
}

// UnmarshalJSON records the body without judging it, so the relay's 413 stays a
// decision about SIZE rather than about shape.
func (in *captableRoundIn) UnmarshalJSON(b []byte) error {
	type v captableRoundIn
	var out v
	in.Fill(maxBody, b, &out)
	out.SizedIn = in.SizedIn
	*in = captableRoundIn(out)
	return nil
}

// OpenRound opens a priced round that investments can be added to.
//
// The round opens OPEN; investing into a closed one is refused.
func (o ops) openRound(ctx context.Context, in *captableRoundIn) (*captableCreated, error) {
	var out captableCreated
	err := o.write(ctx, "rounds.create", in.SizedIn, nil, map[string]goja.BodyField{
		"name":              in.Name,
		"roundType":         in.RoundType,
		"targetAmount":      in.TargetAmount,
		"shareClassId":      in.ShareClassID,
		"pricePerShare":     in.PricePerShare,
		"preMoneyValuation": in.PreMoneyValuation,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// captableCreated is what a create answers: the id of the row it wrote, plus the
// cap table's own sentence.
//
// The id is the fact a caller needs — every later write addresses the row by it —
// and it is the reason a create cannot answer the bare {message, success} an
// update does.
type captableCreated struct {
	// ID is the created row's id.
	ID string `json:"id"`
	// Message is the human sentence the cap table wrote, e.g. "Share issued".
	Message string `json:"message"`
	// Success is true when the row was written.
	Success bool `json:"success"`
}
