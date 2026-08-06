// Package crm is your sales pipeline: the companies, the people, the deals in play.
//
// Plus the Startup Program intake, which lands as a scored application.
//
// The three core entities are faithful to Twenty's `company` / `person` /
// `opportunity` standard objects, with Twenty's composite fields (FULL_NAME,
// EMAILS, CURRENCY, LINKS, ADDRESS) flattened to scalar columns for SQLite.
//
// A CRM contact is a PROSPECT the org tracks. It is NOT a product user: an org's
// own users live in Hanzo IAM and apps/marketing resolves audiences from that
// roster (roster.go), never from this table. The two contact universes are
// deliberate and do not join.
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is the
// value SanitizeIdentity minted from the VALIDATED bearer owner claim
// (HIP-0026) — never a client-supplied header, and never a field of a request
// body. Every store query filters WHERE org=?, so one tenant can never read or
// mutate another's data.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/crm/summary               per-org row counts (companies/contacts/opps)
//	GET    /v1/crm/companies             list companies                 -> {data:[…]}
//	POST   /v1/crm/companies             create a company               -> Company (201)
//	GET    /v1/crm/companies/:id         company detail                 -> Company
//	PUT    /v1/crm/companies/:id         update a company               -> Company
//	DELETE /v1/crm/companies/:id         delete a company (+ clear refs)
//	GET    /v1/crm/contacts              list contacts (?companyId=)     -> {data:[…]}
//	POST   /v1/crm/contacts             create a contact               -> Contact (201)
//	GET    /v1/crm/contacts/:id          contact detail                 -> Contact
//	PUT    /v1/crm/contacts/:id          update a contact               -> Contact
//	DELETE /v1/crm/contacts/:id          delete a contact (+ clear refs)
//	GET    /v1/crm/opportunities         list opportunities (?stage=)    -> {data:[…]}
//	POST   /v1/crm/opportunities        create an opportunity          -> Opportunity (201)
//	GET    /v1/crm/opportunities/:id     opportunity detail             -> Opportunity
//	PUT    /v1/crm/opportunities/:id     update an opportunity          -> Opportunity
//	DELETE /v1/crm/opportunities/:id     delete an opportunity
//
// Every route above is a TYPED op, so each is one registry entry with N
// projections — the REST route, the OpenAPI operation, the /mcp tool, the CLI
// command and the generated SDK method all follow from it. The Startup Program
// intake (POST /v1/crm/applications, applications.go) is the ONE route here that
// stays a raw handler, and says why at its registration.
//
// Order 131: binds /v1/crm/* before the AI subsystem's /v1/* catch-all (150).
// serve.go auto-registers GET /v1/crm/health.
package crm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

const (
	// maxField caps a single text field so an unbounded body can't amplify the
	// shared DB or a list response. CRM fields are short identifiers/labels.
	maxField = 1024
	// defaultLimit / maxLimit bound list responses.
	defaultLimit = 200
	maxLimit     = 1000
)

// stages is the default Twenty opportunity pipeline. A create/update with an
// unknown stage is rejected; empty defaults to NEW.
var stages = map[string]bool{
	"NEW": true, "SCREENING": true, "MEETING": true, "PROPOSAL": true, "CUSTOMER": true,
}

// state is crm's own data; shared deps (logger, brand) live in the embedded
// cloud.Base, reached as s.Log / s.Brand.
type state struct {
	store *Store
	// ai screens Startup Program applications; nil disables screening (non-fatal).
	ai types.AIClient
	// defaultModel is the gateway model for screens ("" → gateway default).
	defaultModel string
	// screenSync runs the AI screen inline instead of detached (tests only).
	screenSync bool
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires the crm surface onto app per HIP-0106. Complex flavour: it keeps a
// package global (mounted) for Shutdown, so it constructs the Service value directly.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("crm.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("crm.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("crm.Mount: empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("crm.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "crm")
	s := &cloud.Service[state]{Base: b, State: state{
		store:        store,
		ai:           deps.AI,
		defaultModel: cloud.DefaultModel,
	}}
	mounted = s

	routes(app, s)

	b.Log.Info("crm mounted", "brand", deps.Brand)
	return nil
}

