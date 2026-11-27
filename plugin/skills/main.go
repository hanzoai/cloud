package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/skills"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
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
		// assumes covers NOTHING it registers. The grant is the subtree those three
		// addresses compose under; the row's own list is what the host ROUTES, which
		// is the three addresses and not the subtree iam also has documents in.
		Prefixes: manifest.GrantFor("skills"),
	}}, []string{"skills"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
