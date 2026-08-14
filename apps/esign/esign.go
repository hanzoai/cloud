// Package esign is a document out for signature, signed and filed with an audit
// trail.
//
// It folds hanzoai/esign (the Documenso fork — "open-source DocuSign") into the
// unified hanzoai/cloud binary as an in-process subsystem (HIP-0106, task #100,
// epic #96). Cloud serves the e-signature surface (/v1/esign/*) ITSELF — per
// tenant, on Base/SQLite — no Next.js/Remix pod, no Prisma, no Postgres.
//
// WRAP, DON'T REWRITE — the read-WRITE variant, reusing the SAME seam captable
// (the #96 pilot) established: the server-side domain (documents, recipients,
// fields, the signing flow/state machine, audit trail, completion) is ported to
// a self-contained goja bundle in github.com/hanzoai/esign; the REUSABLE
// clients/goja binding runs it and gives it PERSISTENCE over per-tenant
// Base/SQLite (__db/__newId/__now, one SQLite file per tenant, ONE transaction
// per request). This leaf adds ZERO storage glue of its own.
//
// THE HARD PART — PDF + PKI — is the one capability goja cannot provide: it is
// implemented as Go host-functions (signer.go: pdfcpu render + digitorus/pdfsign
// x509/PKCS#7 seal) and injected via the additive goja BaseConfig.HostFns as
// __pdf = { stamp, sign }. The signing-request/recipient/field/audit LOGIC and
// the seal ORCHESTRATION stay in the TS bundle; only the crypto/PDF primitive is
// Go. A real signed PDF comes out.
//
// TENANCY. Owner routes (/v1/esign/documents/*) resolve the tenant from the
// VALIDATED cloud principal (principal.Org), never a client header. Recipient
// token routes (/v1/esign/o/:org/sign/:token) are unauthenticated capability
// links: the :org segment selects the tenant DB and the crypto-random token
// authorizes — a wrong org simply cannot hold a valid token. NewBase pre-routes
// the bundle's db to that tenant, so isolation is a host property.
//
// ACTIVATION: esign is NOT staged — it mounts under the mount-all default (empty
// CLOUD_ENABLE), so the one binary serves /v1/esign/* from first boot. The
// standalone esign pod holds NO tenant data (its SQLite has zero documents,
// recipients and users — only operational churn), so cloud's fresh per-tenant
// Base/SQLite is authoritative from the first write, with nothing to migrate; the
// empty esign pod is retired by this fold.
package esign

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	signbundle "github.com/hanzoai/sign"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// maxBody caps a request body. The create route carries a base64-encoded PDF, so
// this is generous (32 MiB) relative to captable's small structured records.
const maxBody = 32 << 20

// state is esign's own data; shared deps live in the embedded cloud.Base.
type state struct {
	host *goja.BaseHost
}

// mounted is the active service so shutdown can release the per-tenant stores.
var mounted *cloud.Service[state]