// ---- the typed-op seam ----
//
// A typed op (zip.Get[In, Out] and friends) is ONE registry entry with N
// projections: the REST route, the schema in the OpenAPI document, the tool at
// /mcp, the CLI command and every generated SDK method are read off it. A raw
// func(*zip.Ctx) error serves the same bytes and is invisible to all of them.
//
// A TypedHandler is func(context.Context, *In) (*Out, error), so three things it
// is never handed, and where each comes from:
//
//   - THE SERVICE. There is no parameter for it, so it arrives as a RECEIVER:
//     ops binds it once and every op is a method value (o.createCompany). That is
//     also the only bound form cmd/zipdoc can lift prose from — a closure returned
//     by a helper is a call expression with no declaration to read.
//   - THE ORG. cloud.Bridge parks the VALIDATED org on the request context and
//     tenant reads it back. It is NEVER an In field: an In field is
//     caller-supplied, so a tenant key read from one is a cross-tenant read the
//     caller asserted for itself.
//   - THE RESPONSE STATUS. zip.WithStatus(201) declares it ON the op, so the
//     document keys its response on the code the route actually sends instead of
//     the 200 a per-request status would leave every generated client expecting.
//
// FAIL CLOSED OFF THE HTTP PATH. The MCP and CLI projections do not pass through
// this prefix and carry no bridge, so an op reached that way finds no org parked
// and refuses with exactly the 403 an unauthenticated REST call gets — the
// handler's own gate, with no second gate to keep in sync.

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/crm openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the mounted Service so each op can be a method value. It carries
// STATE and no logic: every method resolves its org and then calls the same
// store the intake handler beside it calls.
type ops struct{ s *cloud.Service[state] }

// routes registers the CRM surface: companies, contacts, opportunities, the summary
// roll-up, and the Startup Program application intake.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/crm")
	// A typed op receives only a context, so the validated org it reads is parked
	// there by cloud.Bridge. This subsystem does not install it: the program's
	// composer does, once at the root, after the identity check that mints the org
	// and before any subsystem registers a route — an order only the composer can
	// hold.

	// Declared on the GROUP: the op's path is the prefix composed with the leaf,
	// which is the identity every projection keys on, and cmd/zipdoc (zip v1.18.3+)
	// resolves the prefix the same way, so the prose below reaches the document
	// and the tool list.
	o := ops{s: s}
	zip.Get(g, "/summary", o.summary)

	zip.Get(g, "/companies", o.listCompanies)
	zip.Post(g, "/companies", o.createCompany, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/companies/:id", o.getCompany)
	zip.Put(g, "/companies/:id", o.updateCompany)
	zip.Delete(g, "/companies/:id", o.deleteCompany)

	zip.Get(g, "/contacts", o.listContacts)
	zip.Post(g, "/contacts", o.createContact, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/contacts/:id", o.getContact)
	zip.Put(g, "/contacts/:id", o.updateContact)
	zip.Delete(g, "/contacts/:id", o.deleteContact)

	zip.Get(g, "/opportunities", o.listOpps)
	zip.Post(g, "/opportunities", o.createOpp, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/opportunities/:id", o.getOpp)
	zip.Put(g, "/opportunities/:id", o.updateOpp)
	zip.Delete(g, "/opportunities/:id", o.deleteOpp)

	// Startup Program applications. The intake POST is PUBLIC (unauthenticated
	// marketing form) and IP-rate-limited; the reads/mutations are staff-only,
	// gated by tenant() like every other CRM route.
	//
	// The intake is the one route here that is NOT a typed op, and the rate limit
	// is why: it is an HTTP middleware, and the MCP and CLI projections of a typed
	// op do not run it — typing this route would publish an unmetered alias of a
	// deliberately metered public endpoint. Its 64 KiB body cap is the same fact:
	// a typed op is handed an already-decoded In, so the cap could only run after
	// the parse it exists to prevent. Both are wire, so the route stays raw until
	// zip can carry them.
	//
	// THE SUBTREE IS THE SCOPE — not registration order.
	//
	// This used to be a second /v1/crm group with the limiter on it, and the routes
	// it covered were "everything registered after this line": the intake POST plus
	// the three staff routes, with the CRUD above deliberately outside. That was
	// true of the flat, registration-time model. It is not true of zip v1.24, where
	// a definition's middleware wraps the routes in its OWN subtree and nothing
	// else — so the limiter silently narrowed to the single route chained onto it,
	// and the three staff routes lost their cover with no error anywhere. That is
	// the same "silently stops running" failure as an inert seam, wearing the shape
	// of an ordering convention that no longer holds.
	//
	// So the covered routes are now COMPOSED BENEATH the limiter, which is the only
	// thing that states the coverage rather than implying it. The empty prefix
	// keeps the paths exactly as served: this group inherits /v1/crm from g, and
	// with it g's Bridge, which the typed ops below need to resolve their tenant.
	// The CRUD above stays outside because it is registered on g, not here — a
	// property of WHERE a route is written now, not of when.
	applications := g.Group("", middleware.RateLimit(middleware.RateLimitConfig{
		Limit:  intakeRateLimit,
		Window: intakeRateWindow,
		KeyFn:  func(c *zip.Ctx) string { return c.Fiber().IP() },
	}))
	applications.Post("/applications", cloud.Handle(s, apply))
	zip.Get(applications, "/applications", o.listApplications)
	zip.Get(applications, "/applications/:id", o.getApplication)
	zip.Patch(applications, "/applications/:id", o.patchApplication)
}

// ---- shared helpers ----

// tenant resolves the org — the tenant-isolation KEY — for a typed op. It is
// principal.Org's answer carried across the typed signature by cloud.Bridge:
// c.Org() EXACTLY as SanitizeIdentity minted it from the validated IAM owner
// claim (HIP-0026), never lowercased, stripped or truncated (normalizing would
// collapse DISTINCT owners into one bucket — itself a cross-tenant break). A
// request with no validated principal parks nothing, so this refuses.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// clip trims and bounds a text field to maxField.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxField {
		return s[:maxField]
	}
	return s
}

