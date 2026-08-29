package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/event"
)

// Standalone entry for the event app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `event openapi`. Hand-owned — edit the spec below directly.
//
// IT DECLARES NO Prefixes, and that is what the fold bought. The app was called
// analytics and answered on six stems — /v1/analytics, /v1/errors, /v1/insights,
// /v1/replay, /v1/event and /v1/event.js — so UsePrefixes's /v1/<name> default
// covered ONE of them: cloud.Declare's index attributed the other five to no
// subsystem (the tracing and price lookups every request makes), and scope.Use
// could not install this app's own middleware on them, which is load-bearing
// because cloud.Bridge is what parks the validated org a TYPED op reads. The
// workaround was to restate the manifest's routing table here. One prefix, and
// the convention is true again.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:       "event",
		Price:      cloud.Free,
		Use:        event.Use,
		OwnsHealth: true,
		// The event plane owns two background attachments to the bus — the sink's
		// consumers and the ingest connection — so it has a shutdown to run. Without
		// this the drain outlived the process's own teardown and an in-flight insert
		// could be cut off mid-commit.
		Shutdown: event.Shutdown,
	}}, []string{"event"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
