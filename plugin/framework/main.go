package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/framework"

	// THE APP LANES, linked for their init() and nothing else.
	//
	// Neither is a subsystem and neither may become one: a collection IS a
	// framework DocType, an ERP master IS a framework DocType, and all CRUD,
	// permissions, tenancy, install and rendering are the framework's generic
	// surface already. Giving either its own plugin would be a second engine for
	// a model the one engine already serves.
	//
	// So the whole activation is the import. `framework.RegisterModule` runs at
	// init, which is exactly why this is blank: nothing here calls into them, and
	// without the import their init never runs — the module is never registered
	// and POST /v1/framework/modules/<m>/install answers for a module the engine
	// has never heard of. That is what both packages did until now, and erp's own
	// doc named this line as the fix.
	_ "github.com/hanzoai/cloud/apps/cms"
	_ "github.com/hanzoai/cloud/apps/crm"
	_ "github.com/hanzoai/cloud/apps/erp"
	// knowledge's init registers the kb module (kb.page/memory/source/connector/
	// link) the same way — without it, installing kb answers "unknown module" in
	// every deployment where framework is its own process, which is all of them.
	// Its index hooks also register here and FAIL OPEN in this process (no embed
	// client, no lexical store): a knowledge write lands and is picked up by
	// knowledge's own reindex, which is that subsystem's rebuild path.
	_ "github.com/hanzoai/cloud/apps/knowledge"
)

// Standalone entry for the framework app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `framework openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "framework",
		Price:    cloud.Free,
		Use:      framework.Use,
		Shutdown: cloud.CtxShutdown(framework.Shutdown),
	}}, []string{"framework"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
