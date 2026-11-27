package runner

// Build metadata, stamped at link time by the cloud CLI via -ldflags
// (e.g. -X github.com/hanzoai/cloud/runner.Version=1.2.3). Defaults keep
// `go test` and `go run` builds honest about being untagged dev builds.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)
