package connectorruntime

import (
	"fmt"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// apExternals are the packages a connector's source resolves at RUNTIME
// against the in-process shim (shim.js) instead of bundling. The two
// @activepieces/* framework packages are always shimmed; @activepieces/shared
// is shimmed too because pieces import a handful of its pure helpers
// (isNil/isEmpty/assertNotNullOrUndefined) that the shim provides. Everything
// else a piece imports (its own relative files) is bundled into one program.
var apExternals = []string{
	"@activepieces/pieces-framework",
	"@activepieces/pieces-common",
	"@activepieces/shared",
}

// Bundle compiles ONE ActivePieces connector's TypeScript source tree into a
// single CommonJS program, with the framework packages left external so they
// resolve to the in-process shim at run time. This is the connector-ingest
// build step: run it once per connector (offline / CI), commit the JS blob,
// and the runtime compiles+executes that blob natively in goja — no Node.
//
// entryPoint is the connector's index.ts. extraExternal lets a heavier
// connector mark additional npm deps external (each then needs a shim); for
// the framework-only pieces (the long tail) apExternals alone suffice.
func Bundle(entryPoint string, extraExternal ...string) ([]byte, error) {
	res := api.Build(api.BuildOptions{
		EntryPoints: []string{entryPoint},
		Bundle:      true,
		Format:      api.FormatCommonJS,
		Platform:    api.PlatformNode,
		// ES2017 keeps async/await native (goja supports it) so the runtime
		// never has to drive a regenerator/generator downlevel.
		Target:    api.ES2017,
		Write:     false,
		LogLevel:  api.LogLevelSilent,
		Sourcemap: api.SourceMapNone,
		External:  append(append([]string{}, apExternals...), extraExternal...),
	})
	if len(res.Errors) > 0 {
		return nil, fmt.Errorf("connectorruntime: bundle %s: %s", entryPoint, esbuildErrs(res.Errors))
	}
	if len(res.OutputFiles) == 0 {
		return nil, fmt.Errorf("connectorruntime: bundle %s: no output", entryPoint)
	}
	return res.OutputFiles[0].Contents, nil
}

func esbuildErrs(errs []api.Message) string {
	var b strings.Builder
	for i, e := range errs {
		if i > 0 {
			b.WriteString("; ")
		}
		if e.Location != nil {
			fmt.Fprintf(&b, "%s:%d: %s", e.Location.File, e.Location.Line, e.Text)
		} else {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}
