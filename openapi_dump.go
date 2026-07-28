package cloud

// `<binary> openapi` — any cloud binary describes itself instead of serving.
//
// This is the ONE producer of a per-app spec subset. An app's document is not
// sliced out of the fleet's by prefix (that would make the fleet the source and
// the app a derivative, exactly backwards); it is generated from the app's OWN
// live router by the SAME openapi.FleetSpec the whole document is generated
// from, over an app with only that subsystem mounted. Compose upward, never
// carve downward — see openapi/weave.go for the other half.
//
// It lives on Serve because Serve is the single entry every app binary shares:
// cmd/<app>/main.go is generated as one cloud.Serve call, so putting the mode
// here gives every one of them the target at a cost of zero per-app code. A binary
// that mounts its own app (cmd/o11y) calls WriteSpec directly.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// specArg is the argv word that switches a binary from serving to describing.
const specArg = "openapi"

// SpecRequested reports whether argv asks this binary for its document, and the
// file it named: `<binary> openapi <file>`. Read before any flag parsing — the
// mode is a mode, not an option.
//
// A FILE and not stdout, and the argument is required. A subsystem's own
// dependencies write to stdout: hanzoai/commerce prints a sqlite-vec warning and
// GORM debug lines there at mount, which a `> file` redirect splices into the
// front of the document and turns into 71KB of invalid JSON. A writer whose
// output an unrelated library can corrupt is not a writer. `openapi /dev/stdout`
// remains available for a human reading it.
func SpecRequested() (string, bool) {
	if len(os.Args) > 1 && os.Args[1] == specArg {
		if len(os.Args) > 2 {
			return os.Args[2], true
		}
		return "", true
	}
	return "", false
}

// SpecConfig is the deployment the PUBLISHED document describes, and the whole
// of it — zero values everywhere else, on purpose. The returned func removes the
// throwaway data dir.
//
// A published spec must be a function of the code alone. Config decides routes:
// clients/kms registers its secret routes only when a master key resolved, and
// several subsystems gate on brand. If this read the environment, the document
// two developers generated from one commit would differ by whichever CLOUD_*
// variables their shells carried, and the golden would flap in CI for a reason
// no diff could explain. So it reads nothing.
//
// The data dir is a throwaway and is created HERE rather than taken as an
// argument, because mounting opens real stores: the default is /var/lib/cloud,
// and a caller that forgot to override it would either migrate a live store or
// (as cmd/o11y did) fail on it.
func SpecConfig() (*Config, func(), error) {
	dir, err := os.MkdirTemp("", "openapi-spec-*")
	if err != nil {
		return nil, nil, err
	}
	return &Config{Brand: DefaultBrand, Domain: "api.hanzo.ai", DataDir: dir},
		func() { os.RemoveAll(dir) }, nil
}

// WriteSpec writes app's document to path, as indented JSON.
//
// JSON, not YAML, because JSON is what the document IS — the same bytes served
// at /v1/openapi.json and dropped into hanzoai/openapi — and because encoding to
// YAML would put a yaml library in the graph of every app binary to write a
// file only the weave reads. Indented so a subset reviews as a diff.
//
// The whole document is rendered before the file is touched, so a mount or
// projection failure leaves the previous artifact intact rather than truncating
// it into an app that appears to serve nothing.
func WriteSpec(path string, app *zip.App) error {
	if path == "" {
		return fmt.Errorf("usage: %s %s <file>", filepath.Base(os.Args[0]), specArg)
	}
	doc, err := openapi.FleetSpec(app)
	if err != nil {
		return fmt.Errorf("openapi: %w", err)
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// dumpSpec mounts specs into a throwaway app and writes its document.
//
// Enablement is cfg's default, NOT the forced single-service list Serve applies:
// this is the fleet's document, and a STAGED subsystem (config.go's
// stagedSubsystems) is linked but inert until a deployment names it. Describing
// one here would publish routes api.hanzo.ai does not serve, and would make the
// woven document disagree with the fully-mounted golden — which is the equality
// the composition proof rests on.
func dumpSpec(specs []MountSpec, path string) error {
	cfg, done, err := SpecConfig()
	if err != nil {
		return err
	}
	defer done()

	deps := BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: deps.Logger, DisableStartupMessage: true})
	if err := MountAll(app, specs, cfg, deps); err != nil {
		return err
	}
	return WriteSpec(path, app)
}
