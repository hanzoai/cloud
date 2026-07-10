// Package captable folds hanzoai/captable into the unified hanzoai/cloud binary
// as an in-process subsystem (HIP-0106) — the PILOT of epic #96 (fold the
// Captable,Inc app into cloud, drop Next.js/Prisma/Postgres). Cloud serves the
// cap-table surface (/v1/captable/*) ITSELF, per tenant, on Base/SQLite.
//
// WRAP, DON'T REWRITE — the read-WRITE variant. Where clients/plan + clients/
// pricing host a read-only @hanzo catalog in goja, captable hosts the tRPC
// business LOGIC (ported to a self-contained goja bundle in github.com/hanzoai/
// captable) and gives it PERSISTENCE over per-tenant Base/SQLite. The bundle
// carries logic; the Go host carries storage. The seam between them is the
// REUSABLE clients/gojabase binding (the RW-Base goja host), which esign (#100)
// and dataroom (#101) reuse unchanged — this leaf is just:
//
//	captable bundle (github.com/hanzoai/captable.Bundle)  +  the per-tenant Schema
//	                         │
//	                  clients/gojabase.New(...)   ← injects __db/__newId/__now,
//	                         │                       one SQLite file per tenant,
//	                  /v1/captable/* zip routes     one transaction per request
//
// No Prisma, no Postgres, no Next.js in this path. Every route resolves the org
// from the VALIDATED cloud principal (principal.Org), never a client header,
// and that org selects the tenant's DB file AND scopes every row.
//
// ACTIVATION is the standard enable-list gate (config.stagedSubsystems):
// captable mounts ONLY when the operator adds "captable" to CLOUD_ENABLE, so the
// mount-all default is unchanged and the standalone captable service keeps
// authority until the phase-2 cutover.
package captable

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	hcaptable "github.com/hanzoai/captable"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/gojabase"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
)

// maxBody caps a request body. Cap-table payloads are small structured records;
// anything larger is malformed or hostile.
const maxBody = 1 << 20 // 1 MiB

type svc struct {
	host *gojabase.Host
	log  luxlog.Logger
}

// mounted is the active service so Shutdown can release the per-tenant stores.
var mounted *svc

