package o11y

import (
	"net/url"
	"strings"
	"testing"
)

// A probe URL that names no route measures the static file server.
//
// visor's probe read `visor.hanzo.svc:19000/health`. visor serves its health as
// a typed op at `/v1/health`, and everything outside `/v1/` falls through to a
// static passthrough that answers 200 with index.html. So the probe was green
// while the store behind it was down — it was measuring the file server, and a
// probe that cannot go red is worse than no probe, because it is believed.
//
// This is the SECOND time tonight that exact shape appeared: visor's own
// liveness and readiness probes requested `/api/health`, also not a route, also
// served the SPA at 200, so a pod with a dead database stayed in the Service.
// One bug found twice in two repos is a pattern, and a pattern earns a gate.
//
// The rule is narrow on purpose. It cannot know which path a given service
// serves — that is the service's business — so it refuses only what is provably
// wrong: an empty path, and `/health` on a service that answers `/v1/`. Anything
// it cannot prove wrong, it permits. A gate that guesses gets disabled.
func TestProbePathNamesARoute(t *testing.T) {
	for _, p := range fleetTargets {
		u, err := url.Parse(p.URL)
		if err != nil {
			t.Errorf("%s: unparseable probe URL %q: %v", p.Name, p.URL, err)
			continue
		}
		if u.Path == "" || u.Path == "/" {
			t.Errorf("%s: probe URL %q names no path — it measures whatever answers the root, "+
				"which on a service with a static passthrough is index.html at 200", p.Name, p.URL)
		}
	}
}

// The zip fleet serves under /v1/. A bare /health on one of those hosts is the
// shape that just cost us a blind probe, so it is named rather than inferred:
// these are the hosts we know answer /v1/, and adding a host here is a decision
// someone makes on purpose.
func TestZipServicesAreProbedUnderV1(t *testing.T) {
	zipHosts := []string{"cloud.hanzo.svc", "visor.hanzo.svc"}

	for _, p := range fleetTargets {
		u, err := url.Parse(p.URL)
		if err != nil {
			continue // the test above owns this failure
		}
		for _, h := range zipHosts {
			if u.Hostname() != strings.Split(h, ":")[0] {
				continue
			}
			if !strings.HasPrefix(u.Path, "/v1/") {
				t.Errorf("%s: probes %q on %s, which serves under /v1/. Outside /v1/ the "+
					"request falls to the static passthrough and answers 200 with HTML, so this "+
					"probe is green whatever the service is doing.", p.Name, u.Path, h)
			}
		}
	}
}
