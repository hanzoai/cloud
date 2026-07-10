// Package runner is the host-role JIT runner daemon: a bare-metal
// workstation agent that polls GitHub for queued workflow jobs whose
// labels are a subset of this host's labels, mints just-in-time runner
// configs via the GitHub Actions API, and spawns actions-runner with
// --jitconfig. Each spawned runner picks one job, exits, and
// auto-deregisters server-side.
//
// This is the host role only — the standalone daemon that runs on a
// developer box or bare-metal fleet node. The in-cluster controller is
// a separate concern and is not part of this package.
package runner

// Build metadata, stamped at link time by the cloud CLI via -ldflags
// (e.g. -X github.com/hanzoai/cloud/runner.Version=1.2.3). Defaults keep
// `go test` and `go run` builds honest about being untagged dev builds.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)
