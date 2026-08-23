package trust

// The typed-op client for /v1/trust.
//
// A typed op (zip.Get[In, Out]) is ONE registry entry with N projections — the
// REST route, the OpenAPI operation's schema AND prose, the MCP tool, the CLI
// command and the generated SDK method all follow from it. An untyped route
// publishes an address and nothing else.
//
// ONE RULE decides whether a value is typed here or carried verbatim, and it is
// about who owns the shape:
//
//   - The HOST computes it — every count, every coverage row, every framework
//     row, and the document view (which is where the tier rule is applied). Those
//     are this surface's own answers, their shape is fixed by this code, and they
//     are exactly what a badge, an SDK and a page read. They are TYPED.
//   - A TENANT authored it — a control, a policy, a subprocessor, a question, an
//     update, the profile. Their required fields are validated, extra fields are
//     the organization's own, and a Go struct restating one would silently drop
//     whatever a tenant added. Those ride as json.RawMessage.
//
// json.RawMessage and NOT map[string]any: zip asks whether a type marshals
// itself before it asks what it is made of, so a RawMessage publishes `{}` —
// "any JSON", which is true — while map[string]any publishes
// `additionalProperties: {"type":"object"}`, which asserts every value is an
// object and is refuted by the first `"published": true` in a profile. A false
// schema is worse than a thin one.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// ops binds the typed ops to a receiver. A zip.TypedHandler takes no parameter
// for the subsystem, so what an op needs arrives as a receiver — and a method
// value is the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// ---- inputs ----

// noInput is the In of an op that takes nothing off the wire. Its tenant comes
// from the context, never from a field.
type noInput struct{}

// orgRef addresses one organization's PUBLISHED trust centre. This is the one
// place an org arrives from the request, and it is not an authority claim: a
// published trust centre is a public document addressed by a public name, the
// way a site is addressed by its slug. It reaches only the published view, and
// only for an organization that has published one.
type orgRef struct {
	// Org is the organization's slug — the name in its address.
	Org string `json:"org"`
}

// controlRef addresses one control by its dotted id, e.g. iam.pkce.s256.
type controlRef struct {
	// ID is the control's id, dotted lowercase — "iam.pkce.s256".
	ID string `json:"id"`
}

// frameworkRef addresses one framework's clause-by-clause coverage.
type frameworkRef struct {
	// Framework is the framework id — "soc2", "iso27001", "nist80053".
	Framework string `json:"framework"`
}

// evidenceQuery selects the audit rows that stand behind one control.
type evidenceQuery struct {
	// Control is the control id whose trail to read. Required.
	Control string `json:"control"`
	// From is the inclusive lower bound, an RFC 3339 date or instant
	// ("2026-01-01" or "2026-01-01T00:00:00Z"). Empty leaves it unbounded. A
	// malformed bound is refused rather than silently widening the window.
	From string `json:"from"`
	// To is the upper bound, same form and same tolerance.
	To string `json:"to"`
	// Limit caps the rows returned, 1..1000, default 100. It is a string because
	// an unparseable value is refused rather than read as zero.
	Limit string `json:"limit"`
}

// sectionRef addresses one record in one section of the caller's own centre.
type sectionRef struct {
	// Kind is the section — profile, control, document, subprocessor, policy,
	// faq, update or risk. Anything else is not found. The URL is the authority:
	// a value here is bound from the path, which zip binds last.
	Kind string `json:"kind"`
	// ID is the record's id within that section. The single-valued sections
	// (profile, risk) hold one record whatever id is named.
	ID string `json:"id"`
}

// sectionWrite is sectionRef plus the record itself.
//
// The record is nested under `data` rather than being the body, and that is
// forced rather than chosen: a record's fields are the ORGANIZATION's — a
// control has one shape, a subprocessor another, and a tenant may carry fields
// of its own — so the body is an open object, and zip binds a path parameter by
// walking the input's STRUCT FIELDS. An open-object In binds no :kind and a
// struct In cannot hold an open object. Nesting it resolves both halves: the
// envelope is a struct the URL binds onto, and `data` is json.RawMessage, which
// publishes `{}` — any JSON, the only true thing to say about it.
type sectionWrite struct {
	// Kind is the section being written. The URL is the authority.
	Kind string `json:"kind"`
	// ID is the record's id. Omit it on a create and one is minted; the
	// single-valued sections (profile, risk) hold one record whatever is named.
	ID string `json:"id"`
	// Ord orders this record within its section, ascending, ties broken by id.
	// It is the organization's own ordering — the page renders in it.
	Ord int `json:"ord"`
	// Data is the record. What may be in it depends on the section, and every
	// section's required fields are validated: a control is held to the same rule
	// a committed one is, a subprocessor must say what it is for, an update must
	// carry the date it describes.
	Data json.RawMessage `json:"data"`
}