// Mount wires the /v1/esign/* surface onto app per HIP-0106. Constructs the value
// directly (cloud.NewBase) — this subsystem keeps a package global for the Shutdown
// hook and opens a per-tenant goja host + PKI signer from deps.DataDir.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("esign.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("esign.Mount: empty DataDir")
	}
	// Carry the pre-rename data directory over before anything opens a store
	// under the new name. Failing here aborts the boot on purpose: serving an
	// empty document store while signed documents sit orphaned under the old
	// name would look like data loss to every tenant.
	if err := migrateDataDir(deps.DataDir, luxlog.Default()); err != nil {
		return fmt.Errorf("esign.Mount: %w", err)
	}

	// Native health endpoint — always answers (HealthOwner), no JS, no auth.
	app.Get("/v1/esign/health", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "service": "esign"})
	})

	// esign persists PDF BYTES on the object-storage seam (deps.VFS), NOT inline in
	// the per-tenant SQLite — a 32 MiB base64 PDF in a TEXT column would bloat the
	// tenant DB and be re-copied on every read. NewBase injects it as __blob,
	// tenant-scoped. Without VFS esign cannot store documents, so serve health-only
	// (cloud stays up) rather than write PDFs into the tenant DB.
	if deps.VFS == nil {
		luxlog.Default().Error("deps.VFS is nil — PDF byte storage unavailable; serving /v1/esign/health only")
		return nil
	}

	sg, err := newSigner(deps.DataDir, deps.Env)
	if err != nil {
		return fmt.Errorf("esign.Mount: signer: %w", err)
	}
	bundle, err := signbundle.Bundle()
	if err != nil {
		return fmt.Errorf("esign.Mount: load bundle: %w", err)
	}
	host, err := goja.NewBase(goja.BaseConfig{
		Name:    "esign",
		Bundle:  bundle,
		Schema:  schema,
		DataDir: deps.DataDir,
		Blob:    deps.VFS, // PDF bytes go to object storage via __blob, not SQLite
		HostFns: map[string]any{"__pdf": sg.pdfHostObject()},
	})
	if err != nil {
		return fmt.Errorf("esign.Mount: goja NewBase host: %w", err)
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "esign"), State: state{host: host}}
	mounted = s
	routes(app, s)

	s.Log.Info("esign mounted in-process (goja + per-tenant Base)",
		"prefix", "/v1/esign",
		"brand", deps.Brand,
		"env", deps.Env,
		"signer_cn", sg.cert.Subject.CommonName,
	)
	return nil
}

