// Package cli is the Hanzo cloud-control CLI — the client half of the `hanzo`
// binary: one command tree over the Hanzo Cloud.
//
// Every verb registered in newRootCmd MANAGES the Hanzo Cloud from here; every
// other verb cmd/hanzo hands to the Rust fabric CLI (IsControlVerb draws that
// line off this tree). What runs here:
//
//	hanzo login | auth        identity against hanzo.id (IAM)
//	hanzo apps  list|get      the platform apps board (declared/running/drift)
//	hanzo deploy              drive a platform redeploy (rolling, zero-downtime)
//	hanzo clusters …          provision/list/select dedicated DOKS clusters
//	hanzo build               enqueue a platform-native build (runner fabric)
//	hanzo config …            ~/.hanzo/config preferences
//
// It is a THIN client over surfaces that already exist — Hanzo IAM
// (hanzo.id /v1/iam/oauth/*), the platform REST control plane
// (platform.hanzo.ai /v1/*), and the cloud /v1 API. It invents no parallel
// API and holds no business logic; every command is one HTTP call shaped by
// resolved configuration. Secrets live only in ~/.hanzo (0600) or the
// environment — never in source, never logged.
package cli
