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
	"encoding/json"
	"net/http"

	"github.com/zap-proto/zip"
)

// scalar is ONE JSON value carried from the caller to the bundle unchanged —
// the token exactly as it arrived, quotes and all: `"Acme"`, `123`, `null`.
//
// It exists because the bundle's string helpers are LENIENT in a way no Go type
// is. `optString` takes any scalar and calls String(v), so `{"taxId":12345}`
// stores "12345" today; a `*string` field would refuse it with a 400, making the
// route accept LESS. And `reqString`/`oneOf` REFUSE a non-string, so a `*string`
// field that refused it first would answer in zip's envelope instead of the
// bundle's {success,message,errors}. Carrying the token verbatim keeps both: the
// bundle sees what the caller sent and stays the only judge of it.
//
// A Go string is the carrier because it is a string KIND, so every projection
// describes the field as `string` — which is what these fields are. The
// leniency is not in the schema; it is named in each field's own prose.
//
// The zero value means ABSENT — no key was on the wire — which is why every
// field below is `omitempty` and non-pointer. A pointer would collapse the
// distinction the bundle's partial update depends on: encoding/json sets a
// pointer field to nil for an explicit `null` WITHOUT calling UnmarshalJSON, so
// `{"city":null}` and `{}` would arrive identically, and stakeholders.update
// reads them differently (`!== undefined` is true for null, which clears the
// column). A non-pointer scalar records `null` as the four bytes `null`, and an
// empty JSON string as the two bytes `""`, so neither can be confused with
// absent.
type scalar string

// UnmarshalJSON keeps the caller's bytes. It cannot fail: whatever the caller
// sent for this field is the bundle's to judge, so nothing is rejected here.
func (s *scalar) UnmarshalJSON(b []byte) error {
	*s = scalar(b)
	return nil
}

// MarshalJSON writes the carried token back exactly as it arrived. A value that
// did NOT come off the wire — a hand-built op input, a URL param bound by
// zip's setScalar — is not a JSON token but the string it spells, so it is
// quoted. That keeps the type total: every scalar marshals to valid JSON.
func (s scalar) MarshalJSON() ([]byte, error) {
	if json.Valid([]byte(s)) {
		return []byte(s), nil
	}
	return json.Marshal(string(s))
}

// sizedIn is the request-size half of an input, carried by every op below.
//
// The relay capped a body at maxBody and answered 413 (dispatch), and it did so
// AFTER resolving the tenant. A typed op never sees the request, so the size is
// recorded where the bytes are — the input's own UnmarshalJSON — and read back
// after tenantOf, which is what keeps a 403 ahead of a 413 for the caller that
// has both problems. The field is unexported, so it reaches no schema: it is not
// something a caller sends.
type sizedIn struct{ oversize bool }

// fill decodes the caller's object into v, records whether it exceeded maxBody,
// and NEVER refuses the body. It is the one decode path for the inputs below.
//
// A body that is not an object — an array, a bare scalar, `null` — leaves every
// field absent, which is exactly what the relay did: it decoded into `any` and
// the bundle's asObj turned anything that was not an object into `{}`. The
// bundle then answers, in its own envelope, the same "name is required" it
// always did. A type error on ONE key is saved and decoding continues, so a body
// that echoes `"id":123` back at a PATCH still delivers the fields beside it —
// as the relay did, since the URL carries the id and the bundle never read one
// from the body.
func (s *sizedIn) fill(b []byte, v any) {
	s.oversize = len(b) > maxBody
	// The error is deliberately dropped, and dropping it is the wire: see above.
	_ = json.Unmarshal(b, v)
}

