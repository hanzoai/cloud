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

	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/apps/principal"
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
// the fleet door and not create one.

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
