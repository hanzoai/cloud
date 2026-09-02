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
// carries logic; the Go host carries storage. The client between them is the
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
	"github.com/hanzoai/cloud/openapi"
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
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("captable.Use:  nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("captable.Use:  empty DataDir")
	}

	bundle, err := hcaptable.Bundle()
	if err != nil {
		return fmt.Errorf("captable.Use:  load bundle: %w", err)
	}
	host, err := goja.NewBase(goja.BaseConfig{
		Name:    "captable",
		Bundle:  bundle,
		Schema:  schema,
		DataDir: deps.DataDir,
		OnOpen:  seedCompany,
	})
	if err != nil {
		return fmt.Errorf("captable.Use:  goja NewBase host: %w", err)
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
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
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
// bytes through goja.BundleErr.
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
	// Then the bundle's own envelope: a typed op that must answer the bundle's
	// 400/404/409/500 returns a goja.BundleErr, and this writes those bytes back
	// verbatim. Also before the leaves, for the same registration-order reason.
	g.Use(goja.Envelope())

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
	zip.Get(g, "/classes", o.listShareClasses)
	zip.Get(g, "/plans", o.listEquityPlans)
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
	zip.Post(g, "/classes", o.createShareClass, zip.WithStatus(http.StatusCreated))
	// shareClasses.update: same coercing validator as create.
	zip.Patch(g, "/classes/:id", o.amendShareClass)
	// equityPlans.create: `initialSharesReserved` goes through intNum (numeric
	// string accepted), `comments` through optString.
	zip.Post(g, "/plans", o.createEquityPlan, zip.WithStatus(http.StatusCreated))
	// shares.add: `quantity`, `pricePerShare` and `capitalContribution` are coerced
	// numbers; `companyLegends` is validated per element, not per array type.
	zip.Post(g, "/shares", o.issueShares, zip.WithStatus(http.StatusCreated))
	// shares.transfer: `quantity` OMITTED means "transfer the whole certificate",
	// which a typed In cannot say (its zero value means 0), and it is coerced too.
	zip.Post(g, "/shares/transfer", o.transferShares)
	// options.add: `quantity`, `exercisePrice`, `cliffYears` and `vestingYears` are
	// coerced numbers.
	zip.Post(g, "/options", o.grantOptions, zip.WithStatus(http.StatusCreated))
	// safes.create: `capital`, `valuationCap` and `discountRate` are coerced
	// numbers.
	zip.Post(g, "/safes", o.recordSafe, zip.WithStatus(http.StatusCreated))
	// convertibles.create: `capital`, `conversionCap`, `discountRate` and
	// `interestRate` are coerced numbers.
	zip.Post(g, "/convertibles", o.recordConvertible, zip.WithStatus(http.StatusCreated))
	// rounds.create: `targetAmount`, `pricePerShare` and `preMoneyValuation` are
	// coerced numbers.
	zip.Post(g, "/rounds", o.openRound, zip.WithStatus(http.StatusCreated))
	// rounds.investments.add: `amount` is a coerced number and `date`/`comments`
	// go through optDateString/optString.
	// 201: an investment into a priced round MINTS a security, and the bundle says
	// so with created(). The transfer beside it answers 200 because it moves an
	// existing holding rather than creating one — a distinction the untyped relay
	// carried by passing the bundle's own status through, and which a typed op has
	// to declare. The suite caught this within a minute of the conversion.
	zip.Post(g, "/rounds/:id/investments", o.addInvestment, zip.WithStatus(http.StatusCreated))
}