// The prose for this surface. NOTHING here is a typed op and nothing can be: every
// route below is built by a handler FACTORY that closes over a bundle route name
// (owner / ownerID / token), because the domain lives in the ported JS bundle and
// this leaf is only the door to it. zipdoc lifts prose from a named handler's doc
// comment, and a closure has none, so without these declarations every operation
// would publish an operationId and nothing else — an SDK method that cannot explain
// itself and a CLI command with no help, for a surface that moves legally binding
// documents. The two doors below are the fact a reader must not get wrong, so each
// operation says which one it is behind. Declared through the same registry Register
// uses, so a description renders only while the router actually serves the route.
func init() {
	openapi.Describe("/v1/esign/health", http.MethodGet,
		"Whether the e-signature surface is mounted",
		"Answers ok whenever the subsystem is mounted. It is unauthenticated and takes no "+
			"tenant, and it is deliberately shallow: it is registered before the document host is "+
			"built, so it still answers on a deployment that came up WITHOUT object storage and "+
			"therefore serves nothing else. Read it as reachability, never as a promise that "+
			"documents can be stored.")

	// ---- the sender's door: a validated principal, scoped to its own org ----

	openapi.Describe("/v1/esign/documents", http.MethodPost,
		"Upload a PDF and open a draft ready for recipients and fields",
		"Creates a document from a base64 PDF and answers 201 with it in `DRAFT` — the state "+
			"where recipients and fields may still be added, and the only state they may. `title` "+
			"and `pdfBase64` are required; `signingOrder` chooses `PARALLEL` (the default, "+
			"everyone may sign at once) or `SEQUENTIAL`, and that choice is fixed for the "+
			"document's life.\n\n"+
			"The bytes go to object storage, not into the tenant database, and the ORIGINAL is "+
			"kept under its own key so it survives sealing untouched — a completed document can "+
			"always be compared against what was uploaded. Creation is recorded on the audit "+
			"trail.\n\n"+
			"This is the sender's door: a validated principal is required (403 without one) and "+
			"the document lands in that principal's OWN org. Isolation is physical rather than a "+
			"filter — each tenant has its own store — so another org's document id is simply not "+
			"there. Bodies over 32 MiB are refused with 413.")
	openapi.Describe("/v1/esign/documents", http.MethodGet,
		"Your org's documents, newest first",
		"Lists the caller org's documents with their status, recipients and timestamps, newest "+
			"first, capped at 200 — there is no paging, so treat it as the recent window rather "+
			"than a complete export. Requires a validated principal (403 without one) and reads "+
			"the caller's own tenant store, so no other org's documents can appear in it.")
	openapi.Describe("/v1/esign/documents/:id", http.MethodGet,
		"One document with its recipients and field layout",
		"Answers the document, its recipients with each one's read and signing status, and "+
			"every field with its type, page and position — the view a sender's UI renders, and "+
			"where the field ids come from. Requires a validated principal (403 without one) and "+
			"resolves the id in the caller's OWN tenant store, so another org's document id is a "+
			"404 rather than a refusal that would confirm it exists.")
	openapi.Describe("/v1/esign/documents/:id/recipients", http.MethodPost,
		"Add someone to a draft and mint their signing token",
		"Adds a recipient and answers 201 with their id and their signing TOKEN — the "+
			"crypto-random capability that is the only credential the signer's door accepts, so "+
			"this response is where the signing link is built from. `email` is required; `role` "+
			"defaults to `SIGNER`, and a `CC` recipient is recorded as already complete because "+
			"they are never asked to sign. `signingOrder` sets this recipient's position for a "+
			"sequential document.\n\n"+
			"Only while DRAFT: adding a recipient to a document already sent is a 409, because "+
			"the field layout and the turn order were fixed when it went out. Requires a "+
			"validated principal (403 without one), acts only on the caller's own tenant, and an "+
			"unknown document is a 404. The addition is recorded on the audit trail.")
	openapi.Describe("/v1/esign/documents/:id/fields", http.MethodPost,
		"Place a field on the page for one recipient to fill",
		"Adds a field — a signature, date, name, email or text box — at a page and position "+
			"for ONE named recipient, and answers 201 with its id. `recipientId` and a valid "+
			"`type` are required, and the recipient must belong to this document (400 "+
			"otherwise); page defaults to 1 and position defaults to the origin.\n\n"+
			"Fields are what make a recipient signable: a document cannot be sent while any "+
			"signing recipient has none. Only while DRAFT — adding a field to a sent document is "+
			"a 409. Requires a validated principal (403 without one), acts only on the caller's "+
			"own tenant, and an unknown document is a 404. The addition is recorded on the audit "+
			"trail.")
	openapi.Describe("/v1/esign/documents/:id/send", http.MethodPost,
		"Send the document out and get each signer's link",
		"Moves the document from `DRAFT` to `PENDING` and answers the signing tokens — one per "+
			"signing recipient, with the path to hand them — which is how the links reach the "+
			"people who must sign. Nothing is emailed by this call; delivering the links is the "+
			"caller's.\n\n"+
			"It refuses to send an unsignable document: no recipients at all is a 400, and so is "+
			"any signing recipient with no fields to fill, named in the error. Re-sending an "+
			"already-pending document is allowed and re-issues the same links rather than "+
			"restarting anything; a completed document is a 409. Requires a validated principal "+
			"(403 without one) and acts only on the caller's own tenant; an unknown document is "+
			"a 404. The send is recorded on the audit trail.")
	openapi.Describe("/v1/esign/documents/:id/download", http.MethodGet,
		"Download the document — the sealed PDF once it is complete",
		"Answers the document's current PDF as base64 with a `sealed` flag and a filename. "+
			"Before completion that is the original upload; once every signer has finished it is "+
			"the SEALED artifact — the field values rendered onto the page and a real x509 "+
			"PKCS#7 digital signature applied — and `sealed` is true. There is one `pdfBase64` "+
			"field either way, so `sealed` is what tells you which you are holding.\n\n"+
			"Requires a validated principal (403 without one) and resolves the id in the "+
			"caller's OWN tenant store, so another org's document id is a 404.")
	openapi.Describe("/v1/esign/documents/:id/audit", http.MethodGet,
		"The document's full audit trail, oldest first",
		"Answers every recorded event for the document in order — created, recipient added, "+
			"field created, sent, opened, each field inserted, each recipient completed or "+
			"rejected, and completion — with the actor and timestamp on each. This is the "+
			"evidence record behind a signature, so it is append-only and nothing in the surface "+
			"edits it.\n\n"+
			"Requires a validated principal (403 without one) and resolves the id in the "+
			"caller's OWN tenant store, so another org's document id is a 404.")

	// ---- the signer's door: no account, the token IS the credential ----

	openapi.Describe("/v1/esign/o/:org/sign/:token", http.MethodGet,
		"Open a document you were asked to sign, using your signing link",
		"Answers the document, the recipient it identifies, the fields THAT recipient must "+
			"fill, and the PDF to display. The first open also marks the recipient as having "+
			"opened it and records that on the audit trail, so this read has a side effect by "+
			"design.\n\n"+
			"This is the signer's door and it takes NO account: the signing token is the entire "+
			"credential, and it names the recipient, so a signer sees only their own fields and "+
			"never the other recipients' tokens. The `:org` segment selects which tenant's store "+
			"is opened, and the token is then looked up inside it — so a token presented under "+
			"the wrong org simply does not resolve. An unknown or wrong-org token is a 401, "+
			"never a hint that some other document exists.")
	openapi.Describe("/v1/esign/o/:org/sign/:token/fields/:fieldId", http.MethodPost,
		"Fill in one of your fields",
		"Records a value for one field and marks it inserted. A signature field takes `value` "+
			"with `isBase64` true for drawn image bytes, or false for a typed signature; a date, "+
			"name or email field falls back to today, the recipient's name or their email when "+
			"`value` is omitted; any other type requires one.\n\n"+
			"Nothing is sealed here — filling every field still leaves the document pending until "+
			"the completion call. The token is the whole credential and it bounds what can be "+
			"written: a field belonging to another recipient is refused with 401 even under a "+
			"valid token, an unknown field is a 404, and a field already filled is a 409. A "+
			"document not out for signature is a 409, as is a recipient who has already completed "+
			"or rejected. Under SEQUENTIAL order a signer whose turn has not come is refused 403 "+
			"until every earlier signer has signed. Each insertion is recorded on the audit "+
			"trail.")
	openapi.Describe("/v1/esign/o/:org/sign/:token/complete", http.MethodPost,
		"Finish signing — and seal the document if you were the last",
		"Marks this recipient as done and answers whether the DOCUMENT sealed with it. When "+
			"every signing recipient has completed, sealing happens right here in the same "+
			"call: the collected values are rendered onto the PDF, a real x509 PKCS#7 signature "+
			"is applied, the sealed bytes are stored beside the untouched original, and the "+
			"document moves to `COMPLETED`. Until then the answer is the recipient's own "+
			"completion with the document still pending.\n\n"+
			"It refuses to complete a half-filled signature: a recipient with any unfilled field "+
			"is a 400 naming how many remain. A document not out for signature is a 409, as is a "+
			"recipient who has already completed, and under SEQUENTIAL order a signer out of turn "+
			"is a 403. The token is the whole credential — no account, and a token that does not "+
			"resolve under `:org` is a 401. Sealing and completion are one transaction, so a "+
			"failure anywhere leaves the document exactly as it was.")
	openapi.Describe("/v1/esign/o/:org/sign/:token/reject", http.MethodPost,
		"Decline to sign, with an optional reason",
		"Records this recipient's refusal and moves the WHOLE DOCUMENT to `REJECTED` — one "+
			"declining signer ends it for everyone, and there is no route back: the document "+
			"cannot then be signed or completed. An optional `reason` is stored and written onto "+
			"the audit trail with the rejection, which is what the sender sees.\n\n"+
			"A document not out for signature is a 409, and so is a recipient who has already "+
			"signed or already rejected — a refusal cannot be taken back or repeated. The token "+
			"is the whole credential; one that does not resolve under `:org` is a 401.")
}

