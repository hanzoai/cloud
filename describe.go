package cloud

// `<binary> describe <dir>` — any cloud binary projects itself instead of serving.
//
// This is the ONE producer of a per-app artifact. An app's projections are not
// sliced out of the fleet's by prefix (that would make the fleet the source and
// the app a derivative, exactly backwards); they are generated from the app's OWN
// live router by the SAME openapi.FleetSpec the whole fleet is generated from,
// over an app with only that subsystem mounted.
// Compose upward, never carve downward — see openapi/weave.go for the other half.
//
// ONE projection, from ONE mount of ONE registry: openapi.json, what the app's
// addresses are.
//
// It used to write mcp.json beside it — the same registry projected as MCP tools
// — and argued that writing them together made them agree. They did agree, and
// they were BOTH WRONG BY THE SAME 353 OPS: coupling two derived files to each
// other makes neither true, because nothing in that coupling forces either back
// to the registry. The tool catalogue is not generated any more; the host asks
// the child for it (package fleet), so a tool list cannot be stale because there
// is no tool list. This document survives only because the fleet weave needs the
// app's SOURCE synopsis, which no running process can hand back.
//
// It lives on Serve because Serve is the single entry every app binary shares:
// plugin/<app>/main.go is generated as one cloud.Listen call, so putting the mode
// here gives every one of them the target at a cost of zero per-app code. A binary
// that mounts its own app (plugin/o11y) calls Describe directly.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// describeArg is the argv word that switches a binary from serving to describing.
const describeArg = "describe"

// SpecFile is the artifact a describe run writes into the app's own plugin/<app>
// directory. Named here because the host EMBEDS it (plugin/embed.go) and the
// weave READS it — one name, so a rename cannot leave a reader looking for a file
// no writer produces.
const SpecFile = "openapi.json"

// DescribeRequested reports whether argv asks this binary to describe itself, and
// the DIRECTORY it named: `<binary> describe <dir>`. Read before any flag parsing
// — the mode is a mode, not an option.
//
// A DIRECTORY and not stdout, and the argument is required. A subsystem's own
// dependencies write to stdout at mount (hanzoai/commerce prints a sqlite-vec
// warning and GORM debug lines), which a `> file` redirect splices into the front
// of the document and turns into 71KB of invalid JSON. A writer whose output an
// unrelated library can corrupt is not a writer.
func DescribeRequested() (string, bool) {
	if len(os.Args) > 1 && os.Args[1] == describeArg {
		if len(os.Args) > 2 {
			return os.Args[2], true
		}
		return "", true
	}
	return "", false
}

// SpecConfig is the deployment the PUBLISHED artifacts describe, and the whole
// of it — zero values everywhere else, on purpose. The returned func removes the
// throwaway data dir.
//
// A published spec must be a function of the code alone. Config decides routes:
// clients/kms registers its secret routes only when a master key resolved, and
// several subsystems gate on brand. If this read the environment, the artifacts
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

// Describe writes app's projection into dir: the OpenAPI document, from the one
// live router.
//
// JSON, because JSON is what it IS — the same bytes served at /v1/openapi.json
// and dropped into hanzoai/openapi. Encoding to YAML would put a yaml library in
// the graph of every app binary to write a file only the weave reads. Indented so
// a subset reviews as a diff.
//
// It is rendered whole before the file is touched, so a projection failure leaves
// the previous one intact rather than truncating it into an app that appears to
// serve nothing.
func Describe(dir string, app *zip.App) error {
	if dir == "" {
		return fmt.Errorf("usage: %s %s <dir>", filepath.Base(os.Args[0]), describeArg)
	}
	doc, err := openapi.FleetSpec(app)
	if err != nil {
		return fmt.Errorf("openapi: %w", err)
	}
	// An address the fleet delivers to a SIBLING is not this app's to publish.
	//
	// ai is the case: its module registers real routes now (apps/ai composes the
	// whole app rather than a wildcard door), so mounted alone it carries
	// /v1/crawl and /v1/metrics — addresses the manifest routes to the apps that
	// own those prefixes, where this binary's handler never answers. The relay's
	// Yields used to subtract exactly this at the door; a real route is the
	// host's own registration and wins its address, so the door never sees it
	// and the subtraction has to live at the one producer instead. It asks the
	// same routing table the host reads, so a dropped address is the fleet's
	// answer, not a judgment call — and the owner's own subset still carries the
	// operation, which is what the weave requires: one address, one app. The dir
	// names the app because the describe contract writes into plugin/<app>.
	self := filepath.Base(filepath.Clean(dir))
	for path := range doc.Paths {
		if owner := manifest.OwnerOf(path); owner != "" && owner != self {
			delete(doc.Paths, path)
		}
	}
	// The gap is refused HERE, at the one producer, and nowhere downstream.
	//
	// This is the file that mints the artifact eight SDKs, the MCP tool list, the
	// CLI and docs.hanzo.ai are all projections of, so an operation that says
	// nothing about itself becomes a call nobody can explain in every one of them
	// at once — and each of those consumers is a place where the sentence cannot
	// be written. A projection that copes (a placeholder, or the route printed
	// where the description belongs) does not report the gap, it disguises it. So
	// the document is simply not written: the failure names the app, the route and
	// the remedy, in the repo that holds the handler.
	//
	// Refused at the artifact, not in [openapi.Spec], because the two have opposite
	// duties. A deployment's own /v1/openapi.json must answer with what it serves
	// even if a subsystem it mounts is behind on its prose; a COMMITTED artifact is
	// the fleet's contract and has no such excuse.
	//
	// manifest.OwnerOf is handed in because "whose address is this?" is a ROUTING
	// question, and the manifest is the routing table the host itself reads. Asking
	// it here means the gate attributes a declaration exactly as the fleet delivers
	// the request — the two cannot drift, and openapi stays a projection that owes
	// nothing to the fleet's shape.
	if err := openapi.Complete(doc, manifest.OwnerOf); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(dir), err)
	}
	// A subset describes ONE app, so it says what that app is — the synopsis of
	// the package its binary mounts, read from the source the app is built from
	// (openapi.Synopsis). The fleet identity FleetSpec carries is the fallback and
	// stays exactly that: an app whose package has no doc comment publishes the
	// fleet's sentence, unchanged, rather than a sentence invented for it here.
	//
	// This is the ONE place the synopsis is computed. The weave reads it back off
	// the subsets to describe the product tags, so the mapping from app to prose
	// exists once and travels with the artifact.
	if s := openapi.Synopsis(dir); s != "" {
		doc.Info.Description = s
	}
	spec, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, SpecFile), append(spec, '\n'), 0o644)
}

// describe mounts specs into a throwaway app and writes its projections.
//
// Enablement is cfg's default, NOT the forced single-service list Serve applies:
// this is the fleet's document, and a STAGED subsystem (config.go's
// a deployment does not name) is linked but inert until it does. Describing
// one here would publish routes api.hanzo.ai does not serve, and would make the
// woven document disagree with the fully-mounted golden — which is the equality
// the composition proof rests on.
func describe(specs []Plugin, dir string) error {
	cfg, done, err := SpecConfig()
	if err != nil {
		return err
	}
	defer done()

	deps := BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: luxlog.Default(), DisableStartupMessage: true})
	if err := MountAll(app, specs, cfg, deps); err != nil {
		return err
	}
	return Describe(dir, app)
}