// bundleBody assembles the object the bundle validates from the caller's
// verbatim tokens. A field left at its zero value was never on the wire, so it
// contributes NO key — the `undefined` that a partial update reads.
//
// The result is a plain Go value, not bytes, because that is what crosses into
// goja: the same shape the untyped relay handed the host after decoding the
// caller's bytes into `any`. Key order is lost to the map on the way, and cannot
// matter — the bundle reads its fields by name and echoes no request body back.
func bundleBody(fields map[string]scalar) (any, error) {
	obj := make(map[string]json.RawMessage, len(fields))
	for k, v := range fields {
		if v == "" {
			continue // absent: this key was never on the wire
		}
		raw, err := v.MarshalJSON()
		if err != nil {
			return nil, err
		}
		obj[k] = raw
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var body any
	if err := json.Unmarshal(b, &body); err != nil {
		return nil, err
	}
	return body, nil
}

// write is the ONE response path for a body-carrying typed op: refuse an
// oversized body with the relay's 413, run the bundle route on the caller's
// tenant with the assembled body, and decode the 2xx answer into out. A non-2xx
// is the BUNDLE's, relayed through bundleErr exactly as the reads do.
func (o ops) write(ctx context.Context, route string, size sizedIn, params map[string]string, fields map[string]scalar, out any) error {
	org, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	// After the tenant, before the work — the order the relay used.
	if size.oversize {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	body, err := bundleBody(fields)
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
	sizedIn
	// IncorporationCountry is the ISO country the entity is incorporated in.
	// Optional; omitted, null or empty clears it. Any JSON scalar is accepted
	// and stored as its text.
	IncorporationCountry scalar `json:"incorporationCountry,omitempty"`
	// IncorporationState is the state or province of incorporation. Optional;
	// omitted, null or empty clears it. Any JSON scalar is accepted and stored
	// as its text.
	IncorporationState scalar `json:"incorporationState,omitempty"`
	// IncorporationType is the entity kind, e.g. LLC or C_CORP. Optional;
	// omitted, null or empty clears it. Any JSON scalar is accepted and stored
	// as its text.
	IncorporationType scalar `json:"incorporationType,omitempty"`
	// Name is the company's legal name. Required, and it must be a non-empty
	// string — anything else is refused with the cap table's own validation
	// error.
	Name scalar `json:"name,omitempty"`
}

// UnmarshalJSON keeps the caller's tokens and records the body size; it refuses
// nothing, leaving every judgement to the cap table. See sizedIn.fill.
func (in *captableCompanyUpdate) UnmarshalJSON(b []byte) error {
	type body captableCompanyUpdate // sheds the method, so this does not recurse
	var v body
	in.fill(b, &v)
	v.sizedIn = in.sizedIn
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
	err := o.write(ctx, "company.update", in.sizedIn, nil, map[string]scalar{
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
	sizedIn
	// City is the stakeholder's city.
	City scalar `json:"city,omitempty"`
	// CurrentRelationship is how the stakeholder relates to the company, e.g.
	// FOUNDER, INVESTOR or EMPLOYEE. This route stores it as sent — unlike
	// adding a stakeholder, it is not checked against the vocabulary.
	CurrentRelationship scalar `json:"currentRelationship,omitempty"`
	// Email is the stakeholder's email. This route stores it as sent — unlike
	// adding a stakeholder, it is not checked for shape or uniqueness.
	Email scalar `json:"email,omitempty"`
	// ID is the stakeholder to update. It is the path segment: the URL is the
	// addressing authority, and the org it is resolved in comes from the
	// caller's principal, so an id from another tenant is simply not found.
	ID string `json:"id"`
	// InstitutionName names the institution, when the stakeholder is one.
	InstitutionName scalar `json:"institutionName,omitempty"`
	// Name is the stakeholder's full name.
	Name scalar `json:"name,omitempty"`
	// StakeholderType is INDIVIDUAL or INSTITUTION. This route stores it as
	// sent — unlike adding a stakeholder, it is not checked against the
	// vocabulary.
	StakeholderType scalar `json:"stakeholderType,omitempty"`
	// State is the stakeholder's state or province.
	State scalar `json:"state,omitempty"`
	// StreetAddress is the stakeholder's street address.
	StreetAddress scalar `json:"streetAddress,omitempty"`
	// TaxID is the stakeholder's tax identifier.
	TaxID scalar `json:"taxId,omitempty"`
	// Zipcode is the stakeholder's postal code.
	Zipcode scalar `json:"zipcode,omitempty"`
}

// UnmarshalJSON keeps the caller's tokens and records the body size; it refuses
// nothing, leaving every judgement to the cap table. See sizedIn.fill.
func (in *captableStakeholderPatch) UnmarshalJSON(b []byte) error {
	type body captableStakeholderPatch // sheds the method, so this does not recurse
	var v body
	in.fill(b, &v)
	v.sizedIn = in.sizedIn
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
	err := o.write(ctx, "stakeholders.update", in.sizedIn, map[string]string{"id": in.ID}, map[string]scalar{
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
	sizedIn
	// CloseDate is the date to record the round as closed on. Optional: omitted,
	// null or empty records TODAY. Any JSON scalar is accepted and stored as its
	// text, and the text is stored unparsed, so a caller that wants an ISO date
	// sends one.
	CloseDate scalar `json:"closeDate,omitempty"`
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
	in.fill(b, &v)
	v.sizedIn = in.sizedIn
	*in = captableRoundCloseRequest(v)
	return nil
}

// CloseRound closes one of the caller org's fundraising rounds, recording the
// close date and moving its status to CLOSED. Only an OPEN round can be closed:
// a round that is already closed — like an id this org does not hold — is not
// found. Closing a round does not change what was invested in it.
func (o ops) closeRound(ctx context.Context, in *captableRoundCloseRequest) (*captableUpdated, error) {
	var out captableUpdated
	err := o.write(ctx, "rounds.close", in.sizedIn, map[string]string{"id": in.ID}, map[string]scalar{
		"closeDate": in.CloseDate,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
