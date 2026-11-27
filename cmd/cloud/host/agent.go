package host

// THE AGENT'S DOOR, composed. openapi/agent.go writes and answers every document;
// this is the half only the composition root knows: which addresses outside the
// contract an app row answers, and which issuer and brand this deployment is.

import (
	"os"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// useAgent answers the door's documents from read, the index's reading of the
// document.
func useAgent(app *zip.App, read *openapi.Reading) {
	id := env.Or("CLOUD_BRAND", brand.Default)
	openapi.UseAgent(app, read, kept(), brand.Issuer(os.Getenv("CLOUD_IAM_ISSUER"), id), id)
}

// kept are the addresses an app row answers outside /v1 and the well-known files
// — iam's /login/oauth — which robots.txt keeps a crawler out of, read off the
// manifest so a row added there is kept out the day it lands.
func kept() []string {
	var out []string
	for _, a := range manifest.Apps {
		for _, p := range a.Prefixes {
			if p == spec.RootPath || strings.HasPrefix(p, spec.RootPath+"/") || strings.HasPrefix(p, "/.well-known/") {
				continue
			}
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
