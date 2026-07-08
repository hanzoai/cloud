//go:build controlplane

// Command nonharness is NOT part of the module's build graph (it lives under
// testdata/, which `go build ./...`/`go list ./...` skip by convention) and is
// never invoked except by containment_test.go's
// TestContainment_NonHarnessProcessRefuses, which runs it as a real `go run`
// subprocess — a genuine non-test binary, so testing.Testing() is false
// inside it. It exists solely to prove clients/controlplane's fail-closed
// guard (mustHarnessOnly, containment.go) actually panics outside a go-test
// harness, not just that its boolean logic says it should.
//
// Importing the package alone is enough: init() in signer.go calls
// mustHarnessOnly before registering the stub PartialZVerifier, so this
// program panics before main() ever runs.
package main

import (
	_ "github.com/hanzoai/cloud/clients/controlplane"
)

func main() {}