// ---- host-computed shapes ----

// trustTally is how the controls themselves stand, independent of any framework.
type trustTally struct {
	// Total is how many controls this organization publishes.
	Total int `json:"total"`
	// Automated is how many run with nobody in the loop.
	Automated int `json:"automated"`
	// Partial is how many run but do not cover their whole claim. Each says what
	// is missing.
	Partial int `json:"partial"`
	// Absent is how many the organization does not have. An absent control still
	// names the clause it would satisfy — that is a roadmap — but it never moves
	// a coverage number.
	Absent int `json:"absent"`
	// Unverified is how many rest on somebody having READ the source rather than
	// on a test or an audit row. Only a check that can FAIL counts as verified,
	// and coverage counts those one rung weaker than they claim to be.
	Unverified int `json:"unverified"`
	// Statement is the counts as one sentence, safe to quote.
	Statement string `json:"statement"`
}

// coverRow is one framework's counts — what a badge reads.
type coverRow struct {
	// Framework is the framework id — "soc2", "iso27001", "nist80053".
	Framework string `json:"framework"`
	// Name is the published standard's name.
	Name string `json:"name"`
	// Publisher is who publishes it — AICPA, ISO/IEC, NIST.
	Publisher string `json:"publisher"`
	// Edition is which edition the clause list is taken from.
	Edition string `json:"edition"`
	// Unit is what ONE clause is — "criterion", "control", "family". A count
	// without its unit is not a fact, so it travels with every number here.
	Unit string `json:"unit"`
	// Units is the plural of Unit, for rendering a sentence.
	Units string `json:"units"`
	// Total is the framework's WHOLE published clause list — the denominator.
	// Counting only the clauses some control happened to name would report 100%
	// every time.
	Total int `json:"total"`
	// Automated is how many clauses have an automated control behind them that
	// something can fail on behalf of.
	Automated int `json:"automated"`
	// Partial is how many are answered in part.
	Partial int `json:"partial"`
	// None is how many have nothing behind them. It stays visible rather than
	// dropping out of the fraction.
	None int `json:"none"`
	// Statement is the counts as one sentence, carrying the unit.
	Statement string `json:"statement"`
	// Note is what the clause list itself is scoped to, when the framework's
	// catalog says something a count alone would misrepresent.
	Note string `json:"note,omitempty"`
}

// clauseRow is one clause of one framework and what stands behind it.
type clauseRow struct {
	// ID is the clause id as the standard publishes it — "CC6.1", "A.5.15", "AC".
	ID string `json:"id"`
	// Title is the standard's own words for that clause.
	Title string `json:"title"`
	// Group is the clause's section within the standard, when it has one.
	Group string `json:"group,omitempty"`
	// Level is what the strongest control pointed at this clause is worth:
	// "automated", "partial" or "none".
	Level string `json:"level"`
	// Controls are the ids behind it, strongest first. Empty when nothing covers
	// it — and an absent control is never listed here, however it maps.
	Controls []string `json:"controls"`
}

// frameworkRow is a framework and the size of its clause list, with no coverage.
type frameworkRow struct {
	// Framework is the framework id.
	Framework string `json:"framework"`
	// Name is the published standard's name.
	Name string `json:"name"`
	// Publisher is who publishes it.
	Publisher string `json:"publisher"`
	// Edition is which edition this clause list is taken from.
	Edition string `json:"edition"`
	// Unit is what one clause is; Units is its plural.
	Unit string `json:"unit"`
	// Units is the plural of Unit.
	Units string `json:"units"`
	// Total is how many clauses the standard publishes.
	Total int `json:"total"`
}

