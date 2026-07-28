// Isolated codegen module: the Goa v3 design for /v1/platform.
//
// This module is DELIBERATELY separate from github.com/hanzoai/cloud so the
// cloud binary's go.mod never takes a goa.design/goa/v3 runtime dependency.
// The cloud runtime mounts /v1/platform on the canonical zip router (one
// router, per HIP-0106); this module exists only to (a) express the API
// design-first in the Goa DSL and (b) `goa gen` the OpenAPI 3 contract that
// console + external clients consume. Nested go.mod ⇒ `go build ./...` from
// the cloud root skips it.
module github.com/hanzoai/platform-design

go 1.25.0

require goa.design/goa/v3 v3.28.0

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dimfeld/httppath v0.0.0-20170720192232-ee938bf73598 // indirect
	github.com/gohugoio/hashstructure v0.6.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/manveru/faker v0.0.0-20171103152722-9fbc68a78c4d // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	golang.org/x/mod v0.36.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	golang.org/x/tools v0.45.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