// routes wires the /v1/esign/* owner + recipient-token surface. The native
// /v1/esign/health route stays inline in Mount (registered before the host build).
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/esign")
	// Owner routes — tenant = validated principal org. GET reads carry no body.
	g.Post("/documents", owner(s, "documents.create", nil, true))
	g.Get("/documents", owner(s, "documents.list", nil, false))
	g.Get("/documents/:id", ownerID(s, "documents.get", false))
	g.Post("/documents/:id/recipients", ownerID(s, "recipients.add", true))
	g.Post("/documents/:id/fields", ownerID(s, "fields.add", true))
	g.Post("/documents/:id/send", ownerID(s, "documents.send", true))
	g.Get("/documents/:id/download", ownerID(s, "documents.download", false))
	g.Get("/documents/:id/audit", ownerID(s, "documents.audit", false))

	// Recipient token routes — tenant = :org path segment; capability = :token.
	g.Get("/o/:org/sign/:token", token(s, "sign.view", false))
	g.Post("/o/:org/sign/:token/fields/:fieldId", token(s, "sign.field", true))
	g.Post("/o/:org/sign/:token/complete", token(s, "sign.complete", true))
	g.Post("/o/:org/sign/:token/reject", token(s, "sign.reject", true))
}

// owner builds a handler for a principal-gated route with fixed params.
func owner(s *cloud.Service[state], route string, params map[string]string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := principal.Org(c)
		if !ok {
			return principal.Refused(c)
		}
		return dispatch(s, c, route, org, params, readBody)
	}
}

