// Package secret reads a service's OWN secret material at boot, into memory.
//
// A service needs a handful of values it cannot obtain for itself — a signing
// key, a database credential, the client secret of its own machine identity.
// The two usual homes for those are both wrong here. A Kubernetes Secret is a
// base64 field in the API server that every `kubectl get -o yaml` in the
// namespace can read, mounted into a filesystem where anything in the pod can
// read it again. An environment variable is worse: it is inherited by every
// child process, printed by every crash dumper, and shipped whole by anything
// that reports the process environment.
//
// So the material arrives over the network instead, at boot, held only in the
// process's own memory, and the caller proves who it is with the token the
// platform already vouches for: the projected ServiceAccount token at
// HANZO_SA_TOKEN. IAM exchanges that assertion for a bearer (RFC 7523,
// grant_type urn:ietf:params:oauth:grant-type:jwt-bearer), and KMS answers the
// reads that bearer's owner is scoped to. Nothing new is issued to a service to
// let it do this — the identity is the one it already has.
//
// The scope is a service's own material, and only that. Credentials for an
// upstream a request travels to are egress's business, resolved per call
// against the caller's org; this package is the boot fact — what THIS process
// needs to open its own doors, read once, before it serves anything.
//
// Boot is the whole API for a Go service. `cloud secret fetch` is the same read
// for a workload that is not Go: an init container writes the values into a
// memory-backed emptyDir the app then reads (fetch.go).
package secret
