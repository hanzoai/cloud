// Package captable is your cap table: stakeholders, share classes, grants, SAFEs,
// rounds, and who owns what.
//
// In full: stakeholders, share classes, share certificates and transfers, option
// grants and equity plans, SAFEs and convertible notes, priced rounds and their
// investments, and the summary that totals outstanding and fully-diluted ownership
// from them. It runs per tenant on Base/SQLite in the unified cloud binary (HIP-0106).
//
// WRAP, DON'T REWRITE — the read-WRITE variant. Where apps/plan + apps/pricing
// host a read-only @hanzo catalog in goja, captable hosts the tRPC
// business LOGIC (ported to a self-contained goja bundle in github.com/hanzoai/
// captable) and gives it PERSISTENCE over per-tenant Base/SQLite. The bundle
// carries logic; the Go host carries storage. The seam between them is the
// REUSABLE apps/goja binding (the RW-Base goja host), which esign (#100)
// and dataroom (#101) reuse unchanged — this leaf is just:
//
//	captable bundle (github.com/hanzoai/captable.Bundle)  +  the per-tenant Schema
//	                         │
//	                  apps/goja.NewBase(...)   ← injects __db/__newId/__now,
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
// TWENTY routes are TYPED ops, so they carry In/Out types and reach the document,
// the MCP tool list, the CLI and the generated SDKs: the eleven collection reads,
// the round detail read and the five deletes (typed.go, whose whole input is one
// path segment), plus the three writes whose bodies are made only of fields the
// bundle reads as strings (writes.go). Every one relays the bundle's own refusal
// bytes through bundleErr.
//
// ELEVEN body-carrying writes stay untyped relays, and the reason is the REQUEST,
// not the response. The bundle validates with COERCING helpers
// (goja/src/validate.ts): `num` accepts a number OR a numeric string, and
// stakeholders.add accepts an object OR an array. A Go float64 field refuses the
// numeric string those routes accept today — so typing one would make it accept
// LESS — and it cannot carry the refused token onward either, so the bundle's
// {success,message,errors} would become zip's envelope. Each line below says
// which of its fields does that.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/captable")
	// Bridge FIRST: a typed op receives only a context, so the validated org
	// reaches it by being parked there — never as an In field, which is
	// caller-supplied and would be a cross-tenant read the caller asserted for
	// itself. fiber runs middleware in registration order, so this must precede
	// the leaves below; it is prefix-scoped, and nesting under Serve's own Bridge
	// is harmless (the inner one is what the handler sees).
	g.Use(cloud.Bridge())
	// Then the bundle's own envelope: a typed op that must answer the bundle's
	// 400/404/409/500 returns a bundleErr, and this writes those bytes back
	// verbatim. Also before the leaves, for the same registration-order reason.
	g.Use(bundleEnvelope())

	// ---- the typed ops (typed.go carries the models and the prose) ----
	//
	// Declared on the GROUP, so each op's path is the prefix composed with its
	// leaf — the same composition the router does, and the identity every
	// projection keys on. cmd/zipdoc resolves the prefix the same way, so the doc
	// comments reach the document and the MCP tool list.
	//
	// Order is free here: no two /v1/captable routes overlap on method + pattern
	// (every DELETE has its own collection prefix, and the only two parameterised
	// POSTs differ in their third segment), so nothing below can shadow anything
	// else. TestEveryRouteIsTypedOrNamed counts the table either way.
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
	zip.Get(g, "/rounds/:id", o.getRound)
	zip.Delete(g, "/stakeholders/:id", o.deleteStakeholder)
	zip.Delete(g, "/shares/:id", o.deleteShare)
	zip.Delete(g, "/options/:id", o.deleteOption)
	zip.Delete(g, "/safes/:id", o.deleteSafe)
	zip.Delete(g, "/convertibles/:id", o.deleteConvertible)
	// The three body-carrying ops (writes.go). Their bodies are made only of
	// fields the bundle reads as STRINGS, and the scalar carrier hands each token
	// to the bundle unchanged — so the bundle stays the only validator and the
	// wire is the wire it always was.
	zip.Put(g, "/company", o.updateCompany)
	zip.Patch(g, "/stakeholders/:id", o.updateStakeholder)
	zip.Post(g, "/rounds/:id/close", o.closeRound)

	// ---- the untyped relays, and which field keeps each one untyped ----
	//
	// Every one of these carries a COERCED NUMBER — `num`/`intNum`/`optNum` take a
	// number or a numeric string — except stakeholders.add, whose whole body is a
	// union. A Go float64 field refuses the numeric string this route accepts, and
	// cannot carry the refused token onward to keep the bundle's own 400 envelope
	// either, so typing one would change both what the route accepts and how it
	// says no. See writes.go for the full argument.

	// stakeholders.add: the body is a single object OR an array (the tRPC
	// contract). A Go struct decodes one or the other, never both.
	g.Post("/stakeholders", route(s, "stakeholders.add", nil, true))
	// shareClasses.create: `initialSharesAuthorized`, `votesPerShare`, `parValue`,
	// `pricePerShare`, `seniority` and both multiples go through num/intNum, which
	// accept a numeric STRING.
	g.Post("/share-classes", route(s, "shareClasses.create", nil, true))
	// shareClasses.update: same coercing validator as create.
	g.Patch("/share-classes/:id", routeID(s, "shareClasses.update", true))
	// equityPlans.create: `initialSharesReserved` goes through intNum (numeric
	// string accepted), `comments` through optString.
	g.Post("/equity-plans", route(s, "equityPlans.create", nil, true))
	// shares.add: `quantity`, `pricePerShare` and `capitalContribution` are coerced
	// numbers; `companyLegends` is validated per element, not per array type.
	g.Post("/shares", route(s, "shares.add", nil, true))
	// shares.transfer: `quantity` OMITTED means "transfer the whole certificate",
	// which a typed In cannot say (its zero value means 0), and it is coerced too.
	g.Post("/shares/transfer", route(s, "shares.transfer", nil, true))
	// options.add: `quantity`, `exercisePrice`, `cliffYears` and `vestingYears` are
	// coerced numbers.
	g.Post("/options", route(s, "options.add", nil, true))
	// safes.create: `capital`, `valuationCap` and `discountRate` are coerced
	// numbers.
	g.Post("/safes", route(s, "safes.create", nil, true))
	// convertibles.create: `capital`, `conversionCap`, `discountRate` and
	// `interestRate` are coerced numbers.
	g.Post("/convertibles", route(s, "convertibles.create", nil, true))
	// rounds.create: `targetAmount`, `pricePerShare` and `preMoneyValuation` are
	// coerced numbers.
	g.Post("/rounds", route(s, "rounds.create", nil, true))
	// rounds.investments.add: `amount` is a coerced number and `date`/`comments`
	// go through optDateString/optString.
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
