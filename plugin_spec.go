// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
	"fmt"

	"github.com/zap-proto/zip"
)

// PluginSpec returns a MountSpec that serves prefix from a SEPARATE binary
// instead of code linked into this one.
//
// It exists so that where a subsystem runs stops being a property of the source.
// zip.Load returns a zip.Service — the same type a linked-in service is — so the
// only difference between "compiled in" and "its own process" is which MountSpec
// Wire() lists. Moving one out is a one-line edit at the composition root, and
// nothing downstream (routing, health, shutdown ordering) can tell the difference.
//
// The plugin names exactly one of Addr (already listening), Bin (the binary,
// normally go:embed'd) or Path. For Bin and Path, zip starts it as a child on a
// private unix socket and mounts the routes onto it; the child is stopped when
// Shutdown runs, so a plugin subsystem tears down with the rest.
//
// Global is set because zip.Load registers under the prefix it was given. Handing
// it a scoped Router would nest that prefix under the subsystem name and the
// routes would answer somewhere nobody is asking.
func PluginSpec(name, prefix string, p zip.Plugin) MountSpec {
	if p.Name == "" {
		p.Name = name
	}
	return MountSpec{
		Name:   name,
		Global: true,
		Mount: func(router Router, _ Deps) error {
			app, ok := router.(*zip.App)
			if !ok {
				return fmt.Errorf("pluginspec %q: needs the root app, got %T — Global must stay set", name, router)
			}
			return zip.Load(prefix, p)(app)
		},
	}
}