// Mount wires the /v1/captable/* surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("captable.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("captable.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "captable")
	if deps.DataDir == "" {
		return fmt.Errorf("captable.Mount: empty DataDir")
	}

	bundle, err := hcaptable.Bundle()
	if err != nil {
		return fmt.Errorf("captable.Mount: load bundle: %w", err)
	}
	host, err := gojabase.New(gojabase.Config{
		Name:    "captable",
		Bundle:  bundle,
		Schema:  schema,
		DataDir: deps.DataDir,
		OnOpen:  seedCompany,
	})
	if err != nil {
		return fmt.Errorf("captable.Mount: gojabase host: %w", err)
	}
	s := &svc{host: host, log: log}
	mounted = s

	// Route table → bundle route names. GET reads carry no body; mutations do.
	// company
	app.Get("/v1/captable/company", s.route("company.get", nil, false))
	app.Put("/v1/captable/company", s.route("company.update", nil, true))
	// stakeholders
	app.Get("/v1/captable/stakeholders", s.route("stakeholders.list", nil, false))
	app.Post("/v1/captable/stakeholders", s.route("stakeholders.add", nil, true))
	app.Patch("/v1/captable/stakeholders/:id", s.routeID("stakeholders.update", true))
	app.Delete("/v1/captable/stakeholders/:id", s.routeID("stakeholders.delete", false))
	// share classes
	app.Get("/v1/captable/share-classes", s.route("shareClasses.list", nil, false))
	app.Post("/v1/captable/share-classes", s.route("shareClasses.create", nil, true))
	app.Patch("/v1/captable/share-classes/:id", s.routeID("shareClasses.update", true))
	// equity plans
	app.Get("/v1/captable/equity-plans", s.route("equityPlans.list", nil, false))
	app.Post("/v1/captable/equity-plans", s.route("equityPlans.create", nil, true))
	// shares (issuance + transfer). /shares/transfer registers before /shares/:id
	// (different methods anyway) so it can never be shadowed.
	app.Get("/v1/captable/shares", s.route("shares.list", nil, false))
	app.Post("/v1/captable/shares", s.route("shares.add", nil, true))
	app.Post("/v1/captable/shares/transfer", s.route("shares.transfer", nil, true))
	app.Delete("/v1/captable/shares/:id", s.routeID("shares.delete", false))
	// options
	app.Get("/v1/captable/options", s.route("options.list", nil, false))
	app.Post("/v1/captable/options", s.route("options.add", nil, true))
	app.Delete("/v1/captable/options/:id", s.routeID("options.delete", false))
	// SAFEs
	app.Get("/v1/captable/safes", s.route("safes.list", nil, false))
	app.Post("/v1/captable/safes", s.route("safes.create", nil, true))
	app.Delete("/v1/captable/safes/:id", s.routeID("safes.delete", false))
	// convertible notes
	app.Get("/v1/captable/convertibles", s.route("convertibles.list", nil, false))
	app.Post("/v1/captable/convertibles", s.route("convertibles.create", nil, true))
	app.Delete("/v1/captable/convertibles/:id", s.routeID("convertibles.delete", false))
	// rounds + investments
	app.Get("/v1/captable/rounds", s.route("rounds.list", nil, false))
	app.Post("/v1/captable/rounds", s.route("rounds.create", nil, true))
	app.Get("/v1/captable/rounds/:id", s.routeID("rounds.get", false))
	app.Post("/v1/captable/rounds/:id/close", s.routeID("rounds.close", true))
	app.Post("/v1/captable/rounds/:id/investments", s.routeID("rounds.investments.add", true))
	app.Get("/v1/captable/investments", s.route("rounds.investments.list", nil, false))
	// computed cap table
	app.Get("/v1/captable/summary", s.route("captable", nil, false))

	log.Info("captable mounted in-process (goja + per-tenant Base)",
		"prefix", "/v1/captable",
		"version", hcaptable.Version,
		"brand", deps.Brand,
		"env", deps.Env,
	)
	return nil
}

// route builds a zip handler that dispatches a fixed bundle route. readBody
// controls whether the JSON request body is decoded and passed as req.body.
func (s *svc) route(name string, params map[string]string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		return s.dispatch(c, name, params, readBody)
	}
}

// routeID is route with the :id path param threaded into params.
func (s *svc) routeID(name string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		return s.dispatch(c, name, map[string]string{"id": c.Param("id")}, readBody)
	}
}

// dispatch resolves the tenant, decodes the body, runs the bundle route on the
// tenant's Base store (one transaction per request), and writes {status, body}.
func (s *svc) dispatch(c *zip.Ctx, route string, params map[string]string, readBody bool) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body any
	if readBody {
		raw := c.Fiber().Body()
		if len(raw) > maxBody {
			return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				return zip.ErrBadRequest("invalid JSON body")
			}
		}
	}
	resp, err := s.host.Dispatch(c.Context(), org, gojabase.Request{
		Route:  route,
		Params: params,
		Body:   body,
	})
	if err != nil {
		s.log.Error("captable dispatch failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "captable dispatch failed")
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(resp.Status, resp.Body)
}

func init() {
	// Order 133: binds /v1/captable/* before the AI /v1/* catch-all (150) and
	// after the shared infra tier. STAGED (config.stagedSubsystems) — mounts only
	// when the operator names "captable" in CLOUD_ENABLE.
	cloud.RegisterWithShutdown("captable", 133, cloud.Typed(Mount), shutdown)
}

// shutdown closes the per-tenant stores + the goja engine. Idempotent.
func shutdown(context.Context) error {
	if mounted == nil || mounted.host == nil {
		return nil
	}
	err := mounted.host.Close()
	mounted = nil
	return err
}
