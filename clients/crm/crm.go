// Package crm mounts the Hanzo Cloud /v1/crm/* surface: a native-Go,
// per-org CRM (companies, contacts, opportunities) on Base/SQLite. It is the
// first slice of the "collapse the business apps into the unified cloud binary"
// program (universe/docs/architecture/unified-backend-go.md) — a native-Go port
// of the Twenty CRM core model, NOT a proxy to a NestJS backend.
//
// The three entities are faithful to Twenty's `company` / `person` /
// `opportunity` standard objects, with Twenty's composite fields (FULL_NAME,
// EMAILS, CURRENCY, LINKS, ADDRESS) flattened to scalar columns for SQLite.
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// c.Org() — the value SanitizeIdentity minted from the VALIDATED bearer owner
// claim (HIP-0026) — and NEVER a client-supplied header. Every store query
// filters WHERE org=?, so one tenant can never read or mutate another's data.
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
// Order 131: binds /v1/crm/* before the AI subsystem's /v1/* catch-all (150).
// serve.go auto-registers GET /v1/crm/health.
package crm

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
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

type svc struct {
	store *Store
	log   luxlog.Logger
}

// mounted is the active service so Shutdown can release the store.
var mounted *svc

// Mount wires the crm surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("crm.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("crm.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "crm")
	if deps.DataDir == "" {
		return fmt.Errorf("crm.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("crm.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "crm.db"))
	if err != nil {
		return fmt.Errorf("crm.Mount: open store: %w", err)
	}
	s := &svc{store: store, log: log}
	mounted = s

	app.Get("/v1/crm/summary", s.summary)

	app.Get("/v1/crm/companies", s.listCompanies)
	app.Post("/v1/crm/companies", s.createCompany)
	app.Get("/v1/crm/companies/:id", s.getCompany)
	app.Put("/v1/crm/companies/:id", s.updateCompany)
	app.Delete("/v1/crm/companies/:id", s.deleteCompany)

	app.Get("/v1/crm/contacts", s.listContacts)
	app.Post("/v1/crm/contacts", s.createContact)
	app.Get("/v1/crm/contacts/:id", s.getContact)
	app.Put("/v1/crm/contacts/:id", s.updateContact)
	app.Delete("/v1/crm/contacts/:id", s.deleteContact)

	app.Get("/v1/crm/opportunities", s.listOpps)
	app.Post("/v1/crm/opportunities", s.createOpp)
	app.Get("/v1/crm/opportunities/:id", s.getOpp)
	app.Put("/v1/crm/opportunities/:id", s.updateOpp)
	app.Delete("/v1/crm/opportunities/:id", s.deleteOpp)

	log.Info("crm mounted", "brand", deps.Brand)
	return nil
}