// docRow is a document as a READER sees it. This is where the tier rule lands:
// an artifact an independent auditor signed keeps its title and its date and
// loses its address, so "available on request" is a checkable fact rather than
// a phrase on a page.
type docRow struct {
	// ID is the document's id within this organization's centre.
	ID string `json:"id"`
	// Title is what the document is called.
	Title string `json:"title"`
	// Kind is the artifact type — soc2, iso, pentest, letter, caiq, sig, vsa,
	// questionnaire, policy or other.
	Kind string `json:"kind"`
	// Label is the artifact type in words, for rendering.
	Label string `json:"label"`
	// Attested reports whether somebody OUTSIDE this organization put their name
	// to it. Those are the artifacts a reviewer asks for, and they are released
	// through a grant rather than published — there is no field that can say
	// otherwise.
	Attested bool `json:"attested"`
	// Tier is "public" or "gated". It defaults to gated, so a new artifact is
	// closed until somebody opens it deliberately.
	Tier string `json:"tier"`
	// Updated is when the record last changed, unix milliseconds.
	Updated int64 `json:"updated"`
	// Note is anything the organization says about this artifact.
	Note string `json:"note,omitempty"`
	// Href is where to read it, present only when this reader may.
	Href string `json:"href,omitempty"`
	// Released reports whether THIS reader may read it. False means the artifact
	// exists and is available on request.
	Released bool `json:"released"`
}

// ---- answers ----

// centre is a whole trust centre in one document.
type centre struct {
	// Version is the embedded inventory's version.
	Version string `json:"version"`
	// Generated is when this answer was computed, unix milliseconds.
	Generated int64 `json:"generated"`
	// Org is whose centre this is.
	Org string `json:"org"`
	// Profile is the organization's own description of itself.
	Profile json.RawMessage `json:"profile"`
	// Controls is the control inventory, each entry naming what it asserts, the
	// mechanism, where it is enforced, how it is verified and the clauses it maps
	// to.
	Controls []json.RawMessage `json:"controls"`
	// Coverage is the per-framework counts, computed from Controls against each
	// framework's whole published clause list.
	Coverage []coverRow `json:"coverage"`
	// Inventory is how the controls themselves stand, independent of framework.
	Inventory trustTally `json:"inventory"`
	// Frameworks are the clause universes the coverage is computed against.
	Frameworks []frameworkRow `json:"frameworks"`
	// Documents are the artifacts, each saying whether this reader may read it.
	Documents []docRow `json:"documents"`
	// Subprocessors are the third parties this organization sends data to.
	Subprocessors []json.RawMessage `json:"subprocessors"`
	// Policies are the published policies.
	Policies []json.RawMessage `json:"policies"`
	// Faq is the knowledge base — the questions a reviewer asks, answered.
	Faq []json.RawMessage `json:"faq"`
	// Updates is the changelog, newest as the organization ordered it.
	Updates []json.RawMessage `json:"updates"`
	// Risk is the risk profile — label and value pairs describing what this
	// organization handles and how.
	Risk json.RawMessage `json:"risk"`
}

// controlList is the inventory plus its own counts.
type controlList struct {
	// Version is the embedded inventory's version.
	Version string `json:"version"`
	// Total is how many controls this organization publishes.
	Total int `json:"total"`
	// Automated is how many run with nobody in the loop.
	Automated int `json:"automated"`
	// Partial is how many run but do not cover their whole claim.
	Partial int `json:"partial"`
	// Absent is how many the organization does not have. Each still names the
	// clause it would satisfy, and none of them moves a coverage number.
	Absent int `json:"absent"`
	// Unverified is how many rest on somebody having read the source rather than
	// on a test or an audit row.
	Unverified int `json:"unverified"`
	// Statement is the counts as one sentence, safe to quote.
	Statement string `json:"statement"`
	// Controls is every control, opaque because the organization owns its shape.
	Controls []json.RawMessage `json:"controls"`
}

// trustCoverage is every framework's counts. Nothing here is a verdict: there is no
// boolean, and there will not be one.
type trustCoverage struct {
	// Version is the embedded inventory's version.
	Version string `json:"version"`
	// Generated is when this was computed, unix milliseconds.
	Generated int64 `json:"generated"`
	// Controls is how the controls stand, independent of any framework.
	Controls trustTally `json:"controls"`
	// Frameworks is the per-framework counts.
	Frameworks []coverRow `json:"frameworks"`
}

