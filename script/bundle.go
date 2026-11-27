package script

import (
	"fmt"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// frameworkModules are the packages a piece resolves at RUNTIME against the
// in-process shim (shim.js) instead of bundling: the ActivePieces authoring
// framework, its HTTP helpers, and the handful of pure helpers pieces import
// from shared. Everything else a piece imports (its own relative files) is
// bundled into one program.
var frameworkModules = []string{
	"@activepieces/pieces-framework",
	"@activepieces/pieces-common",
	"@activepieces/providers-framework",
	"@activepieces/providers-common",
	"@activepieces/shared",
}

// Bundle compiles ONE ActivePieces piece's TypeScript source tree into a
// single CommonJS program, with the framework packages left external so they
// resolve to the in-process shim at run time. This is the piece-ingest build
// step (cmd/piece): run once per piece, commit the JS blob, and the runtime
// compiles and executes that blob natively in goja — no Node.
//
// A piece that imports any other npm package fails here, which is the answer
// to "can this piece run in-process": its program would need a module the
// runtime does not have.
func Bundle(entryPoint string) ([]byte, error) {
	return build(entryPoint, true)
}

// BundleOpen compiles a piece with EVERY package import left external. The
// program it yields cannot run, but it can describe itself (Describe), which
// is all the catalog needs from a piece whose actions use an SDK.
func BundleOpen(entryPoint string) ([]byte, error) {
	return build(entryPoint, false, []api.Plugin{{
		Name: "open",
		Setup: func(b api.PluginBuild) {
			b.OnResolve(api.OnResolveOptions{Filter: `^[^./]`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: a.Path, External: true}, nil
			})
		},
	}}...)
}

// build bundles one entry. A vendored program's syntax is minified, never its
// identifiers or its line breaks: an error a piece throws still names its own
// functions, and each statement stays on a line of its own, so a scan that
// reads a program line by line reads the piece's statements, not one line that
// is the whole program.
func build(entryPoint string, minify bool, plugins ...api.Plugin) ([]byte, error) {
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
		// goja runs a script, not a module, so it has no import(). Declaring
		// the feature absent makes esbuild lower each one to a require() the
		// shim answers.
		Supported: map[string]bool{"dynamic-import": false},
		External:  frameworkModules,
		Plugins:   plugins,

		MinifySyntax: minify,
	})
	if len(res.Errors) > 0 {
		return nil, fmt.Errorf("script: bundle %s: %s", entryPoint, esbuildErrs(res.Errors))
	}
	if len(res.OutputFiles) == 0 {
		return nil, fmt.Errorf("script: bundle %s: no output", entryPoint)
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
