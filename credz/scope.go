package credz

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hanzoai/cloud/manifest"
)

// Scope is the answer to "which secrets may this app read", and it is derived,
// not configured. There is no second registry: the manifest already names every
// app in the fleet, and the KMS store already holds every secret. An app's scope
// is the path its own name spells.
//
//	/orgs/{adminOrg}/svc/_shared/{NAME}   every app
//	/orgs/{adminOrg}/svc/{app}/{NAME}     that app only
//
// {NAME} is the environment variable the app already reads — CLOUD_AI_API_KEY,
// driverName, DO_API_TOKEN. So provisioning is "put the value where the name
// says", the broker needs to know nothing about what any app wants, and the set
// of credentials is data in the store rather than a table in code that drifts
// from it. The path is built from the peer's identity rather than from the
// request — but see the identity caveat in credz.go: that identity is derived
// from argv, which the peer controls, so this partitions credentials against
// accident and not against a peer that runs code of its own.
//
// The paths sit under the admin org rather than the reserved platform partition
// so they are reachable through the KMS surface that already exists
// (GET /v1/kms/orgs/{adminOrg}/secrets/svc/{app}/{NAME}), gated by the org check
// that already guards it. One store, one authorization model, one way to
// provision.
func Scope(adminOrg, app string) []string {
	base := "/orgs/" + adminOrg + "/svc/"
	// Shared first: a name filed under the app itself overrides the fleet-wide
	// default, which is what makes "everyone gets this issuer, except ai" express.
	return []string{base + shared, base + app}
}

// shared names the fleet-wide scope. It carries '_', which an app name never
// does (manifest names are DNS labels), so it can never collide with one.
const shared = "_shared"

// appNames is the manifest's set of app names, which is the only thing a peer is
// allowed to be. Built once; the manifest is a generated constant.
var appNames = func() map[string]bool {
	m := make(map[string]bool, len(manifest.Apps))
	for _, a := range manifest.Apps {
		m[a.Name] = true
	}
	return m
}()

// appOf resolves the app a peer process is running, from the argv the kernel
// recorded for it. Both spawn shapes the manifest can produce are covered, and
// nothing else is accepted:
//
//	<dir>/<name>              a dedicated per-app binary
//	<dir>/cloud --enable=<n>  the multi-call binary serving one app
//
// The result is checked against the manifest, so the value that becomes a store
// path is always one of a closed set of known names — a peer cannot spell a path
// even if it could spell an argv.
func appOf(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("credz: peer has no argv")
	}
	name := filepath.Base(argv[0])
	if name != manifest.MultiCall {
		if !appNames[name] {
			return "", fmt.Errorf("credz: peer %q is not a manifest app", name)
		}
		return name, nil
	}
	// The multi-call binary is whichever app it was told to enable. Exactly one:
	// a child serving two apps would need two scopes, and merging them is how one
	// plugin quietly acquires another's credentials.
	var enabled []string
	for _, a := range argv[1:] {
		v, ok := enableFlag(a)
		if !ok {
			continue
		}
		for _, n := range strings.Split(v, ",") {
			if n = strings.TrimSpace(n); n != "" {
				enabled = append(enabled, n)
			}
		}
	}
	if len(enabled) != 1 {
		return "", fmt.Errorf("credz: multi-call peer enables %v, need exactly one app", enabled)
	}
	if !appNames[enabled[0]] {
		return "", fmt.Errorf("credz: peer enables %q, which the manifest does not list", enabled[0])
	}
	return enabled[0], nil
}

// enableFlag matches the --enable=<v> the manifest emits, in both Go flag
// spellings. A bare `--enable <v>` is not produced by manifest.App.Plugin and is
// not accepted: guessing at argv shapes nobody generates is how a scope check
// starts matching things it should not.
func enableFlag(arg string) (string, bool) {
	for _, p := range [...]string{"--enable=", "-enable="} {
		if v, ok := strings.CutPrefix(arg, p); ok {
			return v, true
		}
	}
	return "", false
}
