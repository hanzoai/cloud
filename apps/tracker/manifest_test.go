package tracker_test

// The routed-surface gate for tracker. It is in an EXTERNAL test package
// (tracker_test) because it mounts the app the way a host does — through the
// exported Mount — and because internal/manifesttest imports cloud, which the
// in-package tests do not need.
//
// What it caught: /tracker (the embedded board SPA, the whole reason
// tracker.hanzo.ai exists) was registered by Mount and claimed by no manifest
// prefix, so in the fleet the host's page reached whoever owns "/" and the
// visitor got the console's HTML shell with a 200. Every other gate was green —
// the SPA is an untyped route, so it is in no openapi.json and the published-path
// oracle had nothing to compare.

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/tracker"
	"github.com/hanzoai/cloud/internal/manifesttest"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

func TestEveryRouteTrackerServesIsRoutedToIt(t *testing.T) {
	manifesttest.Case{
		Name:  "tracker",
		Mount: tracker.Mount,
		// zip's own per-process control plane (the document, the agent door, the
		// op plane) is served by the HOST for itself, not routed per app. Every
		// app inherits it, so no app's row claims it.
		Exempt: func(p string) bool {
			return strings.HasPrefix(p, "/.well-known/zip") ||
				strings.HasPrefix(p, "/mcp") ||
				strings.HasPrefix(p, "/openapi") ||
				p == "/health" || p == "/healthz" || p == "/readyz"
		},
	}.Run(t)
	t.Cleanup(func() { _ = tracker.Shutdown() })
}
