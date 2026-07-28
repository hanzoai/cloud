package credz

import "github.com/hanzoai/cloud/manifest"

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
// from it. The path is built from the app the LAUNCHER stamped on the peer, never
// from anything in the request — so a peer cannot spell a path, only present a
// token for the one it was started as.
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

// appNames is the manifest's set of app names, which is the only thing a
// launcher may stamp. Built once; the manifest is a generated constant.
//
// This is the broker's second gate, not its first: launch.Open has already
// proven the launcher issued the name. It stays because the two are independent
// facts — "my launcher signed this" and "the fleet has an app by this name" —
// and a name that passes one but not the other means launcher and manifest have
// drifted, which should be a refusal rather than a store path nobody provisioned.
var appNames = func() map[string]bool {
	m := make(map[string]bool, len(manifest.Apps))
	for _, a := range manifest.Apps {
		m[a.Name] = true
	}
	return m
}()
