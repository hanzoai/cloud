// Package esign folds hanzoai/esign (the Documenso fork — "open-source DocuSign")
// into the unified hanzoai/cloud binary as an in-process subsystem (HIP-0106,
// task #100, epic #96). Cloud serves the e-signature surface (/v1/esign/*) ITSELF
// — per tenant, on Base/SQLite — no Next.js/Remix pod, no Prisma, no Postgres.
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
	"github.com/hanzoai/cloud/clients/goja"
	"github.com/hanzoai/cloud/clients/principal"
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
	if deps.Logger == nil {
		return fmt.Errorf("esign.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("esign.Mount: empty DataDir")
	}
	// Carry the pre-rename data directory over before anything opens a store
	// under the new name. Failing here aborts the boot on purpose: serving an
	// empty document store while signed documents sit orphaned under the old
	// name would look like data loss to every tenant.
	if err := migrateDataDir(deps.DataDir, deps.Logger); err != nil {
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
		deps.Logger.Error("deps.VFS is nil — PDF byte storage unavailable; serving /v1/esign/health only")
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
			return zip.ErrForbidden("X-Org-Id required")
		}
		return dispatch(s, c, route, org, params, readBody)
	}
}

// ownerID is owner with the :id path param threaded into params.
func ownerID(s *cloud.Service[state], route string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := principal.Org(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
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
