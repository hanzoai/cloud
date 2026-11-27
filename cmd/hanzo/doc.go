// Command hanzo is the Hanzo control CLI: `hanzo login`, `hanzo apps`,
// `hanzo deploy`, `hanzo gpu connect`, and every other verb the cli package
// serves. Anything it does not serve is handed to the Rust fabric CLI, so the
// single `hanzo` name stays a superset of both.
//
// CLIENT ONLY. This binary used to be both halves — a control CLI and, via
// apps.Wire(), a server that could mount any subsystem or the whole fused
// surface. That is what made it a 3102-package link, and it is why the file was
// deleted with apps.Wire and cmd/cloud when the mega build was killed
// (22f4fc64e). Server mode did not need saving: the host in cmd/cloud starts
// each app as its own plugin process, which is a better answer to the same
// question.
//
// The CLIENT half did need saving, and nothing was left to build it. `hanzo gpu
// connect` is the fleet worker — the process that claims render jobs on every
// BYO GPU — so with no main importing cli, the worker running the fleet could
// not be rebuilt from source in this repo or any other. A fix to it (the
// readiness gate, cli/gpu.go) had nowhere to ship.
//
// So this main links `cli` and nothing else. It cannot mount a subsystem and
// cannot reach apps.Wire; the fleet stays unlinked and the mega build stays
// dead. Serving is cmd/cloud's job, and asking is this one's.
package main
