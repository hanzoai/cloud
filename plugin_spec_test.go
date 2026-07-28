// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
	"testing"

	"github.com/zap-proto/zip"
)

// A plugin subsystem must look like every other one at the composition root:
// same MountSpec type, so Wire() can swap in-process for out-of-process by
// editing one line.
//
// It fills App, not Mount: zip.Load registers the prefix itself, so a scoped
// Router would nest it and the routes would answer under a doubled prefix. That
// used to be a runtime check with an error message; App takes *zip.App, so it
// is now unrepresentable and there is nothing left to test.
func TestPluginSpec_IsAnOrdinaryMountSpec(t *testing.T) {
	s := PluginSpec("search", zip.Plugin{Addr: "127.0.0.1:1"}, "/v1/search")
	if s.Name != "search" {
		t.Fatalf("name = %q, want search", s.Name)
	}
	if s.App == nil {
		t.Fatal("App is nil — the spec would silently mount nothing")
	}
	if s.Mount != nil {
		t.Fatal("Mount must stay nil: a subsystem is scoped or global, not both")
	}
}
