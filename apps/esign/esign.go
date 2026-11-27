package esign

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud/goja"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/object"
	signbundle "github.com/hanzoai/esign"
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

// Use mounts e-signature, resolving the object store the deployment gives it.
//
// The store used to arrive as a field on Deps, resolved once for the whole fleet
// whether or not a given subsystem stored a byte. It is built here instead,
// because building it is free and because a subsystem that is handed another
// subsystem's handle is a subsystem that cannot be read on its own.
//
// The store is the ONLY thing separating this from useWith, which is what the
// tests drive: the resolution is the deployment's, the behaviour is the app's,
// and neither has to pretend to be the other.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("esign: nil app")
	}
	return useWith(app, deps, cloud.S3(luxlog.Default()))
}

// Mount wires the /v1/esign/* surface onto app per HIP-0106. Constructs the value
// directly (cloud.NewBase) — this subsystem keeps a package global for the Shutdown
// hook and opens a per-tenant goja host + PKI signer from cloud.DataDir().
func useWith(app cloud.Router, deps cloud.Deps, s3 object.Blobs) error {
	if cloud.DataDir() == "" {
		return fmt.Errorf("esign.Use:  empty DataDir")
	}
	// Carry the pre-rename data directory over before anything opens a store
	// under the new name. Failing here aborts the boot on purpose: serving an
	// empty document store while signed documents sit orphaned under the old
	// name would look like data loss to every tenant.
	if err := migrateDataDir(cloud.DataDir(), luxlog.Default()); err != nil {
		return fmt.Errorf("esign.Use:  %w", err)
	}

	// ONE registration, at the end, because a group is a sub-application and not a
	// prefix handle: zip's Group returns a NEW App included at that prefix
	// (compose.go:325), so asking for /v1/esign twice yields two Apps with two
	// middleware stacks, and middleware installed on the first does not reach a leaf
	// registered on the second. Health therefore cannot be registered on its own
	// group ahead of the rest — it is declared with them, and declare serves it
	// ALONE when the document plane could not be opened.
	s, err := open(deps, s3)
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
//     handed in, never inline in the per-tenant SQLite — a 32 MiB base64 PDF in a
//     TEXT column would bloat the tenant DB and be re-copied on every read. Without
//     it the only alternative is writing PDFs into the tenant DB.
//   - no token index. It is what resolves a signing token to its tenant, and
//     resolving is what has to happen before any per-tenant store is touched.
//     Without it the only alternative is trusting the caller's own org segment.
func open(deps cloud.Deps, s3 object.Blobs) (*cloud.Service[state], error) {
	if s3 == nil {
		luxlog.Default().Error("no object store — PDF byte storage unavailable; serving /v1/esign/health only")
		return nil, nil
	}
	sg, err := newSigner(cloud.DataDir(), cloud.Env())
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
		Migrate: migrate,
		DataDir: cloud.DataDir(),
		Blob:    s3, // PDF bytes go to object storage via __blob, not SQLite
		HostFns: map[string]any{"__pdf": sg.pdfHostObject()},
	})
	if err != nil {
		return nil, fmt.Errorf("esign.Use:  goja NewBase host: %w", err)
	}
	index, err := openTokenIndex(cloud.DataDir(), deps.MasterKey)
	if err != nil {
		_ = host.Close()
		luxlog.Default().Error("esign token index failed — serving /v1/esign/health only", "err", err)
		return nil, nil
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "esign"), State: state{host: host, index: index}}
	s.Log.Info("esign mounted in-process (goja + per-tenant Base)",
		"prefix", "/v1/esign",
		"brand", cloud.Brand(),
		"env", cloud.Env(),
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