// limitOf bounds a requested page size: absent, zero or negative asks for the
// default, and nothing may ask for more than maxLimit.
func limitOf(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

func defaultCurrency(cur string) string {
	cur = strings.ToUpper(strings.TrimSpace(cur))
	if cur == "" {
		return "USD"
	}
	if len(cur) > 8 {
		return cur[:8]
	}
	return cur
}

// mapErr maps a store sentinel error to the right HTTP error. Non-sentinel
// errors become a 500 with the wrapped message.
func mapErr(err error, notFoundMsg string) error {
	switch err {
	case errNotFound:
		return zip.ErrNotFound(notFoundMsg)
	case errConflict:
		return zip.ErrConflict("already exists")
	case errBadRef:
		return zip.Errorf(http.StatusUnprocessableEntity, "referenced record not found in org")
	default:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
}

// ---- HTTP shapes (the published contract) ----

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire.
type noInput struct{}

// ref addresses ONE record by the id in its path. The URL is the addressing
// authority, so the id binds from there whatever a body says.
type ref struct {
	// ID is the record to act on, from the path.
	ID string `json:"id"`
}

// crmSummary is the org's CRM row counts.
type crmSummary struct {
	// Companies is how many companies the org has.
	Companies int `json:"companies"`
	// Contacts is how many contacts the org has.
	Contacts int `json:"contacts"`
	// Opportunities is how many opportunities the org has.
	Opportunities int `json:"opportunities"`
}

// companyPage asks for a page of the caller org's companies.
type companyPage struct {
	// Limit caps the rows returned: 200 by default, 1000 at most.
	Limit int `json:"limit"`
}

// contactPage asks for a page of the caller org's contacts, optionally only
// those at one company.
type contactPage struct {
	// CompanyID returns only the contacts at that company when set.
	CompanyID string `json:"companyId"`
	// Limit caps the rows returned: 200 by default, 1000 at most.
	Limit int `json:"limit"`
}

// oppPage asks for a page of the caller org's opportunities, optionally only
// those at one pipeline stage.
type oppPage struct {
	// Stage returns only the opportunities at that pipeline stage when set
	// (NEW, SCREENING, MEETING, PROPOSAL or CUSTOMER; case-insensitive).
	Stage string `json:"stage"`
	// Limit caps the rows returned: 200 by default, 1000 at most.
	Limit int `json:"limit"`
}

// companyList is a page of the caller org's companies.
type companyList struct {
	// Data is the page of companies, most recently updated first.
	Data []Company `json:"data"`
}

// contactList is a page of the caller org's contacts.
type contactList struct {
	// Data is the page of contacts, most recently updated first.
	Data []Contact `json:"data"`
}

// oppList is a page of the caller org's opportunities.
type oppList struct {
	// Data is the page of opportunities, most recently updated first.
	Data []Opportunity `json:"data"`
}

// companyReq is the writable shape of a company — what a create or an update
// accepts. The org and the timestamps are server-owned and are never read off
// the wire.
type companyReq struct {
	// ID names the company to update and comes from the path. A create ignores
	// it: the server mints the id.
	ID string `json:"id"`
	// Name is the company name. Required.
	Name string `json:"name"`
	// DomainName is the company's primary domain, e.g. "acme.com".
	DomainName string `json:"domainName"`
	// Employees is the headcount.
	Employees int64 `json:"employees"`
	// City is the head-office city.
	City string `json:"city"`
	// Country is the head-office country.
	Country string `json:"country"`
	// ARR is annual recurring revenue in minor units (cents) of Currency.
	ARR int64 `json:"arr"`
	// Currency is the ISO code ARR is denominated in; empty defaults to USD.
	Currency string `json:"currency"`
	// ICP marks the company as an ideal-customer-profile fit.
	ICP bool `json:"idealCustomerProfile"`
	// Linkedin is the company's LinkedIn URL.
	Linkedin string `json:"linkedinLink"`
	// XLink is the company's X (Twitter) URL.
	XLink string `json:"xLink"`
}

// contactReq is the writable shape of a contact — what a create or an update
// accepts. The org and the timestamps are server-owned.
type contactReq struct {
	// ID names the contact to update and comes from the path. A create ignores
	// it: the server mints the id.
	ID string `json:"id"`
	// FirstName is the person's given name.
	FirstName string `json:"firstName"`
	// LastName is the person's family name.
	LastName string `json:"lastName"`
	// Email is the person's email address.
	Email string `json:"email"`
	// Phone is the person's phone number.
	Phone string `json:"phone"`
	// JobTitle is the person's role at their company.
	JobTitle string `json:"jobTitle"`
	// City is where the person is based.
	City string `json:"city"`
	// CompanyID links the contact to one of the org's companies.
	CompanyID string `json:"companyId"`
	// Linkedin is the person's LinkedIn URL.
	Linkedin string `json:"linkedinLink"`
	// XLink is the person's X (Twitter) URL.
	XLink string `json:"xLink"`
}

// oppReq is the writable shape of an opportunity — what a create or an update
// accepts. The org and the timestamps are server-owned.
type oppReq struct {
	// ID names the opportunity to update and comes from the path. A create
	// ignores it: the server mints the id.
	ID string `json:"id"`
	// Name is the deal name. Required.
	Name string `json:"name"`
	// Amount is the deal value in minor units (cents) of Currency.
	Amount int64 `json:"amount"`
	// Currency is the ISO code Amount is denominated in; empty defaults to USD.
	Currency string `json:"currency"`
	// Stage is the pipeline stage: NEW, SCREENING, MEETING, PROPOSAL or CUSTOMER
	// (case-insensitive). Empty defaults to NEW.
	Stage string `json:"stage"`
	// CloseDate is the expected close, as a unix second (0 = unset).
	CloseDate int64 `json:"closeDate"`
	// CompanyID links the deal to one of the org's companies.
	CompanyID string `json:"companyId"`
	// PointOfContact links the deal to one of the org's contacts.
	PointOfContact string `json:"pointOfContactId"`
}

// ---- companies ----

// CreateCompany adds a company to the caller's org and answers 201 with the stored record.
// A name is required; an empty currency defaults to USD.
//
// Example: {"name": "MaxPower Inc", "domainName": "maxpower.ai", "employees": 42}
func (o ops) createCompany(ctx context.Context, in *companyReq) (*Company, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	name := clip(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	id, err := genID("comp")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	comp := Company{
		ID: id, Org: org, Name: name, DomainName: clip(in.DomainName),
		Employees: in.Employees, City: clip(in.City), Country: clip(in.Country),
		ARR: in.ARR, Currency: defaultCurrency(in.Currency), ICP: in.ICP,
		Linkedin: clip(in.Linkedin), XLink: clip(in.XLink), CreatedAt: now, UpdatedAt: now,
	}
	saved, err := o.s.State.store.CreateCompany(ctx, comp)
	if err != nil {
		return nil, mapErr(err, "")
	}
	return &saved, nil
}

// ListCompanies returns the caller org's companies, most recently updated first.
func (o ops) listCompanies(ctx context.Context, in *companyPage) (*companyList, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListCompanies(ctx, org, limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &companyList{Data: rows}, nil
}

// GetCompany returns one of the caller org's companies. An id belonging to
// another org reads as not found.
//
// Example: {"id": "comp_1"}
func (o ops) getCompany(ctx context.Context, in *ref) (*Company, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	comp, err := o.s.State.store.GetCompany(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "company not found")
	}
	return &comp, nil
}

// UpdateCompany replaces one of the caller org's companies. Every writable
// field is taken from the request, so a field the request omits is CLEARED —
// send the whole record. A name is required.
//
// Example: {"id": "comp_1", "name": "MaxPower Inc", "employees": 64}
func (o ops) updateCompany(ctx context.Context, in *companyReq) (*Company, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	name := clip(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	comp := Company{
		ID: strings.TrimSpace(in.ID), Org: org, Name: name, DomainName: clip(in.DomainName),
		Employees: in.Employees, City: clip(in.City), Country: clip(in.Country),
		ARR: in.ARR, Currency: defaultCurrency(in.Currency), ICP: in.ICP,
		Linkedin: clip(in.Linkedin), XLink: clip(in.XLink), UpdatedAt: time.Now().Unix(),
	}
	saved, err := o.s.State.store.UpdateCompany(ctx, comp)
	if err != nil {
		return nil, mapErr(err, "company not found")
	}
	return &saved, nil
}

// DeleteCompany removes one of the caller org's companies and answers 204. Any
// contact or opportunity in the org that referenced it keeps existing with the
// reference cleared, so nothing is left pointing at a company that is gone.
//
// Example: {"id": "comp_1"}
func (o ops) deleteCompany(ctx context.Context, in *ref) (*struct{}, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := o.s.State.store.DeleteCompany(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("company not found")
	}
	return nil, nil
}

// ---- contacts ----

// CreateContact adds a person to the caller's org and answers 201 with the stored record.
// One of firstName, lastName or email is required, and a companyId must name a
// company in the same org.
//
// Example: {"firstName": "Dave", "lastName": "Lorenzini", "email": "dave@maxpower.ai"}
func (o ops) createContact(ctx context.Context, in *contactReq) (*Contact, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	ct := Contact{
		FirstName: clip(in.FirstName), LastName: clip(in.LastName), Email: clip(in.Email),
		Phone: clip(in.Phone), JobTitle: clip(in.JobTitle), City: clip(in.City),
		CompanyID: clip(in.CompanyID), Linkedin: clip(in.Linkedin), XLink: clip(in.XLink),
	}
	if ct.FirstName == "" && ct.LastName == "" && ct.Email == "" {
		return nil, zip.ErrBadRequest("one of firstName, lastName, or email is required")
	}
	id, err := genID("cont")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	ct.ID, ct.Org, ct.CreatedAt, ct.UpdatedAt = id, org, now, now
	saved, err := o.s.State.store.CreateContact(ctx, ct)
	if err != nil {
		return nil, mapErr(err, "")
	}
	return &saved, nil
}

// ListContacts returns the caller org's contacts, most recently updated first.
// A companyId narrows the page to the people at that company.
func (o ops) listContacts(ctx context.Context, in *contactPage) (*contactList, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListContacts(ctx, org, strings.TrimSpace(in.CompanyID), limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &contactList{Data: rows}, nil
}

// GetContact returns one of the caller org's contacts. An id belonging to
// another org reads as not found.
//
// Example: {"id": "cont_1"}
func (o ops) getContact(ctx context.Context, in *ref) (*Contact, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	ct, err := o.s.State.store.GetContact(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "contact not found")
	}
	return &ct, nil
}

// UpdateContact replaces one of the caller org's contacts. Every writable field
// is taken from the request, so a field the request omits is CLEARED — send the
// whole record. One of firstName, lastName or email is required.
//
// Example: {"id": "cont_1", "firstName": "Dave", "jobTitle": "CTO"}
func (o ops) updateContact(ctx context.Context, in *contactReq) (*Contact, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	ct := Contact{
		ID: strings.TrimSpace(in.ID), Org: org,
		FirstName: clip(in.FirstName), LastName: clip(in.LastName), Email: clip(in.Email),
		Phone: clip(in.Phone), JobTitle: clip(in.JobTitle), City: clip(in.City),
		CompanyID: clip(in.CompanyID), Linkedin: clip(in.Linkedin), XLink: clip(in.XLink),
		UpdatedAt: time.Now().Unix(),
	}
	if ct.FirstName == "" && ct.LastName == "" && ct.Email == "" {
		return nil, zip.ErrBadRequest("one of firstName, lastName, or email is required")
	}
	saved, err := o.s.State.store.UpdateContact(ctx, ct)
	if err != nil {
		return nil, mapErr(err, "contact not found")
	}
	return &saved, nil
}

// DeleteContact removes one of the caller org's contacts and answers 204. Any
// opportunity in the org that named it point of contact keeps existing with
// that reference cleared.
//
// Example: {"id": "cont_1"}
func (o ops) deleteContact(ctx context.Context, in *ref) (*struct{}, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := o.s.State.store.DeleteContact(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("contact not found")
	}
	return nil, nil
}

// ---- opportunities ----

func normStage(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return "NEW", true
	}
	return s, stages[s]
}

// CreateOpportunity adds a deal to the caller's org and answers 201 with the stored record.
// A name is required; the stage defaults to NEW; companyId and pointOfContactId
// must name records in the same org.
//
// Example: {"name": "Enterprise Deal", "amount": 5000000, "stage": "PROPOSAL"}
func (o ops) createOpp(ctx context.Context, in *oppReq) (*Opportunity, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	name := clip(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	stage, valid := normStage(in.Stage)
	if !valid {
		return nil, zip.ErrBadRequest("stage must be one of NEW, SCREENING, MEETING, PROPOSAL, CUSTOMER")
	}
	id, err := genID("oppo")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	opp := Opportunity{
		ID: id, Org: org, Name: name, Amount: in.Amount, Currency: defaultCurrency(in.Currency),
		Stage: stage, CloseDate: in.CloseDate, CompanyID: clip(in.CompanyID),
		PointOfContact: clip(in.PointOfContact), CreatedAt: now, UpdatedAt: now,
	}
	saved, err := o.s.State.store.CreateOpportunity(ctx, opp)
	if err != nil {
		return nil, mapErr(err, "")
	}
	return &saved, nil
}

// ListOpportunities returns the caller org's deals, most recently updated first.
// A stage narrows the page to one pipeline stage.
func (o ops) listOpps(ctx context.Context, in *oppPage) (*oppList, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	stage := strings.ToUpper(strings.TrimSpace(in.Stage))
	rows, err := o.s.State.store.ListOpportunities(ctx, org, stage, limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &oppList{Data: rows}, nil
}

// GetOpportunity returns one of the caller org's deals. An id belonging to
// another org reads as not found.
//
// Example: {"id": "oppo_1"}
func (o ops) getOpp(ctx context.Context, in *ref) (*Opportunity, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	opp, err := o.s.State.store.GetOpportunity(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "opportunity not found")
	}
	return &opp, nil
}

// UpdateOpportunity replaces one of the caller org's deals. Every writable
// field is taken from the request, so a field the request omits is CLEARED —
// send the whole record. A name is required and the stage must be a pipeline
// stage.
//
// Example: {"id": "oppo_1", "name": "Enterprise Deal", "stage": "CUSTOMER"}
func (o ops) updateOpp(ctx context.Context, in *oppReq) (*Opportunity, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	name := clip(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	stage, valid := normStage(in.Stage)
	if !valid {
		return nil, zip.ErrBadRequest("stage must be one of NEW, SCREENING, MEETING, PROPOSAL, CUSTOMER")
	}
	opp := Opportunity{
		ID: strings.TrimSpace(in.ID), Org: org, Name: name, Amount: in.Amount,
		Currency: defaultCurrency(in.Currency), Stage: stage, CloseDate: in.CloseDate,
		CompanyID: clip(in.CompanyID), PointOfContact: clip(in.PointOfContact),
		UpdatedAt: time.Now().Unix(),
	}
	saved, err := o.s.State.store.UpdateOpportunity(ctx, opp)
	if err != nil {
		return nil, mapErr(err, "opportunity not found")
	}
	return &saved, nil
}

// DeleteOpportunity removes one of the caller org's deals and answers 204.
//
// Example: {"id": "oppo_1"}
func (o ops) deleteOpp(ctx context.Context, in *ref) (*struct{}, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := o.s.State.store.DeleteOpportunity(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("opportunity not found")
	}
	return nil, nil
}

// ---- summary ----

// Summary counts the caller org's CRM records: companies, contacts, opportunities.
func (o ops) summary(ctx context.Context, _ *noInput) (*crmSummary, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	companies, contacts, opps, err := o.s.State.store.Counts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return &crmSummary{Companies: companies, Contacts: contacts, Opportunities: opps}, nil
}

// Shutdown closes the crm store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
