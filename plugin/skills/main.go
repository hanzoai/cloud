package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/skills"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the skills app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `skills openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "skills",
		Price: cloud.Free,
		Use:   skills.Use,
		// This subsystem serves the ROOT discovery convention
		// (/.well-known/agent-skills/…), so the /v1/<Name> default UsePrefixes
		// assumes covers NOTHING it registers: SubsystemOf resolved every request to
		// "" and PriceOf to Undeclared, and any middleware the subsystem installed
		// through its own router landed on "/v1/skills" and never ran. Same
		// defect as apps/plan, whose name was one letter off its prefix. The list
		// comes from the manifest so it cannot drift from what the host routes here.
		Prefixes: manifest.PrefixesFor("skills"),
	}}, []string{"skills"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
