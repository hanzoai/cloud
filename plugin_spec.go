// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
	"fmt"

	"github.com/zap-proto/zip"
)

// PluginSpec returns a MountSpec that serves prefixes from a SEPARATE binary
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
// prefixes is variadic because ONE service commonly owns several route subtrees —
// o11y answers both /v1/o11y and /v1/sentry — and a spec that could name only the
// first silently 404s the rest at the host: the request never reaches the child,
// while the host starts, reports healthy, and looks entirely fine. The plugin is
// the unit of deployment; the subtrees it owns are a property of it, not a reason
// to declare it twice. Nothing is defaulted or validated here — zip.Load already
// rejects an empty list by name, and restating that would put the same rule in two
// places for the usual price.
//
// Global is set because zip.Load registers under the prefixes it was given.
// Handing it a scoped Router would nest them under the subsystem name and the
// routes would answer somewhere nobody is asking.
// price is a positional argument for the same reason Global is set below: a plugin
// serves its prefixes from another process, and NOTHING downstream of this spec can
// see what happens in there — so what the surface costs has to be stated by whoever
// decides to mount it, exactly as it is for a linked-in subsystem. Variadic prefixes
// force it ahead of them; that is the only reason it sits where it does.
func PluginSpec(name string, price Price, p zip.Plugin, prefixes ...string) MountSpec {
	if p.Name == "" {
		p.Name = name
	}
	return MountSpec{
		Name:  name,
		Price: price,
		// Stated once, and it reaches both places that care. zip routes on it, and
		// Declare reads it for the boot inventory (/v1/admin/subsystems) and
		// the per-request subsystem attribution tracing hangs off. Leaving it empty
		// falls back to the /v1/<name> convention, which for a plugin owning a second
		// subtree means that subtree's traffic is attributed to NOBODY and the admin
		// board understates what goes dark when the plugin does.
		//
		// It does not narrow middleware here the way it would for a scoped subsystem:
		// MountAll only builds a scope when !Global, and this spec is always Global.
		Prefixes: prefixes,
		Global:   true,
		Mount: func(router Router, _ Deps) error {
			app, ok := router.(*zip.App)
			if !ok {
				return fmt.Errorf("pluginspec %q: needs the root app, got %T — Global must stay set", name, router)
			}
			return zip.Load(p, prefixes...)(app)
		},
	}
}
