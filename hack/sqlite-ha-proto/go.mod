module github.com/hanzoai/cloud/hack/sqlite-ha-proto

go 1.26.4

// Standalone proof — kept OUT of cloud's module graph on purpose. Relative
// replaces target the canonical workspace checkouts (~/work/hanzo/{replicate,sqlite}).
// Run: GOWORK=off CGO_ENABLED=0 go run .
require (
	github.com/hanzoai/replicate v0.9.0
	github.com/hanzoai/sqlite v0.2.1
)

replace github.com/hanzoai/replicate => ../../../replicate

replace github.com/hanzoai/sqlite => ../../../sqlite