// clauseCoverage is one framework, clause by clause, so a number can be checked
// line by line rather than taken on trust.
type clauseCoverage struct {
	// Version is the embedded inventory's version.
	Version string `json:"version"`
	// Generated is when this was computed, unix milliseconds.
	Generated int64 `json:"generated"`
	// Framework is the framework id — "soc2", "iso27001", "nist80053".
	Framework string `json:"framework"`
	// Name is the published standard's name.
	Name string `json:"name"`
	// Publisher is who publishes it — AICPA, ISO/IEC, NIST.
	Publisher string `json:"publisher"`
	// Edition is which edition this clause list is taken from.
	Edition string `json:"edition"`
	// Unit is what ONE clause is — "criterion", "control", "family".
	Unit string `json:"unit"`
	// Units is the plural of Unit, for rendering a sentence.
	Units string `json:"units"`
	// Total is the framework's WHOLE published clause list — the denominator.
	Total int `json:"total"`
	// Automated is how many clauses have an automated control behind them that
	// something can fail on behalf of.
	Automated int `json:"automated"`
	// Partial is how many are answered in part.
	Partial int `json:"partial"`
	// None is how many have nothing behind them.
	None int `json:"none"`
	// Statement is the counts as one sentence, carrying the unit.
	Statement string `json:"statement"`
	// Note is what this clause list is scoped to, when a count alone would
	// misrepresent it.
	Note string `json:"note,omitempty"`
	// Clauses is every clause the standard publishes, with what stands behind it.
	Clauses []clauseRow `json:"clauses"`
}

// frameworkList is the clause universes, without coverage.
type frameworkList struct {
	// Frameworks is each framework and how many clauses it publishes.
	Frameworks []frameworkRow `json:"frameworks"`
}

// trustDocuments lists the artifacts and says whether this reader may read each
// one. The name carries the product because the schema namespace is flat and
// fleet-wide: `documentList` is already framework's, and one name meaning two
// shapes is a refusal at the compose.
type trustDocuments struct {
	// Documents is the list; a gated entry carries no address.
	Documents []docRow `json:"documents"`
}

// subprocessorList is the third parties this organization sends data to.
type subprocessorList struct {
	// Subprocessors is the list, each naming at least what it is and what it is
	// for — a name alone says nothing.
	Subprocessors []json.RawMessage `json:"subprocessors"`
}

// policyList is the published policies.
type policyList struct {
	// Policies is the list.
	Policies []json.RawMessage `json:"policies"`
}

// faqList is the knowledge base.
type faqList struct {
	// Faq is the questions and their answers.
	Faq []json.RawMessage `json:"faq"`
}

// updateList is the changelog.
type updateList struct {
	// Updates is the entries, each carrying the date it describes.
	Updates []json.RawMessage `json:"updates"`
}

// written is the receipt for a write.
type written struct {
	// Kind is the section written.
	Kind string `json:"kind"`
	// ID is the record's id — the one supplied, or the one minted when the
	// caller supplied none.
	ID string `json:"id"`
	// Updated is when it was written, unix milliseconds.
	Updated int64 `json:"updated"`
}

// dropped is the receipt for a delete.
type dropped struct {
	// Kind is the section.
	Kind string `json:"kind"`
	// ID is the record removed.
	ID string `json:"id"`
	// Deleted is always true; a record that was not there is a 404 instead.
	Deleted bool `json:"deleted"`
}

// ---- the ops ----

// Reads a published trust centre — the whole thing in one answer: the
// organization's profile, its control inventory, coverage computed against each
// framework's whole published clause list, its documents, subprocessors,
// policies, knowledge base, updates and risk profile.
//
// This is the PUBLIC endpoint and needs no credential, because a published trust
// centre is a public document. It answers only for an organization that has
// published one — an organization that has not is not found rather than empty,
// since an empty centre and a centre nobody meant to show read the same and are
// not the same thing.
//
// A gated document appears here with its title, its type and its date and NO
// address: the listing says the artifact exists and that reading it takes a
// grant. Nothing an independent auditor signed is ever released through this
// endpoint.
//
// Example: {"org":"hanzo"}
func (o ops) published(ctx context.Context, in *orgRef) (*centre, error) {
	return as[centre](ctx, o, in.Org, "published", nil)
}

// Reads YOUR organization's whole trust centre, including the addresses of your
// own gated documents. Same shape as the published endpoint; the difference is that
// this one is resolved from your validated bearer and shows you your own
// artifacts.
func (o ops) center(ctx context.Context, _ *noInput) (*centre, error) {
	return mine[centre](ctx, o, "center", nil)
}

// Reads your organization's trust-centre profile — the name, tagline and
// summary a visitor sees, whether the centre is published, and where to send
// somebody who wants a gated document.
func (o ops) profile(ctx context.Context, _ *noInput) (*json.RawMessage, error) {
	return mine[json.RawMessage](ctx, o, "profile.get", nil)
}

