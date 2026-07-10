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
// from the VALIDATED cloud principal (principal.Tenant), never a client header,
// and that org selects the tenant's DB file AND scopes every row.
//
// ACTIVATION: captable is NOT staged — it mounts under the mount-all default
// (empty CLOUD_ENABLE), so the one binary serves /v1/captable/* from first boot.
// There is no standalone Captable,Inc pod to defer to (the Next.js/Prisma/Postgres
// app is retired by this fold — no such deployment runs in the fleet), so cloud's
// fresh per-tenant Base/SQLite is authoritative from the first write, with no data
// to migrate.
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
)

// maxBody caps a request body. Cap-table payloads are small structured records;
// anything larger is malformed or hostile.
const maxBody = 1 << 20 // 1 MiB

// state is captable's own data; shared deps live in the embedded cloud.Base.
type state struct {
	host *gojabase.Host
}

// mounted is the active service so Shutdown can release the per-tenant stores.
var mounted *cloud.Service[state]

// Mount wires the /v1/captable/* surface onto app per HIP-0106. Constructs the
// value directly (cloud.NewBase) — this subsystem keeps a package global for the
// Shutdown hook and opens a per-tenant goja host from deps.DataDir.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("captable.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("captable.Mount: nil deps.Logger")
	}
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
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "captable"), State: state{host: host}}
	mounted = s
	routes(app, s)

	s.Log.Info("captable mounted in-process (goja + per-tenant Base)",
		"prefix", "/v1/captable",
		"version", hcaptable.Version,
		"brand", deps.Brand,
		"env", deps.Env,
	)
	return nil
}

// routes wires the /v1/captable/* route table → bundle route names. GET reads
// carry no body; mutations do.
func routes(app *zip.App, s *cloud.Service[state]) {
	// company
	app.Get("/v1/captable/company", route(s, "company.get", nil, false))
	app.Put("/v1/captable/company", route(s, "company.update", nil, true))
	// stakeholders
	app.Get("/v1/captable/stakeholders", route(s, "stakeholders.list", nil, false))
	app.Post("/v1/captable/stakeholders", route(s, "stakeholders.add", nil, true))
	app.Patch("/v1/captable/stakeholders/:id", routeID(s, "stakeholders.update", true))
	app.Delete("/v1/captable/stakeholders/:id", routeID(s, "stakeholders.delete", false))
	// share classes
	app.Get("/v1/captable/share-classes", route(s, "shareClasses.list", nil, false))
	app.Post("/v1/captable/share-classes", route(s, "shareClasses.create", nil, true))
	app.Patch("/v1/captable/share-classes/:id", routeID(s, "shareClasses.update", true))
	// equity plans
	app.Get("/v1/captable/equity-plans", route(s, "equityPlans.list", nil, false))
	app.Post("/v1/captable/equity-plans", route(s, "equityPlans.create", nil, true))
	// shares (issuance + transfer). /shares/transfer registers before /shares/:id
	// (different methods anyway) so it can never be shadowed.
	app.Get("/v1/captable/shares", route(s, "shares.list", nil, false))
	app.Post("/v1/captable/shares", route(s, "shares.add", nil, true))
	app.Post("/v1/captable/shares/transfer", route(s, "shares.transfer", nil, true))
	app.Delete("/v1/captable/shares/:id", routeID(s, "shares.delete", false))
	// options
	app.Get("/v1/captable/options", route(s, "options.list", nil, false))
	app.Post("/v1/captable/options", route(s, "options.add", nil, true))
	app.Delete("/v1/captable/options/:id", routeID(s, "options.delete", false))
	// SAFEs
	app.Get("/v1/captable/safes", route(s, "safes.list", nil, false))
	app.Post("/v1/captable/safes", route(s, "safes.create", nil, true))
	app.Delete("/v1/captable/safes/:id", routeID(s, "safes.delete", false))
	// convertible notes
	app.Get("/v1/captable/convertibles", route(s, "convertibles.list", nil, false))
	app.Post("/v1/captable/convertibles", route(s, "convertibles.create", nil, true))
	app.Delete("/v1/captable/convertibles/:id", routeID(s, "convertibles.delete", false))
	// rounds + investments
	app.Get("/v1/captable/rounds", route(s, "rounds.list", nil, false))
	app.Post("/v1/captable/rounds", route(s, "rounds.create", nil, true))
	app.Get("/v1/captable/rounds/:id", routeID(s, "rounds.get", false))
	app.Post("/v1/captable/rounds/:id/close", routeID(s, "rounds.close", true))
	app.Post("/v1/captable/rounds/:id/investments", routeID(s, "rounds.investments.add", true))
	app.Get("/v1/captable/investments", route(s, "rounds.investments.list", nil, false))
	// computed cap table
	app.Get("/v1/captable/summary", route(s, "captable", nil, false))
}

// route builds a zip handler that dispatches a fixed bundle route. readBody
// controls whether the JSON request body is decoded and passed as req.body.
func route(s *cloud.Service[state], name string, params map[string]string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		return dispatch(s, c, name, params, readBody)
	}
}

// routeID is route with the :id path param threaded into params.
func routeID(s *cloud.Service[state], name string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		return dispatch(s, c, name, map[string]string{"id": c.Param("id")}, readBody)
	}
}

// dispatch resolves the tenant, decodes the body, runs the bundle route on the
// tenant's Base store (one transaction per request), and writes {status, body}.
func dispatch(s *cloud.Service[state], c *zip.Ctx, route string, params map[string]string, readBody bool) error {
	org, ok := principal.Tenant(c)
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
	resp, err := s.State.host.Dispatch(c.Context(), org, gojabase.Request{
		Route:  route,
		Params: params,
		Body:   body,
	})
	if err != nil {
		s.Log.Error("captable dispatch failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "captable dispatch failed")
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(resp.Status, resp.Body)
}

func init() {
	// Order 133: binds /v1/captable/* before the AI /v1/* catch-all (150) and
	// after the shared infra tier. NOT staged — mounts under the mount-all default.
	cloud.RegisterWithShutdown("captable", 133, cloud.Typed(Mount), shutdown)
}

// shutdown closes the per-tenant stores + the goja engine. Idempotent.
func shutdown(context.Context) error {
	if mounted == nil || mounted.State.host == nil {
		return nil
	}
	err := mounted.State.host.Close()
	mounted = nil
	return err
}