func init() {
	cloud.Register("crm", 131, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("crm.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// ---- shared helpers ----

// tenant resolves the org — the tenant-isolation KEY — for a request. It uses
// c.Org() EXACTLY as SanitizeIdentity minted it from the validated IAM owner
// claim (HIP-0026): never lowercased, stripped, or truncated (normalizing would
// collapse DISTINCT owners into one bucket — a cross-tenant break). Reject only
// empty or pathologically long; never transform. Mirrors clients/prompts.
func tenant(c *zip.Ctx) (string, bool) {
	org := strings.TrimSpace(c.Org())
	if org == "" || len(org) > 128 {
		return "", false
	}
	return org, true
}

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

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

func limitOf(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
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

// ---- companies ----

func (s *svc) createCompany(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body Company
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := clip(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	id, err := genID("comp")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	comp := Company{
		ID: id, Org: org, Name: name, DomainName: clip(body.DomainName),
		Employees: body.Employees, City: clip(body.City), Country: clip(body.Country),
		ARR: body.ARR, Currency: defaultCurrency(body.Currency), ICP: body.ICP,
		Linkedin: clip(body.Linkedin), XLink: clip(body.XLink), CreatedAt: now, UpdatedAt: now,
	}
	saved, err := s.store.CreateCompany(c.Context(), comp)
	if err != nil {
		return mapErr(err, "")
	}
	return c.JSON(http.StatusCreated, saved)
}

func (s *svc) listCompanies(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.ListCompanies(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func (s *svc) getCompany(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	comp, err := s.store.GetCompany(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "company not found")
	}
	return c.JSON(http.StatusOK, comp)
}

func (s *svc) updateCompany(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body Company
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := clip(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	comp := Company{
		ID: idParam(c), Org: org, Name: name, DomainName: clip(body.DomainName),
		Employees: body.Employees, City: clip(body.City), Country: clip(body.Country),
		ARR: body.ARR, Currency: defaultCurrency(body.Currency), ICP: body.ICP,
		Linkedin: clip(body.Linkedin), XLink: clip(body.XLink), UpdatedAt: time.Now().Unix(),
	}
	saved, err := s.store.UpdateCompany(c.Context(), comp)
	if err != nil {
		return mapErr(err, "company not found")
	}
	return c.JSON(http.StatusOK, saved)
}

func (s *svc) deleteCompany(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	deleted, err := s.store.DeleteCompany(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("company not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- contacts ----

func (s *svc) createContact(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body Contact
	if err := c.Bind(&body); err != nil {
		return err
	}
	ct := Contact{
		FirstName: clip(body.FirstName), LastName: clip(body.LastName), Email: clip(body.Email),
		Phone: clip(body.Phone), JobTitle: clip(body.JobTitle), City: clip(body.City),
		CompanyID: clip(body.CompanyID), Linkedin: clip(body.Linkedin), XLink: clip(body.XLink),
	}
	if ct.FirstName == "" && ct.LastName == "" && ct.Email == "" {
		return zip.ErrBadRequest("one of firstName, lastName, or email is required")
	}
	id, err := genID("cont")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	ct.ID, ct.Org, ct.CreatedAt, ct.UpdatedAt = id, org, now, now
	saved, err := s.store.CreateContact(c.Context(), ct)
	if err != nil {
		return mapErr(err, "")
	}
	return c.JSON(http.StatusCreated, saved)
}

func (s *svc) listContacts(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.ListContacts(c.Context(), org, strings.TrimSpace(c.Query("companyId")), limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func (s *svc) getContact(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	ct, err := s.store.GetContact(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "contact not found")
	}
	return c.JSON(http.StatusOK, ct)
}

func (s *svc) updateContact(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body Contact
	if err := c.Bind(&body); err != nil {
		return err
	}
	ct := Contact{
		ID: idParam(c), Org: org,
		FirstName: clip(body.FirstName), LastName: clip(body.LastName), Email: clip(body.Email),
		Phone: clip(body.Phone), JobTitle: clip(body.JobTitle), City: clip(body.City),
		CompanyID: clip(body.CompanyID), Linkedin: clip(body.Linkedin), XLink: clip(body.XLink),
		UpdatedAt: time.Now().Unix(),
	}
	if ct.FirstName == "" && ct.LastName == "" && ct.Email == "" {
		return zip.ErrBadRequest("one of firstName, lastName, or email is required")
	}
	saved, err := s.store.UpdateContact(c.Context(), ct)
	if err != nil {
		return mapErr(err, "contact not found")
	}
	return c.JSON(http.StatusOK, saved)
}

func (s *svc) deleteContact(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	deleted, err := s.store.DeleteContact(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("contact not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- opportunities ----

func normStage(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return "NEW", true
	}
	return s, stages[s]
}

func (s *svc) createOpp(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body Opportunity
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := clip(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	stage, valid := normStage(body.Stage)
	if !valid {
		return zip.ErrBadRequest("stage must be one of NEW, SCREENING, MEETING, PROPOSAL, CUSTOMER")
	}
	id, err := genID("oppo")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	o := Opportunity{
		ID: id, Org: org, Name: name, Amount: body.Amount, Currency: defaultCurrency(body.Currency),
		Stage: stage, CloseDate: body.CloseDate, CompanyID: clip(body.CompanyID),
		PointOfContact: clip(body.PointOfContact), CreatedAt: now, UpdatedAt: now,
	}
	saved, err := s.store.CreateOpportunity(c.Context(), o)
	if err != nil {
		return mapErr(err, "")
	}
	return c.JSON(http.StatusCreated, saved)
}

func (s *svc) listOpps(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	stage := strings.ToUpper(strings.TrimSpace(c.Query("stage")))
	rows, err := s.store.ListOpportunities(c.Context(), org, stage, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func (s *svc) getOpp(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	o, err := s.store.GetOpportunity(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "opportunity not found")
	}
	return c.JSON(http.StatusOK, o)
}

func (s *svc) updateOpp(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body Opportunity
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := clip(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	stage, valid := normStage(body.Stage)
	if !valid {
		return zip.ErrBadRequest("stage must be one of NEW, SCREENING, MEETING, PROPOSAL, CUSTOMER")
	}
	o := Opportunity{
		ID: idParam(c), Org: org, Name: name, Amount: body.Amount, Currency: defaultCurrency(body.Currency),
		Stage: stage, CloseDate: body.CloseDate, CompanyID: clip(body.CompanyID),
		PointOfContact: clip(body.PointOfContact), UpdatedAt: time.Now().Unix(),
	}
	saved, err := s.store.UpdateOpportunity(c.Context(), o)
	if err != nil {
		return mapErr(err, "opportunity not found")
	}
	return c.JSON(http.StatusOK, saved)
}

func (s *svc) deleteOpp(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	deleted, err := s.store.DeleteOpportunity(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("opportunity not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- summary ----

func (s *svc) summary(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	companies, contacts, opps, err := s.store.Counts(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"companies": companies, "contacts": contacts, "opportunities": opps,
	})
}

// Shutdown closes the crm store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.store == nil {
		return nil
	}
	err := mounted.store.Close()
	mounted = nil
	return err
}