// Lists every control your organization publishes, with the counts.
//
// A control names what it asserts, the mechanism behind it, the repository and
// file where that mechanism is enforced, how it is verified, and the framework
// clauses it maps to. Status is automated, partial or absent — and an absent one
// still names the clause it would satisfy, which is a roadmap, while never
// moving a coverage number.
func (o ops) listControls(ctx context.Context, _ *noInput) (*controlList, error) {
	return mine[controlList](ctx, o, "controls.list", nil)
}

// Reads one control by id.
//
// Example: {"id":"iam.pkce.s256"}
func (o ops) getControl(ctx context.Context, in *controlRef) (*json.RawMessage, error) {
	return mine[json.RawMessage](ctx, o, "controls.get", map[string]string{"id": in.ID})
}

// Lists the frameworks coverage is computed against, and how many clauses each
// publishes. That count is the denominator of every coverage number, which is
// what keeps an uncovered clause visible instead of dropping out of the
// fraction.
func (o ops) listFrameworks(ctx context.Context, _ *noInput) (*frameworkList, error) {
	return mine[frameworkList](ctx, o, "frameworks.list", nil)
}

// Reads coverage: per framework, how many clauses have an automated control
// behind them, how many are partial, and how many have none — each carrying the
// unit it is counted in, because "12 of 20" is not a fact until you know what
// the 20 are.
//
// Nothing here is a verdict. There is no boolean, and a control that only a
// person has read counts one rung weaker than it claims to be, because only a
// check that can FAIL is evidence.
func (o ops) coverage(ctx context.Context, _ *noInput) (*trustCoverage, error) {
	return mine[trustCoverage](ctx, o, "coverage.list", nil)
}

// Reads one framework clause by clause: every clause the standard publishes,
// what covers it, and which controls stand behind it — so a coverage number can
// be checked line by line rather than taken on trust.
//
// Example: {"framework":"soc2"}
func (o ops) frameworkCoverage(ctx context.Context, in *frameworkRef) (*clauseCoverage, error) {
	return mine[clauseCoverage](ctx, o, "coverage.get", map[string]string{"framework": in.Framework})
}

// Lists your organization's documents. Because this is your own centre, a gated
// artifact carries its address here; through the published endpoint it does not.
func (o ops) listDocuments(ctx context.Context, _ *noInput) (*trustDocuments, error) {
	return mine[trustDocuments](ctx, o, "documents.list", nil)
}

// Lists the third parties your organization sends data to, each naming what it
// is for.
func (o ops) listSubprocessors(ctx context.Context, _ *noInput) (*subprocessorList, error) {
	return mine[subprocessorList](ctx, o, "subprocessors.list", nil)
}

// Lists your organization's published policies.
func (o ops) listPolicies(ctx context.Context, _ *noInput) (*policyList, error) {
	return mine[policyList](ctx, o, "policies.list", nil)
}

// Lists your knowledge base — the questions a reviewer asks, answered once.
func (o ops) listFaq(ctx context.Context, _ *noInput) (*faqList, error) {
	return mine[faqList](ctx, o, "faq.list", nil)
}

// Lists your trust-centre updates, newest as you ordered them.
func (o ops) listUpdates(ctx context.Context, _ *noInput) (*updateList, error) {
	return mine[updateList](ctx, o, "updates.list", nil)
}

// Reads your risk profile — the label and value pairs describing what your
// organization handles and how.
func (o ops) risk(ctx context.Context, _ *noInput) (*json.RawMessage, error) {
	return mine[json.RawMessage](ctx, o, "risk.get", nil)
}

// Reads the audit rows that stand behind one control, over a window.
//
// The inventory decides what evidences what: a control names the audit actions
// that are its trail, and this resolves the control id to those actions and
// reads them. So evidence cannot drift from the inventory, and it is scoped to
// your own organization — the query carries no organization field for a caller
// to fill in.
//
// A control that nothing in the trail evidences says so plainly rather than
// answering an empty page, because an empty page reads like a clean quarter. A
// deployment with no audit store answers 501 and says the trail was not read,
// for the same reason.
//
// Example: {"control":"iam.refresh.rotation","from":"2026-01-01","limit":"50"}
func (o ops) evidence(ctx context.Context, in *evidenceQuery) (*json.RawMessage, error) {
	return mine[json.RawMessage](ctx, o, "evidence.get", map[string]string{
		"control": in.Control, "from": in.From, "to": in.To, "limit": in.Limit,
	})
}

