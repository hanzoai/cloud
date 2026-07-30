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
// REUSABLE clients/goja binding (the RW-Base goja host), which esign (#100)
// and dataroom (#101) reuse unchanged — this leaf is just:
//
//	captable bundle (github.com/hanzoai/captable.Bundle)  +  the per-tenant Schema
//	                         │
//	                  clients/goja.NewBase(...)   ← injects __db/__newId/__now,
//	                         │                       one SQLite file per tenant,
//	                  /v1/captable/* zip routes     one transaction per request
//
// No Prisma, no Postgres, no Next.js in this path. Every route resolves the org
// from the VALIDATED cloud principal (principal.Org), never a client header,
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
	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// maxBody caps a request body. Cap-table payloads are small structured records;
// anything larger is malformed or hostile.
const maxBody = 1 << 20 // 1 MiB

// state is captable's own data; shared deps live in the embedded cloud.Base.
type state struct {
	host *goja.BaseHost
}

// mounted is the active service so Shutdown can release the per-tenant stores.
var mounted *cloud.Service[state]

// Mount wires the /v1/captable/* surface onto app per HIP-0106. Constructs the
// value directly (cloud.NewBase) — this subsystem keeps a package global for the
// Shutdown hook and opens a per-tenant goja host from deps.DataDir.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("captable.Mount: nil app")
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
	host, err := goja.NewBase(goja.BaseConfig{
		Name:    "captable",
		Bundle:  bundle,
		Schema:  schema,
		DataDir: deps.DataDir,
		OnOpen:  seedCompany,
	})
	if err != nil {
		return fmt.Errorf("captable.Mount: goja NewBase host: %w", err)
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

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes wires the /v1/captable/* route table → bundle route names, in TWO
// planes over one dispatch.
//
// The READS are TYPED ops (typed.go), so they carry In/Out types and reach the
// document, the MCP tool list, the CLI and the generated SDKs. The WRITES stay
// untyped relays, each because typing it would MOVE THE WIRE, and each says why
// below. The shared reason is that this surface relays the goja bundle's own
// (status, body): a write answers 400 {success,message,errors} on a validation
// failure and 404/409 {success,message} otherwise, and a typed op's failure path
// can only render zip's {status,code,error}. Several also read the request body
// VERBATIM in ways a Go struct cannot express — see each line.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/captable")
	// Bridge FIRST: a typed op receives only a context, so the validated org
	// reaches it by being parked there — never as an In field, which is
	// caller-supplied and would be a cross-tenant read the caller asserted for
	// itself. fiber runs middleware in registration order, so this must precede
	// the leaves below; it is prefix-scoped, and nesting under Serve's own Bridge
	// is harmless (the inner one is what the handler sees).
	g.Use(cloud.Bridge())

	// ---- the typed reads (typed.go carries the models and the prose) ----
	//
	// Declared on the GROUP, so each op's path is the prefix composed with its
	// leaf — the same composition the router does, and the identity every
	// projection keys on. cmd/zipdoc resolves the prefix the same way, so the doc
	// comments reach the document and the MCP tool list.
	o := ops{s: s}
	zip.Get(g, "/company", o.getCompany)
	zip.Get(g, "/stakeholders", o.listStakeholders)
	zip.Get(g, "/share-classes", o.listShareClasses)
	zip.Get(g, "/equity-plans", o.listEquityPlans)
	zip.Get(g, "/shares", o.listShares)
	zip.Get(g, "/options", o.listOptions)
	zip.Get(g, "/safes", o.listSafes)
	zip.Get(g, "/convertibles", o.listConvertibles)
	zip.Get(g, "/rounds", o.listRounds)
	zip.Get(g, "/investments", o.listInvestments)
	zip.Get(g, "/summary", o.getSummary)

	// ---- the untyped relays, and why each one is still untyped ----

	// company.update: 400 {success,message,errors} when `name` is missing.
	g.Put("/company", route(s, "company.update", nil, true))
	// stakeholders.add: the body is a single object OR an array (the tRPC
	// contract), and a bad enum/email answers 400 with the bundle's error list.
	g.Post("/stakeholders", route(s, "stakeholders.add", nil, true))
	// stakeholders.update: a PARTIAL update keyed on `key !== undefined`, so a
	// typed In would send zero values for omitted fields and overwrite them; 404
	// on an unknown id.
	g.Patch("/stakeholders/:id", routeID(s, "stakeholders.update", true))
	// stakeholders.delete: 400 when the holder still holds equity, 404 otherwise.
	g.Delete("/stakeholders/:id", routeID(s, "stakeholders.delete", false))
	// shareClasses.create: 400 with the bundle's validation list.
	g.Post("/share-classes", route(s, "shareClasses.create", nil, true))
	// shareClasses.update: 400 with the validation list, 404 on an unknown id.
	g.Patch("/share-classes/:id", routeID(s, "shareClasses.update", true))
	// equityPlans.create: 400 with the validation list, including the
	// shareClassId referential check.
	g.Post("/equity-plans", route(s, "equityPlans.create", nil, true))
	// shares.add: 400 with the validation list, 409 on a duplicate certificate id.
	g.Post("/shares", route(s, "shares.add", nil, true))
	// shares.transfer: `quantity` OMITTED means "transfer the whole certificate",
	// which a typed In cannot say (its zero value means 0); 400/404/409 besides.
	// Registered before /shares/:id (different methods anyway) so it can never be
	// shadowed.
	g.Post("/shares/transfer", route(s, "shares.transfer", nil, true))
	// shares.delete: 404 {success,message} on an unknown id.
	g.Delete("/shares/:id", routeID(s, "shares.delete", false))
	// options.add: 400 with the validation list, 409 on a duplicate grant id.
	g.Post("/options", route(s, "options.add", nil, true))
	// options.delete: 404 {success,message} on an unknown id.
	g.Delete("/options/:id", routeID(s, "options.delete", false))
	// safes.create: 400 with the validation list, 409 on a duplicate SAFE id.
	g.Post("/safes", route(s, "safes.create", nil, true))
	// safes.delete: 404 {success,message} on an unknown id.
	g.Delete("/safes/:id", routeID(s, "safes.delete", false))
	// convertibles.create: 400 with the validation list, 409 on a duplicate id.
	g.Post("/convertibles", route(s, "convertibles.create", nil, true))
	// convertibles.delete: 404 {success,message} on an unknown id.
	g.Delete("/convertibles/:id", routeID(s, "convertibles.delete", false))
	// rounds.create: 400 with the validation list, including the priced-round
	// price and share-class checks.
	g.Post("/rounds", route(s, "rounds.create", nil, true))
	// rounds.get: 404 {success,message} on an unknown id — pinned as the bundle's
	// OWN 404 by TestHTTPEndToEnd.
	g.Get("/rounds/:id", routeID(s, "rounds.get", false))
	// rounds.close: `closeDate` OMITTED means today; 404 when no OPEN round matches.
	g.Post("/rounds/:id/close", routeID(s, "rounds.close", true))
	// rounds.investments.add: `date` OMITTED means today; 400/404 besides.
	g.Post("/rounds/:id/investments", routeID(s, "rounds.investments.add", true))
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
	resp, err := s.State.host.Dispatch(c.Context(), org, goja.BaseRequest{
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

// shutdown closes the per-tenant stores + the goja engine. Idempotent.
func Shutdown(context.Context) error {
	if mounted == nil || mounted.State.host == nil {
		return nil
	}
	err := mounted.State.host.Close()
	mounted = nil
	return err
}
