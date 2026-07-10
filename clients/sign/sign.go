// Package sign folds hanzoai/sign (the Documenso fork — "open-source DocuSign")
// into the unified hanzoai/cloud binary as an in-process subsystem (HIP-0106,
// task #100, epic #96). Cloud serves the e-signature surface (/v1/sign/*) ITSELF
// — per tenant, on Base/SQLite — no Next.js/Remix pod, no Prisma, no Postgres.
//
// WRAP, DON'T REWRITE — the read-WRITE variant, reusing the SAME seam captable
// (the #96 pilot) established: the server-side domain (documents, recipients,
// fields, the signing flow/state machine, audit trail, completion) is ported to
// a self-contained goja bundle in github.com/hanzoai/sign; the REUSABLE
// clients/gojabase binding runs it and gives it PERSISTENCE over per-tenant
// Base/SQLite (__db/__newId/__now, one SQLite file per tenant, ONE transaction
// per request). This leaf adds ZERO storage glue of its own.
//
// THE HARD PART — PDF + PKI — is the one capability goja cannot provide: it is
// implemented as Go host-functions (signer.go: pdfcpu render + digitorus/pdfsign
// x509/PKCS#7 seal) and injected via the additive gojabase Config.HostFns as
// __pdf = { stamp, sign }. The signing-request/recipient/field/audit LOGIC and
// the seal ORCHESTRATION stay in the TS bundle; only the crypto/PDF primitive is
// Go. A real signed PDF comes out.
//
// TENANCY. Owner routes (/v1/sign/documents/*) resolve the tenant from the
// VALIDATED cloud principal (principal.Tenant), never a client header. Recipient
// token routes (/v1/sign/o/:org/sign/:token) are unauthenticated capability
// links: the :org segment selects the tenant DB and the crypto-random token
// authorizes — a wrong org simply cannot hold a valid token. gojabase pre-routes
// the bundle's db to that tenant, so isolation is a host property.
//
// ACTIVATION is the standard staged enable-list gate (config.stagedSubsystems):
// sign mounts /v1/sign/* ONLY when the operator names "sign" in CLOUD_ENABLE, so
// the mount-all default is unchanged until the standalone esign pod is cut over.
package sign

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/gojabase"
	"github.com/hanzoai/cloud/clients/principal"
	signbundle "github.com/hanzoai/sign"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// maxBody caps a request body. The create route carries a base64-encoded PDF, so
// this is generous (32 MiB) relative to captable's small structured records.
const maxBody = 32 << 20

type svc struct {
	host *gojabase.Host
	log  luxlog.Logger
}

// mounted is the active service so shutdown can release the per-tenant stores.
var mounted *svc

// Mount wires the /v1/sign/* surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("sign.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("sign.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "sign")
	if deps.DataDir == "" {
		return fmt.Errorf("sign.Mount: empty DataDir")
	}

	// Native health endpoint — always answers (HealthOwner), no JS, no auth.
	app.Get("/v1/sign/health", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "service": "sign"})
	})

	sg, err := newSigner(deps.DataDir)
	if err != nil {
		return fmt.Errorf("sign.Mount: signer: %w", err)
	}
	bundle, err := signbundle.Bundle()
	if err != nil {
		return fmt.Errorf("sign.Mount: load bundle: %w", err)
	}
	host, err := gojabase.New(gojabase.Config{
		Name:    "sign",
		Bundle:  bundle,
		Schema:  schema,
		DataDir: deps.DataDir,
		HostFns: map[string]any{"__pdf": sg.pdfHostObject()},
	})
	if err != nil {
		return fmt.Errorf("sign.Mount: gojabase host: %w", err)
	}
	s := &svc{host: host, log: log}
	mounted = s

	// Owner routes — tenant = validated principal org. GET reads carry no body.
	app.Post("/v1/sign/documents", s.owner("documents.create", nil, true))
	app.Get("/v1/sign/documents", s.owner("documents.list", nil, false))
	app.Get("/v1/sign/documents/:id", s.ownerID("documents.get", false))
	app.Post("/v1/sign/documents/:id/recipients", s.ownerID("recipients.add", true))
	app.Post("/v1/sign/documents/:id/fields", s.ownerID("fields.add", true))
	app.Post("/v1/sign/documents/:id/send", s.ownerID("documents.send", true))
	app.Get("/v1/sign/documents/:id/download", s.ownerID("documents.download", false))
	app.Get("/v1/sign/documents/:id/audit", s.ownerID("documents.audit", false))

	// Recipient token routes — tenant = :org path segment; capability = :token.
	app.Get("/v1/sign/o/:org/sign/:token", s.token("sign.view", false))
	app.Post("/v1/sign/o/:org/sign/:token/fields/:fieldId", s.token("sign.field", true))
	app.Post("/v1/sign/o/:org/sign/:token/complete", s.token("sign.complete", true))
	app.Post("/v1/sign/o/:org/sign/:token/reject", s.token("sign.reject", true))

	log.Info("sign mounted in-process (goja + per-tenant Base)",
		"prefix", "/v1/sign",
		"brand", deps.Brand,
		"env", deps.Env,
		"signer_cn", sg.cert.Subject.CommonName,
	)
	return nil
}

// owner builds a handler for a principal-gated route with fixed params.
func (s *svc) owner(route string, params map[string]string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := principal.Tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		return s.dispatch(c, route, org, params, readBody)
	}
}

// ownerID is owner with the :id path param threaded into params.
func (s *svc) ownerID(route string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := principal.Tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		return s.dispatch(c, route, org, map[string]string{"id": c.Param("id")}, readBody)
	}
}

// token builds a handler for an unauthenticated recipient capability route. The
// :org path segment selects the tenant DB; the bundle authorizes the :token
// against THAT org's recipients. All path params are threaded through.
func (s *svc) token(route string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		org := c.Param("org")
		if org == "" {
			return zip.ErrBadRequest("org required")
		}
		params := map[string]string{"org": org, "token": c.Param("token")}
		if fid := c.Param("fieldId"); fid != "" {
			params["fieldId"] = fid
		}
		return s.dispatch(c, route, org, params, readBody)
	}
}

// dispatch decodes the body, runs the bundle route on the tenant's Base store
// (one transaction per request via gojabase), and writes {status, body}.
func (s *svc) dispatch(c *zip.Ctx, route, tenant string, params map[string]string, readBody bool) error {
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
	resp, err := s.host.Dispatch(c.Context(), tenant, gojabase.Request{
		Route:  route,
		Params: params,
		Body:   body,
	})
	if err != nil {
		s.log.Error("sign dispatch failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "sign dispatch failed")
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(resp.Status, resp.Body)
}

func init() {
	// Order 145 — a per-org product control plane, mounted before hanzoai/ai's
	// /v1/* catch-all (150). STAGED (config.stagedSubsystems) — mounts only when
	// the operator names "sign" in CLOUD_ENABLE. cloud.HealthOwner: sign serves
	// its OWN /v1/sign/health in Mount, so the generic liveness route never
	// shadows it.
	cloud.RegisterWithShutdown("sign", 145, cloud.Typed(Mount), shutdown, cloud.HealthOwner)
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
