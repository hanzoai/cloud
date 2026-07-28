// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
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
// App is set because zip.Load registers under the prefixes it was given. Handing
// it a scoped Router would nest them under the subsystem name and the routes would
// answer somewhere nobody is asking.
//
// Lazy is the caller's call, deliberately, and is NOT defaulted on here. It
// defers starting the child until a request actually reaches one of its
// prefixes, which is what makes a 69-service binary cheap to run — but it is
// only correct for a subsystem whose work is request-driven. An app that owns a
// listener or a background loop (o11y's OTLP collector is the standing example)
// must start with the host or it silently ingests nothing, and the failure looks
// like an empty dashboard rather than an error.
func PluginSpec(name string, p zip.Plugin, prefixes ...string) MountSpec {
	if p.Name == "" {
		p.Name = name
	}
	return MountSpec{
		Name: name,
		// Stated once, and it reaches both places that care. zip routes on it, and
		// indexSubsystems reads it for the boot inventory (/v1/admin/subsystems) and
		// the per-request subsystem attribution tracing hangs off. Leaving it empty
		// falls back to the /v1/<name> convention, which for a plugin owning a second
		// subtree means that subtree's traffic is attributed to NOBODY and the admin
		// board understates what goes dark when the plugin does.
		//
		// It does not narrow middleware the way it would for a scoped subsystem:
		// MountAll only builds a scope for a spec that supplies Mount, and this one
		// supplies App.
		Prefixes: prefixes,
		App:      func(app *zip.App, _ Deps) error { return zip.Load(p, prefixes...)(app) },
	}
}