// Writes one record into a section of YOUR organization's trust centre —
// profile, control, document, subprocessor, policy, faq, update or risk.
//
// A control written here is held to exactly the rule a control committed to the
// deployment's own inventory is held to, by the same validator: its prose may
// not claim a certificate and may not name a framework (a framework belongs in
// the mappings, where it arrives attached to a number), anything short of
// automated must say what is missing, and a mapping to a clause no framework
// declares is refused rather than scored as nothing.
//
// A document defaults to GATED. An artifact an independent auditor signed — a
// SOC 2 report, an ISO certificate, a penetration test, an auditor letter —
// cannot be made public at all; it is released through a grant. A
// self-assessment can, because the organization is the one attesting it.
//
// The deployment's OWN control inventory is governed in git and is not writable
// here: naming one of its ids is a conflict, not an overwrite.
//
// Example: {"kind":"subprocessor","id":"acme-cloud","data":{"name":"A cloud","purpose":"Compute"}}
func (o ops) put(ctx context.Context, in *sectionWrite) (*written, error) {
	if len(in.Data) > maxBody {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "the record is too large")
	}
	// DECODED, not relayed as bytes. goja converts a []byte — which is what a
	// json.RawMessage is — into a JS array of numbers, so the bundle would receive
	// the record's UTF-8 and report every required field missing. The value has to
	// cross as a value.
	var rec any
	if len(in.Data) > 0 {
		if err := json.Unmarshal(in.Data, &rec); err != nil {
			return nil, zip.ErrBadRequest("data is not valid JSON")
		}
	}
	body := map[string]any{"ord": in.Ord, "data": rec}
	return mineBody[written](ctx, o, "section.put", map[string]string{"kind": in.Kind, "id": in.ID}, body)
}

// Removes one record from a section of your organization's trust centre. A
// record that is not there is a 404, never a silent success. A control that
// belongs to the deployment's own inventory is removed by a commit, not by a
// request.
//
// Example: {"kind":"faq","id":"where-is-data-held"}
func (o ops) remove(ctx context.Context, in *sectionRef) (*dropped, error) {
	return mineBody[dropped](ctx, o, "section.delete", map[string]string{"kind": in.Kind, "id": in.ID}, nil)
}

// ---- the one dispatch ----

// mine runs a bundle route on the CALLER's own tenant. The org is the validated
// principal's and is never an In field: an In field is caller-supplied, so a
// tenant read from one is a cross-tenant read the caller asserted for itself.
func mine[T any](ctx context.Context, o ops, route string, params map[string]string) (*T, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to read your trust centre")
	}
	return as[T](ctx, o, org, route, params)
}

// mineBody is mine with a decoded request body.
func mineBody[T any](ctx context.Context, o ops, route string, params map[string]string, body any) (*T, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to write to your trust centre")
	}
	return dispatch[T](ctx, o, org, route, params, body)
}

// as runs a bundle route on a NAMED tenant. Only the published endpoint uses it with
// an org off the request, and only because a published trust centre is a public
// document addressed by a public name.
func as[T any](ctx context.Context, o ops, org, route string, params map[string]string) (*T, error) {
	return dispatch[T](ctx, o, org, route, params, nil)
}

func dispatch[T any](ctx context.Context, o ops, org, route string, params map[string]string, body any) (*T, error) {
	method := http.MethodGet
	switch route {
	case "section.put":
		method = http.MethodPut
	case "section.delete":
		method = http.MethodDelete
	}
	if o.s == nil || o.s.State.host == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "trust is not initialised")
	}
	if org == "" {
		return nil, zip.ErrUnauthorized("no organization on this request")
	}
	resp, err := o.s.State.host.Dispatch(ctx, org, goja.BaseRequest{
		Route: route, Method: method, Params: params, Body: body,
	})
	if err != nil {
		o.log().Error("trust dispatch failed", "route", route, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "trust dispatch failed")
	}
	if resp.Status >= 400 {
		// The bundle's own status and its own sentence, relayed. A refusal that
		// loses its reason is a refusal nobody can act on.
		return nil, zip.Errorf(resp.Status, "%s", reason(resp.Body))
	}
	var out T
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		o.log().Error("trust decode failed", "route", route, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "trust answered a shape this host cannot read")
	}
	return &out, nil
}

// reason lifts the bundle's own message off a refusal.
func reason(body []byte) string {
	var r struct {
		Message string   `json:"message"`
		Errors  []string `json:"errors"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Message == "" {
		return "trust refused the request"
	}
	if len(r.Errors) == 0 {
		return r.Message
	}
	out := r.Message
	for _, e := range r.Errors {
		out += "; " + e
	}
	return out
}
