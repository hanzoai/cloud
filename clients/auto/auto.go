// Package auto mounts the workflow-automation surface at /v1/auto/* in the unified
// cloud binary (HIP-0106). It is a per-org REVERSE PROXY to the standalone Hanzo Auto
// engine (github.com/hanzoai/auto), which runs as its own in-cluster Deployment on
// its own base/SQLite store + tasks worker. cloud does NOT re-embed that engine — one
// engine, one store — it makes the engine reachable IN-PLATFORM, on api.hanzo.ai,
// scoped to the caller's validated tenant. So a signed-in user sees their workflows,
// the connector catalog, and can run flows through the SAME origin as the rest of the
// platform, without a second login.
//
// THE TENANT BOUNDARY (why a bare proxy is NOT enough here).
//
// The auto engine trusts X-Org-Id UNCONDITIONALLY — its route layer scopes every
// query by that header and does no JWT validation of its own (it was built to sit
// behind a gateway that mints identity, HIP-0026). That makes THIS proxy the trust
// boundary: whatever X-Org-Id we forward IS the tenant the engine serves.
//
// cloud's SanitizeIdentity middleware (middleware_identity.go) has already run before
// this handler: it strips EVERY client-supplied authority header and re-sets X-User-Id
// / X-Org-Id ONLY from a VALIDATED credential. But on the bearer-less "Phase-1 data"
// path it RESTORES the client's raw X-Org-Id while leaving X-User-Id EMPTY — exactly
// the anonymous-forge (an off-gateway caller sending `X-Org-Id: victim` with no
// credential). If we forwarded that, an anonymous attacker would read/drive victim's
// workflows. So this proxy GATES on a validated principal (X-User-Id present — the
// same signal principal.Validated uses) and refuses the bearer-less-forge path with
// 403. Past the gate, X-Org-Id is the SanitizeIdentity-pinned validated owner, so the
// engine only ever serves the caller's own org. The gate + outbound identity
// re-stamping live in the pure sub-package clients/auto/proxy (unit-tested in
// isolation); this file only wires them into cloud.Registry.
package auto

import (
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/auto/proxy"
	"github.com/zap-proto/zip"
)

// defaultUpstream is the in-cluster address of the auto engine's Service
// (auto.hanzo.svc, port 80 -> container 8080). Overridable via AUTO_UPSTREAM.
const defaultUpstream = "http://auto.hanzo.svc.cluster.local:80"

func upstream() string {
	if v := strings.TrimSpace(os.Getenv("AUTO_UPSTREAM")); v != "" {
		return v
	}
	return defaultUpstream
}

// Mount wires the /v1/auto/* reverse proxy onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	h, err := proxy.NewHandler(upstream())
	if err != nil {
		return err
	}
	app.Mount("/v1/auto", proxy.Gate(h))
	if deps.Logger != nil {
		deps.Logger.New("subsystem", "auto").
			Info("auto workflow surface mounted (reverse proxy)", "upstream", upstream())
	}
	return nil
}

func init() {
	// Order 140: after the framework (129) and kb (130) so /v1/kb/* resolves at its
	// own subsystem, and before the AI /v1/* catch-all (150) so /v1/auto/* reaches
	// this proxy rather than the AI fallthrough.
	cloud.Register("auto", 140, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return &mountError{"auto.Mount: app is not *zip.App"}
		}
		return Mount(a, deps)
	})
}

type mountError struct{ msg string }

func (e *mountError) Error() string { return e.msg }