// The eleven relays' prose, declared beside the wire facts above.
//
// The twenty typed ops carry their prose in their doc comments, which zipdoc lifts
// into zipdoc_gen.go. These eleven have no doc comment to lift — their handler is
// a dispatch closure over a bundle route name — so without this the document would
// publish eleven operationIds and nothing else: eleven SDK methods that cannot say
// what they write and eleven CLI commands with no help. openapi.Describe is the
// client for exactly the operations the wire refuses to type, and it cannot
// contradict the router: a description whose route is not registered never renders.
func init() {
	// True of all eleven, so it is stated once and appended rather than reworded
	// eleven times. Every one of them writes through the bundle on the caller's own
	// tenant store, so the tenancy, the envelope and the leniency are shared facts.
	const common = "\n\nWrites the caller's OWN cap table: the org resolved from the validated " +
		"principal selects the tenant's store and scopes every row, so there is no field by " +
		"which a caller can write into another company's table; a request with no validated org " +
		"is refused. The whole write runs in one transaction, so a refusal leaves nothing " +
		"behind. Validation is the cap-table bundle's and so is its refusal: a bad body comes " +
		"back as {success:false, message, errors:[…]} with the failing fields listed, and " +
		"numeric fields accept a number OR a numeric string. Bodies are capped at 1 MiB."

	openapi.Describe("/v1/captable/stakeholders", http.MethodPost,
		"Add stakeholders to the cap table",
		"Records the people and institutions that can hold equity — the rows every share, "+
			"option, SAFE, note and investment is issued to.\n\n"+
			"The body is ONE stakeholder object or an ARRAY of them, and the array is the point: "+
			"a whole roster loads in a single call. Email is the identity within the company, so "+
			"a stakeholder whose email is already on the table is SKIPPED rather than duplicated "+
			"or rejected — the 201 reports how many rows were actually inserted, which is what "+
			"makes re-running an import safe. Validation is all-or-nothing across the batch: one "+
			"bad entry refuses the whole array."+common)

	openapi.Describe("/v1/captable/classes", http.MethodPost,
		"Define a share class",
		"Creates a class of stock — its authorized share count, votes per share, par and issue "+
			"price, seniority, conversion rights and liquidation/participation multiples — which "+
			"is what shares, priced rounds and equity plans are then issued against.\n\n"+
			"Two fields are the company's to assign, not the caller's: the class index "+
			"auto-increments per company, and the certificate prefix is DERIVED from the class "+
			"type (CS for COMMON, PS for anything else), so a prefix in the body is ignored."+
			common)

	openapi.Describe("/v1/captable/classes/:id", http.MethodPatch,
		"Amend a share class",
		"Rewrites one share class — the amendment path for a class whose authorized count, "+
			"price, seniority or preference terms have changed.\n\n"+
			"It REPLACES the class rather than merging into it: every field is taken from this "+
			"body, so an omitted field resets to the create-time default instead of keeping its "+
			"current value. Send the full class. The index and the derived prefix are unchanged "+
			"by an amendment. An id that is not this company's is not found."+common)

	openapi.Describe("/v1/captable/plans", http.MethodPost,
		"Open an equity incentive plan",
		"Reserves a pool of shares out of a share class for option grants, with the board "+
			"approval and effective dates and what happens to cancelled options.\n\n"+
			"The share class must already exist in this company — a plan cannot reserve out of "+
			"nothing. Note the field name the bundle reads for the cancellation behaviour is "+
			"`defaultCancellatonBehavior`; that spelling is the wire, and a correctly spelled "+
			"key is simply not seen."+common)

	openapi.Describe("/v1/captable/shares", http.MethodPost,
		"Issue a share certificate",
		"Issues shares of a class to a stakeholder as a certificate: quantity, price and "+
			"capital contributed, the vesting cliff and term, the legends on the certificate, "+
			"and the issue, Rule 144, vesting-start and board-approval dates.\n\n"+
			"Both the stakeholder and the share class must already exist in this company, and "+
			"the certificate id must be unused there — a reused id is a conflict, never a "+
			"silent overwrite of an existing certificate."+common)

	openapi.Describe("/v1/captable/shares/transfer", http.MethodPost,
		"Transfer shares to another stakeholder",
		"Moves shares from one certificate to another stakeholder, in one atomic step.\n\n"+
			"OMITTING `quantity` transfers the WHOLE certificate, which simply reassigns it and "+
			"answers newShareId null — that is the difference between a full and a partial "+
			"transfer, and it is why quantity is absent rather than zero. A partial transfer "+
			"shrinks the source certificate and issues a NEW one to the recipient, so it "+
			"requires a `certificateId` for that new certificate and refuses a reused one. The "+
			"quantity must be between 1 and what the source certificate actually holds; the "+
			"recipient must be a stakeholder of this same company."+common)

	openapi.Describe("/v1/captable/options", http.MethodPost,
		"Grant options from an equity plan",
		"Records an option grant to a stakeholder under an equity plan — quantity, exercise "+
			"price, ISO/NSO type, cliff and vesting years, and the issue, expiration, "+
			"vesting-start, board-approval and Rule 144 dates.\n\n"+
			"The stakeholder and the equity plan must both already exist in this company, and "+
			"the grant id must be unused there — a reused grant id is a conflict, so a grant "+
			"can never be overwritten by a later one carrying the same number."+common)

	openapi.Describe("/v1/captable/safes", http.MethodPost,
		"Record a SAFE",
		"Records a Simple Agreement for Future Equity held by a stakeholder: the capital in, "+
			"the valuation cap and discount, MFN and pro-rata rights, pre- or post-money type, "+
			"and the issue and board-approval dates.\n\n"+
			"The stakeholder must already exist in this company, and the SAFE's public id must "+
			"be unused there — a reused id is a conflict rather than an overwrite. This records "+
			"the instrument; it does not convert it, so nothing is issued against a share class "+
			"until a round does that."+common)

	openapi.Describe("/v1/captable/convertibles", http.MethodPost,
		"Record a convertible note",
		"Records a convertible note held by a stakeholder: the principal, the conversion cap, "+
			"discount and interest rate, MFN, and the issue and board-approval dates.\n\n"+
			"The stakeholder must already exist in this company, and the note's public id must "+
			"be unused there — a reused id is a conflict rather than an overwrite. Like a SAFE, "+
			"this records the instrument only; conversion is not performed here."+common)

	openapi.Describe("/v1/captable/rounds", http.MethodPost,
		"Open a funding round",
		"Opens a round with its name, type and target amount. It starts OPEN with nothing "+
			"raised; investments are then added to it, and closing it is its own call.\n\n"+
			"A PRICED round is the constrained case: it requires a share class that exists in "+
			"this company and a price per share above zero, because that price is what converts "+
			"each investment into issued shares. Its pre-money valuation is optional. A "+
			"non-priced round carries none of the three."+common)

	openapi.Describe("/v1/captable/rounds/:id/investments", http.MethodPost,
		"Record an investment into a round",
		"Records what a stakeholder put into a round and adds it to the round's raised total.\n\n"+
			"On a PRICED round this ISSUES SHARES as well as recording the money: the amount is "+
			"divided by the round's price per share, rounded DOWN to whole shares, and a new "+
			"certificate for them is issued to the investor in the round's share class — so an "+
			"amount too small to buy one whole share is refused rather than recorded as a "+
			"zero-share investment. On a non-priced round the money is recorded and no shares "+
			"are issued.\n\n"+
			"The round must exist in this company and still be OPEN — a closed round refuses "+
			"further investment — and the investor must already be a stakeholder here. The date "+
			"defaults to today when omitted."+common)
}

// route builds a zip handler that dispatches a fixed bundle route. readBody
// controls whether the JSON request body is decoded and passed as req.body.
func route(s *cloud.Service[state], name string, params map[string]string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		return dispatch(s, c, name, params, readBody)
	}
}

// dispatch resolves the tenant, decodes the body, runs the bundle route on the
// tenant's Base store (one transaction per request), and writes {status, body}.
func dispatch(s *cloud.Service[state], c *zip.Ctx, route string, params map[string]string, readBody bool) error {
	org, ok := principal.Org(c)
	if !ok {
		return principal.Refused(c)
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