// ownerID is owner with the :id path param threaded into params.
func ownerID(s *cloud.Service[state], route string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := principal.Org(c)
		if !ok {
			return principal.Refused(c)
		}
		return dispatch(s, c, route, org, map[string]string{"id": c.Param("id")}, readBody)
	}
}

// token builds a handler for an unauthenticated recipient capability route. The
// :org path segment selects the tenant DB; the bundle authorizes the :token
// against THAT org's recipients. All path params are threaded through.
func token(s *cloud.Service[state], route string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		org := c.Param("org")
		if org == "" {
			return zip.ErrBadRequest("org required")
		}
		params := map[string]string{"org": org, "token": c.Param("token")}
		if fid := c.Param("fieldId"); fid != "" {
			params["fieldId"] = fid
		}
		return dispatch(s, c, route, org, params, readBody)
	}
}

// dispatch decodes the body, runs the bundle route on the tenant's Base store
// (one transaction per request via NewBase), and writes {status, body}.
func dispatch(s *cloud.Service[state], c *zip.Ctx, route, tenant string, params map[string]string, readBody bool) error {
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
	resp, err := s.State.host.Dispatch(c.Context(), tenant, goja.BaseRequest{
		Route:  route,
		Params: params,
		Body:   body,
	})
	if err != nil {
		s.Log.Error("esign dispatch failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "esign dispatch failed")
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(resp.Status, resp.Body)
}

// migrateDataDir carries the pre-rename data directory over to the current
// name. Both the per-tenant document stores ({DataDir}/{subsystem}/{tenant}.db)
// and the development signer's key material live under a directory named for
// the subsystem, so renaming sign->esign would otherwise leave every existing
// document unreachable.
//
// One-time and idempotent: it acts only when the old directory exists and the
// new one does not. When BOTH exist a merge would be ambiguous — which copy of
// a tenant's documents wins? — so it leaves them alone and says so, loudly
// enough to be found, rather than picking for the operator.
func migrateDataDir(dataDir string, log luxlog.Logger) error {
	old, cur := filepath.Join(dataDir, "sign"), filepath.Join(dataDir, "esign")
	if _, err := os.Stat(old); err != nil {
		return nil // nothing to carry over: a fresh deployment, or already migrated
	}
	if _, err := os.Stat(cur); err == nil {
		log.Warn("both esign data directories exist; leaving them as they are",
			"previous", old, "current", cur)
		return nil
	}
	if err := os.Rename(old, cur); err != nil {
		return fmt.Errorf("carry %s over to %s: %w", old, cur, err)
	}
	log.Info("carried esign data directory over from the previous name",
		"previous", old, "current", cur)
	return nil
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
