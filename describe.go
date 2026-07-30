package cloud

// `<binary> describe <dir>` — any cloud binary projects itself instead of serving.
//
// This is the ONE producer of a per-app artifact. An app's projections are not
// sliced out of the fleet's by prefix (that would make the fleet the source and
// the app a derivative, exactly backwards); they are generated from the app's OWN
// live router by the SAME openapi.FleetSpec and the SAME zip.App.MCPTools the
// whole fleet is generated from, over an app with only that subsystem mounted.
// Compose upward, never carve downward — see openapi/weave.go for the other half.
//
// TWO projections, ONE mount, one instant, one registry: openapi.json (what the
// app's addresses are) and mcp.json (what its typed ops are as MCP tools). They
// are written together and cannot be generated apart, so a tool cannot exist
// without its op, and a schema cannot go stale on one surface while the other
// moves. That is the whole honesty argument for the door: the reverse direction —
// "every typed op is a tool" — is not tested, it is unfalsifiable by construction.
//
// It lives on Serve because Serve is the single entry every app binary shares:
// plugin/<app>/main.go is generated as one cloud.Serve call, so putting the mode
// here gives every one of them the target at a cost of zero per-app code. A binary
// that mounts its own app (plugin/o11y) calls Describe directly.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// describeArg is the argv word that switches a binary from serving to describing.
const describeArg = "describe"

// SpecFile and ToolsFile are the two artifacts a describe run writes into the
// app's own plugin/<app> directory. Named here because the host EMBEDS ToolsFile
// (plugin/embed.go) and the weave READS SpecFile — one name each, so a rename
// cannot leave a reader looking for a file no writer produces.
const (
	SpecFile  = "openapi.json"
	ToolsFile = "mcp.json"
)

// DescribeRequested reports whether argv asks this binary to describe itself, and
// the DIRECTORY it named: `<binary> describe <dir>`. Read before any flag parsing
// — the mode is a mode, not an option.
//
// A DIRECTORY and not stdout, and the argument is required. There are two
// artifacts, so there is no single stream to write; and a subsystem's own
// dependencies write to stdout anyway (hanzoai/commerce prints a sqlite-vec
// warning and GORM debug lines at mount), which a `> file` redirect splices into
// the front of the document and turns into 71KB of invalid JSON. A writer whose
// output an unrelated library can corrupt is not a writer.
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

// Describe writes app's TWO projections into dir: the OpenAPI document and the
// MCP tool catalogue, from the one live router, in one pass.
//
// JSON for both, because JSON is what they ARE — the document is the same bytes
// served at /v1/openapi.json and dropped into hanzoai/openapi, and the catalogue
// is the same bytes the host hands zip as Plugin.Tools. Encoding to YAML would
// put a yaml library in the graph of every app binary to write a file only the
// weave reads. Indented so a subset reviews as a diff.
//
// Each artifact is rendered whole before its file is touched, so a projection
// failure leaves the previous one intact rather than truncating it into an app
// that appears to serve nothing. mcp.json is written for EVERY app, including the
// ones with no typed ops yet (an empty array), so the host's embed pattern is
// always satisfiable and "this app got its first typed op" shows as a diff in a
// file that already exists rather than as a new one nobody reviews.
func Describe(dir string, app *zip.App) error {
	if dir == "" {
		return fmt.Errorf("usage: %s %s <dir>", filepath.Base(os.Args[0]), describeArg)
	}
	doc, err := openapi.FleetSpec(app)
	if err != nil {
		return fmt.Errorf("openapi: %w", err)
	}
	spec, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	// MCPTools is already sorted by name (zip mcp.go), so this artifact is a
	// function of the op set and not of registration order — an edit that moved
	// nothing a client can see produces no diff.
	tools, err := json.MarshalIndent(app.MCPTools(), "", "  ")
	if err != nil {
		return fmt.Errorf("mcp: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, SpecFile), append(spec, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ToolsFile), append(tools, '\n'), 0o644)
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
	app := zip.New(zip.Config{Logger: deps.Logger, DisableStartupMessage: true})
	if err := MountAll(app, specs, cfg, deps); err != nil {
		return err
	}
	return Describe(dir, app)
}
