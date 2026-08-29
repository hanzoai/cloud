// Package esign is a document out for signature, signed and filed with an audit
// trail.
//
// It folds hanzoai/esign (the Documenso fork — "open-source DocuSign") into the
// unified hanzoai/cloud binary as an in-process subsystem (HIP-0106, task #100,
// epic #96). Cloud serves the e-signature surface (/v1/esign/*) ITSELF — per
// tenant, on Base/SQLite — no Next.js/Remix pod, no Prisma, no Postgres.
//
// WRAP, DON'T REWRITE — the read-WRITE variant, reusing the SAME client captable
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
// VALIDATED cloud principal, never a client header. Recipient
// token routes (/v1/esign/o/:org/sign/:token) are unauthenticated capability
// links: the crypto-random token is the whole credential, and it is what selects
// the tenant DB — resolved through the cross-tenant token index (index.go)
// BEFORE any per-tenant store is opened. The :org segment is the caller's claim
// about which tenant they mean and is only checked against that answer. NewBase
// pre-routes the bundle's db to the resolved tenant, so isolation is a host
// property.
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
	"fmt"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	signbundle "github.com/hanzoai/sign"
	luxlog "github.com/luxfi/log"
)

// maxBody caps a request body. The create route carries a base64-encoded PDF, so
// this is generous (32 MiB) relative to captable's small structured records.
const maxBody = 32 << 20

// state is esign's own data; shared deps live in the embedded cloud.Base.
type state struct {
	host  *goja.BaseHost
	index *tokenIndex
}

// mounted is the active service so shutdown can release the per-tenant stores.
var mounted *cloud.Service[state]

// Mount wires the /v1/esign/* surface onto app per HIP-0106. Constructs the value
// directly (cloud.NewBase) — this subsystem keeps a package global for the Shutdown
// hook and opens a per-tenant goja host + PKI signer from deps.DataDir.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("esign.Use:  nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("esign.Use:  empty DataDir")
	}
	// Carry the pre-rename data directory over before anything opens a store
	// under the new name. Failing here aborts the boot on purpose: serving an
	// empty document store while signed documents sit orphaned under the old
	// name would look like data loss to every tenant.
	if err := migrateDataDir(deps.DataDir, luxlog.Default()); err != nil {
		return fmt.Errorf("esign.Use:  %w", err)
	}

	// ONE registration, at the end, because a group is a sub-application and not a
	// prefix handle: zip's Group returns a NEW App included at that prefix
	// (compose.go:325), so asking for /v1/esign twice yields two Apps with two
	// middleware stacks, and middleware installed on the first does not reach a leaf
	// registered on the second. Health therefore cannot be registered on its own
	// group ahead of the rest — it is declared with them, and declare serves it
	// ALONE when the document plane could not be opened.
	s, err := open(deps)
	if err != nil {
		return err
	}
	declare(app, s)
	if s == nil {
		return nil // health-only: the surface below was never opened
	}
	mounted = s
	return nil
}

// open builds the document plane: the PKI signer, the goja host over per-tenant
// Base/SQLite, and the cross-tenant token index.
//
// It answers (nil, nil) for the two conditions esign can survive without going
// down, and both leave the deployment serving health and nothing else, because
// serving anything else would mean doing the wrong thing rather than nothing:
//
//   - no object storage. esign persists PDF BYTES on the object-storage client
//     (deps.VFS), never inline in the per-tenant SQLite — a 32 MiB base64 PDF in a
//     TEXT column would bloat the tenant DB and be re-copied on every read. Without
//     it the only alternative is writing PDFs into the tenant DB.
//   - no token index. It is what resolves a signing token to its tenant, and
//     resolving is what has to happen before any per-tenant store is touched.
//     Without it the only alternative is trusting the caller's own org segment.
func open(deps cloud.Deps) (*cloud.Service[state], error) {
	if deps.VFS == nil {
		luxlog.Default().Error("deps.VFS is nil — PDF byte storage unavailable; serving /v1/esign/health only")
		return nil, nil
	}
	sg, err := newSigner(deps.DataDir, deps.Env)
	if err != nil {
		return nil, fmt.Errorf("esign.Use:  signer: %w", err)
	}
	bundle, err := signbundle.Bundle()
	if err != nil {
		return nil, fmt.Errorf("esign.Use:  load bundle: %w", err)
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
		return nil, fmt.Errorf("esign.Use:  goja NewBase host: %w", err)
	}
	index, err := openTokenIndex(deps.DataDir)
	if err != nil {
		_ = host.Close()
		luxlog.Default().Error("esign token index failed — serving /v1/esign/health only", "err", err)
		return nil, nil
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "esign"), State: state{host: host, index: index}}
	s.Log.Info("esign mounted in-process (goja + per-tenant Base)",
		"prefix", "/v1/esign",
		"brand", deps.Brand,
		"env", deps.Env,
		"signer_cn", sg.cert.Subject.CommonName,
	)
	return s, nil
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

// shutdown closes the per-tenant stores + the goja engine + the token index.
// Idempotent.
func Shutdown(context.Context) error {
	if mounted == nil {
		return nil
	}
	var firstErr error
	if mounted.State.host != nil {
		if err := mounted.State.host.Close(); err != nil {
			firstErr = err
		}
	}
	if mounted.State.index != nil {
		if err := mounted.State.index.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	mounted = nil
	return firstErr
}
